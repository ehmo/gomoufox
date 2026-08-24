package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillreg "github.com/ehmo/gomoufox/internal/skills"
)

type failingAgentReader struct{}

func (failingAgentReader) Read([]byte) (int, error) { return 0, errors.New("injected read failure") }

func TestInstallDryRunAllDedupesSharedSkillPaths(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	realHome, err := canonicalRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Install(Options{
		Target:  TargetAll,
		Scope:   ScopeUser,
		DryRun:  true,
		HomeDir: home,
		WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Target != TargetAll || plan.Scope != ScopeUser || plan.Toolset != DefaultToolset || !plan.DryRun {
		t.Fatalf("plan metadata = %#v", plan)
	}
	if len(plan.Actions) == 0 {
		t.Fatal("empty dry-run plan")
	}
	seen := map[string]bool{}
	for _, action := range plan.Actions {
		if seen[action.Path] {
			t.Fatalf("duplicate action path: %s", action.Path)
		}
		seen[action.Path] = true
		if action.Status != "would_write" {
			t.Fatalf("dry-run status = %#v", action)
		}
	}
	shared := filepath.Join(realHome, ".agents", "skills", "gomoufox", "SKILL.md")
	if !seen[shared] {
		t.Fatalf("missing shared skill action %s in %#v", shared, plan.Actions)
	}
	claude := filepath.Join(realHome, ".claude", "skills", "gomoufox-mcp", "SKILL.md")
	if !seen[claude] {
		t.Fatalf("missing claude skill action %s in %#v", claude, plan.Actions)
	}
}

func TestInstallAppliesProjectScopeSkillsAndMCP(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	plan, err := Install(Options{
		Target:  TargetCursor,
		Scope:   ScopeProject,
		Force:   true,
		HomeDir: home,
		WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DryRun {
		t.Fatalf("apply plan marked dry-run: %#v", plan)
	}
	skillPath := filepath.Join(work, ".agents", "skills", "gomoufox", "SKILL.md")
	if data, err := os.ReadFile(skillPath); err != nil || !strings.Contains(string(data), "name: gomoufox") {
		t.Fatalf("skill file data=%q err=%v", data, err)
	}
	mcpPath := filepath.Join(work, ".cursor", "mcp.json")
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	servers := parsed["mcpServers"].(map[string]any)
	gomoufox := servers["gomoufox"].(map[string]any)
	args := gomoufox["args"].([]any)
	want := []string{"mcp", "--toolset", "core"}
	if len(args) != len(want) {
		t.Fatalf("args = %#v", args)
	}
	for i, item := range want {
		if args[i] != item {
			t.Fatalf("args = %#v", args)
		}
	}
}

func TestInstallMergesExistingMCPConfigWithoutForce(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	configPath := filepath.Join(home, ".claude", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"mcpServers":{"other":{"command":"other","args":["x"]}},"keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := Install(Options{
		Target:   TargetClaude,
		Scope:    ScopeUser,
		Features: []string{FeatureMCP},
		HomeDir:  home,
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	realHome, err := canonicalRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	realConfigPath := filepath.Join(realHome, ".claude", "mcp.json")
	if len(plan.Actions) != 1 || plan.Actions[0].Path != realConfigPath {
		t.Fatalf("plan actions = %#v", plan.Actions)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["keep"] != true {
		t.Fatalf("top-level key lost: %s", data)
	}
	servers := parsed["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("unrelated server lost: %s", data)
	}
	gomoufox := servers["gomoufox"].(map[string]any)
	if gomoufox["command"] != "gomoufox" {
		t.Fatalf("gomoufox server = %#v", gomoufox)
	}
}

func TestInstallExistingSkillRequiresForceWhenContentsDiffer(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	skillPath := filepath.Join(home, ".agents", "skills", "gomoufox", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("custom skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Install(Options{
		Target:   TargetCodex,
		Scope:    ScopeUser,
		Features: []string{FeatureSkills},
		HomeDir:  home,
		WorkDir:  work,
	})
	if err == nil || !strings.Contains(err.Error(), "differs from the bundled skill") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("Install error = %v", err)
	}
	data, readErr := os.ReadFile(skillPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "custom skill\n" {
		t.Fatalf("skill was overwritten: %q", data)
	}
	plan, err := Install(Options{
		Target: TargetCodex, Scope: ScopeUser, Features: []string{FeatureSkills}, DryRun: true, HomeDir: home, WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	var needsForce bool
	for _, action := range plan.Actions {
		if strings.HasSuffix(action.Path, filepath.Join("gomoufox", "SKILL.md")) && action.Status == "needs_force" {
			needsForce = true
		}
	}
	if !needsForce {
		t.Fatalf("stale skill dry-run plan = %#v", plan.Actions)
	}
}

func TestInstallExistingIdenticalSkillIsUnchanged(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	installables := skillreg.DefaultInstallableSkills()
	skillPath := filepath.Join(home, ".agents", "skills", "gomoufox", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte(installables[0].Files[0].Contents), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := Install(Options{
		Target: TargetCodex, Scope: ScopeUser, Features: []string{FeatureSkills}, HomeDir: home, WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	realHome, err := canonicalRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	realSkillPath := filepath.Join(realHome, ".agents", "skills", "gomoufox", "SKILL.md")
	var unchanged bool
	for _, action := range plan.Actions {
		if action.Path == realSkillPath && action.Status == "unchanged" {
			unchanged = true
		}
	}
	if !unchanged {
		t.Fatalf("identical skill plan = %#v", plan.Actions)
	}
}

func TestInstallForceRewritesExactSkillsWithMode0600(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	if _, err := Install(Options{Target: TargetCodex, Scope: ScopeUser, Features: []string{FeatureSkills}, HomeDir: home, WorkDir: work}); err != nil {
		t.Fatal(err)
	}
	for _, item := range skillreg.DefaultInstallableSkills() {
		path := filepath.Join(home, ".agents", "skills", item.Directory, item.Files[0].Path)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dryRun, err := Install(Options{Target: TargetCodex, Scope: ScopeUser, Features: []string{FeatureSkills}, Force: true, DryRun: true, HomeDir: home, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range dryRun.Actions {
		if action.Status != statusWouldUpdate {
			t.Fatalf("forced dry-run action = %#v", action)
		}
	}
	plan, err := Install(Options{Target: TargetCodex, Scope: ScopeUser, Features: []string{FeatureSkills}, Force: true, HomeDir: home, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range plan.Actions {
		if action.Status != statusUpdated {
			t.Fatalf("forced action = %#v", action)
		}
		info, err := os.Stat(action.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("forced mode for %s = %v", action.Path, info.Mode().Perm())
		}
	}
}

func TestInstallCodexMCPIsIdempotentAfterFirstWrite(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	opts := Options{Target: TargetCodex, Scope: ScopeUser, Features: []string{FeatureMCP}, HomeDir: home, WorkDir: work}
	first, err := Install(opts)
	if err != nil || len(first.Actions) != 1 || first.Actions[0].Status != statusWrote {
		t.Fatalf("first install plan=%#v err=%v", first, err)
	}
	second, err := Install(opts)
	if err != nil || len(second.Actions) != 1 || second.Actions[0].Status != statusUnchanged {
		t.Fatalf("second install plan=%#v err=%v", second, err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil || strings.HasPrefix(string(data), "\n") {
		t.Fatalf("config=%q err=%v", data, err)
	}
}

func TestInstallStaleSkillDoesNotPartiallyWriteOtherSkills(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	installables := skillreg.DefaultInstallableSkills()
	if len(installables) < 2 {
		t.Fatalf("need at least two bundled skills, got %d", len(installables))
	}
	stalePath := filepath.Join(home, ".agents", "skills", installables[1].Directory, installables[1].Files[0].Path)
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePath, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Install(Options{
		Target: TargetCodex, Scope: ScopeUser, Features: []string{FeatureSkills}, HomeDir: home, WorkDir: work,
	})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("Install error = %v", err)
	}
	firstPath := filepath.Join(home, ".agents", "skills", installables[0].Directory, installables[0].Files[0].Path)
	if _, statErr := os.Stat(firstPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Install partially wrote %s: %v", firstPath, statErr)
	}
}

func TestInstallDryRunCreatesNothing(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	if _, err := Install(Options{Target: TargetGemini, Scope: ScopeProject, DryRun: true, HomeDir: home, WorkDir: work}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created .agents err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".gemini")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created .gemini err=%v", err)
	}
}

func TestInstallRejectsInvalidInputs(t *testing.T) {
	for _, tc := range []Options{
		{Target: "bad"},
		{Target: TargetCodex, Scope: "bad"},
		{Target: TargetCodex, Features: []string{"bad"}},
		{Target: TargetCodex, Toolset: "core --enable-eval"},
		{Target: TargetCodex, MCPArgs: []string{"--ok", "bad\narg"}},
	} {
		if _, err := Install(tc); err == nil {
			t.Fatalf("Install(%#v) succeeded", tc)
		}
	}
}

func TestMergeMCPJSONPreservesUnrelatedServers(t *testing.T) {
	data, err := mergeMCPJSON([]byte(`{"mcpServers":{"other":{"command":"other","args":["x"]}},"keep":true}`), "core", nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["keep"] != true {
		t.Fatalf("top-level key lost: %s", data)
	}
	servers := parsed["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatalf("unrelated server lost: %s", data)
	}
	gomoufox := servers["gomoufox"].(map[string]any)
	if gomoufox["command"] != "gomoufox" {
		t.Fatalf("gomoufox server = %#v", gomoufox)
	}
}

func TestMergeCodexTOMLPreservesExistingConfigAndReplacesManagedBlock(t *testing.T) {
	first, err := mergeCodexTOML([]byte("model = \"gpt-5\"\n"), "core", []string{"--max-sessions=2"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(first)
	for _, want := range []string{"model = \"gpt-5\"", "[mcp_servers.gomoufox]", `"--toolset", "core"`, `"--max-sessions=2"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	second, err := mergeCodexTOML(first, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	text = string(second)
	if strings.Count(text, "[mcp_servers.gomoufox]") != 1 || !strings.Contains(text, `"--toolset", "full"`) {
		t.Fatalf("managed block not replaced: %s", text)
	}
}

func TestMergeCodexTOMLRejectsUnmanagedExistingServer(t *testing.T) {
	if _, err := mergeCodexTOML([]byte("[mcp_servers.gomoufox]\ncommand = \"bad\"\n"), "core", nil); err == nil {
		t.Fatal("unmanaged existing server accepted")
	}
}

func TestInstallReportsPlanningReadMergeAndWriteFailures(t *testing.T) {
	base := Options{Target: TargetCodex, Scope: ScopeUser, Features: []string{FeatureMCP}, HomeDir: t.TempDir(), WorkDir: t.TempDir()}
	if _, err := Install(Options{Target: TargetCodex, HomeDir: filepath.Join(t.TempDir(), "missing"), WorkDir: t.TempDir()}); err == nil {
		t.Fatal("missing home root accepted")
	}

	oldResolve, oldExists, oldRead, oldWrite := resolveAgentWrite, agentRegularFileExists, agentReadFile, agentWriteFile0600
	t.Cleanup(func() {
		resolveAgentWrite, agentRegularFileExists, agentReadFile, agentWriteFile0600 = oldResolve, oldExists, oldRead, oldWrite
	})
	wantErr := errors.New("injected")
	resolveAgentWrite = func(string, bool) (string, error) { return "", wantErr }
	if _, err := Install(base); !errors.Is(err, wantErr) {
		t.Fatalf("resolve error = %v", err)
	}
	resolveAgentWrite = oldResolve
	agentRegularFileExists = func(string) (bool, error) { return false, wantErr }
	if _, err := Install(base); !errors.Is(err, wantErr) {
		t.Fatalf("exists error = %v", err)
	}
	agentRegularFileExists = func(string) (bool, error) { return true, nil }
	agentReadFile = func(string) ([]byte, error) { return nil, wantErr }
	if _, err := Install(base); !errors.Is(err, wantErr) {
		t.Fatalf("read error = %v", err)
	}
	agentReadFile = func(string) ([]byte, error) { return []byte("# BEGIN gomoufox managed mcp server\n"), nil }
	if _, err := Install(base); err == nil || !strings.Contains(err.Error(), "missing end marker") {
		t.Fatalf("merge error = %v", err)
	}
	agentRegularFileExists = oldExists
	agentReadFile = oldRead
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "existing race", err: os.ErrExist, want: "pass --force"},
		{name: "write failure", err: wantErr, want: "injected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentWriteFile0600 = func(string, []byte, bool) error { return tc.err }
			_, err := Install(base)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("write error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAgentPathChecksRejectUnsafeAndUnreadableTargets(t *testing.T) {
	root, err := canonicalRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, link} {
		if _, err := regularFileExists(path); err == nil {
			t.Fatalf("regularFileExists(%q) accepted unsafe target", path)
		}
		if _, err := resolveSafeWrite(path, true); err == nil {
			t.Fatalf("resolveSafeWrite(%q) accepted unsafe target", path)
		}
	}
	if exists, err := regularFileExists(filepath.Join(root, "missing")); err != nil || exists {
		t.Fatalf("missing file = %v, %v", exists, err)
	}
	if _, err := regularFileExists(os.DevNull); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("special file error = %v", err)
	}
	large := filepath.Join(root, "large")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", maxAgentFileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentFile(large); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized agent file error = %v", err)
	}
	if _, err := readAgentFile(filepath.Join(root, "missing-read")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing agent file error = %v", err)
	}
	if _, err := readAgentReader("injected", failingAgentReader{}); err == nil {
		t.Fatal("agent reader error was ignored")
	}
	if _, err := resolveSafeWrite(file, false); err == nil {
		t.Fatal("existing path accepted without overwrite")
	}
	if got, err := resolveSafeWrite(filepath.Join(root, "missing"), true); err != nil || !filepath.IsAbs(got) {
		t.Fatalf("safe missing path = %q, %v", got, err)
	}

	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(dir, parentLink); err != nil {
		t.Fatal(err)
	}
	if err := rejectExistingParentSymlinks(filepath.Join(parentLink, "child")); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if _, err := resolveSafeWrite(filepath.Join(parentLink, "child"), true); err == nil {
		t.Fatal("resolveSafeWrite accepted a symlink parent")
	}
	if err := rejectExistingParentSymlinks(filepath.Join(file, "child")); err == nil {
		t.Fatal("file parent accepted")
	}
	if err := rejectExistingParentSymlinks(filepath.Join(root, "missing", "child")); err != nil {
		t.Fatalf("missing prospective parent rejected: %v", err)
	}
	if err := rejectExistingParentSymlinks(string(os.PathSeparator) + "child"); err != nil {
		t.Fatalf("root parent rejected: %v", err)
	}
}

func TestAgentFilesystemErrorsPropagate(t *testing.T) {
	wantErr := errors.New("injected")
	oldExistsLstat, oldResolveLstat, oldParentLstat := agentExistsLstat, agentResolveLstat, agentParentLstat
	oldAbs, oldEval, oldHome, oldGetwd := agentAbs, agentEvalSymlinks, agentUserHomeDir, agentGetwd
	t.Cleanup(func() {
		agentExistsLstat, agentResolveLstat, agentParentLstat = oldExistsLstat, oldResolveLstat, oldParentLstat
		agentAbs, agentEvalSymlinks, agentUserHomeDir, agentGetwd = oldAbs, oldEval, oldHome, oldGetwd
	})
	agentExistsLstat = func(string) (os.FileInfo, error) { return nil, wantErr }
	if _, err := regularFileExists("x"); !errors.Is(err, wantErr) {
		t.Fatalf("regularFileExists error = %v", err)
	}
	agentResolveLstat = func(string) (os.FileInfo, error) { return nil, wantErr }
	if _, err := resolveSafeWrite("x", true); !errors.Is(err, wantErr) {
		t.Fatalf("resolveSafeWrite lstat error = %v", err)
	}
	agentResolveLstat = oldResolveLstat
	agentParentLstat = func(string) (os.FileInfo, error) { return nil, wantErr }
	if err := rejectExistingParentSymlinks(filepath.Join(t.TempDir(), "child")); !errors.Is(err, wantErr) {
		t.Fatalf("parent lstat error = %v", err)
	}
	agentParentLstat = oldParentLstat
	agentAbs = func(string) (string, error) { return "", wantErr }
	if _, err := canonicalRoot("x"); !errors.Is(err, wantErr) {
		t.Fatalf("canonicalRoot abs error = %v", err)
	}
	if _, err := resolveSafeWrite("x", true); !errors.Is(err, wantErr) {
		t.Fatalf("resolveSafeWrite abs error = %v", err)
	}
	agentAbs = oldAbs
	agentEvalSymlinks = func(string) (string, error) { return "", wantErr }
	if _, err := canonicalRoot("x"); !errors.Is(err, wantErr) {
		t.Fatalf("canonicalRoot eval error = %v", err)
	}
	agentEvalSymlinks = oldEval
	agentUserHomeDir = func() (string, error) { return "", wantErr }
	if _, _, err := roots(Options{WorkDir: t.TempDir()}); !errors.Is(err, wantErr) {
		t.Fatalf("home error = %v", err)
	}
	agentUserHomeDir = oldHome
	agentGetwd = func() (string, error) { return "", wantErr }
	if _, _, err := roots(Options{HomeDir: t.TempDir()}); !errors.Is(err, wantErr) {
		t.Fatalf("getwd error = %v", err)
	}
	agentGetwd = oldGetwd
	if _, _, err := roots(Options{HomeDir: t.TempDir(), WorkDir: filepath.Join(t.TempDir(), "missing")}); err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("missing work root error = %v", err)
	}
}

func TestAgentConfigHelpersCoverEveryTargetAndValidationBranch(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	for _, target := range []string{TargetCodex, TargetClaude, TargetCursor, TargetGemini, "unknown"} {
		for _, scope := range []string{ScopeUser, ScopeProject} {
			write := mcpWriteFor(target, scope, home, work, "core", []string{"--max-sessions=2"})
			if target == "unknown" {
				if write.path != "" || write.merge != nil {
					t.Fatalf("unknown target write = %#v", write)
				}
				continue
			}
			if write.path == "" || write.merge == nil {
				t.Fatalf("mcpWriteFor(%s, %s) = %#v", target, scope, write)
			}
			if _, err := write.merge(nil); err != nil {
				t.Fatalf("merge %s/%s: %v", target, scope, err)
			}
		}
	}
	for _, tc := range []struct{ target, scope, want string }{
		{TargetClaude, ScopeProject, filepath.Join(work, ".claude", "skills")},
		{TargetClaude, ScopeUser, filepath.Join(home, ".claude", "skills")},
		{TargetCodex, ScopeProject, filepath.Join(work, ".agents", "skills")},
		{TargetCodex, ScopeUser, filepath.Join(home, ".agents", "skills")},
	} {
		if got := skillRootFor(tc.target, tc.scope, home, work); got != tc.want {
			t.Fatalf("skillRootFor(%s, %s) = %q, want %q", tc.target, tc.scope, got, tc.want)
		}
	}
	if _, err := mergeMCPJSON([]byte("["), "core", nil); err == nil {
		t.Fatal("malformed MCP JSON accepted")
	}
	if _, err := mergeCodexTOML([]byte("# BEGIN gomoufox managed mcp server\n"), "core", nil); err == nil {
		t.Fatal("unterminated managed TOML block accepted")
	}
	for _, tc := range [][2]string{
		{"", "SKILL.md"}, {"nested/skill", "SKILL.md"}, {".hidden", "SKILL.md"}, {"skill", "."}, {"skill", "../escape"},
	} {
		if _, err := installableRelativePath(tc[0], tc[1]); err == nil {
			t.Fatalf("installableRelativePath(%q, %q) succeeded", tc[0], tc[1])
		}
	}
	if !containsTarget("codex,claude", "claude") || containsTarget("codex,claude", "gemini") {
		t.Fatal("containsTarget returned the wrong membership")
	}
	writes := dedupeWrites([]fileWrite{{target: "codex", path: "b"}, {target: "codex", path: "b"}, {target: "claude", path: "b"}, {target: "cursor", path: "a"}})
	if len(writes) != 2 || writes[0].path != "a" || writes[1].target != "codex,claude" {
		t.Fatalf("dedupeWrites = %#v", writes)
	}
	defaults := normalizeOptions(Options{})
	if defaults.Target != TargetAll || defaults.Scope != ScopeUser || defaults.Toolset != DefaultToolset || len(defaults.Features) != 2 {
		t.Fatalf("normalizeOptions defaults = %#v", defaults)
	}
}

func TestAgentPlanRejectsUnsafePackagedSkillPath(t *testing.T) {
	old := agentInstallableSkills
	agentInstallableSkills = func() []skillreg.InstallableSkill {
		return []skillreg.InstallableSkill{{Directory: "../unsafe", Files: []skillreg.InstallableFile{{Path: "SKILL.md"}}}}
	}
	t.Cleanup(func() { agentInstallableSkills = old })
	_, err := planWrites(Options{
		Target: TargetCodex, Scope: ScopeUser, Features: []string{FeatureSkills},
		HomeDir: t.TempDir(), WorkDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid installable skill directory") {
		t.Fatalf("unsafe packaged skill error = %v", err)
	}
}
