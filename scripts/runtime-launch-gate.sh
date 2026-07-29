#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/runtime-launch-gate.sh [flags]

Installs the managed node-direct runtime into an isolated cache, launches a
real browser, loads a local page, and checks text written by page JavaScript.

flags:
  --venv-dir <path>       Runtime cache to use. Default: a temporary directory.
  --preseed-runtime <dir> Copy a verified runtime tree into the temporary cache.
                          Cannot be combined with --venv-dir.
  --dry-run               Print the test command without executing it.
  --help                  Show this help.
EOF
}

venv_dir=""
preseed_runtime="${GOMOUFOX_RUNTIME_LAUNCH_PRESEED:-}"
dry_run="false"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --venv-dir) venv_dir="${2:?missing value for --venv-dir}"; shift 2 ;;
    --preseed-runtime) preseed_runtime="${2:?missing value for --preseed-runtime}"; shift 2 ;;
    --dry-run) dry_run="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -n "$venv_dir" ] && [ -n "$preseed_runtime" ]; then
  echo "--preseed-runtime cannot be combined with --venv-dir" >&2
  exit 2
fi
if [ -n "$preseed_runtime" ] && [ ! -d "$preseed_runtime" ]; then
  echo "preseed runtime is not a directory: $preseed_runtime" >&2
  exit 2
fi

root="$(CDPATH= cd "$(dirname "$0")/.." && pwd)"
temporary_root=""
if [ -z "$venv_dir" ]; then
  temporary_root="$(mktemp -d "${TMPDIR:-/tmp}/gomoufox-runtime-launch.XXXXXX")"
  venv_dir="$temporary_root/venv"
fi
scratch_home="$(mktemp -d "${TMPDIR:-/tmp}/gomoufox-runtime-home.XXXXXX")"

cleanup() {
  rm -rf "$scratch_home"
  if [ -n "$temporary_root" ]; then
    rm -rf "$temporary_root"
  fi
}
trap cleanup EXIT

mkdir -p "$venv_dir"
venv_dir="$(CDPATH= cd "$venv_dir" && pwd)"

assert_no_ambient_playwright_core() {
  local cursor="$1"
  local parent
  while :; do
    if [ -e "$cursor/node_modules/playwright-core" ]; then
      echo "ambient playwright-core found above runtime cache: $cursor/node_modules/playwright-core" >&2
      exit 1
    fi
    parent="$(dirname "$cursor")"
    if [ "$parent" = "$cursor" ]; then
      break
    fi
    cursor="$parent"
  done
}

run() {
  printf '+'
  printf ' %q' "$@"
  printf '\n'
  if [ "$dry_run" != "true" ]; then
    "$@"
  fi
}

assert_no_ambient_playwright_core "$venv_dir"
if [ -n "$preseed_runtime" ]; then
  if [ "$dry_run" = "true" ]; then
    printf '+ cp -rf %q %q\n' "$preseed_runtime" "$venv_dir/runtime"
  else
    cp -rf "$preseed_runtime" "$venv_dir/runtime"
  fi
fi
go_cache="$(go env GOCACHE)"
go_mod_cache="$(go env GOMODCACHE)"

cd "$root"
run env \
  -u NODE_PATH \
  -u NODE_OPTIONS \
  -u GOMOUFOX_CAMOUFOX_PATH \
  -u GOMOUFOX_TRUST_UNVERIFIED_CAMOUFOX_PATH \
  -u GOMOUFOX_SKIP_FETCH \
  "HOME=$scratch_home" \
  "XDG_CACHE_HOME=$scratch_home/.cache" \
  "GOCACHE=$go_cache" \
  "GOMODCACHE=$go_mod_cache" \
  GOMOUFOX_RUNTIME_LAUNCH=1 \
  "GOMOUFOX_RUNTIME_LAUNCH_VENV=$venv_dir" \
  go test -count=1 . -run '^TestManagedRuntimeLaunch$' -v

echo "managed runtime launch gate passed"
