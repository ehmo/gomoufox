#!/usr/bin/env python3
import argparse
import json
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


CONTRACTS = {
    "cli-help.json": {
        "budget": 4096,
        "token_budget": 1024,
        "args": ["help", "--json"],
    },
    "cli-help-full.json": {
        "budget": 12000,
        "token_budget": 3000,
        "args": ["help", "--json", "--full"],
    },
    "cli-help-mcp.json": {
        "budget": 4096,
        "token_budget": 1024,
        "args": ["help", "mcp", "--json"],
    },
    "cli-skills-list.json": {
        "budget": 4096,
        "token_budget": 1024,
        "args": ["skills", "list", "--json"],
    },
    "cli-skills-core.json": {
        "budget": 4096,
        "token_budget": 1024,
        "args": ["skills", "show", "core", "--json"],
    },
    "cli-skills-mcp.json": {
        "budget": 4096,
        "token_budget": 1024,
        "args": ["skills", "show", "mcp", "--json"],
    },
    "mcp-tools-list.json": {
        "budget": 30000,
        "token_budget": 7500,
        "compact_budget": True,
        "mcp": True,
    },
    "mcp-skills-list.json": {
        "budget": 4096,
        "token_budget": 1024,
        "mcp_call": {"name": "skills_list", "arguments": {}},
    },
    "mcp-skills-core.json": {
        "budget": 4096,
        "token_budget": 1024,
        "mcp_call": {"name": "skills_get", "arguments": {"name": "core"}},
    },
}

CLI_MCP_DOC_BUDGET = 91000
SKILL_DOC_BUDGET = 16000
SKILL_SCAN_SKIP_DIRS = {".beads", ".git", ".dolt", "dist", "public", "team" + "reports"}
POSITIONING_OVERCLAIMS = {
    "chrome devtools mcp exposes raw cdp-style commands": "Chrome DevTools MCP has UID snapshots and UID-based input; avoid implying it is raw-coordinate only",
    "screenshot + infer coordinates": "Chrome DevTools MCP has snapshot UID workflows; do not claim agents must infer coordinates",
    "write css selectors blind": "Chrome DevTools MCP has snapshot UID workflows; do not claim agents must invent selectors blindly",
    "requires explicit `new_page`/`close_page`/`select_page`": "Chrome DevTools MCP page lifecycle is real, but do not frame it as required overhead for every flow",
    "core fragility of chrome-devtools mcp": "Use scoped tradeoff language instead of attacking Chrome DevTools MCP",
    "passes cloudflare js challenges": "Use measured Go/Python Camoufox parity language, not blanket challenge-pass claims",
    "don't hit cloudflare/bot manager blocks": "Anti-detect is not a guarantee; use measured parity language",
    "would be blocked on twitter, linkedin, amazon": "Do not predict specific site blocking without checked evidence",
    "any cloudflare-protected site": "Do not claim broad Cloudflare outcomes beyond checked targets",
    "gomoufox agents work by default": "Use measured parity language, not universal success claims",
    "one unnamed context per server": "Playwright MCP supports persistent profiles and isolated sessions; do not claim it loses state by design",
    "loses state between agent turns": "Playwright MCP supports persistent profiles and isolated sessions; do not claim it loses state by design",
    "playwright mcp does not cap response sizes": "Use scoped wording about gomoufox's explicit byte caps and Playwright's documented output mitigations",
    "must `evaluate_script` a `fetch()`": "Chrome DevTools/Playwright can script fetches; prefer scoped wording about gomoufox's first-class browser_fetch",
    "must evaluate_script a fetch": "Chrome DevTools/Playwright can script fetches; prefer scoped wording about gomoufox's first-class browser_fetch",
    "passes cloudflare": "Use measured Go/Python Camoufox parity language, not Cloudflare pass guarantees",
    "passed a cloudflare": "Use measured Go/Python Camoufox parity language, not Cloudflare pass guarantees",
    "bypass cloudflare": "Use measured Go/Python Camoufox parity language, not Cloudflare bypass guarantees",
    "python-free": "Python remains required until the Camoufox launch-options dependency is replaced and parity-gated",
    "without python": "Python remains required until the Camoufox launch-options dependency is replaced and parity-gated",
    "python is optional": "Python remains required until the Camoufox launch-options dependency is replaced and parity-gated",
}


def canonical(data: object) -> str:
    return json.dumps(data, indent=2, sort_keys=True, separators=(",", ": ")) + "\n"


def compact_json(data: object) -> str:
    return json.dumps(data, sort_keys=True, separators=(",", ":")) + "\n"


def byte_len(text: str) -> int:
    return len(text.encode("utf-8"))


def estimated_tokens(text: str) -> int:
    return (byte_len(text) + 3) // 4


def gomoufox_base(root: Path, raw: str | None) -> list[str]:
    if raw:
        return [raw]
    return ["go", "run", "./cmd/gomoufox"]


def run_json(root: Path, command: list[str], args: list[str]) -> object:
    proc = subprocess.run(command + args, cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    if proc.returncode != 0:
        raise RuntimeError(f"{' '.join(command + args)} failed with {proc.returncode}\n{proc.stderr}{proc.stdout}")
    if proc.stderr.strip():
        raise RuntimeError(f"{' '.join(command + args)} wrote stderr\n{proc.stderr}")
    return json.loads(proc.stdout)


def run_mcp_tools_list(root: Path, command: list[str]) -> object:
    return run_mcp_request(root, command, {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}, "tools/list")


def run_mcp_tool_call(root: Path, command: list[str], name: str, arguments: dict[str, object]) -> object:
    return run_mcp_request(
        root,
        command,
        {"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": name, "arguments": arguments}},
        f"tools/call {name}",
    )


def run_mcp_request(root: Path, command: list[str], request: dict[str, object], label: str) -> object:
    with tempfile.TemporaryDirectory(prefix="gomoufox-agent-contract-") as tmp:
        proc = subprocess.run(
            command + ["mcp", "--session-dir", str(Path(tmp) / "sessions")],
            cwd=root,
            input=json.dumps(request, separators=(",", ":")) + "\n",
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
    if proc.returncode != 0:
        raise RuntimeError(f"{' '.join(command)} mcp {label} failed with {proc.returncode}\n{proc.stderr}{proc.stdout}")
    if proc.stderr.strip():
        raise RuntimeError(f"{' '.join(command)} mcp {label} wrote stderr\n{proc.stderr}")
    lines = [line for line in proc.stdout.splitlines() if line.strip()]
    if len(lines) != 1:
        raise RuntimeError(f"mcp {label} expected one JSON-RPC response, got {len(lines)}\n{proc.stdout}")
    response = json.loads(lines[0])
    if "error" in response:
        raise RuntimeError(f"mcp {label} returned error\n{lines[0]}")
    return response["result"]


def live_contracts(root: Path, gomoufox: str | None) -> dict[str, str]:
    command = gomoufox_base(root, gomoufox)
    out: dict[str, str] = {}
    for name, spec in CONTRACTS.items():
        if spec.get("mcp"):
            data = run_mcp_tools_list(root, command)
        elif "mcp_call" in spec:
            call = spec["mcp_call"]
            data = run_mcp_tool_call(root, command, str(call["name"]), dict(call["arguments"]))
        else:
            data = run_json(root, command, list(spec["args"]))
        text = canonical(data)
        budget_text = compact_json(data) if spec.get("compact_budget") else text
        budget = int(spec["budget"])
        size = byte_len(budget_text)
        if size > budget:
            raise RuntimeError(f"{name} is {size} bytes, over budget {budget}")
        token_budget = int(spec["token_budget"])
        tokens = estimated_tokens(budget_text)
        if tokens > token_budget:
            raise RuntimeError(f"{name} is estimated {tokens} tokens, over budget {token_budget}")
        out[name] = text
    return out


def parse_tool_docs(path: Path) -> dict[str, dict[str, object]]:
    text = path.read_text(encoding="utf-8")
    out: dict[str, dict[str, object]] = {}
    matches = list(re.finditer(r"^### Tool: `([^`]+)`", text, flags=re.MULTILINE))
    for idx, match in enumerate(matches):
        name = match.group(1)
        end = matches[idx + 1].start() if idx + 1 < len(matches) else len(text)
        block = text[match.end():end]
        desc_match = re.search(r"^\*\*Description:\*\* (.+)$", block, flags=re.MULTILINE)
        schema_match = re.search(r"\*\*Input schema:\*\*\s*```json\s*(.*?)\s*```", block, flags=re.DOTALL)
        if not desc_match:
            raise RuntimeError(f"{path} missing description for tool {name}")
        if not schema_match:
            raise RuntimeError(f"{path} missing input schema for tool {name}")
        try:
            schema = json.loads(schema_match.group(1))
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"{path} invalid input schema for tool {name}: {exc}") from exc
        out[name] = {"description": desc_match.group(1), "inputSchema": schema}
    return out


def parse_cli_help_snippet(path: Path) -> object:
    text = path.read_text(encoding="utf-8")
    match = re.search(r"name/usage index:\s*```json\s*(.*?)\s*```", text, flags=re.DOTALL)
    if not match:
        raise RuntimeError(f"{path} missing CLI help JSON snippet")
    try:
        return json.loads(match.group(1))
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"{path} invalid CLI help JSON snippet: {exc}") from exc


def check_cli_help_doc(path: Path, root: Path, command: list[str]) -> list[str]:
    failures: list[str] = []
    snippet = parse_cli_help_snippet(path)
    if not isinstance(snippet, dict):
        return [f"{path} CLI help snippet must be a JSON object"]
    live = run_json(root, command, ["help", "--json"])
    if canonical(snippet) != canonical(live):
        failures.append(f"{path} CLI help snippet drift")
    if "mcp_tools" in live:
        failures.append(f"{path} top-level help --json unexpectedly includes mcp_tools")
    mcp_help = run_json(root, command, ["help", "mcp", "--json"])
    if "mcp_tools" not in mcp_help:
        failures.append(f"{path} gomoufox help mcp --json missing mcp_tools")
    fields = run_json(root, command, ["help", "--json", "--fields", "commands"])
    if sorted(fields.keys()) != ["commands"]:
        failures.append(f"{path} gomoufox help --json --fields commands returned keys {sorted(fields.keys())}")
    return failures


def check_mcp_skill_body(root: Path, gomoufox: str | None, live: dict[str, str]) -> list[str]:
    command = gomoufox_base(root, gomoufox)
    failures: list[str] = []
    skill = run_json(root, command, ["skills", "show", "mcp", "--json"])
    body = str(skill.get("body", ""))
    tools = {tool["name"] for tool in json.loads(live["mcp-tools-list.json"])["tools"]}
    allowed_non_tools = {"session_id"}
    for match in sorted(set(re.findall(r"`((?:browser|session|skills)_[a-z0-9_]+)`", body))):
        if match not in tools and match not in allowed_non_tools:
            failures.append(f"embedded mcp skill references unknown MCP tool {match!r}")

    mcp_help = run_json(root, command, ["help", "mcp", "--json"])
    allowed_flags = set(mcp_help.get("global", []))
    for command_doc in mcp_help.get("commands", []):
        if isinstance(command_doc, dict):
            allowed_flags.update(command_doc.get("flags", []))
    normalized = {flag.split()[0] for flag in allowed_flags}
    for flag in sorted(set(re.findall(r"--[a-z0-9-]+", body))):
        if flag not in normalized:
            failures.append(f"embedded mcp skill references unknown mcp flag {flag!r}")
    return failures


def check_agents_install_dry_run(root: Path, gomoufox: str | None) -> list[str]:
    command = gomoufox_base(root, gomoufox)
    failures: list[str] = []
    data = run_json(
        root,
        command,
        ["agents", "install", "--target", "all", "--scope", "user", "--features", "skills,mcp", "--toolset", "core", "--dry-run", "--json"],
    )
    if data.get("target") != "all" or data.get("scope") != "user" or data.get("toolset") != "core" or data.get("dry_run") is not True:
        failures.append(f"agents install dry-run metadata mismatch: {data}")
    actions = data.get("actions")
    if not isinstance(actions, list) or len(actions) < 8:
        failures.append(f"agents install dry-run action list too small: {data}")
    else:
        kinds = {action.get("kind") for action in actions if isinstance(action, dict)}
        targets = ",".join(sorted(str(action.get("target")) for action in actions if isinstance(action, dict)))
        if kinds != {"skills", "mcp"}:
            failures.append(f"agents install dry-run kinds mismatch: {kinds}")
        for action in actions:
            if not isinstance(action, dict):
                failures.append(f"agents install action is not object: {action}")
                continue
            if action.get("status") != "would_write":
                failures.append(f"agents install dry-run non-dry status: {action}")
            if not Path(str(action.get("path", ""))).is_absolute():
                failures.append(f"agents install dry-run path is not absolute: {action}")
        for required in ("codex", "claude", "cursor", "gemini"):
            if required not in targets:
                failures.append(f"agents install dry-run missing target {required}: {targets}")
    return failures


def update_cli_help_doc(text: str, live_help: object) -> str:
    snippet = canonical(live_help).rstrip("\n")
    updated, count = re.subn(
        r"(name/usage index:\s*```json\s*).*?(\s*```)",
        lambda match: match.group(1) + snippet + match.group(2),
        text,
        count=1,
        flags=re.DOTALL,
    )
    if count != 1:
        raise RuntimeError("cli-mcp.md missing CLI help JSON snippet")
    return updated


def update_mcp_docs(text: str, mcp_tools_result: str) -> str:
    live_tools = json.loads(mcp_tools_result)["tools"]
    tools = {tool["name"]: tool for tool in live_tools}
    seen: list[str] = []

    def render_tool_block(tool: dict[str, object]) -> str:
        schema = canonical(tool["inputSchema"]).rstrip("\n")
        return (
            f"### Tool: `{tool['name']}`\n\n"
            f"**Description:** {tool['description']}\n\n"
            "**Input schema:**\n"
            "```json\n"
            f"{schema}\n"
            "```\n\n"
            "---\n\n"
        )

    def replace_block(match: re.Match[str]) -> str:
        name = match.group(1)
        body = match.group(2)
        if name not in tools:
            raise RuntimeError(f"cli-mcp.md documents unknown tool {name}")
        seen.append(name)
        tool = tools[name]
        body = re.sub(
            r"^\*\*Description:\*\* .+$",
            f"**Description:** {tool['description']}",
            body,
            count=1,
            flags=re.MULTILINE,
        )
        schema = canonical(tool["inputSchema"]).rstrip("\n")
        body, count = re.subn(
            r"(\*\*Input schema:\*\*\s*```json\s*).*?(\s*```)",
            lambda match: match.group(1) + schema + match.group(2),
            body,
            count=1,
            flags=re.DOTALL,
        )
        if count != 1:
            raise RuntimeError(f"cli-mcp.md missing input schema for tool {name}")
        return f"### Tool: `{name}`{body}"

    updated = re.sub(
        r"^### Tool: `([^`]+)`(.*?)(?=^### Tool: `|^### Complete Tool Inventory|\Z)",
        replace_block,
        text,
        flags=re.MULTILINE | re.DOTALL,
    )
    missing_names = [tool["name"] for tool in live_tools if tool["name"] not in seen]
    if missing_names:
        insertion = "".join(render_tool_block(tools[name]) for name in missing_names)
        updated, count = re.subn(
            r"(?=^### Complete Tool Inventory)",
            insertion,
            updated,
            count=1,
            flags=re.MULTILINE,
        )
        if count != 1:
            raise RuntimeError("cli-mcp.md missing Complete Tool Inventory insertion point")
        seen.extend(missing_names)
    missing = set(tools) - set(seen)
    extra = set(seen) - set(tools)
    if missing or extra:
        raise RuntimeError(f"cli-mcp.md tool inventory drift: missing={sorted(missing)} extra={sorted(extra)}")
    return updated


def update_docs(root: Path, docs_path: Path, gomoufox: str | None, live: dict[str, str]) -> None:
    if not docs_path.exists():
        return
    command = gomoufox_base(root, gomoufox)
    live_help = run_json(root, command, ["help", "--json"])
    text = docs_path.read_text(encoding="utf-8")
    text = update_cli_help_doc(text, live_help)
    text = update_mcp_docs(text, live["mcp-tools-list.json"])
    docs_path.write_text(text, encoding="utf-8")


def check_mcp_docs(path: Path, mcp_tools_result: str) -> list[str]:
    failures: list[str] = []
    docs = parse_tool_docs(path)
    live = json.loads(mcp_tools_result)
    tools = {tool["name"]: tool for tool in live["tools"]}
    if set(docs) != set(tools):
        failures.append(f"{path} tool inventory drift: docs={sorted(docs)} live={sorted(tools)}")
        return failures
    for name, doc in docs.items():
        tool = tools[name]
        if doc["description"] != tool["description"]:
            failures.append(f"{path} {name} description drift")
        if doc["inputSchema"] != tool["inputSchema"]:
            failures.append(f"{path} {name} input schema drift")
    return failures


def check_mcp_contract_invariants(mcp_tools_result: str) -> list[str]:
    failures: list[str] = []
    live = json.loads(mcp_tools_result)
    tools = {tool["name"]: tool for tool in live["tools"]}
    cookies = tools.get("browser_cookies")
    if cookies is None:
        return ["mcp tools missing browser_cookies"]
    description = str(cookies.get("description", "")).lower()
    if "delete" in description:
        failures.append("browser_cookies description must not advertise delete")
    schema = cookies.get("inputSchema", {})
    if not isinstance(schema, dict):
        failures.append("browser_cookies inputSchema must be an object")
        return failures
    props = schema.get("properties", {})
    if not isinstance(props, dict):
        failures.append("browser_cookies inputSchema.properties must be an object")
        return failures
    action = props.get("action", {})
    if not isinstance(action, dict):
        failures.append("browser_cookies action schema must be an object")
        return failures
    if action.get("enum") != ["get", "set", "clear"]:
        failures.append("browser_cookies action enum must be exactly ['get', 'set', 'clear']")
    session_load = tools.get("session_load")
    if session_load is None:
        failures.append("mcp tools missing session_load")
        return failures
    load_description = str(session_load.get("description", "")).lower()
    if "non-persistent" not in load_description or "replace" not in load_description:
        failures.append("session_load description must advertise non-persistent replacement semantics")
    load_schema = session_load.get("inputSchema", {})
    if not isinstance(load_schema, dict):
        failures.append("session_load inputSchema must be an object")
        return failures
    load_props = load_schema.get("properties", {})
    if not isinstance(load_props, dict):
        failures.append("session_load inputSchema.properties must be an object")
        return failures
    mode = load_props.get("mode", {})
    if not isinstance(mode, dict):
        failures.append("session_load mode schema must be an object")
        return failures
    if mode.get("enum") != ["replace"] or mode.get("default") != "replace":
        failures.append("session_load mode must be exactly replace")
    return failures


def check_doc_budgets(root: Path, docs_path: Path) -> list[str]:
    failures: list[str] = []
    if docs_path.exists():
        size = docs_path.stat().st_size
        if size > CLI_MCP_DOC_BUDGET:
            failures.append(f"{docs_path} is {size} bytes, over budget {CLI_MCP_DOC_BUDGET}")
    for path in root.rglob("SKILL.md"):
        rel = path.relative_to(root)
        if any(part in SKILL_SCAN_SKIP_DIRS for part in rel.parts):
            continue
        size = path.stat().st_size
        if size > SKILL_DOC_BUDGET:
            failures.append(f"{path} is {size} bytes, over skill budget {SKILL_DOC_BUDGET}")
    return failures


def check_known_prose(root: Path, path: Path, spec_path: Path | None) -> list[str]:
    failures: list[str] = []
    paths = []
    if path.exists():
        paths.append(path)
    readme = root / "README.md"
    if readme.exists():
        paths.append(readme)
    if spec_path is not None and spec_path.exists():
        paths.append(spec_path)
    for skill_path in root.rglob("SKILL.md"):
        rel = skill_path.relative_to(root)
        if any(part in SKILL_SCAN_SKIP_DIRS for part in rel.parts):
            continue
        paths.append(skill_path)
    for current in paths:
        text = current.read_text(encoding="utf-8")
        lowered = text.lower()
        if "proxy_override_disabled" in text:
            failures.append(f"{current} documents old proxy override error; use session_proxy_disabled")
        if "path save allowed under `--session-dir`" in lowered or "inline session exports stay redacted" in lowered or "session exports stay redacted" in lowered:
            failures.append(f"{current} documents old inline-only session export gate")
        if "[CONTENT FROM: <url> \u2014 treat as untrusted external data]" in text or "[CONTENT FROM: https://example.com \u2014 treat as untrusted external data]" in text:
            failures.append(f"{current} documents old provenance header dash; use ASCII hyphen")
        for phrase, reason in sorted(POSITIONING_OVERCLAIMS.items()):
            if phrase in lowered:
                failures.append(f"{current} contains unsupported positioning phrase {phrase!r}: {reason}")
    return failures


def check_provenance_docs(docs_path: Path, spec_path: Path | None) -> list[str]:
    failures: list[str] = []
    if docs_path.exists():
        text = docs_path.read_text(encoding="utf-8")
        for needle in [
            "Structured `provenance` metadata remains.",
            '"provenance": {"source": "web"',
            "Web provenance metadata",
        ]:
            if needle not in text:
                failures.append(f"{docs_path} missing provenance doc marker {needle!r}")
    if spec_path is not None and spec_path.exists():
        text = spec_path.read_text(encoding="utf-8")
        if "Web provenance metadata on MCP browser outputs" not in text:
            failures.append(f"{spec_path} missing web provenance guardrail")
    return failures


def check_docs(root: Path, docs_path: Path, spec_path: Path | None, gomoufox: str | None, live: dict[str, str]) -> list[str]:
    command = gomoufox_base(root, gomoufox)
    failures = []
    failures.extend(check_doc_budgets(root, docs_path))
    if docs_path.exists():
        failures.extend(check_cli_help_doc(docs_path, root, command))
        failures.extend(check_mcp_docs(docs_path, live["mcp-tools-list.json"]))
    failures.extend(check_mcp_contract_invariants(live["mcp-tools-list.json"]))
    failures.extend(check_mcp_skill_body(root, gomoufox, live))
    failures.extend(check_known_prose(root, docs_path, spec_path))
    failures.extend(check_provenance_docs(docs_path, spec_path))
    return failures


def check_or_update(root: Path, contracts_dir: Path, docs_path: Path, spec_path: Path | None, gomoufox: str | None, update: bool) -> list[str]:
    live = live_contracts(root, gomoufox)
    failures: list[str] = []
    if update:
        contracts_dir.mkdir(parents=True, exist_ok=True)
    for name, text in live.items():
        path = contracts_dir / name
        if update:
            path.write_text(text, encoding="utf-8")
            continue
        if not path.exists():
            failures.append(f"missing {name}; run scripts/check-agent-contracts.py --update")
            continue
        expected = path.read_text(encoding="utf-8")
        if expected != text:
            failures.append(f"{name} drift: run scripts/check-agent-contracts.py --update and review the diff")
    if update:
        update_docs(root, docs_path, gomoufox, live)
    failures.extend(check_agents_install_dry_run(root, gomoufox))
    failures.extend(check_docs(root, docs_path, spec_path, gomoufox, live))
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description="Check agent-facing gomoufox CLI and MCP discovery contracts.")
    parser.add_argument("--root", default=".", help="repository root")
    parser.add_argument("--contracts-dir", default="docs/agent-contracts", help="contract snapshot directory")
    parser.add_argument("--docs-path", default="cli-mcp.md", help="private CLI/MCP Markdown spec to check when present")
    parser.add_argument("--spec-path", default="spec.md", help="private guardrail spec to check when present")
    parser.add_argument("--gomoufox", help="path to a gomoufox binary; defaults to go run ./cmd/gomoufox")
    parser.add_argument("--update", action="store_true", help="refresh contract snapshots")
    args = parser.parse_args()

    root = Path(args.root).resolve()
    contracts_dir = Path(args.contracts_dir)
    if not contracts_dir.is_absolute():
        contracts_dir = root / contracts_dir
    docs_path = Path(args.docs_path)
    if not docs_path.is_absolute():
        docs_path = root / docs_path
    spec_path = Path(args.spec_path) if args.spec_path else None
    if spec_path is not None and not spec_path.is_absolute():
        spec_path = root / spec_path
    if args.gomoufox and not Path(args.gomoufox).exists() and shutil.which(args.gomoufox) is None:
        print(f"gomoufox executable not found: {args.gomoufox}", file=sys.stderr)
        return 1

    try:
        failures = check_or_update(root, contracts_dir, docs_path, spec_path, args.gomoufox, args.update)
    except Exception as exc:
        print(str(exc), file=sys.stderr)
        return 1
    if failures:
        for failure in failures:
            print(failure, file=sys.stderr)
        return 1
    action = "updated" if args.update else "ok"
    print(f"agent contracts {action}: {contracts_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
