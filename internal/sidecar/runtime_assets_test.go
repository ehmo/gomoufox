package sidecar

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	playwright "github.com/playwright-community/playwright-go"
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

func TestEnsureRuntimeAssetsProvidesPlaywrightCoreModule(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	restoreAssets := replaceRuntimeAssetInstallers(t)
	defer restoreAssets()

	layout, err := EnsureRuntimeAssets(context.Background(), InstallOptions{
		VenvDir: t.TempDir(),
		Runtime: RuntimeNodeDirect,
	})
	if err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}

	info, err := os.Lstat(layout.PlaywrightCoreModule)
	if err != nil {
		t.Fatalf("playwright-core module missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("playwright-core module mode = %v; want symlink", info.Mode())
	}
	resolved, err := filepath.EvalSymlinks(layout.PlaywrightCoreModule)
	if err != nil {
		t.Fatalf("resolve playwright-core module: %v", err)
	}
	if !sameCanonicalFileTree(resolved, layout.PlaywrightPackageDir) {
		t.Fatalf("playwright-core module resolves to %q, want %q", resolved, layout.PlaywrightPackageDir)
	}
}

func TestResolveRuntimeAssetsRejectsMissingPlaywrightCoreModuleAndEnsureRepairsIt(t *testing.T) {
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
	if err := os.RemoveAll(layout.PlaywrightCoreModule); err != nil {
		t.Fatalf("remove playwright-core module: %v", err)
	}

	if _, _, err := ResolveRuntimeAssets(cacheRoot); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("ResolveRuntimeAssets missing module err = %v; want ErrNotInstalled", err)
	}
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{
		VenvDir: cacheRoot,
		Runtime: RuntimeNodeDirect,
	}); err != nil {
		t.Fatalf("repair EnsureRuntimeAssets: %v", err)
	}
	if _, err := os.Stat(layout.PlaywrightCoreModule); err != nil {
		t.Fatalf("repaired playwright-core module missing: %v", err)
	}
}

func TestEnsureRuntimePlaywrightCoreModuleCopiesPackageOnWindows(t *testing.T) {
	root := RuntimeAssetCacheRoot(t.TempDir(), "windows", "amd64")
	if err := os.MkdirAll(root.PlaywrightPackageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "package.json"), []byte(`{"name":"playwright-core"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root.PlaywrightPackageDir, "lib"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "lib", "entry.js"), []byte("module.exports = {};\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureRuntimePlaywrightCoreModule(root, "windows"); err != nil {
		t.Fatalf("ensure Windows playwright-core module: %v", err)
	}
	info, err := os.Lstat(root.PlaywrightCoreModule)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("Windows playwright-core module mode = %v; want copied directory", info.Mode())
	}
	sourceSum, err := treeSHA256(root.PlaywrightPackageDir)
	if err != nil {
		t.Fatal(err)
	}
	moduleSum, err := treeSHA256(root.PlaywrightCoreModule)
	if err != nil {
		t.Fatal(err)
	}
	if sourceSum != moduleSum {
		t.Fatalf("Windows playwright-core module checksum = %s, want %s", moduleSum, sourceSum)
	}

	if err := os.WriteFile(filepath.Join(root.PlaywrightCoreModule, "tampered.js"), []byte("bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureRuntimePlaywrightCoreModule(root, "windows"); err != nil {
		t.Fatalf("repair Windows playwright-core module: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root.PlaywrightCoreModule, "tampered.js")); !os.IsNotExist(err) {
		t.Fatalf("tampered Windows module entry survived repair: %v", err)
	}
}

func TestRuntimePlaywrightCoreModuleResolvesWithoutAmbientNodeModules(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if !runtimeAssetPlatformSupported(runtime.GOOS, runtime.GOARCH) {
		t.Skipf("runtime assets unsupported for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	restorePlatform := overrideSidecarPlatform(t, runtime.GOOS, runtime.GOARCH)
	defer restorePlatform()
	restoreAssets := replaceRuntimeAssetInstallers(t)
	defer restoreAssets()

	layout, err := EnsureRuntimeAssets(context.Background(), InstallOptions{
		VenvDir: t.TempDir(),
		Runtime: RuntimeNodeDirect,
	})
	if err != nil {
		t.Fatalf("EnsureRuntimeAssets: %v", err)
	}

	for name, cwd := range map[string]string{
		"unrelated working directory": t.TempDir(),
		"launcher working directory":  layout.PlaywrightPackageDir,
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(
				node,
				"-e",
				`const { createRequire } = require("module"); process.stdout.write(createRequire(process.argv[1]).resolve("playwright-core"));`,
				layout.LaunchServerJS,
			)
			cmd.Dir = cwd
			for _, entry := range os.Environ() {
				key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
				if key == "HOME" || key == "NODE_PATH" || key == "NODE_OPTIONS" {
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "HOME="+t.TempDir())
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("resolve playwright-core from launchServer.js: %v\n%s", err, out)
			}
			resolved, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
			if err != nil {
				t.Fatalf("resolve returned module path: %v", err)
			}
			want, err := filepath.EvalSymlinks(filepath.Join(layout.PlaywrightPackageDir, "index.js"))
			if err != nil {
				t.Fatalf("resolve expected module path: %v", err)
			}
			if !sameCanonicalFileTree(resolved, want) {
				t.Fatalf("playwright-core resolved to %q, want %q", resolved, want)
			}
		})
	}
}

func TestRuntimeLaunchServerDecodesBase64Payload(t *testing.T) {
	for _, want := range []string{
		`Buffer.from(encoded, "base64").toString("utf8")`,
		`JSON.parse(Buffer.from(encoded, "base64").toString("utf8"))`,
		`payload.host = "127.0.0.1"`,
		`new BrowserServerLauncherImpl("firefox").launchServer(payload)`,
	} {
		if !strings.Contains(runtimeLaunchServerJS, want) {
			t.Fatalf("runtime launch server missing %q:\n%s", want, runtimeLaunchServerJS)
		}
	}
}

func TestRuntimeLaunchServerPinsIPv4Loopback(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	root := t.TempDir()
	launchScript := filepath.Join(root, "camoufox", "launchServer.js")
	moduleDir := filepath.Join(root, "camoufox", "node_modules", "playwright-core")
	internalDir := filepath.Join(moduleDir, "lib")
	if err := os.MkdirAll(internalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launchScript, []byte(runtimeLaunchServerJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "index.js"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := `exports.BrowserServerLauncherImpl = class {
  constructor(browserName) {
    if (browserName !== "firefox")
      throw new Error("launch server did not select Firefox");
  }
  async launchServer(options) {
    if (options.host !== "127.0.0.1")
      throw new Error("launch server did not pin IPv4 loopback");
    return {
      wsEndpoint: () => "ws://" + options.host + ":4321/token",
      close: async () => {}
    };
  }
};`
	if err := os.WriteFile(filepath.Join(internalDir, "browserServerImpl.js"), []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, launchScript)
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString([]byte(`{}`)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run launch server: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "ws://127.0.0.1:4321/token" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestSameFileTreeComparesAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	same := filepath.Join(dir, "nested", "..")
	if !sameFileTree(dir, same) {
		t.Fatalf("sameFileTree(%q, %q) = false", dir, same)
	}
	if sameFileTree(dir, filepath.Join(dir, "other")) {
		t.Fatalf("sameFileTree treated different paths as equal")
	}
}

func TestCanonicalFileTreeComparisonDoesNotMakeBrowserDestinationExternal(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceMarker := filepath.Join(source, "marker")
	if err := os.WriteFile(sourceMarker, []byte("verified"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	if err := os.MkdirAll(filepath.Dir(root.BrowserResourcesDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, root.BrowserResourcesDir); err != nil {
		t.Fatal(err)
	}
	if !sameCanonicalFileTree(source, root.BrowserResourcesDir) {
		t.Fatal("canonical comparison did not resolve the browser destination symlink")
	}
	if sameFileTree(source, root.BrowserResourcesDir) {
		t.Fatal("lexical comparison treated a distinct browser destination as the source")
	}
	if _, err := treeSHA256(root.BrowserResourcesDir); err == nil {
		t.Fatal("tree hash accepted a symlink root")
	}

	if err := copyRuntimeCamoufoxBrowser(source, root); err != nil {
		t.Fatalf("copy browser from symlinked destination: %v", err)
	}
	info, err := os.Lstat(root.BrowserResourcesDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("browser destination mode = %v; want managed directory", info.Mode())
	}
	before, err := treeSHA256(root.BrowserResourcesDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourceMarker, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := treeSHA256(root.BrowserResourcesDir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("managed browser checksum changed with external source: before=%s after=%s", before, after)
	}
}

func TestVerifyRuntimePlaywrightCoreModuleRejectsInvalidLayouts(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		arrange    func(t *testing.T, root RuntimeRoot)
		wantError  error
		wantDetail string
	}{
		{
			name: "unix directory instead of symlink",
			goos: "linux",
			arrange: func(t *testing.T, root RuntimeRoot) {
				t.Helper()
				if err := os.MkdirAll(root.PlaywrightCoreModule, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantError:  ErrVersionMismatch,
			wantDetail: "not a symlink",
		},
		{
			name: "unix dangling symlink",
			goos: "linux",
			arrange: func(t *testing.T, root RuntimeRoot) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(root.PlaywrightCoreModule), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("missing-package", root.PlaywrightCoreModule); err != nil {
					t.Fatal(err)
				}
			},
			wantError:  ErrNotInstalled,
			wantDetail: "resolve playwright-core module",
		},
		{
			name: "unix symlink outside installed package",
			goos: "linux",
			arrange: func(t *testing.T, root RuntimeRoot) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(root.PlaywrightCoreModule), 0o700); err != nil {
					t.Fatal(err)
				}
				other := filepath.Join(t.TempDir(), "other-package")
				if err := os.MkdirAll(other, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, root.PlaywrightCoreModule); err != nil {
					t.Fatal(err)
				}
			},
			wantError:  ErrVersionMismatch,
			wantDetail: "points outside",
		},
		{
			name: "windows symlink instead of copied directory",
			goos: "windows",
			arrange: func(t *testing.T, root RuntimeRoot) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(root.PlaywrightCoreModule), 0o700); err != nil {
					t.Fatal(err)
				}
				target := t.TempDir()
				if err := os.Symlink(target, root.PlaywrightCoreModule); err != nil {
					t.Fatal(err)
				}
			},
			wantError:  ErrVersionMismatch,
			wantDetail: "not a copied directory",
		},
		{
			name: "windows installed package missing",
			goos: "windows",
			arrange: func(t *testing.T, root RuntimeRoot) {
				t.Helper()
				if err := os.MkdirAll(root.PlaywrightCoreModule, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantError:  ErrNotInstalled,
			wantDetail: "hash playwright-core package",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
			tc.arrange(t, root)
			err := verifyRuntimePlaywrightCoreModule(root, tc.goos)
			if !errors.Is(err, tc.wantError) || !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("verify error = %v; want %v containing %q", err, tc.wantError, tc.wantDetail)
			}
		})
	}
}

func TestTreeSHA256HandlesNonRegularEntriesAndMissingRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	sum, err := treeSHA256(root)
	if err != nil {
		t.Fatalf("hash tree containing symlink: %v", err)
	}
	if sum == "" {
		t.Fatal("hash tree containing symlink returned an empty checksum")
	}
	if _, err := treeSHA256(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing tree error = %v; want os.ErrNotExist", err)
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
	expected := runtimeBrowserZipManifestSHA256(t, zipData)
	restoreManifest := replaceManifestChecksum(t, expected)
	defer restoreManifest()
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		_, _ = w.Write(zipData)
	}))
	defer server.Close()
	runtimeAssetHTTPClient = server.Client()
	camoufoxReleaseAssetBaseURL = server.URL
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
	for _, tc := range []struct {
		goos, goarch, suffix string
	}{
		{"darwin", "arm64", "mac.arm64"},
		{"darwin", "amd64", "mac.x86_64"},
		{"linux", "amd64", "lin.x86_64"},
		{"linux", "arm64", "lin.arm64"},
	} {
		got, err := camoufoxReleaseAssetURL("v135.0.1-beta.24", tc.goos, tc.goarch)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.goos, tc.goarch, err)
		}
		wantSuffix := "/v135.0.1-beta.24/camoufox-135.0.1-beta.24-" + tc.suffix + ".zip"
		if !strings.HasSuffix(got, wantSuffix) {
			t.Fatalf("%s/%s asset URL = %q, want suffix %q", tc.goos, tc.goarch, got, wantSuffix)
		}
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
	err := installRuntimeCamoufoxBrowser(context.Background(), RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64"), InstallOptions{})
	if !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "fetch is disabled") {
		t.Fatalf("disabled fetch err = %v", err)
	}
}

func TestRuntimeBrowserDiscoveryAndZipRejections(t *testing.T) {
	root := t.TempDir()
	if _, err := discoverDirectBrowserDir(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing direct browser err = %v", err)
	}
	plainFile := filepath.Join(root, "plain")
	if err := os.WriteFile(plainFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverDirectBrowserDir(plainFile); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("plain non-executable err = %v", err)
	}
	exe := filepath.Join(root, "camoufox")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverDirectBrowserDir(exe); err != nil || got != root {
		t.Fatalf("executable direct browser = %q, %v", got, err)
	}
	nested := filepath.Join(t.TempDir(), "archive", "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "camoufox"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverDownloadedBrowserDir(filepath.Dir(nested)); err != nil || got != nested {
		t.Fatalf("downloaded browser = %q, %v", got, err)
	}
	empty := t.TempDir()
	if _, err := discoverDownloadedBrowserDir(empty); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty downloaded browser err = %v", err)
	}

	badZip := filepath.Join(t.TempDir(), "bad.zip")
	out, err := os.Create(badZip)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	if _, err := zw.Create("../escape"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unzipRuntimeAsset(badZip, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe zip err = %v", err)
	}
	if err := unzipRuntimeAsset(filepath.Join(t.TempDir(), "missing.zip"), t.TempDir()); err == nil || !strings.Contains(err.Error(), "open Camoufox browser archive") {
		t.Fatalf("missing zip err = %v", err)
	}
}

func TestDownloadRuntimeCamoufoxBrowserFailureBranches(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	origClient := runtimeAssetHTTPClient
	origBase := camoufoxReleaseAssetBaseURL
	defer func() {
		runtimeAssetHTTPClient = origClient
		camoufoxReleaseAssetBaseURL = origBase
	}()
	t.Setenv(EnvTrustUnverifiedCamoufoxPath, "1")

	for _, tc := range []struct {
		name string
		body []byte
		code int
		want string
	}{
		{name: "http status", code: http.StatusInternalServerError, want: "HTTP 500"},
		{name: "bad archive", code: http.StatusOK, body: []byte("not a zip"), want: "open Camoufox browser archive"},
		{name: "empty archive", code: http.StatusOK, body: emptyRuntimeZipFixture(t), want: "downloaded Camoufox browser asset is unusable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			runtimeAssetHTTPClient = server.Client()
			camoufoxReleaseAssetBaseURL = server.URL

			root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
			if err := os.MkdirAll(filepath.Dir(root.Root), 0o700); err != nil {
				t.Fatal(err)
			}
			err := downloadRuntimeCamoufoxBrowser(context.Background(), root, InstallOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("download err = %v, want %q", err, tc.want)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := downloadRuntimeCamoufoxBrowser(ctx, RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64"), InstallOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download err = %v", err)
	}
	if err := installRuntimePlaywrightDriver(ctx, RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64"), InstallOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled driver err = %v", err)
	}
}

func TestInstallRuntimePlaywrightDriverUsesPinnedOfficialSources(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture Node.js executable is a shell script")
	}
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	coreArchive := playwrightCoreArchiveFixture(t, map[string]string{
		"package/cli.js":                       "// fixture cli\n",
		"package/package.json":                 `{"name":"playwright-core","version":"` + RequiredPlaywright + `"}`,
		"package/lib/utilsBundleImpl/xdg-open": "#!/bin/sh\n",
	})
	nodeFilename := "node-v" + playwrightDriverNodeVersion + "-linux-x64.tar.gz"
	nodeArchive := playwrightNodeArchiveFixture(t, nodeFilename, "#!/bin/sh\nprintf 'Version "+RequiredPlaywright+"\\n'\n")
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		switch r.URL.Path {
		case "/playwright-core.tgz":
			_, _ = w.Write(coreArchive)
		case "/v" + playwrightDriverNodeVersion + "/" + nodeFilename:
			_, _ = w.Write(nodeArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	restoreSources := replacePinnedPlaywrightSources(t, server, coreArchive, nodeFilename, nodeArchive)
	defer restoreSources()

	cacheDir := t.TempDir()
	root := RuntimeAssetCacheRoot(cacheDir, "linux", "amd64")
	if err := EnsureManagedPlaywrightDriver(context.Background(), InstallOptions{VenvDir: cacheDir}); err != nil {
		t.Fatalf("install pinned Playwright driver: %v", err)
	}
	if err := verifyPinnedPlaywrightDriver(context.Background(), root.PlaywrightDriverDir); err != nil {
		t.Fatalf("verify installed Playwright driver: %v", err)
	}
	if st, err := os.Stat(filepath.Join(root.PlaywrightPackageDir, "lib", "utilsBundleImpl", "xdg-open")); err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("Playwright executable mode was not preserved: %v, %v", st, err)
	}
	if requests["/playwright-core.tgz"] != 1 || requests["/v"+playwrightDriverNodeVersion+"/"+nodeFilename] != 1 {
		t.Fatalf("source requests = %#v", requests)
	}
	manifest := NewRuntimeAssetManifest(root, "linux", "amd64")
	if manifest.Assets[0].Source != server.URL+"/v"+playwrightDriverNodeVersion+"/"+nodeFilename || manifest.Assets[2].Source != server.URL+"/playwright-core.tgz" {
		t.Fatalf("manifest sources = %#v", manifest.Assets)
	}
	if err := installRuntimePlaywrightDriver(context.Background(), root, InstallOptions{}); err != nil {
		t.Fatalf("reuse pinned Playwright driver: %v", err)
	}
	if requests["/playwright-core.tgz"] != 1 || requests["/v"+playwrightDriverNodeVersion+"/"+nodeFilename] != 1 {
		t.Fatalf("verified driver unexpectedly downloaded again: %#v", requests)
	}
	if err := installRuntimePlaywrightDriver(context.Background(), root, InstallOptions{ForceReinstall: true}); err != nil {
		t.Fatalf("replace pinned Playwright driver: %v", err)
	}
	if requests["/playwright-core.tgz"] != 2 || requests["/v"+playwrightDriverNodeVersion+"/"+nodeFilename] != 2 {
		t.Fatalf("forced reinstall source requests = %#v", requests)
	}
}

func TestPinnedPlaywrightDriverOfficialSources(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE_DRIVER") != "1" {
		t.Skip("set GOMOUFOX_LIVE_DRIVER=1 to test pinned official driver sources")
	}
	if _, ok := playwrightNodeAssets[runtime.GOOS+"/"+runtime.GOARCH]; !ok {
		t.Skipf("no pinned driver asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if fixtureDir := os.Getenv("GOMOUFOX_LIVE_DRIVER_FIXTURE_DIR"); fixtureDir != "" {
		asset := playwrightNodeAssets[runtime.GOOS+"/"+runtime.GOARCH]
		coreArchive, err := os.ReadFile(filepath.Join(fixtureDir, "playwright-core-1.57.0.tgz"))
		if err != nil {
			t.Fatal(err)
		}
		nodeArchive, err := os.ReadFile(filepath.Join(fixtureDir, asset.Filename))
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/"+asset.Filename) {
				_, _ = w.Write(nodeArchive)
				return
			}
			_, _ = w.Write(coreArchive)
		}))
		defer server.Close()
		origClient := runtimeAssetHTTPClient
		origCoreURL := playwrightCorePackageURL
		origNodeBase := playwrightNodeReleaseBaseURL
		defer func() {
			runtimeAssetHTTPClient = origClient
			playwrightCorePackageURL = origCoreURL
			playwrightNodeReleaseBaseURL = origNodeBase
		}()
		runtimeAssetHTTPClient = server.Client()
		playwrightCorePackageURL = server.URL + "/playwright-core-1.57.0.tgz"
		playwrightNodeReleaseBaseURL = server.URL
	}
	root := RuntimeAssetCacheRoot(t.TempDir(), runtime.GOOS, runtime.GOARCH)
	if err := installRuntimePlaywrightDriver(context.Background(), root, InstallOptions{ForceReinstall: true}); err != nil {
		t.Fatalf("install official pinned driver: %v", err)
	}
	if err := verifyPinnedPlaywrightDriver(context.Background(), root.PlaywrightDriverDir); err != nil {
		t.Fatalf("verify official pinned driver: %v", err)
	}
	pw, err := playwright.Run(&playwright.RunOptions{DriverDirectory: root.PlaywrightDriverDir})
	if err != nil {
		t.Fatalf("start playwright-go with assembled official driver: %v", err)
	}
	if err := pw.Stop(); err != nil {
		t.Fatalf("stop playwright-go with assembled official driver: %v", err)
	}
}

func TestInstallRuntimePlaywrightDriverFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture Node.js executable is a shell script")
	}
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	validCore := playwrightCoreArchiveFixture(t, map[string]string{
		"package/cli.js":       "// fixture cli\n",
		"package/package.json": `{"version":"` + RequiredPlaywright + `"}`,
	})
	nodeFilename := "node-v" + playwrightDriverNodeVersion + "-linux-x64.tar.gz"
	validNode := playwrightNodeArchiveFixture(t, nodeFilename, "#!/bin/sh\nprintf 'Version "+RequiredPlaywright+"\\n'\n")

	for _, tc := range []struct {
		name          string
		core          []byte
		node          []byte
		mutateDigests func()
		want          string
	}{
		{
			name: "core checksum",
			core: validCore,
			node: validNode,
			mutateDigests: func() {
				playwrightCorePackageSHA512 = strings.Repeat("0", sha512.Size*2)
			},
			want: "playwright-core 1.57.0 archive checksum mismatch",
		},
		{
			name: "node checksum",
			core: validCore,
			node: validNode,
			mutateDigests: func() {
				asset := playwrightNodeAssets["linux/amd64"]
				asset.SHA256 = strings.Repeat("0", sha256.Size*2)
				playwrightNodeAssets["linux/amd64"] = asset
			},
			want: "Node.js 24.11.1 archive checksum mismatch",
		},
		{
			name: "unsafe core member",
			core: playwrightCoreArchiveFixture(t, map[string]string{
				"package/cli.js":       "// fixture cli\n",
				"package/package.json": `{"version":"` + RequiredPlaywright + `"}`,
				"package/../../escape": "nope",
			}),
			node:          validNode,
			mutateDigests: func() {},
			want:          "unsafe runtime archive member",
		},
		{
			name: "reported version",
			core: playwrightCoreArchiveFixture(t, map[string]string{
				"package/cli.js":       "// fixture cli\n",
				"package/package.json": `{"version":"0.0.0"}`,
			}),
			node:          validNode,
			mutateDigests: func() {},
			want:          "installed playwright-core 0.0.0",
		},
		{
			name:          "invalid core archive",
			core:          []byte("not a gzip archive"),
			node:          validNode,
			mutateDigests: func() {},
			want:          "read playwright-core archive",
		},
		{
			name: "missing core file",
			core: playwrightCoreArchiveFixture(t, map[string]string{
				"package/cli.js": "// fixture cli\n",
			}),
			node:          validNode,
			mutateDigests: func() {},
			want:          "missing package/package.json",
		},
		{
			name: "invalid package metadata",
			core: playwrightCoreArchiveFixture(t, map[string]string{
				"package/cli.js":       "// fixture cli\n",
				"package/package.json": `{`,
			}),
			node:          validNode,
			mutateDigests: func() {},
			want:          "decode pinned playwright-core package",
		},
		{
			name:          "invalid node archive",
			core:          validCore,
			node:          []byte("not a gzip archive"),
			mutateDigests: func() {},
			want:          "read Playwright Node.js archive",
		},
		{
			name:          "missing node binary",
			core:          validCore,
			node:          playwrightNodeArchiveFixture(t, "other-node.tar.gz", "#!/bin/sh\nexit 0\n"),
			mutateDigests: func() {},
			want:          "archive missing " + strings.TrimSuffix(nodeFilename, ".tar.gz") + "/bin/node",
		},
		{
			name:          "driver command failure",
			core:          validCore,
			node:          playwrightNodeArchiveFixture(t, nodeFilename, "#!/bin/sh\nexit 1\n"),
			mutateDigests: func() {},
			want:          "run pinned Playwright CLI",
		},
		{
			name:          "unexpected cli version",
			core:          validCore,
			node:          playwrightNodeArchiveFixture(t, nodeFilename, "#!/bin/sh\nprintf 'Version 0.0.0\\n'\n"),
			mutateDigests: func() {},
			want:          "reported an unexpected version",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/playwright-core.tgz" {
					_, _ = w.Write(tc.core)
					return
				}
				_, _ = w.Write(tc.node)
			}))
			defer server.Close()
			restoreSources := replacePinnedPlaywrightSources(t, server, tc.core, nodeFilename, tc.node)
			defer restoreSources()
			tc.mutateDigests()

			root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
			if err := os.MkdirAll(root.PlaywrightDriverDir, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(root.PlaywrightDriverDir, "keep")
			if err := os.WriteFile(marker, []byte("previous"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := installRuntimePlaywrightDriver(context.Background(), root, InstallOptions{ForceReinstall: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install err = %v, want %q", err, tc.want)
			}
			if raw, err := os.ReadFile(marker); err != nil || string(raw) != "previous" {
				t.Fatalf("previous driver changed after failure: %q, %v", raw, err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(root.PlaywrightDriverDir), "escape")); !os.IsNotExist(err) {
				t.Fatalf("unsafe archive wrote outside staging: %v", err)
			}
		})
	}
}

func TestInstallRuntimePlaywrightDriverRejectsUnavailableSources(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	if err := installRuntimePlaywrightDriver(context.Background(), root, InstallOptions{SkipBinaryFetch: true}); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "fetch is disabled") {
		t.Fatalf("skip-fetch err = %v", err)
	}

	restorePlatform()
	restorePlatform = overrideSidecarPlatform(t, "freebsd", "arm64")
	if err := installRuntimePlaywrightDriver(context.Background(), RuntimeAssetCacheRoot(t.TempDir(), "freebsd", "arm64"), InstallOptions{}); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "no pinned Playwright Node.js asset") {
		t.Fatalf("unsupported-platform err = %v", err)
	}
	restorePlatform()
	restorePlatform = overrideSidecarPlatform(t, "linux", "amd64")

	coreArchive := playwrightCoreArchiveFixture(t, map[string]string{
		"package/cli.js":       "// fixture cli\n",
		"package/package.json": `{"version":"` + RequiredPlaywright + `"}`,
	})
	nodeFilename := "node-v" + playwrightDriverNodeVersion + "-linux-x64.tar.gz"
	nodeArchive := playwrightNodeArchiveFixture(t, nodeFilename, "#!/bin/sh\nprintf 'Version "+RequiredPlaywright+"\\n'\n")
	for _, failingPath := range []string{"/playwright-core.tgz", "/v" + playwrightDriverNodeVersion + "/" + nodeFilename} {
		t.Run(failingPath, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == failingPath {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				if r.URL.Path == "/playwright-core.tgz" {
					_, _ = w.Write(coreArchive)
					return
				}
				_, _ = w.Write(nodeArchive)
			}))
			defer server.Close()
			restoreSources := replacePinnedPlaywrightSources(t, server, coreArchive, nodeFilename, nodeArchive)
			defer restoreSources()
			err := installRuntimePlaywrightDriver(context.Background(), RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64"), InstallOptions{})
			if !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "HTTP 503") {
				t.Fatalf("source failure err = %v", err)
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/playwright-core.tgz" {
			_, _ = w.Write(coreArchive)
			return
		}
		_, _ = w.Write(nodeArchive)
	}))
	defer server.Close()
	restoreSources := replacePinnedPlaywrightSources(t, server, coreArchive, nodeFilename, nodeArchive)
	defer restoreSources()
	blockingFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	badRoot := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	badRoot.PlaywrightDriverDir = filepath.Join(blockingFile, "driver")
	if err := installRuntimePlaywrightDriver(context.Background(), badRoot, InstallOptions{}); err == nil {
		t.Fatal("driver install through non-directory parent succeeded")
	}
}

func TestEnsureManagedPlaywrightDriverDefaultCacheAndLockFailure(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	if err := EnsureManagedPlaywrightDriver(context.Background(), InstallOptions{SkipBinaryFetch: true}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("default-cache skip-fetch err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheHome, "gomoufox", "venv", ".gomoufox-install.lock")); err != nil {
		t.Fatalf("default-cache install lock missing: %v", err)
	}

	origTryLock := tryInstallFileLockForAcquire
	defer func() { tryInstallFileLockForAcquire = origTryLock }()
	tryInstallFileLockForAcquire = func(*os.File) error { return errors.New("lock failed") }
	if err := EnsureManagedPlaywrightDriver(context.Background(), InstallOptions{VenvDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "lock failed") {
		t.Fatalf("lock failure err = %v", err)
	}
	if got := playwrightNodeSource("unsupported", "platform"); got != "" {
		t.Fatalf("unsupported node source = %q", got)
	}
}

func TestDownloadRuntimeAssetBytesBoundsAndStatus(t *testing.T) {
	origClient := runtimeAssetHTTPClient
	defer func() {
		runtimeAssetHTTPClient = origClient
	}()
	for _, tc := range []struct {
		name   string
		body   string
		code   int
		max    int64
		stream bool
		want   string
	}{
		{name: "status", code: http.StatusNotFound, max: 8, want: "HTTP 404"},
		{name: "body cap", code: http.StatusOK, body: "123456789", max: 8, want: "exceeds 8-byte limit"},
		{name: "streamed body cap", code: http.StatusOK, body: "123456789", max: 8, stream: true, want: "exceeds 8-byte limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				if tc.stream {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			runtimeAssetHTTPClient = server.Client()
			if _, err := downloadRuntimeAssetBytes(context.Background(), server.URL, tc.max); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("download err = %v, want %q", err, tc.want)
			}
		})
	}

	if _, err := downloadRuntimeAssetBytes(context.Background(), "://invalid", 8); err == nil {
		t.Fatal("invalid asset URL succeeded")
	}
	runtimeAssetHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport failed")
	})}
	if _, err := downloadRuntimeAssetBytes(context.Background(), "https://example.invalid/asset", 8); err == nil || !strings.Contains(err.Error(), "transport failed") {
		t.Fatalf("transport err = %v", err)
	}
	runtimeAssetHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", ContentLength: -1, Body: failingReadCloser{}}, nil
	})}
	if _, err := downloadRuntimeAssetBytes(context.Background(), "https://example.invalid/asset", 8); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("body read err = %v", err)
	}
}

func TestPlaywrightArchiveExtractionRejectsUnsafeShapes(t *testing.T) {
	validCore := []tarFixtureEntry{
		{Name: "package/cli.js", Body: "// cli\n", Mode: 0o644, Typeflag: tar.TypeReg},
		{Name: "package/package.json", Body: `{"version":"` + RequiredPlaywright + `"}`, Mode: 0o644, Typeflag: tar.TypeReg},
	}
	withDirectory := append([]tarFixtureEntry{{Name: "package/lib", Mode: 0o755, Typeflag: tar.TypeDir}}, validCore...)
	if err := extractPlaywrightCoreArchive(tarArchiveFixture(t, withDirectory), t.TempDir()); err != nil {
		t.Fatalf("extract core archive with directory entry: %v", err)
	}
	tooMany := make([]tarFixtureEntry, 0, playwrightCoreMaxEntries+1)
	for i := 0; i <= playwrightCoreMaxEntries; i++ {
		tooMany = append(tooMany, tarFixtureEntry{Name: fmt.Sprintf("package/file-%d", i), Typeflag: tar.TypeReg})
	}
	for _, tc := range []struct {
		name    string
		archive []byte
		want    string
	}{
		{name: "invalid gzip", archive: []byte("invalid"), want: "read playwright-core archive"},
		{name: "invalid tar", archive: gzipFixture(t, []byte("invalid tar")), want: "read playwright-core archive"},
		{name: "symlink", archive: tarArchiveFixture(t, append(validCore, tarFixtureEntry{Name: "package/link", Typeflag: tar.TypeSymlink})), want: "unsupported playwright-core archive member"},
		{name: "duplicate", archive: tarArchiveFixture(t, append(validCore, validCore[0])), want: "file exists"},
		{name: "truncated file", archive: truncatedTarArchiveFixture(t, "package/cli.js", 16, "short"), want: "unexpected EOF"},
		{name: "too many entries", archive: tarArchiveFixture(t, tooMany), want: "too many entries"},
	} {
		t.Run("core "+tc.name, func(t *testing.T) {
			err := extractPlaywrightCoreArchive(tc.archive, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("extract err = %v, want %q", err, tc.want)
			}
		})
	}

	nodeFilename := "node-v" + playwrightDriverNodeVersion + "-linux-x64.tar.gz"
	nodePath := strings.TrimSuffix(nodeFilename, ".tar.gz") + "/bin/node"
	for _, tc := range []struct {
		name    string
		archive []byte
		want    string
	}{
		{name: "invalid gzip", archive: []byte("invalid"), want: "read Playwright Node.js archive"},
		{name: "invalid tar", archive: gzipFixture(t, []byte("invalid tar")), want: "read Playwright Node.js archive"},
		{name: "missing", archive: tarArchiveFixture(t, []tarFixtureEntry{{Name: "other/bin/node", Body: "node", Mode: 0o755, Typeflag: tar.TypeReg}}), want: "archive missing"},
		{name: "non-regular", archive: tarArchiveFixture(t, []tarFixtureEntry{{Name: nodePath, Typeflag: tar.TypeDir}}), want: "not a regular file"},
		{name: "empty", archive: tarArchiveFixture(t, []tarFixtureEntry{{Name: nodePath, Typeflag: tar.TypeReg}}), want: "invalid size"},
		{name: "truncated file", archive: truncatedTarArchiveFixture(t, nodePath, 16, "short"), want: "unexpected EOF"},
	} {
		t.Run("node "+tc.name, func(t *testing.T) {
			err := extractPlaywrightNodeArchive(tc.archive, nodeFilename, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("extract err = %v, want %q", err, tc.want)
			}
		})
	}

	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, nodeExecutableName()), []byte("existing"), 0o700); err != nil {
		t.Fatal(err)
	}
	archive := playwrightNodeArchiveFixture(t, nodeFilename, "#!/bin/sh\nexit 0\n")
	if err := extractPlaywrightNodeArchive(archive, nodeFilename, dst); err == nil || !strings.Contains(err.Error(), "file exists") {
		t.Fatalf("existing node err = %v", err)
	}
}

func TestVerifyPinnedPlaywrightDriverFailureBranches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture Node.js executable is a shell script")
	}
	for _, tc := range []struct {
		name        string
		node        string
		packageJSON string
		writeCLI    bool
		want        string
	}{
		{name: "missing package", node: "#!/bin/sh\nexit 0\n", want: "read pinned playwright-core package"},
		{name: "invalid package", node: "#!/bin/sh\nexit 0\n", packageJSON: `{`, writeCLI: true, want: "decode pinned playwright-core package"},
		{name: "missing cli", node: "#!/bin/sh\nexit 0\n", packageJSON: `{"version":"` + RequiredPlaywright + `"}`, want: "Playwright CLI is unavailable"},
		{name: "command failure", node: "#!/bin/sh\nexit 1\n", packageJSON: `{"version":"` + RequiredPlaywright + `"}`, writeCLI: true, want: "run pinned Playwright CLI"},
		{name: "unexpected version", node: "#!/bin/sh\nprintf 'Version 0.0.0\\n'\n", packageJSON: `{"version":"` + RequiredPlaywright + `"}`, writeCLI: true, want: "reported an unexpected version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			driverDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(driverDir, nodeExecutableName()), []byte(tc.node), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.packageJSON != "" {
				packageDir := filepath.Join(driverDir, "package")
				if err := os.MkdirAll(packageDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(tc.packageJSON), 0o600); err != nil {
					t.Fatal(err)
				}
				if tc.writeCLI {
					if err := os.WriteFile(filepath.Join(packageDir, "cli.js"), []byte("// cli\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := verifyPinnedPlaywrightDriver(context.Background(), driverDir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("verify err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReplaceRuntimeAssetRollsBackPreviousAsset(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "driver")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "keep")
	if err := os.WriteFile(marker, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceRuntimeAsset(filepath.Join(parent, "missing-source"), target); err == nil {
		t.Fatal("replacement with missing source succeeded")
	} else if !strings.Contains(err.Error(), target) {
		t.Fatalf("replacement error does not identify target %q: %v", target, err)
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "previous" {
		t.Fatalf("previous driver was not restored: %q, %v", raw, err)
	}
	if backups, err := filepath.Glob(filepath.Join(parent, ".runtime-asset-backup-*")); err != nil || len(backups) != 0 {
		t.Fatalf("rollback backups = %v, %v", backups, err)
	}

	blockingFile := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(blockingFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceRuntimeAsset(filepath.Join(parent, "source"), filepath.Join(blockingFile, "driver")); err == nil {
		t.Fatal("replacement through non-directory target succeeded")
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

func TestRuntimeAssetRootsAndManifestValidationBranches(t *testing.T) {
	defaultRoot := RuntimeAssetCacheRoot("", "", "")
	if defaultRoot.Root == "" || defaultRoot.NodeJS == "" || defaultRoot.BrowserExecutable == "" {
		t.Fatalf("default runtime root = %#v", defaultRoot)
	}
	windows := RuntimeAssetCacheRoot(t.TempDir(), "windows", "amd64")
	if filepath.Base(windows.NodeJS) != "node.exe" || filepath.Base(windows.BrowserExecutable) != "camoufox.exe" {
		t.Fatalf("Windows runtime root = %#v", windows)
	}
	manifest := NewRuntimeAssetManifest(defaultRoot, "", "")
	if len(manifest.Assets) != 4 || manifest.Assets[0].GOOS == "" || manifest.Assets[0].GOARCH == "" {
		t.Fatalf("default manifest = %#v", manifest)
	}

	valid := NewRuntimeAssetManifest(RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64"), "linux", "amd64")
	for _, tc := range []struct {
		name   string
		mutate func(*RuntimeAssetManifest)
	}{
		{name: "schema", mutate: func(m *RuntimeAssetManifest) { m.SchemaVersion++ }},
		{name: "runtime", mutate: func(m *RuntimeAssetManifest) { m.Runtime = "python" }},
		{name: "Camoufox", mutate: func(m *RuntimeAssetManifest) { m.CamoufoxVersion = "bad" }},
		{name: "Playwright", mutate: func(m *RuntimeAssetManifest) { m.PlaywrightVersion = "bad" }},
		{name: "missing platform assets", mutate: func(m *RuntimeAssetManifest) {
			for i := range m.Assets {
				m.Assets[i].GOOS = "darwin"
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := valid
			m.Assets = append([]RuntimeAssetRecord(nil), valid.Assets...)
			tc.mutate(&m)
			if err := ValidateRuntimeAssetManifest(m, "linux", "amd64"); err == nil {
				t.Fatalf("ValidateRuntimeAssetManifest accepted %s mismatch", tc.name)
			}
		})
	}
}

func TestPopulateRuntimeAssetManifestRejectsUnsafeMissingAndWrongKinds(t *testing.T) {
	root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	if err := os.MkdirAll(root.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(root.Root, "regular")
	if err := os.WriteFile(regular, []byte("data"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root.Root, "dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(root.Root, "linked-dir")
	if err := os.Symlink(dir, symlinkDir); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		asset RuntimeAssetRecord
	}{
		{name: "blank path", asset: RuntimeAssetRecord{Kind: RuntimeAssetNodeJS}},
		{name: "absolute path", asset: RuntimeAssetRecord{Kind: RuntimeAssetNodeJS, Path: regular}},
		{name: "parent traversal", asset: RuntimeAssetRecord{Kind: RuntimeAssetNodeJS, Path: "../escape"}},
		{name: "missing", asset: RuntimeAssetRecord{Kind: RuntimeAssetNodeJS, Path: "missing"}},
		{name: "directory kind is file", asset: RuntimeAssetRecord{Kind: RuntimeAssetCamoufoxBrowser, Path: "regular"}},
		{name: "tree hash rejects symlink", asset: RuntimeAssetRecord{Kind: RuntimeAssetCamoufoxBrowser, Path: "linked-dir"}},
		{name: "file kind is directory", asset: RuntimeAssetRecord{Kind: RuntimeAssetNodeJS, Path: "dir"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := RuntimeAssetManifest{Assets: []RuntimeAssetRecord{tc.asset}}
			if err := PopulateRuntimeAssetManifest(root, &m); err == nil {
				t.Fatalf("PopulateRuntimeAssetManifest accepted %#v", tc.asset)
			}
		})
	}
	m := RuntimeAssetManifest{Assets: []RuntimeAssetRecord{{Kind: RuntimeAssetNodeJS, Path: "regular"}}}
	if err := PopulateRuntimeAssetManifest(root, &m); err != nil {
		t.Fatal(err)
	}
	if m.Assets[0].GOOS == "" || m.Assets[0].GOARCH == "" || m.Assets[0].Size != 4 || m.Assets[0].SHA256 == "" {
		t.Fatalf("populated file record = %#v", m.Assets[0])
	}
}

func TestRuntimeManifestIOAndVerificationFailures(t *testing.T) {
	root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	if _, err := LoadRuntimeAssetManifest(filepath.Join(root.Root, "missing")); err == nil {
		t.Fatal("missing manifest loaded")
	}
	badJSON := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(badJSON, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeAssetManifest(badJSON); err == nil || !strings.Contains(err.Error(), "decode runtime asset manifest") {
		t.Fatalf("malformed manifest error = %v", err)
	}
	parentFile := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteRuntimeAssetManifest(filepath.Join(parentFile, "manifest.json"), RuntimeAssetManifest{}); err == nil {
		t.Fatal("manifest written below file parent")
	}

	if err := VerifyRuntimeLaunchServerFresh(root); err == nil || !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing launch server error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(root.LaunchServerJS), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.LaunchServerJS, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRuntimeLaunchServerFresh(root); err == nil || !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("stale launch server error = %v", err)
	}

	if _, _, err := ResolveRuntimeAssets(root.Root); err == nil || !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing resolved assets error = %v", err)
	}
}

func TestVerifyRuntimeAssetsRejectsPathTypeSizeChecksumAndModeCorruption(t *testing.T) {
	root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	if err := os.MkdirAll(root.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root.Root, "file")
	if err := os.WriteFile(file, []byte("data"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root.Root, "dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root.Root, "linked-dir")
	if err := os.Symlink(dir, linkedDir); err != nil {
		t.Fatal(err)
	}
	base := NewRuntimeAssetManifest(root, "linux", "amd64")
	for i := range base.Assets {
		base.Assets[i].Path = "file"
	}
	for _, tc := range []struct {
		name   string
		mutate func(*RuntimeAssetManifest)
	}{
		{name: "unsafe path", mutate: func(m *RuntimeAssetManifest) { m.Assets[0].Path = "../escape" }},
		{name: "missing", mutate: func(m *RuntimeAssetManifest) { m.Assets[0].Path = "missing" }},
		{name: "directory asset is file", mutate: func(m *RuntimeAssetManifest) { m.Assets[2].Path = "file" }},
		{name: "directory tree is symlink", mutate: func(m *RuntimeAssetManifest) { m.Assets[2].Path = "linked-dir"; m.Assets[2].SHA256 = "x" }},
		{name: "directory checksum", mutate: func(m *RuntimeAssetManifest) { m.Assets[2].Path = "dir"; m.Assets[2].SHA256 = "bad" }},
		{name: "file asset is directory", mutate: func(m *RuntimeAssetManifest) { m.Assets[0].Path = "dir" }},
		{name: "file size", mutate: func(m *RuntimeAssetManifest) { m.Assets[0].Size = 9 }},
		{name: "file checksum", mutate: func(m *RuntimeAssetManifest) { m.Assets[0].SHA256 = "bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			m.Assets = append([]RuntimeAssetRecord(nil), base.Assets...)
			tc.mutate(&m)
			if err := VerifyRuntimeAssets(root, m, "linux", "amd64"); err == nil {
				t.Fatalf("VerifyRuntimeAssets accepted %s corruption", tc.name)
			}
		})
	}
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	m := base
	m.Assets = append([]RuntimeAssetRecord(nil), base.Assets...)
	if err := VerifyRuntimeAssets(root, m, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("node mode error = %v", err)
	}
}

func TestEnsureRuntimeAssetsPropagatesInstallAndFilesystemFailures(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	oldDriver, oldBrowser := installPlaywrightDriverForRuntime, installCamoufoxBrowserForRuntime
	t.Cleanup(func() { installPlaywrightDriverForRuntime, installCamoufoxBrowserForRuntime = oldDriver, oldBrowser })
	wantErr := errors.New("injected")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EnsureRuntimeAssets(ctx, InstallOptions{VenvDir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled install error = %v", err)
	}
	cacheFile := filepath.Join(t.TempDir(), "cache-file")
	if err := os.WriteFile(cacheFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cacheFile}); err == nil {
		t.Fatal("runtime installed below file cache root")
	}

	installPlaywrightDriverForRuntime = func(context.Context, RuntimeRoot, InstallOptions) error { return wantErr }
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: t.TempDir(), ForceReinstall: true}); !errors.Is(err, wantErr) {
		t.Fatalf("driver error = %v", err)
	}
	installPlaywrightDriverForRuntime = func(_ context.Context, root RuntimeRoot, _ InstallOptions) error {
		if err := os.RemoveAll(filepath.Dir(root.LaunchServerJS)); err != nil {
			return err
		}
		return os.WriteFile(filepath.Dir(root.LaunchServerJS), []byte("block"), 0o600)
	}
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: t.TempDir(), ForceReinstall: true}); err == nil {
		t.Fatal("launch server write failure ignored")
	}
	installPlaywrightDriverForRuntime = func(_ context.Context, root RuntimeRoot, _ InstallOptions) error {
		if err := os.WriteFile(root.NodeJS, []byte("node"), 0o700); err != nil {
			return err
		}
		return os.RemoveAll(root.PlaywrightPackageDir)
	}
	installCamoufoxBrowserForRuntime = func(context.Context, RuntimeRoot, InstallOptions) error { return nil }
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: t.TempDir(), ForceReinstall: true}); err == nil {
		t.Fatal("missing playwright package accepted")
	}

	installPlaywrightDriverForRuntime = func(_ context.Context, root RuntimeRoot, _ InstallOptions) error {
		if err := os.MkdirAll(root.PlaywrightPackageDir, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(root.NodeJS, []byte("node"), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "package.json"), []byte(`{"version":"`+RequiredPlaywright+`"}`), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "cli.js"), []byte("cli"), 0o600)
	}
	installCamoufoxBrowserForRuntime = func(context.Context, RuntimeRoot, InstallOptions) error { return wantErr }
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: t.TempDir(), ForceReinstall: true}); !errors.Is(err, wantErr) {
		t.Fatalf("browser error = %v", err)
	}
}

type runtimeAssetFailingDirEntry struct {
	os.DirEntry
	err error
}

func (entry runtimeAssetFailingDirEntry) Info() (os.FileInfo, error) {
	return nil, entry.err
}

func TestRuntimeAssetFilesystemAdapterPropagatesManifestAndHashFailures(t *testing.T) {
	oldFiles := runtimeAssetFiles
	t.Cleanup(func() { runtimeAssetFiles = oldFiles })
	wantErr := errors.New("injected")
	root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	if err := os.MkdirAll(root.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root.Root, "file")
	if err := os.WriteFile(file, []byte("data"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root.Root, "dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	runtimeAssetFiles.open = func(string) (*os.File, error) { return nil, wantErr }
	m := RuntimeAssetManifest{Assets: []RuntimeAssetRecord{{Kind: RuntimeAssetNodeJS, Path: "file"}}}
	if err := PopulateRuntimeAssetManifest(root, &m); !errors.Is(err, wantErr) {
		t.Fatalf("populate hash error = %v", err)
	}
	runtimeAssetFiles = oldFiles
	runtimeAssetFiles.writeFile = func(string, []byte, os.FileMode) error { return wantErr }
	if err := WriteRuntimeAssetManifest(filepath.Join(root.Root, "manifest.json"), RuntimeAssetManifest{}); !errors.Is(err, wantErr) {
		t.Fatalf("manifest write error = %v", err)
	}
	runtimeAssetFiles = oldFiles
	runtimeAssetFiles.open = func(path string) (*os.File, error) {
		if path == file {
			return nil, wantErr
		}
		return os.Open(path)
	}
	manifest := NewRuntimeAssetManifest(root, "linux", "amd64")
	for i := range manifest.Assets {
		if manifest.Assets[i].Kind == RuntimeAssetPlaywrightPackage || manifest.Assets[i].Kind == RuntimeAssetCamoufoxBrowser {
			manifest.Assets[i].Path = "dir"
		} else {
			manifest.Assets[i].Path = "file"
		}
	}
	manifest.Assets[0].SHA256 = "check"
	if err := VerifyRuntimeAssets(root, manifest, "linux", "amd64"); !errors.Is(err, wantErr) {
		t.Fatalf("verify file hash error = %v", err)
	}
	runtimeAssetFiles = oldFiles
	manifest.Assets = append(manifest.Assets, RuntimeAssetRecord{Kind: "ignored", GOOS: "darwin", GOARCH: "arm64", Path: "missing"})
	manifest.Assets[0].SHA256 = ""
	if err := VerifyRuntimeAssets(root, manifest, "linux", "amd64"); err != nil {
		t.Fatalf("other-platform asset was not ignored: %v", err)
	}
	manifest.SchemaVersion++
	if err := VerifyRuntimeAssets(root, manifest, "linux", "amd64"); err == nil {
		t.Fatal("invalid manifest verified")
	}
}

func TestRuntimePlaywrightCoreModulePropagatesFilesystemFailures(t *testing.T) {
	oldFiles := runtimeAssetFiles
	t.Cleanup(func() { runtimeAssetFiles = oldFiles })
	wantErr := errors.New("injected")
	newRoot := func(t *testing.T) RuntimeRoot {
		t.Helper()
		root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
		if err := os.MkdirAll(root.PlaywrightPackageDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "package.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	for _, tc := range []struct {
		name   string
		goos   string
		mutate func(*runtimeAssetFileOperations)
	}{
		{name: "mkdir parent", goos: "linux", mutate: func(ops *runtimeAssetFileOperations) {
			ops.mkdirAll = func(string, os.FileMode) error { return wantErr }
		}},
		{name: "mkdir staging", goos: "linux", mutate: func(ops *runtimeAssetFileOperations) {
			ops.mkdirTemp = func(string, string) (string, error) { return "", wantErr }
		}},
		{name: "remove staging directory", goos: "linux", mutate: func(ops *runtimeAssetFileOperations) { ops.remove = func(string) error { return wantErr } }},
		{name: "relative target", goos: "linux", mutate: func(ops *runtimeAssetFileOperations) {
			ops.rel = func(string, string) (string, error) { return "", wantErr }
		}},
		{name: "create symlink", goos: "linux", mutate: func(ops *runtimeAssetFileOperations) { ops.symlink = func(string, string) error { return wantErr } }},
		{name: "copy Windows package", goos: "windows", mutate: func(ops *runtimeAssetFileOperations) {
			ops.walkDir = func(string, fs.WalkDirFunc) error { return wantErr }
		}},
		{name: "replace target", goos: "linux", mutate: func(ops *runtimeAssetFileOperations) {
			ops.lstat = func(string) (os.FileInfo, error) { return nil, wantErr }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeAssetFiles = oldFiles
			root := newRoot(t)
			tc.mutate(&runtimeAssetFiles)
			if err := ensureRuntimePlaywrightCoreModule(root, tc.goos); err == nil {
				t.Fatal("injected filesystem failure was ignored")
			}
		})
	}

	runtimeAssetFiles = oldFiles
	root := newRoot(t)
	if err := os.MkdirAll(root.PlaywrightCoreModule, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.PlaywrightCoreModule, "module.js"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeAssetFiles.open = func(path string) (*os.File, error) {
		if strings.Contains(path, "module.js") {
			return nil, wantErr
		}
		return os.Open(path)
	}
	if err := verifyRuntimePlaywrightCoreModule(root, "windows"); err == nil || !strings.Contains(err.Error(), "hash playwright-core module") {
		t.Fatalf("Windows module hash error = %v", err)
	}
}

func TestReplaceRuntimeAssetPropagatesEveryAtomicReplacementFailure(t *testing.T) {
	oldFiles := runtimeAssetFiles
	t.Cleanup(func() { runtimeAssetFiles = oldFiles })
	wantErr := errors.New("injected")
	newPaths := func(t *testing.T) (string, string) {
		t.Helper()
		root := t.TempDir()
		source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
		if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		return source, target
	}
	for _, tc := range []struct {
		name   string
		mutate func(*runtimeAssetFileOperations)
	}{
		{name: "backup temp", mutate: func(ops *runtimeAssetFileOperations) {
			ops.mkdirTemp = func(string, string) (string, error) { return "", wantErr }
		}},
		{name: "backup placeholder removal", mutate: func(ops *runtimeAssetFileOperations) { ops.remove = func(string) error { return wantErr } }},
		{name: "backup rename", mutate: func(ops *runtimeAssetFileOperations) { ops.rename = func(string, string) error { return wantErr } }},
		{name: "target lstat", mutate: func(ops *runtimeAssetFileOperations) {
			ops.lstat = func(string) (os.FileInfo, error) { return nil, wantErr }
		}},
		{name: "backup cleanup", mutate: func(ops *runtimeAssetFileOperations) {
			ops.removeAll = func(string) error { return wantErr }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeAssetFiles = oldFiles
			source, target := newPaths(t)
			tc.mutate(&runtimeAssetFiles)
			if err := replaceRuntimeAsset(source, target); err == nil {
				t.Fatal("injected replacement failure was ignored")
			}
		})
	}

	for _, restoreFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("install rename restore=%v", restoreFails), func(t *testing.T) {
			runtimeAssetFiles = oldFiles
			source, target := newPaths(t)
			renames := 0
			runtimeAssetFiles.rename = func(from, to string) error {
				renames++
				if renames == 1 {
					return os.Rename(from, to)
				}
				if renames == 2 || restoreFails {
					return wantErr
				}
				return os.Rename(from, to)
			}
			err := replaceRuntimeAsset(source, target)
			if err == nil || !strings.Contains(err.Error(), "replace runtime asset") {
				t.Fatalf("replacement error = %v", err)
			}
			if restoreFails && !strings.Contains(err.Error(), "restore previous asset") {
				t.Fatalf("restore error = %v", err)
			}
		})
	}
}

func TestRuntimeAssetArchiveWritersPropagateCreateCopyAndCloseFailures(t *testing.T) {
	oldFiles, oldCopy, oldCopyN, oldClose := runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCopyN, runtimeAssetCloseFile
	t.Cleanup(func() {
		runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCopyN, runtimeAssetCloseFile = oldFiles, oldCopy, oldCopyN, oldClose
	})
	wantErr := errors.New("injected")
	core := playwrightCoreArchiveFixture(t, map[string]string{
		"package/cli.js":       "cli",
		"package/package.json": "{}",
	})
	nodeName := "node-v1-linux-x64.tar.gz"
	node := playwrightNodeArchiveFixture(t, nodeName, "node")
	for _, tc := range []struct {
		name   string
		call   func(string) error
		mutate func()
	}{
		{name: "core create", call: func(dst string) error { return extractPlaywrightCoreArchive(core, dst) }, mutate: func() {
			runtimeAssetFiles.openFile = func(string, int, os.FileMode) (*os.File, error) { return nil, wantErr }
		}},
		{name: "core copy", call: func(dst string) error { return extractPlaywrightCoreArchive(core, dst) }, mutate: func() { runtimeAssetCopyN = func(io.Writer, io.Reader, int64) (int64, error) { return 0, wantErr } }},
		{name: "core close", call: func(dst string) error { return extractPlaywrightCoreArchive(core, dst) }, mutate: func() { runtimeAssetCloseFile = func(*os.File) error { return wantErr } }},
		{name: "node create", call: func(dst string) error { return extractPlaywrightNodeArchive(node, nodeName, dst) }, mutate: func() {
			runtimeAssetFiles.openFile = func(string, int, os.FileMode) (*os.File, error) { return nil, wantErr }
		}},
		{name: "node copy", call: func(dst string) error { return extractPlaywrightNodeArchive(node, nodeName, dst) }, mutate: func() { runtimeAssetCopyN = func(io.Writer, io.Reader, int64) (int64, error) { return 0, wantErr } }},
		{name: "node close", call: func(dst string) error { return extractPlaywrightNodeArchive(node, nodeName, dst) }, mutate: func() { runtimeAssetCloseFile = func(*os.File) error { return wantErr } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeAssetFiles, runtimeAssetCopyN, runtimeAssetCloseFile = oldFiles, oldCopyN, oldClose
			tc.mutate()
			if err := tc.call(t.TempDir()); !errors.Is(err, wantErr) {
				t.Fatalf("archive failure = %v", err)
			}
		})
	}
}

func TestCopyAndHashTreesPropagateFilesystemFailures(t *testing.T) {
	oldFiles, oldCopy, oldClose := runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile
	t.Cleanup(func() { runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile = oldFiles, oldCopy, oldClose })
	wantErr := errors.New("injected")
	src := t.TempDir()
	file := filepath.Join(src, "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read fixture directory: %v, %#v", err, entries)
	}
	fileEntry := entries[0]

	for _, tc := range []struct {
		name   string
		call   func() error
		mutate func()
	}{
		{name: "copy walk callback", call: func() error { return copyTree(src, t.TempDir()) }, mutate: func() {
			runtimeAssetFiles.walkDir = func(root string, fn fs.WalkDirFunc) error { return fn(root, nil, wantErr) }
		}},
		{name: "copy relative path", call: func() error { return copyTree(src, t.TempDir()) }, mutate: func() { runtimeAssetFiles.rel = func(string, string) (string, error) { return "", wantErr } }},
		{name: "copy entry info", call: func() error { return copyTree(src, t.TempDir()) }, mutate: func() {
			runtimeAssetFiles.walkDir = func(root string, fn fs.WalkDirFunc) error {
				return fn(file, runtimeAssetFailingDirEntry{DirEntry: fileEntry, err: wantErr}, nil)
			}
		}},
		{name: "copy source open", call: func() error { return copyTree(src, t.TempDir()) }, mutate: func() { runtimeAssetFiles.open = func(string) (*os.File, error) { return nil, wantErr } }},
		{name: "copy target open", call: func() error { return copyTree(src, t.TempDir()) }, mutate: func() {
			runtimeAssetFiles.openFile = func(string, int, os.FileMode) (*os.File, error) { return nil, wantErr }
		}},
		{name: "copy bytes", call: func() error { return copyTree(src, t.TempDir()) }, mutate: func() { runtimeAssetCopy = func(io.Writer, io.Reader) (int64, error) { return 0, wantErr } }},
		{name: "copy close", call: func() error { return copyTree(src, t.TempDir()) }, mutate: func() { runtimeAssetCloseFile = func(*os.File) error { return wantErr } }},
		{name: "hash root lstat", call: func() error { _, err := treeSHA256(src); return err }, mutate: func() { runtimeAssetFiles.lstat = func(string) (os.FileInfo, error) { return nil, wantErr } }},
		{name: "hash walk callback", call: func() error { _, err := treeSHA256(src); return err }, mutate: func() {
			runtimeAssetFiles.walkDir = func(root string, fn fs.WalkDirFunc) error { return fn(root, nil, wantErr) }
		}},
		{name: "hash relative path", call: func() error { _, err := treeSHA256(src); return err }, mutate: func() { runtimeAssetFiles.rel = func(string, string) (string, error) { return "", wantErr } }},
		{name: "hash entry info", call: func() error { _, err := treeSHA256(src); return err }, mutate: func() {
			runtimeAssetFiles.walkDir = func(root string, fn fs.WalkDirFunc) error {
				return fn(file, runtimeAssetFailingDirEntry{DirEntry: fileEntry, err: wantErr}, nil)
			}
		}},
		{name: "hash file open", call: func() error { _, err := treeSHA256(src); return err }, mutate: func() { runtimeAssetFiles.open = func(string) (*os.File, error) { return nil, wantErr } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile = oldFiles, oldCopy, oldClose
			tc.mutate()
			if err := tc.call(); !errors.Is(err, wantErr) {
				t.Fatalf("filesystem failure = %v", err)
			}
		})
	}

	runtimeAssetFiles = oldFiles
	link := filepath.Join(src, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, t.TempDir()); err != nil {
		t.Fatalf("copyTree ignored non-regular entry incorrectly: %v", err)
	}
	runtimeAssetFiles.abs = func(string) (string, error) { return "", wantErr }
	if sameCanonicalFileTree("a", "b") {
		t.Fatal("canonical comparison accepted unresolved paths")
	}
	runtimeAssetFiles = oldFiles
	runtimeAssetFiles.rel = func(string, string) (string, error) { return "", wantErr }
	if got := relPath("root", "target"); got != "target" {
		t.Fatalf("relPath fallback = %q", got)
	}
}

func TestResolveRuntimeAssetsReportsMissingReadyMarkerAndStaleLaunchServer(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	restoreInstallers := replaceRuntimeAssetInstallers(t)
	defer restoreInstallers()
	cache := t.TempDir()
	root, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cache, ForceReinstall: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root.ReadyMarkerPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveRuntimeAssets(cache); err == nil || !strings.Contains(err.Error(), "not marked ready") {
		t.Fatalf("missing ready marker error = %v", err)
	}
	if err := os.WriteFile(root.ReadyMarkerPath, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.LaunchServerJS, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadRuntimeAssetManifest(root.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := range manifest.Assets {
		if manifest.Assets[i].Kind == RuntimeAssetLaunchServerJS {
			manifest.Assets[i].Size = 0
			manifest.Assets[i].SHA256 = ""
		}
	}
	if err := WriteRuntimeAssetManifest(root.ManifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveRuntimeAssets(cache); err == nil || !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("stale launch server error = %v", err)
	}
	if err := os.WriteFile(root.LaunchServerJS, []byte(runtimeLaunchServerJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root.PlaywrightCoreModule); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveRuntimeAssets(cache); err == nil || !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing playwright-core module error = %v", err)
	}
}

func TestEnsureRuntimeAssetsPropagatesPostInstallValidationFailures(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	oldFiles := runtimeAssetFiles
	oldDriver, oldBrowser := installPlaywrightDriverForRuntime, installCamoufoxBrowserForRuntime
	t.Cleanup(func() {
		runtimeAssetFiles = oldFiles
		installPlaywrightDriverForRuntime, installCamoufoxBrowserForRuntime = oldDriver, oldBrowser
	})
	wantErr := errors.New("injected")

	for _, tc := range []struct {
		name   string
		mutate func(root RuntimeRoot)
	}{
		{name: "populate manifest", mutate: func(root RuntimeRoot) {
			runtimeAssetFiles.open = func(path string) (*os.File, error) {
				if path == root.NodeJS {
					return nil, wantErr
				}
				return os.Open(path)
			}
		}},
		{name: "write manifest", mutate: func(root RuntimeRoot) {
			runtimeAssetFiles.writeFile = func(path string, data []byte, mode os.FileMode) error {
				if path == root.ManifestPath {
					return wantErr
				}
				return os.WriteFile(path, data, mode)
			}
		}},
		{name: "verify assets", mutate: func(root RuntimeRoot) {
			calls := 0
			runtimeAssetFiles.stat = func(path string) (os.FileInfo, error) {
				if path == root.NodeJS {
					calls++
					if calls == 2 {
						return nil, wantErr
					}
				}
				return os.Stat(path)
			}
		}},
		{name: "verify core module", mutate: func(root RuntimeRoot) {
			calls := 0
			runtimeAssetFiles.lstat = func(path string) (os.FileInfo, error) {
				if path == root.PlaywrightCoreModule {
					calls++
					if calls == 4 {
						return nil, wantErr
					}
				}
				return os.Lstat(path)
			}
		}},
		{name: "write ready marker", mutate: func(root RuntimeRoot) {
			runtimeAssetFiles.writeFile = func(path string, data []byte, mode os.FileMode) error {
				if path == root.ReadyMarkerPath {
					return wantErr
				}
				return os.WriteFile(path, data, mode)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeAssetFiles = oldFiles
			restoreInstallers := replaceRuntimeAssetInstallers(t)
			defer restoreInstallers()
			cache := t.TempDir()
			root := RuntimeAssetCacheRoot(cache, "linux", "amd64")
			tc.mutate(root)
			if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cache, ForceReinstall: true}); err == nil {
				t.Fatal("post-install failure was ignored")
			}
		})
	}
}

func TestInstallRuntimePlaywrightDriverPropagatesStagingFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture Node.js executable is a shell script")
	}
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	core := playwrightCoreArchiveFixture(t, map[string]string{
		"package/cli.js":       "cli",
		"package/package.json": `{"version":"` + RequiredPlaywright + `"}`,
	})
	nodeName := "node-v" + playwrightDriverNodeVersion + "-linux-x64.tar.gz"
	node := playwrightNodeArchiveFixture(t, nodeName, "#!/bin/sh\nprintf 'Version "+RequiredPlaywright+"\\n'\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/playwright-core.tgz" {
			_, _ = w.Write(core)
			return
		}
		_, _ = w.Write(node)
	}))
	defer server.Close()
	restoreSources := replacePinnedPlaywrightSources(t, server, core, nodeName, node)
	defer restoreSources()
	oldFiles := runtimeAssetFiles
	runtimeAssetFiles.mkdirTemp = func(string, string) (string, error) { return "", errors.New("injected") }
	defer func() { runtimeAssetFiles = oldFiles }()
	if err := installRuntimePlaywrightDriver(context.Background(), RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64"), InstallOptions{ForceReinstall: true}); err == nil {
		t.Fatal("staging failure ignored")
	}
}

func TestPlaywrightCoreArchiveRejectsFilesystemAndOversizeFailures(t *testing.T) {
	oldFiles := runtimeAssetFiles
	t.Cleanup(func() { runtimeAssetFiles = oldFiles })
	wantErr := errors.New("injected")
	dirArchive := tarArchiveFixture(t, []tarFixtureEntry{{Name: "package/lib", Typeflag: tar.TypeDir}})
	fileArchive := playwrightCoreArchiveFixture(t, map[string]string{"package/cli.js": "cli", "package/package.json": "{}"})
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{name: "directory mkdir", raw: dirArchive},
		{name: "file parent mkdir", raw: fileArchive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeAssetFiles = oldFiles
			runtimeAssetFiles.mkdirAll = func(string, os.FileMode) error { return wantErr }
			if err := extractPlaywrightCoreArchive(tc.raw, t.TempDir()); !errors.Is(err, wantErr) {
				t.Fatalf("mkdir error = %v", err)
			}
		})
	}

	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "package/huge", Mode: 0o600, Size: playwrightCoreExtractedMaxBytes + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_ = tw.Flush()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	runtimeAssetFiles = oldFiles
	if err := extractPlaywrightCoreArchive(raw.Bytes(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "extracted-size limit") {
		t.Fatalf("oversize archive error = %v", err)
	}
}

func TestRuntimeCamoufoxInstallReuseCopyAndCancellationBranches(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := installRuntimeCamoufoxBrowser(ctx, RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64"), InstallOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled browser install error = %v", err)
	}

	t.Setenv(EnvTrustUnverifiedCamoufoxPath, "1")
	root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	if err := os.MkdirAll(root.BrowserResourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.BrowserExecutable, []byte("browser"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installRuntimeCamoufoxBrowser(context.Background(), root, InstallOptions{}); err != nil {
		t.Fatalf("reuse installed browser: %v", err)
	}
	if err := copyRuntimeCamoufoxBrowser(root.BrowserResourcesDir, root); err != nil {
		t.Fatalf("copy browser onto itself: %v", err)
	}
	oldFiles := runtimeAssetFiles
	runtimeAssetFiles.removeAll = func(string) error { return errors.New("injected") }
	if err := copyRuntimeCamoufoxBrowser(t.TempDir(), root); err == nil {
		t.Fatal("browser destination removal failure ignored")
	}
	runtimeAssetFiles = oldFiles
	missing := filepath.Join(t.TempDir(), "missing")
	if err := copyRuntimeCamoufoxBrowser(missing, root); err == nil {
		t.Fatal("missing browser source copied")
	}

	oldUserCache := userCacheDir
	defer func() { userCacheDir = oldUserCache }()
	cache := t.TempDir()
	userCacheDir = func() (string, error) { return cache, nil }
	candidate := filepath.Join(cache, "camoufox")
	if err := os.MkdirAll(candidate, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "camoufox"), []byte("cached browser"), 0o700); err != nil {
		t.Fatal(err)
	}
	sum, err := camoufoxBrowserManifestSHA256(candidate)
	if err != nil {
		t.Fatal(err)
	}
	restoreManifest := replaceManifestChecksum(t, sum)
	defer restoreManifest()
	cachedRoot := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	if err := installRuntimeCamoufoxBrowser(context.Background(), cachedRoot, InstallOptions{}); err != nil {
		t.Fatalf("reuse cached browser: %v", err)
	}
	if raw, err := os.ReadFile(cachedRoot.BrowserExecutable); err != nil || string(raw) != "cached browser" {
		t.Fatalf("cached browser copy = %q, %v", raw, err)
	}
}

func TestDownloadRuntimeCamoufoxBrowserPropagatesFilesystemFailures(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	oldFiles, oldCopy, oldClose := runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile
	oldClient, oldBase := runtimeAssetHTTPClient, camoufoxReleaseAssetBaseURL
	t.Cleanup(func() {
		runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile = oldFiles, oldCopy, oldClose
		runtimeAssetHTTPClient, camoufoxReleaseAssetBaseURL = oldClient, oldBase
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("archive")) }))
	defer server.Close()
	runtimeAssetHTTPClient, camoufoxReleaseAssetBaseURL = server.Client(), server.URL
	wantErr := errors.New("injected")
	for _, tc := range []struct {
		name   string
		mutate func()
	}{
		{name: "temporary directory", mutate: func() { runtimeAssetFiles.mkdirTemp = func(string, string) (string, error) { return "", wantErr } }},
		{name: "archive create", mutate: func() { runtimeAssetFiles.create = func(string) (*os.File, error) { return nil, wantErr } }},
		{name: "archive copy", mutate: func() { runtimeAssetCopy = func(io.Writer, io.Reader) (int64, error) { return 0, wantErr } }},
		{name: "archive close", mutate: func() { runtimeAssetCloseFile = func(*os.File) error { return wantErr } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile = oldFiles, oldCopy, oldClose
			tc.mutate()
			root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
			if err := os.MkdirAll(filepath.Dir(root.Root), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := downloadRuntimeCamoufoxBrowser(context.Background(), root, InstallOptions{}); !errors.Is(err, wantErr) {
				t.Fatalf("download filesystem error = %v", err)
			}
		})
	}

	restorePlatform()
	restorePlatform = overrideSidecarPlatform(t, "windows", "amd64")
	if err := downloadRuntimeCamoufoxBrowser(context.Background(), RuntimeAssetCacheRoot(t.TempDir(), "windows", "amd64"), InstallOptions{}); err == nil {
		t.Fatal("unsupported download platform accepted")
	}
}

func TestDownloadRuntimeCamoufoxBrowserRejectsManifestMismatch(t *testing.T) {
	restorePlatform := overrideSidecarPlatform(t, "linux", "amd64")
	defer restorePlatform()
	oldClient, oldBase := runtimeAssetHTTPClient, camoufoxReleaseAssetBaseURL
	defer func() { runtimeAssetHTTPClient, camoufoxReleaseAssetBaseURL = oldClient, oldBase }()
	zipData := runtimeBrowserZipFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(zipData) }))
	defer server.Close()
	runtimeAssetHTTPClient, camoufoxReleaseAssetBaseURL = server.Client(), server.URL
	restoreManifest := replaceManifestChecksum(t, strings.Repeat("0", sha256.Size*2))
	defer restoreManifest()
	root := RuntimeAssetCacheRoot(t.TempDir(), "linux", "amd64")
	if err := os.MkdirAll(filepath.Dir(root.Root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := downloadRuntimeCamoufoxBrowser(context.Background(), root, InstallOptions{}); err == nil || !strings.Contains(err.Error(), "manifest checksum mismatch") {
		t.Fatalf("manifest mismatch error = %v", err)
	}
}

func TestDownloadedBrowserDiscoveryPropagatesWalkErrors(t *testing.T) {
	oldFiles := runtimeAssetFiles
	defer func() { runtimeAssetFiles = oldFiles }()
	wantErr := errors.New("injected")
	root := t.TempDir()
	runtimeAssetFiles.walkDir = func(path string, fn fs.WalkDirFunc) error { return fn(path, nil, wantErr) }
	if _, err := discoverDownloadedBrowserDir(root); !errors.Is(err, wantErr) {
		t.Fatalf("walk callback error = %v", err)
	}
	runtimeAssetFiles.walkDir = func(string, fs.WalkDirFunc) error { return wantErr }
	if _, err := discoverDownloadedBrowserDir(root); !errors.Is(err, wantErr) {
		t.Fatalf("walk error = %v", err)
	}
}

func TestDownloadedBrowserDiscoveryCoversDirectAndSkippedDirectories(t *testing.T) {
	direct := t.TempDir()
	executable := filepath.Join(direct, "camoufox")
	if err := os.WriteFile(executable, []byte("browser"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverDownloadedBrowserDir(direct); err != nil || got != direct {
		t.Fatalf("direct downloaded browser = %q, %v", got, err)
	}
	empty := t.TempDir()
	if err := os.Mkdir(filepath.Join(empty, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverDownloadedBrowserDir(empty); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("nested empty discovery error = %v", err)
	}
}

func TestUnzipRuntimeAssetPropagatesFilesystemAndStreamFailures(t *testing.T) {
	oldFiles, oldCopy, oldClose := runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile
	t.Cleanup(func() { runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile = oldFiles, oldCopy, oldClose })
	wantErr := errors.New("injected")
	makeZip := func(t *testing.T, directory bool, method uint16, zeroMode bool) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "fixture.zip")
		out, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(out)
		header := &zip.FileHeader{Name: "nested/file"}
		if directory {
			header.Name = "nested/"
			header.SetMode(os.ModeDir | 0o700)
		} else {
			header.Method = zip.Store
			if zeroMode {
				header.SetMode(0)
			}
		}
		entry, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if !directory {
			_, _ = entry.Write([]byte("data"))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		if method != zip.Store {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for offset := 0; offset+12 <= len(raw); offset++ {
				switch string(raw[offset : offset+4]) {
				case "PK\x03\x04":
					binary.LittleEndian.PutUint16(raw[offset+8:offset+10], method)
				case "PK\x01\x02":
					binary.LittleEndian.PutUint16(raw[offset+10:offset+12], method)
				}
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	fileZip := makeZip(t, false, zip.Store, false)
	dirZip := makeZip(t, true, zip.Store, false)
	for _, tc := range []struct {
		name   string
		path   string
		mutate func()
	}{
		{name: "destination mkdir", path: fileZip, mutate: func() { runtimeAssetFiles.mkdirAll = func(string, os.FileMode) error { return wantErr } }},
		{name: "directory entry mkdir", path: dirZip, mutate: func() {
			calls := 0
			runtimeAssetFiles.mkdirAll = func(path string, mode os.FileMode) error {
				calls++
				if calls == 2 {
					return wantErr
				}
				return os.MkdirAll(path, mode)
			}
		}},
		{name: "file parent mkdir", path: fileZip, mutate: func() {
			calls := 0
			runtimeAssetFiles.mkdirAll = func(path string, mode os.FileMode) error {
				calls++
				if calls == 2 {
					return wantErr
				}
				return os.MkdirAll(path, mode)
			}
		}},
		{name: "destination open", path: fileZip, mutate: func() {
			runtimeAssetFiles.openFile = func(string, int, os.FileMode) (*os.File, error) { return nil, wantErr }
		}},
		{name: "copy", path: fileZip, mutate: func() { runtimeAssetCopy = func(io.Writer, io.Reader) (int64, error) { return 0, wantErr } }},
		{name: "close", path: fileZip, mutate: func() { runtimeAssetCloseFile = func(*os.File) error { return wantErr } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile = oldFiles, oldCopy, oldClose
			tc.mutate()
			if err := unzipRuntimeAsset(tc.path, filepath.Join(t.TempDir(), "out")); !errors.Is(err, wantErr) {
				t.Fatalf("unzip error = %v", err)
			}
		})
	}

	unsupported := makeZip(t, false, 99, false)
	runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile = oldFiles, oldCopy, oldClose
	if err := unzipRuntimeAsset(unsupported, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("unsupported ZIP compression method opened")
	}
	runtimeAssetFiles, runtimeAssetCopy, runtimeAssetCloseFile = oldFiles, oldCopy, oldClose
	if err := unzipRuntimeAsset(dirZip, filepath.Join(t.TempDir(), "dir-out")); err != nil {
		t.Fatalf("extract directory entry: %v", err)
	}
	if err := unzipRuntimeAsset(fileZip, filepath.Join(t.TempDir(), "file-out")); err != nil {
		t.Fatalf("extract default-mode file: %v", err)
	}
	zeroMode := makeZip(t, false, zip.Store, true)
	if err := unzipRuntimeAsset(zeroMode, filepath.Join(t.TempDir(), "zero-mode-out")); err != nil {
		t.Fatalf("extract zero-mode file: %v", err)
	}
}

func TestFileSHA256ReportsReadFailure(t *testing.T) {
	if _, err := fileSHA256(t.TempDir()); err == nil {
		t.Fatal("directory hashed as a regular file")
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
		if err := os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "package.json"), []byte(`{"name":"playwright-core","version":"`+RequiredPlaywrightJSON+`","main":"index.js"}`), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "index.js"), []byte("module.exports = {};\n"), 0o600)
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (failingReadCloser) Close() error { return nil }

type tarFixtureEntry struct {
	Name     string
	Body     string
	Mode     int64
	Typeflag byte
}

func gzipFixture(t *testing.T, body []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	if _, err := gz.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func tarArchiveFixture(t *testing.T, entries []tarFixtureEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		mode := entry.Mode
		if mode == 0 {
			mode = 0o644
		}
		typeflag := entry.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := int64(0)
		if typeflag == tar.TypeReg || typeflag == tar.TypeRegA {
			size = int64(len(entry.Body))
		}
		header := &tar.Header{Name: entry.Name, Mode: mode, Size: size, Typeflag: typeflag}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := tw.Write([]byte(entry.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func truncatedTarArchiveFixture(t *testing.T, name string, declaredSize int64, body string) []byte {
	t.Helper()
	var tarRaw bytes.Buffer
	tw := tar.NewWriter(&tarRaw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: declaredSize, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	// Deliberately do not close the tar writer: closing would pad the declared
	// body, while this fixture needs the extractor to observe a truncated file.
	return gzipFixture(t, tarRaw.Bytes())
}

func replacePinnedPlaywrightSources(t *testing.T, server *httptest.Server, core []byte, nodeFilename string, node []byte) func() {
	t.Helper()
	origClient := runtimeAssetHTTPClient
	origCoreURL := playwrightCorePackageURL
	origCoreHash := playwrightCorePackageSHA512
	origNodeBase := playwrightNodeReleaseBaseURL
	origNodeAssets := playwrightNodeAssets
	coreSum := sha512.Sum512(core)
	nodeSum := sha256.Sum256(node)
	runtimeAssetHTTPClient = server.Client()
	playwrightCorePackageURL = server.URL + "/playwright-core.tgz"
	playwrightCorePackageSHA512 = fmt.Sprintf("%x", coreSum)
	playwrightNodeReleaseBaseURL = server.URL
	playwrightNodeAssets = map[string]playwrightNodeAsset{
		"linux/amd64": {Filename: nodeFilename, SHA256: fmt.Sprintf("%x", nodeSum)},
	}
	return func() {
		runtimeAssetHTTPClient = origClient
		playwrightCorePackageURL = origCoreURL
		playwrightCorePackageSHA512 = origCoreHash
		playwrightNodeReleaseBaseURL = origNodeBase
		playwrightNodeAssets = origNodeAssets
	}
}

func playwrightCoreArchiveFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		mode := int64(0o644)
		if strings.HasSuffix(name, "/xdg-open") {
			mode = 0o755
		}
		header := &tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func playwrightNodeArchiveFixture(t *testing.T, filename, executable string) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	root := strings.TrimSuffix(filename, ".tar.gz")
	header := &tar.Header{Name: root + "/bin/node", Mode: 0o755, Size: int64(len(executable)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(executable)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
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

func runtimeBrowserZipManifestSHA256(t *testing.T, data []byte) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "browser.zip")
	if err := os.WriteFile(archive, data, 0o600); err != nil {
		t.Fatal(err)
	}
	extract := t.TempDir()
	if err := unzipRuntimeAsset(archive, extract); err != nil {
		t.Fatal(err)
	}
	source, err := discoverDownloadedBrowserDir(extract)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := camoufoxBrowserManifestSHA256(source)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func emptyRuntimeZipFixture(t *testing.T) []byte {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "empty.zip")
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
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
