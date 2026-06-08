#!/usr/bin/env python3
import argparse
import json
import re
import sys
from pathlib import Path


def s(*parts: str) -> str:
    return "".join(parts)


FORBIDDEN_PARTS = {
    s(".", "beads"),
    s(".", "dolt"),
    s("AGENTS", ".md"),
    s("CLAUDE", ".md"),
    s("RESEARCH", "-NOTES.md"),
    "api.md",
    "cli-mcp.md",
    "spec.md",
    s("team", "reports"),
    "dist",
    "public",
    s("realpass", "-gate.sh"),
    s("release", "-gate.sh"),
    s("release", "-local.sh"),
    s("release", "-public-gomoufox.sh"),
}

FORBIDDEN_TEXT = {
    s("github.com/nan/", "gomoufox"),
    s("RESEARCH", "-NOTES"),
    s("AGENTS", ".md"),
    s("CLAUDE", ".md"),
    s("team", "reports"),
    s("bd", " "),
    s("Be", "ads"),
    s("private ", "repo"),
    s("private ", "source"),
    s("scripts/", "realpass-gate.sh"),
    s("scripts/", "release-gate.sh"),
    s("scripts/", "release-local.sh"),
    s("git@github.com:ehmo/", "go", "mf"),
    s("github.com/ehmo/", "go", "mf"),
    s("ehmo/", "go", "mf.git"),
}

TEXT_SUFFIXES = {".go", ".js", ".json", ".md", ".mod", ".sum", ".sh", ".py", ".rb", ".yml", ".yaml", ".txt"}
TEXT_FILE_NAMES = {".gitignore", "LICENSE"}
ALLOWED_DOT_PARTS = {".github", ".gitignore"}
SOURCE_DIRS = {"camoufoxcfg", "cmd", "internal"}
BENCHMARK_LINK_PATTERN = re.compile(r"\]\((docs/benchmarks/[^)\s]+\.json)\)")

macos_user_pattern = s(r"(?<![A-Za-z0-9])/(?:", "Users", r"|Volumes)/[^\s\"'`<>)\]]+")
linux_home_pattern = s(r"(?<![A-Za-z0-9])/(?:", "home", r"|root)/[^\s\"'`<>)\]]+")
macos_private_temp_pattern = s(r"(?<![A-Za-z0-9])/", "private", "/", "var", r"/[^\s\"'`<>)\]]+")
windows_user_pattern = s(r"(?i)\b[A-Z]:\\", "Users", r"\\[^\s\"'`<>)\]]+")
worktree_pattern = s(r"(?<![A-Za-z0-9])(?:~|/)[^\s\"'`<>)\]]*", "Work", "/", "ai", r"/[^\s\"'`<>)\]]+")

LOCAL_PATH_PATTERNS = (
    ("local macOS user path", re.compile(macos_user_pattern)),
    ("local Linux home path", re.compile(linux_home_pattern)),
    ("local macOS private temp path", re.compile(macos_private_temp_pattern)),
    ("local Windows user path", re.compile(windows_user_pattern)),
    ("local worktree path", re.compile(worktree_pattern)),
)


def load_manifest(root: Path) -> dict:
    for path in (root / "scripts" / "public-release-manifest.json", Path(__file__).with_name("public-release-manifest.json")):
        if path.exists():
            return json.loads(path.read_text(encoding="utf-8"))
    raise FileNotFoundError("scripts/public-release-manifest.json")


def manifest_required_files(manifest: dict) -> set[str]:
    required = set(manifest["root_files"])
    required.update(manifest["docs"])
    required.update(manifest.get("formula_files", []))
    required.update(manifest.get("skill_files", []))
    required.update(manifest["github_files"])
    required.update(manifest["generated_files"])
    required.update(manifest["required_script_assets"])
    required.update(f"scripts/{name}" for name in manifest["scripts"])
    return required


def should_scan_text(path: Path) -> bool:
    return path.suffix in TEXT_SUFFIXES or path.name in TEXT_FILE_NAMES


def should_scan_forbidden_text(path: Path) -> bool:
    rel = path.as_posix()
    return rel not in {
        "internal/sidecar/requirements/camoufox.txt",
        "internal/sidecar/requirements/pip.txt",
    }


def rel_files(root: Path) -> list[Path]:
    files = []
    for path in root.rglob("*"):
        if ".git" in path.parts:
            continue
        if path.is_file():
            files.append(path.relative_to(root))
    return sorted(files)


def has_forbidden_part(path: Path) -> str | None:
    for part in path.parts:
        if part in FORBIDDEN_PARTS:
            return part
        if part.startswith(".") and part not in ALLOWED_DOT_PARTS:
            return part
    return None


def allowed_public_path(path: Path, required_files: set[str], required_script_assets: set[str], source_dirs: set[str]) -> bool:
    rel = path.as_posix()
    if rel in required_files:
        return True
    if path.suffix == ".js":
        return rel in required_script_assets
    if path.suffix == ".go":
        return len(path.parts) == 1 or (path.parts[0] in SOURCE_DIRS and path.parts[0] in source_dirs)
    return False


def is_absolute_filesystem_value(value: str) -> bool:
    if not value or "://" in value or value.startswith("//"):
        return False
    if value.startswith("/") and len(value) > 1:
        return True
    return bool(re.match(r"(?i)^[A-Z]:[\\/]", value))


def walk_json_strings(value, path: str = "$"):
    if isinstance(value, str):
        yield path, value
    elif isinstance(value, list):
        for index, item in enumerate(value):
            yield from walk_json_strings(item, f"{path}[{index}]")
    elif isinstance(value, dict):
        for key, item in value.items():
            yield from walk_json_strings(item, f"{path}.{key}")


def leak_failures(rel: Path, text: str) -> list[str]:
    failures: list[str] = []
    rel_posix = rel.as_posix()
    for label, pattern in LOCAL_PATH_PATTERNS:
        for match in pattern.finditer(text):
            failures.append(f"{label} {match.group(0)!r} in {rel_posix}")
    if rel.suffix == ".json":
        try:
            parsed = json.loads(text)
        except json.JSONDecodeError as err:
            failures.append(f"invalid public JSON in {rel_posix}: {err}")
            return failures
        for json_path, value in walk_json_strings(parsed):
            for label, pattern in LOCAL_PATH_PATTERNS:
                if pattern.search(value):
                    failures.append(f"{label} {value!r} in {rel_posix} at {json_path}")
            if is_absolute_filesystem_value(value):
                failures.append(f"absolute filesystem path {value!r} in {rel_posix} at {json_path}")
    return failures


def benchmark_link_failures(rel: Path, text: str, file_set: set[str], manifest_benchmarks: set[str]) -> list[str]:
    failures: list[str] = []
    if rel.suffix != ".md":
        return failures
    rel_posix = rel.as_posix()
    for target in sorted(set(BENCHMARK_LINK_PATTERN.findall(text))):
        if target not in file_set:
            failures.append(f"missing benchmark link target {target} in {rel_posix}")
        if target not in manifest_benchmarks:
            failures.append(f"benchmark link target not in public manifest: {target} in {rel_posix}")
    return failures


def check(root: Path) -> list[str]:
    failures: list[str] = []
    try:
        manifest = load_manifest(root)
    except (OSError, json.JSONDecodeError) as err:
        return [f"invalid public release manifest: {err}"]
    required_files = manifest_required_files(manifest)
    manifest_benchmarks = {
        path
        for path in manifest.get("docs", [])
        if path.startswith("docs/benchmarks/") and path.endswith(".json")
    }
    required_script_assets = set(manifest["required_script_assets"])
    source_dirs = set(manifest["source_dirs"])
    extra_source_dirs = source_dirs - SOURCE_DIRS
    if extra_source_dirs:
        failures.append(f"public manifest source_dirs widen checker allowlist: {', '.join(sorted(extra_source_dirs))}")
    files = rel_files(root)
    file_set = {path.as_posix() for path in files}
    for required in sorted(required_files):
        if required not in file_set:
            failures.append(f"missing required public file: {required}")
    for rel in files:
        forbidden = has_forbidden_part(rel)
        if forbidden:
            failures.append(f"forbidden public path component {forbidden}: {rel.as_posix()}")
            continue
        if not allowed_public_path(rel, required_files, required_script_assets, source_dirs):
            failures.append(f"unexpected public file: {rel.as_posix()}")
            continue
        if rel.suffix and rel.suffix not in TEXT_SUFFIXES:
            failures.append(f"unexpected public file suffix {rel.suffix}: {rel.as_posix()}")
            continue
        if not rel.suffix and rel.name not in TEXT_FILE_NAMES:
            failures.append(f"unexpected public file without text allowlist entry: {rel.as_posix()}")
            continue
        if not should_scan_text(rel):
            failures.append(f"unexpected public file type: {rel.as_posix()}")
            continue
        text_path = root / rel
        try:
            text = text_path.read_text(encoding="utf-8")
        except UnicodeDecodeError as err:
            failures.append(f"invalid UTF-8 public text in {rel.as_posix()}: {err}")
            continue
        if should_scan_forbidden_text(rel):
            for needle in sorted(FORBIDDEN_TEXT):
                if needle in text:
                    failures.append(f"forbidden text {needle!r} in {rel.as_posix()}")
        failures.extend(leak_failures(rel, text))
        if "\u2014" in text:
            failures.append(f"em dash found in public text: {rel.as_posix()}")
        failures.extend(benchmark_link_failures(rel, text, file_set, manifest_benchmarks))
    return failures


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("root", nargs="?", default=".", help="public repo root")
    args = parser.parse_args()
    root = Path(args.root).resolve()
    failures = check(root)
    if failures:
        for failure in failures:
            print(failure, file=sys.stderr)
        return 1
    print(f"public release contract ok: {root}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
