# Go/Python Benchmark

- Generated: 2026-09-09T05:03:57.771492+00:00
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
- Go custom venv: yes
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
| gomoufox | 90 | 10 | 0 | 349,133 | 346,064 | 3,219.6 | 631.4 |
| Python Camoufox | 90 | 10 | 0 | 351,119 | 349,422 | 3,148.6 | 617.4 |

| Ratio | Go / Python |
|---|---:|
| Wall time | 0.994 |
| Target duration | 0.990 |
| Peak RSS | 1.023 |
| Peak CPU | 1.023 |
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
| wall_time_not_slower_than_python | yes | wall_time=0.9943437979716279 max=1.05 |
| target_duration_not_slower_than_python | yes | target_duration=0.9903898437991884 max=1.05 |
| peak_rss_beats_python | no | peak_rss=1.0225296756520703 max=0.95 |
| peak_cpu_beats_python | no | peak_cpu=1.0226757369614512 max=0.95 |
| report_tokens_beats_python | yes | report_tokens=0.157560462167787 max=0.5 |

## Agent Output Footprint

Token estimates use `ceil(bytes / 4)`. They compare generated benchmark artifacts, not model billing.

| Runtime | JSON bytes | Markdown bytes | Estimated tokens |
|---|---:|---:|---:|
| gomoufox | 43,576 | 9,118 | 13,173 |
| Python Camoufox | 325,291 | 9,128 | 83,606 |

## Outcome Classes

- Shared blocked: `adidas`, `ap-news`, `fastly`, `g2`, `huggingface`, `nowsecure-cloudflare`, `oracle-cloud`, `perplexity`, `stackoverflow`, `uniqlo`
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
| 1 | gomoufox | 90 | 10 | 0 | 348,325 | 346,401 | 3,219.6 | 631.4 | 0 |
| 1 | Python Camoufox | 90 | 10 | 0 | 347,522 | 345,854 | 3,148.6 | 559.3 | 0 |
| 2 | gomoufox | 90 | 10 | 0 | 349,941 | 345,728 | 2,717.5 | 435.0 | 0 |
| 2 | Python Camoufox | 90 | 10 | 0 | 354,717 | 352,990 | 3,108.8 | 617.4 | 0 |

## Target Outcomes

| Target | Kind | Tags | Go | Python | Go ms | Python ms |
|---|---|---|---:|---:|---:|---:|
| adidas | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,744 | 3,810 |
| airbnb | travel-platform | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,704 | 3,659 |
| akamai | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,374 | 3,524 |
| angular | framework-docs | docs, spa | passed | passed | 3,349 | 3,292 |
| ansible | platform-docs | docs, media-heavy | passed | passed | 3,556 | 3,538 |
| anthropic | ai-platform | auth-entry, media-heavy | passed | passed | 3,252 | 3,333 |
| ap-news | news-site | media-heavy | blocked | blocked | 3,327 | 3,297 |
| apple | tech-platform | media-heavy, spa | passed | passed | 3,273 | 3,272 |
| astro | framework-docs | docs, static | passed | passed | 3,485 | 3,327 |
| aws | cloud-platform | auth-entry, cloud-platform, media-heavy | passed | passed | 3,399 | 3,305 |
| azure | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,365 | 3,420 |
| bbc | news-site | media-heavy | passed | passed | 3,303 | 3,290 |
| bestbuy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,604 | 3,579 |
| booking | travel-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,480 | 3,488 |
| chrome-dev | browser-docs | docs | passed | passed | 3,797 | 3,706 |
| cloudflare-docs | cdn-security-docs | cloudflare, cdn-security, docs | passed | passed | 3,370 | 3,336 |
| cloudflare-home | cloudflare-edge | cloudflare, cdn-security, spa | passed | passed | 3,516 | 3,378 |
| cloudflare-radar | cdn-security-docs | cloudflare, cdn-security, docs, spa | passed | passed | 4,038 | 3,790 |
| cnn | news-site | media-heavy, spa | passed | passed | 3,293 | 3,266 |
| cohere | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,558 | 3,471 |
| costco | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,806 | 3,419 |
| crates | package-registry | docs | passed | passed | 3,374 | 3,302 |
| creepjs | fingerprint-test | bot-detection, static | passed | passed | 3,209 | 3,458 |
| datadome | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,546 | 3,719 |
| digitalocean | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,286 | 3,251 |
| django | framework-docs | docs, static | passed | passed | 3,452 | 3,429 |
| docker-docs | platform-docs | docs, spa | passed | passed | 3,308 | 3,299 |
| docker-hub | package-registry | auth-entry, spa | passed | passed | 4,152 | 3,456 |
| ebay | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,837 | 3,567 |
| elastic | platform-docs | auth-entry, docs, spa | passed | passed | 3,659 | 3,301 |
| etsy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,557 | 3,629 |
| example | reference-site | static | passed | passed | 3,261 | 3,341 |
| fastly | cdn-security-vendor | cdn-security, media-heavy | blocked | blocked | 3,296 | 3,219 |
| firebase | cloud-platform | cloud-platform, docs, spa | passed | passed | 3,925 | 3,512 |
| flask | framework-docs | docs, static | passed | passed | 3,488 | 11,641 |
| g2 | real-site-anti-bot | bot-detection, auth-entry, spa | blocked | blocked | 3,259 | 3,289 |
| github | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,328 | 3,379 |
| gitlab | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,530 | 3,537 |
| go-dev | language-docs | docs, static | passed | passed | 3,323 | 3,357 |
| google-cloud | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,389 | 3,451 |
| grafana | platform-docs | auth-entry, docs, spa | passed | passed | 3,352 | 3,307 |
| guardian | news-site | media-heavy | passed | passed | 3,361 | 3,302 |
| hacker-news | community-site | auth-entry, static | passed | passed | 3,261 | 3,256 |
| huggingface | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,251 | 3,243 |
| ikea | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,373 | 3,334 |
| imperva | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,331 | 3,298 |
| incolumitas | bot-fingerprint-test | bot-detection | passed | passed | 3,902 | 3,957 |
| kubernetes | platform-docs | docs, spa | passed | passed | 3,261 | 3,462 |
| linode | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,469 | 3,426 |
| mdn | browser-docs | docs, media-heavy | passed | passed | 3,284 | 3,735 |
| microsoft | tech-platform | auth-entry, media-heavy | passed | passed | 3,332 | 3,372 |
| mistral-ai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,448 | 3,428 |
| mozilla | browser-platform | docs, media-heavy | passed | passed | 3,553 | 3,270 |
| mysql | database-docs | docs, media-heavy | passed | passed | 3,276 | 3,270 |
| netlify | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,300 | 3,283 |
| nextjs | framework-docs | docs, spa | passed | passed | 3,334 | 4,334 |
| nginx | platform-docs | docs, static | passed | passed | 3,835 | 3,846 |
| nike | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,721 | 3,927 |
| nodejs | language-docs | docs | passed | passed | 3,391 | 3,505 |
| nowsecure-cloudflare | cloudflare-challenge-test | cloudflare, bot-detection | blocked | blocked | 4,197 | 4,190 |
| npmjs | package-registry | auth-entry, spa | passed | passed | 3,283 | 3,274 |
| npr | news-site | media-heavy | passed | passed | 3,557 | 3,359 |
| openai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,486 | 3,455 |
| oracle-cloud | cloud-platform | auth-entry, cloud-platform, media-heavy | blocked | blocked | 3,322 | 3,548 |
| paypal | payments-platform | ecommerce, auth-entry | passed | passed | 3,394 | 3,412 |
| perplexity | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,366 | 3,472 |
| pixelscan | bot-fingerprint-test | bot-detection | passed | passed | 4,105 | 4,607 |
| pkg-go-dev | language-docs | docs, static | passed | passed | 3,319 | 3,293 |
| playwright | browser-automation-docs | docs, static | passed | passed | 3,322 | 3,241 |
| postgres | database-docs | docs, static | passed | passed | 3,364 | 3,352 |
| prometheus | platform-docs | docs, static | passed | passed | 3,397 | 3,282 |
| pypi | package-registry | docs, static | passed | passed | 3,211 | 3,248 |
| python-org | language-docs | docs | passed | passed | 3,281 | 3,237 |
| rails | framework-docs | docs, static | passed | passed | 3,958 | 3,208 |
| react | framework-docs | docs, spa | passed | passed | 3,334 | 3,272 |
| redis | database-docs | docs, spa | passed | passed | 3,495 | 3,860 |
| reuters | news-site | media-heavy | passed | passed | 3,358 | 3,305 |
| rubygems | package-registry | docs | passed | passed | 3,261 | 3,270 |
| rust-lang | language-docs | docs, static | passed | passed | 3,333 | 3,289 |
| sannysoft | bot-fingerprint-test | bot-detection | passed | passed | 3,264 | 3,433 |
| selenium | browser-automation-docs | docs | passed | passed | 3,275 | 3,411 |
| shopify | commerce-platform | ecommerce, auth-entry, spa | passed | passed | 3,296 | 3,301 |
| spring | framework-docs | docs, spa | passed | passed | 3,348 | 3,308 |
| sqlite | database-docs | docs, static | passed | passed | 3,565 | 3,526 |
| stackoverflow | developer-platform | auth-entry, developer-platform | blocked | blocked | 3,304 | 3,342 |
| stripe | payments-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,419 | 3,640 |
| supabase | developer-platform | auth-entry, developer-platform, docs, spa | passed | passed | 3,378 | 3,304 |
| svelte | framework-docs | docs, spa | passed | passed | 3,269 | 3,298 |
| tailwind | framework-docs | docs, spa | passed | passed | 3,324 | 3,294 |
| target | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,822 | 3,698 |
| terraform | platform-docs | docs | passed | passed | 3,519 | 3,533 |
| uniqlo | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,441 | 3,330 |
| vercel | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,277 | 3,344 |
| vite | framework-docs | docs, spa | passed | passed | 3,287 | 3,267 |
| vue | framework-docs | docs, spa | passed | passed | 3,332 | 3,264 |
| walmart | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,319 | 3,308 |
| web-dev | browser-docs | docs, static | passed | passed | 3,748 | 3,636 |
| wikipedia | reference-site | static, media-heavy | passed | passed | 3,263 | 3,258 |
| yahoo-news | news-site | media-heavy, spa | passed | passed | 3,618 | 3,745 |
| ycombinator | startup-platform | auth-entry, static | passed | passed | 3,510 | 3,384 |
