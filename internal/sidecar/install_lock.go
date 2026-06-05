package sidecar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var errInstallLockBusy = errors.New("install lock busy")

var tryInstallFileLockForAcquire = tryInstallFileLock

type InstallLock struct {
	file *os.File
	path string
}

func acquireInstallLock(ctx context.Context, venvDir string) (*InstallLock, error) {
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	if err := os.MkdirAll(venvDir, 0o700); err != nil {
		return nil, err
	}
	path := installLockPath(venvDir)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = tryInstallFileLockForAcquire(file)
		if err == nil {
			return &InstallLock{file: file, path: path}, nil
		}
		if !errors.Is(err, errInstallLockBusy) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("%w: install lock: %v", ErrTimeout, ctx.Err())
		case <-ticker.C:
		}
	}
}

func installLockPath(venvDir string) string {
	if venvDir == "" {
		venvDir = DefaultCacheDir()
	}
	return filepath.Join(venvDir, ".gomoufox-install.lock")
}

func (l *InstallLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockInstallFile(l.file)
	if closeErr := l.file.Close(); err == nil {
		err = closeErr
	}
	l.file = nil
	return err
}

func (l *InstallLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
