package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Jail struct {
	Root string
}

var (
	pathAbs      = filepath.Abs
	pathRel      = filepath.Rel
	evalSymlinks = filepath.EvalSymlinks
	makeAllDirs  = os.MkdirAll
	pathLstat    = os.Lstat
)

func NewJail(root string) (Jail, error) {
	if root == "" {
		return Jail{}, errors.New("jail root is empty")
	}
	clean, err := pathAbs(root)
	if err != nil {
		return Jail{}, err
	}
	if err := makeAllDirs(clean, 0o700); err != nil {
		return Jail{}, err
	}
	realRoot, err := evalSymlinks(clean)
	if err != nil {
		return Jail{}, err
	}
	return Jail{Root: realRoot}, nil
}

func (j Jail) ResolveRead(path string) (string, error) {
	resolved, err := j.candidate(path)
	if err != nil {
		return "", err
	}
	if err := j.rejectSymlinkComponents(resolved, false); err != nil {
		return "", err
	}
	st, err := pathLstat(resolved)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file: %s", path)
	}
	return resolved, nil
}

func (j Jail) ResolveWrite(path string, overwrite bool) (string, error) {
	resolved, err := j.candidate(path)
	if err != nil {
		return "", err
	}
	if err := j.rejectSymlinkComponents(filepath.Dir(resolved), false); err != nil {
		return "", err
	}
	if st, err := pathLstat(resolved); err == nil {
		if !overwrite {
			return "", fmt.Errorf("path exists: %s", path)
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path contains symlink component: %s", path)
		}
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("path is not a regular file: %s", path)
		}
		return resolved, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return resolved, nil
}

func (j Jail) ResolveDir(path string) (string, error) {
	resolved, err := j.candidate(path)
	if err != nil {
		return "", err
	}
	if err := j.rejectSymlinkComponents(resolved, true); err != nil {
		return "", err
	}
	if err := makeAllDirs(resolved, 0o700); err != nil {
		return "", err
	}
	st, err := pathLstat(resolved)
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("path contains symlink component: %s", path)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", path)
	}
	return resolved, nil
}

func (j Jail) ConfinedPath(resolved string) (string, error) {
	rel, err := pathRel(j.Root, resolved)
	if err != nil {
		return "", err
	}
	if rel != "." && startsWithDotDot(rel) {
		return "", fmt.Errorf("path escapes session dir: %s", resolved)
	}
	return filepath.ToSlash(rel), nil
}

func (j Jail) candidate(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(j.Root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if !inside(j.Root, candidate) {
		return "", fmt.Errorf("path escapes session dir: %s", path)
	}
	return candidate, nil
}

func (j Jail) rejectSymlinkComponents(path string, allowMissingDirs bool) error {
	if !inside(j.Root, path) {
		return fmt.Errorf("path escapes session dir: %s", path)
	}
	rel, err := pathRel(j.Root, path)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := j.Root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		st, err := pathLstat(cur)
		if err != nil {
			if os.IsNotExist(err) && allowMissingDirs {
				return nil
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path contains symlink component: %s", path)
		}
		if !st.IsDir() && cur != path {
			return fmt.Errorf("path component is not a directory: %s", cur)
		}
	}
	return nil
}

func inside(root, path string) bool {
	rel, err := pathRel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != "" && rel != ".." && !startsWithDotDot(rel))
}

func startsWithDotDot(path string) bool {
	return len(path) >= 2 && path[:2] == ".." && (len(path) == 2 || os.IsPathSeparator(path[2]))
}
