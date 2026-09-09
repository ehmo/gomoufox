package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ehmo/gomoufox/internal/policy"
)

func TestFlagSchemaDiscoveryIsBoundedAndComplete(t *testing.T) {
	t.Setenv("GOMOUFOX_DAEMON_TOKEN", "private-do-not-print")
	commands := append(commandHelps(), commandHelp{Name: ""})
	for _, name := range []string{"session export", "session import", "skills list", "skills show", "skills export", "skills install", "agents install"} {
		commands = append(commands, commandHelp{Name: name})
	}
	for _, command := range commands {
		t.Run(command.Name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"help"}, strings.Fields(command.Name)...)
			args = append(args, "--json", "--fields", "flag_schema")
			if code := (Runner{}).Run(context.Background(), args, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
				t.Fatalf("help failed: %d %s", code, &stderr)
			}
			if stdout.Len() > 8192 || strings.Contains(stdout.String(), "private-do-not-print") {
				t.Fatalf("unbounded or secret-bearing discovery: %d", stdout.Len())
			}
			var catalog helpCatalog
			if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
				t.Fatal(err)
			}
			if catalog.FlagSchema == nil || len(catalog.Commands) != 0 {
				t.Fatal("field filter failed")
			}
			for name, spec := range commandFlagSpecs(command.Name) {
				detail, ok := catalog.FlagSchema.Flags[name]
				if !ok || detail.Type == "" || detail.Repeat != spec.Repeat || detail.OptionalValue != (spec.Kind == flagOptionalValue) {
					t.Fatalf("missing flag contract: %s", name)
				}
			}
			for name, spec := range globalFlagSpecs() {
				if (spec.Kind == flagBool) != isGlobalBool(name) || (spec.Kind == flagValue) != isGlobalValue(name) {
					t.Fatalf("global parser drift: %s", name)
				}
				if catalog.FlagSchema.Global[name].Type == "" {
					t.Fatalf("missing global: %s", name)
				}
			}
		})
	}
	compact := buildHelpCatalog(helpOptions{})
	if compact.FlagSchema != nil {
		t.Fatal("compact help expanded unexpectedly")
	}
}

func TestFlagSchemaNumericBoundsMatchBrowserParser(t *testing.T) {
	r := Runner{
		Resolver: cliTestResolver{"example.com": {"8.8.8.8"}},
		Hooks: Hooks{LocalCommand: func(context.Context, LocalCommandRequest) (LocalCommandResponse, error) {
			return LocalCommandResponse{}, nil
		}},
	}
	for _, command := range []string{"get", "screenshot", "eval", "record", "fetch"} {
		for name, detail := range buildCLIFlagSchema(command).Flags {
			if detail.Minimum == nil || detail.Maximum == nil {
				continue
			}
			for _, value := range []int{*detail.Minimum - 1, *detail.Minimum, *detail.Maximum, *detail.Maximum + 1} {
				t.Run(fmt.Sprintf("%s/%s/%d", command, name, value), func(t *testing.T) {
					args := []string{command, "https://example.com", "--" + name, fmt.Sprint(value)}
					if command == "eval" {
						args = append(args, "--enable-eval", "--script", "document.title")
					}
					if command == "record" || name == "max-file-bytes" {
						args = append(args, "--out", t.TempDir()+"/artifact")
					}
					var stderr bytes.Buffer
					code := r.Run(context.Background(), args, Streams{Stderr: &stderr})
					want := ExitOK
					if value < *detail.Minimum || value > *detail.Maximum {
						want = ExitUsage
					}
					if code != want {
						t.Fatalf("schema/parser mismatch: %d want %d: %s", code, want, &stderr)
					}
				})
			}
		}
	}
}

func TestFlagSchemaMCPDefaultsAndBoundsMatchExecution(t *testing.T) {
	var actual MCPRequest
	r := Runner{Hooks: Hooks{MCP: func(_ context.Context, req MCPRequest) error { actual = req; return nil }}}
	schema := buildCLIFlagSchema("mcp")
	var stderr bytes.Buffer
	if code := r.Run(context.Background(), []string{"mcp"}, Streams{Stderr: &stderr}); code != ExitOK {
		t.Fatal(code, &stderr)
	}
	defaults := map[string]any{"port": actual.Port, "transport": actual.Transport, "toolset": actual.Config.Toolset, "max-sessions": actual.Config.Policy.MaxSessions, "max-input-bytes": actual.Config.Policy.MaxInputBytes, "max-response-bytes": actual.Config.Policy.MaxResponseBytes, "session-ttl": actual.Config.Policy.SessionTTL.String(), "enable-eval": actual.Config.Policy.EnableEval}
	for name, value := range defaults {
		if !reflect.DeepEqual(schema.Flags[name].Default, value) {
			t.Fatalf("default drift %s: %v != %v", name, schema.Flags[name].Default, value)
		}
	}
	for _, tc := range []struct {
		name, value string
		ok          bool
	}{
		{"port", "0", false}, {"port", "65535", true}, {"port", "65536", false},
		{"max-sessions", "0", false}, {"max-sessions", fmt.Sprint(policy.HardMaxSessions), true}, {"max-sessions", "21", false},
		{"max-input-bytes", "-1", true}, {"max-input-bytes", fmt.Sprint(policy.HardMaxInputBytes), true}, {"max-input-bytes", fmt.Sprint(policy.HardMaxInputBytes + 1), false},
		{"max-response-bytes", "0", true}, {"max-response-bytes", fmt.Sprint(policy.HardMaxResponseBytes + 1), false},
		{"session-ttl", "24h", true}, {"session-ttl", "24h1s", false}, {"session-ttl", "25", false},
		{"toolset", "core", true}, {"toolset", "other", false}, {"transport", "other", false},
	} {
		code := r.Run(context.Background(), []string{"mcp", "--" + tc.name, tc.value}, Streams{Stderr: &stderr})
		if (code == ExitOK) != tc.ok {
			t.Fatalf("%s=%s schema mismatch: %d %s", tc.name, tc.value, code, &stderr)
		}
	}
	global := schema.Global["timeout"]
	if global.Default != "30s" || global.Format != "duration" {
		t.Fatalf("duration schema: %+v", global)
	}
	for _, raw := range []string{"25", "0s", "-1s"} {
		if _, _, _, err := parseGlobal([]string{"--timeout", raw, "get"}); err == nil {
			t.Fatalf("accepted invalid duration %s", raw)
		}
	}
	parsed, _, _, err := parseGlobal([]string{"--timeout", "25s", "get"})
	if err != nil || parsed.Timeout != 25*time.Second {
		t.Fatalf("duration mismatch: %+v %v", parsed, err)
	}
}
