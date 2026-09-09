package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/policy"
)

func TestLiveSessionAcquisitionPreservesPageState(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 for live session acquisition")
	}
	if cache := os.Getenv("GOMOUFOX_RUNTIME_LAUNCH_VENV"); cache != "" {
		old := newGomoufoxForMCP
		t.Cleanup(func() { newGomoufoxForMCP = old })
		newGomoufoxForMCP = func(ctx context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
			return gomoufox.New(ctx, append(opts, gomoufox.WithVenvDir(cache))...)
		}
	}
	var requests atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><p>Session fixture</p>"))
	}))
	defer fixture.Close()
	cfg := Config{Policy: policy.DefaultConfig(), SessionDir: t.TempDir()}
	cfg.Policy.MaxSessions = 1
	cfg.Policy.AllowLocalhost = true
	cfg.Policy.EnableEval = true
	s := newTestServer(t, cfg)
	defer s.Close()
	ctx := context.Background()
	create := raw(`{"session_id":"work","width":390,"height":844,"reuse_if_exists":true}`)
	if r := s.Handle(ctx, "session_create", create); r.IsError {
		t.Fatal(r)
	}
	assertError(t, s.Handle(ctx, "session_create", raw(`{"session_id":"overflow"}`)), "session_limit")
	navigate := map[string]any{"url": fixture.URL, "session_id": "overflow", "require_existing": true}
	assertError(t, s.Handle(ctx, "browser_navigate", mustRaw(t, navigate)), "session_not_found")
	if requests.Load() != 0 {
		t.Fatal("rejected session made network request")
	}
	navigate["session_id"] = "work"
	if r := s.Handle(ctx, "browser_navigate", mustRaw(t, navigate)); r.IsError {
		t.Fatal(r)
	}
	if r := s.Handle(ctx, "browser_evaluate", raw(`{"session_id":"work","script":"localStorage.setItem('reuse-marker','retained')"}`)); r.IsError {
		t.Fatal(r)
	}
	original := s.sessions.sessions["work"].browser
	if r := s.Handle(ctx, "session_create", create); r.IsError || r.Payload["reused"] != true || r.Payload["browser_started"] != true {
		t.Fatal(r)
	}
	if s.sessions.sessions["work"].browser != original {
		t.Fatal("acquisition replaced browser")
	}
	if r := s.Handle(ctx, "browser_navigate", mustRaw(t, navigate)); r.IsError {
		t.Fatal(r)
	}
	r := s.Handle(ctx, "browser_evaluate", raw(`{"session_id":"work","script":"localStorage.getItem('reuse-marker')"}`))
	if r.IsError || r.Payload["result"] != "retained" {
		t.Fatal(r)
	}
}
