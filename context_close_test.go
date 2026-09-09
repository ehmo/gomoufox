package gomoufox

import (
	"context"
	"errors"
	"testing"
)

type orderedCloseSession struct {
	fakeSession
	events *[]string
}

func (s *orderedCloseSession) Stop() error {
	*s.events = append(*s.events, "session")
	return s.stopErr
}

type orderedCloseSidecar struct {
	fakeSidecar
	events *[]string
}

func (s *orderedCloseSidecar) Stop(context.Context) { *s.events = append(*s.events, "sidecar") }

func TestPersistentContextCloseUsesOwningBrowserShutdown(t *testing.T) {
	old := gracefulSidecarStopSupported
	defer func() { gracefulSidecarStopSupported = old }()
	for _, graceful := range []bool{false, true} {
		gracefulSidecarStopSupported = graceful
		events := []string{}
		failure := errors.New("fixture stop failed")
		b := &Browser{cfg: launchConfig{persistentCtx: true}, done: make(chan struct{}), session: &orderedCloseSession{events: &events, fakeSession: fakeSession{stopErr: failure}}, sidecar: &orderedCloseSidecar{events: &events}}
		raw := &fakeContext{closeErr: errors.New("must not issue another protocol close")}
		c := &Context{browser: b, raw: raw}
		if err := c.Close(); !errors.Is(err, failure) {
			t.Fatalf("close=%v", err)
		}
		first := "session"
		if graceful {
			first = "sidecar"
		}
		if len(events) != 2 || events[0] != first {
			t.Fatalf("graceful=%v order=%v", graceful, events)
		}
		if err := c.Abort(); !errors.Is(err, failure) || len(events) != 2 {
			t.Fatalf("second shutdown err=%v calls=%v", err, events)
		}
	}
	c := &Context{raw: &fakeContext{closeErr: errors.New("Connection closed")}}
	if c.Close() == nil {
		t.Fatal("unowned context error hidden")
	}
}
