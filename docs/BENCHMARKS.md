# Go/Python Benchmark

- Generated: 2026-08-22T23:13:24.008336+00:00
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
- Shared persona SHA-256: bf64fa0f8d96f4fef903fef145acf30ccc6d70569e6dee5d3c44f14e2dff9c7d
- Go report style: compact

This benchmark runs gomoufox and Python Camoufox against the same target set.
It records outcome parity, wall time, browser work duration, peak RSS, peak CPU, and agent-output token footprint.
For parity, both runtimes use `--unsafe-direct-network` and the same generated Linux persona bundle. Local URL guardrails are tested elsewhere.
gomoufox is timed as a built CLI binary. Build time is excluded.
Resource samples cover the gomoufox sidecar process tree and the Python Camoufox harness process tree.

## Modes

- `smoke`: 2 fast targets for quick local checks.
- `full`: 8 detector and real-site targets used for the checked baseline.
- `extended`: 100 read-only public websites for broader resource, speed, parity, and token-footprint evidence.

Selected target tags: `auth-entry`, `bot-detection`, `cdn-security`, `cloud-platform`, `cloudflare`, `developer-platform`, `docs`, `ecommerce`, `media-heavy`, `spa`, `static`.

Refresh the checked baseline after significant browser, sidecar, MCP, CLI, or resource-related changes:

```bash
scripts/benchmark-realpass.py --mode full --go-report-style compact --update-doc docs/BENCHMARKS.md
```

Run the extended matrix before release candidates or major runtime changes:

```bash
scripts/benchmark-realpass.py --mode extended --loops 2 --run-order alternate --go-report-style compact --out dist/benchmarks/extended
```

Run the local fingerprint audit when changing Camoufox pins, launch options,
Firefox prefs, node-direct, WebGL, locale, timezone, screen, fonts, or canvas
handling:

```bash
scripts/fingerprint-audit.py
```

The audit serves one local page and records Python Camoufox, gomoufox with the
Python sidecar, and gomoufox with node-direct. Release gating compares the two
gomoufox runtimes and fails on any unallowed drift in JS-visible fields such as
UA, platform, languages, screen, timezone, WebGL, WebRTC, fonts, and canvas. The
Python Camoufox row is context, not the fail-closed comparator, because its
generated persona can differ on a single local run.

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
| gomoufox | 91 | 9 | 0 | 347,514 | 345,584 | 2,548.9 | 446.2 |
| Python Camoufox | 91 | 9 | 0 | 345,684 | 343,821 | 2,691.4 | 471.4 |

| Ratio | Go / Python |
|---|---:|
| Wall time | 1.005 |
| Target duration | 1.005 |
| Peak RSS | 0.947 |
| Peak CPU | 0.947 |
| Report tokens | 0.160 |

## Go/Python Benchmark Readiness

- Status: candidate
- Candidate: yes
- Note: Go/Python benchmark candidate means node-direct passed the extended comparison gate. Consumer no-Python readiness is recorded separately by scripts/no-python-consumer-canary.sh.

| Criterion | Passed | Detail |
|---|---:|---|
| go_sidecar_runtime_is_node_direct | yes | go_sidecar_runtime=node-direct |
| shared_linux_persona_bundle | yes | persona_os=linux persona_bundle_sha256=bf64fa0f8d96f4fef903fef145acf30ccc6d70569e6dee5d3c44f14e2dff9c7d |
| extended_target_matrix | yes | mode=extended targets=100 |
| no_go_only_outcome_regressions | yes | go_only_regression_count=0 outcome_mismatch_count=0 |
| no_runtime_failures | yes | go_failed=0 python_failed=0 |
| wall_time_not_slower_than_python | yes | wall_time=1.005293852188704 max=1.05 |
| target_duration_not_slower_than_python | yes | target_duration=1.0051276681761732 max=1.05 |
| peak_rss_beats_python | yes | peak_rss=0.9470540083947263 max=0.95 |
| peak_cpu_beats_python | yes | peak_cpu=0.9465422146796776 max=0.95 |
| report_tokens_beats_python | yes | report_tokens=0.1602576639123845 max=0.5 |

## Agent Output Footprint

Token estimates use `ceil(bytes / 4)`. They compare generated benchmark artifacts, not model billing.

| Runtime | JSON bytes | Markdown bytes | Estimated tokens |
|---|---:|---:|---:|
| gomoufox | 43,362 | 9,079 | 13,111 |
| Python Camoufox | 318,170 | 9,075 | 81,812 |

## Outcome Classes

- Shared blocked: `adidas`, `ap-news`, `ebay`, `g2`, `huggingface`, `nowsecure-cloudflare`, `oracle-cloud`, `perplexity`, `stackoverflow`
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
| 1 | gomoufox | 91 | 9 | 0 | 348,769 | 346,744 | 2,452.9 | 446.2 | 0 |
| 1 | Python Camoufox | 91 | 9 | 0 | 343,963 | 341,988 | 2,691.4 | 471.4 | 0 |
| 2 | gomoufox | 91 | 9 | 0 | 346,260 | 344,425 | 2,548.9 | 360.1 | 0 |
| 2 | Python Camoufox | 91 | 9 | 0 | 347,406 | 345,655 | 2,605.5 | 301.8 | 0 |

## Target Outcomes

| Target | Kind | Tags | Go | Python | Go ms | Python ms |
|---|---|---|---:|---:|---:|---:|
| adidas | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,386 | 3,560 |
| airbnb | travel-platform | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,517 | 3,514 |
| akamai | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,403 | 3,552 |
| angular | framework-docs | docs, spa | passed | passed | 3,391 | 3,363 |
| ansible | platform-docs | docs, media-heavy | passed | passed | 3,805 | 4,147 |
| anthropic | ai-platform | auth-entry, media-heavy | passed | passed | 3,287 | 3,402 |
| ap-news | news-site | media-heavy | blocked | blocked | 3,302 | 3,307 |
| apple | tech-platform | media-heavy, spa | passed | passed | 3,360 | 3,314 |
| astro | framework-docs | docs, static | passed | passed | 3,326 | 3,353 |
| aws | cloud-platform | auth-entry, cloud-platform, media-heavy | passed | passed | 3,300 | 3,360 |
| azure | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,448 | 3,457 |
| bbc | news-site | media-heavy | passed | passed | 3,259 | 3,256 |
| bestbuy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,684 | 3,748 |
| booking | travel-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,689 | 3,475 |
| chrome-dev | browser-docs | docs | passed | passed | 3,702 | 3,714 |
| cloudflare-docs | cdn-security-docs | cloudflare, cdn-security, docs | passed | passed | 3,288 | 3,338 |
| cloudflare-home | cloudflare-edge | cloudflare, cdn-security, spa | passed | passed | 4,145 | 4,021 |
| cloudflare-radar | cdn-security-docs | cloudflare, cdn-security, docs, spa | passed | passed | 3,933 | 3,861 |
| cnn | news-site | media-heavy, spa | passed | passed | 3,249 | 3,234 |
| cohere | ai-platform | auth-entry, media-heavy, spa | passed | passed | 4,661 | 3,534 |
| costco | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,488 | 3,589 |
| crates | package-registry | docs | passed | passed | 3,303 | 3,472 |
| creepjs | fingerprint-test | bot-detection, static | passed | passed | 3,233 | 3,218 |
| datadome | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 4,181 | 3,261 |
| digitalocean | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,283 | 3,264 |
| django | framework-docs | docs, static | passed | passed | 3,476 | 3,404 |
| docker-docs | platform-docs | docs, spa | passed | passed | 3,264 | 3,289 |
| docker-hub | package-registry | auth-entry, spa | passed | passed | 3,342 | 3,371 |
| ebay | marketplace | ecommerce, auth-entry, media-heavy | blocked | blocked | 3,422 | 3,528 |
| elastic | platform-docs | auth-entry, docs, spa | passed | passed | 3,295 | 3,608 |
| etsy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,654 | 3,560 |
| example | reference-site | static | passed | passed | 3,252 | 3,264 |
| fastly | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,238 | 3,229 |
| firebase | cloud-platform | cloud-platform, docs, spa | passed | passed | 3,528 | 3,436 |
| flask | framework-docs | docs, static | passed | passed | 3,398 | 3,352 |
| g2 | real-site-anti-bot | bot-detection, auth-entry, spa | blocked | blocked | 3,271 | 3,254 |
| github | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,428 | 3,377 |
| gitlab | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,481 | 3,473 |
| go-dev | language-docs | docs, static | passed | passed | 3,298 | 3,333 |
| google-cloud | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,394 | 3,395 |
| grafana | platform-docs | auth-entry, docs, spa | passed | passed | 3,303 | 5,094 |
| guardian | news-site | media-heavy | passed | passed | 3,258 | 3,289 |
| hacker-news | community-site | auth-entry, static | passed | passed | 3,253 | 3,328 |
| huggingface | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,226 | 3,235 |
| ikea | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,421 | 3,455 |
| imperva | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,313 | 3,279 |
| incolumitas | bot-fingerprint-test | bot-detection | passed | passed | 3,929 | 3,936 |
| kubernetes | platform-docs | docs, spa | passed | passed | 3,266 | 3,279 |
| linode | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,473 | 3,570 |
| mdn | browser-docs | docs, media-heavy | passed | passed | 3,240 | 3,241 |
| microsoft | tech-platform | auth-entry, media-heavy | passed | passed | 3,338 | 3,366 |
| mistral-ai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,385 | 3,377 |
| mozilla | browser-platform | docs, media-heavy | passed | passed | 3,427 | 3,256 |
| mysql | database-docs | docs, media-heavy | passed | passed | 3,463 | 3,424 |
| netlify | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,283 | 3,266 |
| nextjs | framework-docs | docs, spa | passed | passed | 3,317 | 3,303 |
| nginx | platform-docs | docs, static | passed | passed | 3,805 | 3,948 |
| nike | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,511 | 3,521 |
| nodejs | language-docs | docs | passed | passed | 3,351 | 3,417 |
| nowsecure-cloudflare | cloudflare-challenge-test | cloudflare, bot-detection | blocked | blocked | 4,188 | 4,217 |
| npmjs | package-registry | auth-entry, spa | passed | passed | 3,246 | 3,242 |
| npr | news-site | media-heavy | passed | passed | 3,330 | 3,359 |
| openai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,452 | 3,451 |
| oracle-cloud | cloud-platform | auth-entry, cloud-platform, media-heavy | blocked | blocked | 3,300 | 3,478 |
| paypal | payments-platform | ecommerce, auth-entry | passed | passed | 3,810 | 3,346 |
| perplexity | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,302 | 3,446 |
| pixelscan | bot-fingerprint-test | bot-detection | passed | passed | 4,216 | 4,196 |
| pkg-go-dev | language-docs | docs, static | passed | passed | 3,421 | 3,641 |
| playwright | browser-automation-docs | docs, static | passed | passed | 3,234 | 3,234 |
| postgres | database-docs | docs, static | passed | passed | 3,357 | 3,413 |
| prometheus | platform-docs | docs, static | passed | passed | 3,416 | 3,283 |
| pypi | package-registry | docs, static | passed | passed | 3,228 | 3,226 |
| python-org | language-docs | docs | passed | passed | 3,235 | 3,274 |
| rails | framework-docs | docs, static | passed | passed | 3,222 | 3,233 |
| react | framework-docs | docs, spa | passed | passed | 3,277 | 3,283 |
| redis | database-docs | docs, spa | passed | passed | 3,464 | 3,478 |
| reuters | news-site | media-heavy | passed | passed | 3,312 | 3,340 |
| rubygems | package-registry | docs | passed | passed | 3,264 | 3,266 |
| rust-lang | language-docs | docs, static | passed | passed | 3,480 | 3,342 |
| sannysoft | bot-fingerprint-test | bot-detection | passed | passed | 3,741 | 3,361 |
| selenium | browser-automation-docs | docs | passed | passed | 3,276 | 3,310 |
| shopify | commerce-platform | ecommerce, auth-entry, spa | passed | passed | 3,294 | 3,261 |
| spring | framework-docs | docs, spa | passed | passed | 3,330 | 3,313 |
| sqlite | database-docs | docs, static | passed | passed | 3,486 | 3,470 |
| stackoverflow | developer-platform | auth-entry, developer-platform | blocked | blocked | 3,392 | 3,375 |
| stripe | payments-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,414 | 3,406 |
| supabase | developer-platform | auth-entry, developer-platform, docs, spa | passed | passed | 3,319 | 3,328 |
| svelte | framework-docs | docs, spa | passed | passed | 3,281 | 3,284 |
| tailwind | framework-docs | docs, spa | passed | passed | 3,304 | 3,382 |
| target | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,824 | 3,881 |
| terraform | platform-docs | docs | passed | passed | 3,528 | 3,499 |
| uniqlo | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,402 | 3,717 |
| vercel | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,302 | 3,339 |
| vite | framework-docs | docs, spa | passed | passed | 3,257 | 3,264 |
| vue | framework-docs | docs, spa | passed | passed | 3,264 | 3,270 |
| walmart | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,322 | 3,416 |
| web-dev | browser-docs | docs, static | passed | passed | 3,715 | 3,552 |
| wikipedia | reference-site | static, media-heavy | passed | passed | 3,254 | 3,249 |
| yahoo-news | news-site | media-heavy, spa | passed | passed | 3,587 | 4,091 |
| ycombinator | startup-platform | auth-entry, static | passed | passed | 3,453 | 3,368 |
