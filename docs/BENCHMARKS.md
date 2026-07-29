# Go/Python Benchmark

- Generated: 2026-07-29T13:59:26.101812+00:00
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
- Shared persona SHA-256: 2c178d10e17e8b759c5302ad00e76745e8b6658259a1b466fad182fa50103a7e
- Shared persona artifact: [2026-07-29-node-direct-readiness.json](personas/2026-07-29-node-direct-readiness.json)
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
| gomoufox | 91 | 9 | 0 | 346,474 | 344,365 | 2,780.5 | 461.2 |
| Python Camoufox | 91 | 9 | 0 | 346,695 | 345,105 | 3,080.9 | 503.4 |

| Ratio | Go / Python |
|---|---:|
| Wall time | 0.999 |
| Target duration | 0.998 |
| Peak RSS | 0.903 |
| Peak CPU | 0.916 |
| Report tokens | 0.158 |

## Go/Python Benchmark Readiness

- Status: candidate
- Candidate: yes
- Note: Go/Python benchmark candidate means node-direct passed the extended comparison gate. Consumer no-Python readiness is recorded separately by scripts/no-python-consumer-canary.sh.

| Criterion | Passed | Detail |
|---|---:|---|
| go_sidecar_runtime_is_node_direct | yes | go_sidecar_runtime=node-direct |
| shared_linux_persona_bundle | yes | persona_os=linux persona_bundle_sha256=2c178d10e17e8b759c5302ad00e76745e8b6658259a1b466fad182fa50103a7e |
| extended_target_matrix | yes | mode=extended targets=100 |
| no_go_only_outcome_regressions | yes | go_only_regression_count=0 outcome_mismatch_count=1 |
| no_runtime_failures | yes | go_failed=0 python_failed=0 |
| wall_time_not_slower_than_python | yes | wall_time=0.9993625520991073 max=1.05 |
| target_duration_not_slower_than_python | yes | target_duration=0.9978557250691819 max=1.05 |
| peak_rss_beats_python | yes | peak_rss=0.9025139848968186 max=0.95 |
| peak_cpu_beats_python | yes | peak_cpu=0.9161700437028208 max=0.95 |
| report_tokens_beats_python | yes | report_tokens=0.15837667835974356 max=0.5 |

## Agent Output Footprint

Token estimates use `ceil(bytes / 4)`. They compare generated benchmark artifacts, not model billing.

| Runtime | JSON bytes | Markdown bytes | Estimated tokens |
|---|---:|---:|---:|
| gomoufox | 43,318 | 9,050 | 13,093 |
| Python Camoufox | 321,632 | 9,046 | 82,670 |

## Outcome Classes

- Shared blocked: `adidas`, `datadome`, `etsy`, `g2`, `huggingface`, `nowsecure-cloudflare`, `oracle-cloud`, `perplexity`, `stackoverflow`
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
| 1 | gomoufox | 92 | 8 | 0 | 347,110 | 344,869 | 2,524.3 | 461.2 | 1 |
| 1 | Python Camoufox | 91 | 9 | 0 | 346,805 | 345,222 | 2,847.9 | 503.4 | 1 |
| 2 | gomoufox | 91 | 9 | 0 | 345,838 | 343,862 | 2,780.5 | 330.0 | 0 |
| 2 | Python Camoufox | 91 | 9 | 0 | 346,585 | 344,988 | 3,080.9 | 327.8 | 0 |

## Target Outcomes

| Target | Kind | Tags | Go | Python | Go ms | Python ms |
|---|---|---|---:|---:|---:|---:|
| adidas | marketplace | ecommerce, auth-entry, media-heavy, spa | blocked | blocked | 3,497 | 3,819 |
| airbnb | travel-platform | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,535 | 3,502 |
| akamai | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,880 | 3,953 |
| angular | framework-docs | docs, spa | passed | passed | 3,355 | 3,284 |
| ansible | platform-docs | docs, media-heavy | passed | passed | 3,820 | 3,616 |
| anthropic | ai-platform | auth-entry, media-heavy | passed | passed | 3,264 | 3,369 |
| ap-news | news-site | media-heavy | passed | passed | 3,265 | 3,260 |
| apple | tech-platform | media-heavy, spa | passed | passed | 3,292 | 3,287 |
| astro | framework-docs | docs, static | passed | passed | 3,307 | 3,371 |
| aws | cloud-platform | auth-entry, cloud-platform, media-heavy | passed | passed | 3,326 | 3,317 |
| azure | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,526 | 3,971 |
| bbc | news-site | media-heavy | passed | passed | 3,259 | 3,261 |
| bestbuy | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,817 | 3,655 |
| booking | travel-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,506 | 3,484 |
| chrome-dev | browser-docs | docs | passed | passed | 3,648 | 3,738 |
| cloudflare-docs | cdn-security-docs | cloudflare, cdn-security, docs | passed | passed | 3,297 | 3,314 |
| cloudflare-home | cloudflare-edge | cloudflare, cdn-security, spa | passed | passed | 3,681 | 3,874 |
| cloudflare-radar | cdn-security-docs | cloudflare, cdn-security, docs, spa | passed | passed | 3,850 | 3,862 |
| cnn | news-site | media-heavy, spa | passed | passed | 3,262 | 3,225 |
| cohere | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,530 | 3,536 |
| costco | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,943 | 3,929 |
| crates | package-registry | docs | passed | passed | 3,426 | 3,448 |
| creepjs | fingerprint-test | bot-detection, static | passed | passed | 3,210 | 3,205 |
| datadome | bot-defense-vendor | bot-detection, cdn-security | blocked | blocked | 3,334 | 3,318 |
| digitalocean | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,372 | 3,237 |
| django | framework-docs | docs, static | passed | passed | 3,416 | 3,447 |
| docker-docs | platform-docs | docs, spa | passed | passed | 3,325 | 3,341 |
| docker-hub | package-registry | auth-entry, spa | passed | passed | 3,384 | 3,368 |
| ebay | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,940 | 3,934 |
| elastic | platform-docs | auth-entry, docs, spa | passed | passed | 3,258 | 3,270 |
| etsy | marketplace | ecommerce, auth-entry, media-heavy | blocked | blocked | 3,248 | 3,267 |
| example | reference-site | static | passed | passed | 3,235 | 3,222 |
| fastly | cdn-security-vendor | cdn-security, media-heavy | passed | passed | 3,247 | 3,342 |
| firebase | cloud-platform | cloud-platform, docs, spa | passed | passed | 3,548 | 3,537 |
| flask | framework-docs | docs, static | passed | passed | 3,363 | 3,398 |
| g2 | real-site-anti-bot | bot-detection, auth-entry, spa | blocked | blocked | 3,256 | 3,258 |
| github | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,382 | 3,349 |
| gitlab | developer-platform | auth-entry, developer-platform, spa | passed | passed | 3,492 | 3,488 |
| go-dev | language-docs | docs, static | passed | passed | 3,324 | 3,299 |
| google-cloud | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,405 | 3,433 |
| grafana | platform-docs | auth-entry, docs, spa | passed | passed | 3,323 | 3,297 |
| guardian | news-site | media-heavy | passed | passed | 3,310 | 3,289 |
| hacker-news | community-site | auth-entry, static | passed | passed | 3,243 | 3,249 |
| huggingface | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,229 | 3,244 |
| ikea | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,517 | 3,421 |
| imperva | bot-defense-vendor | bot-detection, cdn-security | passed | passed | 3,288 | 3,277 |
| incolumitas | bot-fingerprint-test | bot-detection | passed | passed | 3,924 | 3,864 |
| kubernetes | platform-docs | docs, spa | passed | passed | 3,264 | 3,256 |
| linode | cloud-platform | auth-entry, cloud-platform, docs | passed | passed | 3,393 | 3,306 |
| mdn | browser-docs | docs, media-heavy | passed | passed | 3,253 | 3,295 |
| microsoft | tech-platform | auth-entry, media-heavy | passed | passed | 3,371 | 3,663 |
| mistral-ai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,363 | 3,304 |
| mozilla | browser-platform | docs, media-heavy | passed | passed | 3,594 | 3,607 |
| mysql | database-docs | docs, media-heavy | passed | passed | 3,466 | 3,636 |
| netlify | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,288 | 3,281 |
| nextjs | framework-docs | docs, spa | passed | passed | 3,339 | 3,296 |
| nginx | platform-docs | docs, static | passed | passed | 3,839 | 3,843 |
| nike | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,560 | 3,941 |
| nodejs | language-docs | docs | passed | passed | 3,358 | 3,320 |
| nowsecure-cloudflare | cloudflare-challenge-test | cloudflare, bot-detection | blocked | blocked | 4,225 | 4,194 |
| npmjs | package-registry | auth-entry, spa | passed | passed | 3,262 | 3,242 |
| npr | news-site | media-heavy | passed | passed | 3,413 | 3,375 |
| openai | ai-platform | auth-entry, media-heavy, spa | passed | passed | 3,508 | 3,531 |
| oracle-cloud | cloud-platform | auth-entry, cloud-platform, media-heavy | blocked | blocked | 3,368 | 3,296 |
| paypal | payments-platform | ecommerce, auth-entry | passed | passed | 3,389 | 3,853 |
| perplexity | ai-platform | auth-entry, media-heavy, spa | blocked | blocked | 3,300 | 3,324 |
| pixelscan | bot-fingerprint-test | bot-detection | passed | passed | 4,230 | 4,537 |
| pkg-go-dev | language-docs | docs, static | passed | passed | 3,400 | 3,377 |
| playwright | browser-automation-docs | docs, static | passed | passed | 3,302 | 3,307 |
| postgres | database-docs | docs, static | passed | passed | 3,460 | 3,444 |
| prometheus | platform-docs | docs, static | passed | passed | 3,458 | 3,275 |
| pypi | package-registry | docs, static | passed | passed | 3,208 | 3,202 |
| python-org | language-docs | docs | passed | passed | 3,208 | 3,197 |
| rails | framework-docs | docs, static | passed | passed | 3,228 | 3,214 |
| react | framework-docs | docs, spa | passed | passed | 3,284 | 3,289 |
| redis | database-docs | docs, spa | passed | passed | 3,449 | 3,476 |
| reuters | news-site | media-heavy | passed | passed | 3,599 | 3,314 |
| rubygems | package-registry | docs | passed | passed | 3,252 | 3,239 |
| rust-lang | language-docs | docs, static | passed | passed | 3,391 | 3,209 |
| sannysoft | bot-fingerprint-test | bot-detection | passed | passed | 3,321 | 3,303 |
| selenium | browser-automation-docs | docs | passed | passed | 3,303 | 3,320 |
| shopify | commerce-platform | ecommerce, auth-entry, spa | passed | passed | 3,286 | 3,396 |
| spring | framework-docs | docs, spa | passed | passed | 3,295 | 3,322 |
| sqlite | database-docs | docs, static | passed | passed | 3,496 | 3,492 |
| stackoverflow | developer-platform | auth-entry, developer-platform | blocked | blocked | 3,429 | 3,433 |
| stripe | payments-platform | ecommerce, auth-entry, media-heavy | passed | passed | 3,396 | 3,399 |
| supabase | developer-platform | auth-entry, developer-platform, docs, spa | passed | passed | 3,317 | 3,433 |
| svelte | framework-docs | docs, spa | passed | passed | 3,280 | 3,316 |
| tailwind | framework-docs | docs, spa | passed | passed | 3,346 | 3,297 |
| target | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,866 | 3,776 |
| terraform | platform-docs | docs | passed | passed | 3,592 | 3,567 |
| uniqlo | marketplace | ecommerce, auth-entry, media-heavy, spa | passed | passed | 3,451 | 3,558 |
| vercel | cloud-platform | auth-entry, cloud-platform, docs, spa | passed | passed | 3,276 | 3,281 |
| vite | framework-docs | docs, spa | passed | passed | 3,272 | 3,247 |
| vue | framework-docs | docs, spa | passed | passed | 3,296 | 3,291 |
| walmart | marketplace | ecommerce, auth-entry, media-heavy | passed | passed | 3,325 | 3,319 |
| web-dev | browser-docs | docs, static | passed | passed | 3,673 | 3,839 |
| wikipedia | reference-site | static, media-heavy | passed | passed | 3,260 | 3,266 |
| yahoo-news | news-site | media-heavy, spa | passed | passed | 3,848 | 3,655 |
| ycombinator | startup-platform | auth-entry, static | passed | passed | 3,653 | 3,406 |
