package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryListsLatestSkillsSortedByName(t *testing.T) {
	registry := mustRegistry(t, []Definition{
		def("mcp", "0.1.0"),
		def("core", "0.1.0"),
		def("core", "0.2.0"),
	})
	list := registry.List()
	if len(list) != 2 {
		t.Fatalf("list len = %d", len(list))
	}
	if list[0].Name != "core" || list[0].Version != "0.2.0" || list[1].Name != "mcp" {
		t.Fatalf("list = %#v", list)
	}
	raw, err := json.Marshal(list[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "body") {
		t.Fatalf("summary leaked body = %s", raw)
	}
}

func TestRegistryResolvesExactAndLatestVersions(t *testing.T) {
	registry := mustRegistry(t, []Definition{
		def("core", "0.1.0"),
		def("core", "1.0.0"),
		def("core", "0.10.0"),
	})
	latest, err := registry.Resolve("core", "")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != "1.0.0" {
		t.Fatalf("latest = %#v", latest)
	}
	explicitLatest, err := registry.Resolve("core", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if explicitLatest.Version != latest.Version {
		t.Fatalf("explicit latest = %#v latest=%#v", explicitLatest, latest)
	}
	exact, err := registry.Resolve("core", "0.10.0")
	if err != nil {
		t.Fatal(err)
	}
	if exact.Version != "0.10.0" {
		t.Fatalf("exact = %#v", exact)
	}
}

func TestRegistryResolveErrors(t *testing.T) {
	registry := mustRegistry(t, []Definition{def("core", "0.1.0")})
	if _, err := registry.Resolve("missing", ""); !errors.Is(err, ErrUnknownSkill) || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing skill err = %v", err)
	}
	if _, err := registry.Resolve("core", "0.2.0"); !errors.Is(err, ErrUnknownVersion) || !strings.Contains(err.Error(), "core@0.2.0") {
		t.Fatalf("missing version err = %v", err)
	}
}

func TestRegistryRejectsInvalidDefinitions(t *testing.T) {
	cases := []struct {
		name string
		defs []Definition
		want string
	}{
		{name: "empty name", defs: []Definition{{Name: "", Version: "0.1.0", Summary: "s", MinGomoufox: "0.1.0", Body: "b"}}, want: "invalid skill name"},
		{name: "bad name", defs: []Definition{{Name: "Core", Version: "0.1.0", Summary: "s", MinGomoufox: "0.1.0", Body: "b"}}, want: "invalid skill name"},
		{name: "long name", defs: []Definition{{Name: strings.Repeat("a", MaxNameBytes+1), Version: "0.1.0", Summary: "s", MinGomoufox: "0.1.0", Body: "b"}}, want: "invalid skill name"},
		{name: "bad version", defs: []Definition{{Name: "core", Version: "0.1", Summary: "s", MinGomoufox: "0.1.0", Body: "b"}}, want: "invalid skill version"},
		{name: "bad min", defs: []Definition{{Name: "core", Version: "0.1.0", Summary: "s", MinGomoufox: "0.x.0", Body: "b"}}, want: "invalid min gomoufox version"},
		{name: "missing summary", defs: []Definition{{Name: "core", Version: "0.1.0", MinGomoufox: "0.1.0", Body: "b"}}, want: "missing summary"},
		{name: "missing body", defs: []Definition{{Name: "core", Version: "0.1.0", Summary: "s", MinGomoufox: "0.1.0"}}, want: "missing body"},
		{name: "duplicate", defs: []Definition{def("core", "0.1.0"), def("core", "0.1.0")}, want: "duplicate skill version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRegistry(tc.defs)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v want %q", err, tc.want)
			}
		})
	}
}

func TestNameAndVersionSelectors(t *testing.T) {
	for _, name := range []string{"core", "browser-flow", "mcp2"} {
		if !ValidName(name) {
			t.Fatalf("ValidName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "Core", "core_skill", strings.Repeat("a", MaxNameBytes+1)} {
		if ValidName(name) {
			t.Fatalf("ValidName(%q) = true", name)
		}
	}
	for _, version := range []string{"", "latest", "0.1.0", "10.20.30"} {
		if !ValidVersionSelector(version) {
			t.Fatalf("ValidVersionSelector(%q) = false", version)
		}
	}
	for _, version := range []string{"v0.1.0", "0.1", "0.1.0-rc.1", strings.Repeat("1", MaxVersionBytes+1)} {
		if ValidVersionSelector(version) {
			t.Fatalf("ValidVersionSelector(%q) = true", version)
		}
	}
}

func TestSemverValidationAndOrdering(t *testing.T) {
	for _, raw := range []string{"1..0", "1.a.0"} {
		if _, err := parseSemver(raw); err == nil {
			t.Fatalf("parseSemver(%q) succeeded", raw)
		}
	}
	low, err := parseSemver("1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	highMajor, err := parseSemver("2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	highMinor, err := parseSemver("1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	highPatch, err := parseSemver("1.2.4")
	if err != nil {
		t.Fatal(err)
	}
	if !low.less(highMajor) || !low.less(highMinor) || !low.less(highPatch) || low.less(low) {
		t.Fatalf("semver less mismatch")
	}
}

func TestChecksumAndCopies(t *testing.T) {
	registry := mustRegistry(t, []Definition{def("core", "0.1.0")})
	skill, err := registry.Resolve("core", "")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(skill.Body))
	if skill.SHA256 != hex.EncodeToString(sum[:]) || skill.Bytes != len([]byte(skill.Body)) {
		t.Fatalf("checksum fields = %#v", skill)
	}
	skill.Body = "changed"
	again, err := registry.Resolve("core", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.Body == "changed" {
		t.Fatalf("registry returned mutable skill")
	}
}

func TestDefaultRegistry(t *testing.T) {
	registry := DefaultRegistry()
	list := registry.List()
	if len(list) != 2 || list[0].Name != "core" || list[1].Name != "mcp" {
		t.Fatalf("default list = %#v", list)
	}
	for _, item := range list {
		skill, err := registry.Resolve(item.Name, item.Version)
		if err != nil {
			t.Fatal(err)
		}
		if skill.MinGomoufox != minGomoufoxVersion || !strings.HasSuffix(skill.Body, "\n") {
			t.Fatalf("default skill = %#v", skill)
		}
	}
}

func TestInstallableSkillsMatchRepositoryArtifacts(t *testing.T) {
	installables := DefaultInstallableSkills()
	if len(installables) != 2 {
		t.Fatalf("installable len = %d", len(installables))
	}

	expectedBodies := map[string]string{}
	for _, name := range []string{"core", "mcp"} {
		skill, err := DefaultRegistry().Resolve(name, "")
		if err != nil {
			t.Fatal(err)
		}
		expectedBodies[name] = skill.Body
	}
	expectedBodyByDirectory := map[string]string{
		CoreInstallableDirectory: expectedBodies["core"],
		MCPInstallableDirectory:  expectedBodies["mcp"],
	}
	expectedFrontmatter := map[string]string{
		CoreInstallableDirectory: "name: gomoufox",
		MCPInstallableDirectory:  "name: gomoufox-mcp",
	}

	for _, item := range installables {
		if item.Directory == "" {
			t.Fatalf("empty installable directory: %#v", item)
		}
		for _, file := range item.Files {
			repoPath := filepath.Join("..", "..", "skills", item.Directory, filepath.FromSlash(file.Path))
			repoContents, err := os.ReadFile(repoPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(repoContents) != file.Contents {
				t.Fatalf("%s drifted from generated installable content", repoPath)
			}
			switch file.Path {
			case "SKILL.md":
				frontmatter, body, err := splitSkillMarkdown(file.Contents)
				if err != nil {
					t.Fatalf("%s: %v", repoPath, err)
				}
				if !strings.Contains(frontmatter, expectedFrontmatter[item.Directory]) || !strings.Contains(frontmatter, "description: ") {
					t.Fatalf("%s frontmatter = %q", repoPath, frontmatter)
				}
				if body != expectedBodyByDirectory[item.Directory] {
					t.Fatalf("%s body drifted from embedded skill", repoPath)
				}
				if strings.Contains(body, "\n\t") {
					t.Fatalf("%s contains tab-indented markdown paragraph", repoPath)
				}
			case "agents/openai.yaml":
				for _, required := range []string{"interface:", "display_name:", "short_description:", "default_prompt:"} {
					if !strings.Contains(file.Contents, required) {
						t.Fatalf("%s missing %q", repoPath, required)
					}
				}
			default:
				t.Fatalf("unexpected installable file %s/%s", item.Directory, file.Path)
			}
		}
	}
}

func TestInstallableSkillsRequireCoreAndMCP(t *testing.T) {
	coreOnly := mustRegistry(t, []Definition{def("core", "0.1.0")})
	if _, err := InstallableSkills(coreOnly); !errors.Is(err, ErrUnknownSkill) || !strings.Contains(err.Error(), "mcp") {
		t.Fatalf("core-only err = %v", err)
	}
	mcpOnly := mustRegistry(t, []Definition{def("mcp", "0.1.0")})
	if _, err := InstallableSkills(mcpOnly); !errors.Is(err, ErrUnknownSkill) || !strings.Contains(err.Error(), "core") {
		t.Fatalf("mcp-only err = %v", err)
	}
}

func TestDefaultMCPSkillDocumentsHighRiskGates(t *testing.T) {
	skill, err := DefaultRegistry().Resolve("mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"--allow-browser-fetch",
		"--allow-cookie-values",
		"--allow-cookie-mutation",
		"--allow-snapshot-values",
		"--allow-session-export",
		"--allow-session-import",
		"--allow-session-proxy",
		"--allow-file-upload",
		"--allowed-origins",
		"--allowed-hosts",
		"--session-dir",
		"--toolset core",
		"not a sandbox",
	} {
		if !strings.Contains(skill.Body, text) {
			t.Fatalf("mcp skill missing %q:\n%s", text, skill.Body)
		}
	}
}

func TestDefaultMCPSkillDocumentsInteractionWorkflow(t *testing.T) {
	skill, err := DefaultRegistry().Resolve("mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"`browser_click`",
		"`browser_type`",
		"`browser_press_key`",
		"`browser_hover`",
		"`browser_scroll`",
		"`browser_select_option`",
		"`browser_set_checked`",
		"`browser_form_batch`",
		"`browser_dialog`",
		"responses do not echo file paths",
	} {
		if !strings.Contains(skill.Body, text) {
			t.Fatalf("mcp skill missing %q:\n%s", text, skill.Body)
		}
	}
}

func TestDefaultRegistryPanicsOnInvalidEmbeddedDefinition(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("mustDefaultRegistry did not panic")
		}
	}()
	mustDefaultRegistry([]Definition{{Name: "bad", Version: "0.1.0", Summary: "summary", MinGomoufox: "0.1.0"}})
}

func mustRegistry(t *testing.T, defs []Definition) Registry {
	t.Helper()
	registry, err := NewRegistry(defs)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func def(name, version string) Definition {
	return Definition{
		Name:        name,
		Version:     version,
		Summary:     "summary",
		MinGomoufox: "0.1.0",
		Body:        name + " " + version + "\n",
	}
}

func splitSkillMarkdown(raw string) (string, string, error) {
	if !strings.HasPrefix(raw, "---\n") {
		return "", "", errors.New("missing opening frontmatter marker")
	}
	rest := strings.TrimPrefix(raw, "---\n")
	frontmatter, body, ok := strings.Cut(rest, "\n---\n\n")
	if !ok {
		return "", "", errors.New("missing closing frontmatter marker")
	}
	return frontmatter, body, nil
}
