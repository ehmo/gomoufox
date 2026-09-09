package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRecipesWriteTheReportedCommand(t *testing.T) {
	oldHome, oldWork := userHomeDir, cliGetwd
	t.Cleanup(func() { userHomeDir, cliGetwd = oldHome, oldWork })
	for _, recipe := range []string{"research", "local-development"} {
		t.Run(recipe, func(t *testing.T) {
			dir := t.TempDir()
			userHomeDir = func() (string, error) { return dir, nil }
			cliGetwd = userHomeDir
			var stdout, stderr bytes.Buffer
			args := []string{"setup", "--recipe", recipe, "--target", "cursor", "--scope", "project", "--features", "mcp", "--skip-install", "--skip-doctor", "--json"}
			code := (Runner{}).Run(context.Background(), append(args, "--dry-run"), Streams{Stdout: &stdout, Stderr: &stderr})
			if code != ExitOK {
				t.Fatalf("dry-run: %d %s", code, &stderr)
			}
			var preview SetupResponse
			if err := json.Unmarshal(stdout.Bytes(), &preview); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".cursor", "mcp.json")
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("dry-run wrote config")
			}
			stdout.Reset()
			code = (Runner{}).Run(context.Background(), append(args, "--yes"), Streams{Stdout: &stdout, Stderr: &stderr})
			if code != ExitOK {
				t.Fatalf("apply: %d %s", code, &stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				MCPServers map[string]struct {
					Command string
					Args    []string
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			installed := config.MCPServers["gomoufox"]
			if strings.Join(preview.MCPCommand, "\x00") != strings.Join(append([]string{installed.Command}, installed.Args...), "\x00") || !strings.Contains(preview.NextStep, "restart") {
				t.Fatalf("preview differs from installed command: %+v %s", preview, data)
			}
			var effective MCPRequest
			r := Runner{Hooks: Hooks{MCP: func(_ context.Context, req MCPRequest) error { effective = req; return nil }}}
			if code := r.Run(context.Background(), installed.Args, Streams{Stderr: &stderr}); code != ExitOK {
				t.Fatalf("installed command: %d %s", code, &stderr)
			}
			if effective.Config.Policy.AllowLocalhost != (recipe == "local-development") || effective.Config.Policy.EnableEval || effective.Config.Policy.AllowPrivateIPs || effective.Config.Policy.AllowBrowserFetch {
				t.Fatalf("recipe grants unexpected capability: %+v", effective.Config.Policy)
			}
			if (effective.Config.Toolset == "full") != (recipe == "local-development") {
				t.Fatalf("wrong toolset: %s", effective.Config.Toolset)
			}
		})
	}
}

func TestSetupRecipeOverridesAndValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--recipe", "unknown"}, {"--toolset", "unknown"}, {"--mcp-arg=--unknown"},
		{"--mcp-arg=--help"}, {"--mcp-arg=--transport", "--mcp-arg=http", "--mcp-arg=--auth-token", "--mcp-arg=secret"},
	} {
		if _, err := parseSetup(args); err == nil {
			t.Fatalf("invalid setup accepted: %v", args)
		}
	}
	command, err := parseSetup([]string{"--recipe", "local-development", "--toolset", "core", "--mcp-arg=--enable-eval"})
	if err != nil || command.Toolset != "core" || !strings.Contains(strings.Join(command.MCPArgs, " "), "--enable-eval") {
		t.Fatalf("explicit override: %+v %v", command, err)
	}
	args := setupMCPCommand(setupCommand{Toolset: "core", MCPArgs: []string{"--auth-token", "secret", "--auth-token=secret"}})
	if strings.Contains(strings.Join(args, " "), "secret") {
		t.Fatal("preview leaks token")
	}
}
