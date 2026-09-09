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

func TestHeadfulSessionUsesDedicatedBrowser(t *testing.T) {
	server := newTestServer(t, defaultTestConfig(t))
	defer server.Close()
	result := server.Handle(context.Background(), "session_create", raw(`{"session_id":"login","headful":true}`))
	if result.IsError || result.Payload["headful"] != true || result.Payload["browser_started"] != false {
		t.Fatalf("create: %+v", result)
	}
	state, _ := server.sessions.touchState("login", nil)
	if !state.toOptions().headful || sessionList(t, server.Handle(context.Background(), "session_list", raw(`{}`)))[0]["headful"] != true {
		t.Fatal("headful option lost")
	}
	page := &fakeMCPPage{}
	bc := &fakeMCPContext{page: page}
	browser := &fakeMCPBrowser{context: bc}
	launcher := &fakeGomoufoxLauncher{browsers: []mcpBrowser{browser}}
	factory := &gomoufoxFactory{launcher: launcher}
	session, err := factory.NewBrowserSession(context.Background(), state.toOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || !launcher.calls[0].dedicated || !launcher.calls[0].opts.headful {
		t.Fatalf("launch: %+v", launcher.calls)
	}
	if err := session.Close(); err != nil || page.closeCalls != 0 || bc.closeCalls != 1 || browser.closeCalls != 1 {
		t.Fatalf("cleanup err=%v page=%d context=%d browser=%d", err, page.closeCalls, bc.closeCalls, browser.closeCalls)
	}
	oldNew := newGomoufoxForMCP
	defer func() { newGomoufoxForMCP = oldNew }()
	called := false
	newGomoufoxForMCP = func(_ context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
		called = true
		if len(opts) != 3 {
			t.Fatalf("headful launch options=%d", len(opts))
		}
		return &gomoufox.Browser{}, nil
	}
	if _, err := (realGomoufoxLauncher{}).Launch(context.Background(), sessionOptions{headful: true}, true); err != nil || !called {
		t.Fatalf("launch err=%v called=%v", err, called)
	}
}

func TestLiveMCPLoginHandoff(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE_LOGIN") != "1" {
		t.Skip("set GOMOUFOX_LIVE_LOGIN=1 for visible managed login")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "fixture_login", Value: "authenticated", Path: "/", HttpOnly: true})
			_, _ = w.Write([]byte(`<!doctype html><script>localStorage.setItem('fixture-login','authenticated')</script><p id="ready">Signed in</p>`))
		case "/check":
			c, err := r.Cookie("fixture_login")
			if err != nil || c.Value != "authenticated" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`<!doctype html><p id="authenticated">Authenticated request</p>`))
		default:
			_, _ = w.Write([]byte(`<!doctype html><a id="login" href="/login">Sign in</a>`))
		}
	}))
	defer fixture.Close()
	for _, profile := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "profile"}[profile], func(t *testing.T) {
			cfg := Config{Policy: policy.DefaultConfig(), SessionDir: t.TempDir()}
			cfg.Policy.AllowLocalhost = true
			cfg.Policy.AllowSessionExport = true
			server, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				res := server.Handle(ctx, name, mustRaw(t, args))
				if res.IsError {
					t.Fatalf("%s: %+v", name, res)
				}
				return res.Payload
			}
			args := map[string]any{"session_id": "login", "headful": true}
			if profile {
				args["profile_path"] = "fixture"
			}
			call("session_create", args)
			if profile {
				conflict := server.Handle(ctx, "session_create", raw(`{"session_id":"conflict","profile_path":"fixture"}`))
				assertError(t, conflict, "profile_in_use")
				if conflict.Payload["owner_session_id"] != "login" {
					t.Fatalf("profile owner: %+v", conflict)
				}
			}
			call("browser_navigate", map[string]any{"session_id": "login", "url": fixture.URL})
			call("browser_click", map[string]any{"session_id": "login", "selector": "#login"})
			call("browser_wait_for", map[string]any{"session_id": "login", "selector": "#ready", "timeout_ms": 5000})
			original, _ := server.sessions.touchState("login", nil)
			owned := original.browser
			call("session_save", map[string]any{"session_id": "login", "path": "login.json"})
			data, err := os.ReadFile(server.jail.Root + "/login.json")
			if err != nil {
				t.Fatal(err)
			}
			var saved gomoufox.StorageState
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if len(saved.Cookies) != 1 || !saved.Cookies[0].HTTPOnly || len(saved.Origins) != 1 {
				t.Fatalf("incomplete storage: %s", data)
			}
			call("browser_navigate", map[string]any{"session_id": "login", "url": fixture.URL + "/check"})
			call("browser_wait_for", map[string]any{"session_id": "login", "selector": "#authenticated", "timeout_ms": 5000})
			after, _ := server.sessions.touchState("login", nil)
			if after != original || after.browser != owned {
				t.Fatal("login session was replaced")
			}
			call("session_destroy", map[string]any{"session_id": "login"})
		})
	}
}
