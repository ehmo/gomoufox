package sidecar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	binarySizeWarningOnce   sync.Once
	binarySizeWarningWriter io.Writer = os.Stderr
	camoufoxManifestSHA256            = map[string][]string{
		"v135.0.1-beta.24/darwin/arm64": {
			"11b8ffa50607f52123abc3426ea404079880b58881f53ce28aac9782780f3f16",
			"bd9fac6728f62a12ce4fdc587013978aa0a63e574f026c0f6facfc1c9df64d87",
		},
		"v135.0.1-beta.24/linux/amd64": {
			"41dbd88d7bf89ec667586b77bf4ea33518233bca4e4824c61d89d1cc052de4c7",
		},
	}
	camoufoxManifestRel  = filepath.Rel
	camoufoxManifestInfo = func(d fs.DirEntry) (fs.FileInfo, error) { return d.Info() }
	userCacheDir         = os.UserCacheDir
)

const (
	EnvCamoufoxPath                    = "GOMOUFOX_CAMOUFOX_PATH"
	EnvTrustUnverifiedCamoufoxPath     = "GOMOUFOX_TRUST_UNVERIFIED_CAMOUFOX_PATH"
	trustUnverifiedCamoufoxPathEnabled = "1"
)

type camoufoxPlatform struct {
	GOOS   string
	GOARCH string
}

var camoufoxSupportedPlatforms = []camoufoxPlatform{
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "linux", GOARCH: "amd64"},
}

func EnsureBinary(ctx context.Context, venvPython string, opts InstallOptions) error {
	_ = venvPython // Browser provisioning is Go-managed and does not invoke `camoufox fetch`.
	if opts.VenvDir == "" {
		opts.VenvDir = DefaultCacheDir()
	}
	root := RuntimeAssetCacheRoot(opts.VenvDir, sidecarGOOS, sidecarGOARCH)
	if err := os.MkdirAll(filepath.Dir(root.Root), 0o700); err != nil {
		return err
	}
	if err := installRuntimeCamoufoxBrowser(ctx, root, opts); err != nil {
		return err
	}
	_, err := ResolveManagedCamoufoxExecutable(opts.VenvDir)
	return err
}

// ResolveManagedCamoufoxExecutable returns the executable from gomoufox's
// versioned browser cache. Python and node-direct runtimes share this pinned
// browser tree instead of consulting Camoufox's moving global cache at launch.
func ResolveManagedCamoufoxExecutable(venvDir string) (string, error) {
	root := RuntimeAssetCacheRoot(venvDir, sidecarGOOS, sidecarGOARCH)
	if err := validateCamoufoxBrowserDir(root.BrowserResourcesDir); err != nil {
		return "", fmt.Errorf("%w: managed Camoufox browser is unavailable: %v", ErrNotInstalled, err)
	}
	if !trustUnverifiedCamoufoxPath() {
		if err := verifyCamoufoxManifest(root.BrowserResourcesDir); err != nil {
			return "", fmt.Errorf("%w: managed Camoufox browser failed manifest verification: %v", ErrVersionMismatch, err)
		}
	}
	executable, err := installedRuntimeBrowserExecutable(root)
	if err != nil {
		return "", fmt.Errorf("%w: locate managed Camoufox browser executable: %v", ErrNotInstalled, err)
	}
	return executable, nil
}

func trustUnverifiedCamoufoxPath() bool {
	return os.Getenv(EnvTrustUnverifiedCamoufoxPath) == trustUnverifiedCamoufoxPathEnabled
}

func validateCamoufoxBrowserDir(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("not a directory")
	}
	if _, err := findBrowserExecutable(dir); err != nil {
		return err
	}
	return nil
}

func camoufoxBrowserCacheRoots() []string {
	cacheDir, err := userCacheDir()
	if err != nil || cacheDir == "" {
		return nil
	}
	return []string{filepath.Join(cacheDir, "camoufox")}
}

func discoverUsableBrowserDir(root string) (string, error) {
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
	if _, err := findBrowserExecutable(root); err == nil {
		return root, nil
	}
	return "", fs.ErrNotExist
}

func findBrowserExecutable(root string) (string, error) {
	for _, rel := range browserExecutableCandidates() {
		path := filepath.Join(root, rel)
		if isExecutableFile(path) {
			return path, nil
		}
	}
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root || d.IsDir() {
			return nil
		}
		if isBrowserExecutableName(d.Name()) && isExecutableFile(path) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no Camoufox/Firefox executable found under %s", root)
	}
	return found, nil
}

func browserExecutableCandidates() []string {
	switch sidecarGOOS {
	case "darwin":
		return []string{
			filepath.Join("Camoufox.app", "Contents", "MacOS", "camoufox"),
			filepath.Join("Camoufox.app", "Contents", "MacOS", "firefox"),
			filepath.Join("Firefox.app", "Contents", "MacOS", "camoufox"),
			filepath.Join("Firefox.app", "Contents", "MacOS", "firefox"),
			filepath.Join("Contents", "MacOS", "camoufox"),
			filepath.Join("Contents", "MacOS", "firefox"),
			"firefox",
			"camoufox",
		}
	case "windows":
		return []string{"firefox.exe", "camoufox.exe"}
	case "linux":
		return []string{"camoufox-bin", "camoufox", "firefox"}
	default:
		return []string{"firefox", "camoufox"}
	}
}

func isBrowserExecutableName(name string) bool {
	switch sidecarGOOS {
	case "windows":
		return strings.EqualFold(name, "firefox.exe") || strings.EqualFold(name, "camoufox.exe")
	case "linux":
		return name == "camoufox-bin" || name == "camoufox" || name == "firefox"
	default:
		return name == "firefox" || name == "camoufox"
	}
}

func isExecutableFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	if sidecarGOOS == "windows" {
		return true
	}
	return st.Mode().Perm()&0o111 != 0
}

func verifyCamoufoxManifest(root string) error {
	key := camoufoxManifestKey(CamoufoxBinaryVersion, sidecarGOOS, sidecarGOARCH)
	expected := camoufoxManifestSHA256[key]
	if len(expected) == 0 {
		return fmt.Errorf("%w: no Camoufox binary manifest checksum recorded for %s", ErrVersionMismatch, key)
	}
	got, err := camoufoxBrowserManifestSHA256(root)
	if err != nil {
		return err
	}
	for _, allowed := range expected {
		if got == allowed {
			return nil
		}
	}
	return fmt.Errorf("%w: Camoufox binary manifest checksum mismatch for %s: got %s, expected one of %s", ErrVersionMismatch, key, got, strings.Join(expected, ","))
}

func camoufoxBrowserManifestSHA256(root string) (string, error) {
	var records []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := camoufoxManifestRel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skipManifestPath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := camoufoxManifestInfo(d)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		records = append(records, fmt.Sprintf("%04o %d %x %s", info.Mode().Perm(), info.Size(), sum, rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(records)
	h := sha256.New()
	for i, record := range records {
		if i > 0 {
			_, _ = h.Write([]byte("\n"))
		}
		_, _ = h.Write([]byte(record))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func skipManifestPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		lower := strings.ToLower(part)
		switch lower {
		case ".ds_store", ".gomoufox.lock", ".gomoufox-install.lock", "parent.lock", "lock", "cache", ".cache":
			return true
		}
		if strings.HasSuffix(lower, ".lock") || strings.HasSuffix(lower, ".tmp") {
			return true
		}
	}
	return false
}

func camoufoxManifestKey(version, goos, goarch string) string {
	return version + "/" + goos + "/" + goarch
}

func camoufoxSupportedManifestKeys(version string) []string {
	keys := make([]string, 0, len(camoufoxSupportedPlatforms))
	for _, platform := range camoufoxSupportedPlatforms {
		keys = append(keys, camoufoxManifestKey(version, platform.GOOS, platform.GOARCH))
	}
	sort.Strings(keys)
	return keys
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
