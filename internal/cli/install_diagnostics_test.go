package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gomoufox "github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/sidecar"
)

func TestInstallJSONReportsScopedFailureAndRecovery(t *testing.T) {
	t.Setenv("GOMOUFOX_SKIP_FETCH", "1")
	dir := filepath.Join(t.TempDir(), "cache with spaces")
	var out, stderr bytes.Buffer
	runner := Runner{Hooks: Hooks{Install: func(_ context.Context, req InstallRequest) error {
		if req.Dir != dir || req.Runtime != "python" || req.Python != "/custom/python" {
			t.Fatalf("request = %#v", req)
		}
		return fmt.Errorf("%w: pinned driver is unavailable and fetch is disabled", gomoufox.ErrNotInstalled)
	}}}
	code := runner.Run(context.Background(), []string{"install", "--json", "--dir", dir, "--runtime", "python", "--python", "/custom/python"}, Streams{Stdout: &out, Stderr: &stderr})
	var result InstallResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("code=%d out=%s: %v", code, &out, err)
	}
	if code != ExitUnavailable || result.OK || !result.FetchDisabled || result.Recovery.Reason != "fetch_disabled" || result.CacheDir != dir || result.Runtime != "python" {
		t.Fatalf("result = %#v", result)
	}
	want := []string{result.Executable.Path, "install", "--dir", dir, "--runtime", "python", "--python", "/custom/python"}
	if !reflect.DeepEqual(result.Recovery.Command, want) {
		t.Fatalf("command = %#v", result.Recovery.Command)
	}
	if !reflect.DeepEqual(result.DoctorCommand, []string{result.Executable.Path, "doctor", "--dir", dir, "--runtime", "python", "--json"}) {
		t.Fatalf("doctor = %#v", result.DoctorCommand)
	}
}

func TestInstallationFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"}, {gomoufox.ErrTimeout, "timeout"},
		{os.ErrPermission, "permission_denied"}, {errors.New("mkdir: permission denied"), "permission_denied"},
		{errors.New("fetch is disabled"), "fetch_disabled"}, {errors.New("runtime assets unsupported for windows/arm64"), "unsupported_platform"},
		{errors.New("Python 3.9+ not found"), "missing_python"},
		{errors.New("download archive checksum mismatch synthetic-private-marker"), "integrity_mismatch"},
		{errors.New("download pinned driver: HTTP 404 Not Found"), "download_failed"},
		{errors.New("pip install locked requirements failed"), "dependency_install_failed"},
		{gomoufox.ErrVersionMismatch, "version_mismatch"}, {gomoufox.ErrNotInstalled, "unavailable"}, {errors.New("unknown"), "installation_failed"},
	} {
		t.Run(tc.reason+"/"+tc.err.Error(), func(t *testing.T) {
			result := installResult(InstallRequest{Dir: t.TempDir()}, tc.err)
			if result.Recovery.Reason != tc.reason {
				t.Fatalf("reason = %#v", result.Recovery)
			}
			encoded, _ := json.Marshal(result)
			if bytes.Contains(encoded, []byte("synthetic-private-marker")) {
				t.Fatal("raw failure leaked into structured response")
			}
		})
	}
}

func TestDoctorRecoveryUsesSelectedCacheAndPreservesFetchPolicy(t *testing.T) {
	t.Setenv("GOMOUFOX_SKIP_FETCH", "1")
	t.Setenv(sidecar.EnvCamoufoxPath, "/offline/source")
	for _, tc := range []struct {
		status         string
		force, command bool
	}{
		{"stale_launcher", false, true}, {"corrupt_asset", true, true}, {"invalid_manifest", true, true},
		{"version_mismatch", true, true}, {"missing_manifest", false, true}, {"missing_asset", false, true},
		{"missing_environment", false, true}, {"incomplete_install", false, true}, {"unavailable", false, true},
		{"unsupported_platform", false, false}, {"permission_denied", false, false}, {"inspection_failed", false, false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			r := DoctorReport{Executable: ExecutableCheck{Path: "/bin/current"}, CacheDir: "/custom/cache", Runtime: "node-direct", RuntimeAssets: Check{Status: tc.status, Error: "failed"}}
			recovery := doctorRecovery(r)
			if len(recovery) != 1 || recovery[0].Reason != tc.status || !strings.Contains(recovery[0].Hint, "GOMOUFOX_SKIP_FETCH") || !strings.Contains(recovery[0].Hint, "GOMOUFOX_CAMOUFOX_PATH") {
				t.Fatalf("recovery = %#v", recovery)
			}
			if (len(recovery[0].Command) != 0) != tc.command || containsArgument(recovery[0].Command, "--force") != tc.force {
				t.Fatalf("command = %#v", recovery[0].Command)
			}
			if tc.command && !reflect.DeepEqual(recovery[0].Command, runtimeCommand("/bin/current", "install", "/custom/cache", "node-direct", tc.force)) {
				t.Fatalf("wrong scope: %#v", recovery)
			}
		})
	}
	if r := doctorRecovery(allDoctorOK()); len(r) != 0 {
		t.Fatalf("healthy recovery = %#v", r)
	}
	python := DoctorReport{RuntimeAssets: Check{Status: "unavailable"}, Venv: Check{Status: "missing_environment"}, Runtime: "python", CacheDir: "/python", Executable: ExecutableCheck{Path: "/bin/current"}}
	if r := doctorRecovery(python); r[0].Reason != "missing_environment" {
		t.Fatalf("Python reason = %#v", r)
	}
}

func TestInstallResultSuccessAndForcedRetry(t *testing.T) {
	result := installResult(InstallRequest{}, nil)
	if !result.OK || result.Recovery != nil || result.CacheDir == "" || result.Runtime != "node-direct" {
		t.Fatalf("success = %#v", result)
	}
	result = installResult(InstallRequest{Force: true}, errors.New("download failed"))
	if !containsArgument(result.Recovery.Command, "--force") {
		t.Fatalf("lost force: %#v", result)
	}
	var out bytes.Buffer
	runner := Runner{Hooks: Hooks{Install: func(context.Context, InstallRequest) error { return nil }}}
	if code := runner.Run(context.Background(), []string{"install", "--json"}, Streams{Stdout: &out}); code != ExitOK || !strings.Contains(out.String(), `"ok":true`) || strings.Contains(out.String(), ">>") {
		t.Fatalf("code=%d out=%s", code, &out)
	}
	if code := runner.Run(context.Background(), []string{"install", "--json"}, Streams{Stdout: errWriter{}}); code != ExitRuntime {
		t.Fatalf("write failure = %d", code)
	}
}

func TestRecoveryCommandQuotesPathsLiterally(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	argument := "space ' quotes $(touch " + marker + ") `touch " + marker + "`\nnewline"
	args := []string{"printf", "%s", argument}
	out, err := exec.Command("/bin/sh", "-c", displayCommand(args)).Output()
	if err != nil || string(out) != argument {
		t.Fatalf("quoted command changed argument: %q, %v", out, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("quoted command executed substitution")
	}
}
