package sidecar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	binarySizeWarningOnce   sync.Once
	binarySizeWarningWriter io.Writer = os.Stderr
	camoufoxManifestSHA256            = map[string]string{
		"v135.0.1-beta.24/darwin/arm64": "11b8ffa50607f52123abc3426ea404079880b58881f53ce28aac9782780f3f16",
		"v135.0.1-beta.24/linux/amd64":  "8c8f41172f86e265badaacb1a192e0f37ba55976b293b26b3d0642c5428e2dc3",
	}
	camoufoxManifestRel                 = filepath.Rel
	camoufoxManifestInfo                = func(d fs.DirEntry) (fs.FileInfo, error) { return d.Info() }
	discoverCamoufoxBrowserDirForEnsure = discoverCamoufoxBrowserDir
	userCacheDir                        = os.UserCacheDir
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
	if offlinePath := firstNonEmpty(opts.CamoufoxPath, os.Getenv("GOMOUFOX_CAMOUFOX_PATH")); offlinePath != "" {
		if err := validateCamoufoxBrowserDir(offlinePath); err != nil {
			return fmt.Errorf("%w: invalid offline Camoufox browser path %s: %v", ErrNotInstalled, offlinePath, err)
		}
		return nil
	}

	discovered, discoverErr := discoverCamoufoxBrowserDirForEnsure(ctx, venvPython)
	if discoverErr == nil && discovered != "" {
		if err := validateCamoufoxBrowserDir(discovered); err != nil {
			discoverErr = err
		} else if err := verifyCamoufoxManifest(discovered); err != nil {
			return err
		} else {
			return nil
		}
	}

	if opts.SkipBinaryFetch || os.Getenv("GOMOUFOX_SKIP_FETCH") != "" {
		if discoverErr != nil {
			return fmt.Errorf("%w: usable Camoufox browser binary not found and fetch is disabled: %v", ErrNotInstalled, discoverErr)
		}
		return fmt.Errorf("%w: usable Camoufox browser binary not found and fetch is disabled", ErrNotInstalled)
	}

	binarySizeWarningOnce.Do(func() {
		if binarySizeWarningWriter != nil {
			_, _ = fmt.Fprintln(binarySizeWarningWriter, "gomoufox: Camoufox browser download is approximately 300-660 MB")
		}
	})
	cmd := exec.CommandContext(ctx, venvPython, "-m", "camoufox", "fetch")
	if out, err := runInstallCommand(cmd, opts.Verbose); err != nil {
		return fmt.Errorf("camoufox fetch: %w: %s", err, string(out))
	}

	discovered, err := discoverCamoufoxBrowserDirForEnsure(ctx, venvPython)
	if err != nil {
		return fmt.Errorf("%w: camoufox fetch completed but browser binary was not discoverable: %v", ErrNotInstalled, err)
	}
	if err := validateCamoufoxBrowserDir(discovered); err != nil {
		return fmt.Errorf("%w: camoufox fetch produced unusable browser directory %s: %v", ErrNotInstalled, discovered, err)
	}
	return verifyCamoufoxManifest(discovered)
}

func discoverCamoufoxBrowserDir(ctx context.Context, venvPython string) (string, error) {
	if strings.TrimSpace(venvPython) == "" {
		return "", fmt.Errorf("empty venv python")
	}
	const script = `from pathlib import Path
import os
import sys

roots = []
for key in ("CAMOUFOX_BROWSER_PATH", "CAMOUFOX_EXECUTABLE_PATH"):
    value = os.environ.get(key)
    if value:
        roots.append(Path(value))
try:
    import camoufox
    package = Path(camoufox.__file__).resolve().parent
    roots.append(package)
except Exception:
    pass

candidates = (
    "firefox",
    "camoufox",
    "firefox.exe",
    "camoufox.exe",
    "Camoufox.app/Contents/MacOS/firefox",
    "Firefox.app/Contents/MacOS/firefox",
    "Contents/MacOS/firefox",
)

def executable(path):
    try:
        return path.is_file() and os.access(path, os.X_OK)
    except OSError:
        return False

def usable_dir(path):
    if path.is_file() and executable(path):
        return path.parent
    if not path.is_dir():
        return None
    for rel in candidates:
        if executable(path / rel):
            return path
    return None

for root in roots:
    usable = usable_dir(root)
    if usable:
        print(usable)
        raise SystemExit(0)
    if root.is_dir():
        for child in root.rglob("*"):
            usable = usable_dir(child)
            if usable:
                print(usable)
                raise SystemExit(0)
raise SystemExit(0)
`
	out, err := exec.CommandContext(ctx, venvPython, "-c", script).Output()
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if path != "" {
		return path, nil
	}
	for _, root := range camoufoxBrowserCacheRoots() {
		if usable, err := discoverUsableBrowserDir(root); err == nil {
			return usable, nil
		}
	}
	return "", fs.ErrNotExist
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
	default:
		return []string{"firefox", "camoufox"}
	}
}

func isBrowserExecutableName(name string) bool {
	switch sidecarGOOS {
	case "windows":
		return strings.EqualFold(name, "firefox.exe") || strings.EqualFold(name, "camoufox.exe")
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
	if expected == "" {
		return fmt.Errorf("%w: no Camoufox binary manifest checksum recorded for %s", ErrVersionMismatch, key)
	}
	got, err := camoufoxBrowserManifestSHA256(root)
	if err != nil {
		return err
	}
	if got != expected {
		return fmt.Errorf("%w: Camoufox binary manifest checksum mismatch for %s: got %s, expected %s", ErrVersionMismatch, key, got, expected)
	}
	return nil
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
