package pwbridge

import (
	"testing"
	"time"
)

func TestScreenshotTimeoutReachesDriver(t *testing.T) {
	for _, timeout := range []time.Duration{500 * time.Millisecond, 25 * time.Second} {
		opts := ScreenshotOptions{Timeout: timeout}
		page := toPWScreenshotOptions(opts)
		locator := toPWLocatorScreenshotOptions(opts)
		want := float64(timeout.Milliseconds())
		if page.Timeout == nil || *page.Timeout != want || locator.Timeout == nil || *locator.Timeout != want {
			t.Fatalf("timeout %s: page=%+v locator=%+v", timeout, page.Timeout, locator.Timeout)
		}
	}
}
