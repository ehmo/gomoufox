package gomoufox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/ehmo/gomoufox/camoufoxcfg"
	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/pwbridge"
	sidecarpkg "github.com/ehmo/gomoufox/internal/sidecar"
)

func TestNewWithFakesAndClose(t *testing.T) {
	sidecar := &fakeSidecar{endpoint: "ws://localhost:1234/token", info: SidecarInfo{PID: 42, WSEndpointRedacted: "ws://localhost:1234/<redacted>"}}
	connector := &fakeConnector{session: &fakeSession{browser: &fakeBrowser{connected: true}}}
	b, err := New(context.Background(), WithAutoInstall(false), withSidecarFactory(fakeSidecarFactory(sidecar)), withConnector(connector))
	if err != nil {
		t.Fatal(err)
	}
	if connector.endpoint != sidecar.endpoint {
		t.Fatalf("connector endpoint = %q", connector.endpoint)
	}
	if !b.IsConnected() {
		t.Fatalf("expected connected")
	}
	if got := b.Sidecar().WSEndpointRedacted; got != sidecar.info.WSEndpointRedacted {
		t.Fatalf("sidecar info = %q", got)
	}
	called := make(chan struct{}, 1)
	b.OnDisconnected(func() { called <- struct{}{} })
	b.fireDisconnected()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatalf("disconnect handler not called")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if !sidecar.stopped || !connector.session.stopped {
		t.Fatalf("expected sidecar/session stopped")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestNewHonorsCancelledContextBeforeSideEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sidecarCalled := false
	_, err := New(ctx, WithAutoInstall(false), withSidecarFactory(func(launchConfig) (sidecarHandle, error) {
		sidecarCalled = true
		return &fakeSidecar{}, nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if sidecarCalled {
		t.Fatalf("sidecar factory should not be called")
	}
}

func TestNewAutoInstallAndConnectErrorStopsSidecar(t *testing.T) {
	orig := sidecarEnsureInstalled
	defer func() { sidecarEnsureInstalled = orig }()
	origDriver := pwbridgeEnsureDriver
	defer func() { pwbridgeEnsureDriver = origDriver }()
	installed := false
	driverInstalled := false
	sidecarEnsureInstalled = func(ctx context.Context, opts sidecarpkg.InstallOptions) error {
		installed = true
		if opts.PythonBin != "python3.12" || opts.VenvDir != "/venv" {
			t.Fatalf("install opts = %#v", opts)
		}
		return nil
	}
	pwbridgeEnsureDriver = func(driverDirectory string) error {
		driverInstalled = true
		if driverDirectory != "" {
			t.Fatalf("driver directory = %q", driverDirectory)
		}
		return nil
	}
	sidecar := &fakeSidecar{endpoint: "wss://localhost:1234/rawtoken"}
	_, err := New(context.Background(),
		WithPythonBin("python3.12"),
		WithVenvDir("/venv"),
		withSidecarFactory(fakeSidecarFactory(sidecar)),
		withConnector(&fakeConnector{err: errors.New("connect failed Authorization: Bearer abc.def Cookie: sid=secret wss://localhost:1234/rawtoken")}),
	)
	if !installed {
		t.Fatalf("auto install not called")
	}
	if !driverInstalled {
		t.Fatalf("driver install not called")
	}
	if !errors.Is(err, ErrConnect) || !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("connect err = %v", err)
	}
	for i, secret := range []string{"abc.def", "sid=secret", "/rawtoken"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("connect error leaked diagnostic fixture %d", i)
		}
	}
	if !sidecar.stopped {
		t.Fatalf("sidecar not stopped after connect error")
	}
}

func TestNewAndBrowserCreationErrorEdges(t *testing.T) {
	orig := sidecarEnsureInstalled
	defer func() { sidecarEnsureInstalled = orig }()
	installErr := errors.New("install failed")
	sidecarEnsureInstalled = func(context.Context, sidecarpkg.InstallOptions) error { return installErr }
	if _, err := New(context.Background()); !errors.Is(err, installErr) {
		t.Fatalf("install err = %v", err)
	}

	factoryErr := errors.New("factory failed")
	if _, err := New(context.Background(), WithAutoInstall(false), withSidecarFactory(func(launchConfig) (sidecarHandle, error) {
		return nil, factoryErr
	})); !errors.Is(err, factoryErr) {
		t.Fatalf("factory err = %v", err)
	}

	startErr := errors.New("start failed")
	if _, err := New(context.Background(), WithAutoInstall(false), withSidecarFactory(fakeSidecarFactory(&fakeSidecar{err: startErr}))); !errors.Is(err, startErr) {
		t.Fatalf("start err = %v", err)
	}

	pageErr := errors.New("new page failed")
	raw := &fakeBrowser{connected: true, newCtxErr: errors.New("new context failed"), newCtx: &fakeContext{newPageErr: pageErr}}
	b := &Browser{raw: raw}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.NewContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("new context canceled = %v", err)
	}
	if _, err := b.NewContext(context.Background()); !errors.Is(err, raw.newCtxErr) {
		t.Fatalf("new context raw err = %v", err)
	}
	if _, err := b.NewPage(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("new page canceled = %v", err)
	}
	raw.newCtxErr = nil
	if _, err := b.NewPage(context.Background()); !errors.Is(err, pageErr) {
		t.Fatalf("new page raw err = %v", err)
	}

	closed := &Browser{raw: &fakeBrowser{}, done: make(chan struct{})}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.NewContext(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("closed new context err = %v", err)
	}

	ctxErr := errors.New("new context failed")
	if _, err := (&Browser{raw: &fakeBrowser{newCtxErr: ctxErr}}).NewPage(context.Background()); !errors.Is(err, ctxErr) {
		t.Fatalf("new page context err = %v", err)
	}
}

func TestSidecarErrorsMapToPublicSentinels(t *testing.T) {
	for _, tc := range []struct {
		input error
		want  error
	}{
		{sidecarpkg.ErrNotInstalled, ErrNotInstalled},
		{sidecarpkg.ErrVersionMismatch, ErrVersionMismatch},
		{sidecarpkg.ErrTimeout, ErrTimeout},
		{sidecarpkg.ErrSidecarStart, ErrSidecarStart},
		{sidecarpkg.ErrProfileInUse, ErrSidecarStart},
	} {
		err := mapSidecarError(fmt.Errorf("wrapped: %w", tc.input))
		if !errors.Is(err, tc.want) {
			t.Fatalf("%v did not map to %v: %v", tc.input, tc.want, err)
		}
	}
	if err := mapSidecarError(nil); err != nil {
		t.Fatalf("nil map = %v", err)
	}
	plain := errors.New("plain")
	if got := mapSidecarError(plain); got != plain {
		t.Fatalf("plain error changed: %v", got)
	}
}

func TestOptionsLaterWinsAndMergeFingerprint(t *testing.T) {
	cfg := defaultLaunchConfig()
	WithHeadless(camoufoxcfg.HeadlessFalse)(&cfg)
	WithHeadless(camoufoxcfg.HeadlessVirtual)(&cfg)
	WithLocale("en-US")(&cfg)
	WithLocale("fr-FR", "fr")(&cfg)
	WithFingerprintOverride(camoufoxcfg.FingerprintOverride{"a": 1, "b": 1})(&cfg)
	WithFingerprintOverride(camoufoxcfg.FingerprintOverride{"b": 2})(&cfg)
	if cfg.headless != camoufoxcfg.HeadlessVirtual {
		t.Fatalf("headless = %v", cfg.headless)
	}
	if got := cfg.locale; len(got) != 2 || got[0] != "fr-FR" || got[1] != "fr" {
		t.Fatalf("locale = %#v", got)
	}
	if cfg.fingerprint["a"] != 1 || cfg.fingerprint["b"] != 2 {
		t.Fatalf("fingerprint = %#v", cfg.fingerprint)
	}
}

func TestPublicOptionCoverageAndConversions(t *testing.T) {
	cfg := defaultLaunchConfig()
	WithHumanize(1500 * time.Millisecond)(&cfg)
	WithGeoIP(true)(&cfg)
	WithProxy(camoufoxcfg.ProxyConfig{Server: "http://proxy", Username: "u", Password: "p"})(&cfg)
	WithOS(camoufoxcfg.OSLinux)(&cfg)
	WithBlockImages(true)(&cfg)
	WithBlockWebRTC(true)(&cfg)
	WithBlockWebGL(true)(&cfg)
	WithPersistentContext("/profile")(&cfg)
	WithUnsafeDirectNetwork(true)(&cfg)
	WithAddons("/a", "/b")(&cfg)
	WithWindow(1200, 800)(&cfg)
	WithScreen(1440, 900)(&cfg)
	WithWebGL("vendor", "renderer")(&cfg)
	WithFirefoxUserPrefs(camoufoxcfg.FirefoxUserPrefs{"pref": true})(&cfg)
	WithBrowserArgs("--safe-mode")(&cfg)
	WithCustomFontsOnly(true)(&cfg)
	WithFirefoxVersion(135)(&cfg)
	WithCamoufoxDebug(true)(&cfg)
	WithFonts("Inter", "Arial")(&cfg)
	WithIdleTimeout(time.Minute)(&cfg)
	WithPythonBin("python3.12")(&cfg)
	WithVenvDir("/venv")(&cfg)
	WithConnectTimeout(5 * time.Second)(&cfg)
	WithMainWorldEval(true)(&cfg)
	WithEnableCache(true)(&cfg)
	WithDisableCOOP(true)(&cfg)
	WithExtraEnv("A=B")(&cfg)
	if cfg.humanize == nil || *cfg.humanize != 1.5 || !cfg.geoip || cfg.proxy.Server != "http://proxy" || cfg.os != camoufoxcfg.OSLinux {
		t.Fatalf("launch options not applied: %#v", cfg)
	}
	if !cfg.blockImages || !cfg.blockWebRTC || !cfg.blockWebGL || !cfg.persistentCtx || cfg.userDataDir != "/profile" || !cfg.directNetwork {
		t.Fatalf("boolean/profile options not applied: %#v", cfg)
	}
	if cfg.window.Width != 1200 || cfg.screen.Height != 900 || cfg.webgl.Renderer != "renderer" || len(cfg.fonts) != 2 || len(cfg.addons) != 2 {
		t.Fatalf("dimension/fingerprint options not applied: %#v", cfg)
	}
	if cfg.firefoxPrefs["pref"] != true || cfg.browserArgs[0] != "--safe-mode" || !cfg.customFontsOnly || cfg.ffVersion != 135 || !cfg.camoufoxDebug {
		t.Fatalf("python parity options not applied: %#v", cfg)
	}
	if cfg.idleTimeout != time.Minute || cfg.pythonBin != "python3.12" || cfg.venvDir != "/venv" || cfg.connectTimeout != 5*time.Second {
		t.Fatalf("runtime options not applied: %#v", cfg)
	}
	if !cfg.mainWorldEval || !cfg.enableCache || !cfg.disableCOOP || cfg.extraEnv[0] != "A=B" {
		t.Fatalf("advanced options not applied: %#v", cfg)
	}

	headers := map[string]string{"x": "1"}
	state := &StorageState{Cookies: []Cookie{{Name: "a", Value: "b"}}, Origins: []Origin{{Origin: "https://example.com", LocalStorage: []LSEntry{{Name: "k", Value: "v"}}}}}
	contextCfg := buildContextConfig(
		WithViewport(800, 600),
		WithStorageState(state),
		WithContextProxy(camoufoxcfg.ProxyConfig{Server: "socks5://proxy"}),
		WithContextLocale("en-US"),
		WithTimezoneID("America/Los_Angeles"),
		WithExtraHTTPHeaders(headers),
		WithHTTPCredentials("user", "pass"),
	)
	headers["x"] = "mutated"
	pwOpts := toPWBridgeContextOptions(contextCfg)
	if pwOpts.Viewport.Width != 800 || pwOpts.StorageState.Cookies[0].Name != "a" || pwOpts.Proxy.Server != "socks5://proxy" {
		t.Fatalf("context conversion = %#v", pwOpts)
	}
	if pwOpts.ExtraHTTPHeaders["x"] != "1" || pwOpts.HTTPCredentials.Username != "user" || pwOpts.Locale != "en-US" || pwOpts.TimezoneID == "" {
		t.Fatalf("context scalar conversion = %#v", pwOpts)
	}
}

func TestSidecarManagerReceivesLaunchOptions(t *testing.T) {
	cfg := defaultLaunchConfig()
	WithHumanize(1500 * time.Millisecond)(&cfg)
	WithGeoIP(true)(&cfg)
	WithProxy(camoufoxcfg.ProxyConfig{Server: "http://proxy", Username: "u", Password: "p"})(&cfg)
	WithOS(camoufoxcfg.OSLinux)(&cfg)
	WithLocale("en-US", "en")(&cfg)
	WithBlockImages(true)(&cfg)
	WithBlockWebRTC(true)(&cfg)
	WithBlockWebGL(true)(&cfg)
	WithUnsafeDirectNetwork(true)(&cfg)
	WithAddons("/addon")(&cfg)
	WithWindow(1200, 800)(&cfg)
	WithScreen(1440, 900)(&cfg)
	WithWebGL("Intel", "Iris")(&cfg)
	WithFirefoxUserPrefs(camoufoxcfg.FirefoxUserPrefs{"pref": true})(&cfg)
	WithBrowserArgs("--safe-mode")(&cfg)
	WithCustomFontsOnly(true)(&cfg)
	WithFirefoxVersion(135)(&cfg)
	WithCamoufoxDebug(true)(&cfg)
	WithFonts("Inter")(&cfg)
	WithFingerprintOverride(camoufoxcfg.FingerprintOverride{"navigator.userAgent": "ua"})(&cfg)
	WithMainWorldEval(true)(&cfg)
	WithEnableCache(true)(&cfg)
	WithDisableCOOP(true)(&cfg)
	WithExtraEnv("A=B")(&cfg)
	WithAllowedOrigins("https://example.com", "https://api.example.com:8443")(&cfg)
	WithAllowedHosts("example.com", ".example.org")(&cfg)
	handle, err := newSidecarManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter := handle.(sidecarAdapter)
	got := adapter.manager.Info()
	if got.PID != 0 {
		t.Fatalf("unexpected running manager info = %#v", got)
	}
	scfg := adapter.manager.Config()
	if scfg.Humanize == nil || *scfg.Humanize != 1.5 || !scfg.GeoIP || scfg.Proxy.Server != "http://proxy" || scfg.Proxy.Username != "u" {
		t.Fatalf("scalar options not mapped: %#v", scfg)
	}
	if scfg.OS != "linux" || len(scfg.Locale) != 2 || !scfg.BlockImages || !scfg.BlockWebRTC || !scfg.BlockWebGL || !scfg.DirectNetwork {
		t.Fatalf("persona options not mapped: %#v", scfg)
	}
	if scfg.Window.Width != 1200 || scfg.Screen.Height != 900 || scfg.WebGL.Renderer != "Iris" || scfg.Fonts[0] != "Inter" || scfg.Addons[0] != "/addon" {
		t.Fatalf("dimension options not mapped: %#v", scfg)
	}
	if scfg.FirefoxPrefs["pref"] != true || scfg.BrowserArgs[0] != "--safe-mode" || !scfg.CustomFontsOnly || scfg.FFVersion != 135 || !scfg.CamoufoxDebug {
		t.Fatalf("python parity options not mapped: %#v", scfg)
	}
	if scfg.Fingerprint["navigator.userAgent"] != "ua" || !scfg.MainWorldEval || !scfg.EnableCache || !scfg.DisableCOOP || scfg.ExtraEnv[0] != "A=B" {
		t.Fatalf("advanced options not mapped: %#v", scfg)
	}
	if strings.Join(scfg.Policy.AllowedOrigins, ",") != "https://example.com,https://api.example.com:8443" || strings.Join(scfg.Policy.AllowedHosts, ",") != "example.com,.example.org" {
		t.Fatalf("network policy not mapped: %#v", scfg.Policy)
	}
}

func TestNavigationAndScreenshotOptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	gotoCfg := buildGotoConfig(WaitUntil("domcontentloaded"), WithReferer("https://ref.example"), WithTimeout(3*time.Second)).toBridge(ctx)
	if gotoCfg.WaitUntil != "domcontentloaded" || gotoCfg.Referer != "https://ref.example" || gotoCfg.Timeout != 3*time.Second {
		t.Fatalf("goto cfg = %#v", gotoCfg)
	}
	navCfg := buildNavigateConfig(NavigateWaitUntil("networkidle"), NavigateTimeout(4*time.Second)).toBridge(ctx)
	if navCfg.WaitUntil != "networkidle" || navCfg.Timeout != 4*time.Second {
		t.Fatalf("nav cfg = %#v", navCfg)
	}
	shot := screenshotConfig{typ: "png"}
	FullPage(true)(&shot)
	ScreenshotType("jpeg")(&shot)
	JPEGQuality(90)(&shot)
	Clip(1, 2, 3, 4)(&shot)
	pwShot := shot.toBridge()
	if !pwShot.FullPage || pwShot.Type != "jpeg" || pwShot.Quality != 90 || pwShot.Clip.Width != 3 {
		t.Fatalf("screenshot cfg = %#v", pwShot)
	}
	pdf := pdfConfig{}
	PDFFormat("A4")(&pdf)
	if pdf.format != "A4" {
		t.Fatalf("pdf cfg = %#v", pdf)
	}
	if d := deadlineTimeout(context.Background(), time.Second); d != time.Second {
		t.Fatalf("fallback timeout = %s", d)
	}
	future, cancelFuture := context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
	defer cancelFuture()
	if d := deadlineTimeout(future, time.Second); d <= 0 || d > time.Hour {
		t.Fatalf("future timeout = %s", d)
	}
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	if d := deadlineTimeout(expired, time.Second); d != time.Nanosecond {
		t.Fatalf("expired timeout = %s", d)
	}
}

func TestPersistentContextLimit(t *testing.T) {
	rawCtx := &fakeContext{}
	fb := &fakeBrowser{connected: true, contexts: []pwbridge.BrowserContext{rawCtx}}
	b, err := New(context.Background(),
		WithAutoInstall(false),
		WithPersistentContext(t.TempDir()),
		withSidecarFactory(fakeSidecarFactory(&fakeSidecar{endpoint: "ws://localhost:1/t"})),
		withConnector(&fakeConnector{session: &fakeSession{browser: fb}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.NewContext(context.Background()); err != nil {
		t.Fatalf("first context: %v", err)
	}
	if _, err := b.NewContext(context.Background()); !errors.Is(err, ErrPersistentContextLimit) {
		t.Fatalf("second context error = %v", err)
	}
}

func TestPersistentContextRequiresConnectedPersistentContext(t *testing.T) {
	fb := &fakeBrowser{connected: true}
	b, err := New(context.Background(),
		WithAutoInstall(false),
		WithPersistentContext(t.TempDir()),
		withSidecarFactory(fakeSidecarFactory(&fakeSidecar{endpoint: "ws://localhost:1/t"})),
		withConnector(&fakeConnector{session: &fakeSession{browser: fb}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.NewContext(context.Background()); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("zero-context persistent error = %v", err)
	}
	if fb.newCtx != nil {
		t.Fatalf("persistent fallback allocated ephemeral context: %#v", fb.newCtx)
	}
	if _, err := b.NewContext(context.Background()); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("second zero-context persistent error = %v", err)
	}
	if _, err := b.NewPage(context.Background()); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("zero-context persistent page error = %v", err)
	}
	limitWithoutContext := &Browser{cfg: launchConfig{persistentCtx: true}, raw: fb, done: make(chan struct{}), persistentReturned: true}
	if _, err := limitWithoutContext.NewPage(context.Background()); !errors.Is(err, ErrPersistentContextLimit) {
		t.Fatalf("persistent limit without context page error = %v", err)
	}
}

func TestNewPageOwnsAndClosesThrowawayContext(t *testing.T) {
	fb := &fakeBrowser{connected: true}
	b, err := New(context.Background(),
		WithAutoInstall(false),
		withSidecarFactory(fakeSidecarFactory(&fakeSidecar{endpoint: "ws://localhost:1/t"})),
		withConnector(&fakeConnector{session: &fakeSession{browser: fb}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	page, err := b.NewPage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if page.context == nil || !page.ownsContext {
		t.Fatalf("page did not keep throwaway context: %#v", page)
	}
	if err := page.Close(); err != nil {
		t.Fatal(err)
	}
	if fb.newCtx == nil || !fb.newCtx.closed {
		t.Fatalf("throwaway context not closed: %#v", fb.newCtx)
	}
	if rawPage, ok := page.raw.(*fakePage); !ok || rawPage.closeCalls != 0 {
		t.Fatalf("owned page close sent page close calls = %d, want 0", rawPage.closeCalls)
	}
}

func TestNewPagePersistentReusesConnectedContext(t *testing.T) {
	rawCtx := &fakeContext{}
	fb := &fakeBrowser{connected: true, contexts: []pwbridge.BrowserContext{rawCtx}}
	b, err := New(context.Background(),
		WithAutoInstall(false),
		WithPersistentContext(t.TempDir()),
		withSidecarFactory(fakeSidecarFactory(&fakeSidecar{endpoint: "ws://localhost:1/t"})),
		withConnector(&fakeConnector{session: &fakeSession{browser: fb}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := b.NewPage(context.Background())
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	second, err := b.NewPage(context.Background())
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if first.context.raw != rawCtx || second.context.raw != rawCtx || len(rawCtx.pages) != 2 {
		t.Fatalf("persistent pages first=%#v second=%#v pages=%d", first.context.raw, second.context.raw, len(rawCtx.pages))
	}
}

func TestNewPageClosesThrowawayContextOnPageError(t *testing.T) {
	pageErr := errors.New("new page failed")
	rawCtx := &fakeContext{newPageErr: pageErr}
	fb := &fakeBrowser{connected: true, newCtx: rawCtx}
	b, err := New(context.Background(),
		WithAutoInstall(false),
		withSidecarFactory(fakeSidecarFactory(&fakeSidecar{endpoint: "ws://localhost:1/t"})),
		withConnector(&fakeConnector{session: &fakeSession{browser: fb}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.NewPage(context.Background()); !errors.Is(err, pageErr) {
		t.Fatalf("new page err = %v", err)
	}
	if !rawCtx.closed {
		t.Fatalf("context not closed on page error")
	}
}

func TestStorageStateWrites0600(t *testing.T) {
	ctx := &Context{raw: &fakeContext{storage: &pwbridge.StorageState{Cookies: []pwbridge.Cookie{{Name: "a", Value: "b"}}}}}
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := ctx.StorageState(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Cookies) != 1 || state.Cookies[0].Value != "b" {
		t.Fatalf("state = %#v", state)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o", got)
	}
}

func TestStorageStateConversionsAndWriteErrors(t *testing.T) {
	raw := &pwbridge.StorageState{
		Cookies: []pwbridge.Cookie{{Name: "sid", Value: "secret", Domain: "example.com", Path: "/", HTTPOnly: true, Secure: true, SameSite: "Lax"}},
		Origins: []pwbridge.Origin{{
			Origin:       "https://example.com",
			LocalStorage: []pwbridge.LSEntry{{Name: "theme", Value: "dark"}},
		}},
	}
	state := fromBridgeStorageState(raw)
	if len(state.Cookies) != 1 || !state.Cookies[0].HTTPOnly || len(state.Origins) != 1 || state.Origins[0].LocalStorage[0].Name != "theme" {
		t.Fatalf("state = %#v", state)
	}
	roundTrip := toBridgeStorageState(state)
	if len(roundTrip.Cookies) != 1 || !roundTrip.Cookies[0].Secure || len(roundTrip.Origins) != 1 {
		t.Fatalf("round trip = %#v", roundTrip)
	}
	if fromBridgeStorageState(nil) != nil || toBridgeStorageState(nil) != nil {
		t.Fatalf("nil storage conversion mismatch")
	}
	if _, err := fromBridgeCookies(nil, errors.New("cookies failed")); err == nil {
		t.Fatal("cookie conversion error swallowed")
	}
	parentFile := filepath.Join(t.TempDir(), "parent-file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON0600(filepath.Join(parentFile, "state.json"), state); err == nil {
		t.Fatal("writeJSON under file parent succeeded")
	}
	if err := writeJSON0600(filepath.Join(t.TempDir(), "bad.json"), func() {}); err == nil {
		t.Fatal("writeJSON marshal error succeeded")
	}
	if err := writeBytes0600(filepath.Join(parentFile, "bytes.bin"), []byte("x")); err == nil {
		t.Fatal("writeBytes under file parent succeeded")
	}
}

func TestEnsureInstalledMapsOptionsAndErrors(t *testing.T) {
	orig := sidecarEnsureInstalled
	defer func() { sidecarEnsureInstalled = orig }()
	origDriver := pwbridgeEnsureDriver
	defer func() { pwbridgeEnsureDriver = origDriver }()
	called := false
	pwbridgeEnsureDriver = func(string) error {
		t.Fatalf("driver install should not run after sidecar error")
		return nil
	}
	sidecarEnsureInstalled = func(ctx context.Context, opts sidecarpkg.InstallOptions) error {
		called = true
		if opts.PythonBin != "python3.12" || opts.VenvDir != "/venv" || opts.CamoufoxVersion != "0.4.11" ||
			!opts.SkipBinaryFetch || opts.CamoufoxPath != "/camoufox" || !opts.Verbose || !opts.ForceReinstall {
			t.Fatalf("opts = %#v", opts)
		}
		return fmt.Errorf("wrapped: %w", sidecarpkg.ErrVersionMismatch)
	}
	err := EnsureInstalled(context.Background(), func(o *InstallOptions) {
		o.PythonBin = "python3.12"
		o.VenvDir = "/venv"
		o.CamoufoxVersion = "0.4.11"
		o.SkipBinaryFetch = true
		o.CamoufoxPath = "/camoufox"
		o.Verbose = true
		o.ForceReinstall = true
	})
	if !called {
		t.Fatalf("sidecar ensure not called")
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsureInstalledInstallsPlaywrightDriver(t *testing.T) {
	orig := sidecarEnsureInstalled
	origDriver := pwbridgeEnsureDriver
	defer func() {
		sidecarEnsureInstalled = orig
		pwbridgeEnsureDriver = origDriver
	}()
	sidecarCalled := false
	driverCalled := false
	sidecarEnsureInstalled = func(context.Context, sidecarpkg.InstallOptions) error {
		sidecarCalled = true
		return nil
	}
	pwbridgeEnsureDriver = func(driverDirectory string) error {
		driverCalled = true
		if driverDirectory != "" {
			t.Fatalf("driver directory = %q", driverDirectory)
		}
		return nil
	}
	if err := EnsureInstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sidecarCalled || !driverCalled {
		t.Fatalf("sidecarCalled=%v driverCalled=%v", sidecarCalled, driverCalled)
	}

	driverErr := errors.New("driver failed")
	pwbridgeEnsureDriver = func(string) error { return driverErr }
	if err := EnsureInstalled(context.Background()); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "playwright driver install failed") {
		t.Fatalf("driver install err = %v", err)
	}
}

func TestWriteBytes0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "bytes.bin")
	if err := writeBytes0600(path, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "secret" {
		t.Fatalf("data = %q", data)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o", got)
	}
}

func TestContextWrappersAndRouteRegistry(t *testing.T) {
	req := &fakeRequest{url: "https://example.com"}
	fc := &fakeContext{pages: []pwbridge.Page{&fakePage{}}}
	ctx := &Context{raw: fc}
	if len(ctx.Pages()) != 1 {
		t.Fatalf("pages not wrapped")
	}
	page, err := ctx.NewPage(context.Background())
	if err != nil || page == nil {
		t.Fatalf("new page = %#v, %v", page, err)
	}
	handlerCalled := false
	handler := func(r *Route) {
		handlerCalled = true
		if r.Request().URL() != req.url {
			t.Fatalf("route request = %q", r.Request().URL())
		}
	}
	if err := ctx.Route(context.Background(), "**/*", handler); err != nil {
		t.Fatal(err)
	}
	fc.routeHandler(&fakeRoute{request: req})
	if !handlerCalled {
		t.Fatalf("route handler not wrapped")
	}
	registeredID := bridgeRouteHandlerID(fc.routeHandler)
	if err := ctx.Unroute(context.Background(), "**/*", handler); err != nil {
		t.Fatal(err)
	}
	if fc.unroutePattern != "**/*" || bridgeRouteHandlerID(fc.unrouteHandler) != registeredID {
		t.Fatalf("unroute did not use registered handler")
	}
	fc.unrouteCalls = 0
	if err := ctx.Unroute(context.Background(), "**/*", func(*Route) {}); err != nil {
		t.Fatal(err)
	}
	if fc.unrouteCalls != 0 {
		t.Fatalf("unknown handler should not call raw unroute")
	}
	if err := ctx.Route(context.Background(), "**/again", handler); err != nil {
		t.Fatal(err)
	}
	if err := ctx.Unroute(context.Background(), "**/again", nil); err != nil {
		t.Fatal(err)
	}
	if fc.unroutePattern != "**/again" || fc.unrouteHandler != nil {
		t.Fatalf("nil handler unroute = %q %#v", fc.unroutePattern, fc.unrouteHandler)
	}
	requestSeen := false
	ctx.OnRequest(func(r *Request) { requestSeen = r.URL() == req.url })
	fc.onRequest(req)
	if !requestSeen {
		t.Fatalf("on request not wrapped")
	}
	responseSeen := false
	ctx.OnResponse(func(r *Response) { responseSeen = r.URL() == "https://response.example" })
	fc.onResponse(&fakeResponse{url: "https://response.example"})
	if !responseSeen {
		t.Fatalf("on response not wrapped")
	}
	if ctx.Raw() != fc {
		t.Fatalf("raw mismatch")
	}
}

func TestPageFetchJSONAndErrors(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{"ok": true, "status": 200, "body": `{"ok":true}`, "url": "https://example.com"}}}
	var dst struct {
		OK bool `json:"ok"`
	}
	if err := page.FetchJSON(context.Background(), "https://example.com/api", "GET", nil, nil, &dst); err != nil {
		t.Fatal(err)
	}
	if !dst.OK {
		t.Fatalf("decoded false")
	}
	page.raw = &fakePage{evaluateResult: map[string]any{"ok": true, "status": 200, "body": `not-json`, "url": "https://example.com"}}
	if err := page.FetchJSON(context.Background(), "https://example.com/api", "GET", nil, nil, &dst); !errors.Is(err, ErrBrowserFetch) {
		t.Fatalf("non-json error = %v", err)
	}
	page.raw = &fakePage{evaluateResult: map[string]any{"ok": false, "code": "cors_denied", "status": 0, "body": "", "url": "https://example.com", "message": "blocked"}}
	if _, _, err := page.FetchBytes(context.Background(), "https://example.com/api", "GET", nil, nil); !errors.Is(err, ErrBrowserFetch) {
		t.Fatalf("fetch error = %v", err)
	}
}

func TestPageWrappersAndRouteRegistry(t *testing.T) {
	fp := &fakePage{
		response: &fakeResponse{url: "https://example.com", status: 200, request: &fakeRequest{url: "https://example.com"}},
	}
	page := &Page{raw: fp}
	if resp, err := page.Goto(context.Background(), "https://example.com", WaitUntil("domcontentloaded"), WithTimeout(time.Second)); err != nil || resp.Status() != 200 {
		t.Fatalf("goto = %#v, %v", resp, err)
	}
	if fp.gotoURL != "https://example.com" || fp.gotoOpts.WaitUntil != "domcontentloaded" || fp.gotoOpts.Timeout != time.Second {
		t.Fatalf("goto opts = %q %#v", fp.gotoURL, fp.gotoOpts)
	}
	if _, err := page.GoBack(context.Background(), NavigateTimeout(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if fp.backOpts.Timeout != 2*time.Second {
		t.Fatalf("back opts = %#v", fp.backOpts)
	}
	if _, err := page.GoForward(context.Background(), NavigateWaitUntil("networkidle")); err != nil {
		t.Fatal(err)
	}
	if fp.forwardOpts.WaitUntil != "networkidle" {
		t.Fatalf("forward opts = %#v", fp.forwardOpts)
	}
	if _, err := page.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	actionCalled := false
	if err := page.RunAndWaitForNavigation(context.Background(), func() error {
		actionCalled = true
		return nil
	}, NavigateTimeout(1500*time.Millisecond)); err != nil || !actionCalled {
		t.Fatalf("run and wait navigation action=%t err=%v", actionCalled, err)
	}
	if fp.reloadOpts.Timeout != 1500*time.Millisecond {
		t.Fatalf("run and wait navigation opts = %#v", fp.reloadOpts)
	}
	if _, err := page.Evaluate(context.Background(), "1+1"); err != nil {
		t.Fatal(err)
	}
	if _, err := page.EvaluateInternal(context.Background(), "1+1", "internal"); err != nil || fp.internalEvalArg != "internal" {
		t.Fatalf("internal evaluate arg=%#v err=%v", fp.internalEvalArg, err)
	}
	if err := page.AddInitScript(context.Background(), "window.x=1"); err != nil || fp.initScript != "window.x=1" {
		t.Fatalf("init script = %q %v", fp.initScript, err)
	}
	if html, err := page.Content(context.Background()); err != nil || html == "" {
		t.Fatalf("content = %q %v", html, err)
	}
	if err := page.SetContent(context.Background(), "<p>x</p>", WaitUntil("load")); err != nil || fp.setContentHTML != "<p>x</p>" {
		t.Fatalf("set content = %q %v", fp.setContentHTML, err)
	}
	if title, err := page.Title(context.Background()); err != nil || title != "title" {
		t.Fatalf("title = %q %v", title, err)
	}
	if page.URL() != "https://example.com" {
		t.Fatalf("url = %q", page.URL())
	}
	if err := page.WaitForLoadState(context.Background(), "load"); err != nil || fp.loadState != "load" {
		t.Fatalf("load state = %q %v", fp.loadState, err)
	}
	if _, err := page.WaitForSelector(context.Background(), "#x", WaitForSelectorTimeout(time.Second), WaitForSelectorState("visible")); err != nil {
		t.Fatal(err)
	}
	if fp.waitSelector != "#x" || fp.waitSelectorOpts.State != "visible" || fp.waitSelectorOpts.Timeout != time.Second {
		t.Fatalf("wait selector = %q %#v", fp.waitSelector, fp.waitSelectorOpts)
	}
	if err := page.WaitForURL(context.Background(), "**/x", WithTimeout(time.Second)); err != nil || fp.waitURL != "**/x" {
		t.Fatalf("wait url = %q %v", fp.waitURL, err)
	}
	if shot, err := page.Screenshot(context.Background(), FullPage(true)); err != nil || string(shot) != "png" || !fp.screenshotOpts.FullPage {
		t.Fatalf("shot = %q %#v %v", shot, fp.screenshotOpts, err)
	}
	out := filepath.Join(t.TempDir(), "shot.png")
	if err := page.ScreenshotToFile(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	if pdf, err := page.PDF(context.Background(), PDFFormat("A4")); err != nil || string(pdf) != "pdf" || fp.pdfOpts.Format != "A4" {
		t.Fatalf("pdf = %q %#v %v", pdf, fp.pdfOpts, err)
	}
	if _, err := page.Cookies(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler := func(*Route) {}
	if err := page.Route(context.Background(), "**/*", handler); err != nil {
		t.Fatal(err)
	}
	registeredID := bridgeRouteHandlerID(fp.routeHandler)
	if err := page.Unroute(context.Background(), "**/*", handler); err != nil {
		t.Fatal(err)
	}
	if fp.unroutePattern != "**/*" || bridgeRouteHandlerID(fp.unrouteHandler) != registeredID {
		t.Fatalf("page unroute did not use registered handler")
	}
	fp.unrouteCalls = 0
	if err := page.Unroute(context.Background(), "**/*", func(*Route) {}); err != nil {
		t.Fatal(err)
	}
	if fp.unrouteCalls != 0 {
		t.Fatalf("unknown page handler should not call raw unroute")
	}
	if err := page.Route(context.Background(), "**/again", handler); err != nil {
		t.Fatal(err)
	}
	if err := page.Unroute(context.Background(), "**/again", nil); err != nil {
		t.Fatal(err)
	}
	if fp.unroutePattern != "**/again" || fp.unrouteHandler != nil {
		t.Fatalf("nil page handler unroute = %q %#v", fp.unroutePattern, fp.unrouteHandler)
	}
	requestSeen := false
	page.OnRequest(func(r *Request) { requestSeen = r.URL() == "https://request.example" })
	fp.onRequest(&fakeRequest{url: "https://request.example"})
	if !requestSeen {
		t.Fatalf("page request callback not wrapped")
	}
	requestFailedSeen := false
	page.OnRequestFailed(func(r *Request) { requestFailedSeen = r.URL() == "https://failed.example" })
	fp.onRequestFailed(&fakeRequest{url: "https://failed.example"})
	if !requestFailedSeen {
		t.Fatalf("page request-failed callback not wrapped")
	}
	responseSeen := false
	page.OnResponse(func(r *Response) { responseSeen = r.URL() == "https://response.example" })
	fp.onResponse(&fakeResponse{url: "https://response.example"})
	if !responseSeen {
		t.Fatalf("page response callback not wrapped")
	}
	pageErrorSeen := false
	page.OnPageError(func(err error) { pageErrorSeen = err.Error() == "boom" })
	fp.onPageError(errors.New("boom"))
	if !pageErrorSeen {
		t.Fatalf("page error callback not wrapped")
	}
	consoleSeen := false
	page.OnConsole(func(m ConsoleMessage) { consoleSeen = m.Text == "hello" })
	fp.onConsole(pwbridge.ConsoleMessage{Type: "log", Text: "hello"})
	if !consoleSeen {
		t.Fatalf("console callback not wrapped")
	}
	dialogSeen := false
	page.OnDialog(func(d Dialog) { dialogSeen = d.Type() == "alert" && d.Message() == "hello" })
	fp.onDialog(&fakeDialog{typ: "alert", message: "hello"})
	if !dialogSeen {
		t.Fatalf("dialog callback not wrapped")
	}
	rawDialog := &fakeDialog{typ: "prompt", message: "hello", defaultValue: "name"}
	dialog := Dialog{raw: rawDialog}
	if dialog.DefaultValue() != "name" {
		t.Fatalf("dialog default value = %q", dialog.DefaultValue())
	}
	if err := dialog.Accept(context.Background(), "ok"); err != nil || !rawDialog.accepted || rawDialog.acceptText != "ok" {
		t.Fatalf("dialog accept raw=%#v err=%v", rawDialog, err)
	}
	if err := dialog.Dismiss(context.Background()); err != nil || !rawDialog.dismissed {
		t.Fatalf("dialog dismiss raw=%#v err=%v", rawDialog, err)
	}
	canceledDialogCtx, cancelDialog := context.WithCancel(context.Background())
	cancelDialog()
	if err := dialog.Accept(canceledDialogCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dialog accept = %v", err)
	}
	if err := dialog.Dismiss(canceledDialogCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dialog dismiss = %v", err)
	}
	if err := page.Wheel(context.Background(), 4, 8); err != nil || fp.wheelX != 4 || fp.wheelY != 8 {
		t.Fatalf("wheel = %v deltas=%v/%v", err, fp.wheelX, fp.wheelY)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := page.Wheel(canceled, 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wheel = %v", err)
	}
	if page.Raw() != fp {
		t.Fatalf("raw mismatch")
	}
}

func TestLocatorWrappers(t *testing.T) {
	fl := &fakeLocator{text: "hello", count: 2}
	loc := (&Page{raw: &fakePage{locator: fl}}).Locator("#a")
	if err := loc.Click(context.Background(), LocatorClickTimeout(time.Second), LocatorClickForce(true)); err != nil {
		t.Fatal(err)
	}
	if fl.clickOpts.Timeout != time.Second || !fl.clickOpts.Force {
		t.Fatalf("click opts = %#v", fl.clickOpts)
	}
	if err := loc.Click(context.Background(), LocatorClickButton("right"), LocatorClickCount(2)); err != nil {
		t.Fatal(err)
	}
	if fl.clickOpts.Button != "right" || fl.clickOpts.ClickCount != 2 {
		t.Fatalf("click button/count opts = %#v", fl.clickOpts)
	}
	if err := loc.Fill(context.Background(), "value", LocatorFillTimeout(2*time.Second), LocatorFillForce(true)); err != nil {
		t.Fatal(err)
	}
	if fl.fillValue != "value" || fl.fillOpts.Timeout != 2*time.Second || !fl.fillOpts.Force {
		t.Fatalf("fill = %q %#v", fl.fillValue, fl.fillOpts)
	}
	if err := loc.Type(context.Background(), "typed", LocatorTypeTimeout(5*time.Second), LocatorTypeDelay(25*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if fl.typeValue != "typed" || fl.typeOpts.Timeout != 5*time.Second || fl.typeOpts.Delay != 25*time.Millisecond {
		t.Fatalf("type = %q %#v", fl.typeValue, fl.typeOpts)
	}
	if err := loc.Press(context.Background(), "Enter", LocatorPressTimeout(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if fl.pressKey != "Enter" || fl.pressOpts.Timeout != 6*time.Second {
		t.Fatalf("press = %q %#v", fl.pressKey, fl.pressOpts)
	}
	if err := loc.Hover(context.Background(), LocatorHoverTimeout(7*time.Second), LocatorHoverForce(true)); err != nil {
		t.Fatal(err)
	}
	if fl.hoverOpts.Timeout != 7*time.Second || !fl.hoverOpts.Force {
		t.Fatalf("hover opts = %#v", fl.hoverOpts)
	}
	if err := loc.ScrollIntoViewIfNeeded(context.Background(), LocatorTimeout(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if fl.scrollOpts.Timeout != 8*time.Second {
		t.Fatalf("scroll opts = %#v", fl.scrollOpts)
	}
	fl.selectResult = []string{"us"}
	selected, err := loc.SelectOption(context.Background(), LocatorSelectTimeout(9*time.Second), LocatorSelectForce(true), LocatorSelectValues("us"), LocatorSelectLabels("United States"), LocatorSelectIndexes(1))
	if err != nil || strings.Join(selected, ",") != "us" || fl.selectOpts.Timeout != 9*time.Second || !fl.selectOpts.Force || strings.Join(fl.selectOpts.Values, ",") != "us" || strings.Join(fl.selectOpts.Labels, ",") != "United States" || len(fl.selectOpts.Indexes) != 1 || fl.selectOpts.Indexes[0] != 1 {
		t.Fatalf("select = %v %#v %v", selected, fl.selectOpts, err)
	}
	if err := loc.SetChecked(context.Background(), true, LocatorSetCheckedTimeout(10*time.Second), LocatorSetCheckedForce(true)); err != nil {
		t.Fatal(err)
	}
	if !fl.checked || fl.checkedOpts.Timeout != 10*time.Second || !fl.checkedOpts.Force {
		t.Fatalf("checked opts = checked:%v %#v", fl.checked, fl.checkedOpts)
	}
	if err := loc.SetInputFiles(context.Background(), []string{"a.txt", "b.txt"}, LocatorSetInputFilesTimeout(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fl.inputFiles, ",") != "a.txt,b.txt" || fl.inputFilesOpts.Timeout != 11*time.Second {
		t.Fatalf("input files = %#v %#v", fl.inputFiles, fl.inputFilesOpts)
	}
	if text, err := loc.TextContent(context.Background(), LocatorTextTimeout(4*time.Second)); err != nil || text != "hello" || fl.optionOpts.Timeout != 4*time.Second {
		t.Fatalf("text = %q %#v %v", text, fl.optionOpts, err)
	}
	if html, err := loc.InnerHTML(context.Background(), LocatorTimeout(time.Second)); err != nil || html != "<b>hello</b>" || fl.optionOpts.Timeout != time.Second {
		t.Fatalf("inner = %q %#v %v", html, fl.optionOpts, err)
	}
	if attr, err := loc.GetAttribute(context.Background(), "href"); err != nil || attr != "attr" || fl.attrName != "href" {
		t.Fatalf("attr = %q %q %v", attr, fl.attrName, err)
	}
	if visible, err := loc.IsVisible(context.Background()); err != nil || !visible {
		t.Fatalf("visible = %v %v", visible, err)
	}
	if loc.First() == nil || loc.Last() == nil || loc.Nth(1) == nil {
		t.Fatalf("derived locator nil")
	}
	if err := loc.WaitFor(context.Background(), LocatorWaitTimeout(3*time.Second), LocatorWaitState("attached")); err != nil {
		t.Fatal(err)
	}
	if fl.waitOpts.Timeout != 3*time.Second || fl.waitOpts.State != "attached" {
		t.Fatalf("wait opts = %#v", fl.waitOpts)
	}
	if shot, err := loc.Screenshot(context.Background(), ScreenshotType("jpeg")); err != nil || string(shot) != "shot" || fl.screenshotOpts.Type != "jpeg" {
		t.Fatalf("locator shot = %q %#v %v", shot, fl.screenshotOpts, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := loc.Click(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled click = %v", err)
	}
	if err := loc.Type(canceled, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled type = %v", err)
	}
	if err := loc.Press(canceled, "Enter"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled press = %v", err)
	}
	if err := loc.Hover(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled hover = %v", err)
	}
	if _, err := loc.SelectOption(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled select = %v", err)
	}
	if err := loc.SetChecked(canceled, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled checked = %v", err)
	}
	if err := loc.SetInputFiles(canceled, []string{"a.txt"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled input files = %v", err)
	}
}

func TestNavigationErrorMapping(t *testing.T) {
	if got := mapNavigationError(nil); got != nil {
		t.Fatalf("nil = %v", got)
	}
	if !errors.Is(mapNavigationError(context.DeadlineExceeded), ErrNavigationTimeout) {
		t.Fatalf("deadline not mapped")
	}
	if !errors.Is(mapNavigationError(errors.New("Timeout 30000ms exceeded")), ErrNavigationTimeout) {
		t.Fatalf("timeout string not mapped")
	}
	plain := errors.New("boom")
	if got := mapNavigationError(plain); got != plain {
		t.Fatalf("plain changed: %v", got)
	}
}

func TestResponseRequestNilWhenBridgeHasNoRequest(t *testing.T) {
	resp := &Response{raw: &fakeResponse{}}
	if got := resp.Request(); got != nil {
		t.Fatalf("request = %#v", got)
	}
	resp.raw = &fakeResponse{request: &fakeRequest{url: "https://example.com"}}
	if got := resp.Request(); got == nil || got.URL() != "https://example.com" {
		t.Fatalf("request = %#v", got)
	}
}

func TestRouteRequestResponseLocatorWrappers(t *testing.T) {
	req := &fakeRequest{url: "https://example.com", method: "POST", headers: map[string]string{"x": "y"}, post: "body"}
	resp := &fakeResponse{url: "https://example.com", status: 201, text: `{"a":1}`, body: []byte(`{"a":1}`), request: req}
	route := &Route{raw: &fakeRoute{request: req, response: resp}}
	if route.Request().URL() != req.url {
		t.Fatalf("request URL mismatch")
	}
	if route.Request().Method() != "POST" || route.Request().Headers()["x"] != "y" ||
		route.Request().PostData() != "body" || string(route.Request().PostDataBytes()) != "body" ||
		route.Request().ResourceType() != "document" || !route.Request().IsNavigationRequest() {
		t.Fatalf("request wrapper mismatch")
	}
	if err := route.Continue(&ContinueOptions{Method: "PUT"}); err != nil {
		t.Fatal(err)
	}
	if err := route.Fulfill(&FulfillOptions{Status: 204, BodyString: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := route.Abort("failed"); err != nil {
		t.Fatal(err)
	}
	gotResp, err := route.Fetch(&FetchOptions{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]int
	if err := gotResp.JSON(&decoded); err != nil || decoded["a"] != 1 {
		t.Fatalf("json = %#v, %v", decoded, err)
	}
	if gotResp.URL() != resp.url || gotResp.Status() != 201 || gotResp.StatusText() != "Created" ||
		gotResp.Headers()["content-type"] != "application/json" || !gotResp.OK() ||
		gotResp.Request().URL() != req.url {
		t.Fatalf("response wrapper mismatch")
	}
	if text, err := gotResp.Text(); err != nil || text != `{"a":1}` {
		t.Fatalf("text = %q, %v", text, err)
	}
	loc := (&Page{raw: &fakePage{locator: &fakeLocator{text: "hello", count: 2}}}).Locator("#a")
	if count, err := loc.Count(context.Background()); err != nil || count != 2 {
		t.Fatalf("count = %d, %v", count, err)
	}
	if text, err := loc.TextContent(context.Background()); err != nil || text != "hello" {
		t.Fatalf("text = %q, %v", text, err)
	}
}

func TestRouteNilOptionsAndRawHelpers(t *testing.T) {
	route := &Route{raw: &fakeRoute{response: &fakeResponse{body: []byte(`{}`)}}}
	if err := route.Continue(nil); err != nil {
		t.Fatal(err)
	}
	if err := route.Fulfill(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := route.Fetch(nil); err != nil {
		t.Fatal(err)
	}
	if wrapRouteHandler(nil) != nil {
		t.Fatalf("nil route handler wrapped")
	}
	if key := newRouteKey("**/*", nil); key.handler != 0 {
		t.Fatalf("nil route key = %#v", key)
	}
	if raw := (&ElementHandle{raw: fakeElement{}}).Raw(); raw != nil {
		t.Fatalf("element raw = %#v", raw)
	}
}

func TestSidecarAdapterDelegates(t *testing.T) {
	adapter := sidecarAdapter{manager: sidecarpkg.New(sidecarpkg.Config{VenvDir: t.TempDir(), ConnectTimeout: time.Millisecond})}
	if _, err := adapter.Start(context.Background()); err == nil {
		t.Fatalf("adapter start err = %v", err)
	}
	adapter.Stop(context.Background())
	if info := adapter.Info(); info.PID != 0 {
		t.Fatalf("adapter info = %#v", info)
	}
}

func TestBrowserSidecarEmptyWhenMissing(t *testing.T) {
	if got := (&Browser{}).Sidecar(); got != (SidecarInfo{}) {
		t.Fatalf("sidecar = %#v", got)
	}
}

func bridgeRouteHandlerID(handler pwbridge.RouteHandler) uintptr {
	if handler == nil {
		return 0
	}
	return *(*uintptr)(unsafe.Pointer(&handler))
}

type fakeSidecar struct {
	endpoint string
	info     SidecarInfo
	stopped  bool
	err      error
}

func fakeSidecarFactory(s *fakeSidecar) func(launchConfig) (sidecarHandle, error) {
	return func(launchConfig) (sidecarHandle, error) { return s, nil }
}

func (s *fakeSidecar) Start(context.Context) (string, error) { return s.endpoint, s.err }
func (s *fakeSidecar) Stop(context.Context)                  { s.stopped = true }
func (s *fakeSidecar) Info() SidecarInfo                     { return s.info }

type fakeConnector struct {
	endpoint string
	session  *fakeSession
	err      error
}

func (c *fakeConnector) Connect(ctx context.Context, endpoint string, opts pwbridge.ConnectOptions) (pwbridge.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.endpoint = endpoint
	if c.err != nil {
		return nil, c.err
	}
	return c.session, nil
}

type fakeSession struct {
	browser *fakeBrowser
	stopped bool
}

func (s *fakeSession) Browser() pwbridge.Browser { return s.browser }
func (s *fakeSession) Stop() error {
	s.stopped = true
	return nil
}

type fakeBrowser struct {
	connected  bool
	contexts   []pwbridge.BrowserContext
	newCtx     *fakeContext
	newPage    *fakePage
	newCtxErr  error
	newPageErr error
}

func (b *fakeBrowser) Close() error                        { b.connected = false; return nil }
func (b *fakeBrowser) IsConnected() bool                   { return b.connected }
func (b *fakeBrowser) OnDisconnected(func())               {}
func (b *fakeBrowser) Contexts() []pwbridge.BrowserContext { return b.contexts }
func (b *fakeBrowser) NewContext(pwbridge.ContextOptions) (pwbridge.BrowserContext, error) {
	if b.newCtxErr != nil {
		return nil, b.newCtxErr
	}
	if b.newCtx == nil {
		b.newCtx = &fakeContext{}
	}
	return b.newCtx, nil
}
func (b *fakeBrowser) NewPage(pwbridge.ContextOptions) (pwbridge.Page, error) {
	if b.newPageErr != nil {
		return nil, b.newPageErr
	}
	if b.newPage == nil {
		b.newPage = &fakePage{}
	}
	return b.newPage, nil
}
func (b *fakeBrowser) Version() string { return "fake" }

type fakeContext struct {
	pages          []pwbridge.Page
	storage        *pwbridge.StorageState
	closed         bool
	newPageErr     error
	cookiesErr     error
	addCookiesErr  error
	clearErr       error
	storageErr     error
	routeErr       error
	unrouteErr     error
	closeErr       error
	routePattern   string
	routeHandler   pwbridge.RouteHandler
	unroutePattern string
	unrouteHandler pwbridge.RouteHandler
	unrouteCalls   int
	onRequest      func(pwbridge.Request)
	onResponse     func(pwbridge.Response)
}

func (c *fakeContext) NewPage() (pwbridge.Page, error) {
	if c.newPageErr != nil {
		return nil, c.newPageErr
	}
	p := &fakePage{}
	c.pages = append(c.pages, p)
	return p, nil
}
func (c *fakeContext) Pages() []pwbridge.Page { return c.pages }
func (c *fakeContext) Cookies(urls ...string) ([]pwbridge.Cookie, error) {
	if c.cookiesErr != nil {
		return nil, c.cookiesErr
	}
	return []pwbridge.Cookie{{Name: "cookie", Value: "value"}}, nil
}
func (c *fakeContext) AddCookies(cookies ...pwbridge.Cookie) error { return c.addCookiesErr }
func (c *fakeContext) ClearCookies() error                         { return c.clearErr }
func (c *fakeContext) StorageState() (*pwbridge.StorageState, error) {
	if c.storageErr != nil {
		return nil, c.storageErr
	}
	if c.storage == nil {
		c.storage = &pwbridge.StorageState{}
	}
	return c.storage, nil
}
func (c *fakeContext) Route(pattern string, handler pwbridge.RouteHandler) error {
	c.routePattern = pattern
	c.routeHandler = handler
	return c.routeErr
}
func (c *fakeContext) Unroute(pattern string, handler pwbridge.RouteHandler) error {
	c.unrouteCalls++
	c.unroutePattern = pattern
	c.unrouteHandler = handler
	return c.unrouteErr
}
func (c *fakeContext) OnRequest(fn func(pwbridge.Request))   { c.onRequest = fn }
func (c *fakeContext) OnResponse(fn func(pwbridge.Response)) { c.onResponse = fn }
func (c *fakeContext) Close() error {
	c.closed = true
	return c.closeErr
}
func (c *fakeContext) Raw() any { return c }

type fakePage struct {
	evaluateResult   any
	evaluateErr      error
	evaluateArg      any
	internalEvalArg  any
	locator          pwbridge.Locator
	response         pwbridge.Response
	gotoURL          string
	gotoOpts         pwbridge.GotoOptions
	backOpts         pwbridge.NavigateOptions
	forwardOpts      pwbridge.NavigateOptions
	reloadOpts       pwbridge.NavigateOptions
	initScript       string
	setContentHTML   string
	setContentOpts   pwbridge.GotoOptions
	loadState        string
	loadTimeout      time.Duration
	waitSelector     string
	waitSelectorOpts pwbridge.WaitForSelectorOptions
	waitURL          string
	waitURLOpts      pwbridge.GotoOptions
	screenshotOpts   pwbridge.ScreenshotOptions
	pdfOpts          pwbridge.PDFOptions
	routePattern     string
	routeHandler     pwbridge.RouteHandler
	unroutePattern   string
	unrouteHandler   pwbridge.RouteHandler
	unrouteCalls     int
	onRequest        func(pwbridge.Request)
	onRequestFailed  func(pwbridge.Request)
	onResponse       func(pwbridge.Response)
	onPageError      func(error)
	onConsole        func(pwbridge.ConsoleMessage)
	onDialog         func(pwbridge.Dialog)
	wheelX           float64
	wheelY           float64
	wheelErr         error
	gotoErr          error
	backErr          error
	forwardErr       error
	reloadErr        error
	initErr          error
	contentErr       error
	setContentErr    error
	titleErr         error
	loadErr          error
	waitSelectorErr  error
	waitURLErr       error
	screenshotErr    error
	pdfErr           error
	cookiesErr       error
	routeErr         error
	unrouteErr       error
	closeErr         error
	closeCalls       int
}

func (p *fakePage) Goto(url string, opts pwbridge.GotoOptions) (pwbridge.Response, error) {
	p.gotoURL = url
	p.gotoOpts = opts
	if p.gotoErr != nil {
		return nil, p.gotoErr
	}
	return p.resultResponse(), nil
}
func (p *fakePage) GoBack(opts pwbridge.NavigateOptions) (pwbridge.Response, error) {
	p.backOpts = opts
	if p.backErr != nil {
		return nil, p.backErr
	}
	return p.resultResponse(), nil
}
func (p *fakePage) GoForward(opts pwbridge.NavigateOptions) (pwbridge.Response, error) {
	p.forwardOpts = opts
	if p.forwardErr != nil {
		return nil, p.forwardErr
	}
	return p.resultResponse(), nil
}
func (p *fakePage) Reload(opts pwbridge.NavigateOptions) (pwbridge.Response, error) {
	p.reloadOpts = opts
	if p.reloadErr != nil {
		return nil, p.reloadErr
	}
	return p.resultResponse(), nil
}
func (p *fakePage) RunAndWaitForNavigation(action func() error, opts pwbridge.NavigateOptions) error {
	if err := action(); err != nil {
		return err
	}
	p.reloadOpts = opts
	if p.reloadErr != nil {
		return p.reloadErr
	}
	return nil
}
func (p *fakePage) Evaluate(_ string, args ...any) (any, error) {
	if len(args) > 0 {
		p.evaluateArg = args[0]
	}
	return p.evaluateResult, p.evaluateErr
}
func (p *fakePage) EvaluateInternal(_ string, args ...any) (any, error) {
	if len(args) > 0 {
		p.internalEvalArg = args[0]
	}
	return p.evaluateResult, p.evaluateErr
}
func (p *fakePage) AddInitScript(script string) error {
	p.initScript = script
	return p.initErr
}
func (p *fakePage) Content() (string, error) {
	if p.contentErr != nil {
		return "", p.contentErr
	}
	return "<html></html>", nil
}
func (p *fakePage) SetContent(html string, opts pwbridge.GotoOptions) error {
	p.setContentHTML = html
	p.setContentOpts = opts
	return p.setContentErr
}
func (p *fakePage) Title() (string, error) {
	if p.titleErr != nil {
		return "", p.titleErr
	}
	return "title", nil
}
func (p *fakePage) URL() string { return "https://example.com" }
func (p *fakePage) WaitForLoadState(state string, timeout time.Duration) error {
	p.loadState = state
	p.loadTimeout = timeout
	return p.loadErr
}
func (p *fakePage) WaitForSelector(selector string, opts pwbridge.WaitForSelectorOptions) (pwbridge.ElementHandle, error) {
	p.waitSelector = selector
	p.waitSelectorOpts = opts
	if p.waitSelectorErr != nil {
		return nil, p.waitSelectorErr
	}
	return &fakeElement{}, nil
}
func (p *fakePage) WaitForURL(pattern string, opts pwbridge.GotoOptions) error {
	p.waitURL = pattern
	p.waitURLOpts = opts
	return p.waitURLErr
}
func (p *fakePage) Screenshot(opts pwbridge.ScreenshotOptions) ([]byte, error) {
	p.screenshotOpts = opts
	if p.screenshotErr != nil {
		return nil, p.screenshotErr
	}
	return []byte("png"), nil
}
func (p *fakePage) PDF(opts pwbridge.PDFOptions) ([]byte, error) {
	p.pdfOpts = opts
	if p.pdfErr != nil {
		return nil, p.pdfErr
	}
	return []byte("pdf"), nil
}
func (p *fakePage) Cookies(urls ...string) ([]pwbridge.Cookie, error) {
	if p.cookiesErr != nil {
		return nil, p.cookiesErr
	}
	return []pwbridge.Cookie{}, nil
}
func (p *fakePage) Route(pattern string, handler pwbridge.RouteHandler) error {
	p.routePattern = pattern
	p.routeHandler = handler
	return p.routeErr
}
func (p *fakePage) Unroute(pattern string, handler pwbridge.RouteHandler) error {
	p.unrouteCalls++
	p.unroutePattern = pattern
	p.unrouteHandler = handler
	return p.unrouteErr
}
func (p *fakePage) OnRequest(fn func(pwbridge.Request))        { p.onRequest = fn }
func (p *fakePage) OnRequestFailed(fn func(pwbridge.Request))  { p.onRequestFailed = fn }
func (p *fakePage) OnResponse(fn func(pwbridge.Response))      { p.onResponse = fn }
func (p *fakePage) OnPageError(fn func(error))                 { p.onPageError = fn }
func (p *fakePage) OnConsole(fn func(pwbridge.ConsoleMessage)) { p.onConsole = fn }
func (p *fakePage) OnDialog(fn func(pwbridge.Dialog))          { p.onDialog = fn }
func (p *fakePage) Wheel(deltaX, deltaY float64) error {
	p.wheelX = deltaX
	p.wheelY = deltaY
	return p.wheelErr
}
func (p *fakePage) Locator(string) pwbridge.Locator {
	if p.locator == nil {
		p.locator = &fakeLocator{}
	}
	return p.locator
}
func (p *fakePage) Close() error {
	p.closeCalls++
	return p.closeErr
}
func (p *fakePage) Raw() any { return p }
func (p *fakePage) resultResponse() pwbridge.Response {
	if p.response != nil {
		return p.response
	}
	return &fakeResponse{}
}

type fakeElement struct{}

func (fakeElement) Raw() any { return nil }

type fakeDialog struct {
	typ          string
	message      string
	defaultValue string
	acceptText   string
	accepted     bool
	dismissed    bool
	acceptErr    error
	dismissErr   error
}

func (d *fakeDialog) Type() string         { return d.typ }
func (d *fakeDialog) Message() string      { return d.message }
func (d *fakeDialog) DefaultValue() string { return d.defaultValue }
func (d *fakeDialog) Accept(promptText ...string) error {
	d.accepted = true
	if len(promptText) > 0 {
		d.acceptText = promptText[0]
	}
	return d.acceptErr
}
func (d *fakeDialog) Dismiss() error {
	d.dismissed = true
	return d.dismissErr
}

type fakeRoute struct {
	request  pwbridge.Request
	response pwbridge.Response
	fetchErr error
}

func (r *fakeRoute) Request() pwbridge.Request                { return r.request }
func (r *fakeRoute) Continue(*pwbridge.ContinueOptions) error { return nil }
func (r *fakeRoute) Fulfill(*pwbridge.FulfillOptions) error   { return nil }
func (r *fakeRoute) Abort(string) error                       { return nil }
func (r *fakeRoute) Fetch(*pwbridge.FetchOptions) (pwbridge.Response, error) {
	return r.response, r.fetchErr
}

type fakeRequest struct {
	url     string
	method  string
	headers map[string]string
	post    string
}

func (r *fakeRequest) URL() string                { return r.url }
func (r *fakeRequest) Method() string             { return r.method }
func (r *fakeRequest) Headers() map[string]string { return r.headers }
func (r *fakeRequest) PostData() string           { return r.post }
func (r *fakeRequest) PostDataBytes() []byte      { return []byte(r.post) }
func (r *fakeRequest) ResourceType() string       { return "document" }
func (r *fakeRequest) IsNavigationRequest() bool  { return true }

type fakeResponse struct {
	url     string
	status  int
	text    string
	body    []byte
	request pwbridge.Request
	bodyErr error
}

func (r *fakeResponse) URL() string        { return r.url }
func (r *fakeResponse) Status() int        { return r.status }
func (r *fakeResponse) StatusText() string { return "Created" }
func (r *fakeResponse) Headers() map[string]string {
	return map[string]string{"content-type": "application/json"}
}
func (r *fakeResponse) Body() ([]byte, error)     { return r.body, r.bodyErr }
func (r *fakeResponse) Text() (string, error)     { return r.text, nil }
func (r *fakeResponse) OK() bool                  { return r.status >= 200 && r.status < 300 }
func (r *fakeResponse) Request() pwbridge.Request { return r.request }

type fakeLocator struct {
	text           string
	count          int
	clickOpts      pwbridge.LocatorClickOptions
	fillValue      string
	fillOpts       pwbridge.LocatorFillOptions
	typeValue      string
	typeOpts       pwbridge.LocatorTypeOptions
	pressKey       string
	pressOpts      pwbridge.LocatorPressOptions
	hoverOpts      pwbridge.LocatorHoverOptions
	scrollOpts     pwbridge.LocatorOptions
	selectOpts     pwbridge.LocatorSelectOptions
	selectResult   []string
	checked        bool
	checkedOpts    pwbridge.LocatorSetCheckedOptions
	inputFiles     []string
	inputFilesOpts pwbridge.LocatorSetInputFilesOptions
	optionOpts     pwbridge.LocatorOptions
	attrName       string
	waitOpts       pwbridge.LocatorWaitForOptions
	screenshotOpts pwbridge.ScreenshotOptions
}

func (l *fakeLocator) Click(opts pwbridge.LocatorClickOptions) error { l.clickOpts = opts; return nil }
func (l *fakeLocator) Fill(value string, opts pwbridge.LocatorFillOptions) error {
	l.fillValue = value
	l.fillOpts = opts
	return nil
}
func (l *fakeLocator) Type(value string, opts pwbridge.LocatorTypeOptions) error {
	l.typeValue = value
	l.typeOpts = opts
	return nil
}
func (l *fakeLocator) Press(key string, opts pwbridge.LocatorPressOptions) error {
	l.pressKey = key
	l.pressOpts = opts
	return nil
}
func (l *fakeLocator) Hover(opts pwbridge.LocatorHoverOptions) error {
	l.hoverOpts = opts
	return nil
}
func (l *fakeLocator) ScrollIntoViewIfNeeded(opts pwbridge.LocatorOptions) error {
	l.scrollOpts = opts
	return nil
}
func (l *fakeLocator) SelectOption(opts pwbridge.LocatorSelectOptions) ([]string, error) {
	l.selectOpts = opts
	return l.selectResult, nil
}
func (l *fakeLocator) SetChecked(checked bool, opts pwbridge.LocatorSetCheckedOptions) error {
	l.checked = checked
	l.checkedOpts = opts
	return nil
}
func (l *fakeLocator) SetInputFiles(files []string, opts pwbridge.LocatorSetInputFilesOptions) error {
	l.inputFiles = append([]string(nil), files...)
	l.inputFilesOpts = opts
	return nil
}
func (l *fakeLocator) TextContent(opts pwbridge.LocatorOptions) (string, error) {
	l.optionOpts = opts
	return l.text, nil
}
func (l *fakeLocator) InnerHTML(opts pwbridge.LocatorOptions) (string, error) {
	l.optionOpts = opts
	return "<b>" + l.text + "</b>", nil
}
func (l *fakeLocator) GetAttribute(name string, opts pwbridge.LocatorOptions) (string, error) {
	l.attrName = name
	l.optionOpts = opts
	return "attr", nil
}
func (l *fakeLocator) IsVisible(opts pwbridge.LocatorOptions) (bool, error) {
	l.optionOpts = opts
	return true, nil
}
func (l *fakeLocator) Count() (int, error)      { return l.count, nil }
func (l *fakeLocator) First() pwbridge.Locator  { return l }
func (l *fakeLocator) Last() pwbridge.Locator   { return l }
func (l *fakeLocator) Nth(int) pwbridge.Locator { return l }
func (l *fakeLocator) WaitFor(opts pwbridge.LocatorWaitForOptions) error {
	l.waitOpts = opts
	return nil
}
func (l *fakeLocator) Screenshot(opts pwbridge.ScreenshotOptions) ([]byte, error) {
	l.screenshotOpts = opts
	return []byte("shot"), nil
}

func TestBrowserFetchErrorFormatting(t *testing.T) {
	err := &BrowserFetchError{Code: "cors_denied", URL: "https://x", Method: "GET", Status: 403, BodyPreview: []byte("abc")}
	if !errors.Is(err, ErrBrowserFetch) {
		t.Fatalf("unwrap failed")
	}
	_ = err.Error()
	var nilErr *BrowserFetchError
	if nilErr.Error() != ErrBrowserFetch.Error() {
		t.Fatalf("nil error string = %q", nilErr.Error())
	}
	if got := (&BrowserFetchError{URL: "https://x", Method: "GET"}).Error(); !strings.Contains(got, "blocked before response body") {
		t.Fatalf("default message = %q", got)
	}
}

func TestEvaluateIntoJSON(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{"n": float64(2)}}}
	var dst struct {
		N int `json:"n"`
	}
	if err := page.EvaluateIntoJSON(context.Background(), "expr", &dst); err != nil {
		t.Fatal(err)
	}
	if dst.N != 2 {
		t.Fatalf("dst = %#v", dst)
	}
	page.raw = &fakePage{evaluateErr: errors.New("evaluate failed")}
	if err := page.EvaluateIntoJSON(context.Background(), "expr", &dst); err == nil {
		t.Fatal("evaluate error succeeded")
	}
	page.raw = &fakePage{evaluateResult: func() {}}
	if err := page.EvaluateIntoJSON(context.Background(), "expr", &dst); err == nil {
		t.Fatal("marshal error succeeded")
	}
	page.raw = &fakePage{evaluateResult: map[string]any{"n": "not an int"}}
	if err := page.EvaluateIntoJSON(context.Background(), "expr", &dst); err == nil {
		t.Fatal("unmarshal error succeeded")
	}
}

func TestContextCookieConversions(t *testing.T) {
	ctx := &Context{raw: &fakeContext{}}
	cookies, err := ctx.Cookies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 1 || cookies[0].Value != "value" {
		t.Fatalf("cookies = %#v", cookies)
	}
	if err := ctx.AddCookies(context.Background(), Cookie{Name: "a", Value: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := ctx.ClearCookies(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResponseJSONUsesBody(t *testing.T) {
	resp := &Response{raw: &fakeResponse{body: []byte(`{"x":3}`)}}
	var dst map[string]int
	if err := resp.JSON(&dst); err != nil {
		t.Fatal(err)
	}
	if dst["x"] != 3 {
		t.Fatalf("dst = %#v", dst)
	}
}

func TestFetchBytesMarshalsArbitraryEvaluateResult(t *testing.T) {
	body := map[string]any{"ok": true, "status": 200, "body": "abc", "url": "https://example.com", "headers": map[string]string{}}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var asAny any
	if err := json.Unmarshal(data, &asAny); err != nil {
		t.Fatal(err)
	}
	page := &Page{raw: &fakePage{evaluateResult: asAny}}
	status, got, err := page.FetchBytes(context.Background(), "https://example.com", "", nil, nil)
	if err != nil || status != 200 || string(got) != "abc" {
		t.Fatalf("fetch = %d %q %v", status, got, err)
	}
}

func TestFetchHeadersForEvaluationUsesSerializableMap(t *testing.T) {
	if got := fetchHeadersForEvaluation(nil); len(got) != 0 {
		t.Fatalf("nil headers = %#v", got)
	}
	got := fetchHeadersForEvaluation(map[string]string{"X-Test": "ok"})
	if got["X-Test"] != "ok" {
		t.Fatalf("headers = %#v", got)
	}
}

func TestFetchBytesWithOptionsReportsBrowserSideTruncation(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{"ok": true, "status": 200, "body": "abcd", "url": "https://example.com", "headers": map[string]string{}, "truncated": true}}}
	result, err := page.FetchBytesWithOptions(context.Background(), "https://example.com", "", nil, nil, FetchBytesOptions{MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != 200 || string(result.Body) != "abcd" || !result.Truncated {
		t.Fatalf("result = %#v", result)
	}
	if arg, ok := page.raw.(*fakePage).internalEvalArg.(map[string]any); !ok || arg["maxBytes"] != 4 {
		t.Fatalf("internal evaluate arg = %#v", page.raw.(*fakePage).internalEvalArg)
	}
}

func TestFetchBytesWithOptionsPostTruncatesDecodedBody(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{"ok": true, "status": 200, "body": "abcdef", "url": "https://example.com", "headers": map[string]string{}, "truncated": false}}}
	result, err := page.FetchBytesWithOptions(context.Background(), "https://example.com", "", nil, nil, FetchBytesOptions{MaxBytes: 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Body) != "abc" || !result.Truncated {
		t.Fatalf("post-truncated result = %#v", result)
	}
}

func TestLegacyFetchBytesErrorsOnDefaultCapTruncation(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{"ok": true, "status": 200, "body": "abcd", "url": "https://example.com", "headers": map[string]string{}, "truncated": true}}}
	status, body, err := page.FetchBytes(context.Background(), "https://example.com", "", nil, nil)
	var fetchErr *BrowserFetchError
	if !errors.As(err, &fetchErr) || fetchErr.Code != "response_too_large" || status != 200 || string(body) != "abcd" {
		t.Fatalf("legacy fetch status=%d body=%q err=%#v", status, body, err)
	}
	if arg, ok := page.raw.(*fakePage).internalEvalArg.(map[string]any); !ok || arg["maxBytes"] != policy.DefaultMaxResponseBytes {
		t.Fatalf("internal evaluate arg = %#v", page.raw.(*fakePage).internalEvalArg)
	}
}

func TestFetchBytesWithOptionsAllowsExplicitUnboundedFetch(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{"ok": true, "status": 200, "body": "abcdef", "url": "https://example.com", "headers": map[string]string{}, "truncated": false}}}
	result, err := page.FetchBytesWithOptions(context.Background(), "https://example.com", "", nil, nil, FetchBytesOptions{MaxBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Body) != "abcdef" || result.Truncated {
		t.Fatalf("unbounded result = %#v", result)
	}
	if arg, ok := page.raw.(*fakePage).internalEvalArg.(map[string]any); !ok || arg["maxBytes"] != 0 {
		t.Fatalf("internal evaluate arg = %#v", page.raw.(*fakePage).internalEvalArg)
	}
}

func TestFetchBytesWithOptionsRejectsTooLargeCap(t *testing.T) {
	page := &Page{raw: &fakePage{}}
	if _, err := page.FetchBytesWithOptions(context.Background(), "https://example.com", "", nil, nil, FetchBytesOptions{MaxBytes: policy.HardMaxResponseBytes + 1}); err == nil {
		t.Fatal("too-large fetch cap succeeded")
	}
}

func TestBrowserFetchExpressionStreamsAndCancelsAtCap(t *testing.T) {
	if strings.Contains(browserFetchExpression, "response.text()") {
		t.Fatal("browser fetch expression materializes full response text")
	}
	output := runRootNodeExpression(t, `
const browserFetchExpression = `+browserFetchExpression+`;
let cancelCalls = 0;
let readCalls = 0;
const chunks = [
  new Uint8Array([97, 98, 99]),
  new Uint8Array([100, 101, 102]),
  new Uint8Array([103, 104, 105])
];
globalThis.fetch = async (url) => ({
  url,
  status: 200,
  headers: {forEach: (callback) => callback("text/plain", "content-type")},
  body: {getReader: () => ({
    read: async () => {
      readCalls++;
      return chunks.length ? {done: false, value: chunks.shift()} : {done: true};
    },
    cancel: async () => { cancelCalls++; }
  })}
});
browserFetchExpression({url: "https://example.com/stream", method: "GET", headers: {}, body: "", maxBytes: 5})
  .then(result => console.log(JSON.stringify({result, cancelCalls, readCalls})))
  .catch(error => { console.error(error); process.exit(1); });
`)
	var got struct {
		Result      map[string]any `json:"result"`
		CancelCalls int            `json:"cancelCalls"`
		ReadCalls   int            `json:"readCalls"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if got.Result["body"] != "abcde" || got.Result["truncated"] != true || got.CancelCalls != 1 || got.ReadCalls != 2 {
		t.Fatalf("stream result = %#v", got)
	}
}

func TestBrowserFetchExpressionCancelsAtExactCapWithoutReadAhead(t *testing.T) {
	output := runRootNodeExpression(t, `
const browserFetchExpression = `+browserFetchExpression+`;
let cancelCalls = 0;
let readCalls = 0;
globalThis.fetch = async (url) => ({
  url,
  status: 200,
  headers: {forEach: () => {}},
  body: {getReader: () => ({
    read: async () => {
      readCalls++;
      if (readCalls > 1) throw new Error("read after cap");
      return {done: false, value: new Uint8Array([97, 98, 99, 100, 101])};
    },
    cancel: async () => { cancelCalls++; }
  })}
});
browserFetchExpression({url: "https://example.com/exact", method: "GET", headers: {}, body: "", maxBytes: 5})
  .then(result => console.log(JSON.stringify({result, cancelCalls, readCalls})))
  .catch(error => { console.error(error); process.exit(1); });
`)
	var got struct {
		Result      map[string]any `json:"result"`
		CancelCalls int            `json:"cancelCalls"`
		ReadCalls   int            `json:"readCalls"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if got.Result["body"] != "abcde" || got.Result["truncated"] != true || got.CancelCalls != 1 || got.ReadCalls != 1 {
		t.Fatalf("exact-cap result = %#v", got)
	}
}

func TestBrowserFetchExpressionCapsHugeChunkAndCancels(t *testing.T) {
	output := runRootNodeExpression(t, `
const browserFetchExpression = `+browserFetchExpression+`;
let cancelCalls = 0;
const huge = new Uint8Array(1024 * 1024);
huge[0] = 65;
huge[1] = 66;
huge[2] = 67;
globalThis.fetch = async (url) => ({
  url,
  status: 200,
  headers: {forEach: () => {}},
  body: {getReader: () => ({
    read: async () => ({done: false, value: huge}),
    cancel: async () => { cancelCalls++; }
  })}
});
browserFetchExpression({url: "https://example.com/huge", method: "GET", headers: {}, body: "", maxBytes: 3})
  .then(result => console.log(JSON.stringify({result, cancelCalls})))
  .catch(error => { console.error(error); process.exit(1); });
`)
	var got struct {
		Result      map[string]any `json:"result"`
		CancelCalls int            `json:"cancelCalls"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if got.Result["body"] != "ABC" || got.Result["truncated"] != true || got.CancelCalls != 1 {
		t.Fatalf("huge result = %#v", got)
	}
}

func TestBrowserFetchExpressionTreatsBodylessResponseAsEmptySuccess(t *testing.T) {
	output := runRootNodeExpression(t, `
const browserFetchExpression = `+browserFetchExpression+`;
globalThis.fetch = async (url) => ({
  url,
  status: 204,
  headers: {forEach: () => {}},
  body: null
});
browserFetchExpression({url: "https://example.com/empty", method: "HEAD", headers: {}, body: "", maxBytes: 5})
  .then(result => console.log(JSON.stringify(result)))
  .catch(error => { console.error(error); process.exit(1); });
`)
	var got map[string]any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if got["ok"] != true || got["body"] != "" || got["status"] != float64(204) || got["truncated"] != false {
		t.Fatalf("bodyless result = %#v", got)
	}
}

func TestFetchJSONWithOptionsUsesBoundedErrorPreview(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{
		"ok":        true,
		"status":    500,
		"body":      strings.Repeat("x", 128),
		"url":       "https://example.com",
		"headers":   map[string]string{},
		"truncated": true,
	}}}
	var dst map[string]any
	err := page.FetchJSONWithOptions(context.Background(), "https://example.com", "", nil, nil, &dst, FetchBytesOptions{MaxBytes: 128})
	var fetchErr *BrowserFetchError
	if !errors.As(err, &fetchErr) || len(fetchErr.BodyPreview) != 128 {
		t.Fatalf("fetch json bounded error = %#v", err)
	}
}

func TestFetchJSONWithOptionsErrorsOnTruncatedSuccessBeforeDecode(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{
		"ok":        true,
		"status":    200,
		"body":      `{"partial"`,
		"url":       "https://example.com",
		"headers":   map[string]string{},
		"truncated": true,
	}}}
	var dst map[string]any
	err := page.FetchJSONWithOptions(context.Background(), "https://example.com", "", nil, nil, &dst, FetchBytesOptions{MaxBytes: 10})
	var fetchErr *BrowserFetchError
	if !errors.As(err, &fetchErr) || fetchErr.Code != "response_too_large" {
		t.Fatalf("truncated json error = %#v", err)
	}
}

func runRootNodeExpression(t *testing.T, script string) []byte {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	output, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node expression failed: %v\n%s", err, output)
	}
	return output
}

func TestWrapperCanceledContexts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page := &Page{raw: &fakePage{}}
	for name, fn := range map[string]func() error{
		"goto": func() error {
			_, err := page.Goto(ctx, "https://example.com")
			return err
		},
		"back": func() error {
			_, err := page.GoBack(ctx)
			return err
		},
		"forward": func() error {
			_, err := page.GoForward(ctx)
			return err
		},
		"reload": func() error {
			_, err := page.Reload(ctx)
			return err
		},
		"run-and-wait-navigation": func() error {
			return page.RunAndWaitForNavigation(ctx, func() error { return nil })
		},
		"evaluate": func() error {
			_, err := page.Evaluate(ctx, "1")
			return err
		},
		"evaluate-internal": func() error {
			_, err := page.EvaluateInternal(ctx, "1")
			return err
		},
		"init":          func() error { return page.AddInitScript(ctx, "x") },
		"content":       func() error { _, err := page.Content(ctx); return err },
		"set-content":   func() error { return page.SetContent(ctx, "<p>x</p>") },
		"title":         func() error { _, err := page.Title(ctx); return err },
		"load-state":    func() error { return page.WaitForLoadState(ctx, "load") },
		"selector":      func() error { _, err := page.WaitForSelector(ctx, "#x"); return err },
		"wait-url":      func() error { return page.WaitForURL(ctx, "**/*") },
		"screenshot":    func() error { _, err := page.Screenshot(ctx); return err },
		"pdf":           func() error { _, err := page.PDF(ctx); return err },
		"cookies":       func() error { _, err := page.Cookies(ctx); return err },
		"route":         func() error { return page.Route(ctx, "**/*", func(*Route) {}) },
		"unroute":       func() error { return page.Unroute(ctx, "**/*", nil) },
		"fetch-bytes":   func() error { _, _, err := page.FetchBytes(ctx, "https://example.com", "", nil, nil); return err },
		"locator-fill":  func() error { return page.Locator("#x").Fill(ctx, "x") },
		"locator-text":  func() error { _, err := page.Locator("#x").TextContent(ctx); return err },
		"locator-count": func() error { _, err := page.Locator("#x").Count(ctx); return err },
		"locator-wait":  func() error { return page.Locator("#x").WaitFor(ctx) },
		"locator-shot":  func() error { _, err := page.Locator("#x").Screenshot(ctx); return err },
	} {
		if err := fn(); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s canceled err = %v", name, err)
		}
	}

	rawCtx := &fakeContext{}
	wrapped := &Context{raw: rawCtx}
	for name, fn := range map[string]func() error{
		"new-page":    func() error { _, err := wrapped.NewPage(ctx); return err },
		"cookies":     func() error { _, err := wrapped.Cookies(ctx); return err },
		"add-cookies": func() error { return wrapped.AddCookies(ctx, Cookie{Name: "a"}) },
		"clear":       func() error { return wrapped.ClearCookies(ctx) },
		"storage":     func() error { _, err := wrapped.StorageState(ctx, ""); return err },
		"route":       func() error { return wrapped.Route(ctx, "**/*", func(*Route) {}) },
		"unroute":     func() error { return wrapped.Unroute(ctx, "**/*", nil) },
	} {
		if err := fn(); !errors.Is(err, context.Canceled) {
			t.Fatalf("context %s canceled err = %v", name, err)
		}
	}
}

func TestWrapperRawErrorPropagation(t *testing.T) {
	boom := errors.New("bridge failed")
	fp := &fakePage{
		gotoErr:         boom,
		backErr:         boom,
		forwardErr:      boom,
		reloadErr:       boom,
		evaluateErr:     boom,
		initErr:         boom,
		contentErr:      boom,
		setContentErr:   boom,
		titleErr:        boom,
		loadErr:         boom,
		waitSelectorErr: boom,
		waitURLErr:      boom,
		screenshotErr:   boom,
		pdfErr:          boom,
		cookiesErr:      boom,
		routeErr:        boom,
		unrouteErr:      boom,
		closeErr:        boom,
	}
	page := &Page{raw: fp}
	for name, fn := range map[string]func() error{
		"goto": func() error {
			_, err := page.Goto(context.Background(), "https://example.com")
			return err
		},
		"back": func() error {
			_, err := page.GoBack(context.Background())
			return err
		},
		"forward": func() error {
			_, err := page.GoForward(context.Background())
			return err
		},
		"reload": func() error {
			_, err := page.Reload(context.Background())
			return err
		},
		"run-and-wait-navigation": func() error {
			return page.RunAndWaitForNavigation(context.Background(), func() error { return nil })
		},
		"evaluate":      func() error { _, err := page.Evaluate(context.Background(), "1"); return err },
		"eval-internal": func() error { _, err := page.EvaluateInternal(context.Background(), "1"); return err },
		"init":          func() error { return page.AddInitScript(context.Background(), "x") },
		"content":       func() error { _, err := page.Content(context.Background()); return err },
		"set-content":   func() error { return page.SetContent(context.Background(), "<p>x</p>") },
		"title":         func() error { _, err := page.Title(context.Background()); return err },
		"load-state":    func() error { return page.WaitForLoadState(context.Background(), "load") },
		"selector":      func() error { _, err := page.WaitForSelector(context.Background(), "#x"); return err },
		"wait-url":      func() error { return page.WaitForURL(context.Background(), "**/*") },
		"screenshot":    func() error { _, err := page.Screenshot(context.Background()); return err },
		"screenshot-to": func() error { return page.ScreenshotToFile(context.Background(), filepath.Join(t.TempDir(), "x.png")) },
		"pdf":           func() error { _, err := page.PDF(context.Background()); return err },
		"cookies":       func() error { _, err := page.Cookies(context.Background()); return err },
		"route":         func() error { return page.Route(context.Background(), "**/*", func(*Route) {}) },
		"close":         func() error { return page.Close() },
	} {
		if err := fn(); !errors.Is(err, boom) {
			t.Fatalf("%s raw err = %v", name, err)
		}
	}
	if len(page.routes) != 0 {
		t.Fatalf("failed route left registry entries: %#v", page.routes)
	}
	page.routes = map[routeKey]pwbridge.RouteHandler{newRouteKey("**/*", func(*Route) {}): wrapRouteHandler(func(*Route) {})}
	if err := page.Unroute(context.Background(), "**/*", nil); !errors.Is(err, boom) {
		t.Fatalf("unroute raw err = %v", err)
	}

	cerr := errors.New("context close failed")
	ownedRaw := &fakePage{}
	owned := &Page{raw: ownedRaw, ownsContext: true, context: &Context{raw: &fakeContext{closeErr: cerr}}}
	if err := owned.Close(); !errors.Is(err, cerr) {
		t.Fatalf("owned close err = %v", err)
	}
	if ownedRaw.closeCalls != 0 {
		t.Fatalf("owned close called raw page %d times, want 0", ownedRaw.closeCalls)
	}

	rawCtx := &fakeContext{
		cookiesErr:    boom,
		addCookiesErr: boom,
		clearErr:      boom,
		storageErr:    boom,
		routeErr:      boom,
		unrouteErr:    boom,
		closeErr:      boom,
	}
	wrapped := &Context{raw: rawCtx}
	for name, fn := range map[string]func() error{
		"cookies":     func() error { _, err := wrapped.Cookies(context.Background()); return err },
		"add-cookies": func() error { return wrapped.AddCookies(context.Background(), Cookie{Name: "a"}) },
		"clear":       func() error { return wrapped.ClearCookies(context.Background()) },
		"storage":     func() error { _, err := wrapped.StorageState(context.Background(), ""); return err },
		"route":       func() error { return wrapped.Route(context.Background(), "**/*", func(*Route) {}) },
		"close":       func() error { return wrapped.Close() },
	} {
		if err := fn(); !errors.Is(err, boom) {
			t.Fatalf("context %s raw err = %v", name, err)
		}
	}
	if len(wrapped.routes) != 0 {
		t.Fatalf("failed context route left registry entries: %#v", wrapped.routes)
	}
	wrapped.routes = map[routeKey]pwbridge.RouteHandler{newRouteKey("**/*", func(*Route) {}): wrapRouteHandler(func(*Route) {})}
	if err := wrapped.Unroute(context.Background(), "**/*", nil); !errors.Is(err, boom) {
		t.Fatalf("context unroute raw err = %v", err)
	}
}

func TestResponseRouteFetchAndFileErrorEdges(t *testing.T) {
	boom := errors.New("body failed")
	resp := &Response{raw: &fakeResponse{bodyErr: boom}}
	var dst map[string]any
	if err := resp.JSON(&dst); !errors.Is(err, boom) {
		t.Fatalf("body err = %v", err)
	}
	resp = &Response{raw: &fakeResponse{body: []byte("{")}}
	if err := resp.JSON(&dst); err == nil {
		t.Fatal("invalid response JSON decoded")
	}
	route := &Route{raw: &fakeRoute{fetchErr: boom}}
	if _, err := route.Fetch(&FetchOptions{}); !errors.Is(err, boom) {
		t.Fatalf("route fetch err = %v", err)
	}

	parentFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	page := &Page{raw: &fakePage{}}
	if err := page.ScreenshotToFile(context.Background(), filepath.Join(parentFile, "shot.png")); err == nil {
		t.Fatal("screenshot write under file parent succeeded")
	}
	existingDir := filepath.Join(t.TempDir(), "state-dir")
	if err := os.Mkdir(existingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON0600(existingDir, map[string]int{"x": 1}); err == nil {
		t.Fatal("writeJSON renamed over directory")
	}
	existingDir = filepath.Join(t.TempDir(), "bytes-dir")
	if err := os.Mkdir(existingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeBytes0600(existingDir, []byte("x")); err == nil {
		t.Fatal("writeBytes wrote over directory")
	}
}

func TestStorageStateAndAtomicWriteHookedErrors(t *testing.T) {
	boom := errors.New("boom")
	t.Run("storage write", func(t *testing.T) {
		defer restoreFileHooks()()
		fileCreateTemp = func(string, string) (atomicFile, error) { return nil, boom }
		ctx := &Context{raw: &fakeContext{storage: &pwbridge.StorageState{}}}
		if _, err := ctx.StorageState(context.Background(), filepath.Join(t.TempDir(), "state.json")); !errors.Is(err, boom) {
			t.Fatalf("storage write err = %v", err)
		}
	})

	for _, helper := range []struct {
		name string
		run  func(string) error
	}{
		{"json", func(path string) error { return writeJSON0600(path, map[string]int{"x": 1}) }},
		{"bytes", func(path string) error { return writeBytes0600(path, []byte("x")) }},
	} {
		t.Run(helper.name+" create", func(t *testing.T) {
			defer restoreFileHooks()()
			fileCreateTemp = func(string, string) (atomicFile, error) { return nil, boom }
			if err := helper.run(filepath.Join(t.TempDir(), "out")); !errors.Is(err, boom) {
				t.Fatalf("create err = %v", err)
			}
		})
		t.Run(helper.name+" chmod", func(t *testing.T) {
			defer restoreFileHooks()()
			fileCreateTemp = func(string, string) (atomicFile, error) { return &fakeAtomicFile{chmodErr: boom}, nil }
			if err := helper.run(filepath.Join(t.TempDir(), "out")); !errors.Is(err, boom) {
				t.Fatalf("chmod err = %v", err)
			}
		})
		t.Run(helper.name+" write", func(t *testing.T) {
			defer restoreFileHooks()()
			fileCreateTemp = func(string, string) (atomicFile, error) { return &fakeAtomicFile{writeErr: boom}, nil }
			if err := helper.run(filepath.Join(t.TempDir(), "out")); !errors.Is(err, boom) {
				t.Fatalf("write err = %v", err)
			}
		})
		t.Run(helper.name+" close", func(t *testing.T) {
			defer restoreFileHooks()()
			fileCreateTemp = func(string, string) (atomicFile, error) { return &fakeAtomicFile{closeErr: boom}, nil }
			if err := helper.run(filepath.Join(t.TempDir(), "out")); !errors.Is(err, boom) {
				t.Fatalf("close err = %v", err)
			}
		})
	}
}

func TestBrowserFetchAdditionalErrorEdges(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{"ok": true, "status": 404, "body": strings.Repeat("x", 600), "url": "https://example.com"}}}
	var dst map[string]any
	err := page.FetchJSON(context.Background(), "https://example.com", "", nil, nil, &dst)
	var fetchErr *BrowserFetchError
	if !errors.As(err, &fetchErr) || len(fetchErr.BodyPreview) != 512 {
		t.Fatalf("non-2xx fetch err = %#v", err)
	}
	page.raw = &fakePage{evaluateErr: errors.New("evaluate failed")}
	if err := page.FetchJSON(context.Background(), "https://example.com", "", nil, nil, &dst); !errors.As(err, &fetchErr) || fetchErr.Code != "network_error" {
		t.Fatalf("fetch json evaluate err = %#v", err)
	}
	if _, _, err := page.FetchBytes(context.Background(), "https://example.com", "", nil, nil); !errors.As(err, &fetchErr) || fetchErr.Code != "network_error" {
		t.Fatalf("evaluate fetch err = %#v", err)
	}
	page.raw = &fakePage{evaluateResult: map[string]any{"bad": func() {}}}
	if _, _, err := page.FetchBytes(context.Background(), "https://example.com", "", nil, nil); err == nil {
		t.Fatal("unmarshalable fetch result succeeded")
	}
	page.raw = &fakePage{evaluateResult: "not an object"}
	if _, _, err := page.FetchBytes(context.Background(), "https://example.com", "", nil, nil); err == nil {
		t.Fatal("non-object fetch payload succeeded")
	}
	if got := previewBytes([]byte(strings.Repeat("x", 513))); len(got) != 512 {
		t.Fatalf("preview length = %d", len(got))
	}
	large := []byte(strings.Repeat("x", 600))
	preview := previewBytes(large)
	large[0] = 'y'
	if preview[0] != 'x' || cap(preview) != len(preview) {
		t.Fatalf("preview retained mutable backing array len=%d cap=%d first=%q", len(preview), cap(preview), preview[0])
	}
	short := []byte("abc")
	got := previewBytes(short)
	short[0] = 'z'
	if string(got) != "abc" {
		t.Fatalf("short preview = %q", got)
	}
}

func restoreFileHooks() func() {
	oldMkdirAll := fileMkdirAll
	oldCreateTemp := fileCreateTemp
	oldRemove := fileRemove
	oldRename := fileRename
	return func() {
		fileMkdirAll = oldMkdirAll
		fileCreateTemp = oldCreateTemp
		fileRemove = oldRemove
		fileRename = oldRename
	}
}

type fakeAtomicFile struct {
	chmodErr error
	writeErr error
	closeErr error
}

func (f *fakeAtomicFile) Name() string { return "fake-atomic-file" }

func (f *fakeAtomicFile) Chmod(os.FileMode) error { return f.chmodErr }

func (f *fakeAtomicFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *fakeAtomicFile) Close() error { return f.closeErr }
