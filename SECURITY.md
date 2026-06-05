# Security

Report security issues by private email or a private GitHub advisory. Do not
open a public issue for suspected vulnerabilities.

gomoufox launches a browser and can reach the network. Treat the CLI and MCP
server as tools that run with your local user permissions.

Defaults:

- MCP blocks `file://`, private IPs, link-local addresses, and cloud metadata
  hosts.
- MCP disables JavaScript evaluation unless the operator starts it with
  `--enable-eval`.
- MCP caps input and response sizes.
- Cookie values and session exports stay redacted unless the operator enables
  the matching flag.
- `gomoufox serve` requires an auth token for HTTP access.

Useful checks:

```bash
go test -race -count=1 ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.3.0 ./...
```
