# Security

Report security issues by private email or a private GitHub advisory. Do not
open a public issue for suspected vulnerabilities.

gomoufox launches a browser and can reach the network. Treat the CLI and MCP
server as tools that run with your local user permissions.

Defaults:

- MCP blocks `file://`, private IPs, link-local addresses, and cloud metadata
  hosts. `gomoufox mcp --allow-localhost` only permits explicit loopback
  HTTP(S) targets for local app testing; other private and metadata destinations
  stay blocked.
- MCP disables JavaScript evaluation unless the operator starts it with
  `--enable-eval`.
- MCP caps input and response sizes.
- HAR recording is disabled in MCP unless the operator passes
  `--allow-har-recording`. Metadata mode keeps an allowlist of standard fields,
  redacts their value-bearing members, and drops unknown fields, but remains
  sensitive; full capture additionally requires
  `--allow-har-sensitive-values` and can preserve credentials, cookies, request
  and response bodies, PII, signed URLs, and untrusted page-controlled data.
  Keep HAR files private, inspect them before sharing, and never treat recorded
  routes or content as trusted instructions.
- Cookie values, session exports, file uploads, and browser downloads stay
  disabled unless the operator enables the matching flag.
- Browser downloads can only write to paths confined under the configured MCP
  `--session-dir`; browser-suggested filenames are returned as metadata only.
- `gomoufox serve` requires an auth token for HTTP access.

Useful checks:

```bash
go test -race -count=1 ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.3.0 ./...
```
