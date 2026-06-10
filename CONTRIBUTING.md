# Contributing

Thanks for working on gomoufox.

Start with the standard checks. The Python commands here are development and
release-validation tooling, not gomoufox runtime prerequisites:

```bash
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
python3 scripts/check-agent-contracts.py
go run golang.org/x/vuln/cmd/govulncheck@v1.3.0 ./...
```

Run the explicit Go/Python benchmark before significant browser, sidecar, MCP,
CLI, or resource-related changes:

```bash
scripts/benchmark-realpass.py --mode smoke
```

Use `--mode extended` for the 100-target matrix before release candidates or major
runtime changes. Use `--max-targets <n>` for a bounded investigation run.

For upstream-baseline changes, run full mode with
`--update-doc docs/BENCHMARKS.md` and commit the JSON written under
`docs/benchmarks/`.

Keep changes small. Add tests for behavior, not private helpers. If you touch
CLI output or MCP schemas, update `docs/agent-contracts/` with
`python3 scripts/check-agent-contracts.py --update` and review the diff.

The public repo is generated. Do not edit generated public files by hand. In
public clones, run:

```bash
python3 scripts/check-public-release-contract.py .
```

Security bugs do not belong in public issues. Use `SECURITY.md`.
