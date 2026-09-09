package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCapabilitiesReflectRunningPolicyWithoutBrowser(t *testing.T) {
	for _, toolset := range []string{ToolsetCore, ToolsetFull} {
		t.Run(toolset, func(t *testing.T) {
			cfg := defaultTestConfig(t)
			cfg.Toolset = toolset
			cfg.Policy.EnableEval = true
			cfg.Policy.AllowBrowserFetch = true
			cfg.Policy.AllowLocalhost = true
			cfg.Policy.AllowPrivateIPs = true
			cfg.Policy.MaxSessions = 2
			s := newTestServer(t, cfg)
			result := s.Handle(context.Background(), "browser_capabilities", raw(`{}`))
			if result.IsError {
				t.Fatal(result)
			}
			features := result.Payload["features"].(map[string]any)
			for name := range features {
				if _, ok := toolByName(name); !ok {
					t.Fatalf("capability names unknown tool %s", name)
				}
			}
			if features["browser_evaluate"].(map[string]bool)["callable"] != (toolset == ToolsetFull) || features["browser_fetch"].(map[string]bool)["callable"] {
				t.Fatalf("scope/toolset ignored: %+v", features)
			}
			if features["browser_network_requests"].(map[string]bool)["callable"] != (toolset == ToolsetFull) {
				t.Fatal("diagnostics do not reflect toolset")
			}
			network := result.Payload["network"].(map[string]any)
			if network["private_ips"] != false || network["localhost"] != true || result.Payload["limits"].(map[string]any)["max_sessions"] != 2 {
				t.Fatalf("effective configuration not reflected: %+v", result.Payload)
			}
			if len(cfg.BrowserFactory.(*fakeBrowserFactory).requests) != 0 {
				t.Fatal("capability inspection launched browser")
			}
			assertError(t, s.Handle(context.Background(), "browser_capabilities", raw(`{"unknown":true}`)), "invalid_arguments")
			cfg.Policy.AllowedHosts = []string{"example.com"}
			s = newTestServer(t, cfg)
			features = s.Handle(context.Background(), "browser_capabilities", raw(`{}`)).Payload["features"].(map[string]any)
			if features["browser_fetch"].(map[string]bool)["callable"] != (toolset == ToolsetFull) || features["browser_fetch_form"].(map[string]bool)["callable"] {
				t.Fatal("file gate or target scope ignored")
			}
		})
	}
}

func TestCapabilityScopeIsBoundedAndOmitsCredentials(t *testing.T) {
	origins := []string{"https://user:private-marker@example.com/path?token=private-marker", "%", "not an origin", strings.Repeat("x", 300)}
	for range 12 {
		origins = append(origins, "https://example.com")
	}
	result := capabilityScope(origins, true)
	if len(result["items"].([]string)) != 8 || result["total"] != len(origins) || result["omitted"] != len(origins)-8 {
		t.Fatal(result)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "private-marker") {
		t.Fatal("scope leaks credentials")
	}
	hosts := capabilityScope([]string{"", strings.Repeat("x", 257), "user:private-marker@example.com", "example.com"}, false)
	if len(hosts["items"].([]string)) != 1 || hosts["items"].([]string)[0] != "example.com" || hosts["omitted"] != 3 {
		t.Fatal(hosts)
	}
}
