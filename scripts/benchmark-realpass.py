#!/usr/bin/env python3
import argparse
import json
import os
import pathlib
import re
import shlex
import subprocess
import sys
import time
from datetime import datetime, timezone
from urllib.parse import urlparse


CATALOG_PATH = pathlib.Path(__file__).with_name("realpass-targets.json")


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Benchmark gomoufox against Python Camoufox on the same real-site targets."
    )
    parser.add_argument("--mode", choices=("smoke", "full", "extended", "custom"), default="smoke")
    parser.add_argument("--target", action="append", default=[], help="target URL or name=url; may be repeated")
    parser.add_argument("--target-file", default=str(CATALOG_PATH), help="target catalog JSON path")
    parser.add_argument("--max-targets", type=positive_int, default=0, help="limit selected targets after tier selection")
    parser.add_argument("--loops", type=positive_int, default=1)
    parser.add_argument("--out", default="dist/benchmarks/latest")
    parser.add_argument("--timeout", type=float, default=60.0)
    parser.add_argument("--wait-until", choices=("commit", "domcontentloaded", "load", "networkidle"), default="commit")
    parser.add_argument("--settle", type=float, default=7.0)
    parser.add_argument("--load-state-timeout", type=float, default=0.0, help="extra load-state wait after settle; 0 disables")
    parser.add_argument("--content-max-bytes", type=int, default=250000, help="maximum HTML bytes fetched for classification; 0 fetches full content")
    parser.add_argument("--sample-interval", type=float, default=0.5)
    parser.add_argument(
        "--run-order",
        choices=("alternate", "go-first", "python-first"),
        default="alternate",
        help="runtime execution order for each loop; alternate swaps order on even loops",
    )
    parser.add_argument("--python", default="")
    parser.add_argument("--gomoufox-realpass", default="", help="prebuilt gomoufox-realpass binary; default builds one under --out")
    parser.add_argument("--go-report-style", choices=("compact", "full"), default="compact")
    parser.add_argument("--go-sidecar-runtime", choices=("python", "node-direct"), default="python")
    parser.add_argument("--go-venv-dir", default="", help="custom gomoufox venv for the Go runner; default uses gomoufox cache")
    parser.add_argument("--reuse-browser", dest="reuse_browser", action="store_true", default=True, help="reuse one browser per runtime loop; each target still gets a fresh page context")
    parser.add_argument("--no-reuse-browser", dest="reuse_browser", action="store_false")
    parser.add_argument("--screenshots", action="store_true")
    parser.add_argument("--go-report", help="summarize an existing gomoufox report.json")
    parser.add_argument("--python-report", help="summarize an existing Python Camoufox report.json")
    parser.add_argument("--update-doc", help="write the Markdown report to this path")
    parser.add_argument("--list-targets", action="store_true", help="print selected targets as JSON and exit")
    parser.add_argument("--dry-run", action="store_true", help="print commands without running browsers")
    args = parser.parse_args()
    if args.load_state_timeout < 0:
        parser.error("--load-state-timeout must be >= 0")
    if args.content_max_bytes < 0:
        parser.error("--content-max-bytes must be >= 0")

    out_dir = pathlib.Path(args.out)
    catalog = load_catalog(pathlib.Path(args.target_file))
    target_entries = selected_targets(args, catalog)
    target_metadata = {target["name"]: target for target in target_entries}
    if args.list_targets:
        print(json.dumps(target_entries, separators=(",", ":")))
        return 0
    if args.go_report or args.python_report:
        if not args.go_report or not args.python_report:
            parser.error("--go-report and --python-report must be used together")
        runs = [build_run_from_reports(1, pathlib.Path(args.go_report), pathlib.Path(args.python_report), target_metadata=target_metadata)]
        benchmark = build_benchmark(args, runs, target_entries, catalog)
        write_benchmark(out_dir, benchmark, args.update_doc)
        return 0

    target_args = [target_arg(target) for target in target_entries]
    python_bin = selected_python(args.python)
    go_bin, build_cmd = go_runner(args, out_dir)
    commands = []
    runs = []
    if build_cmd:
        commands.append(build_cmd)
        if not args.dry_run:
            out_dir.mkdir(parents=True, exist_ok=True)
            run_command(build_cmd)
    for loop in range(1, args.loops + 1):
        loop_dir = out_dir / f"{args.mode}-loop-{loop}"
        go_out = loop_dir / "go"
        python_out = loop_dir / "python"
        go_cmd = [
            str(go_bin),
            "--out",
            str(go_out),
            "--timeout",
            f"{args.timeout:g}s",
            "--wait-until",
            args.wait_until,
            "--settle",
            f"{args.settle:g}s",
            "--load-state-timeout",
            f"{args.load_state_timeout:g}s",
            "--content-max-bytes",
            str(args.content_max_bytes),
            "--sample-interval",
            f"{args.sample_interval:g}s",
            "--report-style",
            args.go_report_style,
            "--sidecar-runtime",
            args.go_sidecar_runtime,
            f"--screenshots={str(args.screenshots).lower()}",
            "--unsafe-direct-network",
            "--generated-persona",
            "--max-failed=-1",
        ]
        if args.go_venv_dir:
            go_cmd += ["--venv-dir", args.go_venv_dir]
        if args.reuse_browser:
            go_cmd.append("--reuse-browser")
        for target in target_args:
            go_cmd += ["--target", target]
        python_cmd = [
            python_bin,
            "scripts/python-realpass.py",
            "--out",
            str(python_out),
            "--timeout",
            f"{args.timeout:g}",
            "--wait-until",
            args.wait_until,
            "--settle",
            f"{args.settle:g}",
            "--load-state-timeout",
            f"{args.load_state_timeout:g}",
            "--content-max-bytes",
            str(args.content_max_bytes),
            "--sample-interval",
            f"{args.sample_interval:g}",
        ]
        if not args.screenshots:
            python_cmd.append("--no-screenshots")
        if args.reuse_browser:
            python_cmd.append("--reuse-browser")
        for target in target_args:
            python_cmd += ["--target", target]
        run_order = runtime_order(args.run_order, loop)
        for runtime in run_order:
            commands.append(go_cmd if runtime == "go" else python_cmd)
        if args.dry_run:
            continue
        go_wall_ms = None
        python_wall_ms = None
        for runtime in run_order:
            if runtime == "go":
                go_wall_ms = run_timed(go_cmd)
            else:
                python_wall_ms = run_timed(python_cmd)
        runs.append(
            build_run_from_reports(
                loop,
                go_out / "report.json",
                python_out / "report.json",
                go_wall_ms=go_wall_ms,
                python_wall_ms=python_wall_ms,
                run_order=run_order,
            )
        )

    if args.dry_run:
        for command in commands:
            print(shlex.join(command))
        return 0

    benchmark = build_benchmark(args, runs, target_entries, catalog)
    write_benchmark(out_dir, benchmark, args.update_doc)
    return 0


def load_catalog(path):
    catalog = read_json(path)
    targets = catalog.get("targets") or []
    names = set()
    by_name = {}
    for target in targets:
        for key in ("name", "url", "kind"):
            if not target.get(key):
                raise SystemExit(f"target catalog entry missing {key}: {target}")
        if target["name"] in names:
            raise SystemExit(f"duplicate target name in catalog: {target['name']}")
        names.add(target["name"])
        by_name[target["name"]] = target
        validate_target_tags(target)
    for tier, tier_names in (catalog.get("tiers") or {}).items():
        missing = [name for name in tier_names if name not in names]
        if missing:
            raise SystemExit(f"target tier {tier} references missing targets: {', '.join(missing)}")
    validate_required_tags(catalog, by_name)
    return catalog


def validate_target_tags(target):
    tags = target.get("tags") or []
    if not isinstance(tags, list):
        raise SystemExit(f"target tags must be a list: {target['name']}")
    seen = set()
    for tag in tags:
        if not isinstance(tag, str) or not tag:
            raise SystemExit(f"target tag must be a non-empty string: {target['name']}")
        if slug(tag) != tag:
            raise SystemExit(f"target tag must be lowercase slug text: {target['name']}:{tag}")
        if tag in seen:
            raise SystemExit(f"duplicate target tag: {target['name']}:{tag}")
        seen.add(tag)


def validate_required_tags(catalog, by_name):
    tiers = catalog.get("tiers") or {}
    for tier, tags in (catalog.get("required_tags") or {}).items():
        if tier not in tiers:
            raise SystemExit(f"required tag tier not found: {tier}")
        if not isinstance(tags, list) or not tags:
            raise SystemExit(f"required tags must be a non-empty list: {tier}")
        covered = set()
        for name in tiers[tier]:
            covered.update(by_name[name].get("tags") or [])
        missing = [tag for tag in tags if tag not in covered]
        if missing:
            raise SystemExit(f"target tier {tier} is missing required tags: {', '.join(missing)}")


def selected_targets(args, catalog):
    if args.target:
        targets = [parse_target_entry(raw) for raw in args.target]
    if args.mode == "custom":
        if not args.target:
            raise SystemExit("--mode custom requires at least one --target")
    elif not args.target:
        by_name = {target["name"]: target for target in catalog["targets"]}
        tier_names = (catalog.get("tiers") or {}).get(args.mode)
        if not tier_names:
            raise SystemExit(f"target tier not found: {args.mode}")
        targets = [by_name[name] for name in tier_names]
    if args.max_targets:
        targets = targets[: args.max_targets]
    ensure_unique_targets(targets)
    return targets


def parse_target_entry(raw):
    if "=" in raw:
        name_part, value = raw.split("=", 1)
        name_part = name_part.strip()
        value = value.strip()
    else:
        value = raw.strip()
        name_part = ""
    parsed = urlparse(value)
    if not parsed.scheme or not parsed.netloc:
        raise SystemExit(f"invalid target URL: {raw}")
    if not name_part:
        name_part = slug(parsed.netloc)
    if "|" in name_part:
        name, kind = name_part.split("|", 1)
        kind = kind.strip() or "custom"
    else:
        name, kind = name_part, "custom"
    return {"name": slug(name), "url": value, "kind": slug(kind) or "custom"}


def ensure_unique_targets(targets):
    seen = set()
    for target in targets:
        name = target["name"]
        if name in seen:
            raise SystemExit(f"duplicate target selected: {name}")
        seen.add(name)


def target_arg(target):
    return f"{target['name']}|{target['kind']}={target['url']}"


def selected_python(override):
    if override:
        return override
    for candidate in (
        pathlib.Path.home() / "Library/Caches/gomoufox/venv/bin/python",
        pathlib.Path.home() / ".cache/gomoufox/venv/bin/python",
    ):
        if candidate.exists() and os.access(candidate, os.X_OK):
            return str(candidate)
    return os.environ.get("PYTHON", "python3")


def go_runner(args, out_dir):
    if args.gomoufox_realpass:
        return pathlib.Path(args.gomoufox_realpass), None
    path = pathlib.Path(out_dir) / "gomoufox-realpass-bench"
    return path, ["go", "build", "-trimpath", "-buildvcs=false", "-o", str(path), "./cmd/gomoufox-realpass"]


def run_command(command):
    print("+ " + shlex.join(command), file=sys.stderr, flush=True)
    subprocess.run(command, check=True)


def run_timed(command):
    print("+ " + shlex.join(command), file=sys.stderr, flush=True)
    started = time.monotonic()
    subprocess.run(command, check=True)
    return int((time.monotonic() - started) * 1000)


def runtime_order(order, loop):
    if order == "python-first":
        return ["python", "go"]
    if order == "alternate" and loop % 2 == 0:
        return ["python", "go"]
    return ["go", "python"]


def build_run_from_reports(loop, go_path, python_path, go_wall_ms=None, python_wall_ms=None, target_metadata=None, run_order=None):
    go_report = read_json(go_path)
    python_report = read_json(python_path)
    target_metadata = target_metadata or {}
    go_metrics = metrics_from_report(go_report, go_wall_ms, go_path)
    python_metrics = metrics_from_report(python_report, python_wall_ms, python_path)
    outcomes = target_outcomes(go_report, python_report, target_metadata)
    return {
        "loop": loop,
        "run_order": run_order or ["go", "python"],
        "go_report": str(go_path),
        "python_report": str(python_path),
        "options": options_from_reports(go_report, python_report),
        "go": go_metrics,
        "python": python_metrics,
        "ratios": ratios(go_metrics, python_metrics),
        "target_outcomes": outcomes,
        "outcome_groups": outcome_groups(outcomes),
        "outcome_mismatches": outcome_mismatches(go_report, python_report, target_metadata),
    }


def metrics_from_report(report, wall_ms, report_path=None):
    results = report.get("results") or []
    summary = report.get("summary") or summarize(results)
    if not wall_ms:
        wall_ms = wall_ms_from_report(report) or sum(int(result.get("duration_ms") or 0) for result in results)
    target_duration_ms = sum(int(result.get("duration_ms") or 0) for result in results)
    artifacts = artifact_metrics(report_path)
    metrics = {
        "total": int(summary.get("total") or len(results)),
        "passed": int(summary.get("passed") or 0),
        "blocked": int(summary.get("blocked") or 0),
        "failed": int(summary.get("failed") or 0),
        "wall_ms": int(wall_ms),
        "target_duration_ms": int(target_duration_ms),
        "avg_target_ms": int(target_duration_ms / len(results)) if results else 0,
        "peak_rss_mib": float(summary.get("peak_rss_mib") or 0.0),
        "peak_cpu_percent": float(summary.get("peak_cpu_percent") or 0.0),
    }
    metrics.update(artifacts)
    return metrics


def artifact_metrics(report_path):
    metrics = {
        "report_json_bytes": 0,
        "report_markdown_bytes": 0,
        "report_json_estimated_tokens": 0,
        "report_markdown_estimated_tokens": 0,
        "report_total_estimated_tokens": 0,
    }
    if not report_path:
        return metrics
    json_path = pathlib.Path(report_path)
    md_path = json_path.with_suffix(".md")
    if json_path.exists():
        metrics["report_json_bytes"] = json_path.stat().st_size
        metrics["report_json_estimated_tokens"] = estimate_tokens(metrics["report_json_bytes"])
    if md_path.exists():
        metrics["report_markdown_bytes"] = md_path.stat().st_size
        metrics["report_markdown_estimated_tokens"] = estimate_tokens(metrics["report_markdown_bytes"])
    metrics["report_total_estimated_tokens"] = metrics["report_json_estimated_tokens"] + metrics["report_markdown_estimated_tokens"]
    return metrics


def estimate_tokens(byte_count):
    if byte_count <= 0:
        return 0
    return int((byte_count + 3) / 4)


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
        summary["peak_rss_mib"] = max(summary["peak_rss_mib"], float(resources.get("peak_rss_mib") or 0.0))
        summary["peak_cpu_percent"] = max(summary["peak_cpu_percent"], float(resources.get("max_cpu_percent") or 0.0))
    return summary


def wall_ms_from_report(report):
    started = parse_time(report.get("started_at"))
    finished = parse_time(report.get("finished_at"))
    if started and finished:
        return int((finished - started).total_seconds() * 1000)
    return 0


def parse_time(value):
    if not value:
        return None
    try:
        return datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except ValueError:
        return None


def ratios(go_metrics, python_metrics):
    return {
        "wall_time": ratio(go_metrics["wall_ms"], python_metrics["wall_ms"]),
        "target_duration": ratio(go_metrics["target_duration_ms"], python_metrics["target_duration_ms"]),
        "peak_rss": ratio(go_metrics["peak_rss_mib"], python_metrics["peak_rss_mib"]),
        "peak_cpu": ratio(go_metrics["peak_cpu_percent"], python_metrics["peak_cpu_percent"]),
        "report_tokens": ratio(go_metrics["report_total_estimated_tokens"], python_metrics["report_total_estimated_tokens"]),
    }


def ratio(left, right):
    if not right:
        return None
    return float(left) / float(right)


def outcome_mismatches(go_report, python_report, target_metadata=None):
    return [
        {
            "target": item["target"],
            "go": item["go_outcome"],
            "python": item["python_outcome"],
        }
        for item in target_outcomes(go_report, python_report, target_metadata or {})
        if item["go_outcome"] != item["python_outcome"]
    ]


def target_outcomes(go_report, python_report, target_metadata=None):
    target_metadata = target_metadata or {}
    go_results = {result.get("name"): result for result in go_report.get("results") or []}
    python_results = {result.get("name"): result for result in python_report.get("results") or []}
    outcomes = []
    for name in sorted(set(go_results) | set(python_results)):
        go_result = go_results.get(name) or {}
        python_result = python_results.get(name) or {}
        outcomes.append(
            {
                "target": name,
                "kind": (target_metadata.get(name) or {}).get("kind") or go_result.get("kind") or python_result.get("kind") or "",
                "tags": (target_metadata.get(name) or {}).get("tags") or [],
                "go_outcome": go_result.get("outcome"),
                "python_outcome": python_result.get("outcome"),
                "go_duration_ms": int(go_result.get("duration_ms") or 0),
                "python_duration_ms": int(python_result.get("duration_ms") or 0),
                "go_signals": go_result.get("signals") or [],
                "python_signals": python_result.get("signals") or [],
            }
        )
    return outcomes


def outcome_groups(outcomes):
    groups = {
        "shared_blocked_targets": [],
        "shared_failed_targets": [],
        "go_only_regressions": [],
        "python_only_differences": [],
    }
    for item in outcomes:
        go_outcome = item["go_outcome"]
        python_outcome = item["python_outcome"]
        target = item["target"]
        if go_outcome == "blocked" and python_outcome == "blocked":
            groups["shared_blocked_targets"].append(target)
        elif go_outcome == "failed" and python_outcome == "failed":
            groups["shared_failed_targets"].append(target)
        elif go_outcome in ("blocked", "failed", None) and python_outcome == "passed":
            groups["go_only_regressions"].append(target)
        elif python_outcome in ("blocked", "failed", None) and go_outcome == "passed":
            groups["python_only_differences"].append(target)
    return groups


def build_benchmark(args, runs, targets, catalog):
    benchmark = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "mode": args.mode,
        "target_count": len(targets),
        "target_tier_counts": tier_counts(catalog),
        "target_catalog": display_path(args.target_file),
        "target_names": [target["name"] for target in targets],
        "target_tags": sorted({tag for target in targets for tag in target.get("tags", [])}),
        "loops": len(runs),
        "options": benchmark_options(args, runs),
        "runs": runs,
        "summary": aggregate(runs),
    }
    benchmark["python_removal_readiness"] = python_removal_readiness(benchmark)
    return benchmark


def tier_counts(catalog):
    return {tier: len(names) for tier, names in sorted((catalog.get("tiers") or {}).items())}


def benchmark_options(args, runs):
    options = {
        "timeout_seconds": args.timeout,
        "wait_until": args.wait_until,
        "settle_seconds": args.settle,
        "load_state_timeout_seconds": args.load_state_timeout,
        "content_max_bytes": args.content_max_bytes,
        "sample_interval_seconds": args.sample_interval,
        "run_order": args.run_order,
        "go_runner": "existing_report" if args.go_report else ("prebuilt_binary" if args.gomoufox_realpass else "built_binary"),
        "go_sidecar_runtime": args.go_sidecar_runtime,
        "go_custom_venv": bool(args.go_venv_dir),
        "screenshots": args.screenshots,
        "max_targets": args.max_targets,
    }
    if not args.go_report:
        options["reuse_browser"] = args.reuse_browser
        options["go_report_style"] = args.go_report_style
    if runs and runs[-1].get("options"):
        options.update(runs[-1]["options"])
        options["max_targets"] = args.max_targets
    return options


def options_from_reports(go_report, python_report):
    go_options = go_report.get("options") or {}
    python_options = python_report.get("options") or {}
    out = {}
    for source_key, dest_key in (
        ("timeout", "timeout_seconds"),
        ("settle", "settle_seconds"),
        ("load_state_timeout", "load_state_timeout_seconds"),
        ("sample_interval", "sample_interval_seconds"),
    ):
        go_value = duration_seconds(go_options.get(source_key))
        python_value = duration_seconds(python_options.get(source_key))
        if go_value is None and python_value is None:
            continue
        if go_value is None or python_value is None or abs(go_value - python_value) > 0.000001:
            raise SystemExit(f"report option mismatch for {source_key}: go={go_options.get(source_key)!r} python={python_options.get(source_key)!r}")
        out[dest_key] = go_value
    go_screenshots = go_options.get("screenshots")
    python_screenshots = python_options.get("screenshots")
    if go_screenshots is not None or python_screenshots is not None:
        if go_screenshots != python_screenshots:
            raise SystemExit(f"report option mismatch for screenshots: go={go_screenshots!r} python={python_screenshots!r}")
        out["screenshots"] = bool(go_screenshots)
    go_wait_until = go_options.get("wait_until")
    python_wait_until = python_options.get("wait_until")
    if go_wait_until is not None or python_wait_until is not None:
        if go_wait_until != python_wait_until:
            raise SystemExit(f"report option mismatch for wait_until: go={go_wait_until!r} python={python_wait_until!r}")
        out["wait_until"] = go_wait_until
    go_reuse = go_options.get("reuse_browser")
    python_reuse = python_options.get("reuse_browser")
    if go_reuse is not None or python_reuse is not None:
        if go_reuse != python_reuse:
            raise SystemExit(f"report option mismatch for reuse_browser: go={go_reuse!r} python={python_reuse!r}")
        out["reuse_browser"] = bool(go_reuse)
    go_content_max = go_options.get("content_max_bytes")
    python_content_max = python_options.get("content_max_bytes")
    if go_content_max is not None or python_content_max is not None:
        if go_content_max != python_content_max:
            raise SystemExit(f"report option mismatch for content_max_bytes: go={go_content_max!r} python={python_content_max!r}")
        out["content_max_bytes"] = int(go_content_max)
    if go_options.get("report_style"):
        out["go_report_style"] = go_options.get("report_style")
    if go_options.get("sidecar_runtime"):
        out["go_sidecar_runtime"] = go_options.get("sidecar_runtime")
    if go_options.get("custom_venv"):
        out["go_custom_venv"] = True
    return out


def duration_seconds(value):
    if value is None:
        return None
    if isinstance(value, (int, float)):
        return float(value)
    text = str(value).strip()
    if not text:
        return None
    matches = re.findall(r"([0-9]+(?:\.[0-9]+)?)(ms|s|m|h)", text)
    if not matches or "".join(number + unit for number, unit in matches) != text:
        raise SystemExit(f"invalid duration in report options: {value!r}")
    multipliers = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}
    return sum(float(number) * multipliers[unit] for number, unit in matches)


def display_path(path_value):
    path = pathlib.Path(path_value)
    if not path.is_absolute():
        return path.as_posix()
    bases = (pathlib.Path.cwd(), pathlib.Path(__file__).resolve().parent.parent)
    for base in bases:
        try:
            return path.relative_to(base.resolve()).as_posix()
        except ValueError:
            continue
    return path.name


def aggregate(runs):
    go = aggregate_runtime(run["go"] for run in runs)
    python = aggregate_runtime(run["python"] for run in runs)
    return {
        "go": go,
        "python": python,
        "ratios": ratios(go, python),
        "outcome_mismatch_count": sum(len(run["outcome_mismatches"]) for run in runs),
    }


def python_removal_readiness(benchmark):
    options = benchmark.get("options") or {}
    summary = benchmark.get("summary") or {}
    go = summary.get("go") or {}
    python = summary.get("python") or {}
    ratio_values = summary.get("ratios") or {}
    criteria = [
        {
            "name": "go_sidecar_runtime_is_node_direct",
            "passed": options.get("go_sidecar_runtime") == "node-direct",
            "detail": f"go_sidecar_runtime={options.get('go_sidecar_runtime', 'python')}",
        },
        {
            "name": "extended_target_matrix",
            "passed": benchmark.get("mode") == "extended" and int(benchmark.get("target_count") or 0) >= 100,
            "detail": f"mode={benchmark.get('mode')} targets={benchmark.get('target_count')}",
        },
        {
            "name": "outcome_parity",
            "passed": int(summary.get("outcome_mismatch_count") or 0) == 0,
            "detail": f"outcome_mismatch_count={summary.get('outcome_mismatch_count')}",
        },
        {
            "name": "no_runtime_failures",
            "passed": int(go.get("failed") or 0) == 0 and int(python.get("failed") or 0) == 0,
            "detail": f"go_failed={go.get('failed')} python_failed={python.get('failed')}",
        },
    ]
    for name, maximum in (
        ("wall_time", 0.95),
        ("target_duration", 0.95),
        ("peak_rss", 0.95),
        ("peak_cpu", 0.95),
        ("report_tokens", 0.50),
    ):
        value = ratio_values.get(name)
        criteria.append({
            "name": f"{name}_beats_python",
            "passed": isinstance(value, (int, float)) and value <= maximum,
            "detail": f"{name}={value} max={maximum}",
        })
    passed = all(item["passed"] for item in criteria)
    runtime = options.get("go_sidecar_runtime", "python")
    return {
        "status": "candidate" if passed else ("not_node_direct" if runtime != "node-direct" else "blocked"),
        "candidate": passed,
        "criteria": criteria,
        "note": "node-direct still needs Python for launch payload generation; candidate means the long-lived browser sidecar is ready to promote, not that Python is fully removed.",
    }


def aggregate_runtime(items):
    items = list(items)
    if not items:
        return {}
    return {
        "total": items[-1]["total"],
        "passed": items[-1]["passed"],
        "blocked": items[-1]["blocked"],
        "failed": items[-1]["failed"],
        "wall_ms": int(sum(item["wall_ms"] for item in items) / len(items)),
        "target_duration_ms": int(sum(item["target_duration_ms"] for item in items) / len(items)),
        "avg_target_ms": int(sum(item["avg_target_ms"] for item in items) / len(items)),
        "peak_rss_mib": max(item["peak_rss_mib"] for item in items),
        "peak_cpu_percent": max(item["peak_cpu_percent"] for item in items),
        "report_json_bytes": int(sum(item["report_json_bytes"] for item in items) / len(items)),
        "report_markdown_bytes": int(sum(item["report_markdown_bytes"] for item in items) / len(items)),
        "report_json_estimated_tokens": int(sum(item["report_json_estimated_tokens"] for item in items) / len(items)),
        "report_markdown_estimated_tokens": int(sum(item["report_markdown_estimated_tokens"] for item in items) / len(items)),
        "report_total_estimated_tokens": int(sum(item["report_total_estimated_tokens"] for item in items) / len(items)),
    }


def write_benchmark(out_dir, benchmark, update_doc):
    out_dir.mkdir(parents=True, exist_ok=True)
    json_path = out_dir / "benchmark.json"
    md_path = out_dir / "benchmark.md"
    markdown = markdown_report(benchmark)
    json_path.write_text(json.dumps(benchmark, indent=2) + "\n", encoding="utf-8")
    md_path.write_text(markdown, encoding="utf-8")
    if update_doc:
        doc_path = pathlib.Path(update_doc)
        doc_path.parent.mkdir(parents=True, exist_ok=True)
        doc_path.write_text(markdown, encoding="utf-8")
    print(f"json: {json_path}")
    print(f"report: {md_path}")


def markdown_report(benchmark):
    summary = benchmark["summary"]
    go = summary["go"]
    python = summary["python"]
    ratios_value = summary["ratios"]
    mismatch_count = summary["outcome_mismatch_count"]
    latest_run = benchmark["runs"][-1]
    latest_groups = latest_run.get("outcome_groups") or outcome_groups(latest_run["target_outcomes"])
    tier_counts_value = benchmark.get("target_tier_counts") or {}
    extended_count = int(tier_counts_value.get("extended") or benchmark["target_count"])
    readiness = benchmark.get("python_removal_readiness") or {}
    lines = [
        "# Go/Python Benchmark",
        "",
        f"- Generated: {benchmark['generated_at']}",
        f"- Mode: {benchmark['mode']}",
        f"- Targets: {fmt_int(benchmark['target_count'])}",
        f"- Loops: {fmt_int(benchmark['loops'])}",
        f"- Timeout: {benchmark['options']['timeout_seconds']:g}s",
        f"- Wait until: {benchmark['options'].get('wait_until', 'commit')}",
        f"- Settle: {benchmark['options']['settle_seconds']:g}s",
        f"- Load-state timeout: {benchmark['options'].get('load_state_timeout_seconds', 0):g}s",
        f"- Content max bytes: {fmt_int(benchmark['options'].get('content_max_bytes', 0))}",
        f"- Sample interval: {benchmark['options']['sample_interval_seconds']:g}s",
        f"- Run order: {benchmark['options'].get('run_order', 'existing-reports')}",
        f"- Go runner: {go_runner_label(benchmark['options'].get('go_runner', 'built_binary'))}",
        f"- Go sidecar runtime: {benchmark['options'].get('go_sidecar_runtime', 'python')}",
        f"- Go custom venv: {yes_no(benchmark['options'].get('go_custom_venv'))}",
        f"- Reuse browser: {yes_no(benchmark['options'].get('reuse_browser'))}",
        f"- Go report style: {benchmark['options'].get('go_report_style', 'compact')}",
        "",
        "This benchmark runs gomoufox and Python Camoufox against the same target set.",
        "It records outcome parity, wall time, browser work duration, peak RSS, peak CPU, and agent-output token footprint.",
        "For parity, gomoufox runs with `--unsafe-direct-network` and generated personas. Local URL guardrails are tested elsewhere.",
        runner_interpretation(benchmark["options"].get("go_runner", "built_binary")),
        "Resource samples cover the gomoufox sidecar process tree and the Python Camoufox harness process tree.",
        "",
        "## Modes",
        "",
        "- `smoke`: 2 fast targets for quick local checks.",
        "- `full`: 8 detector and real-site targets used for the checked baseline.",
        f"- `extended`: {extended_count} read-only public websites for broader resource, speed, parity, and token-footprint evidence.",
        "",
        f"Selected target tags: {inline_list(benchmark.get('target_tags') or [])}.",
        "",
        "Refresh the checked baseline after significant browser, sidecar, MCP, CLI, or resource-related changes:",
        "",
        "```bash",
        "scripts/benchmark-realpass.py --mode full --go-report-style compact --update-doc docs/BENCHMARKS.md",
        "```",
        "",
        "Run the extended matrix before release candidates or major runtime changes:",
        "",
        "```bash",
        "scripts/benchmark-realpass.py --mode extended --go-report-style compact --out dist/benchmarks/extended",
        "```",
        "",
        "Run the local fingerprint audit when changing Camoufox pins, launch options,",
        "Firefox prefs, node-direct, WebGL, locale, timezone, screen, fonts, or canvas",
        "handling:",
        "",
        "```bash",
        "scripts/fingerprint-audit.py",
        "```",
        "",
        "The audit serves one local page and records Python Camoufox, gomoufox with the",
        "Python sidecar, and gomoufox with node-direct. Release gating compares the two",
        "gomoufox runtimes and fails on any unallowed drift in JS-visible fields such as",
        "UA, platform, languages, screen, timezone, WebGL, WebRTC, fonts, and canvas. The",
        "Python Camoufox row is context, not the fail-closed comparator, because its",
        "generated persona can differ on a single local run.",
        "",
        "## Pass/Fail Rules",
        "",
        "- Go-only blocked, failed, or missing targets block release.",
        "- Go-only JS-visible fingerprint drift between gomoufox Python sidecar and gomoufox node-direct blocks release unless the changed field is explicitly allowlisted with evidence.",
        "- Go/Python outcome mismatches that reproduce on retry block release.",
        "- Shared Go+Python blocked or failed targets are reported as site or upstream Camoufox behavior, not a gomoufox-only failure.",
        "- Known recurring shared blocks should stay in the explicit release-gate allowlist.",
        "- In release mode, a new shared block, failure, or performance outlier gets one focused retry.",
        "- The retry must confirm Go/Python outcome parity and stay under absolute RSS and CPU caps.",
        "- The gate merges focused retry measurements back into the full report and reruns the strict required-target, resource-ratio, timing-ratio, and report-token checks against that merged evidence.",
        f"- Release gate defaults block peak RSS above {fmt_int(6000)} MiB, peak CPU above 900%, Go RSS above Python * 1.50, and Go CPU above Python * 1.50.",
        "- Full and release gates block Go wall time above Python * 1.05, Go target duration above Python * 1.05, and Go report tokens above Python * 0.50.",
        "- Smoke mode is a functional parity check; its wall time is startup dominated.",
        "- Use `--loops 2 --run-order alternate` when investigating timing changes so neither runtime always runs second.",
        "",
        "## Summary",
        "",
        "| Runtime | Passed | Blocked | Failed | Wall ms | Target ms | Peak RSS MiB | Peak CPU % |",
        "|---|---:|---:|---:|---:|---:|---:|---:|",
        runtime_row("gomoufox", go),
        runtime_row("Python Camoufox", python),
        "",
        "| Ratio | Go / Python |",
        "|---|---:|",
        ratio_row("Wall time", ratios_value["wall_time"]),
        ratio_row("Target duration", ratios_value["target_duration"]),
        ratio_row("Peak RSS", ratios_value["peak_rss"]),
        ratio_row("Peak CPU", ratios_value["peak_cpu"]),
        ratio_row("Report tokens", ratios_value["report_tokens"]),
        "",
        "## Python-Removal Readiness",
        "",
        f"- Status: {readiness.get('status', 'unknown')}",
        f"- Candidate: {yes_no(readiness.get('candidate'))}",
        f"- Note: {readiness.get('note', 'No readiness data recorded.')}",
        "",
        "| Criterion | Passed | Detail |",
        "|---|---:|---|",
        *readiness_rows(readiness.get("criteria") or []),
        "",
        "## Agent Output Footprint",
        "",
        "Token estimates use `ceil(bytes / 4)`. They compare generated benchmark artifacts, not model billing.",
        "",
        "| Runtime | JSON bytes | Markdown bytes | Estimated tokens |",
        "|---|---:|---:|---:|",
        artifact_row("gomoufox", go),
        artifact_row("Python Camoufox", python),
        "",
        "## Outcome Classes",
        "",
        f"- Shared blocked: {inline_list(latest_groups['shared_blocked_targets'])}",
        f"- Shared failed: {inline_list(latest_groups['shared_failed_targets'])}",
        f"- Go-only regressions: {inline_list(latest_groups['go_only_regressions'])}",
        f"- Python-only differences: {inline_list(latest_groups['python_only_differences'])}",
        "",
        "## Interpretation",
        "",
        f"- Outcome mismatches: {mismatch_count}",
        "- The browser dominates wall time. Treat serial Go/Python speed as a parity check, not proof that Go will always outrun Python.",
        "- gomoufox should still win the agent-output footprint. A report-token ratio above 0.50 is a regression.",
        "- gomoufox benefits outside this run include typed Go integration, CLI and MCP surfaces, local URL guardrails, and repeatable checks against Python Camoufox.",
        "",
        "## Per Loop",
        "",
        "| Loop | Runtime | Passed | Blocked | Failed | Wall ms | Target ms | Peak RSS MiB | Peak CPU % | Mismatches |",
        "|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for run in benchmark["runs"]:
        lines.append(loop_row(run["loop"], "gomoufox", run["go"], len(run["outcome_mismatches"])))
        lines.append(loop_row(run["loop"], "Python Camoufox", run["python"], len(run["outcome_mismatches"])))
    lines += [
        "",
        "## Target Outcomes",
        "",
        "| Target | Kind | Tags | Go | Python | Go ms | Python ms |",
        "|---|---|---|---:|---:|---:|---:|",
    ]
    for item in latest_run["target_outcomes"]:
        lines.append(
            f"| {item['target']} | {item['kind']} | {', '.join(item['tags'])} | {item['go_outcome']} | {item['python_outcome']} | "
            f"{fmt_int(item['go_duration_ms'])} | {fmt_int(item['python_duration_ms'])} |"
        )
    lines.append("")
    return "\n".join(lines)


def runtime_row(name, metrics):
    return (
        f"| {name} | {metrics['passed']} | {metrics['blocked']} | {metrics['failed']} | "
        f"{fmt_int(metrics['wall_ms'])} | {fmt_int(metrics['target_duration_ms'])} | {fmt_float(metrics['peak_rss_mib'])} | {metrics['peak_cpu_percent']:.1f} |"
    )


def go_runner_label(value):
    if value == "existing_report":
        return "existing release-gate report"
    return value


def runner_interpretation(value):
    if value == "existing_report":
        return "This document summarizes existing release-gate reports generated by a built gomoufox-realpass binary. Build time is excluded."
    return "gomoufox is timed as a built CLI binary. Build time is excluded."


def loop_row(loop, name, metrics, mismatches):
    return (
        f"| {loop} | {name} | {metrics['passed']} | {metrics['blocked']} | {metrics['failed']} | "
        f"{fmt_int(metrics['wall_ms'])} | {fmt_int(metrics['target_duration_ms'])} | {fmt_float(metrics['peak_rss_mib'])} | "
        f"{metrics['peak_cpu_percent']:.1f} | {mismatches} |"
    )


def ratio_row(name, value):
    if value is None:
        return f"| {name} | n/a |"
    return f"| {name} | {value:.3f} |"


def readiness_rows(criteria):
    if not criteria:
        return ["| none | no | missing readiness criteria |"]
    return [f"| {item.get('name', '')} | {yes_no(item.get('passed'))} | {item.get('detail', '')} |" for item in criteria]


def artifact_row(name, metrics):
    return (
        f"| {name} | {fmt_int(metrics['report_json_bytes'])} | {fmt_int(metrics['report_markdown_bytes'])} | "
        f"{fmt_int(metrics['report_total_estimated_tokens'])} |"
    )


def fmt_int(value):
    return f"{int(value):,}"


def fmt_float(value):
    return f"{float(value):,.1f}"


def inline_list(items):
    if not items:
        return "none"
    return ", ".join(f"`{item}`" for item in items)


def yes_no(value):
    return "yes" if bool(value) else "no"


def slug(value):
    out = []
    last_dash = False
    for char in str(value).lower():
        if char.isascii() and char.isalnum():
            out.append(char)
            last_dash = False
        elif out and not last_dash:
            out.append("-")
            last_dash = True
    return "".join(out).strip("-")


def read_json(path):
    return json.loads(pathlib.Path(path).read_text(encoding="utf-8"))


def positive_int(value):
    parsed = int(value)
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be >= 1")
    return parsed


if __name__ == "__main__":
    raise SystemExit(main())
