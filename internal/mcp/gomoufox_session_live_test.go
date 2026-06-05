package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/camoufoxcfg"
	"github.com/ehmo/gomoufox/internal/a11y"
)

func TestLiveMCPHelpersIgnoreHostilePageWorld(t *testing.T) {
	skipUnlessLiveMCP(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			w.Header().Set("X-Source", "server")
			_, _ = w.Write([]byte("server truth"))
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<!doctype html>
<style>
html, body { margin: 0; min-width: 640px; min-height: 900px; }
#real { display: block; width: 180px; height: 44px; margin: 16px; }
</style>
<main id="trusted">
  <button id="real">Trusted Button</button>
  <input id="secret" aria-label="API token" value="sk-live-secret">
</main>
<script>
window.__patched = true;
window.fetch = async () => new Response("page lie", {status: 418, headers: {"X-Source": "page"}});
Document.prototype.querySelector = () => document.createElement("aside");
Document.prototype.querySelectorAll = () => [document.createElement("aside")];
Object.defineProperty(Element.prototype, "innerText", {get() { return "page lie"; }});
Element.prototype.getAttribute = () => "page-lie";
Element.prototype.getBoundingClientRect = () => ({width: 1, height: 1});
for (const key of ["scrollWidth", "scrollHeight", "clientWidth", "clientHeight", "offsetWidth", "offsetHeight"]) {
  Object.defineProperty(HTMLElement.prototype, key, {get() { return 1; }});
}
Object.defineProperty(window, "innerWidth", {get() { return 1; }});
Object.defineProperty(window, "innerHeight", {get() { return 1; }});
CSS.escape = () => "page-lie";
</script>`))
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := gomoufox.New(ctx,
		gomoufox.WithHeadless(camoufoxcfg.HeadlessTrue),
		gomoufox.WithMainWorldEval(true),
		gomoufox.WithUnsafeDirectNetwork(true),
		gomoufox.WithAutoInstall(false),
	)
	if err != nil {
		t.Fatalf("launch managed Camoufox: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })
	page, err := browser.NewPage(ctx)
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	adaptedPage := adaptPage(page)
	if err := verifyMCPInternalEvaluation(ctx, adaptedPage); err != nil {
		t.Fatalf("internal evaluation startup probe: %v", err)
	}
	session := &gomoufoxSession{page: adaptedPage, refs: a11y.NewStore()}
	if _, err := session.Navigate(ctx, server.URL, navigateOptions{WaitUntil: "domcontentloaded", Timeout: 20 * time.Second}); err != nil {
		t.Fatalf("navigate hostile page: %v", err)
	}

	pageWorld, err := page.Evaluate(ctx, `mw:() => ({
		patched: window.__patched === true,
		queryTag: document.querySelector("main").tagName,
		css: CSS.escape("a b"),
		rectWidth: document.querySelector("#real").getBoundingClientRect().width
	})`)
	if err != nil {
		t.Fatalf("page-world evaluate: %v", err)
	}
	internalWorld, err := page.EvaluateInternal(ctx, `() => ({
		patched: globalThis.__patched === true,
		queryTag: document.querySelector("main").tagName,
		css: CSS.escape("a b"),
		rectWidth: document.querySelector("#real").getBoundingClientRect().width
	})`)
	if err != nil {
		t.Fatalf("internal evaluate: %v", err)
	}
	var pageWorldPayload, internalWorldPayload struct {
		Patched   bool    `json:"patched"`
		QueryTag  string  `json:"queryTag"`
		CSS       string  `json:"css"`
		RectWidth float64 `json:"rectWidth"`
	}
	if err := decodeJSONValue(pageWorld, &pageWorldPayload); err != nil {
		t.Fatalf("decode page-world payload: %v", err)
	}
	if err := decodeJSONValue(internalWorld, &internalWorldPayload); err != nil {
		t.Fatalf("decode internal payload: %v", err)
	}
	if !pageWorldPayload.Patched || pageWorldPayload.QueryTag != "ASIDE" || pageWorldPayload.CSS != "page-lie" || pageWorldPayload.RectWidth != 1 {
		t.Fatalf("page-world did not see hostile patches: %#v", pageWorldPayload)
	}
	if internalWorldPayload.Patched || internalWorldPayload.QueryTag != "MAIN" || internalWorldPayload.CSS == "page-lie" || internalWorldPayload.RectWidth < 100 {
		t.Fatalf("internal evaluation saw hostile patches: %#v", internalWorldPayload)
	}

	evaluated, err := session.Evaluate(ctx, "() => ({patched: window.__patched, helper: typeof window.__gomoufoxMCPNative})", nil, evaluateOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("browser_evaluate: %v", err)
	}
	var evalPayload struct {
		Patched bool   `json:"patched"`
		Helper  string `json:"helper"`
	}
	if err := decodeJSONValue(evaluated, &evalPayload); err != nil {
		t.Fatalf("decode evaluate payload: %v", err)
	}
	if !evalPayload.Patched || evalPayload.Helper != "undefined" {
		t.Fatalf("browser_evaluate payload = %#v", evalPayload)
	}

	fetched, err := session.Fetch(ctx, fetchOptions{URL: server.URL + "/api", Method: "GET", MaxBytes: 64})
	if err != nil {
		t.Fatalf("browser_fetch: %v", err)
	}
	if fetched.Status != 200 || string(fetched.Body) != "server truth" || fetched.Headers["x-source"] != "server" {
		t.Fatalf("fetch saw page-world patch: %#v body=%q", fetched, fetched.Body)
	}
	contentResult, err := session.PageContent(ctx, pageContentOptions{Selector: "main", MaxBytes: 4096, IncludeText: true})
	if err != nil {
		t.Fatalf("browser_get_content: %v", err)
	}
	if !strings.Contains(contentResult.Text, "Trusted Button") || strings.Contains(contentResult.Text, "page lie") || contentResult.HTML != "" {
		t.Fatalf("content saw page-world patch: %#v", contentResult)
	}
	snapshot, err := session.Snapshot(ctx, snapshotOptions{MaxElements: 10, InteractiveOnly: true, IncludeValues: true})
	if err != nil {
		t.Fatalf("browser_snapshot: %v", err)
	}
	if len(snapshot.Elements) == 0 || snapshot.Elements[0]["name"] != "Trusted Button" {
		t.Fatalf("snapshot saw page-world patch: %#v", snapshot.Elements)
	}
	if ref, _ := snapshot.Elements[0]["ref"].(string); strings.Contains(ref, "page-lie") {
		t.Fatalf("snapshot selector used patched CSS.escape: %#v", snapshot.Elements)
	}
	if _, ok := snapshot.Elements[0]["value"]; ok {
		t.Fatalf("snapshot leaked sensitive value: %#v", snapshot.Elements)
	}
	if _, err := session.Screenshot(ctx, screenshotOptions{Selector: "#real", MaxBytes: 1024}); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("selector screenshot did not use trusted metrics: %v", err)
	}
	if _, err := session.Screenshot(ctx, screenshotOptions{FullPage: true, MaxBytes: 1024}); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("full-page screenshot did not use trusted metrics: %v", err)
	}
	width, height := session.selectorMetrics(ctx, "#real")
	if width < 100 || height < 30 {
		t.Fatalf("selector metrics saw page-world layout patch: width=%d height=%d", width, height)
	}
	pageWidth, pageHeight := session.fullPageMetrics(ctx)
	if pageWidth < 100 || pageHeight < 500 {
		t.Fatalf("full-page metrics saw page-world layout patch: width=%d height=%d", pageWidth, pageHeight)
	}
	viewportWidth, viewportHeight := session.viewport(ctx)
	if viewportWidth < 100 || viewportHeight < 100 {
		t.Fatalf("viewport metrics saw page-world window patch: width=%d height=%d", viewportWidth, viewportHeight)
	}
}

func TestLiveMCPByteCapsBoundBrowserWork(t *testing.T) {
	skipUnlessLiveMCP(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write(bytes.Repeat([]byte("L"), 2*1024*1024))
		case "/stream":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			flusher, _ := w.(http.Flusher)
			chunk := bytes.Repeat([]byte("S"), 4096)
			for i := 0; i < 256; i++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(time.Millisecond)
			}
		case "/huge-dom":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html><title>huge dom</title><main id="huge">`)
			for i := 0; i < 12000; i++ {
				_, _ = fmt.Fprintf(w, `<p data-i="%d">large node %d %s</p>`, i, i, strings.Repeat("x", 64))
			}
			_, _ = fmt.Fprint(w, `</main><script>
Document.prototype.querySelector = () => document.createElement("aside");
Object.defineProperty(Element.prototype, "innerText", {get() { return "page lie"; }});
</script>`)
		case "/tall":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html><style>html,body{margin:0;width:900px;height:180000px}main{height:180000px;background:linear-gradient(#fff,#ddd)}</style><main>very tall page</main>`)
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html><main>byte cap smoke origin</main><script>
window.fetch = async () => new Response("page lie", {status: 418});
</script>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	browser, err := gomoufox.New(ctx,
		gomoufox.WithHeadless(camoufoxcfg.HeadlessTrue),
		gomoufox.WithMainWorldEval(true),
		gomoufox.WithUnsafeDirectNetwork(true),
		gomoufox.WithAutoInstall(false),
	)
	if err != nil {
		t.Fatalf("launch managed Camoufox: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })
	page, err := browser.NewPage(ctx)
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	adaptedPage := adaptPage(page)
	if err := verifyMCPInternalEvaluation(ctx, adaptedPage); err != nil {
		t.Fatalf("internal evaluation startup probe: %v", err)
	}
	session := &gomoufoxSession{page: adaptedPage, refs: a11y.NewStore()}
	if _, err := session.Navigate(ctx, server.URL+"/", navigateOptions{WaitUntil: "domcontentloaded", Timeout: 20 * time.Second}); err != nil {
		t.Fatalf("navigate smoke origin: %v", err)
	}

	const fetchCap = 8192
	largeFetchDone := liveProbe(t, "large fetch cap")
	large, err := session.Fetch(ctx, fetchOptions{URL: server.URL + "/large", Method: "GET", MaxBytes: fetchCap})
	largeFetchDone()
	if err != nil {
		t.Fatalf("large fetch: %v", err)
	}
	if len(large.Body) != fetchCap || !large.Truncated {
		t.Fatalf("large fetch cap = len %d truncated %v", len(large.Body), large.Truncated)
	}
	if strings.Contains(string(large.Body), "page lie") {
		t.Fatalf("large fetch used page-world fetch patch: %q", large.Body)
	}

	streamFetchDone := liveProbe(t, "stream fetch cap")
	streamed, err := session.Fetch(ctx, fetchOptions{URL: server.URL + "/stream", Method: "GET", MaxBytes: fetchCap})
	streamFetchDone()
	if err != nil {
		t.Fatalf("stream fetch: %v", err)
	}
	if len(streamed.Body) != fetchCap || !streamed.Truncated {
		t.Fatalf("stream fetch cap = len %d truncated %v", len(streamed.Body), streamed.Truncated)
	}
	if strings.Contains(string(streamed.Body), "page lie") {
		t.Fatalf("stream fetch used page-world fetch patch: %q", streamed.Body)
	}

	if _, err := session.Navigate(ctx, server.URL+"/huge-dom", navigateOptions{WaitUntil: "domcontentloaded", Timeout: 20 * time.Second}); err != nil {
		t.Fatalf("navigate huge DOM: %v", err)
	}
	const contentCap = 4096
	contentDone := liveProbe(t, "huge DOM content cap")
	contentResult, err := session.PageContent(ctx, pageContentOptions{Selector: "#huge", MaxBytes: contentCap, IncludeHTML: true, IncludeText: true})
	contentDone()
	if err != nil {
		t.Fatalf("huge DOM content: %v", err)
	}
	if !contentResult.Truncated || len([]byte(contentResult.HTML)) > contentCap || len([]byte(contentResult.Text)) > contentCap {
		t.Fatalf("huge DOM cap html=%d text=%d truncated=%v", len([]byte(contentResult.HTML)), len([]byte(contentResult.Text)), contentResult.Truncated)
	}
	if strings.Contains(contentResult.Text, "page lie") || !strings.Contains(contentResult.Text, "large node") {
		t.Fatalf("huge DOM content used page-world DOM patch: %#v", contentResult)
	}

	if _, err := session.Navigate(ctx, server.URL+"/tall", navigateOptions{WaitUntil: "domcontentloaded", Timeout: 20 * time.Second}); err != nil {
		t.Fatalf("navigate tall page: %v", err)
	}
	screenshotDone := liveProbe(t, "tall screenshot preflight")
	_, err = session.Screenshot(ctx, screenshotOptions{FullPage: true, MaxBytes: 1024})
	screenshotDone()
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("tall full-page screenshot preflight err = %v", err)
	}
}

func skipUnlessLiveMCP(t *testing.T) {
	t.Helper()
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 to run managed Camoufox MCP live smoke")
	}
}

func liveProbe(t *testing.T, name string) func() {
	t.Helper()
	start := time.Now()
	startAlloc := liveGoAllocBytes()
	startRSS := currentProcessRSSKiB()
	return func() {
		t.Helper()
		endRSS := currentProcessRSSKiB()
		rssText := "unavailable"
		if startRSS >= 0 && endRSS >= 0 {
			rssText = fmt.Sprintf("%d delta=%+d", endRSS, endRSS-startRSS)
		}
		allocDelta := int64(liveGoAllocBytes()) - int64(startAlloc)
		t.Logf("%s: duration=%s go_alloc_delta=%+d rss_kib=%s", name, time.Since(start).Round(time.Millisecond), allocDelta, rssText)
	}
}

func liveGoAllocBytes() uint64 {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return mem.Alloc
}

func currentProcessRSSKiB() int64 {
	if runtime.GOOS == "windows" {
		return -1
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return -1
	}
	rss, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return -1
	}
	return rss
}
