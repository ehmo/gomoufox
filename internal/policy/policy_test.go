package policy

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestCapsOnlyDecrease(t *testing.T) {
	if got, err := ClampResponseCap(0); err != nil || got != DefaultMaxResponseBytes {
		t.Fatalf("default response cap = %d, %v", got, err)
	}
	if got, err := ClampResponseCap(1024); err != nil || got != 1024 {
		t.Fatalf("lower response cap = %d, %v", got, err)
	}
	if _, err := ClampResponseCap(HardMaxResponseBytes + 1); err == nil {
		t.Fatalf("expected response hard cap rejection")
	}
	if got, err := ClampInputCap(0); err != nil || got != DefaultMaxInputBytes {
		t.Fatalf("default input cap = %d, %v", got, err)
	}
	if got, err := ClampInputCap(4096); err != nil || got != 4096 {
		t.Fatalf("lower input cap = %d, %v", got, err)
	}
	if _, err := ClampInputCap(HardMaxInputBytes + 1); err == nil {
		t.Fatalf("expected input hard cap rejection")
	}
	if got, err := ScreenshotCap(false, 1024); err != nil || got != 1024 {
		t.Fatalf("requested screenshot cap = %d, %v", got, err)
	}
	if got, err := ScreenshotCap(false, 0); err != nil || got != DefaultScreenshotBytes {
		t.Fatalf("default viewport screenshot cap = %d, %v", got, err)
	}
	if got, err := ScreenshotCap(true, 0); err != nil || got != FullPageScreenshotBytes {
		t.Fatalf("default full-page screenshot cap = %d, %v", got, err)
	}
	if _, err := ScreenshotCap(false, DefaultScreenshotBytes+1); err == nil {
		t.Fatalf("expected viewport screenshot cap rejection")
	}
	if _, err := ScreenshotCap(true, FullPageScreenshotBytes+1); err == nil {
		t.Fatalf("expected full page screenshot cap rejection")
	}
}

func TestInputCapsByKind(t *testing.T) {
	for _, tc := range []struct {
		kind InputKind
		want int
	}{
		{InputGeneral, DefaultMaxInputBytes},
		{InputScript, ScriptInputBytes},
		{InputTypedText, TypedTextInputBytes},
		{InputHeaders, HeaderInputBytes},
		{InputFetchBody, DefaultMaxInputBytes},
		{InputSessionLoadState, DefaultMaxInputBytes},
	} {
		got, err := ClampInputCapFor(tc.kind, 0)
		if err != nil || got != tc.want {
			t.Fatalf("ClampInputCapFor(%s, 0) = %d, %v; want %d", tc.kind, got, err, tc.want)
		}
	}
	if got, err := ClampInputCapFor(InputScript, 1024); err != nil || got != 1024 {
		t.Fatalf("lower configured cap should narrow script cap, got %d, %v", got, err)
	}
	if got, err := ClampInputCapFor(InputFetchBody, HardMaxInputBytes); err != nil || got != FetchBodyInputBytes {
		t.Fatalf("fetch body cap = %d, %v", got, err)
	}
	if _, err := ClampInputCapFor(InputGeneral, HardMaxInputBytes+1); err == nil {
		t.Fatalf("expected configured input cap over hard limit rejected")
	}
	if _, err := ClampInputCapFor(InputKind("bogus"), 0); err == nil {
		t.Fatalf("expected unknown input kind rejected")
	}
}

func TestValidateConfigDefaultsAndBounds(t *testing.T) {
	cfg, err := ValidateConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxResponseBytes != DefaultMaxResponseBytes {
		t.Fatalf("default response cap = %d", cfg.MaxResponseBytes)
	}
	if cfg.MaxInputBytes != DefaultMaxInputBytes {
		t.Fatalf("default input cap = %d", cfg.MaxInputBytes)
	}
	if cfg.MaxSessions != DefaultMaxSessions {
		t.Fatalf("default max sessions = %d", cfg.MaxSessions)
	}
	if cfg.SessionTTL != DefaultSessionTTL {
		t.Fatalf("default session ttl = %s", cfg.SessionTTL)
	}
	if strings.Join(cfg.AllowedSchemes, ",") != "http,https" {
		t.Fatalf("default schemes = %#v", cfg.AllowedSchemes)
	}

	for _, tc := range []Config{
		{MaxResponseBytes: HardMaxResponseBytes + 1},
		{MaxInputBytes: HardMaxInputBytes + 1},
		{MaxSessions: -1},
		{MaxSessions: HardMaxSessions + 1},
		{SessionTTL: -time.Second},
		{SessionTTL: MaxSessionTTL + time.Nanosecond},
	} {
		if _, err := ValidateConfig(tc); err == nil {
			t.Fatalf("expected invalid config rejected: %#v", tc)
		}
	}
}

func TestHasExplicitTargetScope(t *testing.T) {
	if HasExplicitTargetScope(DefaultConfig()) {
		t.Fatal("default config should not have target scope")
	}
	cfg := DefaultConfig()
	cfg.AllowedOrigins = []string{"https://example.com"}
	if !HasExplicitTargetScope(cfg) {
		t.Fatalf("allowed origin should define target scope: %#v", cfg)
	}
	cfg.AllowedOrigins = nil
	cfg.AllowedHosts = []string{"example.com"}
	if !HasExplicitTargetScope(cfg) {
		t.Fatalf("allowed host should define target scope: %#v", cfg)
	}
}

func TestCookieAndInlineSessionExportPolicy(t *testing.T) {
	allowed, err := CookieValuesAllowed(DefaultConfig(), false)
	if err != nil || allowed {
		t.Fatalf("cookie values without tool request = %v, %v", allowed, err)
	}
	if _, err := CookieValuesAllowed(DefaultConfig(), true); !errors.Is(err, ErrCookieValuesDisabled) {
		t.Fatalf("cookie values without operator opt-in err = %v", err)
	}
	cfg := DefaultConfig()
	cfg.AllowCookieValues = true
	allowed, err = CookieValuesAllowed(cfg, true)
	if err != nil || !allowed {
		t.Fatalf("cookie values with both approvals = %v, %v", allowed, err)
	}

	if _, err := InlineSessionExportAllowed(DefaultConfig(), true, 0); !errors.Is(err, ErrSessionExportDisabled) {
		t.Fatalf("inline session export without operator opt-in err = %v", err)
	}
	cfg = DefaultConfig()
	cfg.AllowSessionExport = true
	if _, err := InlineSessionExportAllowed(cfg, false, 0); !errors.Is(err, ErrSessionExportDisabled) {
		t.Fatalf("inline session export without tool opt-in err = %v", err)
	}
	if _, err := InlineSessionExportAllowed(cfg, true, InlineSessionStateBytes+1); !errors.Is(err, ErrSessionTooLarge) {
		t.Fatalf("inline session export over cap err = %v", err)
	}
	if _, err := InlineSessionExportAllowed(cfg, true, -1); !errors.Is(err, ErrSessionTooLarge) {
		t.Fatalf("inline session export negative size err = %v", err)
	}
	allowed, err = InlineSessionExportAllowed(cfg, true, InlineSessionStateBytes)
	if err != nil || !allowed {
		t.Fatalf("inline session export at cap = %v, %v", allowed, err)
	}
}

func TestTruncate(t *testing.T) {
	got, truncated := Truncate([]byte("abcdef"), 3)
	if string(got) != "abc" || !truncated {
		t.Fatalf("truncate = %q %v", got, truncated)
	}
	got, truncated = Truncate([]byte("abc"), 3)
	if string(got) != "abc" || truncated {
		t.Fatalf("unexpected truncate = %q %v", got, truncated)
	}
}

func TestRedact(t *testing.T) {
	input := `proxy=http://user:pass@example.com Authorization: Bearer abc.def Proxy-Authorization: Bearer proxy.def Cookie: session=secret Set-Cookie: auth=secret ws://localhost:1234/rawtoken wss://localhost:1234/securetoken token=secret {"cookies":[{"name":"sid","value":"cookie-secret"}],"origins":[{"origin":"https://example.com","localStorage":[{"name":"token","value":"storage-secret"}]}]}`
	out := Redact(input)
	for i, secret := range []string{"user:pass", "abc.def", "proxy.def", "session=secret", "auth=secret", "/rawtoken", "/securetoken", "token=secret", "cookie-secret", "storage-secret"} {
		if strings.Contains(out, secret) {
			t.Fatalf("redaction fixture %d survived", i)
		}
	}
	out = Redact(`"https://user:pass@example.com/path"`)
	if strings.Contains(out, "user:pass") || strings.Contains(out, "pass") {
		t.Fatalf("url password survived redaction")
	}
	out = Redact(`ws://localhost:1234/rawtoken wss://localhost:1234/securetoken`)
	if !strings.Contains(out, "ws://localhost:1234/<redacted>") || !strings.Contains(out, "wss://localhost:1234/<redacted>") {
		t.Fatalf("websocket scheme redaction = %q", out)
	}

	var b strings.Builder
	writer := NewRedactWriter(&b)
	n, err := writer.Write([]byte("Authorization: Bear"))
	if err != nil || n != len("Authorization: Bear") || b.Len() != 0 {
		t.Fatalf("split writer first n=%d err=%v out=%q", n, err, b.String())
	}
	secondChunk := "er abc.def\n" + input
	n, err = writer.Write([]byte(secondChunk))
	if err != nil || n != len(secondChunk) {
		t.Fatalf("redact writer n=%d err=%v", n, err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "cookie-secret") || strings.Contains(b.String(), "/securetoken") {
		t.Fatalf("writer leaked diagnostic fixture")
	}
	var overflow strings.Builder
	overflowWriter := NewRedactWriter(&overflow)
	longLine := strings.Repeat("x", maxRedactLineBytes+1)
	if n, err := overflowWriter.Write([]byte(longLine)); err != nil || n != len(longLine) || overflow.Len() == 0 {
		t.Fatalf("overflow writer n=%d err=%v len=%d", n, err, overflow.Len())
	}
	if err := overflowWriter.Flush(); err != nil {
		t.Fatal(err)
	}
	if n, err := NewRedactWriter(errWriter{}).Write([]byte(input + "\n")); err == nil || n != 0 {
		t.Fatalf("redact writer error n=%d err=%v", n, err)
	}
	if n, err := NewRedactWriter(errWriter{}).Write([]byte(longLine)); err == nil || n != 0 {
		t.Fatalf("redact writer overflow error n=%d err=%v", n, err)
	}
	if _, ok := RedactWriter(io.Discard).(*RedactingWriter); !ok {
		t.Fatal("RedactWriter did not return *RedactingWriter")
	}
}

func TestJailRejectsTraversalSymlinkAndNonRegular(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jail.ResolveWrite("", false); err == nil {
		t.Fatalf("expected empty write path rejected")
	}
	if _, err := jail.ResolveWrite("../escape.json", false); err == nil {
		t.Fatalf("expected traversal rejected")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := jail.ResolveWrite("link/file.json", false); err == nil {
		t.Fatalf("expected symlink escape rejected")
	}
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	insideFile := filepath.Join(inside, "state.json")
	if err := os.WriteFile(insideFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	insideLink := filepath.Join(root, "inside-link")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := jail.ResolveRead("inside-link/state.json"); err == nil {
		t.Fatalf("expected read through symlink component rejected")
	}
	if _, err := jail.ResolveWrite("inside-link/new.json", false); err == nil {
		t.Fatalf("expected write through symlink component rejected")
	}
	if _, err := jail.ResolveDir("inside-link/profile"); err == nil {
		t.Fatalf("expected dir create through symlink component rejected")
	}
	dir := filepath.Join(root, "dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := jail.ResolveRead("dir"); err == nil {
		t.Fatalf("expected directory read rejected")
	}
	fileParent := filepath.Join(root, "file-parent")
	if err := os.WriteFile(fileParent, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := jail.ResolveRead("file-parent/child.json"); err == nil {
		t.Fatalf("expected file path component rejected")
	}
}

func TestJailWriteAllowsRegularOverwriteOnlyWhenRequested(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(jail.Root, "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := jail.ResolveWrite("state.json", false); err == nil {
		t.Fatalf("expected existing file rejected without overwrite")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := jail.ResolveWrite("state.json", true); err != nil || resolved != realPath {
		t.Fatalf("overwrite resolve = %q, %v", resolved, err)
	}
	missing, err := jail.ResolveWrite("new-state.json", false)
	if err != nil {
		t.Fatalf("new write resolve failed: %v", err)
	}
	if missing != filepath.Join(jail.Root, "new-state.json") {
		t.Fatalf("new write resolved to %q", missing)
	}
	if _, err := jail.ResolveWrite("missing-dir/state.json", false); err == nil {
		t.Fatalf("expected missing parent rejected")
	}
	dir := filepath.Join(jail.Root, "profile")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := jail.ResolveWrite("profile", true); err == nil {
		t.Fatalf("expected directory overwrite rejected")
	}
	link := filepath.Join(jail.Root, "state-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := jail.ResolveWrite("state-link.json", true); err == nil {
		t.Fatalf("expected final symlink overwrite rejected")
	}
}

func TestNewJailRejectsInvalidRoots(t *testing.T) {
	if _, err := NewJail(""); err == nil {
		t.Fatalf("expected empty jail root rejected")
	}
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJail(filepath.Join(blocker, "child")); err == nil {
		t.Fatalf("expected root under regular file rejected")
	}
}

func TestJailResolveReadRequiresExistingRegularFile(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(jail.Root, "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if resolved, err := jail.ResolveRead(path); err != nil || resolved != path {
		t.Fatalf("absolute read resolve = %q, %v", resolved, err)
	}
	if _, err := jail.ResolveRead(""); err == nil {
		t.Fatalf("expected empty read path rejected")
	}
	if _, err := jail.ResolveRead("missing.json"); err == nil {
		t.Fatalf("expected missing read path rejected")
	}
}

func TestJailConfinedPathNormalizesResolvedPaths(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := jail.ConfinedPath(filepath.Join(jail.Root, "nested", "..", "state.json")); err != nil || got != "state.json" {
		t.Fatalf("confined path = %q, %v", got, err)
	}
	if got, err := jail.ConfinedPath(filepath.Join(jail.Root, "nested", "state.json")); err != nil || got != "nested/state.json" {
		t.Fatalf("nested confined path = %q, %v", got, err)
	}
	if _, err := jail.ConfinedPath(filepath.Join(filepath.Dir(jail.Root), "outside.json")); err == nil {
		t.Fatalf("expected outside confined path rejected")
	}
}

func TestJailResolveDirCreatesNestedDirectories(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := jail.ResolveDir("profiles/account-a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resolved, jail.Root+string(os.PathSeparator)) {
		t.Fatalf("resolved path escaped root: %q not under %q", resolved, jail.Root)
	}
	st, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsDir() {
		t.Fatalf("resolved path is not a directory: %s", resolved)
	}
}

func TestJailResolveDirRejectsEmptyAndFileConflicts(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jail.ResolveDir(""); err == nil {
		t.Fatalf("expected empty dir path rejected")
	}
	profiles := filepath.Join(root, "profiles")
	if err := os.WriteFile(profiles, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := jail.ResolveDir("profiles"); err == nil {
		t.Fatalf("expected file directory conflict rejected")
	}
}

func TestJailRejectSymlinkComponentsRejectsDirectEscapes(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := jail.rejectSymlinkComponents(jail.Root+"-outside", false); err == nil {
		t.Fatalf("expected direct helper escape rejected")
	}
}

func TestNewJailHookedFilesystemErrors(t *testing.T) {
	boom := errors.New("boom")
	t.Run("abs", func(t *testing.T) {
		defer restorePathHooks()()
		pathAbs = func(string) (string, error) { return "", boom }
		if _, err := NewJail("root"); !errors.Is(err, boom) {
			t.Fatalf("abs err = %v", err)
		}
	})
	t.Run("eval symlinks", func(t *testing.T) {
		defer restorePathHooks()()
		evalSymlinks = func(string) (string, error) { return "", boom }
		if _, err := NewJail(t.TempDir()); !errors.Is(err, boom) {
			t.Fatalf("eval err = %v", err)
		}
	})
}

func TestJailHookedResolveErrors(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")

	t.Run("read final lstat", func(t *testing.T) {
		defer restorePathHooks()()
		path := filepath.Join(jail.Root, "state.json")
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		calls := 0
		pathLstat = func(name string) (os.FileInfo, error) {
			calls++
			if calls == 1 {
				return os.Lstat(name)
			}
			return nil, boom
		}
		if _, err := jail.ResolveRead("state.json"); !errors.Is(err, boom) {
			t.Fatalf("read err = %v", err)
		}
	})

	t.Run("write target lstat", func(t *testing.T) {
		defer restorePathHooks()()
		pathLstat = func(string) (os.FileInfo, error) { return nil, boom }
		if _, err := jail.ResolveWrite("new.json", false); !errors.Is(err, boom) {
			t.Fatalf("write err = %v", err)
		}
	})

	t.Run("dir final lstat error", func(t *testing.T) {
		defer restorePathHooks()()
		calls := 0
		pathLstat = func(string) (os.FileInfo, error) {
			calls++
			if calls == 1 {
				return nil, os.ErrNotExist
			}
			return nil, boom
		}
		if _, err := jail.ResolveDir("dir-lstat-error"); !errors.Is(err, boom) {
			t.Fatalf("dir lstat err = %v", err)
		}
	})

	t.Run("dir final symlink", func(t *testing.T) {
		defer restorePathHooks()()
		calls := 0
		pathLstat = func(string) (os.FileInfo, error) {
			calls++
			if calls == 1 {
				return nil, os.ErrNotExist
			}
			return fakeFileInfo{mode: os.ModeSymlink}, nil
		}
		if _, err := jail.ResolveDir("dir-symlink"); err == nil {
			t.Fatalf("expected final symlink rejected")
		}
	})

	t.Run("dir final not directory", func(t *testing.T) {
		defer restorePathHooks()()
		calls := 0
		pathLstat = func(string) (os.FileInfo, error) {
			calls++
			if calls == 1 {
				return nil, os.ErrNotExist
			}
			return fakeFileInfo{mode: 0}, nil
		}
		if _, err := jail.ResolveDir("dir-file"); err == nil {
			t.Fatalf("expected final non-directory rejected")
		}
	})
}

func TestJailHookedRelAndInsideEdges(t *testing.T) {
	root := t.TempDir()
	jail, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")

	t.Run("reject rel error", func(t *testing.T) {
		defer restorePathHooks()()
		calls := 0
		pathRel = func(root, path string) (string, error) {
			calls++
			if calls == 1 {
				return ".", nil
			}
			return "", boom
		}
		if err := jail.rejectSymlinkComponents(jail.Root, false); !errors.Is(err, boom) {
			t.Fatalf("rel err = %v", err)
		}
	})

	t.Run("skip empty and dot parts", func(t *testing.T) {
		defer restorePathHooks()()
		if err := os.MkdirAll(filepath.Join(jail.Root, "a", "b"), 0o700); err != nil {
			t.Fatal(err)
		}
		calls := 0
		pathRel = func(root, path string) (string, error) {
			calls++
			if calls == 1 {
				return "a/b", nil
			}
			return "a//./b", nil
		}
		if err := jail.rejectSymlinkComponents(filepath.Join(jail.Root, "a", "b"), false); err != nil {
			t.Fatalf("skip parts err = %v", err)
		}
	})

	t.Run("inside rel error", func(t *testing.T) {
		defer restorePathHooks()()
		pathRel = func(root, path string) (string, error) { return "", boom }
		if inside(jail.Root, filepath.Join(jail.Root, "x")) {
			t.Fatalf("inside should reject rel errors")
		}
	})

	t.Run("confined path rel error", func(t *testing.T) {
		defer restorePathHooks()()
		pathRel = func(root, path string) (string, error) { return "", boom }
		if _, err := jail.ConfinedPath(filepath.Join(jail.Root, "state.json")); !errors.Is(err, boom) {
			t.Fatalf("confined rel err = %v", err)
		}
	})
}

func restorePathHooks() func() {
	oldAbs := pathAbs
	oldRel := pathRel
	oldEval := evalSymlinks
	oldMkdirAll := makeAllDirs
	oldLstat := pathLstat
	return func() {
		pathAbs = oldAbs
		pathRel = oldRel
		evalSymlinks = oldEval
		makeAllDirs = oldMkdirAll
		pathLstat = oldLstat
	}
}

type fakeFileInfo struct {
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }
