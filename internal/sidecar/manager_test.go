package sidecar

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"reflect"
	"runtime"
	"strings"
	"sync"
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

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func TestWriteLauncherReadsLaunchArgsFromStdin(t *testing.T) {
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
		`orjson.loads(sys.stdin.buffer.read())`,
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

func TestWriteLauncherDoesNotPersistLaunchSecrets(t *testing.T) {
	venv := t.TempDir()
	humanize := 1.25
	path, err := WriteLauncher(venv, Config{
		Headless:        0,
		LaunchProxy:     &ProxyConfig{Server: "http://127.0.0.1:4567", Username: "secret-user", Password: "secret-pass"},
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
		ExtraEnv:        []string{"TOKEN=secret-token"},
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
		`orjson.loads(sys.stdin.buffer.read())`,
		`from browserforge.fingerprints import Screen`,
		`launch_kwargs["screen"] = Screen(min_width=width, max_width=width, min_height=height, max_height=height)`,
		`launch_kwargs["window"] = (window_value.get("width"), window_value.get("height"))`,
		`launch_kwargs["webgl_config"] = (webgl_value.get("vendor"), webgl_value.get("renderer"))`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("launcher missing %q:\n%s", want, text)
		}
	}
	for _, secret := range []string{"secret-user", "secret-pass", "secret-token", "navigator.userAgent", "privacy.resistFingerprinting"} {
		if strings.Contains(text, secret) {
			t.Fatalf("launcher persisted secret/config value %q:\n%s", secret, text)
		}
	}
}

func TestLaunchArgsJSONIncludesPersonaOptionsAndSanitizesEnv(t *testing.T) {
	t.Setenv("PATH", "/safe/bin")
	t.Setenv("HOME", "/gomoufox-home")
	t.Setenv("PUBLIC_FLAG", "kept")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("GOMOUFOX_DAEMON_TOKEN", "ambient-token")
	humanize := 1.25
	raw, err := launchArgsJSON(Config{
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
		ExtraEnv:        []string{"NO_EQUALS", "=blank", "EXPLICIT_TOKEN=explicit-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		`"proxy":{"password":"pass","server":"http://127.0.0.1:4567","username":"user"}`,
		`"geoip":true`,
		`"humanize":1.25`,
		`"os":"linux"`,
		`"locale":["en-US","en"]`,
		`"block_images":true`,
		`"block_webrtc":true`,
		`"block_webgl":true`,
		`"addons":["/addon"]`,
		`"window":{"height":800,"width":1200}`,
		`"screen":{"height":900,"width":1440}`,
		`"webgl_config":{"renderer":"Iris","vendor":"Intel"}`,
		`"firefox_user_prefs":{"privacy.resistFingerprinting":false}`,
		`"args":["--safe-mode"]`,
		`"custom_fonts_only":true`,
		`"ff_version":135`,
		`"debug":true`,
		`"fonts":["Inter"]`,
		`"config":{"navigator.userAgent":"ua"}`,
		`"main_world_eval":true`,
		`"enable_cache":true`,
		`"disable_coop":true`,
		`"PUBLIC_FLAG":"kept"`,
		`"EXPLICIT_TOKEN":"explicit-secret"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("launch args missing %q:\n%s", want, text)
		}
	}
	for _, secret := range []string{"ambient-secret", "ambient-token", "AWS_SECRET_ACCESS_KEY", "GOMOUFOX_DAEMON_TOKEN"} {
		if strings.Contains(text, secret) {
			t.Fatalf("launch args leaked ambient env %q:\n%s", secret, text)
		}
	}
}

func TestLaunchPlanDumpBuildsPythonPayloadFromLaunchArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	python := fakeExecutable(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	input="$(/bin/cat)"
	case "$input" in
		*"\"locale\":[\"en-US\",\"en\"]"* ) ;;
		*) printf '%s\n' "$input" >&2; exit 44 ;;
	esac
	printf '%s\n' '{"browserType":"firefox","env":{"PATH":"/safe/bin"},"proxy":{"server":"http://127.0.0.1:4567","username":"user","password":"pass"},"args":["--safe-mode"]}'
	exit 0
fi
exit 45
`)
	cfg := Config{
		Headless:    0,
		Locale:      []string{"en-US", "en"},
		LaunchProxy: &ProxyConfig{Server: "http://127.0.0.1:4567", Username: "user", Password: "pass"},
		BrowserArgs: []string{"--safe-mode"},
	}
	launchArgs, err := LaunchArgsMap(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if launchArgs["headless"] != true || launchArgs["env"] == nil {
		t.Fatalf("launch args = %#v", launchArgs)
	}
	payload, err := BuildPythonLaunchPayload(context.Background(), python, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if payload["browserType"] != "firefox" || payload["proxy"].(map[string]any)["password"] != "pass" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestLaunchPlanDumpErrorBranches(t *testing.T) {
	if _, err := LaunchArgsMap(Config{Fingerprint: map[string]any{"bad": func() {}}}); err == nil {
		t.Fatal("launch args accepted unmarshalable config")
	}
	if _, err := BuildPythonLaunchPayload(context.Background(), "python", Config{Fingerprint: map[string]any{"bad": func() {}}}); err == nil {
		t.Fatal("python payload accepted unmarshalable config")
	}
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	failing := fakeExecutable(t, `#!/bin/sh
printf '%s\n' 'token=secret' >&2
exit 7
`)
	if _, err := BuildPythonLaunchPayload(context.Background(), failing, Config{}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("failing python err = %v", err)
	}
	invalidJSON := fakeExecutable(t, `#!/bin/sh
printf '%s\n' '{bad'
`)
	if _, err := BuildPythonLaunchPayload(context.Background(), invalidJSON, Config{}); err == nil || !strings.Contains(err.Error(), "decode Python launch payload") {
		t.Fatalf("invalid json err = %v", err)
	}
	nullJSON := fakeExecutable(t, `#!/bin/sh
printf '%s\n' 'null'
`)
	if _, err := BuildPythonLaunchPayload(context.Background(), nullJSON, Config{}); err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Fatalf("null json err = %v", err)
	}
}

func TestPythonLaunchCommandPassesLaunchArgsOnStdin(t *testing.T) {
	venv := t.TempDir()
	manager := New(Config{
		VenvDir:     venv,
		LaunchProxy: &ProxyConfig{Server: "http://127.0.0.1:4567", Username: "secret-user", Password: "secret-pass"},
	})
	cmd, err := manager.launchCommand(context.Background(), "python", RuntimePython)
	if err != nil {
		t.Fatal(err)
	}
	stdin, ok := cmd.Stdin.(*bytes.Reader)
	if !ok {
		t.Fatalf("stdin type = %T", cmd.Stdin)
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "secret-pass") {
		t.Fatalf("stdin launch args missing proxy password: %s", raw)
	}
	data, err := os.ReadFile(filepath.Join(venv, "gomoufox_sidecar_launcher.py"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-pass") || strings.Contains(string(data), "secret-user") {
		t.Fatalf("launcher persisted proxy credentials:\n%s", data)
	}

	badManager := New(Config{Fingerprint: map[string]any{"bad": func() {}}})
	if _, err := badManager.launchCommand(context.Background(), "python", RuntimePython); err == nil {
		t.Fatal("python launch command with unmarshalable launch args succeeded")
	}

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockedManager := New(Config{VenvDir: filepath.Join(blocker, "child")})
	if _, err := blockedManager.launchCommand(context.Background(), "python", RuntimePython); err == nil {
		t.Fatal("python launch command under blocked venv path succeeded")
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
	sidecarProcessExists = func(pid int) bool {
		return pid == 123
	}
	sidecarTerminatePID = func(int) error { return terminateErr }
	if err := os.WriteFile(pidfilePath(venv), []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReapStalePidfile(venv); err != nil {
		t.Fatalf("live legacy pidfile should not terminate without ownership metadata: %v", err)
	}
	if _, err := os.Stat(pidfilePath(venv)); err != nil {
		t.Fatalf("live legacy pidfile should remain: %v", err)
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
	stalePath, err := writePidfile(venv, cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	recordData, err := os.ReadFile(stalePath)
	if err != nil {
		t.Fatal(err)
	}
	var record pidfileRecord
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	record.ParentPID = 999999
	recordData, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePath, recordData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReapStalePidfile(venv); err != nil {
		t.Fatal(err)
	}
	if err := waitWithTimeout(cmd, 2*time.Second); errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale process did not exit: %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale pidfile still exists: %v", err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writePidfile(blocker, 1); err == nil {
		t.Fatal("pidfile under regular file succeeded")
	}
}

func TestManagedPidfilesDoNotReapLiveParentOwnedSidecars(t *testing.T) {
	venv := t.TempDir()
	path, err := writePidfile(venv, 123)
	if err != nil {
		t.Fatal(err)
	}

	oldProcessExists := sidecarProcessExists
	oldTerminatePID := sidecarTerminatePID
	var terminated []int
	sidecarProcessExists = func(pid int) bool {
		return pid == 123 || pid == os.Getpid()
	}
	sidecarTerminatePID = func(pid int) error {
		terminated = append(terminated, pid)
		return nil
	}
	t.Cleanup(func() {
		sidecarProcessExists = oldProcessExists
		sidecarTerminatePID = oldTerminatePID
	})

	if err := ReapStalePidfile(venv); err != nil {
		t.Fatal(err)
	}
	if len(terminated) != 0 {
		t.Fatalf("reaped live parent-owned pidfile: %v", terminated)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live pidfile removed: %v", err)
	}
}

func TestManagedPidfilesReapOrphanedSidecars(t *testing.T) {
	venv := t.TempDir()
	path, err := writePidfile(venv, 123)
	if err != nil {
		t.Fatal(err)
	}
	recordData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record pidfileRecord
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	record.ParentPID = 456
	recordData, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, recordData, 0o600); err != nil {
		t.Fatal(err)
	}

	oldProcessExists := sidecarProcessExists
	oldTerminatePID := sidecarTerminatePID
	var terminated []int
	sidecarProcessExists = func(pid int) bool {
		return pid == 123
	}
	sidecarTerminatePID = func(pid int) error {
		terminated = append(terminated, pid)
		return nil
	}
	t.Cleanup(func() {
		sidecarProcessExists = oldProcessExists
		sidecarTerminatePID = oldTerminatePID
	})

	if err := ReapStalePidfile(venv); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(terminated, []int{123}) {
		t.Fatalf("terminated = %v", terminated)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("orphan pidfile still exists: %v", err)
	}
}

func TestManagedPidfileRejectsFinalSymlink(t *testing.T) {
	venv := t.TempDir()
	path := managedPidfilePath(venv, 123)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := writePidfile(venv, 123); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("write through pidfile symlink err = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("symlink target overwritten: %q", data)
	}
}

func TestManagedPidfileReapEdges(t *testing.T) {
	if err := ReapStalePidfile(t.TempDir()); err != nil {
		t.Fatalf("missing managed pidfile dir err = %v", err)
	}

	venv := t.TempDir()
	dir := pidfileDir(venv)
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ReapStalePidfile(venv); err != nil {
		t.Fatalf("directory pidfile entry should be ignored: %v", err)
	}

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReapStalePidfile(blocker); err == nil {
		t.Fatal("pidfile dir over regular file succeeded")
	}
	readDirBlockedVenv := t.TempDir()
	if err := os.WriteFile(pidfileDir(readDirBlockedVenv), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReapStalePidfile(readDirBlockedVenv); err == nil {
		t.Fatal("managed pidfile dir as file succeeded")
	}
	if err := reapManagedPidfile(filepath.Join(t.TempDir(), "missing.pid")); err != nil {
		t.Fatalf("missing managed pidfile err = %v", err)
	}
	if err := reapManagedPidfile(t.TempDir()); err == nil {
		t.Fatal("managed pidfile directory read succeeded")
	}
	entryErrorVenv := t.TempDir()
	entryErrorDir := pidfileDir(entryErrorVenv)
	if err := os.MkdirAll(entryErrorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(entryErrorDir, "link.pid")); err == nil {
		if err := ReapStalePidfile(entryErrorVenv); err == nil {
			t.Fatal("managed pidfile entry read error succeeded")
		}
	}

	invalid := filepath.Join(dir, "invalid.pid")
	if err := os.WriteFile(invalid, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reapManagedPidfile(invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(invalid); !os.IsNotExist(err) {
		t.Fatalf("invalid managed pidfile still exists: %v", err)
	}

	dead := filepath.Join(dir, "dead.pid")
	if err := os.WriteFile(dead, []byte(`{"pid":321,"parent_pid":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldProcessExists := sidecarProcessExists
	oldTerminatePID := sidecarTerminatePID
	sidecarProcessExists = func(int) bool { return false }
	sidecarTerminatePID = func(pid int) error {
		t.Fatalf("dead process should not be terminated: %d", pid)
		return nil
	}
	t.Cleanup(func() {
		sidecarProcessExists = oldProcessExists
		sidecarTerminatePID = oldTerminatePID
	})
	if err := reapManagedPidfile(dead); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead managed pidfile still exists: %v", err)
	}

	liveUnknownParent := filepath.Join(dir, "live-unknown-parent.pid")
	if err := os.WriteFile(liveUnknownParent, []byte(`{"pid":333,"parent_pid":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecarProcessExists = func(pid int) bool { return pid == 333 }
	if err := reapManagedPidfile(liveUnknownParent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(liveUnknownParent); err != nil {
		t.Fatalf("live unknown-parent pidfile removed: %v", err)
	}

	terminateErr := errors.New("terminate failed")
	orphan := filepath.Join(dir, "orphan-error.pid")
	if err := os.WriteFile(orphan, []byte(`{"pid":444,"parent_pid":555}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecarProcessExists = func(pid int) bool { return pid == 444 }
	sidecarTerminatePID = func(int) error { return terminateErr }
	if err := reapManagedPidfile(orphan); !errors.Is(err, ErrSidecarStart) || !strings.Contains(err.Error(), "terminate failed") {
		t.Fatalf("orphan terminate err = %v", err)
	}
	sidecarProcessExists = oldProcessExists
	sidecarTerminatePID = oldTerminatePID
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
payload="$(cat)"
case "$payload" in
  *'"proxy"'*) ;;
  *) exit 42 ;;
esac
case "$payload" in
  *'127.0.0.1'*) ;;
  *) exit 43 ;;
esac
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
	manager.mu.Lock()
	pidfile := manager.pidfile
	manager.mu.Unlock()
	if pidfile == "" {
		t.Fatal("manager pidfile path is empty")
	}
	if _, err := os.Stat(pidfile); err != nil {
		t.Fatalf("managed pidfile missing: %v", err)
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
	if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
		t.Fatalf("pidfile after stop = %v", err)
	}
}

func TestConcurrentManagersShareVenvWithoutReapingEachOther(t *testing.T) {
	venv := fakeVenv(t, `#!/bin/sh
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	first := New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer first.Stop(context.Background())

	second := New(Config{VenvDir: venv, ConnectTimeout: time.Second})
	if _, err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer second.Stop(context.Background())

	select {
	case <-first.Done():
		t.Fatal("second manager start reaped the first live sidecar")
	case <-time.After(250 * time.Millisecond):
	}
	if first.CurrentState() != StateReady || second.CurrentState() != StateReady {
		t.Fatalf("states = %v/%v", first.CurrentState(), second.CurrentState())
	}
	entries, err := os.ReadDir(pidfileDir(venv))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("managed pidfile count = %d, want 2", len(entries))
	}
}

func TestManagerStartStopWithFakeNodeDirectPayload(t *testing.T) {
	launchScript := filepath.Join(t.TempDir(), "launchServer.js")
	if err := os.WriteFile(launchScript, []byte("// fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	node := fakeExecutable(t, `#!/bin/sh
test "$1" = "`+launchScript+`" || exit 42
payload="$(cat)"
test "$payload" = "e30=" || exit 43
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	cwd := t.TempDir()
	venv := fakeVenv(t, fmt.Sprintf(`#!/bin/sh
test "$1" = "-c" || exit 44
input="$(cat)"
case "$input" in
  *'"headless":true'*) ;;
  *) exit 45 ;;
esac
printf '{"nodejs":%q,"launch_script":%q,"cwd":%q,"stdin_base64":"e30="}\n'
`, node, launchScript, cwd))
	manager := New(Config{VenvDir: venv, Runtime: "node-direct", ConnectTimeout: time.Second})
	endpoint, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "ws://127.0.0.1:4321/token" || manager.CurrentState() != StateReady {
		t.Fatalf("endpoint/state = %q/%v", endpoint, manager.CurrentState())
	}
	if info := manager.Info(); info.PID == 0 || info.Runtime != "node-direct" || info.WSEndpointRedacted != "ws://127.0.0.1:4321/<redacted>" {
		t.Fatalf("info = %#v", info)
	}
	manager.Stop(context.Background())
	if manager.CurrentState() != StateDead {
		t.Fatalf("state after stop = %v", manager.CurrentState())
	}
}

func TestNodeDirectSpecValidationAndBuildErrors(t *testing.T) {
	node := fakeExecutable(t, "#!/bin/sh\n")
	launchScript := filepath.Join(t.TempDir(), "launchServer.js")
	if err := os.WriteFile(launchScript, []byte("// fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	valid := nodeDirectSpec{NodeJS: node, LaunchScript: launchScript, CWD: cwd, StdinBase64: "e30="}
	if err := validateNodeDirectSpec(valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		spec nodeDirectSpec
		want string
	}{
		{"missing node", nodeDirectSpec{LaunchScript: launchScript, CWD: cwd, StdinBase64: "x"}, "missing nodejs"},
		{"missing launch", nodeDirectSpec{NodeJS: node, CWD: cwd, StdinBase64: "x"}, "missing launch_script"},
		{"missing cwd", nodeDirectSpec{NodeJS: node, LaunchScript: launchScript, StdinBase64: "x"}, "missing cwd"},
		{"missing stdin", nodeDirectSpec{NodeJS: node, LaunchScript: launchScript, CWD: cwd}, "missing stdin_base64"},
		{"invalid stdin base64", nodeDirectSpec{NodeJS: node, LaunchScript: launchScript, CWD: cwd, StdinBase64: "not base64"}, "invalid stdin_base64"},
		{"stdin not json", nodeDirectSpec{NodeJS: node, LaunchScript: launchScript, CWD: cwd, StdinBase64: "bm90LWpzb24="}, "not JSON"},
		{"stdin not object", nodeDirectSpec{NodeJS: node, LaunchScript: launchScript, CWD: cwd, StdinBase64: "bnVsbA=="}, "not a JSON object"},
		{"node unusable", nodeDirectSpec{NodeJS: filepath.Join(t.TempDir(), "missing"), LaunchScript: launchScript, CWD: cwd, StdinBase64: "e30="}, "nodejs path unusable"},
		{"node dir", nodeDirectSpec{NodeJS: t.TempDir(), LaunchScript: launchScript, CWD: cwd, StdinBase64: "e30="}, "nodejs path is a directory"},
		{"launch unusable", nodeDirectSpec{NodeJS: node, LaunchScript: filepath.Join(t.TempDir(), "missing"), CWD: cwd, StdinBase64: "e30="}, "launch script unusable"},
		{"launch dir", nodeDirectSpec{NodeJS: node, LaunchScript: t.TempDir(), CWD: cwd, StdinBase64: "e30="}, "launch script is a directory"},
		{"cwd unusable", nodeDirectSpec{NodeJS: node, LaunchScript: launchScript, CWD: filepath.Join(t.TempDir(), "missing"), StdinBase64: "e30="}, "cwd unusable"},
		{"cwd file", nodeDirectSpec{NodeJS: node, LaunchScript: launchScript, CWD: launchScript, StdinBase64: "e30="}, "cwd is not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateNodeDirectSpec(tc.spec); !errors.Is(err, ErrSidecarStart) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	badConfig := Config{Fingerprint: map[string]any{"bad": func() {}}}
	if _, err := buildNodeDirectSpec(context.Background(), fakePython(t, "#!/bin/sh\nexit 0\n"), badConfig); err == nil {
		t.Fatal("unmarshalable launch args succeeded")
	}
	failingPython := fakePython(t, `#!/bin/sh
printf '%s\n' '`+diagnosticSecretFixture+`' >&2
exit 7
`)
	if _, err := buildNodeDirectSpec(context.Background(), failingPython, Config{}); !errors.Is(err, ErrSidecarStart) || strings.Contains(err.Error(), "sid=secret") {
		t.Fatalf("failing python err = %v", err)
	}
	malformedPython := fakePython(t, `#!/bin/sh
printf 'not-json'
`)
	if _, err := buildNodeDirectSpec(context.Background(), malformedPython, Config{}); !errors.Is(err, ErrSidecarStart) || !strings.Contains(err.Error(), "decode node-direct") {
		t.Fatalf("malformed payload err = %v", err)
	}
	invalidSpecPython := fakePython(t, `#!/bin/sh
printf '{"nodejs":"","launch_script":"x","cwd":"x","stdin_base64":"x"}'
`)
	if _, err := buildNodeDirectSpec(context.Background(), invalidSpecPython, Config{}); !errors.Is(err, ErrSidecarStart) || !strings.Contains(err.Error(), "missing nodejs") {
		t.Fatalf("invalid spec err = %v", err)
	}

	manager := New(Config{VenvDir: t.TempDir()})
	if _, err := manager.launchCommand(context.Background(), "python", "bogus"); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("bad runtime err = %v", err)
	}
	if _, err := manager.launchCommand(context.Background(), failingPython, RuntimeNodeDirect); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("node-direct build err = %v", err)
	}
}

func TestNodeDirectLaunchPayloadFreshnessLive(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 to compare live Camoufox launch payload freshness")
	}
	python, err := VenvPython("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{OS: "linux", BlockWebRTC: true}
	first, err := buildNodeDirectSpec(context.Background(), python, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildNodeDirectSpec(context.Background(), python, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.StdinBase64 == second.StdinBase64 {
		t.Fatalf("live Camoufox returned identical node-direct payloads; full-payload caching would need a stricter deterministic proof")
	}
	payload := decodeNodeDirectPayloadForTest(t, first.StdinBase64)
	env, ok := payload["env"].(map[string]any)
	if !ok {
		t.Fatalf("payload env = %#v", payload["env"])
	}
	if _, ok := env["CAMOU_CONFIG_1"].(string); !ok {
		t.Fatalf("payload missing CAMOU_CONFIG_1: %#v", env)
	}
}

func decodeNodeDirectPayloadForTest(t *testing.T, raw string) map[string]any {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestManagerStartRuntimeDefaultAndInvalidRuntime(t *testing.T) {
	venv := fakeVenv(t, `#!/bin/sh
echo "Websocket endpoint: ws://127.0.0.1:4321/token"
while true; do sleep 1; done
`)
	manager := &Manager{cfg: Config{VenvDir: venv, ConnectTimeout: time.Second}, state: StateIdle, done: make(chan struct{})}
	if endpoint, err := manager.Start(context.Background()); err != nil || endpoint == "" {
		t.Fatalf("manual default runtime start endpoint=%q err=%v", endpoint, err)
	}
	manager.Stop(context.Background())

	manager = New(Config{VenvDir: venv, Runtime: "bogus", ConnectTimeout: time.Second})
	if _, err := manager.Start(context.Background()); !errors.Is(err, ErrSidecarStart) || !strings.Contains(err.Error(), "unsupported sidecar runtime") {
		t.Fatalf("invalid runtime err = %v", err)
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
	var diagnostics lockedBuffer
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
	sidecarWritePidfile = func(string, int) (string, error) { return "", writeErr }
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

func fakeExecutable(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-exec")
	if runtime.GOOS == "windows" {
		path += ".cmd"
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
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
