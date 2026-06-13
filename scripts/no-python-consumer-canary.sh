#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: scripts/no-python-consumer-canary.sh --gomoufox <path> [flags]

Run default consumer CLI/MCP flows with python and python3 replaced by failing
PATH canaries. Any default-path Python invocation fails the command and records
which shim was called.

flags:
  --gomoufox <path>            gomoufox binary to test.
  --work-dir <dir>             Scratch directory. Default: temporary directory.
  --skip-install               Skip gomoufox install.
  --skip-doctor                Skip gomoufox doctor.
  --browser-smoke-url <url>    Optional gomoufox get smoke URL.
  --json-out <path>            Write node-direct consumer readiness JSON.
  --dry-run                    Print commands without executing them.
  --help                       Show this help.
EOF
}

gomoufox_bin=""
work_dir=""
skip_install="false"
skip_doctor="false"
browser_smoke_url=""
json_out=""
dry_run="false"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --gomoufox) gomoufox_bin="${2:?missing value for --gomoufox}"; shift 2 ;;
    --work-dir) work_dir="${2:?missing value for --work-dir}"; shift 2 ;;
    --skip-install) skip_install="true"; shift ;;
    --skip-doctor) skip_doctor="true"; shift ;;
    --browser-smoke-url) browser_smoke_url="${2:?missing value for --browser-smoke-url}"; shift 2 ;;
    --json-out) json_out="${2:?missing value for --json-out}"; shift 2 ;;
    --dry-run) dry_run="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$gomoufox_bin" ]; then
  echo "--gomoufox is required" >&2
  exit 2
fi

cleanup=""
if [ -z "$work_dir" ]; then
  if [ "$dry_run" = "true" ]; then
    work_dir="${TMPDIR:-/tmp}/gomoufox-no-python-canary"
  else
    work_dir="$(mktemp -d "${TMPDIR:-/tmp}/gomoufox-no-python-canary.XXXXXX")"
    cleanup="$work_dir"
  fi
fi
if [ -n "$cleanup" ]; then
  trap 'rm -rf "$cleanup"' EXIT
fi

canary_dir="$work_dir/path"
home_dir="$work_dir/home"
xdg_cache_dir="$work_dir/xdg-cache"
session_dir="$work_dir/sessions"
skills_dir="$work_dir/skills"
log_file="$work_dir/python-canary.log"
mcp_in="$work_dir/mcp-in.jsonl"
mcp_out="$work_dir/mcp-out.jsonl"
mcp_err="$work_dir/mcp-stderr.log"

run() {
  printf '+'
  printf ' %q' "$@"
  printf '\n'
  if [ "$dry_run" != "true" ]; then
    "$@"
  fi
}

run_no_python() {
  if [ "$dry_run" = "true" ]; then
    printf '+ env PATH=%q:$PATH HOME=%q XDG_CACHE_HOME=%q GOMOUFOX_PYTHON_CANARY_LOG=%q' "$canary_dir" "$home_dir" "$xdg_cache_dir" "$log_file"
    printf ' %q' "$@"
    printf '\n'
    return
  fi
  env PATH="$canary_dir:$PATH" HOME="$home_dir" XDG_CACHE_HOME="$xdg_cache_dir" GOMOUFOX_PYTHON_CANARY_LOG="$log_file" "$@"
}

if [ "$dry_run" = "true" ]; then
  printf '+ mkdir -p %q %q %q %q %q\n' "$canary_dir" "$home_dir" "$xdg_cache_dir" "$session_dir" "$skills_dir"
  printf '+ install failing python/python3 shims in %q\n' "$canary_dir"
else
  mkdir -p "$canary_dir" "$home_dir" "$xdg_cache_dir" "$session_dir" "$skills_dir"
  cat > "$canary_dir/python" <<'PY'
#!/bin/sh
printf '%s\n' "python invoked: $0 $*" >> "${GOMOUFOX_PYTHON_CANARY_LOG:-/tmp/gomoufox-python-canary.log}"
echo "python must not be invoked by default gomoufox consumer flows" >&2
exit 97
PY
  chmod +x "$canary_dir/python"
  ln -f "$canary_dir/python" "$canary_dir/python3"
  : > "$log_file"
fi

if [ "$skip_install" != "true" ]; then
  run_no_python "$gomoufox_bin" install
fi
if [ "$skip_doctor" != "true" ]; then
  run_no_python "$gomoufox_bin" --json doctor
fi
run_no_python "$gomoufox_bin" --version
run_no_python "$gomoufox_bin" help --json --fields commands
run_no_python "$gomoufox_bin" help mcp --json
run_no_python "$gomoufox_bin" skills list --json
run_no_python "$gomoufox_bin" skills show core --json
run_no_python "$gomoufox_bin" skills show mcp --json
run_no_python "$gomoufox_bin" skills install --target codex --dir "$skills_dir" --dry-run --json
run_no_python "$gomoufox_bin" agents install --target all --scope user --features skills,mcp --toolset core --dry-run --json

if [ "$dry_run" = "true" ]; then
  printf '+ write MCP smoke input %q\n' "$mcp_in"
  printf '+ env PATH=%q:$PATH HOME=%q XDG_CACHE_HOME=%q GOMOUFOX_PYTHON_CANARY_LOG=%q %q mcp --toolset core --session-dir %q < %q > %q 2> %q\n' "$canary_dir" "$home_dir" "$xdg_cache_dir" "$log_file" "$gomoufox_bin" "$session_dir" "$mcp_in" "$mcp_out" "$mcp_err"
  printf '+ test MCP smoke returned three JSON-RPC responses with empty stderr\n'
else
  printf '%s\n%s\n%s\n' \
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
    '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
    '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"skills_list","arguments":{}}}' \
    > "$mcp_in"
  env PATH="$canary_dir:$PATH" HOME="$home_dir" XDG_CACHE_HOME="$xdg_cache_dir" GOMOUFOX_PYTHON_CANARY_LOG="$log_file" \
    "$gomoufox_bin" mcp --toolset core --session-dir "$session_dir" < "$mcp_in" > "$mcp_out" 2> "$mcp_err"
  if [ -s "$mcp_err" ]; then
    echo "MCP no-Python canary wrote stderr:" >&2
    sed -n '1,120p' "$mcp_err" >&2
    exit 1
  fi
  if [ "$(wc -l < "$mcp_out" | tr -d ' ')" -ne 3 ]; then
    echo "MCP no-Python canary expected three JSON-RPC responses" >&2
    sed -n '1,120p' "$mcp_out" >&2
    exit 1
  fi
fi

if [ -n "$browser_smoke_url" ]; then
  run_no_python "$gomoufox_bin" --json get "$browser_smoke_url" --text --max-bytes 4096
fi

if [ "$dry_run" = "true" ]; then
  printf '+ test ! -s %q\n' "$log_file"
  if [ -n "$json_out" ]; then
    printf '+ write node-direct consumer readiness JSON %q\n' "$json_out"
  fi
else
  if [ -s "$log_file" ]; then
    echo "default consumer flow invoked Python:" >&2
    sed -n '1,120p' "$log_file" >&2
    exit 1
  fi
  if [ -n "$json_out" ]; then
    mkdir -p "$(dirname "$json_out")"
    install_check=true
    doctor_check=true
    if [ "$skip_install" = "true" ]; then
      install_check=false
    fi
    if [ "$skip_doctor" = "true" ]; then
      doctor_check=false
    fi
    cat > "$json_out" <<EOF
{
  "node_direct_consumer_readiness": {
    "status": "candidate",
    "candidate": true,
    "runtime": "node-direct",
    "python_invoked": false,
    "python_canary_log": "$log_file",
    "checks": [
      {"name": "install", "passed": $install_check},
      {"name": "doctor", "passed": $doctor_check},
      {"name": "cli_help", "passed": true},
      {"name": "mcp_core_handshake", "passed": true},
      {"name": "skills", "passed": true}
    ],
    "note": "Default consumer install, doctor, CLI discovery, MCP core handshake, and skills ran with failing python/python3 PATH shims."
  }
}
EOF
  fi
fi

echo "no-Python consumer canary passed"
