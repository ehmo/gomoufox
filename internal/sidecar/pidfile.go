package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	sidecarProcessExists = processExists
	sidecarTerminatePID  = terminatePID
)

func pidfilePath(venvDir string) string {
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	return filepath.Join(venvDir, "gomoufox_sidecar.pid")
}

func writePidfile(venvDir string, pid int) error {
	path := pidfilePath(venvDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func removePidfile(venvDir string) {
	_ = os.Remove(pidfilePath(venvDir))
}

func ReapStalePidfile(venvDir string) error {
	path := pidfilePath(venvDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		removePidfile(venvDir)
		return nil
	}
	if sidecarProcessExists(pid) {
		if err := sidecarTerminatePID(pid); err != nil {
			return fmt.Errorf("%w: stale pid %d: %v", ErrSidecarStart, pid, err)
		}
	}
	removePidfile(venvDir)
	return nil
}
