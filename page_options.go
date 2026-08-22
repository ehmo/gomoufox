package gomoufox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ehmo/gomoufox/internal/pwbridge"
)

type GotoOption func(*gotoConfig)
type NavigateOption func(*navigateConfig)
type DownloadOption func(*downloadConfig)

type gotoConfig struct {
	waitUntil string
	referer   string
	timeout   time.Duration
}

type navigateConfig struct {
	waitUntil string
	timeout   time.Duration
}

type downloadConfig struct {
	timeout time.Duration
}

func WaitUntil(state string) GotoOption      { return func(c *gotoConfig) { c.waitUntil = state } }
func WithReferer(referer string) GotoOption  { return func(c *gotoConfig) { c.referer = referer } }
func WithTimeout(d time.Duration) GotoOption { return func(c *gotoConfig) { c.timeout = d } }
func NavigateWaitUntil(state string) NavigateOption {
	return func(c *navigateConfig) { c.waitUntil = state }
}
func NavigateTimeout(d time.Duration) NavigateOption {
	return func(c *navigateConfig) { c.timeout = d }
}
func DownloadTimeout(d time.Duration) DownloadOption {
	return func(c *downloadConfig) { c.timeout = d }
}

func buildGotoConfig(opts ...GotoOption) gotoConfig {
	cfg := gotoConfig{waitUntil: "load"}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func buildNavigateConfig(opts ...NavigateOption) navigateConfig {
	cfg := navigateConfig{waitUntil: "load"}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func buildDownloadConfig(opts ...DownloadOption) downloadConfig {
	var cfg downloadConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func (c gotoConfig) toBridge(ctx context.Context) pwbridge.GotoOptions {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = deadlineTimeout(ctx, 30*time.Second)
	}
	return pwbridge.GotoOptions{WaitUntil: c.waitUntil, Referer: c.referer, Timeout: timeout}
}

func (c navigateConfig) toBridge(ctx context.Context) pwbridge.NavigateOptions {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = deadlineTimeout(ctx, 30*time.Second)
	}
	return pwbridge.NavigateOptions{WaitUntil: c.waitUntil, Timeout: timeout}
}

func (c downloadConfig) toBridge(ctx context.Context) pwbridge.DownloadOptions {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = deadlineTimeout(ctx, 30*time.Second)
	}
	return pwbridge.DownloadOptions{Timeout: timeout}
}

type WaitForSelectorOption func(*waitForSelectorConfig)
type waitForSelectorConfig struct {
	timeout time.Duration
	state   string
}

func WaitForSelectorTimeout(d time.Duration) WaitForSelectorOption {
	return func(c *waitForSelectorConfig) { c.timeout = d }
}

func WaitForSelectorState(state string) WaitForSelectorOption {
	return func(c *waitForSelectorConfig) { c.state = state }
}

type ScreenshotOption func(*screenshotConfig)
type screenshotConfig struct {
	fullPage bool
	typ      string
	quality  int
	clip     *pwbridge.Rect
}

func FullPage(full bool) ScreenshotOption { return func(c *screenshotConfig) { c.fullPage = full } }
func ScreenshotType(format string) ScreenshotOption {
	return func(c *screenshotConfig) { c.typ = format }
}
func JPEGQuality(quality int) ScreenshotOption {
	return func(c *screenshotConfig) { c.quality = quality }
}
func Clip(x, y, width, height float64) ScreenshotOption {
	return func(c *screenshotConfig) {
		c.clip = &pwbridge.Rect{X: x, Y: y, Width: width, Height: height}
	}
}

func (c screenshotConfig) toBridge() pwbridge.ScreenshotOptions {
	return pwbridge.ScreenshotOptions{FullPage: c.fullPage, Type: c.typ, Quality: c.quality, Clip: c.clip}
}

type PDFOption func(*pdfConfig)
type pdfConfig struct{ format string }

func PDFFormat(format string) PDFOption { return func(c *pdfConfig) { c.format = format } }

func deadlineTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 {
			return d
		}
		return time.Nanosecond
	}
	return fallback
}

func mapNavigationError(err error) error {
	if err == nil {
		return nil
	}
	if isFilteringProxyForbidden(err) {
		return fmt.Errorf("%w: filtering proxy denied request", ErrURLBlocked)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrNavigationTimeout, err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return fmt.Errorf("%w: %v", ErrNavigationTimeout, err)
	}
	return err
}

func isFilteringProxyForbidden(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NS_ERROR_PROXY_FORBIDDEN")
}

func mapDownloadError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	return err
}
