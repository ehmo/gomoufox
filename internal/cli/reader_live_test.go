package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox"
)

func TestLiveCLIReaderTextAndMarkdownFallback(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 for live reader extraction")
	}
	if cache := os.Getenv("GOMOUFOX_RUNTIME_LAUNCH_VENV"); cache != "" {
		old := newGomoufoxForLocal
		t.Cleanup(func() { newGomoufoxForLocal = old })
		newGomoufoxForLocal = func(ctx context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
			return gomoufox.New(ctx, append(opts, gomoufox.WithVenvDir(cache))...)
		}
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/large" {
			_, _ = w.Write([]byte(`<head><style>` + strings.Repeat("a", 600000) + `</style></head><body><h1>BODY_AFTER_LARGE_HEAD</h1><p>reader content</p></body>`))
		} else {
			_, _ = w.Write([]byte(`<h1>Visible fixture heading</h1><p>Visible fixture paragraph</p><label>Name <input></label><button>Update</button><script>window.SCRIPT_SENTINEL='script should not appear in extracted reader text'</script>`))
		}
	}))
	defer fixture.Close()
	for _, path := range []string{"/", "/large"} {
		for _, format := range []string{"--text", "--markdown"} {
			var stdout, stderr bytes.Buffer
			args := []string{"get", fixture.URL + path, format, "--allow-localhost", "--max-bytes", "60000", "--json"}
			if code := (Runner{}).Run(context.Background(), args, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
				t.Fatalf("%v: code=%d %s", args, code, &stderr)
			}
			var result struct {
				Content   string
				Truncated bool
				Quality   string `json:"markdown_quality"`
				Method    string `json:"extraction_method"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			want := "Visible fixture heading\nVisible fixture paragraph\nName Update"
			if path == "/large" {
				want = "BODY_AFTER_LARGE_HEAD\nreader content"
			}
			method := "reader_text"
			if format == "--markdown" {
				method = "reader_fallback"
			}
			if result.Content != want || result.Truncated || result.Method != method || (format == "--markdown" && result.Quality != "fallback") {
				t.Fatalf("reader: %s", &stdout)
			}
		}
	}
}
