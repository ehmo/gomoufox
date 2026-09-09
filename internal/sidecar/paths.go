package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
)

func VenvPython(venvDir string) (string, error) {
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	environment, err := pythonEnvironmentDir(venvDir)
	if err != nil {
		return "", err
	}
	venvDir = environment
	var python string
	if sidecarGOOS == "windows" {
		python = filepath.Join(venvDir, "Scripts", "python.exe")
	} else {
		python = filepath.Join(venvDir, "bin", "python")
	}
	if st, err := os.Stat(python); err != nil || st.IsDir() {
		return "", fmt.Errorf("%w: venv python not found at %s", ErrNotInstalled, python)
	}
	return python, nil
}

func VenvPip(venvDir string) (string, error) {
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	environment, err := pythonEnvironmentDir(venvDir)
	if err != nil {
		return "", err
	}
	venvDir = environment
	if sidecarGOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "pip.exe"), nil
	}
	return filepath.Join(venvDir, "bin", "pip"), nil
}
