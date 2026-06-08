#!/usr/bin/env python3
import argparse
import json
import os
import pathlib
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from urllib.parse import urlparse

from camoufox.sync_api import Camoufox


CATALOG_PATH = pathlib.Path(__file__).with_name("realpass-targets.json")

FINGERPRINT_DETECTOR_EXPRESSION = r"""() => {
const canvas = document.createElement("canvas");
canvas.width = 240;
canvas.height = 60;
const ctx = canvas.getContext("2d");
if (ctx) {
  ctx.textBaseline = "top";
  ctx.font = "16px Arial";
  ctx.fillStyle = "#f60";
  ctx.fillRect(0, 0, 120, 40);
  ctx.fillStyle = "#069";
  ctx.fillText("gomoufox fingerprint audit", 4, 8);
}
const glCanvas = document.createElement("canvas");
const gl = glCanvas.getContext("webgl") || glCanvas.getContext("experimental-webgl");
let webgl = {supported: false};
if (gl) {
  const debug = gl.getExtension("WEBGL_debug_renderer_info");
  webgl = {
    supported: true,
    vendor: gl.getParameter(gl.VENDOR),
    renderer: gl.getParameter(gl.RENDERER),
    unmaskedVendor: debug ? gl.getParameter(debug.UNMASKED_VENDOR_WEBGL) : null,
    unmaskedRenderer: debug ? gl.getParameter(debug.UNMASKED_RENDERER_WEBGL) : null
  };
}
const fontNames = ["Arial", "Times New Roman", "Courier New", "Helvetica", "Segoe UI", "Noto Sans"];
const fonts = {};
if (document.fonts && document.fonts.check) {
  for (const name of fontNames) {
    fonts[name] = document.fonts.check("12px \"" + name + "\"");
  }
}
const dataURL = canvas.toDataURL();
return {
  webdriver: navigator.webdriver,
  userAgent: navigator.userAgent,
  appVersion: navigator.appVersion,
  platform: navigator.platform,
  vendor: navigator.vendor,
  productSub: navigator.productSub,
  languages: navigator.languages,
  hardwareConcurrency: navigator.hardwareConcurrency,
  deviceMemory: navigator.deviceMemory || null,
  maxTouchPoints: navigator.maxTouchPoints,
  cookieEnabled: navigator.cookieEnabled,
  doNotTrack: navigator.doNotTrack || null,
  pdfViewerEnabled: navigator.pdfViewerEnabled,
  plugins: navigator.plugins ? navigator.plugins.length : null,
  outerWidth: window.outerWidth,
  outerHeight: window.outerHeight,
  innerWidth: window.innerWidth,
  innerHeight: window.innerHeight,
  screenWidth: screen.width,
  screenHeight: screen.height,
  screenAvailWidth: screen.availWidth,
  screenAvailHeight: screen.availHeight,
  colorDepth: screen.colorDepth,
  pixelDepth: screen.pixelDepth,
  devicePixelRatio: window.devicePixelRatio,
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  webgl,
  webrtc: {supported: typeof RTCPeerConnection !== "undefined"},
  fonts,
  canvas: {dataURLPrefix: dataURL.slice(0, 96), dataURLLength: dataURL.length}
};
}"""


def main() -> int:
    parser = argparse.ArgumentParser(description="Run the pinned Python Camoufox package over gomoufox realpass targets.")
    parser.add_argument("--out", default="dist/realpass/python-direct-generated")
    parser.add_argument("--target", action="append", default=[], help="target URL or name=url; may be repeated")
    parser.add_argument("--target-tier", choices=("smoke", "full", "extended"), default="full")
    parser.add_argument("--target-file", default=str(CATALOG_PATH), help="target catalog JSON path")
    parser.add_argument("--timeout", type=float, default=60.0)
    parser.add_argument("--wait-until", choices=("commit", "domcontentloaded", "load", "networkidle"), default="commit")
    parser.add_argument("--settle", type=float, default=7.0)
    parser.add_argument("--load-state-timeout", type=float, default=10.0, help="extra load-state wait after settle; 0 disables")
    parser.add_argument("--content-max-bytes", type=int, default=0, help="maximum HTML bytes fetched for classification; 0 fetches full content")
    parser.add_argument("--sample-interval", type=float, default=0.5)
    parser.add_argument("--headful", action="store_true")
    parser.add_argument("--screenshots", action=argparse.BooleanOptionalAction, default=True)
    parser.add_argument("--reuse-browser", action="store_true", help="reuse one browser process across targets; each target still gets a fresh page context")
    parser.add_argument("--list-targets", action="store_true")
    args = parser.parse_args()
    if args.load_state_timeout < 0:
        parser.error("--load-state-timeout must be >= 0")
    if args.content_max_bytes < 0:
        parser.error("--content-max-bytes must be >= 0")
    catalog = load_catalog(pathlib.Path(args.target_file))

    if args.list_targets:
        print(json.dumps(catalog_targets(catalog, args.target_tier), separators=(",", ":")))
        return 0

    targets = [parse_target(raw) for raw in args.target] if args.target else catalog_targets(catalog, args.target_tier)
    out_dir = pathlib.Path(args.out)
    shot_dir = out_dir / "screenshots"
    out_dir.mkdir(parents=True, exist_ok=True)
    if args.screenshots:
        shot_dir.mkdir(parents=True, exist_ok=True)

    report = {
        "started_at": now_iso(),
        "finished_at": None,
        "runtime": "python-camoufox",
        "options": {
            "timeout": f"{args.timeout:g}s",
            "wait_until": args.wait_until,
            "settle": f"{args.settle:g}s",
            "load_state_timeout": f"{args.load_state_timeout:g}s",
            "content_max_bytes": args.content_max_bytes,
            "sample_interval": f"{args.sample_interval:g}s",
            "headful": args.headful,
            "screenshots": args.screenshots,
            "reuse_browser": args.reuse_browser,
            "generated_persona": True,
            "unsafe_direct_network": True,
        },
        "summary": {},
        "results": [],
    }

    if args.reuse_browser:
        with Camoufox(headless=not args.headful) as browser:
            for target in targets:
                print(f"python-realpass: {target['name']} -> {target['url']}", file=sys.stderr, flush=True)
                result = run_target(target, args, shot_dir, browser)
                report["results"].append(result)
                append_result_jsonl(out_dir, result)
    else:
        for target in targets:
            print(f"python-realpass: {target['name']} -> {target['url']}", file=sys.stderr, flush=True)
            result = run_target(target, args, shot_dir, None)
            report["results"].append(result)
            append_result_jsonl(out_dir, result)

    report["finished_at"] = now_iso()
    report["summary"] = summarize(report["results"])
    write_reports(out_dir, report)
    print(f"report: {out_dir / 'report.md'}")
    print(f"json: {out_dir / 'report.json'}")
    return 0


def run_target(target, args, shot_dir, browser=None):
    started = time.time()
    result = {
        "name": target["name"],
        "url": target["url"],
        "kind": target["kind"],
        "outcome": "failed",
        "started_at": now_iso(),
        "duration_ms": 0,
        "resources": {},
    }
    monitor = ProcessMonitor(os.getpid(), args.sample_interval)
    monitor.start()
    page = None
    try:
        if browser is None:
            with Camoufox(headless=not args.headful) as owned_browser:
                run_page_probe(owned_browser, target, args, shot_dir, result)
        else:
            run_page_probe(browser, target, args, shot_dir, result)
    except Exception as exc:
        result["error"] = str(exc)
        result["outcome"] = "failed"
    finally:
        result["duration_ms"] = int((time.time() - started) * 1000)
        result["resources"] = monitor.stop()
    return result


def run_page_probe(browser, target, args, shot_dir, result):
    page = browser.new_page()
    try:
        response = page.goto(target["url"], wait_until=args.wait_until, timeout=args.timeout * 1000)
        if response is not None:
            result["status"] = response.status
            result["status_text"] = getattr(response, "status_text", "")
            result["headers"] = selected_headers(response.headers)
        wait_for(args.settle)
        if args.load_state_timeout > 0:
            try:
                page.wait_for_load_state("load", timeout=args.load_state_timeout * 1000)
            except Exception:
                pass
        result["final_url"] = page.url
        try:
            capture = page_capture_for_classification(page, args.content_max_bytes)
            result["title"] = capture["title"]
            content = capture["content"]
            result["content_bytes"] = capture["content_bytes"]
            result["detector"] = capture["detector"]
        except Exception:
            result["title"] = page.title()
            content, content_bytes = page_content_for_classification(page, args.content_max_bytes)
            result["content_bytes"] = content_bytes
            result["detector"] = detector_snapshot(page)
        result["signals"] = classify_signals(result.get("status", 0), result.get("title", ""), result.get("final_url", ""), content)
        if args.screenshots:
            shot_path = shot_dir / f"01-{slug(target['name'])}.png"
            page.screenshot(path=str(shot_path))
            result["screenshot_path"] = str(shot_path)
            result["screenshot_bytes"] = shot_path.stat().st_size
        result["outcome"] = "blocked" if has_blocking_signals(result["signals"]) else "passed"
    finally:
        try:
            page.close()
        except Exception:
            pass


def page_capture_for_classification(page, max_bytes):
    value = page.evaluate(
        """max => {
const html = document.documentElement ? document.documentElement.outerHTML : "";
const limit = max > 0 ? max : html.length;
return {
  title: document.title || "",
  content: html.slice(0, limit),
  content_bytes: html.length,
  detector: ("""
        + FINGERPRINT_DETECTOR_EXPRESSION
        + """)()
};
}""",
        max_bytes,
    )
    if not value or not value.get("content") and not value.get("content_bytes") and not value.get("detector"):
        raise RuntimeError("page capture returned empty payload")
    return value


def page_content_for_classification(page, max_bytes):
    if max_bytes <= 0:
        content = page.content()
        return content, len(content)
    try:
        value = page.evaluate(
            """max => {
const html = document.documentElement ? document.documentElement.outerHTML : "";
return {content: html.slice(0, max), bytes: html.length};
}""",
            max_bytes,
        )
        return value.get("content", ""), int(value.get("bytes") or 0)
    except Exception:
        content = page.content()
        return content[:max_bytes], len(content)


def detector_snapshot(page):
    try:
        return page.evaluate(FINGERPRINT_DETECTOR_EXPRESSION)
    except Exception as exc:
        return {"error": str(exc)}


def classify_signals(status, title, final_url, content):
    strong_text = (title + "\n" + final_url + "\n" + content[:250000]).lower()
    generic_text = (title + "\n" + final_url + "\n" + content[:4096]).lower()
    signals = []

    def add(signal):
        if signal not in signals:
            signals.append(signal)

    if status in (403, 429, 503):
        add(f"http_{status}")
    checks = [
        ("just a moment", "cloudflare_challenge"),
        ("checking your browser", "browser_challenge"),
        ("cf-chl", "cloudflare_challenge"),
        ("cdn-cgi/challenge-platform", "cloudflare_challenge"),
        ("turnstile", "cloudflare_turnstile"),
        ("verify you are human", "human_verification"),
        ("confirm you are human", "human_verification"),
        ("prove you are human", "human_verification"),
        ("captcha", "captcha"),
        ("g-recaptcha", "captcha"),
        ("h-captcha", "captcha"),
        ("robot", "robot_detection"),
        ("bot detection", "bot_detection"),
        ("access denied", "access_denied"),
        ("request blocked", "request_blocked"),
        ("datadome", "datadome"),
        ("akamai", "akamai"),
        ("perimeterx", "perimeterx"),
        ("px-captcha", "perimeterx"),
        ("incapsula", "imperva_incapsula"),
        ("distil", "distil"),
        ("unusual traffic", "unusual_traffic"),
    ]
    for needle, signal in checks:
        if needle in strong_text:
            add(signal)
    for needle in ("403 forbidden", "access forbidden", "forbidden access"):
        if needle in generic_text:
            add("forbidden")
            break
    return signals


def has_blocking_signals(signals):
    blocking = {
        "cloudflare_challenge",
        "browser_challenge",
        "human_verification",
        "access_denied",
        "request_blocked",
        "forbidden",
        "unusual_traffic",
        "http_403",
        "http_429",
        "http_503",
    }
    return any(signal in blocking for signal in signals)


def selected_headers(headers):
    wanted = {"server", "cf-ray", "cf-cache-status", "x-datadome", "x-akamai", "x-cdn", "content-type"}
    out = {key: value for key, value in (headers or {}).items() if key.lower() in wanted}
    return out or None


class ProcessMonitor:
    def __init__(self, root_pid, interval):
        self.root_pid = root_pid
        self.interval = interval
        self.stop_event = threading.Event()
        self.thread = threading.Thread(target=self.run, daemon=True)
        self.samples = 0
        self.cpu_sum = 0.0
        self.summary = {
            "scope": "python_camoufox_harness_process_tree",
            "root_pid": root_pid,
            "samples": 0,
            "peak_rss_kib": 0,
            "peak_rss_mib": 0.0,
            "max_cpu_percent": 0.0,
            "avg_cpu_percent": 0.0,
            "peak_processes": 0,
            "process_commands": [],
            "sample_errors": [],
        }

    def start(self):
        self.thread.start()

    def stop(self):
        self.stop_event.set()
        self.thread.join(timeout=max(1.0, self.interval * 4))
        return self.summary

    def run(self):
        while not self.stop_event.is_set():
            try:
                sample = sample_process_tree(self.root_pid)
                self.samples += 1
                self.cpu_sum += sample["cpu"]
                self.summary["samples"] = self.samples
                if sample["rss_kib"] > self.summary["peak_rss_kib"]:
                    self.summary["peak_rss_kib"] = sample["rss_kib"]
                    self.summary["peak_rss_mib"] = sample["rss_kib"] / 1024
                self.summary["max_cpu_percent"] = max(self.summary["max_cpu_percent"], sample["cpu"])
                self.summary["avg_cpu_percent"] = self.cpu_sum / self.samples
                if sample["count"] > self.summary["peak_processes"]:
                    self.summary["peak_processes"] = sample["count"]
                    self.summary["process_commands"] = sample["commands"]
            except Exception as exc:
                if len(self.summary["sample_errors"]) < 5:
                    self.summary["sample_errors"].append(str(exc))
            self.stop_event.wait(self.interval)


def sample_process_tree(root_pid):
    out = subprocess.check_output(["ps", "-axo", "pid=,ppid=,pcpu=,rss=,comm="], text=True)
    rows = []
    for line in out.splitlines():
        parts = line.split(None, 4)
        if len(parts) < 5:
            continue
        try:
            rows.append({
                "pid": int(parts[0]),
                "ppid": int(parts[1]),
                "cpu": float(parts[2]),
                "rss": int(parts[3]),
                "cmd": parts[4],
            })
        except ValueError:
            pass
    by_pid = {row["pid"]: row for row in rows}
    if root_pid not in by_pid:
        raise RuntimeError(f"root pid {root_pid} not found")
    children = {}
    for row in rows:
        children.setdefault(row["ppid"], []).append(row["pid"])
    stack = [root_pid]
    seen = set()
    rss = 0
    cpu = 0.0
    commands = set()
    while stack:
        pid = stack.pop()
        if pid in seen:
            continue
        seen.add(pid)
        row = by_pid[pid]
        rss += row["rss"]
        cpu += row["cpu"]
        if row["cmd"]:
            commands.add(row["cmd"])
        stack.extend(children.get(pid, []))
    return {"rss_kib": rss, "cpu": cpu, "count": len(seen), "commands": sorted(commands)}


def summarize(results):
    summary = {"total": len(results), "passed": 0, "blocked": 0, "failed": 0, "peak_rss_mib": 0.0, "peak_cpu_percent": 0.0}
    for result in results:
        outcome = result.get("outcome")
        if outcome == "passed":
            summary["passed"] += 1
        elif outcome == "blocked":
            summary["blocked"] += 1
        else:
            summary["failed"] += 1
        resources = result.get("resources") or {}
        summary["peak_rss_mib"] = max(summary["peak_rss_mib"], resources.get("peak_rss_mib", 0.0))
        summary["peak_cpu_percent"] = max(summary["peak_cpu_percent"], resources.get("max_cpu_percent", 0.0))
    return summary


def write_reports(out_dir, report):
    report = dict(report)
    report["summary"] = summarize(report["results"])
    (out_dir / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    (out_dir / "report.md").write_text(markdown_report(report))


def append_result_jsonl(out_dir, result):
    with (out_dir / "results.jsonl").open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(result, separators=(",", ":")) + "\n")


def markdown_report(report):
    summary = report["summary"]
    lines = [
        "# Python Camoufox Real-Site Pass",
        "",
        f"- Started: {report['started_at']}",
        f"- Finished: {report.get('finished_at') or now_iso()}",
        f"- Summary: {summary['passed']} passed, {summary['blocked']} blocked, {summary['failed']} failed, peak RSS {summary['peak_rss_mib']:.1f} MiB, peak CPU {summary['peak_cpu_percent']:.1f}%",
        "",
        "Signals are observed markers; `blocked` requires strong challenge, denial, or HTTP error evidence.",
        "",
        "| Target | Kind | Outcome | Status | Signals | Peak RSS MiB | Max CPU % | Duration ms |",
        "|---|---|---:|---:|---|---:|---:|---:|",
    ]
    for result in report["results"]:
        resources = result.get("resources") or {}
        lines.append(
            f"| {escape_md(result['name'])} | {escape_md(result['kind'])} | {result.get('outcome', '')} | {result.get('status', 0)} | "
            f"{escape_md(','.join(result.get('signals') or []))} | {resources.get('peak_rss_mib', 0.0):.1f} | "
            f"{resources.get('max_cpu_percent', 0.0):.1f} | {result.get('duration_ms', 0)} |"
        )
    lines.append("")
    return "\n".join(lines) + "\n"


def parse_target(raw):
    if "=" in raw:
        name_part, value = raw.split("=", 1)
        name_part = name_part.strip()
        value = value.strip()
    else:
        value = raw.strip()
        name_part = slug(urlparse(value).netloc)
    if "|" in name_part:
        name, kind = name_part.split("|", 1)
        kind = slug(kind) or "custom"
    else:
        name, kind = name_part, "custom"
    parsed = urlparse(value)
    if not parsed.scheme or not parsed.netloc:
        raise ValueError(f"invalid target {raw!r}")
    return {"name": slug(name), "url": value, "kind": kind}


def load_catalog(path):
    catalog = json.loads(path.read_text(encoding="utf-8"))
    targets = catalog.get("targets") or []
    names = {target.get("name") for target in targets}
    for tier, tier_names in (catalog.get("tiers") or {}).items():
        missing = [name for name in tier_names if name not in names]
        if missing:
            raise SystemExit(f"target tier {tier} references missing targets: {', '.join(missing)}")
    return catalog


def catalog_targets(catalog, tier):
    by_name = {target["name"]: target for target in catalog["targets"]}
    tier_names = (catalog.get("tiers") or {}).get(tier)
    if not tier_names:
        raise SystemExit(f"target tier not found: {tier}")
    return [by_name[name] for name in tier_names]


def slug(value):
    out = []
    last_dash = False
    for char in value.lower():
        if char.isascii() and char.isalnum():
            out.append(char)
            last_dash = False
        elif out and not last_dash:
            out.append("-")
            last_dash = True
    return "".join(out).strip("-")


def escape_md(value):
    return str(value).replace("\n", " ").replace("|", "\\|")


def now_iso():
    return datetime.now(timezone.utc).astimezone().isoformat()


def wait_for(seconds):
    if seconds > 0:
        time.sleep(seconds)


if __name__ == "__main__":
    raise SystemExit(main())
