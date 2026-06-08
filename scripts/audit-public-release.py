#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
from pathlib import Path
from pathlib import PurePosixPath
import re


ARCHIVE_PREFIX = "gomoufox_"
REQUIRED_ASSETS = {"checksums.txt", "checksums.json", "gomoufox.rb", "release-provenance.json", "sbom.spdx.json"}
SUPPORTED_HOSTS = {
    ("Darwin", "arm64"): ("darwin", "arm64"),
    ("Darwin", "x86_64"): ("darwin", "amd64"),
    ("Linux", "x86_64"): ("linux", "amd64"),
    ("Linux", "aarch64"): ("linux", "arm64"),
    ("Linux", "arm64"): ("linux", "arm64"),
}
FORMULA_SUPPORTED_TARGETS = {("darwin", "arm64"), ("linux", "amd64")}


def run(cmd: list[str], *, cwd: Path | None = None, env: dict[str, str] | None = None, input_text: str | None = None, timeout: int = 60) -> subprocess.CompletedProcess:
    return subprocess.run(
        cmd,
        cwd=cwd,
        env=env,
        input=input_text,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=timeout,
        check=False,
    )


def require_ok(result: subprocess.CompletedProcess, label: str) -> subprocess.CompletedProcess:
    if result.returncode != 0:
        raise RuntimeError(f"{label} failed with exit {result.returncode}\nstdout={result.stdout}\nstderr={result.stderr}")
    return result


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def host_target(override: str) -> tuple[str, str]:
    if override:
        parts = override.split("/", 1)
        if len(parts) != 2:
            raise SystemExit("--host must look like goos/goarch")
        return parts[0], parts[1]
    key = (platform.system(), platform.machine())
    if key not in SUPPORTED_HOSTS:
        raise SystemExit(f"unsupported audit host: {key[0]} {key[1]}")
    return SUPPORTED_HOSTS[key]


def version_plain(version: str) -> str:
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", version):
        raise SystemExit("--version must look like vX.Y.Z")
    return version[1:]


def download_assets(repo: str, version: str, dest: Path) -> None:
    dest.mkdir(parents=True, exist_ok=True)
    cmd = [
        "gh",
        "release",
        "download",
        version,
        "--repo",
        repo,
        "--dir",
        str(dest),
        "--pattern",
        "gomoufox_*",
        "--pattern",
        "checksums.*",
        "--pattern",
        "gomoufox.rb",
        "--pattern",
        "release-provenance.json",
        "--pattern",
        "sbom.spdx.json",
    ]
    require_ok(run(cmd, timeout=180), "download release assets")


def parse_checksums(path: Path) -> dict[str, str]:
    checksums: dict[str, str] = {}
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        parts = line.split()
        if len(parts) != 2:
            raise RuntimeError(f"malformed checksum line: {line}")
        digest, name = parts
        if len(digest) != 64 or any(ch not in "0123456789abcdef" for ch in digest):
            raise RuntimeError(f"malformed sha256 digest for {name}: {digest}")
        checksums[name.strip()] = digest
    return checksums


def verify_assets(version: str, asset_dir: Path) -> dict:
    files = {path.name for path in asset_dir.iterdir() if path.is_file()}
    missing = sorted(REQUIRED_ASSETS - files)
    if missing:
        raise RuntimeError(f"missing release assets: {', '.join(missing)}")
    checksums = parse_checksums(asset_dir / "checksums.txt")
    checksum_json = json.loads((asset_dir / "checksums.json").read_text(encoding="utf-8"))
    if checksum_json.get("version") != version:
        raise RuntimeError(f"checksums.json version {checksum_json.get('version')} does not match {version}")
    json_artifacts = {item.get("name"): item for item in checksum_json.get("artifacts", [])}
    verified: list[dict] = []
    for name, expected in sorted(checksums.items()):
        path = asset_dir / name
        if not path.exists():
            raise RuntimeError(f"checksums.txt references missing asset {name}")
        actual = sha256_file(path)
        if actual != expected:
            raise RuntimeError(f"{name} sha256 {actual} does not match {expected}")
        metadata = json_artifacts.get(name)
        if metadata is None:
            raise RuntimeError(f"checksums.json missing artifact entry for {name}")
        if metadata.get("sha256") != actual:
            raise RuntimeError(f"checksums.json sha256 for {name} does not match checksums.txt")
        if metadata.get("size_bytes") != path.stat().st_size:
            raise RuntimeError(f"checksums.json size_bytes for {name} does not match file size")
        verified.append({"name": name, "sha256": actual, "size_bytes": path.stat().st_size})
    expected_archives = {
        f"gomoufox_{version_plain(version)}_darwin_amd64.tar.gz",
        f"gomoufox_{version_plain(version)}_darwin_arm64.tar.gz",
        f"gomoufox_{version_plain(version)}_linux_amd64.tar.gz",
        f"gomoufox_{version_plain(version)}_linux_arm64.tar.gz",
    }
    allowed_assets = REQUIRED_ASSETS | expected_archives
    unexpected_assets = files - allowed_assets
    if unexpected_assets:
        raise RuntimeError(f"unexpected release assets: {', '.join(sorted(unexpected_assets))}")
    missing_archives = expected_archives - set(checksums)
    if missing_archives:
        raise RuntimeError(f"missing archive checksums: {', '.join(sorted(missing_archives))}")
    if set(json_artifacts) != set(checksums):
        raise RuntimeError("checksums.json artifact names do not match checksums.txt")
    unexpected_archives = {name for name in files if name.startswith(ARCHIVE_PREFIX) and name.endswith(".tar.gz")} - expected_archives
    if unexpected_archives:
        raise RuntimeError(f"unexpected release archives: {', '.join(sorted(unexpected_archives))}")
    return {"verified": verified, "checksums": checksums}


def verify_attestations(repo: str, version: str, asset_dir: Path, checksums: dict[str, str]) -> dict:
    workflow = f"github.com/{repo}/.github/workflows/release.yml"
    verified: list[dict] = []
    subjects = set(checksums) | REQUIRED_ASSETS
    for name in sorted(subjects):
        subject = asset_dir / name
        require_ok(run([
            "gh",
            "attestation",
            "verify",
            str(subject),
            "-R",
            repo,
            "--source-ref",
            f"refs/tags/{version}",
            "--signer-workflow",
            workflow,
        ], timeout=120), f"attestation verify {name}")
        item = {"name": name, "provenance": True}
        if name.endswith(".tar.gz"):
            require_ok(run([
                "gh",
                "attestation",
                "verify",
                str(subject),
                "-R",
                repo,
                "--source-ref",
                f"refs/tags/{version}",
                "--signer-workflow",
                workflow,
                "--predicate-type",
                "https://spdx.dev/Document/v2.3",
            ], timeout=120), f"SBOM attestation verify {name}")
            item["sbom"] = True
        verified.append(item)
    return {"verified": verified}


def validate_tar_member(member: tarfile.TarInfo) -> None:
    path = PurePosixPath(member.name)
    if path.is_absolute() or ".." in path.parts:
        raise RuntimeError(f"unsafe archive member path: {member.name}")
    if member.issym() or member.islnk() or member.isdev():
        raise RuntimeError(f"unsafe archive member type: {member.name}")


def inspect_archive(version: str, asset_dir: Path, goos: str, goarch: str, extract_dir: Path) -> tuple[Path, dict]:
    archive_name = f"gomoufox_{version_plain(version)}_{goos}_{goarch}.tar.gz"
    archive = asset_dir / archive_name
    if not archive.exists():
        raise RuntimeError(f"host archive not found: {archive_name}")
    with tarfile.open(archive, "r:gz") as tar:
        tar_members = tar.getmembers()
        for member in tar_members:
            validate_tar_member(member)
        members = sorted(member.name for member in tar_members)
        required = {
            f"gomoufox_{version_plain(version)}_{goos}_{goarch}/gomoufox",
            f"gomoufox_{version_plain(version)}_{goos}_{goarch}/gomoufox-realpass",
            f"gomoufox_{version_plain(version)}_{goos}_{goarch}/LICENSE",
            f"gomoufox_{version_plain(version)}_{goos}_{goarch}/README.md",
        }
        missing = sorted(required - set(members))
        if missing:
            raise RuntimeError(f"{archive_name} missing members: {', '.join(missing)}")
        for member in tar_members:
            tar.extract(member, extract_dir)
    root = extract_dir / f"gomoufox_{version_plain(version)}_{goos}_{goarch}"
    return root, {"archive": archive_name, "members": members}


def json_command(binary: Path, args: list[str], label: str) -> dict:
    result = require_ok(run([str(binary), *args], timeout=30), label)
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as err:
        raise RuntimeError(f"{label} wrote invalid JSON: {err}\n{result.stdout}") from err


def audit_binaries(version: str, root: Path, browser_smoke_url: str) -> dict:
    gomoufox = root / "gomoufox"
    realpass = root / "gomoufox-realpass"
    binary_report = {}
    for path, expected in ((gomoufox, f"gomoufox {version}\n"), (realpass, f"gomoufox-realpass {version}\n")):
        result = require_ok(run([str(path), "--version"], timeout=30), f"{path.name} --version")
        if result.stdout != expected:
            raise RuntimeError(f"{path.name} --version = {result.stdout!r}, expected {expected!r}")
        binary_report[path.name] = {"version": result.stdout.strip(), "size_bytes": path.stat().st_size}
    help_json = json_command(gomoufox, ["help", "--json", "--fields", "commands"], "gomoufox help")
    mcp_help = json_command(gomoufox, ["help", "mcp", "--json"], "gomoufox help mcp")
    skills = json_command(gomoufox, ["skills", "list", "--json"], "gomoufox skills list")
    if [item.get("name") for item in skills.get("skills", [])] != ["core", "mcp"]:
        raise RuntimeError(f"unexpected skills list: {skills}")
    skill_dir = root.parent / "skills-install"
    install = json_command(gomoufox, ["skills", "install", "--target", "codex", "--dir", str(skill_dir), "--dry-run", "--json"], "gomoufox skills install")
    browser = None
    if browser_smoke_url:
        started = time.time()
        result = require_ok(run([str(gomoufox), "--json", "get", browser_smoke_url, "--text", "--max-bytes", "4096"], timeout=120), "gomoufox browser smoke")
        browser_payload = json.loads(result.stdout)
        browser = {
            "url": browser_smoke_url,
            "title": browser_payload.get("title", ""),
            "bytes": browser_payload.get("bytes", 0),
            "elapsed_ms": int((time.time() - started) * 1000),
        }
    return {
        "binaries": binary_report,
        "help_commands": len(help_json.get("commands", [])),
        "mcp_tools": len(mcp_help.get("mcp_tools", [])),
        "skills": skills["skills"],
        "skills_install_dry_run": install,
        "browser_smoke": browser,
    }


def audit_mcp(binary: Path) -> dict:
    request_lines = [
        {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "gomoufox-audit", "version": "0"}}},
        {"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}},
        {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
        {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "skills_list", "arguments": {}}},
    ]
    stdin = "".join(json.dumps(item, separators=(",", ":")) + "\n" for item in request_lines)
    proc = require_ok(run([str(binary), "mcp", "--toolset", "core", "--session-dir", str(binary.parent / "mcp-sessions")], input_text=stdin, timeout=15), "gomoufox mcp stdio")
    responses = [json.loads(line) for line in proc.stdout.splitlines() if line.strip()]
    by_id = {item.get("id"): item for item in responses if "id" in item}
    tools = by_id[2]["result"]["tools"]
    names = [tool["name"] for tool in tools]
    for required in ("browser_navigate", "browser_snapshot", "browser_get_content", "skills_list", "skills_get"):
        if required not in names:
            raise RuntimeError(f"MCP tools/list missing {required}")
    return {"tool_count": len(tools), "tools_json_bytes": len(json.dumps(tools, separators=(",", ":")))}


def audit_formula(asset_dir: Path, version: str, checksums: dict[str, str]) -> dict:
    text = (asset_dir / "gomoufox.rb").read_text(encoding="utf-8")
    plain = version_plain(version)
    required = [
        f'version "{plain}"',
        f"releases/download/{version}/gomoufox_{plain}_darwin_arm64.tar.gz",
        f"releases/download/{version}/gomoufox_{plain}_linux_amd64.tar.gz",
        "no supported macOS Intel browser binary",
        "no supported Linux ARM browser binary",
    ]
    missing = [item for item in required if item not in text]
    if missing:
        raise RuntimeError(f"formula missing release references: {', '.join(missing)}")
    for name, digest in sorted(checksums.items()):
        supported = any(name.endswith(f"_{goos}_{goarch}.tar.gz") for goos, goarch in FORMULA_SUPPORTED_TARGETS)
        if name.endswith(".tar.gz") and supported and digest not in text:
            raise RuntimeError(f"formula missing sha256 for {name}: {digest}")
        if name.endswith(".tar.gz") and not supported and name in text:
            raise RuntimeError(f"formula references unsupported Homebrew archive {name}")
    return {"version": plain, "bytes": len(text.encode("utf-8"))}


def brew_env() -> dict[str, str]:
    env = os.environ.copy()
    env["HOMEBREW_NO_AUTO_UPDATE"] = "1"
    env["HOMEBREW_REQUIRE_TAP_TRUST"] = "1"
    return env


def brew_command_available(name: str) -> bool:
    result = run(["brew", "commands"], env=brew_env(), timeout=30)
    if result.returncode != 0:
        return False
    return name in result.stdout.splitlines()


def audit_brew(repo: str, version: str, mode: str) -> dict:
    if mode == "off":
        return {"mode": mode, "status": "skipped"}
    if shutil.which("brew") is None:
        if mode == "install":
            raise RuntimeError("brew not found")
        return {"mode": mode, "status": "missing-brew"}
    if mode == "inspect":
        return {"mode": mode, "status": "available", "brew_version": require_ok(run(["brew", "--version"], timeout=30), "brew --version").stdout.splitlines()[0]}
    if run(["brew", "list", "--versions", "gomoufox"], env=brew_env(), timeout=30).stdout.strip():
        raise RuntimeError("refusing brew install audit because gomoufox is already installed")
    if "ehmo/gomoufox" in run(["brew", "tap"], env=brew_env(), timeout=30).stdout.splitlines():
        raise RuntimeError("refusing brew install audit because ehmo/gomoufox is already tapped")
    trust_supported = brew_command_available("trust")
    try:
        require_ok(run(["brew", "tap", "ehmo/gomoufox", f"https://github.com/{repo}"], env=brew_env(), timeout=120), "brew tap")
        trust_status = "unsupported"
        if trust_supported:
            require_ok(run(["brew", "trust", "--formula", "ehmo/gomoufox/gomoufox"], env=brew_env(), timeout=60), "brew trust")
            trust_status = "trusted"
        require_ok(run(["brew", "install", "gomoufox"], env=brew_env(), timeout=180), "brew install")
        require_ok(run(["brew", "test", "gomoufox"], env=brew_env(), timeout=120), "brew test")
        version_out = require_ok(run(["gomoufox", "--version"], env=brew_env(), timeout=30), "installed gomoufox version").stdout.strip()
        if version_out != f"gomoufox {version}":
            raise RuntimeError(f"installed gomoufox version {version_out!r} does not match {version}")
        return {"mode": mode, "status": "passed", "trust": trust_status, "version": version_out}
    finally:
        run(["brew", "uninstall", "--formula", "gomoufox"], env=brew_env(), timeout=120)
        if trust_supported:
            run(["brew", "untrust", "--formula", "ehmo/gomoufox/gomoufox"], env=brew_env(), timeout=60)
        run(["brew", "untap", "ehmo/gomoufox"], env=brew_env(), timeout=60)


def main() -> int:
    parser = argparse.ArgumentParser(description="Audit a published gomoufox release from the public user path.")
    parser.add_argument("--version", required=True, help="release version, vX.Y.Z")
    parser.add_argument("--repo", default="ehmo/gomoufox")
    parser.add_argument("--assets-dir", help="use an existing directory of downloaded release assets")
    parser.add_argument("--work-dir", help="working directory; default is a temporary directory")
    parser.add_argument("--host", default="", help="override host target as goos/goarch")
    parser.add_argument("--browser-smoke-url", default="", help="optional URL for a real browser CLI smoke")
    parser.add_argument("--brew-mode", choices=("off", "inspect", "install"), default="inspect")
    parser.add_argument("--verify-attestations", action="store_true", help="verify GitHub artifact attestations for release assets")
    parser.add_argument("--json-out", help="write audit report JSON to this path")
    parser.add_argument("--keep-work-dir", action="store_true")
    args = parser.parse_args()

    work_parent = Path(args.work_dir).resolve() if args.work_dir else Path(tempfile.mkdtemp(prefix="gomoufox-public-audit-"))
    work_parent.mkdir(parents=True, exist_ok=True)
    cleanup = args.work_dir is None and not args.keep_work_dir
    report = {"version": args.version, "repo": args.repo, "work_dir": str(work_parent), "ok": False}
    try:
        asset_dir = Path(args.assets_dir).resolve() if args.assets_dir else work_parent / "assets"
        if args.assets_dir is None:
            download_assets(args.repo, args.version, asset_dir)
        goos, goarch = host_target(args.host)
        extract_dir = work_parent / "extract"
        report["assets"] = verify_assets(args.version, asset_dir)
        if args.verify_attestations:
            report["attestations"] = verify_attestations(args.repo, args.version, asset_dir, report["assets"]["checksums"])
        binary_root, report["archive"] = inspect_archive(args.version, asset_dir, goos, goarch, extract_dir)
        report["formula"] = audit_formula(asset_dir, args.version, report["assets"]["checksums"])
        report["binary"] = audit_binaries(args.version, binary_root, args.browser_smoke_url)
        report["mcp"] = audit_mcp(binary_root / "gomoufox")
        report["brew"] = audit_brew(args.repo, args.version, args.brew_mode)
        report["ok"] = True
        if args.json_out:
            Path(args.json_out).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0
    except Exception as err:
        report["error"] = str(err)
        if args.json_out:
            Path(args.json_out).write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(report, indent=2, sort_keys=True), file=sys.stderr)
        return 1
    finally:
        if cleanup:
            shutil.rmtree(work_parent, ignore_errors=True)


if __name__ == "__main__":
    raise SystemExit(main())
