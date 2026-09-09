package gomoufox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type pendingEvaluationPage struct {
	fakePage
	closed   chan struct{}
	started  chan struct{}
	finished chan struct{}
	once     sync.Once
}

func (p *pendingEvaluationPage) Evaluate(string, ...any) (any, error) {
	close(p.started)
	<-p.closed
	close(p.finished)
	return nil, errors.New("page closed")
}
func (p *pendingEvaluationPage) EvaluateInternal(s string, arg ...any) (any, error) {
	return p.Evaluate(s, arg...)
}
func (p *pendingEvaluationPage) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func TestEvaluationDeadlineStopsPendingWork(t *testing.T) {
	for _, internal := range []bool{false, true} {
		raw := &pendingEvaluationPage{closed: make(chan struct{}), started: make(chan struct{}), finished: make(chan struct{})}
		page := &Page{raw: raw}
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-raw.started; cancel() }()
		// Release the old synchronous implementation so the failing test is bounded.
		safety := time.AfterFunc(time.Second, func() { _ = raw.Close() })
		var err error
		if internal {
			_, err = page.EvaluateInternal(ctx, "pending")
		} else {
			_, err = page.Evaluate(ctx, "pending")
		}
		safety.Stop()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("evaluation returned %v", err)
		}
		select {
		case <-raw.finished:
		case <-time.After(time.Second):
			t.Fatal("evaluation did not stop after cancellation")
		}
	}
}

func TestEvaluationReturnsCompletedValueBeforeDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := &fakePage{evaluateResult: "completed"}
	page := &Page{raw: raw}
	value, err := page.Evaluate(ctx, "'completed'")
	if err != nil || value != "completed" || raw.closeCalls != 0 {
		t.Fatalf("completed evaluation = %v, %v; closes=%d", value, err, raw.closeCalls)
	}
}

func TestFetchPreservesEvaluationCancellation(t *testing.T) {
	page := &Page{raw: &fakePage{evaluateErr: context.Canceled}}
	_, err := page.FetchBytesWithOptions(context.Background(), "https://example.com", "POST", nil, []byte("body"), FetchBytesOptions{})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrBrowserFetch) {
		t.Fatalf("fetch cancellation = %v", err)
	}
}
