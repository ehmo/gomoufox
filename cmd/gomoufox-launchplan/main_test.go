package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehmo/gomoufox/internal/sidecar"
)

func TestRunDryRunAndScenarioValidation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--scenario", "all", "--python", "/tmp/python", "--dry-run"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "scenarios: default,fixed,persistent") || stderr.Len() != 0 {
		t.Fatalf("dry run code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--scenario", "bad", "--dry-run"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--scenario must be") {
		t.Fatalf("bad scenario code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--scenario", "all", "--candidate", "candidate.json", "--dry-run"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--candidate requires exactly one --scenario") {
		t.Fatalf("bad candidate scenario code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--bad-flag"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("bad flag code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--help"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stderr.String(), "Usage of gomoufox-launchplan") {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestMainUsesProcessDefaults(t *testing.T) {
	oldArgs := os.Args
	oldExit := exitProcess
	t.Cleanup(func() {
		os.Args = oldArgs
		exitProcess = oldExit
	})
	os.Args = []string{"gomoufox-launchplan", "--scenario", "all", "--dry-run"}
	gotCode := -1
	exitProcess = func(code int) { gotCode = code }
	main()
	if gotCode != 0 {
		t.Fatalf("main exit = %d", gotCode)
	}
}

func TestRunWritesLaunchPlanAndRedactsSensitiveValues(t *testing.T) {
	oldBuild := buildPythonLaunchPayload
	oldNow := nowUTC
	t.Cleanup(func() {
		buildPythonLaunchPayload = oldBuild
		nowUTC = oldNow
	})
	nowUTC = func() time.Time { return time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC) }
	buildPythonLaunchPayload = func(_ context.Context, _ string, cfg sidecar.Config) (map[string]any, error) {
		if cfg.Locale[0] != "en-US" {
			t.Fatalf("cfg = %#v", cfg)
		}
		return map[string]any{
			"env":            map[string]any{"PATH": "/safe/bin", "TOKEN": "secret-token"},
			"proxy":          map[string]any{"server": "http://127.0.0.1:4567", "username": "user", "password": "pass"},
			"executablePath": "/example/browser/firefox",
			"args":           []any{"--safe-mode"},
		}, nil
	}
	outDir := filepath.Join(t.TempDir(), "audit")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--scenario", "fixed", "--python", "/managed/python", "--out", outDir, "--json"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("run code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, path := range []string{filepath.Join(outDir, "launch-plan-audit.json"), filepath.Join(outDir, "launch-plan-audit.md")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	text := stdout.String()
	for _, forbidden := range []string{"secret-token", `"password":"pass"`, `"username":"user"`, "/example/browser"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("report leaked %q\n%s", forbidden, text)
		}
	}
	for _, want := range []string{`"generated_at": "2026-06-07T12:00:00Z"`, `"candidate_drift": []`, `"keys"`, `"\u003credacted\u003e"`, `"\u003cpath\u003e"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q\n%s", want, text)
		}
	}
	var parsed report
	if err := json.Unmarshal([]byte(text[strings.Index(text, "{"):]), &parsed); err != nil {
		t.Fatal(err)
	}
	proxy := parsed.Scenarios[0].PythonPayload["proxy"].(map[string]any)
	if proxy["username"] != "<redacted>" || proxy["password"] != "<redacted>" || parsed.Scenarios[0].PythonPayload["executablePath"] != "<path>" {
		t.Fatalf("parsed redaction failed: %#v", parsed.Scenarios[0].PythonPayload)
	}
}

func TestRunCandidateDriftFailsClosed(t *testing.T) {
	oldBuild := buildPythonLaunchPayload
	t.Cleanup(func() { buildPythonLaunchPayload = oldBuild })
	buildPythonLaunchPayload = func(context.Context, string, sidecar.Config) (map[string]any, error) {
		return map[string]any{"args": []any{"--safe-mode"}, "nested": map[string]any{"value": "python"}}, nil
	}
	candidatePath := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(candidatePath, []byte(`{"args":["--safe-mode"],"nested":{"value":"go"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--scenario", "fixed", "--python", "/managed/python", "--candidate", candidatePath, "--out", ""}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "candidate drift") {
		t.Fatalf("drift code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var rep report
	if err := json.Unmarshal([]byte(stdout.String()), &rep); err == nil {
		t.Fatalf("stdout should not contain implicit json when --json is unset: %#v", rep)
	}

	stdout.Reset()
	stderr.Reset()
	if err := os.WriteFile(candidatePath, []byte(`{"python_payload":{"args":["--safe-mode"],"nested":{"value":"python"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code = run(context.Background(), []string{"--scenario", "fixed", "--python", "/managed/python", "--candidate", candidatePath, "--out", "", "--json"}, &stdout, &stderr)
	if code != 0 || strings.Contains(stderr.String(), "candidate drift") {
		t.Fatalf("matching candidate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"candidate_status": "compared"`) {
		t.Fatalf("matching report missing candidate status\n%s", stdout.String())
	}
}

func TestRunErrorBranches(t *testing.T) {
	oldBuild := buildPythonLaunchPayload
	t.Cleanup(func() { buildPythonLaunchPayload = oldBuild })
	buildPythonLaunchPayload = func(context.Context, string, sidecar.Config) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--scenario", "all", "--python", "/managed/python", "--out", "", "--json"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"name": "default"`) || !strings.Contains(stdout.String(), `"name": "persistent"`) {
		t.Fatalf("all scenario code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	buildPythonLaunchPayload = func(context.Context, string, sidecar.Config) (map[string]any, error) {
		return nil, errors.New("token=secret")
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--scenario", "fixed", "--python", "/managed/python"}, &stdout, &stderr)
	if code != 1 || strings.Contains(stderr.String(), "secret") {
		t.Fatalf("build error code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	buildPythonLaunchPayload = func(context.Context, string, sidecar.Config) (map[string]any, error) {
		return map[string]any{"bad": func() {}}, nil
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--scenario", "fixed", "--python", "/managed/python", "--out", ""}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "unsupported") {
		t.Fatalf("marshal error code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	buildPythonLaunchPayload = func(context.Context, string, sidecar.Config) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--scenario", "fixed", "--python", "/managed/python", "--out", filepath.Join(blocker, "child")}, &stdout, &stderr)
	if code != 1 || stderr.Len() == 0 {
		t.Fatalf("mkdir error code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	outDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(outDir, "launch-plan-audit.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--scenario", "fixed", "--python", "/managed/python", "--out", outDir}, &stdout, &stderr)
	if code != 1 || stderr.Len() == 0 {
		t.Fatalf("json write error code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	outDir = t.TempDir()
	if err := os.Mkdir(filepath.Join(outDir, "launch-plan-audit.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--scenario", "fixed", "--python", "/managed/python", "--out", outDir}, &stdout, &stderr)
	if code != 1 || stderr.Len() == 0 {
		t.Fatalf("markdown write error code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--candidate", filepath.Join(t.TempDir(), "missing.json")}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "read candidate") {
		t.Fatalf("missing candidate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	badCandidate := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badCandidate, []byte(`{bad`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--candidate", badCandidate}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "decode candidate") {
		t.Fatalf("bad candidate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if err := os.WriteFile(badCandidate, []byte(`null`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"--candidate", badCandidate}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "candidate payload is not a JSON object") {
		t.Fatalf("null candidate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	payloadCandidate := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(payloadCandidate, []byte(`{"candidate_payload":{"ok":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readCandidate(payloadCandidate)
	if err != nil || got["ok"] != true {
		t.Fatalf("candidate_payload branch = %#v, %v", got, err)
	}
}

func TestHelpers(t *testing.T) {
	t.Setenv("PYTHON", "/env/python")
	if got := selectPython("", filepath.Join(t.TempDir(), "missing")); got != "/env/python" {
		t.Fatalf("env python = %q", got)
	}
	if got := selectPython("/override/python", ""); got != "/override/python" {
		t.Fatalf("override python = %q", got)
	}
	if got := displayPath("/override/python"); got != "<path>" {
		t.Fatalf("display path = %q", got)
	}
	if got := displayPath("python3"); got != "python3" {
		t.Fatalf("display relative path = %q", got)
	}
	t.Setenv("PYTHON", "")
	if got := selectPython("", filepath.Join(t.TempDir(), "missing")); got != "python3" {
		t.Fatalf("fallback python = %q", got)
	}
	if keys := sortedMapKeys(map[string]any{"b": 1, "a": 2}); strings.Join(keys, ",") != "a,b" {
		t.Fatalf("keys = %v", keys)
	}
	drift := diffValues("$", map[string]any{"a": []any{1}}, map[string]any{"a": []any{2}})
	if len(drift) != 1 || drift[0].Field != "$.a" {
		t.Fatalf("drift = %#v", drift)
	}
	if markdown := markdownReport(report{Scenarios: []scenarioReport{{Name: "fixed", CandidateStatus: "compared", CandidateDrift: drift}}}); !strings.Contains(markdown, "| fixed | compared | 1 |") {
		t.Fatalf("markdown = %s", markdown)
	}
	if _, err := buildScenario(context.Background(), "bad", "python", nil); err == nil {
		t.Fatal("bad buildScenario succeeded")
	}
}
