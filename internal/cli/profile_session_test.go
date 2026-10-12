package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ehmo/gomoufox"
)

// Firefox drops session cookies (no expiry) on restart, so a login kept only
// in a --profile does not survive. open must say so when it sees them.
func TestOpenProfileWarnsAboutSessionCookies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		cookies []gomoufox.Cookie
		warn    bool
	}{
		{name: "session cookie", profile: "/profile", cookies: []gomoufox.Cookie{{Name: "sid", Expires: -1}, {Name: "pref", Expires: 4102444800}}, warn: true},
		{name: "persistent cookies only", profile: "/profile", cookies: []gomoufox.Cookie{{Name: "pref", Expires: 4102444800}}},
		{name: "no profile", cookies: []gomoufox.Cookie{{Name: "sid", Expires: -1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := &fakeLocalPage{url: "https://example.com", snapshot: &gomoufox.StorageState{Cookies: tc.cookies}}
			t.Cleanup(fakeOpenPage(t, page))
			resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
				Command: "open",
				Args:    []string{"https://example.com"},
				Flags:   map[string]any{},
				Profile: tc.profile,
			})
			if err != nil {
				t.Fatal(err)
			}
			warned := strings.Contains(resp.Stderr, "1 session cookie") && strings.Contains(resp.Stderr, "--save-session")
			if warned != tc.warn {
				t.Fatalf("stderr = %q, want warning %v", resp.Stderr, tc.warn)
			}
		})
	}
}

func TestOpenProfileWithoutSnapshot(t *testing.T) {
	req := LocalCommandRequest{Command: "open", Args: []string{"https://example.com"}, Flags: map[string]any{}, Profile: "/profile"}
	page := &fakeLocalPage{url: "https://example.com", snapshotErr: errors.New("closed")}
	t.Cleanup(fakeOpenPage(t, page))
	resp, err := defaultLocalCommand(context.Background(), req)
	if err != nil || resp.Stderr != "" {
		t.Fatalf("stderr = %q err = %v", resp.Stderr, err)
	}

	page = &fakeLocalPage{url: "https://example.com", waitClosedErr: errors.New("wait failed")}
	t.Cleanup(fakeOpenPage(t, page))
	if _, err := defaultLocalCommand(context.Background(), req); err == nil || err.Error() != "wait failed" {
		t.Fatalf("err = %v", err)
	}
}

// The snapshot repeats while the window stays open, so a login completed
// after navigation is still seen.
func TestOpenSnapshotsWhileWindowOpen(t *testing.T) {
	old := sessionSnapshotInterval
	sessionSnapshotInterval = time.Millisecond
	t.Cleanup(func() { sessionSnapshotInterval = old })

	page := &fakeLocalPage{
		url:            "https://example.com",
		snapshot:       &gomoufox.StorageState{Cookies: []gomoufox.Cookie{{Name: "sid", Expires: -1}}},
		waitClosedFunc: func() { time.Sleep(20 * time.Millisecond) },
	}
	t.Cleanup(fakeOpenPage(t, page))
	resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{Command: "open", Args: []string{"https://example.com"}, Flags: map[string]any{}, Profile: "/profile"})
	if err != nil || !strings.Contains(resp.Stderr, "1 session cookie") {
		t.Fatalf("stderr = %q err = %v", resp.Stderr, err)
	}
}
