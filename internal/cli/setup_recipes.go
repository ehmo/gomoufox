package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func applySetupRecipe(command *setupCommand) error {
	defaultToolset := "core"
	switch command.Recipe {
	case "research":
	case "local-development":
		defaultToolset = "full"
		command.MCPArgs = append([]string{"--allow-localhost"}, command.MCPArgs...)
	default:
		return errors.New("--recipe must be research or local-development")
	}
	if command.Toolset == "" {
		command.Toolset = defaultToolset
	}
	args := append([]string{"mcp", "--toolset", command.Toolset}, command.MCPArgs...)
	var diagnostics strings.Builder
	validated := false
	runner := Runner{Hooks: Hooks{MCP: func(_ context.Context, req MCPRequest) error {
		validated = true
		if req.Transport != "stdio" {
			return errors.New("agent setup requires --transport stdio")
		}
		return nil
	}}}
	if code := runner.Run(context.Background(), args, Streams{Stderr: &diagnostics}); code != ExitOK || !validated {
		return fmt.Errorf("invalid setup MCP command: %s", strings.TrimSpace(diagnostics.String()))
	}
	return nil
}

func setupMCPCommand(command setupCommand) []string {
	args := append([]string{"gomoufox", "mcp", "--toolset", command.Toolset}, command.MCPArgs...)
	for i := range args {
		if args[i] == "--auth-token" && i+1 < len(args) {
			args[i+1] = "[redacted]"
		} else if strings.HasPrefix(args[i], "--auth-token=") {
			args[i] = "--auth-token=[redacted]"
		}
	}
	return args
}

func printSetupMCP(out io.Writer, response SetupResponse) {
	if len(response.MCPCommand) > 0 {
		command, _ := json.Marshal(response.MCPCommand)
		_, _ = fmt.Fprintf(out, "MCP command (argv): %s\n%s\n", command, response.NextStep)
	}
}
