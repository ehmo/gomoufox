package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gomoufox "github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/camoufoxcfg"
	"github.com/ehmo/gomoufox/internal/buildinfo"
	"github.com/ehmo/gomoufox/internal/policy"
)

type target struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Kind string `json:"kind"`
}

type report struct {
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at"`
	GoOS       string         `json:"goos"`
	GoArch     string         `json:"goarch"`
	Options    reportOptions  `json:"options"`
	Summary    reportSummary  `json:"summary"`
	Results    []targetResult `json:"results"`
}

type reportOptions struct {
	Timeout          string `json:"timeout"`
	WaitUntil        string `json:"wait_until"`
	Settle           string `json:"settle"`
	LoadStateTimeout string `json:"load_state_timeout"`
	ContentMaxBytes  int    `json:"content_max_bytes,omitempty"`
	SampleInterval   string `json:"sample_interval"`
	ReportStyle      string `json:"report_style"`
	DebugReport      bool   `json:"debug_report,omitempty"`
	Headful          bool   `json:"headful"`
	Screenshots      bool   `json:"screenshots"`
	Repeats          int    `json:"repeats"`
	ReuseBrowser     bool   `json:"reuse_browser"`
	UnsafeDirect     bool   `json:"unsafe_direct_network"`
	GeneratedPersona bool   `json:"generated_persona"`
	SidecarRuntime   string `json:"sidecar_runtime,omitempty"`
	CustomVenv       bool   `json:"custom_venv,omitempty"`
	ExpectPassed     int    `json:"expect_passed,omitempty"`
	MaxBlocked       int    `json:"max_blocked"`
	MaxFailed        int    `json:"max_failed"`
	MaxRSSMiB        string `json:"max_rss_mib,omitempty"`
	MaxCPUPercent    string `json:"max_cpu_percent,omitempty"`
}

type reportSummary struct {
	Total          int     `json:"total"`
	Passed         int     `json:"passed"`
	Blocked        int     `json:"blocked"`
	Failed         int     `json:"failed"`
	PeakRSSMiB     float64 `json:"peak_rss_mib"`
	PeakCPUPercent float64 `json:"peak_cpu_percent"`
}

type compactReport struct {
	StartedAt  time.Time             `json:"started_at"`
	FinishedAt time.Time             `json:"finished_at"`
	GoOS       string                `json:"goos,omitempty"`
	GoArch     string                `json:"goarch,omitempty"`
	Options    reportOptions         `json:"options"`
	Summary    reportSummary         `json:"summary"`
	Results    []compactTargetResult `json:"results"`
}

type compactTargetResult struct {
	Name            string                 `json:"name"`
	Kind            string                 `json:"kind,omitempty"`
	Attempt         int                    `json:"attempt,omitempty"`
	Outcome         string                 `json:"outcome"`
	Error           string                 `json:"error,omitempty"`
	NavigationError string                 `json:"navigation_error,omitempty"`
	StartedAt       time.Time              `json:"started_at"`
	DurationMS      int64                  `json:"duration_ms"`
	Status          int                    `json:"status,omitempty"`
	Signals         []string               `json:"signals,omitempty"`
	Resources       compactResourceSummary `json:"resources"`
}

type compactResourceSummary struct {
	Samples       int     `json:"samples,omitempty"`
	PeakRSSMiB    float64 `json:"peak_rss_mib"`
	MaxCPUPercent float64 `json:"max_cpu_percent"`
	PeakProcesses int     `json:"peak_processes,omitempty"`
}

type gateOptions struct {
	ExpectPassed  int
	MaxFailed     *int
	MaxBlocked    *int
	MaxRSSMiB     *float64
	MaxCPUPercent *float64
}

type targetResult struct {
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	Kind            string            `json:"kind"`
	Attempt         int               `json:"attempt"`
	Outcome         string            `json:"outcome"`
	Error           string            `json:"error,omitempty"`
	NavigationError string            `json:"navigation_error,omitempty"`
	StartedAt       time.Time         `json:"started_at"`
	DurationMS      int64             `json:"duration_ms"`
	Status          int               `json:"status,omitempty"`
	StatusText      string            `json:"status_text,omitempty"`
	FinalURL        string            `json:"final_url,omitempty"`
	Title           string            `json:"title,omitempty"`
	ContentBytes    int               `json:"content_bytes,omitempty"`
	ScreenshotPath  string            `json:"screenshot_path,omitempty"`
	ScreenshotBytes int               `json:"screenshot_bytes,omitempty"`
	Signals         []string          `json:"signals,omitempty"`
	Detector        map[string]any    `json:"detector,omitempty"`
	Resources       resourceSummary   `json:"resources"`
	Headers         map[string]string `json:"headers,omitempty"`
}

type resourceSummary struct {
	Scope           string   `json:"scope,omitempty"`
	RootPID         int      `json:"root_pid,omitempty"`
	Samples         int      `json:"samples"`
	PeakRSSKiB      int64    `json:"peak_rss_kib"`
	PeakRSSMiB      float64  `json:"peak_rss_mib"`
	MaxCPUPercent   float64  `json:"max_cpu_percent"`
	AvgCPUPercent   float64  `json:"avg_cpu_percent"`
	PeakProcesses   int      `json:"peak_processes"`
	ProcessCommands []string `json:"process_commands,omitempty"`
	SampleErrors    []string `json:"sample_errors,omitempty"`
}

type processRow struct {
	PID  int
	PPID int
	CPU  float64
	RSS  int64
	Cmd  string
}

type reportStyle string

const (
	reportStyleCompact reportStyle = "compact"
	reportStyleFull    reportStyle = "full"
)

type processSample struct {
	RSSKiB   int64
	CPU      float64
	Count    int
	Commands []string
}

type realpassBrowser interface {
	NewPage(context.Context, ...gomoufox.ContextOption) (realpassPage, error)
	Sidecar() gomoufox.SidecarInfo
	Close() error
}

type realpassPage interface {
	Goto(context.Context, string, ...gomoufox.GotoOption) (realpassResponse, error)
	WaitForLoadState(context.Context, string) error
	URL() string
	Title(context.Context) (string, error)
	Content(context.Context) (string, error)
	Evaluate(context.Context, string, ...any) (any, error)
	ScreenshotToFile(context.Context, string, ...gomoufox.ScreenshotOption) error
	Close() error
}

type realpassResponse interface {
	Status() int
	StatusText() string
	Headers() map[string]string
}

type realpassBrowserFunc struct {
	newPage func(context.Context, ...gomoufox.ContextOption) (realpassPage, error)
	sidecar func() gomoufox.SidecarInfo
	close   func() error
}

func (b realpassBrowserFunc) NewPage(ctx context.Context, opts ...gomoufox.ContextOption) (realpassPage, error) {
	return b.newPage(ctx, opts...)
}
func (b realpassBrowserFunc) Sidecar() gomoufox.SidecarInfo { return b.sidecar() }
func (b realpassBrowserFunc) Close() error                  { return b.close() }

type realpassPageFunc struct {
	gotoFn          func(context.Context, string, ...gomoufox.GotoOption) (realpassResponse, error)
	waitLoadStateFn func(context.Context, string) error
	urlFn           func() string
	titleFn         func(context.Context) (string, error)
	contentFn       func(context.Context) (string, error)
	evaluateFn      func(context.Context, string, ...any) (any, error)
	screenshotFn    func(context.Context, string, ...gomoufox.ScreenshotOption) error
	closeFn         func() error
}

func (p realpassPageFunc) Goto(ctx context.Context, url string, opts ...gomoufox.GotoOption) (realpassResponse, error) {
	return p.gotoFn(ctx, url, opts...)
}
func (p realpassPageFunc) WaitForLoadState(ctx context.Context, state string) error {
	return p.waitLoadStateFn(ctx, state)
}
func (p realpassPageFunc) URL() string { return p.urlFn() }
func (p realpassPageFunc) Title(ctx context.Context) (string, error) {
	return p.titleFn(ctx)
}
func (p realpassPageFunc) Content(ctx context.Context) (string, error) {
	return p.contentFn(ctx)
}
func (p realpassPageFunc) Evaluate(ctx context.Context, expression string, args ...any) (any, error) {
	return p.evaluateFn(ctx, expression, args...)
}
func (p realpassPageFunc) ScreenshotToFile(ctx context.Context, filePath string, opts ...gomoufox.ScreenshotOption) error {
	return p.screenshotFn(ctx, filePath, opts...)
}
func (p realpassPageFunc) Close() error { return p.closeFn() }

type realpassResponseFunc struct {
	status     func() int
	statusText func() string
	headers    func() map[string]string
}

func (r realpassResponseFunc) Status() int                { return r.status() }
func (r realpassResponseFunc) StatusText() string         { return r.statusText() }
func (r realpassResponseFunc) Headers() map[string]string { return r.headers() }

var gomoufoxNewForRealpass = gomoufox.New

var newRealpassBrowser = func(ctx context.Context, opts ...gomoufox.Option) (realpassBrowser, error) {
	b, err := gomoufoxNewForRealpass(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return newLiveRealpassBrowser(b.NewPage, b.Sidecar, b.Close), nil
}

func newLiveRealpassBrowser(
	newPage func(context.Context, ...gomoufox.ContextOption) (*gomoufox.Page, error),
	sidecar func() gomoufox.SidecarInfo,
	close func() error,
) realpassBrowser {
	return realpassBrowserFunc{
		newPage: func(ctx context.Context, opts ...gomoufox.ContextOption) (realpassPage, error) {
			p, err := newPage(ctx, opts...)
			if err != nil {
				return nil, err
			}
			return newLiveRealpassPage(p.Goto, p.WaitForLoadState, p.URL, p.Title, p.Content, p.Evaluate, p.ScreenshotToFile, p.Close), nil
		},
		sidecar: sidecar,
		close:   close,
	}
}

func newLiveRealpassPage(
	gotoFn func(context.Context, string, ...gomoufox.GotoOption) (*gomoufox.Response, error),
	waitLoadStateFn func(context.Context, string) error,
	urlFn func() string,
	titleFn func(context.Context) (string, error),
	contentFn func(context.Context) (string, error),
	evaluateFn func(context.Context, string, ...any) (any, error),
	screenshotFn func(context.Context, string, ...gomoufox.ScreenshotOption) error,
	closeFn func() error,
) realpassPage {
	return realpassPageFunc{
		gotoFn: func(ctx context.Context, url string, opts ...gomoufox.GotoOption) (realpassResponse, error) {
			resp, err := gotoFn(ctx, url, opts...)
			if err != nil || resp == nil {
				return nil, err
			}
			return newLiveRealpassResponse(resp.Status, resp.StatusText, resp.Headers), nil
		},
		waitLoadStateFn: waitLoadStateFn,
		urlFn:           urlFn,
		titleFn:         titleFn,
		contentFn:       contentFn,
		evaluateFn:      evaluateFn,
		screenshotFn:    screenshotFn,
		closeFn:         closeFn,
	}
}

func newLiveRealpassResponse(status func() int, statusText func() string, headers func() map[string]string) realpassResponse {
	return realpassResponseFunc{
		status:     status,
		statusText: statusText,
		headers:    headers,
	}
}

type processMonitor struct {
	rootPID  int
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}

	mu      sync.Mutex
	summary resourceSummary
	cpuSum  float64
}

type targetFlags []target
type nameFlags []string

var exitProcess = os.Exit

func main() { exitProcess(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, stdout, stderr *os.File) int {
	if len(args) > 0 && args[0] == "compare" {
		return runCompare(ctx, args[1:], stdout, stderr)
	}
	var customTargets targetFlags
	outDir := "dist/realpass/latest"
	timeout := 60 * time.Second
	waitUntil := "commit"
	settle := 7 * time.Second
	loadStateTimeout := 10 * time.Second
	contentMaxBytes := 0
	sampleInterval := 500 * time.Millisecond
	reportStyleRaw := string(reportStyleCompact)
	debugReport := false
	headful := false
	screenshots := true
	repeats := 1
	maxTargets := 0
	listTargets := false
	reuseBrowser := false
	unsafeDirectNetwork := false
	generatedPersona := false
	sidecarRuntime := string(gomoufox.SidecarRuntimePython)
	venvDir := ""
	expectPassed := 0
	maxBlocked := -1
	maxFailed := 0
	maxRSSMiB := 0.0
	maxCPUPercent := 0.0
	showVersion := false

	flagOutput := policy.NewRedactWriter(stderr)
	defer func() { _ = flagOutput.Flush() }()
	fs := flag.NewFlagSet("gomoufox-realpass", flag.ContinueOnError)
	fs.SetOutput(flagOutput)
	fs.Var(&customTargets, "target", "target URL or name=url; may be repeated")
	fs.StringVar(&outDir, "out", outDir, "output directory")
	fs.DurationVar(&timeout, "timeout", timeout, "per-navigation timeout")
	fs.StringVar(&waitUntil, "wait-until", waitUntil, "navigation lifecycle to wait for: commit, domcontentloaded, load, or networkidle")
	fs.DurationVar(&settle, "settle", settle, "post-navigation wait before classification")
	fs.DurationVar(&loadStateTimeout, "load-state-timeout", loadStateTimeout, "extra load-state wait after settle; 0 disables")
	fs.IntVar(&contentMaxBytes, "content-max-bytes", contentMaxBytes, "maximum HTML bytes fetched for classification; 0 fetches full content")
	fs.DurationVar(&sampleInterval, "sample-interval", sampleInterval, "process resource sample interval")
	fs.StringVar(&reportStyleRaw, "report-style", reportStyleRaw, "report.md detail level: compact or full")
	fs.BoolVar(&debugReport, "debug-report", debugReport, "write report-full.json alongside compact report output")
	fs.BoolVar(&headful, "headful", headful, "run visible browser windows")
	fs.BoolVar(&screenshots, "screenshots", screenshots, "write per-target screenshots")
	fs.IntVar(&repeats, "repeat", repeats, "attempts per target")
	fs.IntVar(&maxTargets, "max-targets", maxTargets, "limit target count after selection")
	fs.BoolVar(&listTargets, "list-targets", listTargets, "print default targets as JSON and exit")
	fs.BoolVar(&reuseBrowser, "reuse-browser", reuseBrowser, "reuse one browser process across targets")
	fs.BoolVar(&unsafeDirectNetwork, "unsafe-direct-network", unsafeDirectNetwork, "UNSAFE: bypass gomoufox local filtering proxy and URL guardrails for browser navigations")
	fs.BoolVar(&generatedPersona, "generated-persona", generatedPersona, "let Camoufox generate launch/context persona defaults")
	fs.StringVar(&sidecarRuntime, "sidecar-runtime", sidecarRuntime, "gomoufox sidecar runtime: python or node-direct")
	fs.StringVar(&venvDir, "venv-dir", venvDir, "managed gomoufox venv directory; default cache")
	fs.IntVar(&expectPassed, "expect-passed", expectPassed, "fail if passed target count is lower than this value; 0 disables")
	fs.IntVar(&maxBlocked, "max-blocked", maxBlocked, "fail if blocked target count exceeds this value; -1 disables")
	fs.IntVar(&maxFailed, "max-failed", maxFailed, "fail if failed target count exceeds this value; -1 disables")
	fs.Float64Var(&maxRSSMiB, "max-rss-mib", maxRSSMiB, "fail if peak RSS MiB exceeds this value; 0 disables")
	fs.Float64Var(&maxCPUPercent, "max-cpu-percent", maxCPUPercent, "fail if peak CPU percent exceeds this value; 0 disables")
	fs.BoolVar(&showVersion, "version", showVersion, "print version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if showVersion {
		_, _ = fmt.Fprintf(stdout, "gomoufox-realpass %s\n", buildinfo.Version)
		return 0
	}
	if listTargets {
		_ = json.NewEncoder(stdout).Encode(defaultTargets())
		return 0
	}
	if repeats < 1 {
		_, _ = fmt.Fprintln(stderr, "repeat must be >= 1")
		return 2
	}
	if sampleInterval <= 0 {
		_, _ = fmt.Fprintln(stderr, "sample-interval must be > 0")
		return 2
	}
	if loadStateTimeout < 0 {
		_, _ = fmt.Fprintln(stderr, "load-state-timeout must be >= 0")
		return 2
	}
	if contentMaxBytes < 0 {
		_, _ = fmt.Fprintln(stderr, "content-max-bytes must be >= 0")
		return 2
	}
	if err := validateWaitUntil(waitUntil); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if _, err := parseSidecarRuntime(sidecarRuntime); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	reportStyleValue, err := parseReportStyle(reportStyleRaw)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	gate, err := gateOptionsFromRaw(expectPassed, maxBlocked, maxFailed, maxRSSMiB, maxCPUPercent)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
		return 2
	}
	targets := []target(customTargets)
	if len(targets) == 0 {
		targets = defaultTargets()
	}
	if maxTargets > 0 && maxTargets < len(targets) {
		targets = targets[:maxTargets]
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		_, _ = fmt.Fprintf(stderr, "create output dir: %s\n", policy.Redact(err.Error()))
		return 1
	}
	shotDir := filepath.Join(outDir, "screenshots")
	if screenshots {
		if err := os.MkdirAll(shotDir, 0o700); err != nil {
			_, _ = fmt.Fprintf(stderr, "create screenshot dir: %s\n", policy.Redact(err.Error()))
			return 1
		}
	}

	rep := report{
		StartedAt: time.Now(),
		GoOS:      runtime.GOOS,
		GoArch:    runtime.GOARCH,
		Options: reportOptions{
			Timeout:          timeout.String(),
			WaitUntil:        waitUntil,
			Settle:           settle.String(),
			LoadStateTimeout: loadStateTimeout.String(),
			ContentMaxBytes:  contentMaxBytes,
			SampleInterval:   sampleInterval.String(),
			ReportStyle:      string(reportStyleValue),
			DebugReport:      debugReport,
			Headful:          headful,
			Screenshots:      screenshots,
			Repeats:          repeats,
			ReuseBrowser:     reuseBrowser,
			UnsafeDirect:     unsafeDirectNetwork,
			GeneratedPersona: generatedPersona,
			SidecarRuntime:   sidecarRuntime,
			CustomVenv:       venvDir != "",
			ExpectPassed:     expectPassed,
			MaxBlocked:       maxBlocked,
			MaxFailed:        maxFailed,
			MaxRSSMiB:        thresholdString(maxRSSMiB),
			MaxCPUPercent:    thresholdString(maxCPUPercent),
		},
	}
	var sharedBrowser realpassBrowser
	if reuseBrowser {
		browser, err := launchBrowser(ctx, headful, unsafeDirectNetwork, generatedPersona, sidecarRuntime, venvDir)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "start reusable browser: %s\n", policy.Redact(err.Error()))
			return 1
		}
		sharedBrowser = browser
		defer func() { _ = sharedBrowser.Close() }()
	}
	for _, tgt := range targets {
		for attempt := 1; attempt <= repeats; attempt++ {
			_, _ = fmt.Fprintf(stderr, "realpass: %s attempt %d -> %s\n", tgt.Name, attempt, policy.Redact(tgt.URL))
			var res targetResult
			if sharedBrowser != nil {
				res = runTargetWithBrowser(ctx, sharedBrowser, tgt, attempt, timeout, waitUntil, settle, loadStateTimeout, contentMaxBytes, sampleInterval, generatedPersona, screenshots, shotDir)
			} else {
				res = runTarget(ctx, tgt, attempt, timeout, waitUntil, settle, loadStateTimeout, contentMaxBytes, sampleInterval, headful, unsafeDirectNetwork, generatedPersona, sidecarRuntime, venvDir, screenshots, shotDir)
			}
			rep.Results = append(rep.Results, res)
			_ = appendResultJSONL(outDir, res)
		}
	}
	rep.FinishedAt = time.Now()
	rep.Summary = summarize(rep.Results)
	if err := writeReports(outDir, rep); err != nil {
		_, _ = fmt.Fprintf(stderr, "write report: %s\n", policy.Redact(err.Error()))
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "report: %s\n", filepath.Join(outDir, "report.md"))
	_, _ = fmt.Fprintf(stdout, "json: %s\n", filepath.Join(outDir, "report.json"))
	if failures := evaluateReportGate(rep, gate); len(failures) > 0 {
		for _, failure := range failures {
			_, _ = fmt.Fprintf(stderr, "realpass gate: %s\n", policy.Redact(failure))
		}
		return 1
	}
	return 0
}

func defaultTargets() []target {
	return []target{
		{Name: "nowsecure-cloudflare", URL: "https://nowsecure.nl", Kind: "cloudflare-challenge-test"},
		{Name: "cloudflare-home", URL: "https://www.cloudflare.com/", Kind: "cloudflare-edge"},
		{Name: "sannysoft", URL: "https://bot.sannysoft.com/", Kind: "bot-fingerprint-test"},
		{Name: "pixelscan", URL: "https://pixelscan.net/", Kind: "bot-fingerprint-test"},
		{Name: "creepjs", URL: "https://abrahamjuliot.github.io/creepjs/", Kind: "fingerprint-test"},
		{Name: "incolumitas", URL: "https://bot.incolumitas.com/", Kind: "bot-fingerprint-test"},
		{Name: "datadome", URL: "https://datadome.co/", Kind: "bot-defense-vendor"},
		{Name: "g2", URL: "https://www.g2.com/", Kind: "real-site-anti-bot"},
	}
}

func runTarget(ctx context.Context, tgt target, attempt int, timeout time.Duration, waitUntil string, settle, loadStateTimeout time.Duration, contentMaxBytes int, sampleInterval time.Duration, headful, unsafeDirectNetwork, generatedPersona bool, sidecarRuntime, venvDir string, screenshots bool, shotDir string) (res targetResult) {
	start := time.Now()
	res = targetResult{Name: tgt.Name, URL: tgt.URL, Kind: tgt.Kind, Attempt: attempt, StartedAt: start}
	targetCtx, cancel := context.WithTimeout(ctx, timeout+settle+45*time.Second)
	defer cancel()

	browser, err := launchBrowser(targetCtx, headful, unsafeDirectNetwork, generatedPersona, sidecarRuntime, venvDir)
	if err != nil {
		res.Error = err.Error()
		res.Outcome = "failed"
		res.DurationMS = time.Since(start).Milliseconds()
		return res
	}
	defer func() { _ = browser.Close() }()

	return runTargetWithBrowserContext(targetCtx, browser, res, start, timeout, waitUntil, settle, loadStateTimeout, contentMaxBytes, sampleInterval, generatedPersona, screenshots, shotDir)
}

func runTargetWithBrowser(ctx context.Context, browser realpassBrowser, tgt target, attempt int, timeout time.Duration, waitUntil string, settle, loadStateTimeout time.Duration, contentMaxBytes int, sampleInterval time.Duration, generatedPersona, screenshots bool, shotDir string) targetResult {
	start := time.Now()
	res := targetResult{Name: tgt.Name, URL: tgt.URL, Kind: tgt.Kind, Attempt: attempt, StartedAt: start}
	targetCtx, cancel := context.WithTimeout(ctx, timeout+settle+45*time.Second)
	defer cancel()
	return runTargetWithBrowserContext(targetCtx, browser, res, start, timeout, waitUntil, settle, loadStateTimeout, contentMaxBytes, sampleInterval, generatedPersona, screenshots, shotDir)
}

func launchBrowser(ctx context.Context, headful, unsafeDirectNetwork, generatedPersona bool, sidecarRuntime, venvDir string) (realpassBrowser, error) {
	headlessMode := camoufoxcfg.HeadlessTrue
	if headful {
		headlessMode = camoufoxcfg.HeadlessFalse
	}
	runtime, err := parseSidecarRuntime(sidecarRuntime)
	if err != nil {
		return nil, err
	}
	opts := []gomoufox.Option{gomoufox.WithHeadless(headlessMode), gomoufox.WithSidecarRuntime(runtime)}
	if venvDir != "" {
		opts = append(opts, gomoufox.WithVenvDir(venvDir))
	}
	if unsafeDirectNetwork {
		opts = append(opts, gomoufox.WithUnsafeDirectNetwork(true))
	}
	if !generatedPersona {
		opts = append(opts,
			gomoufox.WithHumanize(1200*time.Millisecond),
			gomoufox.WithWindow(1365, 768),
			gomoufox.WithScreen(1365, 768),
			gomoufox.WithLocale("en-US"),
		)
	}
	return newRealpassBrowser(ctx, opts...)
}

func parseSidecarRuntime(raw string) (gomoufox.SidecarRuntime, error) {
	switch raw {
	case "", string(gomoufox.SidecarRuntimePython):
		return gomoufox.SidecarRuntimePython, nil
	case string(gomoufox.SidecarRuntimeNodeDirect):
		return gomoufox.SidecarRuntimeNodeDirect, nil
	default:
		return "", fmt.Errorf("sidecar-runtime must be %s or %s", gomoufox.SidecarRuntimePython, gomoufox.SidecarRuntimeNodeDirect)
	}
}

func runTargetWithBrowserContext(targetCtx context.Context, browser realpassBrowser, res targetResult, start time.Time, timeout time.Duration, waitUntil string, settle, loadStateTimeout time.Duration, contentMaxBytes int, sampleInterval time.Duration, generatedPersona, screenshots bool, shotDir string) (out targetResult) {
	out = res
	monitor := newProcessMonitor(browser.Sidecar().PID, sampleInterval)
	monitor.start(targetCtx)
	defer func() {
		monitor.stopAndWait()
		out.Resources = monitor.snapshot()
	}()

	contextOpts := []gomoufox.ContextOption{}
	if !generatedPersona {
		contextOpts = append(contextOpts, gomoufox.WithViewport(1365, 768), gomoufox.WithContextLocale("en-US"))
	}
	page, err := browser.NewPage(targetCtx, contextOpts...)
	if err != nil {
		out.Error = err.Error()
		out.Outcome = "failed"
		out.DurationMS = time.Since(start).Milliseconds()
		return out
	}
	defer func() { _ = page.Close() }()

	resp, err := page.Goto(targetCtx, out.URL, gomoufox.WaitUntil(waitUntil), gomoufox.WithTimeout(timeout))
	var navigationErr error
	if err != nil {
		navigationErr = err
		out.NavigationError = err.Error()
		if !errors.Is(err, gomoufox.ErrNavigationTimeout) {
			out.Error = err.Error()
			out.Outcome = "failed"
		}
	} else if resp != nil {
		out.Status = resp.Status()
		out.StatusText = resp.StatusText()
		out.Headers = selectedHeaders(resp.Headers())
	}
	waitFor(targetCtx, settle)
	if loadStateTimeout > 0 {
		loadCtx, loadCancel := context.WithTimeout(targetCtx, loadStateTimeout)
		_ = page.WaitForLoadState(loadCtx, "load")
		loadCancel()
	}

	out.FinalURL = page.URL()
	content := ""
	title, html, contentBytes, detector, captureErr := capturePageData(targetCtx, page, contentMaxBytes)
	if captureErr == nil {
		out.Title = title
		content = html
		out.ContentBytes = contentBytes
		out.Detector = detector
	} else {
		if title, err := page.Title(targetCtx); err == nil {
			out.Title = title
		}
		if html, contentBytes, err := pageContentForClassification(targetCtx, page, contentMaxBytes); err == nil {
			content = html
			out.ContentBytes = contentBytes
		} else if out.Error == "" {
			out.Error = "content: " + err.Error()
		}
		out.Detector = detectorSnapshot(targetCtx, page)
	}
	if navigationErr != nil && out.Error == "" && !usablePageAfterNavigationTimeout(out.FinalURL, content) {
		out.Error = navigationErr.Error()
	}
	out.Signals = classifySignals(out.Status, out.Title, out.FinalURL, content)
	if screenshots {
		shotPath := filepath.Join(shotDir, fmt.Sprintf("%02d-%s.png", out.Attempt, slug(out.Name)))
		if err := page.ScreenshotToFile(targetCtx, shotPath); err == nil {
			out.ScreenshotPath = shotPath
			if st, statErr := os.Stat(shotPath); statErr == nil {
				out.ScreenshotBytes = int(st.Size())
			}
		} else if out.Error == "" {
			out.Error = "screenshot: " + err.Error()
		}
	}
	if out.Error != "" {
		out.Outcome = "failed"
	} else if hasBlockingSignals(out.Signals) {
		out.Outcome = "blocked"
	} else {
		out.Outcome = "passed"
	}
	out.DurationMS = time.Since(start).Milliseconds()
	return out
}

const fingerprintDetectorExpression = `() => {
const canvas = document.createElement("canvas");
canvas.width = 240;
canvas.height = 60;
const ctx = canvas.getContext("2d");
if (ctx) {
  ctx.textBaseline = "top";
  ctx.font = "16px Arial";
  ctx.fillStyle = "#f60";
  ctx.fillRect(0, 0, 120, 40);
  ctx.fillStyle = "#069";
  ctx.fillText("gomoufox fingerprint audit", 4, 8);
}
const glCanvas = document.createElement("canvas");
const gl = glCanvas.getContext("webgl") || glCanvas.getContext("experimental-webgl");
let webgl = {supported: false};
if (gl) {
  const debug = gl.getExtension("WEBGL_debug_renderer_info");
  webgl = {
    supported: true,
    vendor: gl.getParameter(gl.VENDOR),
    renderer: gl.getParameter(gl.RENDERER),
    unmaskedVendor: debug ? gl.getParameter(debug.UNMASKED_VENDOR_WEBGL) : null,
    unmaskedRenderer: debug ? gl.getParameter(debug.UNMASKED_RENDERER_WEBGL) : null
  };
}
const fontNames = ["Arial", "Times New Roman", "Courier New", "Helvetica", "Segoe UI", "Noto Sans"];
const fonts = {};
if (document.fonts && document.fonts.check) {
  for (const name of fontNames) {
    fonts[name] = document.fonts.check("12px \"" + name + "\"");
  }
}
const dataURL = canvas.toDataURL();
return {
  webdriver: navigator.webdriver,
  userAgent: navigator.userAgent,
  appVersion: navigator.appVersion,
  platform: navigator.platform,
  vendor: navigator.vendor,
  productSub: navigator.productSub,
  languages: navigator.languages,
  hardwareConcurrency: navigator.hardwareConcurrency,
  deviceMemory: navigator.deviceMemory || null,
  maxTouchPoints: navigator.maxTouchPoints,
  cookieEnabled: navigator.cookieEnabled,
  doNotTrack: navigator.doNotTrack || null,
  pdfViewerEnabled: navigator.pdfViewerEnabled,
  plugins: navigator.plugins ? navigator.plugins.length : null,
  outerWidth: window.outerWidth,
  outerHeight: window.outerHeight,
  innerWidth: window.innerWidth,
  innerHeight: window.innerHeight,
  screenWidth: screen.width,
  screenHeight: screen.height,
  screenAvailWidth: screen.availWidth,
  screenAvailHeight: screen.availHeight,
  colorDepth: screen.colorDepth,
  pixelDepth: screen.pixelDepth,
  devicePixelRatio: window.devicePixelRatio,
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  webgl,
  webrtc: {supported: typeof RTCPeerConnection !== "undefined"},
  fonts,
  canvas: {dataURLPrefix: dataURL.slice(0, 96), dataURLLength: dataURL.length}
};
}`

func capturePageData(ctx context.Context, page realpassPage, maxBytes int) (string, string, int, map[string]any, error) {
	value, err := page.Evaluate(ctx, `max => {
const html = document.documentElement ? document.documentElement.outerHTML : "";
const limit = max > 0 ? max : html.length;
return {
  title: document.title || "",
  content: html.slice(0, limit),
  content_bytes: html.length,
  detector: (`+fingerprintDetectorExpression+`)()
};
}`, maxBytes)
	if err != nil {
		return "", "", 0, nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", "", 0, nil, err
	}
	var out struct {
		Title        string         `json:"title"`
		Content      string         `json:"content"`
		ContentBytes int            `json:"content_bytes"`
		Detector     map[string]any `json:"detector"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", "", 0, nil, err
	}
	if out.Content == "" && out.ContentBytes == 0 && len(out.Detector) == 0 {
		return "", "", 0, nil, errors.New("page capture returned empty payload")
	}
	return out.Title, out.Content, out.ContentBytes, out.Detector, nil
}

func pageContentForClassification(ctx context.Context, page realpassPage, maxBytes int) (string, int, error) {
	if maxBytes <= 0 {
		html, err := page.Content(ctx)
		if err != nil {
			return "", 0, err
		}
		return html, len(html), nil
	}
	value, err := page.Evaluate(ctx, `max => {
const html = document.documentElement ? document.documentElement.outerHTML : "";
return {content: html.slice(0, max), bytes: html.length};
}`, maxBytes)
	if err != nil {
		html, contentErr := page.Content(ctx)
		if contentErr != nil {
			return "", 0, err
		}
		return firstN(html, maxBytes), len(html), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", 0, err
	}
	var out struct {
		Content string `json:"content"`
		Bytes   int    `json:"bytes"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, err
	}
	return out.Content, out.Bytes, nil
}

func usablePageAfterNavigationTimeout(finalURL, content string) bool {
	finalURL = strings.TrimSpace(finalURL)
	return finalURL != "" && finalURL != "about:blank" && strings.TrimSpace(content) != ""
}

func validateWaitUntil(state string) error {
	switch state {
	case "commit", "domcontentloaded", "load", "networkidle":
		return nil
	default:
		return errors.New("wait-until must be commit, domcontentloaded, load, or networkidle")
	}
}

func parseReportStyle(value string) (reportStyle, error) {
	switch reportStyle(strings.TrimSpace(value)) {
	case reportStyleCompact:
		return reportStyleCompact, nil
	case reportStyleFull:
		return reportStyleFull, nil
	default:
		return "", errors.New("report-style must be full or compact")
	}
}

func detectorSnapshot(ctx context.Context, page realpassPage) map[string]any {
	value, err := page.Evaluate(ctx, fingerprintDetectorExpression)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{"error": err.Error()}
	}
	return out
}

func classifySignals(status int, title, finalURL, content string) []string {
	var signals []string
	add := func(signal string) {
		for _, existing := range signals {
			if existing == signal {
				return
			}
		}
		signals = append(signals, signal)
	}
	if status == 403 || status == 429 || status == 503 {
		add(fmt.Sprintf("http_%d", status))
	}
	strongText := strings.ToLower(title + "\n" + finalURL + "\n" + firstN(content, 250000))
	genericText := strings.ToLower(title + "\n" + finalURL + "\n" + firstN(content, 4096))
	checks := []struct {
		needle string
		signal string
	}{
		{"just a moment", "cloudflare_challenge"},
		{"checking your browser", "browser_challenge"},
		{"cf-chl", "cloudflare_challenge"},
		{"cdn-cgi/challenge-platform", "cloudflare_challenge"},
		{"turnstile", "cloudflare_turnstile"},
		{"verify you are human", "human_verification"},
		{"confirm you are human", "human_verification"},
		{"prove you are human", "human_verification"},
		{"captcha", "captcha"},
		{"g-recaptcha", "captcha"},
		{"h-captcha", "captcha"},
		{"robot", "robot_detection"},
		{"bot detection", "bot_detection"},
		{"access denied", "access_denied"},
		{"request blocked", "request_blocked"},
		{"datadome", "datadome"},
		{"akamai", "akamai"},
		{"perimeterx", "perimeterx"},
		{"px-captcha", "perimeterx"},
		{"incapsula", "imperva_incapsula"},
		{"distil", "distil"},
		{"unusual traffic", "unusual_traffic"},
	}
	for _, check := range checks {
		if strings.Contains(strongText, check.needle) {
			add(check.signal)
		}
	}
	for _, needle := range []string{"403 forbidden", "access forbidden", "forbidden access"} {
		if strings.Contains(genericText, needle) {
			add("forbidden")
			break
		}
	}
	return signals
}

func hasBlockingSignals(signals []string) bool {
	for _, signal := range signals {
		switch signal {
		case "cloudflare_challenge", "browser_challenge", "human_verification", "access_denied", "request_blocked", "forbidden", "unusual_traffic", "http_403", "http_429", "http_503":
			return true
		}
	}
	return false
}

func selectedHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, key := range []string{"server", "cf-ray", "cf-cache-status", "x-datadome", "x-akamai", "x-cdn", "content-type"} {
		for gotKey, value := range headers {
			if strings.EqualFold(gotKey, key) {
				out[gotKey] = value
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func summarize(results []targetResult) reportSummary {
	var summary reportSummary
	summary.Total = len(results)
	for _, result := range results {
		switch result.Outcome {
		case "passed":
			summary.Passed++
		case "blocked":
			summary.Blocked++
		default:
			summary.Failed++
		}
		if result.Resources.PeakRSSMiB > summary.PeakRSSMiB {
			summary.PeakRSSMiB = result.Resources.PeakRSSMiB
		}
		if result.Resources.MaxCPUPercent > summary.PeakCPUPercent {
			summary.PeakCPUPercent = result.Resources.MaxCPUPercent
		}
	}
	return summary
}

func gateOptionsFromRaw(expectPassed, maxBlocked, maxFailed int, maxRSSMiB, maxCPUPercent float64) (gateOptions, error) {
	if expectPassed < 0 {
		return gateOptions{}, errors.New("expect-passed must be >= 0")
	}
	if maxBlocked < -1 {
		return gateOptions{}, errors.New("max-blocked must be >= -1")
	}
	if maxFailed < -1 {
		return gateOptions{}, errors.New("max-failed must be >= -1")
	}
	if maxRSSMiB < 0 {
		return gateOptions{}, errors.New("max-rss-mib must be >= 0")
	}
	if maxCPUPercent < 0 {
		return gateOptions{}, errors.New("max-cpu-percent must be >= 0")
	}
	opts := gateOptions{ExpectPassed: expectPassed}
	if maxBlocked >= 0 {
		opts.MaxBlocked = &maxBlocked
	}
	if maxFailed >= 0 {
		opts.MaxFailed = &maxFailed
	}
	if maxRSSMiB > 0 {
		opts.MaxRSSMiB = &maxRSSMiB
	}
	if maxCPUPercent > 0 {
		opts.MaxCPUPercent = &maxCPUPercent
	}
	return opts, nil
}

func evaluateReportGate(rep report, opts gateOptions) []string {
	if rep.Summary.Total == 0 && len(rep.Results) > 0 {
		rep.Summary = summarize(rep.Results)
	}
	var failures []string
	if opts.ExpectPassed > 0 && rep.Summary.Passed < opts.ExpectPassed {
		failures = append(failures, fmt.Sprintf("passed %d < expected %d", rep.Summary.Passed, opts.ExpectPassed))
	}
	if opts.MaxFailed != nil && rep.Summary.Failed > *opts.MaxFailed {
		failures = append(failures, fmt.Sprintf("failed %d > max %d", rep.Summary.Failed, *opts.MaxFailed))
	}
	if opts.MaxBlocked != nil && rep.Summary.Blocked > *opts.MaxBlocked {
		failures = append(failures, fmt.Sprintf("blocked %d > max %d", rep.Summary.Blocked, *opts.MaxBlocked))
	}
	if opts.MaxRSSMiB != nil && rep.Summary.PeakRSSMiB > *opts.MaxRSSMiB {
		failures = append(failures, fmt.Sprintf("peak RSS %.1f MiB > max %.1f MiB", rep.Summary.PeakRSSMiB, *opts.MaxRSSMiB))
	}
	if opts.MaxCPUPercent != nil && rep.Summary.PeakCPUPercent > *opts.MaxCPUPercent {
		failures = append(failures, fmt.Sprintf("peak CPU %.1f%% > max %.1f%%", rep.Summary.PeakCPUPercent, *opts.MaxCPUPercent))
	}
	return failures
}

func runCompare(ctx context.Context, args []string, stdout, stderr *os.File) int {
	_ = ctx
	var goPath, pythonPath string
	var allowedBlocked nameFlags
	var allowedFailed nameFlags
	var requiredTargets nameFlags
	requireOutcomeMatch := false
	expectPassed := 0
	maxBlockedRaw := -1
	maxFailedRaw := -1
	maxRSSRaw := 0.0
	maxCPURaw := 0.0
	maxGoRSSRatio := 0.0
	maxGoCPURatio := 0.0
	maxGoWallRatio := 0.0
	maxGoTargetDurationRatio := 0.0
	maxGoReportTokenRatio := 0.0

	flagOutput := policy.NewRedactWriter(stderr)
	defer func() { _ = flagOutput.Flush() }()
	fs := flag.NewFlagSet("gomoufox-realpass compare", flag.ContinueOnError)
	fs.SetOutput(flagOutput)
	fs.StringVar(&goPath, "go", goPath, "gomoufox report.json")
	fs.StringVar(&pythonPath, "python", pythonPath, "Python Camoufox report.json")
	fs.Var(&allowedBlocked, "allow-blocked-target", "target name allowed to be blocked in both Go and Python reports; may be repeated")
	fs.Var(&allowedFailed, "allow-failed-target", "target name allowed to fail in both Go and Python reports; may be repeated")
	fs.Var(&requiredTargets, "require-target", "target name that must be present in both reports; may be repeated")
	fs.BoolVar(&requireOutcomeMatch, "require-outcome-match", requireOutcomeMatch, "fail when per-target outcomes differ")
	fs.IntVar(&expectPassed, "expect-passed", expectPassed, "fail if passed count is lower than this value; 0 disables")
	fs.IntVar(&maxBlockedRaw, "max-blocked", maxBlockedRaw, "fail if blocked count exceeds this value; -1 disables")
	fs.IntVar(&maxFailedRaw, "max-failed", maxFailedRaw, "fail if failed count exceeds this value; -1 disables")
	fs.Float64Var(&maxRSSRaw, "max-rss-mib", maxRSSRaw, "fail if peak RSS MiB exceeds this value; -1 disables")
	fs.Float64Var(&maxCPURaw, "max-cpu-percent", maxCPURaw, "fail if peak CPU percent exceeds this value; -1 disables")
	fs.Float64Var(&maxGoRSSRatio, "max-go-rss-ratio", maxGoRSSRatio, "fail if Go peak RSS is greater than Python peak RSS times this ratio; 0 disables")
	fs.Float64Var(&maxGoCPURatio, "max-go-cpu-ratio", maxGoCPURatio, "fail if Go peak CPU is greater than Python peak CPU times this ratio; 0 disables")
	fs.Float64Var(&maxGoWallRatio, "max-go-wall-ratio", maxGoWallRatio, "fail if Go report wall time is greater than Python report wall time times this ratio; 0 disables")
	fs.Float64Var(&maxGoTargetDurationRatio, "max-go-target-duration-ratio", maxGoTargetDurationRatio, "fail if Go target duration is greater than Python target duration times this ratio; 0 disables")
	fs.Float64Var(&maxGoReportTokenRatio, "max-go-report-token-ratio", maxGoReportTokenRatio, "fail if Go report artifact tokens are greater than Python report artifact tokens times this ratio; 0 disables")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if goPath == "" || pythonPath == "" {
		_, _ = fmt.Fprintln(stderr, "--go and --python are required")
		return 2
	}
	goReport, err := readReport(goPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "read go report: %s\n", policy.Redact(err.Error()))
		return 1
	}
	pythonReport, err := readReport(pythonPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "read python report: %s\n", policy.Redact(err.Error()))
		return 1
	}
	opts, err := gateOptionsFromRaw(expectPassed, maxBlockedRaw, maxFailedRaw, maxRSSRaw, maxCPURaw)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
		return 2
	}
	if err := validateRatio("max-go-rss-ratio", maxGoRSSRatio); err != nil {
		_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
		return 2
	}
	if err := validateRatio("max-go-cpu-ratio", maxGoCPURatio); err != nil {
		_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
		return 2
	}
	if err := validateRatio("max-go-wall-ratio", maxGoWallRatio); err != nil {
		_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
		return 2
	}
	if err := validateRatio("max-go-target-duration-ratio", maxGoTargetDurationRatio); err != nil {
		_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
		return 2
	}
	if err := validateRatio("max-go-report-token-ratio", maxGoReportTokenRatio); err != nil {
		_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
		return 2
	}
	var failures []string
	if requireOutcomeMatch {
		failures = append(failures, outcomeDiffs(goReport.Results, pythonReport.Results)...)
	}
	allowedBlockedSet := nameSet(allowedBlocked)
	allowedFailedSet := nameSet(allowedFailed)
	requiredTargetSet := nameSet(requiredTargets)
	failures = append(failures, requiredTargetFailures(goReport.Results, pythonReport.Results, requiredTargetSet)...)
	failures = append(failures, blockedTargetFailures("go", goReport.Results, allowedBlockedSet)...)
	failures = append(failures, blockedTargetFailures("python", pythonReport.Results, allowedBlockedSet)...)
	failures = append(failures, failedTargetFailures("go", goReport.Results, allowedFailedSet)...)
	failures = append(failures, failedTargetFailures("python", pythonReport.Results, allowedFailedSet)...)
	failures = append(failures, reportOptionDiffs(goReport.Options, pythonReport.Options)...)
	failures = append(failures, resourceRatioFailures(goReport, pythonReport, maxGoRSSRatio, maxGoCPURatio)...)
	failures = append(failures, runtimeRatioFailures(goReport, pythonReport, goPath, pythonPath, maxGoWallRatio, maxGoTargetDurationRatio, maxGoReportTokenRatio)...)
	goGateReport := reportForGate(goReport, allowedBlockedSet, allowedFailedSet)
	pythonGateReport := reportForGate(pythonReport, allowedBlockedSet, allowedFailedSet)
	for _, failure := range evaluateReportGate(goGateReport, opts) {
		failures = append(failures, "go: "+failure)
	}
	for _, failure := range evaluateReportGate(pythonGateReport, opts) {
		failures = append(failures, "python: "+failure)
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		for _, failure := range failures {
			_, _ = fmt.Fprintln(stderr, policy.Redact(failure))
		}
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "compare: ok")
	return 0
}

func validateRatio(name string, value float64) error {
	if value == 0 {
		return nil
	}
	if value < 0 {
		return fmt.Errorf("%s must be > 0 or 0 to disable", name)
	}
	return nil
}

func outcomeDiffs(goResults, pythonResults []targetResult) []string {
	goOutcomes := outcomesByName(goResults)
	pythonOutcomes := outcomesByName(pythonResults)
	var diffs []string
	for name, goOutcome := range goOutcomes {
		pythonOutcome, ok := pythonOutcomes[name]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("outcome mismatch %s: go=%s python=<missing>", name, goOutcome))
			continue
		}
		if pythonOutcome != goOutcome {
			diffs = append(diffs, fmt.Sprintf("outcome mismatch %s: go=%s python=%s", name, goOutcome, pythonOutcome))
		}
	}
	for name, pythonOutcome := range pythonOutcomes {
		if _, ok := goOutcomes[name]; !ok {
			diffs = append(diffs, fmt.Sprintf("outcome mismatch %s: go=<missing> python=%s", name, pythonOutcome))
		}
	}
	sort.Strings(diffs)
	return diffs
}

func outcomesByName(results []targetResult) map[string]string {
	out := make(map[string]string, len(results))
	for _, result := range results {
		out[result.Name] = result.Outcome
	}
	return out
}

func requiredTargetFailures(goResults, pythonResults []targetResult, required map[string]bool) []string {
	if len(required) == 0 {
		return nil
	}
	goOutcomes := outcomesByName(goResults)
	pythonOutcomes := outcomesByName(pythonResults)
	var failures []string
	for name := range required {
		if _, ok := goOutcomes[name]; !ok {
			failures = append(failures, fmt.Sprintf("go: required target %s is missing", name))
		}
		if _, ok := pythonOutcomes[name]; !ok {
			failures = append(failures, fmt.Sprintf("python: required target %s is missing", name))
		}
	}
	for name := range goOutcomes {
		if !required[name] {
			failures = append(failures, fmt.Sprintf("go: unexpected target %s is not in --require-target", name))
		}
	}
	for name := range pythonOutcomes {
		if !required[name] {
			failures = append(failures, fmt.Sprintf("python: unexpected target %s is not in --require-target", name))
		}
	}
	sort.Strings(failures)
	return failures
}

func blockedTargetFailures(label string, results []targetResult, allowed map[string]bool) []string {
	if len(allowed) == 0 {
		return nil
	}
	var failures []string
	for _, result := range results {
		if result.Outcome == "blocked" && !allowed[result.Name] {
			failures = append(failures, fmt.Sprintf("%s: blocked target %s is not in --allow-blocked-target", label, result.Name))
		}
	}
	sort.Strings(failures)
	return failures
}

func failedTargetFailures(label string, results []targetResult, allowed map[string]bool) []string {
	if len(allowed) == 0 {
		return nil
	}
	var failures []string
	for _, result := range results {
		if result.Outcome == "failed" && !allowed[result.Name] {
			failures = append(failures, fmt.Sprintf("%s: failed target %s is not in --allow-failed-target", label, result.Name))
		}
	}
	sort.Strings(failures)
	return failures
}

func reportOptionDiffs(goOptions, pythonOptions reportOptions) []string {
	var failures []string
	addStringDiff := func(name, goValue, pythonValue string) {
		if goValue != pythonValue {
			failures = append(failures, fmt.Sprintf("runtime option mismatch %s: go=%s python=%s", name, emptyOption(goValue), emptyOption(pythonValue)))
		}
	}
	addDurationDiff := func(name, goValue, pythonValue string) {
		if goValue == "" && pythonValue == "" {
			return
		}
		goDuration, goErr := time.ParseDuration(goValue)
		pythonDuration, pythonErr := time.ParseDuration(pythonValue)
		if goErr == nil && pythonErr == nil {
			if goDuration != pythonDuration {
				failures = append(failures, fmt.Sprintf("runtime option mismatch %s: go=%s python=%s", name, emptyOption(goValue), emptyOption(pythonValue)))
			}
			return
		}
		addStringDiff(name, goValue, pythonValue)
	}
	addBoolDiff := func(name string, goValue, pythonValue bool) {
		if goValue != pythonValue {
			failures = append(failures, fmt.Sprintf("runtime option mismatch %s: go=%t python=%t", name, goValue, pythonValue))
		}
	}
	addDurationDiff("timeout", goOptions.Timeout, pythonOptions.Timeout)
	addStringDiff("wait_until", goOptions.WaitUntil, pythonOptions.WaitUntil)
	addDurationDiff("settle", goOptions.Settle, pythonOptions.Settle)
	addDurationDiff("load_state_timeout", goOptions.LoadStateTimeout, pythonOptions.LoadStateTimeout)
	if goOptions.ContentMaxBytes != pythonOptions.ContentMaxBytes {
		failures = append(failures, fmt.Sprintf("runtime option mismatch content_max_bytes: go=%d python=%d", goOptions.ContentMaxBytes, pythonOptions.ContentMaxBytes))
	}
	addDurationDiff("sample_interval", goOptions.SampleInterval, pythonOptions.SampleInterval)
	addBoolDiff("headful", goOptions.Headful, pythonOptions.Headful)
	addBoolDiff("screenshots", goOptions.Screenshots, pythonOptions.Screenshots)
	addBoolDiff("reuse_browser", goOptions.ReuseBrowser, pythonOptions.ReuseBrowser)
	addBoolDiff("unsafe_direct_network", goOptions.UnsafeDirect, pythonOptions.UnsafeDirect)
	addBoolDiff("generated_persona", goOptions.GeneratedPersona, pythonOptions.GeneratedPersona)
	return failures
}

func emptyOption(value string) string {
	if value == "" {
		return "<unset>"
	}
	return value
}

func reportForGate(rep report, allowedBlocked, allowedFailed map[string]bool) report {
	if len(rep.Results) == 0 || len(allowedBlocked) == 0 && len(allowedFailed) == 0 {
		return rep
	}
	filtered := rep
	filtered.Results = make([]targetResult, 0, len(rep.Results))
	for _, result := range rep.Results {
		if result.Outcome == "blocked" && allowedBlocked[result.Name] {
			continue
		}
		if result.Outcome == "failed" && allowedFailed[result.Name] {
			continue
		}
		filtered.Results = append(filtered.Results, result)
	}
	filtered.Summary = summarize(filtered.Results)
	return filtered
}

func resourceRatioFailures(goReport, pythonReport report, maxRSSRatio, maxCPURatio float64) []string {
	goSummary := goReport.Summary
	if goSummary.Total == 0 && len(goReport.Results) > 0 {
		goSummary = summarize(goReport.Results)
	}
	pythonSummary := pythonReport.Summary
	if pythonSummary.Total == 0 && len(pythonReport.Results) > 0 {
		pythonSummary = summarize(pythonReport.Results)
	}
	var failures []string
	if maxRSSRatio > 0 {
		if pythonSummary.PeakRSSMiB <= 0 {
			if goSummary.PeakRSSMiB > 0 {
				failures = append(failures, fmt.Sprintf("go: peak RSS %.1f MiB cannot be compared because python peak RSS is %.1f MiB", goSummary.PeakRSSMiB, pythonSummary.PeakRSSMiB))
			}
		} else if goSummary.PeakRSSMiB > pythonSummary.PeakRSSMiB*maxRSSRatio {
			failures = append(failures, fmt.Sprintf("go: peak RSS %.1f MiB > python %.1f MiB * %.2f", goSummary.PeakRSSMiB, pythonSummary.PeakRSSMiB, maxRSSRatio))
		}
	}
	if maxCPURatio > 0 {
		if pythonSummary.PeakCPUPercent <= 0 {
			if goSummary.PeakCPUPercent > 0 {
				failures = append(failures, fmt.Sprintf("go: peak CPU %.1f%% cannot be compared because python peak CPU is %.1f%%", goSummary.PeakCPUPercent, pythonSummary.PeakCPUPercent))
			}
		} else if goSummary.PeakCPUPercent > pythonSummary.PeakCPUPercent*maxCPURatio {
			failures = append(failures, fmt.Sprintf("go: peak CPU %.1f%% > python %.1f%% * %.2f", goSummary.PeakCPUPercent, pythonSummary.PeakCPUPercent, maxCPURatio))
		}
	}
	return failures
}

func runtimeRatioFailures(goReport, pythonReport report, goPath, pythonPath string, maxWallRatio, maxTargetDurationRatio, maxReportTokenRatio float64) []string {
	goRuntime := runtimeMetricsForCompare(goReport, goPath)
	pythonRuntime := runtimeMetricsForCompare(pythonReport, pythonPath)
	var failures []string
	failures = appendRatioFailure(failures, "wall time", "ms", goRuntime.wallMS, pythonRuntime.wallMS, maxWallRatio)
	failures = appendRatioFailure(failures, "target duration", "ms", goRuntime.targetDurationMS, pythonRuntime.targetDurationMS, maxTargetDurationRatio)
	failures = appendRatioFailure(failures, "report tokens", "tokens", goRuntime.reportTokens, pythonRuntime.reportTokens, maxReportTokenRatio)
	return failures
}

type compareRuntimeMetrics struct {
	wallMS           float64
	targetDurationMS float64
	reportTokens     float64
}

func runtimeMetricsForCompare(rep report, reportPath string) compareRuntimeMetrics {
	targetDurationMS := 0.0
	for _, result := range rep.Results {
		targetDurationMS += float64(result.DurationMS)
	}
	wallMS := targetDurationMS
	if !rep.StartedAt.IsZero() && !rep.FinishedAt.IsZero() && rep.FinishedAt.After(rep.StartedAt) {
		wallMS = float64(rep.FinishedAt.Sub(rep.StartedAt).Milliseconds())
	}
	return compareRuntimeMetrics{
		wallMS:           wallMS,
		targetDurationMS: targetDurationMS,
		reportTokens:     float64(reportArtifactTokens(reportPath)),
	}
}

func reportArtifactTokens(reportPath string) int64 {
	if reportPath == "" {
		return 0
	}
	jsonBytes := fileSizeOrZero(reportPath)
	mdBytes := fileSizeOrZero(strings.TrimSuffix(reportPath, filepath.Ext(reportPath)) + ".md")
	return estimateTokens(jsonBytes + mdBytes)
}

func fileSizeOrZero(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func estimateTokens(byteCount int64) int64 {
	if byteCount <= 0 {
		return 0
	}
	return (byteCount + 3) / 4
}

func appendRatioFailure(failures []string, label, unit string, goValue, pythonValue, maxRatio float64) []string {
	if maxRatio <= 0 {
		return failures
	}
	if pythonValue <= 0 {
		if goValue > 0 {
			return append(failures, fmt.Sprintf("go: %s %.0f %s cannot be compared because python %s is %.0f %s", label, goValue, unit, label, pythonValue, unit))
		}
		return failures
	}
	if goValue > pythonValue*maxRatio {
		return append(failures, fmt.Sprintf("go: %s %.0f %s > python %.0f %s * %.2f", label, goValue, unit, pythonValue, unit, maxRatio))
	}
	return failures
}

func nameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

func thresholdString(value float64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func readReport(path string) (report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return report{}, err
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return report{}, err
	}
	return rep, nil
}

func writeReports(outDir string, rep report) error {
	if rep.FinishedAt.IsZero() {
		rep.FinishedAt = time.Now()
	}
	rep.Summary = summarize(rep.Results)
	rep = redactReport(rep)
	style := reportStyle(rep.Options.ReportStyle)
	if style == "" {
		style = reportStyleFull
	}
	if _, err := parseReportStyle(string(style)); err != nil {
		return err
	}
	jsonPayload := any(rep)
	if style == reportStyleCompact {
		jsonPayload = compactReportFrom(rep)
		if rep.Options.DebugReport {
			fullData, err := json.MarshalIndent(rep, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(outDir, "report-full.json"), append(fullData, '\n'), 0o600); err != nil {
				return err
			}
		}
	}
	data, err := json.MarshalIndent(jsonPayload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "report.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "report.md"), []byte(markdownReportWithStyle(rep, style)), 0o600)
}

func compactReportFrom(rep report) compactReport {
	out := compactReport{
		StartedAt:  rep.StartedAt,
		FinishedAt: rep.FinishedAt,
		GoOS:       rep.GoOS,
		GoArch:     rep.GoArch,
		Options:    rep.Options,
		Summary:    rep.Summary,
		Results:    make([]compactTargetResult, 0, len(rep.Results)),
	}
	for _, result := range rep.Results {
		out.Results = append(out.Results, compactTargetResult{
			Name:            result.Name,
			Kind:            result.Kind,
			Attempt:         result.Attempt,
			Outcome:         result.Outcome,
			Error:           result.Error,
			NavigationError: result.NavigationError,
			StartedAt:       result.StartedAt,
			DurationMS:      result.DurationMS,
			Status:          result.Status,
			Signals:         result.Signals,
			Resources: compactResourceSummary{
				Samples:       result.Resources.Samples,
				PeakRSSMiB:    result.Resources.PeakRSSMiB,
				MaxCPUPercent: result.Resources.MaxCPUPercent,
				PeakProcesses: result.Resources.PeakProcesses,
			},
		})
	}
	return out
}

func appendResultJSONL(outDir string, result targetResult) error {
	data, err := json.Marshal(redactTargetResult(result))
	if err != nil {
		return err
	}
	path := filepath.Join(outDir, "results.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.Write(append(data, '\n'))
	return err
}

func redactReport(rep report) report {
	for i := range rep.Results {
		rep.Results[i] = redactTargetResult(rep.Results[i])
	}
	return rep
}

func redactTargetResult(result targetResult) targetResult {
	result.Name = policy.Redact(result.Name)
	result.URL = policy.Redact(result.URL)
	result.Kind = policy.Redact(result.Kind)
	result.Outcome = policy.Redact(result.Outcome)
	result.Error = policy.Redact(result.Error)
	result.NavigationError = policy.Redact(result.NavigationError)
	result.StatusText = policy.Redact(result.StatusText)
	result.FinalURL = policy.Redact(result.FinalURL)
	result.Title = policy.Redact(result.Title)
	result.ScreenshotPath = policy.Redact(result.ScreenshotPath)
	result.Signals = redactStringSlice(result.Signals)
	result.Detector = redactAnyMap(result.Detector)
	result.Resources = redactResourceSummary(result.Resources)
	result.Headers = redactStringMap(result.Headers)
	return result
}

func redactResourceSummary(summary resourceSummary) resourceSummary {
	summary.ProcessCommands = redactStringSlice(summary.ProcessCommands)
	summary.SampleErrors = redactStringSlice(summary.SampleErrors)
	return summary
}

func redactStringSlice(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = policy.Redact(value)
	}
	return out
}

func redactStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		value = policy.Redact(value)
		if sensitiveReportHeader(key) {
			value = "<redacted>"
		}
		out[policy.Redact(key)] = value
	}
	return out
}

func sensitiveReportHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	default:
		return false
	}
}

func redactAnyMap(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[policy.Redact(key)] = redactAny(value)
	}
	return out
}

func redactAny(value any) any {
	switch typed := value.(type) {
	case string:
		return policy.Redact(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactAny(item)
		}
		return out
	case map[string]any:
		return redactAnyMap(typed)
	case map[string]string:
		return redactStringMap(typed)
	default:
		return value
	}
}

func markdownReport(rep report) string {
	return markdownReportWithStyle(rep, reportStyleFull)
}

func markdownReportWithStyle(rep report, style reportStyle) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# gomoufox Real-Site Pass\n\n")
	fmt.Fprintf(&b, "- Started: %s\n", rep.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Finished: %s\n", rep.FinishedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Platform: %s/%s\n", rep.GoOS, rep.GoArch)
	fmt.Fprintf(&b, "- Summary: %d passed, %d blocked, %d failed, peak RSS %.1f MiB, peak CPU %.1f%%\n\n", rep.Summary.Passed, rep.Summary.Blocked, rep.Summary.Failed, rep.Summary.PeakRSSMiB, rep.Summary.PeakCPUPercent)
	fmt.Fprintf(&b, "Signals are observed markers; `blocked` requires strong challenge, denial, or HTTP error evidence.\n\n")
	fmt.Fprintf(&b, "| Target | Kind | Outcome | Status | Signals | Peak RSS MiB | Max CPU %% | Duration ms |\n")
	fmt.Fprintf(&b, "|---|---|---:|---:|---|---:|---:|---:|\n")
	for _, result := range rep.Results {
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %s | %.1f | %.1f | %d |\n",
			escapeMD(result.Name),
			escapeMD(result.Kind),
			result.Outcome,
			result.Status,
			escapeMD(strings.Join(result.Signals, ",")),
			result.Resources.PeakRSSMiB,
			result.Resources.MaxCPUPercent,
			result.DurationMS,
		)
	}
	if style == reportStyleCompact {
		return b.String()
	}
	fmt.Fprintf(&b, "\n## Details\n\n")
	for _, result := range rep.Results {
		fmt.Fprintf(&b, "### %s attempt %d\n\n", result.Name, result.Attempt)
		fmt.Fprintf(&b, "- URL: `%s`\n", result.URL)
		fmt.Fprintf(&b, "- Final URL: `%s`\n", result.FinalURL)
		fmt.Fprintf(&b, "- Title: %s\n", escapeMD(result.Title))
		fmt.Fprintf(&b, "- Outcome: `%s`\n", result.Outcome)
		if result.Error != "" {
			fmt.Fprintf(&b, "- Error: `%s`\n", escapeMD(result.Error))
		}
		if result.NavigationError != "" {
			fmt.Fprintf(&b, "- Navigation warning: `%s`\n", escapeMD(result.NavigationError))
		}
		if result.ScreenshotPath != "" {
			fmt.Fprintf(&b, "- Screenshot: `%s` (%d bytes)\n", result.ScreenshotPath, result.ScreenshotBytes)
		}
		scope := result.Resources.Scope
		if scope == "" {
			scope = "process_tree"
		}
		fmt.Fprintf(&b, "- Resources: %s, root PID %d, %d samples, peak RSS %.1f MiB, max CPU %.1f%%, avg CPU %.1f%%, peak processes %d\n\n",
			scope,
			result.Resources.RootPID,
			result.Resources.Samples,
			result.Resources.PeakRSSMiB,
			result.Resources.MaxCPUPercent,
			result.Resources.AvgCPUPercent,
			result.Resources.PeakProcesses,
		)
	}
	return b.String()
}

func newProcessMonitor(rootPID int, interval time.Duration) *processMonitor {
	return &processMonitor{
		rootPID:  rootPID,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		summary:  resourceSummary{Scope: "gomoufox_sidecar_process_tree", RootPID: rootPID},
	}
}

func (m *processMonitor) start(ctx context.Context) {
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		m.sample(ctx)
		for {
			select {
			case <-m.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.sample(ctx)
			}
		}
	}()
}

func (m *processMonitor) stopAndWait() {
	select {
	case <-m.done:
		return
	default:
	}
	close(m.stop)
	<-m.done
}

func (m *processMonitor) sample(ctx context.Context) {
	sample, err := sampleProcessTree(ctx, m.rootPID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		if len(m.summary.SampleErrors) < 5 {
			m.summary.SampleErrors = append(m.summary.SampleErrors, err.Error())
		}
		return
	}
	m.summary.Samples++
	m.cpuSum += sample.CPU
	if sample.RSSKiB > m.summary.PeakRSSKiB {
		m.summary.PeakRSSKiB = sample.RSSKiB
		m.summary.PeakRSSMiB = float64(sample.RSSKiB) / 1024
	}
	if sample.CPU > m.summary.MaxCPUPercent {
		m.summary.MaxCPUPercent = sample.CPU
	}
	if sample.Count > m.summary.PeakProcesses {
		m.summary.PeakProcesses = sample.Count
		m.summary.ProcessCommands = sample.Commands
	}
	if m.summary.Samples > 0 {
		m.summary.AvgCPUPercent = m.cpuSum / float64(m.summary.Samples)
	}
}

func (m *processMonitor) snapshot() resourceSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.summary
}

func sampleProcessTree(ctx context.Context, rootPID int) (processSample, error) {
	if rootPID <= 0 {
		return processSample{}, errors.New("missing root pid")
	}
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,pcpu=,rss=,comm=").Output()
	if err != nil {
		return processSample{}, err
	}
	rows := parsePSRows(string(out))
	return aggregateProcessTree(rootPID, rows)
}

func parsePSRows(out string) []processRow {
	var rows []processRow
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		cpu, cpuErr := strconv.ParseFloat(fields[2], 64)
		rss, rssErr := strconv.ParseInt(fields[3], 10, 64)
		if pidErr != nil || ppidErr != nil || cpuErr != nil || rssErr != nil {
			continue
		}
		rows = append(rows, processRow{PID: pid, PPID: ppid, CPU: cpu, RSS: rss, Cmd: strings.Join(fields[4:], " ")})
	}
	return rows
}

func aggregateProcessTree(rootPID int, rows []processRow) (processSample, error) {
	byPID := map[int]processRow{}
	children := map[int][]int{}
	for _, row := range rows {
		byPID[row.PID] = row
		children[row.PPID] = append(children[row.PPID], row.PID)
	}
	if _, ok := byPID[rootPID]; !ok {
		return processSample{}, fmt.Errorf("root pid %d not found", rootPID)
	}
	seen := map[int]bool{}
	stack := []int{rootPID}
	var sample processSample
	commandSet := map[string]bool{}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		row := byPID[pid]
		sample.RSSKiB += row.RSS
		sample.CPU += row.CPU
		sample.Count++
		if row.Cmd != "" && !commandSet[row.Cmd] {
			commandSet[row.Cmd] = true
			sample.Commands = append(sample.Commands, row.Cmd)
		}
		stack = append(stack, children[pid]...)
	}
	sort.Strings(sample.Commands)
	return sample, nil
}

func (f *targetFlags) Set(raw string) error {
	tgt, err := parseTarget(raw)
	if err != nil {
		return err
	}
	*f = append(*f, tgt)
	return nil
}

func (f *targetFlags) String() string {
	parts := make([]string, 0, len(*f))
	for _, target := range *f {
		parts = append(parts, target.Name+"="+target.URL)
	}
	return strings.Join(parts, ",")
}

func (f *nameFlags) Set(raw string) error {
	name := strings.TrimSpace(raw)
	if name == "" || slug(name) != name {
		return errors.New("target name must use report target names")
	}
	*f = append(*f, name)
	return nil
}

func (f *nameFlags) String() string {
	return strings.Join(*f, ",")
}

func parseTarget(raw string) (target, error) {
	name := ""
	value := raw
	if before, after, ok := strings.Cut(raw, "="); ok {
		name = strings.TrimSpace(before)
		value = strings.TrimSpace(after)
	}
	kind := "custom"
	if before, after, ok := strings.Cut(name, "|"); ok {
		name = strings.TrimSpace(before)
		if parsedKind := slug(strings.TrimSpace(after)); parsedKind != "" {
			kind = parsedKind
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return target{}, fmt.Errorf("invalid target %q", raw)
	}
	if name == "" {
		name = slug(parsed.Host)
	}
	return target{Name: slug(name), URL: parsed.String(), Kind: kind}, nil
}

func waitFor(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func firstN(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
}

func slug(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func escapeMD(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	return value
}
