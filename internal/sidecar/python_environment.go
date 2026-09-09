package sidecar

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const pythonEnvironmentMarker = ".gomoufox-python-environment"
const pythonEnvironmentsDirectory = "python-envs"

// Existing cache/bin environments remain usable until the first successful
// staged install publishes a marker. Failed or interrupted staging is never
// selected by directory age or by a partially written environment.
func pythonEnvironmentDir(cacheDir string) (string, error) {
	marker := filepath.Join(cacheDir, pythonEnvironmentMarker)
	info, err := runtimeAssetFiles.lstat(marker)
	if os.IsNotExist(err) {
		return cacheDir, nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: active Python environment marker must be a regular file", ErrNotInstalled)
	}
	f, err := runtimeAssetFiles.open(marker)
	if os.IsNotExist(err) {
		return cacheDir, nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(raw))
	if len(raw) > 1024 || !strings.HasPrefix(name, "env-") || strings.ContainsAny(name, "/\\\x00") || name != filepath.Base(name) {
		return "", fmt.Errorf("%w: invalid active Python environment marker at %s", ErrNotInstalled, marker)
	}
	parent := filepath.Join(cacheDir, pythonEnvironmentsDirectory)
	if err := validatePythonEnvironmentsDirectory(parent); err != nil {
		return "", err
	}
	dir := filepath.Join(parent, name)
	info, err = runtimeAssetFiles.lstat(dir)
	if err != nil {
		return "", fmt.Errorf("%w: active Python environment is unavailable: %v", ErrNotInstalled, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: active Python environment must be a managed directory", ErrNotInstalled)
	}
	return dir, nil
}

func ensurePythonInstalled(ctx context.Context, opts InstallOptions) error {
	cacheDir := opts.VenvDir
	current, currentErr := VenvPython(cacheDir)
	replaceEnvironment := opts.ForceReinstall || currentErr != nil
	if !replaceEnvironment {
		replaceEnvironment = checkCompatibilityForInstall(ctx, current) != nil
	}
	var environment, pointerDir, pointerFile string
	published := false
	defer func() {
		if pointerDir != "" {
			_ = runtimeAssetFiles.removeAll(pointerDir)
		}
		if environment != "" && !published {
			_ = runtimeAssetFiles.removeAll(environment)
		}
	}()
	if replaceEnvironment {
		python, err := findPythonForInstall(opts.PythonBin)
		if err != nil {
			return err
		}
		parent := filepath.Join(cacheDir, pythonEnvironmentsDirectory)
		if err := runtimeAssetFiles.mkdirAll(parent, 0700); err != nil {
			return err
		}
		if err := validatePythonEnvironmentsDirectory(parent); err != nil {
			return err
		}
		environment, err = runtimeAssetFiles.mkdirTemp(parent, "env-")
		if err != nil {
			return err
		}
		// Keep this path after publication: venv scripts contain absolute
		// interpreter paths and cannot be made relocatable by renaming a dir.
		if err := ensureVenvForInstall(ctx, python, environment); err != nil {
			return err
		}
		if err := ensureCamoufoxForInstall(ctx, environment, opts); err != nil {
			return err
		}
		venvPython, err := venvPythonAfterInstall(environment)
		if err != nil {
			return err
		}
		if err := checkCompatibilityForInstall(ctx, venvPython); err != nil {
			return err
		}
		pointerDir, err = runtimeAssetFiles.mkdirTemp(cacheDir, ".python-env-pointer-")
		if err != nil {
			return err
		}
		pointerFile = filepath.Join(pointerDir, "active")
		if err := runtimeAssetFiles.writeFile(pointerFile, []byte(filepath.Base(environment)+"\n"), 0600); err != nil {
			return err
		}
	}
	prepared, err := prepareRuntimeAssets(ctx, opts)
	if err != nil {
		return err
	}
	defer prepared.cleanup()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := prepared.publish(); err != nil {
		return err
	}
	if pointerFile != "" {
		if err := runtimeAssetFiles.rename(pointerFile, filepath.Join(cacheDir, pythonEnvironmentMarker)); err != nil {
			if rollbackErr := prepared.rollback(); rollbackErr != nil {
				return fmt.Errorf("publish Python environment: %v; runtime rollback: %w", err, rollbackErr)
			}
			return fmt.Errorf("publish Python environment: %w", err)
		}
		published = true
	}
	prepared.commit()
	return nil
}

func validatePythonEnvironmentsDirectory(path string) error {
	info, err := runtimeAssetFiles.lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: Python environments must be stored in a managed directory", ErrNotInstalled)
	}
	return nil
}
