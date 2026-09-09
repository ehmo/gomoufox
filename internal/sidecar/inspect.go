package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type InstallationCheck struct {
	OK              bool   `json:"ok"`
	Status          string `json:"status,omitempty"`
	ExpectedVersion string `json:"expected_version,omitempty"`
	Version         string `json:"version,omitempty"`
	VersionSource   string `json:"version_source,omitempty"`
	PkgVersion      string `json:"pkg_version,omitempty"`
	DriverVersion   string `json:"driver_version,omitempty"`
	Match           *bool  `json:"match,omitempty"`
	Path            string `json:"path,omitempty"`
	Platform        string `json:"platform,omitempty"`
	Warning         string `json:"warning,omitempty"`
	Error           string `json:"error,omitempty"`
}

type InstallationInspection struct {
	Runtime       string
	CacheDir      string
	RuntimeAssets InstallationCheck
	Node          InstallationCheck
	Python        InstallationCheck
	Venv          InstallationCheck
	CamoufoxPkg   InstallationCheck
	Playwright    InstallationCheck
	CamoufoxBin   InstallationCheck
}

// InspectInstallation reads the selected cache. It never provisions assets,
// acquires an install lock, launches a browser or runs package imports.
func InspectInstallation(ctx context.Context, cacheDir, runtimeName string) InstallationInspection {
	if cacheDir == "" {
		cacheDir = DefaultCacheDir()
	}
	if absolute, err := filepath.Abs(cacheDir); err == nil {
		cacheDir = absolute
	}
	if runtimeName == "" {
		runtimeName = RuntimeNodeDirect
	}
	root := RuntimeAssetCacheRoot(cacheDir, sidecarGOOS, sidecarGOARCH)
	r := InstallationInspection{
		Runtime: runtimeName, CacheDir: cacheDir,
		RuntimeAssets: InstallationCheck{Path: root.Root, ExpectedVersion: CamoufoxBinaryVersion, Platform: sidecarGOOS + "/" + sidecarGOARCH},
		Node:          InstallationCheck{Path: root.NodeJS, ExpectedVersion: playwrightDriverNodeVersion},
		Playwright:    InstallationCheck{Path: filepath.Join(root.PlaywrightPackageDir, "package.json"), ExpectedVersion: RequiredPlaywright},
		CamoufoxBin:   InstallationCheck{Path: root.BrowserResourcesDir, ExpectedVersion: CamoufoxBinaryVersion},
		CamoufoxPkg:   InstallationCheck{OK: true, Status: "not_required"},
	}
	if !runtimeAssetPlatformSupported(sidecarGOOS, sidecarGOARCH) {
		failInspection(&r.RuntimeAssets, "unsupported_platform", errors.New("no pinned runtime assets for this platform"))
		return r
	}
	if runtimeName != RuntimeNodeDirect && runtimeName != RuntimePython {
		failInspection(&r.RuntimeAssets, "unsupported_runtime", errors.New("runtime must be node-direct or python"))
		return r
	}
	if exe, err := installedRuntimeBrowserExecutable(root); err == nil {
		r.CamoufoxBin.Path = exe
	}
	inspectPackageVersion(&r.Playwright)
	if runtimeName == RuntimePython {
		inspectPythonInstallation(ctx, cacheDir, root, &r)
		return r
	}
	var m RuntimeAssetManifest
	err := readInspectionJSON(root.ManifestPath, &m)
	if err != nil {
		status := "invalid_manifest"
		if errors.Is(err, os.ErrNotExist) {
			status = "missing_manifest"
		}
		failInspection(&r.RuntimeAssets, status, err)
		return r
	}
	r.RuntimeAssets.Version, r.RuntimeAssets.VersionSource = m.CamoufoxVersion, "runtime manifest"
	r.CamoufoxBin.Version, r.CamoufoxBin.VersionSource = m.CamoufoxVersion, "runtime manifest; integrity reported separately"
	// A manifest without digests cannot establish cache integrity.
	for _, a := range m.Assets {
		if a.GOOS == sidecarGOOS && a.GOARCH == sidecarGOARCH && len(a.SHA256) != 64 {
			failInspection(&r.RuntimeAssets, "invalid_manifest", errors.New("runtime asset digest is missing or malformed"))
			return r
		}
	}
	if _, _, err := ResolveRuntimeAssets(cacheDir); err != nil {
		failInspection(&r.RuntimeAssets, inspectionFailureStatus(err), err)
		return r
	}
	r.RuntimeAssets.OK, r.RuntimeAssets.Status = true, "verified"
	r.CamoufoxBin.OK, r.CamoufoxBin.Status = true, "verified"
	inspectNode(ctx, &r.Node)
	inspectPlaywrightCLI(ctx, root, &r.Playwright)
	return r
}

func failInspection(check *InstallationCheck, status string, err error) {
	check.OK, check.Status, check.Error = false, status, err.Error()
}

func inspectionFailureStatus(err error) string {
	switch {
	case strings.Contains(err.Error(), "launch server is stale"):
		return "stale_launcher"
	case strings.Contains(err.Error(), "not marked ready"):
		return "incomplete_install"
	case strings.Contains(err.Error(), "checksum mismatch"), strings.Contains(err.Error(), " size "):
		return "corrupt_asset"
	case errors.Is(err, ErrNotInstalled):
		return "missing_asset"
	case errors.Is(err, ErrVersionMismatch):
		return "version_mismatch"
	default:
		return "inspection_failed"
	}
}

func inspectPackageVersion(check *InstallationCheck) {
	var pkg struct {
		Version string `json:"version"`
	}
	if err := readInspectionJSON(check.Path, &pkg); err != nil {
		failInspection(check, "unavailable", err)
		return
	}
	check.Version, check.VersionSource = pkg.Version, "package.json"
	check.PkgVersion = pkg.Version
	check.OK = pkg.Version == check.ExpectedVersion
	if check.OK {
		check.Status = "version_matches"
	} else {
		failInspection(check, "version_mismatch", errors.New("installed package version differs from the expected pin"))
	}
}

func inspectNode(ctx context.Context, check *InstallationCheck) {
	out, err := inspectionCommand(ctx, check.Path, "--version")
	if err != nil {
		failInspection(check, "unavailable", err)
		return
	}
	check.Version, check.VersionSource = strings.TrimPrefix(strings.TrimSpace(string(out)), "v"), "node --version"
	check.OK = check.Version == check.ExpectedVersion
	if check.OK {
		check.Status = "version_matches"
	} else {
		failInspection(check, "version_mismatch", errors.New("installed Node version differs from the expected pin"))
	}
}

func inspectPlaywrightCLI(ctx context.Context, root RuntimeRoot, check *InstallationCheck) {
	if !check.OK {
		return
	}
	out, err := inspectionCommand(ctx, root.NodeJS, filepath.Join(root.PlaywrightPackageDir, "cli.js"), "--version")
	if err != nil {
		failInspection(check, "unavailable", err)
		return
	}
	check.DriverVersion = strings.TrimPrefix(strings.TrimSpace(string(out)), "Version ")
	match := check.DriverVersion == RequiredPlaywright && check.PkgVersion == RequiredPlaywright
	check.Match = &match
	check.OK = match
	if !match {
		failInspection(check, "version_mismatch", errors.New("Playwright CLI version differs from the expected pin"))
	}
}

func inspectPythonInstallation(ctx context.Context, cacheDir string, root RuntimeRoot, r *InstallationInspection) {
	r.Venv = InstallationCheck{Path: cacheDir}
	if active, err := pythonEnvironmentDir(cacheDir); err == nil {
		r.Venv.Path = active
	}
	r.CamoufoxPkg = InstallationCheck{ExpectedVersion: RequiredCamoufox}
	python, err := VenvPython(cacheDir)
	if err != nil {
		failInspection(&r.Venv, "missing_environment", err)
	} else {
		r.Venv.OK, r.Venv.Status = true, "present"
		r.Python.Path = python
		// -B prevents bytecode writes; -I ignores user site packages and PYTHON*.
		out, err := inspectionCommand(ctx, python, "-B", "-I", "-c", `import importlib.metadata as m, json, platform
def version(name):
    try: return m.version(name)
    except m.PackageNotFoundError: return ""
print(json.dumps({"python": platform.python_version(), "camoufox": version("camoufox"), "playwright": version("playwright")}))`)
		var versions struct{ Python, Camoufox, Playwright string }
		if err == nil {
			err = json.Unmarshal(out, &versions)
		}
		if err != nil {
			failInspection(&r.Python, "unavailable", err)
			failInspection(&r.CamoufoxPkg, "unavailable", errors.New("Python package metadata could not be read"))
		} else {
			r.Python.OK, r.Python.Version, r.Python.VersionSource = true, versions.Python, "platform.python_version"
			r.CamoufoxPkg.Version, r.CamoufoxPkg.VersionSource = versions.Camoufox, "Python distribution metadata"
			r.CamoufoxPkg.OK = versions.Camoufox == RequiredCamoufox
			if !r.CamoufoxPkg.OK {
				failInspection(&r.CamoufoxPkg, "version_mismatch", errors.New("Camoufox package is missing or differs from the expected pin"))
			}
			r.Playwright.PkgVersion, r.Playwright.DriverVersion = versions.Playwright, r.Playwright.Version
			match := versions.Playwright == RequiredPlaywright && r.Playwright.Version == RequiredPlaywright
			r.Playwright.Match, r.Playwright.OK = &match, match
			if !match {
				failInspection(&r.Playwright, "version_mismatch", errors.New("Python Playwright package or managed driver differs from the expected pin"))
			}
		}
	}
	inspectNode(ctx, &r.Node)
	inspectPlaywrightCLI(ctx, root, &r.Playwright)
	if err := validateCamoufoxBrowserDir(root.BrowserResourcesDir); err != nil {
		failInspection(&r.CamoufoxBin, "missing_asset", err)
	} else if err := verifyCamoufoxManifest(root.BrowserResourcesDir); err != nil {
		failInspection(&r.CamoufoxBin, "corrupt_asset", err)
	} else {
		r.CamoufoxBin.OK, r.CamoufoxBin.Status = true, "verified"
		r.CamoufoxBin.Version, r.CamoufoxBin.VersionSource = CamoufoxBinaryVersion, "pinned browser content digest"
	}
	r.RuntimeAssets.OK = r.Venv.OK && r.Python.OK && r.CamoufoxPkg.OK && r.Playwright.OK && r.Node.OK && r.CamoufoxBin.OK
	if r.RuntimeAssets.OK {
		r.RuntimeAssets.Status = "verified"
	} else {
		failInspection(&r.RuntimeAssets, "unavailable", errors.New("one or more Python runtime checks failed"))
	}
}

func readInspectionJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("inspection JSON exceeds 1 MiB")
	}
	return json.Unmarshal(data, value)
}

type inspectionOutput struct{ data []byte }

func (b *inspectionOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 4096 {
		return 0, errors.New("inspection command output exceeds 4096 bytes")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func inspectionCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	var output inspectionOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("version inspection failed: %w", err)
	}
	return output.data, nil
}
