# Go/Python Benchmark

- Generated: 2026-10-28T20:56:02.714983+00:00
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
| gomoufox | 89 | 11 | 0 | 346,182 | 344,149 | 2,548.7 | 451.0 |
| Python Camoufox | 87 | 13 | 0 | 345,438 | 343,787 | 2,686.4 | 478.3 |

| Ratio | Go / Python |
|---|---:|
| Wall time | 1.002 |
| Target duration | 1.001 |
| Peak RSS | 0.949 |
| Peak CPU | 0.943 |
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
| no_go_only_outcome_regressions | yes | go_only_regression_count=0 outcome_mismatch_count=2 |
| no_runtime_failures | yes | go_failed=0 python_failed=0 |
| wall_time_not_slower_than_python | yes | wall_time=1.0021537873656055 max=1.05 |
| target_duration_not_slower_than_python | yes | target_duration=1.0010529775704142 max=1.05 |
| peak_rss_beats_python | yes | peak_rss=0.9487288357403346 max=0.95 |
| peak_cpu_beats_python | yes | peak_cpu=0.9429228517666736 max=0.95 |
| report_tokens_beats_python | yes | report_tokens=0.1586110710335209 max=0.5 |

## Agent Output Footprint

Token estimates use `ceil(bytes / 4)`. They compare generated benchmark artifacts, not model billing.

| Runtime | JSON bytes | Markdown bytes | Estimated tokens |
|---|---:|---:|---:|
| gomoufox | 43,620 | 9,146 | 13,192 |
| Python Camoufox | 323,513 | 9,172 | 83,172 |

## Outcome Classes

- Shared blocked: `adidas`, `ap-news`, `ebay`, `fastly`, `g2`, `huggingface`, `nowsecure-cloudflare`, `oracle-cloud`, `perplexity`, `stackoverflow`, `uniqlo`
- Shared failed: none
- Go-only regressions: none
- Python-only differences: `cloudflare-radar`, `npmjs`

## Interpretation

- Outcome mismatches: 2
- The browser dominates wall time. Treat serial Go/Python speed as a parity check, not proof that Go will always outrun Python.
- gomoufox should still win the agent-output footprint. A report-token ratio above 0.50 is a regression.
- gomoufox benefits outside this run include typed Go integration, CLI and MCP surfaces, local URL guardrails, and repeatable checks against Python Camoufox.

## Per Loop

| Loop | Runtime | Passed | Blocked | Failed | Wall ms | Target ms | Peak RSS MiB | Peak CPU % | Mismatches |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | gomoufox | 89 | 11 | 0 | 348,949 | 346,947 | 2,548.7 | 360.7 | 0 |
| 1 | Python Camoufox | 89 | 11 | 0 | 345,885 | 344,216 | 2,581.1 | 442.0 | 0 |
| 2 | gomoufox | 89 | 11 | 0 | 343,416 | 341,352 | 2,275.1 | 451.0 | 2 |
| 2 | Python Camoufox | 87 | 13 | 0 | 344,992 | 343,358 | 2,686.4 | 478.3 | 2 |

## Target Outcomes

| Target | Kind | Tags | Go | Python | Go ms | Python ms |
|---|---|---|---:|---:|---:|---:|
| adidas | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,479 | 3,504 |
| airbnb | travel-platform | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,587 | 3,574 |
| akamai | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,329 | 3,333 |
| angular | framework-docs | docs, spa | passed | passed | 3,363 | 3,287 |
| ansible | platform-docs | docs, media-heavy | passed | passed | 3,506 | 3,441 |
| anthropic | ai-platform | auth-entry, media-heavy | passed | passed | 3,355 | 3,398 |
| ap-news | news-site | media-heavy | blocked | blocked | 3,309 | 3,409 |
| apple | tech-platform | media-heavy, spa | passed | passed | 3,296 | 3,371 |
| astro | framework-docs | docs, static | passed | passed | 3,359 | 3,317 |
| aws | cloud-platform | auth-entry, cloud-platform, media-heavy | passed | passed | 3,386 | 3,356 |
| azure | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,578 | 3,806 |
| bbc | news-site | media-heavy | passed | passed | 3,264 | 3,302 |
| bestbuy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,697 | 3,609 |
| booking | travel-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,330 | 3,507 |
| chrome-dev | browser-docs | docs | passed | passed | 3,473 | 3,620 |
| cloudflare-docs | cdn-security-docs | cloudflare, cdn-security, docs | passed | passed | 3,321 | 3,307 |
| cloudflare-home | cloudflare-edge | cloudflare, cdn-security, spa | passed | passed | 3,449 | 3,395 |
| cloudflare-radar | cdn-security-docs | cloudflare, cdn-security, docs, spa | passed | blocked | 3,591 | 3,291 |
| cnn | news-site | media-heavy, spa | passed | passed | 3,237 | 3,223 |
| cohere | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,343 | 3,334 |
| costco | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,289 | 3,358 |
| crates | package-registry | docs | passed | passed | 3,325 | 3,395 |
| creepjs | fingerprint-test | bot-detection, static | passed | passed | 3,313 | 3,214 |
| datadome | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,421 | 3,279 |
| digitalocean | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,275 | 3,372 |
| django | framework-docs | docs, static | passed | passed | 3,449 | 3,434 |
| docker-docs | platform-docs | docs, spa | passed | passed | 3,306 | 3,388 |
| docker-hub | package-registry | auth-entry, spa | passed | passed | 3,491 | 3,402 |
| ebay | marketplace | ecommerce, auth-entry, media-heavy | blocked | blocked | 3,416 | 3,431 |
| elastic | platform-docs | auth-entry, docs, spa | passed | passed | 3,311 | 3,620 |
| etsy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,587 | 3,619 |
| example | reference-site | static | passed | passed | 3,263 | 3,262 |
| fastly | cdn-security-vendor | cdn-security, media-heavy | blocked | blocked | 3,243 | 3,230 |
| firebase | cloud-platform | cloud-platform, docs, spa | passed | passed | 3,587 | 3,598 |
| flask | framework-docs | docs, static | passed | passed | 3,540 | 3,341 |
| g2 | real-site-anti-bot | bot-detection, auth-entry, spa | blocked | blocked | 3,264 | 3,275 |
| github | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,415 | 3,404 |
| gitlab | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,521 | 3,551 |
| go-dev | language-docs | docs, static | passed | passed | 3,449 | 3,527 |
| google-cloud | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,491 | 3,433 |
| grafana | platform-docs | auth-entry, docs, spa | passed | passed | 3,373 | 3,373 |
| guardian | news-site | media-heavy | passed | passed | 3,273 | 3,261 |
| hacker-news | community-site | auth-entry, static | passed | passed | 3,280 | 3,273 |
| huggingface | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,240 | 3,317 |
| ikea | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,362 | 3,422 |
| imperva | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,319 | 3,262 |
| incolumitas | bot-fingerprint-test | bot-detection | passed | passed | 4,047 | 3,918 |
| kubernetes | platform-docs | docs, spa | passed | passed | 3,302 | 3,307 |
| linode | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,398 | 3,900 |
| mdn | browser-docs | docs, media-heavy | passed | passed | 3,261 | 3,336 |
| microsoft | tech-platform | auth-entry, media-heavy | passed | passed | 3,473 | 3,404 |
| mistral-ai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,377 | 3,431 |
| mozilla | browser-platform | docs, media-heavy | passed | passed | 3,555 | 3,521 |
| mysql | database-docs | docs, media-heavy | passed | passed | 3,565 | 3,596 |
| netlify | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,454 | 3,499 |
| nextjs | framework-docs | docs, spa | passed | passed | 3,330 | 3,352 |
| nginx | platform-docs | docs, static | passed | passed | 3,846 | 3,860 |
| nike | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,532 | 3,496 |
| nodejs | language-docs | docs | passed | passed | 3,455 | 3,419 |
| nowsecure-cloudflare | cloudflare-challenge-test | cloudflare, bot-detection | blocked | blocked | 4,239 | 4,226 |
| npmjs | package-registry | auth-entry, spa | passed | blocked | 3,277 | 3,434 |
| npr | news-site | media-heavy | passed | passed | 3,449 | 3,441 |
| openai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,528 | 3,568 |
| oracle-cloud | cloud-platform | auth-entry, cloud-platform, media-heavy | blocked | blocked | 3,378 | 3,454 |
| paypal | payments-platform | ecommerce, auth-entry | passed | passed | 3,478 | 3,424 |
| perplexity | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,297 | 3,330 |
| pixelscan | bot-fingerprint-test | bot-detection | passed | passed | 3,519 | 3,829 |
| pkg-go-dev | language-docs | docs, static | passed | passed | 3,346 | 3,398 |
| playwright | browser-automation-docs | docs, static | passed | passed | 3,249 | 3,200 |
| postgres | database-docs | docs, static | passed | passed | 3,382 | 3,426 |
| prometheus | platform-docs | docs, static | passed | passed | 3,322 | 3,259 |
| pypi | package-registry | docs, static | passed | passed | 3,207 | 3,220 |
| python-org | language-docs | docs | passed | passed | 3,238 | 3,219 |
| rails | framework-docs | docs, static | passed | passed | 3,245 | 3,233 |
| react | framework-docs | docs, spa | passed | passed | 3,365 | 3,300 |
| redis | database-docs | docs, spa | passed | passed | 3,507 | 3,510 |
| reuters | news-site | media-heavy | passed | passed | 3,405 | 3,523 |
| rubygems | package-registry | docs | passed | passed | 3,248 | 3,265 |
| rust-lang | language-docs | docs, static | passed | passed | 3,311 | 3,312 |
| sannysoft | bot-fingerprint-test | bot-detection | passed | passed | 3,364 | 3,342 |
| selenium | browser-automation-docs | docs | passed | passed | 3,298 | 3,302 |
| shopify | commerce-platform | ecommerce, auth-entry, spa | passed | passed | 3,333 | 3,341 |
| spring | framework-docs | docs, spa | passed | passed | 3,343 | 3,432 |
| sqlite | database-docs | docs, static | passed | passed | 3,517 | 3,522 |
| stackoverflow | developer-platform | auth-entry, developer-platform | blocked | blocked | 3,465 | 3,425 |
| stripe | payments-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,457 | 3,420 |
| supabase | developer-platform | auth-entry, developer-platform, docs, spa | passed | passed | 3,316 | 3,384 |
| svelte | framework-docs | docs, spa | passed | passed | 3,342 | 3,263 |
| tailwind | framework-docs | docs, spa | passed | passed | 3,353 | 3,321 |
| target | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,903 | 3,844 |
| terraform | platform-docs | docs | passed | passed | 3,544 | 3,586 |
| uniqlo | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,463 | 3,368 |
| vercel | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,311 | 3,331 |
| vite | framework-docs | docs, spa | passed | passed | 3,263 | 3,276 |
| vue | framework-docs | docs, spa | passed | passed | 3,286 | 3,284 |
| walmart | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,348 | 4,002 |
| web-dev | browser-docs | docs, static | passed | passed | 3,584 | 3,622 |
| wikipedia | reference-site | static, media-heavy | passed | passed | 3,289 | 3,283 |
| yahoo-news | news-site | media-heavy, spa | passed | passed | 3,529 | 3,535 |
| ycombinator | startup-platform | auth-entry, static | passed | passed | 3,404 | 3,440 |
