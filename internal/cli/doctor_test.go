package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	gomoufox "github.com/ehmo/gomoufox"
)

func TestDoctorNeverInstalls(t *testing.T) {
	original := ensureInstalledForCLI
	t.Cleanup(func() { ensureInstalledForCLI = original })
	called := false
	ensureInstalledForCLI = func(context.Context, ...func(*gomoufox.InstallOptions)) error {
		called = true
		return gomoufox.ErrNotInstalled
	}
	_, _ = defaultDoctor(context.Background(), DoctorRequest{Dir: t.TempDir()})
	if called {
		t.Fatal("doctor called the installer; inspection must not create or repair a cache")
	}
}

func TestDoctorExecutableIdentity(t *testing.T) {
	dir := t.TempDir()
	current, other, link := filepath.Join(dir, "current"), filepath.Join(dir, "other"), filepath.Join(dir, "link")
	for _, path := range []string{current, other} {
		if err := os.WriteFile(path, []byte("binary"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(current, link); err != nil {
		t.Fatal(err)
	}
	executable, lookup, build := doctorExecutable, doctorLookPath, doctorBuildInfo
	t.Cleanup(func() { doctorExecutable, doctorLookPath, doctorBuildInfo = executable, lookup, build })
	doctorExecutable = func() (string, error) { return link, nil }
	doctorLookPath = func(string) (string, error) { return other, nil }
	doctorBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{GoVersion: "test-go", Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef"}, {Key: "vcs.modified", Value: "true"}}}, true
	}
	r := doctorExecutableCheck()
	resolved, _ := filepath.EvalSymlinks(current)
	if r.Path != link || r.ResolvedPath != resolved || r.PATHBinary != other || r.Revision != "abcdef" || !r.Modified || !strings.Contains(r.Warning, "different file") || r.UpdateCheck != "not_performed" {
		t.Fatalf("identity = %#v", r)
	}
	doctorLookPath = func(string) (string, error) { return current, nil }
	if r := doctorExecutableCheck(); strings.Contains(r.Warning, "different file") {
		t.Fatalf("same binary: %#v", r)
	}
}

func TestDoctorSelectionAndMissingCacheAreReadOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	var out, stderr bytes.Buffer
	code := (Runner{}).Run(context.Background(), []string{"doctor", "--dir", dir, "--runtime", "node-direct", "--json"}, Streams{Stdout: &out, Stderr: &stderr})
	if code != ExitUnavailable || !strings.Contains(out.String(), `"status":"missing_manifest"`) || !strings.Contains(out.String(), dir) {
		t.Fatalf("code=%d out=%s stderr=%s", code, &out, &stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("inspection created cache: %v", err)
	}
	for _, args := range [][]string{{"doctor", "extra"}, {"doctor", "--runtime", "other"}, {"doctor", "--dir"}} {
		if code := (Runner{}).Run(context.Background(), args, Streams{}); code != ExitUsage {
			t.Fatalf("%v = %d", args, code)
		}
	}
}

func TestDoctorWarningDoesNotHideIntegrityFailure(t *testing.T) {
	r := DoctorReport{CamoufoxBin: Check{Status: "corrupt_asset", Error: "digest mismatch", Warning: "trust override"}}
	if !r.hasFailure() {
		t.Fatal("warning hid integrity failure")
	}
	var out bytes.Buffer
	printDoctor(&out, r)
	if !strings.Contains(out.String(), "[FAIL]  camoufox-bin") {
		t.Fatal(out.String())
	}
}

func TestDoctorTextIncludesSelectedCacheVersionsAndRepairCommand(t *testing.T) {
	var out bytes.Buffer
	printDoctor(&out, DoctorReport{
		CacheDir: "/selected cache", Runtime: "node-direct",
		Node:     Check{Path: "/selected cache/node", Version: "1.2.3", ExpectedVersion: "24.11.1", Error: "version mismatch"},
		Recovery: []RuntimeRecovery{{Reason: "version_mismatch", Hint: "repair selected cache", Command: runtimeCommand("", "install", "/selected cache", "node-direct", true)}},
	})
	for _, want := range []string{"/selected cache (node-direct)", "expected 24.11.1", "/selected cache/node", "[NEXT] version_mismatch: repair selected cache", "'gomoufox' 'install' '--dir' '/selected cache' '--runtime' 'node-direct' '--force'"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, &out)
		}
	}
}
