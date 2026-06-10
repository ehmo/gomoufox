#!/usr/bin/env python3
import argparse
import json
import sys
from pathlib import Path


def check(path: Path) -> list[str]:
    data = json.loads(path.read_text(encoding="utf-8"))
    readiness = data.get("node_direct_consumer_readiness")
    if not isinstance(readiness, dict):
        if isinstance(data.get("python_removal_readiness"), dict):
            return ["missing node_direct_consumer_readiness; Go/Python benchmark readiness is not consumer no-Python readiness"]
        return ["missing node_direct_consumer_readiness"]
    failures: list[str] = []
    if readiness.get("status") != "candidate" or readiness.get("candidate") is not True:
        failures.append(f"readiness status {readiness.get('status')!r} is not 'candidate'")
    if readiness.get("runtime") != "node-direct":
        failures.append(f"runtime {readiness.get('runtime')!r} is not 'node-direct'")
    if readiness.get("python_invoked") is not False:
        failures.append(f"python_invoked {readiness.get('python_invoked')!r} is not false")
    checks = readiness.get("checks") or []
    for item in checks:
        if not item.get("passed"):
            failures.append(f"{item.get('name')}: {item.get('detail')}")
    if not checks:
        failures.append("readiness checks missing")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description="Fail unless a node-direct consumer readiness artifact is ready to promote.")
    parser.add_argument("--artifact", required=True, help="consumer readiness JSON artifact to check")
    args = parser.parse_args()
    failures = check(Path(args.artifact))
    if failures:
        for failure in failures:
            print(failure, file=sys.stderr)
        return 1
    print("node-direct consumer readiness ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
