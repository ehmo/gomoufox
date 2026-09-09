package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFormBatchReportsCompletedActionsWithoutReplaying(t *testing.T) {
	session := &fakeBrowserSession{pressErr: errors.New("Timeout 1000ms exceeded: private value")}
	cfg := defaultTestConfig(t)
	cfg.BrowserFactory = &fakeBrowserFactory{session: session}
	server := newTestServer(t, cfg)
	response := server.Handle(context.Background(), "browser_form_batch", raw(`{"actions":[{"kind":"type","selector":"#first","text":"private value"},{"kind":"press_key","selector":"#missing","key":"Enter"},{"kind":"type","selector":"#last","text":"must not run"}]}`))
	assertError(t, response, "browser_error")
	if response.Payload["completed_actions"] != 1 || response.Payload["failed_index"] != 1 || response.Payload["total_actions"] != 3 || response.Payload["phase"] != "execution" || response.Payload["failed_action_may_have_applied"] != true {
		t.Fatalf("progress=%+v", response)
	}
	if len(session.typeCalls) != 1 || len(session.pressCalls) != 1 {
		t.Fatalf("executed extra actions: type=%d press=%d", len(session.typeCalls), len(session.pressCalls))
	}
	data, _ := json.Marshal(response)
	if strings.Contains(string(data), "private value") || strings.Contains(string(data), "must not run") {
		t.Fatalf("input leaked: %s", data)
	}
}

func TestInvalidBatchValidatesAllActionsBeforeExecution(t *testing.T) {
	session := &fakeBrowserSession{}
	factory := &fakeBrowserFactory{session: session}
	cfg := defaultTestConfig(t)
	cfg.BrowserFactory = factory
	server := newTestServer(t, cfg)
	response := server.Handle(context.Background(), "browser_form_batch", raw(`{"actions":[{"kind":"type","selector":"#first","text":"valid"},{"kind":"type","selector":"#second"}]}`))
	assertError(t, response, "invalid_arguments")
	if response.Payload["completed_actions"] != 0 || response.Payload["failed_index"] != 1 || response.Payload["phase"] != "validation" {
		t.Fatalf("progress=%+v", response)
	}
	if len(factory.requests) != 0 || len(session.typeCalls) != 0 {
		t.Fatal("invalid batch started a browser action")
	}
	detail := response.Payload["argument_error"].(map[string]any)
	if detail["field"] != "actions[1]" {
		t.Fatalf("argument detail=%+v", detail)
	}
}
