package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSessionCapacityErrorsSupportRecovery(t *testing.T) {
	cfg := defaultTestConfig(t)
	cfg.Policy.MaxSessions = 1
	s := newTestServer(t, cfg)
	ctx := context.Background()
	if result := s.Handle(ctx, "session_create", raw(`{"session_id":"work"}`)); result.IsError {
		t.Fatal(result)
	}
	for _, tool := range []string{"session_create", "browser_get_content"} {
		result := s.Handle(ctx, tool, raw(`{"session_id":"overflow"}`))
		assertError(t, result, "session_limit")
		capacity := result.Payload["capacity"].(map[string]int)
		if capacity["used"] != 1 || capacity["limit"] != 1 || capacity["available"] != 0 || result.Payload["next_tool"] != "session_list" {
			t.Fatal(result)
		}
		ids := result.Payload["active_session_ids"].([]string)
		if len(ids) != 1 || ids[0] != "work" || len(cfg.BrowserFactory.(*fakeBrowserFactory).requests) != 0 {
			t.Fatal("capacity rejection changed browser state")
		}
	}
	if result := s.Handle(ctx, "session_destroy", raw(`{"session_id":"work"}`)); result.IsError {
		t.Fatal(result)
	}
	list := s.Handle(ctx, "session_list", raw(`{}`))
	if list.Payload["capacity"].(map[string]int)["available"] != 1 {
		t.Fatal(list)
	}
	if result := s.Handle(ctx, "session_create", raw(`{"session_id":"overflow"}`)); result.IsError {
		t.Fatal(result)
	}
}

func TestCapacityErrorBoundsIdentifiersAndKeepsSentinel(t *testing.T) {
	store := newSessionStore(1, time.Hour)
	if _, err := store.create(sessionOptions{id: strings.Repeat("x", 129)}); err != nil {
		t.Fatal(err)
	}
	_, err := store.create(sessionOptions{id: "other"})
	if !errors.Is(err, errSessionLimit) {
		t.Fatal(err)
	}
	result := sessionError(err)
	if len(result.Payload["active_session_ids"].([]string)) != 0 || result.Payload["session_ids_omitted"] != 1 {
		t.Fatal(result)
	}
}
