# Go/Python Benchmark

- Generated: 2026-11-24T18:45:31.553250+00:00
- Mode: extended
- Targets: 100
- Loops: 2
- Timeout: 60s
- Wait until: commit
- Settle: 3s
- Load-state timeout: 0s
- Content max bytes: 250,000
- Sample interval: 0.5s
- Run order: alternate
- Go runner: prebuilt_binary
- Go sidecar runtime: node-direct
- Go custom venv: no
- Reuse browser: yes
- Generated persona OS: linux
- Shared persona SHA-256: d787a57c6677d1f793bff5c25a2b624b77c9d808ff011a62b129fbd46166c360
- Go report style: compact

This benchmark runs gomoufox and Python Camoufox against the same target set.
It records outcome parity, wall time, browser work duration, peak RSS, peak CPU, and agent-output token footprint.
For parity, both runtimes use `--unsafe-direct-network` and the same generated Linux persona bundle. Local URL guardrails are tested elsewhere.
gomoufox is timed as a built CLI binary. Build time is excluded.
Resource samples cover each complete harness process tree, including the runner, every driver and the browser.
Both runtimes use a fixed sampling period that includes snapshot collection time. A slow sample can delay the next one.

## Modes

- `smoke`: 2 fast targets for quick local checks.
- `full`: 8 detector and real-site targets used for the checked baseline.
- `extended`: 100 read-only public websites for broader resource, speed, parity, and token-footprint evidence.

Selected target tags: `auth-entry`, `bot-detection`, `cdn-security`, `cloud-platform`, `cloudflare`, `developer-platform`, `docs`, `ecommerce`, `media-heavy`, `spa`, `static`.

Refresh the checked baseline after significant browser, sidecar, MCP, CLI, or resource-related changes:

```bash
scripts/benchmark-realpass.py --mode full --go-sidecar-runtime node-direct --go-report-style compact --update-doc docs/BENCHMARKS.md
```

Run the extended matrix before release candidates or major runtime changes:

```bash
scripts/benchmark-realpass.py --mode extended --go-sidecar-runtime node-direct --loops 2 --run-order alternate --go-report-style compact --out dist/benchmarks/extended
```

Run the local fingerprint audit when changing Camoufox pins, launch options,
Firefox prefs, node-direct, WebGL, locale, timezone, screen, fonts, or canvas
handling:

```bash
scripts/fingerprint-audit.py
```

The audit serves one local page and records Python Camoufox, gomoufox with the
Python sidecar, and gomoufox with node-direct using one shared persona. Every
runtime must complete its probe and retain all expected fields. Gating compares the two
gomoufox runtimes and fails on any unallowed drift in JS-visible fields such as
UA, platform, languages, screen, timezone, WebGL, WebRTC, fonts, and canvas.
The Python Camoufox row provides an upstream reference.

## Pass/Fail Rules

- Go-only blocked, failed, or missing targets block release.
- Across alternating loops, a target counts as Go-only only when its Go-only outcome persists in every paired observation. Paired mismatches remain recorded.
- Go-only JS-visible fingerprint drift between gomoufox Python sidecar and gomoufox node-direct blocks release unless the changed field is explicitly allowlisted with evidence.
- Go/Python outcome mismatches that persist through the paired confirmation block release.
- Shared Go+Python blocked or failed targets are reported as site or upstream Camoufox behavior, not a gomoufox-only failure.
- Known recurring shared blocks should stay in the explicit release-gate allowlist.
- In release mode, a new shared block, failure, or performance outlier gets one focused retry.
- If the focused retry differs by outcome, the gate runs one confirmation with Python first. A persistent mismatch blocks release.
- Retry and confirmation samples must stay under absolute RSS and CPU caps.
- The gate merges focused retry measurements back into the full report and reruns the strict required-target, resource-ratio, timing-ratio, and report-token checks against that merged evidence.
- Release gate defaults block peak RSS above 6,000 MiB, peak CPU above 900%, Go RSS above Python * 1.50, and Go CPU above Python * 1.50.
- Full and release gates block Go wall time above Python * 1.05, Go target duration above Python * 1.05, and Go report tokens above Python * 0.50.
- Smoke mode is a functional parity check; its wall time is startup dominated.
- Use `--loops 2 --run-order alternate` when investigating timing changes so neither runtime always runs second.

## Summary

| Runtime | Passed | Blocked | Failed | Wall ms | Target ms | Peak RSS MiB | Peak CPU % |
|---|---:|---:|---:|---:|---:|---:|---:|
| gomoufox | 89 | 11 | 0 | 352,321 | 349,257 | 2,592.9 | 383.9 |
| Python Camoufox | 89 | 11 | 0 | 355,031 | 352,765 | 2,820.7 | 439.9 |

| Ratio | Go / Python |
|---|---:|
| Wall time | 0.992 |
| Target duration | 0.990 |
| Peak RSS | 0.919 |
| Peak CPU | 0.873 |
| Report tokens | 0.159 |

## Go/Python Benchmark Readiness

- Status: candidate
- Candidate: yes
- Note: Go/Python benchmark candidate means node-direct passed the extended comparison gate. Consumer no-Python readiness is recorded separately by scripts/no-python-consumer-canary.sh.

| Criterion | Passed | Detail |
|---|---:|---|
| complete_harness_resource_scope | yes | resource_scope=harness_process_tree |
| fixed_period_resource_sampling | yes | resource_sampling=fixed_period |
| go_sidecar_runtime_is_node_direct | yes | go_sidecar_runtime=node-direct |
| shared_linux_persona_bundle | yes | persona_os=linux persona_bundle_sha256=d787a57c6677d1f793bff5c25a2b624b77c9d808ff011a62b129fbd46166c360 |
| extended_target_matrix | yes | mode=extended targets=100 |
| no_go_only_outcome_regressions | yes | go_only_regression_count=0 outcome_mismatch_count=1 |
| no_runtime_failures | yes | go_failed=0 python_failed=0 |
| wall_time_not_slower_than_python | yes | wall_time=0.9923668637386595 max=1.05 |
| target_duration_not_slower_than_python | yes | target_duration=0.9900557028049835 max=1.05 |
| peak_rss_beats_python | yes | peak_rss=0.919267457705986 max=0.95 |
| peak_cpu_beats_python | yes | peak_cpu=0.8726983405319392 max=0.95 |
| report_tokens_beats_python | yes | report_tokens=0.15880896283117757 max=0.5 |

## Agent Output Footprint

Token estimates use `ceil(bytes / 4)`. They compare generated benchmark artifacts, not model billing.

| Runtime | JSON bytes | Markdown bytes | Estimated tokens |
|---|---:|---:|---:|
| gomoufox | 43,659 | 9,180 | 13,211 |
| Python Camoufox | 323,626 | 9,125 | 83,188 |

## Outcome Classes

- Shared blocked: `adidas`, `ap-news`, `ebay`, `fastly`, `g2`, `huggingface`, `nowsecure-cloudflare`, `oracle-cloud`, `perplexity`, `stackoverflow`, `uniqlo`
- Shared failed: none
- Go-only regressions: none
- Python-only differences: none

## Interpretation

- Outcome mismatches: 1
- The browser dominates wall time. Treat serial Go/Python speed as a parity check, not proof that Go will always outrun Python.
- gomoufox should still win the agent-output footprint. A report-token ratio above 0.50 is a regression.
- gomoufox benefits outside this run include typed Go integration, CLI and MCP surfaces, local URL guardrails, and repeatable checks against Python Camoufox.

## Per Loop

| Loop | Runtime | Passed | Blocked | Failed | Wall ms | Target ms | Peak RSS MiB | Peak CPU % | Mismatches |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | gomoufox | 87 | 13 | 0 | 355,444 | 352,484 | 2,592.9 | 343.5 | 1 |
| 1 | Python Camoufox | 88 | 12 | 0 | 352,594 | 350,412 | 2,662.7 | 439.9 | 1 |
| 2 | gomoufox | 89 | 11 | 0 | 349,198 | 346,030 | 2,276.2 | 383.9 | 0 |
| 2 | Python Camoufox | 89 | 11 | 0 | 357,468 | 355,119 | 2,820.7 | 382.8 | 0 |

## Target Outcomes

| Target | Kind | Tags | Go | Python | Go ms | Python ms |
|---|---|---|---:|---:|---:|---:|
| adidas | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,569 | 3,672 |
| airbnb | travel-platform | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,510 | 3,570 |
| akamai | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,453 | 3,523 |
| angular | framework-docs | docs, spa | passed | passed | 3,474 | 3,494 |
| ansible | platform-docs | docs, media-heavy | passed | passed | 3,596 | 3,589 |
| anthropic | ai-platform | auth-entry, media-heavy | passed | passed | 3,330 | 3,307 |
| ap-news | news-site | media-heavy | blocked | blocked | 3,512 | 3,389 |
| apple | tech-platform | media-heavy, spa | passed | passed | 3,333 | 3,318 |
| astro | framework-docs | docs, static | passed | passed | 3,388 | 3,383 |
| aws | cloud-platform | auth-entry, cloud-platform, media-heavy | passed | passed | 3,341 | 3,362 |
| azure | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,502 | 4,203 |
| bbc | news-site | media-heavy | passed | passed | 3,306 | 3,328 |
| bestbuy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,514 | 3,740 |
| booking | travel-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,336 | 3,371 |
| chrome-dev | browser-docs | docs | passed | passed | 3,619 | 4,329 |
| cloudflare-docs | cdn-security-docs | cloudflare, cdn-security, docs | passed | passed | 3,359 | 3,434 |
| cloudflare-home | cloudflare-edge | cloudflare, cdn-security, spa | passed | passed | 3,437 | 3,451 |
| cloudflare-radar | cdn-security-docs | cloudflare, cdn-security, docs, spa | passed | passed | 3,497 | 3,475 |
| cnn | news-site | media-heavy, spa | passed | passed | 3,305 | 3,281 |
| cohere | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,547 | 3,641 |
| costco | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,310 | 3,337 |
| crates | package-registry | docs | passed | passed | 3,365 | 3,352 |
| creepjs | fingerprint-test | bot-detection, static | passed | passed | 3,336 | 3,337 |
| datadome | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,422 | 3,443 |
| digitalocean | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,588 | 3,291 |
| django | framework-docs | docs, static | passed | passed | 3,453 | 3,455 |
| docker-docs | platform-docs | docs, spa | passed | passed | 3,344 | 3,362 |
| docker-hub | package-registry | auth-entry, spa | passed | passed | 3,692 | 3,437 |
| ebay | marketplace | ecommerce, auth-entry, media-heavy | blocked | blocked | 3,590 | 3,347 |
| elastic | platform-docs | auth-entry, docs, spa | passed | passed | 3,322 | 3,577 |
| etsy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,586 | 3,601 |
| example | reference-site | static | passed | passed | 3,308 | 3,400 |
| fastly | cdn-security-vendor | cdn-security, media-heavy | blocked | blocked | 3,297 | 3,307 |
| firebase | cloud-platform | cloud-platform, docs, spa | passed | passed | 3,539 | 3,808 |
| flask | framework-docs | docs, static | passed | passed | 3,432 | 3,497 |
| g2 | real-site-anti-bot | bot-detection, auth-entry, spa | blocked | blocked | 3,624 | 3,331 |
| github | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,414 | 3,386 |
| gitlab | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,576 | 3,570 |
| go-dev | language-docs | docs, static | passed | passed | 3,412 | 3,548 |
| google-cloud | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,515 | 3,492 |
| grafana | platform-docs | auth-entry, docs, spa | passed | passed | 3,354 | 3,395 |
| guardian | news-site | media-heavy | passed | passed | 3,332 | 3,314 |
| hacker-news | community-site | auth-entry, static | passed | passed | 3,294 | 3,331 |
| huggingface | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,296 | 3,279 |
| ikea | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,395 | 4,582 |
| imperva | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,581 | 3,376 |
| incolumitas | bot-fingerprint-test | bot-detection | passed | passed | 4,010 | 3,906 |
| kubernetes | platform-docs | docs, spa | passed | passed | 3,293 | 3,307 |
| linode | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,420 | 3,436 |
| mdn | browser-docs | docs, media-heavy | passed | passed | 3,324 | 3,292 |
| microsoft | tech-platform | auth-entry, media-heavy | passed | passed | 3,395 | 3,403 |
| mistral-ai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,438 | 3,379 |
| mozilla | browser-platform | docs, media-heavy | passed | passed | 3,299 | 3,580 |
| mysql | database-docs | docs, media-heavy | passed | passed | 3,414 | 3,611 |
| netlify | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,960 | 3,550 |
| nextjs | framework-docs | docs, spa | passed | passed | 3,357 | 3,961 |
| nginx | platform-docs | docs, static | passed | passed | 3,974 | 3,905 |
| nike | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,737 | 7,295 |
| nodejs | language-docs | docs | passed | passed | 3,416 | 3,418 |
| nowsecure-cloudflare | cloudflare-challenge-test | cloudflare, bot-detection | blocked | blocked | 4,285 | 4,312 |
| npmjs | package-registry | auth-entry, spa | passed | passed | 3,289 | 3,329 |
| npr | news-site | media-heavy | passed | passed | 3,367 | 3,390 |
| openai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,550 | 3,531 |
| oracle-cloud | cloud-platform | auth-entry, cloud-platform, media-heavy | blocked | blocked | 3,526 | 3,434 |
| paypal | payments-platform | ecommerce, auth-entry | passed | passed | 3,460 | 3,436 |
| perplexity | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,429 | 3,414 |
| pixelscan | bot-fingerprint-test | bot-detection | passed | passed | 3,541 | 3,512 |
| pkg-go-dev | language-docs | docs, static | passed | passed | 3,416 | 3,391 |
| playwright | browser-automation-docs | docs, static | passed | passed | 3,297 | 3,322 |
| postgres | database-docs | docs, static | passed | passed | 3,555 | 3,544 |
| prometheus | platform-docs | docs, static | passed | passed | 3,518 | 3,504 |
| pypi | package-registry | docs, static | passed | passed | 3,264 | 3,274 |
| python-org | language-docs | docs | passed | passed | 3,256 | 3,252 |
| rails | framework-docs | docs, static | passed | passed | 3,272 | 3,659 |
| react | framework-docs | docs, spa | passed | passed | 3,318 | 3,351 |
| redis | database-docs | docs, spa | passed | passed | 3,516 | 3,489 |
| reuters | news-site | media-heavy | passed | passed | 3,330 | 3,328 |
| rubygems | package-registry | docs | passed | passed | 3,319 | 3,389 |
| rust-lang | language-docs | docs, static | passed | passed | 3,395 | 3,364 |
| sannysoft | bot-fingerprint-test | bot-detection | passed | passed | 3,387 | 3,398 |
| selenium | browser-automation-docs | docs | passed | passed | 3,350 | 3,340 |
| shopify | commerce-platform | ecommerce, auth-entry, spa | passed | passed | 3,366 | 3,357 |
| spring | framework-docs | docs, spa | passed | passed | 3,361 | 3,491 |
| sqlite | database-docs | docs, static | passed | passed | 3,575 | 3,573 |
| stackoverflow | developer-platform | auth-entry, developer-platform | blocked | blocked | 3,529 | 3,510 |
| stripe | payments-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,506 | 3,517 |
| supabase | developer-platform | auth-entry, developer-platform, docs, spa | passed | passed | 3,338 | 3,361 |
| svelte | framework-docs | docs, spa | passed | passed | 3,386 | 3,342 |
| tailwind | framework-docs | docs, spa | passed | passed | 3,351 | 3,433 |
| target | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,799 | 3,805 |
| terraform | platform-docs | docs | passed | passed | 3,568 | 3,507 |
| uniqlo | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,462 | 4,277 |
| vercel | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,361 | 3,363 |
| vite | framework-docs | docs, spa | passed | passed | 3,301 | 3,328 |
| vue | framework-docs | docs, spa | passed | passed | 3,730 | 3,733 |
| walmart | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,354 | 4,289 |
| web-dev | browser-docs | docs, static | passed | passed | 3,696 | 3,705 |
| wikipedia | reference-site | static, media-heavy | passed | passed | 3,309 | 3,304 |
| yahoo-news | news-site | media-heavy, spa | passed | passed | 3,607 | 3,711 |
| ycombinator | startup-platform | auth-entry, static | passed | passed | 3,449 | 3,492 |
