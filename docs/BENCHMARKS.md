# Go/Python Benchmark

- Generated: 2026-10-12T21:44:09.457015+00:00
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
- Go runner: built_binary
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
| gomoufox | 90 | 10 | 0 | 347,170 | 345,410 | 3,063.5 | 584.0 |
| Python Camoufox | 90 | 10 | 0 | 346,618 | 345,053 | 2,671.0 | 493.9 |

| Ratio | Go / Python |
|---|---:|
| Wall time | 1.002 |
| Target duration | 1.001 |
| Peak RSS | 1.147 |
| Peak CPU | 1.182 |
| Report tokens | 0.158 |

## Go/Python Benchmark Readiness

- Status: blocked
- Candidate: no
- Note: Go/Python benchmark candidate means node-direct passed the extended comparison gate. Consumer no-Python readiness is recorded separately by scripts/no-python-consumer-canary.sh.

| Criterion | Passed | Detail |
|---|---:|---|
| complete_harness_resource_scope | yes | resource_scope=harness_process_tree |
| fixed_period_resource_sampling | yes | resource_sampling=fixed_period |
| go_sidecar_runtime_is_node_direct | yes | go_sidecar_runtime=node-direct |
| shared_linux_persona_bundle | yes | persona_os=linux persona_bundle_sha256=d787a57c6677d1f793bff5c25a2b624b77c9d808ff011a62b129fbd46166c360 |
| extended_target_matrix | yes | mode=extended targets=100 |
| no_go_only_outcome_regressions | yes | go_only_regression_count=0 outcome_mismatch_count=0 |
| no_runtime_failures | yes | go_failed=0 python_failed=0 |
| wall_time_not_slower_than_python | yes | wall_time=1.001592531259196 max=1.05 |
| target_duration_not_slower_than_python | yes | target_duration=1.0010346236665093 max=1.05 |
| peak_rss_beats_python | no | peak_rss=1.1469168806706171 max=0.95 |
| peak_cpu_beats_python | no | peak_cpu=1.1824255922251468 max=0.95 |
| report_tokens_beats_python | yes | report_tokens=0.15781627574267543 max=0.5 |

## Agent Output Footprint

Token estimates use `ceil(bytes / 4)`. They compare generated benchmark artifacts, not model billing.

| Runtime | JSON bytes | Markdown bytes | Estimated tokens |
|---|---:|---:|---:|
| gomoufox | 43,408 | 9,096 | 13,127 |
| Python Camoufox | 323,625 | 9,090 | 83,179 |

## Outcome Classes

- Shared blocked: `ap-news`, `ebay`, `fastly`, `g2`, `huggingface`, `nowsecure-cloudflare`, `oracle-cloud`, `perplexity`, `stackoverflow`, `uniqlo`
- Shared failed: none
- Go-only regressions: none
- Python-only differences: none

## Interpretation

- Outcome mismatches: 0
- The browser dominates wall time. Treat serial Go/Python speed as a parity check, not proof that Go will always outrun Python.
- gomoufox should still win the agent-output footprint. A report-token ratio above 0.50 is a regression.
- gomoufox benefits outside this run include typed Go integration, CLI and MCP surfaces, local URL guardrails, and repeatable checks against Python Camoufox.

## Per Loop

| Loop | Runtime | Passed | Blocked | Failed | Wall ms | Target ms | Peak RSS MiB | Peak CPU % | Mismatches |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | gomoufox | 91 | 9 | 0 | 345,011 | 343,311 | 2,986.5 | 498.5 | 0 |
| 1 | Python Camoufox | 91 | 9 | 0 | 347,337 | 345,695 | 2,671.0 | 397.5 | 0 |
| 2 | gomoufox | 90 | 10 | 0 | 349,330 | 347,509 | 3,063.5 | 584.0 | 0 |
| 2 | Python Camoufox | 90 | 10 | 0 | 345,899 | 344,412 | 2,620.1 | 493.9 | 0 |

## Target Outcomes

| Target | Kind | Tags | Go | Python | Go ms | Python ms |
|---|---|---|---:|---:|---:|---:|
| adidas | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,691 | 3,599 |
| airbnb | travel-platform | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,803 | 3,612 |
| akamai | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,286 | 3,504 |
| angular | framework-docs | docs, spa | passed | passed | 3,418 | 3,459 |
| ansible | platform-docs | docs, media-heavy | passed | passed | 3,829 | 3,947 |
| anthropic | ai-platform | auth-entry, media-heavy | passed | passed | 3,267 | 3,409 |
| ap-news | news-site | media-heavy | blocked | blocked | 3,266 | 3,288 |
| apple | tech-platform | media-heavy, spa | passed | passed | 3,367 | 3,340 |
| astro | framework-docs | docs, static | passed | passed | 3,276 | 3,381 |
| aws | cloud-platform | auth-entry, cloud-platform, media-heavy | passed | passed | 3,554 | 3,352 |
| azure | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,872 | 3,571 |
| bbc | news-site | media-heavy | passed | passed | 3,498 | 3,290 |
| bestbuy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,410 | 3,446 |
| booking | travel-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,358 | 3,497 |
| chrome-dev | browser-docs | docs | passed | passed | 3,629 | 3,484 |
| cloudflare-docs | cdn-security-docs | cloudflare, cdn-security, docs | passed | passed | 3,324 | 3,299 |
| cloudflare-home | cloudflare-edge | cloudflare, cdn-security, spa | passed | passed | 3,527 | 3,380 |
| cloudflare-radar | cdn-security-docs | cloudflare, cdn-security, docs, spa | passed | passed | 3,345 | 3,547 |
| cnn | news-site | media-heavy, spa | passed | passed | 3,321 | 3,263 |
| cohere | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,418 | 3,326 |
| costco | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,281 | 3,264 |
| crates | package-registry | docs | passed | passed | 3,313 | 3,310 |
| creepjs | fingerprint-test | bot-detection, static | passed | passed | 3,205 | 3,469 |
| datadome | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,374 | 3,370 |
| digitalocean | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,312 | 3,294 |
| django | framework-docs | docs, static | passed | passed | 3,386 | 3,389 |
| docker-docs | platform-docs | docs, spa | passed | passed | 3,357 | 3,359 |
| docker-hub | package-registry | auth-entry, spa | passed | passed | 3,355 | 3,368 |
| ebay | marketplace | ecommerce, auth-entry, media-heavy | blocked | blocked | 3,420 | 3,335 |
| elastic | platform-docs | auth-entry, docs, spa | passed | passed | 3,278 | 3,343 |
| etsy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 8,276 | 3,660 |
| example | reference-site | static | passed | passed | 3,426 | 3,235 |
| fastly | cdn-security-vendor | cdn-security, media-heavy | blocked | blocked | 3,222 | 3,242 |
| firebase | cloud-platform | cloud-platform, docs, spa | passed | passed | 3,452 | 3,519 |
| flask | framework-docs | docs, static | passed | passed | 3,272 | 3,379 |
| g2 | real-site-anti-bot | bot-detection, auth-entry, spa | blocked | blocked | 3,229 | 3,266 |
| github | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,481 | 3,329 |
| gitlab | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,504 | 3,525 |
| go-dev | language-docs | docs, static | passed | passed | 3,404 | 3,354 |
| google-cloud | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,460 | 3,413 |
| grafana | platform-docs | auth-entry, docs, spa | passed | passed | 3,319 | 3,459 |
| guardian | news-site | media-heavy | passed | passed | 3,266 | 3,339 |
| hacker-news | community-site | auth-entry, static | passed | passed | 3,251 | 3,347 |
| huggingface | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,232 | 3,498 |
| ikea | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,281 | 3,274 |
| imperva | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,489 | 3,379 |
| incolumitas | bot-fingerprint-test | bot-detection | passed | passed | 4,006 | 3,958 |
| kubernetes | platform-docs | docs, spa | passed | passed | 3,256 | 3,279 |
| linode | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,442 | 3,554 |
| mdn | browser-docs | docs, media-heavy | passed | passed | 3,253 | 3,268 |
| microsoft | tech-platform | auth-entry, media-heavy | passed | passed | 3,677 | 3,436 |
| mistral-ai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,435 | 3,329 |
| mozilla | browser-platform | docs, media-heavy | passed | passed | 3,310 | 3,559 |
| mysql | database-docs | docs, media-heavy | passed | passed | 3,934 | 3,323 |
| netlify | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,465 | 3,900 |
| nextjs | framework-docs | docs, spa | passed | passed | 3,276 | 3,313 |
| nginx | platform-docs | docs, static | passed | passed | 3,920 | 4,072 |
| nike | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,597 | 3,750 |
| nodejs | language-docs | docs | passed | passed | 3,355 | 3,399 |
| nowsecure-cloudflare | cloudflare-challenge-test | cloudflare, bot-detection | blocked | blocked | 4,199 | 4,286 |
| npmjs | package-registry | auth-entry, spa | passed | passed | 3,271 | 3,504 |
| npr | news-site | media-heavy | passed | passed | 3,472 | 3,423 |
| openai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,612 | 3,473 |
| oracle-cloud | cloud-platform | auth-entry, cloud-platform, media-heavy | blocked | blocked | 3,417 | 3,419 |
| paypal | payments-platform | ecommerce, auth-entry | passed | passed | 3,502 | 3,394 |
| perplexity | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,274 | 3,391 |
| pixelscan | bot-fingerprint-test | bot-detection | passed | passed | 4,084 | 4,242 |
| pkg-go-dev | language-docs | docs, static | passed | passed | 3,310 | 3,308 |
| playwright | browser-automation-docs | docs, static | passed | passed | 3,348 | 3,367 |
| postgres | database-docs | docs, static | passed | passed | 3,463 | 3,618 |
| prometheus | platform-docs | docs, static | passed | passed | 3,454 | 3,434 |
| pypi | package-registry | docs, static | passed | passed | 3,202 | 3,235 |
| python-org | language-docs | docs | passed | passed | 3,206 | 3,243 |
| rails | framework-docs | docs, static | passed | passed | 3,249 | 3,250 |
| react | framework-docs | docs, spa | passed | passed | 3,270 | 3,285 |
| redis | database-docs | docs, spa | passed | passed | 3,436 | 3,428 |
| reuters | news-site | media-heavy | passed | passed | 3,313 | 3,386 |
| rubygems | package-registry | docs | passed | passed | 3,226 | 3,266 |
| rust-lang | language-docs | docs, static | passed | passed | 3,244 | 3,292 |
| sannysoft | bot-fingerprint-test | bot-detection | passed | passed | 3,385 | 3,422 |
| selenium | browser-automation-docs | docs | passed | passed | 3,291 | 3,379 |
| shopify | commerce-platform | ecommerce, auth-entry, spa | passed | passed | 3,311 | 3,381 |
| spring | framework-docs | docs, spa | passed | passed | 3,318 | 3,295 |
| sqlite | database-docs | docs, static | passed | passed | 3,474 | 3,505 |
| stackoverflow | developer-platform | auth-entry, developer-platform | blocked | blocked | 3,396 | 3,408 |
| stripe | payments-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,512 | 3,381 |
| supabase | developer-platform | auth-entry, developer-platform, docs, spa | passed | passed | 3,352 | 3,307 |
| svelte | framework-docs | docs, spa | passed | passed | 3,247 | 3,309 |
| tailwind | framework-docs | docs, spa | passed | passed | 3,347 | 3,408 |
| target | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,819 | 3,882 |
| terraform | platform-docs | docs | passed | passed | 3,456 | 3,491 |
| uniqlo | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,319 | 3,308 |
| vercel | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,300 | 3,436 |
| vite | framework-docs | docs, spa | passed | passed | 3,307 | 3,283 |
| vue | framework-docs | docs, spa | passed | passed | 3,320 | 3,789 |
| walmart | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,339 | 3,545 |
| web-dev | browser-docs | docs, static | passed | passed | 3,582 | 3,598 |
| wikipedia | reference-site | static, media-heavy | passed | passed | 3,259 | 3,253 |
| yahoo-news | news-site | media-heavy, spa | passed | passed | 3,750 | 3,537 |
| ycombinator | startup-platform | auth-entry, static | passed | passed | 3,643 | 3,398 |
