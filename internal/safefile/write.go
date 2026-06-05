package safefile

import (
	"fmt"
	"io"
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
	fileLink   = os.Link
	fileLstat  = os.Lstat
)

// WriteFile0600 atomically writes data with mode 0600. When overwrite is true,
// existing regular files may be replaced, but final symlinks are never followed.
func WriteFile0600(path string, data []byte, overwrite bool) error {
	if err := checkExisting(path, overwrite); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := fileMkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := checkExisting(path, overwrite); err != nil {
		return err
	}
	tmp, err := fileCreateTemp(dir, ".gomoufox-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = fileRemove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if n, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	} else if n != len(data) {
		_ = tmp.Close()
		return io.ErrShortWrite
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !overwrite {
		if err := fileLink(tmpName, path); err != nil {
			if os.IsExist(err) {
				return fmt.Errorf("path exists: %s: %w", path, os.ErrExist)
			}
			return err
		}
		return nil
	}
	if err := checkExisting(path, true); err != nil {
		return err
	}
	return fileRename(tmpName, path)
}

func checkExisting(path string, overwrite bool) error {
	if st, err := fileLstat(path); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through symlink: %s", path)
		}
		if st.IsDir() {
			return fmt.Errorf("path is a directory: %s", path)
		}
		if !overwrite {
			return fmt.Errorf("path exists: %s: %w", path, os.ErrExist)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
