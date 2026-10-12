package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
