package mcp

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/a11y"
	"github.com/ehmo/gomoufox/internal/policy"
)

func browserError(err error) Response { return browserFailure("browser_error", err, "") }

// Only fixed descriptions leave this boundary. Driver errors may contain page
// text, selectors, scripts, credentials and local filesystem paths.
func browserFailure(code string, err error, phase string) Response {
	reason, message, hint := classifyBrowserError(err)
	if phase == "launch" && reason == "unknown" {
		reason = "launch"
		message = "The browser could not start."
		hint = "Run gomoufox doctor and check the runtime and profile owner before retrying."
	}
	if phase == "evaluate" && reason == "unknown" {
		reason = "script"
		message = "JavaScript evaluation failed."
		hint = "Check the expression and arg binding; inspect the page state before repeating side effects."
	}
	response := mcpError(code)
	response.Payload["reason"] = reason
	response.Payload["message"] = message
	response.Payload["hint"] = hint
	if phase != "" {
		response.Payload["phase"] = phase
	}
	return response
}

func classifyBrowserError(err error) (reason, message, hint string) {
	text := ""
	if err != nil {
		text = strings.ToLower(err.Error())
	}
	var network net.Error
	var fetch *gomoufox.BrowserFetchError
	if errors.As(err, &fetch) && fetch != nil {
		switch fetch.Code {
		case "cors_denied", "csp_blocked", "mixed_content_blocked":
			return fetch.Code, "The browser reported an origin or resource policy failure.", "Inspect the page origin and permitted resource flow. Use an authorized site configuration or supported endpoint; do not disable browser security to retry."
		}
	}
	switch {
	case errors.Is(err, gomoufox.ErrURLBlocked):
		return "network_policy", "The request was rejected by the network policy.", policy.NetworkBlockHint
	case errors.Is(err, gomoufox.ErrEvaluationInterrupted):
		return "evaluation_interrupted", "Evaluation exceeded its deadline or was canceled; the page was closed to stop pending work.", "Destroy this session and create a new one. Inspect persisted state before repeating side effects."
	case errors.Is(err, gomoufox.ErrNotInstalled), errors.Is(err, gomoufox.ErrVersionMismatch), strings.Contains(text, "executable doesn't exist"), strings.Contains(text, "executable does not exist"):
		return "runtime", "The browser runtime is missing or incompatible.", "Run gomoufox doctor; use gomoufox install to repair the reported runtime assets."
	case strings.Contains(text, "profile is in use"), strings.Contains(text, "profile in use"), strings.Contains(text, "profile cannot be loaded"), strings.Contains(text, "parent.lock"), strings.Contains(text, "profile is already in use"):
		return "profile_in_use", "The browser profile is unavailable or owned by another process.", "Close the process that owns this profile, or use a different profile. Keep its lock files intact."
	case errors.Is(err, gomoufox.ErrSidecarStart), errors.Is(err, gomoufox.ErrConnect), errors.Is(err, gomoufox.ErrSidecarDied):
		return "launch", "The browser process or connection failed.", "Run gomoufox doctor and check the runtime before creating a new session."
	case errors.Is(err, gomoufox.ErrSessionClosed), strings.Contains(text, "target page, context or browser has been closed"), strings.Contains(text, "connection closed"), strings.Contains(text, "browser has been closed"), strings.Contains(text, "window is null"):
		return "closed", "The page or browser session is closed.", "List sessions, then create or select an active session before continuing."
	case errors.Is(err, a11y.ErrStaleRef), errors.Is(err, a11y.ErrUnknownRef):
		return "stale_ref", "The element reference is no longer valid.", "Take a fresh browser_snapshot and use a reference from that snapshot."
	case errors.Is(err, a11y.ErrAmbiguousRef), strings.Contains(text, "strict mode violation"):
		return "ambiguous_selector", "The target matches more than one element.", "Take a fresh snapshot and choose one element, or narrow the selector."
	case strings.Contains(text, "frame was detached"), strings.Contains(text, "frame has been detached"), strings.Contains(text, "frame not found"):
		return "frame", "The target frame changed or is unavailable.", "Inspect the page after the frame finishes loading, then resolve the target again."
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, gomoufox.ErrTimeout), errors.Is(err, gomoufox.ErrNavigationTimeout), errors.As(err, &network) && network.Timeout(), strings.Contains(text, "timeout"), strings.Contains(text, "timed out"):
		return "timeout", "The operation did not finish before its deadline.", "Inspect the current page before retrying. Wait for a specific element or increase timeout_ms within the tool's limit."
	case errors.Is(err, context.Canceled):
		return "canceled", "The operation was canceled.", "Inspect current session state before starting another operation."
	case errors.Is(err, gomoufox.ErrElementNotFound), strings.Contains(text, "not a valid selector"), strings.Contains(text, "unknown engine"), strings.Contains(text, "unexpected token while parsing selector"), strings.Contains(text, "no element matches"):
		return "selector", "The target selector could not be resolved.", "Take a fresh snapshot and verify the selector and frame before retrying."
	case strings.Contains(text, "ns_error_"), strings.Contains(text, "net::err_"), strings.Contains(text, "connection refused"), strings.Contains(text, "no such host"):
		return "network", "The browser could not reach the target.", "Check target reachability and the configured proxy. Keep the current network policy in place."
	case strings.Contains(text, "typeerror"), strings.Contains(text, "syntaxerror"), strings.Contains(text, "referenceerror"):
		return "script", "JavaScript evaluation failed.", "Check the expression and arg binding; inspect the page state before repeating side effects."
	default:
		return "unknown", "The browser operation failed.", "Inspect the current page and diagnostic tools before retrying; do not replay completed actions blindly."
	}
}
