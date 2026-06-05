package safefile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWriteFile0600WritesAtomicallyAndHonorsOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "out.txt")
	if err := WriteFile0600(path, []byte("one"), false); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "one" {
		t.Fatalf("data = %q err=%v", data, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v err=%v", st, err)
	}
	if err := WriteFile0600(path, []byte("two"), false); err == nil {
		t.Fatal("no-overwrite write succeeded")
	}
	if err := WriteFile0600(path, []byte("two"), true); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "two" {
		t.Fatalf("overwrite data = %q err=%v", data, err)
	}
}

func TestWriteFile0600RejectsDirectoryAndFinalSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile0600(dir, []byte("x"), true); err == nil {
		t.Fatal("directory overwrite succeeded")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup requires elevated privileges on some Windows systems")
	}
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := WriteFile0600(link, []byte("replacement"), true); err == nil {
		t.Fatal("symlink overwrite succeeded")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "target" {
		t.Fatalf("symlink target data = %q err=%v", data, err)
	}
	st, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced: mode=%v", st.Mode())
	}
}

func TestWriteFile0600RejectsParentFileAndNonOverwriteExistingFile(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile0600(filepath.Join(parent, "child"), []byte("x"), false); err == nil {
		t.Fatal("write under file parent succeeded")
	}

	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile0600(existing, []byte("new"), false); err == nil {
		t.Fatal("no-overwrite existing write succeeded")
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "old" {
		t.Fatalf("existing data = %q err=%v", data, err)
	}
}

func TestWriteFile0600HookedErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name string
		run  func(string) error
		want error
	}{
		{
			name: "mkdir",
			run: func(path string) error {
				fileMkdirAll = func(string, os.FileMode) error { return boom }
				return WriteFile0600(path, []byte("x"), true)
			},
			want: boom,
		},
		{
			name: "create",
			run: func(path string) error {
				fileCreateTemp = func(string, string) (atomicFile, error) { return nil, boom }
				return WriteFile0600(path, []byte("x"), true)
			},
			want: boom,
		},
		{
			name: "chmod",
			run: func(path string) error {
				fileCreateTemp = func(string, string) (atomicFile, error) {
					return &fakeAtomicFile{name: filepath.Join(filepath.Dir(path), "tmp"), chmodErr: boom}, nil
				}
				return WriteFile0600(path, []byte("x"), true)
			},
			want: boom,
		},
		{
			name: "write",
			run: func(path string) error {
				fileCreateTemp = func(string, string) (atomicFile, error) {
					return &fakeAtomicFile{name: filepath.Join(filepath.Dir(path), "tmp"), writeErr: boom}, nil
				}
				return WriteFile0600(path, []byte("x"), true)
			},
			want: boom,
		},
		{
			name: "short write",
			run: func(path string) error {
				fileCreateTemp = func(string, string) (atomicFile, error) {
					return &fakeAtomicFile{name: filepath.Join(filepath.Dir(path), "tmp"), shortWrite: true}, nil
				}
				return WriteFile0600(path, []byte("x"), true)
			},
			want: io.ErrShortWrite,
		},
		{
			name: "close",
			run: func(path string) error {
				fileCreateTemp = func(string, string) (atomicFile, error) {
					return &fakeAtomicFile{name: filepath.Join(filepath.Dir(path), "tmp"), closeErr: boom}, nil
				}
				return WriteFile0600(path, []byte("x"), true)
			},
			want: boom,
		},
		{
			name: "link exists race",
			run: func(path string) error {
				fileCreateTemp = func(string, string) (atomicFile, error) {
					return &fakeAtomicFile{name: filepath.Join(filepath.Dir(path), "tmp")}, nil
				}
				fileLink = func(string, string) error { return os.ErrExist }
				return WriteFile0600(path, []byte("x"), false)
			},
			want: nil,
		},
		{
			name: "link",
			run: func(path string) error {
				fileCreateTemp = func(string, string) (atomicFile, error) {
					return &fakeAtomicFile{name: filepath.Join(filepath.Dir(path), "tmp")}, nil
				}
				fileLink = func(string, string) error { return boom }
				return WriteFile0600(path, []byte("x"), false)
			},
			want: boom,
		},
		{
			name: "rename",
			run: func(path string) error {
				fileCreateTemp = func(string, string) (atomicFile, error) {
					return &fakeAtomicFile{name: filepath.Join(filepath.Dir(path), "tmp")}, nil
				}
				fileRename = func(string, string) error { return boom }
				return WriteFile0600(path, []byte("x"), true)
			},
			want: boom,
		},
		{
			name: "lstat before mkdir",
			run: func(path string) error {
				fileLstat = func(string) (os.FileInfo, error) { return nil, boom }
				return WriteFile0600(path, []byte("x"), true)
			},
			want: boom,
		},
		{
			name: "lstat after mkdir",
			run: func(path string) error {
				calls := 0
				fileLstat = func(string) (os.FileInfo, error) {
					calls++
					if calls == 2 {
						return nil, boom
					}
					return nil, os.ErrNotExist
				}
				return WriteFile0600(path, []byte("x"), true)
			},
			want: boom,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer restoreFileHooks()()
			err := tc.run(filepath.Join(t.TempDir(), "out"))
			if tc.want == nil {
				if err == nil || !errors.Is(err, os.ErrExist) || err.Error() == os.ErrExist.Error() {
					t.Fatalf("err = %v, want wrapped exists path error", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestWriteFile0600RechecksBeforeOverwriteRename(t *testing.T) {
	defer restoreFileHooks()()
	calls := 0
	fileLstat = func(string) (os.FileInfo, error) {
		calls++
		if calls == 3 {
			return fakeFileInfo{mode: os.ModeSymlink}, nil
		}
		return nil, os.ErrNotExist
	}
	fileCreateTemp = func(dir, pattern string) (atomicFile, error) {
		return &fakeAtomicFile{name: filepath.Join(dir, "tmp")}, nil
	}
	err := WriteFile0600(filepath.Join(t.TempDir(), "out"), []byte("x"), true)
	if err == nil || !strings.Contains(err.Error(), "refusing to write through symlink") {
		t.Fatalf("err = %v", err)
	}
}

func restoreFileHooks() func() {
	oldMkdirAll := fileMkdirAll
	oldCreateTemp := fileCreateTemp
	oldRemove := fileRemove
	oldRename := fileRename
	oldLink := fileLink
	oldLstat := fileLstat
	return func() {
		fileMkdirAll = oldMkdirAll
		fileCreateTemp = oldCreateTemp
		fileRemove = oldRemove
		fileRename = oldRename
		fileLink = oldLink
		fileLstat = oldLstat
	}
}

type fakeAtomicFile struct {
	name       string
	chmodErr   error
	writeErr   error
	closeErr   error
	shortWrite bool
}

func (f *fakeAtomicFile) Name() string { return f.name }
func (f *fakeAtomicFile) Chmod(os.FileMode) error {
	return f.chmodErr
}
func (f *fakeAtomicFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.shortWrite {
		return len(p) - 1, nil
	}
	return len(p), nil
}
func (f *fakeAtomicFile) Close() error { return f.closeErr }

type fakeFileInfo struct {
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }
