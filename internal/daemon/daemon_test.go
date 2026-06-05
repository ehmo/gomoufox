package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const diagnosticSecretFixture = `proxy=http://user:pass@example.com Authorization: Bearer abc.def Cookie: sid=secret Set-Cookie: auth=secret wss://127.0.0.1:9222/rawtoken token=secret {"cookies":[{"name":"sid","value":"cookie-secret"}],"origins":[{"origin":"https://example.com","localStorage":[{"name":"token","value":"storage-secret"}]}]}`

func TestNewRequiresAuthAndValidCaps(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrAuthTokenRequired) {
		t.Fatalf("New without token err = %v", err)
	}
	if _, err := New(Config{AuthToken: "tok", MaxInputBytes: 2 << 20}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New with huge cap err = %v", err)
	}
}

func TestHealthRequiresAuthAndReportsVersions(t *testing.T) {
	server := newTestServer(t, Config{
		Version:        "v0.1.0",
		Ready:          true,
		ActiveSessions: func() int { return 3 },
	})
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}

	rr = call(server, http.MethodGet, "/v1/health", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "v0.1.0" || body["ready"] != true || body["active_sessions"].(float64) != 3 {
		t.Fatalf("health body = %#v", body)
	}
	if body["camoufox_pkg"] == "" || body["playwright"] == "" || body["camoufox_bin"] == "" {
		t.Fatalf("missing version lock fields: %#v", body)
	}
}

func TestCommandExecutesEnvelope(t *testing.T) {
	called := false
	server := newTestServer(t, Config{Executor: func(ctx context.Context, verb string, env Envelope) Result {
		called = true
		if verb != "get" || len(env.Args) != 1 || env.Args[0] != "https://example.com" || !env.JSON {
			t.Fatalf("verb=%s env=%#v", verb, env)
		}
		if env.Flags["markdown"] != true || env.Profile != "/tmp/profile" {
			t.Fatalf("flags/profile = %#v %q", env.Flags, env.Profile)
		}
		return Result{ExitCode: 0, Stdout: "ok\n"}
	}})
	rr := call(server, http.MethodPost, "/v1/commands/get", `{"args":["https://example.com"],"flags":{"markdown":true},"profile":"/tmp/profile","json":true}`)
	if rr.Code != http.StatusOK || !called {
		t.Fatalf("code=%d called=%v body=%s", rr.Code, called, rr.Body.String())
	}
	var result Result
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "ok\n" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCommandAndSessionEndpointsRedactExecutorStderr(t *testing.T) {
	server := newTestServer(t, Config{AllowSessionExport: true, Executor: func(context.Context, string, Envelope) Result {
		return Result{ExitCode: 1, Stderr: diagnosticSecretFixture + "\n"}
	}})
	for _, path := range []string{"/v1/commands/get", "/v1/session/export", "/v1/sessions/work/destroy"} {
		rr := call(server, http.MethodPost, path, `{"args":["https://example.com"],"flags":{"out":"state.json"},"profile":"/profile","json":true}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s code=%d body=%s", path, rr.Code, rr.Body.String())
		}
		var result Result
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		body := result.Stderr
		for i, secret := range []string{"user:pass", "abc.def", "sid=secret", "auth=secret", "/rawtoken", "token=secret", "cookie-secret", "storage-secret"} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s leaked diagnostic fixture %d", path, i)
			}
		}
		if !strings.Contains(body, "<redacted>") {
			t.Fatalf("%s did not show redaction in %q", path, body)
		}
	}
}

func TestSessionExportImportEndpointsExecuteEnvelopes(t *testing.T) {
	var calls []string
	server := newTestServer(t, Config{AllowSessionExport: true, Executor: func(ctx context.Context, verb string, env Envelope) Result {
		calls = append(calls, verb)
		switch verb {
		case "session export":
			if env.Flags["out"] != "state.json" || env.Profile != "/profile" || !env.JSON {
				t.Fatalf("export env = %#v", env)
			}
			return Result{ExitCode: 0, Stdout: "state.json\n"}
		case "session import":
			if env.Flags["file"] != "in.json" || env.Flags["out"] != "out.json" {
				t.Fatalf("import env = %#v", env)
			}
			return Result{ExitCode: 0, Stdout: "imported\n"}
		default:
			t.Fatalf("unexpected verb %q", verb)
		}
		return Result{ExitCode: 1}
	}})

	rr := call(server, http.MethodPost, "/v1/session/export", `{"flags":{"out":"state.json"},"profile":"/profile","json":true}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "state.json") {
		t.Fatalf("export code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = call(server, http.MethodPost, "/v1/session/import", `{"flags":{"file":"in.json","out":"out.json"}}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "imported") {
		t.Fatalf("import code=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Join(calls, ",") != "session export,session import" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestSessionExportRequiresOperatorOptIn(t *testing.T) {
	called := false
	server := newTestServer(t, Config{Executor: func(context.Context, string, Envelope) Result {
		called = true
		return Result{ExitCode: 0, Stdout: "should-not-run\n"}
	}})

	rr := call(server, http.MethodPost, "/v1/session/export", `{"flags":{"out":"state.json"},"profile":"/profile","json":true}`)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "session_export_disabled") {
		t.Fatalf("export code=%d body=%s", rr.Code, rr.Body.String())
	}
	if called {
		t.Fatal("disabled session export reached executor")
	}
	var result Result
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 2 || result.Stderr != "session_export_disabled\n" {
		t.Fatalf("disabled export result = %#v", result)
	}
}

func TestSessionDestroyEndpointDecodesIDAndAcceptsEmptyBody(t *testing.T) {
	server := newTestServer(t, Config{Executor: func(ctx context.Context, verb string, env Envelope) Result {
		if verb != "session destroy" {
			t.Fatalf("verb = %q", verb)
		}
		if len(env.Args) != 1 || env.Args[0] != "work account" {
			t.Fatalf("env args = %#v", env.Args)
		}
		return Result{ExitCode: 0, Stdout: "destroyed\n"}
	}})
	rr := call(server, http.MethodPost, "/v1/sessions/work%20account/destroy", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "destroyed") {
		t.Fatalf("destroy code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCommandRejectsEvalWhenDisabledAndUnknownRoute(t *testing.T) {
	server := newTestServer(t, Config{})
	rr := call(server, http.MethodPost, "/v1/commands/eval", `{"args":["https://example.com"],"flags":{}}`)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "eval_disabled") {
		t.Fatalf("eval code=%d body=%s", rr.Code, rr.Body.String())
	}
	var result Result
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 || result.Stderr != "eval_disabled\n" {
		t.Fatalf("eval result = %#v", result)
	}
	rr = call(server, http.MethodPost, "/v1/commands/open", `{}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("open code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = call(server, http.MethodPost, "/v1/nope", `{}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("path code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = call(server, http.MethodPost, "/v1/session/open", `{}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("session path code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = call(server, http.MethodPost, "/v1/sessions/work/extra/destroy", `{}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("session destroy path code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCommandRejectsBadEnvelopeAndOversizeBody(t *testing.T) {
	server := newTestServer(t, Config{AllowSessionExport: true, MaxInputBytes: 32})
	rr := call(server, http.MethodPost, "/v1/commands/fetch", `{"args":[],"unknown":true}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid command envelope") {
		t.Fatalf("unknown code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = call(server, http.MethodPost, "/v1/commands/fetch", `{"args":["xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversize code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = call(server, http.MethodPost, "/v1/session/export", `{"unknown":true}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid command envelope") {
		t.Fatalf("session bad envelope code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = call(server, http.MethodPost, "/v1/session/import", `{"flags":{}} {}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid command envelope") {
		t.Fatalf("trailing envelope code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = call(server, http.MethodPost, "/v1/sessions/%20/destroy", ``)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid session id") {
		t.Fatalf("bad session id code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = callRawPath(server, http.MethodPost, "/v1/sessions/%zz/destroy", ``)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid session id") {
		t.Fatalf("malformed session id code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = callRawPath(server, http.MethodPost, "/v1/sessions/work%2Faccount/destroy", ``)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid session id") {
		t.Fatalf("slash session id code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = call(server, http.MethodPost, "/v1/sessions/work/destroy", `{"unknown":true}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid command envelope") {
		t.Fatalf("destroy bad envelope code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestEvalCanBeEnabledAndDefaultExecutorIsExplicit(t *testing.T) {
	server := newTestServer(t, Config{EnableEval: true})
	rr := call(server, http.MethodPost, "/v1/commands/eval", `{"args":["https://example.com"],"flags":{}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	var result Result
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 || !strings.Contains(result.Stderr, "not configured") {
		t.Fatalf("result = %#v", result)
	}
}

func newTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	cfg.AuthToken = "tok"
	server, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func call(server *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	return rr
}

func callRawPath(server *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/v1/raw", strings.NewReader(body))
	req.URL.Path = path
	req.RequestURI = path
	req.Header.Set("Authorization", "Bearer tok")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	return rr
}
