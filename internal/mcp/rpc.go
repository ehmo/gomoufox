package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"

	"github.com/ehmo/gomoufox/internal/buildinfo"
	"github.com/ehmo/gomoufox/internal/policy"
)

const protocolVersion = "2025-06-18"

const (
	maxStructuredContentBytes       = 16 * 1024
	maxStructuredContentFieldBytes  = 4 * 1024
	maxStructuredContentKeyBytes    = 128
	maxStructuredContentOmittedKeys = 32
	maxStructuredContentOmittedJSON = 2048
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func HandleJSONRPC(ctx context.Context, server *Server, data []byte) ([]byte, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return marshalRPC(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}}), true
	}
	id, hasID := rpcID(raw)
	if !hasID {
		return nil, false
	}
	var method string
	if err := json.Unmarshal(raw["method"], &method); err != nil || method == "" {
		return marshalRPC(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32600, Message: "invalid request"}}), true
	}
	switch method {
	case "initialize":
		return marshalRPC(rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "gomoufox", "version": buildinfo.Version},
		}}), true
	case "ping":
		return marshalRPC(rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{}}), true
	case "tools/list":
		return marshalRPC(rpcResponse{JSONRPC: "2.0", ID: id, Result: toolsListResult(server)}), true
	case "tools/call":
		result, err := callTool(ctx, server, raw["params"])
		if err != nil {
			return marshalRPC(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32602, Message: "invalid params"}}), true
		}
		return marshalRPC(rpcResponse{JSONRPC: "2.0", ID: id, Result: result}), true
	default:
		return marshalRPC(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32601, Message: "method not found"}}), true
	}
}

func toolsListResult(server *Server) map[string]any {
	tools := server.Tools()
	return map[string]any{
		"tools": tools,
		"_meta": map[string]any{
			"gomoufox/toolset":      server.toolset,
			"gomoufox/tool_count":   len(tools),
			"gomoufox/core_command": "gomoufox mcp --toolset core",
			"gomoufox/agent_hint":   "use core for lower-token browser control; use full for diagnostics or gated tools",
		},
	}
}

func ServeStdio(ctx context.Context, server *Server, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, ok := HandleJSONRPC(ctx, server, scanner.Bytes())
		if !ok {
			continue
		}
		if _, err := out.Write(resp); err != nil {
			return err
		}
		if _, err := out.Write([]byte("\n")); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

func NewHTTPHandler(server *Server, authToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authToken != "" && r.Header.Get("Authorization") != "Bearer "+authToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "method_not_allowed"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(server.cfg.MaxInputBytes)+64*1024))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_request"})
			return
		}
		resp, ok := HandleJSONRPC(r.Context(), server, body)
		if !ok {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	})
}

func callTool(ctx context.Context, server *Server, params json.RawMessage) (map[string]any, error) {
	var in struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      json.RawMessage `json:"_meta"` // reserved by the MCP spec on all request params; accepted and ignored
	}
	if err := decode(params, &in); err != nil || in.Name == "" {
		return nil, ErrInvalidCall
	}
	if len(in.Arguments) == 0 {
		in.Arguments = []byte("{}")
	}
	resp := server.Handle(ctx, in.Name, in.Arguments)
	return toolResult(resp)
}

func rpcID(raw map[string]json.RawMessage) (json.RawMessage, bool) {
	id, hasID := raw["id"]
	if !hasID {
		return nil, false
	}
	if len(id) == 0 {
		return json.RawMessage("null"), true
	}
	return id, true
}

func toolResult(resp Response) (map[string]any, error) {
	payload := resp.Payload
	if resp.IsError {
		payload = redactErrorPayload(payload)
	}
	if len(resp.Content) > 0 {
		result := map[string]any{"content": resp.Content}
		structured, err := structuredContent(payload)
		if err != nil {
			return nil, err
		}
		if structured != nil {
			result["structuredContent"] = structured
		}
		if resp.IsError {
			result["isError"] = true
		}
		return result, nil
	}
	text := "{}"
	if len(payload) > 0 {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		text = string(data)
	}
	result := map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
	}
	structured, _ := structuredContent(payload)
	if structured != nil {
		result["structuredContent"] = structured
	}
	if resp.IsError {
		result["isError"] = true
	}
	return result, nil
}

func redactErrorPayload(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return payload
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = redactErrorValue(value)
	}
	return out
}

func redactErrorValue(value any) any {
	switch typed := value.(type) {
	case string:
		return policy.Redact(typed)
	case map[string]any:
		return redactErrorPayload(typed)
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			item = policy.Redact(item)
			if sensitiveFetchHeader(key) {
				item = "<redacted>"
			}
			out[key] = item
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactErrorValue(item))
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactErrorPayload(item))
		}
		return out
	default:
		return value
	}
}

func structuredContent(payload map[string]any) (map[string]any, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(payload))
	budget := structuredContentBudget{}
	if value, ok := payload["error"]; ok {
		out["error"] = structuredContentRequiredValue("error", value, &budget)
	}
	if value, ok := payload["provenance"]; ok {
		provenance, err := structuredContentProvenance(value, &budget)
		if err != nil {
			return nil, err
		}
		out["provenance"] = provenance
	}
	for _, key := range structuredContentKeys(payload) {
		if key == "error" || key == "provenance" {
			continue
		}
		value := payload[key]
		if structuredContentTextOnlyField(key) {
			budget.omit(key)
			continue
		}
		fieldBytes, err := structuredContentFieldBytes(key, value)
		if err != nil {
			return nil, err
		}
		if fieldBytes > maxStructuredContentFieldBytes {
			budget.omit(key)
			continue
		}
		out[key] = value
		if payloadJSONBytes(out) > maxStructuredContentBytes {
			delete(out, key)
			budget.omit(key)
		}
	}
	if budget.omittedCount > 0 {
		applyStructuredContentMeta(out, &budget)
	}
	return out, nil
}

func structuredContentTextOnlyField(key string) bool {
	switch key {
	case "body", "content", "headers", "messages", "page_errors", "requests", "state":
		return true
	default:
		return false
	}
}

func structuredContentKeys(payload map[string]any) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func structuredContentFieldBytes(key string, value any) (int, error) {
	data, err := json.Marshal(map[string]any{key: value})
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

type structuredContentBudget struct {
	omitted      []string
	omittedCount int
}

func (b *structuredContentBudget) omit(key string) {
	b.omittedCount++
	if len(b.omitted) >= maxStructuredContentOmittedKeys {
		return
	}
	clipped, _ := policy.Truncate([]byte(key), maxStructuredContentKeyBytes)
	candidate := append(append([]string{}, b.omitted...), string(clipped))
	if structuredContentOmittedJSONBytes(candidate) > maxStructuredContentOmittedJSON {
		return
	}
	b.omitted = candidate
}

func structuredContentOmittedJSONBytes(omitted []string) int {
	data, _ := json.Marshal(omitted)
	return len(data)
}

func structuredContentRequiredValue(key string, value any, budget *structuredContentBudget) any {
	if fieldBytes, err := structuredContentFieldBytes(key, value); err == nil && fieldBytes <= maxStructuredContentFieldBytes {
		return value
	}
	budget.omit(key)
	text, ok := value.(string)
	if !ok {
		return "<omitted>"
	}
	clipped, _ := policy.Truncate([]byte(text), maxStructuredContentFieldBytes/2)
	return string(clipped)
}

func structuredContentProvenance(value any, budget *structuredContentBudget) (map[string]any, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		if _, err := structuredContentFieldBytes("provenance", value); err != nil {
			return nil, err
		}
		budget.omit("provenance")
		return map[string]any{"source": "web", "trust": "untrusted"}, nil
	}
	out := map[string]any{}
	for _, key := range []string{"source", "trust"} {
		if item, ok := raw[key]; ok {
			out[key] = structuredContentRequiredValue("provenance."+key, item, budget)
		}
	}
	if item, ok := raw["url"]; ok {
		out["url"] = item
		if fieldBytes, err := structuredContentFieldBytes("provenance", out); err != nil {
			return nil, err
		} else if fieldBytes > maxStructuredContentFieldBytes {
			delete(out, "url")
			budget.omit("provenance.url")
		}
	}
	return out, nil
}

func applyStructuredContentMeta(out map[string]any, budget *structuredContentBudget) {
	for {
		out["_meta"] = structuredContentMeta(budget)
		if payloadJSONBytes(out) <= maxStructuredContentBytes {
			return
		}
		key, _ := structuredContentTrimKey(out)
		delete(out, key)
		budget.omit(key)
	}
}

func structuredContentMeta(budget *structuredContentBudget) map[string]any {
	meta := map[string]any{
		"truncated":       true,
		"omitted":         budget.omitted,
		"omitted_count":   budget.omittedCount,
		"max_bytes":       maxStructuredContentBytes,
		"max_field_bytes": maxStructuredContentFieldBytes,
	}
	if budget.omittedCount > len(budget.omitted) {
		meta["omitted_truncated"] = true
	}
	return map[string]any{
		"gomoufox/structuredContent": meta,
	}
}

func structuredContentTrimKey(out map[string]any) (string, bool) {
	keys := structuredContentKeys(out)
	for i := len(keys) - 1; i >= 0; i-- {
		if !structuredContentReservedKey(keys[i]) {
			return keys[i], true
		}
	}
	for i := len(keys) - 1; i >= 0; i-- {
		if keys[i] != "_meta" {
			return keys[i], true
		}
	}
	return "", false
}

func structuredContentReservedKey(key string) bool {
	return key == "_meta" || key == "error" || key == "provenance"
}

func marshalRPC(resp rpcResponse) []byte {
	data, err := json.Marshal(resp)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
	}
	return data
}
