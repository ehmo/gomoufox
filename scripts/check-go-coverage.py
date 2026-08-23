#!/usr/bin/env python3
import argparse
import sys
from decimal import Decimal, InvalidOperation
from pathlib import Path


def parse_profile(path: Path) -> tuple[int, int, list[tuple[str, int]]]:
    covered = 0
    total = 0
    uncovered = []
    lines = path.read_text(encoding="utf-8").splitlines()
    if not lines or not lines[0].startswith("mode: "):
        raise ValueError("coverage profile is missing its mode header")
    for line_number, line in enumerate(lines[1:], start=2):
        if not line:
            continue
        fields = line.rsplit(maxsplit=2)
        if len(fields) != 3:
            raise ValueError(f"coverage profile line {line_number} is malformed")
        try:
            statements = int(fields[1])
            count = int(fields[2])
        except ValueError as err:
            raise ValueError(f"coverage profile line {line_number} has invalid counts") from err
        if statements < 0 or count < 0:
            raise ValueError(f"coverage profile line {line_number} has negative counts")
        total += statements
        if count > 0:
            covered += statements
        elif statements > 0:
            uncovered.append((fields[0], statements))
    if total == 0:
        raise ValueError("coverage profile contains no statements")
    return covered, total, uncovered


def main() -> int:
    parser = argparse.ArgumentParser(description="Check exact statement counts in a Go coverage profile.")
    parser.add_argument("--profile", required=True)
    parser.add_argument("--min", required=True, dest="minimum")
    args = parser.parse_args()
    try:
        minimum = Decimal(args.minimum)
    except InvalidOperation:
        print(f"invalid coverage minimum: {args.minimum}", file=sys.stderr)
        return 2
    if not minimum.is_finite() or minimum < 0 or minimum > 100:
        print(f"coverage minimum must be between 0 and 100: {args.minimum}", file=sys.stderr)
        return 2
    try:
        covered, total, uncovered = parse_profile(Path(args.profile))
    except (OSError, ValueError) as err:
        print(f"invalid coverage profile: {err}", file=sys.stderr)
        return 2
    percentage = Decimal(covered) * Decimal(100) / Decimal(total)
    report = f"coverage: {covered}/{total} = {percentage:.6f}%"
    if percentage < minimum:
        print(f"{report} < required {minimum}%", file=sys.stderr)
        for source_range, statements in uncovered:
            suffix = "statement" if statements == 1 else "statements"
            print(f"uncovered: {source_range} ({statements} {suffix})", file=sys.stderr)
        return 1
    print(report)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
