package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestInstallExistingSkillSkipsWithoutForce(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	skillPath := filepath.Join(home, ".agents", "skills", "gomoufox", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("custom skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := Install(Options{
		Target:   TargetCodex,
		Scope:    ScopeUser,
		Features: []string{FeatureSkills},
		HomeDir:  home,
		WorkDir:  work,
	})
	if err != nil {
		t.Fatalf("Install error = %v", err)
	}
	realHome, err := canonicalRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	realSkillPath := filepath.Join(realHome, ".agents", "skills", "gomoufox", "SKILL.md")
	var skipped bool
	for _, action := range plan.Actions {
		if action.Path == realSkillPath && action.Status == "skipped" {
			skipped = true
			break
		}
	}
	if !skipped {
		t.Fatalf("existing skill was not skipped: %#v", plan.Actions)
	}
	data, readErr := os.ReadFile(skillPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "custom skill\n" {
		t.Fatalf("skill was overwritten: %q", data)
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
