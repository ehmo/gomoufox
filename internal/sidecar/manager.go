package sidecar

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ehmo/gomoufox/internal/netguard"
	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/safefile"
)

var (
	sidecarListen                = net.Listen
	sidecarAssignProcessBoundary = assignProcessBoundary
	sidecarWritePidfile          = writePidfile
	sidecarStdoutPipe            = func(cmd *exec.Cmd) (io.ReadCloser, error) { return cmd.StdoutPipe() }
	sidecarStderrPipe            = func(cmd *exec.Cmd) (io.ReadCloser, error) { return cmd.StderrPipe() }
	sidecarStopKillTimeout       = 5 * time.Second
)

const sidecarStartupStderrLimit = 8 * 1024

type Manager struct {
	cfg      Config
	mu       sync.Mutex
	state    State
	cmd      *exec.Cmd
	endpoint string
	done     chan struct{}
	info     Info
	lock     *ProfileLock
	proxy    *http.Server
	proxyLn  net.Listener
	pidfile  string
}

func New(cfg Config) *Manager {
	cfg.Runtime = normalizeRuntime(cfg.Runtime)
	return &Manager{cfg: cfg, state: StateIdle, done: make(chan struct{})}
}

func normalizeRuntime(runtimeName string) string {
	if runtimeName == "" {
		return RuntimeNodeDirect
	}
	return runtimeName
}

func (m *Manager) launchCommand(ctx context.Context, python, runtimeName string) (*exec.Cmd, error) {
	switch runtimeName {
	case RuntimePython:
		launchJSON, err := launchArgsJSON(m.cfg)
		if err != nil {
			return nil, err
		}
		launcher, err := writeLauncher(m.cfg.VenvDir)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(python, "-u", launcher)
		cmd.Stdin = bytes.NewReader(launchJSON)
		return cmd, nil
	case RuntimeNodeDirect:
		spec, err := buildNodeDirectSpec(ctx, python, m.cfg)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(spec.NodeJS, spec.LaunchScript)
		cmd.Dir = spec.CWD
		cmd.Stdin = strings.NewReader(spec.StdinBase64)
		return cmd, nil
	default:
		return nil, fmt.Errorf("%w: unsupported sidecar runtime %q", ErrSidecarStart, runtimeName)
	}
}

func (m *Manager) Start(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.state != StateIdle {
		m.mu.Unlock()
		return "", fmt.Errorf("sidecar start from invalid state %d", m.state)
	}
	m.state = StateStarting
	m.mu.Unlock()

	timeout := m.cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := ReapStalePidfile(m.cfg.VenvDir); err != nil {
		m.setDead()
		return "", err
	}
	if m.cfg.Persistent {
		lock, err := AcquireProfileLock(m.cfg.UserDataDir)
		if err != nil {
			m.setDead()
			return "", err
		}
		m.mu.Lock()
		m.lock = lock
		m.mu.Unlock()
	}
	runtimeName := normalizeRuntime(m.cfg.Runtime)
	if runtimeName != RuntimePython && runtimeName != RuntimeNodeDirect {
		m.setDead()
		return "", fmt.Errorf("%w: unsupported sidecar runtime %q", ErrSidecarStart, runtimeName)
	}
	var python string
	if runtimeName == RuntimePython {
		var err error
		python, err = VenvPython(m.cfg.VenvDir)
		if err != nil {
			m.setDead()
			return "", err
		}
	}
	if m.cfg.DirectNetwork {
		if m.cfg.Proxy != nil {
			copy := *m.cfg.Proxy
			m.cfg.LaunchProxy = &copy
		}
	} else {
		if err := m.startFilteringProxy(readyCtx); err != nil {
			m.setDead()
			return "", err
		}
	}
	cmd, err := m.launchCommand(readyCtx, python, runtimeName)
	if err != nil {
		m.setDead()
		return "", err
	}
	cmd.Env = append(os.Environ(), m.cfg.ExtraEnv...)
	setProcessGroup(cmd)
	stdout, err := sidecarStdoutPipe(cmd)
	if err != nil {
		m.setDead()
		return "", fmt.Errorf("%w: %v", errors.New("stdout pipe"), err)
	}
	stderr, err := sidecarStderrPipe(cmd)
	if err != nil {
		m.setDead()
		return "", err
	}
	if err := cmd.Start(); err != nil {
		m.setDead()
		return "", err
	}
	if err := sidecarAssignProcessBoundary(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		m.setDead()
		return "", fmt.Errorf("%w: assign process boundary: %v", ErrSidecarStart, err)
	}
	m.mu.Lock()
	m.cmd = cmd
	m.info.Runtime = runtimeName
	m.info.PID = cmd.Process.Pid
	m.mu.Unlock()
	diagnostics := newStartupDiagnostics(sidecarStartupStderrLimit)
	diagnosticsDone := make(chan struct{})
	go func() {
		defer close(diagnosticsDone)
		m.forwardDiagnostics(stderr, diagnostics)
	}()
	waitStarted := false
	startWait := func() {
		if !waitStarted {
			waitStarted = true
			go m.wait()
		}
	}
	pidfile, err := sidecarWritePidfile(m.cfg.VenvDir, cmd.Process.Pid)
	if err != nil {
		startWait()
		m.Stop(context.Background())
		return "", err
	}
	m.mu.Lock()
	m.pidfile = pidfile
	m.mu.Unlock()

	endpoint, err := ParseEndpoint(readyCtx, stdout, timeout)
	startWait()
	if err != nil {
		m.Stop(context.Background())
		return "", startupErrorWithDiagnostics(err, diagnostics, diagnosticsDone)
	}
	m.mu.Lock()
	m.endpoint = endpoint
	m.info.WSEndpointRedacted = RedactEndpoint(endpoint)
	m.state = StateReady
	m.mu.Unlock()
	return endpoint, nil
}

func (m *Manager) Stop(ctx context.Context) {
	m.mu.Lock()
	if m.state == StateDead || m.state == StateIdle {
		m.mu.Unlock()
		return
	}
	m.state = StateShuttingDown
	cmd := m.cmd
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		terminateProcessTree(cmd)
		select {
		case <-m.done:
		case <-time.After(sidecarStopKillTimeout):
			killProcessTree(cmd)
			<-m.done
		case <-ctx.Done():
			killProcessTree(cmd)
		}
	}
	m.setDead()
}

func (m *Manager) Endpoint() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.endpoint
}

func (m *Manager) CurrentState() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Manager) Done() <-chan struct{} { return m.done }

func (m *Manager) PID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd == nil || m.cmd.Process == nil {
		return 0
	}
	return m.cmd.Process.Pid
}

func (m *Manager) Info() Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info
}

func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg := m.cfg
	cfg.Locale = append([]string(nil), cfg.Locale...)
	cfg.Addons = append([]string(nil), cfg.Addons...)
	cfg.Fonts = append([]string(nil), cfg.Fonts...)
	cfg.BrowserArgs = append([]string(nil), cfg.BrowserArgs...)
	cfg.ExtraEnv = append([]string(nil), cfg.ExtraEnv...)
	if cfg.FirefoxPrefs != nil {
		cfg.FirefoxPrefs = map[string]any{}
		for key, value := range m.cfg.FirefoxPrefs {
			cfg.FirefoxPrefs[key] = value
		}
	}
	if cfg.Fingerprint != nil {
		cfg.Fingerprint = map[string]any{}
		for key, value := range m.cfg.Fingerprint {
			cfg.Fingerprint[key] = value
		}
	}
	if cfg.Proxy != nil {
		copy := *cfg.Proxy
		cfg.Proxy = &copy
	}
	if cfg.LaunchProxy != nil {
		copy := *cfg.LaunchProxy
		cfg.LaunchProxy = &copy
	}
	if cfg.Window != nil {
		copy := *cfg.Window
		cfg.Window = &copy
	}
	if cfg.Screen != nil {
		copy := *cfg.Screen
		cfg.Screen = &copy
	}
	if cfg.WebGL != nil {
		copy := *cfg.WebGL
		cfg.WebGL = &copy
	}
	return cfg
}

func (m *Manager) wait() {
	if m.cmd != nil {
		_ = m.cmd.Wait()
		releaseProcessBoundary(m.cmd)
	}
	m.setDead()
}

func (m *Manager) setDead() {
	m.mu.Lock()
	alreadyDead := m.state == StateDead
	m.state = StateDead
	lock := m.lock
	m.lock = nil
	proxy := m.proxy
	proxyLn := m.proxyLn
	m.proxy = nil
	m.proxyLn = nil
	pidfile := m.pidfile
	m.pidfile = ""
	m.mu.Unlock()
	if proxy != nil {
		_ = proxy.Close()
	}
	if proxyLn != nil {
		_ = proxyLn.Close()
	}
	if lock != nil {
		_ = lock.Release()
	}
	removePidfile(pidfile)
	if !alreadyDead {
		closeOnce(m.done)
	}
}

func (m *Manager) forwardDiagnostics(r io.Reader, capture *startupDiagnostics) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 8192), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if capture != nil {
			capture.WriteString(line + "\n")
		}
		slog.Debug("gomoufox sidecar", "stderr", policy.Redact(line))
	}
	if err := scanner.Err(); err != nil {
		text := err.Error()
		if capture != nil {
			capture.WriteString(text + "\n")
		}
		slog.Debug("gomoufox sidecar", "stderr", policy.Redact(text))
	}
}

type startupDiagnostics struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func newStartupDiagnostics(limit int) *startupDiagnostics {
	return &startupDiagnostics{limit: limit}
}

func (d *startupDiagnostics) WriteString(text string) {
	if d == nil || d.limit <= 0 || text == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	remaining := d.limit - len(d.buf)
	if remaining <= 0 {
		d.truncated = true
		return
	}
	if len(text) > remaining {
		text = text[:remaining]
		d.truncated = true
	}
	d.buf = append(d.buf, text...)
}

func (d *startupDiagnostics) Excerpt() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	text := string(d.buf)
	truncated := d.truncated
	d.mu.Unlock()
	text = strings.TrimSpace(policy.Redact(text))
	if text == "" {
		return ""
	}
	if truncated {
		text += "\n... <truncated>"
	}
	return text
}

func startupErrorWithDiagnostics(err error, diagnostics *startupDiagnostics, done <-chan struct{}) error {
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
	}
	if excerpt := diagnostics.Excerpt(); excerpt != "" {
		return fmt.Errorf("%w: sidecar stderr: %s", err, excerpt)
	}
	return err
}

func (m *Manager) startFilteringProxy(ctx context.Context) error {
	var upstream *url.URL
	if m.cfg.Proxy != nil {
		parsed, err := url.Parse(m.cfg.Proxy.Server)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("%w: invalid operator proxy", ErrSidecarStart)
		}
		if m.cfg.Proxy.Username != "" {
			if m.cfg.Proxy.Password != "" {
				parsed.User = url.UserPassword(m.cfg.Proxy.Username, m.cfg.Proxy.Password)
			} else {
				parsed.User = url.User(m.cfg.Proxy.Username)
			}
		}
		switch parsed.Scheme {
		case "http":
			upstream = parsed
		case "https":
			return fmt.Errorf("%w: HTTPS upstream proxy support requires an approved TLS connector and is not enabled yet", ErrSidecarStart)
		case "socks5", "socks5h":
			return fmt.Errorf("%w: SOCKS upstream proxy support requires an approved connector and is not enabled yet", ErrSidecarStart)
		default:
			return fmt.Errorf("%w: unsupported operator proxy scheme %q", ErrSidecarStart, parsed.Scheme)
		}
	}
	cfg := m.cfg.Policy
	if len(cfg.AllowedSchemes) == 0 {
		cfg.AllowedSchemes = policy.DefaultConfig().AllowedSchemes
	}
	ln, err := sidecarListen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("%w: start filtering proxy: %v", ErrSidecarStart, err)
	}
	server := &http.Server{
		Handler: netguard.FilteringProxy{Validator: netguard.NewValidator(cfg, nil), UpstreamProxy: upstream},
	}
	m.cfg.LaunchProxy = &ProxyConfig{Server: netguard.ProxyURL(ln.Addr().String())}
	m.mu.Lock()
	m.proxy = server
	m.proxyLn = ln
	m.mu.Unlock()
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
			slog.Debug("gomoufox filtering proxy exited", "error", err)
		}
	}()
	return nil
}

func launchArgsJSON(cfg Config) ([]byte, error) {
	launchArgs := map[string]any{
		"env":      browserLaunchEnv(cfg),
		"headless": cfg.Headless != 1,
	}
	if cfg.Persistent {
		launchArgs["persistent_context"] = true
		launchArgs["user_data_dir"] = cfg.UserDataDir
	}
	if cfg.LaunchProxy != nil && cfg.LaunchProxy.Server != "" {
		proxy := map[string]any{"server": cfg.LaunchProxy.Server}
		if cfg.LaunchProxy.Username != "" {
			proxy["username"] = cfg.LaunchProxy.Username
		}
		if cfg.LaunchProxy.Password != "" {
			proxy["password"] = cfg.LaunchProxy.Password
		}
		launchArgs["proxy"] = proxy
	}
	if cfg.GeoIP {
		launchArgs["geoip"] = true
	}
	if cfg.Humanize != nil {
		launchArgs["humanize"] = *cfg.Humanize
	}
	if cfg.OS != "" {
		launchArgs["os"] = cfg.OS
	}
	if len(cfg.Locale) > 0 {
		launchArgs["locale"] = cfg.Locale
	}
	if cfg.BlockImages {
		launchArgs["block_images"] = true
	}
	if cfg.BlockWebRTC {
		launchArgs["block_webrtc"] = true
	}
	if cfg.BlockWebGL {
		launchArgs["block_webgl"] = true
	}
	if len(cfg.Addons) > 0 {
		launchArgs["addons"] = cfg.Addons
	}
	if cfg.Window != nil {
		launchArgs["window"] = map[string]int{"width": cfg.Window.Width, "height": cfg.Window.Height}
	}
	if cfg.Screen != nil {
		launchArgs["screen"] = map[string]int{"width": cfg.Screen.Width, "height": cfg.Screen.Height}
	}
	if cfg.WebGL != nil {
		launchArgs["webgl_config"] = map[string]string{"vendor": cfg.WebGL.Vendor, "renderer": cfg.WebGL.Renderer}
	}
	if len(cfg.FirefoxPrefs) > 0 {
		launchArgs["firefox_user_prefs"] = cfg.FirefoxPrefs
	}
	if len(cfg.BrowserArgs) > 0 {
		launchArgs["args"] = cfg.BrowserArgs
	}
	if cfg.CustomFontsOnly {
		launchArgs["custom_fonts_only"] = true
	}
	if cfg.FFVersion > 0 {
		launchArgs["ff_version"] = cfg.FFVersion
	}
	if cfg.CamoufoxDebug {
		launchArgs["debug"] = true
	}
	if len(cfg.Fonts) > 0 {
		launchArgs["fonts"] = cfg.Fonts
	}
	if len(cfg.Fingerprint) > 0 {
		launchArgs["config"] = cfg.Fingerprint
	}
	if cfg.MainWorldEval {
		launchArgs["main_world_eval"] = true
	}
	if cfg.EnableCache {
		launchArgs["enable_cache"] = true
	}
	if cfg.DisableCOOP {
		launchArgs["disable_coop"] = true
	}
	return json.Marshal(launchArgs)
}

func browserLaunchEnv(cfg Config) map[string]string {
	env := map[string]string{}
	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" || sensitiveBrowserEnvKey(key) {
			continue
		}
		env[key] = value
	}
	for _, pair := range cfg.ExtraEnv {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		env[key] = value
	}
	return env
}

func sensitiveBrowserEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, prefix := range []string{"ANTHROPIC_", "AWS_", "CLAUDE_", "CODEX_", "GITHUB_", "HASP_", "HOLMES_", "OPENAI_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	for _, needle := range []string{"API_KEY", "AUTH", "COOKIE", "CREDENTIAL", "PASSWORD", "SECRET", "TOKEN"} {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}

func WriteLauncher(venvDir string, cfg Config) (string, error) {
	if _, err := launchArgsJSON(cfg); err != nil {
		return "", err
	}
	return writeLauncher(venvDir)
}

func writeLauncher(venvDir string) (string, error) {
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	path := filepath.Join(venvDir, "gomoufox_sidecar_launcher.py")
	content := []byte(`import base64
import contextlib
from pathlib import Path
import subprocess
import sys
import orjson
from browserforge.fingerprints import Screen
from camoufox.server import LAUNCH_SCRIPT, get_nodejs, launch_options, to_camel_case_dict

launch_kwargs = orjson.loads(sys.stdin.buffer.read())
persistent_user_data_dir = None
if launch_kwargs.pop("persistent_context", False):
    persistent_user_data_dir = launch_kwargs.pop("user_data_dir", None)
screen_value = launch_kwargs.get("screen")
if isinstance(screen_value, dict):
    width = screen_value.get("width")
    height = screen_value.get("height")
    launch_kwargs["screen"] = Screen(min_width=width, max_width=width, min_height=height, max_height=height)
window_value = launch_kwargs.get("window")
if isinstance(window_value, dict):
    launch_kwargs["window"] = (window_value.get("width"), window_value.get("height"))
webgl_value = launch_kwargs.get("webgl_config")
if isinstance(webgl_value, dict):
    launch_kwargs["webgl_config"] = (webgl_value.get("vendor"), webgl_value.get("renderer"))
with contextlib.redirect_stdout(sys.stderr):
    config = launch_options(**launch_kwargs)
if config.get("proxy") is None:
    config.pop("proxy", None)
nodejs = get_nodejs()
payload = to_camel_case_dict(config)
if persistent_user_data_dir:
    payload["_userDataDir"] = persistent_user_data_dir
    payload["_sharedBrowser"] = True
data = orjson.dumps(payload)
process = subprocess.Popen([nodejs, str(LAUNCH_SCRIPT)], cwd=Path(nodejs).parent / "package", stdin=subprocess.PIPE, text=True)
if process.stdin:
    process.stdin.write(base64.b64encode(data).decode())
    process.stdin.close()
raise SystemExit(process.wait())
`)
	if err := safefile.WriteFile0600(path, content, true); err != nil {
		return "", err
	}
	return path, nil
}

func closeOnce(ch chan struct{}) {
	defer func() { _ = recover() }()
	close(ch)
}

func DefaultCacheDir() string {
	home, _ := os.UserHomeDir()
	switch sidecarGOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Caches", "gomoufox", "venv")
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "gomoufox", "venv")
	default:
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			return filepath.Join(xdg, "gomoufox", "venv")
		}
		return filepath.Join(home, ".cache", "gomoufox", "venv")
	}
}
