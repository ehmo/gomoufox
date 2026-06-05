package mcp

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/policy"
)

const (
	defaultObservationEvents = 50
	maxObservationEvents     = 200
	maxObservationTextBytes  = 512
	maxObservationURLBytes   = 1024
)

type observationBuffers struct {
	mu                sync.Mutex
	console           []map[string]any
	pageErrors        []map[string]any
	network           []map[string]any
	dialogs           []map[string]any
	consoleDropped    int
	pageErrorsDropped int
	networkDropped    int
	dialogDropped     int
	generation        uint64
}

func newObservationBuffers() *observationBuffers {
	return &observationBuffers{}
}

func (b *observationBuffers) addConsole(event map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.console, b.consoleDropped = appendObservation(b.console, b.consoleDropped, event)
}

func (b *observationBuffers) addPageError(event map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pageErrors, b.pageErrorsDropped = appendObservation(b.pageErrors, b.pageErrorsDropped, event)
}

func (b *observationBuffers) addPageErrorsDropped(count int) {
	if count <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pageErrorsDropped += count
}

func (b *observationBuffers) addNetwork(event map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.network, b.networkDropped = appendObservation(b.network, b.networkDropped, event)
}

func (b *observationBuffers) addDialog(event map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dialogs, b.dialogDropped = appendObservation(b.dialogs, b.dialogDropped, event)
}

func (b *observationBuffers) consoleMessages(limit int, clear bool) consoleMessagesResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := consoleMessagesResult{
		Messages:          tailObservations(b.console, limit),
		PageErrors:        tailObservations(b.pageErrors, limit),
		ConsoleDropped:    b.consoleDropped,
		PageErrorsDropped: b.pageErrorsDropped,
	}
	if clear {
		b.console = nil
		b.pageErrors = nil
		b.consoleDropped = 0
		b.pageErrorsDropped = 0
	}
	return result
}

func (b *observationBuffers) networkRequests(limit int, clear bool) networkRequestsResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := networkRequestsResult{Requests: tailObservations(b.network, limit), Dropped: b.networkDropped}
	if clear {
		b.network = nil
		b.networkDropped = 0
	}
	return result
}

func (b *observationBuffers) dialogEvents(limit int, clear bool) ([]map[string]any, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	events := tailObservations(b.dialogs, limit)
	dropped := b.dialogDropped
	if clear {
		b.dialogs = nil
		b.dialogDropped = 0
	}
	return events, dropped
}

func (b *observationBuffers) resetAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.console = nil
	b.pageErrors = nil
	b.network = nil
	b.dialogs = nil
	b.consoleDropped = 0
	b.pageErrorsDropped = 0
	b.networkDropped = 0
	b.dialogDropped = 0
	b.generation++
}

func (b *observationBuffers) nextGeneration() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.generation++
	return b.generation
}

func (b *observationBuffers) acceptsGeneration(generation uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.generation == generation
}

func appendObservation(events []map[string]any, dropped int, event map[string]any) ([]map[string]any, int) {
	events = append(events, event)
	if len(events) <= maxObservationEvents {
		return events, dropped
	}
	copy(events, events[1:])
	events[len(events)-1] = nil
	return events[:maxObservationEvents], dropped + 1
}

func tailObservations(events []map[string]any, limit int) []map[string]any {
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}
	out := make([]map[string]any, 0, limit)
	for _, event := range events[len(events)-limit:] {
		out = append(out, cloneObservation(event))
	}
	return out
}

func cloneObservation(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (s *gomoufoxSession) ensureObservations() *observationBuffers {
	if s.observations == nil {
		s.observations = newObservationBuffers()
	}
	return s.observations
}

func (s *gomoufoxSession) attachObservers(ctx context.Context) {
	if s.page == nil {
		return
	}
	buffers := s.ensureObservations()
	generation := buffers.nextGeneration()
	s.page.OnConsole(func(message gomoufox.ConsoleMessage) {
		if !buffers.acceptsGeneration(generation) {
			return
		}
		buffers.addConsole(consoleObservation(message))
	})
	s.page.OnRequest(func(request *gomoufox.Request) {
		if request != nil && buffers.acceptsGeneration(generation) {
			buffers.addNetwork(requestObservation(request))
		}
	})
	s.page.OnRequestFailed(func(request *gomoufox.Request) {
		if request != nil && buffers.acceptsGeneration(generation) {
			buffers.addNetwork(requestFailedObservation(request))
		}
	})
	s.page.OnResponse(func(response *gomoufox.Response) {
		if response != nil && buffers.acceptsGeneration(generation) {
			buffers.addNetwork(responseObservation(response))
		}
	})
	s.page.OnPageError(func(err error) {
		if err != nil && buffers.acceptsGeneration(generation) {
			buffers.addPageError(pageErrorObservation("error", err.Error()))
		}
	})
	s.page.OnDialog(func(dialog gomoufox.Dialog) {
		if dialog == (gomoufox.Dialog{}) || !buffers.acceptsGeneration(generation) {
			return
		}
		action, prompt := s.dialogPolicySnapshot()
		actionErr := handleDialog(dialog, action, prompt)
		buffers.addDialog(dialogObservation(dialog, action, actionErr))
		if actionErr != nil {
			buffers.addPageError(pageErrorObservation("dialog", actionErr.Error()))
		}
	})
	_ = s.installPageErrorObserver(ctx)
}

func consoleObservation(message gomoufox.ConsoleMessage) map[string]any {
	text, truncated := redactObservationString(message.Text, maxObservationTextBytes)
	return map[string]any{
		"type":       policy.Redact(message.Type),
		"text":       text,
		"truncated":  truncated,
		"created_at": observationTime(),
	}
}

func dialogObservation(dialog gomoufox.Dialog, action string, actionErr error) map[string]any {
	message, truncated := redactObservationString(dialog.Message(), maxObservationTextBytes)
	out := map[string]any{
		"type":                  policy.Redact(dialog.Type()),
		"message":               message,
		"message_truncated":     truncated,
		"default_value_present": dialog.DefaultValue() != "",
		"action":                action,
		"handled":               actionErr == nil,
		"created_at":            observationTime(),
	}
	if actionErr != nil {
		out["error"] = policy.Redact(actionErr.Error())
	}
	return out
}

func requestObservation(request *gomoufox.Request) map[string]any {
	url, urlTruncated := sanitizeDiagnosticURL(request.URL(), maxObservationURLBytes)
	headers, headersTruncated := cappedObservationHeaders(request.Headers())
	return map[string]any{
		"event":             "request",
		"url":               url,
		"url_truncated":     urlTruncated,
		"method":            policy.Redact(request.Method()),
		"resource_type":     policy.Redact(request.ResourceType()),
		"navigation":        request.IsNavigationRequest(),
		"headers":           headers,
		"headers_truncated": headersTruncated,
		"created_at":        observationTime(),
	}
}

func responseObservation(response *gomoufox.Response) map[string]any {
	url, urlTruncated := sanitizeDiagnosticURL(response.URL(), maxObservationURLBytes)
	headers, headersTruncated := cappedObservationHeaders(response.Headers())
	out := map[string]any{
		"event":             "response",
		"url":               url,
		"url_truncated":     urlTruncated,
		"status":            response.Status(),
		"status_text":       policy.Redact(response.StatusText()),
		"headers":           headers,
		"headers_truncated": headersTruncated,
		"created_at":        observationTime(),
	}
	if request := response.Request(); request != nil {
		out["method"] = policy.Redact(request.Method())
		out["resource_type"] = policy.Redact(request.ResourceType())
		out["navigation"] = request.IsNavigationRequest()
	}
	return out
}

func requestFailedObservation(request *gomoufox.Request) map[string]any {
	url, urlTruncated := sanitizeDiagnosticURL(request.URL(), maxObservationURLBytes)
	headers, headersTruncated := cappedObservationHeaders(request.Headers())
	return map[string]any{
		"event":             "request_failed",
		"url":               url,
		"url_truncated":     urlTruncated,
		"method":            policy.Redact(request.Method()),
		"resource_type":     policy.Redact(request.ResourceType()),
		"navigation":        request.IsNavigationRequest(),
		"headers":           headers,
		"headers_truncated": headersTruncated,
		"created_at":        observationTime(),
	}
}

func pageErrorObservation(kind, message string) map[string]any {
	text, truncated := redactObservationString(message, maxObservationTextBytes)
	return map[string]any{
		"type":       policy.Redact(kind),
		"text":       text,
		"truncated":  truncated,
		"created_at": observationTime(),
	}
}

func redactObservationString(value string, maxBytes int) (string, bool) {
	value = policy.Redact(value)
	out, truncated := policy.Truncate([]byte(value), maxBytes)
	return string(out), truncated
}

func sanitizeDiagnosticURL(raw string, maxBytes int) (string, bool) {
	redacted := policy.Redact(raw)
	parsed, err := url.Parse(redacted)
	if err != nil || parsed.Scheme == "" {
		return redactObservationString(redacted, maxBytes)
	}
	parsed.User = nil
	parsed.Fragment = ""
	if parsed.RawQuery != "" {
		query := parsed.Query()
		for key, values := range query {
			for i := range values {
				values[i] = "<redacted>"
			}
			query[key] = values
		}
		parsed.RawQuery = query.Encode()
	}
	parsed.Path = sanitizeDiagnosticPath(parsed.Path)
	out, truncated := policy.Truncate([]byte(policy.Redact(parsed.String())), maxBytes)
	return string(out), truncated
}

func sanitizeDiagnosticPath(path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if sensitivePathSegment(part) {
			parts[i] = "<redacted>"
		}
	}
	return strings.Join(parts, "/")
}

func sensitivePathSegment(segment string) bool {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return false
	}
	lower := strings.ToLower(segment)
	for _, marker := range []string{"token", "secret", "password", "apikey", "api-key", "api_key", "jwt", "bearer", "session"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if len(segment) < 24 {
		return false
	}
	base64ish := 0
	for _, r := range segment {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '=' {
			base64ish++
		}
	}
	return base64ish == len(segment)
}

func cappedObservationHeaders(headers map[string]string) (map[string]string, bool) {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(headers))
	used := 0
	truncated := false
	for _, key := range keys {
		name, nameTruncated := policy.Truncate([]byte(key), maxFetchResponseHeaderNameBytes)
		rawValue := headers[key]
		if !safeObservationHeaderValue(key) {
			rawValue = "<redacted>"
		} else {
			rawValue = policy.Redact(rawValue)
		}
		value, valueTruncated := policy.Truncate([]byte(rawValue), maxFetchResponseHeaderValueBytes)
		if nameTruncated || valueTruncated {
			truncated = true
		}
		needed := len(name) + len(value)
		if len(out) >= maxFetchResponseHeaders || used+needed > maxFetchResponseHeaderPayloadBytes {
			truncated = true
			break
		}
		out[string(name)] = string(value)
		used += needed
	}
	if len(out) < len(headers) {
		truncated = true
	}
	return out, truncated
}

func safeObservationHeaderValue(name string) bool {
	switch strings.ToLower(name) {
	case "content-type", "content-length":
		return true
	default:
		return false
	}
}

func sanitizeObservationEvents(events []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		out = append(out, sanitizeObservationMap(event))
	}
	return out
}

func sanitizeObservationMap(event map[string]any) map[string]any {
	out := make(map[string]any, len(event))
	for key, value := range event {
		if observationBodyField(key) {
			continue
		}
		switch strings.ToLower(key) {
		case "url":
			if raw, ok := value.(string); ok {
				sanitized, truncated := sanitizeDiagnosticURL(raw, maxObservationURLBytes)
				out[key] = sanitized
				if _, exists := event["url_truncated"]; !exists && truncated {
					out["url_truncated"] = true
				}
				continue
			}
		case "headers":
			headers, truncated := sanitizeObservationHeadersValue(value)
			out[key] = headers
			if _, exists := event["headers_truncated"]; !exists && truncated {
				out["headers_truncated"] = true
			}
			continue
		}
		out[key] = sanitizeObservationValue(value)
	}
	return out
}

func sanitizeObservationHeadersValue(value any) (map[string]string, bool) {
	switch typed := value.(type) {
	case map[string]string:
		return cappedObservationHeaders(typed)
	case map[string]any:
		headers := make(map[string]string, len(typed))
		for key, item := range typed {
			headers[key] = policy.Redact(toObservationString(item))
		}
		return cappedObservationHeaders(headers)
	default:
		return map[string]string{}, false
	}
}

func sanitizeObservationValue(value any) any {
	switch typed := value.(type) {
	case string:
		out, _ := redactObservationString(typed, maxObservationTextBytes)
		return out
	case map[string]any:
		return sanitizeObservationMap(typed)
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			value, _ := redactObservationString(item, maxObservationTextBytes)
			out[key] = value
		}
		return out
	case []map[string]any:
		return sanitizeObservationEvents(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizeObservationValue(item))
		}
		return out
	default:
		return value
	}
}

func observationBodyField(key string) bool {
	switch strings.ToLower(key) {
	case "body", "request_body", "requestbody", "response_body", "responsebody", "post_data", "postdata", "body_preview", "request_body_preview", "response_body_preview":
		return true
	default:
		return false
	}
}

func toObservationString(value any) string {
	if value == nil {
		return ""
	}
	if str, ok := value.(string); ok {
		return str
	}
	return fmt.Sprint(value)
}

func sanitizeObservationAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out, _ := sanitizeObservationValue(in).(map[string]any)
	return out
}

func observationTime() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func (s *gomoufoxSession) ConsoleMessages(ctx context.Context, opts observeOptions) (consoleMessagesResult, error) {
	if err := s.drainPageErrors(ctx, opts.Clear); err != nil {
		return consoleMessagesResult{}, err
	}
	return s.ensureObservations().consoleMessages(opts.MaxEvents, opts.Clear), nil
}

func (s *gomoufoxSession) NetworkRequests(_ context.Context, opts observeOptions) (networkRequestsResult, error) {
	return s.ensureObservations().networkRequests(opts.MaxEvents, opts.Clear), nil
}

func (s *gomoufoxSession) PerformanceSnapshot(ctx context.Context) (performanceSnapshot, error) {
	result, err := s.page.EvaluateInternal(ctx, performanceSnapshotExpression)
	if err != nil {
		return performanceSnapshot{}, err
	}
	var snapshot performanceSnapshot
	if err := decodeJSONValue(result, &snapshot); err != nil {
		return performanceSnapshot{}, err
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	if snapshot.Memory == nil {
		snapshot.Memory = map[string]any{}
	}
	snapshot.Memory["go_alloc_bytes"] = mem.Alloc
	snapshot.Memory["go_sys_bytes"] = mem.Sys
	snapshot.SampledAtUTC = observationTime()
	snapshot.URL, _ = sanitizeDiagnosticURL(snapshot.URL, maxObservationURLBytes)
	snapshot.Title, _ = redactObservationString(snapshot.Title, maxObservationTextBytes)
	snapshot.Navigation = sanitizeObservationAnyMap(snapshot.Navigation)
	snapshot.Resources = sanitizeObservationAnyMap(snapshot.Resources)
	snapshot.Memory = sanitizeObservationAnyMap(snapshot.Memory)
	snapshot.Viewport = sanitizeObservationAnyMap(snapshot.Viewport)
	return snapshot, nil
}

func (s *gomoufoxSession) installPageErrorObserver(ctx context.Context) error {
	if s.page == nil {
		return nil
	}
	return s.page.AddInitScript(ctx, "("+pageErrorObserverInstallExpression+")();")
}

func (s *gomoufoxSession) drainPageErrors(ctx context.Context, clear bool) error {
	if s.page == nil {
		return nil
	}
	result, err := s.page.EvaluateInternal(ctx, pageErrorObserverDrainExpression, map[string]any{"clear": clear, "max": maxObservationEvents})
	if err != nil {
		return err
	}
	var payload struct {
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
		Dropped int `json:"dropped"`
	}
	if err := decodeJSONValue(result, &payload); err != nil {
		return err
	}
	buffers := s.ensureObservations()
	for _, item := range payload.Errors {
		buffers.addPageError(pageErrorObservation(item.Type, item.Message))
	}
	buffers.addPageErrorsDropped(payload.Dropped)
	return nil
}
