package cli

import (
	"fmt"

	gomoufox "github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/policy"
)

// Flag names and arity come from the same specs used by the command parser.
// Semantic constraints are checked against command execution in flag_schema_test.go.
type cliFlagSchema struct {
	Command     string                   `json:"command"`
	Usage       string                   `json:"usage"`
	Global      map[string]cliFlagDetail `json:"global"`
	Flags       map[string]cliFlagDetail `json:"flags,omitempty"`
	Constraints []string                 `json:"constraints,omitempty"`
}

type cliFlagDetail struct {
	Type          string   `json:"type"`
	Format        string   `json:"format,omitempty"`
	Units         string   `json:"units,omitempty"`
	Enum          []string `json:"enum,omitempty"`
	Default       any      `json:"default,omitempty"`
	DefaultSource string   `json:"default_source,omitempty"`
	Minimum       *int     `json:"minimum,omitempty"`
	Maximum       *int     `json:"maximum,omitempty"`
	MaxBytes      int      `json:"max_bytes,omitempty"`
	MaxItems      int      `json:"max_items,omitempty"`
	Repeat        bool     `json:"repeat,omitempty"`
	Required      bool     `json:"required,omitempty"`
	OptionalValue bool     `json:"optional_value,omitempty"`
	Notes         string   `json:"notes,omitempty"`
}

func commandFlagSpecs(command string) map[string]flagSpec {
	switch command {
	case "help":
		return helpFlagSpecs()
	case "doctor":
		return doctorFlagSpecs()
	case "install":
		return installFlagSpecs()
	case "setup":
		return setupFlagSpecs()
	case "serve":
		return serveFlagSpecs()
	case "mcp":
		return mcpFlagSpecs()
	case "open":
		return openFlagSpecs()
	case "record":
		return recordFlagSpecs()
	case "get":
		return getFlagSpecs()
	case "screenshot":
		return screenshotFlagSpecs()
	case "eval":
		return evalFlagSpecs()
	case "fetch":
		return fetchFlagSpecs()
	case "session export":
		return sessionExportFlagSpecs()
	case "session import":
		return sessionImportFlagSpecs()
	case "skills list":
		return skillsListFlagSpecs()
	case "skills show":
		return skillsShowFlagSpecs()
	case "skills export":
		return skillsExportFlagSpecs()
	case "skills install":
		return skillsInstallFlagSpecs()
	case "agents install":
		return agentsInstallFlagSpecs()
	}
	return nil
}

func globalFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"headless": {Kind: flagBool}, "headful": {Kind: flagBool},
		"allow-localhost": {Kind: flagBool}, "json": {Kind: flagBool},
		"verbose": {Kind: flagBool}, "version": {Kind: flagBool}, "help": {Kind: flagBool},
		"profile": {Kind: flagValue}, "proxy": {Kind: flagValue}, "timeout": {Kind: flagValue},
		"locale": {Kind: flagValue}, "os": {Kind: flagValue}, "server": {Kind: flagValue},
		"server-token": {Kind: flagValue}, "allow-schemes": {Kind: flagValue},
	}
}

func buildCLIFlagSchema(command string) *cliFlagSchema {
	schema := &cliFlagSchema{Command: command, Usage: "gomoufox <command> [flags]", Global: describeFlags("global", globalFlagSpecs())}
	if command == "" {
		schema.Command = "global"
	} else {
		schema.Flags = describeFlags(command, commandFlagSpecs(command))
		doc, _ := helpForCommand(command)
		schema.Usage = doc.Usage
	}
	schema.Constraints = commandFlagConstraints(command)
	return schema
}

func describeFlags(command string, specs map[string]flagSpec) map[string]cliFlagDetail {
	out := make(map[string]cliFlagDetail, len(specs))
	for name, spec := range specs {
		detail := describeFlag(command, name, spec)
		detail.Repeat = spec.Repeat
		detail.OptionalValue = spec.Kind == flagOptionalValue
		detail.Required = (name == "out" && (command == "record" || command == "session export" || command == "session import" || command == "skills export")) || (name == "file" && command == "session import")
		out[name] = detail
	}
	return out
}

func flagRange(defaultValue, minValue, maxValue int, units string) cliFlagDetail {
	return cliFlagDetail{Type: "integer", Default: defaultValue, Minimum: &minValue, Maximum: &maxValue, Units: units}
}

func flagEnum(defaultValue string, values ...string) cliFlagDetail {
	return cliFlagDetail{Type: "string", Default: defaultValue, Enum: values}
}

func describeFlag(command, name string, spec flagSpec) cliFlagDetail {
	d := cliFlagDetail{Type: "string", Default: ""}
	if spec.Kind == flagBool {
		d = cliFlagDetail{Type: "boolean", Default: false}
	}
	switch name {
	case "timeout", "session-ttl":
		d.Format, d.Units, d.Default = "duration", "Go duration suffix: ns, us, µs, ms, s, m, h", "30s"
		d.Notes = "Greater than zero; a suffix is required, for example 500ms or 25s."
		if name == "session-ttl" {
			d.Default = policy.DefaultSessionTTL.String()
			d.Notes += " Maximum 24h."
		}
	case "width", "height":
		value := 1280
		if name == "height" {
			value = 800
		}
		d = flagRange(value, 1, 100000, "CSS pixels")
		if command != "screenshot" {
			d.Notes = "Default applies when either viewport flag is supplied; otherwise use the browser context default."
		}
	case "max-sessions":
		d = flagRange(policy.DefaultMaxSessions, 1, policy.HardMaxSessions, "sessions")
	case "max-input-bytes", "max-response-bytes":
		value, maxValue := policy.DefaultMaxInputBytes, policy.HardMaxInputBytes
		if name == "max-response-bytes" {
			value, maxValue = policy.DefaultMaxResponseBytes, policy.HardMaxResponseBytes
		}
		d = cliFlagDetail{Type: "integer", Default: value, Maximum: &maxValue, Units: "bytes", Notes: "Zero or negative selects the default cap."}
	case "max-file-bytes":
		d = flagRange(gomoufox.MaxTransferFileBytes, 1, gomoufox.MaxTransferFileBytes, "bytes")
	case "max-bytes":
		d = flagRange(policy.DefaultMaxResponseBytes, 1, policy.HardMaxResponseBytes, "bytes")
		if command == "record" {
			d = flagRange(int(gomoufox.DefaultHARMaxBytes), 1, int(gomoufox.HardHARMaxBytes), "bytes")
		}
		if command == "screenshot" {
			d = cliFlagDetail{Type: "integer", Units: "bytes", DefaultSource: "No CLI output byte cap unless a positive value is supplied.", Notes: fmt.Sprintf("Positive values must be at most %d, or %d with --full-page. Zero or negative disables this CLI cap.", policy.DefaultScreenshotBytes, policy.FullPageScreenshotBytes)}
		}
	case "quality":
		d = flagRange(0, 0, 100, "percent")
		d.Notes = "JPEG only; zero uses the browser default."
	case "port":
		value := 3742
		if command == "serve" {
			value = 3741
		}
		d = flagRange(value, 1, 65535, "TCP port")
	case "wait-load-state":
		value := "domcontentloaded"
		if command == "screenshot" {
			value = "load"
		}
		d = flagEnum(value, "domcontentloaded", "load", "networkidle")
	case "transport":
		d = flagEnum("stdio", "stdio", "http")
	case "toolset":
		value := "core"
		if command == "mcp" {
			value = "full"
		}
		d = flagEnum(value, "core", "full")
		if command == "setup" {
			d.Default = nil
			d.DefaultSource = "research recipe: core; local-development recipe: full"
		}
	case "runtime":
		d = flagEnum("node-direct", "node-direct", "python")
	case "recipe":
		d = flagEnum("research", "research", "local-development")
	case "target":
		d = flagEnum("all", "codex", "claude", "cursor", "gemini", "all")
		if command == "skills install" {
			d = flagEnum("codex", "codex")
		}
	case "scope":
		d = flagEnum("user", "user", "project")
	case "features":
		d.Format, d.Default, d.Enum = "csv", "skills,mcp", []string{"skills", "mcp"}
		d.Notes = "Enum applies to each CSV item."
	case "capture":
		d = flagEnum("metadata", "metadata", "full")
	case "method":
		d = flagEnum("GET", "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD")
		d.Notes = "Case-insensitive. Multipart defaults to POST."
	case "os":
		d = flagEnum("", "windows", "macos", "linux")
		d.DefaultSource = "Generated fingerprint OS when omitted."
	case "profile", "dir", "python", "out", "file", "from-profile", "cookies-file", "save-session", "session-dir", "script-file", "arg-file", "data-file":
		d.Format = "path"
		if name == "profile" {
			d.Notes = "Relative to the CLI working directory; use an absolute path for repeatable ownership."
		}
		if name == "script-file" {
			d.MaxBytes = policy.ScriptInputBytes
		}
		if name == "arg-file" {
			d.MaxBytes = evalArgMaxBytes
			d.Notes = "File contents must be JSON."
		}
		if name == "data-file" {
			d.MaxBytes = gomoufox.MaxTransferFileBytes
		}
		if name == "session-dir" {
			d.DefaultSource = "OS user config directory/gomoufox/sessions, with a temp-directory fallback."
		}
		if name == "dir" && (command == "install" || command == "doctor") {
			d.DefaultSource = "OS cache directory/gomoufox/venv; see doctor."
		}
	case "arg":
		d.Format, d.MaxBytes = "json", evalArgMaxBytes
		d.Notes = "Passed to a function parameter; no global arg is created. Excludes --arg-file."
	case "script":
		d.Format, d.MaxBytes = "javascript", policy.ScriptInputBytes
	case "data":
		d.MaxBytes = policy.HardMaxInputBytes
	case "form", "form-file":
		d.Format, d.MaxItems = "name=value", 128
		if name == "form-file" {
			d.Format, d.MaxItems, d.MaxBytes = "name=path", 32, gomoufox.MaxTransferFileBytes
		}
		d.Notes = "Complete multipart body is limited to 50 MiB."
	case "header":
		d.Format = "name:value"
	case "clip":
		d.Format, d.Units = "x,y,width,height (numbers)", "CSS pixels"
	case "wait-selector":
		d.Format = "css-selector"
	case "url-filter":
		d.Format, d.MaxBytes = "glob", 2048
	case "allowed-origins", "allowed-hosts", "allow-schemes", "fields":
		d.Format = "csv"
		if name == "fields" {
			d.Enum = []string{"usage", "global", "cli_guardrail_overrides", "commands", "discovery", "mcp_tools", "flag_schema"}
			d.Notes = "Enum applies to each CSV item."
		}
	case "proxy", "server", "navigate-first":
		d.Format = "url"
		if name == "server" {
			d.DefaultSource = "GOMOUFOX_DAEMON when set"
		}
	case "server-token", "auth-token":
		d.Format = "secret"
		if name == "server-token" || command == "serve" {
			d.DefaultSource = "GOMOUFOX_DAEMON_TOKEN when set"
		}
	case "bind":
		d.Default = "127.0.0.1"
	case "humanize":
		d.Default, d.Format = "true", "true|false|seconds (number)"
		d.Notes = "Non-negative seconds. Optional value must use =, for example --humanize=0.5."
	case "wait":
		d.Default, d.Notes = true, "open waits for closure; --wait=false is unsupported."
	case "no-wait":
		d.Notes = "Unsupported; open must retain ownership until closure."
	case "headless":
		d.Default, d.Notes = true, "open and record force headful mode."
	case "headful":
		d.Notes = "open and record force headful mode."
	case "allow-localhost":
		d.Notes = "Browser CLI and MCP startup only. Permits explicit loopback HTTP(S); other private networks and metadata endpoints remain blocked."
	case "json":
		d.Notes = "Structured stdout where supported; diagnostics stay on stderr."
	case "locale":
		d.Format = "BCP-47 language tag"
	case "raw":
		d.Notes = "With --json, keeps body as a string inside the JSON envelope. Without --json, stdout is already body bytes. Excludes --out."
	case "no-content-warning":
		d.Notes = "Suppresses the text warning. Structured `provenance` metadata remains."
	case "version":
		if command == "skills show" {
			d.DefaultSource = "Latest embedded version of the selected skill."
		}
	}
	return d
}

func commandFlagConstraints(command string) []string {
	common := "Global flags can precede or follow the command. A separate CLI call creates a new browser context; --profile and storage state persist authentication, not page state."
	specific := map[string]string{
		"":               "--allow-schemes applies to browser CLI commands; mcp and serve reject scheme overrides. --allow-localhost permits loopback HTTP(S), not all private networks.",
		"get":            "--html, --text and --markdown are mutually exclusive; default is markdown. --cookies-file and --profile are mutually exclusive.",
		"eval":           "Requires --enable-eval and exactly one of --script or --script-file. --arg and --arg-file are mutually exclusive; the script must be a function to receive its JSON argument.",
		"fetch":          "--data and --data-file are mutually exclusive. Multipart excludes those body flags and Content-Type overrides. --out excludes --max-bytes and --raw; --max-file-bytes and --overwrite require --out. --cookies-file excludes --profile.",
		"open":           "Forces headful mode; rejects --headless and --no-wait. Storage export happens after window closure.",
		"record":         "Requires --out; forces headful mode. --profile is unsupported; use --cookies-file. --save-session must differ from --out.",
		"mcp":            "HTTP requires --auth-token. --allow-har-sensitive-values requires --allow-har-recording. Call browser_capabilities after startup to inspect effective gates, limits and scope.",
		"serve":          "Requires --auth-token or GOMOUFOX_DAEMON_TOKEN. URL guardrail overrides are unsupported.",
		"install":        "--python requires --runtime python.",
		"setup":          "--mcp-arg overrides are validated as a stdio MCP command; restart the agent connection after applying.",
		"session export": "Requires --out and a source profile through --from-profile or --profile.",
		"session import": "Requires --file and --out; --overwrite permits replacing storage state.",
		"skills export":  "Requires --out.",
	}
	result := []string{common}
	if note := specific[command]; note != "" {
		result = append(result, note)
	}
	if command == "session" || command == "skills" || command == "agents" {
		subcommand := "install"
		if command == "session" {
			subcommand = "import"
		}
		result = append(result, "Query a subcommand: gomoufox help "+command+" "+subcommand+" --json --fields flag_schema.")
	}
	return result
}
