package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func inspectionFixture(t *testing.T) (string, RuntimeRoot) {
	t.Helper()
	t.Cleanup(overrideSidecarPlatform(t, "linux", "amd64"))
	t.Cleanup(replaceRuntimeAssetInstallers(t))
	driver := installPlaywrightDriverForRuntime
	installPlaywrightDriverForRuntime = func(ctx context.Context, root RuntimeRoot, opts InstallOptions) error {
		if err := driver(ctx, root, opts); err != nil {
			return err
		}
		if err := os.WriteFile(root.NodeJS, []byte("#!/bin/sh\nif [ \"$1\" = '--version' ]; then echo v24.11.1; else echo 'Version 1.57.0'; fi\n"), 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "package.json"), []byte(`{"version":"1.57.0"}`), 0600)
	}
	cache := t.TempDir()
	root, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	return cache, root
}

func refreshInspectionManifest(t *testing.T, root RuntimeRoot) {
	t.Helper()
	m := NewRuntimeAssetManifest(root, sidecarGOOS, sidecarGOARCH)
	if err := PopulateRuntimeAssetManifest(root, &m); err != nil {
		t.Fatal(err)
	}
	if err := WriteRuntimeAssetManifest(root.ManifestPath, m); err != nil {
		t.Fatal(err)
	}
}

func TestInspectInstallationReportsObservedVersionsWithoutWrites(t *testing.T) {
	cache, root := inspectionFixture(t)
	before, err := treeSHA256(cache)
	if err != nil {
		t.Fatal(err)
	}
	r := InspectInstallation(context.Background(), cache, "")
	if r.Runtime != RuntimeNodeDirect || r.CacheDir != cache || !r.RuntimeAssets.OK || r.RuntimeAssets.Status != "verified" || !r.Node.OK || r.Node.Version != "24.11.1" || !r.Playwright.OK || r.Playwright.Version != "1.57.0" || r.Playwright.DriverVersion != "1.57.0" || r.CamoufoxPkg.Status != "not_required" || r.CamoufoxBin.Path != filepath.Join(root.BrowserResourcesDir, "camoufox") {
		t.Fatalf("inspection = %#v", r)
	}
	after, err := treeSHA256(cache)
	if err != nil || before != after {
		t.Fatalf("cache changed: before=%s after=%s err=%v", before, after, err)
	}
	if _, err := os.Stat(filepath.Join(cache, ".gomoufox-install.lock")); !os.IsNotExist(err) {
		t.Fatalf("inspection acquired lock: %v", err)
	}
}

func TestInspectInstallationRetainsFailureEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		mutate       func(*testing.T, RuntimeRoot)
	}{
		{"missing manifest", "missing_manifest", func(t *testing.T, r RuntimeRoot) {
			if err := os.Remove(r.ManifestPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"invalid manifest", "invalid_manifest", func(t *testing.T, r RuntimeRoot) {
			if err := os.WriteFile(r.ManifestPath, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing digest", "invalid_manifest", func(t *testing.T, r RuntimeRoot) {
			m, _ := LoadRuntimeAssetManifest(r.ManifestPath)
			m.Assets[0].SHA256 = ""
			if err := WriteRuntimeAssetManifest(r.ManifestPath, m); err != nil {
				t.Fatal(err)
			}
		}},
		{"not ready", "incomplete_install", func(t *testing.T, r RuntimeRoot) {
			if err := os.Remove(r.ReadyMarkerPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"bad pin", "version_mismatch", func(t *testing.T, r RuntimeRoot) {
			m, _ := LoadRuntimeAssetManifest(r.ManifestPath)
			m.CamoufoxVersion = "old"
			if err := WriteRuntimeAssetManifest(r.ManifestPath, m); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing node", "missing_asset", func(t *testing.T, r RuntimeRoot) {
			if err := os.Remove(r.NodeJS); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt browser", "corrupt_asset", func(t *testing.T, r RuntimeRoot) {
			if err := os.WriteFile(filepath.Join(r.BrowserResourcesDir, "camoufox"), []byte("corrupt"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"stale launcher", "stale_launcher", func(t *testing.T, r RuntimeRoot) {
			if err := os.WriteFile(r.LaunchServerJS, []byte("old launcher"), 0600); err != nil {
				t.Fatal(err)
			}
			refreshInspectionManifest(t, r)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache, root := inspectionFixture(t)
			tc.mutate(t, root)
			before, _ := treeSHA256(cache)
			r := InspectInstallation(context.Background(), cache, RuntimeNodeDirect)
			if r.RuntimeAssets.OK || r.RuntimeAssets.Status != tc.status || r.RuntimeAssets.Path != root.Root || r.RuntimeAssets.ExpectedVersion != CamoufoxBinaryVersion || r.RuntimeAssets.Error == "" || r.CamoufoxBin.OK {
				t.Fatalf("inspection = %#v", r)
			}
			after, _ := treeSHA256(cache)
			if before != after {
				t.Fatal("inspection repaired or changed the cache")
			}
		})
	}
}

func TestInspectInstallationDoesNotInventVersions(t *testing.T) {
	cache, root := inspectionFixture(t)
	if err := os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "package.json"), []byte(`{"version":"0.0.1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	refreshInspectionManifest(t, root)
	r := InspectInstallation(context.Background(), cache, RuntimeNodeDirect)
	if r.Playwright.OK || r.Playwright.Version != "0.0.1" || r.Playwright.ExpectedVersion != RequiredPlaywright {
		t.Fatalf("package = %#v", r.Playwright)
	}
	if err := os.WriteFile(root.NodeJS, []byte("#!/bin/sh\necho v1.2.3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	refreshInspectionManifest(t, root)
	r = InspectInstallation(context.Background(), cache, RuntimeNodeDirect)
	if r.Node.OK || r.Node.Version != "1.2.3" {
		t.Fatalf("node = %#v", r.Node)
	}
}

func TestInspectPythonInstallation(t *testing.T) {
	cache, root := inspectionFixture(t)
	// This fixture has a deterministic browser digest, distinct from release assets.
	key := camoufoxManifestKey(CamoufoxBinaryVersion, sidecarGOOS, sidecarGOARCH)
	old := camoufoxManifestSHA256[key]
	t.Cleanup(func() { camoufoxManifestSHA256[key] = old })
	sum, err := camoufoxBrowserManifestSHA256(root.BrowserResourcesDir)
	if err != nil {
		t.Fatal(err)
	}
	camoufoxManifestSHA256[key] = []string{sum}
	r := InspectInstallation(context.Background(), cache, RuntimePython)
	if r.RuntimeAssets.OK || r.Venv.Status != "missing_environment" {
		t.Fatalf("missing venv = %#v", r)
	}
	bin := filepath.Join(cache, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(bin, "python")
	metadata := `{"python":"3.13.0","camoufox":"` + RequiredCamoufox + `","playwright":"` + RequiredPlaywright + `"}`
	script := "#!/bin/sh\n[ \"$1\" = '-B' ] && [ \"$2\" = '-I' ] && [ \"$3\" = '-c' ] || exit 1\nprintf '%s\\n' '" + metadata + "'\n"
	if err := os.WriteFile(python, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	before, _ := treeSHA256(cache)
	r = InspectInstallation(context.Background(), cache, RuntimePython)
	if !r.RuntimeAssets.OK || r.Python.Version != "3.13.0" || r.CamoufoxPkg.Version != RequiredCamoufox || r.Playwright.Match == nil || !*r.Playwright.Match {
		t.Fatalf("Python = %#v", r)
	}
	after, _ := treeSHA256(cache)
	if before != after {
		t.Fatal("Python inspection wrote to cache")
	}
	if err := os.WriteFile(python, []byte("#!/bin/sh\necho '{}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r = InspectInstallation(context.Background(), cache, RuntimePython)
	if r.RuntimeAssets.OK || r.CamoufoxPkg.OK || r.Playwright.OK {
		t.Fatalf("missing packages = %#v", r)
	}
}

func TestInspectionLimitsAndSelection(t *testing.T) {
	t.Cleanup(overrideSidecarPlatform(t, "linux", "amd64"))
	cache := filepath.Join(t.TempDir(), "absent")
	r := InspectInstallation(context.Background(), cache, RuntimeNodeDirect)
	if r.RuntimeAssets.Status != "missing_manifest" || r.RuntimeAssets.Version != "" || r.Node.Version != "" || r.Playwright.Version != "" {
		t.Fatalf("missing = %#v", r)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("created cache: %v", err)
	}
	if r := InspectInstallation(context.Background(), cache, "other"); r.RuntimeAssets.Status != "unsupported_runtime" {
		t.Fatalf("unsupported = %#v", r)
	}
	t.Cleanup(overrideSidecarPlatform(t, "windows", "arm64"))
	if r := InspectInstallation(context.Background(), cache, ""); r.RuntimeAssets.Status != "unsupported_platform" {
		t.Fatalf("platform = %#v", r)
	}
	var buf inspectionOutput
	if _, err := buf.Write(make([]byte, 4097)); err == nil {
		t.Fatal("oversized output accepted")
	}
	path := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	var v any
	if err := readInspectionJSON(path, &v); err == nil {
		t.Fatal("oversized JSON accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inspectionCommand(ctx, "/bin/sh", "-c", "sleep 10"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := inspectionCommand(ctx, "/bin/sh", "-c", "exec sleep 10"); err == nil {
		t.Fatal("deadline ignored")
	}
}

func TestInspectionReportsUnavailableProcessesAndFiles(t *testing.T) {
	t.Cleanup(overrideSidecarPlatform(t, "linux", "amd64"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if r := InspectInstallation(context.Background(), "", ""); r.CacheDir != DefaultCacheDir() || r.RuntimeAssets.OK {
		t.Fatalf("default missing cache = %#v", r)
	}
	if got := inspectionFailureStatus(errors.New("unexpected failure")); got != "inspection_failed" {
		t.Fatalf("unclassified failure = %s", got)
	}
	var value any
	if err := readInspectionJSON(t.TempDir(), &value); err == nil {
		t.Fatal("directory accepted as JSON")
	}
	cache, root := inspectionFixture(t)
	if err := os.WriteFile(root.NodeJS, []byte("#!/bin/sh\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	refreshInspectionManifest(t, root)
	r := InspectInstallation(context.Background(), cache, RuntimeNodeDirect)
	if r.Node.Status != "unavailable" || r.Playwright.Status != "unavailable" {
		t.Fatalf("failing version processes = %#v", r)
	}
	if err := os.WriteFile(root.NodeJS, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo v24.11.1; else echo 'Version 0.0.1'; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	refreshInspectionManifest(t, root)
	r = InspectInstallation(context.Background(), cache, RuntimeNodeDirect)
	if r.Playwright.Status != "version_mismatch" || r.Playwright.DriverVersion != "0.0.1" {
		t.Fatalf("mismatched driver = %#v", r.Playwright)
	}
	if err := os.MkdirAll(filepath.Join(cache, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "bin", "python"), []byte("#!/bin/sh\necho 'invalid json'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r = InspectInstallation(context.Background(), cache, RuntimePython)
	if r.Python.Status != "unavailable" || r.CamoufoxPkg.Status != "unavailable" || r.CamoufoxBin.Status != "corrupt_asset" {
		t.Fatalf("unreadable package metadata and unpinned browser = %#v", r)
	}
	if err := os.RemoveAll(root.BrowserResourcesDir); err != nil {
		t.Fatal(err)
	}
	r = InspectInstallation(context.Background(), cache, RuntimePython)
	if r.CamoufoxBin.Status != "missing_asset" {
		t.Fatalf("missing Python browser = %#v", r.CamoufoxBin)
	}
}
