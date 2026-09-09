package renderinfo

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestCaptureDimensionsComeFromEncodedImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 17, 43))
	for _, format := range []string{"png", "jpeg"} {
		var data bytes.Buffer
		if format == "png" {
			_ = png.Encode(&data, img)
		} else {
			_ = jpeg.Encode(&data, img, nil)
		}
		size, err := ImageSize(data.Bytes())
		if err != nil || size.Width != 17 || size.Height != 43 {
			t.Fatalf("%s: %+v %v", format, size, err)
		}
	}
	if _, err := ImageSize([]byte("invalid")); err == nil {
		t.Fatal("invalid image accepted")
	}
}

func TestRenderingMetricsKeepFontCoverageUnverified(t *testing.T) {
	evaluate := func(context.Context, string, ...any) (any, error) {
		return map[string]any{"width": 390, "height": 844, "device_pixel_ratio": 2, "browser_platform": "MacIntel", "font_set_status": "loaded"}, nil
	}
	metrics, err := Inspect(context.Background(), evaluate, "linux")
	if err != nil || metrics.Width != 390 || metrics.DevicePixelRatio != 2 || metrics.RequestedOS != "linux" || metrics.GlyphCoverage != "not_verified" || metrics.HostOS == "" {
		t.Fatalf("metrics=%+v err=%v", metrics, err)
	}
	metrics, err = Inspect(context.Background(), evaluate, "")
	if err != nil || metrics.RequestedOS != "auto" {
		t.Fatalf("default=%+v err=%v", metrics, err)
	}
	for _, value := range []any{nil, "bad", func() {}} {
		_, err := Inspect(context.Background(), func(context.Context, string, ...any) (any, error) { return value, nil }, "")
		if err == nil {
			t.Fatalf("invalid metrics accepted: %#v", value)
		}
	}
	boom := errors.New("closed")
	if _, err := Inspect(context.Background(), func(context.Context, string, ...any) (any, error) { return nil, boom }, ""); !errors.Is(err, boom) {
		t.Fatal(err)
	}
}
