package gomoufox

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/pwbridge"
)

type Page struct {
	browser     *Browser
	context     *Context
	raw         pwbridge.Page
	ownsContext bool
	mu          sync.Mutex
	routes      map[routeKey]pwbridge.RouteHandler
}

type Dialog struct{ raw pwbridge.Dialog }

func (d Dialog) Type() string         { return d.raw.Type() }
func (d Dialog) Message() string      { return d.raw.Message() }
func (d Dialog) DefaultValue() string { return d.raw.DefaultValue() }
func (d Dialog) Accept(ctx context.Context, promptText ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.raw.Accept(promptText...)
}
func (d Dialog) Dismiss(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.raw.Dismiss()
}

func (p *Page) Goto(ctx context.Context, url string, opts ...GotoOption) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := buildGotoConfig(opts...)
	resp, err := p.raw.Goto(url, cfg.toBridge(ctx))
	if err != nil {
		return nil, mapNavigationError(err)
	}
	return &Response{raw: resp}, nil
}

func (p *Page) GoBack(ctx context.Context, opts ...NavigateOption) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := p.raw.GoBack(buildNavigateConfig(opts...).toBridge(ctx))
	if err != nil {
		return nil, mapNavigationError(err)
	}
	return &Response{raw: resp}, nil
}

func (p *Page) GoForward(ctx context.Context, opts ...NavigateOption) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := p.raw.GoForward(buildNavigateConfig(opts...).toBridge(ctx))
	if err != nil {
		return nil, mapNavigationError(err)
	}
	return &Response{raw: resp}, nil
}

func (p *Page) Reload(ctx context.Context, opts ...NavigateOption) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := p.raw.Reload(buildNavigateConfig(opts...).toBridge(ctx))
	if err != nil {
		return nil, mapNavigationError(err)
	}
	return &Response{raw: resp}, nil
}

func (p *Page) RunAndWaitForNavigation(ctx context.Context, action func() error, opts ...NavigateOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := p.raw.RunAndWaitForNavigation(action, buildNavigateConfig(opts...).toBridge(ctx))
	if err != nil {
		return mapNavigationError(err)
	}
	return nil
}

func (p *Page) Evaluate(ctx context.Context, expression string, arg ...any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.raw.Evaluate(expression, arg...)
}

// EvaluateInternal executes expression through the browser helper evaluation
// path without adding Camoufox's main-world "mw:" prefix. MCP sessions use this
// only after a startup probe confirms it stays separate from page-world scripts.
func (p *Page) EvaluateInternal(ctx context.Context, expression string, arg ...any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.raw.EvaluateInternal(expression, arg...)
}

func (p *Page) EvaluateIntoJSON(ctx context.Context, expression string, dst any, arg ...any) error {
	result, err := p.Evaluate(ctx, expression, arg...)
	if err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func (p *Page) AddInitScript(ctx context.Context, script string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.raw.AddInitScript(script)
}

func (p *Page) Content(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p.raw.Content()
}

func (p *Page) SetContent(ctx context.Context, html string, opts ...GotoOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.raw.SetContent(html, buildGotoConfig(opts...).toBridge(ctx))
}

func (p *Page) Title(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p.raw.Title()
}

func (p *Page) URL() string { return p.raw.URL() }

func (p *Page) WaitForLoadState(ctx context.Context, state string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.raw.WaitForLoadState(state, deadlineTimeout(ctx, 0))
}

func (p *Page) WaitForSelector(ctx context.Context, selector string, opts ...WaitForSelectorOption) (*ElementHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := waitForSelectorConfig{timeout: deadlineTimeout(ctx, 10*time.Second)}
	for _, opt := range opts {
		opt(&cfg)
	}
	el, err := p.raw.WaitForSelector(selector, pwbridge.WaitForSelectorOptions{Timeout: cfg.timeout, State: cfg.state})
	if err != nil {
		return nil, err
	}
	return &ElementHandle{raw: el}, nil
}

func (p *Page) WaitForURL(ctx context.Context, urlPattern string, opts ...GotoOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.raw.WaitForURL(urlPattern, buildGotoConfig(opts...).toBridge(ctx))
}

func (p *Page) Screenshot(ctx context.Context, opts ...ScreenshotOption) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := screenshotConfig{typ: "png"}
	for _, opt := range opts {
		opt(&cfg)
	}
	return p.raw.Screenshot(cfg.toBridge())
}

func (p *Page) ScreenshotToFile(ctx context.Context, filePath string, opts ...ScreenshotOption) error {
	data, err := p.Screenshot(ctx, opts...)
	if err != nil {
		return err
	}
	return writeBytes0600(filePath, data)
}

func (p *Page) PDF(ctx context.Context, opts ...PDFOption) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var cfg pdfConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return p.raw.PDF(pwbridge.PDFOptions{Format: cfg.format})
}

func (p *Page) Cookies(ctx context.Context, urls ...string) ([]Cookie, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return fromBridgeCookies(p.raw.Cookies(urls...))
}

func (p *Page) Route(ctx context.Context, urlPattern string, handler RouteHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wrapped := wrapRouteHandler(handler)
	if handler != nil {
		p.mu.Lock()
		if p.routes == nil {
			p.routes = make(map[routeKey]pwbridge.RouteHandler)
		}
		key := newRouteKey(urlPattern, handler)
		p.routes[key] = wrapped
		p.mu.Unlock()
	}
	if err := p.raw.Route(urlPattern, wrapped); err != nil {
		if handler != nil {
			p.mu.Lock()
			delete(p.routes, newRouteKey(urlPattern, handler))
			p.mu.Unlock()
		}
		return err
	}
	return nil
}

func (p *Page) Unroute(ctx context.Context, urlPattern string, handler RouteHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if handler == nil {
		p.mu.Lock()
		for key := range p.routes {
			if key.pattern == urlPattern {
				delete(p.routes, key)
			}
		}
		p.mu.Unlock()
		return p.raw.Unroute(urlPattern, nil)
	}
	key := newRouteKey(urlPattern, handler)
	p.mu.Lock()
	wrapped := p.routes[key]
	delete(p.routes, key)
	p.mu.Unlock()
	if wrapped == nil {
		return nil
	}
	return p.raw.Unroute(urlPattern, wrapped)
}

func (p *Page) OnRequest(handler func(*Request)) {
	p.raw.OnRequest(func(r pwbridge.Request) { handler(&Request{raw: r}) })
}

func (p *Page) OnRequestFailed(handler func(*Request)) {
	p.raw.OnRequestFailed(func(r pwbridge.Request) { handler(&Request{raw: r}) })
}

func (p *Page) OnResponse(handler func(*Response)) {
	p.raw.OnResponse(func(r pwbridge.Response) { handler(&Response{raw: r}) })
}

func (p *Page) OnPageError(handler func(error)) {
	p.raw.OnPageError(handler)
}

func (p *Page) OnConsole(handler func(ConsoleMessage)) {
	p.raw.OnConsole(func(m pwbridge.ConsoleMessage) {
		handler(ConsoleMessage(m))
	})
}

func (p *Page) OnDialog(handler func(Dialog)) {
	p.raw.OnDialog(func(d pwbridge.Dialog) {
		handler(Dialog{raw: d})
	})
}

func (p *Page) Wheel(ctx context.Context, deltaX, deltaY float64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.raw.Wheel(deltaX, deltaY)
}

func (p *Page) FetchJSON(ctx context.Context, url, method string, headers map[string]string, body []byte, dst any) error {
	return p.FetchJSONWithOptions(ctx, url, method, headers, body, dst, FetchBytesOptions{})
}

// FetchJSONWithOptions is FetchJSON with response acquisition controls.
func (p *Page) FetchJSONWithOptions(ctx context.Context, url, method string, headers map[string]string, body []byte, dst any, opts FetchBytesOptions) error {
	if method == "" {
		method = "GET"
	}
	result, err := p.FetchBytesWithOptions(ctx, url, method, headers, body, opts)
	if err != nil {
		return err
	}
	status := result.StatusCode
	data := result.Body
	if status < 200 || status >= 300 {
		return &BrowserFetchError{Code: "network_error", URL: url, Method: method, Status: status, BodyPreview: previewBytes(data), Message: "browser fetch returned non-success status"}
	}
	if result.Truncated {
		return &BrowserFetchError{Code: "response_too_large", URL: url, Method: method, Status: status, BodyPreview: previewBytes(data), Message: "browser fetch response exceeded max bytes"}
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return &BrowserFetchError{Code: "non_json", URL: url, Method: method, Status: status, BodyPreview: previewBytes(data), Message: err.Error()}
	}
	return nil
}

// FetchBytesOptions controls browser-side response body acquisition.
type FetchBytesOptions struct {
	// MaxBytes caps bytes read from the browser response stream. Zero uses the
	// default response cap. Negative values explicitly disable the cap.
	MaxBytes int
}

// FetchBytesResult is the result of a bounded in-browser fetch.
type FetchBytesResult struct {
	StatusCode int
	Body       []byte
	Truncated  bool
}

func (p *Page) FetchBytes(ctx context.Context, url, method string, headers map[string]string, body []byte) (int, []byte, error) {
	if method == "" {
		method = "GET"
	}
	result, err := p.FetchBytesWithOptions(ctx, url, method, headers, body, FetchBytesOptions{})
	if err != nil {
		return result.StatusCode, result.Body, err
	}
	if result.Truncated {
		return result.StatusCode, result.Body, &BrowserFetchError{
			Code:        "response_too_large",
			URL:         url,
			Method:      method,
			Status:      result.StatusCode,
			BodyPreview: previewBytes(result.Body),
			Message:     "browser fetch response exceeded max bytes",
		}
	}
	return result.StatusCode, result.Body, nil
}

// FetchBytesWithOptions is FetchBytes with response acquisition controls. It
// uses EvaluateInternal for browser-side fetch acquisition, so page-world
// window.fetch patches are not part of the helper contract.
func (p *Page) FetchBytesWithOptions(ctx context.Context, url, method string, headers map[string]string, body []byte, opts FetchBytesOptions) (FetchBytesResult, error) {
	if err := ctx.Err(); err != nil {
		return FetchBytesResult{}, err
	}
	if method == "" {
		method = "GET"
	}
	maxBytes, err := fetchMaxBytes(opts)
	if err != nil {
		return FetchBytesResult{}, err
	}
	result, err := p.raw.EvaluateInternal(browserFetchExpression, map[string]any{
		"url":      url,
		"method":   method,
		"headers":  fetchHeadersForEvaluation(headers),
		"body":     string(body),
		"maxBytes": maxBytes,
	})
	if err != nil {
		return FetchBytesResult{}, &BrowserFetchError{Code: "network_error", URL: url, Method: method, Status: 0, Message: err.Error()}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return FetchBytesResult{}, err
	}
	var payload struct {
		OK        bool              `json:"ok"`
		Code      string            `json:"code"`
		URL       string            `json:"url"`
		Status    int               `json:"status"`
		Headers   map[string]string `json:"headers"`
		Body      string            `json:"body"`
		Message   string            `json:"message"`
		Truncated bool              `json:"truncated"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return FetchBytesResult{}, err
	}
	responseBody := []byte(payload.Body)
	if maxBytes > 0 {
		if capped, truncated := policy.Truncate(responseBody, maxBytes); truncated {
			responseBody = capped
			payload.Truncated = true
		}
	}
	if !payload.OK {
		return FetchBytesResult{StatusCode: payload.Status, Body: responseBody, Truncated: payload.Truncated}, &BrowserFetchError{
			Code: payload.Code, URL: payload.URL, Method: method, Status: payload.Status,
			BodyPreview: previewBytes(responseBody), Message: payload.Message,
		}
	}
	return FetchBytesResult{StatusCode: payload.Status, Body: responseBody, Truncated: payload.Truncated}, nil
}

func fetchHeadersForEvaluation(headers map[string]string) map[string]any {
	out := make(map[string]any, len(headers))
	for key, value := range headers {
		out[key] = value
	}
	return out
}

func fetchMaxBytes(opts FetchBytesOptions) (int, error) {
	if opts.MaxBytes < 0 {
		return 0, nil
	}
	if opts.MaxBytes == 0 {
		return policy.DefaultMaxResponseBytes, nil
	}
	return policy.ClampResponseCap(opts.MaxBytes)
}

func (p *Page) Locator(selector string) Locator {
	return &locator{raw: p.raw.Locator(selector)}
}

func (p *Page) Close() error {
	if p.ownsContext && p.context != nil {
		return p.context.Close()
	}
	err := p.raw.Close()
	return err
}

func (p *Page) Raw() any { return p.raw.Raw() }

const browserFetchExpression = `async ({url, method, headers, body, maxBytes}) => {
  let reader;
  let cancelReader = async () => {};
  try {
    const response = await fetch(url, {method, headers, body: body || undefined, credentials: "include"});
    const headersObject = {};
    if (response.headers && typeof response.headers.forEach === "function") {
      response.headers.forEach((value, key) => { headersObject[key] = value; });
    }
    if (!response.body) return {ok: true, url: response.url, status: response.status, headers: headersObject, body: "", truncated: false};
    reader = response.body.getReader ? response.body.getReader() : null;
    if (!reader) return {ok: false, code: "body_unreadable", url: response.url || url, method, status: response.status, headers: headersObject, body: "", message: "streaming response body is unavailable"};
    cancelReader = async () => { try { await reader.cancel(); } catch (_) {} };
    const limit = Math.max(0, Number(maxBytes) || 0);
    const chunks = [];
    let total = 0;
    let truncated = false;
    while (true) {
      const item = await reader.read();
      if (item.done) break;
      const chunk = item.value || new Uint8Array();
      if (limit > 0 && total + chunk.byteLength > limit) {
        const keep = Math.max(0, limit - total);
        if (keep > 0) {
          chunks.push(chunk.slice(0, keep));
          total += keep;
        }
        truncated = true;
        await cancelReader();
        break;
      }
      chunks.push(chunk);
      total += chunk.byteLength;
      if (limit > 0 && total >= limit) {
        truncated = true;
        await cancelReader();
        break;
      }
    }
    const bytes = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.byteLength;
    }
    const text = new TextDecoder().decode(bytes);
    return {ok: true, url: response.url, status: response.status, headers: headersObject, body: text, truncated};
  } catch (error) {
    await cancelReader();
    const message = error && error.message ? String(error.message) : String(error);
    let code = "network_error";
    if (/content security policy|csp/i.test(message)) code = "csp_blocked";
    if (/cors|cross-origin/i.test(message)) code = "cors_denied";
    if (/mixed content/i.test(message)) code = "mixed_content_blocked";
    return {ok: false, code, url, method, status: 0, body: "", message};
  }
}`

func previewBytes(data []byte) []byte {
	if len(data) > 512 {
		return append([]byte(nil), data[:512]...)
	}
	return append([]byte(nil), data...)
}
