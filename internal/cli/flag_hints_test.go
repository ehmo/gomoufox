package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestWrongFlagsSuggestOnlySupportedCorrections(t *testing.T) {
	for _, tc := range []struct{ command, flag, hint string }{
		{"screenshot", "output", "use --out"},
		{"get", "url", "URL is positional"},
		{"get", "format", "--html, --text or --markdown"},
		{"screenshot", "format", "filename extension"},
		{"eval", "wait-for-selector", "use --wait-selector"},
		{"get", "wait_until", "use --wait-load-state"},
		{"get", "wait-until", "use --wait-load-state"},
		{"screenshot", "load_state", "use --wait-load-state"},
		{"eval", "load-state", "use --wait-load-state"},
		{"fetch", "max_bytes", "use --max-bytes"},
		{"mcp", "output", ""},
		{"fetch", "format", ""},
		{"get", "allow-private-ips", ""},
	} {
		t.Run(tc.command+"/"+tc.flag, func(t *testing.T) {
			var stderr bytes.Buffer
			code := (Runner{}).Run(context.Background(), []string{tc.command, "--" + tc.flag, "private-input-marker"}, Streams{Stderr: &stderr})
			if code != ExitUsage || !strings.Contains(stderr.String(), "unknown flag") || strings.Contains(stderr.String(), "private-input-marker") {
				t.Fatalf("unsafe correction: %d %s", code, &stderr)
			}
			if tc.hint != "" && !strings.Contains(stderr.String(), tc.hint) {
				t.Fatalf("missing hint: %s", &stderr)
			}
			if tc.hint == "" && strings.Contains(stderr.String(), ";") {
				t.Fatalf("unsupported correction: %s", &stderr)
			}
		})
	}
	var stderr bytes.Buffer
	code := (Runner{}).Run(context.Background(), []string{"--allow_localhost", "get"}, Streams{Stderr: &stderr})
	if code != ExitUsage || !strings.Contains(stderr.String(), "unknown global flag: --allow_localhost; use --allow-localhost") {
		t.Fatal(code, &stderr)
	}
	stderr.Reset()
	code = (Runner{}).Run(context.Background(), []string{"get", "--timeout", "25"}, Streams{Stderr: &stderr})
	if code != ExitUsage || !strings.Contains(stderr.String(), "--timeout: invalid duration; include a unit") {
		t.Fatal(code, &stderr)
	}
}
