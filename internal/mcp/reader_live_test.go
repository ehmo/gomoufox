package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/policy"
)

func TestLiveMCPReaderExtraction(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 for live reader extraction")
	}
	if cache := os.Getenv("GOMOUFOX_RUNTIME_LAUNCH_VENV"); cache != "" {
		old := newGomoufoxForMCP
		t.Cleanup(func() { newGomoufoxForMCP = old })
		newGomoufoxForMCP = func(ctx context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
			return gomoufox.New(ctx, append(opts, gomoufox.WithVenvDir(cache))...)
		}
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/unicode" {
			_, _ = w.Write([]byte("<!doctype html><p>日本語</p>"))
			return
		}
		_, _ = w.Write([]byte(`<head><style>` + strings.Repeat("a", 600000) + `</style></head><body><h1>Visible fixture heading</h1><p>Visible fixture paragraph</p><script>window.SCRIPT_SENTINEL='private code'</script></body>`))
	}))
	defer fixture.Close()
	cfg := Config{Policy: policy.DefaultConfig(), SessionDir: t.TempDir()}
	cfg.Policy.AllowLocalhost = true
	cfg.Policy.ContentWarning = false
	s := newTestServer(t, cfg)
	defer s.Close()
	ctx := context.Background()
	if r := s.Handle(ctx, "browser_navigate", mustRaw(t, map[string]any{"url": fixture.URL})); r.IsError {
		t.Fatal(r)
	}
	for _, format := range []string{"text", "markdown"} {
		r := s.Handle(ctx, "browser_get_content", mustRaw(t, map[string]any{"format": format, "max_bytes": 60000}))
		if r.IsError {
			_, err := s.sessions.sessions["default"].browser.PageContent(ctx, pageContentOptions{MaxBytes: 60000, IncludeHTML: format != "text", IncludeText: true})
			t.Fatalf("%s: %v; response=%+v", format, err, r)
		}
		if r.IsError || r.Payload["content"] != "Visible fixture heading\nVisible fixture paragraph" {
			t.Fatal(r)
		}
		if format == "markdown" && (r.Payload["markdown_quality"] != "fallback" || r.Payload["extraction_method"] != "reader_fallback" || r.Payload["truncated"] != true || strings.Join(r.Payload["truncation_sources"].([]string), ",") != "html_acquisition") {
			t.Fatal("HTML acquisition cap must remain observable", r)
		}
		if format == "text" && (r.Payload["truncated"] != false || r.Payload["extraction_method"] != "reader_text") {
			t.Fatal("large head consumed text budget", r)
		}
	}
	if r := s.Handle(ctx, "browser_navigate", mustRaw(t, map[string]any{"url": fixture.URL + "/unicode"})); r.IsError {
		t.Fatal(r)
	}
	for _, format := range []string{"text", "html"} {
		limit, want, source := 4, "日", "text_acquisition"
		if format == "html" {
			limit, want, source = 7, "<p>日", "html_acquisition"
		}
		r := s.Handle(ctx, "browser_get_content", mustRaw(t, map[string]any{"format": format, "selector": "p", "max_bytes": limit}))
		if r.IsError || r.Payload["content"] != want || r.Payload["bytes"] != len(want) || strings.Join(r.Payload["truncation_sources"].([]string), ",") != source {
			t.Fatal(r)
		}
	}
}
