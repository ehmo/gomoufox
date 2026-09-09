package gomoufox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestLivePersistentLocalStorageFlushesAtClose(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE_LOGIN") != "1" {
		t.Skip("set GOMOUFOX_LIVE_LOGIN=1 for persistent storage checks")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><p>Persistent storage fixture</p>"))
	}))
	defer fixture.Close()
	for _, mode := range []SidecarRuntime{SidecarRuntimeNodeDirect, SidecarRuntimePython} {
		for _, closeContext := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/context-close=%v", mode, closeContext), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				opts := []Option{WithPersistentContext(t.TempDir()), WithAutoInstall(false), WithAllowLocalhost(true), WithSidecarRuntime(mode)}
				if cache := os.Getenv("GOMOUFOX_RUNTIME_LAUNCH_VENV"); cache != "" {
					opts = append(opts, WithVenvDir(cache))
				}
				if mode == SidecarRuntimePython {
					opts = append(opts, WithHumanize(0))
				}
				for attempt := 0; attempt < 2; attempt++ {
					b, err := New(ctx, opts...)
					if err != nil {
						t.Fatal(err)
					}
					defer b.Close()
					c, err := b.NewContext(ctx)
					if err != nil {
						t.Fatal(err)
					}
					p, err := c.NewPage(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := p.Goto(ctx, fixture.URL); err != nil {
						t.Fatal(err)
					}
					if attempt == 0 {
						if _, err := p.EvaluateInternal(ctx, `()=>localStorage.setItem('fixture','persist')`); err != nil {
							t.Fatal(err)
						}
					} else {
						value, err := p.EvaluateInternal(ctx, `()=>localStorage.getItem('fixture')`)
						if err != nil || value != "persist" {
							t.Fatalf("reopened storage=%v err=%v", value, err)
						}
					}
					if closeContext {
						if err := c.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if err := b.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
