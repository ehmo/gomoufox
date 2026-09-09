package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/renderinfo"
	"image"
	"image/png"
	"path/filepath"
	"testing"
)

type captureMetadataPage struct {
	*fakeLocalPage
	metricsErr error
}

func (p *captureMetadataPage) RenderingInfo(context.Context, string) (*renderinfo.Metrics, error) {
	return &renderinfo.Metrics{Width: 390, Height: 844, DevicePixelRatio: 1}, p.metricsErr
}

func TestScreenshotReportsCaptureAndViewportSeparately(t *testing.T) {
	var imageData bytes.Buffer
	_ = png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 17, 43)))
	page := &captureMetadataPage{fakeLocalPage: &fakeLocalPage{screenshot: imageData.Bytes()}}
	original := openPageForLocal
	t.Cleanup(func() { openPageForLocal = original })
	openPageForLocal = func(context.Context, LocalCommandRequest, []gomoufox.ContextOption) (localPage, func(), error) {
		return page, func() {}, nil
	}
	for _, metricsErr := range []error{nil, errors.New("unavailable")} {
		page.metricsErr = metricsErr
		response, err := localScreenshot(context.Background(), LocalCommandRequest{Args: []string{"https://example.com"}, Flags: map[string]any{"out": filepath.Join(t.TempDir(), "clip.png"), "width": "390", "height": "844"}, JSON: true})
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Width, Height int
			Requested     renderinfo.Size `json:"requested_viewport"`
			Rendering     *renderinfo.Metrics
			Status        string `json:"rendering_status"`
		}
		if err := json.Unmarshal(response.Stdout, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Width != 17 || payload.Height != 43 || payload.Requested.Width != 390 || payload.Requested.Height != 844 {
			t.Fatalf("dimensions: %s", response.Stdout)
		}
		if metricsErr == nil && (payload.Rendering == nil || payload.Rendering.Width != 390) {
			t.Fatalf("metrics: %s", response.Stdout)
		}
		if metricsErr != nil && payload.Status != "unavailable" {
			t.Fatalf("missing status: %s", response.Stdout)
		}
	}
	if _, err := (realLocalPage{p: &fakeGomoufoxPage{}}).RenderingInfo(context.Background(), ""); err == nil {
		t.Fatal("unsupported metrics accepted")
	}
}

type internalMetricsPage struct{ *fakeGomoufoxPage }

func (p internalMetricsPage) EvaluateInternal(ctx context.Context, script string, args ...any) (any, error) {
	return p.Evaluate(ctx, script, args...)
}

func TestRealLocalPagePreservesContentFailuresAndInternalMetrics(t *testing.T) {
	failure := errors.New("page content unavailable")
	page := &fakeGomoufoxPage{contentErr: failure}
	if _, err := (realLocalPage{p: page}).BodyText(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("content error = %v", err)
	}
	page.evaluateValue = map[string]any{"width": 390, "height": 844, "device_pixel_ratio": 2}
	got, err := (realLocalPage{p: internalMetricsPage{page}}).RenderingInfo(context.Background(), "linux")
	if err != nil || got.Width != 390 || got.RequestedOS != "linux" || page.evaluateScript != renderinfo.Expression {
		t.Fatalf("metrics = %#v, %v", got, err)
	}
	if _, err := contextOptionsForLocal(LocalCommandRequest{Flags: map[string]any{"cookies_file": filepath.Join(t.TempDir(), "missing.json")}}, nil); err == nil {
		t.Fatal("unreadable storage state accepted")
	}
	if opts, err := contextOptionsForLocal(LocalCommandRequest{Flags: map[string]any{"width": "390", "height": "844"}}, nil); err != nil || len(opts) != 1 {
		t.Fatalf("viewport option missing: %d %v", len(opts), err)
	}
}
