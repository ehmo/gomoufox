package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ehmo/gomoufox/internal/a11y"
)

type failedNavigationSession struct{ fakeBrowserSession }

type recoveryBrowserFactory struct{ session browserSession }

func (f recoveryBrowserFactory) NewBrowserSession(context.Context, sessionOptions) (browserSession, error) {
	return f.session, nil
}

func (s *failedNavigationSession) Navigate(context.Context, string, navigateOptions) (navigateResult, error) {
	return s.navigateResult, s.navigateErr
}

func TestNavigationFailureReportsRetainedSession(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, errors.New("Connection closed")} {
		session := &failedNavigationSession{fakeBrowserSession{navigateResult: navigateResult{URL: "https://example.com/ready?token=private-value#private-fragment"}, navigateErr: failure}}
		cfg := defaultTestConfig(t)
		cfg.Validator = &fakeValidator{}
		cfg.BrowserFactory = recoveryBrowserFactory{session}
		server := newTestServer(t, cfg)
		resp := server.Handle(context.Background(), "browser_navigate", raw(`{"session_id":"work","url":"https://example.com","wait_until":"networkidle"}`))
		if !resp.IsError || resp.Payload["session_id"] != "work" || resp.Payload["phase"] != "navigation" {
			t.Fatalf("navigation failure = %#v", resp)
		}
		encoded, _ := json.Marshal(resp.Payload)
		if strings.Contains(string(encoded), "private-") {
			t.Fatalf("diagnostic URL leaked values: %s", encoded)
		}
		if failure == context.DeadlineExceeded {
			if resp.Payload["session_retained"] != true || resp.Payload["next_tool"] != "browser_snapshot" {
				t.Fatalf("missing recovery: %#v", resp.Payload)
			}
		} else if resp.Payload["next_tool"] == "browser_snapshot" {
			t.Fatalf("closed page offered snapshot: %#v", resp.Payload)
		}
	}
}

func TestNavigationFailureKeepsCurrentURL(t *testing.T) {
	for _, page := range []*fakeMCPPage{
		{url: "https://example.com/ready", gotoErr: context.DeadlineExceeded},
		{url: "https://example.com/ready", titleErr: context.DeadlineExceeded},
	} {
		session := &gomoufoxSession{page: page, refs: a11y.NewStore()}
		nodes := []a11y.Element{{Role: "button", Name: "Old page"}}
		session.refs.Capture(page.url, nodes)
		result, err := session.Navigate(context.Background(), "https://example.com", navigateOptions{Timeout: time.Second})
		if !errors.Is(err, context.DeadlineExceeded) || result.URL != page.url {
			t.Fatalf("partial navigation = %#v, %v", result, err)
		}
		if _, err := session.refs.Resolve("e1", nodes); !errors.Is(err, a11y.ErrUnknownRef) {
			t.Fatalf("old reference survived navigation: %v", err)
		}
	}
}
