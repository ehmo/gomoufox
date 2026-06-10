#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/public-consumer-canary.sh --version vX.Y.Z|latest [flags]

Validate a public gomoufox release from a clean consumer path.

flags:
  --repo owner/name             GitHub repo. Default: ehmo/gomoufox
  --assets-dir <dir>            Use already downloaded release assets.
  --work-dir <dir>              Scratch directory. Default: temporary directory.
  --host goos/goarch            Host archive to test. Default: detected host.
  --verify-attestations         Verify release attestations with gh.
  --go-install                  Also test go install github.com/<repo>/cmd/gomoufox@<version>.
  --brew-mode off|inspect|install
                                Formula check mode. Default: off.
  --runtime-smoke              Also run install/doctor under the no-Python canary.
  --browser-smoke-url <url>     Optional gomoufox get smoke URL.
  --dry-run                     Print commands without executing network or file work.
  --help                        Show this help.
EOF
}

repo="ehmo/gomoufox"
version=""
assets_dir=""
work_dir=""
host=""
verify_attestations="false"
go_install="false"
brew_mode="off"
runtime_smoke="false"
browser_smoke_url=""
dry_run="false"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) repo="${2:?missing value for --repo}"; shift 2 ;;
    --version) version="${2:?missing value for --version}"; shift 2 ;;
    --assets-dir) assets_dir="${2:?missing value for --assets-dir}"; shift 2 ;;
    --work-dir) work_dir="${2:?missing value for --work-dir}"; shift 2 ;;
    --host) host="${2:?missing value for --host}"; shift 2 ;;
    --verify-attestations) verify_attestations="true"; shift ;;
    --go-install) go_install="true"; shift ;;
    --brew-mode) brew_mode="${2:?missing value for --brew-mode}"; shift 2 ;;
    --runtime-smoke) runtime_smoke="true"; shift ;;
    --browser-smoke-url) browser_smoke_url="${2:?missing value for --browser-smoke-url}"; shift 2 ;;
    --dry-run) dry_run="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if ! [[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "--repo must look like owner/name" >&2
  exit 2
fi
if [ -z "$version" ]; then
  echo "--version is required" >&2
  exit 2
fi
case "$brew_mode" in
  off|inspect|install) ;;
  *) echo "--brew-mode must be off, inspect, or install" >&2; exit 2 ;;
esac

run() {
  printf '+'
  printf ' %q' "$@"
  printf '\n'
  if [ "$dry_run" != "true" ]; then
    "$@"
  fi
}

run_shell() {
  printf '+ %s\n' "$1"
  if [ "$dry_run" != "true" ]; then
    bash -c "$1"
  fi
}

detect_host() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"
  case "$os/$arch" in
    Darwin/arm64) printf '%s\n' darwin/arm64 ;;
    Darwin/x86_64) printf '%s\n' darwin/amd64 ;;
    Linux/x86_64) printf '%s\n' linux/amd64 ;;
    Linux/aarch64|Linux/arm64) printf '%s\n' linux/arm64 ;;
    *) echo "unsupported host: $os $arch" >&2; exit 2 ;;
  esac
}

version_plain() {
  local value="$1"
  if ! [[ "$value" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "--version must look like vX.Y.Z or latest" >&2
    exit 2
  fi
  printf '%s\n' "${value#v}"
}

require_file() {
  local path="$1"
  if [ ! -f "$path" ]; then
    echo "missing release asset: $path" >&2
    exit 1
  fi
}

verify_checksums() {
  local dir="$1"
  if command -v shasum >/dev/null 2>&1; then
    run_shell "cd $(printf '%q' "$dir") && shasum -a 256 -c checksums.txt"
  elif command -v sha256sum >/dev/null 2>&1; then
    run_shell "cd $(printf '%q' "$dir") && sha256sum -c checksums.txt"
  else
    echo "shasum or sha256sum is required" >&2
    exit 1
  fi
}

reject_unsafe_archive_members() {
  local archive="$1"
  local list="$2"
  if [ "$dry_run" = "true" ]; then
    printf '+ tar -tzf %q > %q\n' "$archive" "$list"
    printf '+ awk archive-member-safety-check %q\n' "$list"
    return
  fi
  tar -tzf "$archive" > "$list"
  awk '
    $0 ~ /^\// || $0 ~ /(^|\/)\.\.($|\/)/ {
      print "unsafe archive member path: " $0 > "/dev/stderr"
      bad = 1
    }
    END { exit bad }
  ' "$list"
}

if [ "$version" = "latest" ]; then
  if [ "$dry_run" = "true" ]; then
    run gh release view --repo "$repo" --json tagName -q .tagName
    version="v0.0.0"
  else
    version="$(gh release view --repo "$repo" --json tagName -q .tagName)"
  fi
fi

plain="$(version_plain "$version")"
if [ -z "$host" ]; then
  host="$(detect_host)"
fi
if ! [[ "$host" =~ ^(darwin|linux)/(amd64|arm64)$ ]]; then
  echo "--host must be one of darwin/amd64, darwin/arm64, linux/amd64, linux/arm64" >&2
  exit 2
fi
goos="${host%%/*}"
goarch="${host##*/}"
tap="${repo%%/*}/${repo##*/}"

cleanup=""
if [ -z "$work_dir" ]; then
  if [ "$dry_run" = "true" ]; then
    work_dir="${TMPDIR:-/tmp}/gomoufox-public-canary"
  else
    work_dir="$(mktemp -d "${TMPDIR:-/tmp}/gomoufox-public-canary.XXXXXX")"
    cleanup="$work_dir"
  fi
fi
if [ -n "$cleanup" ]; then
  trap 'rm -rf "$cleanup"' EXIT
fi
if [ -z "$assets_dir" ]; then
  assets_dir="$work_dir/assets"
fi

archive_name="gomoufox_${plain}_${goos}_${goarch}.tar.gz"
archive="$assets_dir/$archive_name"
extract_dir="$work_dir/extract"
archive_root="gomoufox_${plain}_${goos}_${goarch}"
gomoufox_bin="$extract_dir/$archive_root/gomoufox"
mcp_in="$work_dir/mcp-in.jsonl"
mcp_out="$work_dir/mcp-out.jsonl"
mcp_err="$work_dir/mcp-stderr.log"

if [ "$dry_run" != "true" ]; then
  mkdir -p "$work_dir" "$assets_dir" "$extract_dir"
fi

if [ ! -d "$assets_dir" ] || [ -z "$(find "$assets_dir" -maxdepth 1 -type f -print -quit 2>/dev/null || true)" ]; then
  run gh release download "$version" --repo "$repo" --dir "$assets_dir" --pattern "gomoufox_*" --pattern "checksums.*" --pattern "gomoufox.rb" --pattern "release-provenance.json" --pattern "sbom.spdx.json"
fi

for required in checksums.txt checksums.json gomoufox.rb release-provenance.json sbom.spdx.json "$archive_name"; do
  if [ "$dry_run" = "true" ]; then
    printf '+ test -f %q\n' "$assets_dir/$required"
  else
    require_file "$assets_dir/$required"
  fi
done

verify_checksums "$assets_dir"

if [ "$verify_attestations" = "true" ]; then
  for asset in "$assets_dir"/*; do
    if [ "$dry_run" = "true" ]; then
      run gh attestation verify "$assets_dir/<asset>" --repo "$repo"
      break
    fi
    [ -f "$asset" ] || continue
    run gh attestation verify "$asset" --repo "$repo"
  done
fi

reject_unsafe_archive_members "$archive" "$work_dir/archive-members.txt"
run tar -xzf "$archive" -C "$extract_dir"
canary_args=(--gomoufox "$gomoufox_bin" --work-dir "$work_dir/no-python")
if [ "$runtime_smoke" != "true" ]; then
  canary_args+=(--skip-install --skip-doctor)
fi
if [ -n "$browser_smoke_url" ]; then
  canary_args+=(--browser-smoke-url "$browser_smoke_url")
fi
if [ "$dry_run" = "true" ]; then
  canary_args+=(--dry-run)
fi
if [ "$dry_run" = "true" ]; then
  printf '+ bash scripts/no-python-consumer-canary.sh'
  printf ' %q' "${canary_args[@]}"
  printf '\n'
  bash scripts/no-python-consumer-canary.sh "${canary_args[@]}"
else
  run bash scripts/no-python-consumer-canary.sh "${canary_args[@]}"
fi

case "$brew_mode" in
  off) ;;
  inspect)
    run grep -F "class Gomoufox < Formula" "$assets_dir/gomoufox.rb"
    run grep -F "github.com/$repo/releases/download/$version/gomoufox_" "$assets_dir/gomoufox.rb"
    ;;
  install)
    case "$host" in
      darwin/arm64|linux/amd64) ;;
      *) echo "Homebrew formula install is unsupported on $host" >&2; exit 2 ;;
    esac
    run command -v brew
    run env HOMEBREW_NO_AUTO_UPDATE=1 brew tap "$tap" "https://github.com/$repo"
    if [ "$dry_run" = "true" ]; then
      printf '+ if brew commands | grep -qx trust; then brew trust --formula %q; fi\n' "$tap/gomoufox"
    elif brew commands 2>/dev/null | grep -qx trust; then
      run brew trust --formula "$tap/gomoufox"
    fi
    run env HOMEBREW_NO_AUTO_UPDATE=1 brew install "$tap/gomoufox"
    run brew test gomoufox
    run brew uninstall --force gomoufox
    ;;
esac

if [ "$go_install" = "true" ]; then
  run command -v go
  run env "GOBIN=$work_dir/gobin" go install "github.com/$repo/cmd/gomoufox@$version"
  run "$work_dir/gobin/gomoufox" --version
fi

echo "public consumer canary passed: $repo $version $host"
