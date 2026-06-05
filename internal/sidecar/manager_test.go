package sidecar

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const diagnosticSecretFixture = `proxy=http://user:pass@example.com Authorization: Bearer abc.def Cookie: sid=secret Set-Cookie: auth=secret wss://127.0.0.1:9222/rawtoken token=secret {"cookies":[{"name":"sid","value":"cookie-secret"}],"origins":[{"origin":"https://example.com","localStorage":[{"name":"token","value":"storage-secret"}]}]}`

func assertNoDiagnosticSecrets(t *testing.T, text string) {
	t.Helper()
	for i, secret := range []string{"user:pass", "abc.def", "sid=secret", "auth=secret", "/rawtoken", "token=secret", "cookie-secret", "storage-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostic secret fixture %d survived", i)
		}
	}
}

type failingDiagnosticReader struct{}

func (failingDiagnosticReader) Read([]byte) (int, error) {
	return 0, errors.New(diagnosticSecretFixture)
}

func TestWriteLauncherReflectsConfig(t *testing.T) {
	venv := t.TempDir()
	profile := filepath.Join(t.TempDir(), "profile")
	path, err := WriteLauncher(venv, Config{Headless: 1, Persistent: true, UserDataDir: profile})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`\"headless\":false`,
		`\"persistent_context\":true`,
		profile,
		`persistent_user_data_dir = launch_kwargs.pop("user_data_dir", None)`,
		`payload["_userDataDir"] = persistent_user_data_dir`,
		`payload["_sharedBrowser"] = True`,
		`config.pop("proxy", None)`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("launcher missing %q:\n%s", want, text)
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("launcher perms = %o", st.Mode().Perm())
	}
}

func TestWriteLauncherIncludesProxyAndPersonaOptions(t *testing.T) {
	venv := t.TempDir()
	humanize := 1.25
	path, err := WriteLauncher(venv, Config{
		Headless:        0,
		LaunchProxy:     &ProxyConfig{Server: "http://127.0.0.1:4567", Username: "user", Password: "pass"},
		GeoIP:           true,
		Humanize:        &humanize,
		OS:              "linux",
		Locale:          []string{"en-US", "en"},
		BlockImages:     true,
		BlockWebRTC:     true,
		BlockWebGL:      true,
		Addons:          []string{"/addon"},
		Window:          &Size{Width: 1200, Height: 800},
		Screen:          &Size{Width: 1440, Height: 900},
		WebGL:           &WebGLConfig{Vendor: "Intel", Renderer: "Iris"},
		FirefoxPrefs:    map[string]any{"privacy.resistFingerprinting": false},
		BrowserArgs:     []string{"--safe-mode"},
		CustomFontsOnly: true,
		FFVersion:       135,
		CamoufoxDebug:   true,
		Fonts:           []string{"Inter"},
		Fingerprint:     map[string]any{"navigator.userAgent": "ua"},
		MainWorldEval:   true,
		EnableCache:     true,
		DisableCOOP:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`\"proxy\":{\"password\":\"pass\",\"server\":\"http://127.0.0.1:4567\",\"username\":\"user\"}`,
		`\"geoip\":true`,
		`\"humanize\":1.25`,
		`\"os\":\"linux\"`,
		`\"locale\":[\"en-US\",\"en\"]`,
		`\"block_images\":true`,
		`\"block_webrtc\":true`,
		`\"block_webgl\":true`,
		`\"addons\":[\"/addon\"]`,
		`\"window\":{\"height\":800,\"width\":1200}`,
		`\"screen\":{\"height\":900,\"width\":1440}`,
		`from browserforge.fingerprints import Screen`,
		`launch_kwargs["screen"] = Screen(min_width=width, max_width=width, min_height=height, max_height=height)`,
		`launch_kwargs["window"] = (window_value.get("width"), window_value.get("height"))`,
		`\"webgl_config\":{\"renderer\":\"Iris\",\"vendor\":\"Intel\"}`,
		`launch_kwargs["webgl_config"] = (webgl_value.get("vendor"), webgl_value.get("renderer"))`,
		`\"firefox_user_prefs\":{\"privacy.resistFingerprinting\":false}`,
		`\"args\":[\"--safe-mode\"]`,
		`\"custom_fonts_only\":true`,
		`\"ff_version\":135`,
		`\"debug\":true`,
		`\"fonts\":[\"Inter\"]`,
		`\"config\":{\"navigator.userAgent\":\"ua\"}`,
		`\"main_world_eval\":true`,
		`\"enable_cache\":true`,
		`\"disable_coop\":true`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("launcher missing %q:\n%s", want, text)
		}
	}
}

func TestWriteLauncherDefaultsAndErrorBranches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := WriteLauncher("", Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "gomoufox_sidecar_launcher.py") {
		t.Fatalf("default launcher path = %q", path)
	}

	if _, err := WriteLauncher(t.TempDir(), Config{Fingerprint: map[string]any{"bad": func() {}}}); err == nil {
		t.Fatal("launcher with unmarshalable fingerprint succeeded")
	}

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteLauncher(filepath.Join(blocker, "child"), Config{}); err == nil {
		t.Fatal("launcher under regular file succeeded")
	}
	venvWithBlockedLauncher := t.TempDir()
	if err := os.Mkdir(filepath.Join(venvWithBlockedLauncher, "gomoufox_sidecar_launcher.py"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteLauncher(venvWithBlockedLauncher, Config{}); err == nil {
		t.Fatal("launcher written over directory succeeded")
	}
}

func TestWriteLauncherRejectsFinalSymlink(t *testing.T) {
	venv := t.TempDir()
	target := filepath.Join(t.TempDir(), "launcher-target.py")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(venv, "gomoufox_sidecar_launcher.py")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := WriteLauncher(venv, Config{}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("launcher symlink err = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("target content = %q", data)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("launcher path was replaced unexpectedly: %s", info.Mode())
	}
}

func TestProfileLockRejectsSecondLockAndParentLock(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireProfileLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireProfileLock(dir); !errors.Is(err, ErrProfileInUse) {
		t.Fatalf("second lock err = %v", err)
	}
	if !strings.HasSuffix(lock.Path(), ".gomoufox.lock") {
		t.Fatalf("lock path = %q", lock.Path())
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	parentLockDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(parentLockDir, "parent.lock"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireProfileLock(parentLockDir); !errors.Is(err, ErrProfileInUse) {
		t.Fatalf("parent.lock err = %v", err)
	}
	if _, err := AcquireProfileLock(""); !errors.Is(err, ErrProfileInUse) {
		t.Fatalf("empty profile lock err = %v", err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireProfileLock(filepath.Join(blocker, "profile")); err == nil {
		t.Fatal("profile lock under regular file succeeded")
	}
	lockFileDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(lockFileDir, ".gomoufox.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireProfileLock(lockFileDir); err == nil {
		t.Fatal("profile lock over directory succeeded")
	}
	var nilProfileLock *ProfileLock
	if nilProfileLock.Path() != "" {
		t.Fatalf("nil profile lock path = %q", nilProfileLock.Path())
	}
	if err := nilProfileLock.Release(); err != nil {
		t.Fatalf("nil profile lock release err = %v", err)
	}
}

func TestPidfileInvalidAndStaleProcessReaping(t *testing.T) {
	if processExists(0) {
		t.Fatal("pid 0 reported alive")
	}
	venv := t.TempDir()
	if err := os.WriteFile(pidfilePath(venv), []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReapStalePidfile(venv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pidfilePath(venv)); !os.IsNotExist(err) {
		t.Fatalf("invalid pidfile still exists: %v", err)
	}

	oldProcessExists := sidecarProcessExists
	oldTerminatePID := sidecarTerminatePID
	t.Cleanup(func() {
		sidecarProcessExists = oldProcessExists
		sidecarTerminatePID = oldTerminatePID
	})
	terminateErr := errors.New("terminate failed")
	sidecarProcessExists = func(int) bool { return true }
	sidecarTerminatePID = func(int) error { return terminateErr }
	if err := os.WriteFile(pidfilePath(venv), []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReapStalePidfile(venv); !errors.Is(err, ErrSidecarStart) || !strings.Contains(err.Error(), "terminate failed") {
		t.Fatalf("stale pid terminate err = %v", err)
	}
	sidecarProcessExists = oldProcessExists
	sidecarTerminatePID = oldTerminatePID

	if runtime.GOOS == "windows" {
		t.Skip("process reaping is experimental on Windows")
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = waitWithTimeout(cmd, time.Second)
		}
	})
	if err := writePidfile(venv, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := ReapStalePidfile(venv); err != nil {
		t.Fatal(err)
	}
	if err := waitWithTimeout(cmd, 2*time.Second); errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale process did not exit: %v", err)
	}
	if _, err := os.Stat(pidfilePath(venv)); !os.IsNotExist(err) {
		t.Fatalf("stale pidfile still exists: %v", err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePidfile(blocker, 1); err == nil {
		t.Fatal("pidfile under regular file succeeded")
	}
}

func TestProcessBoundaryNoProcessGuards(t *testing.T) {
	cmd := &exec.Cmd{}
	setProcessGroup(cmd)
	if err := assignProcessBoundary(cmd); err != nil {
		t.Fatalf("assign boundary err = %v", err)
	}
	releaseProcessBoundary(cmd)
	terminateProcessTree(cmd)
	killProcessTree(cmd)

	if runtime.GOOS == "windows" {
		return
	}
	live := exec.Command("sleep", "60")
	setProcessGroup(live)
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	killProcessTree(live)
	if err := waitWithTimeout(live, 2*time.Second); errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("killed process did not exit: %v", err)
	}
}

func TestManagerStartStopWithFakePython(t *testing.T) {
	venv := fakeVenv(t, `#!/bin/sh
grep -q '"proxy"' "$2" || exit 42
grep -q '127.0.0.1' "$2" || exit 43
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager := New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	endpoint, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "ws://127.0.0.1:4321/token" || manager.CurrentState() != StateReady {
		t.Fatalf("endpoint/state = %q/%v", endpoint, manager.CurrentState())
	}
	if manager.Endpoint() != endpoint || manager.PID() == 0 {
		t.Fatalf("accessors endpoint=%q pid=%d", manager.Endpoint(), manager.PID())
	}
	if info := manager.Info(); info.PID == 0 || info.WSEndpointRedacted != "ws://127.0.0.1:4321/<redacted>" {
		t.Fatalf("info = %#v", info)
	}
	if _, err := os.Stat(pidfilePath(venv)); err != nil {
		t.Fatalf("pidfile missing: %v", err)
	}
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("second start succeeded")
	}
	if manager.cfg.LaunchProxy == nil || !strings.HasPrefix(manager.cfg.LaunchProxy.Server, "http://127.0.0.1:") {
		t.Fatalf("launch proxy = %#v", manager.cfg.LaunchProxy)
	}
	proxyURL, err := url.Parse(manager.cfg.LaunchProxy.Server)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get("http://127.0.0.1/private")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "url blocked") {
		t.Fatalf("proxy response = %d %q", resp.StatusCode, body)
	}
	manager.Stop(context.Background())
	if manager.CurrentState() != StateDead {
		t.Fatalf("state after stop = %v", manager.CurrentState())
	}
	select {
	case <-manager.Done():
	case <-time.After(time.Second):
		t.Fatalf("manager done did not close")
	}
	if _, err := os.Stat(pidfilePath(venv)); !os.IsNotExist(err) {
		t.Fatalf("pidfile after stop = %v", err)
	}
}

func TestManagerStopCleanupAndDiagnosticsEdges(t *testing.T) {
	idle := New(Config{})
	idle.Stop(context.Background())
	if idle.CurrentState() != StateIdle {
		t.Fatalf("idle stop state = %v", idle.CurrentState())
	}

	lock, err := AcquireProfileLock(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := New(Config{VenvDir: t.TempDir()})
	manager.lock = lock
	var diagnostics bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&diagnostics, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	manager.forwardDiagnostics(strings.NewReader(diagnosticSecretFixture + "\n"))
	assertNoDiagnosticSecrets(t, diagnostics.String())
	diagnostics.Reset()
	manager.forwardDiagnostics(failingDiagnosticReader{})
	assertNoDiagnosticSecrets(t, diagnostics.String())
	manager.setDead()
	if err := lock.Release(); err != nil {
		t.Fatalf("released lock should be inert after setDead: %v", err)
	}
	manager.Stop(context.Background())
}

func TestManagerConfigReturnsDeepCopy(t *testing.T) {
	humanize := 1.25
	manager := New(Config{
		Locale:       []string{"en-US"},
		Addons:       []string{"/addon"},
		Fonts:        []string{"Inter"},
		BrowserArgs:  []string{"--safe-mode"},
		ExtraEnv:     []string{"A=B"},
		FirefoxPrefs: map[string]any{"pref": "one"},
		Fingerprint:  map[string]any{"ua": "one"},
		Proxy:        &ProxyConfig{Server: "http://operator.example:8080", Username: "u", Password: "p"},
		LaunchProxy:  &ProxyConfig{Server: "http://127.0.0.1:8888"},
		Window:       &Size{Width: 1200, Height: 800},
		Screen:       &Size{Width: 1440, Height: 900},
		WebGL:        &WebGLConfig{Vendor: "Intel", Renderer: "Iris"},
		Humanize:     &humanize,
	})
	cfg := manager.Config()
	cfg.Locale[0] = "fr-FR"
	cfg.Addons[0] = "/other"
	cfg.Fonts[0] = "Other"
	cfg.BrowserArgs[0] = "--other"
	cfg.ExtraEnv[0] = "C=D"
	cfg.FirefoxPrefs["pref"] = "two"
	cfg.Fingerprint["ua"] = "two"
	cfg.Proxy.Server = "http://changed"
	cfg.LaunchProxy.Server = "http://changed"
	cfg.Window.Width = 1
	cfg.Screen.Width = 1
	cfg.WebGL.Vendor = "changed"

	again := manager.Config()
	if again.Locale[0] != "en-US" || again.Addons[0] != "/addon" || again.Fonts[0] != "Inter" || again.BrowserArgs[0] != "--safe-mode" || again.ExtraEnv[0] != "A=B" {
		t.Fatalf("slice copy failed: %#v", again)
	}
	if again.FirefoxPrefs["pref"] != "one" || again.Fingerprint["ua"] != "one" || again.Proxy.Server != "http://operator.example:8080" || again.LaunchProxy.Server != "http://127.0.0.1:8888" {
		t.Fatalf("map/proxy copy failed: %#v", again)
	}
	if again.Window.Width != 1200 || again.Screen.Width != 1440 || again.WebGL.Vendor != "Intel" {
		t.Fatalf("nested config copy failed: %#v", again)
	}
}

func TestManagerFailsClosedForUnsupportedOperatorProxy(t *testing.T) {
	venv := fakeVenv(t, `#!/bin/sh
echo "should not start"
`)
	manager := New(Config{VenvDir: venv, ConnectTimeout: time.Second, Proxy: &ProxyConfig{Server: "socks5://proxy.example:1080"}})
	if _, err := manager.Start(context.Background()); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("Start err = %v", err)
	}
}

func TestManagerFilteringProxyOperatorProxyBranches(t *testing.T) {
	for _, proxy := range []string{"http://", "https://proxy.example:8443", "ftp://proxy.example:21"} {
		manager := New(Config{Proxy: &ProxyConfig{Server: proxy}})
		if err := manager.startFilteringProxy(context.Background()); !errors.Is(err, ErrSidecarStart) {
			t.Fatalf("proxy %q err = %v", proxy, err)
		}
	}

	manager := New(Config{Proxy: &ProxyConfig{Server: "http://proxy.example:8080", Username: "user"}})
	if err := manager.startFilteringProxy(context.Background()); err != nil {
		t.Fatalf("http proxy username branch err = %v", err)
	}
	if manager.cfg.LaunchProxy == nil || !strings.HasPrefix(manager.cfg.LaunchProxy.Server, "http://127.0.0.1:") {
		t.Fatalf("launch proxy = %#v", manager.cfg.LaunchProxy)
	}
	manager.setDead()
}

func TestManagerDirectNetworkSkipsFilteringProxy(t *testing.T) {
	venv := fakeVenv(t, `#!/bin/sh
grep -q '\\"proxy\\":' "$2" && exit 44
printf 'Websocket endpoint:\033[93m ws://127.0.0.1:1234/direct \033[0m\n'
sleep 5
`)
	manager := New(Config{VenvDir: venv, ConnectTimeout: time.Second, DirectNetwork: true})
	endpoint, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "ws://127.0.0.1:1234/direct" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if manager.cfg.LaunchProxy != nil {
		t.Fatalf("launch proxy = %#v", manager.cfg.LaunchProxy)
	}
	manager.Stop(context.Background())

	venv = fakeVenv(t, `#!/bin/sh
printf 'Websocket endpoint: ws://127.0.0.1:1234/direct-proxy\n'
sleep 5
`)
	manager = New(Config{
		VenvDir:        venv,
		ConnectTimeout: time.Second,
		DirectNetwork:  true,
		Proxy:          &ProxyConfig{Server: "http://proxy.example:8080", Username: "user", Password: "pass"},
	})
	if _, err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.cfg.LaunchProxy == nil || manager.cfg.LaunchProxy == manager.cfg.Proxy || manager.cfg.LaunchProxy.Server != "http://proxy.example:8080" {
		t.Fatalf("direct launch proxy = %#v original=%#v", manager.cfg.LaunchProxy, manager.cfg.Proxy)
	}
	manager.Stop(context.Background())
}

func TestManagerFilteringProxyListenEdges(t *testing.T) {
	oldListen := sidecarListen
	restore := func() { sidecarListen = oldListen }
	t.Cleanup(restore)

	sidecarListen = func(network, address string) (net.Listener, error) {
		return nil, errors.New("listen failed")
	}
	if err := New(Config{}).startFilteringProxy(context.Background()); !errors.Is(err, ErrSidecarStart) || !strings.Contains(err.Error(), "listen failed") {
		t.Fatalf("listen failure err = %v", err)
	}

	listener := &failingListener{accepted: make(chan struct{})}
	sidecarListen = func(network, address string) (net.Listener, error) {
		return listener, nil
	}
	if err := New(Config{}).startFilteringProxy(context.Background()); err != nil {
		t.Fatalf("start proxy with failing listener err = %v", err)
	}
	select {
	case <-listener.accepted:
	case <-time.After(time.Second):
		t.Fatal("proxy server did not accept on fake listener")
	}
	time.Sleep(10 * time.Millisecond)
}

func TestManagerStartsWithHTTPOperatorProxy(t *testing.T) {
	venv := fakeVenv(t, `#!/bin/sh
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager := New(Config{VenvDir: venv, ConnectTimeout: time.Second, Proxy: &ProxyConfig{Server: "http://proxy.example:8080", Username: "user", Password: "pass"}})
	if _, err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start err = %v", err)
	}
	manager.Stop(context.Background())
}

func TestManagerStopWithCanceledContextKillsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell signal trapping is unix-only")
	}
	venv := fakeVenv(t, `#!/bin/sh
trap '' TERM
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager := New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager.Stop(ctx)
	select {
	case <-manager.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not stop after canceled-context kill")
	}
}

func TestManagerStopTimeoutKillsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell signal trapping is unix-only")
	}
	oldTimeout := sidecarStopKillTimeout
	sidecarStopKillTimeout = 10 * time.Millisecond
	t.Cleanup(func() { sidecarStopKillTimeout = oldTimeout })

	venv := fakeVenv(t, `#!/bin/sh
trap '' TERM
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager := New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.Stop(context.Background())
	select {
	case <-manager.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not stop after timeout kill")
	}
}

type failingListener struct {
	accepted chan struct{}
}

func (l *failingListener) Accept() (net.Conn, error) {
	select {
	case <-l.accepted:
	default:
		close(l.accepted)
	}
	return nil, errors.New("accept failed")
}

func (l *failingListener) Close() error { return nil }

func (l *failingListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 39876}
}

func TestManagerPersistentProfileStartsWithExclusiveLock(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile")
	venv := fakeVenv(t, `#!/bin/sh
grep -q 'persistent_context' "$2" || exit 44
grep -q 'user_data_dir' "$2" || exit 45
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager := New(Config{VenvDir: venv, ConnectTimeout: time.Second, Persistent: true, UserDataDir: profile})
	endpoint, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "ws://127.0.0.1:4321/token" || manager.CurrentState() != StateReady {
		t.Fatalf("endpoint/state = %q/%v", endpoint, manager.CurrentState())
	}
	if _, err := AcquireProfileLock(profile); !errors.Is(err, ErrProfileInUse) {
		t.Fatalf("second profile lock err = %v", err)
	}
	manager.Stop(context.Background())
	lock, err := AcquireProfileLock(profile)
	if err != nil {
		t.Fatalf("profile lock was not released after stop: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPersistentProfileLockFailures(t *testing.T) {
	manager := New(Config{Persistent: true})
	if _, err := manager.Start(context.Background()); !errors.Is(err, ErrProfileInUse) {
		t.Fatalf("empty profile start err = %v", err)
	}
	if manager.CurrentState() != StateDead {
		t.Fatalf("state after empty profile start = %v", manager.CurrentState())
	}

	profile := filepath.Join(t.TempDir(), "profile")
	venv := fakeVenv(t, `#!/bin/sh
exit 0
`)
	manager = New(Config{VenvDir: venv, ConnectTimeout: time.Millisecond, Persistent: true, UserDataDir: profile})
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("persistent start without endpoint succeeded")
	}
	lock, err := AcquireProfileLock(profile)
	if err != nil {
		t.Fatalf("profile lock was not released after launch failure: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerStartFailureEdges(t *testing.T) {
	manager := New(Config{VenvDir: t.TempDir(), ConnectTimeout: time.Millisecond})
	if manager.PID() != 0 {
		t.Fatalf("idle pid = %d", manager.PID())
	}
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("start without venv python succeeded")
	}
	if manager.CurrentState() != StateDead {
		t.Fatalf("state after failed start = %v", manager.CurrentState())
	}
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("restart from dead state succeeded")
	}

	if runtime.GOOS == "windows" {
		t.Skip("non-executable fake python is unix-only")
	}
	venv := t.TempDir()
	bin := filepath.Join(venv, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "python"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager = New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("start with non-executable python succeeded")
	}

	venv = fakeVenv(t, `#!/bin/sh
exit 0
`)
	manager = New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("start without endpoint succeeded")
	}
}

func TestManagerStartCleanupErrorBranches(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := New(Config{VenvDir: blocker, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("start with unreadable pidfile path succeeded")
	}
	if manager.CurrentState() != StateDead {
		t.Fatalf("pidfile reap failure state = %v", manager.CurrentState())
	}

	venv := fakeVenv(t, `#!/bin/sh
echo "should not start"
`)
	manager = New(Config{VenvDir: venv, ConnectTimeout: time.Second, Fingerprint: map[string]any{"bad": func() {}}})
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("start with unmarshalable launcher config succeeded")
	}
	if manager.CurrentState() != StateDead {
		t.Fatalf("launcher failure state = %v", manager.CurrentState())
	}

	stdoutErr := errors.New("stdout failed")
	oldStdoutPipe := sidecarStdoutPipe
	sidecarStdoutPipe = func(*exec.Cmd) (io.ReadCloser, error) { return nil, stdoutErr }
	t.Cleanup(func() { sidecarStdoutPipe = oldStdoutPipe })
	venv = fakeVenv(t, `#!/bin/sh
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager = New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "stdout failed") {
		t.Fatalf("stdout pipe err = %v", err)
	}
	sidecarStdoutPipe = oldStdoutPipe

	stderrErr := errors.New("stderr failed")
	oldStderrPipe := sidecarStderrPipe
	sidecarStderrPipe = func(*exec.Cmd) (io.ReadCloser, error) { return nil, stderrErr }
	t.Cleanup(func() { sidecarStderrPipe = oldStderrPipe })
	venv = fakeVenv(t, `#!/bin/sh
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager = New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); !errors.Is(err, stderrErr) {
		t.Fatalf("stderr pipe err = %v", err)
	}
	sidecarStderrPipe = oldStderrPipe

	assignErr := errors.New("assign failed")
	oldAssign := sidecarAssignProcessBoundary
	sidecarAssignProcessBoundary = func(*exec.Cmd) error { return assignErr }
	t.Cleanup(func() { sidecarAssignProcessBoundary = oldAssign })
	venv = fakeVenv(t, `#!/bin/sh
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager = New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); !errors.Is(err, ErrSidecarStart) || !strings.Contains(err.Error(), "assign failed") {
		t.Fatalf("assign boundary err = %v", err)
	}
	sidecarAssignProcessBoundary = oldAssign

	writeErr := errors.New("pidfile failed")
	oldWritePidfile := sidecarWritePidfile
	sidecarWritePidfile = func(string, int) error { return writeErr }
	t.Cleanup(func() { sidecarWritePidfile = oldWritePidfile })
	venv = fakeVenv(t, `#!/bin/sh
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager = New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); !errors.Is(err, writeErr) {
		t.Fatalf("pidfile hook err = %v", err)
	}
	sidecarWritePidfile = oldWritePidfile

	venv = fakeVenv(t, `#!/bin/sh
	echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	if err := os.Mkdir(filepath.Join(venv, "gomoufox_sidecar.pid"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager = New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); err == nil {
		t.Fatal("start with blocked pidfile write succeeded")
	}
	if manager.CurrentState() != StateDead {
		t.Fatalf("pidfile write failure state = %v", manager.CurrentState())
	}
}

func fakeVenv(t *testing.T, script string) string {
	t.Helper()
	venv := t.TempDir()
	bin := filepath.Join(venv, "bin")
	if runtime.GOOS == "windows" {
		bin = filepath.Join(venv, "Scripts")
	}
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(bin, "python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(bin, "python.exe")
	}
	if err := os.WriteFile(python, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return venv
}

func waitWithTimeout(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}
