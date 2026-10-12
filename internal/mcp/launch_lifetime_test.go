package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/ehmo/gomoufox"
)

// HTTP transport cancels each request ctx when the response is sent. The
// browser launched during that request must outlive it.
func TestLaunchedBrowserOutlivesRequest(t *testing.T) {
	old := newGomoufoxForMCP
	t.Cleanup(func() { newGomoufoxForMCP = old })
	var launchCtx context.Context
	newGomoufoxForMCP = func(ctx context.Context, _ ...gomoufox.Option) (*gomoufox.Browser, error) {
		launchCtx = ctx
		return closeableBrowser(), nil
	}

	request, finish := context.WithCancel(context.Background())
	browser, err := (realGomoufoxLauncher{}).Launch(request, sessionOptions{headful: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if err := launchCtx.Err(); err != nil {
		t.Fatalf("browser lifetime ctx ended with the request: %v", err)
	}

	if err := browser.Close(); err != nil {
		t.Fatal(err)
	}
	if launchCtx.Err() == nil {
		t.Fatal("browser lifetime ctx still open after Close")
	}
}

func closeableBrowser() *gomoufox.Browser {
	browser := &gomoufox.Browser{}
	setField(browser, "done", make(chan struct{}))
	return browser
}

func TestLaunchStopsWhenRequestEndsDuringStartup(t *testing.T) {
	old := newGomoufoxForMCP
	t.Cleanup(func() { newGomoufoxForMCP = old })
	request, finish := context.WithCancel(context.Background())
	newGomoufoxForMCP = func(ctx context.Context, _ ...gomoufox.Option) (*gomoufox.Browser, error) {
		finish()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := (realGomoufoxLauncher{}).Launch(request, sessionOptions{}, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	// A launch that completes after the request ended is discarded.
	request, finish = context.WithCancel(context.Background())
	var launchCtx context.Context
	newGomoufoxForMCP = func(ctx context.Context, _ ...gomoufox.Option) (*gomoufox.Browser, error) {
		launchCtx = ctx
		finish()
		<-ctx.Done()
		return closeableBrowser(), nil
	}
	if _, err := (realGomoufoxLauncher{}).Launch(request, sessionOptions{}, false); !errors.Is(err, context.Canceled) || launchCtx.Err() == nil {
		t.Fatalf("late launch err = %v lifetime err = %v", err, launchCtx.Err())
	}

	boom := errors.New("boom")
	newGomoufoxForMCP = func(context.Context, ...gomoufox.Option) (*gomoufox.Browser, error) { return nil, boom }
	if _, err := (realGomoufoxLauncher{}).Launch(context.Background(), sessionOptions{}, false); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}
