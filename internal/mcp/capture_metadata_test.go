package mcp

import (
	"bytes"
	"context"
	"github.com/ehmo/gomoufox/internal/renderinfo"
	"image"
	"image/png"
	"testing"
)

type captureMetadataPage struct{ *fakeMCPPage }

func (p captureMetadataPage) EvaluateInternal(ctx context.Context, expression string, args ...any) (any, error) {
	if expression == renderinfo.Expression {
		return map[string]any{"width": 390, "height": 844, "device_pixel_ratio": 2}, nil
	}
	return p.fakeMCPPage.EvaluateInternal(ctx, expression, args...)
}

func TestMCPScreenshotUsesEncodedDimensions(t *testing.T) {
	var data bytes.Buffer
	_ = png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 136, 74)))
	session := &gomoufoxSession{page: captureMetadataPage{&fakeMCPPage{screenshotData: data.Bytes()}}, requestedViewport: &renderinfo.Size{Width: 390, Height: 844}}
	shot, err := session.Screenshot(context.Background(), screenshotOptions{FullPage: true})
	if err != nil || shot.Width != 136 || shot.Height != 74 || shot.Rendering == nil || shot.Rendering.Width != 390 || shot.Rendering.DevicePixelRatio != 2 || shot.RequestedViewport.Width != 390 {
		t.Fatalf("shot=%+v err=%v", shot, err)
	}
	cfg := defaultTestConfig(t)
	cfg.BrowserFactory = &fakeBrowserFactory{session: &fakeBrowserSession{screenshotResult: shot}}
	server := newTestServer(t, cfg)
	response := server.Handle(context.Background(), "browser_screenshot", raw(`{}`))
	if response.IsError || response.Payload["width"] != 136 || response.Payload["rendering"] == nil || response.Payload["requested_viewport"] == nil {
		t.Fatalf("response=%+v", response)
	}
}
