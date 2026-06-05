package gomoufox

import (
	"encoding/json"
	"unsafe"

	"github.com/ehmo/gomoufox/internal/pwbridge"
)

type RouteHandler func(r *Route)

type Route struct {
	raw pwbridge.Route
}

func (r *Route) Request() *Request { return &Request{raw: r.raw.Request()} }
func (r *Route) Continue(opts *ContinueOptions) error {
	return r.raw.Continue(toBridgeContinueOptions(opts))
}
func (r *Route) Fulfill(opts *FulfillOptions) error {
	return r.raw.Fulfill(toBridgeFulfillOptions(opts))
}
func (r *Route) Abort(errorCode string) error { return r.raw.Abort(errorCode) }
func (r *Route) Fetch(opts *FetchOptions) (*Response, error) {
	resp, err := r.raw.Fetch(toBridgeFetchOptions(opts))
	if err != nil {
		return nil, err
	}
	return &Response{raw: resp}, nil
}

type ContinueOptions struct {
	URL      string
	Method   string
	Headers  map[string]string
	PostData []byte
}

type FulfillOptions struct {
	Status      int
	Headers     map[string]string
	ContentType string
	Body        []byte
	BodyString  string
	Path        string
}

type FetchOptions struct {
	URL      string
	Method   string
	Headers  map[string]string
	PostData []byte
}

type Request struct {
	raw pwbridge.Request
}

func (r *Request) URL() string                { return r.raw.URL() }
func (r *Request) Method() string             { return r.raw.Method() }
func (r *Request) Headers() map[string]string { return r.raw.Headers() }
func (r *Request) PostData() string           { return r.raw.PostData() }
func (r *Request) PostDataBytes() []byte      { return r.raw.PostDataBytes() }
func (r *Request) ResourceType() string       { return r.raw.ResourceType() }
func (r *Request) IsNavigationRequest() bool  { return r.raw.IsNavigationRequest() }

type Response struct {
	raw pwbridge.Response
}

func (r *Response) URL() string                { return r.raw.URL() }
func (r *Response) Status() int                { return r.raw.Status() }
func (r *Response) StatusText() string         { return r.raw.StatusText() }
func (r *Response) Headers() map[string]string { return r.raw.Headers() }
func (r *Response) Body() ([]byte, error)      { return r.raw.Body() }
func (r *Response) Text() (string, error)      { return r.raw.Text() }
func (r *Response) JSON(dst any) error {
	body, err := r.Body()
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}
func (r *Response) OK() bool { return r.raw.OK() }
func (r *Response) Request() *Request {
	req := r.raw.Request()
	if req == nil {
		return nil
	}
	return &Request{raw: req}
}

type ConsoleMessage struct {
	Type string
	Text string
	Args []string
}

type ElementHandle struct{ raw pwbridge.ElementHandle }

func (e *ElementHandle) Raw() any { return e.raw.Raw() }

func wrapRouteHandler(handler RouteHandler) pwbridge.RouteHandler {
	if handler == nil {
		return nil
	}
	return func(r pwbridge.Route) {
		handler(&Route{raw: r})
	}
}

type routeKey struct {
	pattern string
	handler uintptr
}

func newRouteKey(pattern string, handler RouteHandler) routeKey {
	return routeKey{pattern: pattern, handler: routeHandlerID(handler)}
}

func routeHandlerID(handler RouteHandler) uintptr {
	if handler == nil {
		return 0
	}
	return *(*uintptr)(unsafe.Pointer(&handler))
}

func toBridgeContinueOptions(opts *ContinueOptions) *pwbridge.ContinueOptions {
	if opts == nil {
		return nil
	}
	return &pwbridge.ContinueOptions{URL: opts.URL, Method: opts.Method, Headers: opts.Headers, PostData: opts.PostData}
}

func toBridgeFulfillOptions(opts *FulfillOptions) *pwbridge.FulfillOptions {
	if opts == nil {
		return nil
	}
	return &pwbridge.FulfillOptions{
		Status: opts.Status, Headers: opts.Headers, ContentType: opts.ContentType,
		Body: opts.Body, BodyString: opts.BodyString, Path: opts.Path,
	}
}

func toBridgeFetchOptions(opts *FetchOptions) *pwbridge.FetchOptions {
	if opts == nil {
		return nil
	}
	return &pwbridge.FetchOptions{URL: opts.URL, Method: opts.Method, Headers: opts.Headers, PostData: opts.PostData}
}
