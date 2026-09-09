package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProfileOwnershipHeldThroughCleanup(t *testing.T) {
	store := newSessionStore(5, time.Minute)
	path := filepath.Join(t.TempDir(), "private-profile")
	owner, err := store.create(sessionOptions{id: "login", profilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	started, finish := make(chan struct{}), make(chan struct{})
	owner.browser = &fakeBrowserSession{closeFunc: func() error { close(started); <-finish; return nil }}
	destroyed := make(chan error, 1)
	go func() { _, err := store.destroy("login", false); destroyed <- err }()
	<-started
	_, err = store.create(sessionOptions{id: "second", profilePath: path})
	var conflict *profileOwnerError
	if !errors.As(err, &conflict) || conflict.sessionID != "login" {
		t.Fatalf("profile lease lost: %v", err)
	}
	result := sessionError(err)
	if result.Payload["owner_session_id"] != "login" || strings.Contains(mustJSONText(result.Payload), path) {
		t.Fatalf("owner diagnostic: %+v", result)
	}
	close(finish)
	if err := <-destroyed; err != nil {
		t.Fatal(err)
	}
	if _, err := store.create(sessionOptions{id: "second", profilePath: path}); err != nil {
		t.Fatal(err)
	}
	if err := store.closeAll(); err != nil || len(store.profilePaths) != 0 {
		t.Fatalf("close=%v leases=%d", err, len(store.profilePaths))
	}
	store.releaseSessionPaths(nil)
}

func TestProfileOwnerResponseAndStateSources(t *testing.T) {
	server := newTestServer(t, defaultTestConfig(t))
	defer server.Close()
	ctx := context.Background()
	if r := server.Handle(ctx, "session_create", raw(`{"session_id":"login","profile_path":"owned"}`)); r.IsError {
		t.Fatal(r)
	}
	response := server.Handle(ctx, "session_create", raw(`{"session_id":"second","profile_path":"owned"}`))
	assertError(t, response, "profile_in_use")
	if response.Payload["owner_session_id"] != "login" {
		t.Fatal(response)
	}
	listed := sessionList(t, server.Handle(ctx, "session_list", raw(`{}`)))
	if len(listed) != 1 || listed[0]["state_source"] != "profile" {
		t.Fatal(listed)
	}
	if (&sessionState{}).stateSource() != "ephemeral" || (&sessionState{storageStatePath: "state.json"}).stateSource() != "storage_state" {
		t.Fatal("state source mislabeled")
	}
}

func TestExpiredProfileCanBeReacquiredAfterCleanup(t *testing.T) {
	store := newSessionStore(5, time.Minute)
	now := time.Now()
	store.now = func() time.Time { return now }
	if _, err := store.create(sessionOptions{id: "old", profilePath: "profile"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := store.create(sessionOptions{id: "next", profilePath: "profile"}); err != nil {
		t.Fatal(err)
	}
}
