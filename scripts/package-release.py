#!/usr/bin/env python3
import argparse
import gzip
import hashlib
import json
import os
import re
import shutil
import subprocess
import tarfile
import tempfile
from dataclasses import dataclass
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_TARGETS = (("darwin", "arm64"), ("darwin", "amd64"), ("linux", "arm64"), ("linux", "amd64"))
SUPPORTED_TARGETS = set(DEFAULT_TARGETS)


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
    lines = [
        "class Gomoufox < Formula",
        '  desc "Go driver, CLI, and MCP server for Camoufox"',
        '  homepage "https://github.com/ehmo/gomoufox"',
        f'  version "{version_plain}"',
        '  license "MIT"',
        "",
    ]
    for goos, section in (("darwin", "on_macos"), ("linux", "on_linux")):
        available = {arch: by_target[(goos, arch)] for arch in ("arm64", "amd64") if (goos, arch) in by_target}
        if not available:
            continue
        lines.append(f"  {section} do")
        if "arm64" in available:
            lines.append("    if Hardware::CPU.arm?")
            lines.append(formula_target_block(available["arm64"], version))
            if "amd64" in available:
                lines.append("    else")
                lines.append(formula_target_block(available["amd64"], version))
            else:
                lines.append("    else")
                lines.append('      odie "gomoufox does not ship an Intel archive for this release"')
            lines.append("    end")
        else:
            lines.append("    if Hardware::CPU.arm?")
            lines.append('      odie "gomoufox does not ship an ARM archive for this release"')
            lines.append("    else")
            lines.append(formula_target_block(available["amd64"], version))
            lines.append("    end")
        lines.append("  end")
        lines.append("")
    lines.extend([
        "  def install",
        '    bin.install "gomoufox"',
        '    bin.install "gomoufox-realpass"',
        "  end",
        "",
        "  test do",
        '    assert_match "gomoufox v#{version}", shell_output("#{bin}/gomoufox --version")',
        '    assert_match "gomoufox-realpass v#{version}", shell_output("#{bin}/gomoufox-realpass --version")',
        '    assert_match "commands", shell_output("#{bin}/gomoufox help --json --fields commands")',
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
        if (artifact.goos, artifact.goarch) not in targets:
            continue
        url = f"https://github.com/ehmo/gomoufox/releases/download/"
        for want in (url, artifact.name, artifact.sha256):
            if want not in text:
                raise SystemExit(f"{path} missing {want} for {artifact.goos}/{artifact.goarch}")


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
        if formula_path:
            print(f"Homebrew formula: {formula_path}")
        if verify_formula_path:
            print(f"Verify Homebrew formula: {verify_formula_path}")
        return 0

    artifacts = build_artifacts(args.version, targets, out_dir, smoke=not args.skip_host_smoke)
    if formula_path:
        write_formula(formula_path, args.version, artifacts)
    if verify_formula_path:
        verify_formula(verify_formula_path, artifacts, verify_targets)
    for artifact in sorted(artifacts, key=lambda item: item.name):
        print(f"{artifact.sha256}  {artifact.name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
