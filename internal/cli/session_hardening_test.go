package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type stalledSnapshotPage struct {
	*fakeGomoufoxPage
	entered chan context.Context
	release chan struct{}
	done    chan struct{}
}

func (p stalledSnapshotPage) Evaluate(ctx context.Context, _ string, _ ...any) (any, error) {
	defer close(p.done)
	p.entered <- ctx
	select {
	case <-ctx.Done():
		_ = p.Close()
		return nil, ctx.Err()
	case <-p.release:
		return nil, nil
	}
}

func TestSnapshotTimeoutKeepsInteractivePageOpen(t *testing.T) {
	old := sessionSnapshotTimeout
	sessionSnapshotTimeout = 10 * time.Millisecond
	t.Cleanup(func() { sessionSnapshotTimeout = old })
	p := stalledSnapshotPage{
		fakeGomoufoxPage: &fakeGomoufoxPage{raw: &fakePlaywrightPageRaw{context: &fakePlaywrightStorageContext{}}},
		entered:          make(chan context.Context, 1), release: make(chan struct{}), done: make(chan struct{}),
	}
	defer func() { close(p.release); <-p.done }()
	_, err := boundedSessionSnapshot(context.Background(), realLocalPage{p: p})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("snapshot error = %v", err)
	}
	evaluationContext := <-p.entered
	if evaluationContext.Err() != nil || evaluationContext.Done() != nil {
		t.Fatal("background snapshot can cancel and close the interactive page")
	}
}

func TestBoundedSnapshotRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := boundedSessionSnapshot(ctx, &fakeLocalPage{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot error = %v", err)
	}
}

func TestInputReadChecksOpenedDescriptor(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := localOpenFile
	t.Cleanup(func() { localOpenFile = old })
	for _, kind := range []string{"closed", "directory"} {
		t.Run(kind, func(t *testing.T) {
			localOpenFile = func(string) (*os.File, error) {
				if kind == "directory" {
					return os.Open(base)
				}
				f, err := os.Open(path)
				if err == nil {
					_ = f.Close()
				}
				return f, err
			}
			if _, err := readCappedFileBytes(path, 1024); err == nil {
				t.Fatal("invalid opened descriptor accepted")
			}
		})
	}
}

type faultInputRoot struct {
	*os.Root
	open  func(string) (*os.File, error)
	child func(string) (*os.Root, error)
}

func (r faultInputRoot) Open(path string) (*os.File, error)     { return r.open(path) }
func (r faultInputRoot) OpenRoot(path string) (*os.Root, error) { return r.child(path) }

func TestDaemonInputRejectsAcquisitionFailures(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "input"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.json", "other.json"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := localOpenInputRoot
	t.Cleanup(func() { localOpenInputRoot = old })
	for _, kind := range []string{"outside", "missing root", "directory", "parent open", "parent closed", "parent changed", "file open", "file closed", "file changed"} {
		t.Run(kind, func(t *testing.T) {
			localOpenInputRoot = old
			req := LocalCommandRequest{inputRoot: base}
			path := filepath.Join(base, "state.json")
			switch kind {
			case "outside":
				path = filepath.Join(t.TempDir(), "state.json")
			case "missing root":
				req.inputRoot = filepath.Join(base, "missing")
				path = filepath.Join(req.inputRoot, "state.json")
			case "directory":
				path = filepath.Join(base, "input")
			default:
				root, err := os.OpenRoot(base)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				wrapped := faultInputRoot{Root: root, open: root.Open, child: root.OpenRoot}
				if kind == "parent open" || kind == "parent closed" || kind == "parent changed" {
					path = filepath.Join(base, "input", "state.json")
					wrapped.child = func(string) (*os.Root, error) {
						if kind == "parent open" {
							return nil, os.ErrPermission
						}
						other, err := os.OpenRoot(base)
						if err == nil && kind == "parent closed" {
							_ = other.Close()
						}
						return other, err
					}
				} else {
					wrapped.open = func(string) (*os.File, error) {
						if kind == "file open" {
							return nil, os.ErrPermission
						}
						f, err := os.Open(filepath.Join(base, "other.json"))
						if err == nil && kind == "file closed" {
							_ = f.Close()
						}
						return f, err
					}
				}
				localOpenInputRoot = func(string) (localInputRoot, error) { return wrapped, nil }
			}
			if file, err := openInputFile(req, path); err == nil {
				_ = file.Close()
				t.Fatal("unsafe input acquisition accepted")
			}
		})
	}
}
