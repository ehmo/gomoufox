# Changelog

## v0.1.28

- Let agent skills discover the live gomoufox contract through MCP when the
  MCP server is registered but the CLI is absent from the agent shell.
- Detect stale installed skill files: dry runs report `needs_force`, exact
  matches stay unchanged, and apply runs fail before writing any files unless
  `--force` permits the update.
- Add capped `gomoufox eval --arg-file` input so JSON arguments can stay out of
  process argument lists. Daemon requests receive the JSON value, not the
  client file path.
- Refresh the hash-locked Python dependencies used by the legacy sidecar
  release gate.

## v0.1.27

- Require exact 100% Go statement coverage in private and public fast and
  release gates, including platform-specific branches exercised on Linux and
  macOS.
- Allow `ebay` and `ap-news` to remain shared blocked outcomes in release
  parity checks while keeping runtime mismatches and failed targets blocking.
- Give built-binary CLI integration tests a separate cold-build timeout so a
  slow hosted runner does not consume the MCP interaction timeout.

## v0.1.24

- Add `--allow-localhost` to browser CLI commands so explicit localhost and
  loopback HTTP(S) targets reach the browser filtering proxy, while broader
  private networks and metadata endpoints stay blocked.
- Remove the ineffective `--allow-private-ips` CLI flag and return marked
  filtering-proxy blocks as exit 8 errors instead of successful page content.

## v0.1.23

- Build with Go 1.26.6 to fix five reachable standard-library
  vulnerabilities found by the public release gate.

## v0.1.22

- Recognize the upstream `camoufox-bin` executable in Linux managed browser
  archives, with regression coverage for executable discovery.
- Publish a Linux amd64 container image at `ghcr.io/ehmo/gomoufox` with the
  pinned node-direct runtime preinstalled.
- Let `gomoufox serve` read its bearer token from
  `GOMOUFOX_DAEMON_TOKEN`, while keeping `--auth-token` as the explicit
  override.
- Build and exercise the container daemon before release publication, then
  publish versioned and latest image tags with registry provenance and SBOM
  attestations.

## v0.1.21

- Fix clean node-direct installs so `launchServer.js` resolves the pinned
  `playwright-core` package without an ambient `node_modules`. Existing caches
  repair on the next install check.
- Bind node-direct and Python launch servers to IPv4 loopback, preventing
  dual-stack port collisions from routing WebSocket clients to unrelated
  localhost services.
- Add an isolated managed-browser launch gate to nightly CI and release
  validation, including a weekly cold install and an exact JavaScript-written
  page marker.
- Force Homebrew tap trust enforcement in the public release canary.
- Refresh the hash-locked Python dependencies used by the legacy sidecar
  release gate.
- Confirm a mismatched real-site retry once in the opposite runtime order,
  while keeping persistent Go/Python drift fail-closed.
- Align the named release shared-block baseline with the checked 100-site
  benchmark, including recurring Etsy parity, while keeping zero failures.
- Balance release benchmark evidence across both Go-first and Python-first
  runtime order, with both harnesses constrained to generated Linux personas.
- Reuse one recorded persona across both benchmark runtimes and apply its
  fingerprint without random merge values.
- Keep node-direct launch add-ons aligned with the Python launch path by
  loading only add-ons supplied through `WithAddons`.
- Keep generated node-direct personas aligned with the pinned browser version,
  default font set, custom font options, and partial fingerprint overrides.
- Launch node-direct through the pinned Playwright server entry point with
  bounded Node heap settings.
- Bound realpass page and browser shutdown, terminate the affected sidecar
  process group after a timeout, and record a failed partial report.

## v0.1.20

- Add native HAR recording across the Go API, interactive `gomoufox record`
  CLI, and gated MCP `browser_har_start`/`browser_har_stop` tools. Metadata
  capture allowlists standard fields, redacts their values, and drops unknown
  fields by default, but remains sensitive. Full capture requires explicit
  opt-in and preserves request and response content.
- Add `browser_fetch_form`, a gated MCP multipart upload tool that sends files
  from `--session-dir` through the browser context without inline file bytes,
  plus binary-safe response encoding and stricter fetch header redaction.
- Refresh bundled Python requirement locks used by the legacy sidecar release
  gate.
- Pin the legacy Python sidecar and Python parity runner to gomoufox's
  manifest-verified v135 browser tree instead of Camoufox's moving global
  download.
- Assemble the pinned Playwright 1.57 driver from checksum-verified official
  npm and Node.js artifacts after the legacy Playwright driver CDN was retired,
  and share that managed driver between node-direct and Python runtimes.
- Build with Go 1.26.5 to incorporate the standard-library fix for
  `GO-2026-5856` (`CVE-2026-42505`).
- Upgrade `golang.org/x/net` to v0.56.0 and
  `github.com/go-jose/go-jose/v3` to v3.0.5 so the release does not retain
  dormant vulnerable module versions.

## v0.1.18

- Add `gomoufox mcp --allow-localhost` for explicit loopback HTTP(S) targets
  while preserving private-network, metadata, DNS-rebinding, and redirect
  guardrails.

## v0.1.17

- Fix node-direct MCP sessions that use persistent profiles, OS persona, or
  browser-context locale options, preventing `browser_start_failed` on the
  first navigation.
- Normalize CLI `--profile` paths before launch so relative profiles no longer
  reach Playwright as invalid `userDataDir` values.
- Refresh bundled Python requirement locks used by the legacy sidecar release
  gate.

## v0.1.16

- Add native browser download support to the Go API and MCP server, including
  an opt-in `browser_download` tool that saves only under `--session-dir`.

## v0.1.14

- Fix `gomoufox open` human-login launches that need profile, locale, or
  humanize launch options by selecting the Python sidecar instead of the
  node-direct runtime.
- Treat manual browser window closure as successful completion for
  `gomoufox open --save-session --wait`, so the storage state is still written
  after the operator logs in and closes the window.
- Document the human-login session handoff across CLI help, MCP guidance,
  bundled agent skills, generated agent contracts, README, and the public
  mirror.

## v0.1.13

- Fix MCP client compatibility by accepting reserved `params._meta` on
  `tools/call` and removing JSON Schema combinators from advertised tool
  input schemas.
- Restore browser interaction reliability by skipping falsy fingerprint screen
  samples, decoding snapshot element resolvers, including small snapshot
  element lists in `structuredContent`, and fixing `browser_wait_for`
  `url_contains`.
- Make text-heavy MCP tool responses fall back to text content instead of
  emitting metadata-only `structuredContent`.

## v0.1.12

- Fix `gomoufox setup --features mcp --yes` so existing MCP config files are
  merged instead of failing with a raw `path exists` error.

## v0.1.11

- Improve human CLI help with grouped commands, `-h`, `-v`, and `gomoufox
  version`.
- Add `gomoufox setup` for guided runtime, doctor, and agent skill plus MCP
  setup.

## v0.1.10

- Fix the generated Homebrew formula to install binaries from the release
  archive's top-level directory, and add release audit coverage for that path.

## v0.1.9

- Add `gomoufox agents install` to install bundled Agent Skills and guarded
  MCP stdio configuration for Codex, Claude, Cursor, and Gemini.

## v0.1.8

- Remove Python from the default node-direct install path for public consumers,
  including CLI, MCP, skills, and doctor flows.
- Route node-direct Playwright use through one managed runtime driver cache and
  reject stale managed launch-server scripts.
- Fix the managed launch server payload decoding path and hostile-page viewport
  metrics fallback.
- Validate the v0.1.8 release gate, no-Python release archive canary, and
  Homebrew formula install path.
- Update release coverage and binary-size floors to match the validated
  node-direct baseline.

## v0.1.7

- Clear checked-out release files before the public publish job runs
  `gh run download`, so the candidate artifact can extract without file
  collisions.

## v0.1.6

- Remove `actions/download-artifact` from the public publish job. The workflow
  now downloads the release candidate with `gh run download`, because the v7 and
  v8 action pins still emitted a `Buffer()` deprecation warning.

## v0.1.5

- Switch the public release workflow to the Node 24-compatible
  `actions/download-artifact` v7 pin after the v8 pin emitted a runtime
  `Buffer()` deprecation warning.

## v0.1.4

- Update the public release workflow to the Node 24-compatible
  `actions/download-artifact` v8 pin.

## v0.1.3

- Harden the public release audit with bounded transient Homebrew install
  retries, cleanup before retry, deterministic failure fail-fast behavior, and
  unsafe checksum asset-name rejection.
- Split private/public release workflow privileges so read-only gates run
  before deploy keys or write-scoped release tokens are available.
- Keep the public publication path faster by deferring duplicate coverage and
  vulnerability checks to the public release gate while still running package,
  contract, docs, agent contract, test, vet, and CLI smoke checks before tagging.
- Add private scheduled and public manual Go/Python benchmark workflows with
  loop caps, retained artifacts, and workflow summaries.
- Add Python 3.9 CI coverage for the public audit retry path.
- Install the pinned `uv 0.11.19` lock generator in private CI before running
  the release gate.
- Make Python lock generation platform-stable by omitting resolver provenance
  annotations from generated lock files.

## v0.1.2

- Add public release SBOM and provenance artifacts, plus GitHub artifact
  attestations in the release workflow.
- Add a reusable post-release public audit that verifies assets, checksums,
  attestations, archives, CLI discovery, MCP, skills, and the Homebrew path.
- Narrow the Homebrew formula to browser-supported hosts: macOS Apple Silicon
  and Linux amd64.
- Add Python-removal readiness reporting so node-direct cannot replace Python
  until it beats Python on correctness, resources, timing, and report tokens.
- Add release-gate checking for hash-locked Python requirement freshness, with
  the lock generator pinned to `uv 0.11.19`.
- Install the pinned lock generator in the private public-release workflow.
- Generate the public Homebrew formula before public contract checks during
  publication.
- Make sidecar diagnostics test logging race-safe under `go test -race`.

## v0.1.1

- Replace the `go-readability` dependency chain with gomoufox's built-in
  article/main Markdown extractor.
- Lower the Linux `gomoufox` release binary from 11,038,846 bytes to 9,732,222
  bytes in the release gate.
- Refresh the checked 100-target Go/Python Camoufox benchmark from the June 8,
  2026 release gate: 95 passed, 5 shared blocked, 0 failed, and zero outcome
  mismatches.
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
