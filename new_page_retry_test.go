package gomoufox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ehmo/gomoufox/internal/pwbridge"
)

func TestContextNewPageRetriesMissingWindow(t *testing.T) {
	windowErr := errors.New("playwright: Protocol error (Browser.newPage): window is null")
	attempts := 0
	raw := &fakeContext{newPageFunc: func() (pwbridge.Page, error) {
		attempts++
		if attempts < 3 {
			return nil, windowErr
		}
		return &fakePage{}, nil
	}}
	c := &Context{raw: raw}
	page, err := c.NewPage(context.Background())
	if err != nil || page == nil {
		t.Fatalf("NewPage = %v, %v", page, err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestContextNewPageMissingWindowStopsAtDeadline(t *testing.T) {
	windowErr := errors.New("playwright: Protocol error (Browser.newPage): window is null")
	attempts := 0
	raw := &fakeContext{newPageFunc: func() (pwbridge.Page, error) {
		attempts++
		return nil, windowErr
	}}
	c := &Context{raw: raw}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.NewPage(ctx); !errors.Is(err, windowErr) {
		t.Fatalf("NewPage err = %v, want the window error", err)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want a retry before the deadline", attempts)
	}
}

func TestContextNewPageDoesNotRetryOtherErrors(t *testing.T) {
	boom := errors.New("boom")
	attempts := 0
	raw := &fakeContext{newPageFunc: func() (pwbridge.Page, error) {
		attempts++
		return nil, boom
	}}
	c := &Context{raw: raw}
	if _, err := c.NewPage(context.Background()); !errors.Is(err, boom) || attempts != 1 {
		t.Fatalf("NewPage err = %v attempts = %d", err, attempts)
	}
}
