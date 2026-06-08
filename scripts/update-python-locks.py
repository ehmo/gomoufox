#!/usr/bin/env python3
import argparse
import difflib
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
REQ_DIR = ROOT / "internal" / "sidecar" / "requirements"
LOCKS = {
    "camoufox": REQ_DIR / "camoufox.txt",
    "pip": REQ_DIR / "pip.txt",
}
INPUTS = {
    "camoufox": REQ_DIR / "camoufox-input.txt",
    "pip": REQ_DIR / "pip-input.txt",
}
UV_VERSION = "0.11.19"


def run(cmd: list[str], quiet: bool = False) -> None:
    kwargs = {}
    if quiet:
        kwargs["stdout"] = subprocess.PIPE
    subprocess.run(cmd, cwd=ROOT, check=True, **kwargs)


def validate_uv_version() -> None:
    result = subprocess.run(
        ["uv", "--version"],
        cwd=ROOT,
        check=True,
        stdout=subprocess.PIPE,
        text=True,
    )
    found = result.stdout.strip()
    fields = found.split()
    version = fields[1] if len(fields) >= 2 and fields[0] == "uv" else ""
    if version != UV_VERSION:
        raise SystemExit(
            f"uv {UV_VERSION} is required; found {found}. "
            f"Install with: python3 -m pip install --user uv=={UV_VERSION}"
        )


def compile_lock(name: str, out: Path) -> None:
    run(
        [
            "uv",
            "-q",
            "pip",
            "compile",
            "--universal",
            "--generate-hashes",
            "--upgrade",
            "--no-header",
            "--custom-compile-command",
            "python3 scripts/update-python-locks.py",
            "-o",
            str(out),
            str(INPUTS[name]),
        ],
        quiet=True,
    )


def validate_lock(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    if "--hash=sha256:" not in text:
        raise SystemExit(f"{path} has no sha256 hashes")
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or line.startswith("--hash="):
            continue
        if "==" in line and "--hash=sha256:" not in text:
            raise SystemExit(f"{path} contains an unhashed requirement: {line}")


def validate_with_pip() -> None:
    with tempfile.TemporaryDirectory(prefix="gomoufox-python-locks-") as tmp:
        venv = Path(tmp) / "venv"
        run([sys.executable, "-m", "venv", str(venv)])
        python = venv / ("Scripts/python.exe" if sys.platform == "win32" else "bin/python")
        pip = venv / ("Scripts/pip.exe" if sys.platform == "win32" else "bin/pip")
        run(
            [
                str(python),
                "-m",
                "pip",
                "install",
                "-q",
                "--disable-pip-version-check",
                "--upgrade",
                "--require-hashes",
                "--only-binary=:all:",
                "-r",
                str(LOCKS["pip"]),
            ]
        )
        run(
            [
                str(pip),
                "install",
                "-q",
                "--dry-run",
                "--ignore-installed",
                "--disable-pip-version-check",
                "--require-hashes",
                "--only-binary=:all:",
                "-r",
                str(LOCKS["camoufox"]),
            ]
        )


def check_clean(generated: dict[str, str]) -> None:
    failed = False
    for name, new_text in generated.items():
        path = LOCKS[name]
        old_text = path.read_text(encoding="utf-8")
        if old_text == new_text:
            continue
        failed = True
        diff = difflib.unified_diff(
            old_text.splitlines(True),
            new_text.splitlines(True),
            fromfile=str(path),
            tofile=f"{path} (generated)",
        )
        sys.stderr.writelines(diff)
    if failed:
        raise SystemExit("Python requirement locks are stale; run scripts/update-python-locks.py")


def main() -> int:
    parser = argparse.ArgumentParser(description="Regenerate hash-locked Python requirement files.")
    parser.add_argument("--check", action="store_true", help="verify generated locks match the repo")
    parser.add_argument("--skip-pip-validate", action="store_true", help="skip pip dry-run validation")
    args = parser.parse_args()

    if shutil.which("uv") is None:
        raise SystemExit("uv is required: https://docs.astral.sh/uv/")
    validate_uv_version()

    if args.check:
        with tempfile.TemporaryDirectory(prefix="gomoufox-python-lock-check-") as tmp:
            generated: dict[str, str] = {}
            for name in LOCKS:
                out = Path(tmp) / f"{name}.txt"
                compile_lock(name, out)
                validate_lock(out)
                generated[name] = out.read_text(encoding="utf-8")
            check_clean(generated)
    else:
        for name, path in LOCKS.items():
            compile_lock(name, path)
            validate_lock(path)

    if not args.skip_pip_validate:
        validate_with_pip()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
