package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	gomoufox "github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/buildinfo"
	"github.com/ehmo/gomoufox/internal/daemon"
	mcpserver "github.com/ehmo/gomoufox/internal/mcp"
	"github.com/ehmo/gomoufox/internal/netguard"
	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/safefile"
	"github.com/ehmo/gomoufox/internal/sidecar"
	skillreg "github.com/ehmo/gomoufox/internal/skills"
)

const (
	ExitOK             = 0
	ExitRuntime        = 1
	ExitUsage          = 2
	ExitUnavailable    = 3
	ExitVersion        = 4
	ExitNavigation     = 5
	ExitTimeout        = 6
	ExitElement        = 7
	ExitURLBlocked     = 8
	ExitSessionAuth    = 9
	ExitCommandTimeout = 124

	// ExitCommandTimout is kept for compatibility with older tests/imports.
	ExitCommandTimout = ExitCommandTimeout
)

type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type Hooks struct {
	Install      func(context.Context, InstallRequest) error
	Doctor       func(context.Context, DoctorRequest) (DoctorReport, error)
	Serve        func(context.Context, ServeRequest) error
	MCP          func(context.Context, MCPRequest) error
	Forward      func(context.Context, ForwardRequest) (ForwardResponse, error)
	LocalCommand func(context.Context, LocalCommandRequest) (LocalCommandResponse, error)
}

type Runner struct {
	Hooks    Hooks
	Resolver netguard.Resolver
}

type InstallRequest struct {
	Dir    string
	Python string
	Force  bool
}

type DoctorRequest struct {
	JSON bool
}

type ServeRequest struct {
	Bind               string
	Port               int
	AuthToken          string
	EnableEval         bool
	AllowSessionExport bool
	SessionDir         string
	AllowedOrigins     []string
	AllowedHosts       []string
}

type MCPRequest struct {
	Transport string
	Port      int
	AuthToken string
	Config    mcpserver.Config
	Stdin     io.Reader
	Stdout    io.Writer
}

type ForwardRequest struct {
	ServerURL string
	Token     string
	Endpoint  string
	Verb      string
	Envelope  daemon.Envelope
}

type ForwardResponse struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type LocalCommandRequest struct {
	Command        string
	Args           []string
	Flags          map[string]any
	Profile        string
	JSON           bool
	AllowedOrigins []string
	AllowedHosts   []string
	DisplayOut     string
}

type LocalCommandResponse struct {
	ExitCode int
	Stdout   []byte
	Stderr   string
}

type DoctorReport struct {
	Python      Check `json:"python"`
	Venv        Check `json:"venv"`
	CamoufoxPkg Check `json:"camoufox_pkg"`
	Playwright  Check `json:"playwright"`
	CamoufoxBin Check `json:"camoufox_bin"`
	Display     Check `json:"display"`
}

type Check struct {
	OK            bool   `json:"ok"`
	Version       string `json:"version,omitempty"`
	PkgVersion    string `json:"pkg_version,omitempty"`
	DriverVersion string `json:"driver_version,omitempty"`
	Match         *bool  `json:"match,omitempty"`
	Path          string `json:"path,omitempty"`
	Platform      string `json:"platform,omitempty"`
	Warning       string `json:"warning,omitempty"`
	Error         string `json:"error,omitempty"`
}

var (
	errDaemonUsage           = errors.New("invalid forwarded command")
	errSessionExportDisabled = errors.New("session_export_disabled")
	userHomeDir              = os.UserHomeDir
)

type daemonUsageError struct {
	err error
}

func (e daemonUsageError) Error() string {
	if e.err == nil {
		return errDaemonUsage.Error()
	}
	return e.err.Error()
}

func (e daemonUsageError) Is(target error) bool {
	return target == errDaemonUsage
}

func newDaemonUsageError(err error) error {
	if err == nil {
		return nil
	}
	return daemonUsageError{err: err}
}

func writeDiagnosticLine(w io.Writer, value any) {
	_, _ = fmt.Fprintln(w, policy.Redact(fmt.Sprint(value)))
}

func writeDiagnostic(w io.Writer, text string) {
	_, _ = fmt.Fprint(w, policy.Redact(text))
}

func writeDiagnosticf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprint(w, policy.Redact(fmt.Sprintf(format, args...)))
}

func (r Runner) Run(ctx context.Context, args []string, streams Streams) int {
	streams = normalizeStreams(streams)
	global, command, rest, err := parseGlobal(args)
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	if global.Help {
		return runHelp(global, appendCommand(command, rest...), streams)
	}
	if global.Version {
		_, _ = fmt.Fprintf(streams.Stdout, "gomoufox %s\n", buildinfo.Version)
		return ExitOK
	}
	if command == "" {
		_, _ = fmt.Fprintln(streams.Stderr, "usage: gomoufox <command> [flags]")
		return ExitUsage
	}
	if global.HeadlessSet && global.Headful && global.Headless {
		_, _ = fmt.Fprintln(streams.Stderr, "--headless and --headful are mutually exclusive")
		return ExitUsage
	}
	if global.Server != "" && canForward(command, rest) {
		if code := validateDaemonForward(global, streams.Stderr); code != ExitOK {
			return code
		}
		return r.forward(ctx, global, command, rest, streams)
	}
	if global.ServerSet && !canForward(command, rest) {
		writeDiagnosticf(streams.Stderr, "--server is not supported for %s\n", command)
		return ExitUsage
	}

	switch command {
	case "help":
		return runHelp(global, rest, streams)
	case "doctor":
		return r.runDoctor(ctx, global, rest, streams)
	case "install":
		return r.runInstall(ctx, rest, streams)
	case "eval":
		return r.runEval(ctx, global, rest, streams)
	case "serve":
		return r.runServe(ctx, global, rest, streams)
	case "mcp":
		return r.runMCP(ctx, global, rest, streams)
	case "skills":
		return runSkills(global, rest, streams)
	case "get":
		return r.runBrowserCommand(ctx, global, rest, commandGet, streams)
	case "screenshot":
		return r.runBrowserCommand(ctx, global, rest, commandScreenshot, streams)
	case "fetch":
		return r.runBrowserCommand(ctx, global, rest, commandFetch, streams)
	case "open":
		return r.runBrowserCommand(ctx, global, rest, commandOpen, streams)
	case "session":
		session, err := parseSession(rest)
		if err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitUsage
		}
		return r.executeLocalCommand(ctx, global, "session "+session.Subcommand, session.Parsed, streams)
	default:
		_, _ = fmt.Fprintf(streams.Stderr, "unknown command: %s\n", command)
		return ExitUsage
	}
}

func normalizeStreams(streams Streams) Streams {
	if streams.Stdin == nil {
		streams.Stdin = strings.NewReader("")
	}
	if streams.Stdout == nil {
		streams.Stdout = io.Discard
	}
	if streams.Stderr == nil {
		streams.Stderr = io.Discard
	}
	return streams
}

type commandHelp struct {
	Name     string   `json:"name"`
	Usage    string   `json:"usage"`
	Summary  string   `json:"summary,omitempty"`
	Flags    []string `json:"flags,omitempty"`
	Examples []string `json:"examples,omitempty"`
}

type helpCatalog struct {
	Usage     string        `json:"usage,omitempty"`
	Global    []string      `json:"global,omitempty"`
	Commands  []commandHelp `json:"commands,omitempty"`
	Discovery []string      `json:"discovery,omitempty"`
	MCPTools  []string      `json:"mcp_tools,omitempty"`
}

func runHelp(global globalFlags, args []string, streams Streams) int {
	opts, err := parseHelp(args)
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	if opts.Command != "" {
		if _, ok := helpForCommand(opts.Command); !ok {
			_, _ = fmt.Fprintf(streams.Stderr, "unknown help topic: %s\n", opts.Command)
			return ExitUsage
		}
	}
	if global.JSON {
		encoder := json.NewEncoder(streams.Stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(buildHelpCatalog(opts)); err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitRuntime
		}
		return ExitOK
	}
	printHelpText(streams.Stdout, opts.Command)
	return ExitOK
}

type helpOptions struct {
	Command string
	Fields  map[string]bool
	Full    bool
}

func parseHelp(args []string) (helpOptions, error) {
	parsed, err := parseFlags(args, helpFlagSpecs())
	if err != nil {
		return helpOptions{}, err
	}
	if len(parsed.Positionals) > 2 {
		return helpOptions{}, errors.New("usage: gomoufox help [command] [--json] [--fields <list>] [--full]")
	}
	fields, err := helpFields(parsed.value("fields"))
	if err != nil {
		return helpOptions{}, err
	}
	opts := helpOptions{Fields: fields, Full: parsed.bool("full")}
	if len(parsed.Positionals) > 0 {
		opts.Command = strings.Join(parsed.Positionals, " ")
	}
	return opts, nil
}

func helpFields(raw string) (map[string]bool, error) {
	if raw == "" {
		return nil, nil
	}
	out := map[string]bool{}
	for _, field := range splitCSV(raw) {
		switch field {
		case "usage", "global", "commands", "discovery", "mcp_tools":
			out[field] = true
		default:
			return nil, fmt.Errorf("unknown help field: %s", field)
		}
	}
	return out, nil
}

func appendCommand(command string, rest ...string) []string {
	if command == "" {
		return nil
	}
	out := make([]string, 0, len(rest)+1)
	out = append(out, command)
	out = append(out, rest...)
	return out
}

func buildHelpCatalog(opts helpOptions) helpCatalog {
	all := helpCatalog{
		Usage:    "gomoufox <command> [flags]",
		Global:   globalHelpFlags(),
		Commands: commandHelpIndex(),
		Discovery: []string{
			"gomoufox help --json",
			"gomoufox help <command> --json",
			"gomoufox help --json --fields commands",
			"gomoufox skills list --json",
			"gomoufox skills show core --json",
			"gomoufox mcp --help",
		},
	}
	if opts.Command != "" {
		doc, _ := helpForCommand(opts.Command)
		all.Commands = []commandHelp{doc}
	}
	if opts.Full && opts.Command == "" {
		all.Commands = commandHelps()
	}
	if opts.Command == "mcp" {
		for _, tool := range mcpserver.Tools() {
			all.MCPTools = append(all.MCPTools, tool.Name)
		}
	}
	if len(opts.Fields) == 0 {
		return all
	}
	catalog := helpCatalog{}
	if opts.Fields["usage"] {
		catalog.Usage = all.Usage
	}
	if opts.Fields["global"] {
		catalog.Global = all.Global
	}
	if opts.Fields["commands"] {
		catalog.Commands = all.Commands
	}
	if opts.Fields["discovery"] {
		catalog.Discovery = all.Discovery
	}
	if opts.Fields["mcp_tools"] {
		catalog.MCPTools = all.MCPTools
	}
	return catalog
}

func commandHelpIndex() []commandHelp {
	commands := commandHelps()
	out := make([]commandHelp, 0, len(commands))
	for _, command := range commands {
		out = append(out, commandHelp{Name: command.Name, Usage: command.Usage})
	}
	return out
}

func printHelpText(w io.Writer, command string) {
	if command != "" {
		doc, _ := helpForCommand(command)
		_, _ = fmt.Fprintf(w, "usage: %s\n%s\n", doc.Usage, doc.Summary)
		if len(doc.Flags) > 0 {
			_, _ = fmt.Fprintf(w, "flags: %s\n", strings.Join(doc.Flags, " "))
		}
		if command == "mcp" {
			tools := make([]string, 0, len(mcpserver.Tools()))
			for _, tool := range mcpserver.Tools() {
				tools = append(tools, tool.Name)
			}
			_, _ = fmt.Fprintf(w, "tools: %s\n", strings.Join(tools, ","))
		}
		if len(doc.Examples) > 0 {
			_, _ = fmt.Fprintf(w, "examples: %s\n", strings.Join(doc.Examples, " ; "))
		}
		return
	}
	_, _ = fmt.Fprintln(w, "usage: gomoufox <command> [flags]")
	_, _ = fmt.Fprintf(w, "commands: %s\n", strings.Join(commandNames(), " "))
	_, _ = fmt.Fprintf(w, "global: %s\n", strings.Join(globalHelpFlags(), " "))
	_, _ = fmt.Fprintln(w, "discovery: gomoufox help --json ; gomoufox skills list --json ; gomoufox mcp --help")
}

func commandNames() []string {
	commands := commandHelps()
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		out = append(out, command.Name)
	}
	return out
}

func helpForCommand(name string) (commandHelp, bool) {
	for _, command := range commandHelps() {
		if command.Name == name {
			return command, true
		}
	}
	for _, command := range nestedCommandHelps() {
		if command.Name == name {
			return command, true
		}
	}
	return commandHelp{}, false
}

func nestedCommandHelps() []commandHelp {
	return []commandHelp{
		{
			Name:     "session export",
			Usage:    "gomoufox session export --out <path> [--profile <dir>|--from-profile <dir>]",
			Summary:  "export Playwright storage_state JSON from a profile",
			Flags:    []string{"--out", "--profile", "--from-profile"},
			Examples: []string{"gomoufox session export --out state.json --profile profiles/site"},
		},
		{
			Name:     "session import",
			Usage:    "gomoufox session import --file <path> --out <profile> [--overwrite]",
			Summary:  "import Playwright storage_state JSON into a profile directory",
			Flags:    []string{"--file", "--out", "--overwrite"},
			Examples: []string{"gomoufox session import --file state.json --out profiles/site --overwrite"},
		},
		{
			Name:     "skills list",
			Usage:    "gomoufox skills list [--json]",
			Summary:  "list embedded versioned agent skills",
			Flags:    []string{"--json"},
			Examples: []string{"gomoufox skills list --json"},
		},
		{
			Name:     "skills show",
			Usage:    "gomoufox skills show <name> [--version <v>] [--json]",
			Summary:  "print one embedded agent skill",
			Flags:    []string{"--version", "--json"},
			Examples: []string{"gomoufox skills show core --json"},
		},
		{
			Name:     "skills export",
			Usage:    "gomoufox skills export --out <dir> [--force] [--json]",
			Summary:  "write installable skill files to a directory",
			Flags:    []string{"--out", "--force", "--json"},
			Examples: []string{"gomoufox skills export --out ./skills"},
		},
		{
			Name:     "skills install",
			Usage:    "gomoufox skills install [--target codex] [--dir <dir>] [--force] [--dry-run] [--json]",
			Summary:  "install version-matched agent skills for a supported agent target",
			Flags:    []string{"--target", "--dir", "--force", "--dry-run", "--json"},
			Examples: []string{"gomoufox skills install --target codex --dry-run --json"},
		},
	}
}

func globalHelpFlags() []string {
	return []string{
		"--json",
		"--help",
		"--version",
		"--verbose",
		"--profile <dir>",
		"--timeout <dur>",
		"--proxy <url>",
		"--locale <tag>",
		"--os <windows|macos|linux>",
		"--headless|--headful",
		"--server <url>",
		"--server-token <token>",
		"--allow-schemes <csv>",
		"--allow-private-ips",
	}
}

func commandHelps() []commandHelp {
	return []commandHelp{
		{
			Name:     "doctor",
			Usage:    "gomoufox doctor [--json]",
			Summary:  "check local Python, venv, Camoufox, Playwright, binary, and display readiness",
			Examples: []string{"gomoufox --json doctor"},
		},
		{
			Name:    "install",
			Usage:   "gomoufox install [--dir <venv>] [--python <path>] [--force]",
			Summary: "provision the pinned Camoufox Python environment and browser binary",
			Flags:   []string{"--dir", "--python", "--force"},
		},
		{
			Name:     "open",
			Usage:    "gomoufox open <url> [--profile <dir>] [--save-session <path>] [--wait]",
			Summary:  "open a headful browser for login or manual inspection",
			Flags:    []string{"--wait", "--save-session", "--humanize"},
			Examples: []string{"gomoufox open https://example.com --profile profiles/site"},
		},
		{
			Name:     "get",
			Usage:    "gomoufox get <url> [--html|--text|--markdown] [--wait-selector <css>]",
			Summary:  "navigate and print page content with guardrails and response caps",
			Flags:    []string{"--html", "--text", "--markdown", "--wait-selector", "--wait-load-state", "--max-bytes", "--cookies-file"},
			Examples: []string{"gomoufox --json get https://example.com --text"},
		},
		{
			Name:     "screenshot",
			Usage:    "gomoufox screenshot <url> --out <path> [--full-page|--wait-selector <css>]",
			Summary:  "capture a page or element screenshot",
			Flags:    []string{"--out", "--full-page", "--width", "--height", "--wait-selector", "--wait-load-state", "--quality", "--clip", "--max-bytes"},
			Examples: []string{"gomoufox screenshot https://example.com --out page.png"},
		},
		{
			Name:     "eval",
			Usage:    "gomoufox eval <url> --enable-eval (--script <js>|--script-file <path>)",
			Summary:  "run JavaScript only when explicitly enabled",
			Flags:    []string{"--enable-eval", "--script", "--script-file", "--wait-selector", "--wait-load-state", "--arg"},
			Examples: []string{"gomoufox --json eval https://example.com --enable-eval --script 'document.title'"},
		},
		{
			Name:     "fetch",
			Usage:    "gomoufox fetch <url> [--method GET] [--data <text>] [--navigate-first <url>]",
			Summary:  "execute an HTTP request from inside the browser context",
			Flags:    []string{"--method", "--data", "--data-file", "--header", "--content-type", "--cookies-file", "--navigate-first", "--max-bytes", "--raw"},
			Examples: []string{"gomoufox --json fetch https://api.example.com/me --profile profiles/site"},
		},
		{
			Name:     "session",
			Usage:    "gomoufox session <export|import> [flags]",
			Summary:  "import or export Playwright storage_state JSON",
			Flags:    []string{"export --out <path>", "export --from-profile <dir>", "import --file <path>", "import --out <path>", "import --overwrite"},
			Examples: []string{"gomoufox session export --out state.json --profile profiles/site"},
		},
		{
			Name:     "skills",
			Usage:    "gomoufox skills <list|show|export|install> [flags]",
			Summary:  "list, print, export, or install version-matched agent skills",
			Flags:    []string{"list", "show <name>", "export --out <dir>", "install --target codex", "--version", "--dir", "--force", "--dry-run"},
			Examples: []string{"gomoufox skills list --json", "gomoufox skills show core", "gomoufox skills export --out ./skills", "gomoufox skills install --target codex --dry-run --json"},
		},
		{
			Name:    "serve",
			Usage:   "gomoufox serve --auth-token <token> [--bind 127.0.0.1] [--port 3741] [--session-dir <dir>]",
			Summary: "run the local daemon HTTP API for CLI forwarding",
			Flags:   []string{"--auth-token", "--bind", "--port", "--session-dir", "--enable-eval", "--allow-session-export", "--allowed-origins", "--allowed-hosts"},
		},
		{
			Name:    "mcp",
			Usage:   "gomoufox mcp [--transport stdio|http] [--session-dir <dir>]",
			Summary: "run the token-capped MCP browser tool server for agents",
			Flags: []string{
				"--transport",
				"--toolset",
				"--auth-token",
				"--port",
				"--session-dir",
				"--allowed-origins",
				"--allowed-hosts",
				"--max-input-bytes",
				"--max-response-bytes",
				"--session-ttl",
				"--max-sessions",
				"--enable-eval",
				"--no-content-warning",
				"--allow-browser-fetch",
				"--allow-cookie-values",
				"--allow-cookie-mutation",
				"--allow-snapshot-values",
				"--allow-session-export",
				"--allow-session-import",
				"--allow-session-proxy",
				"--allow-file-upload",
			},
			Examples: []string{"gomoufox mcp", "gomoufox mcp --toolset core", "gomoufox mcp --allow-browser-fetch --allowed-hosts api.example.com", "gomoufox mcp --transport http --auth-token $TOKEN"},
		},
		{
			Name:     "help",
			Usage:    "gomoufox help [command] [--json] [--fields <list>] [--full]",
			Summary:  "print compact command discovery for humans or agents",
			Flags:    []string{"--json", "--fields", "--full"},
			Examples: []string{"gomoufox help --json", "gomoufox help mcp --json"},
		},
	}
}

func (r Runner) runServe(ctx context.Context, global globalFlags, args []string, streams Streams) int {
	if global.AllowPrivateIPs || global.AllowSchemes != "" {
		_, _ = fmt.Fprintln(streams.Stderr, "gomoufox serve does not allow URL guardrail overrides")
		return ExitUsage
	}
	parsed, err := parseFlags(args, serveFlagSpecs())
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	if len(parsed.Positionals) != 0 {
		_, _ = fmt.Fprintln(streams.Stderr, "usage: gomoufox serve [flags]")
		return ExitUsage
	}
	req := ServeRequest{
		Bind:               parsed.valueDefault("bind", "127.0.0.1"),
		Port:               3741,
		AuthToken:          parsed.value("auth-token"),
		EnableEval:         parsed.bool("enable-eval"),
		AllowSessionExport: parsed.bool("allow-session-export"),
		SessionDir:         parsed.valueDefault("session-dir", defaultServeSessionDir()),
		AllowedOrigins:     splitCSV(parsed.value("allowed-origins")),
		AllowedHosts:       splitCSV(parsed.value("allowed-hosts")),
	}
	if portRaw := parsed.value("port"); portRaw != "" {
		port, err := parsePort(portRaw)
		if err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitUsage
		}
		req.Port = port
	}
	if req.AuthToken == "" {
		_, _ = fmt.Fprintln(streams.Stderr, "gomoufox serve requires --auth-token")
		return ExitSessionAuth
	}
	if parsed.has("bind") && !isLoopbackBind(req.Bind) {
		_, _ = fmt.Fprintf(streams.Stderr, "WARNING: gomoufox serve binding to non-loopback address %s\n", req.Bind)
	}
	serve := r.Hooks.Serve
	if serve == nil {
		serve = defaultServe
	}
	if err := serve(ctx, req); err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return mapError(err)
	}
	return ExitOK
}

func defaultServe(ctx context.Context, req ServeRequest) error {
	server, err := newDefaultServeDaemon(req)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: fmt.Sprintf("%s:%d", req.Bind, req.Port), Handler: server}
	go func() {
		<-ctx.Done()
		_ = httpServer.Shutdown(context.Background())
	}()
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func newDefaultServeDaemon(req ServeRequest) (*daemon.Server, error) {
	return daemon.New(daemon.Config{
		AuthToken:          req.AuthToken,
		EnableEval:         req.EnableEval,
		AllowSessionExport: req.AllowSessionExport,
		Ready:              true,
		Executor: func(ctx context.Context, verb string, env daemon.Envelope) daemon.Result {
			return executeDaemonLocalCommand(ctx, req, verb, env)
		},
	})
}

func executeDaemonLocalCommand(ctx context.Context, serve ServeRequest, verb string, env daemon.Envelope) daemon.Result {
	localReq, err := localRequestFromDaemonEnvelope(verb, env)
	if err != nil {
		return daemonErrorResult(err)
	}
	if err := validateDaemonSessionExportEnabled(serve, localReq.Command); err != nil {
		return daemonErrorResult(err)
	}
	if err := jailDaemonSessionPaths(serve, &localReq); err != nil {
		return daemonErrorResult(newDaemonUsageError(errors.New("path_rejected")))
	}
	if err := validateDaemonLocalCommand(ctx, serve, localReq); err != nil {
		return daemonErrorResult(err)
	}
	localReq.AllowedOrigins = append([]string{}, serve.AllowedOrigins...)
	localReq.AllowedHosts = append([]string{}, serve.AllowedHosts...)
	resp, err := defaultLocalCommand(ctx, localReq)
	if err != nil {
		return daemonErrorResult(err)
	}
	return daemon.Result{ExitCode: resp.ExitCode, Stdout: string(resp.Stdout), Stderr: resp.Stderr}
}

func daemonErrorResult(err error) daemon.Result {
	return daemon.Result{ExitCode: mapError(err), Stderr: policy.Redact(err.Error()) + "\n"}
}

func localRequestFromDaemonEnvelope(verb string, env daemon.Envelope) (LocalCommandRequest, error) {
	flags, err := normalizeDaemonFlags(verb, env.Flags)
	if err != nil {
		return LocalCommandRequest{}, newDaemonUsageError(err)
	}
	return LocalCommandRequest{
		Command: verb,
		Args:    append([]string{}, env.Args...),
		Flags:   flags,
		Profile: env.Profile,
		JSON:    env.JSON,
	}, nil
}

func jailDaemonSessionPaths(serve ServeRequest, req *LocalCommandRequest) error {
	if req.Command != "session import" && req.Command != "session export" {
		return nil
	}
	root := serve.SessionDir
	if root == "" {
		root = defaultServeSessionDir()
	}
	jail, err := policy.NewJail(root)
	if err != nil {
		return err
	}
	switch req.Command {
	case "session import":
		src, err := jail.ResolveRead(flagString(*req, "file", ""))
		if err != nil {
			return err
		}
		dst, err := jail.ResolveWrite(flagString(*req, "out", ""), localBool(*req, "overwrite"))
		if err != nil {
			return err
		}
		displayOut, _ := jail.ConfinedPath(dst)
		req.Flags["file"] = src
		req.Flags["out"] = dst
		req.DisplayOut = displayOut
	case "session export":
		dst, err := jail.ResolveWrite(flagString(*req, "out", ""), false)
		if err != nil {
			return err
		}
		displayOut, _ := jail.ConfinedPath(dst)
		profileJail, err := policy.NewJail(filepath.Join(jail.Root, "profiles"))
		if err != nil {
			return err
		}
		profile, err := profileJail.ResolveDir(flagString(*req, "from_profile", req.Profile))
		if err != nil {
			return err
		}
		req.Flags["out"] = dst
		req.DisplayOut = displayOut
		if _, ok := req.Flags["from_profile"]; ok {
			req.Flags["from_profile"] = profile
		} else {
			req.Profile = profile
		}
	}
	return nil
}

func normalizeDaemonFlags(command string, flags map[string]any) (map[string]any, error) {
	specs, ok := daemonFlagSpecs(command)
	if !ok {
		return nil, fmt.Errorf("unsupported daemon command: %s", command)
	}
	out := make(map[string]any, len(flags))
	for name, raw := range flags {
		spec, ok := specs[name]
		if !ok {
			return nil, fmt.Errorf("unknown flag: --%s", strings.ReplaceAll(name, "_", "-"))
		}
		switch spec.Kind {
		case flagBool:
			value, ok := raw.(bool)
			if !ok {
				return nil, fmt.Errorf("--%s expects a boolean value", strings.ReplaceAll(name, "_", "-"))
			}
			out[name] = value
		case flagValue, flagOptionalValue:
			switch value := raw.(type) {
			case string:
				out[name] = value
			case []string:
				if !spec.Repeat {
					return nil, fmt.Errorf("--%s may only be provided once", strings.ReplaceAll(name, "_", "-"))
				}
				out[name] = append([]string{}, value...)
			case []any:
				if !spec.Repeat {
					return nil, fmt.Errorf("--%s may only be provided once", strings.ReplaceAll(name, "_", "-"))
				}
				values := make([]string, 0, len(value))
				for _, item := range value {
					text, ok := item.(string)
					if !ok {
						return nil, fmt.Errorf("--%s expects string values", strings.ReplaceAll(name, "_", "-"))
					}
					values = append(values, text)
				}
				out[name] = values
			default:
				return nil, fmt.Errorf("--%s expects a string value", strings.ReplaceAll(name, "_", "-"))
			}
		}
	}
	return out, nil
}

func daemonFlagSpecs(command string) (map[string]flagSpec, bool) {
	specs := map[string]flagSpec{
		"timeout":  {Kind: flagValue},
		"proxy":    {Kind: flagValue},
		"locale":   {Kind: flagValue},
		"os":       {Kind: flagValue},
		"headful":  {Kind: flagBool},
		"headless": {Kind: flagBool},
	}
	add := func(name string, spec flagSpec) {
		specs[flagEnvelopeName(name)] = spec
	}
	switch command {
	case "get":
		for name, spec := range map[string]flagSpec{
			"markdown":        {Kind: flagBool},
			"html":            {Kind: flagBool},
			"text":            {Kind: flagBool},
			"wait-selector":   {Kind: flagValue},
			"wait-load-state": {Kind: flagValue},
			"max-bytes":       {Kind: flagValue},
			"cookies-file":    {Kind: flagValue},
		} {
			add(name, spec)
		}
	case "screenshot":
		for name, spec := range map[string]flagSpec{
			"out":             {Kind: flagValue},
			"full-page":       {Kind: flagBool},
			"width":           {Kind: flagValue},
			"height":          {Kind: flagValue},
			"wait-selector":   {Kind: flagValue},
			"wait-load-state": {Kind: flagValue},
			"quality":         {Kind: flagValue},
			"clip":            {Kind: flagValue},
			"max-bytes":       {Kind: flagValue},
		} {
			add(name, spec)
		}
	case "fetch":
		for name, spec := range map[string]flagSpec{
			"method":         {Kind: flagValue},
			"data":           {Kind: flagValue},
			"data-file":      {Kind: flagValue},
			"header":         {Kind: flagValue, Repeat: true},
			"content-type":   {Kind: flagValue},
			"cookies-file":   {Kind: flagValue},
			"navigate-first": {Kind: flagValue},
			"max-bytes":      {Kind: flagValue},
			"raw":            {Kind: flagBool},
		} {
			add(name, spec)
		}
	case "eval":
		for name, spec := range map[string]flagSpec{
			"script":          {Kind: flagValue},
			"script-file":     {Kind: flagValue},
			"wait-selector":   {Kind: flagValue},
			"wait-load-state": {Kind: flagValue},
			"arg":             {Kind: flagValue},
			"enable-eval":     {Kind: flagBool},
		} {
			add(name, spec)
		}
	case "session import":
		for name, spec := range map[string]flagSpec{
			"file":      {Kind: flagValue},
			"out":       {Kind: flagValue},
			"overwrite": {Kind: flagBool},
		} {
			add(name, spec)
		}
	case "session export":
		for name, spec := range map[string]flagSpec{
			"out":          {Kind: flagValue},
			"from-profile": {Kind: flagValue},
		} {
			add(name, spec)
		}
	default:
		return nil, false
	}
	return specs, true
}

func validateDaemonLocalCommand(ctx context.Context, serve ServeRequest, req LocalCommandRequest) error {
	if err := validateDaemonCommandSyntax(req); err != nil {
		return newDaemonUsageError(err)
	}
	if err := validateDaemonGlobalFlags(req); err != nil {
		return newDaemonUsageError(err)
	}
	if proxy := flagString(req, "proxy", ""); proxy != "" {
		validator := netguard.NewValidator(policy.DefaultConfig(), nil)
		if _, err := validator.ValidateProxy(proxy); err != nil {
			return fmt.Errorf("invalid --proxy: %v", err)
		}
	}
	urls := daemonURLsForValidation(req)
	if len(urls) == 0 {
		return nil
	}
	cfg := policy.DefaultConfig()
	cfg.AllowedOrigins = append([]string{}, serve.AllowedOrigins...)
	cfg.AllowedHosts = append([]string{}, serve.AllowedHosts...)
	validator := netguard.NewValidator(cfg, nil)
	for _, raw := range urls {
		if raw == "" {
			continue
		}
		if _, err := validator.Validate(ctx, raw); err != nil {
			return err
		}
	}
	return nil
}

func validateDaemonSessionExportEnabled(serve ServeRequest, command string) error {
	if command == "session export" && !serve.AllowSessionExport {
		return newDaemonUsageError(errSessionExportDisabled)
	}
	return nil
}

func validateDaemonGlobalFlags(req LocalCommandRequest) error {
	if raw := flagString(req, "timeout", ""); raw != "" {
		if _, err := ParseDuration(raw, 30*time.Second); err != nil {
			return err
		}
	}
	if osName := flagString(req, "os", ""); osName != "" && osName != "windows" && osName != "macos" && osName != "linux" {
		return errors.New("--os must be windows, macos, or linux")
	}
	return nil
}

func validateDaemonCommandSyntax(req LocalCommandRequest) error {
	args := daemonCommandArgs(req)
	switch req.Command {
	case "get":
		_, err := parseGet(args)
		return err
	case "screenshot":
		_, err := parseScreenshot(args)
		return err
	case "fetch":
		_, err := parseFetch(args)
		return err
	case "eval":
		_, err := parseEval(args)
		return err
	case "session import":
		_, err := parseSession(append([]string{"import"}, args...))
		return err
	case "session export":
		_, err := parseSession(append([]string{"export"}, args...))
		return err
	default:
		return fmt.Errorf("unsupported daemon command: %s", req.Command)
	}
}

func daemonCommandArgs(req LocalCommandRequest) []string {
	args := append([]string{}, req.Args...)
	for name, raw := range req.Flags {
		if isDaemonGlobalFlag(name) {
			continue
		}
		flag := "--" + strings.ReplaceAll(name, "_", "-")
		switch value := raw.(type) {
		case bool:
			if value {
				args = append(args, flag)
			} else {
				args = append(args, flag+"=false")
			}
		case string:
			args = append(args, flag, value)
		case []string:
			for _, item := range value {
				args = append(args, flag, item)
			}
		}
	}
	return args
}

func isDaemonGlobalFlag(name string) bool {
	switch name {
	case "timeout", "proxy", "locale", "os", "headful", "headless":
		return true
	default:
		return false
	}
}

func daemonURLsForValidation(req LocalCommandRequest) []string {
	if len(req.Args) == 0 {
		return nil
	}
	switch req.Command {
	case "get", "screenshot", "eval":
		return []string{req.Args[0]}
	case "fetch":
		return []string{req.Args[0], flagString(req, "navigate_first", "")}
	default:
		return nil
	}
}

func defaultMCP(ctx context.Context, req MCPRequest) error {
	server, err := mcpserver.New(req.Config)
	if err != nil {
		return err
	}
	switch req.Transport {
	case "stdio":
		return mcpserver.ServeStdio(ctx, server, req.Stdin, req.Stdout)
	case "http":
		httpServer := &http.Server{
			Addr:    fmt.Sprintf("127.0.0.1:%d", req.Port),
			Handler: mcpserver.NewHTTPHandler(server, req.AuthToken),
		}
		go func() {
			<-ctx.Done()
			_ = httpServer.Shutdown(context.Background())
		}()
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	default:
		return fmt.Errorf("unsupported MCP transport: %s", req.Transport)
	}
}

func (r Runner) runMCP(ctx context.Context, global globalFlags, args []string, streams Streams) int {
	if global.AllowPrivateIPs || global.AllowSchemes != "" {
		_, _ = fmt.Fprintln(streams.Stderr, "MCP does not allow URL guardrail overrides")
		return ExitUsage
	}
	parsed, err := parseFlags(args, mcpFlagSpecs())
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	if len(parsed.Positionals) != 0 {
		_, _ = fmt.Fprintln(streams.Stderr, "usage: gomoufox mcp [flags]")
		return ExitUsage
	}
	transport := parsed.valueDefault("transport", "stdio")
	if transport != "stdio" && transport != "http" {
		_, _ = fmt.Fprintln(streams.Stderr, "--transport must be stdio or http")
		return ExitUsage
	}
	if transport == "http" && parsed.value("auth-token") == "" {
		_, _ = fmt.Fprintln(streams.Stderr, "HTTP transport requires --auth-token; use stdio transport for local-only use")
		return ExitSessionAuth
	}
	toolset := parsed.valueDefault("toolset", mcpserver.ToolsetFull)
	if toolset != mcpserver.ToolsetFull && toolset != mcpserver.ToolsetCore {
		_, _ = fmt.Fprintln(streams.Stderr, "--toolset must be full or core")
		return ExitUsage
	}
	port := 3742
	if portRaw := parsed.value("port"); portRaw != "" {
		parsedPort, err := parsePort(portRaw)
		if err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitUsage
		}
		port = parsedPort
	}
	cfg := policy.DefaultConfig()
	if raw := parsed.value("max-input-bytes"); raw != "" {
		n, err := parseIntFlag(raw, "--max-input-bytes")
		if err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitUsage
		}
		if _, err := policy.ClampInputCap(n); err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitUsage
		}
		cfg.MaxInputBytes = n
	}
	if raw := parsed.value("max-response-bytes"); raw != "" {
		n, err := parseIntFlag(raw, "--max-response-bytes")
		if err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitUsage
		}
		if _, err := policy.ClampResponseCap(n); err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitUsage
		}
		cfg.MaxResponseBytes = n
	}
	if raw := parsed.value("session-ttl"); raw != "" {
		ttl, err := ParseDuration(raw, policy.DefaultConfig().SessionTTL)
		if err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitUsage
		}
		if ttl > 24*time.Hour {
			_, _ = fmt.Fprintln(streams.Stderr, "--session-ttl must be at most 24h")
			return ExitUsage
		}
		cfg.SessionTTL = ttl
	}
	if raw := parsed.value("max-sessions"); raw != "" {
		n, err := parseIntFlag(raw, "--max-sessions")
		if err != nil || n < 1 || n > 20 {
			_, _ = fmt.Fprintln(streams.Stderr, "--max-sessions must be between 1 and 20")
			return ExitUsage
		}
		cfg.MaxSessions = n
	}
	cfg.EnableEval = parsed.bool("enable-eval")
	cfg.ContentWarning = !parsed.bool("no-content-warning")
	cfg.AllowBrowserFetch = parsed.bool("allow-browser-fetch")
	cfg.AllowCookieValues = parsed.bool("allow-cookie-values")
	cfg.AllowCookieMutation = parsed.bool("allow-cookie-mutation")
	cfg.AllowSnapshotValues = parsed.bool("allow-snapshot-values")
	cfg.AllowSessionExport = parsed.bool("allow-session-export")
	cfg.AllowSessionImport = parsed.bool("allow-session-import")
	cfg.AllowSessionProxy = parsed.bool("allow-session-proxy")
	cfg.AllowFileUpload = parsed.bool("allow-file-upload")
	cfg.AllowedOrigins = splitCSV(parsed.value("allowed-origins"))
	cfg.AllowedHosts = splitCSV(parsed.value("allowed-hosts"))
	req := MCPRequest{
		Transport: transport,
		Port:      port,
		AuthToken: parsed.value("auth-token"),
		Config: mcpserver.Config{
			Policy:     cfg,
			SessionDir: parsed.valueDefault("session-dir", defaultMCPSessionDir()),
			Toolset:    toolset,
		},
		Stdin:  streams.Stdin,
		Stdout: streams.Stdout,
	}
	run := r.Hooks.MCP
	if run == nil {
		run = defaultMCP
	}
	if err := run(ctx, req); err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return mapError(err)
	}
	return ExitOK
}

func defaultMCPSessionDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "gomoufox", "sessions")
}

func defaultServeSessionDir() string {
	return defaultMCPSessionDir()
}

func (r Runner) runInstall(ctx context.Context, args []string, streams Streams) int {
	parsed, err := parseFlags(args, installFlagSpecs())
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	if len(parsed.Positionals) != 0 {
		_, _ = fmt.Fprintln(streams.Stderr, "usage: gomoufox install [flags]")
		return ExitUsage
	}
	req := InstallRequest{Dir: parsed.value("dir"), Python: parsed.value("python"), Force: parsed.bool("force")}
	install := r.Hooks.Install
	if install == nil {
		install = defaultInstallEnsureInstalled
	}
	if err := install(ctx, req); err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return mapError(err)
	}
	_, _ = fmt.Fprintln(streams.Stdout, ">> gomoufox install complete")
	return ExitOK
}

var defaultInstallEnsureInstalled = func(ctx context.Context, req InstallRequest) error {
	return ensureInstalledForCLI(ctx, func(o *gomoufox.InstallOptions) {
		o.VenvDir = req.Dir
		o.PythonBin = req.Python
		o.ForceReinstall = req.Force
	})
}

func (r Runner) runDoctor(ctx context.Context, global globalFlags, args []string, streams Streams) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(streams.Stderr, "usage: gomoufox doctor")
		return ExitUsage
	}
	doctor := r.Hooks.Doctor
	if doctor == nil {
		doctor = defaultDoctor
	}
	report, err := doctor(ctx, DoctorRequest{JSON: global.JSON})
	report = redactDoctorReport(report)
	if global.JSON {
		_ = json.NewEncoder(streams.Stdout).Encode(report)
	} else {
		printDoctor(streams.Stdout, report)
	}
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUnavailable
	}
	if report.hasFailure() {
		return ExitUnavailable
	}
	return ExitOK
}

var defaultDoctorEnsureInstalled = func(ctx context.Context) error {
	return ensureInstalledForCLI(ctx, func(o *gomoufox.InstallOptions) {
		o.SkipBinaryFetch = true
	})
}

var ensureInstalledForCLI = gomoufox.EnsureInstalled
var doctorGOOS = runtime.GOOS
var doctorLookupEnv = os.LookupEnv
var doctorLookPath = exec.LookPath

func defaultDoctor(ctx context.Context, req DoctorRequest) (DoctorReport, error) {
	match := true
	report := DoctorReport{
		Python:  Check{OK: true},
		Venv:    Check{OK: true},
		Display: doctorDisplayCheck(),
	}
	err := defaultDoctorEnsureInstalled(ctx)
	if err != nil {
		report.CamoufoxPkg = Check{OK: false, Error: policy.Redact(err.Error())}
		return report, err
	}
	report.CamoufoxPkg = Check{OK: true, Version: sidecar.RequiredCamoufox}
	report.Playwright = Check{OK: true, PkgVersion: sidecar.RequiredPlaywright, DriverVersion: sidecar.RequiredPlaywright, Match: &match}
	report.CamoufoxBin = doctorCamoufoxBinCheck()
	return report, nil
}

func doctorCamoufoxBinCheck() Check {
	check := Check{OK: true}
	if path, ok := doctorLookupEnv(sidecar.EnvCamoufoxPath); ok && strings.TrimSpace(path) != "" {
		check.Path = path
	}
	if trust, ok := doctorLookupEnv(sidecar.EnvTrustUnverifiedCamoufoxPath); ok && trust == "1" && check.Path != "" {
		check.Warning = "using unverified offline Camoufox path; manifest verification is disabled by " + sidecar.EnvTrustUnverifiedCamoufoxPath + "=1"
	}
	return check
}

func doctorDisplayCheck() Check {
	if doctorGOOS != "linux" {
		return Check{OK: true}
	}
	if display, ok := doctorLookupEnv("DISPLAY"); ok && strings.TrimSpace(display) != "" {
		return Check{OK: true, Path: display}
	}
	if auto, ok := doctorLookupEnv("GOMOUFOX_AUTO_DISPLAY"); ok && auto == "1" {
		if _, err := doctorLookPath("Xvfb"); err != nil {
			return Check{OK: false, Warning: "DISPLAY not set and GOMOUFOX_AUTO_DISPLAY=1, but Xvfb was not found in PATH; headful mode needs Xvfb or an existing display"}
		}
		return Check{OK: true, Warning: "DISPLAY not set; GOMOUFOX_AUTO_DISPLAY=1 will try to start Xvfb for headful runs"}
	}
	return Check{OK: false, Warning: "DISPLAY not set; headful mode requires GOMOUFOX_AUTO_DISPLAY=1 or an existing display"}
}

func printDoctor(w io.Writer, report DoctorReport) {
	printCheck(w, "python3", report.Python)
	printCheck(w, "venv", report.Venv)
	printCheck(w, "camoufox", report.CamoufoxPkg)
	printCheck(w, "playwright", report.Playwright)
	printCheck(w, "camoufox-bin", report.CamoufoxBin)
	printCheck(w, "display", report.Display)
}

func printCheck(w io.Writer, name string, check Check) {
	tag := "OK"
	message := check.message()
	if check.Warning != "" {
		tag = "WARN"
		message = check.Warning
	} else if !check.OK {
		tag = "FAIL"
		message = check.Error
	}
	_, _ = fmt.Fprintf(w, "[%s]  %-13s %s\n", tag, name, policy.Redact(message))
}

func redactDoctorReport(report DoctorReport) DoctorReport {
	report.Python = redactCheck(report.Python)
	report.Venv = redactCheck(report.Venv)
	report.CamoufoxPkg = redactCheck(report.CamoufoxPkg)
	report.Playwright = redactCheck(report.Playwright)
	report.CamoufoxBin = redactCheck(report.CamoufoxBin)
	report.Display = redactCheck(report.Display)
	return report
}

func redactCheck(check Check) Check {
	check.Warning = policy.Redact(check.Warning)
	check.Error = policy.Redact(check.Error)
	return check
}

func (c Check) message() string {
	switch {
	case c.PkgVersion != "" || c.DriverVersion != "":
		msg := c.PkgVersion
		if c.DriverVersion != "" {
			msg += "  (driver " + c.DriverVersion
			if c.Match != nil {
				if *c.Match {
					msg += " - MATCH"
				} else {
					msg += " - MISMATCH"
				}
			}
			msg += ")"
		}
		return strings.TrimSpace(msg)
	case c.Version != "":
		if c.Platform != "" {
			return c.Version + "  " + c.Platform
		}
		return c.Version
	case c.Path != "":
		return c.Path
	case c.Error != "":
		return c.Error
	default:
		return ""
	}
}

func (r DoctorReport) hasFailure() bool {
	for _, check := range []Check{r.Python, r.Venv, r.CamoufoxPkg, r.Playwright, r.CamoufoxBin, r.Display} {
		if check.Warning != "" {
			continue
		}
		if !check.OK {
			return true
		}
	}
	return false
}

type commandKind int

const (
	commandOpen commandKind = iota
	commandGet
	commandScreenshot
	commandEval
	commandFetch
)

func helpFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"fields": {Kind: flagValue},
		"full":   {Kind: flagBool},
	}
}

func installFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"dir":    {Kind: flagValue},
		"force":  {Kind: flagBool},
		"python": {Kind: flagValue},
	}
}

func serveFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"port":                 {Kind: flagValue},
		"bind":                 {Kind: flagValue},
		"auth-token":           {Kind: flagValue},
		"allowed-origins":      {Kind: flagValue},
		"allowed-hosts":        {Kind: flagValue},
		"enable-eval":          {Kind: flagBool},
		"allow-session-export": {Kind: flagBool},
		"session-dir":          {Kind: flagValue},
	}
}

func mcpFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"transport":             {Kind: flagValue},
		"toolset":               {Kind: flagValue},
		"port":                  {Kind: flagValue},
		"auth-token":            {Kind: flagValue},
		"allowed-origins":       {Kind: flagValue},
		"allowed-hosts":         {Kind: flagValue},
		"enable-eval":           {Kind: flagBool},
		"no-content-warning":    {Kind: flagBool},
		"allow-browser-fetch":   {Kind: flagBool},
		"allow-cookie-values":   {Kind: flagBool},
		"allow-cookie-mutation": {Kind: flagBool},
		"allow-snapshot-values": {Kind: flagBool},
		"allow-session-export":  {Kind: flagBool},
		"allow-session-import":  {Kind: flagBool},
		"allow-session-proxy":   {Kind: flagBool},
		"allow-file-upload":     {Kind: flagBool},
		"max-input-bytes":       {Kind: flagValue},
		"max-response-bytes":    {Kind: flagValue},
		"session-dir":           {Kind: flagValue},
		"session-ttl":           {Kind: flagValue},
		"max-sessions":          {Kind: flagValue},
	}
}

func openFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"wait":         {Kind: flagBool},
		"no-wait":      {Kind: flagBool},
		"save-session": {Kind: flagValue},
		"humanize":     {Kind: flagOptionalValue},
	}
}

func getFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"markdown":        {Kind: flagBool},
		"html":            {Kind: flagBool},
		"text":            {Kind: flagBool},
		"wait-selector":   {Kind: flagValue},
		"wait-load-state": {Kind: flagValue},
		"max-bytes":       {Kind: flagValue},
		"cookies-file":    {Kind: flagValue},
	}
}

func screenshotFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"out":             {Kind: flagValue},
		"full-page":       {Kind: flagBool},
		"width":           {Kind: flagValue},
		"height":          {Kind: flagValue},
		"wait-selector":   {Kind: flagValue},
		"wait-load-state": {Kind: flagValue},
		"quality":         {Kind: flagValue},
		"clip":            {Kind: flagValue},
		"max-bytes":       {Kind: flagValue},
	}
}

func evalFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"script":          {Kind: flagValue},
		"script-file":     {Kind: flagValue},
		"wait-selector":   {Kind: flagValue},
		"wait-load-state": {Kind: flagValue},
		"arg":             {Kind: flagValue},
		"enable-eval":     {Kind: flagBool},
	}
}

func fetchFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"method":         {Kind: flagValue},
		"data":           {Kind: flagValue},
		"data-file":      {Kind: flagValue},
		"header":         {Kind: flagValue, Repeat: true},
		"content-type":   {Kind: flagValue},
		"cookies-file":   {Kind: flagValue},
		"navigate-first": {Kind: flagValue},
		"max-bytes":      {Kind: flagValue},
		"raw":            {Kind: flagBool},
	}
}

func sessionExportFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"out":          {Kind: flagValue},
		"from-profile": {Kind: flagValue},
	}
}

func sessionImportFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"file":      {Kind: flagValue},
		"out":       {Kind: flagValue},
		"overwrite": {Kind: flagBool},
	}
}

func skillsListFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{}
}

func skillsShowFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"version": {Kind: flagValue},
	}
}

func skillsExportFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"out":   {Kind: flagValue},
		"force": {Kind: flagBool},
	}
}

func skillsInstallFlagSpecs() map[string]flagSpec {
	return map[string]flagSpec{
		"target":  {Kind: flagValue},
		"dir":     {Kind: flagValue},
		"force":   {Kind: flagBool},
		"dry-run": {Kind: flagBool},
	}
}

type skillsCommand struct {
	Subcommand string
	Name       string
	Version    string
	Out        string
	Target     string
	Dir        string
	Force      bool
	DryRun     bool
}

type skillsListResponse struct {
	Skills []skillreg.Summary `json:"skills"`
}

type skillsWriteResponse struct {
	Target  string   `json:"target,omitempty"`
	Dir     string   `json:"dir"`
	DryRun  bool     `json:"dry_run,omitempty"`
	Written []string `json:"written"`
}

func runSkills(global globalFlags, args []string, streams Streams) int {
	command, err := parseSkills(args)
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	registry := skillreg.DefaultRegistry()
	encoder := json.NewEncoder(streams.Stdout)
	encoder.SetEscapeHTML(false)
	if command.Subcommand == "list" {
		items := registry.List()
		if global.JSON {
			if err := encoder.Encode(skillsListResponse{Skills: items}); err != nil {
				writeDiagnosticLine(streams.Stderr, err)
				return ExitRuntime
			}
			return ExitOK
		}
		printSkillsList(streams.Stdout, items)
		return ExitOK
	}
	if command.Subcommand == "export" || command.Subcommand == "install" {
		installables := skillreg.DefaultInstallableSkills()
		dir := command.Out
		if command.Subcommand == "install" {
			resolved, err := resolveSkillsInstallDir(command.Target, command.Dir)
			if err != nil {
				writeDiagnosticLine(streams.Stderr, err)
				return ExitUsage
			}
			dir = resolved
		}
		written, err := writeInstallableSkills(dir, installables, command.Force, command.DryRun)
		if err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitRuntime
		}
		if global.JSON {
			if err := encoder.Encode(skillsWriteResponse{
				Target:  command.Target,
				Dir:     dir,
				DryRun:  command.DryRun,
				Written: written,
			}); err != nil {
				writeDiagnosticLine(streams.Stderr, err)
				return ExitRuntime
			}
			return ExitOK
		}
		printSkillsWritten(streams.Stdout, written, command.DryRun)
		return ExitOK
	}
	skill, err := registry.Resolve(command.Name, command.Version)
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	if global.JSON {
		if err := encoder.Encode(skill); err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitRuntime
		}
		return ExitOK
	}
	printSkill(streams.Stdout, skill)
	return ExitOK
}

func parseSkills(args []string) (skillsCommand, error) {
	if len(args) == 0 {
		return skillsCommand{}, errors.New("usage: gomoufox skills <list|show|export|install>")
	}
	switch args[0] {
	case "list":
		parsed, err := parseFlags(args[1:], skillsListFlagSpecs())
		if err != nil {
			return skillsCommand{}, err
		}
		if len(parsed.Positionals) != 0 {
			return skillsCommand{}, errors.New("usage: gomoufox skills list")
		}
		return skillsCommand{Subcommand: "list"}, nil
	case "show":
		parsed, err := parseFlags(args[1:], skillsShowFlagSpecs())
		if err != nil {
			return skillsCommand{}, err
		}
		if len(parsed.Positionals) != 1 {
			return skillsCommand{}, errors.New("usage: gomoufox skills show <name> [--version <v>]")
		}
		return skillsCommand{Subcommand: "show", Name: parsed.Positionals[0], Version: parsed.value("version")}, nil
	case "export":
		parsed, err := parseFlags(args[1:], skillsExportFlagSpecs())
		if err != nil {
			return skillsCommand{}, err
		}
		if len(parsed.Positionals) != 0 || parsed.value("out") == "" {
			return skillsCommand{}, errors.New("usage: gomoufox skills export --out <dir> [--force]")
		}
		return skillsCommand{Subcommand: "export", Out: parsed.value("out"), Force: parsed.bool("force")}, nil
	case "install":
		parsed, err := parseFlags(args[1:], skillsInstallFlagSpecs())
		if err != nil {
			return skillsCommand{}, err
		}
		if len(parsed.Positionals) != 0 {
			return skillsCommand{}, errors.New("usage: gomoufox skills install [--target codex] [--dir <dir>] [--force] [--dry-run]")
		}
		return skillsCommand{
			Subcommand: "install",
			Target:     parsed.valueDefault("target", "codex"),
			Dir:        parsed.value("dir"),
			Force:      parsed.bool("force"),
			DryRun:     parsed.bool("dry-run"),
		}, nil
	default:
		return skillsCommand{}, errors.New("usage: gomoufox skills <list|show|export|install>")
	}
}

func printSkillsList(w io.Writer, items []skillreg.Summary) {
	for _, item := range items {
		_, _ = fmt.Fprintf(w, "%s %s min=%s sha256=%s bytes=%d - %s\n", item.Name, item.Version, item.MinGomoufox, item.SHA256, item.Bytes, item.Summary)
	}
}

func printSkill(w io.Writer, skill skillreg.Skill) {
	_, _ = fmt.Fprintf(w, "name: %s\nversion: %s\nmin_gomoufox: %s\nsha256: %s\nbytes: %d\n\n%s", skill.Name, skill.Version, skill.MinGomoufox, skill.SHA256, skill.Bytes, skill.Body)
}

func printSkillsWritten(w io.Writer, written []string, dryRun bool) {
	verb := "wrote"
	if dryRun {
		verb = "would write"
	}
	for _, path := range written {
		_, _ = fmt.Fprintf(w, "%s %s\n", verb, filepath.ToSlash(path))
	}
}

func resolveSkillsInstallDir(target, dir string) (string, error) {
	if target == "" {
		target = "codex"
	}
	if target != "codex" {
		return "", fmt.Errorf("unsupported skills target: %s", target)
	}
	if dir != "" {
		return dir, nil
	}
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		return filepath.Join(codexHome, "skills"), nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve codex skills directory: %w", err)
	}
	return filepath.Join(home, ".codex", "skills"), nil
}

func writeInstallableSkills(base string, installables []skillreg.InstallableSkill, force, dryRun bool) ([]string, error) {
	if strings.TrimSpace(base) == "" {
		return nil, errors.New("skills output directory is required")
	}
	var written []string
	for _, item := range installables {
		if err := validateInstallableDirectory(item.Directory); err != nil {
			return nil, err
		}
		for _, file := range item.Files {
			rel, err := installableRelativePath(item.Directory, file.Path)
			if err != nil {
				return nil, err
			}
			written = append(written, rel)
			if dryRun {
				continue
			}
			dest := filepath.Join(base, rel)
			if err := writeInstallableFile(dest, file.Contents, force); err != nil {
				return nil, err
			}
		}
	}
	return written, nil
}

func validateInstallableDirectory(dir string) error {
	if dir == "" || filepath.Clean(dir) != dir || filepath.IsAbs(dir) || strings.ContainsAny(dir, `/\`) || strings.HasPrefix(dir, ".") {
		return fmt.Errorf("invalid installable skill directory: %s", dir)
	}
	return nil
}

func installableRelativePath(dir, file string) (string, error) {
	clean := filepath.Clean(file)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid installable skill file path: %s", file)
	}
	return filepath.Join(dir, clean), nil
}

func writeInstallableFile(path, contents string, force bool) error {
	err := safefile.WriteFile0600(path, []byte(contents), force)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists; pass --force to overwrite", path)
	}
	return err
}

func (r Runner) runEval(ctx context.Context, global globalFlags, args []string, streams Streams) int {
	parsed, err := parseEvalFlags(args)
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	if !parsed.bool("enable-eval") {
		_, _ = fmt.Fprintln(streams.Stderr, "eval is disabled; pass --enable-eval")
		return ExitRuntime
	}
	if err := validateEval(parsed); err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	if code := r.validateBrowserInputs(ctx, global, parsed, commandEval, streams); code != ExitOK {
		return code
	}
	return r.executeLocalCommand(ctx, global, "eval", parsed, streams)
}

func (r Runner) runBrowserCommand(ctx context.Context, global globalFlags, args []string, kind commandKind, streams Streams) int {
	parsed, err := parseBrowserCommand(args, kind)
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	if code := r.validateBrowserInputs(ctx, global, parsed, kind, streams); code != ExitOK {
		return code
	}
	return r.executeLocalCommand(ctx, global, commandName(kind), parsed, streams)
}

func (r Runner) validateBrowserInputs(ctx context.Context, global globalFlags, parsed parsedFlags, kind commandKind, streams Streams) int {
	if kind == commandOpen && global.HeadlessSet {
		_, _ = fmt.Fprintln(streams.Stderr, "gomoufox open forces headful mode; --headless is not allowed")
		return ExitUsage
	}
	if global.Profile != "" && (kind == commandGet || kind == commandFetch) && parsed.value("cookies-file") != "" {
		_, _ = fmt.Fprintln(streams.Stderr, "--cookies-file is mutually exclusive with --profile")
		return ExitUsage
	}
	if global.Proxy != "" {
		validator := netguard.NewValidator(policy.DefaultConfig(), r.Resolver)
		if _, err := validator.ValidateProxy(global.Proxy); err != nil {
			writeDiagnosticf(streams.Stderr, "invalid --proxy: %v\n", err)
			return ExitSessionAuth
		}
	}
	urls := urlsForValidation(parsed, kind)
	if len(urls) == 0 {
		return ExitOK
	}
	cfg := policy.DefaultConfig()
	if global.AllowSchemes != "" {
		cfg.AllowedSchemes = append(cfg.AllowedSchemes, splitCSV(global.AllowSchemes)...)
	}
	cfg.AllowPrivateIPs = global.AllowPrivateIPs
	validator := netguard.NewValidator(cfg, r.Resolver)
	if global.AllowPrivateIPs {
		_, _ = fmt.Fprintln(streams.Stderr, "WARNING: --allow-private-ips disables private IP and metadata destination blocking for this CLI command")
	}
	for _, raw := range urls {
		if raw == "" {
			continue
		}
		if _, err := validator.Validate(ctx, raw); err != nil {
			writeDiagnosticLine(streams.Stderr, err)
			return ExitURLBlocked
		}
	}
	return ExitOK
}

func urlsForValidation(parsed parsedFlags, kind commandKind) []string {
	if len(parsed.Positionals) == 0 {
		return nil
	}
	switch kind {
	case commandFetch:
		return []string{parsed.Positionals[0], parsed.value("navigate-first")}
	default:
		return []string{parsed.Positionals[0]}
	}
}

func commandName(kind commandKind) string {
	switch kind {
	case commandOpen:
		return "open"
	case commandGet:
		return "get"
	case commandScreenshot:
		return "screenshot"
	case commandEval:
		return "eval"
	case commandFetch:
		return "fetch"
	default:
		return ""
	}
}

func (r Runner) executeLocalCommand(ctx context.Context, global globalFlags, command string, parsed parsedFlags, streams Streams) int {
	local := r.Hooks.LocalCommand
	if local == nil {
		local = defaultLocalCommand
	}
	flags := parsed.flagMap()
	addForwardGlobalFlags(flags, global)
	if command == "open" {
		flags["headful"] = true
		if _, ok := flags["humanize"]; !ok {
			flags["humanize"] = "true"
		}
	}
	resp, err := local(ctx, LocalCommandRequest{
		Command: command,
		Args:    append([]string{}, parsed.Positionals...),
		Flags:   flags,
		Profile: global.Profile,
		JSON:    global.JSON,
	})
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return mapError(err)
	}
	if len(resp.Stdout) > 0 {
		_, _ = streams.Stdout.Write(resp.Stdout)
	}
	if resp.Stderr != "" {
		writeDiagnostic(streams.Stderr, resp.Stderr)
	}
	return resp.ExitCode
}

func parseBrowserCommand(args []string, kind commandKind) (parsedFlags, error) {
	switch kind {
	case commandOpen:
		return parseOpen(args)
	case commandGet:
		return parseGet(args)
	case commandScreenshot:
		return parseScreenshot(args)
	case commandFetch:
		return parseFetch(args)
	default:
		return parsedFlags{}, errors.New("unsupported command")
	}
}

func parseOpen(args []string) (parsedFlags, error) {
	parsed, err := parseFlags(args, openFlagSpecs())
	if err != nil {
		return parsed, err
	}
	if len(parsed.Positionals) != 1 {
		return parsed, errors.New("usage: gomoufox open <URL>")
	}
	if parsed.has("no-wait") {
		return parsed, errors.New("--no-wait is not supported in v1")
	}
	if parsed.has("wait") && !parsed.bool("wait") {
		return parsed, errors.New("--wait=false is not supported in v1")
	}
	return parsed, nil
}

func parseGet(args []string) (parsedFlags, error) {
	parsed, err := parseFlags(args, getFlagSpecs())
	if err != nil {
		return parsed, err
	}
	if len(parsed.Positionals) != 1 {
		return parsed, errors.New("usage: gomoufox get <URL>")
	}
	if err := validateFormatFlags(parsed); err != nil {
		return parsed, err
	}
	if err := validateLoadState(parsed.valueDefault("wait-load-state", "domcontentloaded")); err != nil {
		return parsed, err
	}
	if err := validateMaxBytes(parsed.value("max-bytes"), policy.HardMaxResponseBytes, "--max-bytes"); err != nil {
		return parsed, err
	}
	return parsed, nil
}

func parseScreenshot(args []string) (parsedFlags, error) {
	parsed, err := parseFlags(args, screenshotFlagSpecs())
	if err != nil {
		return parsed, err
	}
	if len(parsed.Positionals) != 1 {
		return parsed, errors.New("usage: gomoufox screenshot <URL>")
	}
	if err := validateLoadState(parsed.valueDefault("wait-load-state", "load")); err != nil {
		return parsed, err
	}
	for _, item := range []struct {
		name string
		min  int
		max  int
	}{
		{"width", 1, 100000},
		{"height", 1, 100000},
		{"quality", 0, 100},
	} {
		if raw := parsed.value(item.name); raw != "" {
			n, err := parseIntFlag(raw, "--"+item.name)
			if err != nil || n < item.min || n > item.max {
				return parsed, fmt.Errorf("--%s must be between %d and %d", item.name, item.min, item.max)
			}
		}
	}
	if raw := parsed.value("max-bytes"); raw != "" {
		n, err := parseIntFlag(raw, "--max-bytes")
		if err != nil {
			return parsed, err
		}
		if _, err := policy.ScreenshotCap(parsed.bool("full-page"), n); err != nil {
			return parsed, err
		}
	}
	return parsed, nil
}

func parseEval(args []string) (parsedFlags, error) {
	parsed, err := parseEvalFlags(args)
	if err != nil {
		return parsed, err
	}
	return parsed, validateEval(parsed)
}

func parseEvalFlags(args []string) (parsedFlags, error) {
	parsed, err := parseFlags(args, evalFlagSpecs())
	if err != nil {
		return parsed, err
	}
	return parsed, nil
}

func validateEval(parsed parsedFlags) error {
	if len(parsed.Positionals) != 1 {
		return errors.New("usage: gomoufox eval <URL> --script <js>")
	}
	if parsed.has("script") == parsed.has("script-file") {
		return errors.New("exactly one of --script or --script-file is required")
	}
	if len(parsed.value("script")) > 64*1024 {
		return errors.New("--script exceeds 65536 bytes")
	}
	if err := validateLoadState(parsed.valueDefault("wait-load-state", "domcontentloaded")); err != nil {
		return err
	}
	return nil
}

func parseFetch(args []string) (parsedFlags, error) {
	parsed, err := parseFlags(args, fetchFlagSpecs())
	if err != nil {
		return parsed, err
	}
	if len(parsed.Positionals) != 1 {
		return parsed, errors.New("usage: gomoufox fetch <URL>")
	}
	method := strings.ToUpper(parsed.valueDefault("method", "GET"))
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
	default:
		return parsed, errors.New("--method must be one of GET, POST, PUT, PATCH, DELETE, HEAD")
	}
	if parsed.has("data") && parsed.has("data-file") {
		return parsed, errors.New("--data and --data-file are mutually exclusive")
	}
	if len(parsed.value("data")) > policy.HardMaxInputBytes {
		return parsed, fmt.Errorf("--data exceeds %d bytes", policy.HardMaxInputBytes)
	}
	for _, header := range parsed.valueList("header") {
		if !strings.Contains(header, ":") {
			return parsed, errors.New("--header must be in K:V format")
		}
	}
	if err := validateMaxBytes(parsed.value("max-bytes"), policy.HardMaxResponseBytes, "--max-bytes"); err != nil {
		return parsed, err
	}
	return parsed, nil
}

type sessionCommand struct {
	Subcommand string
	Parsed     parsedFlags
}

func parseSession(args []string) (sessionCommand, error) {
	if len(args) == 0 {
		return sessionCommand{}, errors.New("usage: gomoufox session <export|import>")
	}
	sub := args[0]
	switch sub {
	case "export":
		parsed, err := parseFlags(args[1:], sessionExportFlagSpecs())
		if err != nil {
			return sessionCommand{}, err
		}
		if len(parsed.Positionals) != 0 || parsed.value("out") == "" {
			return sessionCommand{}, errors.New("usage: gomoufox session export --out <path>")
		}
		return sessionCommand{Subcommand: sub, Parsed: parsed}, nil
	case "import":
		parsed, err := parseFlags(args[1:], sessionImportFlagSpecs())
		if err != nil {
			return sessionCommand{}, err
		}
		if len(parsed.Positionals) != 0 || parsed.value("file") == "" || parsed.value("out") == "" {
			return sessionCommand{}, errors.New("usage: gomoufox session import --file <path> --out <path>")
		}
		return sessionCommand{Subcommand: sub, Parsed: parsed}, nil
	default:
		return sessionCommand{}, errors.New("usage: gomoufox session <export|import>")
	}
}

func validateFormatFlags(parsed parsedFlags) error {
	count := 0
	for _, name := range []string{"markdown", "html", "text"} {
		if parsed.has(name) && parsed.bool(name) {
			count++
		}
	}
	if count > 1 {
		return errors.New("--markdown, --html, and --text are mutually exclusive")
	}
	return nil
}

func validateLoadState(state string) error {
	switch state {
	case "domcontentloaded", "load", "networkidle":
		return nil
	default:
		return errors.New("--wait-load-state must be domcontentloaded, load, or networkidle")
	}
}

func validateMaxBytes(raw string, hard int, name string) error {
	if raw == "" {
		return nil
	}
	n, err := parseIntFlag(raw, name)
	if err != nil {
		return err
	}
	if n <= 0 || n > hard {
		return fmt.Errorf("%s must be between 1 and %d", name, hard)
	}
	return nil
}

func (r Runner) forward(ctx context.Context, global globalFlags, command string, args []string, streams Streams) int {
	req, err := buildForwardRequest(global, command, args)
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return ExitUsage
	}
	forward := r.Hooks.Forward
	if forward == nil {
		forward = defaultForward
	}
	resp, err := forward(ctx, req)
	if err != nil {
		writeDiagnosticLine(streams.Stderr, err)
		return mapError(err)
	}
	if resp.Stdout != "" {
		_, _ = fmt.Fprint(streams.Stdout, resp.Stdout)
	}
	if resp.Stderr != "" {
		writeDiagnostic(streams.Stderr, resp.Stderr)
	}
	return resp.ExitCode
}

func buildForwardRequest(global globalFlags, command string, args []string) (ForwardRequest, error) {
	if global.AllowPrivateIPs || global.AllowSchemes != "" {
		return ForwardRequest{}, errors.New("daemon forwarding does not allow URL guardrail overrides")
	}
	var parsed parsedFlags
	var endpoint string
	var verb string
	var err error
	switch command {
	case "get":
		parsed, err = parseGet(args)
		endpoint = "/v1/commands/get"
		verb = "get"
	case "screenshot":
		parsed, err = parseScreenshot(args)
		endpoint = "/v1/commands/screenshot"
		verb = "screenshot"
	case "fetch":
		parsed, err = parseFetch(args)
		endpoint = "/v1/commands/fetch"
		verb = "fetch"
	case "eval":
		parsed, err = parseEval(args)
		if err == nil && !parsed.bool("enable-eval") {
			return ForwardRequest{}, errors.New("eval is disabled; pass --enable-eval")
		}
		endpoint = "/v1/commands/eval"
		verb = "eval"
	case "session":
		session, err := parseSession(args)
		if err != nil {
			return ForwardRequest{}, err
		}
		parsed = session.Parsed
		endpoint = "/v1/session/" + session.Subcommand
		verb = "session " + session.Subcommand
	default:
		return ForwardRequest{}, fmt.Errorf("--server is not supported for %s", command)
	}
	if err != nil {
		return ForwardRequest{}, err
	}
	flags := parsed.flagMap()
	addForwardGlobalFlags(flags, global)
	return ForwardRequest{
		ServerURL: global.Server,
		Token:     global.ServerToken,
		Endpoint:  endpoint,
		Verb:      verb,
		Envelope: daemon.Envelope{
			Args:    append([]string{}, parsed.Positionals...),
			Flags:   flags,
			Profile: global.Profile,
			JSON:    global.JSON,
		},
	}, nil
}

func addForwardGlobalFlags(flags map[string]any, global globalFlags) {
	if global.TimeoutRaw != "" {
		flags["timeout"] = global.TimeoutRaw
	}
	if global.Proxy != "" {
		flags["proxy"] = global.Proxy
	}
	if global.Locale != "" {
		flags["locale"] = global.Locale
	}
	if global.OS != "" {
		flags["os"] = global.OS
	}
	if global.Headful {
		flags["headful"] = true
	}
	if global.HeadlessSet {
		flags["headless"] = global.Headless
	}
}

func defaultForward(ctx context.Context, req ForwardRequest) (ForwardResponse, error) {
	base, err := url.Parse(req.ServerURL)
	if err != nil {
		return ForwardResponse{}, err
	}
	base.Path = strings.TrimRight(base.Path, "/") + req.Endpoint
	body, err := json.Marshal(req.Envelope)
	if err != nil {
		return ForwardResponse{}, err
	}
	httpReq, err := newForwardRequest(ctx, http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return ForwardResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return ForwardResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ForwardResponse{}, err
	}
	var result daemon.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return ForwardResponse{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ForwardResponse{ExitCode: ExitSessionAuth, Stderr: "unauthorized\n"}, nil
	}
	if resp.StatusCode >= 400 {
		if result.Stderr == "" {
			var payload struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(data, &payload); err == nil && payload.Error != "" {
				result.Stderr = payload.Error + "\n"
			}
		}
		if result.ExitCode == 0 {
			result.ExitCode = ExitRuntime
		}
	}
	return ForwardResponse{ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: policy.Redact(result.Stderr)}, nil
}

var newForwardRequest = http.NewRequestWithContext

func canForward(command string, args []string) bool {
	switch command {
	case "get", "screenshot", "fetch", "eval":
		return true
	case "session":
		return len(args) > 0 && (args[0] == "export" || args[0] == "import")
	default:
		return false
	}
}

type globalFlags struct {
	Profile         string
	Headless        bool
	HeadlessSet     bool
	Headful         bool
	Proxy           string
	TimeoutRaw      string
	Timeout         time.Duration
	Locale          string
	OS              string
	Server          string
	ServerSet       bool
	ServerToken     string
	ServerTokenSet  bool
	AllowSchemes    string
	AllowPrivateIPs bool
	JSON            bool
	Verbose         bool
	Version         bool
	Help            bool
}

func parseGlobal(args []string) (globalFlags, string, []string, error) {
	var global globalFlags
	global.Headless = true
	global.Timeout = 30 * time.Second
	commandIndex := findCommandIndex(args)
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if i == commandIndex {
			continue
		}
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			rest = append(rest, arg)
			continue
		}
		name, value, hasValue := splitLongFlag(arg)
		if commandIndex >= 0 && i > commandIndex && args[commandIndex] == "skills" && name == "version" {
			rest = append(rest, arg)
			continue
		}
		if isGlobalBool(name) {
			parsed, err := boolFlagValue(name, value, hasValue)
			if err != nil {
				return global, "", nil, err
			}
			applyGlobalBool(&global, name, parsed)
			continue
		}
		if isGlobalValue(name) {
			if !hasValue {
				i++
				if i >= len(args) || i == commandIndex {
					return global, "", nil, fmt.Errorf("--%s requires a value", name)
				}
				value = args[i]
			}
			if err := applyGlobalValue(&global, name, value); err != nil {
				return global, "", nil, err
			}
			continue
		}
		if commandIndex < 0 || i < commandIndex {
			return global, "", nil, fmt.Errorf("unknown global flag: --%s", name)
		}
		rest = append(rest, arg)
	}
	if global.Server == "" {
		global.Server = os.Getenv("GOMOUFOX_DAEMON")
	}
	if global.ServerToken == "" {
		global.ServerToken = os.Getenv("GOMOUFOX_DAEMON_TOKEN")
	}
	if commandIndex < 0 {
		return global, "", rest, nil
	}
	return global, args[commandIndex], rest, nil
}

func findCommandIndex(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--") {
			name, _, hasValue := splitLongFlag(arg)
			if isGlobalValue(name) && !hasValue {
				i++
			}
			continue
		}
		return i
	}
	return -1
}

func isGlobalBool(name string) bool {
	switch name {
	case "headless", "headful", "allow-private-ips", "json", "verbose", "version", "help":
		return true
	default:
		return false
	}
}

func isGlobalValue(name string) bool {
	switch name {
	case "profile", "proxy", "timeout", "locale", "os", "server", "server-token", "allow-schemes":
		return true
	default:
		return false
	}
}

func applyGlobalBool(global *globalFlags, name string, value bool) {
	switch name {
	case "headless":
		global.Headless = value
		global.HeadlessSet = true
	case "headful":
		global.Headful = value
	case "allow-private-ips":
		global.AllowPrivateIPs = value
	case "json":
		global.JSON = value
	case "verbose":
		global.Verbose = value
	case "version":
		global.Version = value
	case "help":
		global.Help = value
	}
}

func applyGlobalValue(global *globalFlags, name, value string) error {
	switch name {
	case "profile":
		global.Profile = value
	case "proxy":
		global.Proxy = value
	case "timeout":
		timeout, err := ParseDuration(value, 30*time.Second)
		if err != nil {
			return err
		}
		global.TimeoutRaw = value
		global.Timeout = timeout
	case "locale":
		global.Locale = value
	case "os":
		if value != "" && value != "windows" && value != "macos" && value != "linux" {
			return errors.New("--os must be windows, macos, or linux")
		}
		global.OS = value
	case "server":
		global.Server = value
		global.ServerSet = true
	case "server-token":
		global.ServerToken = value
		global.ServerTokenSet = true
	case "allow-schemes":
		global.AllowSchemes = value
	}
	return nil
}

func validateDaemonForward(global globalFlags, stderr io.Writer) int {
	parsed, err := url.Parse(global.Server)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		_, _ = fmt.Fprintln(stderr, "invalid --server URL")
		return ExitSessionAuth
	}
	if parsed.User != nil {
		_, _ = fmt.Fprintln(stderr, "--server URL must not contain userinfo")
		return ExitSessionAuth
	}
	if global.ServerToken == "" {
		_, _ = fmt.Fprintln(stderr, "gomoufox --server requires --server-token or GOMOUFOX_DAEMON_TOKEN")
		return ExitSessionAuth
	}
	return ExitOK
}

func mapError(err error) int {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ExitCommandTimeout
	case errors.Is(err, gomoufox.ErrNotInstalled), errors.Is(err, gomoufox.ErrSidecarStart), errors.Is(err, gomoufox.ErrConnect), errors.Is(err, gomoufox.ErrSidecarDied):
		return ExitUnavailable
	case errors.Is(err, gomoufox.ErrVersionMismatch):
		return ExitVersion
	case errors.Is(err, gomoufox.ErrNavigationTimeout), errors.Is(err, gomoufox.ErrTimeout):
		return ExitTimeout
	case errors.Is(err, gomoufox.ErrElementNotFound):
		return ExitElement
	case errors.Is(err, gomoufox.ErrURLBlocked), errors.Is(err, netguard.ErrBlocked):
		return ExitURLBlocked
	case errors.Is(err, errDaemonUsage):
		return ExitUsage
	case errors.Is(err, gomoufox.ErrSessionClosed):
		return ExitSessionAuth
	default:
		return ExitRuntime
	}
}

type flagKind int

const (
	flagBool flagKind = iota
	flagValue
	flagOptionalValue
)

type flagSpec struct {
	Kind   flagKind
	Repeat bool
}

type parsedFlags struct {
	Positionals []string
	bools       map[string]bool
	values      map[string][]string
}

func parseFlags(args []string, specs map[string]flagSpec) (parsedFlags, error) {
	parsed := parsedFlags{
		bools:  make(map[string]bool),
		values: make(map[string][]string),
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			parsed.Positionals = append(parsed.Positionals, arg)
			continue
		}
		name, value, hasValue := splitLongFlag(arg)
		spec, ok := specs[name]
		if !ok {
			return parsed, fmt.Errorf("unknown flag: --%s", name)
		}
		switch spec.Kind {
		case flagBool:
			parsedValue, err := boolFlagValue(name, value, hasValue)
			if err != nil {
				return parsed, err
			}
			parsed.bools[name] = parsedValue
		case flagValue:
			if !hasValue {
				i++
				if i >= len(args) {
					return parsed, fmt.Errorf("--%s requires a value", name)
				}
				value = args[i]
			}
			if !spec.Repeat && len(parsed.values[name]) > 0 {
				return parsed, fmt.Errorf("--%s may only be provided once", name)
			}
			parsed.values[name] = append(parsed.values[name], value)
		case flagOptionalValue:
			if !hasValue {
				value = "true"
			}
			parsed.values[name] = append(parsed.values[name], value)
		}
	}
	return parsed, nil
}

func splitLongFlag(arg string) (string, string, bool) {
	trimmed := strings.TrimPrefix(arg, "--")
	if name, value, ok := strings.Cut(trimmed, "="); ok {
		return name, value, true
	}
	return trimmed, "", false
}

func boolFlagValue(name, value string, hasValue bool) (bool, error) {
	if !hasValue {
		return true, nil
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("--%s expects a boolean value", name)
	}
}

func (p parsedFlags) bool(name string) bool {
	return p.bools[name]
}

func (p parsedFlags) has(name string) bool {
	if _, ok := p.bools[name]; ok {
		return true
	}
	return len(p.values[name]) > 0
}

func (p parsedFlags) value(name string) string {
	values := p.values[name]
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func (p parsedFlags) valueDefault(name, fallback string) string {
	if value := p.value(name); value != "" {
		return value
	}
	return fallback
}

func (p parsedFlags) valueList(name string) []string {
	return append([]string{}, p.values[name]...)
}

func (p parsedFlags) flagMap() map[string]any {
	out := make(map[string]any)
	for name, value := range p.bools {
		out[flagEnvelopeName(name)] = value
	}
	for name, values := range p.values {
		key := flagEnvelopeName(name)
		if len(values) == 1 {
			out[key] = values[0]
		} else if len(values) > 1 {
			out[key] = append([]string{}, values...)
		}
	}
	return out
}

func flagEnvelopeName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

func parsePort(raw string) (int, error) {
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return 0, errors.New("--port must be between 1 and 65535")
	}
	return port, nil
}

func parseIntFlag(raw, name string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return n, nil
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func isLoopbackBind(bind string) bool {
	host := bind
	if strings.Contains(bind, ":") {
		if parsedHost, _, err := net.SplitHostPort(bind); err == nil {
			host = parsedHost
		}
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ParseDuration(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, errors.New("duration must be greater than zero")
	}
	return duration, nil
}
