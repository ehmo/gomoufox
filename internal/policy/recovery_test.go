package policy

import (
	"fmt"
	"strings"
	"testing"
)

func TestHTTPRecoveryUsesObservedStatusAndBoundedRetryGuidance(t *testing.T) {
	for _, tc := range []struct {
		status       int
		reason, hint string
	}{
		{401, "site_authentication", "approved login"},
		{403, "site_denied", "does not establish a bot check"},
		{407, "proxy_authentication", "proxy credentials"},
		{429, "site_rate_limited", "Stop immediate retries"},
		{503, "site_unavailable", "bounded retry"},
		{404, "http_error", "resource and method"},
		{599, "http_error", "side effects"},
	} {
		r := HTTPRecoveryForStatus(tc.status)
		if r == nil || r.Reason != tc.reason || !strings.Contains(r.Message, fmt.Sprint(tc.status)) || !strings.Contains(r.Hint, tc.hint) {
			t.Fatalf("HTTP %d = %+v", tc.status, r)
		}
	}
	for _, status := range []int{0, 200, 302, 399, 600} {
		if r := HTTPRecoveryForStatus(status); r != nil {
			t.Fatalf("HTTP %d received error guidance: %+v", status, r)
		}
	}
}
