package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox/internal/policy"
)

func TestArgumentErrorsExplainObservedAgentFailures(t *testing.T) {
	server := newTestServer(t, defaultTestConfig(t))
	for _, test := range []struct{ name, args, field, rule, hint string }{
		{"browser_screenshot", `{"max_bytes":4000000}`, "max_bytes", "maximum", "2097152"},
		{"browser_get_content", `{"max_bytes":1000000}`, "max_bytes", "maximum", "524288"},
		{"browser_snapshot", `{"max_elements":1200}`, "max_elements", "maximum", "1000"},
		{"browser_wait_for", `{"wait_until":"networkidle"}`, "wait_until", "unknown_field", "load_state"},
		{"browser_wait_for", `{"selector":"input","text":"hello"}`, "condition", "exactly_one", "one"},
		{"browser_navigate", `{"url":"https://example.com","wait_until":"ready"}`, "wait_until", "enum", "allowed"},
		{"browser_navigate", `{}`, "url", "required", "required"},
		{"browser_click", `{"selector":17}`, "selector", "type", "type"},
		{"browser_click", `{}`, "target", "exactly_one", "ref"},
		{"browser_click", `[]`, "arguments", "type", "type"},
		{"browser_click", `{`, "arguments", "json", "JSON"},
		{"browser_click", `{} {}`, "arguments", "json", "trailing"},
		{"browser_form_batch", `{"actions":[{"kind":"select_option","selector":"select","indexes":[-1]}]}`, "actions[0].indexes[0]", "minimum", "0"},
	} {
		t.Run(test.name+test.rule, func(t *testing.T) {
			result := server.Handle(context.Background(), test.name, raw(test.args))
			assertError(t, result, "invalid_arguments")
			detail, ok := result.Payload["argument_error"].(map[string]any)
			if !ok || detail["field"] != test.field || detail["rule"] != test.rule || !strings.Contains(result.Payload["hint"].(string), test.hint) {
				t.Fatalf("diagnostic=%+v", result)
			}
		})
	}
}

func TestOversizedMCPInputsExplainSeparateFileBudget(t *testing.T) {
	cfg := defaultTestConfig(t)
	cfg.Policy.EnableEval = true
	server := newTestServer(t, cfg)
	for _, args := range []json.RawMessage{
		raw(strings.Repeat("x", server.cfg.MaxInputBytes+1)),
		func() json.RawMessage {
			data, _ := json.Marshal(map[string]any{"script": strings.Repeat("x", policy.ScriptInputBytes+1)})
			return data
		}(),
	} {
		response := server.Handle(context.Background(), "browser_evaluate", args)
		assertError(t, response, "input_too_large")
		if !strings.Contains(response.Payload["hint"].(string), "body_path") || !strings.Contains(response.Payload["hint"].(string), "50 MiB") {
			t.Fatalf("input hint=%#v", response)
		}
	}
}

func TestArgumentDiagnosticsDoNotEchoValuesOrUnknownKeys(t *testing.T) {
	server := newTestServer(t, defaultTestConfig(t))
	secret := "never-print-this-secret"
	for _, args := range []string{`{"` + secret + `":"` + secret + `"}`, `{"url":"https://example.com","wait_until":"` + secret + `"}`} {
		result := server.Handle(context.Background(), "browser_navigate", raw(args))
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("input leaked: %s", encoded)
		}
	}
}

func TestSchemaArgumentConstraintsAndAliases(t *testing.T) {
	tests := []struct {
		value  any
		schema map[string]any
		rule   string
	}{
		{false, map[string]any{"type": "boolean"}, ""},
		{nil, map[string]any{"type": "null"}, ""},
		{"value", map[string]any{"type": "future_schema_type"}, ""},
		{1.5, map[string]any{"type": "integer"}, "type"},
		{2.0, map[string]any{"type": "number", "maximum": 1}, "maximum"},
		{0.0, map[string]any{"type": "number", "minimum": 1}, "minimum"},
		{"long", map[string]any{"type": "string", "maxLength": 2}, "max_bytes"},
		{"a", map[string]any{"type": "string", "pattern": "^b"}, "pattern"},
		{"b", map[string]any{"type": "string", "pattern": "^b"}, ""},
		{"b", map[string]any{"type": "string", "pattern": "["}, ""},
		{[]any{}, map[string]any{"type": "array", "minItems": 1}, "min_items"},
		{[]any{1.0, 2.0}, map[string]any{"type": "array", "maxItems": 1}, "max_items"},
		{[]any{"ok"}, map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, ""},
	}
	for _, test := range tests {
		detail := schemaArgumentIssue(test.value, test.schema, "")
		if test.rule == "" {
			if detail != nil {
				t.Fatalf("unexpected issue: %v", detail)
			}
		} else if detail == nil || detail["rule"] != test.rule {
			t.Fatalf("wanted %s: %v", test.rule, detail)
		}
	}
	if got := argumentAlias("timeout-ms", map[string]any{"timeout_ms": true}); got != "timeout_ms" {
		t.Fatal(got)
	}
	if got := argumentAlias("unknown", nil); got != "" {
		t.Fatal(got)
	}
}

func TestArgumentDiagnosticsPreserveHandlerDetail(t *testing.T) {
	server := newTestServer(t, defaultTestConfig(t))
	response := mcpError("invalid_arguments")
	response.Payload["argument_error"] = map[string]any{"field": "timeout_ms", "rule": "maximum"}
	response.Payload["hint"] = "Keep the bounded timeout."
	got := server.explainArguments(toolDefinition{}, nil, response)
	if got.Payload["hint"] != "Keep the bounded timeout." {
		t.Fatalf("handler guidance overwritten: %#v", got)
	}
}
