package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/a11y"
)

func TestBrowserErrorsClassifyWithoutEchoingInputs(t *testing.T) {
	secret := "private-selector-cookie-password-url"
	for _, test := range []struct {
		err    error
		reason string
	}{
		{fmt.Errorf("%w: %w", gomoufox.ErrEvaluationInterrupted, context.DeadlineExceeded), "evaluation_interrupted"},
		{gomoufox.ErrNotInstalled, "runtime"}, {gomoufox.ErrVersionMismatch, "runtime"},
		{errors.New("profile is in use: " + secret), "profile_in_use"},
		{gomoufox.ErrSidecarStart, "launch"}, {gomoufox.ErrSidecarDied, "launch"},
		{gomoufox.ErrSessionClosed, "closed"}, {errors.New("Browser.newPage: window is null: " + secret), "closed"},
		{a11y.ErrUnknownRef, "stale_ref"}, {a11y.ErrStaleRef, "stale_ref"},
		{a11y.ErrAmbiguousRef, "ambiguous_selector"}, {errors.New("strict mode violation: " + secret), "ambiguous_selector"},
		{errors.New("frame was detached: " + secret), "frame"},
		{context.DeadlineExceeded, "timeout"}, {gomoufox.ErrTimeout, "timeout"}, {gomoufox.ErrNavigationTimeout, "timeout"},
		{errors.New("Timeout 1000ms exceeded waiting for " + secret), "timeout"},
		{context.Canceled, "canceled"}, {gomoufox.ErrElementNotFound, "selector"},
		{errors.New("not a valid selector: " + secret), "selector"},
		{errors.New("NS_ERROR_UNKNOWN_HOST: " + secret), "network"},
		{errors.New("TypeError: " + secret), "script"},
		{&gomoufox.BrowserFetchError{Code: "cors_denied", URL: secret, Message: secret}, "cors_denied"},
		{&gomoufox.BrowserFetchError{Code: "csp_blocked", BodyPreview: []byte(secret)}, "csp_blocked"},
		{&gomoufox.BrowserFetchError{Code: "mixed_content_blocked", Message: secret}, "mixed_content_blocked"},
		{&gomoufox.BrowserFetchError{Code: "unrecognized", Message: secret}, "unknown"},
		{errors.New("driver failure: " + secret), "unknown"}, {nil, "unknown"},
	} {
		response := browserError(test.err)
		if response.Payload["reason"] != test.reason || response.Payload["hint"] == "" {
			t.Fatalf("%v: %+v", test.err, response)
		}
		encoded, _ := json.Marshal(response)
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("input leaked: %s", encoded)
		}
	}
}

func TestLaunchAndOperationFailureKeepErrorCodes(t *testing.T) {
	cfg := defaultTestConfig(t)
	cfg.BrowserFactory = &fakeBrowserFactory{err: errors.New("private launch path")}
	server := newTestServer(t, cfg)
	response := server.Handle(context.Background(), "browser_snapshot", raw(`{}`))
	assertError(t, response, "browser_start_failed")
	if response.Payload["phase"] != "launch" || response.Payload["reason"] != "launch" {
		t.Fatalf("launch: %+v", response)
	}
	cfg.BrowserFactory = &fakeBrowserFactory{session: &fakeBrowserSession{clickErr: fmt.Errorf("%w: private selector", context.DeadlineExceeded)}}
	server = newTestServer(t, cfg)
	response = server.Handle(context.Background(), "browser_click", raw(`{"selector":"#private-selector"}`))
	assertError(t, response, "browser_error")
	if response.Payload["reason"] != "timeout" {
		t.Fatalf("operation: %+v", response)
	}
}
