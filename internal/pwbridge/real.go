package pwbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	playwright "github.com/playwright-community/playwright-go"
)

type RealConnector struct {
	DriverDirectory string
}

type playwrightRuntime struct {
	firefox playwrightBrowserType
	stop    func() error
}

type playwrightBrowserType interface {
	Connect(string, ...playwright.BrowserTypeConnectOptions) (playwright.Browser, error)
}

var (
	playwrightRun     = playwright.Run
	playwrightInstall = playwright.Install
)

var runPlaywright = func(opts *playwright.RunOptions) (playwrightRuntime, error) {
	pw, err := playwrightRun(opts)
	if err != nil {
		return playwrightRuntime{}, err
	}
	return playwrightRuntime{firefox: pw.Firefox, stop: pw.Stop}, nil
}

func (c RealConnector) Connect(ctx context.Context, endpoint string, opts ConnectOptions) (Session, error) {
	prepared, err := c.Prepare(ctx)
	if err != nil {
		return nil, err
	}
	return prepared.Connect(ctx, endpoint, opts)
}

func (c RealConnector) Prepare(ctx context.Context) (PreparedConnector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runOpts := &playwright.RunOptions{DriverDirectory: c.DriverDirectory}
	pw, err := runPlaywright(runOpts)
	if err != nil {
		return nil, err
	}
	return &preparedRealConnector{pw: pw}, nil
}

type preparedRealConnector struct {
	pw playwrightRuntime
}

func (c *preparedRealConnector) Connect(ctx context.Context, endpoint string, opts ConnectOptions) (Session, error) {
	if err := ctx.Err(); err != nil {
		_ = c.Stop()
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	browser, err := c.pw.firefox.Connect(endpoint, playwright.BrowserTypeConnectOptions{
		Timeout: playwright.Float(float64(timeout.Milliseconds())),
	})
	if err != nil {
		_ = c.Stop()
		return nil, err
	}
	pw := c.pw
	c.pw = playwrightRuntime{}
	return &realSession{pw: pw, browser: &realBrowser{raw: browser}}, nil
}

func (c *preparedRealConnector) Stop() error {
	if c == nil || c.pw.stop == nil {
		return nil
	}
	stop := c.pw.stop
	c.pw = playwrightRuntime{}
	return stop()
}

func EnsureDriver(driverDirectory string) error {
	return playwrightInstall(&playwright.RunOptions{DriverDirectory: driverDirectory, SkipInstallBrowsers: true})
}

type realSession struct {
	pw      playwrightRuntime
	browser *realBrowser
}

func (s *realSession) Browser() Browser { return s.browser }

func (s *realSession) Stop() error {
	if s.browser != nil && s.browser.raw != nil {
		_ = s.browser.raw.Close()
	}
	if s.pw.stop != nil {
		_ = s.pw.stop()
	}
	return nil
}

type realBrowser struct {
	raw playwright.Browser
}

func (b *realBrowser) Close() error      { return b.raw.Close() }
func (b *realBrowser) IsConnected() bool { return b.raw.IsConnected() }
func (b *realBrowser) OnDisconnected(fn func()) {
	b.raw.OnDisconnected(func(playwright.Browser) { fn() })
}
func (b *realBrowser) Contexts() []BrowserContext {
	raw := b.raw.Contexts()
	out := make([]BrowserContext, 0, len(raw))
	for _, ctx := range raw {
		out = append(out, &realContext{raw: ctx})
	}
	return out
}
func (b *realBrowser) NewContext(opts ContextOptions) (BrowserContext, error) {
	ctx, err := b.raw.NewContext(toBrowserContextOptions(opts))
	if err != nil {
		return nil, err
	}
	return &realContext{raw: ctx}, nil
}
func (b *realBrowser) NewPage(opts ContextOptions) (Page, error) {
	page, err := b.raw.NewPage(toBrowserNewPageOptions(opts))
	if err != nil {
		return nil, err
	}
	return &realPage{raw: page}, nil
}
func (b *realBrowser) Version() string { return b.raw.Version() }

type realContext struct {
	raw playwright.BrowserContext
}

func (c *realContext) NewPage() (Page, error) {
	p, err := c.raw.NewPage()
	if err != nil {
		return nil, err
	}
	return &realPage{raw: p}, nil
}
func (c *realContext) Pages() []Page {
	raw := c.raw.Pages()
	out := make([]Page, 0, len(raw))
	for _, p := range raw {
		out = append(out, &realPage{raw: p})
	}
	return out
}
func (c *realContext) Cookies(urls ...string) ([]Cookie, error) {
	cookies, err := c.raw.Cookies(urls...)
	if err != nil {
		return nil, err
	}
	return fromPWCookies(cookies), nil
}
func (c *realContext) AddCookies(cookies ...Cookie) error {
	return c.raw.AddCookies(toPWOptionalCookies(cookies))
}
func (c *realContext) ClearCookies() error { return c.raw.ClearCookies() }
func (c *realContext) StorageState() (*StorageState, error) {
	state, err := c.raw.StorageState()
	if err != nil {
		return nil, err
	}
	return fromPWStorageState(state), nil
}
func (c *realContext) Route(urlPattern string, handler RouteHandler) error {
	return c.raw.Route(urlPattern, func(route playwright.Route) {
		handler(&realRoute{raw: route})
	})
}
func (c *realContext) Unroute(urlPattern string, handler RouteHandler) error {
	return c.raw.Unroute(urlPattern)
}
func (c *realContext) OnRequest(fn func(Request)) {
	c.raw.OnRequest(func(r playwright.Request) { fn(&realRequest{raw: r}) })
}
func (c *realContext) OnResponse(fn func(Response)) {
	c.raw.OnResponse(func(r playwright.Response) { fn(&realResponse{raw: r}) })
}
func (c *realContext) Close() error { return c.raw.Close() }
func (c *realContext) Raw() any     { return c.raw }

type realPage struct {
	raw playwright.Page
}

func (p *realPage) Goto(url string, opts GotoOptions) (Response, error) {
	resp, err := p.raw.Goto(url, toPWGotoOptions(opts))
	if err != nil || resp == nil {
		return nil, err
	}
	return &realResponse{raw: resp}, nil
}
func (p *realPage) GoBack(opts NavigateOptions) (Response, error) {
	resp, err := p.raw.GoBack(toPWGoBackOptions(opts))
	if err != nil || resp == nil {
		return nil, err
	}
	return &realResponse{raw: resp}, nil
}
func (p *realPage) GoForward(opts NavigateOptions) (Response, error) {
	resp, err := p.raw.GoForward(toPWGoForwardOptions(opts))
	if err != nil || resp == nil {
		return nil, err
	}
	return &realResponse{raw: resp}, nil
}
func (p *realPage) Reload(opts NavigateOptions) (Response, error) {
	resp, err := p.raw.Reload(toPWReloadOptions(opts))
	if err != nil || resp == nil {
		return nil, err
	}
	return &realResponse{raw: resp}, nil
}
func (p *realPage) RunAndWaitForNavigation(action func() error, opts NavigateOptions) error {
	mainFrame := p.raw.MainFrame()
	_, err := p.raw.ExpectEvent("framenavigated", action, playwright.PageExpectEventOptions{
		Timeout: timeoutPtr(opts.Timeout),
		Predicate: func(event any) bool {
			frame, ok := event.(playwright.Frame)
			return ok && frame == mainFrame
		},
	})
	if err != nil {
		return err
	}
	if opts.WaitUntil == "commit" {
		return nil
	}
	return p.raw.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   toLoadState(opts.WaitUntil),
		Timeout: timeoutPtr(opts.Timeout),
	})
}
func (p *realPage) Evaluate(expression string, arg ...any) (any, error) {
	return p.raw.Evaluate(expression, arg...)
}
func (p *realPage) EvaluateInternal(expression string, arg ...any) (any, error) {
	return p.raw.Evaluate(expression, arg...)
}
func (p *realPage) AddInitScript(script string) error {
	return p.raw.AddInitScript(playwright.Script{Content: playwright.String(script)})
}
func (p *realPage) Content() (string, error) { return p.raw.Content() }
func (p *realPage) SetContent(html string, opts GotoOptions) error {
	return p.raw.SetContent(html, toPWSetContentOptions(opts))
}
func (p *realPage) Title() (string, error) { return p.raw.Title() }
func (p *realPage) URL() string            { return p.raw.URL() }
func (p *realPage) WaitForLoadState(state string, timeout time.Duration) error {
	return p.raw.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   toLoadState(state),
		Timeout: timeoutPtr(timeout),
	})
}
func (p *realPage) WaitForSelector(selector string, opts WaitForSelectorOptions) (ElementHandle, error) {
	//nolint:staticcheck // ElementHandle waits are the bridge contract used by snapshot/action compatibility tests.
	el, err := p.raw.WaitForSelector(selector, playwright.PageWaitForSelectorOptions{
		Timeout: timeoutPtr(opts.Timeout),
		State:   toWaitForSelectorState(opts.State),
	})
	if err != nil || el == nil {
		return nil, err
	}
	return &realElement{raw: el}, nil
}
func (p *realPage) WaitForURL(urlPattern string, opts GotoOptions) error {
	return p.raw.WaitForURL(urlPattern, toPWWaitForURLOptions(opts))
}
func (p *realPage) Screenshot(opts ScreenshotOptions) ([]byte, error) {
	return p.raw.Screenshot(toPWScreenshotOptions(opts))
}
func (p *realPage) PDF(opts PDFOptions) ([]byte, error) {
	return p.raw.PDF(toPWPDFOptions(opts))
}
func (p *realPage) Cookies(urls ...string) ([]Cookie, error) {
	return (&realContext{raw: p.raw.Context()}).Cookies(urls...)
}
func (p *realPage) Route(urlPattern string, handler RouteHandler) error {
	return p.raw.Route(urlPattern, func(route playwright.Route) {
		handler(&realRoute{raw: route})
	})
}
func (p *realPage) Unroute(urlPattern string, handler RouteHandler) error {
	return p.raw.Unroute(urlPattern)
}
func (p *realPage) OnRequest(fn func(Request)) {
	p.raw.OnRequest(func(r playwright.Request) { fn(&realRequest{raw: r}) })
}
func (p *realPage) OnRequestFailed(fn func(Request)) {
	p.raw.OnRequestFailed(func(r playwright.Request) { fn(&realRequest{raw: r}) })
}
func (p *realPage) OnResponse(fn func(Response)) {
	p.raw.OnResponse(func(r playwright.Response) { fn(&realResponse{raw: r}) })
}
func (p *realPage) OnPageError(fn func(error)) {
	p.raw.OnPageError(fn)
}
func (p *realPage) OnConsole(fn func(ConsoleMessage)) {
	p.raw.OnConsole(func(m playwright.ConsoleMessage) {
		fn(ConsoleMessage{Type: m.Type(), Text: m.Text()})
	})
}
func (p *realPage) OnDialog(fn func(Dialog)) {
	p.raw.OnDialog(func(dialog playwright.Dialog) {
		fn(&realDialog{raw: dialog})
	})
}
func (p *realPage) Wheel(deltaX, deltaY float64) error {
	return p.raw.Mouse().Wheel(deltaX, deltaY)
}
func (p *realPage) Locator(selector string) Locator {
	return &realLocator{raw: p.raw.Locator(selector)}
}
func (p *realPage) Close() error { return p.raw.Close() }
func (p *realPage) Raw() any     { return p.raw }

type realDialog struct{ raw playwright.Dialog }

func (d *realDialog) Type() string         { return d.raw.Type() }
func (d *realDialog) Message() string      { return d.raw.Message() }
func (d *realDialog) DefaultValue() string { return d.raw.DefaultValue() }
func (d *realDialog) Accept(promptText ...string) error {
	return d.raw.Accept(promptText...)
}
func (d *realDialog) Dismiss() error { return d.raw.Dismiss() }

type realElement struct{ raw playwright.ElementHandle }

func (e *realElement) Raw() any { return e.raw }

type realRoute struct{ raw playwright.Route }

func (r *realRoute) Request() Request { return &realRequest{raw: r.raw.Request()} }
func (r *realRoute) Continue(opts *ContinueOptions) error {
	return r.raw.Continue(toPWRouteContinueOptions(opts))
}
func (r *realRoute) Fulfill(opts *FulfillOptions) error {
	return r.raw.Fulfill(toPWRouteFulfillOptions(opts))
}
func (r *realRoute) Abort(errorCode string) error {
	if errorCode == "" {
		errorCode = "failed"
	}
	return r.raw.Abort(errorCode)
}
func (r *realRoute) Fetch(opts *FetchOptions) (Response, error) {
	resp, err := r.raw.Fetch(toPWRouteFetchOptions(opts))
	if err != nil || resp == nil {
		return nil, err
	}
	return &realAPIResponse{raw: resp}, nil
}

type realRequest struct{ raw playwright.Request }

func (r *realRequest) URL() string                { return r.raw.URL() }
func (r *realRequest) Method() string             { return r.raw.Method() }
func (r *realRequest) Headers() map[string]string { return r.raw.Headers() }
func (r *realRequest) PostData() string {
	data, _ := r.raw.PostData()
	return data
}
func (r *realRequest) PostDataBytes() []byte {
	data, _ := r.raw.PostDataBuffer()
	return data
}
func (r *realRequest) ResourceType() string      { return r.raw.ResourceType() }
func (r *realRequest) IsNavigationRequest() bool { return r.raw.IsNavigationRequest() }

type realResponse struct{ raw playwright.Response }

func (r *realResponse) URL() string                { return r.raw.URL() }
func (r *realResponse) Status() int                { return r.raw.Status() }
func (r *realResponse) StatusText() string         { return r.raw.StatusText() }
func (r *realResponse) Headers() map[string]string { return r.raw.Headers() }
func (r *realResponse) Body() ([]byte, error)      { return r.raw.Body() }
func (r *realResponse) Text() (string, error)      { return r.raw.Text() }
func (r *realResponse) OK() bool                   { return r.raw.Ok() }
func (r *realResponse) Request() Request           { return &realRequest{raw: r.raw.Request()} }

type realAPIResponse struct{ raw playwright.APIResponse }

func (r *realAPIResponse) URL() string                { return r.raw.URL() }
func (r *realAPIResponse) Status() int                { return r.raw.Status() }
func (r *realAPIResponse) StatusText() string         { return r.raw.StatusText() }
func (r *realAPIResponse) Headers() map[string]string { return r.raw.Headers() }
func (r *realAPIResponse) Body() ([]byte, error)      { return r.raw.Body() }
func (r *realAPIResponse) Text() (string, error)      { return r.raw.Text() }
func (r *realAPIResponse) OK() bool                   { return r.raw.Ok() }
func (r *realAPIResponse) Request() Request           { return nil }

type realLocator struct{ raw playwright.Locator }

func (l *realLocator) Click(opts LocatorClickOptions) error {
	return l.raw.Click(playwright.LocatorClickOptions{
		Timeout:    timeoutPtr(opts.Timeout),
		Force:      boolPtr(opts.Force),
		Button:     mouseButtonPtr(opts.Button),
		ClickCount: intPtr(opts.ClickCount),
	})
}
func (l *realLocator) Fill(value string, opts LocatorFillOptions) error {
	return l.raw.Fill(value, playwright.LocatorFillOptions{Timeout: timeoutPtr(opts.Timeout), Force: boolPtr(opts.Force)})
}
func (l *realLocator) Type(value string, opts LocatorTypeOptions) error {
	return l.raw.PressSequentially(value, playwright.LocatorPressSequentiallyOptions{Timeout: timeoutPtr(opts.Timeout), Delay: timeoutPtr(opts.Delay)})
}
func (l *realLocator) Press(key string, opts LocatorPressOptions) error {
	return l.raw.Press(key, playwright.LocatorPressOptions{Timeout: timeoutPtr(opts.Timeout)})
}
func (l *realLocator) Hover(opts LocatorHoverOptions) error {
	return l.raw.Hover(playwright.LocatorHoverOptions{Timeout: timeoutPtr(opts.Timeout), Force: boolPtr(opts.Force)})
}
func (l *realLocator) ScrollIntoViewIfNeeded(opts LocatorOptions) error {
	return l.raw.ScrollIntoViewIfNeeded(playwright.LocatorScrollIntoViewIfNeededOptions{Timeout: timeoutPtr(opts.Timeout)})
}
func (l *realLocator) SelectOption(opts LocatorSelectOptions) ([]string, error) {
	values := playwright.SelectOptionValues{}
	if opts.Values != nil {
		values.Values = &opts.Values
	}
	if opts.Labels != nil {
		values.Labels = &opts.Labels
	}
	if opts.Indexes != nil {
		values.Indexes = &opts.Indexes
	}
	return l.raw.SelectOption(values, playwright.LocatorSelectOptionOptions{Timeout: timeoutPtr(opts.Timeout), Force: boolPtr(opts.Force)})
}
func (l *realLocator) SetChecked(checked bool, opts LocatorSetCheckedOptions) error {
	return l.raw.SetChecked(checked, playwright.LocatorSetCheckedOptions{Timeout: timeoutPtr(opts.Timeout), Force: boolPtr(opts.Force)})
}
func (l *realLocator) SetInputFiles(files []string, opts LocatorSetInputFilesOptions) error {
	return l.raw.SetInputFiles(files, playwright.LocatorSetInputFilesOptions{Timeout: timeoutPtr(opts.Timeout)})
}
func (l *realLocator) TextContent(opts LocatorOptions) (string, error) {
	return l.raw.TextContent(playwright.LocatorTextContentOptions{Timeout: timeoutPtr(opts.Timeout)})
}
func (l *realLocator) InnerHTML(opts LocatorOptions) (string, error) {
	return l.raw.InnerHTML(playwright.LocatorInnerHTMLOptions{Timeout: timeoutPtr(opts.Timeout)})
}
func (l *realLocator) GetAttribute(name string, opts LocatorOptions) (string, error) {
	return l.raw.GetAttribute(name, playwright.LocatorGetAttributeOptions{Timeout: timeoutPtr(opts.Timeout)})
}
func (l *realLocator) IsVisible(opts LocatorOptions) (bool, error) {
	return l.raw.IsVisible(playwright.LocatorIsVisibleOptions{Timeout: timeoutPtr(opts.Timeout)})
}
func (l *realLocator) Count() (int, error) { return l.raw.Count() }
func (l *realLocator) First() Locator      { return &realLocator{raw: l.raw.First()} }
func (l *realLocator) Last() Locator       { return &realLocator{raw: l.raw.Last()} }
func (l *realLocator) Nth(index int) Locator {
	return &realLocator{raw: l.raw.Nth(index)}
}
func (l *realLocator) WaitFor(opts LocatorWaitForOptions) error {
	return l.raw.WaitFor(playwright.LocatorWaitForOptions{Timeout: timeoutPtr(opts.Timeout), State: toWaitForSelectorState(opts.State)})
}
func (l *realLocator) Screenshot(opts ScreenshotOptions) ([]byte, error) {
	return l.raw.Screenshot(toPWLocatorScreenshotOptions(opts))
}

func toBrowserContextOptions(opts ContextOptions) playwright.BrowserNewContextOptions {
	out := playwright.BrowserNewContextOptions{}
	if opts.Viewport != nil {
		out.Viewport = &playwright.Size{Width: opts.Viewport.Width, Height: opts.Viewport.Height}
	}
	if opts.Locale != "" {
		out.Locale = playwright.String(opts.Locale)
	}
	if opts.TimezoneID != "" {
		out.TimezoneId = playwright.String(opts.TimezoneID)
	}
	if len(opts.ExtraHTTPHeaders) > 0 {
		out.ExtraHttpHeaders = opts.ExtraHTTPHeaders
	}
	if opts.HTTPCredentials != nil {
		out.HttpCredentials = &playwright.HttpCredentials{
			Username: opts.HTTPCredentials.Username,
			Password: opts.HTTPCredentials.Password,
		}
	}
	if opts.StorageState != nil {
		out.StorageState = toPWStorageState(opts.StorageState)
	}
	if opts.Proxy != nil {
		out.Proxy = &playwright.Proxy{
			Server:   opts.Proxy.Server,
			Username: playwright.String(opts.Proxy.Username),
			Password: playwright.String(opts.Proxy.Password),
		}
	}
	return out
}

func toBrowserNewPageOptions(opts ContextOptions) playwright.BrowserNewPageOptions {
	var out playwright.BrowserNewPageOptions
	ctx := toBrowserContextOptions(opts)
	b, _ := json.Marshal(ctx)
	_ = json.Unmarshal(b, &out)
	return out
}

func toPWStorageState(state *StorageState) *playwright.OptionalStorageState {
	if state == nil {
		return nil
	}
	var out playwright.OptionalStorageState
	b, _ := json.Marshal(state)
	_ = json.Unmarshal(b, &out)
	return &out
}

func fromPWStorageState(state *playwright.StorageState) *StorageState {
	var out StorageState
	if state == nil {
		return &out
	}
	b, _ := json.Marshal(state)
	_ = json.Unmarshal(b, &out)
	return &out
}

func fromPWCookies(cookies []playwright.Cookie) []Cookie {
	out := make([]Cookie, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: c.Expires, HTTPOnly: c.HttpOnly, Secure: c.Secure, SameSite: sameSiteString(c.SameSite),
		})
	}
	return out
}

func toPWOptionalCookies(cookies []Cookie) []playwright.OptionalCookie {
	out := make([]playwright.OptionalCookie, 0, len(cookies))
	for _, c := range cookies {
		sameSite := playwright.SameSiteAttribute(c.SameSite)
		out = append(out, playwright.OptionalCookie{
			Name: c.Name, Value: c.Value, Domain: playwright.String(c.Domain), Path: playwright.String(c.Path),
			Expires: playwright.Float(c.Expires), HttpOnly: playwright.Bool(c.HTTPOnly), Secure: playwright.Bool(c.Secure),
			SameSite: &sameSite,
		})
	}
	return out
}

func toPWGotoOptions(opts GotoOptions) playwright.PageGotoOptions {
	return playwright.PageGotoOptions{
		WaitUntil: toWaitUntil(opts.WaitUntil),
		Referer:   stringPtr(opts.Referer),
		Timeout:   timeoutPtr(opts.Timeout),
	}
}

func toPWGoBackOptions(opts NavigateOptions) playwright.PageGoBackOptions {
	return playwright.PageGoBackOptions{WaitUntil: toWaitUntil(opts.WaitUntil), Timeout: timeoutPtr(opts.Timeout)}
}

func toPWGoForwardOptions(opts NavigateOptions) playwright.PageGoForwardOptions {
	return playwright.PageGoForwardOptions{WaitUntil: toWaitUntil(opts.WaitUntil), Timeout: timeoutPtr(opts.Timeout)}
}

func toPWReloadOptions(opts NavigateOptions) playwright.PageReloadOptions {
	return playwright.PageReloadOptions{WaitUntil: toWaitUntil(opts.WaitUntil), Timeout: timeoutPtr(opts.Timeout)}
}

func toPWSetContentOptions(opts GotoOptions) playwright.PageSetContentOptions {
	return playwright.PageSetContentOptions{WaitUntil: toWaitUntil(opts.WaitUntil), Timeout: timeoutPtr(opts.Timeout)}
}

func toPWWaitForURLOptions(opts GotoOptions) playwright.PageWaitForURLOptions {
	return playwright.PageWaitForURLOptions{WaitUntil: toWaitUntil(opts.WaitUntil), Timeout: timeoutPtr(opts.Timeout)}
}

func toPWScreenshotOptions(opts ScreenshotOptions) playwright.PageScreenshotOptions {
	out := playwright.PageScreenshotOptions{
		FullPage: playwright.Bool(opts.FullPage),
		Timeout:  playwright.Float(10000),
	}
	if opts.Type != "" {
		v := playwright.ScreenshotType(opts.Type)
		out.Type = &v
	}
	if opts.Quality > 0 {
		out.Quality = playwright.Int(opts.Quality)
	}
	if opts.Clip != nil {
		out.Clip = &playwright.Rect{X: opts.Clip.X, Y: opts.Clip.Y, Width: opts.Clip.Width, Height: opts.Clip.Height}
	}
	return out
}

func toPWLocatorScreenshotOptions(opts ScreenshotOptions) playwright.LocatorScreenshotOptions {
	out := playwright.LocatorScreenshotOptions{}
	if opts.Type != "" {
		v := playwright.ScreenshotType(opts.Type)
		out.Type = &v
	}
	if opts.Quality > 0 {
		out.Quality = playwright.Int(opts.Quality)
	}
	return out
}

func toPWPDFOptions(opts PDFOptions) playwright.PagePdfOptions {
	if opts.Format == "" {
		return playwright.PagePdfOptions{}
	}
	return playwright.PagePdfOptions{Format: playwright.String(opts.Format)}
}

func toPWRouteContinueOptions(opts *ContinueOptions) playwright.RouteContinueOptions {
	if opts == nil {
		return playwright.RouteContinueOptions{}
	}
	return playwright.RouteContinueOptions{
		URL:      stringPtr(opts.URL),
		Method:   stringPtr(opts.Method),
		Headers:  opts.Headers,
		PostData: opts.PostData,
	}
}

func toPWRouteFulfillOptions(opts *FulfillOptions) playwright.RouteFulfillOptions {
	if opts == nil {
		return playwright.RouteFulfillOptions{}
	}
	out := playwright.RouteFulfillOptions{
		Status:      playwright.Int(opts.Status),
		Headers:     opts.Headers,
		ContentType: stringPtr(opts.ContentType),
		Body:        opts.BodyString,
		Path:        stringPtr(opts.Path),
	}
	if len(opts.Body) > 0 {
		out.Body = string(opts.Body)
	}
	return out
}

func toPWRouteFetchOptions(opts *FetchOptions) playwright.RouteFetchOptions {
	if opts == nil {
		return playwright.RouteFetchOptions{}
	}
	return playwright.RouteFetchOptions{
		URL:      stringPtr(opts.URL),
		Method:   stringPtr(opts.Method),
		Headers:  opts.Headers,
		PostData: opts.PostData,
	}
}

func toWaitUntil(state string) *playwright.WaitUntilState {
	if state == "" {
		state = "load"
	}
	v := playwright.WaitUntilState(state)
	return &v
}

func toLoadState(state string) *playwright.LoadState {
	if state == "" {
		state = "load"
	}
	v := playwright.LoadState(state)
	return &v
}

func toWaitForSelectorState(state string) *playwright.WaitForSelectorState {
	if state == "" {
		return nil
	}
	v := playwright.WaitForSelectorState(state)
	return &v
}

func timeoutPtr(d time.Duration) *float64 {
	if d <= 0 {
		return nil
	}
	return playwright.Float(float64(d.Milliseconds()))
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return playwright.String(s)
}

func boolPtr(v bool) *bool {
	if !v {
		return nil
	}
	return playwright.Bool(v)
}

func intPtr(v int) *int {
	if v <= 0 {
		return nil
	}
	return playwright.Int(v)
}

func mouseButtonPtr(button string) *playwright.MouseButton {
	if button == "" {
		return nil
	}
	v := playwright.MouseButton(button)
	return &v
}

func sameSiteString(v *playwright.SameSiteAttribute) string {
	if v == nil {
		return ""
	}
	return string(*v)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
