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
)

func TestManagedRuntimeLaunch(t *testing.T) {
	if os.Getenv("GOMOUFOX_RUNTIME_LAUNCH") != "1" {
		t.Skip("set GOMOUFOX_RUNTIME_LAUNCH=1 to run the managed runtime launch gate")
	}
	testManagedRuntimeLaunch(t, SidecarRuntimeNodeDirect)
}

func TestManagedPythonEnvironmentLaunch(t *testing.T) {
	if os.Getenv("GOMOUFOX_PYTHON_RUNTIME_LAUNCH") != "1" {
		t.Skip("set GOMOUFOX_PYTHON_RUNTIME_LAUNCH=1 to run the managed Python environment launch gate")
	}
	testManagedRuntimeLaunch(t, SidecarRuntimePython)
}

func testManagedRuntimeLaunch(t *testing.T, runtime SidecarRuntime) {
	t.Helper()
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
		opts.Runtime = runtime
	}); err != nil {
		t.Fatalf("install managed %s runtime: %v", runtime, err)
	}
	browser, err := New(ctx,
		WithVenvDir(venvDir),
		WithSidecarRuntime(runtime),
		WithAutoInstall(false),
		WithAllowLocalhost(true),
	)
	if err != nil {
		t.Fatalf("launch managed %s runtime: %v", runtime, err)
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
