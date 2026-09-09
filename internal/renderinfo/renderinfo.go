package renderinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"runtime"
)

type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type FontFace struct {
	Family string `json:"family"`
	Status string `json:"status"`
}

type Metrics struct {
	Width              int        `json:"width"`
	Height             int        `json:"height"`
	DevicePixelRatio   float64    `json:"device_pixel_ratio"`
	BrowserPlatform    string     `json:"browser_platform"`
	BodyFontFamily     string     `json:"body_font_family"`
	FontSetStatus      string     `json:"font_set_status"`
	FontFaces          []FontFace `json:"font_faces"`
	FontFacesTruncated bool       `json:"font_faces_truncated"`
	HostOS             string     `json:"host_os"`
	RequestedOS        string     `json:"requested_os"`
	GlyphCoverage      string     `json:"glyph_coverage"`
}

const Expression = `() => {
 const faces = [];
 let count = 0;
 if (document.fonts) for (const face of document.fonts) {
   count++;
   if (count > 32) break;
   faces.push({family:String(face.family).slice(0,256), status:face.status});
 }
 return {width:innerWidth,height:innerHeight,device_pixel_ratio:devicePixelRatio,
 browser_platform:String(navigator.platform).slice(0,128),
 body_font_family:document.body ? getComputedStyle(document.body).fontFamily.slice(0,512) : '',
 font_set_status:document.fonts ? document.fonts.status : 'unavailable',
 font_faces:faces,font_faces_truncated:count > faces.length};
}`

func Inspect(ctx context.Context, evaluate func(context.Context, string, ...any) (any, error), requestedOS string) (*Metrics, error) {
	value, err := evaluate(ctx, Expression)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var metrics Metrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		return nil, err
	}
	if metrics.Width <= 0 || metrics.Height <= 0 || metrics.DevicePixelRatio <= 0 {
		return nil, errors.New("rendering metrics unavailable")
	}
	if requestedOS == "" {
		requestedOS = "auto"
	}
	metrics.HostOS = runtime.GOOS
	metrics.RequestedOS = requestedOS
	metrics.GlyphCoverage = "not_verified"
	return &metrics, nil
}

// ImageSize reads encoded dimensions without decoding the pixel buffer.
func ImageSize(data []byte) (Size, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	return Size{Width: config.Width, Height: config.Height}, err
}
