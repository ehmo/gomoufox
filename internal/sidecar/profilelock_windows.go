//go:build windows

package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
)

type ProfileLock struct {
	file *os.File
	path string
}

func AcquireProfileLock(dir string) (*ProfileLock, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: empty profile directory", ErrProfileInUse)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(dir, "parent.lock")); err == nil {
		return nil, fmt.Errorf("%w: Firefox parent.lock exists at %s", ErrProfileInUse, dir)
	}
	path := filepath.Join(dir, ".gomoufox.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrProfileInUse, dir)
	}
	return &ProfileLock{file: file, path: path}, nil
}

func (l *ProfileLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	if err == nil {
		err = os.Remove(l.path)
	}
	return err
}

func (l *ProfileLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
