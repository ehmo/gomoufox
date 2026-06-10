#!/usr/bin/env python3
import argparse
import http.server
import json
import os
import pathlib
import shlex
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone


TARGET_NAME = "fingerprint-local"
TARGET_KIND = "local-fingerprint"
TARGET_PATH = "/fingerprint.html"
DRY_RUN_TARGET_URL = "http://127.0.0.1:<port>/fingerprint.html"

COMPARE_FIELDS = [
    "webdriver",
    "userAgent",
    "appVersion",
    "platform",
    "vendor",
    "productSub",
    "languages",
    "hardwareConcurrency",
    "deviceMemory",
    "maxTouchPoints",
    "cookieEnabled",
    "doNotTrack",
    "pdfViewerEnabled",
    "plugins",
    "screenWidth",
    "screenHeight",
    "screenAvailWidth",
    "screenAvailHeight",
    "colorDepth",
    "pixelDepth",
    "devicePixelRatio",
    "timezone",
    "webgl.supported",
    "webgl.vendor",
    "webgl.renderer",
    "webgl.unmaskedVendor",
    "webgl.unmaskedRenderer",
    "webrtc.supported",
    "fonts",
    "canvas.dataURLLength",
]

HTML = """<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>gomoufox fingerprint audit</title>
  <meta name="viewport" content="width=device-width, initial-scale=1">
</head>
<body>
  <main>
    <h1>gomoufox fingerprint audit</h1>
    <p>This local page gives gomoufox and Python Camoufox the same deterministic document.</p>
    <canvas id="probe" width="240" height="60"></canvas>
    <script>
      const ctx = document.getElementById("probe").getContext("2d");
      if (ctx) {
        ctx.textBaseline = "top";
        ctx.font = "16px Arial";
        ctx.fillText("local fingerprint probe", 4, 8);
      }
    </script>
  </main>
</body>
</html>
"""


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Compare JS-visible fingerprint fields across Python Camoufox, gomoufox Python sidecar, and gomoufox node-direct."
    )
    parser.add_argument("--out", default="dist/fingerprint-audit/latest")
    parser.add_argument("--gomoufox-realpass", default="", help="prebuilt gomoufox-realpass binary; default builds one under --out")
    parser.add_argument("--python", default="", help="Python executable with Camoufox installed for the explicit comparison baseline")
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--wait-until", choices=("commit", "domcontentloaded", "load", "networkidle"), default="load")
    parser.add_argument("--settle", type=float, default=0.5)
    parser.add_argument("--sample-interval", type=float, default=0.2)
    parser.add_argument("--content-max-bytes", type=int, default=20000)
    parser.add_argument("--allow-drift-field", action="append", default=[], help="field path allowed to drift between gomoufox runtimes")
    parser.add_argument("--python-report", help="existing Python Camoufox report.json")
    parser.add_argument("--gomoufox-python-report", help="existing gomoufox Python-sidecar report.json")
    parser.add_argument("--gomoufox-node-report", help="existing gomoufox node-direct report.json")
    parser.add_argument("--dry-run", action="store_true", help="print commands without running browsers")
    args = parser.parse_args()
    if args.timeout <= 0:
        parser.error("--timeout must be > 0")
    if args.settle < 0:
        parser.error("--settle must be >= 0")
    if args.sample_interval <= 0:
        parser.error("--sample-interval must be > 0")
    if args.content_max_bytes < 0:
        parser.error("--content-max-bytes must be >= 0")
    args.python = selected_python(args.python)

    report_args = [args.python_report, args.gomoufox_python_report, args.gomoufox_node_report]
    if any(report_args):
        if not all(report_args):
            parser.error("--python-report, --gomoufox-python-report, and --gomoufox-node-report must be used together")
        return write_audit_from_reports(
            args,
            target_url=target_url_from_reports(report_args) or DRY_RUN_TARGET_URL,
            reports={
                "python_camoufox": pathlib.Path(args.python_report),
                "gomoufox_python": pathlib.Path(args.gomoufox_python_report),
                "gomoufox_node_direct": pathlib.Path(args.gomoufox_node_report),
            },
        )

    out_dir = pathlib.Path(args.out)
    go_bin, build_cmd = go_runner(args, out_dir)
    if args.dry_run:
        commands = []
        if build_cmd:
            commands.append(build_cmd)
        commands.extend(run_commands(args, go_bin, DRY_RUN_TARGET_URL))
        for command in commands:
            print(shlex.join(command))
        return 0

    out_dir.mkdir(parents=True, exist_ok=True)
    if build_cmd:
        run_command(build_cmd)
    server = LocalServer()
    try:
        server.start()
        target_url = server.url
        reports = {}
        for label, command, report_path in runtime_commands(args, go_bin, target_url):
            run_command(command)
            reports[label] = report_path
        return write_audit_from_reports(args, target_url=target_url, reports=reports)
    finally:
        server.stop()


def go_runner(args, out_dir):
    if args.gomoufox_realpass:
        return pathlib.Path(args.gomoufox_realpass), None
    path = out_dir / "bin" / "gomoufox-realpass-fingerprint"
    return path, ["go", "build", "-trimpath", "-buildvcs=false", "-o", str(path), "./cmd/gomoufox-realpass"]


def selected_python(override):
    if override:
        return override
    for candidate in (
        pathlib.Path.home() / "Library/Caches/gomoufox/venv/bin/python",
        pathlib.Path.home() / ".cache/gomoufox/venv/bin/python",
    ):
        if candidate.exists() and os.access(candidate, os.X_OK):
            return str(candidate)
    return os.environ.get("PYTHON", sys.executable)


def runtime_commands(args, go_bin, target_url):
    target = f"{TARGET_NAME}|{TARGET_KIND}={target_url}"
    python_out = pathlib.Path(args.out) / "python-camoufox"
    go_python_out = pathlib.Path(args.out) / "gomoufox-python"
    go_node_out = pathlib.Path(args.out) / "gomoufox-node-direct"
    commands = [
        (
            "python_camoufox",
            [
                args.python,
                "scripts/python-realpass.py",
                "--out",
                str(python_out),
                "--target",
                target,
                "--timeout",
                f"{args.timeout:g}",
                "--wait-until",
                args.wait_until,
                "--settle",
                f"{args.settle:g}",
                "--load-state-timeout",
                "0",
                "--content-max-bytes",
                str(args.content_max_bytes),
                "--sample-interval",
                f"{args.sample_interval:g}",
                "--no-screenshots",
            ],
            python_out / "report.json",
        ),
        (
            "gomoufox_python",
            go_realpass_command(args, go_bin, go_python_out, target, "python"),
            go_python_out / "report.json",
        ),
        (
            "gomoufox_node_direct",
            go_realpass_command(args, go_bin, go_node_out, target, "node-direct"),
            go_node_out / "report.json",
        ),
    ]
    return commands


def run_commands(args, go_bin, target_url):
    return [command for _, command, _ in runtime_commands(args, go_bin, target_url)]


def go_realpass_command(args, go_bin, out_dir, target, runtime):
    return [
        str(go_bin),
        "--out",
        str(out_dir),
        "--target",
        target,
        "--timeout",
        f"{args.timeout:g}s",
        "--wait-until",
        args.wait_until,
        "--settle",
        f"{args.settle:g}s",
        "--load-state-timeout",
        "0s",
        "--content-max-bytes",
        str(args.content_max_bytes),
        "--sample-interval",
        f"{args.sample_interval:g}s",
        "--report-style",
        "compact",
        "--sidecar-runtime",
        runtime,
        "--screenshots=false",
        "--unsafe-direct-network",
        "--max-failed=-1",
        "--max-blocked=-1",
    ]


def run_command(command):
    print("+ " + shlex.join(command), file=sys.stderr, flush=True)
    subprocess.run(command, check=True)


def write_audit_from_reports(args, target_url, reports):
    out_dir = pathlib.Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    loaded = {name: summarize_report(path) for name, path in reports.items()}
    allowed_fields = set(args.allow_drift_field)
    drifts = compare_go_runtime_detectors(
        loaded["gomoufox_python"]["detector"],
        loaded["gomoufox_node_direct"]["detector"],
        allowed_fields,
    )
    audit = {
        "generated_at": now_iso(),
        "target": {"name": TARGET_NAME, "kind": TARGET_KIND, "url": target_url},
        "compared_fields": COMPARE_FIELDS,
        "allowed_drift_fields": sorted(allowed_fields),
        "runtimes": loaded,
        "go_only_drift": drifts,
        "unallowed_go_only_drift": [drift for drift in drifts if not drift["allowed"]],
    }
    json_path = out_dir / "fingerprint-audit.json"
    md_path = out_dir / "fingerprint-audit.md"
    json_path.write_text(json.dumps(audit, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    md_path.write_text(markdown_audit(audit), encoding="utf-8")
    print(f"audit: {md_path}")
    print(f"json: {json_path}")
    failures = audit["unallowed_go_only_drift"]
    if failures:
        fields = ", ".join(drift["field"] for drift in failures)
        print(f"Go-only fingerprint drift: {fields}", file=sys.stderr)
        return 1
    return 0


def summarize_report(path):
    path = pathlib.Path(path)
    report = json.loads(path.read_text(encoding="utf-8"))
    results = report.get("results") or []
    if not results:
        raise SystemExit(f"report has no results: {path}")
    result = results[0]
    resources = result.get("resources") or {}
    summary = report.get("summary") or {}
    return {
        "report": str(path),
        "outcome": result.get("outcome", ""),
        "status": int(result.get("status") or 0),
        "duration_ms": int(result.get("duration_ms") or 0),
        "peak_rss_mib": float(resources.get("peak_rss_mib") or summary.get("peak_rss_mib") or 0.0),
        "peak_cpu_percent": float(resources.get("max_cpu_percent") or summary.get("peak_cpu_percent") or 0.0),
        "detector": result.get("detector") or {},
    }


def compare_go_runtime_detectors(go_python, go_node, allowed_fields):
    drifts = []
    for field in COMPARE_FIELDS:
        left = nested_value(go_python, field)
        right = nested_value(go_node, field)
        if canonical(left) != canonical(right):
            drifts.append(
                {
                    "field": field,
                    "gomoufox_python": left,
                    "gomoufox_node_direct": right,
                    "allowed": field in allowed_fields,
                }
            )
    return drifts


def nested_value(data, path):
    current = data
    for part in path.split("."):
        if not isinstance(current, dict) or part not in current:
            return None
        current = current[part]
    return current


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def markdown_audit(audit):
    lines = [
        "# Fingerprint Audit",
        "",
        f"- Generated: {audit['generated_at']}",
        f"- Target: `{audit['target']['name']}`",
        f"- Compared fields: {len(audit['compared_fields'])}",
        "",
        "| Runtime | Outcome | Status | Duration ms | Peak RSS MiB | Peak CPU % |",
        "|---|---:|---:|---:|---:|---:|",
    ]
    for label, runtime in audit["runtimes"].items():
        lines.append(
            "| {label} | {outcome} | {status} | {duration} | {rss:.1f} | {cpu:.1f} |".format(
                label=label.replace("_", " "),
                outcome=runtime["outcome"],
                status=runtime["status"],
                duration=runtime["duration_ms"],
                rss=runtime["peak_rss_mib"],
                cpu=runtime["peak_cpu_percent"],
            )
        )
    lines.extend(["", "## Go-Only Drift", ""])
    if not audit["go_only_drift"]:
        lines.append("None.")
    else:
        lines.extend(["| Field | Allowed | gomoufox Python | gomoufox node-direct |", "|---|---:|---|---|"])
        for drift in audit["go_only_drift"]:
            lines.append(
                "| {field} | {allowed} | `{left}` | `{right}` |".format(
                    field=drift["field"],
                    allowed=str(drift["allowed"]).lower(),
                    left=compact_json(drift["gomoufox_python"]),
                    right=compact_json(drift["gomoufox_node_direct"]),
                )
            )
    lines.append("")
    return "\n".join(lines)


def compact_json(value):
    text = json.dumps(value, sort_keys=True, separators=(",", ":"))
    if len(text) > 96:
        return text[:93] + "..."
    return text


def target_url_from_reports(paths):
    for path in paths:
        report = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
        results = report.get("results") or []
        if results and results[0].get("url"):
            return results[0]["url"]
    return ""


def now_iso():
    return datetime.now(timezone.utc).isoformat()


class LocalServer:
    def __init__(self):
        self.httpd = None
        self.thread = None
        self.url = ""

    def start(self):
        handler = make_handler()
        self.httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        port = self.httpd.server_address[1]
        self.url = f"http://127.0.0.1:{port}{TARGET_PATH}"
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()
        time.sleep(0.05)

    def stop(self):
        if self.httpd:
            self.httpd.shutdown()
            self.httpd.server_close()
        if self.thread:
            self.thread.join(timeout=2)


def make_handler():
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path != TARGET_PATH:
                self.send_response(404)
                self.end_headers()
                return
            body = HTML.encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, _format, *_args):
            return

    return Handler


if __name__ == "__main__":
    raise SystemExit(main())
