package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCLIPolicyRejectionReportsRecoveryBeforeBrowserStart(t *testing.T) {
	var stdout, stderr bytes.Buffer
	started := false
	runner := Runner{Hooks: Hooks{LocalCommand: func(context.Context, LocalCommandRequest) (LocalCommandResponse, error) {
		started = true
		return LocalCommandResponse{}, nil
	}}}
	code := runner.Run(context.Background(), []string{"get", "http://127.0.0.1/?private=secret", "--json"}, Streams{Stdout: &stdout, Stderr: &stderr})
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if code != ExitURLBlocked || started || result["reason"] != "network_policy" || !strings.Contains(result["hint"].(string), "--allow-localhost") || strings.Contains(stdout.String(), "secret") {
		t.Fatalf("code=%d started=%t response=%s", code, started, &stdout)
	}
}

func TestCLIHTTPStatusKeepsContentAndRecoverySeparate(t *testing.T) {
	for _, command := range []string{"get", "fetch"} {
		for _, status := range []int{200, 403, 429} {
			page := &fakeLocalPage{gotoStatus: status, html: "<p>Site response</p>", bodyText: "Site response", fetchStatus: status, fetchBody: []byte("Site response")}
			restore := fakeOpenPage(t, page)
			for _, useJSON := range []bool{true, false} {
				r, err := defaultLocalCommand(context.Background(), LocalCommandRequest{Command: command, Args: []string{"https://example.com"}, JSON: useJSON, Flags: map[string]any{"text": true}})
				if err != nil || r.ExitCode != ExitOK || !strings.Contains(string(r.Stdout), "Site response") {
					t.Fatalf("%s HTTP %d: %+v, %v", command, status, r, err)
				}
				if useJSON {
					var result map[string]any
					if err := json.Unmarshal(r.Stdout, &result); err != nil {
						t.Fatal(err)
					}
					if result["status"] != float64(status) || (result["http_recovery"] != nil) != (status >= 400) || result["error"] != nil {
						t.Fatalf("%s HTTP %d: %s", command, status, r.Stdout)
					}
				} else if (r.Stderr != "") != (status >= 400) {
					t.Fatalf("%s HTTP %d stderr = %q", command, status, r.Stderr)
				}
			}
			restore()
		}
	}
	if navigationStatus(nil) != 0 {
		t.Fatal("missing response invented a status")
	}
}
