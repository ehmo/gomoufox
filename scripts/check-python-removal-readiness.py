#!/usr/bin/env python3
import argparse
import json
import sys
from pathlib import Path


def check(path: Path) -> list[str]:
    data = json.loads(path.read_text(encoding="utf-8"))
    readiness = data.get("python_removal_readiness")
    if not isinstance(readiness, dict):
        return ["missing python_removal_readiness"]
    failures: list[str] = []
    if readiness.get("status") != "candidate" or readiness.get("candidate") is not True:
        failures.append(f"readiness status {readiness.get('status')!r} is not 'candidate'")
    for item in readiness.get("criteria") or []:
        if not item.get("passed"):
            failures.append(f"{item.get('name')}: {item.get('detail')}")
    if not readiness.get("criteria"):
        failures.append("readiness criteria missing")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description="Fail unless a node-direct benchmark artifact is ready to promote.")
    parser.add_argument("--artifact", required=True, help="benchmark JSON artifact to check")
    args = parser.parse_args()
    failures = check(Path(args.artifact))
    if failures:
        for failure in failures:
            print(failure, file=sys.stderr)
        return 1
    print("python-removal readiness ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
