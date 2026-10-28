package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/daemon"
)

func TestBrowserCommandsAcceptCookiesFile(t *testing.T) {
	for _, args := range [][]string{
		{"get", "https://example.com", "--cookies-file", "state.json"},
		{"screenshot", "https://example.com", "--out", "page.png", "--cookies-file", "state.json"},
		{"eval", "https://example.com", "--enable-eval", "--script", "() => 1", "--cookies-file", "state.json"},
	} {
		t.Run(args[0], func(t *testing.T) {
			var got LocalCommandRequest
			var stderr bytes.Buffer
			runner := Runner{Hooks: Hooks{LocalCommand: func(_ context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
				got = req
				return LocalCommandResponse{ExitCode: ExitOK}, nil
			}}}
			if code := runner.Run(context.Background(), args, Streams{Stdout: &bytes.Buffer{}, Stderr: &stderr}); code != ExitOK {
				t.Fatalf("code = %d stderr = %q", code, stderr.String())
			}
			if flagString(got, "cookies_file", "") != "state.json" {
				t.Fatalf("flags = %v", got.Flags)
			}

			stderr.Reset()
			withProfile := append(append([]string{}, args...), "--profile", "/profile")
			if code := runner.Run(context.Background(), withProfile, Streams{Stderr: &stderr}); code != ExitUsage || !strings.Contains(stderr.String(), "mutually exclusive") {
				t.Fatalf("--profile code = %d stderr = %q", code, stderr.String())
			}
		})
	}
}

// The daemon must not read storage state from outside its session directory.
func TestDaemonConfinesCookiesFile(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "state.json")
	for _, path := range []string{outside, filepath.Join(root, "state.json")} {
		if err := os.WriteFile(path, []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []struct {
		verb  string
		flags map[string]any
	}{
		{verb: "get"},
		{verb: "screenshot", flags: map[string]any{"out": "page.png"}},
		{verb: "eval", flags: map[string]any{"enable_eval": true, "script": "() => 1"}},
	} {
		t.Run(command.verb, func(t *testing.T) {
			flags := map[string]any{"cookies_file": outside}
			for k, v := range command.flags {
				flags[k] = v
			}
			req := LocalCommandRequest{Command: command.verb, Flags: flags}
			if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err == nil {
				t.Fatalf("outside cookies file accepted: %v", req.Flags)
			}

			result := executeDaemonLocalCommand(context.Background(), ServeRequest{SessionDir: root}, command.verb, daemon.Envelope{Args: []string{"http://93.184.216.34/"}, Flags: flags})
			assertDaemonPathRejected(t, result, root)

			req = LocalCommandRequest{Command: command.verb, Flags: command.flags}
			if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err != nil || flagString(req, "cookies_file", "") != "" {
				t.Fatalf("no cookies file: err = %v flags = %v", err, req.Flags)
			}

			req = LocalCommandRequest{Command: command.verb, Flags: map[string]any{"cookies_file": "state.json"}}
			if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err != nil {
				t.Fatal(err)
			}
			if resolved := flagString(req, "cookies_file", ""); !filepath.IsAbs(resolved) || filepath.Base(resolved) != "state.json" {
				t.Fatalf("resolved cookies file = %q", resolved)
			}
		})
	}

	// Verbs without session paths pass through unchanged.
	req := LocalCommandRequest{Command: "open", Flags: map[string]any{}}
	if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err != nil || len(req.Flags) != 0 {
		t.Fatalf("open: err = %v flags = %v", err, req.Flags)
	}
}

// Daemon screenshots must write only inside the session directory, and
// daemon profiles must stay under its profiles subdirectory.
func TestDaemonConfinesScreenshotAndProfile(t *testing.T) {
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := LocalCommandRequest{Command: "screenshot", Flags: map[string]any{"out": outside}}
	if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err == nil {
		t.Fatalf("outside screenshot out accepted: %v", req.Flags)
	}
	result := executeDaemonLocalCommand(context.Background(), ServeRequest{SessionDir: root}, "screenshot", daemon.Envelope{Args: []string{"http://93.184.216.34/"}, Flags: map[string]any{"out": outside}})
	assertDaemonPathRejected(t, result, root)
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("outside file changed: %q %v", data, err)
	}

	req = LocalCommandRequest{Command: "screenshot", Flags: map[string]any{"out": "page.png"}}
	if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err != nil {
		t.Fatal(err)
	}
	if got := flagString(req, "out", ""); got != filepath.Join(realRoot, "page.png") || req.DisplayOut != "page.png" {
		t.Fatalf("out = %q display = %q", got, req.DisplayOut)
	}

	// Screenshots overwrite an existing file inside the jail, as the CLI does.
	if err := os.WriteFile(filepath.Join(root, "page.png"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	req = LocalCommandRequest{Command: "screenshot", Flags: map[string]any{"out": "page.png"}}
	if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err != nil {
		t.Fatalf("existing in-jail out rejected: %v", err)
	}

	outsideProfile := t.TempDir()
	for _, verb := range []string{"get", "screenshot", "eval"} {
		req = LocalCommandRequest{Command: verb, Flags: map[string]any{}, Profile: outsideProfile}
		if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err == nil {
			t.Fatalf("%s: outside profile accepted: %q", verb, req.Profile)
		}

		req = LocalCommandRequest{Command: verb, Flags: map[string]any{}, Profile: "work"}
		if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		if want := filepath.Join(realRoot, "profiles", "work"); req.Profile != want {
			t.Fatalf("%s: profile = %q, want %q", verb, req.Profile, want)
		}
	}
	result = executeDaemonLocalCommand(context.Background(), ServeRequest{SessionDir: root}, "get", daemon.Envelope{Args: []string{"http://93.184.216.34/"}, Profile: outsideProfile})
	assertDaemonPathRejected(t, result, root, outsideProfile)

	// A file where the profiles directory belongs fails closed.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "profiles"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	req = LocalCommandRequest{Command: "get", Flags: map[string]any{}, Profile: "work"}
	if err := jailDaemonSessionPaths(ServeRequest{SessionDir: blocked}, &req); err == nil {
		t.Fatalf("profile accepted without a profiles directory: %q", req.Profile)
	}
}

// Daemon eval script files stay in the session directory, and no daemon file
// flag can read or write inside the profiles directory.
func TestDaemonReservesProfilesDir(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.js")
	if err := os.WriteFile(outside, []byte("() => 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "profiles", "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"script.js", "profiles/work/cookies.sqlite"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("() => 1"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	req := LocalCommandRequest{Command: "eval", Flags: map[string]any{"script_file": outside}}
	if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err == nil {
		t.Fatalf("outside script file accepted: %v", req.Flags)
	}
	result := executeDaemonLocalCommand(context.Background(), ServeRequest{SessionDir: root, EnableEval: true}, "eval", daemon.Envelope{Args: []string{"http://93.184.216.34/"}, Flags: map[string]any{"script_file": "/nonexistent/x.js"}})
	assertDaemonPathRejected(t, result, root)

	req = LocalCommandRequest{Command: "eval", Flags: map[string]any{"script_file": "script.js"}}
	if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err != nil || !filepath.IsAbs(flagString(req, "script_file", "")) {
		t.Fatalf("in-jail script file: err = %v flags = %v", err, req.Flags)
	}

	for _, req := range []LocalCommandRequest{
		{Command: "eval", Flags: map[string]any{"script_file": "profiles/work/cookies.sqlite"}},
		{Command: "get", Flags: map[string]any{"cookies_file": "profiles/work/cookies.sqlite"}},
		{Command: "screenshot", Flags: map[string]any{"out": "profiles/work/user.js"}},
		{Command: "fetch", Flags: map[string]any{"data_file": "profiles/work/cookies.sqlite"}},
		{Command: "fetch", Flags: map[string]any{"form_file": []string{"f=profiles/work/cookies.sqlite"}}},
		{Command: "fetch", Flags: map[string]any{"out": "profiles/work/user.js", "overwrite": true}},
		{Command: "session import", Flags: map[string]any{"file": "profiles/work/cookies.sqlite", "out": "copy.json"}},
		{Command: "session import", Flags: map[string]any{"file": "script.js", "out": "profiles/work/state.json"}},
		{Command: "session export", Flags: map[string]any{"out": "profiles/work/state.json"}, Profile: "work"},
	} {
		if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err == nil {
			t.Fatalf("%s: profiles path accepted: %v", req.Command, req.Flags)
		}
	}
}

// A profiles symlink must not move the profile root out of the session directory.
func TestDaemonRejectsProfilesSymlink(t *testing.T) {
	for _, command := range []string{"get", "session export"} {
		root := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "profiles")); err != nil {
			t.Fatal(err)
		}
		req := LocalCommandRequest{Command: command, Flags: map[string]any{}, Profile: "work"}
		if command == "session export" {
			req.Flags["out"] = "state.json"
		}
		if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err == nil {
			t.Fatalf("%s: profiles symlink accepted: %q", command, req.Profile)
		}
	}
}

func TestDaemonFetchConfinesProfile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	req := LocalCommandRequest{Command: "fetch", Profile: outside, Flags: map[string]any{}}
	if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err == nil {
		t.Fatalf("outside profile accepted: %q", req.Profile)
	}
	req.Profile = "work"
	if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(realRoot, "profiles", "work"); req.Profile != want {
		t.Fatalf("profile = %q, want %q", req.Profile, want)
	}
}

func TestDaemonRejectsCookiesFileWithProfile(t *testing.T) {
	t.Cleanup(fakeOpenPage(t, &fakeLocalPage{}))
	for _, command := range []string{"get", "screenshot", "fetch", "eval"} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			flags := map[string]any{"cookies_file": "state.json"}
			if command == "screenshot" {
				flags["out"] = "page.png"
			}
			if command == "eval" {
				flags["enable_eval"] = true
				flags["script"] = "() => 1"
			}
			result := executeDaemonLocalCommand(context.Background(), ServeRequest{SessionDir: root, EnableEval: true}, command, daemon.Envelope{
				Args: []string{"http://93.184.216.34/"}, Profile: "work", Flags: flags,
			})
			if result.ExitCode != ExitUsage || !strings.Contains(result.Stderr, "mutually exclusive") {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestDaemonInputAcquisitionRejectsSymlinkSwap(t *testing.T) {
	for _, insideProfiles := range []bool{false, true} {
		for _, parentSwap := range []bool{false, true} {
			for _, command := range []string{"get", "eval", "fetch", "multipart", "session import"} {
				t.Run(command+map[bool]string{false: "/file", true: "/parent"}[parentSwap]+map[bool]string{false: "/outside", true: "/profiles"}[insideProfiles], func(t *testing.T) {
					root := t.TempDir()
					outside := t.TempDir()
					if insideProfiles {
						outside = filepath.Join(root, "profiles", "work")
						if err := os.MkdirAll(outside, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					inputDir := filepath.Join(root, "input")
					if err := os.Mkdir(inputDir, 0o700); err != nil {
						t.Fatal(err)
					}
					for _, dir := range []string{inputDir, outside} {
						if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					req := LocalCommandRequest{Command: command, Flags: map[string]any{}}
					switch command {
					case "get":
						req.Flags["cookies_file"] = "input/state.json"
					case "eval":
						req.Flags["script_file"] = "input/state.json"
					case "fetch":
						req.Flags["data_file"] = "input/state.json"
					case "multipart":
						req.Command = "fetch"
						req.Flags["form_file"] = []string{"file=input/state.json"}
					case "session import":
						req.Flags["file"] = "input/state.json"
						req.Flags["out"] = "copy.json"
					}
					if err := jailDaemonSessionPaths(ServeRequest{SessionDir: root}, &req); err != nil {
						t.Fatal(err)
					}
					link, target := filepath.Join(inputDir, "state.json"), filepath.Join(outside, "state.json")
					if parentSwap {
						link, target = inputDir, outside
						if err := os.Rename(inputDir, inputDir+"-old"); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, link); err != nil {
						t.Fatal(err)
					}
					var err error
					switch command {
					case "get":
						_, err = contextOptionsForLocal(req, nil)
					case "eval":
						_, err = scriptFromFlags(req)
					case "fetch":
						_, err = bodyFromFlags(req)
					case "multipart":
						_, _, err = multipartBody(req)
					case "session import":
						_, err = localSessionImport(req)
					}
					if err == nil {
						t.Fatal("input escaped the session directory after validation")
					}
				})
			}
		}
	}
}

type blockingSessionSnapshotPage struct {
	*fakeLocalPage
	snapshotFunc func(context.Context) (*gomoufox.StorageState, error)
}

func (p blockingSessionSnapshotPage) SessionSnapshot(ctx context.Context) (*gomoufox.StorageState, error) {
	return p.snapshotFunc(ctx)
}

func TestWaitClosedSnapshotsBoundsBlockedSnapshot(t *testing.T) {
	oldTimeout, oldInterval := sessionSnapshotTimeout, sessionSnapshotInterval
	sessionSnapshotTimeout, sessionSnapshotInterval = 10*time.Millisecond, time.Millisecond
	t.Cleanup(func() { sessionSnapshotTimeout, sessionSnapshotInterval = oldTimeout, oldInterval })
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{}, 1)
	var calls atomic.Int32
	page := blockingSessionSnapshotPage{fakeLocalPage: &fakeLocalPage{closeWait: make(chan struct{})}, snapshotFunc: func(ctx context.Context) (*gomoufox.StorageState, error) {
		calls.Add(1)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("snapshot has no deadline")
		}
		entered <- struct{}{}
		<-release
		return nil, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := waitClosedSnapshots(ctx, page); done <- err }()
	<-entered
	time.Sleep(30 * time.Millisecond)
	close(page.closeWait)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("closed waiter was stalled by SessionSnapshot")
	}
	if calls.Load() != 1 {
		t.Fatalf("started %d blocked snapshots", calls.Load())
	}
}

func TestWaitClosedSnapshotsReturnsOuterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	page := blockingSessionSnapshotPage{fakeLocalPage: &fakeLocalPage{closeWait: make(chan struct{})}, snapshotFunc: func(ctx context.Context) (*gomoufox.StorageState, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, err := waitClosedSnapshots(ctx, page)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
