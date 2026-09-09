package gomoufox

import (
	"context"
	"testing"
	"time"
)

func TestOperationTimeoutCannotExtendCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	for _, value := range []time.Duration{0, time.Second, 10 * time.Second} {
		got := boundTimeoutToContext(ctx, value)
		if got <= 0 || got > 500*time.Millisecond {
			t.Fatalf("requested %s exceeded parent: %s", value, got)
		}
	}
	if got := boundTimeoutToContext(ctx, 100*time.Millisecond); got != 100*time.Millisecond {
		t.Fatalf("shorter timeout changed: %s", got)
	}
	if got := boundTimeoutToContext(context.Background(), time.Second); got != time.Second {
		t.Fatal(got)
	}
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	if got := boundTimeoutToContext(expired, time.Second); got != time.Nanosecond {
		t.Fatal(got)
	}
}

func TestSelectorAndCaptureUseRemainingBudget(t *testing.T) {
	raw := &fakePage{}
	page := &Page{raw: raw}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if _, err := page.WaitForSelector(ctx, "#late"); err != nil {
		t.Fatal(err)
	}
	if got := raw.waitSelectorOpts.Timeout; got <= 10*time.Second || got > 25*time.Second {
		t.Fatalf("wait used fixed budget: %s", got)
	}
	if _, err := page.WaitForSelector(ctx, "#late", WaitForSelectorTimeout(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := raw.waitSelectorOpts.Timeout; got <= 0 || got > 25*time.Second {
		t.Fatalf("explicit wait exceeded parent: %s", got)
	}
	if _, err := page.Screenshot(ctx); err != nil {
		t.Fatal(err)
	}
	if got := raw.screenshotOpts.Timeout; got <= 10*time.Second || got > 25*time.Second {
		t.Fatalf("capture used fixed budget: %s", got)
	}
}
