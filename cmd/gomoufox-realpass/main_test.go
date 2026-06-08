package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	gomoufox "github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/buildinfo"
)

const diagnosticSecretFixture = `proxy=http://user:pass@example.com Authorization: Bearer abc.def Proxy-Authorization: Bearer proxy.def Cookie: sid=secret Set-Cookie: auth=secret wss://127.0.0.1:9222/rawtoken token=secret {"cookies":[{"name":"sid","value":"cookie-secret"}],"origins":[{"origin":"https://example.com","localStorage":[{"name":"token","value":"storage-secret"}]}]}`

func assertNoDiagnosticSecrets(t *testing.T, text string) {
	t.Helper()
	for i, secret := range []string{
		"user:pass",
		"abc.def",
		"proxy.def",
		"sid=secret",
		"auth=secret",
		"/rawtoken",
		"token=secret",
		"cookie-secret",
		"storage-secret",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostic secret fixture %d survived", i)
		}
	}
}

func TestParseTarget(t *testing.T) {
	got, err := parseTarget("cf=https://www.cloudflare.com/path")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "cf" || got.URL != "https://www.cloudflare.com/path" || got.Kind != "custom" {
		t.Fatalf("target = %#v", got)
	}

	got, err = parseTarget("cf|cloudflare-edge=https://www.cloudflare.com/path")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "cf" || got.URL != "https://www.cloudflare.com/path" || got.Kind != "cloudflare-edge" {
		t.Fatalf("target with kind = %#v", got)
	}

	got, err = parseTarget("https://bot.sannysoft.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "bot-sannysoft-com" {
		t.Fatalf("derived target name = %q", got.Name)
	}

	if _, err := parseTarget("not a url"); err == nil {
		t.Fatal("invalid target parsed")
	}
}

func TestClassifySignals(t *testing.T) {
	signals := classifySignals(403, "Just a moment...", "https://example.com", `<script src="/cdn-cgi/challenge-platform/h/g/cf-chl.js"></script>Verify you are human`)
	want := []string{"http_403", "cloudflare_challenge", "human_verification"}
	if !containsAll(signals, want) {
		t.Fatalf("signals = %#v want at least %#v", signals, want)
	}
	if !hasBlockingSignals(signals) {
		t.Fatal("blocking signals not detected")
	}

	signals = classifySignals(200, "Normal Page", "https://example.com", "<main>hello</main>")
	if len(signals) != 0 || hasBlockingSignals(signals) {
		t.Fatalf("normal page signals = %#v", signals)
	}

	signals = classifySignals(200, "Bot Management Platform", "https://example.com", "<main>captcha mitigation, turnstile, and robot detection product copy</main>")
	if !containsAll(signals, []string{"captcha", "cloudflare_turnstile", "robot_detection"}) {
		t.Fatalf("vendor copy signals = %#v", signals)
	}
	if hasBlockingSignals(signals) {
		t.Fatalf("vendor copy should not be classified as blocked: %#v", signals)
	}

	signals = classifySignals(200, "Best Buy | Official Online Store | Shop Now & Save", "https://www.bestbuy.com/", "<script>const labels = ['captcha','forbidden','akamai'];</script><main>normal retail page</main>")
	if !containsAll(signals, []string{"captcha", "akamai"}) || hasBlockingSignals(signals) {
		t.Fatalf("weak retail content should not be classified as blocked: %#v", signals)
	}

	signals = classifySignals(200, "Access Denied", "https://example.com", "<main>request blocked due to unusual traffic</main>")
	if !hasBlockingSignals(signals) {
		t.Fatalf("access denial should be classified as blocked: %#v", signals)
	}

	signals = classifySignals(200, "403 Forbidden", "https://example.com", "<main>403 forbidden</main>")
	if !containsAll(signals, []string{"forbidden"}) || !hasBlockingSignals(signals) {
		t.Fatalf("explicit forbidden page should be classified as blocked: %#v", signals)
	}
}

func TestParsePSRowsAndAggregateProcessTree(t *testing.T) {
	rows := parsePSRows(`
 10  1  0.5  1000 python
 11 10  7.5  2000 camoufox
 12 11  1.0  3000 Web Content
 99  1 42.0 99999 unrelated
bad row
`)
	if len(rows) != 4 {
		t.Fatalf("rows = %#v", rows)
	}
	sample, err := aggregateProcessTree(10, rows)
	if err != nil {
		t.Fatal(err)
	}
	if sample.RSSKiB != 6000 || sample.CPU != 9.0 || sample.Count != 3 {
		t.Fatalf("sample = %#v", sample)
	}
	if !reflect.DeepEqual(sample.Commands, []string{"Web Content", "camoufox", "python"}) {
		t.Fatalf("commands = %#v", sample.Commands)
	}
	if _, err := aggregateProcessTree(404, rows); err == nil {
		t.Fatal("missing root aggregated")
	}
	cyclic, err := aggregateProcessTree(1, []processRow{
		{PID: 1, PPID: 2, CPU: 1, RSS: 10, Cmd: "root"},
		{PID: 2, PPID: 1, CPU: 2, RSS: 20, Cmd: "child"},
	})
	if err != nil || cyclic.Count != 2 || cyclic.RSSKiB != 30 {
		t.Fatalf("cyclic sample = %#v err=%v", cyclic, err)
	}
	rows = parsePSRows("1 0 bad 2 cmd\n2 0 1.0 bad cmd\n3 0 1.0 2\n4 0 1.0 2 valid command\n")
	if len(rows) != 1 || rows[0].Cmd != "valid command" {
		t.Fatalf("invalid row filtering = %#v", rows)
	}
}

func TestSummarize(t *testing.T) {
	summary := summarize([]targetResult{
		{Outcome: "passed", Resources: resourceSummary{PeakRSSMiB: 10, MaxCPUPercent: 1}},
		{Outcome: "blocked", Resources: resourceSummary{PeakRSSMiB: 20, MaxCPUPercent: 3}},
		{Outcome: "failed", Resources: resourceSummary{PeakRSSMiB: 15, MaxCPUPercent: 2}},
	})
	if summary.Total != 3 || summary.Passed != 1 || summary.Blocked != 1 || summary.Failed != 1 || summary.PeakRSSMiB != 20 || summary.PeakCPUPercent != 3 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestMarkdownReportIncludesDiagnosticBranches(t *testing.T) {
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	got := markdownReport(report{
		StartedAt:  started,
		FinishedAt: started.Add(time.Second),
		GoOS:       "linux",
		GoArch:     "amd64",
		Summary: reportSummary{
			Passed:         1,
			Blocked:        1,
			Failed:         1,
			PeakRSSMiB:     128.5,
			PeakCPUPercent: 42.5,
		},
		Results: []targetResult{
			{
				Name:            "diagnostic|target",
				Kind:            "cloudflare-edge",
				URL:             "https://example.com/start",
				FinalURL:        "https://example.com/final",
				Title:           "Title | Needs Escaping",
				Outcome:         "failed",
				Status:          500,
				Signals:         []string{"http_500", "access_denied"},
				Error:           "navigation failed | redacted",
				NavigationError: "context deadline exceeded",
				ScreenshotPath:  "screenshots/diagnostic.png",
				ScreenshotBytes: 1024,
				DurationMS:      250,
				Resources:       resourceSummary{RootPID: 123, Samples: 3, PeakRSSMiB: 128.5, MaxCPUPercent: 42.5, AvgCPUPercent: 21.25, PeakProcesses: 4},
			},
			{
				Name:      "scoped",
				Kind:      "baseline",
				URL:       "https://example.org",
				FinalURL:  "https://example.org",
				Title:     "Scoped",
				Outcome:   "passed",
				Status:    200,
				Resources: resourceSummary{Scope: "custom_scope"},
			},
		},
	})

	for _, want := range []string{
		`# gomoufox Real-Site Pass`,
		`diagnostic\|target`,
		`Title \| Needs Escaping`,
		`- Error: ` + "`navigation failed \\| redacted`",
		`- Navigation warning: ` + "`context deadline exceeded`",
		`- Screenshot: ` + "`screenshots/diagnostic.png` (1024 bytes)",
		`Resources: process_tree`,
		`Resources: custom_scope`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown report missing %q:\n%s", want, got)
		}
	}
}

func TestCompactMarkdownReportKeepsSummaryWithoutDetails(t *testing.T) {
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	rep := report{
		StartedAt:  started,
		FinishedAt: started.Add(time.Second),
		GoOS:       "linux",
		GoArch:     "amd64",
		Summary: reportSummary{
			Passed:         1,
			Blocked:        0,
			Failed:         0,
			PeakRSSMiB:     128.5,
			PeakCPUPercent: 42.5,
		},
		Results: []targetResult{{
			Name:       "target|with|pipes",
			Kind:       "reference-site",
			URL:        "https://example.com/start",
			FinalURL:   "https://example.com/final",
			Title:      "Verbose title that belongs only in full reports",
			Outcome:    "passed",
			Status:     200,
			Signals:    []string{"ok|signal"},
			DurationMS: 250,
			Resources:  resourceSummary{PeakRSSMiB: 128.5, MaxCPUPercent: 42.5},
		}},
	}
	got := markdownReportWithStyle(rep, reportStyleCompact)
	for _, want := range []string{
		`# gomoufox Real-Site Pass`,
		`target\|with\|pipes`,
		`ok\|signal`,
		`| Target | Kind | Outcome | Status | Signals | Peak RSS MiB | Max CPU % | Duration ms |`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compact markdown missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{
		"## Details",
		"Verbose title that belongs only in full reports",
		"https://example.com/final",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("compact markdown kept detail %q:\n%s", forbidden, got)
		}
	}
	if full := markdownReportWithStyle(rep, reportStyleFull); !strings.Contains(full, "## Details") || len(full) <= len(got) {
		t.Fatalf("full markdown did not include detail or was not larger\nfull=%s\ncompact=%s", full, got)
	}
}

func TestUnsafeDirectNetworkFlagIsExplicit(t *testing.T) {
	code, _, stderr := runRealpassForTest(t, "--direct-network", "--list-targets")
	if code != 2 || !strings.Contains(stderr, "flag provided but not defined: -direct-network") {
		t.Fatalf("legacy flag code=%d stderr=%q", code, stderr)
	}

	code, stdout, stderr := runRealpassForTest(t, "--unsafe-direct-network", "--list-targets")
	if code != 0 {
		t.Fatalf("unsafe flag discovery code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"name":"cloudflare-home"`) || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRunValidationAndCompareErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--repeat", "0"}, "repeat must be >= 1"},
		{[]string{"--sample-interval", "0s"}, "sample-interval must be > 0"},
		{[]string{"--load-state-timeout=-1s"}, "load-state-timeout must be >= 0"},
		{[]string{"--content-max-bytes=-1"}, "content-max-bytes must be >= 0"},
		{[]string{"--expect-passed", "-1"}, "expect-passed must be >= 0"},
		{[]string{"--max-blocked", "-2"}, "max-blocked must be >= -1"},
		{[]string{"--max-failed", "-2"}, "max-failed must be >= -1"},
		{[]string{"--max-rss-mib", "-1"}, "max-rss-mib must be >= 0"},
		{[]string{"--max-cpu-percent", "-1"}, "max-cpu-percent must be >= 0"},
		{[]string{"--wait-until", "idle"}, "wait-until must be commit, domcontentloaded, load, or networkidle"},
		{[]string{"--sidecar-runtime", "bad"}, "sidecar-runtime must be python or node-direct"},
		{[]string{"compare"}, "--go and --python are required"},
		{[]string{"compare", "--go", "missing", "--python", "missing"}, "read go report"},
	} {
		code, _, stderr := runRealpassForTest(t, tc.args...)
		if code == 0 || !strings.Contains(stderr, tc.want) {
			t.Fatalf("%v code=%d stderr=%q want %q", tc.args, code, stderr, tc.want)
		}
	}
	code, _, stderr := runRealpassForTest(t, "--target", "bad=http://user:pass@?token=secret")
	if code != 2 {
		t.Fatalf("invalid secret target code=%d stderr_len=%d", code, len(stderr))
	}
	assertNoDiagnosticSecrets(t, stderr)

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runRealpassForTest(t, "--out", filepath.Join(blocker, "child"), "--list-targets=false")
	if code == 0 || !strings.Contains(stderr, "create output dir") {
		t.Fatalf("mkdir code=%d stderr=%q", code, stderr)
	}

	outDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outDir, "screenshots"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runRealpassForTest(t, "--out", outDir, "--target", "x=https://example.com")
	if code == 0 || !strings.Contains(stderr, "create screenshot dir") {
		t.Fatalf("screenshot dir code=%d stderr=%q", code, stderr)
	}
}

func TestHelpExitsZero(t *testing.T) {
	code, stdout, stderr := runRealpassForTest(t, "--help")
	if code != 0 || stdout != "" || !strings.Contains(stderr, "Usage of gomoufox-realpass") || !strings.Contains(stderr, "max-rss-mib") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestLaunchBrowserOptionsAndFailure(t *testing.T) {
	old := newRealpassBrowser
	t.Cleanup(func() { newRealpassBrowser = old })
	called := false
	newRealpassBrowser = func(context.Context, ...gomoufox.Option) (realpassBrowser, error) {
		called = true
		return &fakeRealpassBrowser{pid: os.Getpid(), page: &fakeRealpassPage{}}, nil
	}
	if _, err := launchBrowser(context.Background(), false, false, false, "node-direct", t.TempDir()); err != nil || !called {
		t.Fatalf("launch err=%v called=%v", err, called)
	}
	if _, err := launchBrowser(context.Background(), false, false, false, "bad", ""); err == nil {
		t.Fatal("bad sidecar runtime succeeded")
	}
	newRealpassBrowser = func(context.Context, ...gomoufox.Option) (realpassBrowser, error) {
		return nil, errors.New("boom")
	}
	if _, err := launchBrowser(context.Background(), true, true, true, "python", ""); err == nil {
		t.Fatal("launch error branch succeeded")
	}
}

func TestRealpassAdaptersAndMainExit(t *testing.T) {
	browserClosed := false
	pageClosed := false
	page := realpassPageFunc{
		gotoFn: func(context.Context, string, ...gomoufox.GotoOption) (realpassResponse, error) {
			return realpassResponseFunc{
				status:     func() int { return 201 },
				statusText: func() string { return "Created" },
				headers:    func() map[string]string { return map[string]string{"server": "test"} },
			}, nil
		},
		waitLoadStateFn: func(context.Context, string) error { return nil },
		urlFn:           func() string { return "https://example.com/final" },
		titleFn:         func(context.Context) (string, error) { return "title", nil },
		contentFn:       func(context.Context) (string, error) { return "<main>ok</main>", nil },
		evaluateFn:      func(context.Context, string, ...any) (any, error) { return map[string]any{"ok": true}, nil },
		screenshotFn: func(context.Context, string, ...gomoufox.ScreenshotOption) error {
			return nil
		},
		closeFn: func() error { pageClosed = true; return nil },
	}
	browser := realpassBrowserFunc{
		newPage: func(context.Context, ...gomoufox.ContextOption) (realpassPage, error) { return page, nil },
		sidecar: func() gomoufox.SidecarInfo {
			return gomoufox.SidecarInfo{PID: os.Getpid()}
		},
		close: func() error { browserClosed = true; return nil },
	}
	resp, err := page.Goto(context.Background(), "https://example.com")
	if err != nil || resp.Status() != 201 || resp.StatusText() != "Created" || resp.Headers()["server"] != "test" {
		t.Fatalf("response=%#v err=%v", resp, err)
	}
	if err := page.WaitForLoadState(context.Background(), "load"); err != nil {
		t.Fatal(err)
	}
	if page.URL() != "https://example.com/final" {
		t.Fatalf("url = %q", page.URL())
	}
	if title, err := page.Title(context.Background()); err != nil || title != "title" {
		t.Fatalf("title=%q err=%v", title, err)
	}
	if html, err := page.Content(context.Background()); err != nil || !strings.Contains(html, "ok") {
		t.Fatalf("content=%q err=%v", html, err)
	}
	if value, err := page.Evaluate(context.Background(), "() => true"); err != nil || value.(map[string]any)["ok"] != true {
		t.Fatalf("evaluate=%#v err=%v", value, err)
	}
	if err := page.ScreenshotToFile(context.Background(), filepath.Join(t.TempDir(), "shot.png")); err != nil {
		t.Fatal(err)
	}
	if err := page.Close(); err != nil || !pageClosed {
		t.Fatalf("page close err=%v closed=%v", err, pageClosed)
	}
	if got, err := browser.NewPage(context.Background()); err != nil || got == nil || browser.Sidecar().PID == 0 {
		t.Fatalf("browser page=%#v err=%v sidecar=%#v", got, err, browser.Sidecar())
	}
	if err := browser.Close(); err != nil || !browserClosed {
		t.Fatalf("browser close err=%v closed=%v", err, browserClosed)
	}

	oldNew := gomoufoxNewForRealpass
	t.Cleanup(func() { gomoufoxNewForRealpass = oldNew })
	gomoufoxNewForRealpass = func(context.Context, ...gomoufox.Option) (*gomoufox.Browser, error) {
		return &gomoufox.Browser{}, nil
	}
	if got, err := newRealpassBrowser(context.Background()); err != nil || got == nil {
		t.Fatalf("newRealpassBrowser success = %#v err=%v", got, err)
	}
	gomoufoxNewForRealpass = func(context.Context, ...gomoufox.Option) (*gomoufox.Browser, error) {
		return nil, errors.New("new failed")
	}
	if _, err := newRealpassBrowser(context.Background()); err == nil {
		t.Fatal("newRealpassBrowser error branch succeeded")
	}
	liveBrowser := newLiveRealpassBrowser(
		func(context.Context, ...gomoufox.ContextOption) (*gomoufox.Page, error) { return &gomoufox.Page{}, nil },
		func() gomoufox.SidecarInfo { return gomoufox.SidecarInfo{PID: 123} },
		func() error { return nil },
	)
	if got, err := liveBrowser.NewPage(context.Background()); err != nil || got == nil || liveBrowser.Sidecar().PID != 123 {
		t.Fatalf("live browser page=%#v err=%v sidecar=%#v", got, err, liveBrowser.Sidecar())
	}
	if err := liveBrowser.Close(); err != nil {
		t.Fatal(err)
	}
	liveErr := errors.New("page failed")
	liveBrowser = newLiveRealpassBrowser(
		func(context.Context, ...gomoufox.ContextOption) (*gomoufox.Page, error) { return nil, liveErr },
		func() gomoufox.SidecarInfo { return gomoufox.SidecarInfo{} },
		func() error { return nil },
	)
	if _, err := liveBrowser.NewPage(context.Background()); !errors.Is(err, liveErr) {
		t.Fatalf("live browser err = %v", err)
	}
	livePage := newLiveRealpassPage(
		func(context.Context, string, ...gomoufox.GotoOption) (*gomoufox.Response, error) {
			return &gomoufox.Response{}, nil
		},
		func(context.Context, string) error { return nil },
		func() string { return "https://example.com/live" },
		func(context.Context) (string, error) { return "live", nil },
		func(context.Context) (string, error) { return "<main>live</main>", nil },
		func(context.Context, string, ...any) (any, error) { return map[string]any{"live": true}, nil },
		func(context.Context, string, ...gomoufox.ScreenshotOption) error { return nil },
		func() error { return nil },
	)
	if got, err := livePage.Goto(context.Background(), "https://example.com"); err != nil || got == nil {
		t.Fatalf("live page goto=%#v err=%v", got, err)
	}
	livePage = newLiveRealpassPage(
		func(context.Context, string, ...gomoufox.GotoOption) (*gomoufox.Response, error) { return nil, nil },
		func(context.Context, string) error { return nil },
		func() string { return "" },
		func(context.Context) (string, error) { return "", nil },
		func(context.Context) (string, error) { return "", nil },
		func(context.Context, string, ...any) (any, error) { return nil, nil },
		func(context.Context, string, ...gomoufox.ScreenshotOption) error { return nil },
		func() error { return nil },
	)
	if got, err := livePage.Goto(context.Background(), "https://example.com"); err != nil || got != nil {
		t.Fatalf("live nil goto=%#v err=%v", got, err)
	}
	livePage = newLiveRealpassPage(
		func(context.Context, string, ...gomoufox.GotoOption) (*gomoufox.Response, error) { return nil, liveErr },
		func(context.Context, string) error { return nil },
		func() string { return "" },
		func(context.Context) (string, error) { return "", nil },
		func(context.Context) (string, error) { return "", nil },
		func(context.Context, string, ...any) (any, error) { return nil, nil },
		func(context.Context, string, ...gomoufox.ScreenshotOption) error { return nil },
		func() error { return nil },
	)
	if _, err := livePage.Goto(context.Background(), "https://example.com"); !errors.Is(err, liveErr) {
		t.Fatalf("live goto err = %v", err)
	}
	liveResp := newLiveRealpassResponse(
		func() int { return 204 },
		func() string { return "No Content" },
		func() map[string]string { return map[string]string{"server": "live"} },
	)
	if liveResp.Status() != 204 || liveResp.StatusText() != "No Content" || liveResp.Headers()["server"] != "live" {
		t.Fatalf("live response = %#v", liveResp)
	}

	oldExit := exitProcess
	oldArgs := os.Args
	t.Cleanup(func() {
		exitProcess = oldExit
		os.Args = oldArgs
	})
	var exitCode int
	exitProcess = func(code int) { exitCode = code; panic("exit") }
	os.Args = []string{"gomoufox-realpass", "--repeat", "0"}
	func() {
		defer func() {
			if recovered := recover(); recovered != "exit" {
				t.Fatalf("recover = %#v", recovered)
			}
		}()
		main()
	}()
	if exitCode != 2 {
		t.Fatalf("exit code = %d", exitCode)
	}
}

func TestRunHelpExitsZero(t *testing.T) {
	code, stdout, stderr := runRealpassForTest(t, "--help")
	if code != 0 {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "Usage of gomoufox-realpass") {
		t.Fatalf("help stderr = %q", stderr)
	}
}

func TestRunVersionExitsZero(t *testing.T) {
	oldVersion := buildinfo.Version
	buildinfo.Version = "v0.1.2-test"
	t.Cleanup(func() { buildinfo.Version = oldVersion })

	code, stdout, stderr := runRealpassForTest(t, "--version")
	if code != 0 || stdout != "gomoufox-realpass v0.1.2-test\n" || stderr != "" {
		t.Fatalf("version code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestEvaluateReportGateThresholds(t *testing.T) {
	rep := report{
		Summary: reportSummary{Total: 3, Passed: 1, Blocked: 1, Failed: 1, PeakRSSMiB: 900, PeakCPUPercent: 450},
	}
	failures := evaluateReportGate(rep, gateOptions{
		ExpectPassed:  2,
		MaxFailed:     intPtr(0),
		MaxBlocked:    intPtr(0),
		MaxRSSMiB:     floatPtr(750),
		MaxCPUPercent: floatPtr(400),
	})
	for _, want := range []string{"passed 1 < expected 2", "failed 1 > max 0", "blocked 1 > max 0", "peak RSS 900.0 MiB > max 750.0 MiB", "peak CPU 450.0% > max 400.0%"} {
		if !containsSubstring(failures, want) {
			t.Fatalf("failures = %#v, missing %q", failures, want)
		}
	}
	if got := evaluateReportGate(rep, gateOptions{ExpectPassed: 1, MaxFailed: intPtr(1), MaxBlocked: intPtr(1), MaxRSSMiB: floatPtr(900), MaxCPUPercent: floatPtr(450)}); len(got) != 0 {
		t.Fatalf("unexpected gate failures = %#v", got)
	}
}

func TestRunCompareReportsFailsOnOutcomeDriftAndThresholds(t *testing.T) {
	dir := t.TempDir()
	goPath := filepath.Join(dir, "go.json")
	pythonPath := filepath.Join(dir, "python.json")
	writeReportForTest(t, goPath, report{
		Summary: reportSummary{Total: 2, Passed: 1, Blocked: 1, PeakRSSMiB: 800, PeakCPUPercent: 250},
		Results: []targetResult{
			{Name: "a", Outcome: "passed"},
			{Name: "b", Outcome: "blocked"},
		},
	})
	writeReportForTest(t, pythonPath, report{
		Summary: reportSummary{Total: 2, Passed: 2, PeakRSSMiB: 700, PeakCPUPercent: 240},
		Results: []targetResult{
			{Name: "a", Outcome: "passed"},
			{Name: "b", Outcome: "passed"},
		},
	})

	code, stdout, stderr := runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--require-outcome-match")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "outcome mismatch b: go=blocked python=passed") {
		t.Fatalf("drift code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--require-target", "a", "--require-target", "b", "--require-target", "c")
	if code != 1 || !strings.Contains(stderr, "go: required target c is missing") || !strings.Contains(stderr, "python: required target c is missing") {
		t.Fatalf("missing required target code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--require-target", "a")
	if code != 1 || !strings.Contains(stderr, "go: unexpected target b is not in --require-target") || !strings.Contains(stderr, "python: unexpected target b is not in --require-target") {
		t.Fatalf("unexpected target code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--require-outcome-match", "--require-target", "a", "--require-target", "b", "--allow-blocked-target", "b")
	if code != 0 || !strings.Contains(stdout, "compare: ok") || stderr != "" {
		t.Fatalf("complete required target set code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--max-rss-mib", "750")
	if code != 1 || !strings.Contains(stderr, "go: peak RSS 800.0 MiB > max 750.0 MiB") {
		t.Fatalf("rss code=%d stderr=%q", code, stderr)
	}
	writeReportForTest(t, pythonPath, report{Summary: reportSummary{Total: 1, Passed: 1, PeakRSSMiB: 950}})
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--max-rss-mib", "900")
	if code != 1 || !strings.Contains(stderr, "python: peak RSS 950.0 MiB > max 900.0 MiB") {
		t.Fatalf("python rss code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--require-outcome-match", "--max-rss-mib", "900")
	if code != 0 || !strings.Contains(stdout, "compare: ok") || stderr != "" {
		t.Fatalf("ok code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runRealpassForTest(t, "compare", "--go", goPath, "--python", goPath, "--require-outcome-match")
	if code != 0 || !strings.Contains(stdout, "compare: ok") || stderr != "" {
		t.Fatalf("dispatch code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	writeReportForTest(t, pythonPath, report{Results: []targetResult{{Name: "c", Outcome: "passed"}}})
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--require-outcome-match")
	if code != 1 || !strings.Contains(stderr, "outcome mismatch b: go=blocked python=<missing>") || !strings.Contains(stderr, "outcome mismatch c: go=<missing> python=passed") {
		t.Fatalf("missing outcome code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", "{bad")
	if code != 1 || !strings.Contains(stderr, "read python report") {
		t.Fatalf("bad python path code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--max-rss-mib", "-1")
	if code != 2 || !strings.Contains(stderr, "max-rss-mib") {
		t.Fatalf("bad threshold code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--require-outcome-match", "--allow-blocked-target", "expected")
	if code != 1 || !strings.Contains(stderr, "go: blocked target b is not in --allow-blocked-target") || !strings.Contains(stderr, "python: blocked target b is not in --allow-blocked-target") {
		t.Fatalf("unexpected blocked target code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--require-outcome-match", "--allow-blocked-target", "b")
	if code != 0 || !strings.Contains(stdout, "compare: ok") || stderr != "" {
		t.Fatalf("allowed blocked target code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	started := time.Unix(100, 0)
	writeReportForTest(t, goPath, report{
		StartedAt:  started,
		FinishedAt: started.Add(7 * time.Second),
		Summary:    reportSummary{Total: 1, Blocked: 1, PeakRSSMiB: 1900, PeakCPUPercent: 300},
		Results: []targetResult{
			{Name: "etsy", Outcome: "blocked", DurationMS: 6000},
		},
	})
	writeReportForTest(t, pythonPath, report{
		StartedAt:  started,
		FinishedAt: started.Add(4 * time.Second),
		Summary:    reportSummary{Total: 1, Blocked: 1, PeakRSSMiB: 1100, PeakCPUPercent: 250},
		Results: []targetResult{
			{Name: "etsy", Outcome: "blocked", DurationMS: 3000},
		},
	})
	code, stdout, stderr = runCompareForTest(t,
		"--go", goPath,
		"--python", pythonPath,
		"--require-outcome-match",
		"--require-target", "etsy",
		"--expect-passed", "0",
		"--max-blocked", "-1",
		"--max-failed", "-1",
		"--max-rss-mib", "6000",
		"--max-cpu-percent", "900",
		"--max-go-rss-ratio", "0",
		"--max-go-cpu-ratio", "0",
		"--max-go-wall-ratio", "0",
		"--max-go-target-duration-ratio", "0",
		"--max-go-report-token-ratio", "0",
	)
	if code != 0 || !strings.Contains(stdout, "compare: ok") || stderr != "" {
		t.Fatalf("retry parity compare code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	writeReportForTest(t, goPath, report{
		Summary: reportSummary{Total: 2, Passed: 1, Failed: 1},
		Results: []targetResult{
			{Name: "a", Outcome: "passed", DurationMS: 100},
			{Name: "shared", Outcome: "failed", DurationMS: 100},
		},
	})
	writeReportForTest(t, pythonPath, report{
		Summary: reportSummary{Total: 2, Passed: 1, Failed: 1},
		Results: []targetResult{
			{Name: "a", Outcome: "passed", DurationMS: 100},
			{Name: "shared", Outcome: "failed", DurationMS: 100},
		},
	})
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--require-outcome-match", "--expect-passed", "1", "--max-failed", "0")
	if code != 1 || !strings.Contains(stderr, "go: failed 1 > max 0") || !strings.Contains(stderr, "python: failed 1 > max 0") {
		t.Fatalf("unallowed shared failure code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--require-outcome-match", "--expect-passed", "1", "--max-failed", "0", "--allow-failed-target", "shared")
	if code != 0 || !strings.Contains(stdout, "compare: ok") || stderr != "" {
		t.Fatalf("allowed shared failure code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	writeReportForTest(t, pythonPath, report{
		Summary: reportSummary{Total: 2, Passed: 2},
		Results: []targetResult{
			{Name: "a", Outcome: "passed", DurationMS: 100},
			{Name: "shared", Outcome: "passed", DurationMS: 100},
		},
	})
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--require-outcome-match", "--allow-failed-target", "shared")
	if code != 1 || !strings.Contains(stderr, "outcome mismatch shared: go=failed python=passed") {
		t.Fatalf("go-only allowed failure code=%d stderr=%q", code, stderr)
	}
	writeReportForTest(t, goPath, report{
		Summary: reportSummary{Total: 2, Passed: 1, Blocked: 1, PeakRSSMiB: 800, PeakCPUPercent: 250},
		Results: []targetResult{
			{Name: "a", Outcome: "passed"},
			{Name: "b", Outcome: "blocked"},
		},
	})
	writeReportForTest(t, pythonPath, report{
		Summary: reportSummary{Total: 2, Passed: 2, PeakRSSMiB: 700, PeakCPUPercent: 240},
		Results: []targetResult{
			{Name: "a", Outcome: "passed"},
			{Name: "b", Outcome: "passed"},
		},
	})
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--max-go-rss-ratio", "1.10")
	if code != 1 || !strings.Contains(stderr, "go: peak RSS 800.0 MiB > python 700.0 MiB * 1.10") {
		t.Fatalf("rss ratio code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--max-go-cpu-ratio", "1.01")
	if code != 1 || !strings.Contains(stderr, "go: peak CPU 250.0% > python 240.0% * 1.01") {
		t.Fatalf("cpu ratio code=%d stderr=%q", code, stderr)
	}
	started = time.Unix(100, 0)
	writeReportForTest(t, goPath, report{
		StartedAt:  started,
		FinishedAt: started.Add(4 * time.Second),
		Results:    []targetResult{{Name: "a", Outcome: "passed", DurationMS: 3000}},
	})
	writeReportForTest(t, pythonPath, report{
		StartedAt:  started,
		FinishedAt: started.Add(2 * time.Second),
		Results:    []targetResult{{Name: "a", Outcome: "passed", DurationMS: 1000}},
	})
	if err := os.WriteFile(strings.TrimSuffix(goPath, filepath.Ext(goPath))+".md", []byte(strings.Repeat("g", 80)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(pythonPath, filepath.Ext(pythonPath))+".md", []byte("p"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--max-go-wall-ratio", "1.50")
	if code != 1 || !strings.Contains(stderr, "go: wall time 4000 ms > python 2000 ms * 1.50") {
		t.Fatalf("wall ratio code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--max-go-target-duration-ratio", "2.00")
	if code != 1 || !strings.Contains(stderr, "go: target duration 3000 ms > python 1000 ms * 2.00") {
		t.Fatalf("duration ratio code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath, "--max-go-report-token-ratio", "1.01")
	if code != 1 || !strings.Contains(stderr, "go: report tokens") {
		t.Fatalf("token ratio code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--max-go-wall-ratio", "-0.5")
	if code != 2 || !strings.Contains(stderr, "max-go-wall-ratio must be > 0 or 0 to disable") {
		t.Fatalf("bad wall ratio code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--max-go-target-duration-ratio", "-0.5")
	if code != 2 || !strings.Contains(stderr, "max-go-target-duration-ratio must be > 0 or 0 to disable") {
		t.Fatalf("bad target duration ratio code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--max-go-report-token-ratio", "-0.5")
	if code != 2 || !strings.Contains(stderr, "max-go-report-token-ratio must be > 0 or 0 to disable") {
		t.Fatalf("bad report token ratio code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--max-go-rss-ratio", "-0.5")
	if code != 2 || !strings.Contains(stderr, "max-go-rss-ratio must be > 0 or 0 to disable") {
		t.Fatalf("bad rss ratio code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--max-go-cpu-ratio", "-0.5")
	if code != 2 || !strings.Contains(stderr, "max-go-cpu-ratio must be > 0 or 0 to disable") {
		t.Fatalf("bad cpu ratio code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", goPath, "--allow-blocked-target", "bad name")
	if code != 2 || !strings.Contains(stderr, "target name must use report target names") {
		t.Fatalf("bad allowed target code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--bad")
	if code != 2 || !strings.Contains(stderr, "flag provided but not defined") {
		t.Fatalf("bad flag code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runCompareForTest(t, "--help")
	if code != 0 || !strings.Contains(stderr, "Usage of gomoufox-realpass compare") {
		t.Fatalf("help code=%d stderr=%q", code, stderr)
	}
}

func TestResourceRatioFailuresHandlesComputedSummariesAndMissingBaselines(t *testing.T) {
	failures := resourceRatioFailures(
		report{Results: []targetResult{
			{Outcome: "passed", Resources: resourceSummary{PeakRSSMiB: 200, MaxCPUPercent: 80}},
		}},
		report{Results: []targetResult{
			{Outcome: "passed", Resources: resourceSummary{PeakRSSMiB: 0, MaxCPUPercent: 0}},
		}},
		1.2,
		1.5,
	)
	if !containsSubstring(failures, "go: peak RSS 200.0 MiB cannot be compared because python peak RSS is 0.0 MiB") {
		t.Fatalf("missing RSS baseline failure: %#v", failures)
	}
	if !containsSubstring(failures, "go: peak CPU 80.0% cannot be compared because python peak CPU is 0.0%") {
		t.Fatalf("missing CPU baseline failure: %#v", failures)
	}

	failures = resourceRatioFailures(
		report{Results: []targetResult{
			{Outcome: "passed", Resources: resourceSummary{PeakRSSMiB: 100, MaxCPUPercent: 40}},
		}},
		report{Results: []targetResult{
			{Outcome: "passed", Resources: resourceSummary{PeakRSSMiB: 100, MaxCPUPercent: 40}},
		}},
		1.2,
		1.5,
	)
	if len(failures) != 0 {
		t.Fatalf("unexpected resource ratio failures: %#v", failures)
	}
}

func TestRuntimeRatioFailuresHandlesFallbackAndMissingBaselines(t *testing.T) {
	if got := failedTargetFailures("go", []targetResult{{Name: "bad", Outcome: "failed"}}, map[string]bool{"other": true}); !containsSubstring(got, "go: failed target bad is not in --allow-failed-target") {
		t.Fatalf("missing failed target allowlist failure: %#v", got)
	}
	failures := runtimeRatioFailures(
		report{Results: []targetResult{{Outcome: "passed", DurationMS: 200}}},
		report{},
		"",
		"",
		1.2,
		1.2,
		1.2,
	)
	if !containsSubstring(failures, "go: wall time 200 ms cannot be compared because python wall time is 0 ms") ||
		!containsSubstring(failures, "go: target duration 200 ms cannot be compared because python target duration is 0 ms") {
		t.Fatalf("missing runtime baseline failures: %#v", failures)
	}
	if got := appendRatioFailure(nil, "wall time", "ms", 0, 0, 1.2); len(got) != 0 {
		t.Fatalf("zero baseline with zero go value failed: %#v", got)
	}
	if got := appendRatioFailure(nil, "wall time", "ms", 100, 100, 1.2); len(got) != 0 {
		t.Fatalf("passing ratio failed: %#v", got)
	}
	if got := estimateTokens(0); got != 0 {
		t.Fatalf("zero token estimate = %d", got)
	}
	if got := reportArtifactTokens(""); got != 0 {
		t.Fatalf("empty artifact tokens = %d", got)
	}
}

func TestCompareRejectsRuntimeOptionDriftAndAllowsSubUnityBudgets(t *testing.T) {
	dir := t.TempDir()
	goPath := filepath.Join(dir, "go.json")
	pythonPath := filepath.Join(dir, "python.json")
	started := time.Unix(100, 0)
	baseOptions := reportOptions{
		Timeout:          "1s",
		WaitUntil:        "commit",
		Settle:           "0s",
		SampleInterval:   "1h",
		Headful:          false,
		Screenshots:      false,
		ReuseBrowser:     true,
		UnsafeDirect:     true,
		GeneratedPersona: true,
	}
	writeReportForTest(t, goPath, report{
		StartedAt:  started,
		FinishedAt: started.Add(900 * time.Millisecond),
		Options:    baseOptions,
		Results:    []targetResult{{Name: "a", Outcome: "passed", DurationMS: 900}},
	})
	writeReportForTest(t, pythonPath, report{
		StartedAt:  started,
		FinishedAt: started.Add(time.Second),
		Options:    baseOptions,
		Results:    []targetResult{{Name: "a", Outcome: "passed", DurationMS: 1000}},
	})
	if err := os.WriteFile(strings.TrimSuffix(goPath, filepath.Ext(goPath))+".md", []byte("g"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(pythonPath, filepath.Ext(pythonPath))+".md", []byte(strings.Repeat("p", 80)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCompareForTest(t,
		"--go", goPath,
		"--python", pythonPath,
		"--max-go-wall-ratio", "0.95",
		"--max-go-target-duration-ratio", "0.95",
		"--max-go-report-token-ratio", "0.95",
	)
	if code != 0 || !strings.Contains(stdout, "compare: ok") || stderr != "" {
		t.Fatalf("sub-unity winning compare code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	drifted := baseOptions
	drifted.ReuseBrowser = false
	writeReportForTest(t, pythonPath, report{
		StartedAt:  started,
		FinishedAt: started.Add(time.Second),
		Options:    drifted,
		Results:    []targetResult{{Name: "a", Outcome: "passed", DurationMS: 1000}},
	})
	code, _, stderr = runCompareForTest(t, "--go", goPath, "--python", pythonPath)
	if code != 1 || !strings.Contains(stderr, "runtime option mismatch reuse_browser: go=true python=false") {
		t.Fatalf("option drift compare code=%d stderr=%q", code, stderr)
	}
}

func TestReportOptionDiffsCoverBenchmarkOptions(t *testing.T) {
	failures := reportOptionDiffs(reportOptions{
		Timeout:          "1s",
		WaitUntil:        "commit",
		Settle:           "1s",
		LoadStateTimeout: "0s",
		ContentMaxBytes:  250000,
		SampleInterval:   "500ms",
		ReuseBrowser:     true,
		UnsafeDirect:     true,
		GeneratedPersona: true,
	}, reportOptions{
		Timeout:          "2s",
		WaitUntil:        "load",
		Settle:           "bad",
		LoadStateTimeout: "1s",
		ContentMaxBytes:  0,
		SampleInterval:   "bad",
		Headful:          true,
		Screenshots:      true,
	})
	for _, want := range []string{
		"runtime option mismatch timeout: go=1s python=2s",
		"runtime option mismatch wait_until: go=commit python=load",
		"runtime option mismatch settle: go=1s python=bad",
		"runtime option mismatch load_state_timeout: go=0s python=1s",
		"runtime option mismatch content_max_bytes: go=250000 python=0",
		"runtime option mismatch sample_interval: go=500ms python=bad",
		"runtime option mismatch headful: go=false python=true",
		"runtime option mismatch screenshots: go=false python=true",
		"runtime option mismatch reuse_browser: go=true python=false",
		"runtime option mismatch unsafe_direct_network: go=true python=false",
		"runtime option mismatch generated_persona: go=true python=false",
	} {
		if !containsSubstring(failures, want) {
			t.Fatalf("failures missing %q: %#v", want, failures)
		}
	}
	if failures := reportOptionDiffs(reportOptions{}, reportOptions{Timeout: "1s"}); !containsSubstring(failures, "go=<unset> python=1s") {
		t.Fatalf("unset option not reported: %#v", failures)
	}
	if emptyOption("") != "<unset>" || emptyOption("set") != "set" {
		t.Fatalf("emptyOption mismatch")
	}
}

func TestReportOptionsUseUnsafeDirectNetworkField(t *testing.T) {
	data, err := json.Marshal(reportOptions{UnsafeDirect: true})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"unsafe_direct_network":true`) || strings.Contains(text, `"direct_network":`) {
		t.Fatalf("report options json = %s", text)
	}
}

func TestGateOptionsFromRawValidation(t *testing.T) {
	for _, args := range []struct {
		expectPassed int
		maxBlocked   int
		maxFailed    int
		maxRSSMiB    float64
		maxCPU       float64
	}{
		{expectPassed: -1, maxBlocked: -1, maxFailed: -1},
		{maxBlocked: -2, maxFailed: -1},
		{maxBlocked: -1, maxFailed: -2},
		{maxBlocked: -1, maxFailed: -1, maxRSSMiB: -1},
		{maxBlocked: -1, maxFailed: -1, maxCPU: -1},
	} {
		if _, err := gateOptionsFromRaw(args.expectPassed, args.maxBlocked, args.maxFailed, args.maxRSSMiB, args.maxCPU); err == nil {
			t.Fatalf("gateOptionsFromRaw(%#v) succeeded", args)
		}
	}
	opts, err := gateOptionsFromRaw(2, 0, 0, 750, 400)
	if err != nil {
		t.Fatal(err)
	}
	if opts.ExpectPassed != 2 || opts.MaxBlocked == nil || opts.MaxFailed == nil || opts.MaxRSSMiB == nil || opts.MaxCPUPercent == nil {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestRunTargetWithBrowserContextClassifiesAndWritesScreenshot(t *testing.T) {
	dir := t.TempDir()
	page := &fakeRealpassPage{
		url:        "https://example.com/final",
		title:      "Just a moment...",
		content:    `<script src="/cdn-cgi/challenge-platform/x.js"></script>verify you are human`,
		evaluate:   map[string]any{"webdriver": false},
		screenshot: []byte("png"),
		response: fakeRealpassResponse{
			status:     200,
			statusText: "OK",
			headers: map[string]string{
				"CF-Ray":       "ray",
				"Content-Type": "text/html",
				"Ignored":      "x",
			},
		},
	}
	browser := &fakeRealpassBrowser{pid: os.Getpid(), page: page}
	res := runTargetWithBrowserContext(context.Background(), browser, targetResult{
		Name:      "cloudflare",
		URL:       "https://example.com",
		Kind:      "test",
		Attempt:   2,
		StartedAt: time.Now(),
	}, time.Now(), time.Second, "commit", 0, 0, 0, time.Hour, false, true, dir)
	if res.Outcome != "blocked" || res.Status != 200 || res.Headers["CF-Ray"] != "ray" || res.Headers["Ignored"] != "" {
		t.Fatalf("result = %#v", res)
	}
	if !containsAll(res.Signals, []string{"cloudflare_challenge", "human_verification"}) {
		t.Fatalf("signals = %#v", res.Signals)
	}
	if res.Detector["webdriver"] != false || res.ScreenshotBytes != 3 || page.closed != true {
		t.Fatalf("detector/screenshot/page = %#v %#v", res, page)
	}
	if _, err := os.Stat(filepath.Join(dir, "02-cloudflare.png")); err != nil {
		t.Fatal(err)
	}
}

func TestRunTargetWithBrowserContextFailureBranches(t *testing.T) {
	start := time.Now()
	newPageErr := errors.New("new page failed")
	res := runTargetWithBrowserContext(context.Background(), &fakeRealpassBrowser{pid: os.Getpid(), newPageErr: newPageErr}, targetResult{
		Name: "x", URL: "https://example.com", StartedAt: start,
	}, start, time.Second, "commit", 0, 0, 0, time.Hour, true, false, t.TempDir())
	if res.Outcome != "failed" || res.Error != newPageErr.Error() {
		t.Fatalf("new page result = %#v", res)
	}

	page := &fakeRealpassPage{
		url:           "https://example.com",
		titleErr:      errors.New("title ignored"),
		contentErr:    errors.New("content failed"),
		evaluateErr:   errors.New("eval failed"),
		screenshotErr: errors.New("shot failed"),
		response:      fakeRealpassResponse{status: 200},
	}
	res = runTargetWithBrowserContext(context.Background(), &fakeRealpassBrowser{pid: os.Getpid(), page: page}, targetResult{
		Name: "x", URL: "https://example.com", StartedAt: start,
	}, start, time.Second, "commit", 0, 0, 0, time.Hour, true, true, t.TempDir())
	if res.Outcome != "failed" || !strings.Contains(res.Error, "content failed") || res.Detector["error"] == "" {
		t.Fatalf("content result = %#v", res)
	}

	navTimeout := fmt.Errorf("%w: Timeout 30000ms exceeded", gomoufox.ErrNavigationTimeout)
	page = &fakeRealpassPage{
		url:      "https://example.com/final",
		title:    "Loaded",
		content:  "<main>ok</main>",
		gotoErr:  navTimeout,
		evaluate: map[string]any{"webdriver": false},
	}
	res = runTargetWithBrowserContext(context.Background(), &fakeRealpassBrowser{pid: os.Getpid(), page: page}, targetResult{
		Name: "x", URL: "https://example.com", StartedAt: start,
	}, start, time.Second, "commit", 0, 0, 0, time.Hour, true, false, t.TempDir())
	if res.Outcome != "passed" || res.Error != "" || !strings.Contains(res.NavigationError, "Timeout 30000ms exceeded") {
		t.Fatalf("recoverable navigation timeout result = %#v", res)
	}

	page = &fakeRealpassPage{
		url:      "about:blank",
		content:  "",
		gotoErr:  navTimeout,
		evaluate: map[string]any{},
	}
	res = runTargetWithBrowserContext(context.Background(), &fakeRealpassBrowser{pid: os.Getpid(), page: page}, targetResult{
		Name: "x", URL: "https://example.com", StartedAt: start,
	}, start, time.Second, "commit", 0, 0, 0, time.Hour, true, false, t.TempDir())
	if res.Outcome != "failed" || !strings.Contains(res.Error, "Timeout 30000ms exceeded") {
		t.Fatalf("unusable navigation timeout result = %#v", res)
	}

	gotoErr := errors.New("goto failed")
	page = &fakeRealpassPage{url: "https://example.com", content: "<main>ok</main>", gotoErr: gotoErr, evaluate: map[string]any{}}
	res = runTargetWithBrowserContext(context.Background(), &fakeRealpassBrowser{pid: os.Getpid(), page: page}, targetResult{
		Name: "x", URL: "https://example.com", StartedAt: start,
	}, start, time.Second, "commit", 0, 0, 0, time.Hour, true, false, t.TempDir())
	if res.Outcome != "failed" || res.Error != gotoErr.Error() {
		t.Fatalf("goto result = %#v", res)
	}

	page = &fakeRealpassPage{
		url:           "https://example.com",
		content:       "<main>ok</main>",
		evaluate:      make(chan int),
		screenshotErr: errors.New("shot failed"),
		response:      fakeRealpassResponse{status: 200},
	}
	res = runTargetWithBrowserContext(context.Background(), &fakeRealpassBrowser{pid: os.Getpid(), page: page}, targetResult{
		Name: "x", URL: "https://example.com", StartedAt: start,
	}, start, time.Second, "commit", 0, 0, 0, time.Hour, true, true, t.TempDir())
	if res.Outcome != "failed" || !strings.Contains(res.Error, "shot failed") || res.Detector["error"] == "" {
		t.Fatalf("screenshot result = %#v", res)
	}

	page = &fakeRealpassPage{evaluate: math.Inf(1)}
	if got := detectorSnapshot(context.Background(), page); !strings.Contains(got["error"].(string), "unsupported value") {
		t.Fatalf("marshal detector = %#v", got)
	}
	page = &fakeRealpassPage{evaluate: []int{1}}
	if got := detectorSnapshot(context.Background(), page); !strings.Contains(got["error"].(string), "cannot unmarshal") {
		t.Fatalf("unmarshal detector = %#v", got)
	}
}

func TestRunTargetWithBrowserContextUsesCombinedCapture(t *testing.T) {
	start := time.Now()
	page := &fakeRealpassPage{
		url:        "https://example.com/final",
		titleErr:   errors.New("title should come from combined capture"),
		contentErr: errors.New("content should come from combined capture"),
		evaluate: map[string]any{
			"title":         "Combined",
			"content":       "<main>ok</main>",
			"content_bytes": 42,
			"detector":      map[string]any{"webdriver": false},
		},
		response: fakeRealpassResponse{status: 200, statusText: "OK"},
	}
	res := runTargetWithBrowserContext(context.Background(), &fakeRealpassBrowser{pid: os.Getpid(), page: page}, targetResult{
		Name: "combined", URL: "https://example.com", StartedAt: start,
	}, start, time.Second, "commit", 0, 0, 100, time.Hour, true, false, t.TempDir())
	if res.Outcome != "passed" || res.Title != "Combined" || res.ContentBytes != 42 || res.Detector["webdriver"] != false {
		t.Fatalf("combined capture result = %#v", res)
	}
}

func TestFingerprintDetectorExpressionCoversHighRiskSurface(t *testing.T) {
	for _, want := range []string{
		"navigator.webdriver",
		"navigator.userAgent",
		"navigator.platform",
		"navigator.languages",
		"screen.availWidth",
		"screen.colorDepth",
		"window.devicePixelRatio",
		"Intl.DateTimeFormat",
		"WEBGL_debug_renderer_info",
		"RTCPeerConnection",
		"document.fonts.check",
		"canvas.toDataURL",
	} {
		if !strings.Contains(fingerprintDetectorExpression, want) {
			t.Fatalf("fingerprint detector missing %q\n%s", want, fingerprintDetectorExpression)
		}
	}
}

func TestPageCaptureAndContentCapBranches(t *testing.T) {
	ctx := context.Background()
	page := &fakeRealpassPage{evaluate: map[string]any{
		"title":         "Loaded",
		"content":       "<main>abcdef</main>",
		"content_bytes": 19,
		"detector":      map[string]any{"webdriver": false},
	}}
	title, content, bytes, detector, err := capturePageData(ctx, page, 6)
	if err != nil || title != "Loaded" || content != "<main>abcdef</main>" || bytes != 19 || detector["webdriver"] != false {
		t.Fatalf("capture title=%q content=%q bytes=%d detector=%#v err=%v", title, content, bytes, detector, err)
	}
	if _, _, _, _, err := capturePageData(ctx, &fakeRealpassPage{evaluate: []int{1}}, 6); err == nil {
		t.Fatal("capture accepted non-object payload")
	}
	if _, _, _, _, err := capturePageData(ctx, &fakeRealpassPage{evaluate: map[string]any{}}, 6); err == nil || !strings.Contains(err.Error(), "empty payload") {
		t.Fatalf("empty capture err = %v", err)
	}

	content, bytes, err = pageContentForClassification(ctx, &fakeRealpassPage{evaluate: map[string]any{"content": "abc", "bytes": 6}}, 3)
	if err != nil || content != "abc" || bytes != 6 {
		t.Fatalf("capped content=%q bytes=%d err=%v", content, bytes, err)
	}
	content, bytes, err = pageContentForClassification(ctx, &fakeRealpassPage{evaluateErr: errors.New("eval failed"), content: "abcdef"}, 3)
	if err != nil || content != "abc" || bytes != 6 {
		t.Fatalf("fallback content=%q bytes=%d err=%v", content, bytes, err)
	}
	if _, _, err := pageContentForClassification(ctx, &fakeRealpassPage{evaluateErr: errors.New("eval failed"), contentErr: errors.New("content failed")}, 3); err == nil || !strings.Contains(err.Error(), "eval failed") {
		t.Fatalf("fallback error = %v", err)
	}
	if _, _, err := pageContentForClassification(ctx, &fakeRealpassPage{evaluate: math.Inf(1)}, 3); err == nil {
		t.Fatal("content cap accepted unmarshalable JSON value")
	}
	if _, _, err := pageContentForClassification(ctx, &fakeRealpassPage{evaluate: []int{1}}, 3); err == nil {
		t.Fatal("content cap accepted non-object payload")
	}
}

func TestLaunchAndRunTargetUseInjectedBrowserFactory(t *testing.T) {
	old := newRealpassBrowser
	t.Cleanup(func() { newRealpassBrowser = old })
	launchErr := errors.New("launch failed")
	newRealpassBrowser = func(context.Context, ...gomoufox.Option) (realpassBrowser, error) {
		return nil, launchErr
	}
	res := runTarget(context.Background(), target{Name: "x", URL: "https://example.com"}, 1, time.Second, "commit", 0, 0, 0, time.Hour, false, true, true, "python", "", false, t.TempDir())
	if res.Outcome != "failed" || res.Error != launchErr.Error() {
		t.Fatalf("launch failure result = %#v", res)
	}

	page := &fakeRealpassPage{url: "https://example.com", content: "<main>ok</main>", evaluate: map[string]any{}, response: fakeRealpassResponse{status: 200}}
	closed := false
	newRealpassBrowser = func(context.Context, ...gomoufox.Option) (realpassBrowser, error) {
		return &fakeRealpassBrowser{pid: os.Getpid(), page: page, closeFunc: func() { closed = true }}, nil
	}
	res = runTarget(context.Background(), target{Name: "x", URL: "https://example.com"}, 1, time.Second, "commit", 0, 0, 0, time.Hour, true, true, true, "node-direct", t.TempDir(), false, t.TempDir())
	if res.Outcome != "passed" || !closed {
		t.Fatalf("run target result=%#v closed=%v", res, closed)
	}

	res = runTargetWithBrowser(context.Background(), &fakeRealpassBrowser{pid: os.Getpid(), page: page}, target{Name: "wrapped", URL: "https://example.com"}, 1, time.Second, "commit", 0, 0, 0, time.Hour, true, false, t.TempDir())
	if res.Outcome != "passed" {
		t.Fatalf("wrapped target result=%#v", res)
	}
}

func TestRunWithInjectedReusableBrowserGatesAndReportErrors(t *testing.T) {
	old := newRealpassBrowser
	t.Cleanup(func() { newRealpassBrowser = old })
	page := &fakeRealpassPage{url: "https://example.com/final", title: "ok", content: "<main>ok</main>", evaluate: map[string]any{}, response: fakeRealpassResponse{status: 200, statusText: "OK"}}
	browser := &fakeRealpassBrowser{pid: os.Getpid(), page: page}
	newRealpassBrowser = func(context.Context, ...gomoufox.Option) (realpassBrowser, error) {
		return browser, nil
	}
	dir := t.TempDir()
	customVenv := filepath.Join(t.TempDir(), "custom-venv")
	code, stdout, stderr := runRealpassForTest(t,
		"--out", dir,
		"--target", "one=https://user:pass@example.com/one?token=secret",
		"--target", "two=https://example.com/two",
		"--max-targets", "1",
		"--repeat", "2",
		"--reuse-browser",
		"--report-style", "compact",
		"--screenshots=false",
		"--settle", "0s",
		"--timeout", "1s",
		"--wait-until", "domcontentloaded",
		"--sidecar-runtime", "node-direct",
		"--venv-dir", customVenv,
		"--sample-interval", "1h",
		"--expect-passed", "2",
		"--max-blocked", "0",
		"--max-failed", "0",
		"--max-rss-mib", "10000",
		"--max-cpu-percent", "10000",
	)
	if code != 0 || !strings.Contains(stdout, "report:") || stderr == "" || !browser.closed {
		t.Fatalf("success code=%d stdout=%q stderr=%q closed=%v", code, stdout, stderr, browser.closed)
	}
	assertNoDiagnosticSecrets(t, stderr)
	rep, err := readReport(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 2 || rep.Summary.Passed != 2 || rep.Options.ReuseBrowser != true || rep.Options.ReportStyle != "compact" || rep.Options.Screenshots != false || rep.Options.WaitUntil != "domcontentloaded" || rep.Options.SidecarRuntime != "node-direct" || rep.Options.CustomVenv != true {
		t.Fatalf("report = %#v", rep)
	}
	reportJSON, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	reportMD, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "report-full.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("compact run wrote report-full.json by default: %v", err)
	}
	resultsJSONL, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoDiagnosticSecrets(t, string(reportJSON))
	assertNoDiagnosticSecrets(t, string(reportMD))
	assertNoDiagnosticSecrets(t, string(resultsJSONL))
	if bytes.Contains(reportJSON, []byte(customVenv)) || bytes.Contains(reportMD, []byte(customVenv)) {
		t.Fatal("report leaked custom venv path")
	}
	if lines := strings.Split(strings.TrimSpace(string(resultsJSONL)), "\n"); len(lines) != 2 {
		t.Fatalf("results.jsonl lines = %d\n%s", len(lines), resultsJSONL)
	}
	if strings.Contains(string(reportMD), "## Details") {
		t.Fatalf("compact report included details:\n%s", reportMD)
	}
	if strings.Contains(string(reportJSON), "final_url") || strings.Contains(string(reportJSON), "detector") {
		t.Fatalf("compact JSON kept details\ncompact=%s", reportJSON)
	}

	debugDir := t.TempDir()
	code, _, stderr = runRealpassForTest(t,
		"--out", debugDir,
		"--target", "one=https://user:pass@example.com/one?token=secret",
		"--reuse-browser",
		"--report-style", "compact",
		"--debug-report",
		"--screenshots=false",
		"--settle", "0s",
		"--timeout", "1s",
		"--sample-interval", "1h",
		"--max-failed=-1",
	)
	if code != 0 {
		t.Fatalf("debug report run code=%d stderr=%q", code, stderr)
	}
	reportFullJSON, err := os.ReadFile(filepath.Join(debugDir, "report-full.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoDiagnosticSecrets(t, string(reportFullJSON))
	if !strings.Contains(string(reportFullJSON), "final_url") {
		t.Fatalf("debug report missing full details:\n%s", reportFullJSON)
	}

	code, _, stderr = runRealpassForTest(t, "--target", "one=https://example.com/one", "--report-style", "tiny")
	if code != 2 || !strings.Contains(stderr, "report-style must be full or compact") {
		t.Fatalf("bad report style code=%d stderr=%q", code, stderr)
	}

	code, _, stderr = runRealpassForTest(t,
		"--out", t.TempDir(),
		"--target", "one=https://example.com/one",
		"--screenshots=false",
		"--settle", "0s",
		"--timeout", "1s",
		"--sample-interval", "1h",
		"--expect-passed", "2",
	)
	if code != 1 || !strings.Contains(stderr, "realpass gate: passed 1 < expected 2") {
		t.Fatalf("gate code=%d stderr=%q", code, stderr)
	}

	reportBlocked := t.TempDir()
	if err := os.Mkdir(filepath.Join(reportBlocked, "report.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runRealpassForTest(t,
		"--out", reportBlocked,
		"--target", "one=https://example.com/one",
		"--screenshots=false",
		"--settle", "0s",
		"--timeout", "1s",
		"--sample-interval", "1h",
	)
	if code != 1 || !strings.Contains(stderr, "write report") {
		t.Fatalf("report error code=%d stderr=%q", code, stderr)
	}

	newRealpassBrowser = func(context.Context, ...gomoufox.Option) (realpassBrowser, error) {
		return nil, errors.New("reuse failed " + diagnosticSecretFixture)
	}
	code, _, stderr = runRealpassForTest(t,
		"--out", t.TempDir(),
		"--target", "one=https://example.com/one",
		"--reuse-browser",
	)
	if code != 1 || !strings.Contains(stderr, "start reusable browser") {
		t.Fatalf("reuse launch code=%d stderr=%q", code, stderr)
	}
	assertNoDiagnosticSecrets(t, stderr)
}

func TestReportRenderingAndHelpers(t *testing.T) {
	dir := t.TempDir()
	rep := report{
		StartedAt:  time.Unix(1, 0),
		FinishedAt: time.Unix(2, 0),
		GoOS:       "darwin",
		GoArch:     "arm64",
		Results: []targetResult{{
			Name: "a|b", URL: "https://user:pass@example.com/?token=secret", FinalURL: "wss://127.0.0.1:9222/rawtoken",
			Kind: "custom", Title: "line\nbreak " + diagnosticSecretFixture, Outcome: "failed", Error: "bad|pipe " + diagnosticSecretFixture, Attempt: 1,
			StatusText: "Set-Cookie: auth=secret",
			Headers:    map[string]string{"server": "Cookie: sid=secret", "set-cookie": "sid=cookie-secret"},
			Signals:    []string{"token=secret"},
			Detector: map[string]any{
				"error":   diagnosticSecretFixture,
				"nested":  map[string]any{"cookie": "Cookie: sid=secret"},
				"items":   []any{diagnosticSecretFixture, map[string]any{"token": "token=secret"}},
				"headers": map[string]string{"authorization": "abc.def"},
				"ok":      true,
			},
			ScreenshotPath: "shot.png", ScreenshotBytes: 3,
			Resources: resourceSummary{RootPID: 1, Samples: 2, PeakRSSMiB: 3, MaxCPUPercent: 4, AvgCPUPercent: 5, PeakProcesses: 6, ProcessCommands: []string{"camoufox " + diagnosticSecretFixture}, SampleErrors: []string{"ps " + diagnosticSecretFixture}},
		}},
	}
	if err := writeReports(dir, rep); err != nil {
		t.Fatal(err)
	}
	loaded, err := readReport(filepath.Join(dir, "report.json"))
	if err != nil || loaded.Summary.Total != 1 || loaded.Summary.Failed != 1 {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	md, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if text := string(md); !strings.Contains(text, "a\\|b") || !strings.Contains(text, "bad\\|pipe") {
		t.Fatalf("markdown = %s", text)
	}
	jsonReport, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoDiagnosticSecrets(t, string(jsonReport))
	assertNoDiagnosticSecrets(t, string(md))
	if thresholdString(0) != "" || thresholdString(12.5) != "12.5" {
		t.Fatalf("thresholdString mismatch")
	}
	if got := selectedHeaders(map[string]string{"server": "s", "x-cdn": "cdn", "other": "x"}); got["server"] != "s" || got["x-cdn"] != "cdn" || got["other"] != "" {
		t.Fatalf("headers = %#v", got)
	}
	if got := selectedHeaders(nil); got != nil {
		t.Fatalf("nil headers = %#v", got)
	}
	if got := selectedHeaders(map[string]string{"other": "x"}); got != nil {
		t.Fatalf("unselected headers = %#v", got)
	}
	if firstN("abcdef", 3) != "abc" || firstN("abc", 10) != "abc" || escapeMD("a|b\nc") != "a\\|b c" {
		t.Fatalf("string helpers mismatch")
	}
	if _, err := readReport(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing report read succeeded")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readReport(bad); err == nil {
		t.Fatal("bad report read succeeded")
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeReports(blocked, rep); err == nil {
		t.Fatal("writeReports under file succeeded")
	}
	badJSON := report{Results: []targetResult{{Name: "bad", Detector: map[string]any{"bad": func() {}}}}}
	if err := writeReports(dir, badJSON); err == nil {
		t.Fatal("writeReports with unmarshalable detector succeeded")
	}
	mdBlocked := filepath.Join(t.TempDir(), "reports")
	if err := os.MkdirAll(filepath.Join(mdBlocked, "report.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeReports(mdBlocked, report{}); err == nil {
		t.Fatal("writeReports over report.md directory succeeded")
	}
	if err := writeReports(dir, report{Options: reportOptions{ReportStyle: "tiny"}}); err == nil {
		t.Fatal("writeReports accepted invalid style")
	}
	debugBadJSON := report{Options: reportOptions{ReportStyle: string(reportStyleCompact), DebugReport: true}, Results: []targetResult{{Name: "bad", Detector: map[string]any{"bad": func() {}}}}}
	if err := writeReports(t.TempDir(), debugBadJSON); err == nil {
		t.Fatal("writeReports compact debug accepted unmarshalable full report")
	}
	debugBlocked := t.TempDir()
	if err := os.Mkdir(filepath.Join(debugBlocked, "report-full.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeReports(debugBlocked, report{Options: reportOptions{ReportStyle: string(reportStyleCompact), DebugReport: true}}); err == nil {
		t.Fatal("writeReports compact debug wrote over report-full.json directory")
	}
}

func TestAppendResultJSONLErrors(t *testing.T) {
	if err := appendResultJSONL(t.TempDir(), targetResult{Name: "bad", Detector: map[string]any{"bad": func() {}}}); err == nil {
		t.Fatal("appendResultJSONL accepted unmarshalable result")
	}
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendResultJSONL(blocked, targetResult{Name: "ok"}); err == nil {
		t.Fatal("appendResultJSONL opened child under file path")
	}
}

func TestProcessMonitorAndWaitHelpers(t *testing.T) {
	monitor := newProcessMonitor(-1, time.Hour)
	monitor.sample(context.Background())
	for i := 0; i < 10; i++ {
		monitor.sample(context.Background())
	}
	if got := monitor.snapshot(); len(got.SampleErrors) != 5 {
		t.Fatalf("sample errors = %#v", got.SampleErrors)
	}
	if _, err := sampleProcessTree(context.Background(), -1); err == nil {
		t.Fatal("bad pid sample succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waitFor(ctx, time.Hour)
	if _, err := sampleProcessTree(ctx, os.Getpid()); err == nil {
		t.Fatal("canceled ps sample succeeded")
	}
	waitFor(context.Background(), 0)
	waitFor(context.Background(), time.Millisecond)

	monitor = newProcessMonitor(os.Getpid(), time.Millisecond)
	monitor.start(context.Background())
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := monitor.snapshot(); got.Samples >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	monitor.stopAndWait()
	monitor.stopAndWait()
	if got := monitor.snapshot(); got.Samples < 2 {
		t.Fatalf("monitor snapshot = %#v", got)
	}
	canceled, cancelStart := context.WithCancel(context.Background())
	cancelStart()
	monitor = newProcessMonitor(os.Getpid(), time.Hour)
	monitor.start(canceled)
	select {
	case <-monitor.done:
	case <-time.After(time.Second):
		t.Fatal("canceled monitor did not stop")
	}
}

func TestTargetFlagsSetAndString(t *testing.T) {
	var flags targetFlags
	if err := flags.Set("cf=https://example.com"); err != nil {
		t.Fatal(err)
	}
	if err := flags.Set("bad target"); err == nil {
		t.Fatal("bad target flag succeeded")
	}
	if got := flags.String(); got != "cf=https://example.com" {
		t.Fatalf("flags string = %q", got)
	}
}

func runCompareForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	stdout := tempOutputFile(t, dir, "stdout")
	stderr := tempOutputFile(t, dir, "stderr")
	defer func() { _ = stdout.Close() }()
	defer func() { _ = stderr.Close() }()
	code := runCompare(context.Background(), args, stdout, stderr)
	return code, readOutputFile(t, stdout), readOutputFile(t, stderr)
}

func writeReportForTest(t *testing.T, path string, rep report) {
	t.Helper()
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runRealpassForTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	stdout := tempOutputFile(t, dir, "stdout")
	stderr := tempOutputFile(t, dir, "stderr")
	defer func() { _ = stdout.Close() }()
	defer func() { _ = stderr.Close() }()
	code := run(context.Background(), args, stdout, stderr)
	return code, readOutputFile(t, stdout), readOutputFile(t, stderr)
}

func tempOutputFile(t *testing.T, dir, name string) *os.File {
	t.Helper()
	f, err := os.CreateTemp(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func readOutputFile(t *testing.T, f *os.File) string {
	t.Helper()
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func containsAll(got, want []string) bool {
	set := map[string]bool{}
	for _, value := range got {
		set[value] = true
	}
	for _, value := range want {
		if !set[value] {
			return false
		}
	}
	return true
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func intPtr(v int) *int { return &v }

func floatPtr(v float64) *float64 { return &v }

type fakeRealpassBrowser struct {
	pid        int
	page       realpassPage
	newPageErr error
	closeFunc  func()
	closed     bool
}

func (b *fakeRealpassBrowser) NewPage(context.Context, ...gomoufox.ContextOption) (realpassPage, error) {
	if b.newPageErr != nil {
		return nil, b.newPageErr
	}
	return b.page, nil
}

func (b *fakeRealpassBrowser) Sidecar() gomoufox.SidecarInfo {
	return gomoufox.SidecarInfo{PID: b.pid}
}

func (b *fakeRealpassBrowser) Close() error {
	b.closed = true
	if b.closeFunc != nil {
		b.closeFunc()
	}
	return nil
}

type fakeRealpassPage struct {
	url           string
	title         string
	content       string
	evaluate      any
	screenshot    []byte
	response      realpassResponse
	gotoErr       error
	titleErr      error
	contentErr    error
	evaluateErr   error
	screenshotErr error
	closed        bool
}

func (p *fakeRealpassPage) Goto(context.Context, string, ...gomoufox.GotoOption) (realpassResponse, error) {
	if p.gotoErr != nil {
		return nil, p.gotoErr
	}
	return p.response, nil
}

func (p *fakeRealpassPage) WaitForLoadState(context.Context, string) error { return nil }
func (p *fakeRealpassPage) URL() string                                    { return p.url }

func (p *fakeRealpassPage) Title(context.Context) (string, error) {
	if p.titleErr != nil {
		return "", p.titleErr
	}
	return p.title, nil
}

func (p *fakeRealpassPage) Content(context.Context) (string, error) {
	if p.contentErr != nil {
		return "", p.contentErr
	}
	return p.content, nil
}

func (p *fakeRealpassPage) Evaluate(context.Context, string, ...any) (any, error) {
	if p.evaluateErr != nil {
		return nil, p.evaluateErr
	}
	return p.evaluate, nil
}

func (p *fakeRealpassPage) ScreenshotToFile(_ context.Context, path string, _ ...gomoufox.ScreenshotOption) error {
	if p.screenshotErr != nil {
		return p.screenshotErr
	}
	return os.WriteFile(path, p.screenshot, 0o600)
}

func (p *fakeRealpassPage) Close() error {
	p.closed = true
	return nil
}

type fakeRealpassResponse struct {
	status     int
	statusText string
	headers    map[string]string
}

func (r fakeRealpassResponse) Status() int                { return r.status }
func (r fakeRealpassResponse) StatusText() string         { return r.statusText }
func (r fakeRealpassResponse) Headers() map[string]string { return r.headers }
