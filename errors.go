package gomoufox

import (
	"errors"
	"fmt"
)

var (
	ErrSidecarStart           = errors.New("gomoufox: sidecar failed to start")
	ErrVersionMismatch        = errors.New("gomoufox: playwright protocol version mismatch")
	ErrConnect                = errors.New("gomoufox: failed to connect to sidecar WebSocket")
	ErrTimeout                = errors.New("gomoufox: operation timed out")
	ErrNotInstalled           = errors.New("gomoufox: camoufox not installed; call EnsureInstalled or use WithAutoInstall(true)")
	ErrSidecarDied            = errors.New("gomoufox: sidecar process exited unexpectedly")
	ErrNavigationTimeout      = errors.New("gomoufox: navigation timed out")
	ErrElementNotFound        = errors.New("gomoufox: element not found")
	ErrSessionClosed          = errors.New("gomoufox: session closed")
	ErrURLBlocked             = errors.New("gomoufox: url blocked by guardrail")
	ErrBrowserFetch           = errors.New("gomoufox: browser fetch failed")
	ErrPersistentContextLimit = errors.New("gomoufox: persistent context limit reached")
)

// BrowserFetchError is returned when in-browser fetch fails before gomoufox can
// expose a successful response.
type BrowserFetchError struct {
	Code        string
	URL         string
	Method      string
	Status      int
	BodyPreview []byte
	Message     string
}

func (e *BrowserFetchError) Error() string {
	if e == nil {
		return ErrBrowserFetch.Error()
	}
	msg := e.Message
	if msg == "" {
		msg = "browser fetch was blocked before response body was readable"
	}
	if e.Status != 0 {
		return fmt.Sprintf("%s: %s %s: %s (status %d)", ErrBrowserFetch, e.Method, e.URL, msg, e.Status)
	}
	return fmt.Sprintf("%s: %s %s: %s", ErrBrowserFetch, e.Method, e.URL, msg)
}

func (e *BrowserFetchError) Unwrap() error {
	return ErrBrowserFetch
}
