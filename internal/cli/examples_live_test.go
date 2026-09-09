package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ehmo/gomoufox"
)

func TestLiveCLIArgumentAndRawExamples(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 for CLI examples")
	}
	if cache := os.Getenv("GOMOUFOX_RUNTIME_LAUNCH_VENV"); cache != "" {
		old := newGomoufoxForLocal
		t.Cleanup(func() { newGomoufoxForLocal = old })
		newGomoufoxForLocal = func(ctx context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
			return gomoufox.New(ctx, append(opts, gomoufox.WithVenvDir(cache))...)
		}
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":42}`))
	}))
	defer fixture.Close()
	argumentPath := filepath.Join(t.TempDir(), "argument.json")
	if err := os.WriteFile(argumentPath, []byte(`{"selector":"h1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, argFlags := range [][]string{{"--arg", `{"selector":"h1"}`}, {"--arg-file", argumentPath}} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"eval", fixture.URL, "--allow-localhost", "--enable-eval", "--script", "arg => arg.selector"}, argFlags...)
		code := (Runner{}).Run(context.Background(), args, Streams{Stdout: &stdout, Stderr: &stderr})
		if code != ExitOK || stdout.String() != "\"h1\"\n" {
			t.Fatalf("argument example: %d %s %s", code, &stdout, &stderr)
		}
	}
	for _, flags := range [][]string{nil, {"--raw"}, {"--json"}, {"--json", "--raw"}} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"fetch", fixture.URL, "--allow-localhost", "--navigate-first", fixture.URL}, flags...)
		code := (Runner{}).Run(context.Background(), args, Streams{Stdout: &stdout, Stderr: &stderr})
		if code != ExitOK {
			t.Fatalf("raw example: %d %s", code, &stderr)
		}
		if len(flags) == 0 || flags[0] == "--raw" {
			if stdout.String() != `{"value":42}` {
				t.Fatalf("body bytes: %s", &stdout)
			}
			continue
		}
		var result map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		var want any = map[string]any{"value": float64(42)}
		if len(flags) == 2 {
			want = `{"value":42}`
		}
		if !reflect.DeepEqual(result["body"], want) || result["status"] != float64(200) {
			t.Fatalf("body envelope: %s", &stdout)
		}
	}
}
