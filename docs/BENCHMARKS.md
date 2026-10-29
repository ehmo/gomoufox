# Go/Python Benchmark

- Generated: 2026-10-29T01:42:22.915019+00:00
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
| gomoufox | 86 | 14 | 0 | 349,292 | 347,265 | 2,863.0 | 380.6 |
| Python Camoufox | 88 | 12 | 0 | 348,015 | 346,069 | 3,072.3 | 608.8 |

| Ratio | Go / Python |
|---|---:|
| Wall time | 1.004 |
| Target duration | 1.003 |
| Peak RSS | 0.932 |
| Peak CPU | 0.625 |
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
| no_go_only_outcome_regressions | yes | go_only_regression_count=0 outcome_mismatch_count=4 |
| no_runtime_failures | yes | go_failed=0 python_failed=0 |
| wall_time_not_slower_than_python | yes | wall_time=1.003669382066865 max=1.05 |
| target_duration_not_slower_than_python | yes | target_duration=1.0034559582048665 max=1.05 |
| peak_rss_beats_python | yes | peak_rss=0.9318771296343386 max=0.95 |
| peak_cpu_beats_python | yes | peak_cpu=0.6251642575558477 max=0.95 |
| report_tokens_beats_python | yes | report_tokens=0.1586443771917183 max=0.5 |

## Agent Output Footprint

Token estimates use `ceil(bytes / 4)`. They compare generated benchmark artifacts, not model billing.

| Runtime | JSON bytes | Markdown bytes | Estimated tokens |
|---|---:|---:|---:|
| gomoufox | 43,653 | 9,187 | 13,210 |
| Python Camoufox | 323,851 | 9,215 | 83,268 |

## Outcome Classes

- Shared blocked: `adidas`, `ap-news`, `ebay`, `fastly`, `g2`, `huggingface`, `nowsecure-cloudflare`, `npmjs`, `oracle-cloud`, `perplexity`, `stackoverflow`, `uniqlo`
- Shared failed: none
- Go-only regressions: `cloudflare-radar`, `openai`
- Python-only differences: none

## Interpretation

- Outcome mismatches: 4
- The browser dominates wall time. Treat serial Go/Python speed as a parity check, not proof that Go will always outrun Python.
- gomoufox should still win the agent-output footprint. A report-token ratio above 0.50 is a regression.
- gomoufox benefits outside this run include typed Go integration, CLI and MCP surfaces, local URL guardrails, and repeatable checks against Python Camoufox.

## Per Loop

| Loop | Runtime | Passed | Blocked | Failed | Wall ms | Target ms | Peak RSS MiB | Peak CPU % | Mismatches |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | gomoufox | 89 | 11 | 0 | 350,627 | 348,691 | 2,536.3 | 380.6 | 2 |
| 1 | Python Camoufox | 87 | 13 | 0 | 348,289 | 346,596 | 2,487.3 | 470.4 | 2 |
| 2 | gomoufox | 86 | 14 | 0 | 347,958 | 345,839 | 2,863.0 | 336.5 | 2 |
| 2 | Python Camoufox | 88 | 12 | 0 | 347,741 | 345,543 | 3,072.3 | 608.8 | 2 |

## Target Outcomes

| Target | Kind | Tags | Go | Python | Go ms | Python ms |
|---|---|---|---:|---:|---:|---:|
| adidas | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,531 | 3,646 |
| airbnb | travel-platform | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,807 | 3,594 |
| akamai | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,492 | 3,379 |
| angular | framework-docs | docs, spa | passed | passed | 3,428 | 3,298 |
| ansible | platform-docs | docs, media-heavy | passed | passed | 3,444 | 3,600 |
| anthropic | ai-platform | auth-entry, media-heavy | passed | passed | 3,379 | 3,368 |
| ap-news | news-site | media-heavy | blocked | blocked | 3,429 | 3,432 |
| apple | tech-platform | media-heavy, spa | passed | passed | 3,285 | 3,367 |
| astro | framework-docs | docs, static | passed | passed | 3,438 | 3,329 |
| aws | cloud-platform | auth-entry, cloud-platform, media-heavy | passed | passed | 3,380 | 3,413 |
| azure | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,481 | 3,977 |
| bbc | news-site | media-heavy | passed | passed | 3,314 | 3,370 |
| bestbuy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,459 | 3,509 |
| booking | travel-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,453 | 3,937 |
| chrome-dev | browser-docs | docs | passed | passed | 4,013 | 3,628 |
| cloudflare-docs | cdn-security-docs | cloudflare, cdn-security, docs | passed | passed | 3,316 | 3,342 |
| cloudflare-home | cloudflare-edge | cloudflare, cdn-security, spa | passed | passed | 3,442 | 3,450 |
| cloudflare-radar | cdn-security-docs | cloudflare, cdn-security, docs, spa | blocked | passed | 3,428 | 3,525 |
| cnn | news-site | media-heavy, spa | passed | passed | 3,560 | 3,250 |
| cohere | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,332 | 3,388 |
| costco | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,353 | 3,269 |
| crates | package-registry | docs | passed | passed | 3,572 | 3,333 |
| creepjs | fingerprint-test | bot-detection, static | passed | passed | 3,219 | 3,302 |
| datadome | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,282 | 3,337 |
| digitalocean | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,310 | 3,292 |
| django | framework-docs | docs, static | passed | passed | 3,422 | 3,424 |
| docker-docs | platform-docs | docs, spa | passed | passed | 3,326 | 3,314 |
| docker-hub | package-registry | auth-entry, spa | passed | passed | 3,444 | 3,418 |
| ebay | marketplace | ecommerce, auth-entry, media-heavy | blocked | blocked | 3,813 | 3,469 |
| elastic | platform-docs | auth-entry, docs, spa | passed | passed | 3,296 | 3,360 |
| etsy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,729 | 3,615 |
| example | reference-site | static | passed | passed | 3,242 | 3,260 |
| fastly | cdn-security-vendor | cdn-security, media-heavy | blocked | blocked | 3,287 | 3,248 |
| firebase | cloud-platform | cloud-platform, docs, spa | passed | passed | 4,486 | 3,634 |
| flask | framework-docs | docs, static | passed | passed | 3,383 | 3,350 |
| g2 | real-site-anti-bot | bot-detection, auth-entry, spa | blocked | blocked | 3,297 | 3,300 |
| github | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,385 | 3,347 |
| gitlab | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,523 | 3,584 |
| go-dev | language-docs | docs, static | passed | passed | 3,408 | 3,390 |
| google-cloud | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,459 | 3,443 |
| grafana | platform-docs | auth-entry, docs, spa | passed | passed | 3,343 | 3,501 |
| guardian | news-site | media-heavy | passed | passed | 3,248 | 3,267 |
| hacker-news | community-site | auth-entry, static | passed | passed | 3,372 | 3,290 |
| huggingface | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,270 | 3,258 |
| ikea | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,453 | 3,396 |
| imperva | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,312 | 3,311 |
| incolumitas | bot-fingerprint-test | bot-detection | passed | passed | 4,032 | 3,904 |
| kubernetes | platform-docs | docs, spa | passed | passed | 3,287 | 3,279 |
| linode | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,742 | 3,915 |
| mdn | browser-docs | docs, media-heavy | passed | passed | 3,323 | 3,269 |
| microsoft | tech-platform | auth-entry, media-heavy | passed | passed | 3,442 | 3,530 |
| mistral-ai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,455 | 3,477 |
| mozilla | browser-platform | docs, media-heavy | passed | passed | 3,581 | 3,586 |
| mysql | database-docs | docs, media-heavy | passed | passed | 3,316 | 3,448 |
| netlify | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,408 | 3,403 |
| nextjs | framework-docs | docs, spa | passed | passed | 3,346 | 3,443 |
| nginx | platform-docs | docs, static | passed | passed | 3,836 | 3,990 |
| nike | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,603 | 3,546 |
| nodejs | language-docs | docs | passed | passed | 3,423 | 3,490 |
| nowsecure-cloudflare | cloudflare-challenge-test | cloudflare, bot-detection | blocked | blocked | 4,298 | 4,806 |
| npmjs | package-registry | auth-entry, spa | blocked | blocked | 3,344 | 3,341 |
| npr | news-site | media-heavy | passed | passed | 3,449 | 3,510 |
| openai | ai-platform | auth-entry, media-heavy, spa | blocked | passed | 3,648 | 3,535 |
| oracle-cloud | cloud-platform | auth-entry, cloud-platform, media-heavy | blocked | blocked | 3,353 | 3,344 |
| paypal | payments-platform | ecommerce, auth-entry | passed | passed | 3,575 | 3,893 |
| perplexity | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,398 | 3,358 |
| pixelscan | bot-fingerprint-test | bot-detection | passed | passed | 3,831 | 3,491 |
| pkg-go-dev | language-docs | docs, static | passed | passed | 3,344 | 3,328 |
| playwright | browser-automation-docs | docs, static | passed | passed | 3,276 | 3,219 |
| postgres | database-docs | docs, static | passed | passed | 3,405 | 3,400 |
| prometheus | platform-docs | docs, static | passed | passed | 3,389 | 3,450 |
| pypi | package-registry | docs, static | passed | passed | 3,227 | 3,226 |
| python-org | language-docs | docs | passed | passed | 3,229 | 3,219 |
| rails | framework-docs | docs, static | passed | passed | 3,424 | 3,224 |
| react | framework-docs | docs, spa | passed | passed | 3,383 | 3,287 |
| redis | database-docs | docs, spa | passed | passed | 3,532 | 3,482 |
| reuters | news-site | media-heavy | passed | passed | 3,340 | 3,627 |
| rubygems | package-registry | docs | passed | passed | 3,316 | 3,383 |
| rust-lang | language-docs | docs, static | passed | passed | 3,288 | 3,286 |
| sannysoft | bot-fingerprint-test | bot-detection | passed | passed | 3,377 | 3,600 |
| selenium | browser-automation-docs | docs | passed | passed | 3,295 | 3,298 |
| shopify | commerce-platform | ecommerce, auth-entry, spa | passed | passed | 3,371 | 3,369 |
| spring | framework-docs | docs, spa | passed | passed | 3,385 | 3,377 |
| sqlite | database-docs | docs, static | passed | passed | 3,544 | 3,531 |
| stackoverflow | developer-platform | auth-entry, developer-platform | blocked | blocked | 3,458 | 3,492 |
| stripe | payments-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,469 | 3,473 |
| supabase | developer-platform | auth-entry, developer-platform, docs, spa | passed | passed | 3,341 | 3,354 |
| svelte | framework-docs | docs, spa | passed | passed | 3,469 | 3,263 |
| tailwind | framework-docs | docs, spa | passed | passed | 3,378 | 3,293 |
| target | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 4,006 | 3,795 |
| terraform | platform-docs | docs | passed | passed | 3,559 | 3,563 |
| uniqlo | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,411 | 3,604 |
| vercel | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,316 | 3,328 |
| vite | framework-docs | docs, spa | passed | passed | 3,320 | 3,306 |
| vue | framework-docs | docs, spa | passed | passed | 3,331 | 3,271 |
| walmart | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,312 | 3,399 |
| web-dev | browser-docs | docs, static | passed | passed | 3,722 | 3,667 |
| wikipedia | reference-site | static, media-heavy | passed | passed | 3,276 | 3,280 |
| yahoo-news | news-site | media-heavy, spa | passed | passed | 3,549 | 3,499 |
| ycombinator | startup-platform | auth-entry, static | passed | passed | 3,462 | 3,578 |
