package gomoufox

import (
	"github.com/ehmo/gomoufox/internal/harcapture"
	"github.com/ehmo/gomoufox/internal/pwbridge"
)

const (
	DefaultHARMaxBytes = harcapture.DefaultMaxBytes
	HardHARMaxBytes    = harcapture.HardMaxBytes
)

type HARCapture string

const (
	HARCaptureMetadata HARCapture = "metadata"
	HARCaptureFull     HARCapture = "full"
)

type HAROptions struct {
	Path      string
	Capture   HARCapture
	URLFilter string
	MaxBytes  int64
	Overwrite bool
}

type HARResult struct {
	Path            string
	Capture         HARCapture
	Bytes           int64
	Entries         int
	Routes          []HARRoute
	RoutesTruncated bool
}

// HARRoute is a redacted endpoint summary captured from a HAR entry. Query
// values and URL user information are never included.
type HARRoute struct {
	Method string
	URL    string
	Status int
}

func publicHARResult(result harcapture.Result) HARResult {
	routes := make([]HARRoute, 0, len(result.Routes))
	for _, route := range result.Routes {
		routes = append(routes, HARRoute{Method: route.Method, URL: route.URL, Status: route.Status})
	}
	return HARResult{
		Path:            result.Path,
		Capture:         HARCapture(result.Capture),
		Bytes:           result.Bytes,
		Entries:         result.Entries,
		Routes:          routes,
		RoutesTruncated: result.RoutesTruncated,
	}
}

func cloneHARResult(result HARResult) HARResult {
	result.Routes = append([]HARRoute(nil), result.Routes...)
	return result
}

func prepareHAR(options HAROptions) (*harcapture.Recorder, *pwbridge.HAROptions, error) {
	recorder, err := harcapture.Prepare(harcapture.Options{
		Destination: options.Path,
		Capture:     harcapture.Capture(options.Capture),
		URLFilter:   options.URLFilter,
		MaxBytes:    options.MaxBytes,
		Overwrite:   options.Overwrite,
	})
	if err != nil {
		return nil, nil, err
	}
	native := recorder.NativeOptions()
	return recorder, &pwbridge.HAROptions{
		Path:               native.Path,
		Mode:               native.Mode,
		Content:            native.Content,
		OmitRequestContent: native.OmitRequestContent,
		URLFilter:          native.URLFilter,
	}, nil
}
