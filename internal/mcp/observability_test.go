package mcp

import (
	"fmt"
	"strings"
	"testing"
)

func TestObservationBuffersBoundCloneAndClear(t *testing.T) {
	buffers := newObservationBuffers()
	for i := 0; i < maxObservationEvents+1; i++ {
		event := map[string]any{"text": fmt.Sprintf("event-%03d", i)}
		buffers.addConsole(event)
		buffers.addPageError(event)
		buffers.addNetwork(event)
	}
	buffers.addPageErrorsDropped(5)

	console := buffers.consoleMessages(2, false)
	if len(console.Messages) != 2 || console.Messages[0]["text"] != "event-199" || console.Messages[1]["text"] != "event-200" {
		t.Fatalf("console tail = %#v", console.Messages)
	}
	if len(console.PageErrors) != 2 || console.ConsoleDropped != 1 || console.PageErrorsDropped != 6 {
		t.Fatalf("console counters = %#v", console)
	}
	network := buffers.networkRequests(3, false)
	if len(network.Requests) != 3 || network.Requests[0]["text"] != "event-198" || network.Dropped != 1 {
		t.Fatalf("network tail = %#v", network)
	}

	console.Messages[0]["text"] = "mutated"
	again := buffers.consoleMessages(2, true)
	if again.Messages[0]["text"] != "event-199" {
		t.Fatalf("buffer returned mutable backing map = %#v", again.Messages)
	}
	empty := buffers.consoleMessages(2, false)
	if len(empty.Messages) != 0 || len(empty.PageErrors) != 0 || empty.ConsoleDropped != 0 || empty.PageErrorsDropped != 0 {
		t.Fatalf("console clear failed = %#v", empty)
	}
	network = buffers.networkRequests(3, true)
	if len(network.Requests) != 3 || network.Dropped != 1 {
		t.Fatalf("network before clear = %#v", network)
	}
	network = buffers.networkRequests(3, false)
	if len(network.Requests) != 0 || network.Dropped != 0 {
		t.Fatalf("network clear failed = %#v", network)
	}
}

func TestObservationSanitizersRedactURLsHeadersBodiesAndLongValues(t *testing.T) {
	if sanitizeDiagnosticPath("") != "" {
		t.Fatal("empty path changed")
	}
	urlValue, truncated := sanitizeDiagnosticURL("https://user:pass@example.com/download/abcdefghijklmnopqrstuvwxyz012345?code=oauth-secret&email=user@example.com#fragment", 4096)
	if truncated {
		t.Fatalf("unexpected url truncation")
	}
	for _, secret := range []string{"user:pass", "abcdefghijklmnopqrstuvwxyz012345", "oauth-secret", "user@example.com", "fragment"} {
		if strings.Contains(urlValue, secret) {
			t.Fatalf("url leaked %q: %s", secret, urlValue)
		}
	}

	events := sanitizeObservationEvents([]map[string]any{{
		"url":          "https://example.com/?token=secret",
		"body":         "must-not-return",
		"responseBody": "must-not-return",
		"text":         "Authorization: Bearer text-secret " + strings.Repeat("x", maxObservationTextBytes+1),
		"headers": map[string]string{
			"content-type":            "text/html",
			"content-length":          "12",
			"authorization":           "Bearer header-secret",
			"x-api-key":               "api-secret",
			"cf-access-jwt-assertion": "cf-secret",
		},
	}})
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	event := events[0]
	if _, ok := event["body"]; ok {
		t.Fatalf("body leaked = %#v", event)
	}
	if _, ok := event["responseBody"]; ok {
		t.Fatalf("responseBody leaked = %#v", event)
	}
	headers := event["headers"].(map[string]string)
	if headers["content-type"] != "text/html" || headers["content-length"] != "12" {
		t.Fatalf("safe headers = %#v", headers)
	}
	for _, name := range []string{"authorization", "x-api-key", "cf-access-jwt-assertion"} {
		if headers[name] != "<redacted>" {
			t.Fatalf("%s = %q", name, headers[name])
		}
	}
	encoded := mustJSONText(map[string]any{"event": event})
	for _, secret := range []string{"secret", "must-not-return", strings.Repeat("x", maxObservationTextBytes+1)} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("sanitized event leaked %q: %s", secret, encoded)
		}
	}

	fallbackURL, fallbackTruncated := sanitizeDiagnosticURL("not a url token=fallback-secret "+strings.Repeat("x", maxObservationURLBytes), 64)
	if !fallbackTruncated || strings.Contains(fallbackURL, "fallback-secret") || len(fallbackURL) != 64 {
		t.Fatalf("fallback url = %q truncated=%t", fallbackURL, fallbackTruncated)
	}
	headers, headersTruncated := sanitizeObservationHeadersValue(map[string]any{
		"content-length": 42,
		"x-api-key":      "nested-secret",
	})
	if headersTruncated || headers["content-length"] != "42" || headers["x-api-key"] != "<redacted>" {
		t.Fatalf("map-any headers = %#v truncated=%t", headers, headersTruncated)
	}
	headers, headersTruncated = sanitizeObservationHeadersValue("bad")
	if headersTruncated || len(headers) != 0 {
		t.Fatalf("default headers = %#v truncated=%t", headers, headersTruncated)
	}
	value := sanitizeObservationValue(map[string]string{"token": "token=value-secret"}).(map[string]string)
	if strings.Contains(value["token"], "value-secret") {
		t.Fatalf("map string value leaked = %#v", value)
	}
	if toObservationString(nil) != "" {
		t.Fatal("nil observation string not empty")
	}

	longHeaderName := strings.Repeat("h", maxFetchResponseHeaderNameBytes+1)
	largeHeaders := map[string]string{
		longHeaderName: "secret",
		"content-type": strings.Repeat("t", maxFetchResponseHeaderValueBytes+1),
	}
	for i := 0; i < maxFetchResponseHeaders+1; i++ {
		largeHeaders[fmt.Sprintf("x-%03d", i)] = "secret"
	}
	headers, headersTruncated = cappedObservationHeaders(largeHeaders)
	if !headersTruncated || len(headers) > maxFetchResponseHeaders {
		t.Fatalf("large headers = len:%d truncated:%t", len(headers), headersTruncated)
	}

	longURL := "https://example.com/?" + strings.Repeat("q", maxObservationURLBytes)
	longEvent := sanitizeObservationMap(map[string]any{"url": longURL})
	if longEvent["url_truncated"] != true {
		t.Fatalf("url truncation flag missing = %#v", longEvent)
	}
	headerEvent := sanitizeObservationMap(map[string]any{"headers": largeHeaders})
	if headerEvent["headers_truncated"] != true {
		t.Fatalf("headers truncation flag missing = %#v", headerEvent)
	}
	arrayMaps := sanitizeObservationValue([]map[string]any{{"text": "token=array-secret"}}).([]map[string]any)
	if strings.Contains(mustJSONText(map[string]any{"array": arrayMaps}), "array-secret") {
		t.Fatalf("array map leaked = %#v", arrayMaps)
	}
	arrayAny := sanitizeObservationValue([]any{"token=any-secret", map[string]any{"text": "password=map-secret"}}).([]any)
	encoded = mustJSONText(map[string]any{"array": arrayAny})
	if strings.Contains(encoded, "any-secret") || strings.Contains(encoded, "map-secret") {
		t.Fatalf("array any leaked = %s", encoded)
	}
}
