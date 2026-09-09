package gomoufox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ehmo/gomoufox/camoufoxcfg"
)

func TestLiveContextViewportControlsLayout(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 to run managed Camoufox viewport checks")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>html,body{margin:0}#layout{width:100vw;height:100vh}</style><div id="layout"></div>`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent=%v", persistent), func(t *testing.T) {
			opts := []Option{WithAutoInstall(false), WithAllowLocalhost(true), WithOS(camoufoxcfg.OSMacOS)}
			if persistent {
				opts = append(opts, WithPersistentContext(t.TempDir()))
			}
			browser, err := New(ctx, opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer browser.Close()
			for _, width := range []int{390, 1440, 390} {
				page, err := browser.NewPage(ctx, WithViewport(width, 844))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := page.Goto(ctx, server.URL); err != nil {
					t.Fatal(err)
				}
				value, err := page.EvaluateInternal(ctx, `() => ({width:innerWidth,height:innerHeight,client:document.documentElement.clientWidth,rect:document.querySelector('#layout').getBoundingClientRect().width,small:matchMedia('(max-width:500px)').matches})`)
				if err != nil {
					t.Fatal(err)
				}
				var metrics struct {
					Width, Height, Client, Rect int
					Small                       bool
				}
				raw, _ := json.Marshal(value)
				if err := json.Unmarshal(raw, &metrics); err != nil {
					t.Fatal(err)
				}
				if metrics.Width != width || metrics.Height != 844 || metrics.Client != width || metrics.Rect != width || metrics.Small != (width <= 500) {
					t.Errorf("viewport %dx844: %s", width, raw)
				}
				shot, err := page.Screenshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				image, err := png.DecodeConfig(bytes.NewReader(shot))
				if err != nil || image.Width != width || image.Height != 844 {
					t.Errorf("capture %dx844: %v, %v", width, image, err)
				}
				if err := page.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
