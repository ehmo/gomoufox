package sidecar

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	runtimeAssetManifestSchemaVersion = 1
	runtimeAssetReadyMarker           = ".gomoufox-runtime-ready"
	playwrightDriverNodeVersion       = "24.11.1"
	playwrightCoreArchiveMaxBytes     = 32 << 20
	playwrightCoreExtractedMaxBytes   = 64 << 20
	playwrightCoreMaxEntries          = 4096
	playwrightNodeArchiveMaxBytes     = 128 << 20
	playwrightNodeBinaryMaxBytes      = 192 << 20
)

var (
	installPlaywrightDriverForRuntime = installRuntimePlaywrightDriver
	installCamoufoxBrowserForRuntime  = installRuntimeCamoufoxBrowser
	runtimeAssetHTTPClient            = http.DefaultClient
	camoufoxReleaseAssetBaseURL       = "https://github.com/daijro/camoufox/releases/download"
	// The package digest is npm's dist.integrity SHA-512 for
	// playwright-core@1.57.0. Node 24.11.1 and the per-platform SHA-256 values
	// come from Playwright v1.57.0's build-playwright-driver.sh and Node's signed
	// SHASUMS256.txt release manifest.
	playwrightCorePackageURL     = "https://registry.npmjs.org/playwright-core/-/playwright-core-1.57.0.tgz"
	playwrightCorePackageSHA512  = "6a04dc2a5330fe68c158e9c3ea4159b6d000187822fcdc34099da8e89a9649b325236d7d94014b6590b2a81c93b2f54026ae570391fc700e8faef051a41584b9"
	playwrightNodeReleaseBaseURL = "https://nodejs.org/dist"
	playwrightNodeAssets         = map[string]playwrightNodeAsset{
		"darwin/arm64": {
			Filename: "node-v24.11.1-darwin-arm64.tar.gz",
			SHA256:   "b05aa3a66efe680023f930bd5af3fdbbd542794da5644ca2ad711d68cbd4dc35",
		},
		"linux/amd64": {
			Filename: "node-v24.11.1-linux-x64.tar.gz",
			SHA256:   "58a5ff5cc8f2200e458bea22e329d5c1994aa1b111d499ca46ec2411d58239ca",
		},
	}
)

type playwrightNodeAsset struct {
	Filename string
	SHA256   string
}

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
	PlaywrightCoreModule string
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
		PlaywrightCoreModule: filepath.Join(root, "camoufox", "node_modules", "playwright-core"),
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
			{Name: "node", Kind: RuntimeAssetNodeJS, GOOS: goos, GOARCH: goarch, Path: relPath(root.Root, root.NodeJS), Source: playwrightNodeSource(goos, goarch), License: "Node.js"},
			{Name: "launch-server", Kind: RuntimeAssetLaunchServerJS, GOOS: goos, GOARCH: goarch, Path: relPath(root.Root, root.LaunchServerJS), Source: "gomoufox-release-asset://launch-server", License: "Apache-2.0"},
			{Name: "playwright-package", Kind: RuntimeAssetPlaywrightPackage, GOOS: goos, GOARCH: goarch, Path: relPath(root.Root, root.PlaywrightPackageDir), Source: playwrightCorePackageURL, License: "Apache-2.0"},
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
	if err := verifyRuntimePlaywrightCoreModule(root, sidecarGOOS); err != nil {
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
	if err := ensureRuntimePlaywrightCoreModule(root, sidecarGOOS); err != nil {
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
	if err := verifyRuntimePlaywrightCoreModule(root, sidecarGOOS); err != nil {
		return RuntimeRoot{}, err
	}
	if err := os.WriteFile(root.ReadyMarkerPath, []byte("manifest-ready\n"), 0o600); err != nil {
		return RuntimeRoot{}, err
	}
	return root, nil
}

func ensureRuntimePlaywrightCoreModule(root RuntimeRoot, goos string) error {
	if err := verifyRuntimePlaywrightCoreModule(root, goos); err == nil {
		return nil
	}
	parent := filepath.Dir(root.PlaywrightCoreModule)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".playwright-core-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if goos == "windows" {
		if err := copyTree(root.PlaywrightPackageDir, staging); err != nil {
			return fmt.Errorf("copy playwright-core module: %w", err)
		}
	} else {
		if err := os.Remove(staging); err != nil {
			return err
		}
		target, err := filepath.Rel(parent, root.PlaywrightPackageDir)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, staging); err != nil {
			return fmt.Errorf("link playwright-core module: %w", err)
		}
	}
	if err := replaceRuntimeAsset(staging, root.PlaywrightCoreModule); err != nil {
		return fmt.Errorf("install playwright-core module: %w", err)
	}
	return verifyRuntimePlaywrightCoreModule(root, goos)
}

func verifyRuntimePlaywrightCoreModule(root RuntimeRoot, goos string) error {
	info, err := os.Lstat(root.PlaywrightCoreModule)
	if err != nil {
		return fmt.Errorf("%w: playwright-core module is unavailable: %v", ErrNotInstalled, err)
	}
	if goos == "windows" {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: Windows playwright-core module is not a copied directory", ErrVersionMismatch)
		}
		sourceSum, err := treeSHA256(root.PlaywrightPackageDir)
		if err != nil {
			return fmt.Errorf("%w: hash playwright-core package: %v", ErrNotInstalled, err)
		}
		moduleSum, err := treeSHA256(root.PlaywrightCoreModule)
		if err != nil {
			return fmt.Errorf("%w: hash playwright-core module: %v", ErrNotInstalled, err)
		}
		if sourceSum != moduleSum {
			return fmt.Errorf("%w: Windows playwright-core module differs from the installed package", ErrVersionMismatch)
		}
		return nil
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%w: playwright-core module is not a symlink", ErrVersionMismatch)
	}
	resolved, err := filepath.EvalSymlinks(root.PlaywrightCoreModule)
	if err != nil {
		return fmt.Errorf("%w: resolve playwright-core module: %v", ErrNotInstalled, err)
	}
	if !sameCanonicalFileTree(resolved, root.PlaywrightPackageDir) {
		return fmt.Errorf("%w: playwright-core module points outside the installed package", ErrVersionMismatch)
	}
	return nil
}

func installRuntimePlaywrightDriver(ctx context.Context, root RuntimeRoot, opts InstallOptions) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if !opts.ForceReinstall && verifyPinnedPlaywrightDriver(ctx, root.PlaywrightDriverDir) == nil {
		return nil
	}
	if opts.SkipBinaryFetch || os.Getenv("GOMOUFOX_SKIP_FETCH") != "" {
		return fmt.Errorf("%w: pinned Playwright driver is unavailable and fetch is disabled", ErrNotInstalled)
	}

	nodeAsset, ok := playwrightNodeAssets[sidecarGOOS+"/"+sidecarGOARCH]
	if !ok {
		return fmt.Errorf("%w: no pinned Playwright Node.js asset for %s/%s", ErrNotInstalled, sidecarGOOS, sidecarGOARCH)
	}
	coreArchive, err := downloadRuntimeAssetBytes(ctx, playwrightCorePackageURL, playwrightCoreArchiveMaxBytes)
	if err != nil {
		return fmt.Errorf("%w: download pinned playwright-core %s: %v", ErrNotInstalled, RequiredPlaywright, err)
	}
	coreSum := sha512.Sum512(coreArchive)
	if !strings.EqualFold(hex.EncodeToString(coreSum[:]), playwrightCorePackageSHA512) {
		return fmt.Errorf("%w: playwright-core %s archive checksum mismatch", ErrVersionMismatch, RequiredPlaywright)
	}
	nodeURL := strings.TrimRight(playwrightNodeReleaseBaseURL, "/") + "/v" + playwrightDriverNodeVersion + "/" + nodeAsset.Filename
	nodeArchive, err := downloadRuntimeAssetBytes(ctx, nodeURL, playwrightNodeArchiveMaxBytes)
	if err != nil {
		return fmt.Errorf("%w: download pinned Playwright Node.js %s: %v", ErrNotInstalled, playwrightDriverNodeVersion, err)
	}
	nodeSum := sha256.Sum256(nodeArchive)
	if !strings.EqualFold(hex.EncodeToString(nodeSum[:]), nodeAsset.SHA256) {
		return fmt.Errorf("%w: Playwright Node.js %s archive checksum mismatch", ErrVersionMismatch, playwrightDriverNodeVersion)
	}

	parent := filepath.Dir(root.PlaywrightDriverDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".playwright-driver-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := extractPlaywrightCoreArchive(coreArchive, staging); err != nil {
		return err
	}
	if err := extractPlaywrightNodeArchive(nodeArchive, nodeAsset.Filename, staging); err != nil {
		return err
	}
	if err := verifyPinnedPlaywrightDriver(ctx, staging); err != nil {
		return err
	}
	return replaceRuntimeAsset(staging, root.PlaywrightDriverDir)
}

// EnsureManagedPlaywrightDriver installs the exact driver used by both the
// node-direct and legacy Python runtimes. It avoids playwright-go's retired
// driver CDN without changing the pinned Playwright protocol version.
func EnsureManagedPlaywrightDriver(ctx context.Context, opts InstallOptions) error {
	venvDir := opts.VenvDir
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	opts.VenvDir = venvDir
	lock, err := acquireInstallLock(ctx, venvDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	root := RuntimeAssetCacheRoot(venvDir, sidecarGOOS, sidecarGOARCH)
	return installRuntimePlaywrightDriver(ctx, root, opts)
}

func playwrightNodeSource(goos, goarch string) string {
	asset, ok := playwrightNodeAssets[goos+"/"+goarch]
	if !ok {
		return ""
	}
	return strings.TrimRight(playwrightNodeReleaseBaseURL, "/") + "/v" + playwrightDriverNodeVersion + "/" + asset.Filename
}

func downloadRuntimeAssetBytes(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := runtimeAssetHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("archive exceeds %d-byte limit", maxBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("archive exceeds %d-byte limit", maxBytes)
	}
	return raw, nil
}

func extractPlaywrightCoreArchive(raw []byte, dst string) error {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: read playwright-core archive: %v", ErrVersionMismatch, err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var total int64
	entries := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: read playwright-core archive: %v", ErrVersionMismatch, err)
		}
		entries++
		if entries > playwrightCoreMaxEntries {
			return fmt.Errorf("%w: playwright-core archive has too many entries", ErrVersionMismatch)
		}
		name, err := safeRuntimeArchiveMember(header.Name, "package")
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(filepath.Join(dst, filepath.FromSlash(name)), 0o700); err != nil {
				return err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("%w: unsupported playwright-core archive member %q", ErrVersionMismatch, header.Name)
		}
		if header.Size < 0 || header.Size > playwrightCoreExtractedMaxBytes-total {
			return fmt.Errorf("%w: playwright-core archive exceeds extracted-size limit", ErrVersionMismatch)
		}
		total += header.Size
		target := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if os.FileMode(header.Mode).Perm()&0o111 != 0 {
			mode = 0o700
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(out, tr, header.Size)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for _, required := range []string{"cli.js", "package.json"} {
		if st, err := os.Stat(filepath.Join(dst, "package", required)); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("%w: playwright-core archive missing package/%s", ErrVersionMismatch, required)
		}
	}
	return nil
}

func extractPlaywrightNodeArchive(raw []byte, filename, dst string) error {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: read Playwright Node.js archive: %v", ErrVersionMismatch, err)
	}
	defer func() { _ = gz.Close() }()
	archiveRoot := strings.TrimSuffix(filename, ".tar.gz")
	wanted := archiveRoot + "/bin/node"
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: read Playwright Node.js archive: %v", ErrVersionMismatch, err)
		}
		if path.Clean(header.Name) != wanted {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("%w: Playwright Node.js binary is not a regular file", ErrVersionMismatch)
		}
		if header.Size <= 0 || header.Size > playwrightNodeBinaryMaxBytes {
			return fmt.Errorf("%w: Playwright Node.js binary has invalid size", ErrVersionMismatch)
		}
		target := filepath.Join(dst, nodeExecutableName())
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(out, tr, header.Size)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return nil
	}
	return fmt.Errorf("%w: Playwright Node.js archive missing %s", ErrVersionMismatch, wanted)
}

func safeRuntimeArchiveMember(name, requiredRoot string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == requiredRoot || !strings.HasPrefix(clean, requiredRoot+"/") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("%w: unsafe runtime archive member %q", ErrVersionMismatch, name)
	}
	return clean, nil
}

func verifyPinnedPlaywrightDriver(ctx context.Context, driverDir string) error {
	node := filepath.Join(driverDir, nodeExecutableName())
	if st, err := os.Stat(node); err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%w: pinned Playwright Node.js executable is unavailable", ErrNotInstalled)
	}
	packageJSON := filepath.Join(driverDir, "package", "package.json")
	raw, err := os.ReadFile(packageJSON)
	if err != nil {
		return fmt.Errorf("%w: read pinned playwright-core package: %v", ErrNotInstalled, err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return fmt.Errorf("%w: decode pinned playwright-core package: %v", ErrVersionMismatch, err)
	}
	if pkg.Version != RequiredPlaywright {
		return fmt.Errorf("%w: installed playwright-core %s, required %s", ErrVersionMismatch, pkg.Version, RequiredPlaywright)
	}
	cli := filepath.Join(driverDir, "package", "cli.js")
	if st, err := os.Stat(cli); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("%w: pinned Playwright CLI is unavailable", ErrNotInstalled)
	}
	out, err := exec.CommandContext(ctx, node, cli, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: run pinned Playwright CLI: %v", ErrVersionMismatch, err)
	}
	if strings.TrimSpace(string(out)) != "Version "+RequiredPlaywright {
		return fmt.Errorf("%w: pinned Playwright CLI reported an unexpected version", ErrVersionMismatch)
	}
	return nil
}

func replaceRuntimeAsset(source, target string) error {
	parent := filepath.Dir(target)
	backup := ""
	if _, err := os.Lstat(target); err == nil {
		placeholder, err := os.MkdirTemp(parent, ".runtime-asset-backup-")
		if err != nil {
			return err
		}
		if err := os.Remove(placeholder); err != nil {
			return err
		}
		backup = placeholder
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		if backup != "" {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				return fmt.Errorf("replace runtime asset %s: %v; restore previous asset: %w", target, err, restoreErr)
			}
		}
		return fmt.Errorf("replace runtime asset %s: %w", target, err)
	}
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
	}
	return nil
}

func installRuntimeCamoufoxBrowser(ctx context.Context, root RuntimeRoot, opts InstallOptions) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	explicitSource := firstNonEmpty(opts.CamoufoxPath, os.Getenv(EnvCamoufoxPath))
	if explicitSource != "" {
		if err := validateCamoufoxBrowserDir(explicitSource); err != nil {
			return fmt.Errorf("%w: invalid Camoufox browser asset source %s: %v", ErrNotInstalled, explicitSource, err)
		}
		if !trustUnverifiedCamoufoxPath() {
			if err := verifyCamoufoxManifest(explicitSource); err != nil {
				return fmt.Errorf("%w: offline Camoufox browser path %s failed manifest verification: %v; set %s=1 only for trusted local builds", ErrVersionMismatch, explicitSource, err, EnvTrustUnverifiedCamoufoxPath)
			}
		}
		return copyRuntimeCamoufoxBrowser(explicitSource, root)
	}

	if !opts.ForceReinstall {
		if err := validateCamoufoxBrowserDir(root.BrowserResourcesDir); err == nil {
			if trustUnverifiedCamoufoxPath() || verifyCamoufoxManifest(root.BrowserResourcesDir) == nil {
				return nil
			}
		}
	}

	// Camoufox's Python package fetches the newest supported browser into a
	// global cache. Reuse that cache only when it matches gomoufox's exact pin;
	// a valid but newer tree must not be mistaken for the pinned runtime.
	for _, candidateRoot := range camoufoxBrowserCacheRoots() {
		candidate, err := discoverUsableBrowserDir(candidateRoot)
		if err != nil || verifyCamoufoxManifest(candidate) != nil {
			continue
		}
		return copyRuntimeCamoufoxBrowser(candidate, root)
	}
	return downloadRuntimeCamoufoxBrowser(ctx, root, opts)
}

func copyRuntimeCamoufoxBrowser(source string, root RuntimeRoot) error {
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

const path = require("path");
const playwrightRoot = path.dirname(require.resolve("playwright-core"));
const { BrowserServerLauncherImpl } = require(path.join(playwrightRoot, "lib", "browserServerImpl.js"));

async function main() {
  let input = "";
  process.stdin.setEncoding("utf8");
  for await (const chunk of process.stdin) input += chunk;
  const encoded = input.trim();
  const payload = encoded ? JSON.parse(Buffer.from(encoded, "base64").toString("utf8")) : {};
  payload.host = "127.0.0.1";
  const browser = await new BrowserServerLauncherImpl("firefox").launchServer(payload);
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
	if err := verifyCamoufoxManifest(source); err != nil {
		return err
	}
	return copyRuntimeCamoufoxBrowser(source, root)
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

func sameCanonicalFileTree(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(absA); err == nil {
		absA = resolved
	}
	if resolved, err := filepath.EvalSymlinks(absB); err == nil {
		absB = resolved
	}
	return absA == absB
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
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("hash tree root is not a directory: %s", root)
	}
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
