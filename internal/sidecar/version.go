package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type CompatEntry struct {
	CamoufoxVersion string
	PlaywrightProto string
	PlaywrightGo    string
}

var VersionMatrix = []CompatEntry{{
	CamoufoxVersion: RequiredCamoufox,
	PlaywrightProto: RequiredPlaywright,
	PlaywrightGo:    PlaywrightGoVersion,
}}

func CheckCompatibility(ctx context.Context, venvPython string) error {
	camoufoxVersion, err := installedCamoufoxVersion(ctx, venvPython)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotInstalled, err)
	}
	playwrightVersion, err := installedPlaywrightVersion(venvPython)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrVersionMismatch, err)
	}
	if playwrightVersion != RequiredPlaywright && playwrightVersion != RequiredPlaywrightJSON {
		return fmt.Errorf("%w: installed playwright %s, required %s for camoufox %s and playwright-go %s", ErrVersionMismatch, playwrightVersion, RequiredPlaywright, RequiredCamoufox, PlaywrightGoVersion)
	}
	if camoufoxVersion != RequiredCamoufox {
		return fmt.Errorf("%w: installed camoufox %s, required %s", ErrVersionMismatch, camoufoxVersion, RequiredCamoufox)
	}
	return nil
}

func installedCamoufoxVersion(ctx context.Context, venvPython string) (string, error) {
	script := `import importlib.metadata
try:
    import camoufox
    v = getattr(camoufox, "__version__", None)
    if isinstance(v, str):
        print(v)
    else:
        print(importlib.metadata.version("camoufox"))
except Exception as e:
    raise SystemExit(str(e))`
	out, err := exec.CommandContext(ctx, venvPython, "-c", script).Output()
	if err != nil {
		return "", fmt.Errorf("gomoufox: camoufox not installed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func installedPlaywrightVersion(venvPython string) (string, error) {
	root, err := sitePackagesRoot(venvPython)
	if err != nil {
		return "", err
	}
	matches, err := filepath.Glob(filepath.Join(root, "playwright", "driver", "package", "package.json"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(root)), "lib", "python*", "site-packages", "playwright", "driver", "package", "package.json"))
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("gomoufox: playwright package.json not found")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return "", err
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", err
	}
	return strings.TrimSpace(pkg.Version), nil
}

func sitePackagesRoot(venvPython string) (string, error) {
	out, err := exec.Command(venvPython, "-c", "import site; print(site.getsitepackages()[0])").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
