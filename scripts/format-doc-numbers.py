#!/usr/bin/env python3
import argparse
import re
import sys
from pathlib import Path


DEFAULT_PATHS = (
    "README.md",
    "CHANGELOG.md",
    "CONTRIBUTING.md",
    "SECURITY.md",
    "docs/BENCHMARKS.md",
    "skills/gomoufox/SKILL.md",
    "skills/gomoufox-mcp/SKILL.md",
)

NUMBER_PATTERN = re.compile(r"(?<![A-Za-z0-9_.,/])(\d{4,})(\.\d+)?(?![A-Za-z0-9_./])")


def should_format(text: str, match: re.Match[str]) -> bool:
    raw = match.group(1)
    decimal = match.group(2)
    if raw.startswith("0"):
        return False
    value = int(raw)
    if decimal is None and 1900 <= value <= 2099:
        return False
    before = text[match.start() - 1] if match.start() > 0 else ""
    after = text[match.end()] if match.end() < len(text) else ""
    after_next = text[match.end() + 1] if match.end() + 1 < len(text) else ""
    if before == "v":
        return False
    if after == "-" and after_next.isdigit():
        return False
    return True


def format_segment(segment: str) -> str:
    def replace(match: re.Match[str]) -> str:
        if not should_format(segment, match):
            return match.group(0)
        decimal = match.group(2) or ""
        return f"{int(match.group(1)):,}{decimal}"

    return NUMBER_PATTERN.sub(replace, segment)


def format_line(line: str, in_fence: bool) -> tuple[str, bool]:
    stripped = line.lstrip()
    if stripped.startswith("```"):
        return line, not in_fence
    if in_fence:
        return line, in_fence
    parts = line.split("`")
    for index in range(0, len(parts), 2):
        parts[index] = format_segment(parts[index])
    return "`".join(parts), in_fence


def format_text(text: str) -> str:
    lines = text.splitlines(keepends=True)
    in_fence = False
    out: list[str] = []
    for line in lines:
        formatted, in_fence = format_line(line, in_fence)
        out.append(formatted)
    return "".join(out)


def existing_paths(root: Path, raw_paths: list[str]) -> list[Path]:
    paths: list[Path] = []
    for raw in raw_paths:
        path = root / raw
        if path.exists():
            paths.append(path)
    return paths


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--write", action="store_true", help="rewrite files in place")
    parser.add_argument("paths", nargs="*", help="Markdown files to check")
    args = parser.parse_args()

    raw_paths = args.paths or list(DEFAULT_PATHS)
    failures: list[str] = []
    for path in existing_paths(Path.cwd(), raw_paths):
        if path.suffix != ".md":
            continue
        original = path.read_text(encoding="utf-8")
        formatted = format_text(original)
        if formatted == original:
            continue
        if args.write:
            path.write_text(formatted, encoding="utf-8")
        else:
            failures.append(path.as_posix())
    if failures:
        for path in failures:
            print(f"{path} has unformatted large numbers; run scripts/format-doc-numbers.py --write", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
