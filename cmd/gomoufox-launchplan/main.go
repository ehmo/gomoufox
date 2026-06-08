package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/sidecar"
)

type report struct {
	GeneratedAt string           `json:"generated_at"`
	Python      string           `json:"python"`
	Scenarios   []scenarioReport `json:"scenarios"`
}

type scenarioReport struct {
	Name             string         `json:"name"`
	CandidateStatus  string         `json:"candidate_status"`
	LaunchArgs       map[string]any `json:"launch_args"`
	PythonPayload    map[string]any `json:"python_payload,omitempty"`
	CandidatePayload map[string]any `json:"candidate_payload,omitempty"`
	CandidateDrift   []valueDrift   `json:"candidate_drift"`
}

type valueDrift struct {
	Field     string `json:"field"`
	Python    any    `json:"python,omitempty"`
	Candidate any    `json:"candidate,omitempty"`
}

var (
	buildPythonLaunchPayload = sidecar.BuildPythonLaunchPayload
	nowUTC                   = func() time.Time { return time.Now().UTC() }
	exitProcess              = os.Exit
)

func main() {
	exitProcess(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var scenarios multiFlag
	var python, venvDir, outDir, candidatePath string
	var dryRun, printJSON bool
	fs := flag.NewFlagSet("gomoufox-launchplan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&scenarios, "scenario", "scenario to dump: default, fixed, persistent, or all; may be repeated")
	fs.StringVar(&python, "python", "", "Python executable with camoufox installed")
	fs.StringVar(&venvDir, "venv-dir", "", "gomoufox venv directory used when --python is unset")
	fs.StringVar(&outDir, "out", "dist/launch-plan/latest", "output directory")
	fs.StringVar(&candidatePath, "candidate", "", "optional pure-Go candidate payload JSON to compare against a single scenario")
	fs.BoolVar(&dryRun, "dry-run", false, "print selected scenarios and Python path without calling Python")
	fs.BoolVar(&printJSON, "json", false, "print JSON report to stdout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	names, err := expandScenarios(scenarios)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if candidatePath != "" && len(names) != 1 {
		_, _ = fmt.Fprintln(stderr, "--candidate requires exactly one --scenario")
		return 2
	}
	python = selectPython(python, venvDir)
	if dryRun {
		_, _ = fmt.Fprintf(stdout, "python: %s\n", python)
		_, _ = fmt.Fprintf(stdout, "scenarios: %s\n", strings.Join(names, ","))
		return 0
	}
	candidate, err := readCandidate(candidatePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
		return 2
	}
	rep := report{
		GeneratedAt: nowUTC().Format(time.RFC3339Nano),
		Python:      displayPath(python),
	}
	failed := false
	for _, name := range names {
		scenario, err := buildScenario(ctx, name, python, candidate)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
			return 1
		}
		if len(scenario.CandidateDrift) > 0 {
			failed = true
		}
		rep.Scenarios = append(rep.Scenarios, scenario)
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	data = append(data, '\n')
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
			return 1
		}
		jsonPath := filepath.Join(outDir, "launch-plan-audit.json")
		if err := os.WriteFile(jsonPath, data, 0o600); err != nil {
			_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
			return 1
		}
		mdPath := filepath.Join(outDir, "launch-plan-audit.md")
		if err := os.WriteFile(mdPath, []byte(markdownReport(rep)), 0o600); err != nil {
			_, _ = fmt.Fprintln(stderr, policy.Redact(err.Error()))
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "audit: %s\njson: %s\n", mdPath, jsonPath)
	}
	if printJSON {
		_, _ = stdout.Write(data)
	}
	if failed {
		_, _ = fmt.Fprintln(stderr, "launch-plan candidate drift detected")
		return 1
	}
	return 0
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func expandScenarios(values []string) ([]string, error) {
	if len(values) == 0 {
		values = []string{"fixed"}
	}
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "all" {
			for _, name := range []string{"default", "fixed", "persistent"} {
				if !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
			}
			continue
		}
		if _, err := scenarioConfig(value); err != nil {
			return nil, err
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, nil
}

func selectPython(override, venvDir string) string {
	if override != "" {
		return override
	}
	if python, err := sidecar.VenvPython(venvDir); err == nil {
		return python
	}
	if env := os.Getenv("PYTHON"); env != "" {
		return env
	}
	return "python3"
}

func displayPath(value string) string {
	if filepath.IsAbs(value) {
		return "<path>"
	}
	return value
}

func readCandidate(path string) (map[string]any, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read candidate: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode candidate: %w", err)
	}
	if raw == nil {
		return nil, fmt.Errorf("candidate payload is not a JSON object")
	}
	if payload, ok := raw["candidate_payload"].(map[string]any); ok {
		return payload, nil
	}
	if payload, ok := raw["python_payload"].(map[string]any); ok {
		return payload, nil
	}
	return raw, nil
}

func buildScenario(ctx context.Context, name, python string, candidate map[string]any) (scenarioReport, error) {
	cfg, err := scenarioConfig(name)
	if err != nil {
		return scenarioReport{}, err
	}
	launchArgs, _ := sidecar.LaunchArgsMap(cfg)
	payload, err := buildPythonLaunchPayload(ctx, python, cfg)
	if err != nil {
		return scenarioReport{}, err
	}
	out := scenarioReport{
		Name:            name,
		CandidateStatus: "not_supplied",
		LaunchArgs:      sanitizeMap(launchArgs),
		PythonPayload:   sanitizeMap(payload),
		CandidateDrift:  []valueDrift{},
	}
	if candidate != nil {
		out.CandidateStatus = "compared"
		out.CandidatePayload = sanitizeMap(candidate)
		out.CandidateDrift = diffValues("$", out.PythonPayload, out.CandidatePayload)
	}
	return out, nil
}

func scenarioConfig(name string) (sidecar.Config, error) {
	switch name {
	case "default":
		return sidecar.Config{Headless: 0}, nil
	case "fixed":
		return sidecar.Config{
			Headless:      0,
			DirectNetwork: true,
			OS:            "linux",
			Locale:        []string{"en-US", "en"},
			BlockWebRTC:   true,
			Window:        &sidecar.Size{Width: 1200, Height: 800},
			Screen:        &sidecar.Size{Width: 1440, Height: 900},
			FirefoxPrefs:  map[string]any{"privacy.resistFingerprinting": false},
			BrowserArgs:   []string{"--safe-mode"},
			Fonts:         []string{"Arial", "Inter"},
			MainWorldEval: true,
			EnableCache:   true,
		}, nil
	case "persistent":
		return sidecar.Config{
			Headless:    0,
			Persistent:  true,
			UserDataDir: "gomoufox-launch-plan-profile",
			Locale:      []string{"en-US"},
		}, nil
	default:
		return sidecar.Config{}, fmt.Errorf("--scenario must be default, fixed, persistent, or all")
	}
}

func sanitizeMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range sortedMapKeys(in) {
		out[key] = sanitizeValue(key, in[key])
	}
	return out
}

func sanitizeValue(key string, value any) any {
	lower := strings.ToLower(key)
	switch typed := value.(type) {
	case map[string]any:
		if lower == "env" {
			return map[string]any{"count": len(typed), "keys": sortedMapKeys(typed)}
		}
		return sanitizeMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeValue(key, item)
		}
		return out
	case string:
		if lower == "username" || lower == "password" {
			return "<redacted>"
		}
		if strings.Contains(lower, "path") || strings.Contains(lower, "dir") || lower == "cwd" || lower == "nodejs" || lower == "launchscript" {
			return "<path>"
		}
		return typed
	default:
		return typed
	}
}

func sortedMapKeys(in map[string]any) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func diffValues(path string, left, right any) []valueDrift {
	if reflect.DeepEqual(left, right) {
		return nil
	}
	leftMap, leftOK := left.(map[string]any)
	rightMap, rightOK := right.(map[string]any)
	if leftOK && rightOK {
		keys := map[string]bool{}
		for key := range leftMap {
			keys[key] = true
		}
		for key := range rightMap {
			keys[key] = true
		}
		var names []string
		for key := range keys {
			names = append(names, key)
		}
		sort.Strings(names)
		var out []valueDrift
		for _, key := range names {
			out = append(out, diffValues(path+"."+key, leftMap[key], rightMap[key])...)
		}
		return out
	}
	return []valueDrift{{Field: path, Python: left, Candidate: right}}
}

func markdownReport(rep report) string {
	lines := []string{
		"# Launch Plan Audit",
		"",
		"| Scenario | Candidate | Drift fields |",
		"|---|---:|---:|",
	}
	for _, scenario := range rep.Scenarios {
		lines = append(lines, fmt.Sprintf("| %s | %s | %d |", scenario.Name, scenario.CandidateStatus, len(scenario.CandidateDrift)))
	}
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}
