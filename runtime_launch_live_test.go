package gomoufox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehmo/gomoufox/camoufoxcfg"
)

func TestManagedRuntimeLaunch(t *testing.T) {
	if os.Getenv("GOMOUFOX_RUNTIME_LAUNCH") != "1" {
		t.Skip("set GOMOUFOX_RUNTIME_LAUNCH=1 to run the managed runtime launch gate")
	}
	venvDir := os.Getenv("GOMOUFOX_RUNTIME_LAUNCH_VENV")
	if venvDir == "" {
		t.Fatal("GOMOUFOX_RUNTIME_LAUNCH_VENV must name an isolated runtime cache")
	}
	if !filepath.IsAbs(venvDir) {
		t.Fatalf("GOMOUFOX_RUNTIME_LAUNCH_VENV = %q, want an absolute path", venvDir)
	}

	const marker = "gomoufox-managed-runtime-javascript-ok"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html>
<html><body>
<p id="runtime-marker">javascript-did-not-run</p>
<script>document.querySelector("#runtime-marker").textContent = %q;</script>
</body></html>`, marker)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if err := EnsureInstalled(ctx, func(opts *InstallOptions) {
		opts.VenvDir = venvDir
		opts.Runtime = SidecarRuntimeNodeDirect
	}); err != nil {
		t.Fatalf("install managed node-direct runtime: %v", err)
	}
	browser, err := New(ctx,
		WithVenvDir(venvDir),
		WithSidecarRuntime(SidecarRuntimeNodeDirect),
		WithAutoInstall(false),
		WithHeadless(camoufoxcfg.HeadlessTrue),
		WithAllowLocalhost(true),
	)
	if err != nil {
		t.Fatalf("launch managed node-direct runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := browser.Close(); err != nil {
			t.Errorf("close browser: %v", err)
		}
	})

	page, err := browser.NewPage(ctx)
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	response, err := page.Goto(ctx, server.URL, WaitUntil("domcontentloaded"), WithTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("navigate to local test page: %v", err)
	}
	if response == nil || response.Status() != http.StatusOK {
		t.Fatalf("local test page response = %#v, want HTTP %d", response, http.StatusOK)
	}
	text, err := page.Locator("#runtime-marker").TextContent(ctx, LocatorTextTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("read JavaScript-written marker: %v", err)
	}
	if text != marker {
		t.Fatalf("JavaScript-written marker = %q, want %q", text, marker)
	}
}
