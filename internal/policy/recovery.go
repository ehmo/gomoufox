package policy

import "fmt"

const NetworkBlockHint = "Check the target and effective network policy. Authorized loopback HTTP(S) requires --allow-localhost; other private networks, metadata endpoints, unsafe redirects and DNS rebinding remain blocked. Do not change transports to evade this rejection."

// HTTPRecovery describes the observed status without inferring a bot check
// from page text, headers or an access-denied status alone.
type HTTPRecovery struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func HTTPRecoveryForStatus(status int) *HTTPRecovery {
	if status < 400 || status > 599 {
		return nil
	}
	r := &HTTPRecovery{Reason: "http_error", Message: fmt.Sprintf("The server returned HTTP %d.", status)}
	switch status {
	case 401:
		r.Reason = "site_authentication"
		r.Hint = "Inspect the response and use the site's approved login flow if authorized. Reuse that session before retrying; do not replay completed side effects."
	case 403:
		r.Reason = "site_denied"
		r.Hint = "Inspect the response or current page for access requirements. HTTP 403 alone does not establish a bot check. Stop unchanged retries; use an approved login or documented API only when authorized."
	case 407:
		r.Reason = "proxy_authentication"
		r.Hint = "Check the configured proxy credentials before retrying. Keep credentials out of shared diagnostics."
	case 429:
		r.Reason = "site_rate_limited"
		r.Hint = "Stop immediate retries. Inspect Retry-After when available and the site's documented limits before a bounded retry; do not create more sessions to evade the limit."
	case 503:
		r.Reason = "site_unavailable"
		r.Hint = "Inspect Retry-After when available and the current response. HTTP 503 alone does not establish a bot check. Wait before a bounded retry, checking state before repeating side effects."
	default:
		r.Hint = "Inspect the status and response before retrying. Check the requested resource and method, and do not repeat completed side effects blindly."
	}
	return r
}
