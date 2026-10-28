package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/policy"
)

// TestLiveMCPLongLivedPagination uses the MCP handlers and managed runtime.
// Heap samples describe the Node sidecar, not the browser or Go process.
func TestLiveMCPLongLivedPagination(t *testing.T) {
	skipUnlessLiveMCP(t)
	dir := os.Getenv("GOMOUFOX_MEMORY_EVIDENCE")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checkpoint.json", "node-heap.jsonl"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	requestPath, _ := json.Marshal(filepath.Join(dir, "checkpoint.json"))
	samplePath, _ := json.Marshal(filepath.Join(dir, "node-heap.jsonl"))
	probe := fmt.Sprintf(`const fs = require('fs');
const v8 = require('v8');
v8.setFlagsFromString('--expose_gc');
const collectGarbage = require('vm').runInNewContext('gc');
const path = require('path');
const root = path.dirname(require.resolve('playwright-core', {paths: [path.dirname(process.argv[1])]}));
const {ElementHandleDispatcher} = require(path.join(root, 'lib/server/dispatchers/elementHandlerDispatcher.js'));
let last = '', busy = false;
setInterval(() => {
  if (busy || !fs.existsSync(%s)) return;
  const request = fs.readFileSync(%s, 'utf8');
  if (request === last) return;
  last = request; busy = true;
  const checkpoint = JSON.parse(request);
  const sample = () => {
    const elementHandles = checkpoint.gc ? v8.queryObjects(ElementHandleDispatcher) : -1;
    fs.appendFileSync(%s, JSON.stringify({checkpoint: checkpoint.cycle, pid: process.pid, elementHandles, ...process.memoryUsage(), ...v8.getHeapStatistics()}) + '\n', {mode: 0o600});
    busy = false;
  };
  if (checkpoint.gc) collectGarbage();
  sample();
}, 25).unref();
`, requestPath, requestPath, samplePath)
	probePath := filepath.Join(dir, "heap-probe.cjs")
	if err := os.WriteFile(probePath, []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}
	old := newGomoufoxForMCP
	t.Cleanup(func() { newGomoufoxForMCP = old })
	newGomoufoxForMCP = func(ctx context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
		opts = append(opts, gomoufox.WithAutoInstall(false), gomoufox.WithExtraEnv("NODE_OPTIONS=--require="+probePath))
		return gomoufox.New(ctx, opts...)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "fixture_auth", Value: "authenticated", Path: "/", HttpOnly: true})
			fmt.Fprint(w, `<!doctype html><script>localStorage.setItem('fixture-auth','authenticated')</script><p>Signed in</p>`)
			return
		}
		cookie, err := r.Cookie("fixture_auth")
		if err != nil || cookie.Value != "authenticated" {
			http.Error(w, "missing login", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Library fixture</title><style>#rows {height:600px;overflow:auto}</style><main><p id="status"></p><button id="select">Select All</button><button id="deliver">Deliver to device</button><div id="rows"></div><dialog id="delivery"><input id="device" type="checkbox"><button id="apply">Make Changes</button></dialog><dialog id="result"><p>Request to deliver title(s) has been sent.</p><button id="close">Close</button></dialog><button id="next">Next page</button></main><script>
let cycle=1;
function render() {
 document.querySelector('#status').textContent='Page '+cycle+' authenticated='+localStorage.getItem('fixture-auth');
 document.querySelector('#rows').innerHTML=Array.from({length:2000},(_,i)=>'<article><span>'+('row '+i+' ').repeat(40)+'</span><button id="item'+i+'">Item '+i+'</button></article>').join('');
}
document.querySelector('#select').onclick=()=>document.querySelector('#status').textContent+=' Selected 25';
document.querySelector('#deliver').onclick=()=>document.querySelector('#delivery').showModal();
document.querySelector('#apply').onclick=()=>{document.querySelector('#delivery').close();document.querySelector('#result').showModal();};
document.querySelector('#close').onclick=()=>document.querySelector('#result').close();
document.querySelector('#next').onclick=()=>{cycle++;render();};
render();</script>`)
	}))
	defer fixture.Close()
	cfg := Config{Policy: policy.DefaultConfig(), SessionDir: t.TempDir()}
	cfg.Policy.AllowLocalhost = true
	s := newTestServer(t, cfg)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		args["session_id"] = "memory"
		res := s.Handle(ctx, name, mustRaw(t, args))
		if res.IsError {
			t.Fatalf("%s: %+v (heap samples: %s)", name, res, dir)
		}
		return res.Payload
	}
	type heapSample struct {
		Checkpoint int    `json:"checkpoint"`
		HeapUsed   uint64 `json:"heapUsed"`
		Handles    int    `json:"elementHandles"`
	}
	sample := func(cycle int, gc bool) heapSample {
		t.Helper()
		request, _ := json.Marshal(map[string]any{"cycle": cycle, "gc": gc})
		pending := filepath.Join(dir, "checkpoint.tmp")
		if err := os.WriteFile(pending, request, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(pending, filepath.Join(dir, "checkpoint.json")); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(filepath.Join(dir, "node-heap.jsonl"))
			for _, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var sample heapSample
				if json.Unmarshal([]byte(row), &sample) == nil && sample.Checkpoint == cycle && sample.HeapUsed > 0 {
					t.Logf("cycle=%d node_heap_used=%d element_handles=%d", cycle, sample.HeapUsed, sample.Handles)
					return sample
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("missing Node heap checkpoint %d in %s", cycle, dir)
		return heapSample{}
	}
	call("session_create", map[string]any{"width": 1280, "height": 900, "reuse_if_exists": true, "headful": os.Getenv("GOMOUFOX_MEMORY_HEADFUL") == "1"})
	call("browser_navigate", map[string]any{"url": fixture.URL + "/login"})
	call("browser_navigate", map[string]any{"url": fixture.URL + "/page"})
	call("browser_wait_for", map[string]any{"selector": "#select", "timeout_ms": 5000})
	bounded := call("browser_get_content", map[string]any{"selector": "#rows", "format": "html", "max_bytes": 1024})
	if bounded["truncated"] != true || len(fmt.Sprint(bounded["content"])) > 1024 || !strings.Contains(fmt.Sprint(bounded["truncation_sources"]), "html_acquisition") {
		t.Fatalf("large DOM acquisition was not capped: %v", bounded)
	}
	baseline := sample(0, true)
	for cycle := 1; cycle <= 41; cycle++ {
		call("browser_snapshot", map[string]any{"interactive_only": true, "max_elements": 250})
		call("browser_click", map[string]any{"selector": "#select"})
		status := call("browser_get_content", map[string]any{"selector": "#status", "format": "text", "max_bytes": 8000})
		if !strings.Contains(fmt.Sprint(status), fmt.Sprintf("Page %d authenticated=authenticated Selected 25", cycle)) {
			t.Fatalf("page state lost at cycle %d: %v", cycle, status)
		}
		call("browser_click", map[string]any{"selector": "#deliver"})
		call("browser_get_content", map[string]any{"selector": "#delivery", "format": "html", "max_bytes": 14000})
		call("browser_set_checked", map[string]any{"selector": "#device", "checked": true})
		call("browser_click", map[string]any{"selector": "#apply"})
		call("browser_wait_for", map[string]any{"selector": "#result[open] #close", "timeout_ms": 5000})
		result := call("browser_get_content", map[string]any{"selector": "#result", "format": "text", "max_bytes": 5000})
		if !strings.Contains(fmt.Sprint(result), "Request to deliver title(s) has been sent.") {
			t.Fatalf("missing result: %v", result)
		}
		call("browser_click", map[string]any{"selector": "#close"})
		if cycle < 41 {
			call("browser_click", map[string]any{"selector": "#next"})
		}
		retained := sample(cycle, cycle == 41)
		if cycle == 41 && retained.HeapUsed > baseline.HeapUsed+16*1024*1024 {
			t.Errorf("Node retained heap grew by %d bytes after 41 cycles", retained.HeapUsed-baseline.HeapUsed)
		}
		if cycle == 41 && retained.Handles > baseline.Handles+4 {
			t.Errorf("Node retained %d extra element handles after 41 cycles", retained.Handles-baseline.Handles)
		}
	}
}
