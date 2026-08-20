# Go/Python Benchmark

- Generated: 2026-08-20T15:56:58.066505+00:00
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
- Shared persona SHA-256: 239a4cd650fa9a992289e3b06775d73ae02e4382684d57a04d2b40313bd3b02f
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
| gomoufox | 91 | 9 | 0 | 361,018 | 357,829 | 2,591.9 | 544.2 |
| Python Camoufox | 91 | 9 | 0 | 925,972 | 888,313 | 2,383.3 | 401.4 |

| Ratio | Go / Python |
|---|---:|
| Wall time | 0.390 |
| Target duration | 0.403 |
| Peak RSS | 1.088 |
| Peak CPU | 1.356 |
| Report tokens | 0.159 |

## Go/Python Benchmark Readiness

- Status: blocked
- Candidate: no
- Note: Go/Python benchmark candidate means node-direct passed the extended comparison gate. Consumer no-Python readiness is recorded separately by scripts/no-python-consumer-canary.sh.

| Criterion | Passed | Detail |
|---|---:|---|
| go_sidecar_runtime_is_node_direct | yes | go_sidecar_runtime=node-direct |
| shared_linux_persona_bundle | yes | persona_os=linux persona_bundle_sha256=239a4cd650fa9a992289e3b06775d73ae02e4382684d57a04d2b40313bd3b02f |
| extended_target_matrix | yes | mode=extended targets=100 |
| no_go_only_outcome_regressions | yes | go_only_regression_count=0 outcome_mismatch_count=3 |
| no_runtime_failures | yes | go_failed=0 python_failed=0 |
| wall_time_not_slower_than_python | yes | wall_time=0.38988003956923106 max=1.05 |
| target_duration_not_slower_than_python | yes | target_duration=0.40281860110118844 max=1.05 |
| peak_rss_beats_python | no | peak_rss=1.0875160622033408 max=0.95 |
| peak_cpu_beats_python | no | peak_cpu=1.3557548579970107 max=0.95 |
| report_tokens_beats_python | yes | report_tokens=0.15888884834774572 max=0.5 |

## Agent Output Footprint

Token estimates use `ceil(bytes / 4)`. They compare generated benchmark artifacts, not model billing.

| Runtime | JSON bytes | Markdown bytes | Estimated tokens |
|---|---:|---:|---:|
| gomoufox | 43,178 | 9,076 | 13,064 |
| Python Camoufox | 319,873 | 9,011 | 82,221 |

## Outcome Classes

- Shared blocked: `adidas`, `ap-news`, `ebay`, `g2`, `huggingface`, `nowsecure-cloudflare`, `oracle-cloud`, `perplexity`, `stackoverflow`
- Shared failed: none
- Go-only regressions: none
- Python-only differences: none

## Interpretation

- Outcome mismatches: 3
- The browser dominates wall time. Treat serial Go/Python speed as a parity check, not proof that Go will always outrun Python.
- gomoufox should still win the agent-output footprint. A report-token ratio above 0.50 is a regression.
- gomoufox benefits outside this run include typed Go integration, CLI and MCP surfaces, local URL guardrails, and repeatable checks against Python Camoufox.

## Per Loop

| Loop | Runtime | Passed | Blocked | Failed | Wall ms | Target ms | Peak RSS MiB | Peak CPU % | Mismatches |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | gomoufox | 92 | 8 | 0 | 364,882 | 362,877 | 2,555.0 | 329.0 | 3 |
| 1 | Python Camoufox | 92 | 7 | 1 | 1,383,983 | 1,334,094 | 1,692.9 | 106.1 | 3 |
| 2 | gomoufox | 91 | 9 | 0 | 357,154 | 352,781 | 2,591.9 | 544.2 | 0 |
| 2 | Python Camoufox | 91 | 9 | 0 | 467,962 | 442,533 | 2,383.3 | 401.4 | 0 |

## Target Outcomes

| Target | Kind | Tags | Go | Python | Go ms | Python ms |
|---|---|---|---:|---:|---:|---:|
| adidas | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,498 | 3,794 |
| airbnb | travel-platform | ecommerce, auth-entry, media-heavy, spa | passed | passed | 4,065 | 4,555 |
| akamai | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,349 | 3,468 |
| angular | framework-docs | docs, spa | passed | passed | 3,377 | 3,356 |
| ansible | platform-docs | docs, media-heavy | passed | passed | 3,497 | 3,588 |
| anthropic | ai-platform | auth-entry, media-heavy | passed | passed | 3,374 | 3,592 |
| ap-news | news-site | media-heavy | blocked | blocked | 3,522 | 3,292 |
| apple | tech-platform | media-heavy, spa | passed | passed | 3,376 | 3,831 |
| astro | framework-docs | docs, static | passed | passed | 3,405 | 3,365 |
| aws | cloud-platform | auth-entry, cloud-platform, media-heavy | passed | passed | 3,352 | 3,782 |
| azure | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,552 | 3,576 |
| bbc | news-site | media-heavy | passed | passed | 3,754 | 4,032 |
| bestbuy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,810 | 3,791 |
| booking | travel-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,484 | 3,583 |
| chrome-dev | browser-docs | docs | passed | passed | 4,172 | 3,734 |
| cloudflare-docs | cdn-security-docs | cloudflare, cdn-security, docs | passed | passed | 3,331 | 3,341 |
| cloudflare-home | cloudflare-edge | cloudflare, cdn-security, spa | passed | passed | 4,314 | 5,813 |
| cloudflare-radar | cdn-security-docs | cloudflare, cdn-security, docs, spa | passed | passed | 4,218 | 4,167 |
| cnn | news-site | media-heavy, spa | passed | passed | 3,404 | 3,251 |
| cohere | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,601 | 3,533 |
| costco | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,514 | 3,755 |
| crates | package-registry | docs | passed | passed | 3,309 | 3,360 |
| creepjs | fingerprint-test | bot-detection, static | passed | passed | 3,290 | 9,371 |
| datadome | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,360 | 8,136 |
| digitalocean | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,835 | 3,290 |
| django | framework-docs | docs, static | passed | passed | 3,519 | 3,386 |
| docker-docs | platform-docs | docs, spa | passed | passed | 3,421 | 3,875 |
| docker-hub | package-registry | auth-entry, spa | passed | passed | 3,362 | 3,371 |
| ebay | marketplace | ecommerce, auth-entry, media-heavy | blocked | blocked | 3,341 | 3,467 |
| elastic | platform-docs | auth-entry, docs, spa | passed | passed | 3,297 | 3,445 |
| etsy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,563 | 3,573 |
| example | reference-site | static | passed | passed | 3,286 | 6,084 |
| fastly | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,240 | 3,330 |
| firebase | cloud-platform | cloud-platform, docs, spa | passed | passed | 3,662 | 4,345 |
| flask | framework-docs | docs, static | passed | passed | 3,451 | 3,442 |
| g2 | real-site-anti-bot | bot-detection, auth-entry, spa | blocked | blocked | 3,288 | 6,984 |
| github | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,456 | 20,162 |
| gitlab | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,488 | 4,720 |
| go-dev | language-docs | docs, static | passed | passed | 3,308 | 3,411 |
| google-cloud | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,612 | 3,613 |
| grafana | platform-docs | auth-entry, docs, spa | passed | passed | 3,319 | 3,347 |
| guardian | news-site | media-heavy | passed | passed | 3,761 | 4,340 |
| hacker-news | community-site | auth-entry, static | passed | passed | 3,253 | 3,241 |
| huggingface | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,319 | 3,224 |
| ikea | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,381 | 3,641 |
| imperva | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,341 | 3,305 |
| incolumitas | bot-fingerprint-test | bot-detection | passed | passed | 3,953 | 11,112 |
| kubernetes | platform-docs | docs, spa | passed | passed | 3,249 | 3,364 |
| linode | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,513 | 3,381 |
| mdn | browser-docs | docs, media-heavy | passed | passed | 3,353 | 3,409 |
| microsoft | tech-platform | auth-entry, media-heavy | passed | passed | 3,384 | 4,504 |
| mistral-ai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,436 | 3,423 |
| mozilla | browser-platform | docs, media-heavy | passed | passed | 3,525 | 3,584 |
| mysql | database-docs | docs, media-heavy | passed | passed | 3,677 | 3,552 |
| netlify | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,602 | 3,407 |
| nextjs | framework-docs | docs, spa | passed | passed | 3,339 | 3,466 |
| nginx | platform-docs | docs, static | passed | passed | 3,871 | 3,875 |
| nike | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,875 | 3,604 |
| nodejs | language-docs | docs | passed | passed | 3,840 | 3,674 |
| nowsecure-cloudflare | cloudflare-challenge-test | cloudflare, bot-detection | blocked | blocked | 5,116 | 28,251 |
| npmjs | package-registry | auth-entry, spa | passed | passed | 3,314 | 3,598 |
| npr | news-site | media-heavy | passed | passed | 3,382 | 3,366 |
| openai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,657 | 3,724 |
| oracle-cloud | cloud-platform | auth-entry, cloud-platform, media-heavy | blocked | blocked | 3,481 | 3,391 |
| paypal | payments-platform | ecommerce, auth-entry | passed | passed | 3,363 | 3,350 |
| perplexity | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,301 | 3,306 |
| pixelscan | bot-fingerprint-test | bot-detection | passed | passed | 4,128 | 12,700 |
| pkg-go-dev | language-docs | docs, static | passed | passed | 3,412 | 4,027 |
| playwright | browser-automation-docs | docs, static | passed | passed | 3,618 | 3,867 |
| postgres | database-docs | docs, static | passed | passed | 3,458 | 3,485 |
| prometheus | platform-docs | docs, static | passed | passed | 3,489 | 3,301 |
| pypi | package-registry | docs, static | passed | passed | 3,245 | 3,235 |
| python-org | language-docs | docs | passed | passed | 3,406 | 3,556 |
| rails | framework-docs | docs, static | passed | passed | 3,356 | 3,236 |
| react | framework-docs | docs, spa | passed | passed | 3,289 | 3,356 |
| redis | database-docs | docs, spa | passed | passed | 3,459 | 3,423 |
| reuters | news-site | media-heavy | passed | passed | 4,165 | 3,742 |
| rubygems | package-registry | docs | passed | passed | 3,269 | 3,395 |
| rust-lang | language-docs | docs, static | passed | passed | 3,502 | 3,381 |
| sannysoft | bot-fingerprint-test | bot-detection | passed | passed | 3,362 | 4,456 |
| selenium | browser-automation-docs | docs | passed | passed | 3,502 | 3,601 |
| shopify | commerce-platform | ecommerce, auth-entry, spa | passed | passed | 3,363 | 3,289 |
| spring | framework-docs | docs, spa | passed | passed | 3,375 | 3,326 |
| sqlite | database-docs | docs, static | passed | passed | 3,578 | 3,518 |
| stackoverflow | developer-platform | auth-entry, developer-platform | blocked | blocked | 3,392 | 4,291 |
| stripe | payments-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,423 | 3,390 |
| supabase | developer-platform | auth-entry, developer-platform, docs, spa | passed | passed | 3,521 | 3,430 |
| svelte | framework-docs | docs, spa | passed | passed | 3,299 | 3,373 |
| tailwind | framework-docs | docs, spa | passed | passed | 3,346 | 3,321 |
| target | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,890 | 3,871 |
| terraform | platform-docs | docs | passed | passed | 3,482 | 3,577 |
| uniqlo | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,593 | 5,140 |
| vercel | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,462 | 3,725 |
| vite | framework-docs | docs, spa | passed | passed | 3,273 | 3,257 |
| vue | framework-docs | docs, spa | passed | passed | 3,304 | 3,292 |
| walmart | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,395 | 3,367 |
| web-dev | browser-docs | docs, static | passed | passed | 3,963 | 4,121 |
| wikipedia | reference-site | static, media-heavy | passed | passed | 3,281 | 9,459 |
| yahoo-news | news-site | media-heavy, spa | passed | passed | 4,108 | 3,647 |
| ycombinator | startup-platform | auth-entry, static | passed | passed | 3,411 | 3,375 |
