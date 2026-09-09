package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/policy"
)

func TestMCPPolicyRejectionKeepsRecoveryThroughNavigationFailure(t *testing.T) {
	cfg := defaultTestConfig(t)
	browser := &fakeBrowserSession{navigateErr: fmt.Errorf("%w: private-value", gomoufox.ErrURLBlocked)}
	cfg.BrowserFactory = &fakeBrowserFactory{session: browser}
	server := newTestServer(t, cfg)
	for _, url := range []string{"file:///private-value", "http://93.184.216.34"} {
		r := server.Handle(context.Background(), "browser_navigate", mustRaw(t, map[string]any{"url": url}))
		if !r.IsError || r.Payload["reason"] != "network_policy" || !strings.Contains(r.Payload["hint"].(string), "Do not change transports") || r.Payload["next_tool"] != nil || strings.Contains(mustJSONText(r.Payload), "private-value") {
			t.Fatalf("policy rejection = %+v", r)
		}
	}
}

func TestMCPHTTPStatusKeepsContentAndRecoverySeparate(t *testing.T) {
	for _, status := range []int{200, 403, 429} {
		cfg := defaultTestConfig(t)
		cfg.Policy.AllowBrowserFetch = true
		cfg.Policy.AllowedHosts = []string{"93.184.216.34"}
		cfg.BrowserFactory = &fakeBrowserFactory{session: &fakeBrowserSession{
			navigateResult: navigateResult{Status: status, Title: "Site response"},
			fetchResult:    fetchResult{Status: status, Body: []byte("Site response")},
		}}
		server := newTestServer(t, cfg)
		for _, tool := range []string{"browser_navigate", "browser_fetch"} {
			r := server.Handle(context.Background(), tool, raw(`{"url":"http://93.184.216.34"}`))
			if r.IsError || r.Payload["status"] != status || r.Payload["error"] != nil || (r.Payload["http_recovery"] != nil) != (status >= 400) {
				t.Fatalf("%s HTTP %d = %+v", tool, status, r)
			}
			if status == 403 && r.Payload["http_recovery"].(*policy.HTTPRecovery).Reason != "site_denied" {
				t.Fatalf("site response mislabeled: %+v", r)
			}
		}
	}
}
