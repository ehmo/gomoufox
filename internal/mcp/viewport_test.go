package mcp

import (
	"context"
	"testing"
)

func TestNamedSessionViewportValidationAndPersistence(t *testing.T) {
	server := newTestServer(t, defaultTestConfig(t))
	ctx := context.Background()
	for _, args := range []string{`{"session_id":"bad","width":0}`, `{"session_id":"bad","height":-1}`, `{"session_id":"bad","width":10001}`} {
		assertError(t, server.Handle(ctx, "session_create", raw(args)), "invalid_arguments")
	}
	response := server.Handle(ctx, "session_create", raw(`{"session_id":"mobile","width":390,"height":844}`))
	if response.IsError {
		t.Fatalf("create: %+v", response)
	}
	state, err := server.sessions.touchState("mobile", nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := state.toOptions()
	if opts.width != 390 || opts.height != 844 {
		t.Fatalf("viewport lost: %+v", opts)
	}
	response = server.Handle(ctx, "session_create", raw(`{"session_id":"height","height":900}`))
	if response.IsError {
		t.Fatalf("create: %+v", response)
	}
	state, _ = server.sessions.touchState("height", nil)
	if state.width != 1280 || state.height != 900 {
		t.Fatalf("default width lost: %+v", state)
	}
	response = server.Handle(ctx, "session_create", raw(`{"session_id":"width","width":500}`))
	if response.IsError {
		t.Fatalf("create: %+v", response)
	}
	state, _ = server.sessions.touchState("width", nil)
	if state.width != 500 || state.height != 800 {
		t.Fatalf("default height lost: %+v", state)
	}
}
