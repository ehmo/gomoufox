package sidecar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ehmo/gomoufox/internal/safefile"
)

var (
	sidecarProcessExists = processExists
	sidecarTerminatePID  = terminatePID
)

type pidfileRecord struct {
	PID       int    `json:"pid"`
	ParentPID int    `json:"parent_pid"`
	CreatedAt string `json:"created_at"`
}

func pidfilePath(venvDir string) string {
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	return filepath.Join(venvDir, "gomoufox_sidecar.pid")
}

func pidfileDir(venvDir string) string {
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	return filepath.Join(venvDir, "gomoufox_sidecars")
}

func managedPidfilePath(venvDir string, pid int) string {
	return filepath.Join(pidfileDir(venvDir), strconv.Itoa(pid)+".pid")
}

func writePidfile(venvDir string, pid int) (string, error) {
	path := managedPidfilePath(venvDir, pid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	record := pidfileRecord{
		PID:       pid,
		ParentPID: os.Getpid(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, _ := json.Marshal(record)
	if err := safefile.WriteFile0600(path, append(data, '\n'), true); err != nil {
		return "", err
	}
	return path, nil
}

func removePidfile(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

func ReapStalePidfile(venvDir string) error {
	if err := reapLegacyPidfile(venvDir); err != nil {
		return err
	}
	dir := pidfileDir(venvDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := reapManagedPidfile(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	_ = os.Remove(dir)
	return nil
}

func reapLegacyPidfile(venvDir string) error {
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
		removePidfile(path)
		return nil
	}
	if !sidecarProcessExists(pid) {
		removePidfile(path)
	}
	return nil
}

func reapManagedPidfile(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var record pidfileRecord
	if err := json.Unmarshal(data, &record); err != nil || record.PID <= 0 {
		removePidfile(path)
		return nil
	}
	if !sidecarProcessExists(record.PID) {
		removePidfile(path)
		return nil
	}
	if record.ParentPID <= 0 || sidecarProcessExists(record.ParentPID) {
		return nil
	}
	if err := sidecarTerminatePID(record.PID); err != nil {
		return fmt.Errorf("%w: stale pid %d: %v", ErrSidecarStart, record.PID, err)
	}
	removePidfile(path)
	return nil
}
