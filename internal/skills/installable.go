package skills

import "fmt"

const (
	CoreInstallableDirectory = "gomoufox"
	MCPInstallableDirectory  = "gomoufox-mcp"
)

type InstallableFile struct {
	Path     string
	Contents string
}

type InstallableSkill struct {
	Directory string
	Files     []InstallableFile
}

func DefaultInstallableSkills() []InstallableSkill {
	installables, _ := InstallableSkills(DefaultRegistry())
	return installables
}

func InstallableSkills(registry Registry) ([]InstallableSkill, error) {
	core, err := registry.Resolve("core", LatestVersion)
	if err != nil {
		return nil, err
	}
	mcp, err := registry.Resolve("mcp", LatestVersion)
	if err != nil {
		return nil, err
	}
	return []InstallableSkill{
		{
			Directory: CoreInstallableDirectory,
			Files: []InstallableFile{
				{
					Path: "SKILL.md",
					Contents: skillMarkdown(
						"gomoufox",
						"Use when an agent needs browser automation with gomoufox, Camoufox, the gomoufox CLI, or the gomoufox Go library.",
						core.Body,
					),
				},
				{Path: "agents/openai.yaml", Contents: coreOpenAIYAML},
			},
		},
		{
			Directory: MCPInstallableDirectory,
			Files: []InstallableFile{
				{
					Path: "SKILL.md",
					Contents: skillMarkdown(
						"gomoufox-mcp",
						"Use when an agent needs to wire or drive gomoufox MCP browser tools with compact output and guardrails.",
						mcp.Body,
					),
				},
				{Path: "agents/openai.yaml", Contents: mcpOpenAIYAML},
			},
		},
	}, nil
}

func skillMarkdown(name, description, body string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s", name, description, body)
}

const coreOpenAIYAML = `interface:
  display_name: "gomoufox"
  short_description: "Use gomoufox for Camoufox browser automation through the Go library, CLI, and MCP server."
  default_prompt: "Use gomoufox to automate this browser task with compact output, explicit guardrails, and measured Camoufox parity."
`

const mcpOpenAIYAML = `interface:
  display_name: "gomoufox MCP"
  short_description: "Wire agents to the gomoufox MCP browser tool server with compact, guarded workflows."
  default_prompt: "Use gomoufox MCP tools for this browser task. Start with compact snapshots, refs, and guarded sessions."
`
