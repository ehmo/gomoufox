package sidecar

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	camoufoxRequirementsLock = "requirements/camoufox.txt"
	pipRequirementsLock      = "requirements/pip.txt"
)

//go:embed requirements/*.txt
var pythonRequirements embed.FS

var (
	readPythonRequirementsLock  = pythonRequirements.ReadFile
	mkdirPythonRequirementsTemp = os.MkdirTemp
	writePythonRequirementsFile = os.WriteFile
	removePythonRequirementsAll = os.RemoveAll
)

func materializePythonRequirementsLock(name string) (string, func(), error) {
	data, err := readPythonRequirementsLock(name)
	if err != nil {
		return "", func() {}, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", func() {}, fmt.Errorf("empty Python requirements lock: %s", name)
	}
	dir, err := mkdirPythonRequirementsTemp("", "gomoufox-"+strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))+"-*")
	if err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(dir, filepath.Base(name))
	if err := writePythonRequirementsFile(path, data, 0o600); err != nil {
		_ = removePythonRequirementsAll(dir)
		return "", func() {}, err
	}
	return path, func() { _ = removePythonRequirementsAll(dir) }, nil
}

func hashLockedPipInstallArgs(lockFile string, upgrade bool) []string {
	args := []string{"install", "--disable-pip-version-check"}
	if upgrade {
		args = append(args, "--upgrade")
	}
	return append(args, "--require-hashes", "--only-binary=:all:", "-r", lockFile)
}
