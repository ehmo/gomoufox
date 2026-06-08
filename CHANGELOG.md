# Changelog

## Unreleased

- Add public release SBOM and provenance artifacts, plus GitHub artifact
  attestations in the release workflow.
- Add a reusable post-release public audit that verifies assets, checksums,
  attestations, archives, CLI discovery, MCP, skills, and the Homebrew path.
- Narrow the Homebrew formula to browser-supported hosts: macOS Apple Silicon
  and Linux amd64.
- Add Python-removal readiness reporting so node-direct cannot replace Python
  until it beats Python on correctness, resources, timing, and report tokens.
- Add release-gate checking for hash-locked Python requirement freshness.

## v0.1.1

- Replace the `go-readability` dependency chain with gomoufox's built-in
  article/main Markdown extractor.
- Lower the Linux `gomoufox` release binary from 11,038,846 bytes to 9,732,222
  bytes in the omarchy release gate.
- Refresh the checked 100-target Go/Python Camoufox benchmark from the June 8,
  2026 omarchy release gate: 95 passed, 5 shared blocked, 0 failed, and zero
  outcome mismatches.
- Keep benchmark docs tied to the generated benchmark report and preserve the
  fingerprint-audit release guidance during benchmark refreshes.
- Generate and stage the Homebrew formula during public repo publication so new
  release tags do not require a manual formula edit before the public release
  workflow runs.

## v0.1.0

- Ship the first Go wrapper for the pinned Camoufox stack.
- Add the Go library, `gomoufox` CLI, and MCP server.
- Add URL guardrails for schemes, private IPs, redirects, and browser traffic.
- Add the managed Camoufox installer and sidecar lifecycle.
- Add release-size builds and 100% Go statement coverage.
- Confine daemon session import/export files and export profiles under
  `serve --session-dir`, with symlink checks, capped storage-state files, and
  safe `0600` writes.
- Redact `browser_snapshot` form values by default; `--allow-snapshot-values`
  plus `include_values` can return only short values classified as safe.
- Route MCP-owned helper scripts through a startup-probed internal helper
  evaluation path and remove the page-visible MCP helper object.
- Bound CLI and public Page fetch acquisition with stream reads, cap-aware
  cancellation, explicit truncation metadata, and copied error previews.
- Add checked agent-facing CLI/MCP discovery contracts and generated public
  snapshots for `gomoufox help --json --full`, `gomoufox help mcp --json`,
  and MCP `tools/list`.
- Add bounded MCP diagnosis tools for console/page errors, network summaries,
  and performance snapshots with redacted URLs, headers, and text.
- Add generated public CI and release validation.
- Add deterministic macOS/Linux release archives, checksums, and a Homebrew
  formula for the public repo.
- Add a public export contract so internal notes, agent files, local issue data,
  research notes, and test reports stay out of the public repo.
- Add installable `SKILL.md` agent skills plus `gomoufox skills export/install`
  so agents can load gomoufox guidance without npm, npx, or network fetches.
- Add a real MCP stdio integration test that runs the built `gomoufox` binary.
- Add Go/Python real-site parity gates for smoke, full, and soak release checks.
- Add the Go/Python real-site benchmark baseline.
- Add the 100-target Go/Python benchmark: same outcomes as Python Camoufox,
  lower RSS, lower CPU, and a smaller report-token footprint.
