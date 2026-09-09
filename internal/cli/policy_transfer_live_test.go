package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ehmo/gomoufox"
)

func TestLiveCLILocalhostOptInPreservesBinaryPOST(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 for live localhost and binary POST checks")
	}
	if cache := os.Getenv("GOMOUFOX_RUNTIME_LAUNCH_VENV"); cache != "" {
		old := newGomoufoxForLocal
		t.Cleanup(func() { newGomoufoxForLocal = old })
		newGomoufoxForLocal = func(ctx context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
			return gomoufox.New(ctx, append(opts, gomoufox.WithVenvDir(cache))...)
		}
	}
	data := bytes.Repeat([]byte{0, 255, 128, 1, 42, 13, 10, 254}, 128*1024)
	path := filepath.Join(t.TempDir(), "binary.dat")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "fixture", HttpOnly: true, Path: "/"})
			_, _ = io.WriteString(w, "<p>upload fixture</p>")
			return
		}
		cookie, err := r.Cookie("session")
		if r.Method != http.MethodPost || err != nil || cookie.Value != "fixture" {
			http.Error(w, "missing authenticated POST", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, int64(len(data)+1)))
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"bytes": len(body), "sha256": fmt.Sprintf("%x", sha256.Sum256(body))})
	}))
	defer fixture.Close()
	args := []string{"fetch", fixture.URL + "/upload", "--navigate-first", fixture.URL, "--method", "POST", "--data-file", path, "--json"}
	var stdout, stderr bytes.Buffer
	streams := Streams{Stdout: &stdout, Stderr: &stderr}
	if code := (Runner{}).Run(context.Background(), args, streams); code != ExitURLBlocked || requests.Load() != 0 {
		t.Fatalf("default policy: code=%d requests=%d stderr=%s", code, requests.Load(), &stderr)
	}
	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), append(args, "--allow-localhost"), streams); code != ExitOK {
		t.Fatalf("allowed POST: code=%d stderr=%s", code, &stderr)
	}
	var result struct {
		Status int
		Body   struct {
			Bytes  int
			SHA256 string
		}
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != 200 || result.Body.Bytes != len(data) || result.Body.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatalf("binary POST = %s", &stdout)
	}
}
