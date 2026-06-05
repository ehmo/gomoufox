package camoufoxcfg

// ProxyConfig describes a proxy server for all browser traffic.
// Credentials are redacted in diagnostics and do not weaken URL guardrails.
type ProxyConfig struct {
	Server   string
	Username string
	Password string
}
