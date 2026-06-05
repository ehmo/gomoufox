package sidecar

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ehmo/gomoufox/internal/policy"
)

var (
	venvPythonAfterInstall  = VenvPython
	venvPipForCamoufox      = VenvPip
	venvPipAfterCreate      = VenvPip
	installDiagnosticWriter = io.Writer(os.Stderr)
)

func EnsureInstalled(ctx context.Context, opts InstallOptions) error {
	venvDir := opts.VenvDir
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	lock, err := acquireInstallLock(ctx, venvDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	python, err := FindPython(opts.PythonBin)
	if err != nil {
		return err
	}
	if err := EnsureVenv(ctx, python, venvDir); err != nil {
		return err
	}
	if err := EnsureCamoufox(ctx, venvDir, opts); err != nil {
		return err
	}
	venvPython, err := venvPythonAfterInstall(venvDir)
	if err != nil {
		return err
	}
	if err := EnsureBinary(ctx, venvPython, opts); err != nil {
		return err
	}
	return CheckCompatibility(ctx, venvPython)
}

func EnsureCamoufox(ctx context.Context, venvDir string, opts InstallOptions) error {
	requiredCamoufox := opts.CamoufoxVersion
	if requiredCamoufox == "" {
		requiredCamoufox = RequiredCamoufox
	}
	if requiredCamoufox != RequiredCamoufox {
		return fmt.Errorf("%w: unsupported camoufox pin %s, required %s", ErrVersionMismatch, requiredCamoufox, RequiredCamoufox)
	}
	pip, err := venvPipForCamoufox(venvDir)
	if err != nil {
		return err
	}
	if opts.ForceReinstall || !compatibleInstalled(ctx, venvDir) {
		cmd := exec.CommandContext(ctx, pip, "install", "camoufox[geoip]=="+requiredCamoufox, "playwright=="+RequiredPlaywright)
		if out, err := runInstallCommand(cmd, opts.Verbose); err != nil {
			return fmt.Errorf("pip install pinned camoufox/playwright: %w: %s", err, string(out))
		}
	}
	return nil
}

func runInstallCommand(cmd *exec.Cmd, verbose bool) ([]byte, error) {
	if verbose {
		stdout := policy.NewRedactWriter(installDiagnosticWriter)
		stderr := policy.NewRedactWriter(installDiagnosticWriter)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		err := cmd.Run()
		if flushErr := stdout.Flush(); flushErr != nil && err == nil {
			err = flushErr
		}
		if flushErr := stderr.Flush(); flushErr != nil && err == nil {
			err = flushErr
		}
		return nil, err
	}
	out, err := cmd.CombinedOutput()
	return []byte(policy.Redact(string(out))), err
}

func FindPython(override string) (string, error) {
	candidates := []string{}
	if override != "" {
		candidates = append(candidates, override)
	}
	if env := os.Getenv("GOMOUFOX_PYTHON"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, "python3", "python", "python3.13", "python3.12", "python3.11", "python3.10", "python3.9")
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		if err := checkPythonVersion(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("gomoufox: Python 3.9+ not found")
}

func EnsureVenv(ctx context.Context, pythonBin, venvDir string) error {
	if _, err := VenvPython(venvDir); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(venvDir), 0o700); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, pythonBin, "-m", "venv", venvDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create venv: %w: %s", err, string(out))
	}
	pip, err := venvPipAfterCreate(venvDir)
	if err != nil {
		return err
	}
	cmd = exec.CommandContext(ctx, pip, "install", "--upgrade", "pip=="+RequiredPip)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("upgrade pip: %w: %s", err, string(out))
	}
	return nil
}

func compatibleInstalled(ctx context.Context, venvDir string) bool {
	venvPython, err := VenvPython(venvDir)
	if err != nil {
		return false
	}
	return CheckCompatibility(ctx, venvPython) == nil
}

func checkPythonVersion(path string) error {
	cmd := exec.Command(path, "-c", "import sys; raise SystemExit(0 if sys.version_info >= (3, 9) else 1)")
	return cmd.Run()
}
