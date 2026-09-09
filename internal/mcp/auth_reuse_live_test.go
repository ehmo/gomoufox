package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/policy"
)

func TestLiveMCPEvaluationAndAuthReuse(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE_LOGIN") != "1" {
		t.Skip("set GOMOUFOX_LIVE_LOGIN=1 for fresh MCP auth checks")
	}
	if cache := os.Getenv("GOMOUFOX_RUNTIME_LAUNCH_VENV"); cache != "" {
		old := newGomoufoxForMCP
		t.Cleanup(func() { newGomoufoxForMCP = old })
		newGomoufoxForMCP = func(ctx context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
			return gomoufox.New(ctx, append(opts, gomoufox.WithVenvDir(cache))...)
		}
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "fixture_auth", Value: "authenticated", Path: "/", HttpOnly: true, Expires: time.Now().Add(time.Hour)})
			_, _ = w.Write([]byte(`<!doctype html><script>localStorage.setItem('fixture-auth','authenticated')</script><p>Signed in</p>`))
		} else {
			c, err := r.Cookie("fixture_auth")
			if err != nil || c.Value != "authenticated" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`<!doctype html><p id="authenticated">Authenticated request</p><script>window.pageWorldMarker='page-world'</script>`))
		}
	}))
	defer fixture.Close()
	cfg := Config{Policy: policy.DefaultConfig(), SessionDir: t.TempDir()}
	cfg.Policy.AllowLocalhost = true
	cfg.Policy.AllowSessionImport = true
	cfg.Policy.AllowSessionExport = true
	cfg.Policy.EnableEval = true
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	call := func(t *testing.T, name string, args map[string]any) map[string]any {
		t.Helper()
		res := server.Handle(ctx, name, mustRaw(t, args))
		if res.IsError {
			t.Fatalf("%s: %+v", name, res)
		}
		return res.Payload
	}
	for _, mode := range []string{"import-create", "import-load", "profile"} {
		t.Run(mode, func(t *testing.T) {
			id := "auth"
			args := map[string]any{"session_id": id}
			if mode == "profile" {
				args["profile_path"] = "fixture"
			}
			call(t, "session_create", args)
			call(t, "browser_navigate", map[string]any{"session_id": id, "url": fixture.URL + "/login"})
			call(t, "session_save", map[string]any{"session_id": id, "path": "state.json", "overwrite": true})
			call(t, "session_destroy", map[string]any{"session_id": id})
			if mode == "import-create" {
				args["storage_state_path"] = "state.json"
			}
			call(t, "session_create", args)
			if mode == "import-load" {
				call(t, "session_load", map[string]any{"session_id": id, "path": "state.json"})
			}
			call(t, "browser_navigate", map[string]any{"session_id": id, "url": fixture.URL + "/check"})
			call(t, "browser_wait_for", map[string]any{"session_id": id, "selector": "#authenticated", "timeout_ms": 3000})
			for _, probe := range []struct {
				script    string
				arg, want any
			}{
				{"1+1", nil, float64(2)},
				{"localStorage.getItem('fixture-auth')", nil, "authenticated"},
				{"window.pageWorldMarker", nil, "page-world"},
				{"arg => arg.value+1", map[string]any{"value": 2}, float64(3)},
				{"async arg => { await new Promise(resolve=>setTimeout(resolve,25)); return arg.value+1; }", map[string]any{"value": 3}, float64(4)},
				{"(async () => {await new Promise(resolve=>setTimeout(resolve,25));return 5;})()", nil, float64(5)},
			} {
				response := server.Handle(ctx, "browser_evaluate", mustRaw(t, map[string]any{"session_id": id, "script": probe.script, "arg": probe.arg}))
				got, _ := json.Marshal(response.Payload["result"])
				want, _ := json.Marshal(probe.want)
				if response.IsError || string(got) != string(want) {
					t.Errorf("%s: %+v want=%s", probe.script, response, want)
				}
			}
			call(t, "session_destroy", map[string]any{"session_id": id})
		})
	}
}
