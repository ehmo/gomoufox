package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"

	gomoufox "github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/content"
	"github.com/ehmo/gomoufox/internal/daemon"
	mcpserver "github.com/ehmo/gomoufox/internal/mcp"
	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/sidecar"
	skillreg "github.com/ehmo/gomoufox/internal/skills"
)

type cliTestResolver map[string][]string

func (r cliTestResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	raw, ok := r[host]
	if !ok {
		return nil, errors.New("missing test DNS record")
	}
	addrs := make([]net.IPAddr, 0, len(raw))
	for _, item := range raw {
		addrs = append(addrs, net.IPAddr{IP: net.ParseIP(item)})
	}
	return addrs, nil
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

const diagnosticSecretFixture = `proxy=http://user:pass@example.com Authorization: Bearer abc.def Cookie: sid=secret Set-Cookie: auth=secret wss://127.0.0.1:9222/rawtoken token=secret {"cookies":[{"name":"sid","value":"cookie-secret"}],"origins":[{"origin":"https://example.com","localStorage":[{"name":"token","value":"storage-secret"}]}]}`

func assertNoDiagnosticSecrets(t *testing.T, text string) {
	t.Helper()
	for i, secret := range []string{
		"user:pass",
		"abc.def",
		"sid=secret",
		"auth=secret",
		"/rawtoken",
		"token=secret",
		"cookie-secret",
		"storage-secret",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostic secret fixture %d survived", i)
		}
	}
}

func assertDaemonPathRejected(t *testing.T, result daemon.Result, roots ...string) {
	t.Helper()
	if result.ExitCode != ExitUsage || strings.TrimSpace(result.Stderr) != "path_rejected" {
		t.Fatalf("daemon path rejection result = %#v", result)
	}
	assertNoDaemonHostPath(t, result.Stderr, roots...)
}

func assertNoDaemonHostPath(t *testing.T, text string, roots ...string) {
	t.Helper()
	for _, root := range roots {
		for _, candidate := range daemonHostPathCandidates(t, root) {
			if strings.Contains(text, candidate) {
				t.Fatalf("daemon output leaked host path %q in %q", candidate, text)
			}
		}
	}
}

func assertDaemonJSONPath(t *testing.T, result daemon.Result, want string, roots ...string) {
	t.Helper()
	if result.ExitCode != ExitOK {
		t.Fatalf("daemon json result = %#v", result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		t.Fatalf("daemon json output = %q, %v", result.Stdout, err)
	}
	if payload["path"] != want {
		t.Fatalf("daemon json path = %#v want %q in %q", payload["path"], want, result.Stdout)
	}
	assertNoDaemonHostPath(t, result.Stdout, roots...)
}

func daemonHostPathCandidates(t *testing.T, root string) []string {
	t.Helper()
	if root == "" {
		return nil
	}
	candidates := []string{root}
	if abs, err := filepath.Abs(root); err == nil {
		candidates = append(candidates, abs)
	}
	if jail, err := policy.NewJail(root); err == nil {
		candidates = append(candidates, jail.Root)
	}
	return compactUniqueNonEmpty(candidates)
}

func compactUniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || value == "." {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func TestEvalDisabledExactError(t *testing.T) {
	var stderr bytes.Buffer
	code := Runner{}.Run(context.Background(), []string{"eval", "https://example.com", "--script", "1+1"}, Streams{Stderr: &stderr})
	if code != ExitRuntime {
		t.Fatalf("code = %d", code)
	}
	if got := stderr.String(); got != "eval is disabled; pass --enable-eval\n" {
		t.Fatalf("stderr = %q", got)
	}
	stderr.Reset()
	code = Runner{}.Run(context.Background(), []string{"eval", "https://example.com"}, Streams{Stderr: &stderr})
	if code != ExitRuntime {
		t.Fatalf("code without script = %d", code)
	}
	if got := stderr.String(); got != "eval is disabled; pass --enable-eval\n" {
		t.Fatalf("stderr without script = %q", got)
	}
}

func TestServeRequiresAuthToken(t *testing.T) {
	var stderr bytes.Buffer
	code := Runner{}.Run(context.Background(), []string{"serve"}, Streams{Stderr: &stderr})
	if code != ExitSessionAuth {
		t.Fatalf("code = %d", code)
	}
	if got := stderr.String(); got != "gomoufox serve requires --auth-token\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRunnerRedactsDiagnosticErrorsAndStderr(t *testing.T) {
	var stderr bytes.Buffer
	runner := Runner{Hooks: Hooks{LocalCommand: func(context.Context, LocalCommandRequest) (LocalCommandResponse, error) {
		return LocalCommandResponse{}, errors.New(diagnosticSecretFixture)
	}}}
	code := runner.Run(context.Background(), []string{"get", "http://93.184.216.34"}, Streams{Stderr: &stderr})
	if code != ExitRuntime {
		t.Fatalf("local error code=%d stderr=%q", code, stderr.String())
	}
	assertNoDiagnosticSecrets(t, stderr.String())

	stderr.Reset()
	runner.Hooks.LocalCommand = func(context.Context, LocalCommandRequest) (LocalCommandResponse, error) {
		return LocalCommandResponse{ExitCode: ExitRuntime, Stderr: diagnosticSecretFixture + "\n"}, nil
	}
	code = runner.Run(context.Background(), []string{"get", "http://93.184.216.34"}, Streams{Stderr: &stderr})
	if code != ExitRuntime {
		t.Fatalf("local stderr code=%d stderr=%q", code, stderr.String())
	}
	assertNoDiagnosticSecrets(t, stderr.String())

	stderr.Reset()
	runner.Hooks.LocalCommand = nil
	runner.Hooks.MCP = func(context.Context, MCPRequest) error { return errors.New(diagnosticSecretFixture) }
	code = runner.Run(context.Background(), []string{"mcp"}, Streams{Stderr: &stderr})
	if code != ExitRuntime {
		t.Fatalf("mcp error code=%d stderr=%q", code, stderr.String())
	}
	assertNoDiagnosticSecrets(t, stderr.String())

	stderr.Reset()
	runner.Hooks.MCP = nil
	runner.Hooks.Install = func(context.Context, InstallRequest) error { return errors.New(diagnosticSecretFixture) }
	code = runner.Run(context.Background(), []string{"install"}, Streams{Stderr: &stderr})
	if code != ExitRuntime {
		t.Fatalf("install error code=%d stderr=%q", code, stderr.String())
	}
	assertNoDiagnosticSecrets(t, stderr.String())

	stderr.Reset()
	runner.Hooks.Install = nil
	runner.Hooks.Serve = func(context.Context, ServeRequest) error { return errors.New(diagnosticSecretFixture) }
	code = runner.Run(context.Background(), []string{"serve", "--auth-token", "tok"}, Streams{Stderr: &stderr})
	if code != ExitRuntime {
		t.Fatalf("serve error code=%d stderr=%q", code, stderr.String())
	}
	assertNoDiagnosticSecrets(t, stderr.String())
}

func TestRunnerRedactsForwardAndDoctorDiagnostics(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := Runner{Hooks: Hooks{Forward: func(context.Context, ForwardRequest) (ForwardResponse, error) {
		return ForwardResponse{}, errors.New(diagnosticSecretFixture)
	}}}
	code := runner.Run(context.Background(), []string{"--server", "http://127.0.0.1:3741", "--server-token", "tok", "get", "http://93.184.216.34"}, Streams{Stderr: &stderr})
	if code != ExitRuntime {
		t.Fatalf("forward error code=%d stderr=%q", code, stderr.String())
	}
	assertNoDiagnosticSecrets(t, stderr.String())

	stderr.Reset()
	runner.Hooks.Forward = func(context.Context, ForwardRequest) (ForwardResponse, error) {
		return ForwardResponse{ExitCode: ExitRuntime, Stderr: diagnosticSecretFixture + "\n"}, nil
	}
	code = runner.Run(context.Background(), []string{"--server", "http://127.0.0.1:3741", "--server-token", "tok", "get", "http://93.184.216.34"}, Streams{Stderr: &stderr})
	if code != ExitRuntime {
		t.Fatalf("forward stderr code=%d stderr=%q", code, stderr.String())
	}
	assertNoDiagnosticSecrets(t, stderr.String())

	stderr.Reset()
	runner.Hooks.Forward = nil
	runner.Hooks.Doctor = func(context.Context, DoctorRequest) (DoctorReport, error) {
		return DoctorReport{
			Python:      Check{OK: false, Error: diagnosticSecretFixture},
			Venv:        Check{OK: true, Warning: diagnosticSecretFixture},
			CamoufoxPkg: Check{OK: true},
			Playwright:  Check{OK: true},
			CamoufoxBin: Check{OK: true},
			Display:     Check{OK: true},
		}, errors.New(diagnosticSecretFixture)
	}
	code = runner.Run(context.Background(), []string{"--json", "doctor"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitUnavailable {
		t.Fatalf("doctor code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertNoDiagnosticSecrets(t, stdout.String())
	assertNoDiagnosticSecrets(t, stderr.String())
}

func TestRunnerRedactsFormattedProxyDiagnostics(t *testing.T) {
	var stderr bytes.Buffer
	code := Runner{}.Run(context.Background(), []string{"--proxy", "http://user:pass@", "get", "http://93.184.216.34"}, Streams{Stderr: &stderr})
	if code != ExitSessionAuth {
		t.Fatalf("proxy code=%d stderr=%q", code, stderr.String())
	}
	assertNoDiagnosticSecrets(t, stderr.String())
}

func TestServeHookReceivesParsedRequest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	runner := Runner{Hooks: Hooks{Serve: func(ctx context.Context, req ServeRequest) error {
		called = true
		if req.Bind != "127.0.0.2" || req.Port != 3888 || req.AuthToken != "tok" || !req.EnableEval || !req.AllowSessionExport {
			t.Fatalf("request = %#v", req)
		}
		if len(req.AllowedOrigins) != 1 || req.AllowedOrigins[0] != "https://example.com" {
			t.Fatalf("origins = %#v", req.AllowedOrigins)
		}
		if len(req.AllowedHosts) != 1 || req.AllowedHosts[0] != ".example.com" {
			t.Fatalf("hosts = %#v", req.AllowedHosts)
		}
		return nil
	}}}
	code := runner.Run(context.Background(), []string{"serve", "--auth-token", "tok", "--bind=127.0.0.2", "--port", "3888", "--enable-eval", "--allow-session-export", "--allowed-origins", "https://example.com", "--allowed-hosts=.example.com"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK || !called {
		t.Fatalf("code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
}

func TestServeRejectsBadPort(t *testing.T) {
	var stderr bytes.Buffer
	code := Runner{}.Run(context.Background(), []string{"serve", "--auth-token", "tok", "--port", "999999"}, Streams{Stderr: &stderr})
	if code != ExitUsage {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestMCPHTTPRequiresTokenAndRejectsGuardrailOverrides(t *testing.T) {
	var stderr bytes.Buffer
	code := Runner{}.Run(context.Background(), []string{"mcp", "--transport", "http"}, Streams{Stderr: &stderr})
	if code != ExitSessionAuth {
		t.Fatalf("code = %d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	code = Runner{}.Run(context.Background(), []string{"mcp", "--allow-private-ips"}, Streams{Stderr: &stderr})
	if code != ExitUsage {
		t.Fatalf("code = %d stderr=%q", code, stderr.String())
	}
}

func TestMainCLIAndMCPDoNotExposeUnsafeDirectNetwork(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"help"},
		{"mcp", "--help"},
		{"help", "mcp"},
	} {
		var stdout, stderr bytes.Buffer
		if code := (Runner{}).Run(context.Background(), args, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
			t.Fatalf("%v code=%d stderr=%q", args, code, stderr.String())
		}
		if got := stdout.String(); strings.Contains(got, "direct-network") || strings.Contains(got, "unsafe-direct-network") {
			t.Fatalf("%v exposes unsafe direct network flag: %q", args, got)
		}
	}
	for _, args := range [][]string{
		{"get", "http://93.184.216.34", "--unsafe-direct-network"},
		{"serve", "--auth-token", "tok", "--unsafe-direct-network"},
		{"mcp", "--unsafe-direct-network"},
	} {
		var stderr bytes.Buffer
		if code := (Runner{}).Run(context.Background(), args, Streams{Stderr: &stderr}); code != ExitUsage {
			t.Fatalf("%v code=%d stderr=%q", args, code, stderr.String())
		}
	}
}

func TestDaemonForwardRequiresTokenAndRejectsUserinfo(t *testing.T) {
	var stderr bytes.Buffer
	code := Runner{}.Run(context.Background(), []string{"--server", "http://127.0.0.1:3741", "get", "https://example.com"}, Streams{Stderr: &stderr})
	if code != ExitSessionAuth {
		t.Fatalf("code = %d", code)
	}
	if got := stderr.String(); got != "gomoufox --server requires --server-token or GOMOUFOX_DAEMON_TOKEN\n" {
		t.Fatalf("stderr = %q", got)
	}
	stderr.Reset()
	code = Runner{}.Run(context.Background(), []string{"--server", "http://token@127.0.0.1:3741", "--server-token", "x", "get", "https://example.com"}, Streams{Stderr: &stderr})
	if code != ExitSessionAuth {
		t.Fatalf("code = %d", code)
	}
}

func TestInstallHookAndExitMapping(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	runner := Runner{Hooks: Hooks{Install: func(ctx context.Context, req InstallRequest) error {
		called = true
		if req.Dir != "/tmp/g" || req.Python != "python3.12" || !req.Force {
			t.Fatalf("request = %#v", req)
		}
		return nil
	}}}
	code := runner.Run(context.Background(), []string{"install", "--dir", "/tmp/g", "--python=python3.12", "--force"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK || !called {
		t.Fatalf("code=%d called=%v stderr=%q", code, called, stderr.String())
	}
	if stdout.String() != ">> gomoufox install complete\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	restore := replaceDefaultInstallEnsureInstalled(t, func(ctx context.Context, req InstallRequest) error {
		called = true
		if req.Dir != "/tmp/default" || req.Python != "python3.13" || !req.Force {
			t.Fatalf("default request = %#v", req)
		}
		return nil
	})
	called = false
	code = (Runner{}).Run(context.Background(), []string{"install", "--dir", "/tmp/default", "--python", "python3.13", "--force"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK || !called || stdout.String() != ">> gomoufox install complete\n" {
		t.Fatalf("default code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
	restore()

	runner.Hooks.Install = func(context.Context, InstallRequest) error { return gomoufox.ErrURLBlocked }
	code = runner.Run(context.Background(), []string{"install"}, Streams{Stderr: &stderr})
	if code != ExitURLBlocked {
		t.Fatalf("mapped code = %d", code)
	}
}

func TestDoctorJSONAndFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := Runner{Hooks: Hooks{Doctor: func(context.Context, DoctorRequest) (DoctorReport, error) {
		return DoctorReport{Python: Check{OK: true, Version: "3.12"}}, errors.New("failed")
	}}}
	code := runner.Run(context.Background(), []string{"--json", "doctor"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitUnavailable {
		t.Fatalf("code = %d", code)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"version":"3.12"`)) {
		t.Fatalf("json stdout = %q", stdout.String())
	}
}

func TestDefaultDoctorUsesInstallHook(t *testing.T) {
	called := false
	restore := replaceDefaultDoctorEnsureInstalled(t, func(context.Context) error {
		called = true
		return nil
	})
	report, err := defaultDoctor(context.Background(), DoctorRequest{JSON: true})
	if err != nil || !called {
		t.Fatalf("defaultDoctor success err=%v called=%v", err, called)
	}
	if !report.CamoufoxPkg.OK || !report.Playwright.OK || report.Playwright.Match == nil || !*report.Playwright.Match || !report.CamoufoxBin.OK {
		t.Fatalf("success report = %#v", report)
	}
	if report.CamoufoxPkg.Version != sidecar.RequiredCamoufox ||
		report.Playwright.PkgVersion != sidecar.RequiredPlaywright ||
		report.Playwright.DriverVersion != sidecar.RequiredPlaywright {
		t.Fatalf("doctor pins = camoufox %q playwright %q driver %q", report.CamoufoxPkg.Version, report.Playwright.PkgVersion, report.Playwright.DriverVersion)
	}
	restore()

	restore = replaceDefaultDoctorEnsureInstalled(t, func(context.Context) error {
		return gomoufox.ErrNotInstalled
	})
	report, err = defaultDoctor(context.Background(), DoctorRequest{})
	if !errors.Is(err, gomoufox.ErrNotInstalled) || report.CamoufoxPkg.OK || !strings.Contains(report.CamoufoxPkg.Error, "not installed") {
		t.Fatalf("failure report=%#v err=%v", report, err)
	}
	restore()
}

func TestDefaultDoctorWarnsWhenLinuxDisplayIsMissing(t *testing.T) {
	restoreInstall := replaceDefaultDoctorEnsureInstalled(t, func(context.Context) error { return nil })
	defer restoreInstall()
	restoreDisplay := replaceDoctorDisplayEnvironment(t, "linux", map[string]string{}, nil)
	defer restoreDisplay()

	report, err := defaultDoctor(context.Background(), DoctorRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Display.Warning == "" || !strings.Contains(report.Display.Warning, "DISPLAY not set") {
		t.Fatalf("doctor display check = %#v", report.Display)
	}
	if report.hasFailure() {
		t.Fatalf("display warning should not make doctor fail: %#v", report)
	}
}

func TestDoctorDisplayCheckVariants(t *testing.T) {
	cases := []struct {
		name        string
		goos        string
		env         map[string]string
		lookPathErr error
		wantOK      bool
		wantWarning string
		wantPath    string
	}{
		{name: "non linux", goos: "darwin", env: map[string]string{}, wantOK: true},
		{name: "linux display set", goos: "linux", env: map[string]string{"DISPLAY": ":99"}, wantOK: true, wantPath: ":99"},
		{name: "linux auto display with xvfb", goos: "linux", env: map[string]string{"GOMOUFOX_AUTO_DISPLAY": "1"}, wantOK: true, wantWarning: "will try to start Xvfb"},
		{name: "linux auto display missing xvfb", goos: "linux", env: map[string]string{"GOMOUFOX_AUTO_DISPLAY": "1"}, lookPathErr: errors.New("missing"), wantOK: false, wantWarning: "Xvfb was not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := replaceDoctorDisplayEnvironment(t, tc.goos, tc.env, tc.lookPathErr)
			defer restore()
			got := doctorDisplayCheck()
			if got.OK != tc.wantOK || !strings.Contains(got.Warning, tc.wantWarning) || got.Path != tc.wantPath {
				t.Fatalf("display check = %#v", got)
			}
		})
	}
}

func TestVersionAndUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := (Runner{}).Run(context.Background(), []string{"--version"}, Streams{Stdout: &stdout}); code != ExitOK {
		t.Fatalf("version code = %d", code)
	}
	if stdout.String() != "gomoufox dev\n" {
		t.Fatalf("version stdout = %q", stdout.String())
	}
	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--help"}, Streams{Stdout: &stdout}); code != ExitOK {
		t.Fatalf("help code = %d", code)
	}
	if !strings.Contains(stdout.String(), "usage: gomoufox") || !strings.Contains(stdout.String(), "discovery:") {
		t.Fatalf("help stdout = %q", stdout.String())
	}
	if code := (Runner{}).Run(context.Background(), []string{"wat"}, Streams{Stderr: &stderr}); code != ExitUsage {
		t.Fatalf("unknown code = %d", code)
	}
}

func TestAgentOptimizedHelpDiscovery(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := (Runner{}).Run(context.Background(), []string{"mcp", "--help"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("mcp help code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "usage: gomoufox mcp") || !strings.Contains(got, "tools: browser_navigate") || !strings.Contains(got, "--toolset") || !strings.Contains(got, "--max-response-bytes") || !strings.Contains(got, "--allowed-origins") {
		t.Fatalf("mcp help = %q", got)
	}

	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"help", "fetch"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("fetch help code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "gomoufox fetch <url>") || !strings.Contains(got, "--navigate-first") {
		t.Fatalf("fetch help = %q", got)
	}
	if got := stdout.String(); strings.Contains(got, "--body") || !strings.Contains(got, "--data") {
		t.Fatalf("fetch help flags drifted = %q", got)
	}

	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"help", "screenshot"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("screenshot help code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); strings.Contains(got, "--selector") || !strings.Contains(got, "--wait-selector") || !strings.Contains(got, "--clip") {
		t.Fatalf("screenshot help flags drifted = %q", got)
	}

	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"help", "help"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("help help code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "gomoufox help [command]") || !strings.Contains(got, "--json") {
		t.Fatalf("help help = %q", got)
	}

	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"help", "skills"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills help code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "gomoufox skills <list|show|export|install>") || !strings.Contains(got, "--version") || !strings.Contains(got, "--dry-run") {
		t.Fatalf("skills help = %q", got)
	}

	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--json", "help", "mcp"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("json help code=%d stderr=%q", code, stderr.String())
	}
	var catalog helpCatalog
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("help json = %q err=%v", stdout.String(), err)
	}
	if len(catalog.Commands) != 1 || catalog.Commands[0].Name != "mcp" || len(catalog.MCPTools) == 0 || catalog.MCPTools[0] != "browser_navigate" {
		t.Fatalf("catalog = %#v", catalog)
	}
	if len(catalog.Commands[0].Flags) == 0 || catalog.Commands[0].Summary == "" {
		t.Fatalf("command-specific catalog omitted details = %#v", catalog.Commands[0])
	}

	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--json", "help"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("top json help code=%d stderr=%q", code, stderr.String())
	}
	catalog = helpCatalog{}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("top help json = %q err=%v", stdout.String(), err)
	}
	if len(catalog.Commands) < 11 || catalog.Commands[0].Name != "doctor" || len(catalog.Discovery) == 0 || catalog.MCPTools != nil {
		t.Fatalf("top catalog = %#v", catalog)
	}
	if catalog.Commands[0].Summary != "" || len(catalog.Commands[0].Flags) != 0 || len(catalog.Commands[0].Examples) != 0 {
		t.Fatalf("top catalog should be a compact index = %#v", catalog.Commands[0])
	}
	if bytes.Contains(stdout.Bytes(), []byte("check local Python")) {
		t.Fatalf("top catalog leaked command summaries = %q", stdout.String())
	}

	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--json", "help", "--fields", "commands"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("fielded help code=%d stderr=%q", code, stderr.String())
	}
	catalog = helpCatalog{}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("fielded help json = %q err=%v", stdout.String(), err)
	}
	if len(catalog.Commands) < 11 || catalog.Usage != "" || len(catalog.Global) != 0 || len(catalog.Discovery) != 0 {
		t.Fatalf("fielded catalog = %#v", catalog)
	}
	if bytes.Contains(stdout.Bytes(), []byte("global")) || bytes.Contains(stdout.Bytes(), []byte("discovery")) {
		t.Fatalf("fielded output included unrequested fields = %q", stdout.String())
	}

	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--json", "help", "mcp", "--fields", "usage,global,discovery,mcp_tools"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("mcp fielded help code=%d stderr=%q", code, stderr.String())
	}
	catalog = helpCatalog{}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("mcp fielded help json = %q err=%v", stdout.String(), err)
	}
	if catalog.Usage == "" || len(catalog.Global) == 0 || len(catalog.Discovery) == 0 || len(catalog.MCPTools) == 0 || len(catalog.Commands) != 0 {
		t.Fatalf("mcp fielded catalog = %#v", catalog)
	}

	stdout.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--json", "help", "--full"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("full help code=%d stderr=%q", code, stderr.String())
	}
	catalog = helpCatalog{}
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatalf("full help json = %q err=%v", stdout.String(), err)
	}
	if catalog.Commands[0].Summary == "" {
		t.Fatalf("full catalog omitted details = %#v", catalog.Commands[0])
	}

	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"help", "unknown"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitUsage || !strings.Contains(stderr.String(), "unknown help topic") {
		t.Fatalf("unknown help code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--json", "help", "unknown"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitUsage || stdout.Len() != 0 || !strings.Contains(stderr.String(), "unknown help topic") {
		t.Fatalf("unknown json help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--json", "help"}, Streams{Stdout: errWriter{}, Stderr: &stderr}); code != ExitRuntime || !strings.Contains(stderr.String(), "write failed") {
		t.Fatalf("json help write error code=%d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--json", "help", "--fields", "wat"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitUsage || !strings.Contains(stderr.String(), "unknown help field") {
		t.Fatalf("bad help field code=%d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"help", "--unknown-help-flag"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitUsage || !strings.Contains(stderr.String(), "unknown flag") {
		t.Fatalf("bad help flag code=%d stderr=%q", code, stderr.String())
	}
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"help", "mcp", "extra"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitUsage || !strings.Contains(stderr.String(), "usage: gomoufox help") {
		t.Fatalf("too many help args code=%d stderr=%q", code, stderr.String())
	}
}

func TestSkillsCLI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := (Runner{}).Run(context.Background(), []string{"skills", "list"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills list code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "core 0.1.0") || !strings.Contains(got, "mcp 0.1.0") || strings.Contains(got, "# gomoufox core") {
		t.Fatalf("skills list = %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"skills", "list", "--json"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills list json code=%d stderr=%q", code, stderr.String())
	}
	var list skillsListResponse
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		t.Fatalf("skills list json = %q err=%v", stdout.String(), err)
	}
	if len(list.Skills) != 2 || list.Skills[0].Name != "core" || list.Skills[0].SHA256 == "" || list.Skills[0].Bytes == 0 {
		t.Fatalf("skills list payload = %#v", list)
	}
	if bytes.Contains(stdout.Bytes(), []byte(`"body"`)) {
		t.Fatalf("skills list leaked body = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"skills", "show", "core"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills show code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "name: core") || !strings.Contains(got, "# gomoufox core") {
		t.Fatalf("skills show = %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"skills", "show", "core", "--version", "0.1.0", "--json"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills show json code=%d stderr=%q", code, stderr.String())
	}
	var skill struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		MinGomoufox string `json:"min_gomoufox"`
		SHA256      string `json:"sha256"`
		Bytes       int    `json:"bytes"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &skill); err != nil {
		t.Fatalf("skills show json = %q err=%v", stdout.String(), err)
	}
	if skill.Name != "core" || skill.Version != "0.1.0" || skill.MinGomoufox != "0.1.0" || skill.SHA256 == "" || skill.Bytes == 0 || !strings.Contains(skill.Body, "gomoufox core") {
		t.Fatalf("skills show payload = %#v", skill)
	}

	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"skills", "show", "core", "--version=0.1.0"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills show version= code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "version: 0.1.0") {
		t.Fatalf("skills show version= stdout = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"--version"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK || stdout.String() != "gomoufox dev\n" {
		t.Fatalf("global version code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestSkillsCLIExportAndInstall(t *testing.T) {
	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "exported")
	var stdout, stderr bytes.Buffer
	if code := (Runner{}).Run(context.Background(), []string{"skills", "export", "--out", outDir, "--json"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills export code=%d stderr=%q", code, stderr.String())
	}
	var exported skillsWriteResponse
	if err := json.Unmarshal(stdout.Bytes(), &exported); err != nil {
		t.Fatalf("skills export json = %q err=%v", stdout.String(), err)
	}
	expected := []string{
		filepath.Join("gomoufox", "SKILL.md"),
		filepath.Join("gomoufox", "agents", "openai.yaml"),
		filepath.Join("gomoufox-mcp", "SKILL.md"),
		filepath.Join("gomoufox-mcp", "agents", "openai.yaml"),
	}
	if exported.Dir != outDir || exported.DryRun || !sameStringSlice(exported.Written, expected) {
		t.Fatalf("skills export payload = %#v", exported)
	}
	coreSkill, err := os.ReadFile(filepath.Join(outDir, "gomoufox", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(coreSkill, []byte("---\nname: gomoufox\n")) || !bytes.Contains(coreSkill, []byte("# gomoufox core")) {
		t.Fatalf("exported core skill = %q", coreSkill)
	}

	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"skills", "export", "--out", outDir}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitRuntime || !strings.Contains(stderr.String(), "pass --force") {
		t.Fatalf("skills export overwrite code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"skills", "export", "--out", outDir, "--force"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK || !strings.Contains(stdout.String(), "wrote gomoufox/SKILL.md") {
		t.Fatalf("skills export force code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	installDir := filepath.Join(tmp, "installed")
	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"skills", "install", "--target", "codex", "--dir", installDir, "--dry-run", "--json"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills install dry-run code=%d stderr=%q", code, stderr.String())
	}
	var dryRun skillsWriteResponse
	if err := json.Unmarshal(stdout.Bytes(), &dryRun); err != nil {
		t.Fatalf("skills install dry-run json = %q err=%v", stdout.String(), err)
	}
	if dryRun.Target != "codex" || dryRun.Dir != installDir || !dryRun.DryRun || !sameStringSlice(dryRun.Written, expected) {
		t.Fatalf("skills install dry-run payload = %#v", dryRun)
	}
	if _, err := os.Stat(installDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created install dir err=%v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"skills", "install", "--target", "codex", "--dir", installDir, "--force"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills install code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(installDir, "gomoufox-mcp", "agents", "openai.yaml")); err != nil {
		t.Fatalf("installed mcp openai metadata: %v", err)
	}
}

func TestSkillsCLIInstallDefaultsToCodexHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(tmp, "codex-home"))
	var stdout, stderr bytes.Buffer
	if code := (Runner{}).Run(context.Background(), []string{"skills", "install", "--dry-run", "--json"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("skills install default code=%d stderr=%q", code, stderr.String())
	}
	var payload skillsWriteResponse
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("skills install default json = %q err=%v", stdout.String(), err)
	}
	if payload.Target != "codex" || payload.Dir != filepath.Join(os.Getenv("CODEX_HOME"), "skills") || !payload.DryRun {
		t.Fatalf("skills install default payload = %#v", payload)
	}
}

func TestSkillsCLIInstallHomeFallbackAndError(t *testing.T) {
	oldUserHomeDir := userHomeDir
	t.Cleanup(func() { userHomeDir = oldUserHomeDir })
	t.Setenv("CODEX_HOME", "")

	home := filepath.Join(t.TempDir(), "home")
	userHomeDir = func() (string, error) { return home, nil }
	dir, err := resolveSkillsInstallDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, ".codex", "skills") {
		t.Fatalf("fallback dir = %q", dir)
	}

	boom := errors.New("boom")
	userHomeDir = func() (string, error) { return "", boom }
	if _, err := resolveSkillsInstallDir("codex", ""); !errors.Is(err, boom) {
		t.Fatalf("home error = %v", err)
	}
}

func TestSkillsCLIPlainDryRunAndWriterValidation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := (Runner{}).Run(context.Background(), []string{"skills", "install", "--target", "codex", "--dir", t.TempDir(), "--dry-run"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("plain dry-run code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "would write gomoufox/SKILL.md") {
		t.Fatalf("plain dry-run stdout = %q", stdout.String())
	}

	if _, err := writeInstallableSkills(" ", nil, false, false); err == nil {
		t.Fatal("empty output directory accepted")
	}
	base := t.TempDir()
	for _, tc := range []struct {
		name        string
		installable skillreg.InstallableSkill
	}{
		{name: "bad directory", installable: skillreg.InstallableSkill{Directory: "bad/dir"}},
		{name: "bad file path", installable: skillreg.InstallableSkill{Directory: "ok", Files: []skillreg.InstallableFile{{Path: "../SKILL.md", Contents: "x"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := writeInstallableSkills(base, []skillreg.InstallableSkill{tc.installable}, false, false); err == nil {
				t.Fatalf("%s accepted", tc.name)
			}
		})
	}

	parent := filepath.Join(base, "parent")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeInstallableSkills(parent, []skillreg.InstallableSkill{
		{Directory: "gomoufox", Files: []skillreg.InstallableFile{{Path: "SKILL.md", Contents: "x"}}},
	}, false, false); err == nil {
		t.Fatal("writeInstallableSkills under file parent succeeded")
	}
	if err := writeInstallableFile(filepath.Join(parent, "child"), "x", false); err == nil {
		t.Fatal("write under file parent succeeded")
	}
	if err := writeInstallableFile(base, "x", false); err == nil {
		t.Fatal("write over directory succeeded")
	}
}

func TestSkillsCLIUsageErrors(t *testing.T) {
	cases := [][]string{
		{"skills"},
		{"skills", "wat"},
		{"skills", "list", "extra"},
		{"skills", "list", "--wat"},
		{"skills", "show"},
		{"skills", "show", "core", "extra"},
		{"skills", "show", "core", "--wat"},
		{"skills", "show", "core", "--version"},
		{"skills", "show", "missing"},
		{"skills", "show", "core", "--version", "9.9.9"},
		{"skills", "export"},
		{"skills", "export", "--out", t.TempDir(), "extra"},
		{"skills", "export", "--wat"},
		{"skills", "install", "extra"},
		{"skills", "install", "--wat"},
		{"skills", "install", "--target", "other", "--dry-run"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := (Runner{}).Run(context.Background(), args, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitUsage || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestSkillsCLIJSONWriteErrors(t *testing.T) {
	for _, args := range [][]string{
		{"skills", "list", "--json"},
		{"skills", "show", "core", "--json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stderr bytes.Buffer
			if code := (Runner{}).Run(context.Background(), args, Streams{Stdout: errWriter{}, Stderr: &stderr}); code != ExitRuntime || !strings.Contains(stderr.String(), "write failed") {
				t.Fatalf("%v code=%d stderr=%q", args, code, stderr.String())
			}
		})
	}
	t.Run("skills install", func(t *testing.T) {
		var stderr bytes.Buffer
		if code := (Runner{}).Run(context.Background(), []string{"skills", "install", "--target", "codex", "--dir", t.TempDir(), "--dry-run", "--json"}, Streams{Stdout: errWriter{}, Stderr: &stderr}); code != ExitRuntime || !strings.Contains(stderr.String(), "write failed") {
			t.Fatalf("skills install json write code=%d stderr=%q", code, stderr.String())
		}
	})
}

func TestAgentHelpGoldenContracts(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		budget int
	}{
		{name: "cli-help.json", args: []string{"help", "--json"}, budget: 4096},
		{name: "cli-help-full.json", args: []string{"help", "--json", "--full"}, budget: 12000},
		{name: "cli-help-mcp.json", args: []string{"help", "mcp", "--json"}, budget: 4096},
		{name: "cli-skills-list.json", args: []string{"skills", "list", "--json"}, budget: 4096},
		{name: "cli-skills-core.json", args: []string{"skills", "show", "core", "--json"}, budget: 4096},
		{name: "cli-skills-mcp.json", args: []string{"skills", "show", "mcp", "--json"}, budget: 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := (Runner{}).Run(context.Background(), tc.args, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
				t.Fatalf("%v code=%d stderr=%q", tc.args, code, stderr.String())
			}
			actual := canonicalJSONForTest(t, stdout.Bytes())
			expected, err := os.ReadFile(filepath.Join("..", "..", "docs", "agent-contracts", tc.name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, expected) {
				t.Fatalf("%s drifted; run python3 scripts/check-agent-contracts.py --update", tc.name)
			}
			if len(actual) > tc.budget {
				t.Fatalf("%s = %d bytes, budget %d", tc.name, len(actual), tc.budget)
			}
		})
	}
}

func TestCommandHelpAdvertisesParsedFlags(t *testing.T) {
	parsed := map[string]map[string]flagSpec{
		"install":    installFlagSpecs(),
		"open":       openFlagSpecs(),
		"get":        getFlagSpecs(),
		"screenshot": screenshotFlagSpecs(),
		"eval":       evalFlagSpecs(),
		"fetch":      fetchFlagSpecs(),
		"session":    mergeFlagSpecs(sessionExportFlagSpecs(), sessionImportFlagSpecs()),
		"skills":     mergeFlagSpecs(skillsListFlagSpecs(), skillsShowFlagSpecs(), skillsExportFlagSpecs(), skillsInstallFlagSpecs()),
		"serve":      serveFlagSpecs(),
		"mcp":        mcpFlagSpecs(),
		"help":       helpFlagSpecs(),
	}
	hidden := map[string]map[string]bool{
		"open": {"no-wait": true},
	}
	for command, specs := range parsed {
		t.Run(command, func(t *testing.T) {
			doc, ok := helpForCommand(command)
			if !ok {
				t.Fatalf("missing help for %s", command)
			}
			advertised := advertisedFlagNames(doc.Flags)
			for name := range specs {
				if hidden[command][name] {
					continue
				}
				if !advertised[name] {
					t.Fatalf("%s parses --%s but help flags are %#v", command, name, doc.Flags)
				}
			}
		})
	}
}

func TestMCPHelpDiscoversHighRiskGateFlags(t *testing.T) {
	doc, ok := helpForCommand("mcp")
	if !ok {
		t.Fatal("missing mcp help")
	}
	flags := advertisedFlagNames(doc.Flags)
	for _, flag := range []string{
		"enable-eval",
		"allow-browser-fetch",
		"allow-cookie-values",
		"allow-cookie-mutation",
		"allow-snapshot-values",
		"allow-session-export",
		"allow-session-import",
		"allow-session-proxy",
		"allow-file-upload",
		"allowed-origins",
		"allowed-hosts",
	} {
		if !flags[flag] {
			t.Fatalf("mcp help missing --%s in %#v", flag, doc.Flags)
		}
	}
}

func TestGlobalFlagsAfterCommandAndDaemonForwardEnvelope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	runner := Runner{Hooks: Hooks{Forward: func(ctx context.Context, req ForwardRequest) (ForwardResponse, error) {
		called = true
		if req.ServerURL != "http://127.0.0.1:3741" || req.Token != "tok" || req.Endpoint != "/v1/commands/get" || req.Verb != "get" {
			t.Fatalf("forward request = %#v", req)
		}
		if req.Envelope.Profile != "/tmp/profile" || !req.Envelope.JSON {
			t.Fatalf("envelope globals = %#v", req.Envelope)
		}
		if len(req.Envelope.Args) != 1 || req.Envelope.Args[0] != "https://example.com" {
			t.Fatalf("envelope args = %#v", req.Envelope.Args)
		}
		if req.Envelope.Flags["html"] != true || req.Envelope.Flags["timeout"] != "45s" {
			t.Fatalf("envelope flags = %#v", req.Envelope.Flags)
		}
		return ForwardResponse{ExitCode: ExitElement, Stdout: "out\n", Stderr: "err\n"}, nil
	}}}
	code := runner.Run(context.Background(), []string{
		"get", "https://example.com", "--html", "--json", "--profile", "/tmp/profile",
		"--timeout=45s", "--server", "http://127.0.0.1:3741", "--server-token", "tok",
	}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitElement || !called {
		t.Fatalf("code=%d called=%v", code, called)
	}
	if stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDaemonEnvIgnoredForLocalOnlyAndExplicitServerRejected(t *testing.T) {
	t.Setenv("GOMOUFOX_DAEMON", "http://127.0.0.1:3741")
	t.Setenv("GOMOUFOX_DAEMON_TOKEN", "")
	var stderr bytes.Buffer
	called := false
	runner := Runner{Hooks: Hooks{Doctor: func(context.Context, DoctorRequest) (DoctorReport, error) {
		called = true
		return allDoctorOK(), nil
	}}}
	code := runner.Run(context.Background(), []string{"doctor"}, Streams{Stderr: &stderr})
	if code != ExitOK || !called {
		t.Fatalf("code=%d called=%v stderr=%q", code, called, stderr.String())
	}

	stderr.Reset()
	code = runner.Run(context.Background(), []string{"doctor", "--server", "http://127.0.0.1:3741"}, Streams{Stderr: &stderr})
	if code != ExitUsage || !strings.Contains(stderr.String(), "--server is not supported for doctor") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestCommandFlagValidationWithoutBrowser(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"get formats", []string{"get", "http://93.184.216.34", "--html", "--text"}},
		{"screenshot cap", []string{"screenshot", "http://93.184.216.34", "--full-page", "--max-bytes", "10485761"}},
		{"fetch method", []string{"fetch", "http://93.184.216.34", "--method", "TRACE"}},
		{"fetch header", []string{"fetch", "http://93.184.216.34", "--header", "bad"}},
		{"eval script mutex", []string{"eval", "http://93.184.216.34", "--enable-eval", "--script", "1", "--script-file", "x.js"}},
		{"mcp response cap", []string{"mcp", "--max-response-bytes", "524289"}},
		{"session export requires out", []string{"session", "export"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Runner{}.Run(context.Background(), tc.args, Streams{Stderr: &stderr})
			if code != ExitUsage {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestServeGuardrailOverrideAndBindWarning(t *testing.T) {
	var stderr bytes.Buffer
	code := Runner{}.Run(context.Background(), []string{"serve", "--auth-token", "tok", "--allow-private-ips"}, Streams{Stderr: &stderr})
	if code != ExitUsage || !strings.Contains(stderr.String(), "does not allow URL guardrail overrides") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}

	stderr.Reset()
	called := false
	runner := Runner{Hooks: Hooks{Serve: func(ctx context.Context, req ServeRequest) error {
		called = true
		if req.Bind != "0.0.0.0" || req.AuthToken != "tok" {
			t.Fatalf("request = %#v", req)
		}
		return nil
	}}}
	code = runner.Run(context.Background(), []string{"serve", "--auth-token", "tok", "--bind", "0.0.0.0"}, Streams{Stderr: &stderr})
	if code != ExitOK || !called || !strings.Contains(stderr.String(), "WARNING:") {
		t.Fatalf("code=%d called=%v stderr=%q", code, called, stderr.String())
	}
}

func TestMCPAuthGuardrailsAndBounds(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{"mcp", "--transport", "http"}, ExitSessionAuth},
		{[]string{"mcp", "--transport", "bogus"}, ExitUsage},
		{[]string{"mcp", "--session-ttl", "0s"}, ExitUsage},
		{[]string{"mcp", "--session-ttl", "25h"}, ExitUsage},
		{[]string{"mcp", "--max-sessions", "21"}, ExitUsage},
		{[]string{"mcp", "--allow-schemes", "file"}, ExitUsage},
		{[]string{"mcp", "--toolset", "wide"}, ExitUsage},
	}
	for _, tc := range tests {
		var stderr bytes.Buffer
		if code := (Runner{}).Run(context.Background(), tc.args, Streams{Stderr: &stderr}); code != tc.code {
			t.Fatalf("%v code=%d want=%d stderr=%q", tc.args, code, tc.code, stderr.String())
		}
	}
}

func TestMCPHookReceivesParsedRuntimeRequest(t *testing.T) {
	called := false
	runner := Runner{Hooks: Hooks{MCP: func(ctx context.Context, req MCPRequest) error {
		called = true
		if req.Transport != "http" || req.Port != 3888 || req.AuthToken != "tok" {
			t.Fatalf("request = %#v", req)
		}
		if req.Config.SessionDir == "" || req.Config.Toolset != mcpserver.ToolsetCore || !req.Config.Policy.EnableEval || !req.Config.Policy.AllowBrowserFetch || !req.Config.Policy.AllowCookieValues || !req.Config.Policy.AllowCookieMutation || !req.Config.Policy.AllowSnapshotValues || !req.Config.Policy.AllowSessionExport || !req.Config.Policy.AllowSessionImport || !req.Config.Policy.AllowSessionProxy || !req.Config.Policy.AllowFileUpload {
			t.Fatalf("config = %#v", req.Config)
		}
		if req.Config.Policy.ContentWarning || req.Config.Policy.MaxInputBytes != 1024 || req.Config.Policy.MaxResponseBytes != 2048 || req.Config.Policy.MaxSessions != 3 || req.Config.Policy.SessionTTL != 45*time.Minute {
			t.Fatalf("policy = %#v", req.Config.Policy)
		}
		if strings.Join(req.Config.Policy.AllowedOrigins, ",") != "https://example.com" || strings.Join(req.Config.Policy.AllowedHosts, ",") != ".example.com" {
			t.Fatalf("allowlists = %#v %#v", req.Config.Policy.AllowedOrigins, req.Config.Policy.AllowedHosts)
		}
		return nil
	}}}
	code := runner.Run(context.Background(), []string{
		"mcp",
		"--transport", "http",
		"--toolset", "core",
		"--port", "3888",
		"--auth-token", "tok",
		"--session-dir", t.TempDir(),
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
		"--allowed-origins", "https://example.com",
		"--allowed-hosts", ".example.com",
		"--max-input-bytes", "1024",
		"--max-response-bytes", "2048",
		"--session-ttl", "45m",
		"--max-sessions", "3",
	}, Streams{})
	if code != ExitOK || !called {
		t.Fatalf("code=%d called=%v", code, called)
	}
}

func TestDefaultServeAndMCPTransportsShutdown(t *testing.T) {
	runUntilCanceled := func(t *testing.T, fn func(context.Context) error) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- fn(ctx) }()
		time.Sleep(25 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("server did not shut down")
		}
	}

	runUntilCanceled(t, func(ctx context.Context) error {
		return defaultServe(ctx, ServeRequest{Bind: "127.0.0.1", Port: 0, AuthToken: "tok"})
	})
	runUntilCanceled(t, func(ctx context.Context) error {
		return defaultMCP(ctx, MCPRequest{
			Transport: "http",
			Port:      0,
			AuthToken: "tok",
			Config:    mcpserver.Config{SessionDir: t.TempDir()},
		})
	})

	var stdout bytes.Buffer
	err := defaultMCP(context.Background(), MCPRequest{
		Transport: "stdio",
		Config:    mcpserver.Config{SessionDir: t.TempDir()},
		Stdin:     strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}` + "\n"),
		Stdout:    &stdout,
	})
	if err != nil || !strings.Contains(stdout.String(), `"result":{}`) {
		t.Fatalf("stdio mcp err=%v stdout=%q", err, stdout.String())
	}
	if err := defaultMCP(context.Background(), MCPRequest{Transport: "bad", Config: mcpserver.Config{SessionDir: t.TempDir()}}); err == nil {
		t.Fatal("bad transport succeeded")
	}
}

func TestLocalURLGuardrailsAndProxyValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"scheme blocked", []string{"get", "file:///tmp/x"}, ExitURLBlocked, "url blocked"},
		{"private blocked", []string{"get", "http://127.0.0.1"}, ExitURLBlocked, "resolved address"},
		{"private override", []string{"get", "http://127.0.0.1", "--allow-private-ips"}, ExitRuntime, "WARNING:"},
		{"navigate first blocked", []string{"fetch", "http://93.184.216.34", "--navigate-first", "http://127.0.0.1"}, ExitURLBlocked, "resolved address"},
		{"bad proxy", []string{"get", "http://93.184.216.34", "--proxy", "ftp://proxy.example"}, ExitSessionAuth, "invalid --proxy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			runner := Runner{Hooks: Hooks{LocalCommand: func(context.Context, LocalCommandRequest) (LocalCommandResponse, error) {
				return LocalCommandResponse{ExitCode: ExitRuntime}, nil
			}}}
			code := runner.Run(context.Background(), tc.args, Streams{Stderr: &stderr})
			if code != tc.code || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestLocalCommandHookReceivesParsedRequest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	runner := Runner{Hooks: Hooks{LocalCommand: func(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
		called = true
		if req.Command != "get" || len(req.Args) != 1 || req.Args[0] != "http://93.184.216.34" || req.Profile != "/profile" || !req.JSON {
			t.Fatalf("request = %#v", req)
		}
		if req.Flags["html"] != true || req.Flags["timeout"] != "5s" || req.Flags["headless"] != false {
			t.Fatalf("flags = %#v", req.Flags)
		}
		return LocalCommandResponse{ExitCode: ExitOK, Stdout: []byte("ok\n"), Stderr: "warn\n"}, nil
	}}}
	code := runner.Run(context.Background(), []string{"get", "http://93.184.216.34", "--html", "--json", "--profile", "/profile", "--timeout", "5s", "--headless=false"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK || !called {
		t.Fatalf("code=%d called=%v", code, called)
	}
	if stdout.String() != "ok\n" || stderr.String() != "warn\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestOpenForcesHeadfulAndRejectsNoWaitModes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	runner := Runner{Hooks: Hooks{LocalCommand: func(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
		called = true
		if req.Command != "open" || len(req.Args) != 1 || req.Args[0] != "http://93.184.216.34" {
			t.Fatalf("request = %#v", req)
		}
		if req.Flags["headful"] != true || req.Flags["save_session"] != "state.json" || req.Flags["humanize"] != "1.5" {
			t.Fatalf("flags = %#v", req.Flags)
		}
		return LocalCommandResponse{ExitCode: ExitOK, Stdout: []byte("ok\n")}, nil
	}}}
	code := runner.Run(context.Background(), []string{"open", "http://93.184.216.34", "--save-session", "state.json", "--humanize=1.5"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK || !called || stdout.String() != "ok\n" || stderr.String() != "" {
		t.Fatalf("code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}

	for _, args := range [][]string{
		{"open", "http://93.184.216.34", "--no-wait"},
		{"open", "http://93.184.216.34", "--wait=false"},
		{"open", "http://93.184.216.34", "--headless=false"},
	} {
		called = false
		stderr.Reset()
		code = runner.Run(context.Background(), args, Streams{Stderr: &stderr})
		if code != ExitUsage || called {
			t.Fatalf("%v code=%d called=%v stderr=%q", args, code, called, stderr.String())
		}
	}
}

func TestEvalEnabledHookAndParserEdges(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	runner := Runner{Hooks: Hooks{LocalCommand: func(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
		called = true
		if req.Command != "eval" || len(req.Args) != 1 || req.Args[0] != "http://93.184.216.34" || !req.JSON {
			t.Fatalf("request = %#v", req)
		}
		if req.Flags["script"] != "(() => 1)()" || req.Flags["enable_eval"] != true || req.Flags["wait_load_state"] != "networkidle" || req.Flags["arg"] != `{"x":1}` {
			t.Fatalf("flags = %#v", req.Flags)
		}
		return LocalCommandResponse{ExitCode: ExitOK, Stdout: []byte("eval\n")}, nil
	}}}
	code := runner.Run(context.Background(), []string{
		"--json",
		"eval",
		"http://93.184.216.34",
		"--enable-eval",
		"--script", "(() => 1)()",
		"--wait-load-state", "networkidle",
		"--arg", `{"x":1}`,
	}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK || !called || stdout.String() != "eval\n" || stderr.String() != "" {
		t.Fatalf("code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}

	parsed, err := parseEval([]string{"http://93.184.216.34", "--script", "1"})
	if err != nil || parsed.value("script") != "1" {
		t.Fatalf("parseEval parsed=%#v err=%v", parsed, err)
	}
	if _, err := parseEval([]string{"http://93.184.216.34", "--script", "1", "--wait-load-state", "idle"}); err == nil {
		t.Fatal("bad load state eval succeeded")
	}
	if _, err := parseEval([]string{"http://93.184.216.34", "--script", strings.Repeat("x", 64*1024+1)}); err == nil {
		t.Fatal("oversized eval script succeeded")
	}
}

func TestExitCodeMapping(t *testing.T) {
	tests := []struct {
		err  error
		code int
	}{
		{gomoufox.ErrSidecarDied, ExitUnavailable},
		{gomoufox.ErrVersionMismatch, ExitVersion},
		{gomoufox.ErrNavigationTimeout, ExitTimeout},
		{gomoufox.ErrTimeout, ExitTimeout},
		{gomoufox.ErrElementNotFound, ExitElement},
		{gomoufox.ErrURLBlocked, ExitURLBlocked},
		{gomoufox.ErrSessionClosed, ExitSessionAuth},
		{context.DeadlineExceeded, ExitCommandTimeout},
	}
	for _, tc := range tests {
		var stderr bytes.Buffer
		runner := Runner{Hooks: Hooks{Install: func(context.Context, InstallRequest) error { return tc.err }}}
		if code := runner.Run(context.Background(), []string{"install"}, Streams{Stderr: &stderr}); code != tc.code {
			t.Fatalf("%v mapped to %d want %d stderr=%q", tc.err, code, tc.code, stderr.String())
		}
	}
}

func TestDoctorHumanWarnFailAndJSONPlaywrightFields(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := Runner{Hooks: Hooks{Doctor: func(context.Context, DoctorRequest) (DoctorReport, error) {
		report := allDoctorOK()
		report.Python = Check{OK: true, Version: "3.12.4"}
		report.Display = Check{OK: false, Warning: "DISPLAY not set"}
		return report, nil
	}}}
	code := runner.Run(context.Background(), []string{"doctor"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK || !strings.Contains(stdout.String(), "[WARN]") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	runner.Hooks.Doctor = func(context.Context, DoctorRequest) (DoctorReport, error) {
		report := allDoctorOK()
		report.CamoufoxPkg = Check{OK: false, Error: "missing"}
		return report, nil
	}
	code = runner.Run(context.Background(), []string{"doctor"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitUnavailable || !strings.Contains(stdout.String(), "[FAIL]") {
		t.Fatalf("code=%d stdout=%q", code, stdout.String())
	}

	match := true
	stdout.Reset()
	runner.Hooks.Doctor = func(context.Context, DoctorRequest) (DoctorReport, error) {
		report := allDoctorOK()
		report.Playwright = Check{OK: true, PkgVersion: "1.57.0", DriverVersion: "1.57.0", Match: &match}
		return report, nil
	}
	code = runner.Run(context.Background(), []string{"doctor", "--json"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var report DoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid json: %v: %q", err, stdout.String())
	}
	if report.Playwright.PkgVersion != "1.57.0" || report.Playwright.DriverVersion != "1.57.0" || report.Playwright.Match == nil || !*report.Playwright.Match {
		t.Fatalf("report = %#v", report.Playwright)
	}
}

func TestParseDuration(t *testing.T) {
	fallback := 30 * time.Second
	got, err := ParseDuration("", fallback)
	if err != nil || got != fallback {
		t.Fatalf("empty got=%s err=%v", got, err)
	}
	got, err = ParseDuration("1500ms", fallback)
	if err != nil || got != 1500*time.Millisecond {
		t.Fatalf("duration got=%s err=%v", got, err)
	}
	for _, raw := range []string{"", "0s", "-1s", "nope"} {
		if raw == "" {
			continue
		}
		if _, err := ParseDuration(raw, fallback); err == nil {
			t.Fatalf("ParseDuration(%q) succeeded", raw)
		}
	}
}

func TestBuildForwardSessionEnvelope(t *testing.T) {
	req, err := buildForwardRequest(globalFlags{Server: "http://127.0.0.1:3741", ServerToken: "tok", JSON: true}, "session", []string{"export", "--out", "state.json"})
	if err != nil {
		t.Fatalf("build err = %v", err)
	}
	if req.Endpoint != "/v1/session/export" || req.Verb != "session export" || !req.Envelope.JSON {
		t.Fatalf("request = %#v", req)
	}
	if req.Envelope.Flags["out"] != "state.json" {
		t.Fatalf("flags = %#v", req.Envelope.Flags)
	}
}

func TestDefaultLocalGetScreenshotEvalAndFetchWithFakes(t *testing.T) {
	page := &fakeLocalPage{
		html:          "<html><body><main>Body text</main></body></html>",
		bodyText:      "Body text",
		title:         "Title",
		url:           "https://example.com/final",
		screenshot:    []byte("png"),
		evaluateValue: map[string]any{"ok": true},
		fetchStatus:   201,
		fetchBody:     []byte(`{"id":42}`),
	}
	restore := fakeOpenPage(t, page)
	defer restore()

	getResp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "get",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"wait_selector": "#main", "wait_load_state": "load", "max_bytes": "1024"},
		JSON:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if getResp.ExitCode != ExitOK || !bytes.Contains(getResp.Stdout, []byte(`"final_url":"https://example.com/final"`)) || page.waitSelector != "#main" {
		t.Fatalf("get resp=%s page=%#v", getResp.Stdout, page)
	}

	out := filepath.Join(t.TempDir(), "shot.png")
	shotResp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "screenshot",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"out": out, "width": "320", "height": "240", "full_page": true, "quality": "80", "clip": "1,2,3,4"},
		JSON:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(shotResp.Stdout, []byte(`"path"`)) || page.screenshotCalls == 0 {
		t.Fatalf("screenshot resp=%s calls=%d", shotResp.Stdout, page.screenshotCalls)
	}
	if data, err := os.ReadFile(out); err != nil || string(data) != "png" {
		t.Fatalf("shot file = %q %v", data, err)
	}

	scriptPath := filepath.Join(t.TempDir(), "script.js")
	if err := os.WriteFile(scriptPath, []byte("arg => arg.ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	evalResp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "eval",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"script_file": scriptPath, "arg": `{"ok":true}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(evalResp.Stdout, []byte(`"ok":true`)) || page.script != "arg => arg.ok" || len(page.evalArgs) != 1 {
		t.Fatalf("eval resp=%s script=%q args=%#v", evalResp.Stdout, page.script, page.evalArgs)
	}

	fetchResp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "fetch",
		Args:    []string{"https://api.example.com/me"},
		Flags:   map[string]any{"method": "POST", "header": []string{"X-Test: yes"}, "data": `{"q":1}`, "navigate_first": "https://example.com/app"},
		JSON:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(fetchResp.Stdout, []byte(`"status":201`)) || page.fetchURL != "https://api.example.com/me" || page.fetchHeaders["X-Test"] != "yes" {
		t.Fatalf("fetch resp=%s page=%#v", fetchResp.Stdout, page)
	}
}

func TestDefaultLocalFetchRawTruncatesAndDataFile(t *testing.T) {
	bodyPath := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(bodyPath, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	page := &fakeLocalPage{
		url:         "https://example.com/final",
		fetchStatus: 200,
		fetchBody:   []byte(`{"abcdef":true}`),
	}
	restore := fakeOpenPage(t, page)
	defer restore()

	resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "fetch",
		Args:    []string{"https://api.example.com/data"},
		Flags: map[string]any{
			"content_type": "text/plain",
			"data_file":    bodyPath,
			"max_bytes":    "5",
			"raw":          true,
		},
		JSON: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Stdout, &payload); err != nil {
		t.Fatalf("json = %v: %s", err, resp.Stdout)
	}
	body, _ := payload["body"].(string)
	if !strings.HasPrefix(body, `{"abc`) || !strings.Contains(body, "truncated") {
		t.Fatalf("body = %q", body)
	}
	if payload["truncated"] != true || payload["bytes"] != float64(5) {
		t.Fatalf("metadata = %#v", payload)
	}
	if page.fetchMaxBytes != 5 {
		t.Fatalf("fetch max bytes = %d", page.fetchMaxBytes)
	}
	if string(page.fetchRequest) != "payload" || page.fetchHeaders["Content-Type"] != "text/plain" {
		t.Fatalf("page fetch = %#v", page)
	}
}

func TestDefaultLocalSessionImportExportWithFakes(t *testing.T) {
	src := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(src, []byte(`{"cookies":[{"name":"a","value":"b"}],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "normalized.json")
	resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session import",
		Flags:   map[string]any{"file": src, "out": dst},
		JSON:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(resp.Stdout, []byte(`"cookies":1`)) {
		t.Fatalf("import resp=%s", resp.Stdout)
	}
	if st, err := os.Stat(dst); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("dst mode = %v %v", st, err)
	}

	browser := &fakeLocalBrowser{state: &gomoufox.StorageState{Cookies: []gomoufox.Cookie{{Name: "a", Value: "b"}}}}
	restore := fakeOpenBrowser(t, browser)
	defer restore()
	out := filepath.Join(t.TempDir(), "export.json")
	resp, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session export",
		Profile: "/profile",
		Flags:   map[string]any{"out": out},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Stdout) != out+"\n" || browser.path != "" || !browser.closed {
		t.Fatalf("export resp=%q browser=%#v", resp.Stdout, browser)
	}
	if st, err := os.Stat(out); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("exported state mode = %v %v", st, err)
	}
}

func TestDefaultLocalOpenBlocksAndSavesSessionWithFakes(t *testing.T) {
	out := filepath.Join(t.TempDir(), "saved.json")
	page := &fakeLocalPage{
		url:         "https://example.com/final",
		closeWait:   make(chan struct{}),
		waitEntered: make(chan struct{}),
		state:       &gomoufox.StorageState{Cookies: []gomoufox.Cookie{{Name: "sid", Value: "secret"}}},
	}
	restore := fakeOpenPage(t, page)
	defer restore()

	type result struct {
		resp LocalCommandResponse
		err  error
	}
	done := make(chan result, 1)
	waitEntered := page.waitEntered
	closeWait := page.closeWait
	go func() {
		resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
			Command: "open",
			Args:    []string{"https://example.com"},
			Flags:   map[string]any{"save_session": out},
		})
		done <- result{resp: resp, err: err}
	}()

	<-waitEntered
	select {
	case got := <-done:
		t.Fatalf("open returned before page close: resp=%#v err=%v", got.resp, got.err)
	case <-time.After(25 * time.Millisecond):
	}
	close(closeWait)
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.resp.ExitCode != ExitOK || string(got.resp.Stdout) != "https://example.com/final\n"+out+"\n" {
		t.Fatalf("response = %#v", got.resp)
	}
	if len(page.gotoURLs) != 1 || page.gotoURLs[0] != "https://example.com" || page.waitClosedCalls != 1 || page.storageCalls != 1 || !page.closed {
		t.Fatalf("page = %#v", page)
	}
	if st, err := os.Stat(out); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("saved mode = %v %v", st, err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"sid"`)) {
		t.Fatalf("saved state = %s", data)
	}
}

func TestOpenHelperParsesHumanizeAndStorageState(t *testing.T) {
	enabled, duration, err := humanizeForLocal(LocalCommandRequest{Command: "open", Flags: map[string]any{}})
	if err != nil || !enabled || duration != 0 {
		t.Fatalf("open default humanize enabled=%v duration=%s err=%v", enabled, duration, err)
	}
	enabled, duration, err = humanizeForLocal(LocalCommandRequest{Command: "get", Flags: map[string]any{}})
	if err != nil || enabled || duration != 0 {
		t.Fatalf("get default humanize enabled=%v duration=%s err=%v", enabled, duration, err)
	}
	enabled, duration, err = humanizeForLocal(LocalCommandRequest{Flags: map[string]any{"humanize": "1.5"}})
	if err != nil || !enabled || duration != 1500*time.Millisecond {
		t.Fatalf("float humanize enabled=%v duration=%s err=%v", enabled, duration, err)
	}
	enabled, duration, err = humanizeForLocal(LocalCommandRequest{Flags: map[string]any{"humanize": "false"}})
	if err != nil || enabled || duration != 0 {
		t.Fatalf("false humanize enabled=%v duration=%s err=%v", enabled, duration, err)
	}
	if _, _, err := humanizeForLocal(LocalCommandRequest{Flags: map[string]any{"humanize": "-1"}}); err == nil {
		t.Fatal("negative humanize succeeded")
	}

	state, err := storageStateFromPlaywright(&playwright.StorageState{
		Cookies: []playwright.Cookie{{Name: "sid", Value: "secret", Domain: "example.com", Path: "/"}},
		Origins: []playwright.Origin{{
			Origin:       "https://example.com",
			LocalStorage: []playwright.NameValue{{Name: "theme", Value: "dark"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Cookies) != 1 || state.Cookies[0].Name != "sid" || len(state.Origins) != 1 || state.Origins[0].LocalStorage[0].Name != "theme" {
		t.Fatalf("state = %#v", state)
	}
	empty, err := storageStateFromPlaywright(nil)
	if err != nil || len(empty.Cookies) != 0 || len(empty.Origins) != 0 {
		t.Fatalf("empty = %#v err=%v", empty, err)
	}
}

func TestLocalOptionAndFileHelperEdges(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte(`{"cookies":[{"name":"sid","value":"1"}],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := contextOptionsForLocal(
		LocalCommandRequest{Flags: map[string]any{"cookies_file": statePath}},
		[]gomoufox.ContextOption{gomoufox.WithViewport(10, 20)},
	)
	if err != nil || len(opts) != 2 {
		t.Fatalf("context opts len=%d err=%v", len(opts), err)
	}
	if _, err := contextOptionsForLocal(LocalCommandRequest{Flags: map[string]any{"cookies_file": filepath.Join(t.TempDir(), "missing.json")}}, nil); err == nil {
		t.Fatal("missing cookies file succeeded")
	}

	cfg, err := proxyConfig("http://user:pass@proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "http://proxy.example:8080" || cfg.Username != "user" || cfg.Password != "pass" {
		t.Fatalf("proxy cfg = %#v", cfg)
	}
	cfg, err = proxyConfig("http://proxy.example")
	if err != nil || cfg.Server != "http://proxy.example" || cfg.Username != "" || cfg.Password != "" {
		t.Fatalf("proxy cfg without auth = %#v err=%v", cfg, err)
	}
	if _, err := proxyConfig("%"); err == nil {
		t.Fatal("bad proxy URL succeeded")
	}

	browserOpts, err := browserOptions(LocalCommandRequest{
		Command: "get",
		Profile: "/profile",
		Flags: map[string]any{
			"headless": true,
			"humanize": "0.25",
			"locale":   "en-US",
			"os":       "linux",
			"proxy":    "http://user:pass@proxy.example:8080",
		},
	})
	if err != nil || len(browserOpts) != 6 {
		t.Fatalf("browser opts len=%d err=%v", len(browserOpts), err)
	}
	browserOpts, err = browserOptions(LocalCommandRequest{Command: "open", Flags: map[string]any{}})
	if err != nil || len(browserOpts) != 2 {
		t.Fatalf("open browser opts len=%d err=%v", len(browserOpts), err)
	}
	if _, err := browserOptions(LocalCommandRequest{Flags: map[string]any{"proxy": "%"}}); err == nil {
		t.Fatal("bad browser proxy succeeded")
	}
	if _, err := browserOptions(LocalCommandRequest{Flags: map[string]any{"humanize": "soon"}}); err == nil {
		t.Fatal("bad humanize succeeded")
	}
}

func TestOpenRealPageUsesInjectedBrowserAndClosesAll(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte(`{"cookies":[{"name":"sid","value":"1"}],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	page := &fakeGomoufoxPage{url: "https://example.com/final"}
	browser := &fakeGomoufoxBrowser{page: page}
	restore := replaceNewBrowserForLocal(t, func(context.Context, ...gomoufox.Option) (gomoufoxBrowserForLocal, error) {
		return browser, nil
	})
	defer restore()

	local, closeAll, err := openRealPage(
		context.Background(),
		LocalCommandRequest{Command: "get", Flags: map[string]any{"cookies_file": statePath}},
		[]gomoufox.ContextOption{gomoufox.WithViewport(10, 20)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if local.URL() != "https://example.com/final" {
		t.Fatalf("url = %q", local.URL())
	}
	if browser.newPageOpts != 2 {
		t.Fatalf("context option count = %d", browser.newPageOpts)
	}
	closeAll()
	if page.closeCalls != 1 || browser.closeCalls != 1 {
		t.Fatalf("close calls page=%d browser=%d", page.closeCalls, browser.closeCalls)
	}
}

func TestOpenRealPageAndBrowserErrorBranchesWithInjectedBrowser(t *testing.T) {
	if _, _, err := openRealPage(context.Background(), LocalCommandRequest{Flags: map[string]any{"proxy": "%"}}, nil); err == nil {
		t.Fatal("openRealPage with bad proxy succeeded")
	}
	if _, _, err := openRealBrowser(context.Background(), LocalCommandRequest{Flags: map[string]any{"proxy": "%"}}); err == nil {
		t.Fatal("openRealBrowser with bad proxy succeeded")
	}

	restore := replaceNewBrowserForLocal(t, func(context.Context, ...gomoufox.Option) (gomoufoxBrowserForLocal, error) {
		return nil, errors.New("new browser failed")
	})
	if _, _, err := openRealPage(context.Background(), LocalCommandRequest{}, nil); err == nil {
		t.Fatal("openRealPage with browser error succeeded")
	}
	if _, _, err := openRealBrowser(context.Background(), LocalCommandRequest{}); err == nil {
		t.Fatal("openRealBrowser with browser error succeeded")
	}
	restore()

	browser := &fakeGomoufoxBrowser{}
	restore = replaceNewBrowserForLocal(t, func(context.Context, ...gomoufox.Option) (gomoufoxBrowserForLocal, error) {
		return browser, nil
	})
	if _, _, err := openRealPage(context.Background(), LocalCommandRequest{Flags: map[string]any{"cookies_file": filepath.Join(t.TempDir(), "missing.json")}}, nil); err == nil {
		t.Fatal("openRealPage with bad cookies file succeeded")
	}
	if browser.closeCalls != 1 {
		t.Fatalf("browser close after cookies error = %d", browser.closeCalls)
	}
	restore()

	browser = &fakeGomoufoxBrowser{pageErr: errors.New("new page failed")}
	restore = replaceNewBrowserForLocal(t, func(context.Context, ...gomoufox.Option) (gomoufoxBrowserForLocal, error) {
		return browser, nil
	})
	if _, _, err := openRealPage(context.Background(), LocalCommandRequest{}, nil); err == nil {
		t.Fatal("openRealPage with page error succeeded")
	}
	if browser.closeCalls != 1 {
		t.Fatalf("browser close after page error = %d", browser.closeCalls)
	}
	restore()
}

func TestOpenRealBrowserUsesInjectedBrowserForStorageState(t *testing.T) {
	storageCtx := &fakeGomoufoxStorageContext{state: &gomoufox.StorageState{
		Cookies: []gomoufox.Cookie{{Name: "sid", Value: "secret"}},
	}}
	browser := &fakeGomoufoxBrowser{context: storageCtx}
	restore := replaceNewBrowserForLocal(t, func(context.Context, ...gomoufox.Option) (gomoufoxBrowserForLocal, error) {
		return browser, nil
	})
	defer restore()

	local, closeAll, err := openRealBrowser(context.Background(), LocalCommandRequest{Profile: "/profile"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := local.StorageState(context.Background(), filepath.Join(t.TempDir(), "state.json"))
	if err != nil || len(state.Cookies) != 1 || state.Cookies[0].Name != "sid" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if storageCtx.closeCalls != 1 {
		t.Fatalf("context close calls = %d", storageCtx.closeCalls)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	closeAll()
	if browser.closeCalls != 2 {
		t.Fatalf("browser close calls = %d", browser.closeCalls)
	}
}

func TestRealLocalPageDelegatesToGomoufoxPage(t *testing.T) {
	raw := &fakePlaywrightPageRaw{
		context: &fakePlaywrightStorageContext{state: &playwright.StorageState{
			Cookies: []playwright.Cookie{{Name: "sid", Value: "secret", Domain: "example.com", Path: "/"}},
		}},
	}
	page := &fakeGomoufoxPage{
		content:       "<html><body>Body</body></html>",
		bodyText:      "Body",
		title:         "Title",
		url:           "https://example.com/final",
		screenshot:    []byte("png"),
		evaluateValue: map[string]any{"ok": true},
		fetchStatus:   201,
		fetchBody:     []byte(`{"ok":true}`),
		raw:           raw,
	}
	local := realLocalPage{p: page}

	if err := local.Goto(context.Background(), "https://example.com"); err != nil {
		t.Fatal(err)
	}
	if err := local.WaitForSelector(context.Background(), "#ready"); err != nil {
		t.Fatal(err)
	}
	content, err := local.Content(context.Background())
	if err != nil || content != "<html><body>Body</body></html>" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	body, err := local.BodyText(context.Background())
	if err != nil || body != "Body" || page.locatorSelector != "body" {
		t.Fatalf("body=%q selector=%q err=%v", body, page.locatorSelector, err)
	}
	title, err := local.Title(context.Background())
	if err != nil || title != "Title" {
		t.Fatalf("title=%q err=%v", title, err)
	}
	if local.URL() != "https://example.com/final" {
		t.Fatalf("url = %q", local.URL())
	}
	shot, err := local.Screenshot(context.Background())
	if err != nil || string(shot) != "png" || page.screenshotCalls != 1 {
		t.Fatalf("shot=%q calls=%d err=%v", string(shot), page.screenshotCalls, err)
	}
	eval, err := local.Evaluate(context.Background(), "arg => arg", map[string]any{"x": 1})
	if err != nil || eval.(map[string]any)["ok"] != true || page.evaluateScript != "arg => arg" || len(page.evaluateArgs) != 1 {
		t.Fatalf("eval=%#v script=%q args=%#v err=%v", eval, page.evaluateScript, page.evaluateArgs, err)
	}
	status, data, err := local.FetchBytes(context.Background(), "https://api.example.com", "POST", map[string]string{"Accept": "application/json"}, []byte(`{"in":true}`))
	if err != nil || status != 201 || string(data) != `{"ok":true}` || page.fetchMethod != "POST" {
		t.Fatalf("fetch status=%d data=%q method=%q err=%v", status, string(data), page.fetchMethod, err)
	}
	if page.fetchURL != "https://api.example.com" || string(page.fetchRequest) != `{"in":true}` {
		t.Fatalf("page fetch records = %#v", page)
	}
	bounded, err := local.FetchBytesWithOptions(context.Background(), "https://api.example.com", "GET", nil, nil, gomoufox.FetchBytesOptions{MaxBytes: 4})
	if err != nil || bounded.StatusCode != 201 || string(bounded.Body) != `{"ok` || !bounded.Truncated || page.fetchMaxBytes != 4 {
		t.Fatalf("bounded fetch = %#v max=%d err=%v", bounded, page.fetchMaxBytes, err)
	}
	if err := local.WaitClosed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raw.eventName != "close" || len(raw.eventOptions) != 1 || raw.eventOptions[0].Timeout == nil || *raw.eventOptions[0].Timeout != 0 {
		t.Fatalf("wait event=%q options=%#v", raw.eventName, raw.eventOptions)
	}
	state, err := local.StorageState(context.Background())
	if err != nil || len(state.Cookies) != 1 || state.Cookies[0].Name != "sid" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if err := local.Close(); err != nil || page.closeCalls != 1 {
		t.Fatalf("close calls=%d err=%v", page.closeCalls, err)
	}
	if page.gotoURL != "https://example.com" || page.waitSelector != "#ready" {
		t.Fatalf("page records = %#v", page)
	}
}

func TestRealLocalPageAndBrowserErrorEdges(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newBrowserForLocal(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("newBrowserForLocal canceled err = %v", err)
	}
	adapter := gomoufoxBrowserAdapter{b: &gomoufox.Browser{}}
	if _, err := adapter.NewPage(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("adapter NewPage canceled err = %v", err)
	}
	if _, err := adapter.NewContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("adapter NewContext canceled err = %v", err)
	}

	local := realLocalPage{p: &fakeGomoufoxPage{}}
	if !errors.Is(local.WaitClosed(canceled), context.Canceled) {
		t.Fatal("WaitClosed did not return context cancellation")
	}
	if _, err := local.StorageState(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal("StorageState did not return context cancellation")
	}

	if err := (realLocalPage{p: &fakeGomoufoxPage{raw: struct{}{}}}).WaitClosed(context.Background()); err == nil {
		t.Fatal("unsupported WaitClosed raw succeeded")
	}
	if _, err := (realLocalPage{p: &fakeGomoufoxPage{raw: struct{}{}}}).StorageState(context.Background()); err == nil {
		t.Fatal("unsupported StorageState raw succeeded")
	}

	rawErr := errors.New("storage failed")
	raw := &fakePlaywrightPageRaw{context: &fakePlaywrightStorageContext{err: rawErr}}
	if _, err := (realLocalPage{p: &fakeGomoufoxPage{raw: raw}}).StorageState(context.Background()); !errors.Is(err, rawErr) {
		t.Fatalf("storage error = %v", err)
	}

	waitEntered := make(chan struct{})
	waitRelease := make(chan struct{})
	raw = &fakePlaywrightPageRaw{waitEntered: waitEntered, waitRelease: waitRelease}
	waitCtx, waitCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (realLocalPage{p: &fakeGomoufoxPage{raw: raw}}).WaitClosed(waitCtx)
	}()
	<-waitEntered
	waitCancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked WaitClosed err = %v", err)
	}
	close(waitRelease)

	contextErr := errors.New("new context failed")
	browser := realLocalBrowser{b: &fakeGomoufoxBrowser{contextErr: contextErr}}
	if _, err := browser.StorageState(context.Background(), "state.json"); !errors.Is(err, contextErr) {
		t.Fatalf("browser storage context error = %v", err)
	}
	storageErr := errors.New("state failed")
	storageCtx := &fakeGomoufoxStorageContext{err: storageErr}
	browser = realLocalBrowser{b: &fakeGomoufoxBrowser{context: storageCtx}}
	if _, err := browser.StorageState(context.Background(), "state.json"); !errors.Is(err, storageErr) || storageCtx.closeCalls != 1 {
		t.Fatalf("browser storage error=%v closeCalls=%d", err, storageCtx.closeCalls)
	}
}

func TestCookiesFileIsLoadedAndExclusiveWithProfile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte(`{"cookies":[{"name":"sid","value":"1"}],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := readStorageStateFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Cookies) != 1 || state.Cookies[0].Name != "sid" {
		t.Fatalf("state = %#v", state)
	}
	var stderr bytes.Buffer
	code := Runner{}.Run(context.Background(), []string{"get", "https://example.com", "--profile", "/profile", "--cookies-file", statePath}, Streams{Stderr: &stderr})
	if code != ExitUsage || !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestLocalFlagAndFileHelperEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exists.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFile0600(path, []byte("new"), false); err == nil {
		t.Fatal("write without overwrite succeeded")
	}
	target := filepath.Join(t.TempDir(), "target.txt")
	link := filepath.Join(t.TempDir(), "link.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := writeFile0600(link, []byte("replacement"), true); err == nil {
		t.Fatal("symlink write succeeded")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "target" {
		t.Fatalf("symlink target = %q err=%v", data, err)
	}
	st, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced unexpectedly: mode=%v", st.Mode())
	}
	if _, err := readCappedFileBytes(path, 2); err == nil {
		t.Fatal("capped read succeeded")
	}
	if _, err := bodyFromFlags(LocalCommandRequest{Flags: map[string]any{"data_file": filepath.Join(t.TempDir(), "missing.txt")}}); err == nil {
		t.Fatal("missing data file succeeded")
	}
	if got := flagString(LocalCommandRequest{Flags: map[string]any{"name": []string{"", "last"}}}, "name", "fallback"); got != "last" {
		t.Fatalf("flagString = %q", got)
	}
	if got := flagString(LocalCommandRequest{Flags: map[string]any{"name": []string{""}}}, "name", "fallback"); got != "fallback" {
		t.Fatalf("flagString fallback = %q", got)
	}
	if got := flagStringList(LocalCommandRequest{Flags: map[string]any{"header": "A: b"}}, "header"); len(got) != 1 || got[0] != "A: b" {
		t.Fatalf("flagStringList = %#v", got)
	}
	if got := flagDuration(LocalCommandRequest{Flags: map[string]any{"timeout": "bad"}}, "timeout", time.Second); got != time.Second {
		t.Fatalf("bad duration fallback = %s", got)
	}
	if got := flagDuration(LocalCommandRequest{Flags: map[string]any{"timeout": "-1s"}}, "timeout", time.Second); got != time.Second {
		t.Fatalf("negative duration fallback = %s", got)
	}
	if screenshotFormat("x.JPG") != "jpeg" || screenshotFormat("x") != "png" {
		t.Fatalf("screenshot formats mismatch")
	}
	if _, err := parseClip("1,2,3"); err == nil {
		t.Fatal("short clip succeeded")
	}
	if _, err := parseClip("1,2,no,4"); err == nil {
		t.Fatal("bad clip succeeded")
	}
}

func TestDefaultForwardPreservesDaemonResult(t *testing.T) {
	server := daemonResultServer(t, daemon.Result{ExitCode: ExitNavigation, Stdout: "body", Stderr: "nav\n"})
	req := ForwardRequest{
		ServerURL: server,
		Token:     "tok",
		Endpoint:  "/v1/commands/get",
		Envelope:  daemon.Envelope{Args: []string{"https://example.com"}},
	}
	resp, err := defaultForward(context.Background(), req)
	if err != nil {
		t.Fatalf("forward err = %v", err)
	}
	if resp.ExitCode != ExitNavigation || resp.Stdout != "body" || resp.Stderr != "nav\n" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestDefaultServeDaemonExecutesForwardedGet(t *testing.T) {
	page := &fakeLocalPage{
		html:     "<html><body><main>Hello daemon</main></body></html>",
		bodyText: "Hello daemon",
		title:    "Daemon",
		url:      "http://93.184.216.34/final",
	}
	restore := fakeOpenPage(t, page)
	defer restore()

	handler, err := newDefaultServeDaemon(ServeRequest{AuthToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp, err := defaultForward(context.Background(), ForwardRequest{
		ServerURL: server.URL,
		Token:     "tok",
		Endpoint:  "/v1/commands/get",
		Envelope: daemon.Envelope{
			Args:  []string{"http://93.184.216.34"},
			Flags: map[string]any{"text": true, "timeout": "1s"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != ExitOK || !strings.Contains(resp.Stdout, "Hello daemon") || resp.Stderr != "" {
		t.Fatalf("response = %#v", resp)
	}
	if len(page.gotoURLs) != 1 || page.gotoURLs[0] != "http://93.184.216.34" || page.closed != true {
		t.Fatalf("page = %#v", page)
	}
}

func TestDefaultServeForwardsRunnerGetOverHTTP(t *testing.T) {
	page := &fakeLocalPage{
		html:     "<html><body><p>served through runner</p></body></html>",
		bodyText: "served through runner",
		title:    "Served",
		url:      "http://93.184.216.34/served",
	}
	restore := fakeOpenPage(t, page)
	defer restore()

	port := freeTCPPort(t)
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- defaultServe(ctx, ServeRequest{Bind: "127.0.0.1", Port: port, AuthToken: "tok"})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("serve shutdown err = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("serve did not shut down")
		}
	})
	waitForDaemonHealth(t, base, "tok")

	var stdout, stderr bytes.Buffer
	code := Runner{}.Run(context.Background(), []string{
		"--server", base,
		"--server-token", "tok",
		"get", "http://93.184.216.34",
		"--text",
		"--timeout", "1s",
	}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "served through runner") || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if len(page.gotoURLs) != 1 || page.gotoURLs[0] != "http://93.184.216.34" {
		t.Fatalf("page = %#v", page)
	}
}

func TestDefaultServeDaemonRejectsGuardrailBypassBeforeBrowserLaunch(t *testing.T) {
	page := &fakeLocalPage{}
	restore := fakeOpenPage(t, page)
	defer restore()

	handler, err := newDefaultServeDaemon(ServeRequest{AuthToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp, err := defaultForward(context.Background(), ForwardRequest{
		ServerURL: server.URL,
		Token:     "tok",
		Endpoint:  "/v1/commands/get",
		Envelope: daemon.Envelope{
			Args:  []string{"http://127.0.0.1/"},
			Flags: map[string]any{"text": true, "allow_private_ips": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != ExitUsage || !strings.Contains(resp.Stderr, "unknown flag: --allow-private-ips") {
		t.Fatalf("response = %#v", resp)
	}
	if len(page.gotoURLs) != 0 {
		t.Fatalf("browser launched for blocked URL: %#v", page.gotoURLs)
	}

	resp, err = defaultForward(context.Background(), ForwardRequest{
		ServerURL: server.URL,
		Token:     "tok",
		Endpoint:  "/v1/commands/get",
		Envelope:  daemon.Envelope{Args: []string{"http://127.0.0.1/"}, Flags: map[string]any{"text": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != ExitURLBlocked || !strings.Contains(resp.Stderr, "blocked") {
		t.Fatalf("private URL response = %#v", resp)
	}
	if len(page.gotoURLs) != 0 {
		t.Fatalf("browser launched for blocked URL: %#v", page.gotoURLs)
	}
}

func TestDefaultServeDaemonRejectsUnknownEnvelopeFlags(t *testing.T) {
	handler, err := newDefaultServeDaemon(ServeRequest{AuthToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp, err := defaultForward(context.Background(), ForwardRequest{
		ServerURL: server.URL,
		Token:     "tok",
		Endpoint:  "/v1/commands/get",
		Envelope: daemon.Envelope{
			Args:  []string{"http://93.184.216.34"},
			Flags: map[string]any{"not_a_real_flag": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != ExitUsage || !strings.Contains(resp.Stderr, "unknown flag: --not-a-real-flag") {
		t.Fatalf("response = %#v", resp)
	}
}

func TestDefaultServeDaemonNormalizesForwardedRepeatedFlags(t *testing.T) {
	page := &fakeLocalPage{
		url:         "http://93.184.216.34/final",
		fetchStatus: 202,
		fetchBody:   []byte("accepted"),
	}
	restore := fakeOpenPage(t, page)
	defer restore()

	handler, err := newDefaultServeDaemon(ServeRequest{AuthToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp, err := defaultForward(context.Background(), ForwardRequest{
		ServerURL: server.URL,
		Token:     "tok",
		Endpoint:  "/v1/commands/fetch",
		Envelope: daemon.Envelope{
			Args: []string{"http://93.184.216.34/api"},
			Flags: map[string]any{
				"header":         []string{"X-Test: yes"},
				"navigate_first": "http://93.184.216.34/app",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != ExitOK || resp.Stdout != "accepted" {
		t.Fatalf("response = %#v", resp)
	}
	if page.fetchHeaders["X-Test"] != "yes" {
		t.Fatalf("headers = %#v", page.fetchHeaders)
	}
	if len(page.gotoURLs) != 1 || page.gotoURLs[0] != "http://93.184.216.34/app" {
		t.Fatalf("goto urls = %#v", page.gotoURLs)
	}
}

func TestDefaultServeDaemonEvalGateProducesAgentReadableError(t *testing.T) {
	handler, err := newDefaultServeDaemon(ServeRequest{AuthToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp, err := defaultForward(context.Background(), ForwardRequest{
		ServerURL: server.URL,
		Token:     "tok",
		Endpoint:  "/v1/commands/eval",
		Envelope: daemon.Envelope{
			Args:  []string{"http://93.184.216.34"},
			Flags: map[string]any{"script": "1+1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != ExitRuntime || resp.Stderr != "eval_disabled\n" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestDaemonEnvelopeValidationEdges(t *testing.T) {
	if err := newDaemonUsageError(nil); err != nil {
		t.Fatalf("nil usage error = %v", err)
	}
	if got := (daemonUsageError{}).Error(); got != errDaemonUsage.Error() {
		t.Fatalf("empty daemon usage error = %q", got)
	}

	for _, tc := range []struct {
		name    string
		command string
		flags   map[string]any
		want    string
	}{
		{"unsupported command", "open", nil, "unsupported daemon command: open"},
		{"bool type", "get", map[string]any{"text": "true"}, "--text expects a boolean value"},
		{"value type", "get", map[string]any{"max_bytes": 12}, "--max-bytes expects a string value"},
		{"nonrepeat string list", "get", map[string]any{"max_bytes": []string{"1", "2"}}, "--max-bytes may only be provided once"},
		{"nonrepeat any list", "get", map[string]any{"max_bytes": []any{"1"}}, "--max-bytes may only be provided once"},
		{"repeat bad item", "fetch", map[string]any{"header": []any{"A: b", 1}}, "--header expects string values"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeDaemonFlags(tc.command, tc.flags)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v want %q", err, tc.want)
			}
		})
	}

	flags, err := normalizeDaemonFlags("fetch", map[string]any{"header": []any{"A: b"}, "headless": false})
	if err != nil {
		t.Fatal(err)
	}
	if got := flags["header"].([]string); len(got) != 1 || got[0] != "A: b" || flags["headless"] != false {
		t.Fatalf("normalized flags = %#v", flags)
	}
	flags, err = normalizeDaemonFlags("fetch", map[string]any{"header": []string{"B: c"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := flags["header"].([]string); len(got) != 1 || got[0] != "B: c" {
		t.Fatalf("string list flags = %#v", flags)
	}
	for _, command := range []string{"get", "screenshot", "fetch", "eval", "session import", "session export"} {
		if _, ok := daemonFlagSpecs(command); !ok {
			t.Fatalf("missing daemon flag specs for %s", command)
		}
	}

	req, err := localRequestFromDaemonEnvelope("get", daemon.Envelope{
		Args:    []string{"http://93.184.216.34"},
		Flags:   map[string]any{"text": true, "timeout": "1s"},
		Profile: "p",
		JSON:    true,
	})
	if err != nil || req.Command != "get" || !req.JSON || req.Profile != "p" || req.Flags["text"] != true {
		t.Fatalf("local req=%#v err=%v", req, err)
	}

	for _, tc := range []struct {
		name string
		req  LocalCommandRequest
		want string
	}{
		{"bad get syntax", LocalCommandRequest{Command: "get"}, "usage: gomoufox get"},
		{"bad screenshot syntax", LocalCommandRequest{Command: "screenshot"}, "usage: gomoufox screenshot"},
		{"bad fetch syntax", LocalCommandRequest{Command: "fetch"}, "usage: gomoufox fetch"},
		{"bad eval syntax", LocalCommandRequest{Command: "eval"}, "usage: gomoufox eval"},
		{"bad session import syntax", LocalCommandRequest{Command: "session import"}, "usage: gomoufox session import"},
		{"bad session export syntax", LocalCommandRequest{Command: "session export", Flags: map[string]any{"bogus": "x"}}, "unknown flag"},
		{"unsupported syntax", LocalCommandRequest{Command: "open"}, "unsupported daemon command: open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDaemonCommandSyntax(tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v want %q", err, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		req  LocalCommandRequest
		want string
	}{
		{"bad timeout", LocalCommandRequest{Flags: map[string]any{"timeout": "bad"}}, "invalid duration"},
		{"bad os", LocalCommandRequest{Flags: map[string]any{"os": "plan9"}}, "--os must be windows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDaemonGlobalFlags(tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v want %q", err, tc.want)
			}
		})
	}

	if err := validateDaemonLocalCommand(context.Background(), ServeRequest{}, LocalCommandRequest{Command: "session export", Flags: map[string]any{"from_profile": "p", "out": "state.json"}}); err != nil {
		t.Fatalf("session export validation = %v", err)
	}
	if err := validateDaemonLocalCommand(context.Background(), ServeRequest{}, LocalCommandRequest{Command: "get"}); err == nil || !errors.Is(err, errDaemonUsage) {
		t.Fatalf("syntax validation err = %v", err)
	}
	if err := validateDaemonLocalCommand(context.Background(), ServeRequest{}, LocalCommandRequest{Command: "get", Args: []string{"http://93.184.216.34"}, Flags: map[string]any{"timeout": "bad"}}); err == nil || !errors.Is(err, errDaemonUsage) {
		t.Fatalf("global validation err = %v", err)
	}
	if err := validateDaemonLocalCommand(context.Background(), ServeRequest{}, LocalCommandRequest{Command: "get", Args: []string{"http://93.184.216.34"}, Flags: map[string]any{"proxy": "://bad"}}); err == nil || !strings.Contains(err.Error(), "invalid --proxy") {
		t.Fatalf("proxy validation err = %v", err)
	}
	if err := validateDaemonLocalCommand(context.Background(), ServeRequest{}, LocalCommandRequest{Command: "get", Args: []string{"http://127.0.0.1"}}); err == nil {
		t.Fatal("private URL daemon validation succeeded")
	}
	if err := validateDaemonLocalCommand(context.Background(), ServeRequest{AllowedHosts: []string{"93.184.216.34"}}, LocalCommandRequest{Command: "fetch", Args: []string{"http://93.184.216.34"}, Flags: map[string]any{"navigate_first": ""}}); err != nil {
		t.Fatalf("empty navigate_first validation = %v", err)
	}
	if got := daemonURLsForValidation(LocalCommandRequest{}); got != nil {
		t.Fatalf("empty daemon URLs = %#v", got)
	}
	if got := daemonURLsForValidation(LocalCommandRequest{Command: "fetch", Args: []string{"http://a"}, Flags: map[string]any{"navigate_first": "http://b"}}); len(got) != 2 || got[0] != "http://a" || got[1] != "http://b" {
		t.Fatalf("fetch daemon URLs = %#v", got)
	}
	if got := daemonURLsForValidation(LocalCommandRequest{Command: "session export", Args: []string{"x"}}); got != nil {
		t.Fatalf("session daemon URLs = %#v", got)
	}
	args := daemonCommandArgs(LocalCommandRequest{Args: []string{"http://a"}, Flags: map[string]any{"text": false, "header": []string{"A: b"}, "timeout": "1s", "ignored": 7}})
	if !containsString(args, "--text=false") || !containsString(args, "--header") || !containsString(args, "A: b") || containsString(args, "--timeout") || containsString(args, "--ignored") {
		t.Fatalf("daemon args = %#v", args)
	}

	usage := executeDaemonLocalCommand(context.Background(), ServeRequest{}, "open", daemon.Envelope{})
	if usage.ExitCode != ExitUsage || !strings.Contains(usage.Stderr, "unsupported daemon command") {
		t.Fatalf("usage result = %#v", usage)
	}
	blocked := executeDaemonLocalCommand(context.Background(), ServeRequest{}, "get", daemon.Envelope{Args: []string{"http://127.0.0.1"}})
	if blocked.ExitCode != ExitURLBlocked {
		t.Fatalf("blocked result = %#v", blocked)
	}
	restore := fakeOpenPageError(t, errors.New("open failed"))
	runtimeResult := executeDaemonLocalCommand(context.Background(), ServeRequest{}, "get", daemon.Envelope{Args: []string{"http://93.184.216.34"}})
	restore()
	if runtimeResult.ExitCode != ExitRuntime || !strings.Contains(runtimeResult.Stderr, "open failed") {
		t.Fatalf("runtime result = %#v", runtimeResult)
	}

	errorPayload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	t.Cleanup(errorPayload.Close)
	resp, err := defaultForward(context.Background(), ForwardRequest{ServerURL: errorPayload.URL, Endpoint: "/x", Token: "tok"})
	if err != nil || resp.ExitCode != ExitRuntime || resp.Stderr != "bad request\n" {
		t.Fatalf("error payload resp=%#v err=%v", resp, err)
	}

	shortBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(shortBody.Close)
	if _, err := defaultForward(context.Background(), ForwardRequest{ServerURL: shortBody.URL, Endpoint: "/x", Token: "tok"}); err == nil {
		t.Fatal("short forward body succeeded")
	}
}

func TestExecuteDaemonLocalCommandCarriesAllowlistsToBrowserLaunch(t *testing.T) {
	page := &fakeGomoufoxPage{
		content:  "<html><body>ok</body></html>",
		bodyText: "ok",
		title:    "OK",
		url:      "http://93.184.216.34",
	}
	browser := &fakeGomoufoxBrowser{page: page}
	launchOptions := 0
	restore := replaceNewBrowserForLocal(t, func(_ context.Context, opts ...gomoufox.Option) (gomoufoxBrowserForLocal, error) {
		launchOptions = len(opts)
		return browser, nil
	})
	defer restore()

	result := executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{
			AllowedOrigins: []string{"http://93.184.216.34"},
			AllowedHosts:   []string{"93.184.216.34"},
		},
		"get",
		daemon.Envelope{Args: []string{"http://93.184.216.34"}},
	)
	if result.ExitCode != ExitOK {
		t.Fatalf("daemon result = %#v", result)
	}
	if launchOptions != 2 {
		t.Fatalf("browser launch option count = %d, want allow-origin and allow-host options", launchOptions)
	}
}

func TestExecuteDaemonLocalCommandJailsSessionImportReadPath(t *testing.T) {
	jailRoot := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside-state.json")
	if err := os.WriteFile(outside, []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(jailRoot, "normalized.json")

	result := executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{SessionDir: jailRoot},
		"session import",
		daemon.Envelope{Flags: map[string]any{"file": outside, "out": out}},
	)
	assertDaemonPathRejected(t, result, jailRoot)
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("daemon import wrote output despite jailed read failure: %v", err)
	}
}

func TestExecuteDaemonLocalCommandJailsSessionImportWritePath(t *testing.T) {
	jailRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(jailRoot, "state.json"), []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-normalized.json")

	result := executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{SessionDir: jailRoot},
		"session import",
		daemon.Envelope{Flags: map[string]any{"file": "state.json", "out": outside}},
	)
	assertDaemonPathRejected(t, result, jailRoot)
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("daemon import wrote outside output: %v", err)
	}
}

func TestExecuteDaemonLocalCommandJailsSessionExportPaths(t *testing.T) {
	jailRoot := t.TempDir()
	outsideOut := filepath.Join(t.TempDir(), "outside-state.json")
	browser := &fakeLocalBrowser{state: &gomoufox.StorageState{}}
	restore := fakeOpenBrowser(t, browser)
	defer restore()

	result := executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{SessionDir: jailRoot},
		"session export",
		daemon.Envelope{Profile: "site", Flags: map[string]any{"out": "state.json"}},
	)
	if result.ExitCode != ExitUsage || !strings.Contains(result.Stderr, "session_export_disabled") {
		t.Fatalf("disabled daemon export result = %#v", result)
	}
	if browser.path != "" || browser.closed {
		t.Fatalf("disabled daemon export opened browser: %#v", browser)
	}

	result = executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{AllowSessionExport: true, SessionDir: jailRoot},
		"session export",
		daemon.Envelope{Profile: "site", Flags: map[string]any{"out": outsideOut}},
	)
	assertDaemonPathRejected(t, result, jailRoot)
	if browser.path != "" || browser.closed {
		t.Fatalf("daemon export opened browser despite jailed output failure: %#v", browser)
	}

	result = executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{AllowSessionExport: true, SessionDir: jailRoot},
		"session export",
		daemon.Envelope{Profile: filepath.Join(t.TempDir(), "outside-profile"), Flags: map[string]any{"out": "state.json"}},
	)
	assertDaemonPathRejected(t, result, jailRoot)
	if browser.path != "" || browser.closed {
		t.Fatalf("daemon export opened browser despite jailed profile failure: %#v", browser)
	}
}

func TestExecuteDaemonLocalCommandRejectsSessionSymlinkTraversal(t *testing.T) {
	jailRoot := t.TempDir()
	outsideDir := t.TempDir()
	linkDir := filepath.Join(jailRoot, "link")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(jailRoot, "state.json"), []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result := executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{SessionDir: jailRoot},
		"session import",
		daemon.Envelope{Flags: map[string]any{"file": "state.json", "out": filepath.Join("link", "out.json")}},
	)
	assertDaemonPathRejected(t, result, jailRoot)
	if _, err := os.Stat(filepath.Join(outsideDir, "out.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("daemon import wrote through symlink traversal: %v", err)
	}
}

func isolateUserConfigDir(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	config := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("home", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("AppData", config)
}

func TestExecuteDaemonLocalCommandUsesDefaultSessionJail(t *testing.T) {
	isolateUserConfigDir(t)
	jailRoot := defaultServeSessionDir()
	if err := os.MkdirAll(jailRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jailRoot, "state.json"), []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result := executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{},
		"session import",
		daemon.Envelope{Flags: map[string]any{"file": "state.json", "out": "normalized.json"}},
	)
	if result.ExitCode != ExitOK {
		t.Fatalf("default jail import result = %#v", result)
	}
	if !strings.Contains(result.Stdout, "normalized.json") {
		t.Fatalf("default jail import stdout = %q", result.Stdout)
	}
	assertNoDaemonHostPath(t, result.Stdout, jailRoot)
	if _, err := os.Stat(filepath.Join(jailRoot, "normalized.json")); err != nil {
		t.Fatalf("default jail output missing: %v", err)
	}

	result = executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{SessionDir: jailRoot},
		"session import",
		daemon.Envelope{JSON: true, Flags: map[string]any{"file": "state.json", "out": "json-normalized.json"}},
	)
	assertDaemonJSONPath(t, result, "json-normalized.json", jailRoot)
}

func TestExecuteDaemonLocalCommandSessionJailSetupErrors(t *testing.T) {
	badRoot := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(badRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{SessionDir: badRoot},
		"session import",
		daemon.Envelope{Flags: map[string]any{"file": "state.json", "out": "normalized.json"}},
	)
	assertDaemonPathRejected(t, result, badRoot)

	jailRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(jailRoot, "profiles"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{AllowSessionExport: true, SessionDir: jailRoot},
		"session export",
		daemon.Envelope{Profile: "site", Flags: map[string]any{"out": "state.json"}},
	)
	assertDaemonPathRejected(t, result, jailRoot)
}

func TestExecuteDaemonLocalCommandJailsSessionExportFromProfileFlag(t *testing.T) {
	jailRoot := t.TempDir()
	browser := &fakeLocalBrowser{state: &gomoufox.StorageState{}}
	restore := fakeOpenBrowser(t, browser)
	defer restore()

	result := executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{AllowSessionExport: true, SessionDir: jailRoot},
		"session export",
		daemon.Envelope{Flags: map[string]any{"from_profile": "site", "out": "state.json"}},
	)
	if result.ExitCode != ExitOK {
		t.Fatalf("from-profile export result = %#v", result)
	}
	if strings.TrimSpace(result.Stdout) != "state.json" {
		t.Fatalf("from-profile export stdout = %q", result.Stdout)
	}
	assertNoDaemonHostPath(t, result.Stdout, jailRoot)
	if browser.path != "" || !browser.closed {
		t.Fatalf("from-profile export browser = %#v", browser)
	}
	if _, err := os.Stat(filepath.Join(jailRoot, "state.json")); err != nil {
		t.Fatalf("from-profile export output missing: %v", err)
	}
}

func TestExecuteDaemonLocalCommandJailsSessionExportEnvelopeProfile(t *testing.T) {
	jailRoot := t.TempDir()
	browser := &fakeLocalBrowser{state: &gomoufox.StorageState{}}
	restore := fakeOpenBrowser(t, browser)
	defer restore()

	result := executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{AllowSessionExport: true, SessionDir: jailRoot},
		"session export",
		daemon.Envelope{Profile: "site", Flags: map[string]any{"out": "state.json"}},
	)
	if result.ExitCode != ExitOK {
		t.Fatalf("envelope profile export result = %#v", result)
	}
	if strings.TrimSpace(result.Stdout) != "state.json" {
		t.Fatalf("envelope profile export stdout = %q", result.Stdout)
	}
	assertNoDaemonHostPath(t, result.Stdout, jailRoot)
	if _, err := os.Stat(filepath.Join(jailRoot, "state.json")); err != nil {
		t.Fatalf("envelope profile export output missing: %v", err)
	}

	result = executeDaemonLocalCommand(
		context.Background(),
		ServeRequest{AllowSessionExport: true, SessionDir: jailRoot},
		"session export",
		daemon.Envelope{JSON: true, Profile: "site-json", Flags: map[string]any{"out": "state-json.json"}},
	)
	assertDaemonJSONPath(t, result, "state-json.json", jailRoot)
}

func TestLocalSessionImportExportCapStorageStateFiles(t *testing.T) {
	src := filepath.Join(t.TempDir(), "large-state.json")
	largeState := `{"cookies":[],"origins":[],"pad":"` + strings.Repeat("x", policy.InlineSessionLoadBytes) + `"}`
	if err := os.WriteFile(src, []byte(largeState), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session import",
		Flags:   map[string]any{"file": src, "out": filepath.Join(t.TempDir(), "out.json")},
	}); err == nil || !strings.Contains(err.Error(), "file exceeds") {
		t.Fatalf("large session import err = %v", err)
	}

	cookieValue := strings.Repeat("x", policy.InlineSessionLoadBytes)
	browser := &fakeLocalBrowser{state: &gomoufox.StorageState{Cookies: []gomoufox.Cookie{{Name: "large", Value: cookieValue}}}}
	restore := fakeOpenBrowser(t, browser)
	defer restore()
	if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session export",
		Profile: "/profile",
		Flags:   map[string]any{"out": filepath.Join(t.TempDir(), "state.json")},
	}); err == nil || !strings.Contains(err.Error(), "state exceeds") {
		t.Fatalf("large session export err = %v", err)
	}
}

func TestLocalSessionExportWritesNilStateSafely(t *testing.T) {
	browser := &fakeLocalBrowser{}
	restore := fakeOpenBrowser(t, browser)
	defer restore()
	out := filepath.Join(t.TempDir(), "state.json")

	resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session export",
		Profile: "/profile",
		Flags:   map[string]any{"out": out},
		JSON:    true,
	})
	if err != nil || !bytes.Contains(resp.Stdout, []byte(`"cookies":0`)) {
		t.Fatalf("nil state export resp=%s err=%v", resp.Stdout, err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("nil state export output missing: %v", err)
	}
}

func TestWriteFile0600RejectsFinalSymlink(t *testing.T) {
	link, target := symlinkedOutput(t)
	if err := writeFile0600(link, []byte("new"), true); err == nil {
		t.Fatal("write through symlink succeeded")
	}
	assertFileContent(t, target, "original")
	assertSymlink(t, link)
}

func TestCLIFileWritersRejectFinalSymlink(t *testing.T) {
	page := &fakeLocalPage{
		url:        "https://example.com/final",
		screenshot: []byte("png"),
		state:      &gomoufox.StorageState{},
	}
	restore := fakeOpenPage(t, page)
	defer restore()

	sessionSrc := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(sessionSrc, []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func(string) error
	}{
		{
			name: "screenshot",
			run: func(out string) error {
				_, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
					Command: "screenshot",
					Args:    []string{"https://example.com"},
					Flags:   map[string]any{"out": out},
				})
				return err
			},
		},
		{
			name: "session import",
			run: func(out string) error {
				_, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
					Command: "session import",
					Flags:   map[string]any{"file": sessionSrc, "out": out, "overwrite": true},
				})
				return err
			},
		},
		{
			name: "open save session",
			run: func(out string) error {
				_, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
					Command: "open",
					Args:    []string{"https://example.com"},
					Flags:   map[string]any{"save_session": out},
				})
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			link, target := symlinkedOutput(t)
			if err := tc.run(link); err == nil {
				t.Fatal("write through symlink succeeded")
			}
			assertFileContent(t, target, "original")
			assertSymlink(t, link)
		})
	}
}

func TestParserAndValidationEdges(t *testing.T) {
	for kind, want := range map[commandKind]string{
		commandOpen:       "open",
		commandGet:        "get",
		commandScreenshot: "screenshot",
		commandEval:       "eval",
		commandFetch:      "fetch",
	} {
		if got := commandName(kind); got != want {
			t.Fatalf("commandName(%d) = %q want %q", kind, got, want)
		}
	}
	if got := commandName(commandKind(99)); got != "" {
		t.Fatalf("unknown command name = %q", got)
	}
	if _, err := parseBrowserCommand(nil, commandKind(99)); err == nil {
		t.Fatal("unsupported browser command parsed")
	}
	for _, raw := range []string{"bad", "0", strconv.Itoa(policy.HardMaxResponseBytes + 1)} {
		if err := validateMaxBytes(raw, policy.HardMaxResponseBytes, "--max-bytes"); err == nil {
			t.Fatalf("validateMaxBytes(%q) succeeded", raw)
		}
	}
	if err := validateMaxBytes("", policy.HardMaxResponseBytes, "--max-bytes"); err != nil {
		t.Fatalf("empty max bytes err = %v", err)
	}
	if _, err := parseOpen([]string{"http://93.184.216.34", "--wait"}); err != nil {
		t.Fatalf("parseOpen wait err = %v", err)
	}
	for _, args := range [][]string{
		{},
		{"http://93.184.216.34", "--html", "--text"},
		{"http://93.184.216.34", "--wait-load-state", "idle"},
		{"http://93.184.216.34", "--max-bytes", "0"},
	} {
		if _, err := parseGet(args); err == nil {
			t.Fatalf("parseGet(%v) succeeded", args)
		}
	}
	if parsed, err := parseGet([]string{"http://93.184.216.34", "--markdown", "--max-bytes", "1024"}); err != nil || !parsed.bool("markdown") {
		t.Fatalf("parseGet success parsed=%#v err=%v", parsed, err)
	}
	for _, args := range [][]string{
		{},
		{"export", "--out", "state.json", "extra"},
		{"import", "--file", "state.json"},
		{"remove"},
	} {
		if _, err := parseSession(args); err == nil {
			t.Fatalf("parseSession(%v) succeeded", args)
		}
	}
	for _, args := range [][]string{
		{},
		{"http://93.184.216.34", "--wait-load-state", "idle"},
		{"http://93.184.216.34", "--width", "0"},
		{"http://93.184.216.34", "--height", "100001"},
		{"http://93.184.216.34", "--quality", "101"},
		{"http://93.184.216.34", "--max-bytes", "bad"},
	} {
		if _, err := parseScreenshot(args); err == nil {
			t.Fatalf("parseScreenshot(%v) succeeded", args)
		}
	}
	if parsed, err := parseEvalFlags([]string{"http://93.184.216.34", "--enable-eval=false"}); err != nil || parsed.bool("enable-eval") {
		t.Fatalf("parseEvalFlags parsed=%#v err=%v", parsed, err)
	}
	for _, args := range [][]string{
		{"http://93.184.216.34", "--script", "1", "--script-file", "x.js"},
		{"http://93.184.216.34", "--script", strings.Repeat("x", 64*1024+1)},
		{"http://93.184.216.34", "--script", "1", "--wait-load-state", "idle"},
	} {
		if _, err := parseEval(args); err == nil {
			t.Fatalf("parseEval(%v) succeeded", args)
		}
	}
	if parsed, err := parseFetch([]string{
		"http://93.184.216.34",
		"--method", "put",
		"--header", "A: b",
		"--header", "C: d",
		"--data", "{}",
	}); err != nil || strings.ToUpper(parsed.value("method")) != "PUT" || len(parsed.valueList("header")) != 2 {
		t.Fatalf("parseFetch parsed=%#v err=%v", parsed, err)
	}
	if _, err := parseFetch([]string{"http://93.184.216.34", "--data", "x", "--data-file", "body.txt"}); err == nil {
		t.Fatal("data/data-file mutex succeeded")
	}
	if _, err := parseFetch([]string{"http://93.184.216.34", "--data", strings.Repeat("x", policy.HardMaxInputBytes+1)}); err == nil {
		t.Fatal("oversized fetch data succeeded")
	}
	if parsed, err := parseFetch([]string{"http://93.184.216.34", "--method", "head"}); err != nil || strings.ToUpper(parsed.value("method")) != "HEAD" {
		t.Fatalf("parseFetch HEAD parsed=%#v err=%v", parsed, err)
	}
	if _, err := parseFetch([]string{"http://93.184.216.34", "--method", "TRACE"}); err == nil {
		t.Fatal("unsupported fetch method succeeded")
	}
	if _, err := parseFetch([]string{"http://93.184.216.34", "--header", "bad"}); err == nil {
		t.Fatal("bad fetch header succeeded")
	}
	parsed, err := parseFlags([]string{"--header", "A: b", "--header=C: d", "--raw=false"}, map[string]flagSpec{
		"header": {Kind: flagValue, Repeat: true},
		"raw":    {Kind: flagBool},
	})
	if err != nil {
		t.Fatal(err)
	}
	flags := parsed.flagMap()
	if flags["raw"] != false {
		t.Fatalf("raw flag = %#v", flags)
	}
	headers, ok := flags["header"].([]string)
	if !ok || len(headers) != 2 {
		t.Fatalf("header flags = %#v", flags)
	}
	if _, err := boolFlagValue("headless", "maybe", true); err == nil {
		t.Fatal("invalid bool flag value succeeded")
	}
}

func TestForwardRequestAndDefaultForwardEdges(t *testing.T) {
	global := globalFlags{
		Server:      "http://127.0.0.1:3741/base",
		ServerToken: "tok",
		Profile:     "/profile",
		TimeoutRaw:  "7s",
		Proxy:       "http://proxy.example",
		Locale:      "en-US",
		OS:          "linux",
		Headful:     true,
		HeadlessSet: true,
		Headless:    false,
		JSON:        true,
	}
	req, err := buildForwardRequest(global, "screenshot", []string{
		"http://93.184.216.34",
		"--width", "10",
		"--height", "10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Endpoint != "/v1/commands/screenshot" || req.Verb != "screenshot" || req.Envelope.Profile != "/profile" || !req.Envelope.JSON {
		t.Fatalf("request = %#v", req)
	}
	for _, name := range []string{"timeout", "proxy", "locale", "os", "headful", "headless"} {
		if _, ok := req.Envelope.Flags[name]; !ok {
			t.Fatalf("missing forwarded global flag %q in %#v", name, req.Envelope.Flags)
		}
	}
	req, err = buildForwardRequest(globalFlags{Server: "http://127.0.0.1:3741", ServerToken: "tok"}, "session", []string{
		"import", "--file", "state.json", "--out", "normalized.json", "--overwrite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Endpoint != "/v1/session/import" || req.Verb != "session import" || req.Envelope.Flags["overwrite"] != true {
		t.Fatalf("session request = %#v", req)
	}
	for _, tc := range []struct {
		command string
		args    []string
		global  globalFlags
	}{
		{"get", []string{"http://93.184.216.34"}, globalFlags{AllowSchemes: "file"}},
		{"eval", []string{"http://93.184.216.34", "--script", "1"}, globalFlags{}},
		{"doctor", nil, globalFlags{}},
	} {
		if _, err := buildForwardRequest(tc.global, tc.command, tc.args); err == nil {
			t.Fatalf("buildForwardRequest(%s, %v) succeeded", tc.command, tc.args)
		}
	}

	var stderr bytes.Buffer
	runner := Runner{Hooks: Hooks{Forward: func(context.Context, ForwardRequest) (ForwardResponse, error) {
		return ForwardResponse{}, gomoufox.ErrNavigationTimeout
	}}}
	code := runner.Run(context.Background(), []string{"--server", "http://127.0.0.1:3741", "--server-token", "tok", "get", "http://93.184.216.34"}, Streams{Stderr: &stderr})
	if code != ExitTimeout {
		t.Fatalf("forward error code=%d stderr=%q", code, stderr.String())
	}

	if _, err := defaultForward(context.Background(), ForwardRequest{ServerURL: "http://[::1"}); err == nil {
		t.Fatal("invalid forward server URL succeeded")
	}
	if _, err := defaultForward(context.Background(), ForwardRequest{
		ServerURL: "http://127.0.0.1",
		Envelope:  daemon.Envelope{Flags: map[string]any{"bad": make(chan int)}},
	}); err == nil {
		t.Fatal("unmarshalable forward envelope succeeded")
	}
	badJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(badJSON.Close)
	if _, err := defaultForward(context.Background(), ForwardRequest{ServerURL: badJSON.URL, Endpoint: "/x", Token: "tok"}); err == nil {
		t.Fatal("bad forward response JSON succeeded")
	}
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(daemon.Result{})
	}))
	t.Cleanup(unauthorized.Close)
	resp, err := defaultForward(context.Background(), ForwardRequest{ServerURL: unauthorized.URL, Endpoint: "/x", Token: "tok"})
	if err != nil || resp.ExitCode != ExitSessionAuth || resp.Stderr != "unauthorized\n" {
		t.Fatalf("unauthorized resp=%#v err=%v", resp, err)
	}
	serverError := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(daemon.Result{})
	}))
	t.Cleanup(serverError.Close)
	resp, err = defaultForward(context.Background(), ForwardRequest{ServerURL: serverError.URL, Endpoint: "/x", Token: "tok"})
	if err != nil || resp.ExitCode != ExitRuntime {
		t.Fatalf("server error resp=%#v err=%v", resp, err)
	}
}

func TestRunnerGlobalAndInstallEdges(t *testing.T) {
	tests := []struct {
		args []string
		code int
		want string
	}{
		{nil, ExitUsage, "usage:"},
		{[]string{"--bad", "doctor"}, ExitUsage, "unknown global flag"},
		{[]string{"--profile"}, ExitUsage, "--profile requires a value"},
		{[]string{"--timeout", "0s", "doctor"}, ExitUsage, "duration"},
		{[]string{"--os", "plan9", "doctor"}, ExitUsage, "--os"},
		{[]string{"--headful", "--headless", "doctor"}, ExitUsage, "mutually exclusive"},
		{[]string{"install", "extra"}, ExitUsage, "usage: gomoufox install"},
		{[]string{"serve", "extra", "--auth-token", "tok"}, ExitUsage, "usage: gomoufox serve"},
		{[]string{"mcp", "extra"}, ExitUsage, "usage: gomoufox mcp"},
	}
	for _, tc := range tests {
		var stderr bytes.Buffer
		if code := (Runner{}).Run(context.Background(), tc.args, Streams{Stderr: &stderr}); code != tc.code || !strings.Contains(stderr.String(), tc.want) {
			t.Fatalf("%v code=%d stderr=%q", tc.args, code, stderr.String())
		}
	}
	if canForward("session", nil) || canForward("doctor", nil) || !canForward("session", []string{"import"}) {
		t.Fatalf("canForward session/default mismatch")
	}
	if !isLoopbackBind("localhost") || !isLoopbackBind("127.0.0.1:3741") || !isLoopbackBind("[::1]:3741") || isLoopbackBind("0.0.0.0") {
		t.Fatalf("loopback bind classification mismatch")
	}
	mismatch := false
	if msg := (Check{OK: true, PkgVersion: "1.57.0", DriverVersion: "1.58.0", Match: &mismatch}).message(); !strings.Contains(msg, "MISMATCH") {
		t.Fatalf("mismatch message = %q", msg)
	}
	if !(DoctorReport{Python: Check{OK: false}, Display: Check{Warning: "DISPLAY not set"}}).hasFailure() {
		t.Fatal("doctor failure was not detected")
	}
}

func TestRunnerRuntimeParserAdditionalEdges(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	runner := Runner{Hooks: Hooks{LocalCommand: func(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
		called = true
		if req.Command != "session import" || req.Flags["file"] != "state.json" || req.Flags["out"] != "out.json" {
			t.Fatalf("session request = %#v", req)
		}
		return LocalCommandResponse{ExitCode: ExitOK, Stdout: []byte("imported\n")}, nil
	}}}
	code := runner.Run(context.Background(), []string{"session", "import", "--file", "state.json", "--out", "out.json"}, Streams{Stdout: &stdout, Stderr: &stderr})
	if code != ExitOK || !called || stdout.String() != "imported\n" {
		t.Fatalf("session code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}

	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"serve", "--auth-token", "tok", "--enable-eval=maybe"}, ExitUsage},
		{[]string{"install", "--force=maybe"}, ExitUsage},
		{[]string{"doctor", "extra"}, ExitUsage},
		{[]string{"eval", "http://93.184.216.34", "--enable-eval=maybe"}, ExitUsage},
		{[]string{"mcp", "--enable-eval=maybe"}, ExitUsage},
		{[]string{"mcp", "--port", "bad"}, ExitUsage},
		{[]string{"mcp", "--max-input-bytes", "bad"}, ExitUsage},
		{[]string{"mcp", "--max-input-bytes", strconv.Itoa(policy.HardMaxInputBytes + 1)}, ExitUsage},
		{[]string{"mcp", "--max-response-bytes", "bad"}, ExitUsage},
		{[]string{"mcp", "--max-sessions", "bad"}, ExitUsage},
	} {
		stderr.Reset()
		if code := (Runner{}).Run(context.Background(), tc.args, Streams{Stderr: &stderr}); code != tc.code {
			t.Fatalf("%v code=%d want=%d stderr=%q", tc.args, code, tc.code, stderr.String())
		}
	}

	runner = Runner{Hooks: Hooks{Serve: func(context.Context, ServeRequest) error {
		return gomoufox.ErrSidecarDied
	}}}
	stderr.Reset()
	if code := runner.Run(context.Background(), []string{"serve", "--auth-token", "tok"}, Streams{Stderr: &stderr}); code != ExitUnavailable {
		t.Fatalf("serve hook error code=%d stderr=%q", code, stderr.String())
	}
	runner = Runner{Hooks: Hooks{MCP: func(context.Context, MCPRequest) error {
		return gomoufox.ErrSessionClosed
	}}}
	stderr.Reset()
	if code := runner.Run(context.Background(), []string{"mcp"}, Streams{Stderr: &stderr}); code != ExitSessionAuth {
		t.Fatalf("mcp hook error code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"mcp"}, Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("default mcp code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	runner = Runner{Hooks: Hooks{Doctor: func(context.Context, DoctorRequest) (DoctorReport, error) {
		return DoctorReport{Python: Check{OK: false, Error: "missing"}}, nil
	}}}
	if code := runner.Run(context.Background(), []string{"doctor"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitUnavailable {
		t.Fatalf("doctor failure report code=%d", code)
	}
	restoreDoctorEnsure := defaultDoctorEnsureInstalled
	defaultDoctorEnsureInstalled = func(context.Context) error { return nil }
	t.Cleanup(func() { defaultDoctorEnsureInstalled = restoreDoctorEnsure })
	stdout.Reset()
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"doctor"}, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("default doctor code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	stderr.Reset()
	if code := (Runner{}).Run(context.Background(), []string{"serve", "--auth-token", "tok", "--port", strconv.Itoa(port)}, Streams{Stderr: &stderr}); code != ExitRuntime {
		t.Fatalf("default serve occupied port code=%d stderr=%q", code, stderr.String())
	}
	if err := defaultServe(context.Background(), ServeRequest{Bind: "127.0.0.1", Port: -1, AuthToken: "tok"}); err == nil {
		t.Fatal("defaultServe invalid port succeeded")
	}
	if err := defaultServe(context.Background(), ServeRequest{Bind: "127.0.0.1", Port: 3741}); err == nil {
		t.Fatal("defaultServe empty token succeeded")
	}
	if err := defaultMCP(context.Background(), MCPRequest{Transport: "stdio"}); err == nil {
		t.Fatal("defaultMCP invalid config succeeded")
	}
	if err := defaultMCP(context.Background(), MCPRequest{Transport: "http", Port: -1, AuthToken: "tok", Config: mcpserver.Config{SessionDir: t.TempDir()}}); err == nil {
		t.Fatal("defaultMCP invalid port succeeded")
	}
	t.Setenv("HOME", "")
	t.Setenv("home", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	if dir := defaultMCPSessionDir(); !strings.Contains(dir, "gomoufox") {
		t.Fatalf("fallback MCP session dir = %q", dir)
	}

	if _, err := parseOpen([]string{"http://93.184.216.34", "--bogus"}); err == nil {
		t.Fatal("parseOpen unknown flag succeeded")
	}
	if _, err := parseOpen([]string{}); err == nil {
		t.Fatal("parseOpen missing URL succeeded")
	}
	if _, err := parseGet([]string{"http://93.184.216.34", "--bogus"}); err == nil {
		t.Fatal("parseGet unknown flag succeeded")
	}
	if _, err := parseScreenshot([]string{"http://93.184.216.34", "--bogus"}); err == nil {
		t.Fatal("parseScreenshot unknown flag succeeded")
	}
	if _, err := parseEvalFlags([]string{"http://93.184.216.34", "--bogus"}); err == nil {
		t.Fatal("parseEvalFlags unknown flag succeeded")
	}
	if _, err := parseEval([]string{"http://93.184.216.34", "--bogus"}); err == nil {
		t.Fatal("parseEval unknown flag succeeded")
	}
	if _, err := parseEval([]string{"--script", "1"}); err == nil {
		t.Fatal("parseEval missing URL succeeded")
	}
	if _, err := parseFetch([]string{"http://93.184.216.34", "--bogus"}); err == nil {
		t.Fatal("parseFetch unknown flag succeeded")
	}
	if _, err := parseFetch([]string{}); err == nil {
		t.Fatal("parseFetch missing URL succeeded")
	}
	if _, err := parseFetch([]string{"http://93.184.216.34", "--max-bytes", "bad"}); err == nil {
		t.Fatal("parseFetch bad max bytes succeeded")
	}
	if _, err := parseSession([]string{"export", "--bogus"}); err == nil {
		t.Fatal("parseSession export flag error succeeded")
	}
	if _, err := parseSession([]string{"import", "--bogus"}); err == nil {
		t.Fatal("parseSession import flag error succeeded")
	}
	if _, err := parseFlags([]string{"--name"}, map[string]flagSpec{"name": {Kind: flagValue}}); err == nil {
		t.Fatal("missing value flag succeeded")
	}
	if _, err := parseFlags([]string{"--name", "a", "--name", "b"}, map[string]flagSpec{"name": {Kind: flagValue}}); err == nil {
		t.Fatal("duplicate value flag succeeded")
	}
	if parsed, err := parseFlags([]string{"--humanize"}, map[string]flagSpec{"humanize": {Kind: flagOptionalValue}}); err != nil || parsed.value("humanize") != "true" {
		t.Fatalf("optional flag parsed=%#v err=%v", parsed, err)
	}
	if got, err := boolFlagValue("flag", "yes", true); err != nil || !got {
		t.Fatalf("yes bool got=%v err=%v", got, err)
	}

	if _, err := buildForwardRequest(globalFlags{}, "fetch", []string{}); err == nil {
		t.Fatal("forward fetch parse error succeeded")
	}
	if req, err := buildForwardRequest(globalFlags{}, "eval", []string{"http://93.184.216.34", "--enable-eval", "--script", "1"}); err != nil || req.Endpoint != "/v1/commands/eval" || req.Verb != "eval" {
		t.Fatalf("forward eval req=%#v err=%v", req, err)
	}
	if _, err := buildForwardRequest(globalFlags{}, "session", []string{"export"}); err == nil {
		t.Fatal("forward session parse error succeeded")
	}
	parsedGet, err := parseGet([]string{"http://93.184.216.34"})
	if err != nil {
		t.Fatal(err)
	}
	runner = Runner{Hooks: Hooks{LocalCommand: func(context.Context, LocalCommandRequest) (LocalCommandResponse, error) {
		return LocalCommandResponse{}, errors.New("local failed")
	}}}
	stderr.Reset()
	if code := runner.executeLocalCommand(context.Background(), globalFlags{}, "get", parsedGet, Streams{Stderr: &stderr}); code != ExitRuntime {
		t.Fatalf("local command error code=%d stderr=%q", code, stderr.String())
	}
	if code := (Runner{}).forward(context.Background(), globalFlags{AllowPrivateIPs: true}, "get", []string{"http://93.184.216.34"}, Streams{Stderr: &stderr}); code != ExitUsage {
		t.Fatalf("forward guardrail override code=%d", code)
	}
	stderr.Reset()
	if code := (Runner{}).forward(context.Background(), globalFlags{Server: "://bad", ServerToken: "tok"}, "get", []string{"http://93.184.216.34"}, Streams{Stderr: &stderr}); code != ExitRuntime {
		t.Fatalf("default forward parse code=%d stderr=%q", code, stderr.String())
	}
	if _, err := defaultForward(context.Background(), ForwardRequest{
		ServerURL: "http://127.0.0.1",
		Endpoint:  "/v1/commands/get",
		Envelope:  daemon.Envelope{Flags: map[string]any{"bad": math.NaN()}},
	}); err == nil {
		t.Fatal("forward marshal error succeeded")
	}
	if _, err := defaultForward(context.Background(), ForwardRequest{
		ServerURL: "http://127.0.0.1:1",
		Endpoint:  "/v1/commands/get",
		Envelope:  daemon.Envelope{},
	}); err == nil {
		t.Fatal("forward transport error succeeded")
	}
	requestErr := errors.New("request failed")
	oldForwardRequest := newForwardRequest
	newForwardRequest = func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, requestErr
	}
	t.Cleanup(func() { newForwardRequest = oldForwardRequest })
	if _, err := defaultForward(context.Background(), ForwardRequest{
		ServerURL: "http://127.0.0.1",
		Endpoint:  "/v1/commands/get",
		Envelope:  daemon.Envelope{},
	}); !errors.Is(err, requestErr) {
		t.Fatalf("forward request err = %v", err)
	}
	newForwardRequest = oldForwardRequest
	if code := validateDaemonForward(globalFlags{Server: "://bad", ServerToken: "tok"}, &stderr); code != ExitSessionAuth {
		t.Fatalf("invalid daemon forward code=%d", code)
	}
	if mapError(errors.New("plain")) != ExitRuntime {
		t.Fatal("plain error did not map to runtime")
	}
	if _, _, _, err := parseGlobal([]string{"--verbose=maybe", "doctor"}); err == nil {
		t.Fatal("invalid verbose global succeeded")
	}
	global, command, _, err := parseGlobal([]string{"--verbose", "--locale", "en-US", "--os", "linux", "doctor"})
	if err != nil || command != "doctor" || !global.Verbose || global.Locale != "en-US" || global.OS != "linux" {
		t.Fatalf("global parse=%#v command=%q err=%v", global, command, err)
	}

	runner = Runner{
		Hooks: Hooks{LocalCommand: func(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
			return LocalCommandResponse{ExitCode: ExitOK}, nil
		}},
		Resolver: cliTestResolver{"example.com": {"93.184.216.34"}},
	}
	if code := runner.Run(context.Background(), []string{"fetch", "http://example.com"}, Streams{Stderr: &stderr}); code != ExitOK {
		t.Fatalf("fetch validation with empty navigate-first code=%d stderr=%q", code, stderr.String())
	}
	if code := runner.Run(context.Background(), []string{"--allow-schemes", "ftp", "fetch", "ftp://example.com"}, Streams{Stderr: &stderr}); code != ExitOK {
		t.Fatalf("fetch validation with extra scheme code=%d stderr=%q", code, stderr.String())
	}
	if code := (Runner{}).Run(context.Background(), []string{"eval", "ftp://example.com", "--enable-eval", "--script", "1"}, Streams{Stderr: &stderr}); code != ExitURLBlocked {
		t.Fatalf("eval guardrail code=%d stderr=%q", code, stderr.String())
	}
	if code := (Runner{}).validateBrowserInputs(context.Background(), globalFlags{}, parsedFlags{}, commandGet, Streams{Stderr: &stderr}); code != ExitOK {
		t.Fatalf("empty validation urls code=%d stderr=%q", code, stderr.String())
	}
}

func TestDefaultEnsureInstalledHooksUseRequests(t *testing.T) {
	oldEnsure := ensureInstalledForCLI
	t.Cleanup(func() { ensureInstalledForCLI = oldEnsure })

	calls := 0
	ensureInstalledForCLI = func(ctx context.Context, opts ...func(*gomoufox.InstallOptions)) error {
		calls++
		var cfg gomoufox.InstallOptions
		for _, opt := range opts {
			opt(&cfg)
		}
		switch calls {
		case 1:
			if cfg.VenvDir != "/venv" || cfg.PythonBin != "/python" || !cfg.ForceReinstall || cfg.SkipBinaryFetch {
				t.Fatalf("install cfg = %#v", cfg)
			}
		case 2:
			if !cfg.SkipBinaryFetch || cfg.ForceReinstall || cfg.VenvDir != "" || cfg.PythonBin != "" {
				t.Fatalf("doctor cfg = %#v", cfg)
			}
		default:
			t.Fatalf("unexpected ensure call %d", calls)
		}
		return nil
	}
	if err := defaultInstallEnsureInstalled(context.Background(), InstallRequest{Dir: "/venv", Python: "/python", Force: true}); err != nil {
		t.Fatal(err)
	}
	if err := defaultDoctorEnsureInstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("ensure calls = %d", calls)
	}
}

func TestLocalHelperErrorEdges(t *testing.T) {
	if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "bad"}); err == nil {
		t.Fatal("unsupported local command succeeded")
	}
	if _, err := jsonResponse(make(chan int)); err == nil {
		t.Fatal("unmarshalable json response succeeded")
	}
	if script, err := scriptFromFlags(LocalCommandRequest{Flags: map[string]any{"script": "1+1"}}); err != nil || script != "1+1" {
		t.Fatalf("inline script=%q err=%v", script, err)
	}
	if _, err := scriptFromFlags(LocalCommandRequest{Flags: map[string]any{}}); err == nil {
		t.Fatal("missing script source succeeded")
	}
	if body, err := bodyFromFlags(LocalCommandRequest{Flags: map[string]any{}}); err != nil || body != nil {
		t.Fatalf("empty body=%q err=%v", body, err)
	}
	out := filepath.Join(t.TempDir(), "state.json")
	if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session export",
		Flags:   map[string]any{"out": out},
	}); err == nil {
		t.Fatal("session export without profile succeeded")
	}
	badJSON := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(badJSON, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session import",
		Flags:   map[string]any{"file": badJSON, "out": out},
	}); err == nil {
		t.Fatal("bad session import JSON succeeded")
	}
}

func TestAdditionalLocalHelperBranchesWithFakes(t *testing.T) {
	restoreOpenErr := fakeOpenPageError(t, errors.New("open failed"))
	for _, command := range []string{"screenshot", "eval", "fetch", "open"} {
		if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{Command: command, Args: []string{"https://example.com"}}); err == nil {
			t.Fatalf("%s open failure succeeded", command)
		}
	}
	restoreOpenErr()

	openWithPage := func(page *fakeLocalPage) func() {
		restore := fakeOpenPage(t, page)
		t.Cleanup(restore)
		return restore
	}

	restore := openWithPage(&fakeLocalPage{gotoErr: errors.New("goto failed")})
	if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "get", Args: []string{"https://example.com"}}); err == nil {
		t.Fatal("get navigation failure succeeded")
	}
	restore()

	restore = openWithPage(&fakeLocalPage{html: "<p>Body</p>", bodyText: "Body", url: "https://example.com/final"})
	resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "get", Args: []string{"https://example.com"}, Flags: map[string]any{"html": true}})
	if err != nil || !strings.Contains(string(resp.Stdout), "<p>Body</p>") {
		t.Fatalf("html get resp=%q err=%v", resp.Stdout, err)
	}
	restore()

	jpg := filepath.Join(t.TempDir(), "shot.jpg")
	restore = openWithPage(&fakeLocalPage{screenshot: []byte("jpeg")})
	resp, err = defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "screenshot", Args: []string{"https://example.com"}, Flags: map[string]any{"out": jpg, "quality": "80"}})
	if err != nil || strings.TrimSpace(string(resp.Stdout)) != jpg {
		t.Fatalf("jpeg screenshot resp=%q err=%v", resp.Stdout, err)
	}
	restore()

	src := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(src, []byte(`{"cookies":[{"name":"sid","value":"1"}],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out.json")
	resp, err = defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "session import", Flags: map[string]any{"file": src, "out": dst}})
	if err != nil || !strings.Contains(string(resp.Stdout), "Imported 1 cookies") {
		t.Fatalf("session import resp=%q err=%v", resp.Stdout, err)
	}

	restoreBrowser := fakeOpenBrowser(t, &fakeLocalBrowser{state: &gomoufox.StorageState{Cookies: []gomoufox.Cookie{{Name: "sid", Value: "1"}}}})
	resp, err = defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "session export", Profile: "/profile", JSON: true, Flags: map[string]any{"out": filepath.Join(t.TempDir(), "state.json")}})
	if err != nil || !bytes.Contains(resp.Stdout, []byte(`"cookies":1`)) {
		t.Fatalf("session export json=%q err=%v", resp.Stdout, err)
	}
	restoreBrowser()

	badState := filepath.Join(t.TempDir(), "bad-state.json")
	if err := os.WriteFile(badState, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readStorageStateFile(badState); err == nil {
		t.Fatal("invalid storage state read succeeded")
	}
	if _, err := storageStateFromPlaywright(&playwright.StorageState{Cookies: []playwright.Cookie{{Expires: math.NaN()}}}); err == nil {
		t.Fatal("nan storage state marshaled successfully")
	}
	if _, err := browserOptions(LocalCommandRequest{Flags: map[string]any{"headless": false, "humanize": "yes"}}); err != nil {
		t.Fatalf("browser options err = %v", err)
	}
	oldNewGomoufox := newGomoufoxForLocal
	newGomoufoxForLocal = func(context.Context, ...gomoufox.Option) (*gomoufox.Browser, error) {
		return nil, nil
	}
	t.Cleanup(func() { newGomoufoxForLocal = oldNewGomoufox })
	if browser, err := newBrowserForLocal(context.Background()); err != nil {
		t.Fatalf("new browser err = %v", err)
	} else if _, ok := browser.(gomoufoxBrowserAdapter); !ok {
		t.Fatalf("new browser type = %T", browser)
	}
	newGomoufoxForLocal = oldNewGomoufox

	closeErr := errors.New("close failed")
	oldCloseBrowser := closeGomoufoxBrowserForLocal
	closeGomoufoxBrowserForLocal = func(*gomoufox.Browser) error { return closeErr }
	t.Cleanup(func() { closeGomoufoxBrowserForLocal = oldCloseBrowser })
	if err := (gomoufoxBrowserAdapter{}).Close(); !errors.Is(err, closeErr) {
		t.Fatalf("adapter close err = %v", err)
	}
	closeGomoufoxBrowserForLocal = oldCloseBrowser

	extractErr := errors.New("extract failed")
	oldExtract := extractContentForLocal
	extractContentForLocal = func(string, string, string, content.Format, int) (content.Result, error) {
		return content.Result{}, extractErr
	}
	t.Cleanup(func() { extractContentForLocal = oldExtract })
	restore = openWithPage(&fakeLocalPage{html: "<p>Body</p>", bodyText: "Body", url: "https://example.com/final"})
	if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "get", Args: []string{"https://example.com"}}); !errors.Is(err, extractErr) {
		t.Fatalf("extract err = %v", err)
	}
	restore()
	extractContentForLocal = oldExtract

	unmarshalErr := errors.New("unmarshal failed")
	oldUnmarshal := unmarshalStorageStateJSON
	unmarshalStorageStateJSON = func([]byte, any) error { return unmarshalErr }
	t.Cleanup(func() { unmarshalStorageStateJSON = oldUnmarshal })
	if _, err := storageStateFromPlaywright(&playwright.StorageState{}); !errors.Is(err, unmarshalErr) {
		t.Fatalf("storage unmarshal err = %v", err)
	}
	unmarshalStorageStateJSON = oldUnmarshal
	if got := flagInt(LocalCommandRequest{Flags: map[string]any{"n": "bad"}}, "n", 7); got != 7 {
		t.Fatalf("bad flagInt = %d", got)
	}
	if got := flagDuration(LocalCommandRequest{Flags: map[string]any{"timeout": "250ms"}}, "timeout", time.Second); got != 250*time.Millisecond {
		t.Fatalf("valid duration = %s", got)
	}
	if err := writeFile0600(filepath.Join(filepath.Join(t.TempDir(), "missing"), "x"), []byte("x"), false); err != nil {
		t.Fatalf("write missing parent err = %v", err)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFile0600(filepath.Join(blocker, "x"), []byte("x"), false); err == nil {
		t.Fatal("write through regular file succeeded")
	}

	parsedOpen, err := parseOpen([]string{"https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	restore = openWithPage(&fakeLocalPage{url: "https://example.com"})
	if code := (Runner{}).executeLocalCommand(context.Background(), globalFlags{}, "open", parsedOpen, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK || !strings.Contains(stdout.String(), "https://example.com") {
		t.Fatalf("default local open code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	restore()
}

func TestDefaultLocalCommandPageErrorEdgesWithFakes(t *testing.T) {
	openWithPage := func(t *testing.T, page *fakeLocalPage) {
		t.Helper()
		restore := fakeOpenPage(t, page)
		t.Cleanup(restore)
	}

	restoreOpenErr := fakeOpenPageError(t, errors.New("open page failed"))
	if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "get", Args: []string{"https://example.com"}}); err == nil {
		t.Fatal("get with open page failure succeeded")
	}
	restoreOpenErr()

	openWithPage(t, &fakeLocalPage{html: "<main>Body</main>", bodyText: "Body", url: "https://example.com/final"})
	resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "get",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"text": true},
	})
	if err != nil || string(resp.Stdout) == "" {
		t.Fatalf("plain get resp=%q err=%v", resp.Stdout, err)
	}

	openWithPage(t, &fakeLocalPage{waitErr: errors.New("missing")})
	_, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "get",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"wait_selector": "#main"},
	})
	if !errors.Is(err, gomoufox.ErrElementNotFound) {
		t.Fatalf("selector err = %v", err)
	}

	openWithPage(t, &fakeLocalPage{contentErr: errors.New("content failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "get", Args: []string{"https://example.com"}}); err == nil {
		t.Fatal("content failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{screenshot: []byte("png")})
	resp, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "screenshot",
		Args:    []string{"https://example.com"},
	})
	if err != nil || string(resp.Stdout) != "png" {
		t.Fatalf("raw screenshot resp=%q err=%v", resp.Stdout, err)
	}

	openWithPage(t, &fakeLocalPage{screenshot: []byte("png")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "screenshot",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"max_bytes": "2"},
	}); err == nil {
		t.Fatal("oversized screenshot succeeded")
	}

	openWithPage(t, &fakeLocalPage{screenshotErr: errors.New("shot failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "screenshot", Args: []string{"https://example.com"}}); err == nil {
		t.Fatal("screenshot failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{gotoErr: errors.New("shot nav failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "screenshot", Args: []string{"https://example.com"}}); err == nil {
		t.Fatal("screenshot navigation failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{waitErr: errors.New("missing")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "screenshot",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"wait_selector": "#shot"},
	}); !errors.Is(err, gomoufox.ErrElementNotFound) {
		t.Fatalf("screenshot selector err = %v", err)
	}

	openWithPage(t, &fakeLocalPage{screenshot: []byte("png")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "screenshot",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"clip": "1,2,3"},
	}); err == nil {
		t.Fatal("screenshot invalid clip succeeded")
	}

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	openWithPage(t, &fakeLocalPage{screenshot: []byte("png")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "screenshot",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"out": filepath.Join(blocker, "shot.png")},
	}); err == nil {
		t.Fatal("screenshot write under regular file succeeded")
	}

	openWithPage(t, &fakeLocalPage{gotoErr: errors.New("eval nav failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "eval",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"script": "1"},
	}); err == nil {
		t.Fatal("eval navigation failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{waitErr: errors.New("missing")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "eval",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"script": "1", "wait_selector": "#eval"},
	}); !errors.Is(err, gomoufox.ErrElementNotFound) {
		t.Fatalf("eval selector err = %v", err)
	}

	openWithPage(t, &fakeLocalPage{})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "eval",
		Args:    []string{"https://example.com"},
	}); err == nil {
		t.Fatal("eval without script source succeeded")
	}

	openWithPage(t, &fakeLocalPage{})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "eval",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"script": "arg => arg", "arg": "{"},
	}); err == nil {
		t.Fatal("bad eval arg JSON succeeded")
	}

	openWithPage(t, &fakeLocalPage{evalErr: errors.New("eval failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "eval",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"script": "1"},
	}); err == nil {
		t.Fatal("eval failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{fetchBody: []byte("body")})
	resp, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "fetch",
		Args:    []string{"https://api.example.com"},
		Flags:   map[string]any{"data": `{"ok":true}`},
	})
	if err != nil || string(resp.Stdout) != "body" {
		t.Fatalf("raw fetch resp=%q err=%v", resp.Stdout, err)
	}

	openWithPage(t, &fakeLocalPage{gotoErr: errors.New("nav failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "fetch",
		Args:    []string{"https://api.example.com"},
		Flags:   map[string]any{"navigate_first": "https://example.com"},
	}); err == nil {
		t.Fatal("fetch navigate failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{fetchErr: errors.New("fetch failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "fetch", Args: []string{"https://api.example.com"}}); err == nil {
		t.Fatal("fetch failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "fetch",
		Args:    []string{"https://api.example.com"},
		Flags:   map[string]any{"data_file": filepath.Join(t.TempDir(), "missing")},
	}); err == nil {
		t.Fatal("fetch missing data file succeeded")
	}

	openWithPage(t, &fakeLocalPage{gotoErr: errors.New("open nav failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "open", Args: []string{"https://example.com"}}); err == nil {
		t.Fatal("open navigation failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{url: "https://example.com/final", waitClosedErr: errors.New("close wait failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "open", Args: []string{"https://example.com"}}); err == nil {
		t.Fatal("open wait failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{url: "https://example.com/final", storageErr: errors.New("state failed")})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "open",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"save_session": filepath.Join(t.TempDir(), "state.json")},
	}); err == nil {
		t.Fatal("open storage failure succeeded")
	}

	openWithPage(t, &fakeLocalPage{url: "https://example.com/final", state: &gomoufox.StorageState{}})
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "open",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"save_session": filepath.Join(blocker, "state.json")},
	}); err == nil {
		t.Fatal("open save-session write under regular file succeeded")
	}

	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session import",
		Flags:   map[string]any{"file": filepath.Join(t.TempDir(), "missing"), "out": filepath.Join(t.TempDir(), "out.json")},
	}); err == nil {
		t.Fatal("missing session import file succeeded")
	}

	src := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(src, []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	existingOut := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(existingOut, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session import",
		Flags:   map[string]any{"file": src, "out": existingOut},
	}); err == nil {
		t.Fatal("session import overwrite without flag succeeded")
	}

	restoreBrowserErr := fakeOpenBrowserError(t, errors.New("open browser failed"))
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session export",
		Profile: "/profile",
		Flags:   map[string]any{"out": filepath.Join(t.TempDir(), "state.json")},
	}); err == nil {
		t.Fatal("session export open browser failure succeeded")
	}
	restoreBrowserErr()

	browser := &fakeLocalBrowser{storageErr: errors.New("state failed")}
	restore := fakeOpenBrowser(t, browser)
	t.Cleanup(restore)
	if _, err = defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "session export",
		Profile: "/profile",
		Flags:   map[string]any{"out": filepath.Join(t.TempDir(), "state.json")},
	}); err == nil {
		t.Fatal("browser storage failure succeeded")
	}
}

type fakeLocalPage struct {
	html            string
	bodyText        string
	title           string
	url             string
	screenshot      []byte
	evaluateValue   any
	fetchStatus     int
	fetchBody       []byte
	gotoURLs        []string
	waitSelector    string
	screenshotCalls int
	script          string
	evalArgs        []any
	fetchURL        string
	fetchMethod     string
	fetchHeaders    map[string]string
	fetchRequest    []byte
	fetchMaxBytes   int
	closeWait       chan struct{}
	waitEntered     chan struct{}
	waitClosedCalls int
	state           *gomoufox.StorageState
	storageCalls    int
	closed          bool
	gotoErr         error
	waitErr         error
	contentErr      error
	screenshotErr   error
	evalErr         error
	fetchErr        error
	waitClosedErr   error
	storageErr      error
}

type fakeGomoufoxBrowser struct {
	page        gomoufoxPageForLocal
	pageErr     error
	context     gomoufoxStorageContextForLocal
	contextErr  error
	newPageOpts int
	closeCalls  int
}

func (b *fakeGomoufoxBrowser) NewPage(ctx context.Context, opts ...gomoufox.ContextOption) (gomoufoxPageForLocal, error) {
	b.newPageOpts = len(opts)
	if b.pageErr != nil {
		return nil, b.pageErr
	}
	if b.page == nil {
		b.page = &fakeGomoufoxPage{}
	}
	return b.page, nil
}

func (b *fakeGomoufoxBrowser) NewContext(ctx context.Context, opts ...gomoufox.ContextOption) (gomoufoxStorageContextForLocal, error) {
	if b.contextErr != nil {
		return nil, b.contextErr
	}
	if b.context == nil {
		b.context = &fakeGomoufoxStorageContext{}
	}
	return b.context, nil
}

func (b *fakeGomoufoxBrowser) Close() error {
	b.closeCalls++
	return nil
}

type fakeGomoufoxPage struct {
	content         string
	bodyText        string
	title           string
	url             string
	screenshot      []byte
	evaluateValue   any
	fetchStatus     int
	fetchBody       []byte
	raw             any
	gotoURL         string
	waitSelector    string
	locatorSelector string
	screenshotCalls int
	evaluateScript  string
	evaluateArgs    []any
	fetchURL        string
	fetchMethod     string
	fetchHeaders    map[string]string
	fetchRequest    []byte
	fetchMaxBytes   int
	closeCalls      int
	gotoErr         error
	waitErr         error
	contentErr      error
	locatorErr      error
	titleErr        error
	screenshotErr   error
	evaluateErr     error
	fetchErr        error
	closeErr        error
}

func (p *fakeGomoufoxPage) Goto(ctx context.Context, url string, opts ...gomoufox.GotoOption) (*gomoufox.Response, error) {
	p.gotoURL = url
	return nil, p.gotoErr
}

func (p *fakeGomoufoxPage) WaitForSelector(ctx context.Context, selector string, opts ...gomoufox.WaitForSelectorOption) (*gomoufox.ElementHandle, error) {
	p.waitSelector = selector
	return nil, p.waitErr
}

func (p *fakeGomoufoxPage) Content(context.Context) (string, error) {
	return p.content, p.contentErr
}

func (p *fakeGomoufoxPage) Locator(selector string) gomoufox.Locator {
	p.locatorSelector = selector
	return &fakeGomoufoxLocator{text: p.bodyText, err: p.locatorErr}
}

func (p *fakeGomoufoxPage) Title(context.Context) (string, error) { return p.title, p.titleErr }
func (p *fakeGomoufoxPage) URL() string                           { return p.url }

func (p *fakeGomoufoxPage) Screenshot(ctx context.Context, opts ...gomoufox.ScreenshotOption) ([]byte, error) {
	p.screenshotCalls++
	return p.screenshot, p.screenshotErr
}

func (p *fakeGomoufoxPage) Evaluate(ctx context.Context, script string, args ...any) (any, error) {
	p.evaluateScript = script
	p.evaluateArgs = args
	return p.evaluateValue, p.evaluateErr
}

func (p *fakeGomoufoxPage) FetchBytes(ctx context.Context, url, method string, headers map[string]string, body []byte) (int, []byte, error) {
	p.fetchURL = url
	p.fetchMethod = method
	p.fetchHeaders = headers
	p.fetchRequest = append([]byte(nil), body...)
	return p.fetchStatus, p.fetchBody, p.fetchErr
}
func (p *fakeGomoufoxPage) FetchBytesWithOptions(ctx context.Context, url, method string, headers map[string]string, body []byte, opts gomoufox.FetchBytesOptions) (gomoufox.FetchBytesResult, error) {
	status, data, err := p.FetchBytes(ctx, url, method, headers, body)
	p.fetchMaxBytes = opts.MaxBytes
	if err != nil {
		return gomoufox.FetchBytesResult{}, err
	}
	truncated := false
	if opts.MaxBytes > 0 && len(data) > opts.MaxBytes {
		data = data[:opts.MaxBytes]
		truncated = true
	}
	return gomoufox.FetchBytesResult{StatusCode: status, Body: data, Truncated: truncated}, nil
}

func (p *fakeGomoufoxPage) Raw() any { return p.raw }

func (p *fakeGomoufoxPage) Close() error {
	p.closeCalls++
	return p.closeErr
}

type fakeGomoufoxLocator struct {
	text string
	err  error
}

func (l *fakeGomoufoxLocator) Click(context.Context, ...gomoufox.LocatorClickOption) error {
	return l.err
}

func (l *fakeGomoufoxLocator) Fill(context.Context, string, ...gomoufox.LocatorFillOption) error {
	return l.err
}

func (l *fakeGomoufoxLocator) Type(context.Context, string, ...gomoufox.LocatorTypeOption) error {
	return l.err
}

func (l *fakeGomoufoxLocator) Press(context.Context, string, ...gomoufox.LocatorPressOption) error {
	return l.err
}

func (l *fakeGomoufoxLocator) Hover(context.Context, ...gomoufox.LocatorHoverOption) error {
	return l.err
}

func (l *fakeGomoufoxLocator) ScrollIntoViewIfNeeded(context.Context, ...gomoufox.LocatorOption) error {
	return l.err
}

func (l *fakeGomoufoxLocator) SelectOption(context.Context, ...gomoufox.LocatorSelectOption) ([]string, error) {
	return nil, l.err
}

func (l *fakeGomoufoxLocator) SetChecked(context.Context, bool, ...gomoufox.LocatorSetCheckedOption) error {
	return l.err
}

func (l *fakeGomoufoxLocator) SetInputFiles(context.Context, []string, ...gomoufox.LocatorSetInputFilesOption) error {
	return l.err
}

func (l *fakeGomoufoxLocator) TextContent(context.Context, ...gomoufox.LocatorTextContentOption) (string, error) {
	return l.text, l.err
}

func (l *fakeGomoufoxLocator) InnerHTML(context.Context, ...gomoufox.LocatorOption) (string, error) {
	return "", l.err
}

func (l *fakeGomoufoxLocator) GetAttribute(context.Context, string, ...gomoufox.LocatorOption) (string, error) {
	return "", l.err
}

func (l *fakeGomoufoxLocator) IsVisible(context.Context, ...gomoufox.LocatorOption) (bool, error) {
	return false, l.err
}

func (l *fakeGomoufoxLocator) Count(context.Context) (int, error) { return 0, l.err }
func (l *fakeGomoufoxLocator) First() gomoufox.Locator            { return l }
func (l *fakeGomoufoxLocator) Last() gomoufox.Locator             { return l }
func (l *fakeGomoufoxLocator) Nth(int) gomoufox.Locator           { return l }

func (l *fakeGomoufoxLocator) WaitFor(context.Context, ...gomoufox.LocatorWaitForOption) error {
	return l.err
}

func (l *fakeGomoufoxLocator) Screenshot(context.Context, ...gomoufox.ScreenshotOption) ([]byte, error) {
	return nil, l.err
}

type fakeGomoufoxStorageContext struct {
	state      *gomoufox.StorageState
	closeCalls int
	err        error
}

func (c *fakeGomoufoxStorageContext) StorageState(context.Context, string) (*gomoufox.StorageState, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.state == nil {
		return &gomoufox.StorageState{}, nil
	}
	return c.state, nil
}

func (c *fakeGomoufoxStorageContext) Close() error {
	c.closeCalls++
	return nil
}

type fakePlaywrightPageRaw struct {
	eventName    string
	eventOptions []playwright.PageWaitForEventOptions
	eventErr     error
	context      playwright.BrowserContext
	waitEntered  chan struct{}
	waitRelease  chan struct{}
}

func (p *fakePlaywrightPageRaw) WaitForEvent(event string, options ...playwright.PageWaitForEventOptions) (any, error) {
	p.eventName = event
	p.eventOptions = options
	if p.waitEntered != nil {
		close(p.waitEntered)
		p.waitEntered = nil
	}
	if p.waitRelease != nil {
		<-p.waitRelease
	}
	return nil, p.eventErr
}

func (p *fakePlaywrightPageRaw) Context() playwright.BrowserContext { return p.context }

type fakePlaywrightStorageContext struct {
	playwright.BrowserContext
	state *playwright.StorageState
	err   error
}

func (c *fakePlaywrightStorageContext) StorageState(path ...string) (*playwright.StorageState, error) {
	return c.state, c.err
}

func (p *fakeLocalPage) Goto(ctx context.Context, url string, opts ...gomoufox.GotoOption) error {
	p.gotoURLs = append(p.gotoURLs, url)
	if p.gotoErr != nil {
		return p.gotoErr
	}
	return nil
}
func (p *fakeLocalPage) WaitForSelector(ctx context.Context, selector string) error {
	p.waitSelector = selector
	if p.waitErr != nil {
		return p.waitErr
	}
	return nil
}
func (p *fakeLocalPage) Content(context.Context) (string, error) {
	if p.contentErr != nil {
		return "", p.contentErr
	}
	return p.html, nil
}
func (p *fakeLocalPage) BodyText(context.Context) (string, error) { return p.bodyText, nil }
func (p *fakeLocalPage) Title(context.Context) (string, error)    { return p.title, nil }
func (p *fakeLocalPage) URL() string                              { return p.url }
func (p *fakeLocalPage) Screenshot(context.Context, ...gomoufox.ScreenshotOption) ([]byte, error) {
	p.screenshotCalls++
	if p.screenshotErr != nil {
		return nil, p.screenshotErr
	}
	return p.screenshot, nil
}
func (p *fakeLocalPage) Evaluate(ctx context.Context, script string, args ...any) (any, error) {
	p.script = script
	p.evalArgs = args
	if p.evalErr != nil {
		return nil, p.evalErr
	}
	return p.evaluateValue, nil
}
func (p *fakeLocalPage) FetchBytes(ctx context.Context, url, method string, headers map[string]string, body []byte) (int, []byte, error) {
	p.fetchURL = url
	p.fetchMethod = method
	p.fetchHeaders = headers
	p.fetchRequest = append([]byte(nil), body...)
	if p.fetchErr != nil {
		return 0, nil, p.fetchErr
	}
	return p.fetchStatus, p.fetchBody, nil
}
func (p *fakeLocalPage) FetchBytesWithOptions(ctx context.Context, url, method string, headers map[string]string, body []byte, opts gomoufox.FetchBytesOptions) (gomoufox.FetchBytesResult, error) {
	status, data, err := p.FetchBytes(ctx, url, method, headers, body)
	p.fetchMaxBytes = opts.MaxBytes
	if err != nil {
		return gomoufox.FetchBytesResult{}, err
	}
	truncated := false
	if opts.MaxBytes > 0 && len(data) > opts.MaxBytes {
		data = data[:opts.MaxBytes]
		truncated = true
	}
	return gomoufox.FetchBytesResult{StatusCode: status, Body: data, Truncated: truncated}, nil
}
func (p *fakeLocalPage) WaitClosed(ctx context.Context) error {
	p.waitClosedCalls++
	if p.waitClosedErr != nil {
		return p.waitClosedErr
	}
	if p.waitEntered != nil {
		close(p.waitEntered)
		p.waitEntered = nil
	}
	if p.closeWait == nil {
		return nil
	}
	select {
	case <-p.closeWait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *fakeLocalPage) StorageState(context.Context) (*gomoufox.StorageState, error) {
	p.storageCalls++
	if p.storageErr != nil {
		return nil, p.storageErr
	}
	if p.state == nil {
		return &gomoufox.StorageState{}, nil
	}
	return p.state, nil
}
func (p *fakeLocalPage) Close() error { p.closed = true; return nil }

type fakeLocalBrowser struct {
	state      *gomoufox.StorageState
	path       string
	closed     bool
	storageErr error
}

func (b *fakeLocalBrowser) StorageState(ctx context.Context, path string) (*gomoufox.StorageState, error) {
	b.path = path
	if b.storageErr != nil {
		return nil, b.storageErr
	}
	return b.state, nil
}
func (b *fakeLocalBrowser) Close() error { b.closed = true; return nil }

func fakeOpenPage(t *testing.T, page *fakeLocalPage) func() {
	t.Helper()
	orig := openPageForLocal
	openPageForLocal = func(context.Context, LocalCommandRequest, []gomoufox.ContextOption) (localPage, func(), error) {
		return page, func() { _ = page.Close() }, nil
	}
	return func() { openPageForLocal = orig }
}

func fakeOpenPageError(t *testing.T, err error) func() {
	t.Helper()
	orig := openPageForLocal
	openPageForLocal = func(context.Context, LocalCommandRequest, []gomoufox.ContextOption) (localPage, func(), error) {
		return nil, nil, err
	}
	return func() { openPageForLocal = orig }
}

func fakeOpenBrowser(t *testing.T, browser *fakeLocalBrowser) func() {
	t.Helper()
	orig := openBrowserForLocal
	openBrowserForLocal = func(context.Context, LocalCommandRequest) (localStorageBrowser, func(), error) {
		return browser, func() { _ = browser.Close() }, nil
	}
	return func() { openBrowserForLocal = orig }
}

func fakeOpenBrowserError(t *testing.T, err error) func() {
	t.Helper()
	orig := openBrowserForLocal
	openBrowserForLocal = func(context.Context, LocalCommandRequest) (localStorageBrowser, func(), error) {
		return nil, nil, err
	}
	return func() { openBrowserForLocal = orig }
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitForDaemonHealth(t *testing.T, baseURL, token string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/v1/health", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("health status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon health not ready: %v", lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func symlinkedOutput(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "out")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link, target
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s content = %q want %q", path, data, want)
	}
}

func assertSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink: %s", path, info.Mode())
	}
}

func daemonResultServer(t *testing.T, result daemon.Result) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/commands/get" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mergeFlagSpecs(sets ...map[string]flagSpec) map[string]flagSpec {
	out := map[string]flagSpec{}
	for _, set := range sets {
		for name, spec := range set {
			out[name] = spec
		}
	}
	return out
}

func advertisedFlagNames(flags []string) map[string]bool {
	out := map[string]bool{}
	for _, flag := range flags {
		for _, field := range strings.Fields(flag) {
			for _, part := range strings.Split(field, "|") {
				part = strings.Trim(part, "[](),")
				if !strings.HasPrefix(part, "--") {
					continue
				}
				name := strings.TrimPrefix(part, "--")
				if i := strings.IndexAny(name, "=<"); i >= 0 {
					name = name[:i]
				}
				out[name] = true
			}
		}
	}
	return out
}

func canonicalJSONForTest(t *testing.T, raw []byte) []byte {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func allDoctorOK() DoctorReport {
	match := true
	return DoctorReport{
		Python:      Check{OK: true, Version: "3.12.4"},
		Venv:        Check{OK: true, Path: "/tmp/venv"},
		CamoufoxPkg: Check{OK: true, Version: "0.4.11"},
		Playwright:  Check{OK: true, PkgVersion: "1.57.0", DriverVersion: "1.57.0", Match: &match},
		CamoufoxBin: Check{OK: true, Version: "v135.0.1-beta.24", Platform: "linux/x64"},
		Display:     Check{OK: true},
	}
}

func replaceDefaultDoctorEnsureInstalled(t *testing.T, fn func(context.Context) error) func() {
	t.Helper()
	orig := defaultDoctorEnsureInstalled
	defaultDoctorEnsureInstalled = fn
	restore := func() { defaultDoctorEnsureInstalled = orig }
	t.Cleanup(restore)
	return restore
}

func replaceDoctorDisplayEnvironment(t *testing.T, goos string, env map[string]string, lookPathErr error) func() {
	t.Helper()
	origGOOS := doctorGOOS
	origLookupEnv := doctorLookupEnv
	origLookPath := doctorLookPath
	doctorGOOS = goos
	doctorLookupEnv = func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
	doctorLookPath = func(file string) (string, error) {
		if lookPathErr != nil {
			return "", lookPathErr
		}
		return "/usr/bin/" + file, nil
	}
	restore := func() {
		doctorGOOS = origGOOS
		doctorLookupEnv = origLookupEnv
		doctorLookPath = origLookPath
	}
	t.Cleanup(restore)
	return restore
}

func replaceDefaultInstallEnsureInstalled(t *testing.T, fn func(context.Context, InstallRequest) error) func() {
	t.Helper()
	orig := defaultInstallEnsureInstalled
	defaultInstallEnsureInstalled = fn
	restore := func() { defaultInstallEnsureInstalled = orig }
	t.Cleanup(restore)
	return restore
}

func replaceNewBrowserForLocal(t *testing.T, fn func(context.Context, ...gomoufox.Option) (gomoufoxBrowserForLocal, error)) func() {
	t.Helper()
	orig := newBrowserForLocal
	newBrowserForLocal = fn
	restore := func() { newBrowserForLocal = orig }
	t.Cleanup(restore)
	return restore
}
