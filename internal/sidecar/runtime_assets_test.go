package sidecar

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureRuntimeAssetsWritesManifestCacheLayout(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	restoreAssets := replaceRuntimeAssetInstallers(t)
	defer restoreAssets()

	cacheRoot := t.TempDir()
	layout, err := EnsureRuntimeAssets(context.Background(), InstallOptions{
		VenvDir: cacheRoot,
		Runtime: RuntimeNodeDirect,
	})
	if err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}

	for _, dir := range []string{layout.Root, layout.BrowserResourcesDir, layout.PlaywrightPackageDir, filepath.Dir(layout.NodeJS), filepath.Dir(layout.LaunchServerJS)} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("dir %s stat=%v err=%v; want directory", dir, st, err)
		}
	}
	if _, err := os.Stat(layout.ReadyMarkerPath); err != nil {
		t.Fatalf("install stamp missing: %v", err)
	}

	raw, err := os.ReadFile(layout.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest RuntimeAssetManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.SchemaVersion != runtimeAssetManifestSchemaVersion {
		t.Fatalf("schema=%d; want %d", manifest.SchemaVersion, runtimeAssetManifestSchemaVersion)
	}
	if manifest.Runtime != RuntimeNodeDirect {
		t.Fatalf("manifest runtime = %s", manifest.Runtime)
	}
	if manifest.PlaywrightVersion != RequiredPlaywrightJSON {
		t.Fatalf("manifest playwright pin = %s", manifest.PlaywrightVersion)
	}
	wantPaths := []string{
		relPath(layout.Root, layout.BrowserResourcesDir),
		relPath(layout.Root, layout.PlaywrightPackageDir),
		relPath(layout.Root, layout.NodeJS),
		relPath(layout.Root, layout.LaunchServerJS),
	}
	for _, want := range wantPaths {
		found := false
		for _, asset := range manifest.Assets {
			if asset.Path == want && asset.Source != "" && asset.SHA256 != "" && asset.License != "" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("manifest missing asset path with provenance/checksum/license: %s", want)
		}
	}
	if !strings.Contains(layout.Root, filepath.Join("runtime", "v1", CamoufoxBinaryVersion, "linux-amd64")) {
		t.Fatalf("layout root %q does not encode versioned platform cache", layout.Root)
	}
}

func TestRuntimeLaunchServerDecodesBase64Payload(t *testing.T) {
	for _, want := range []string{
		`Buffer.from(encoded, "base64").toString("utf8")`,
		`JSON.parse(Buffer.from(encoded, "base64").toString("utf8"))`,
	} {
		if !strings.Contains(runtimeLaunchServerJS, want) {
			t.Fatalf("runtime launch server missing %q:\n%s", want, runtimeLaunchServerJS)
		}
	}
}

func TestResolveRuntimeAssetsRejectsStaleLaunchServer(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	restoreAssets := replaceRuntimeAssetInstallers(t)
	defer restoreAssets()

	cacheRoot := t.TempDir()
	layout, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cacheRoot, Runtime: RuntimeNodeDirect})
	if err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}
	if err := os.WriteFile(layout.LaunchServerJS, []byte("// old launch server"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveRuntimeAssets(cacheRoot); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("ResolveRuntimeAssets stale launch server err = %v", err)
	}
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cacheRoot, Runtime: RuntimeNodeDirect}); err != nil {
		t.Fatalf("EnsureRuntimeAssets should refresh stale launch server: %v", err)
	}
	if err := VerifyRuntimeLaunchServerFresh(layout); err != nil {
		t.Fatalf("launch server was not refreshed: %v", err)
	}
}

func TestEnsureRuntimeAssetsRejectsUnsupportedPlatform(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "plan9", "riscv64")
	defer restorePlatform()

	_, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: t.TempDir(), Runtime: RuntimeNodeDirect})
	if !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err=%v; want unsupported ErrNotInstalled", err)
	}
}

func TestEnsureRuntimeAssetsReusesReadyRuntimeWhenFetchDisabled(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	restoreAssets := replaceRuntimeAssetInstallers(t)
	cacheRoot := t.TempDir()
	layout, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cacheRoot, Runtime: RuntimeNodeDirect})
	if err != nil {
		t.Fatalf("initial EnsureRuntimeAssets: %v", err)
	}
	restoreAssets()
	origDriver := installPlaywrightDriverForRuntime
	origBrowser := installCamoufoxBrowserForRuntime
	defer func() {
		installPlaywrightDriverForRuntime = origDriver
		installCamoufoxBrowserForRuntime = origBrowser
	}()
	installPlaywrightDriverForRuntime = func(context.Context, RuntimeRoot, InstallOptions) error {
		t.Fatal("ready runtime should not reinstall Playwright")
		return nil
	}
	installCamoufoxBrowserForRuntime = func(context.Context, RuntimeRoot, InstallOptions) error {
		t.Fatal("ready runtime should not reinstall Camoufox")
		return nil
	}
	got, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cacheRoot, Runtime: RuntimeNodeDirect, SkipBinaryFetch: true})
	if err != nil {
		t.Fatalf("reuse EnsureRuntimeAssets: %v", err)
	}
	if got.Root != layout.Root {
		t.Fatalf("reuse root = %q, want %q", got.Root, layout.Root)
	}
}

func TestEnsureRuntimeAssetsDownloadsCamoufoxBrowserWithoutPython(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	origDriver := installPlaywrightDriverForRuntime
	origClient := runtimeAssetHTTPClient
	origBase := camoufoxReleaseAssetBaseURL
	origUserCache := userCacheDir
	defer func() {
		installPlaywrightDriverForRuntime = origDriver
		runtimeAssetHTTPClient = origClient
		camoufoxReleaseAssetBaseURL = origBase
		userCacheDir = origUserCache
	}()
	userCacheDir = func() (string, error) { return filepath.Join(t.TempDir(), "user-cache"), nil }
	installPlaywrightDriverForRuntime = func(ctx context.Context, root RuntimeRoot, opts InstallOptions) error {
		if err := os.MkdirAll(root.PlaywrightPackageDir, 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(root.NodeJS), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(root.NodeJS, []byte("#!/bin/sh\n"), 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "package.json"), []byte(`{"version":"`+RequiredPlaywrightJSON+`"}`), 0o600)
	}
	zipData := runtimeBrowserZipFixture(t)
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		_, _ = w.Write(zipData)
	}))
	defer server.Close()
	runtimeAssetHTTPClient = server.Client()
	camoufoxReleaseAssetBaseURL = server.URL
	t.Setenv(EnvTrustUnverifiedCamoufoxPath, "1")

	layout, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: t.TempDir(), Runtime: RuntimeNodeDirect})
	if err != nil {
		t.Fatalf("EnsureRuntimeAssets download: %v", err)
	}
	if requestedPath != "/"+CamoufoxBinaryVersion+"/camoufox-"+strings.TrimPrefix(CamoufoxBinaryVersion, "v")+"-lin.x86_64.zip" {
		t.Fatalf("download path = %q", requestedPath)
	}
	if _, err := os.Stat(layout.LaunchServerJS); err != nil {
		t.Fatalf("launch server missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.BrowserResourcesDir, "camoufox")); err != nil {
		t.Fatalf("browser not extracted: %v", err)
	}
	if _, _, err := ResolveRuntimeAssets(filepath.Clean(filepath.Join(layout.Root, "..", "..", "..", ".."))); err != nil {
		t.Fatalf("ResolveRuntimeAssets after download: %v", err)
	}
}

func TestCamoufoxReleaseAssetURLAndDisabledFetch(t *testing.T) {
	got, err := camoufoxReleaseAssetURL("v135.0.1-beta.24", "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/v135.0.1-beta.24/camoufox-135.0.1-beta.24-mac.arm64.zip") {
		t.Fatalf("asset URL = %q", got)
	}
	if _, err := camoufoxReleaseAssetURL("v1.2.3", "windows", "arm64"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("unsupported URL err = %v", err)
	}

	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	origUserCache := userCacheDir
	userCacheDir = func() (string, error) { return filepath.Join(t.TempDir(), "user-cache"), nil }
	defer func() { userCacheDir = origUserCache }()
	t.Setenv("GOMOUFOX_SKIP_FETCH", "1")
	err = installRuntimeCamoufoxBrowser(context.Background(), RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64"), InstallOptions{})
	if !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "fetch is disabled") {
		t.Fatalf("disabled fetch err = %v", err)
	}
}

func TestVerifyRuntimeAssetsRejectsDirectoryCorruption(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	restoreAssets := replaceRuntimeAssetInstallers(t)
	defer restoreAssets()

	cacheRoot := t.TempDir()
	layout, err := EnsureRuntimeAssets(context.Background(), InstallOptions{
		VenvDir: cacheRoot,
		Runtime: RuntimeNodeDirect,
	})
	if err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}
	manifest, err := LoadRuntimeAssetManifest(layout.ManifestPath)
	if err != nil {
		t.Fatalf("LoadRuntimeAssetManifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.PlaywrightPackageDir, "tampered.js"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = VerifyRuntimeAssets(layout, manifest, "linux", "amd64")
	if !errors.Is(err, ErrVersionMismatch) || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err=%v; want directory checksum mismatch", err)
	}
}

func TestEnsureRuntimeAssetsForceReinstallRefreshesManifest(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	restoreAssets := replaceRuntimeAssetInstallers(t)
	defer restoreAssets()

	cacheRoot := t.TempDir()
	layout, err := EnsureRuntimeAssets(context.Background(), InstallOptions{
		VenvDir: cacheRoot,
		Runtime: RuntimeNodeDirect,
	})
	if err != nil {
		t.Fatalf("initial EnsureRuntimeAssets: %v", err)
	}
	initial, err := LoadRuntimeAssetManifest(layout.ManifestPath)
	if err != nil {
		t.Fatalf("initial manifest: %v", err)
	}
	initialNodeChecksum := runtimeAssetChecksumForPath(t, initial, relPath(layout.Root, layout.NodeJS))
	if err := os.WriteFile(layout.NodeJS, []byte("#!/bin/sh\necho stale\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{
		VenvDir:        cacheRoot,
		Runtime:        RuntimeNodeDirect,
		ForceReinstall: true,
	}); err != nil {
		t.Fatalf("force reinstall EnsureRuntimeAssets: %v", err)
	}
	refreshed, err := LoadRuntimeAssetManifest(layout.ManifestPath)
	if err != nil {
		t.Fatalf("refreshed manifest: %v", err)
	}
	refreshedNodeChecksum := runtimeAssetChecksumForPath(t, refreshed, relPath(layout.Root, layout.NodeJS))
	if refreshedNodeChecksum != initialNodeChecksum {
		t.Fatalf("node checksum after force reinstall = %s, want restored %s", refreshedNodeChecksum, initialNodeChecksum)
	}
	if err := VerifyRuntimeAssets(layout, refreshed, "linux", "amd64"); err != nil {
		t.Fatalf("VerifyRuntimeAssets after force reinstall: %v", err)
	}
}

func TestEnsureInstalledNodeDirectAvoidsPythonBootstrap(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	restoreAssets := replaceRuntimeAssetInstallers(t)
	defer restoreAssets()

	origFindPython := findPythonForInstall
	origEnsureVenv := ensureVenvForInstall
	origEnsureCamoufox := ensureCamoufoxForInstall
	origEnsureBinary := ensureBinaryForInstall
	origCheckCompatibility := checkCompatibilityForInstall
	defer func() {
		findPythonForInstall = origFindPython
		ensureVenvForInstall = origEnsureVenv
		ensureCamoufoxForInstall = origEnsureCamoufox
		ensureBinaryForInstall = origEnsureBinary
		checkCompatibilityForInstall = origCheckCompatibility
	}()

	fail := func(name string) {
		t.Fatalf("%s called on node-direct install path", name)
	}
	findPythonForInstall = func(string) (string, error) { fail("FindPython"); return "", nil }
	ensureVenvForInstall = func(context.Context, string, string) error { fail("EnsureVenv"); return nil }
	ensureCamoufoxForInstall = func(context.Context, string, InstallOptions) error { fail("EnsureCamoufox"); return nil }
	ensureBinaryForInstall = func(context.Context, string, InstallOptions) error { fail("EnsureBinary"); return nil }
	checkCompatibilityForInstall = func(context.Context, string) error { fail("CheckCompatibility"); return nil }

	cacheRoot := t.TempDir()
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: cacheRoot, Runtime: RuntimeNodeDirect}); err != nil {
		t.Fatalf("EnsureInstalled node-direct: %v", err)
	}
	layout := RuntimeAssetCacheRoot(cacheRoot, "linux", "amd64")
	if _, err := os.Stat(layout.ManifestPath); err != nil {
		t.Fatalf("node-direct manifest missing: %v", err)
	}
	if _, err := os.Stat(layout.ReadyMarkerPath); err != nil {
		t.Fatalf("node-direct install stamp missing: %v", err)
	}
}

func runtimeAssetChecksumForPath(t *testing.T, manifest RuntimeAssetManifest, path string) string {
	t.Helper()
	for _, asset := range manifest.Assets {
		if asset.Path == path {
			return asset.SHA256
		}
	}
	t.Fatalf("manifest missing asset path %s", path)
	return ""
}

func overrideSidecarPlatform(t *testing.T, goos, goarch string) func() {
	t.Helper()
	oldGOOS, oldGOARCH := sidecarGOOS, sidecarGOARCH
	sidecarGOOS, sidecarGOARCH = goos, goarch
	return func() {
		sidecarGOOS, sidecarGOARCH = oldGOOS, oldGOARCH
	}
}

func replaceRuntimeAssetInstallers(t *testing.T) func() {
	t.Helper()
	origDriver := installPlaywrightDriverForRuntime
	origBrowser := installCamoufoxBrowserForRuntime
	installPlaywrightDriverForRuntime = func(ctx context.Context, root RuntimeRoot, opts InstallOptions) error {
		t.Helper()
		if err := os.MkdirAll(root.PlaywrightPackageDir, 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(root.NodeJS), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(root.NodeJS, []byte("#!/bin/sh\n"), 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "package.json"), []byte(`{"version":"`+RequiredPlaywrightJSON+`"}`), 0o600)
	}
	installCamoufoxBrowserForRuntime = func(ctx context.Context, root RuntimeRoot, opts InstallOptions) error {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(root.LaunchServerJS), 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(root.BrowserResourcesDir, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root.BrowserResourcesDir, "camoufox"), []byte("browser"), 0o700)
	}
	return func() {
		installPlaywrightDriverForRuntime = origDriver
		installCamoufoxBrowserForRuntime = origBrowser
	}
}

func runtimeBrowserZipFixture(t *testing.T) []byte {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "browser.zip")
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	header := &zip.FileHeader{Name: "bundle/camoufox", Method: zip.Deflate}
	header.SetMode(0o700)
	f, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("#!/bin/sh\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
