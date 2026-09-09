package gomoufox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ehmo/gomoufox/camoufoxcfg"
)

func TestLiveLoginSurvivesWindowCloseAndStorageImport(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE_LOGIN") != "1" {
		t.Skip("set GOMOUFOX_LIVE_LOGIN=1 for managed headful login checks")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "fixture_login", Value: "authenticated", Path: "/", HttpOnly: true})
			_, _ = w.Write([]byte(`<!doctype html><script>localStorage.setItem('fixture-login','authenticated')</script>`))
		} else {
			_, _ = w.Write([]byte(`<!doctype html><p>Session check</p>`))
		}
	}))
	defer server.Close()
	for _, runtime := range []SidecarRuntime{SidecarRuntimeNodeDirect, SidecarRuntimePython} {
		for _, persistent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/profile=%v", runtime, persistent), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				opts := []Option{WithSidecarRuntime(runtime), WithAutoInstall(false), WithAllowLocalhost(true), WithHeadless(camoufoxcfg.HeadlessFalse), WithFirefoxUserPrefs(camoufoxcfg.FirefoxUserPrefs{"dom.allow_scripts_to_close_windows": true})}
				if runtime == SidecarRuntimePython {
					opts = append(opts, WithHumanize(0))
				}
				if persistent {
					opts = append(opts, WithPersistentContext(t.TempDir()))
				}
				browser, err := New(ctx, opts...)
				if err != nil {
					t.Fatal(err)
				}
				defer browser.Close()
				bc, err := browser.NewContext(ctx)
				if err != nil {
					t.Fatal(err)
				}
				page, err := bc.NewPage(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := page.Goto(ctx, server.URL+"/login"); err != nil {
					t.Fatal(err)
				}
				for _, other := range bc.Pages() {
					if other.URL() != page.URL() {
						_ = other.Close()
					}
				}
				if _, err := page.EvaluateInternal(ctx, `() => {setTimeout(() => window.close(), 20); return true}`); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(2 * time.Second)
				for len(bc.Pages()) > 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if len(bc.Pages()) != 0 {
					t.Fatal("fixture window did not close")
				}
				// The first storage export must happen after the last window closes.
				state, err := bc.StorageState(ctx, "")
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Cookies) != 1 || !state.Cookies[0].HTTPOnly || len(state.Origins) != 1 {
					t.Fatalf("login storage incomplete: cookies=%d origins=%d", len(state.Cookies), len(state.Origins))
				}
				if err := browser.Close(); err != nil {
					t.Fatal(err)
				}
				next, err := New(ctx, WithAutoInstall(false), WithAllowLocalhost(true))
				if err != nil {
					t.Fatal(err)
				}
				defer next.Close()
				nextContext, err := next.NewContext(ctx, WithStorageState(state))
				if err != nil {
					t.Fatal(err)
				}
				nextPage, err := nextContext.NewPage(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := nextPage.Goto(ctx, server.URL+"/check"); err != nil {
					t.Fatal(err)
				}
				value, err := nextPage.EvaluateInternal(ctx, `() => localStorage.getItem('fixture-login')`)
				if err != nil || value != "authenticated" {
					t.Fatalf("imported localStorage=%v, %v", value, err)
				}
				cookies, err := nextContext.Cookies(ctx)
				if err != nil || len(cookies) != 1 || cookies[0].Value != "authenticated" {
					t.Fatalf("imported cookies=%d, %v", len(cookies), err)
				}
			})
		}
	}
}
