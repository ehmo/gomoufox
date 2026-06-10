package sidecar

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
)

const (
	runtimeAssetManifestSchemaVersion = 1
	runtimeAssetReadyMarker           = ".gomoufox-runtime-ready"
)

var (
	installPlaywrightDriverForRuntime = installRuntimePlaywrightDriver
	installCamoufoxBrowserForRuntime  = installRuntimeCamoufoxBrowser
	runtimeAssetHTTPClient            = http.DefaultClient
	camoufoxReleaseAssetBaseURL       = "https://github.com/daijro/camoufox/releases/download"
)

type RuntimeAssetKind string

const (
	RuntimeAssetNodeJS            RuntimeAssetKind = "nodejs"
	RuntimeAssetLaunchServerJS    RuntimeAssetKind = "launch-server-js"
	RuntimeAssetPlaywrightPackage RuntimeAssetKind = "playwright-package"
	RuntimeAssetCamoufoxBrowser   RuntimeAssetKind = "camoufox-browser"
)

type RuntimeAssetManifest struct {
	SchemaVersion     int                  `json:"schema_version"`
	Runtime           string               `json:"runtime"`
	CamoufoxVersion   string               `json:"camoufox_version"`
	PlaywrightVersion string               `json:"playwright_version"`
	GeneratedAt       string               `json:"generated_at,omitempty"`
	Assets            []RuntimeAssetRecord `json:"assets"`
}

type RuntimeAssetRecord struct {
	Name    string           `json:"name"`
	Kind    RuntimeAssetKind `json:"kind"`
	GOOS    string           `json:"goos"`
	GOARCH  string           `json:"goarch"`
	Path    string           `json:"path"`
	SHA256  string           `json:"sha256"`
	Size    int64            `json:"size,omitempty"`
	Source  string           `json:"source,omitempty"`
	License string           `json:"license,omitempty"`
}

type RuntimeRoot struct {
	Root                 string
	ManifestPath         string
	PlaywrightDriverDir  string
	NodeJS               string
	LaunchServerJS       string
	PlaywrightPackageDir string
	BrowserExecutable    string
	BrowserResourcesDir  string
	ReadyMarkerPath      string
}

func RuntimeAssetCacheRoot(cacheDir, goos, goarch string) RuntimeRoot {
	if cacheDir == "" {
		cacheDir = DefaultCacheDir()
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	root := filepath.Join(cacheDir, "runtime", "v1", CamoufoxBinaryVersion, goos+"-"+goarch)
	node := "node"
	if goos == "windows" {
		node = "node.exe"
	}
	browser := "camoufox"
	if goos == "windows" {
		browser = "camoufox.exe"
	}
	return RuntimeRoot{
		Root:                 root,
		ManifestPath:         filepath.Join(root, "manifest.json"),
		PlaywrightDriverDir:  filepath.Join(root, "playwright"),
		NodeJS:               filepath.Join(root, "playwright", node),
		LaunchServerJS:       filepath.Join(root, "camoufox", "launchServer.js"),
		PlaywrightPackageDir: filepath.Join(root, "playwright", "package"),
		BrowserExecutable:    filepath.Join(root, "camoufox", "browser", browser),
		BrowserResourcesDir:  filepath.Join(root, "camoufox", "browser"),
		ReadyMarkerPath:      filepath.Join(root, runtimeAssetReadyMarker),
	}
}

func NewRuntimeAssetManifest(root RuntimeRoot, goos, goarch string) RuntimeAssetManifest {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return RuntimeAssetManifest{
		SchemaVersion:     runtimeAssetManifestSchemaVersion,
		Runtime:           RuntimeNodeDirect,
		CamoufoxVersion:   CamoufoxBinaryVersion,
		PlaywrightVersion: RequiredPlaywrightJSON,
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
		Assets: []RuntimeAssetRecord{
			{Name: "node", Kind: RuntimeAssetNodeJS, GOOS: goos, GOARCH: goarch, Path: relPath(root.Root, root.NodeJS), Source: "gomoufox-release-asset://node", License: "Node.js"},
			{Name: "launch-server", Kind: RuntimeAssetLaunchServerJS, GOOS: goos, GOARCH: goarch, Path: relPath(root.Root, root.LaunchServerJS), Source: "gomoufox-release-asset://launch-server", License: "Apache-2.0"},
			{Name: "playwright-package", Kind: RuntimeAssetPlaywrightPackage, GOOS: goos, GOARCH: goarch, Path: relPath(root.Root, root.PlaywrightPackageDir), Source: "gomoufox-release-asset://playwright-package", License: "Apache-2.0"},
			{Name: "camoufox-browser", Kind: RuntimeAssetCamoufoxBrowser, GOOS: goos, GOARCH: goarch, Path: relPath(root.Root, root.BrowserResourcesDir), Source: "gomoufox-release-asset://camoufox-browser", License: "MPL-2.0"},
		},
	}
}

func PopulateRuntimeAssetManifest(root RuntimeRoot, m *RuntimeAssetManifest) error {
	for i := range m.Assets {
		asset := &m.Assets[i]
		if asset.GOOS == "" {
			asset.GOOS = sidecarGOOS
		}
		if asset.GOARCH == "" {
			asset.GOARCH = sidecarGOARCH
		}
		if strings.TrimSpace(asset.Path) == "" || filepath.IsAbs(asset.Path) || strings.Contains(asset.Path, "..") {
			return fmt.Errorf("%w: unsafe runtime asset path %q", ErrVersionMismatch, asset.Path)
		}
		path := filepath.Join(root.Root, asset.Path)
		st, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%w: stat runtime asset %s: %v", ErrNotInstalled, asset.Kind, err)
		}
		if asset.Kind == RuntimeAssetPlaywrightPackage || asset.Kind == RuntimeAssetCamoufoxBrowser {
			if !st.IsDir() {
				return fmt.Errorf("%w: runtime asset %s is not a directory", ErrVersionMismatch, asset.Kind)
			}
			sum, err := treeSHA256(path)
			if err != nil {
				return err
			}
			asset.Size = 0
			asset.SHA256 = sum
			continue
		}
		if st.IsDir() {
			return fmt.Errorf("%w: runtime asset %s is a directory", ErrVersionMismatch, asset.Kind)
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return err
		}
		asset.Size = st.Size()
		asset.SHA256 = sum
	}
	return nil
}

func ValidateRuntimeAssetManifest(m RuntimeAssetManifest, goos, goarch string) error {
	if m.SchemaVersion != runtimeAssetManifestSchemaVersion {
		return fmt.Errorf("%w: runtime asset manifest schema %d", ErrVersionMismatch, m.SchemaVersion)
	}
	if m.Runtime != RuntimeNodeDirect {
		return fmt.Errorf("%w: runtime asset manifest runtime %q", ErrVersionMismatch, m.Runtime)
	}
	if m.CamoufoxVersion != CamoufoxBinaryVersion {
		return fmt.Errorf("%w: runtime asset Camoufox %s", ErrVersionMismatch, m.CamoufoxVersion)
	}
	if m.PlaywrightVersion != RequiredPlaywrightJSON {
		return fmt.Errorf("%w: runtime asset Playwright %s", ErrVersionMismatch, m.PlaywrightVersion)
	}
	required := map[RuntimeAssetKind]bool{
		RuntimeAssetNodeJS:            false,
		RuntimeAssetLaunchServerJS:    false,
		RuntimeAssetPlaywrightPackage: false,
		RuntimeAssetCamoufoxBrowser:   false,
	}
	for _, a := range m.Assets {
		if a.GOOS != goos || a.GOARCH != goarch {
			continue
		}
		if _, ok := required[a.Kind]; ok {
			required[a.Kind] = true
		}
	}
	for kind, ok := range required {
		if !ok {
			return fmt.Errorf("%w: runtime asset manifest missing %s for %s/%s", ErrNotInstalled, kind, goos, goarch)
		}
	}
	return nil
}

func LoadRuntimeAssetManifest(path string) (RuntimeAssetManifest, error) {
	var m RuntimeAssetManifest
	raw, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("decode runtime asset manifest: %w", err)
	}
	return m, nil
}

func WriteRuntimeAssetManifest(path string, m RuntimeAssetManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}

func ResolveRuntimeAssets(cacheDir string) (RuntimeRoot, RuntimeAssetManifest, error) {
	root := RuntimeAssetCacheRoot(cacheDir, sidecarGOOS, sidecarGOARCH)
	m, err := LoadRuntimeAssetManifest(root.ManifestPath)
	if err != nil {
		return RuntimeRoot{}, RuntimeAssetManifest{}, fmt.Errorf("%w: load node-direct runtime manifest: %v", ErrNotInstalled, err)
	}
	if _, err := os.Stat(root.ReadyMarkerPath); err != nil {
		return RuntimeRoot{}, RuntimeAssetManifest{}, fmt.Errorf("%w: node-direct runtime is not marked ready: %v", ErrNotInstalled, err)
	}
	if err := VerifyRuntimeAssets(root, m, sidecarGOOS, sidecarGOARCH); err != nil {
		return RuntimeRoot{}, RuntimeAssetManifest{}, err
	}
	if err := VerifyRuntimeLaunchServerFresh(root); err != nil {
		return RuntimeRoot{}, RuntimeAssetManifest{}, err
	}
	return root, m, nil
}

func VerifyRuntimeLaunchServerFresh(root RuntimeRoot) error {
	raw, err := os.ReadFile(root.LaunchServerJS)
	if err != nil {
		return fmt.Errorf("%w: read runtime launch server: %v", ErrNotInstalled, err)
	}
	if string(raw) != runtimeLaunchServerJS {
		return fmt.Errorf("%w: runtime launch server is stale", ErrVersionMismatch)
	}
	return nil
}

func VerifyRuntimeAssets(root RuntimeRoot, m RuntimeAssetManifest, goos, goarch string) error {
	if err := ValidateRuntimeAssetManifest(m, goos, goarch); err != nil {
		return err
	}
	for _, a := range m.Assets {
		if a.GOOS != goos || a.GOARCH != goarch {
			continue
		}
		if strings.TrimSpace(a.Path) == "" || filepath.IsAbs(a.Path) || strings.Contains(a.Path, "..") {
			return fmt.Errorf("%w: unsafe runtime asset path %q", ErrVersionMismatch, a.Path)
		}
		path := filepath.Join(root.Root, a.Path)
		st, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%w: missing runtime asset %s at %s", ErrNotInstalled, a.Kind, path)
		}
		if a.Kind == RuntimeAssetPlaywrightPackage || a.Kind == RuntimeAssetCamoufoxBrowser {
			if !st.IsDir() {
				return fmt.Errorf("%w: runtime asset %s is not a directory", ErrVersionMismatch, a.Kind)
			}
			if a.SHA256 != "" {
				got, err := treeSHA256(path)
				if err != nil {
					return err
				}
				if !strings.EqualFold(got, a.SHA256) {
					return fmt.Errorf("%w: runtime asset %s checksum mismatch", ErrVersionMismatch, a.Kind)
				}
			}
			continue
		}
		if st.IsDir() {
			return fmt.Errorf("%w: runtime asset %s is a directory", ErrVersionMismatch, a.Kind)
		}
		if a.Size > 0 && st.Size() != a.Size {
			return fmt.Errorf("%w: runtime asset %s size %d != %d", ErrVersionMismatch, a.Kind, st.Size(), a.Size)
		}
		if a.SHA256 != "" {
			got, err := fileSHA256(path)
			if err != nil {
				return err
			}
			if !strings.EqualFold(got, a.SHA256) {
				return fmt.Errorf("%w: runtime asset %s checksum mismatch", ErrVersionMismatch, a.Kind)
			}
		}
		if a.Kind == RuntimeAssetNodeJS && st.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("%w: runtime asset node is not executable", ErrVersionMismatch)
		}
	}
	return nil
}

func EnsureRuntimeAssets(ctx context.Context, opts InstallOptions) (RuntimeRoot, error) {
	select {
	case <-ctx.Done():
		return RuntimeRoot{}, ctx.Err()
	default:
	}
	if !runtimeAssetPlatformSupported(sidecarGOOS, sidecarGOARCH) {
		return RuntimeRoot{}, fmt.Errorf("%w: runtime assets unsupported for %s/%s", ErrNotInstalled, sidecarGOOS, sidecarGOARCH)
	}
	if !opts.ForceReinstall {
		if root, _, err := ResolveRuntimeAssets(opts.VenvDir); err == nil {
			return root, nil
		}
	}
	root := RuntimeAssetCacheRoot(opts.VenvDir, sidecarGOOS, sidecarGOARCH)
	for _, dir := range []string{
		root.Root,
		filepath.Dir(root.NodeJS),
		filepath.Dir(root.LaunchServerJS),
		root.PlaywrightPackageDir,
		root.BrowserResourcesDir,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return RuntimeRoot{}, err
		}
	}
	if err := installPlaywrightDriverForRuntime(ctx, root, opts); err != nil {
		return RuntimeRoot{}, err
	}
	if err := writeRuntimeLaunchServer(root); err != nil {
		return RuntimeRoot{}, err
	}
	if err := installCamoufoxBrowserForRuntime(ctx, root, opts); err != nil {
		return RuntimeRoot{}, err
	}
	m := NewRuntimeAssetManifest(root, sidecarGOOS, sidecarGOARCH)
	if err := PopulateRuntimeAssetManifest(root, &m); err != nil {
		return RuntimeRoot{}, err
	}
	if err := WriteRuntimeAssetManifest(root.ManifestPath, m); err != nil {
		return RuntimeRoot{}, err
	}
	if err := ValidateRuntimeAssetManifest(m, sidecarGOOS, sidecarGOARCH); err != nil {
		return RuntimeRoot{}, err
	}
	if err := VerifyRuntimeAssets(root, m, sidecarGOOS, sidecarGOARCH); err != nil {
		return RuntimeRoot{}, err
	}
	if err := os.WriteFile(root.ReadyMarkerPath, []byte("manifest-ready\n"), 0o600); err != nil {
		return RuntimeRoot{}, err
	}
	return root, nil
}

func installRuntimePlaywrightDriver(ctx context.Context, root RuntimeRoot, opts InstallOptions) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	driver, err := playwright.NewDriver(&playwright.RunOptions{
		DriverDirectory:     root.PlaywrightDriverDir,
		SkipInstallBrowsers: true,
		Verbose:             opts.Verbose,
		Stdout:              installDiagnosticWriter,
		Stderr:              installDiagnosticWriter,
	})
	if err != nil {
		return err
	}
	return driver.Install()
}

func installRuntimeCamoufoxBrowser(ctx context.Context, root RuntimeRoot, opts InstallOptions) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	source := firstNonEmpty(opts.CamoufoxPath, os.Getenv(EnvCamoufoxPath))
	if source == "" {
		for _, candidateRoot := range camoufoxBrowserCacheRoots() {
			if candidate, err := discoverUsableBrowserDir(candidateRoot); err == nil {
				source = candidate
				break
			}
		}
	}
	if source == "" {
		return downloadRuntimeCamoufoxBrowser(ctx, root, opts)
	}
	if err := validateCamoufoxBrowserDir(source); err != nil {
		return fmt.Errorf("%w: invalid Camoufox browser asset source %s: %v", ErrNotInstalled, source, err)
	}
	if !trustUnverifiedCamoufoxPath() {
		if err := verifyCamoufoxManifest(source); err != nil {
			return err
		}
	}
	if sameFileTree(source, root.BrowserResourcesDir) {
		return nil
	}
	if err := os.RemoveAll(root.BrowserResourcesDir); err != nil {
		return err
	}
	return copyTree(source, root.BrowserResourcesDir)
}

func writeRuntimeLaunchServer(root RuntimeRoot) error {
	if err := os.MkdirAll(filepath.Dir(root.LaunchServerJS), 0o700); err != nil {
		return err
	}
	return os.WriteFile(root.LaunchServerJS, []byte(runtimeLaunchServerJS), 0o600)
}

const runtimeLaunchServerJS = `"use strict";

const { firefox } = require("playwright-core");

async function main() {
  let input = "";
  process.stdin.setEncoding("utf8");
  for await (const chunk of process.stdin) input += chunk;
  const encoded = input.trim();
  const payload = encoded ? JSON.parse(Buffer.from(encoded, "base64").toString("utf8")) : {};
  const browser = await firefox.launchServer(payload);
  console.log(browser.wsEndpoint());
  const close = async () => {
    try {
      await browser.close();
    } finally {
      process.exit(0);
    }
  };
  process.on("SIGTERM", close);
  process.on("SIGINT", close);
}

main().catch((err) => {
  console.error(err && err.stack ? err.stack : String(err));
  process.exit(1);
});
`

func downloadRuntimeCamoufoxBrowser(ctx context.Context, root RuntimeRoot, opts InstallOptions) error {
	assetURL, err := camoufoxReleaseAssetURL(CamoufoxBinaryVersion, sidecarGOOS, sidecarGOARCH)
	if err != nil {
		return err
	}
	if opts.SkipBinaryFetch || os.Getenv("GOMOUFOX_SKIP_FETCH") != "" {
		return fmt.Errorf("%w: no Go-managed Camoufox browser asset source found and fetch is disabled; set %s to a verified Camoufox browser directory", ErrNotInstalled, EnvCamoufoxPath)
	}
	binarySizeWarningOnce.Do(func() {
		if binarySizeWarningWriter != nil {
			_, _ = fmt.Fprintln(binarySizeWarningWriter, "gomoufox: Camoufox browser download is approximately 300-660 MB")
		}
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	resp, err := runtimeAssetHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("download Camoufox browser asset: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("download Camoufox browser asset: HTTP %s", resp.Status)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(root.Root), "camoufox-browser-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	archivePath := filepath.Join(tmp, "camoufox.zip")
	out, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	extractDir := filepath.Join(tmp, "extract")
	if err := unzipRuntimeAsset(archivePath, extractDir); err != nil {
		return err
	}
	source, err := discoverDownloadedBrowserDir(extractDir)
	if err != nil {
		return fmt.Errorf("%w: downloaded Camoufox browser asset is unusable: %v", ErrNotInstalled, err)
	}
	if !trustUnverifiedCamoufoxPath() {
		if err := verifyCamoufoxManifest(source); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(root.BrowserResourcesDir); err != nil {
		return err
	}
	return copyTree(source, root.BrowserResourcesDir)
}

func discoverDownloadedBrowserDir(root string) (string, error) {
	if usable, err := discoverDirectBrowserDir(root); err == nil {
		return usable, nil
	}
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root || !d.IsDir() {
			return nil
		}
		if usable, err := discoverDirectBrowserDir(path); err == nil {
			found = usable
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fs.ErrNotExist
	}
	return found, nil
}

func discoverDirectBrowserDir(root string) (string, error) {
	if isExecutableFile(root) {
		return filepath.Dir(root), nil
	}
	st, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	for _, rel := range browserExecutableCandidates() {
		if isExecutableFile(filepath.Join(root, rel)) {
			return root, nil
		}
	}
	return "", fs.ErrNotExist
}

func camoufoxReleaseAssetURL(version, goos, goarch string) (string, error) {
	platform := ""
	switch goos + "/" + goarch {
	case "darwin/arm64":
		platform = "mac.arm64"
	case "darwin/amd64":
		platform = "mac.x86_64"
	case "linux/amd64":
		platform = "lin.x86_64"
	case "linux/arm64":
		platform = "lin.arm64"
	default:
		return "", fmt.Errorf("%w: no Camoufox browser release asset for %s/%s", ErrNotInstalled, goos, goarch)
	}
	plain := strings.TrimPrefix(version, "v")
	return strings.TrimRight(camoufoxReleaseAssetBaseURL, "/") + "/" + version + "/camoufox-" + plain + "-" + platform + ".zip", nil
}

func unzipRuntimeAsset(archivePath, dst string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open Camoufox browser archive: %w", err)
	}
	defer func() { _ = reader.Close() }()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, f := range reader.File {
		name := filepath.Clean(f.Name)
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("%w: unsafe Camoufox archive member %q", ErrVersionMismatch, f.Name)
		}
		target := filepath.Join(dst, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.FileInfo().Mode()
		if mode == 0 {
			mode = 0o600
		}
		dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
		if err != nil {
			_ = src.Close()
			return err
		}
		_, copyErr := io.Copy(dstFile, src)
		closeErr := dstFile.Close()
		srcErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if srcErr != nil {
			return srcErr
		}
	}
	return nil
}

func sameFileTree(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func treeSHA256(root string) (string, error) {
	var records []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			records = append(records, "dir\t"+rel+"\t"+info.Mode().Perm().String())
			return nil
		}
		if !info.Mode().IsRegular() {
			records = append(records, "other\t"+rel+"\t"+info.Mode().Type().String())
			return nil
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return err
		}
		records = append(records, fmt.Sprintf("file\t%s\t%d\t%s\t%s", rel, info.Size(), info.Mode().Perm().String(), sum))
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(records)
	h := sha256.New()
	for _, record := range records {
		if _, err := io.WriteString(h, record+"\n"); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func runtimeAssetPlatformSupported(goos, goarch string) bool {
	for _, platform := range camoufoxSupportedPlatforms {
		if platform.GOOS == goos && platform.GOARCH == goarch {
			return true
		}
	}
	return false
}
