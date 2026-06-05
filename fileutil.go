package gomoufox

import (
	"os"
	"path/filepath"
)

type atomicFile interface {
	Name() string
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Close() error
}

var (
	fileMkdirAll   = os.MkdirAll
	fileCreateTemp = func(dir, pattern string) (atomicFile, error) {
		return os.CreateTemp(dir, pattern)
	}
	fileRemove = os.Remove
	fileRename = os.Rename
)

func writeBytes0600(path string, data []byte) error {
	if err := fileMkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := fileCreateTemp(filepath.Dir(path), ".gomoufox-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = fileRemove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return fileRename(tmpName, path)
}
