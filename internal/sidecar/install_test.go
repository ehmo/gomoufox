package sidecar

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type errDiagnosticWriter struct{}

func (errDiagnosticWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestInstallLockWaitsForRelease(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireInstallLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path() != installLockPath(dir) {
		t.Fatalf("lock path = %q", first.Path())
	}
	errc := make(chan error, 1)
	go func() {
		second, err := acquireInstallLock(context.Background(), dir)
		if err != nil {
			errc <- err
			return
		}
		errc <- second.Release()
	}()
	select {
	case err := <-errc:
		t.Fatalf("second lock acquired before release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("second lock did not acquire after release")
	}
	if _, err := os.Stat(installLockPath(dir)); err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
}

func TestInstallLockContextTimeoutAndNilPath(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireInstallLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := acquireInstallLock(ctx, dir); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout err = %v", err)
	}
	var nilLock *InstallLock
	if nilLock.Path() != "" {
		t.Fatalf("nil lock path = %q", nilLock.Path())
	}
	if err := (&InstallLock{}).Release(); err != nil {
		t.Fatalf("empty lock release err = %v", err)
	}
	if got := installLockPath(""); !strings.HasSuffix(got, ".gomoufox-install.lock") {
		t.Fatalf("default lock path = %q", got)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstallLock(context.Background(), blocker); err == nil {
		t.Fatal("lock under regular file succeeded")
	}

	closed, err := os.CreateTemp(t.TempDir(), "closed-lock-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tryInstallFileLock(closed); err == nil || errors.Is(err, errInstallLockBusy) {
		t.Fatalf("closed install lock err = %v", err)
	}

	oldTryLock := tryInstallFileLockForAcquire
	lockErr := errors.New("lock failed")
	tryInstallFileLockForAcquire = func(*os.File) error { return lockErr }
	t.Cleanup(func() { tryInstallFileLockForAcquire = oldTryLock })
	if _, err := acquireInstallLock(context.Background(), t.TempDir()); !errors.Is(err, lockErr) {
		t.Fatalf("non-busy lock err = %v", err)
	}
}

func TestEnsureBinaryOfflinePathRequiresManifestByDefault(t *testing.T) {
	browser := fakeBrowserTree(t)
	missingPython := filepath.Join(t.TempDir(), "python")
	if err := EnsureBinary(context.Background(), missingPython, InstallOptions{CamoufoxPath: browser}); !errors.Is(err, ErrVersionMismatch) || !strings.Contains(err.Error(), EnvTrustUnverifiedCamoufoxPath) {
		t.Fatalf("unverified offline path err = %v", err)
	}

	expected, err := camoufoxBrowserManifestSHA256(browser)
	if err != nil {
		t.Fatal(err)
	}
	restore := replaceManifestChecksum(t, expected)
	defer restore()
	if err := EnsureBinary(context.Background(), missingPython, InstallOptions{CamoufoxPath: browser}); err != nil {
		t.Fatalf("verified offline path err = %v", err)
	}
}

func TestEnsureBinaryOfflinePathAllowsExplicitUnverifiedTrust(t *testing.T) {
	browser := fakeBrowserTree(t)
	t.Setenv(EnvTrustUnverifiedCamoufoxPath, "1")
	if err := EnsureBinary(context.Background(), filepath.Join(t.TempDir(), "python"), InstallOptions{CamoufoxPath: browser}); err != nil {
		t.Fatal(err)
	}

	t.Setenv(EnvTrustUnverifiedCamoufoxPath, "true")
	if err := EnsureBinary(context.Background(), filepath.Join(t.TempDir(), "python"), InstallOptions{CamoufoxPath: browser}); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("non-explicit trust override err = %v", err)
	}
}

func TestEnsureBinaryInvalidOfflinePathAndEmptyDiscovery(t *testing.T) {
	if err := EnsureBinary(context.Background(), filepath.Join(t.TempDir(), "python"), InstallOptions{CamoufoxPath: t.TempDir()}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("invalid offline path err = %v", err)
	}
	if _, err := discoverCamoufoxBrowserDir(context.Background(), ""); err == nil {
		t.Fatal("empty python discovery succeeded")
	}
	if err := verifyCamoufoxManifest(fakeBrowserTree(t)); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("missing manifest checksum err = %v", err)
	}
}

func TestEnsureBinarySkipFetchWithEmptyDiscovery(t *testing.T) {
	oldDiscover := discoverCamoufoxBrowserDirForEnsure
	discoverCamoufoxBrowserDirForEnsure = func(context.Context, string) (string, error) {
		return "", nil
	}
	t.Cleanup(func() { discoverCamoufoxBrowserDirForEnsure = oldDiscover })
	if err := EnsureBinary(context.Background(), "python", InstallOptions{SkipBinaryFetch: true}); !errors.Is(err, ErrNotInstalled) || strings.Contains(err.Error(), ": <nil>") {
		t.Fatalf("skip fetch empty discovery err = %v", err)
	}
}

func TestEnsureBinaryEnvOfflinePath(t *testing.T) {
	browser := fakeBrowserTree(t)
	t.Setenv(EnvCamoufoxPath, browser)
	expected, err := camoufoxBrowserManifestSHA256(browser)
	if err != nil {
		t.Fatal(err)
	}
	restore := replaceManifestChecksum(t, expected)
	defer restore()
	if err := EnsureBinary(context.Background(), filepath.Join(t.TempDir(), "python"), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestVenvPipPath(t *testing.T) {
	venv := t.TempDir()
	pip, err := VenvPip(venv)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(venv, "bin", "pip")
	if runtime.GOOS == "windows" {
		want = filepath.Join(venv, "Scripts", "pip.exe")
	}
	if pip != want {
		t.Fatalf("pip path = %q want %q", pip, want)
	}
	t.Setenv("HOME", t.TempDir())
	if pip, err := VenvPip(""); err != nil || !strings.Contains(pip, "gomoufox") {
		t.Fatalf("default pip path = %q, %v", pip, err)
	}
	if _, err := VenvPython(""); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("default venv python err = %v", err)
	}
}

func TestFindPythonAndVersionCheckWithOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	python := fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	case "$2" in
		*sys.version_info*)
			exit 0
			;;
	esac
fi
exit 42
`)
	got, err := FindPython(python)
	if err != nil || got != python {
		t.Fatalf("FindPython = %q, %v", got, err)
	}
	t.Setenv("GOMOUFOX_PYTHON", python)
	got, err = FindPython("")
	if err != nil || got != python {
		t.Fatalf("FindPython env = %q, %v", got, err)
	}
	bad := fakePython(t, `#!/bin/sh
exit 1
`)
	if err := checkPythonVersion(bad); err == nil {
		t.Fatal("bad python version check succeeded")
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GOMOUFOX_PYTHON", "")
	if _, err := FindPython("missing-python"); err == nil {
		t.Fatal("FindPython succeeded without candidates")
	}
}

func TestRunInstallCommandVerbose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command is unix-only")
	}
	var diagnostics bytes.Buffer
	oldWriter := installDiagnosticWriter
	installDiagnosticWriter = &diagnostics
	t.Cleanup(func() { installDiagnosticWriter = oldWriter })
	out, err := runInstallCommand(exec.Command("sh", "-c", "printf '%s\n' '"+diagnosticSecretFixture+"' >&2"), true)
	if err != nil || out != nil {
		t.Fatalf("verbose command out=%q err=%v", out, err)
	}
	assertNoDiagnosticSecrets(t, diagnostics.String())

	out, err = runInstallCommand(exec.Command("sh", "-c", "printf '%s\n' '"+diagnosticSecretFixture+"'; exit 1"), false)
	if err == nil {
		t.Fatal("non-verbose command succeeded")
	}
	assertNoDiagnosticSecrets(t, string(out))

	installDiagnosticWriter = errDiagnosticWriter{}
	out, err = runInstallCommand(exec.Command("sh", "-c", "printf x"), true)
	if err == nil || out != nil {
		t.Fatalf("stdout flush failure out=%q err=%v", out, err)
	}
	out, err = runInstallCommand(exec.Command("sh", "-c", "printf x >&2"), true)
	if err == nil || out != nil {
		t.Fatalf("stderr flush failure out=%q err=%v", out, err)
	}
	out, err = runInstallCommand(exec.Command("sh", "-c", "exit 7"), true)
	if err == nil || out != nil {
		t.Fatalf("verbose run failure out=%q err=%v", out, err)
	}
}

func TestEnsureVenvCreatesAndUpgradesPipWithFakePython(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	marker := filepath.Join(t.TempDir(), "pip-args")
	lockMarker := filepath.Join(t.TempDir(), "pip-lock")
	python := fakePython(t, `#!/bin/sh
if [ "$1" = "-m" ] && [ "$2" = "venv" ]; then
	mkdir -p "$3/bin"
	cat > "$3/bin/python" <<'PY'
#!/bin/sh
if [ "$1" = "-c" ]; then
	exit 0
fi
exit 0
PY
	cat > "$3/bin/pip" <<PIP
#!/bin/sh
printf '%s\n' "\$*" > `+shQuote(marker)+`
while [ "\$#" -gt 0 ]; do
	if [ "\$1" = "-r" ]; then
		cat "\$2" > `+shQuote(lockMarker)+`
	fi
	shift
done
exit 0
PIP
	chmod +x "$3/bin/python" "$3/bin/pip"
	exit 0
fi
exit 42
`)
	venv := filepath.Join(t.TempDir(), "venv")
	if err := EnsureVenv(context.Background(), python, venv); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	assertHashLockedPipArgs(t, string(data), true)
	lockData, err := os.ReadFile(lockMarker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lockData), "pip=="+RequiredPip) || !strings.Contains(string(lockData), "--hash=sha256:") {
		t.Fatalf("pip lock = %q", lockData)
	}
	if !strings.Contains(string(data), "install") {
		t.Fatalf("pip args = %q", data)
	}
	if err := EnsureVenv(context.Background(), python, venv); err != nil {
		t.Fatalf("existing venv err = %v", err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureVenv(context.Background(), python, filepath.Join(blocker, "venv")); err == nil {
		t.Fatal("venv under regular file succeeded")
	}

	failingCreate := fakePython(t, `#!/bin/sh
echo create failed
exit 7
`)
	if err := EnsureVenv(context.Background(), failingCreate, filepath.Join(t.TempDir(), "venv")); err == nil || !strings.Contains(err.Error(), "create venv") {
		t.Fatalf("create failure err = %v", err)
	}

	failingUpgrade := fakePython(t, `#!/bin/sh
if [ "$1" = "-m" ] && [ "$2" = "venv" ]; then
	mkdir -p "$3/bin"
	cat > "$3/bin/python" <<'PY'
#!/bin/sh
exit 0
PY
	cat > "$3/bin/pip" <<'PIP'
#!/bin/sh
echo upgrade failed
exit 8
PIP
	chmod +x "$3/bin/python" "$3/bin/pip"
	exit 0
fi
exit 42
`)
	if err := EnsureVenv(context.Background(), failingUpgrade, filepath.Join(t.TempDir(), "venv")); err == nil || !strings.Contains(err.Error(), "upgrade pip") {
		t.Fatalf("upgrade failure err = %v", err)
	}

	lockErr := errors.New("pip lock missing")
	oldReadLock := readPythonRequirementsLock
	readPythonRequirementsLock = func(string) ([]byte, error) { return nil, lockErr }
	t.Cleanup(func() { readPythonRequirementsLock = oldReadLock })
	if err := EnsureVenv(context.Background(), python, filepath.Join(t.TempDir(), "venv")); !errors.Is(err, lockErr) || !strings.Contains(err.Error(), "locked pip requirements") {
		t.Fatalf("missing pip lock err = %v", err)
	}
	readPythonRequirementsLock = oldReadLock

	oldPipAfterCreate := venvPipAfterCreate
	pipPathErr := errors.New("pip path failed")
	venvPipAfterCreate = func(string) (string, error) { return "", pipPathErr }
	t.Cleanup(func() { venvPipAfterCreate = oldPipAfterCreate })
	python = fakePython(t, `#!/bin/sh
if [ "$1" = "-m" ] && [ "$2" = "venv" ]; then
	mkdir -p "$3/bin"
	touch "$3/bin/python"
	exit 0
fi
exit 42
`)
	if err := EnsureVenv(context.Background(), python, filepath.Join(t.TempDir(), "venv")); !errors.Is(err, pipPathErr) {
		t.Fatalf("venv pip path err = %v", err)
	}
	venvPipAfterCreate = oldPipAfterCreate
}

func TestEnsureCamoufoxPinsAndInstallBranches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	if err := EnsureCamoufox(context.Background(), t.TempDir(), InstallOptions{CamoufoxVersion: "0.0.0"}); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("unsupported pin err = %v", err)
	}

	siteRoot := fakePlaywrightPackage(t, RequiredPlaywright)
	venv := fakeCompatibleVenv(t, siteRoot, RequiredCamoufox)
	marker := filepath.Join(t.TempDir(), "pip-called")
	writeFakePip(t, venv, `#!/bin/sh
touch `+shQuote(marker)+`
exit 64
`)
	if err := EnsureCamoufox(context.Background(), venv, InstallOptions{}); err != nil {
		t.Fatalf("compatible install err = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pip was called for compatible install: %v", err)
	}

	forceMarker := filepath.Join(t.TempDir(), "pip-args")
	forceLockMarker := filepath.Join(t.TempDir(), "camoufox-lock")
	writeFakePip(t, venv, `#!/bin/sh
printf '%s\n' "$*" > `+shQuote(forceMarker)+`
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-r" ]; then
		cat "$2" > `+shQuote(forceLockMarker)+`
	fi
	shift
done
exit 0
`)
	if err := EnsureCamoufox(context.Background(), venv, InstallOptions{ForceReinstall: true}); err != nil {
		t.Fatalf("force install err = %v", err)
	}
	data, err := os.ReadFile(forceMarker)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	assertHashLockedPipArgs(t, text, false)
	lockData, err := os.ReadFile(forceLockMarker)
	if err != nil {
		t.Fatal(err)
	}
	lockText := string(lockData)
	if !strings.Contains(lockText, "camoufox=="+RequiredCamoufox) || !strings.Contains(lockText, "playwright=="+RequiredPlaywright) || !strings.Contains(lockText, "--hash=sha256:") {
		t.Fatalf("camoufox lock = %q", lockText)
	}
	if !strings.Contains(text, "install") {
		t.Fatalf("pip args = %q", text)
	}
	if compatibleInstalled(context.Background(), t.TempDir()) {
		t.Fatal("empty venv reported compatible")
	}

	brokenVenv := fakeCompatibleVenv(t, siteRoot, "0.0.0")
	writeFakePip(t, brokenVenv, `#!/bin/sh
echo install failed
exit 9
`)
	if err := EnsureCamoufox(context.Background(), brokenVenv, InstallOptions{}); err == nil || !strings.Contains(err.Error(), "pip install locked") {
		t.Fatalf("pip failure err = %v", err)
	}

	lockErr := errors.New("camoufox lock missing")
	oldReadLock := readPythonRequirementsLock
	readPythonRequirementsLock = func(string) ([]byte, error) { return nil, lockErr }
	t.Cleanup(func() { readPythonRequirementsLock = oldReadLock })
	lockVenv := fakeCompatibleVenv(t, siteRoot, "0.0.0")
	writeFakePip(t, lockVenv, `#!/bin/sh
exit 0
`)
	if err := EnsureCamoufox(context.Background(), lockVenv, InstallOptions{}); !errors.Is(err, lockErr) || !strings.Contains(err.Error(), "locked camoufox/playwright requirements") {
		t.Fatalf("missing camoufox lock err = %v", err)
	}
	readPythonRequirementsLock = oldReadLock

	oldPipForCamoufox := venvPipForCamoufox
	pipPathErr := errors.New("pip path failed")
	venvPipForCamoufox = func(string) (string, error) { return "", pipPathErr }
	t.Cleanup(func() { venvPipForCamoufox = oldPipForCamoufox })
	if err := EnsureCamoufox(context.Background(), t.TempDir(), InstallOptions{}); !errors.Is(err, pipPathErr) {
		t.Fatalf("camoufox pip path err = %v", err)
	}
	venvPipForCamoufox = oldPipForCamoufox
}

func TestPythonRequirementsLocksMatchPinsAndFailClosed(t *testing.T) {
	camoufoxLock, err := readPythonRequirementsLock(camoufoxRequirementsLock)
	if err != nil {
		t.Fatal(err)
	}
	camoufoxText := string(camoufoxLock)
	if !strings.Contains(camoufoxText, "camoufox=="+RequiredCamoufox) ||
		!strings.Contains(camoufoxText, "playwright=="+RequiredPlaywright) ||
		!strings.Contains(camoufoxText, "--hash=sha256:") {
		t.Fatalf("camoufox lock does not match pins")
	}
	pipLock, err := readPythonRequirementsLock(pipRequirementsLock)
	if err != nil {
		t.Fatal(err)
	}
	pipText := string(pipLock)
	if !strings.Contains(pipText, "pip=="+RequiredPip) || !strings.Contains(pipText, "--hash=sha256:") {
		t.Fatalf("pip lock does not match pin")
	}

	path, cleanup, err := materializePythonRequirementsLock(pipRequirementsLock)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != pipText {
		t.Fatalf("materialized lock mismatch")
	}
	cleanup()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("materialized lock cleanup err = %v", err)
	}

	oldRead := readPythonRequirementsLock
	oldMkdir := mkdirPythonRequirementsTemp
	oldWrite := writePythonRequirementsFile
	t.Cleanup(func() {
		readPythonRequirementsLock = oldRead
		mkdirPythonRequirementsTemp = oldMkdir
		writePythonRequirementsFile = oldWrite
	})

	readErr := errors.New("read lock failed")
	readPythonRequirementsLock = func(string) ([]byte, error) { return nil, readErr }
	if _, cleanup, err := materializePythonRequirementsLock(pipRequirementsLock); !errors.Is(err, readErr) {
		cleanup()
		t.Fatalf("read lock err = %v", err)
	}

	readPythonRequirementsLock = func(string) ([]byte, error) { return []byte(" \n"), nil }
	if _, cleanup, err := materializePythonRequirementsLock(pipRequirementsLock); err == nil || !strings.Contains(err.Error(), "empty Python requirements lock") {
		cleanup()
		t.Fatalf("empty lock err = %v", err)
	}

	readPythonRequirementsLock = oldRead
	mkdirErr := errors.New("mkdir lock failed")
	mkdirPythonRequirementsTemp = func(string, string) (string, error) { return "", mkdirErr }
	if _, cleanup, err := materializePythonRequirementsLock(pipRequirementsLock); !errors.Is(err, mkdirErr) {
		cleanup()
		t.Fatalf("mkdir lock err = %v", err)
	}

	mkdirPythonRequirementsTemp = oldMkdir
	writeErr := errors.New("write lock failed")
	writePythonRequirementsFile = func(string, []byte, os.FileMode) error { return writeErr }
	if _, cleanup, err := materializePythonRequirementsLock(pipRequirementsLock); !errors.Is(err, writeErr) {
		cleanup()
		t.Fatalf("write lock err = %v", err)
	}
}

func TestEnsureInstalledWithCompatibleOfflineVenv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	siteRoot := fakePlaywrightPackage(t, RequiredPlaywright)
	venv := fakeCompatibleVenv(t, siteRoot, RequiredCamoufox)
	writeFakePip(t, venv, `#!/bin/sh
exit 65
`)
	python, err := VenvPython(venv)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureInstalled(context.Background(), InstallOptions{
		VenvDir:      venv,
		PythonBin:    python,
		Runtime:      RuntimePython,
		CamoufoxPath: fakeVerifiedBrowserTree(t),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureInstalledPropagatesLocalFailureBranches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: filepath.Join(blocker, "venv")}); err == nil {
		t.Fatal("install lock path under regular file succeeded")
	}

	t.Setenv("PATH", t.TempDir())
	t.Setenv("GOMOUFOX_PYTHON", "")
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: t.TempDir(), PythonBin: "missing-python", Runtime: RuntimePython}); err == nil || !strings.Contains(err.Error(), "Python 3.9") {
		t.Fatalf("python discovery err = %v", err)
	}

	pythonCreatesBrokenVenv := fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "venv" ]; then
	echo create failed
	exit 7
fi
exit 42
`)
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: filepath.Join(t.TempDir(), "venv"), PythonBin: pythonCreatesBrokenVenv, Runtime: RuntimePython}); err == nil || !strings.Contains(err.Error(), "create venv") {
		t.Fatalf("ensure venv err = %v", err)
	}

	siteRoot := fakePlaywrightPackage(t, RequiredPlaywright)
	venv := fakeCompatibleVenv(t, siteRoot, RequiredCamoufox)
	python, err := VenvPython(venv)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: venv, PythonBin: python, Runtime: RuntimePython, CamoufoxVersion: "0.0.0"}); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("unsupported camoufox pin err = %v", err)
	}

	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: venv, PythonBin: python, Runtime: RuntimePython, CamoufoxPath: t.TempDir()}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("binary validation err = %v", err)
	}

	mismatchedVenv := fakeCompatibleVenv(t, siteRoot, "0.0.0")
	writeFakePip(t, mismatchedVenv, `#!/bin/sh
exit 0
`)
	mismatchedPython, err := VenvPython(mismatchedVenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: mismatchedVenv, PythonBin: mismatchedPython, Runtime: RuntimePython, CamoufoxPath: fakeVerifiedBrowserTree(t)}); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("final compatibility err = %v", err)
	}

	forcedVenv := fakeCompatibleVenv(t, siteRoot, RequiredCamoufox)
	forcedPython, err := VenvPython(forcedVenv)
	if err != nil {
		t.Fatal(err)
	}
	writeFakePip(t, forcedVenv, `#!/bin/sh
exit 0
`)
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: forcedVenv, PythonBin: forcedPython, Runtime: RuntimePython, CamoufoxPath: fakeVerifiedBrowserTree(t), ForceReinstall: true}); err != nil {
		t.Fatalf("forced reinstall err = %v", err)
	}

	oldVenvPythonAfterInstall := venvPythonAfterInstall
	venvPythonErr := errors.New("venv python failed")
	venvPythonAfterInstall = func(string) (string, error) { return "", venvPythonErr }
	t.Cleanup(func() { venvPythonAfterInstall = oldVenvPythonAfterInstall })
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: forcedVenv, PythonBin: forcedPython, Runtime: RuntimePython, CamoufoxPath: fakeVerifiedBrowserTree(t)}); !errors.Is(err, venvPythonErr) {
		t.Fatalf("post-install venv python err = %v", err)
	}
	venvPythonAfterInstall = oldVenvPythonAfterInstall
}

func TestInstallLifecycleAdditionalLocalBranchEdges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GOMOUFOX_PYTHON", "")
	if err := EnsureInstalled(context.Background(), InstallOptions{Runtime: RuntimePython}); err == nil || !strings.Contains(err.Error(), "Python 3.9") {
		t.Fatalf("default venv install err = %v", err)
	}

	lockDir := t.TempDir()
	if err := os.Mkdir(installLockPath(lockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstallLock(context.Background(), lockDir); err == nil {
		t.Fatal("install lock opened over directory")
	}

	defaultLock, err := acquireInstallLock(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := defaultLock.Release(); err != nil {
		t.Fatal(err)
	}

	if err := EnsureCamoufox(context.Background(), t.TempDir(), InstallOptions{}); err == nil || !strings.Contains(err.Error(), "pip install locked") {
		t.Fatalf("missing pip err = %v", err)
	}

	pythonWithoutPip := fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "venv" ]; then
	mkdir -p "$3/bin"
	cat > "$3/bin/python" <<'PY'
#!/bin/sh
exit 0
PY
	chmod +x "$3/bin/python"
	exit 0
fi
exit 42
`)
	if err := EnsureVenv(context.Background(), pythonWithoutPip, filepath.Join(t.TempDir(), "venv")); err == nil || !strings.Contains(err.Error(), "upgrade pip") {
		t.Fatalf("missing pip after venv err = %v", err)
	}

}

func TestEnsureInstalledCreatesVenvThenFailsBinaryValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}

	pythonRemovedAfterPip := fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "venv" ]; then
	mkdir -p "$3/bin"
	cat > "$3/bin/python" <<'PY'
#!/bin/sh
exit 0
PY
	cat > "$3/bin/pip" <<'PIP'
#!/bin/sh
exit 0
PIP
	chmod +x "$3/bin/python" "$3/bin/pip"
	exit 0
fi
exit 42
`)
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: filepath.Join(t.TempDir(), "venv"), PythonBin: pythonRemovedAfterPip, Runtime: RuntimePython, CamoufoxPath: t.TempDir()}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("created venv binary validation err = %v", err)
	}
}

func TestBrowserExecutableDiscoveryHelpers(t *testing.T) {
	root := t.TempDir()
	exeName := "camoufox"
	if runtime.GOOS == "windows" {
		exeName = "camoufox.exe"
	}
	nested := filepath.Join(root, "nested", "bin")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(nested, exeName)
	if err := os.WriteFile(exe, []byte("fake executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := findBrowserExecutable(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != exe {
		t.Fatalf("executable = %q want %q", got, exe)
	}
	directRoot := t.TempDir()
	direct := filepath.Join(directRoot, exeName)
	if err := os.WriteFile(direct, []byte("fake executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := findBrowserExecutable(directRoot); err != nil || got != direct {
		t.Fatalf("direct executable = %q, %v", got, err)
	}
	if !isBrowserExecutableName(exeName) || isBrowserExecutableName("chrome") {
		t.Fatalf("executable name classifier mismatch")
	}
	if _, err := findBrowserExecutable(t.TempDir()); err == nil {
		t.Fatal("empty executable search succeeded")
	}
	if _, err := findBrowserExecutable(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing executable root succeeded")
	}
	restoreRuntime := setSidecarRuntime(t, "linux", "amd64")
	execRoot := t.TempDir()
	execPath := filepath.Join(execRoot, "firefox")
	if err := os.WriteFile(execPath, []byte("fake executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverUsableBrowserDir(execPath); err != nil || got != execRoot {
		t.Fatalf("executable root discovery = %q, %v", got, err)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverUsableBrowserDir(plain); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("plain file discovery err = %v", err)
	}
	if _, err := discoverUsableBrowserDir(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("empty browser discovery err = %v", err)
	}
	restoreRuntime()
	if err := validateCamoufoxBrowserDir(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing browser dir validated")
	}
	plainFile := filepath.Join(root, "file")
	if err := os.WriteFile(plainFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateCamoufoxBrowserDir(plainFile); err == nil {
		t.Fatal("plain file browser dir validated")
	}
	if isExecutableFile(root) || isExecutableFile(plainFile) {
		t.Fatalf("non-executable paths reported executable")
	}
	if !skipManifestPath("cache/file") || !skipManifestPath("profile/foo.tmp") || skipManifestPath("resources/prefs.js") {
		t.Fatalf("manifest skip classifier mismatch")
	}
	restoreManifest := replaceManifestChecksum(t, "expected")
	defer restoreManifest()
	if err := verifyCamoufoxManifest(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing manifest root verified")
	}
	if _, err := camoufoxBrowserManifestSHA256(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing manifest root hashed")
	}
	if runtime.GOOS != "windows" {
		browser := fakeBrowserTree(t)
		unreadable := filepath.Join(browser, "resources", "unreadable")
		if err := os.WriteFile(unreadable, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unreadable, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
		if _, err := camoufoxBrowserManifestSHA256(browser); err == nil {
			t.Fatal("unreadable manifest file hashed")
		}
	}
}

func TestDiscoverCamoufoxBrowserDirFallsBackToCamoufoxCacheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	cache := t.TempDir()
	camoufoxRoot := filepath.Join(cache, "camoufox")
	exe := filepath.Join(camoufoxRoot, "Camoufox.app", "Contents", "MacOS", "camoufox")
	if err := os.MkdirAll(filepath.Dir(exe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("fake executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	restore := replaceUserCacheDir(t, cache, nil)
	defer restore()

	python := fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	exit 0
fi
exit 42
`)
	got, err := discoverCamoufoxBrowserDir(context.Background(), python)
	if err != nil {
		t.Fatal(err)
	}
	if got != camoufoxRoot {
		t.Fatalf("discovered cache root = %q want %q", got, camoufoxRoot)
	}
}

func TestCamoufoxCacheRootsAndUnsupportedManifestPlatform(t *testing.T) {
	restoreCache := replaceUserCacheDir(t, "", errors.New("cache unavailable"))
	if roots := camoufoxBrowserCacheRoots(); roots != nil {
		t.Fatalf("cache error roots = %#v", roots)
	}
	restoreCache()
	restoreCache = replaceUserCacheDir(t, "", nil)
	if roots := camoufoxBrowserCacheRoots(); roots != nil {
		t.Fatalf("empty cache roots = %#v", roots)
	}
	restoreCache()

	restoreRuntime := setSidecarRuntime(t, "windows", "arm64")
	defer restoreRuntime()
	if err := verifyCamoufoxManifest(fakeBrowserTree(t)); !errors.Is(err, ErrVersionMismatch) || !strings.Contains(err.Error(), "no Camoufox binary manifest checksum") {
		t.Fatalf("unsupported manifest err = %v", err)
	}
}

func TestRuntimePlatformHelpersCanBeExercisedLocally(t *testing.T) {
	home := t.TempDir()
	localAppData := filepath.Join(t.TempDir(), "localappdata")
	xdgCache := filepath.Join(t.TempDir(), "xdg-cache")
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", localAppData)

	restore := setSidecarRuntime(t, "darwin", "arm64")
	if got := strings.Join(browserExecutableCandidates(), "\n"); !strings.Contains(got, filepath.Join("Camoufox.app", "Contents", "MacOS", "camoufox")) {
		t.Fatalf("darwin candidates = %q", got)
	}
	if got, want := DefaultCacheDir(), filepath.Join(home, "Library", "Caches", "gomoufox", "venv"); got != want {
		t.Fatalf("darwin cache dir = %q want %q", got, want)
	}
	restore()

	restore = setSidecarRuntime(t, "windows", "amd64")
	venv := t.TempDir()
	python := filepath.Join(venv, "Scripts", "python.exe")
	if err := os.MkdirAll(filepath.Dir(python), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, []byte("fake python"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := VenvPython(venv); err != nil || got != python {
		t.Fatalf("windows python = %q, %v", got, err)
	}
	if got, want := mustVenvPip(t, venv), filepath.Join(venv, "Scripts", "pip.exe"); got != want {
		t.Fatalf("windows pip = %q want %q", got, want)
	}
	if !isBrowserExecutableName("FIREFOX.EXE") || isBrowserExecutableName("firefox") {
		t.Fatalf("windows executable classifier mismatch")
	}
	if got := browserExecutableCandidates(); len(got) != 2 || got[0] != "firefox.exe" || got[1] != "camoufox.exe" {
		t.Fatalf("windows candidates = %#v", got)
	}
	nonExecutable := filepath.Join(t.TempDir(), "firefox.exe")
	if err := os.WriteFile(nonExecutable, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !isExecutableFile(nonExecutable) {
		t.Fatal("windows regular file was not executable")
	}
	if got, want := DefaultCacheDir(), filepath.Join(localAppData, "gomoufox", "venv"); got != want {
		t.Fatalf("windows cache dir = %q want %q", got, want)
	}
	restore()

	restore = setSidecarRuntime(t, "freebsd", "amd64")
	if got := browserExecutableCandidates(); len(got) != 2 || got[0] != "firefox" || got[1] != "camoufox" {
		t.Fatalf("default candidates = %#v", got)
	}
	t.Setenv("XDG_CACHE_HOME", xdgCache)
	if got, want := DefaultCacheDir(), filepath.Join(xdgCache, "gomoufox", "venv"); got != want {
		t.Fatalf("xdg cache dir = %q want %q", got, want)
	}
	t.Setenv("XDG_CACHE_HOME", "")
	if got, want := DefaultCacheDir(), filepath.Join(home, ".cache", "gomoufox", "venv"); got != want {
		t.Fatalf("home cache dir = %q want %q", got, want)
	}
	restore()
}

func TestEnsureBinarySkipFetchFailsWhenNoUsableBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	restoreCache := replaceUserCacheDir(t, t.TempDir(), nil)
	defer restoreCache()
	python := fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	exit 0
fi
exit 42
`)
	err := EnsureBinary(context.Background(), python, InstallOptions{SkipBinaryFetch: true})
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("EnsureBinary err = %v", err)
	}
}

func TestEnsureBinaryFetchUsesFakeDownloadAndChecksum(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	restoreCache := replaceUserCacheDir(t, t.TempDir(), nil)
	defer restoreCache()
	browser := fakeBrowserTree(t)
	expected, err := camoufoxBrowserManifestSHA256(browser)
	if err != nil {
		t.Fatal(err)
	}
	restoreManifest := replaceManifestChecksum(t, expected)
	defer restoreManifest()
	marker := filepath.Join(t.TempDir(), "fetched")
	python := fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	if [ -f `+shQuote(marker)+` ]; then
		printf '%s\n' `+shQuote(browser)+`
	fi
	exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "camoufox" ] && [ "$3" = "fetch" ]; then
	touch `+shQuote(marker)+`
	exit 0
fi
exit 42
`)
	if err := EnsureBinary(context.Background(), python, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fetch marker missing: %v", err)
	}
}

func TestEnsureBinaryDiscoveryAndFetchErrorEdges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	restoreCache := replaceUserCacheDir(t, t.TempDir(), nil)
	defer restoreCache()
	browser := fakeBrowserTree(t)
	expected, err := camoufoxBrowserManifestSHA256(browser)
	if err != nil {
		t.Fatal(err)
	}
	restoreManifest := replaceManifestChecksum(t, expected)
	defer restoreManifest()
	python := fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	printf '%s\n' `+shQuote(browser)+`
	exit 0
fi
exit 42
`)
	if err := EnsureBinary(context.Background(), python, InstallOptions{}); err != nil {
		t.Fatalf("discovered binary err = %v", err)
	}

	invalidBrowser := t.TempDir()
	python = fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	printf '%s\n' `+shQuote(invalidBrowser)+`
	exit 0
fi
exit 42
`)
	if err := EnsureBinary(context.Background(), python, InstallOptions{SkipBinaryFetch: true}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("invalid discovered binary err = %v", err)
	}

	python = fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "camoufox" ] && [ "$3" = "fetch" ]; then
	echo fetch failed
	exit 9
fi
exit 42
`)
	if err := EnsureBinary(context.Background(), python, InstallOptions{}); err == nil || !strings.Contains(err.Error(), "camoufox fetch") {
		t.Fatalf("fetch failure err = %v", err)
	}

	python = fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "camoufox" ] && [ "$3" = "fetch" ]; then
	exit 0
fi
exit 42
`)
	if err := EnsureBinary(context.Background(), python, InstallOptions{}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("fetch without discovery err = %v", err)
	}
}

func TestEnsureBinaryAdditionalLocalBranchEdges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	restoreCache := replaceUserCacheDir(t, t.TempDir(), nil)
	defer restoreCache()

	if _, err := discoverCamoufoxBrowserDir(context.Background(), fakePython(t, `#!/bin/sh
exit 9
`)); err == nil {
		t.Fatal("discovery command failure succeeded")
	}

	browser := fakeBrowserTree(t)
	python := fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	printf '%s\n' `+shQuote(browser)+`
	exit 0
fi
exit 42
`)
	if err := EnsureBinary(context.Background(), python, InstallOptions{}); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("discovered manifest failure err = %v", err)
	}

	invalidBrowser := t.TempDir()
	marker := filepath.Join(t.TempDir(), "fetched")
	python = fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	if [ -f `+shQuote(marker)+` ]; then
		printf '%s\n' `+shQuote(invalidBrowser)+`
	fi
	exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "camoufox" ] && [ "$3" = "fetch" ]; then
	touch `+shQuote(marker)+`
	exit 0
fi
exit 42
`)
	if err := EnsureBinary(context.Background(), python, InstallOptions{}); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "fetch produced unusable") {
		t.Fatalf("post-fetch invalid browser err = %v", err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "chrome"), []byte("not a camoufox executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := findBrowserExecutable(root); err == nil {
		t.Fatal("non-browser walk entry found executable")
	}
}

func TestCheckCompatibilityWithFakePythonAndPackageJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	for _, playwrightVersion := range []string{RequiredPlaywright, RequiredPlaywrightJSON} {
		t.Run(playwrightVersion, func(t *testing.T) {
			siteRoot := fakePlaywrightPackage(t, playwrightVersion)
			python := fakeCompatPython(t, siteRoot, RequiredCamoufox, 0)
			if err := CheckCompatibility(context.Background(), python); err != nil {
				t.Fatalf("CheckCompatibility err = %v", err)
			}
		})
	}
}

func TestCheckCompatibilityFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}
	siteRoot := fakePlaywrightPackage(t, RequiredPlaywright)
	if err := CheckCompatibility(context.Background(), fakeCompatPython(t, siteRoot, "", 7)); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing camoufox err = %v", err)
	}
	if err := CheckCompatibility(context.Background(), fakeCompatPython(t, siteRoot, "0.0.0", 0)); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("camoufox mismatch err = %v", err)
	}
	badPlaywrightRoot := fakePlaywrightPackage(t, "0.0.0")
	if err := CheckCompatibility(context.Background(), fakeCompatPython(t, badPlaywrightRoot, RequiredCamoufox, 0)); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("playwright mismatch err = %v", err)
	}
	emptyRoot := t.TempDir()
	if err := CheckCompatibility(context.Background(), fakeCompatPython(t, emptyRoot, RequiredCamoufox, 0)); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("missing playwright err = %v", err)
	}
}

func TestInstalledPlaywrightVersionMetadataEdges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell python is unix-only")
	}

	failingSitePython := fakePython(t, `#!/bin/sh
exit 9
`)
	if _, err := sitePackagesRoot(failingSitePython); err == nil {
		t.Fatal("site packages root succeeded with failing python")
	}
	if _, err := installedPlaywrightVersion(failingSitePython); err == nil {
		t.Fatal("playwright version succeeded with failing site root")
	}
	if _, err := installedPlaywrightVersion(fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	printf '%s\n' '/tmp/[bad-glob'
	exit 0
fi
exit 42
`)); err == nil {
		t.Fatal("playwright lookup succeeded with bad glob root")
	}

	base := t.TempDir()
	reportedRoot := filepath.Join(base, "python3.13", "site-packages")
	fallbackPackage := filepath.Join(base, "lib", "python3.13", "site-packages", "playwright", "driver", "package")
	if err := os.MkdirAll(fallbackPackage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fallbackPackage, "package.json"), []byte(`{"version":"`+RequiredPlaywright+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := installedPlaywrightVersion(fakeSitePython(t, reportedRoot)); err != nil || got != RequiredPlaywright {
		t.Fatalf("fallback playwright version = %q, %v", got, err)
	}

	dirPackageRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirPackageRoot, "playwright", "driver", "package", "package.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := installedPlaywrightVersion(fakeSitePython(t, dirPackageRoot)); err == nil {
		t.Fatal("directory package.json was read successfully")
	}

	badJSONRoot := t.TempDir()
	badJSONPackage := filepath.Join(badJSONRoot, "playwright", "driver", "package")
	if err := os.MkdirAll(badJSONPackage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badJSONPackage, "package.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := installedPlaywrightVersion(fakeSitePython(t, badJSONRoot)); err == nil {
		t.Fatal("invalid package.json parsed successfully")
	}
}

func TestCamoufoxManifestSupportMatrixHasChecksums(t *testing.T) {
	supported := make(map[string]bool)
	for _, key := range camoufoxSupportedManifestKeys(CamoufoxBinaryVersion) {
		supported[key] = true
		got, ok := camoufoxManifestSHA256[key]
		if !ok {
			t.Fatalf("supported platform missing manifest checksum: %s", key)
		}
		if len(got) == 0 {
			t.Fatalf("manifest checksums for %s are empty", key)
		}
		for _, checksum := range got {
			if !isLowerHexSHA256(checksum) {
				t.Fatalf("manifest checksum for %s is not a lowercase sha256: %q", key, checksum)
			}
		}
	}
	for key, checksums := range camoufoxManifestSHA256 {
		if len(checksums) == 0 {
			t.Fatalf("manifest checksum is empty for %s", key)
		}
		if !supported[key] {
			t.Fatalf("manifest checksum recorded for unsupported platform: %s", key)
		}
	}
	if supported[camoufoxManifestKey(CamoufoxBinaryVersion, "windows", "arm64")] {
		t.Fatal("windows/arm64 is marked supported without an upstream Camoufox asset")
	}
}

func TestCamoufoxManifestSupportMatrixIncludesLinuxReleasePlatform(t *testing.T) {
	supported := make(map[string]bool)
	for _, key := range camoufoxSupportedManifestKeys(CamoufoxBinaryVersion) {
		supported[key] = true
	}
	if !supported[camoufoxManifestKey(CamoufoxBinaryVersion, "linux", "amd64")] {
		t.Fatal("linux/amd64 release platform is not covered by the Camoufox manifest support matrix")
	}
}

func TestCamoufoxManifestVerificationCoversSupportedPlatforms(t *testing.T) {
	browser := fakeBrowserTree(t)
	expected, err := camoufoxBrowserManifestSHA256(browser)
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range camoufoxSupportedPlatforms {
		t.Run(platform.GOOS+"_"+platform.GOARCH, func(t *testing.T) {
			restoreRuntime := setSidecarRuntime(t, platform.GOOS, platform.GOARCH)
			defer restoreRuntime()
			restoreManifest := replaceManifestChecksum(t, expected)
			defer restoreManifest()
			if err := verifyCamoufoxManifest(browser); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCamoufoxManifestChecksumValidatesAndSkipsVolatileFiles(t *testing.T) {
	browser := fakeBrowserTree(t)
	first, err := camoufoxBrowserManifestSHA256(browser)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browser, ".gomoufox.lock"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(browser, "Cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browser, "Cache", "ignored"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := camoufoxBrowserManifestSHA256(browser)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("manifest changed for volatile files: %s != %s", second, first)
	}
	restoreManifest := replaceManifestChecksum(t, first)
	defer restoreManifest()
	if err := verifyCamoufoxManifest(browser); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browser, "resources", "prefs.js"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCamoufoxManifest(browser); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("verify mismatch err = %v", err)
	}
}

func TestCamoufoxManifestChecksumFilesystemErrorEdges(t *testing.T) {
	browser := fakeBrowserTree(t)
	oldRel := camoufoxManifestRel
	oldInfo := camoufoxManifestInfo
	restore := func() {
		camoufoxManifestRel = oldRel
		camoufoxManifestInfo = oldInfo
	}
	t.Cleanup(restore)

	camoufoxManifestRel = func(root, path string) (string, error) {
		if path == root {
			return oldRel(root, path)
		}
		return "", errors.New("rel failed")
	}
	if _, err := camoufoxBrowserManifestSHA256(browser); err == nil || !strings.Contains(err.Error(), "rel failed") {
		t.Fatalf("rel failure err = %v", err)
	}
	camoufoxManifestRel = oldRel

	camoufoxManifestInfo = func(fs.DirEntry) (fs.FileInfo, error) {
		return nil, errors.New("info failed")
	}
	if _, err := camoufoxBrowserManifestSHA256(browser); err == nil || !strings.Contains(err.Error(), "info failed") {
		t.Fatalf("info failure err = %v", err)
	}
}

func isLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}

func fakeBrowserTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	exe := "firefox"
	if runtime.GOOS == "windows" {
		exe = "firefox.exe"
	}
	if err := os.WriteFile(filepath.Join(dir, exe), []byte("fake executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	resources := filepath.Join(dir, "resources")
	if err := os.MkdirAll(resources, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "prefs.js"), []byte("pref=value"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func fakeVerifiedBrowserTree(t *testing.T) string {
	t.Helper()
	browser := fakeBrowserTree(t)
	expected, err := camoufoxBrowserManifestSHA256(browser)
	if err != nil {
		t.Fatal(err)
	}
	restore := replaceManifestChecksum(t, expected)
	t.Cleanup(restore)
	return browser
}

func assertHashLockedPipArgs(t *testing.T, args string, upgrade bool) {
	t.Helper()
	for _, want := range []string{"--disable-pip-version-check", "--require-hashes", "--only-binary=:all:", "-r"} {
		if !strings.Contains(args, want) {
			t.Fatalf("pip args %q missing %s", args, want)
		}
	}
	if upgrade && !strings.Contains(args, "--upgrade") {
		t.Fatalf("pip args %q missing --upgrade", args)
	}
	if !upgrade && strings.Contains(args, "--upgrade") {
		t.Fatalf("pip args %q unexpectedly upgrades", args)
	}
}

func fakePython(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "python")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakePlaywrightPackage(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "playwright", "driver", "package")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"version":`+strconv.Quote(version)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func fakeCompatPython(t *testing.T, siteRoot, camoufoxVersion string, camoufoxExit int) string {
	t.Helper()
	camoufoxBranch := "exit " + strconv.Itoa(camoufoxExit)
	if camoufoxExit == 0 {
		camoufoxBranch = "printf '%s\\n' " + shQuote(camoufoxVersion) + "\n\t\t\texit 0"
	}
	script := `#!/bin/sh
if [ "$1" = "-c" ]; then
	case "$2" in
		*getsitepackages*)
			printf '%s\n' ` + shQuote(siteRoot) + `
			exit 0
			;;
		*importlib.metadata*)
			` + camoufoxBranch + `
			;;
	esac
fi
exit 42
`
	return fakePython(t, script)
}

func fakeCompatibleVenv(t *testing.T, siteRoot, camoufoxVersion string) string {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "-c" ]; then
	case "$2" in
		*sys.version_info*)
			exit 0
			;;
		*getsitepackages*)
			printf '%s\n' ` + shQuote(siteRoot) + `
			exit 0
			;;
		*importlib.metadata*)
			printf '%s\n' ` + shQuote(camoufoxVersion) + `
			exit 0
			;;
	esac
fi
exit 42
`
	return fakeVenv(t, script)
}

func fakeSitePython(t *testing.T, siteRoot string) string {
	t.Helper()
	return fakePython(t, `#!/bin/sh
if [ "$1" = "-c" ]; then
	case "$2" in
		*getsitepackages*)
			printf '%s\n' `+shQuote(siteRoot)+`
			exit 0
			;;
	esac
fi
exit 42
`)
}

func writeFakePip(t *testing.T, venv, script string) string {
	t.Helper()
	pip, err := VenvPip(venv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pip), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pip, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return pip
}

func mustVenvPip(t *testing.T, venv string) string {
	t.Helper()
	pip, err := VenvPip(venv)
	if err != nil {
		t.Fatal(err)
	}
	return pip
}

func setSidecarRuntime(t *testing.T, goos, goarch string) func() {
	t.Helper()
	oldGOOS, oldGOARCH := sidecarGOOS, sidecarGOARCH
	sidecarGOOS, sidecarGOARCH = goos, goarch
	restore := func() {
		sidecarGOOS, sidecarGOARCH = oldGOOS, oldGOARCH
	}
	t.Cleanup(restore)
	return restore
}

func replaceManifestChecksum(t *testing.T, expected string) func() {
	t.Helper()
	original := camoufoxManifestSHA256
	copyMap := make(map[string][]string, len(original))
	for key, value := range original {
		copyMap[key] = append([]string(nil), value...)
	}
	key := camoufoxManifestKey(CamoufoxBinaryVersion, sidecarGOOS, sidecarGOARCH)
	camoufoxManifestSHA256 = copyMap
	camoufoxManifestSHA256[key] = []string{expected}
	return func() {
		camoufoxManifestSHA256 = original
	}
}

func replaceUserCacheDir(t *testing.T, dir string, err error) func() {
	t.Helper()
	original := userCacheDir
	userCacheDir = func() (string, error) {
		return dir, err
	}
	restore := func() {
		userCacheDir = original
	}
	t.Cleanup(restore)
	return restore
}

func shQuote(value string) string {
	return strconv.Quote(strings.ReplaceAll(value, "'", "'\"'\"'"))
}
