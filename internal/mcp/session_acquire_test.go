package mcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentSessionAcquisitionReusesOneOwner(t *testing.T) {
	store := newSessionStore(1, time.Hour)
	var created atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fresh, err := store.createOrReuse(sessionOptions{id: "work", width: 390, height: 844}, true)
			if err != nil {
				t.Error(err)
			}
			if fresh {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 || store.capacity()["used"] != 1 {
		t.Fatalf("created=%d capacity=%v", created.Load(), store.capacity())
	}
	if _, _, err := store.createOrReuse(sessionOptions{id: "work"}, true); !errors.Is(err, errSessionOptions) {
		t.Fatalf("changed settings accepted: %v", err)
	}
	store.sessions["work"].lifecycle.Store(uint32(sessionInitializing))
	if _, _, err := store.createOrReuse(sessionOptions{id: "work", width: 390, height: 844}, true); !errors.Is(err, errSessionClosing) {
		t.Fatalf("initializing session reused: %v", err)
	}
}

func TestRequiredSessionDoesNotLaunchAfterFailedAcquisition(t *testing.T) {
	cfg := defaultTestConfig(t)
	cfg.Validator = &fakeValidator{}
	cfg.Policy.MaxSessions = 1
	s := newTestServer(t, cfg)
	ctx := context.Background()
	if result := s.Handle(ctx, "session_create", raw(`{"session_id":"work","width":390,"reuse_if_exists":true}`)); result.IsError || result.Payload["created"] != true {
		t.Fatal(result)
	}
	result := s.Handle(ctx, "session_create", raw(`{"session_id":"work","width":390,"reuse_if_exists":true}`))
	if result.IsError || result.Payload["reused"] != true || result.Payload["created"] != false {
		t.Fatal(result)
	}
	assertError(t, s.Handle(ctx, "session_create", raw(`{"session_id":"work","reuse_if_exists":true}`)), "session_options_mismatch")
	assertError(t, s.Handle(ctx, "session_create", raw(`{"session_id":"rejected"}`)), "session_limit")
	for _, name := range []string{"rejected", "missing"} {
		assertError(t, s.Handle(ctx, "browser_navigate", mustRaw(t, map[string]any{"session_id": name, "url": "https://example.com", "require_existing": true})), "session_not_found")
	}
	if len(cfg.BrowserFactory.(*fakeBrowserFactory).requests) != 0 {
		t.Fatal("failed acquisition launched browser")
	}
	if result := s.Handle(ctx, "browser_navigate", raw(`{"session_id":"work","url":"https://example.com","require_existing":true}`)); result.IsError {
		t.Fatal(result)
	}
	result = s.Handle(ctx, "session_create", raw(`{"session_id":"work","width":390,"reuse_if_exists":true}`))
	if result.IsError || result.Payload["browser_started"] != true || len(cfg.BrowserFactory.(*fakeBrowserFactory).requests) != 1 {
		t.Fatal("reuse replaced browser")
	}
	s.sessions.now = func() time.Time { return time.Now().Add(2 * cfg.Policy.SessionTTL) }
	assertError(t, s.Handle(ctx, "browser_navigate", raw(`{"session_id":"work","url":"https://example.com","require_existing":true}`)), "session_not_found")
	if s.sessions.capacity()["used"] != 0 {
		t.Fatal("expired required session recreated")
	}
}

func TestSessionAcquisitionRejectsOwnerClosedWhileWaiting(t *testing.T) {
	server := newTestServer(t, defaultTestConfig(t))
	session, err := server.sessions.create(sessionOptions{id: "work"})
	if err != nil {
		t.Fatal(err)
	}
	session.opMu.Lock()
	started, proceed := make(chan struct{}), make(chan struct{})
	server.sessions.now = func() time.Time { close(started); <-proceed; return time.Now() }
	result := make(chan Response, 1)
	go func() {
		result <- server.sessionCreate(context.Background(), raw(`{"session_id":"work","reuse_if_exists":true}`))
	}()
	<-started
	close(proceed)
	// Acquisition holds the store lock during its reuse check. Once this lock
	// is available, that check has finished and the handler waits on opMu.
	server.sessions.mu.Lock()
	server.sessions.mu.Unlock()
	session.lifecycle.Store(uint32(sessionClosing))
	session.opMu.Unlock()
	assertError(t, <-result, "session_closed")
}

func TestSessionErrorsKeepStableMessagesAndLimitIdentity(t *testing.T) {
	if got := (&profileOwnerError{sessionID: "private-session"}).Error(); got != "profile is owned by another session" {
		t.Fatal(got)
	}
	err := &sessionCapacityError{used: 1, limit: 1}
	if err.Error() != errSessionLimit.Error() || !errors.Is(err, errSessionLimit) {
		t.Fatalf("capacity error lost identity: %v", err)
	}
}
