#!/usr/bin/env python3
import argparse
import datetime
import gzip
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
from dataclasses import dataclass
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_TARGETS = (("darwin", "arm64"), ("darwin", "amd64"), ("linux", "arm64"), ("linux", "amd64"))
SUPPORTED_TARGETS = set(DEFAULT_TARGETS)
FORMULA_SUPPORTED_TARGETS = {("darwin", "arm64"), ("linux", "amd64")}


@dataclass(frozen=True)
class Artifact:
    goos: str
    goarch: str
    name: str
    sha256: str
    size_bytes: int


def go_env(name: str) -> str:
    return subprocess.check_output(["go", "env", name], cwd=ROOT, text=True).strip()


def parse_targets(value: str) -> list[tuple[str, str]]:
    if value == "host":
        return [(go_env("GOOS"), go_env("GOARCH"))]
    targets: list[tuple[str, str]] = []
    for item in re.split(r"[\s,]+", value.strip()):
        if not item:
            continue
        if "/" not in item:
            raise SystemExit(f"target must look like goos/goarch: {item}")
        goos, goarch = item.split("/", 1)
        target = (goos, goarch)
        if target not in SUPPORTED_TARGETS:
            supported = ", ".join(f"{os_}/{arch}" for os_, arch in DEFAULT_TARGETS)
            raise SystemExit(f"unsupported target {item}; supported: {supported}")
        if target not in targets:
            targets.append(target)
    if not targets:
        raise SystemExit("--targets selected no targets")
    return targets


def version_without_v(version: str) -> str:
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", version):
        raise SystemExit("--version must look like vX.Y.Z")
    return version[1:]


def run(cmd: list[str], *, env: dict[str, str] | None = None) -> None:
    subprocess.run(cmd, cwd=ROOT, env=env, check=True)


def build_binary(version: str, goos: str, goarch: str, package: str, output: Path) -> None:
    env = os.environ.copy()
    env.update({"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"})
    ldflags = f"-s -w -buildid= -X github.com/ehmo/gomoufox/internal/buildinfo.Version={version}"
    run([
        "go",
        "build",
        "-trimpath",
        "-buildvcs=false",
        "-ldflags",
        ldflags,
        "-o",
        str(output),
        package,
    ], env=env)


def print_build(version: str, goos: str, goarch: str, package: str, output: Path) -> None:
    ldflags = f"-s -w -buildid= -X github.com/ehmo/gomoufox/internal/buildinfo.Version={version}"
    print(
        "GOOS={goos} GOARCH={goarch} CGO_ENABLED=0 go build -trimpath -buildvcs=false "
        "-ldflags {ldflags!r} -o {output} {package}".format(
            goos=goos,
            goarch=goarch,
            ldflags=ldflags,
            output=output,
            package=package,
        )
    )


def add_tar_file(tar: tarfile.TarFile, src: Path, arcname: str, mode: int) -> None:
    info = tar.gettarinfo(str(src), arcname)
    info.mtime = 0
    info.uid = 0
    info.gid = 0
    info.uname = ""
    info.gname = ""
    info.mode = mode
    info.pax_headers = {}
    with src.open("rb") as f:
        tar.addfile(info, f)


def write_archive(version_plain: str, goos: str, goarch: str, stage: Path, out_dir: Path) -> Artifact:
    archive_root = f"gomoufox_{version_plain}_{goos}_{goarch}"
    archive_name = f"{archive_root}.tar.gz"
    archive_path = out_dir / archive_name
    with archive_path.open("wb") as raw:
        with gzip.GzipFile(fileobj=raw, mode="wb", filename="", mtime=0, compresslevel=9) as gz:
            with tarfile.open(fileobj=gz, mode="w") as tar:
                add_tar_file(tar, stage / "gomoufox", f"{archive_root}/gomoufox", 0o755)
                add_tar_file(tar, stage / "gomoufox-realpass", f"{archive_root}/gomoufox-realpass", 0o755)
                add_tar_file(tar, ROOT / "LICENSE", f"{archive_root}/LICENSE", 0o644)
                add_tar_file(tar, ROOT / "README.md", f"{archive_root}/README.md", 0o644)
    digest = sha256_file(archive_path)
    return Artifact(goos=goos, goarch=goarch, name=archive_name, sha256=digest, size_bytes=archive_path.stat().st_size)


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def write_checksum_files(version: str, artifacts: list[Artifact], out_dir: Path) -> None:
    lines = [f"{artifact.sha256}  {artifact.name}\n" for artifact in sorted(artifacts, key=lambda item: item.name)]
    (out_dir / "checksums.txt").write_text("".join(lines), encoding="utf-8")
    payload = {
        "version": version,
        "artifacts": [
            {
                "goos": artifact.goos,
                "goarch": artifact.goarch,
                "name": artifact.name,
                "sha256": artifact.sha256,
                "size_bytes": artifact.size_bytes,
            }
            for artifact in sorted(artifacts, key=lambda item: (item.goos, item.goarch))
        ],
    }
    (out_dir / "checksums.json").write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def command_output(cmd: list[str]) -> str:
    try:
        return subprocess.check_output(cmd, cwd=ROOT, text=True, stderr=subprocess.DEVNULL).strip()
    except (OSError, subprocess.CalledProcessError):
        return ""


def source_metadata() -> dict:
    status = command_output(["git", "status", "--porcelain", "--untracked-files=no"])
    return {
        "repository": "https://github.com/ehmo/gomoufox",
        "commit": command_output(["git", "rev-parse", "HEAD"]),
        "tag": command_output(["git", "describe", "--tags", "--exact-match"]),
        "dirty": bool(status),
    }


def go_modules() -> list[dict[str, str]]:
    raw = command_output(["go", "list", "-m", "-json", "all"])
    modules: list[dict[str, str]] = []
    decoder = json.JSONDecoder()
    index = 0
    while index < len(raw):
        while index < len(raw) and raw[index].isspace():
            index += 1
        if index >= len(raw):
            break
        item, next_index = decoder.raw_decode(raw, index)
        index = next_index
        module = {"path": item.get("Path", ""), "version": item.get("Version", "")}
        if "Replace" in item:
            replacement = item["Replace"]
            module["replace_path"] = replacement.get("Path", "")
            module["replace_version"] = replacement.get("Version", "")
        modules.append(module)
    return modules


def material(path: str) -> dict:
    file_path = ROOT / path
    return {
        "uri": path,
        "sha256": sha256_file(file_path),
        "size_bytes": file_path.stat().st_size,
    }


def write_release_provenance(version: str, artifacts: list[Artifact], out_dir: Path) -> None:
    payload = {
        "schema_version": "gomoufox.release-provenance.v1",
        "version": version,
        "created": datetime.datetime.now(datetime.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
        "source": source_metadata(),
        "builder": {
            "tool": "scripts/package-release.py",
            "go_version": command_output(["go", "env", "GOVERSION"]),
            "python_version": platform_python_version(),
        },
        "build": {
            "trimpath": True,
            "buildvcs": False,
            "cgo_enabled": False,
            "ldflags": "-s -w -buildid= -X github.com/ehmo/gomoufox/internal/buildinfo.Version=<version>",
        },
        "materials": [material("go.mod"), material("go.sum"), material("LICENSE")],
        "modules": go_modules(),
        "artifacts": [
            {
                "name": artifact.name,
                "goos": artifact.goos,
                "goarch": artifact.goarch,
                "sha256": artifact.sha256,
                "size_bytes": artifact.size_bytes,
            }
            for artifact in sorted(artifacts, key=lambda item: item.name)
        ],
    }
    (out_dir / "release-provenance.json").write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def platform_python_version() -> str:
    return ".".join(str(part) for part in sys.version_info[:3])


def spdx_id(value: str) -> str:
    safe = re.sub(r"[^A-Za-z0-9.-]", "-", value)
    return "SPDXRef-" + safe.strip("-")


def write_sbom(version: str, artifacts: list[Artifact], out_dir: Path) -> None:
    created = datetime.datetime.now(datetime.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    packages = [
        {
            "SPDXID": "SPDXRef-Package-gomoufox",
            "name": "github.com/ehmo/gomoufox",
            "versionInfo": version,
            "downloadLocation": "https://github.com/ehmo/gomoufox",
            "filesAnalyzed": False,
            "licenseConcluded": "MIT",
            "licenseDeclared": "MIT",
            "copyrightText": "NOASSERTION",
        }
    ]
    relationships = [
        {"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES", "relatedSpdxElement": "SPDXRef-Package-gomoufox"}
    ]
    for module in go_modules():
        if not module["path"] or module["path"] == "github.com/ehmo/gomoufox":
            continue
        module_id = spdx_id("Module-" + module["path"] + "-" + module.get("version", ""))
        packages.append({
            "SPDXID": module_id,
            "name": module["path"],
            "versionInfo": module.get("version", ""),
            "downloadLocation": "NOASSERTION",
            "filesAnalyzed": False,
            "licenseConcluded": "NOASSERTION",
            "licenseDeclared": "NOASSERTION",
            "copyrightText": "NOASSERTION",
        })
        relationships.append({"spdxElementId": "SPDXRef-Package-gomoufox", "relationshipType": "DEPENDS_ON", "relatedSpdxElement": module_id})
    files = []
    for artifact in sorted(artifacts, key=lambda item: item.name):
        file_id = spdx_id("File-" + artifact.name)
        files.append({
            "SPDXID": file_id,
            "fileName": artifact.name,
            "checksums": [{"algorithm": "SHA256", "checksumValue": artifact.sha256}],
            "licenseConcluded": "NOASSERTION",
            "copyrightText": "NOASSERTION",
        })
        relationships.append({"spdxElementId": "SPDXRef-Package-gomoufox", "relationshipType": "CONTAINS", "relatedSpdxElement": file_id})
    document = {
        "spdxVersion": "SPDX-2.3",
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": f"gomoufox-{version}",
        "documentNamespace": f"https://github.com/ehmo/gomoufox/releases/download/{version}/sbom.spdx.json",
        "creationInfo": {"created": created, "creators": ["Tool: scripts/package-release.py", "Organization: ehmo"]},
        "packages": packages,
        "files": files,
        "relationships": relationships,
    }
    (out_dir / "sbom.spdx.json").write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def smoke_host(version: str, out_dir: Path) -> None:
    checks = (
        (out_dir / "gomoufox", f"gomoufox {version}\n"),
        (out_dir / "gomoufox-realpass", f"gomoufox-realpass {version}\n"),
    )
    for binary, expected in checks:
        got = subprocess.check_output([str(binary), "--version"], text=True)
        if got != expected:
            raise SystemExit(f"{binary.name} --version = {got!r}, expected {expected!r}")
    subprocess.check_call([str(out_dir / "gomoufox-realpass"), "--help"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def copy_host_binaries(stage: Path, out_dir: Path) -> None:
    for name in ("gomoufox", "gomoufox-realpass"):
        dst = out_dir / name
        shutil.copy2(stage / name, dst)
        dst.chmod(0o755)


def build_artifacts(version: str, targets: list[tuple[str, str]], out_dir: Path, smoke: bool) -> list[Artifact]:
    out_dir.mkdir(parents=True, exist_ok=True)
    for child in out_dir.iterdir():
        if child.is_dir():
            shutil.rmtree(child)
        else:
            child.unlink()
    host = (go_env("GOOS"), go_env("GOARCH"))
    artifacts: list[Artifact] = []
    with tempfile.TemporaryDirectory(prefix="gomoufox-package-") as tmp:
        tmp_root = Path(tmp)
        for goos, goarch in targets:
            stage = tmp_root / f"{goos}_{goarch}"
            stage.mkdir(parents=True)
            build_binary(version, goos, goarch, "./cmd/gomoufox", stage / "gomoufox")
            build_binary(version, goos, goarch, "./cmd/gomoufox-realpass", stage / "gomoufox-realpass")
            artifact = write_archive(version_without_v(version), goos, goarch, stage, out_dir)
            artifacts.append(artifact)
            if (goos, goarch) == host:
                copy_host_binaries(stage, out_dir)
    write_checksum_files(version, artifacts, out_dir)
    if smoke and host in targets:
        smoke_host(version, out_dir)
    return artifacts


def formula_target_block(artifact: Artifact, version: str) -> str:
    url = f"https://github.com/ehmo/gomoufox/releases/download/{version}/{artifact.name}"
    return f'      url "{url}"\n      sha256 "{artifact.sha256}"'


def write_formula(path: Path, version: str, artifacts: list[Artifact]) -> str:
    by_target = {(artifact.goos, artifact.goarch): artifact for artifact in artifacts}
    version_plain = version_without_v(version)
    darwin_arm = by_target.get(("darwin", "arm64"))
    linux_amd = by_target.get(("linux", "amd64"))
    lines = [
        "class Gomoufox < Formula",
        '  desc "Go driver, CLI, and MCP server for Camoufox"',
        '  homepage "https://github.com/ehmo/gomoufox"',
        f'  version "{version_plain}"',
        '  license "MIT"',
        "",
    ]
    if darwin_arm or any(goos == "darwin" for goos, _ in by_target):
        lines.append("  on_macos do")
        lines.append("    if Hardware::CPU.arm?")
        if darwin_arm:
            lines.append(formula_target_block(darwin_arm, version))
        else:
            lines.append('      odie "gomoufox does not ship a macOS ARM archive for this release"')
        lines.append("    else")
        lines.append('      odie "gomoufox Homebrew requires Apple Silicon because pinned Camoufox has no supported macOS Intel browser binary"')
        lines.append("    end")
        lines.append("  end")
        lines.append("")
    if linux_amd or any(goos == "linux" for goos, _ in by_target):
        lines.append("  on_linux do")
        lines.append("    if Hardware::CPU.arm?")
        lines.append('      odie "gomoufox Homebrew requires Linux amd64 because pinned Camoufox has no supported Linux ARM browser binary"')
        lines.append("    else")
        if linux_amd:
            lines.append(formula_target_block(linux_amd, version))
        else:
            lines.append('      odie "gomoufox does not ship a Linux amd64 archive for this release"')
        lines.append("    end")
        lines.append("  end")
        lines.append("")
    lines.extend([
        "  def install",
        '    archive_root = Dir["gomoufox_*"].find { |path| File.directory?(path) } || "."',
        '    bin.install "#{archive_root}/gomoufox"',
        '    bin.install "#{archive_root}/gomoufox-realpass"',
        "  end",
        "",
        "  test do",
        '    assert_match "gomoufox v#{version}", shell_output("#{bin}/gomoufox --version")',
        '    assert_match "gomoufox-realpass v#{version}", shell_output("#{bin}/gomoufox-realpass --version")',
        '    assert_match "commands", shell_output("#{bin}/gomoufox help --json --fields commands")',
        '    assert_match "actions", shell_output("#{bin}/gomoufox agents install --target all --scope user --features skills,mcp --toolset core --dry-run --json")',
        "  end",
        "end",
        "",
    ])
    text = "\n".join(lines)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    return text


def verify_formula(path: Path, artifacts: list[Artifact], targets: list[tuple[str, str]]) -> None:
    text = path.read_text(encoding="utf-8")
    for artifact in artifacts:
        target = (artifact.goos, artifact.goarch)
        if target not in targets or target not in FORMULA_SUPPORTED_TARGETS:
            continue
        url = f"https://github.com/ehmo/gomoufox/releases/download/"
        for want in (url, artifact.name, artifact.sha256):
            if want not in text:
                raise SystemExit(f"{path} missing {want} for {artifact.goos}/{artifact.goarch}")
    for goos, goarch in set(targets) - FORMULA_SUPPORTED_TARGETS:
        for artifact in artifacts:
            if (artifact.goos, artifact.goarch) == (goos, goarch) and artifact.name in text:
                raise SystemExit(f"{path} must not install unsupported Homebrew target {goos}/{goarch}")


def main() -> int:
    parser = argparse.ArgumentParser(description="Build deterministic gomoufox release archives and formula metadata.")
    parser.add_argument("--version", required=True, help="release version, vX.Y.Z")
    parser.add_argument("--out", help="output directory; default dist/release/<version>")
    parser.add_argument("--targets", default=",".join(f"{goos}/{goarch}" for goos, goarch in DEFAULT_TARGETS), help='comma-separated goos/goarch targets or "host"')
    parser.add_argument("--formula", help="write a Homebrew formula for the selected artifacts")
    parser.add_argument("--verify-formula", help="verify that a Homebrew formula references selected artifacts")
    parser.add_argument("--verify-formula-targets", default="all", help='formula targets to verify: "all", "host", or comma-separated goos/goarch targets')
    parser.add_argument("--skip-host-smoke", action="store_true", help="skip host binary version smoke checks")
    parser.add_argument("--dry-run", action="store_true", help="print build/archive plan without writing files")
    args = parser.parse_args()

    version_plain = version_without_v(args.version)
    targets = parse_targets(args.targets)
    out_dir = Path(args.out).resolve() if args.out else ROOT / "dist" / "release" / args.version
    formula_path = Path(args.formula).resolve() if args.formula else None
    verify_formula_path = Path(args.verify_formula).resolve() if args.verify_formula else None

    if args.verify_formula_targets == "all":
        verify_targets = targets
    else:
        verify_targets = parse_targets(args.verify_formula_targets)

    if args.dry_run:
        for goos, goarch in targets:
            stage = out_dir / f".build/{goos}_{goarch}"
            print_build(args.version, goos, goarch, "./cmd/gomoufox", stage / "gomoufox")
            print_build(args.version, goos, goarch, "./cmd/gomoufox-realpass", stage / "gomoufox-realpass")
            print(f"archive: {out_dir / f'gomoufox_{version_plain}_{goos}_{goarch}.tar.gz'}")
        print(f"checksums.txt: {out_dir / 'checksums.txt'}")
        print(f"checksums.json: {out_dir / 'checksums.json'}")
        print(f"release-provenance.json: {out_dir / 'release-provenance.json'}")
        print(f"sbom.spdx.json: {out_dir / 'sbom.spdx.json'}")
        if formula_path:
            print(f"Homebrew formula: {formula_path}")
        if verify_formula_path:
            print(f"Verify Homebrew formula: {verify_formula_path}")
        return 0

    artifacts = build_artifacts(args.version, targets, out_dir, smoke=not args.skip_host_smoke)
    write_release_provenance(args.version, artifacts, out_dir)
    write_sbom(args.version, artifacts, out_dir)
    if formula_path:
        write_formula(formula_path, args.version, artifacts)
    if verify_formula_path:
        verify_formula(verify_formula_path, artifacts, verify_targets)
    for artifact in sorted(artifacts, key=lambda item: item.name):
        print(f"{artifact.sha256}  {artifact.name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
