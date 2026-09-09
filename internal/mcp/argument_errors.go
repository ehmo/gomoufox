package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Diagnostics use the live schema after the handler rejects a call. They do not
// replace the handler's validation or echo supplied argument values.
func (s *Server) explainArguments(def toolDefinition, args json.RawMessage, response Response) Response {
	if response.Payload["argument_error"] != nil {
		return response
	}
	if len(args) == 0 {
		args = []byte("{}")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(args))
	if err := decoder.Decode(&value); err != nil {
		return argumentFailure(response, "arguments", "json", "Send one JSON object.", nil)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return argumentFailure(response, "arguments", "json", "Send one JSON object without trailing data.", nil)
	}
	schema := def.Schema()
	if detail := schemaArgumentIssue(value, schema, ""); detail != nil {
		response.Payload["argument_error"] = detail
		response.Payload["hint"] = detail["hint"]
		return response
	}
	object, _ := value.(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	if properties["ref"] != nil && properties["selector"] != nil {
		if !exactlyOne(nonemptyArgument(object, "ref"), nonemptyArgument(object, "selector")) {
			return argumentFailure(response, "target", "exactly_one", "Provide exactly one of ref or selector.", []string{"ref", "selector"})
		}
	}
	if def.Name == "browser_wait_for" {
		return argumentFailure(response, "condition", "exactly_one", "Provide exactly one wait condition.", []string{"selector", "text", "url_contains", "load_state"})
	}
	if def.Name == "session_load" {
		return argumentFailure(response, "storage_state", "exactly_one", "Provide exactly one of path or state.", []string{"path", "state"})
	}
	if def.Name == "browser_fetch" {
		if _, pathPresent := object["body_path"]; pathPresent {
			return argumentFailure(response, "body_path", "body_source", "Provide a nonempty body_path or an inline body, never both. Binary file bodies require --allow-browser-file-fetch.", []string{"body", "body_path"})
		}
	}
	if index, ok := response.Payload["failed_index"].(int); ok && def.Name == "browser_form_batch" {
		return argumentFailure(response, fmt.Sprintf("actions[%d]", index), "action_fields", "Provide one target and the fields for this action kind: text, key, one option list, or checked. No actions have run.", nil)
	}
	return argumentFailure(response, "arguments", "combination", "Check this tool's live input schema and conditional field requirements.", nil)
}

func inputSizeError(field string, cap int) Response {
	hint := fmt.Sprintf("Keep %s within %d bytes. Use browser_fetch.body_path or browser_fetch_form file paths for uploads up to 50 MiB per file; both require --allow-browser-file-fetch and an explicit target scope. File bytes do not count toward the MCP message limit.", field, cap)
	return argumentFailure(mcpError("input_too_large"), field, "max_bytes", hint, cap)
}

func nonemptyArgument(object map[string]any, key string) bool {
	value, _ := object[key].(string)
	return value != ""
}

func argumentFailure(response Response, field, rule, hint string, expected any) Response {
	detail := map[string]any{"field": field, "rule": rule, "hint": hint}
	if expected != nil {
		detail["expected"] = expected
	}
	response.Payload["argument_error"] = detail
	response.Payload["hint"] = hint
	return response
}

func schemaArgumentIssue(value any, schema map[string]any, path string) map[string]any {
	issue := func(rule, hint string, expected any) map[string]any {
		field := path
		if field == "" {
			field = "arguments"
		}
		result := map[string]any{"field": field, "rule": rule, "hint": hint}
		if expected != nil {
			result["expected"] = expected
		}
		return result
	}
	kind, _ := schema["type"].(string)
	if kind != "" && !argumentTypeMatches(value, kind) {
		return issue("type", "Use the declared JSON type.", kind)
	}
	if enum, ok := schema["enum"].([]string); ok {
		text, _ := value.(string)
		found := false
		for _, allowed := range enum {
			if text == allowed {
				found = true
				break
			}
		}
		if !found {
			return issue("enum", "Choose one of the allowed values.", enum)
		}
	}
	switch typed := value.(type) {
	case float64:
		if min, ok := schema["minimum"].(int); ok && typed < float64(min) {
			return issue("minimum", fmt.Sprintf("Use a value of at least %d.", min), min)
		}
		if max, ok := schema["maximum"].(int); ok && typed > float64(max) {
			return issue("maximum", fmt.Sprintf("Use a value no greater than %d.", max), max)
		}
	case string:
		if max, ok := schema["maxLength"].(int); ok && len(typed) > max {
			return issue("max_bytes", fmt.Sprintf("Keep this field within %d bytes.", max), max)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			match, err := regexp.MatchString(pattern, typed)
			if err == nil && !match {
				return issue("pattern", "Use the format specified by this field.", pattern)
			}
		}
	case []any:
		if min, ok := schema["minItems"].(int); ok && len(typed) < min {
			return issue("min_items", fmt.Sprintf("Provide at least %d items.", min), min)
		}
		if max, ok := schema["maxItems"].(int); ok && len(typed) > max {
			return issue("max_items", fmt.Sprintf("Provide at most %d items.", max), max)
		}
		if item, ok := schema["items"].(map[string]any); ok {
			for i, child := range typed {
				if detail := schemaArgumentIssue(child, item, fmt.Sprintf("%s[%d]", path, i)); detail != nil {
					return detail
				}
			}
		}
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		allowed := make([]string, 0, len(properties))
		for name := range properties {
			allowed = append(allowed, name)
		}
		sort.Strings(allowed)
		if schema["additionalProperties"] == false {
			names := make([]string, 0, len(typed))
			for name := range typed {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if properties[name] == nil {
					if replacement := argumentAlias(name, properties); replacement != "" {
						detail := issue("unknown_field", fmt.Sprintf("Replace %s with %s.", name, replacement), replacement)
						detail["field"] = argumentField(path, name)
						return detail
					}
					return issue("unknown_field", "Remove fields absent from this tool's schema.", allowed)
				}
			}
		}
		if required, ok := schema["required"].([]string); ok {
			for _, name := range required {
				if _, ok := typed[name]; !ok {
					return map[string]any{"field": argumentField(path, name), "rule": "required", "hint": "Provide this required field."}
				}
				if text, ok := typed[name].(string); ok && text == "" && (name == "url" || name == "session_id" || name == "path" || name == "script") {
					return map[string]any{"field": argumentField(path, name), "rule": "nonempty", "hint": "Provide a non-empty value."}
				}
			}
		}
		for _, name := range allowed {
			if child, ok := typed[name]; ok {
				property, _ := properties[name].(map[string]any)
				if detail := schemaArgumentIssue(child, property, argumentField(path, name)); detail != nil {
					return detail
				}
			}
		}
	}
	return nil
}

func argumentTypeMatches(value any, kind string) bool {
	switch kind {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && math.Trunc(number) == number
	case "null":
		return value == nil
	default:
		return true
	}
}
func argumentField(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}
func argumentAlias(name string, properties map[string]any) string {
	// Only known spellings are echoed; arbitrary supplied field names stay private.
	aliases := map[string]string{"wait_until": "load_state", "load_state": "wait_until", "timeout": "timeout_ms", "sessionId": "session_id", "maxBytes": "max_bytes", "fullPage": "full_page", "maxElements": "max_elements"}
	if replacement := aliases[name]; properties[replacement] != nil {
		return replacement
	}
	if strings.Contains(name, "-") {
		replacement := strings.ReplaceAll(name, "-", "_")
		if properties[replacement] != nil {
			return replacement
		}
	}
	return ""
}
