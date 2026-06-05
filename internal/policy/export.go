package policy

import (
	"errors"
	"fmt"
)

var (
	ErrCookieValuesDisabled  = errors.New("cookie values disabled")
	ErrSessionExportDisabled = errors.New("session export disabled")
	ErrSessionTooLarge       = errors.New("session state too large")
)

func CookieValuesAllowed(cfg Config, includeValues bool) (bool, error) {
	if !includeValues {
		return false, nil
	}
	if !cfg.AllowCookieValues {
		return false, ErrCookieValuesDisabled
	}
	return true, nil
}

func InlineSessionExportAllowed(cfg Config, includeState bool, stateBytes int) (bool, error) {
	if !cfg.AllowSessionExport || !includeState {
		return false, ErrSessionExportDisabled
	}
	if stateBytes < 0 {
		return false, fmt.Errorf("%w: negative size", ErrSessionTooLarge)
	}
	if stateBytes > InlineSessionStateBytes {
		return false, fmt.Errorf("%w: %d exceeds %d", ErrSessionTooLarge, stateBytes, InlineSessionStateBytes)
	}
	return true, nil
}
