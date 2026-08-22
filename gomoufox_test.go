package gomoufox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/ehmo/gomoufox/camoufoxcfg"
	"github.com/ehmo/gomoufox/internal/netguard"
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
	installed := false
	sidecarEnsureInstalled = func(ctx context.Context, opts sidecarpkg.InstallOptions) error {
		installed = true
		if opts.PythonBin != "python3.12" || opts.VenvDir != "/venv" || opts.Runtime != string(SidecarRuntimeNodeDirect) {
			t.Fatalf("install opts = %#v", opts)
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

func TestNewPreparesConnectorWhileSidecarStarts(t *testing.T) {
	prepareStarted := make(chan struct{})
	allowPrepare := make(chan struct{})
	prepared := &fakePreparedConnector{session: &fakeSession{browser: &fakeBrowser{connected: true}}}
	connector := &fakePreparableConnector{
		started:  prepareStarted,
		release:  allowPrepare,
		prepared: prepared,
	}
	sidecar := &fakeSidecar{
		endpoint: "ws://localhost:1234/token",
		startFn: func(context.Context) (string, error) {
			select {
			case <-prepareStarted:
			case <-time.After(time.Second):
				t.Fatal("connector preparation did not start before sidecar startup completed")
			}
			close(allowPrepare)
			return "ws://localhost:1234/token", nil
		},
	}
	b, err := New(context.Background(), WithAutoInstall(false), withSidecarFactory(fakeSidecarFactory(sidecar)), withConnector(connector))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if prepared.endpoint != sidecar.endpoint {
		t.Fatalf("prepared connector endpoint = %q", prepared.endpoint)
	}
	if connector.fallbackConnect {
		t.Fatalf("fallback connector path used instead of prepared connector")
	}
}

func TestNewStopsPreparedConnectorWhenSidecarStartFails(t *testing.T) {
	prepareStarted := make(chan struct{})
	allowPrepare := make(chan struct{})
	prepared := &fakePreparedConnector{session: &fakeSession{browser: &fakeBrowser{connected: true}}}
	connector := &fakePreparableConnector{
		started:  prepareStarted,
		release:  allowPrepare,
		prepared: prepared,
	}
	startErr := errors.New("sidecar failed")
	sidecar := &fakeSidecar{
		err: startErr,
		startFn: func(context.Context) (string, error) {
			select {
			case <-prepareStarted:
			case <-time.After(time.Second):
				t.Fatal("connector preparation did not start")
			}
			close(allowPrepare)
			return "", startErr
		},
	}
	if _, err := New(context.Background(), WithAutoInstall(false), withSidecarFactory(fakeSidecarFactory(sidecar)), withConnector(connector)); !errors.Is(err, startErr) {
		t.Fatalf("err = %v", err)
	}
	if !prepared.stopped {
		t.Fatalf("prepared connector was not stopped after sidecar failure")
	}
}

func TestNewStopsSidecarWhenConnectorPrepareFails(t *testing.T) {
	prepareStarted := make(chan struct{})
	allowPrepare := make(chan struct{})
	prepareErr := errors.New("prepare failed")
	connector := &fakePreparableConnector{
		started: prepareStarted,
		release: allowPrepare,
		err:     prepareErr,
	}
	sidecar := &fakeSidecar{
		endpoint: "ws://localhost:1234/token",
		startFn: func(context.Context) (string, error) {
			select {
			case <-prepareStarted:
			case <-time.After(time.Second):
				t.Fatal("connector preparation did not start")
			}
			close(allowPrepare)
			return "ws://localhost:1234/token", nil
		},
	}
	if _, err := New(context.Background(), WithAutoInstall(false), withSidecarFactory(fakeSidecarFactory(sidecar)), withConnector(connector)); !errors.Is(err, ErrConnect) || !strings.Contains(err.Error(), "prepare failed") {
		t.Fatalf("err = %v", err)
	}
	if !sidecar.stopped {
		t.Fatalf("sidecar was not stopped after prepare failure")
	}
}

func TestNewStopsPreparedConnectorWhenPreparedConnectFails(t *testing.T) {
	prepareStarted := make(chan struct{})
	allowPrepare := make(chan struct{})
	connectErr := errors.New("prepared connect failed")
	prepared := &fakePreparedConnector{err: connectErr}
	connector := &fakePreparableConnector{
		started:  prepareStarted,
		release:  allowPrepare,
		prepared: prepared,
	}
	sidecar := &fakeSidecar{
		endpoint: "ws://localhost:1234/token",
		startFn: func(context.Context) (string, error) {
			select {
			case <-prepareStarted:
			case <-time.After(time.Second):
				t.Fatal("connector preparation did not start")
			}
			close(allowPrepare)
			return "ws://localhost:1234/token", nil
		},
	}
	if _, err := New(context.Background(), WithAutoInstall(false), withSidecarFactory(fakeSidecarFactory(sidecar)), withConnector(connector)); !errors.Is(err, ErrConnect) || !strings.Contains(err.Error(), "prepared connect failed") {
		t.Fatalf("err = %v", err)
	}
	if !sidecar.stopped || !prepared.stopped {
		t.Fatalf("cleanup sidecar=%v prepared=%v", sidecar.stopped, prepared.stopped)
	}
}

func TestNewSidecarStartFailureWithoutPreparedConnector(t *testing.T) {
	startErr := errors.New("sidecar failed before connect")
	sidecar := &fakeSidecar{err: startErr}
	if _, err := New(context.Background(), WithAutoInstall(false), withSidecarFactory(fakeSidecarFactory(sidecar)), withConnector(&fakeConnector{})); !errors.Is(err, startErr) {
		t.Fatalf("err = %v", err)
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
	if _, err := b.NewContext(context.Background(), WithHARRecording(HAROptions{})); err == nil {
		t.Fatal("context with invalid HAR options succeeded")
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

func TestExactFingerprintReplacesGeneratedConfigAndAcceptsLaterOverrides(t *testing.T) {
	cfg := defaultLaunchConfig()
	WithFingerprintOverride(camoufoxcfg.FingerprintOverride{"stale": true})(&cfg)
	WithExactFingerprint(camoufoxcfg.FingerprintOverride{"navigator.userAgent": "exact"})(&cfg)
	WithFingerprintOverride(camoufoxcfg.FingerprintOverride{"screen.width": 1920})(&cfg)
	if !cfg.fingerprintExact {
		t.Fatal("exact fingerprint option did not mark the config exact")
	}
	if _, ok := cfg.fingerprint["stale"]; ok {
		t.Fatalf("exact fingerprint retained prior override: %#v", cfg.fingerprint)
	}
	if cfg.fingerprint["navigator.userAgent"] != "exact" || cfg.fingerprint["screen.width"] != 1920 {
		t.Fatalf("exact fingerprint = %#v", cfg.fingerprint)
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
	WithBrowserAcceptDownloads(false)(&cfg)
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
	if cfg.acceptDownloads == nil || *cfg.acceptDownloads {
		t.Fatalf("accept downloads option not applied: %#v", cfg.acceptDownloads)
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
		WithAcceptDownloads(true),
	)
	headers["x"] = "mutated"
	pwOpts := toPWBridgeContextOptions(contextCfg)
	if pwOpts.Viewport.Width != 800 || pwOpts.StorageState.Cookies[0].Name != "a" || pwOpts.Proxy.Server != "socks5://proxy" {
		t.Fatalf("context conversion = %#v", pwOpts)
	}
	if pwOpts.ExtraHTTPHeaders["x"] != "1" || pwOpts.HTTPCredentials.Username != "user" || pwOpts.Locale != "en-US" || pwOpts.TimezoneID == "" {
		t.Fatalf("context scalar conversion = %#v", pwOpts)
	}
	if pwOpts.AcceptDownloads == nil || !*pwOpts.AcceptDownloads {
		t.Fatalf("context accept downloads conversion = %#v", pwOpts.AcceptDownloads)
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
	WithExactFingerprint(camoufoxcfg.FingerprintOverride{"navigator.userAgent": "ua"})(&cfg)
	WithMainWorldEval(true)(&cfg)
	WithEnableCache(true)(&cfg)
	WithDisableCOOP(true)(&cfg)
	WithExtraEnv("A=B")(&cfg)
	WithBrowserAcceptDownloads(false)(&cfg)
	WithAllowedOrigins("https://example.com", "https://api.example.com:8443")(&cfg)
	WithAllowedHosts("example.com", ".example.org")(&cfg)
	WithAllowLocalhost(true)(&cfg)
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
	if scfg.Fingerprint["navigator.userAgent"] != "ua" || !scfg.FingerprintExact || !scfg.MainWorldEval || !scfg.EnableCache || !scfg.DisableCOOP || scfg.ExtraEnv[0] != "A=B" {
		t.Fatalf("advanced options not mapped: %#v", scfg)
	}
	if scfg.AcceptDownloads == nil || *scfg.AcceptDownloads {
		t.Fatalf("accept downloads not mapped: %#v", scfg.AcceptDownloads)
	}
	if strings.Join(scfg.Policy.AllowedOrigins, ",") != "https://example.com,https://api.example.com:8443" || strings.Join(scfg.Policy.AllowedHosts, ",") != "example.com,.example.org" || !scfg.Policy.AllowLocalhost {
		t.Fatalf("network policy not mapped: %#v", scfg.Policy)
	}
	WithSidecarRuntime(SidecarRuntimeNodeDirect)(&cfg)
	handle, err = newSidecarManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	scfg = handle.(sidecarAdapter).manager.Config()
	if scfg.Runtime != string(SidecarRuntimeNodeDirect) {
		t.Fatalf("runtime not mapped: %#v", scfg)
	}

	oldResolve := resolveManagedCamoufoxExecutable
	t.Cleanup(func() { resolveManagedCamoufoxExecutable = oldResolve })
	resolveManagedCamoufoxExecutable = func(venvDir string) (string, error) {
		if venvDir != "/managed/venv" {
			t.Fatalf("resolver venv = %q", venvDir)
		}
		return "/managed/venv/runtime/camoufox", nil
	}
	cfg.sidecarRuntime = SidecarRuntimePython
	cfg.venvDir = "/managed/venv"
	handle, err = newSidecarManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	scfg = handle.(sidecarAdapter).manager.Config()
	if scfg.ExecutablePath != "/managed/venv/runtime/camoufox" {
		t.Fatalf("managed Python executable = %q", scfg.ExecutablePath)
	}
	resolveErr := errors.New("managed browser unavailable")
	resolveManagedCamoufoxExecutable = func(string) (string, error) { return "", resolveErr }
	if _, err := newSidecarManager(cfg); !errors.Is(err, resolveErr) {
		t.Fatalf("managed browser resolver err = %v", err)
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

func TestMapDownloadErrorPreservesNonTimeoutError(t *testing.T) {
	wantErr := errors.New("download failed")
	if got := mapDownloadError(wantErr); !errors.Is(got, wantErr) {
		t.Fatalf("mapped download error = %v", got)
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

const validHARForTest = `{"log":{"version":"1.2","creator":{"name":"test","version":"1"},"entries":[{"startedDateTime":"2026-07-19T00:00:00Z","time":1,"request":{"method":"GET","url":"https://example.com/api?q=secret","httpVersion":"HTTP/2","cookies":[],"headers":[],"queryString":[],"headersSize":0,"bodySize":0},"response":{"status":200,"statusText":"OK","httpVersion":"HTTP/2","cookies":[],"headers":[],"content":{"size":0,"mimeType":"application/json"},"redirectURL":"","headersSize":0,"bodySize":0},"cache":{},"timings":{"send":0,"wait":1,"receive":0}}]}}`

func TestHARRecordingPublicLifecycle(t *testing.T) {
	rawContext := &fakeContext{}
	rawBrowser := &fakeBrowser{connected: true, newCtx: rawContext}
	session := &fakeSession{browser: rawBrowser}
	browser, err := New(context.Background(),
		WithAutoInstall(false),
		withSidecarFactory(fakeSidecarFactory(&fakeSidecar{endpoint: "ws://localhost:1/t"})),
		withConnector(&fakeConnector{session: session}),
	)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "capture.har")
	ctx, err := browser.NewContext(context.Background(), WithHARRecording(HAROptions{
		Path:      destination,
		URLFilter: "**/api/**",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if rawBrowser.newCtxOptions.HAR == nil || rawBrowser.newCtxOptions.HAR.Path == destination {
		t.Fatalf("HAR bridge options = %#v", rawBrowser.newCtxOptions.HAR)
	}
	if rawBrowser.newCtxOptions.HAR.Mode != "minimal" || rawBrowser.newCtxOptions.HAR.Content != "omit" || !rawBrowser.newCtxOptions.HAR.OmitRequestContent || rawBrowser.newCtxOptions.HAR.URLFilter != "**/api/**" {
		t.Fatalf("HAR bridge options = %#v", rawBrowser.newCtxOptions.HAR)
	}
	rawContext.closeFunc = func() error {
		return os.WriteFile(rawBrowser.newCtxOptions.HAR.Path, []byte(validHARForTest), 0o600)
	}
	if _, ok := ctx.HARResult(); ok {
		t.Fatal("HAR result available before close")
	}
	if err := ctx.Close(); err != nil {
		t.Fatal(err)
	}
	result, ok := ctx.HARResult()
	if !ok || result.Path != destination || result.Capture != HARCaptureMetadata || result.Entries != 1 || result.Bytes == 0 || len(result.Routes) != 1 || result.RoutesTruncated {
		t.Fatalf("HAR result = %#v ok=%t", result, ok)
	}
	if result.Routes[0].Method != "GET" || result.Routes[0].URL != "https://example.com/api?q=%3Credacted%3E" || result.Routes[0].Status != 200 {
		t.Fatalf("HAR route = %#v", result.Routes[0])
	}
	result.Routes[0].URL = "mutated"
	again, ok := ctx.HARResult()
	if !ok || again.Routes[0].URL == "mutated" {
		t.Fatalf("HAR result was not cloned: %#v ok=%t", again, ok)
	}
	if rawContext.closeCalls != 1 {
		t.Fatalf("close calls = %d", rawContext.closeCalls)
	}
	if err := ctx.Close(); err != nil || rawContext.closeCalls != 1 {
		t.Fatalf("second close err=%v calls=%d", err, rawContext.closeCalls)
	}
}

func TestHARAbortDiscardsArtifact(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "capture.har")
	recorder, native, err := prepareHAR(HAROptions{Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	rawContext := &fakeContext{closeFunc: func() error {
		return os.WriteFile(native.Path, []byte(validHARForTest), 0o600)
	}}
	ctx := &Context{raw: rawContext, har: recorder}
	privateDirectory := filepath.Dir(native.Path)

	if err := ctx.Abort(); err != nil {
		t.Fatal(err)
	}
	if rawContext.closeCalls != 1 {
		t.Fatalf("abort close calls = %d", rawContext.closeCalls)
	}
	if _, ok := ctx.HARResult(); ok {
		t.Fatal("aborted HAR exposed a result")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("aborted HAR destination exists: %v", err)
	}
	if _, err := os.Stat(privateDirectory); !os.IsNotExist(err) {
		t.Fatalf("aborted HAR private directory exists: %v", err)
	}
	if err := ctx.Close(); err != nil || rawContext.closeCalls != 1 {
		t.Fatalf("close after abort err=%v calls=%d", err, rawContext.closeCalls)
	}
}

func TestBrowserNewPageFailureAbortsHAR(t *testing.T) {
	boom := errors.New("new page failed")
	rawContext := &fakeContext{newPageErr: boom}
	rawBrowser := &fakeBrowser{connected: true, newCtx: rawContext}
	browser := &Browser{raw: rawBrowser, done: make(chan struct{})}
	destination := filepath.Join(t.TempDir(), "capture.har")
	rawContext.closeFunc = func() error {
		return os.WriteFile(rawBrowser.newCtxOptions.HAR.Path, []byte(validHARForTest), 0o600)
	}

	if _, err := browser.NewPage(context.Background(), WithHARRecording(HAROptions{Path: destination})); !errors.Is(err, boom) {
		t.Fatalf("new page error = %v", err)
	}
	if rawContext.closeCalls != 1 {
		t.Fatalf("rollback close calls = %d", rawContext.closeCalls)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed startup published HAR: %v", err)
	}
	if rawBrowser.newCtxOptions.HAR == nil {
		t.Fatal("missing HAR bridge options")
	}
	if _, err := os.Stat(filepath.Dir(rawBrowser.newCtxOptions.HAR.Path)); !os.IsNotExist(err) {
		t.Fatalf("failed startup retained private HAR: %v", err)
	}
	if len(browser.harContexts) != 0 {
		t.Fatalf("failed startup retained tracked contexts: %d", len(browser.harContexts))
	}
}

func TestBrowserCloseDuringNewPageDiscardsProvisionalHAR(t *testing.T) {
	newPageStarted := make(chan struct{})
	releaseNewPage := make(chan struct{})
	rawContext := &fakeContext{newPageFunc: func() (pwbridge.Page, error) {
		close(newPageStarted)
		<-releaseNewPage
		return &fakePage{}, nil
	}}
	rawBrowser := &fakeBrowser{connected: true, newCtx: rawContext}
	browser := &Browser{raw: rawBrowser, done: make(chan struct{})}
	destination := filepath.Join(t.TempDir(), "capture.har")
	rawContext.closeFunc = func() error {
		return os.WriteFile(rawBrowser.newCtxOptions.HAR.Path, []byte(validHARForTest), 0o600)
	}

	result := make(chan error, 1)
	go func() {
		_, err := browser.NewPage(context.Background(), WithHARRecording(HAROptions{Path: destination}))
		result <- err
	}()
	select {
	case <-newPageStarted:
	case <-time.After(time.Second):
		t.Fatal("new page did not start")
	}
	if err := browser.Close(); err != nil {
		t.Fatal(err)
	}
	close(releaseNewPage)
	select {
	case err := <-result:
		if !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("new page error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("new page did not finish")
	}
	if rawContext.closeCalls != 1 {
		t.Fatalf("provisional close calls = %d", rawContext.closeCalls)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("provisional HAR was published: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(rawBrowser.newCtxOptions.HAR.Path)); !os.IsNotExist(err) {
		t.Fatalf("provisional HAR private directory remains: %v", err)
	}
}

func TestBrowserCloseFlushesTrackedHARAndPersistentRejectsIt(t *testing.T) {
	rawContext := &fakeContext{}
	rawBrowser := &fakeBrowser{connected: true, newCtx: rawContext}
	session := &fakeSession{browser: rawBrowser}
	browser, err := New(context.Background(),
		WithAutoInstall(false),
		withSidecarFactory(fakeSidecarFactory(&fakeSidecar{endpoint: "ws://localhost:1/t"})),
		withConnector(&fakeConnector{session: session}),
	)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "capture.har")
	if _, err := browser.NewContext(context.Background(), WithHARRecording(HAROptions{Path: destination, Capture: HARCaptureFull})); err != nil {
		t.Fatal(err)
	}
	rawContext.closeFunc = func() error {
		return os.WriteFile(rawBrowser.newCtxOptions.HAR.Path, []byte(validHARForTest), 0o600)
	}
	if err := browser.Close(); err != nil {
		t.Fatal(err)
	}
	if !session.stopped || rawContext.closeCalls != 1 {
		t.Fatalf("browser close stopped=%t context calls=%d", session.stopped, rawContext.closeCalls)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != validHARForTest {
		t.Fatalf("full HAR = %q err=%v", data, err)
	}

	persistent := &Browser{
		cfg:  launchConfig{persistentCtx: true},
		raw:  &fakeBrowser{contexts: []pwbridge.BrowserContext{&fakeContext{}}},
		done: make(chan struct{}),
	}
	if _, err := persistent.NewContext(context.Background(), WithHARRecording(HAROptions{Path: filepath.Join(t.TempDir(), "persistent.har")})); !errors.Is(err, ErrHARPersistentContext) {
		t.Fatalf("persistent HAR error = %v", err)
	}
}

func TestBrowserClosePreservesFirstCloseError(t *testing.T) {
	boom := errors.New("stop failed")
	browser := &Browser{session: &fakeSession{stopErr: boom}, done: make(chan struct{})}
	if err := browser.Close(); !errors.Is(err, boom) {
		t.Fatalf("first close error = %v", err)
	}
	if err := browser.Close(); !errors.Is(err, boom) {
		t.Fatalf("second close error = %v", err)
	}
}

func TestHARContextCreationFailureCleansPrivateArtifact(t *testing.T) {
	boom := errors.New("new context failed")
	rawBrowser := &fakeBrowser{connected: true, newCtxErr: boom}
	browser := &Browser{raw: rawBrowser, done: make(chan struct{})}
	if _, err := browser.NewContext(context.Background(), WithHARRecording(HAROptions{Path: filepath.Join(t.TempDir(), "capture.har")})); !errors.Is(err, boom) {
		t.Fatalf("new context error = %v", err)
	}
	if rawBrowser.newCtxOptions.HAR == nil {
		t.Fatal("missing captured HAR options")
	}
	if _, err := os.Stat(filepath.Dir(rawBrowser.newCtxOptions.HAR.Path)); !os.IsNotExist(err) {
		t.Fatalf("private directory remains: %v", err)
	}
}

func TestHARLifecycleRefusesInvalidAndClosingContexts(t *testing.T) {
	if _, _, err := prepareHAR(HAROptions{}); err == nil {
		t.Fatal("empty HAR options succeeded")
	}
	var nilContext *Context
	if result, ok := nilContext.HARResult(); ok || result.Path != "" || result.Routes != nil {
		t.Fatalf("nil context HAR result = %#v ok=%t", result, ok)
	}

	closedContext := &Context{harClosed: true, harProvisional: true}
	if closedContext.commitHAR() {
		t.Fatal("closed context committed HAR")
	}
	browser := &Browser{closed: true}
	if browser.commitHARContext(&Context{}) {
		t.Fatal("closed browser committed HAR context")
	}
	if browser.trackHARContext(&Context{}) {
		t.Fatal("closed browser tracked HAR context")
	}
}

func TestBrowserNewPageCommitsHARBeforeReturning(t *testing.T) {
	rawContext := &fakeContext{}
	rawBrowser := &fakeBrowser{connected: true, newCtx: rawContext}
	browser := &Browser{raw: rawBrowser, done: make(chan struct{})}
	destination := filepath.Join(t.TempDir(), "capture.har")
	rawContext.closeFunc = func() error {
		return os.WriteFile(rawBrowser.newCtxOptions.HAR.Path, []byte(validHARForTest), 0o600)
	}

	page, err := browser.NewPage(context.Background(), WithHARRecording(HAROptions{Path: destination}))
	if err != nil {
		t.Fatal(err)
	}
	if page.context == nil || page.context.harProvisional {
		t.Fatalf("returned page has provisional context: %#v", page.context)
	}
	if err := page.Close(); err != nil {
		t.Fatal(err)
	}
	if result, ok := page.context.HARResult(); !ok || result.Path != destination {
		t.Fatalf("committed HAR result = %#v ok=%t", result, ok)
	}
}

func TestBrowserNewContextAbortsHARWhenCloseWinsTracking(t *testing.T) {
	rawContext := &fakeContext{}
	rawBrowser := &fakeBrowser{connected: true}
	browser := &Browser{raw: rawBrowser, done: make(chan struct{})}
	destination := filepath.Join(t.TempDir(), "capture.har")
	rawBrowser.newCtxFunc = func(pwbridge.ContextOptions) (pwbridge.BrowserContext, error) {
		browser.mu.Lock()
		browser.closed = true
		browser.mu.Unlock()
		return rawContext, nil
	}

	if _, err := browser.NewContext(context.Background(), WithHARRecording(HAROptions{Path: destination})); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("new context error = %v", err)
	}
	if rawContext.closeCalls != 1 {
		t.Fatalf("rollback close calls = %d", rawContext.closeCalls)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("tracking race published HAR: %v", err)
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
	origDriver := sidecarEnsureManagedPlaywrightDriver
	defer func() { sidecarEnsureManagedPlaywrightDriver = origDriver }()
	called := false
	sidecarEnsureManagedPlaywrightDriver = func(context.Context, sidecarpkg.InstallOptions) error {
		t.Fatalf("driver install should not run after sidecar error")
		return nil
	}
	sidecarEnsureInstalled = func(ctx context.Context, opts sidecarpkg.InstallOptions) error {
		called = true
		if opts.PythonBin != "python3.12" || opts.VenvDir != "/venv" || opts.CamoufoxVersion != "0.4.11" ||
			opts.Runtime != string(SidecarRuntimePython) || !opts.SkipBinaryFetch || opts.CamoufoxPath != "/camoufox" || !opts.Verbose || !opts.ForceReinstall {
			t.Fatalf("opts = %#v", opts)
		}
		return fmt.Errorf("wrapped: %w", sidecarpkg.ErrVersionMismatch)
	}
	err := EnsureInstalled(context.Background(), func(o *InstallOptions) {
		o.PythonBin = "python3.12"
		o.VenvDir = "/venv"
		o.Runtime = SidecarRuntimePython
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

func TestEnsureInstalledDefaultsToNodeDirectRuntime(t *testing.T) {
	orig := sidecarEnsureInstalled
	origDriver := sidecarEnsureManagedPlaywrightDriver
	defer func() {
		sidecarEnsureInstalled = orig
		sidecarEnsureManagedPlaywrightDriver = origDriver
	}()
	sidecarEnsureManagedPlaywrightDriver = func(context.Context, sidecarpkg.InstallOptions) error {
		t.Fatalf("node-direct default install should not repeat driver install")
		return nil
	}
	sidecarEnsureInstalled = func(_ context.Context, opts sidecarpkg.InstallOptions) error {
		if opts.Runtime != "" {
			t.Fatalf("default install should leave runtime empty for sidecar default, got %#v", opts)
		}
		return nil
	}
	if err := EnsureInstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNewAutoInstallPassesConfiguredRuntime(t *testing.T) {
	orig := sidecarEnsureInstalled
	origDriver := sidecarEnsureManagedPlaywrightDriver
	defer func() {
		sidecarEnsureInstalled = orig
		sidecarEnsureManagedPlaywrightDriver = origDriver
	}()
	sidecarEnsureManagedPlaywrightDriver = func(context.Context, sidecarpkg.InstallOptions) error { return nil }
	sidecarEnsureInstalled = func(_ context.Context, opts sidecarpkg.InstallOptions) error {
		if opts.Runtime != string(SidecarRuntimePython) {
			t.Fatalf("auto-install runtime = %q", opts.Runtime)
		}
		return nil
	}
	_, err := New(context.Background(),
		WithSidecarRuntime(SidecarRuntimePython),
		withSidecarFactory(fakeSidecarFactory(&fakeSidecar{endpoint: "ws://127.0.0.1:1234"})),
		withConnector(&fakeConnector{session: &fakeSession{browser: &fakeBrowser{connected: true}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestConfigureConnectorUsesManagedDriverForBothRuntimes(t *testing.T) {
	cfg := defaultLaunchConfig()
	cfg.venvDir = filepath.Join(t.TempDir(), "cache")
	configureConnectorForRuntime(&cfg)
	real, ok := cfg.connector.(pwbridge.RealConnector)
	if !ok {
		t.Fatalf("connector = %T, want pwbridge.RealConnector", cfg.connector)
	}
	if real.DriverDirectory != managedPlaywrightDriverDir(cfg.venvDir) {
		t.Fatalf("driver directory = %q, want %q", real.DriverDirectory, managedPlaywrightDriverDir(cfg.venvDir))
	}

	custom := pwbridge.RealConnector{DriverDirectory: "/custom-driver"}
	cfg.connector = custom
	configureConnectorForRuntime(&cfg)
	if got := cfg.connector.(pwbridge.RealConnector).DriverDirectory; got != custom.DriverDirectory {
		t.Fatalf("custom driver directory = %q, want %q", got, custom.DriverDirectory)
	}

	cfg.connector = pwbridge.RealConnector{}
	cfg.sidecarRuntime = SidecarRuntimePython
	configureConnectorForRuntime(&cfg)
	if got := cfg.connector.(pwbridge.RealConnector).DriverDirectory; got != managedPlaywrightDriverDir(cfg.venvDir) {
		t.Fatalf("python runtime driver directory = %q, want managed", got)
	}
	pointer := &pwbridge.RealConnector{}
	cfg.connector = pointer
	configureConnectorForRuntime(&cfg)
	if pointer.DriverDirectory != managedPlaywrightDriverDir(cfg.venvDir) {
		t.Fatalf("pointer driver directory = %q", pointer.DriverDirectory)
	}
	cfg.connector = (*pwbridge.RealConnector)(nil)
	configureConnectorForRuntime(&cfg)
}

func TestEnsureInstalledInstallsPlaywrightDriverForLegacyPython(t *testing.T) {
	orig := sidecarEnsureInstalled
	origDriver := sidecarEnsureManagedPlaywrightDriver
	defer func() {
		sidecarEnsureInstalled = orig
		sidecarEnsureManagedPlaywrightDriver = origDriver
	}()
	sidecarCalled := false
	driverCalled := false
	sidecarEnsureInstalled = func(context.Context, sidecarpkg.InstallOptions) error {
		sidecarCalled = true
		return nil
	}
	sidecarEnsureManagedPlaywrightDriver = func(_ context.Context, opts sidecarpkg.InstallOptions) error {
		driverCalled = true
		if opts.VenvDir != "/venv" || opts.SkipBinaryFetch || opts.ForceReinstall {
			t.Fatalf("driver install options = %#v", opts)
		}
		return nil
	}
	if err := EnsureInstalled(context.Background(), func(o *InstallOptions) {
		o.Runtime = SidecarRuntimePython
		o.VenvDir = "/venv"
	}); err != nil {
		t.Fatal(err)
	}
	if !sidecarCalled || !driverCalled {
		t.Fatalf("sidecarCalled=%v driverCalled=%v", sidecarCalled, driverCalled)
	}

	driverErr := errors.New("driver failed")
	sidecarEnsureManagedPlaywrightDriver = func(context.Context, sidecarpkg.InstallOptions) error { return driverErr }
	if err := EnsureInstalled(context.Background(), func(o *InstallOptions) { o.Runtime = SidecarRuntimePython }); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "playwright driver install failed") {
		t.Fatalf("driver install err = %v", err)
	}
	sidecarEnsureManagedPlaywrightDriver = func(context.Context, sidecarpkg.InstallOptions) error {
		return sidecarpkg.ErrVersionMismatch
	}
	if err := EnsureInstalled(context.Background(), func(o *InstallOptions) { o.Runtime = SidecarRuntimePython }); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("driver version error = %v", err)
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
	downloadEvent := &fakeDownload{url: "https://example.com/receipt", suggestedFilename: "receipt.pdf"}
	downloadSeen := false
	page.OnDownload(func(d *Download) {
		downloadSeen = d.URL() == "https://example.com/receipt" && d.SuggestedFilename() == "receipt.pdf"
	})
	fp.onDownload(downloadEvent)
	if !downloadSeen {
		t.Fatalf("download callback not wrapped")
	}
	actionDownload := &fakeDownload{url: "https://example.com/action", suggestedFilename: "action.txt"}
	fp.download = actionDownload
	downloadActionCalled := false
	download, err := page.RunAndWaitForDownload(context.Background(), func() error {
		downloadActionCalled = true
		return nil
	}, DownloadTimeout(1750*time.Millisecond))
	if err != nil || !downloadActionCalled || download.URL() != "https://example.com/action" || fp.downloadOpts.Timeout != 1750*time.Millisecond {
		t.Fatalf("download wait action=%t download=%#v opts=%#v err=%v", downloadActionCalled, download, fp.downloadOpts, err)
	}
	savePath := filepath.Join(t.TempDir(), "receipt.pdf")
	if err := download.SaveAs(context.Background(), savePath); err != nil || actionDownload.savePath != savePath {
		t.Fatalf("download save path=%q err=%v", actionDownload.savePath, err)
	}
	actionDownload.saveErr = errors.New("timeout while saving")
	if err := download.SaveAs(context.Background(), savePath); !errors.Is(err, ErrTimeout) {
		t.Fatalf("download save timeout = %v", err)
	}
	actionDownload.saveErr = nil
	if err := download.Failure(context.Background()); err != nil {
		t.Fatalf("download failure = %v", err)
	}
	actionDownload.failureErr = errors.New("failure details unavailable")
	if err := download.Failure(context.Background()); !errors.Is(err, actionDownload.failureErr) {
		t.Fatalf("download failure raw err = %v", err)
	}
	actionDownload.failureErr = nil
	if err := download.Cancel(context.Background()); err != nil || actionDownload.cancelCalls != 1 {
		t.Fatalf("download cancel calls=%d err=%v", actionDownload.cancelCalls, err)
	}
	actionDownload.cancelErr = errors.New("cancel failed")
	if err := download.Cancel(context.Background()); !errors.Is(err, actionDownload.cancelErr) {
		t.Fatalf("download cancel raw err = %v", err)
	}
	actionDownload.cancelErr = nil
	if err := download.Delete(context.Background()); err != nil {
		t.Fatalf("download delete = %v", err)
	}
	actionDownload.deleteErr = errors.New("delete failed")
	if err := download.Delete(context.Background()); !errors.Is(err, actionDownload.deleteErr) {
		t.Fatalf("download delete raw err = %v", err)
	}
	actionDownload.deleteErr = nil
	canceledDownloadCtx, cancelDownload := context.WithCancel(context.Background())
	cancelDownload()
	if err := download.SaveAs(canceledDownloadCtx, savePath); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download save = %v", err)
	}
	blocking := &fakeDownload{saveStarted: make(chan struct{}), saveRelease: make(chan struct{})}
	blockingDownload := &Download{raw: blocking}
	ctxDuringSave, cancelDuringSave := context.WithCancel(context.Background())
	saveDone := make(chan error, 1)
	go func() { saveDone <- blockingDownload.SaveAs(ctxDuringSave, savePath) }()
	<-blocking.saveStarted
	cancelDuringSave()
	if err := <-saveDone; !errors.Is(err, context.Canceled) || blocking.cancelCalls != 1 {
		t.Fatalf("save cancellation err=%v cancelCalls=%d", err, blocking.cancelCalls)
	}
	if err := download.Failure(canceledDownloadCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download failure = %v", err)
	}
	if err := download.Cancel(canceledDownloadCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download cancel = %v", err)
	}
	if err := download.Delete(canceledDownloadCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download delete = %v", err)
	}
	fp.downloadErr = context.DeadlineExceeded
	if _, err := page.RunAndWaitForDownload(context.Background(), func() error { return nil }); !errors.Is(err, ErrTimeout) {
		t.Fatalf("download wait timeout = %v", err)
	}
	fp.downloadErr = nil
	if _, err := page.RunAndWaitForDownload(canceledDownloadCtx, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download wait = %v", err)
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
	if got := mapNavigationError(errors.New("Frame.Goto: NS_ERROR_PROXY_FORBIDDEN")); !errors.Is(got, ErrURLBlocked) {
		t.Fatalf("proxy block mapped to %v", got)
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
	startFn  func(context.Context) (string, error)
}

func fakeSidecarFactory(s *fakeSidecar) func(launchConfig) (sidecarHandle, error) {
	return func(launchConfig) (sidecarHandle, error) { return s, nil }
}

func (s *fakeSidecar) Start(ctx context.Context) (string, error) {
	if s.startFn != nil {
		return s.startFn(ctx)
	}
	return s.endpoint, s.err
}
func (s *fakeSidecar) Stop(context.Context) { s.stopped = true }
func (s *fakeSidecar) Info() SidecarInfo    { return s.info }

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

type fakePreparableConnector struct {
	started         chan struct{}
	release         chan struct{}
	prepared        *fakePreparedConnector
	err             error
	fallbackConnect bool
}

func (c *fakePreparableConnector) Prepare(ctx context.Context) (pwbridge.PreparedConnector, error) {
	close(c.started)
	select {
	case <-c.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.prepared, nil
}

func (c *fakePreparableConnector) Connect(ctx context.Context, endpoint string, opts pwbridge.ConnectOptions) (pwbridge.Session, error) {
	c.fallbackConnect = true
	return c.prepared.Connect(ctx, endpoint, opts)
}

type fakePreparedConnector struct {
	endpoint string
	session  pwbridge.Session
	err      error
	stopped  bool
}

func (c *fakePreparedConnector) Connect(ctx context.Context, endpoint string, opts pwbridge.ConnectOptions) (pwbridge.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.endpoint = endpoint
	if c.err != nil {
		return nil, c.err
	}
	return c.session, nil
}

func (c *fakePreparedConnector) Stop() error {
	c.stopped = true
	return nil
}

type fakeSession struct {
	browser *fakeBrowser
	stopped bool
	stopErr error
}

func (s *fakeSession) Browser() pwbridge.Browser { return s.browser }
func (s *fakeSession) Stop() error {
	s.stopped = true
	return s.stopErr
}

type fakeBrowser struct {
	connected     bool
	contexts      []pwbridge.BrowserContext
	newCtx        *fakeContext
	newCtxFunc    func(pwbridge.ContextOptions) (pwbridge.BrowserContext, error)
	newPage       *fakePage
	newCtxErr     error
	newPageErr    error
	newCtxOptions pwbridge.ContextOptions
}

func (b *fakeBrowser) Close() error                        { b.connected = false; return nil }
func (b *fakeBrowser) IsConnected() bool                   { return b.connected }
func (b *fakeBrowser) OnDisconnected(func())               {}
func (b *fakeBrowser) Contexts() []pwbridge.BrowserContext { return b.contexts }
func (b *fakeBrowser) NewContext(opts pwbridge.ContextOptions) (pwbridge.BrowserContext, error) {
	b.newCtxOptions = opts
	if b.newCtxFunc != nil {
		return b.newCtxFunc(opts)
	}
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
	newPageFunc    func() (pwbridge.Page, error)
	newPageErr     error
	cookiesErr     error
	addCookiesErr  error
	clearErr       error
	storageErr     error
	routeErr       error
	unrouteErr     error
	closeErr       error
	closeFunc      func() error
	closeCalls     int
	routePattern   string
	routeHandler   pwbridge.RouteHandler
	unroutePattern string
	unrouteHandler pwbridge.RouteHandler
	unrouteCalls   int
	onRequest      func(pwbridge.Request)
	onResponse     func(pwbridge.Response)
}

func (c *fakeContext) NewPage() (pwbridge.Page, error) {
	if c.newPageFunc != nil {
		return c.newPageFunc()
	}
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
	c.closeCalls++
	if c.closeFunc != nil {
		if err := c.closeFunc(); err != nil {
			return err
		}
	}
	return c.closeErr
}
func (c *fakeContext) Raw() any { return c }

type fakePage struct {
	evaluateResult   any
	evaluateErr      error
	evaluateHook     func(*fakePage)
	evaluateArg      any
	internalEvalArg  any
	locator          pwbridge.Locator
	response         pwbridge.Response
	gotoURL          string
	gotoOpts         pwbridge.GotoOptions
	backOpts         pwbridge.NavigateOptions
	forwardOpts      pwbridge.NavigateOptions
	reloadOpts       pwbridge.NavigateOptions
	downloadOpts     pwbridge.DownloadOptions
	download         pwbridge.Download
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
	onDownload       func(pwbridge.Download)
	wheelX           float64
	wheelY           float64
	wheelErr         error
	gotoErr          error
	backErr          error
	forwardErr       error
	reloadErr        error
	downloadErr      error
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
func (p *fakePage) RunAndWaitForDownload(action func() error, opts pwbridge.DownloadOptions) (pwbridge.Download, error) {
	if err := action(); err != nil {
		return nil, err
	}
	p.downloadOpts = opts
	if p.downloadErr != nil {
		return nil, p.downloadErr
	}
	if p.download != nil {
		return p.download, nil
	}
	return &fakeDownload{url: "https://example.com/file", suggestedFilename: "file.txt"}, nil
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
	if p.evaluateHook != nil {
		p.evaluateHook(p)
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
func (p *fakePage) OnDownload(fn func(pwbridge.Download))      { p.onDownload = fn }
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

type fakeDownload struct {
	url               string
	suggestedFilename string
	savePath          string
	saveErr           error
	failureErr        error
	cancelErr         error
	deleteErr         error
	cancelCalls       int
	saveStarted       chan struct{}
	saveRelease       chan struct{}
	releaseOnce       sync.Once
}

func (d *fakeDownload) URL() string               { return d.url }
func (d *fakeDownload) SuggestedFilename() string { return d.suggestedFilename }
func (d *fakeDownload) SaveAs(path string) error {
	d.savePath = path
	if d.saveStarted != nil {
		close(d.saveStarted)
		<-d.saveRelease
	}
	return d.saveErr
}
func (d *fakeDownload) Failure() error { return d.failureErr }
func (d *fakeDownload) Cancel() error {
	d.cancelCalls++
	if d.saveRelease != nil {
		d.releaseOnce.Do(func() { close(d.saveRelease) })
	}
	return d.cancelErr
}
func (d *fakeDownload) Delete() error { return d.deleteErr }

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
	url            string
	method         string
	headers        map[string]string
	post           string
	failureErr     error
	redirectedFrom pwbridge.Request
}

func (r *fakeRequest) URL() string                { return r.url }
func (r *fakeRequest) Method() string             { return r.method }
func (r *fakeRequest) Headers() map[string]string { return r.headers }
func (r *fakeRequest) Failure() error             { return r.failureErr }
func (r *fakeRequest) RedirectedFrom() pwbridge.Request {
	return r.redirectedFrom
}
func (r *fakeRequest) PostData() string          { return r.post }
func (r *fakeRequest) PostDataBytes() []byte     { return []byte(r.post) }
func (r *fakeRequest) ResourceType() string      { return "document" }
func (r *fakeRequest) IsNavigationRequest() bool { return true }

type fakeResponse struct {
	url     string
	status  int
	text    string
	body    []byte
	request pwbridge.Request
	bodyErr error
	headers map[string]string
}

func (r *fakeResponse) URL() string        { return r.url }
func (r *fakeResponse) Status() int        { return r.status }
func (r *fakeResponse) StatusText() string { return "Created" }
func (r *fakeResponse) Headers() map[string]string {
	if r.headers != nil {
		return r.headers
	}
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
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{"ok": true, "status": 200, "body": "abcd", "url": "https://example.com", "headers": map[string]string{"X-Test": "yes"}, "truncated": true}}}
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

func TestFetchBytesWithOptionsRejectsInvalidBase64(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{
		"ok": true, "status": 200, "body_encoding": "base64", "body_base64": "!",
	}}}
	if _, err := page.FetchBytesWithOptions(context.Background(), "https://example.com", http.MethodGet, nil, nil, FetchBytesOptions{}); err == nil {
		t.Fatal("invalid base64 response was accepted")
	}
}

func TestFetchBytesWithOptionsClassifiesBlockedEvaluateOutcomes(t *testing.T) {
	for _, test := range []struct {
		name   string
		result any
		err    error
	}{
		{name: "evaluate_error", err: errors.New("evaluate failed")},
		{name: "successful_payload", result: map[string]any{"ok": true, "status": 200, "body": "blocked"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := testBlockedMarker(t)
			root := &fakeRequest{url: "http://public.example/start", method: http.MethodGet}
			raw := &fakePage{evaluateResult: test.result, evaluateErr: test.err}
			raw.evaluateHook = func(page *fakePage) {
				page.onResponse(&fakeResponse{
					status: http.StatusForbidden, headers: map[string]string{netguard.BlockedHeader: marker}, request: root,
				})
			}
			page := &Page{raw: raw}
			if _, err := page.FetchBytesWithOptions(context.Background(), root.url, root.method, nil, nil, FetchBytesOptions{}); !errors.Is(err, ErrURLBlocked) {
				t.Fatalf("fetch error = %v", err)
			}
		})
	}
}

func TestFetchBytesWithOptionsClassifiesBlockedHTTPRedirect(t *testing.T) {
	marker := testBlockedMarker(t)
	root := &fakeRequest{url: "http://public.example/start", method: http.MethodGet}
	blocked := &fakeRequest{url: "http://10.0.0.1/private", method: http.MethodGet, redirectedFrom: root}
	raw := &fakePage{evaluateResult: map[string]any{
		"ok": false, "code": "cors_denied", "url": "http://public.example/start", "status": 0, "message": "NetworkError",
	}}
	raw.evaluateHook = func(page *fakePage) {
		page.onResponse(&fakeResponse{
			status:  http.StatusForbidden,
			headers: map[string]string{netguard.BlockedHeader: marker},
			request: blocked,
		})
	}
	page := &Page{raw: raw}
	_, err := page.FetchBytesWithOptions(context.Background(), root.url, http.MethodGet, nil, nil, FetchBytesOptions{})
	if !errors.Is(err, ErrURLBlocked) || !strings.Contains(err.Error(), "resolved address") {
		t.Fatalf("fetch err = %v", err)
	}
}

func TestFetchBytesWithOptionsClassifiesBlockedHTTPSRedirect(t *testing.T) {
	root := &fakeRequest{url: "http://public.example/start", method: http.MethodGet}
	blocked := &fakeRequest{
		url: "https://10.0.0.1/private", method: http.MethodGet, redirectedFrom: root,
		failureErr: errors.New("NS_ERROR_PROXY_FORBIDDEN"),
	}
	raw := &fakePage{evaluateResult: map[string]any{
		"ok": false, "code": "cors_denied", "url": "http://public.example/start", "status": 0, "message": "NetworkError",
	}}
	raw.evaluateHook = func(page *fakePage) { page.onRequestFailed(blocked) }
	page := &Page{raw: raw}
	_, err := page.FetchBytesWithOptions(context.Background(), root.url, http.MethodGet, nil, nil, FetchBytesOptions{})
	if !errors.Is(err, ErrURLBlocked) {
		t.Fatalf("fetch err = %v", err)
	}
}

func TestFetchBytesWithOptionsClassifiesCORSHiddenPrivateRedirect(t *testing.T) {
	root := &fakeRequest{url: "http://public.example/start", method: http.MethodGet}
	raw := &fakePage{evaluateResult: map[string]any{
		"ok": false, "code": "cors_denied", "url": root.url, "status": 0, "message": "NetworkError",
	}}
	raw.evaluateHook = func(page *fakePage) {
		page.onResponse(&fakeResponse{
			url: root.url, status: http.StatusFound,
			headers: map[string]string{"Location": "http://10.0.0.1/private?secret=must-not-surface"},
			request: root,
		})
	}
	page := &Page{browser: &Browser{cfg: defaultLaunchConfig()}, raw: raw}
	_, err := page.FetchBytesWithOptions(context.Background(), root.url, http.MethodGet, nil, nil, FetchBytesOptions{})
	if !errors.Is(err, ErrURLBlocked) || strings.Contains(err.Error(), "must-not-surface") {
		t.Fatalf("fetch err = %v", err)
	}
}

func TestFetchBytesWithOptionsPreservesCORSErrorForPublicRedirect(t *testing.T) {
	root := &fakeRequest{url: "http://public.example/start", method: http.MethodGet}
	raw := &fakePage{evaluateResult: map[string]any{
		"ok": false, "code": "cors_denied", "url": root.url, "status": 0, "message": "NetworkError",
	}}
	raw.evaluateHook = func(page *fakePage) {
		page.onResponse(&fakeResponse{
			url: root.url, status: http.StatusFound,
			headers: map[string]string{"Location": "https://93.184.216.34/public"},
			request: root,
		})
	}
	page := &Page{browser: &Browser{cfg: defaultLaunchConfig()}, raw: raw}
	_, err := page.FetchBytesWithOptions(context.Background(), root.url, http.MethodGet, nil, nil, FetchBytesOptions{})
	if !errors.Is(err, ErrBrowserFetch) || errors.Is(err, ErrURLBlocked) {
		t.Fatalf("fetch err = %v", err)
	}
}

func TestFetchBytesWithOptionsIgnoresNonRedirectLocation(t *testing.T) {
	for _, status := range []int{http.StatusMultipleChoices, http.StatusNotModified, http.StatusUseProxy, 306} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			root := &fakeRequest{url: "http://public.example/start", method: http.MethodGet}
			raw := &fakePage{evaluateResult: map[string]any{
				"ok": false, "code": "cors_denied", "url": root.url, "status": status, "message": "NetworkError",
			}}
			raw.evaluateHook = func(page *fakePage) {
				page.onResponse(&fakeResponse{
					url: root.url, status: status,
					headers: map[string]string{"Location": "http://10.0.0.1/private"},
					request: root,
				})
			}
			page := &Page{browser: &Browser{cfg: defaultLaunchConfig()}, raw: raw}
			_, err := page.FetchBytesWithOptions(context.Background(), root.url, http.MethodGet, nil, nil, FetchBytesOptions{})
			if !errors.Is(err, ErrBrowserFetch) || errors.Is(err, ErrURLBlocked) {
				t.Fatalf("fetch err = %v", err)
			}
		})
	}
}

func TestFetchBytesWithOptionsMatchesBrowserNormalizedURL(t *testing.T) {
	for _, target := range []string{
		"http://public.example/a/../start",
		"http://public.example:80/start",
		"http://public.example:080/start",
		"http://public.example/a/%2e%2e/start",
		`http://public.example/a\../start`,
	} {
		t.Run(target, func(t *testing.T) {
			root := &fakeRequest{url: "http://public.example/start", method: http.MethodGet}
			raw := &fakePage{evaluateResult: map[string]any{
				"ok": false, "code": "cors_denied", "url": target, "status": 0, "message": "NetworkError",
			}}
			raw.evaluateHook = func(page *fakePage) {
				page.onResponse(&fakeResponse{
					url: root.url, status: http.StatusFound,
					headers: map[string]string{"Location": "http://10.0.0.1/private"},
					request: root,
				})
			}
			page := &Page{browser: &Browser{cfg: defaultLaunchConfig()}, raw: raw}
			_, err := page.FetchBytesWithOptions(context.Background(), target, http.MethodGet, nil, nil, FetchBytesOptions{})
			if !errors.Is(err, ErrURLBlocked) {
				t.Fatalf("fetch err = %v", err)
			}
		})
	}
}

func TestSameFetchURLNormalizesIDNA(t *testing.T) {
	if !sameFetchURL("http://xn--bcher-kva.de/start", "http://bücher.de/start") {
		t.Fatal("IDNA-equivalent fetch URLs did not match")
	}
}

func TestSameFetchURLNormalizesIPv6(t *testing.T) {
	if !sameFetchURL("http://[2001:4860:4860::8888]/start", "http://[2001:4860:4860:0:0:0:0:8888]/start") {
		t.Fatal("IPv6-equivalent fetch URLs did not match")
	}
}

func TestSameFetchURLNormalizesLegacyIPv4(t *testing.T) {
	for _, legacy := range []string{
		"http://0135.0270.0330.0042/start",
		"http://0x5db8d822/start",
		"http://1572395042/start",
		"http://93.184.55330/start",
	} {
		if !sameFetchURL("http://93.184.216.34/start", legacy) {
			t.Fatalf("IPv4-equivalent fetch URL did not match: %s", legacy)
		}
	}
}

func TestFetchURLNormalizationRejectsInvalidLegacyIPv4(t *testing.T) {
	for _, host := range []string{
		"1.2.3.4.5",
		"1.2.3.999",
		"256.2.3.4",
		"0x100000000",
		"0xnothex",
	} {
		if _, ok := canonicalURLIPv4(host); ok {
			t.Fatalf("invalid IPv4 host was accepted: %s", host)
		}
	}
	if got, ok := canonicalURLIPv4("127.0.0.1."); !ok || got != "127.0.0.1" {
		t.Fatalf("trailing-dot IPv4 = %q, %v", got, ok)
	}
	if got, ok := canonicalURLIPv4("0x"); !ok || got != "0.0.0.0" {
		t.Fatalf("empty hex digits = %q, %v", got, ok)
	}
}

func TestFetchURLNormalizationEdgeCases(t *testing.T) {
	if sameFetchURL("http://example.com/%", "http://example.com/") {
		t.Fatal("invalid URL matched a valid URL")
	}
	if !sameFetchURL("http://example.com:8080/start", "HTTP://EXAMPLE.COM:08080/start#fragment") {
		t.Fatal("equivalent non-default ports did not match")
	}
	for input, want := range map[string]string{
		"":         "/",
		"/path/.":  "/path/",
		"/path/..": "/",
	} {
		if got := cleanURLPath(input); got != want {
			t.Fatalf("cleanURLPath(%q) = %q, want %q", input, got, want)
		}
	}
	if got := headerValue(map[string]string{"X-Test": "yes"}, "missing"); got != "" {
		t.Fatalf("missing header = %q", got)
	}
}

func TestGuardrailFetchObservationInactiveCases(t *testing.T) {
	page := &Page{browser: &Browser{cfg: defaultLaunchConfig()}}
	if page.activeFetchOwns(nil) {
		t.Fatal("nil request belongs to inactive fetch")
	}
	if page.activeFetchOwns(&fakeRequest{url: "http://example.com", method: http.MethodGet}) {
		t.Fatal("request belongs to inactive fetch")
	}
	page.guardrailFetchContext = context.Background()
	for name, response := range map[string]*fakeResponse{
		"missing_location": {url: "http://example.com", status: http.StatusFound},
		"invalid_base":     {url: "%", status: http.StatusFound, headers: map[string]string{"Location": "http://10.0.0.1"}},
		"invalid_target":   {url: "http://example.com", status: http.StatusFound, headers: map[string]string{"Location": "%"}},
	} {
		if reason, blocked := page.blockedFetchRedirectReason(response); blocked || reason != "" {
			t.Fatalf("%s classified as blocked: %q", name, reason)
		}
	}
	page.guardrailFetchContext = nil
	if reason, blocked := page.blockedFetchRedirectReason(&fakeResponse{
		url: "http://example.com", status: http.StatusFound,
		headers: map[string]string{"Location": "http://10.0.0.1"},
	}); blocked || reason != "" {
		t.Fatalf("redirect without active context classified as blocked: %q", reason)
	}
}

func TestAcquireFetchHonorsCancellationWhileBusy(t *testing.T) {
	page := &Page{}
	if err := page.acquireFetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := page.acquireFetch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("busy acquire error = %v", err)
	}
	page.releaseFetch()
}

type errObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *errObservedContext) Err() error {
	err := c.Context.Err()
	if err == nil {
		c.once.Do(func() { close(c.observed) })
	}
	return err
}

func TestFetchBytesWithOptionsReturnsBusyAcquireCancellation(t *testing.T) {
	page := &Page{}
	if err := page.acquireFetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	ctx := &errObservedContext{Context: base, observed: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, err := page.FetchBytesWithOptions(ctx, "http://example.com", http.MethodGet, nil, nil, FetchBytesOptions{})
		result <- err
	}()
	<-ctx.observed
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch error = %v", err)
	}
	page.releaseFetch()
}

func TestFetchBytesWithOptionsCancelsWhileWaitingForPageFetch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	raw := &fakePage{evaluateResult: map[string]any{"ok": true, "status": 200, "body": "ok"}}
	raw.evaluateHook = func(*fakePage) {
		close(started)
		<-release
	}
	page := &Page{raw: raw}
	firstDone := make(chan error, 1)
	go func() {
		_, err := page.FetchBytesWithOptions(context.Background(), "http://public.example/first", http.MethodGet, nil, nil, FetchBytesOptions{})
		firstDone <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := page.FetchBytesWithOptions(ctx, "http://public.example/second", http.MethodGet, nil, nil, FetchBytesOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting fetch err = %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first fetch err = %v", err)
	}
}

func TestFetchBytesWithOptionsIgnoresUnrelatedBlockedRequest(t *testing.T) {
	marker := testBlockedMarker(t)
	unrelated := &fakeRequest{url: "http://unrelated.example/", method: http.MethodGet}
	raw := &fakePage{evaluateResult: map[string]any{
		"ok": true, "url": "http://public.example/start", "status": 200, "body": "ok",
	}}
	raw.evaluateHook = func(page *fakePage) {
		page.onResponse(&fakeResponse{
			status:  http.StatusForbidden,
			headers: map[string]string{netguard.BlockedHeader: marker},
			request: unrelated,
		})
	}
	page := &Page{raw: raw}
	result, err := page.FetchBytesWithOptions(context.Background(), "http://public.example/start", http.MethodGet, nil, nil, FetchBytesOptions{})
	if err != nil || string(result.Body) != "ok" {
		t.Fatalf("fetch result=%#v err=%v", result, err)
	}
	if _, ok := netguard.ConsumeBlockedMarker(marker); !ok {
		t.Fatal("unrelated response marker was consumed")
	}
}

func testBlockedMarker(t *testing.T) string {
	t.Helper()
	rr := httptest.NewRecorder()
	proxy := netguard.FilteringProxy{Validator: netguard.NewValidator(policy.DefaultConfig(), nil)}
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	marker := rr.Header().Get(netguard.BlockedHeader)
	if rr.Code != http.StatusForbidden || marker == "" {
		t.Fatalf("test filtering proxy response code=%d headers=%v", rr.Code, rr.Header())
	}
	return marker
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

func TestFetchBytesWithOptionsPreservesBinaryRequestAndResponse(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateResult: map[string]any{"ok": true, "status": 200, "body_base64": "/wAB", "body_encoding": "base64", "url": "https://example.com", "headers": map[string]string{}, "truncated": false}}}
	result, err := page.FetchBytesWithOptions(context.Background(), "https://example.com", "POST", nil, []byte{0xff, 0x00, 0x01}, FetchBytesOptions{MaxBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != 200 || string(result.Body) != string([]byte{0xff, 0x00, 0x01}) || result.Truncated {
		t.Fatalf("binary result = %#v", result)
	}
	arg, ok := page.raw.(*fakePage).internalEvalArg.(map[string]any)
	if !ok || arg["bodyBase64"] != "/wAB" || arg["hasBody"] != true {
		t.Fatalf("binary fetch arg = %#v", page.raw.(*fakePage).internalEvalArg)
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
	if strings.Contains(browserFetchExpression, "response.text()") || strings.Contains(browserFetchExpression, "TextDecoder") {
		t.Fatal("browser fetch expression decodes response as text")
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
browserFetchExpression({url: "https://example.com/stream", method: "GET", headers: {}, bodyBase64: "", hasBody: false, maxBytes: 5})
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
	if got.Result["body_base64"] != "YWJjZGU=" || got.Result["body_encoding"] != "base64" || got.Result["truncated"] != true || got.CancelCalls != 1 || got.ReadCalls != 2 {
		t.Fatalf("stream result = %#v", got)
	}
}

func TestBrowserFetchExpressionSendsBinaryRequestBody(t *testing.T) {
	output := runRootNodeExpression(t, `
const browserFetchExpression = `+browserFetchExpression+`;
let requestBytes = [];
globalThis.fetch = async (url, opts) => {
  requestBytes = Array.from(opts.body || []);
  return {
    url,
    status: 200,
    headers: {forEach: () => {}},
    body: {getReader: () => ({
      read: async () => ({done: true}),
      cancel: async () => {}
    })}
  };
};
browserFetchExpression({url: "https://example.com/post", method: "POST", headers: {}, bodyBase64: "/wAB", hasBody: true, maxBytes: 5})
  .then(result => console.log(JSON.stringify({result, requestBytes})))
  .catch(error => { console.error(error); process.exit(1); });
`)
	var got struct {
		RequestBytes []int          `json:"requestBytes"`
		Result       map[string]any `json:"result"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if fmt.Sprint(got.RequestBytes) != "[255 0 1]" || got.Result["body_base64"] != "" {
		t.Fatalf("binary request result = %#v", got)
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
browserFetchExpression({url: "https://example.com/exact", method: "GET", headers: {}, bodyBase64: "", hasBody: false, maxBytes: 5})
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
	if got.Result["body_base64"] != "YWJjZGU=" || got.Result["truncated"] != true || got.CancelCalls != 1 || got.ReadCalls != 1 {
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
browserFetchExpression({url: "https://example.com/huge", method: "GET", headers: {}, bodyBase64: "", hasBody: false, maxBytes: 3})
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
	if got.Result["body_base64"] != "QUJD" || got.Result["truncated"] != true || got.CancelCalls != 1 {
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
browserFetchExpression({url: "https://example.com/empty", method: "HEAD", headers: {}, bodyBase64: "", hasBody: false, maxBytes: 5})
  .then(result => console.log(JSON.stringify(result)))
  .catch(error => { console.error(error); process.exit(1); });
`)
	var got map[string]any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if got["ok"] != true || got["body_base64"] != "" || got["body_encoding"] != "base64" || got["status"] != float64(204) || got["truncated"] != false {
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
