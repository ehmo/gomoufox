package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/camoufoxcfg"
	"github.com/ehmo/gomoufox/internal/a11y"
	"github.com/ehmo/gomoufox/internal/policy"
)

type gomoufoxFactory struct {
	mu       sync.Mutex
	launcher gomoufoxLauncher
	shared   mcpBrowser
}

type gomoufoxLauncher interface {
	Launch(context.Context, sessionOptions, bool) (mcpBrowser, error)
}

type realGomoufoxLauncher struct {
	policy policy.Config
}

var newGomoufoxForMCP = gomoufox.New

type mcpBrowser interface {
	NewContext(context.Context, ...gomoufox.ContextOption) (mcpContext, error)
	NewPage(context.Context, ...gomoufox.ContextOption) (mcpPage, error)
	Close() error
}

type mcpContext interface {
	NewPage(context.Context) (mcpPage, error)
	Cookies(context.Context, ...string) ([]gomoufox.Cookie, error)
	AddCookies(context.Context, ...gomoufox.Cookie) error
	ClearCookies(context.Context) error
	StorageState(context.Context, string) (*gomoufox.StorageState, error)
	Close() error
}

type mcpPage interface {
	Goto(context.Context, string, ...gomoufox.GotoOption) (*gomoufox.Response, error)
	RunAndWaitForNavigation(context.Context, func() error, ...gomoufox.NavigateOption) error
	Title(context.Context) (string, error)
	URL() string
	Content(context.Context) (string, error)
	Evaluate(context.Context, string, ...any) (any, error)
	EvaluateInternal(context.Context, string, ...any) (any, error)
	AddInitScript(context.Context, string) error
	WaitForLoadState(context.Context, string) error
	WaitForSelector(context.Context, string, ...gomoufox.WaitForSelectorOption) (*gomoufox.ElementHandle, error)
	WaitForURL(context.Context, string, ...gomoufox.GotoOption) error
	Screenshot(context.Context, ...gomoufox.ScreenshotOption) ([]byte, error)
	Locator(string) gomoufox.Locator
	Wheel(context.Context, float64, float64) error
	FetchBytes(context.Context, string, string, map[string]string, []byte) (int, []byte, error)
	OnRequest(func(*gomoufox.Request))
	OnRequestFailed(func(*gomoufox.Request))
	OnResponse(func(*gomoufox.Response))
	OnPageError(func(error))
	OnConsole(func(gomoufox.ConsoleMessage))
	OnDialog(func(gomoufox.Dialog))
	Close() error
}

type browserAdapter struct {
	newContext func(context.Context, ...gomoufox.ContextOption) (mcpContext, error)
	newPage    func(context.Context, ...gomoufox.ContextOption) (mcpPage, error)
	close      func() error
}

type contextAdapter struct {
	newPage      func(context.Context) (mcpPage, error)
	cookies      func(context.Context, ...string) ([]gomoufox.Cookie, error)
	addCookies   func(context.Context, ...gomoufox.Cookie) error
	clearCookies func(context.Context) error
	storageState func(context.Context, string) (*gomoufox.StorageState, error)
	close        func() error
}

type pageAdapter struct {
	gotoFunc         func(context.Context, string, ...gomoufox.GotoOption) (*gomoufox.Response, error)
	waitNavigation   func(context.Context, func() error, ...gomoufox.NavigateOption) error
	title            func(context.Context) (string, error)
	url              func() string
	content          func(context.Context) (string, error)
	evaluate         func(context.Context, string, ...any) (any, error)
	evaluateInternal func(context.Context, string, ...any) (any, error)
	addInitScript    func(context.Context, string) error
	waitLoadState    func(context.Context, string) error
	waitSelector     func(context.Context, string, ...gomoufox.WaitForSelectorOption) (*gomoufox.ElementHandle, error)
	waitURL          func(context.Context, string, ...gomoufox.GotoOption) error
	screenshot       func(context.Context, ...gomoufox.ScreenshotOption) ([]byte, error)
	locator          func(string) gomoufox.Locator
	wheel            func(context.Context, float64, float64) error
	fetchBytes       func(context.Context, string, string, map[string]string, []byte) (int, []byte, error)
	onRequest        func(func(*gomoufox.Request))
	onRequestFailed  func(func(*gomoufox.Request))
	onResponse       func(func(*gomoufox.Response))
	onPageError      func(func(error))
	onConsole        func(func(gomoufox.ConsoleMessage))
	onDialog         func(func(gomoufox.Dialog))
	close            func() error
}

func (l realGomoufoxLauncher) Launch(ctx context.Context, opts sessionOptions, dedicated bool) (mcpBrowser, error) {
	launchOpts := []gomoufox.Option{gomoufox.WithMainWorldEval(true)}
	if opts.os != "" {
		launchOpts = append(launchOpts, gomoufox.WithOS(camoufoxcfg.OS(opts.os)))
	}
	if opts.locale != "" {
		launchOpts = append(launchOpts, gomoufox.WithLocale(opts.locale))
	}
	if opts.proxy != "" && dedicated {
		proxy, err := proxyConfig(opts.proxy)
		if err != nil {
			return nil, err
		}
		launchOpts = append(launchOpts, gomoufox.WithProxy(proxy))
	}
	if opts.profilePath != "" {
		launchOpts = append(launchOpts, gomoufox.WithPersistentContext(opts.profilePath))
	}
	if len(l.policy.AllowedOrigins) > 0 {
		launchOpts = append(launchOpts, gomoufox.WithAllowedOrigins(l.policy.AllowedOrigins...))
	}
	if len(l.policy.AllowedHosts) > 0 {
		launchOpts = append(launchOpts, gomoufox.WithAllowedHosts(l.policy.AllowedHosts...))
	}
	browser, err := newGomoufoxForMCP(ctx, launchOpts...)
	if err != nil {
		return nil, err
	}
	return adaptBrowser(browser), nil
}

func adaptBrowser(b *gomoufox.Browser) mcpBrowser {
	return browserAdapter{
		newContext: func(ctx context.Context, opts ...gomoufox.ContextOption) (mcpContext, error) {
			c, err := b.NewContext(ctx, opts...)
			if err != nil {
				return nil, err
			}
			return adaptContext(c), nil
		},
		newPage: func(ctx context.Context, opts ...gomoufox.ContextOption) (mcpPage, error) {
			p, err := b.NewPage(ctx, opts...)
			if err != nil {
				return nil, err
			}
			return adaptPage(p), nil
		},
		close: b.Close,
	}
}

func adaptContext(c *gomoufox.Context) mcpContext {
	return contextAdapter{
		newPage: func(ctx context.Context) (mcpPage, error) {
			p, err := c.NewPage(ctx)
			if err != nil {
				return nil, err
			}
			return adaptPage(p), nil
		},
		cookies:      c.Cookies,
		addCookies:   c.AddCookies,
		clearCookies: c.ClearCookies,
		storageState: c.StorageState,
		close:        c.Close,
	}
}

func adaptPage(p *gomoufox.Page) mcpPage {
	return pageAdapter{
		gotoFunc:         p.Goto,
		waitNavigation:   p.RunAndWaitForNavigation,
		title:            p.Title,
		url:              p.URL,
		content:          p.Content,
		evaluate:         p.Evaluate,
		evaluateInternal: p.EvaluateInternal,
		addInitScript:    p.AddInitScript,
		waitLoadState:    p.WaitForLoadState,
		waitSelector:     p.WaitForSelector,
		waitURL:          p.WaitForURL,
		screenshot:       p.Screenshot,
		locator:          p.Locator,
		wheel:            p.Wheel,
		fetchBytes:       p.FetchBytes,
		onRequest:        p.OnRequest,
		onRequestFailed:  p.OnRequestFailed,
		onResponse:       p.OnResponse,
		onPageError:      p.OnPageError,
		onConsole:        p.OnConsole,
		onDialog:         p.OnDialog,
		close:            p.Close,
	}
}

func (b browserAdapter) NewContext(ctx context.Context, opts ...gomoufox.ContextOption) (mcpContext, error) {
	return b.newContext(ctx, opts...)
}
func (b browserAdapter) NewPage(ctx context.Context, opts ...gomoufox.ContextOption) (mcpPage, error) {
	return b.newPage(ctx, opts...)
}
func (b browserAdapter) Close() error { return b.close() }

func (c contextAdapter) NewPage(ctx context.Context) (mcpPage, error) { return c.newPage(ctx) }
func (c contextAdapter) Cookies(ctx context.Context, urls ...string) ([]gomoufox.Cookie, error) {
	return c.cookies(ctx, urls...)
}
func (c contextAdapter) AddCookies(ctx context.Context, cookies ...gomoufox.Cookie) error {
	return c.addCookies(ctx, cookies...)
}
func (c contextAdapter) ClearCookies(ctx context.Context) error { return c.clearCookies(ctx) }
func (c contextAdapter) StorageState(ctx context.Context, path string) (*gomoufox.StorageState, error) {
	return c.storageState(ctx, path)
}
func (c contextAdapter) Close() error { return c.close() }

func (p pageAdapter) Goto(ctx context.Context, u string, opts ...gomoufox.GotoOption) (*gomoufox.Response, error) {
	return p.gotoFunc(ctx, u, opts...)
}
func (p pageAdapter) RunAndWaitForNavigation(ctx context.Context, action func() error, opts ...gomoufox.NavigateOption) error {
	return p.waitNavigation(ctx, action, opts...)
}
func (p pageAdapter) Title(ctx context.Context) (string, error) { return p.title(ctx) }
func (p pageAdapter) URL() string                               { return p.url() }
func (p pageAdapter) Content(ctx context.Context) (string, error) {
	return p.content(ctx)
}
func (p pageAdapter) Evaluate(ctx context.Context, expr string, arg ...any) (any, error) {
	return p.evaluate(ctx, expr, arg...)
}
func (p pageAdapter) EvaluateInternal(ctx context.Context, expr string, arg ...any) (any, error) {
	if p.evaluateInternal == nil {
		return nil, errors.New("internal evaluation unavailable")
	}
	return p.evaluateInternal(ctx, expr, arg...)
}
func (p pageAdapter) AddInitScript(ctx context.Context, script string) error {
	return p.addInitScript(ctx, script)
}
func (p pageAdapter) WaitForLoadState(ctx context.Context, state string) error {
	return p.waitLoadState(ctx, state)
}
func (p pageAdapter) WaitForSelector(ctx context.Context, selector string, opts ...gomoufox.WaitForSelectorOption) (*gomoufox.ElementHandle, error) {
	return p.waitSelector(ctx, selector, opts...)
}
func (p pageAdapter) WaitForURL(ctx context.Context, pattern string, opts ...gomoufox.GotoOption) error {
	return p.waitURL(ctx, pattern, opts...)
}
func (p pageAdapter) Screenshot(ctx context.Context, opts ...gomoufox.ScreenshotOption) ([]byte, error) {
	return p.screenshot(ctx, opts...)
}
func (p pageAdapter) Locator(selector string) gomoufox.Locator { return p.locator(selector) }
func (p pageAdapter) Wheel(ctx context.Context, deltaX, deltaY float64) error {
	return p.wheel(ctx, deltaX, deltaY)
}
func (p pageAdapter) FetchBytes(ctx context.Context, u, method string, headers map[string]string, body []byte) (int, []byte, error) {
	return p.fetchBytes(ctx, u, method, headers, body)
}
func (p pageAdapter) OnRequest(fn func(*gomoufox.Request))       { p.onRequest(fn) }
func (p pageAdapter) OnRequestFailed(fn func(*gomoufox.Request)) { p.onRequestFailed(fn) }
func (p pageAdapter) OnResponse(fn func(*gomoufox.Response))     { p.onResponse(fn) }
func (p pageAdapter) OnPageError(fn func(error))                 { p.onPageError(fn) }
func (p pageAdapter) OnConsole(fn func(gomoufox.ConsoleMessage)) { p.onConsole(fn) }
func (p pageAdapter) OnDialog(fn func(gomoufox.Dialog))          { p.onDialog(fn) }
func (p pageAdapter) Close() error                               { return p.close() }

func newGomoufoxFactory(cfg policy.Config) *gomoufoxFactory {
	return &gomoufoxFactory{launcher: realGomoufoxLauncher{policy: cfg}}
}

func (f *gomoufoxFactory) NewBrowserSession(ctx context.Context, opts sessionOptions) (browserSession, error) {
	dedicated := opts.profilePath != "" || opts.os != "" || opts.proxy != ""
	browser, closeBrowser, err := f.browser(ctx, opts, dedicated)
	if err != nil {
		return nil, err
	}
	contextOpts, err := contextOptions(opts)
	if err != nil {
		if closeBrowser {
			_ = browser.Close()
		}
		return nil, err
	}
	if opts.profilePath != "" {
		browserContext, err := browser.NewContext(ctx, contextOpts...)
		if err != nil {
			if closeBrowser {
				_ = browser.Close()
			}
			return nil, err
		}
		page, err := browserContext.NewPage(ctx)
		if err != nil {
			_ = browserContext.Close()
			if closeBrowser {
				_ = browser.Close()
			}
			return nil, err
		}
		if err := verifyMCPInternalEvaluation(ctx, page); err != nil {
			_ = page.Close()
			_ = browserContext.Close()
			if closeBrowser {
				_ = browser.Close()
			}
			return nil, err
		}
		return newGomoufoxSession(ctx, browser, browserContext, page, closeBrowser), nil
	}
	browserContext, err := browser.NewContext(ctx, contextOpts...)
	if err != nil {
		if closeBrowser {
			_ = browser.Close()
		}
		return nil, err
	}
	page, err := browserContext.NewPage(ctx)
	if err != nil {
		_ = browserContext.Close()
		if closeBrowser {
			_ = browser.Close()
		}
		return nil, err
	}
	if err := verifyMCPInternalEvaluation(ctx, page); err != nil {
		_ = page.Close()
		_ = browserContext.Close()
		if closeBrowser {
			_ = browser.Close()
		}
		return nil, err
	}
	return newGomoufoxSession(ctx, browser, browserContext, page, closeBrowser), nil
}

func (f *gomoufoxFactory) browser(ctx context.Context, opts sessionOptions, dedicated bool) (mcpBrowser, bool, error) {
	if dedicated {
		browser, err := f.launcher.Launch(ctx, opts, true)
		return browser, true, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shared == nil {
		browser, err := f.launcher.Launch(ctx, sessionOptions{}, false)
		if err != nil {
			return nil, false, err
		}
		f.shared = browser
	}
	return f.shared, false, nil
}

type gomoufoxSession struct {
	browser      mcpBrowser
	context      mcpContext
	page         mcpPage
	closeBrowser bool
	refs         *a11y.Store
	observations *observationBuffers
	dialogMu     sync.Mutex
	dialogPolicy string
	dialogPrompt string
}

func newGomoufoxSession(ctx context.Context, browser mcpBrowser, browserContext mcpContext, page mcpPage, closeBrowser bool) *gomoufoxSession {
	session := &gomoufoxSession{
		browser:      browser,
		context:      browserContext,
		page:         page,
		closeBrowser: closeBrowser,
		refs:         a11y.NewStore(),
		observations: newObservationBuffers(),
		dialogPolicy: dialogPolicyDismiss,
	}
	session.attachObservers(ctx)
	return session
}

func (s *gomoufoxSession) Navigate(ctx context.Context, rawURL string, opts navigateOptions) (navigateResult, error) {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	resp, err := s.page.Goto(ctx, rawURL, gomoufox.WaitUntil(opts.WaitUntil), gomoufox.WithTimeout(opts.Timeout))
	if err != nil {
		return navigateResult{}, err
	}
	title, err := s.page.Title(ctx)
	if err != nil {
		return navigateResult{}, err
	}
	status := 0
	if resp != nil {
		status = resp.Status()
	}
	s.refs.Clear()
	return navigateResult{URL: s.page.URL(), Title: title, Status: status}, nil
}

func (s *gomoufoxSession) PageContent(ctx context.Context, opts pageContentOptions) (pageContent, error) {
	title, err := s.page.Title(ctx)
	if err != nil {
		return pageContent{}, err
	}
	result, err := s.page.EvaluateInternal(ctx, boundedContentExpression, map[string]any{
		"selector":    opts.Selector,
		"maxBytes":    opts.MaxBytes,
		"includeHTML": opts.IncludeHTML,
		"includeText": opts.IncludeText,
	})
	if err != nil {
		return pageContent{}, err
	}
	var payload struct {
		OK        bool   `json:"ok"`
		URL       string `json:"url"`
		HTML      string `json:"html"`
		Text      string `json:"text"`
		Message   string `json:"message"`
		Truncated bool   `json:"truncated"`
	}
	if err := decodeJSONValue(result, &payload); err != nil {
		return pageContent{}, err
	}
	if !payload.OK {
		return pageContent{}, errors.New(payload.Message)
	}
	return pageContent{URL: payload.URL, Title: title, HTML: payload.HTML, Text: payload.Text, Truncated: payload.Truncated}, nil
}

func (s *gomoufoxSession) Evaluate(ctx context.Context, script string, arg any, opts evaluateOptions) (any, error) {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	script = mainWorldExpression(script)
	if arg == nil {
		return s.page.Evaluate(ctx, script)
	}
	return s.page.Evaluate(ctx, script, arg)
}

func mainWorldExpression(script string) string {
	if strings.HasPrefix(script, "mw:") {
		return script
	}
	return "mw:" + script
}

func (s *gomoufoxSession) Click(ctx context.Context, target elementTarget, opts clickOptions) error {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	locator, err := s.locator(ctx, target)
	if err != nil {
		return err
	}
	click := func() error {
		return locator.Click(ctx, gomoufox.LocatorClickTimeout(opts.Timeout), gomoufox.LocatorClickButton(opts.Button), gomoufox.LocatorClickCount(opts.ClickCount))
	}
	if opts.WaitForNavigation {
		return s.page.RunAndWaitForNavigation(ctx, click, gomoufox.NavigateTimeout(opts.Timeout), gomoufox.NavigateWaitUntil("domcontentloaded"))
	}
	return click()
}

func (s *gomoufoxSession) Type(ctx context.Context, target elementTarget, text string, opts typeOptions) error {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	locator, err := s.locator(ctx, target)
	if err != nil {
		return err
	}
	if opts.ClearFirst {
		if err := locator.Fill(ctx, "", gomoufox.LocatorFillTimeout(opts.Timeout)); err != nil {
			return err
		}
	}
	if err := locator.Type(ctx, text, gomoufox.LocatorTypeTimeout(opts.Timeout), gomoufox.LocatorTypeDelay(opts.Delay)); err != nil {
		return err
	}
	if opts.PressEnterAfter {
		return locator.Press(ctx, "Enter", gomoufox.LocatorPressTimeout(opts.Timeout))
	}
	return nil
}

func (s *gomoufoxSession) PressKey(ctx context.Context, target elementTarget, key string, opts pressOptions) error {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	locator, err := s.locator(ctx, target)
	if err != nil {
		return err
	}
	return locator.Press(ctx, key, gomoufox.LocatorPressTimeout(opts.Timeout))
}

func (s *gomoufoxSession) Hover(ctx context.Context, target elementTarget, opts hoverOptions) error {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	locator, err := s.locator(ctx, target)
	if err != nil {
		return err
	}
	return locator.Hover(ctx, gomoufox.LocatorHoverTimeout(opts.Timeout), gomoufox.LocatorHoverForce(opts.Force))
}

func (s *gomoufoxSession) Scroll(ctx context.Context, opts scrollOptions) error {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	if opts.Target.Ref != "" || opts.Target.Selector != "" {
		locator, err := s.locator(ctx, opts.Target)
		if err != nil {
			return err
		}
		if err := locator.ScrollIntoViewIfNeeded(ctx, gomoufox.LocatorTimeout(opts.Timeout)); err != nil {
			return err
		}
	}
	if opts.DeltaX == 0 && opts.DeltaY == 0 {
		return nil
	}
	return s.page.Wheel(ctx, opts.DeltaX, opts.DeltaY)
}

func (s *gomoufoxSession) SelectOption(ctx context.Context, target elementTarget, opts selectOptionOptions) ([]string, error) {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	locator, err := s.locator(ctx, target)
	if err != nil {
		return nil, err
	}
	selectOpts := []gomoufox.LocatorSelectOption{gomoufox.LocatorSelectTimeout(opts.Timeout), gomoufox.LocatorSelectForce(opts.Force)}
	if opts.Values != nil {
		selectOpts = append(selectOpts, gomoufox.LocatorSelectValues(opts.Values...))
	}
	if opts.Labels != nil {
		selectOpts = append(selectOpts, gomoufox.LocatorSelectLabels(opts.Labels...))
	}
	if opts.Indexes != nil {
		selectOpts = append(selectOpts, gomoufox.LocatorSelectIndexes(opts.Indexes...))
	}
	return locator.SelectOption(ctx, selectOpts...)
}

func (s *gomoufoxSession) SetChecked(ctx context.Context, target elementTarget, checked bool, opts checkedOptions) error {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	locator, err := s.locator(ctx, target)
	if err != nil {
		return err
	}
	return locator.SetChecked(ctx, checked, gomoufox.LocatorSetCheckedTimeout(opts.Timeout), gomoufox.LocatorSetCheckedForce(opts.Force))
}

func (s *gomoufoxSession) UploadFile(ctx context.Context, target elementTarget, files []string, opts uploadOptions) error {
	ctx, cancel := timeoutContext(ctx, opts.Timeout)
	defer cancel()
	locator, err := s.locator(ctx, target)
	if err != nil {
		return err
	}
	return locator.SetInputFiles(ctx, files, gomoufox.LocatorSetInputFilesTimeout(opts.Timeout))
}

func (s *gomoufoxSession) Dialog(_ context.Context, opts dialogOptions) (dialogResult, error) {
	switch opts.Action {
	case dialogActionSetPolicy:
		if !validDialogPolicy(opts.Policy) {
			return dialogResult{}, ErrInvalidCall
		}
		s.dialogMu.Lock()
		s.dialogPolicy = opts.Policy
		s.dialogPrompt = opts.PromptText
		s.dialogMu.Unlock()
		return dialogResult{Policy: opts.Policy}, nil
	case dialogActionHistory:
		buffers := s.ensureObservations()
		dialogs, dropped := buffers.dialogEvents(opts.MaxEvents, opts.Clear)
		return dialogResult{Policy: s.currentDialogPolicy(), Dialogs: dialogs, Dropped: dropped}, nil
	default:
		return dialogResult{}, ErrInvalidCall
	}
}

func (s *gomoufoxSession) dialogPolicySnapshot() (string, string) {
	s.dialogMu.Lock()
	defer s.dialogMu.Unlock()
	if s.dialogPolicy == "" {
		return dialogPolicyDismiss, ""
	}
	return s.dialogPolicy, s.dialogPrompt
}

func (s *gomoufoxSession) currentDialogPolicy() string {
	policy, _ := s.dialogPolicySnapshot()
	return policy
}

func handleDialog(dialog gomoufox.Dialog, policy string, promptText string) error {
	if policy == dialogPolicyAccept {
		if promptText != "" {
			return dialog.Accept(context.Background(), promptText)
		}
		return dialog.Accept(context.Background())
	}
	return dialog.Dismiss(context.Background())
}

func (s *gomoufoxSession) WaitFor(ctx context.Context, condition waitCondition) error {
	ctx, cancel := timeoutContext(ctx, condition.Timeout)
	defer cancel()
	switch condition.Kind {
	case "selector":
		_, err := s.page.WaitForSelector(ctx, condition.Value, gomoufox.WaitForSelectorTimeout(condition.Timeout), gomoufox.WaitForSelectorState("visible"))
		return err
	case "text":
		_, err := s.page.WaitForSelector(ctx, "text="+condition.Value, gomoufox.WaitForSelectorTimeout(condition.Timeout), gomoufox.WaitForSelectorState("visible"))
		return err
	case "url_contains":
		return s.page.WaitForURL(ctx, "**"+condition.Value+"**", gomoufox.WithTimeout(condition.Timeout))
	case "load_state":
		return s.page.WaitForLoadState(ctx, condition.Value)
	default:
		return ErrInvalidCall
	}
}

func (s *gomoufoxSession) Screenshot(ctx context.Context, opts screenshotOptions) (screenshotResult, error) {
	var data []byte
	var err error
	if opts.Selector != "" {
		if opts.MaxBytes > 0 {
			width, height := s.selectorMetrics(ctx, opts.Selector)
			if screenshotPixelBytes(width, height) > screenshotWorkLimitBytes(opts.MaxBytes) {
				return screenshotResult{}, errResponseTooLarge
			}
		}
		data, err = s.page.Locator(opts.Selector).Screenshot(ctx)
	} else {
		if opts.FullPage && opts.MaxBytes > 0 {
			width, height := s.fullPageMetrics(ctx)
			if screenshotPixelBytes(width, height) > screenshotWorkLimitBytes(opts.MaxBytes) {
				return screenshotResult{}, errResponseTooLarge
			}
		}
		data, err = s.page.Screenshot(ctx, gomoufox.FullPage(opts.FullPage))
	}
	if err != nil {
		return screenshotResult{}, err
	}
	width, height := s.viewport(ctx)
	return screenshotResult{URL: s.page.URL(), Width: width, Height: height, Data: data}, nil
}

func (s *gomoufoxSession) Snapshot(ctx context.Context, opts snapshotOptions) (snapshotResult, error) {
	title, err := s.page.Title(ctx)
	if err != nil {
		return snapshotResult{}, err
	}
	elements, err := s.snapshotElements(ctx, opts.MaxElements, opts.InteractiveOnly, opts.IncludeValues)
	if err != nil {
		return snapshotResult{}, err
	}
	sanitizeSnapshotValues(elements, opts.IncludeValues)
	snapshot := s.refs.Capture(s.page.URL(), elements)
	out := make([]map[string]any, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		entry := map[string]any{"ref": item.Ref, "role": item.Role, "name": item.Name}
		if item.Level != 0 {
			entry["level"] = item.Level
		}
		if snapshotValueAllowed(item.Role, item.ValueKind, item.Value, opts.IncludeValues) {
			entry["value"] = item.Value
		}
		if item.Href != "" {
			entry["href"] = item.Href
		}
		if item.Required {
			entry["required"] = true
		}
		out = append(out, entry)
	}
	return snapshotResult{URL: s.page.URL(), Title: title, Elements: out}, nil
}

func sanitizeSnapshotValues(elements []a11y.Element, includeValues bool) {
	for i := range elements {
		if !snapshotValueAllowed(elements[i].Role, elements[i].ValueKind, elements[i].Value, includeValues) {
			elements[i].Value = ""
			elements[i].ValueKind = ""
		}
	}
}

func snapshotValueAllowed(role string, valueKind string, value string, includeValues bool) bool {
	return includeValues && role == "textbox" && valueKind == "safe" && value != "" && len(value) <= maxSnapshotValueLength
}

func (s *gomoufoxSession) Fetch(ctx context.Context, opts fetchOptions) (fetchResult, error) {
	if opts.NavigateFirst != "" {
		if _, err := s.Navigate(ctx, opts.NavigateFirst, navigateOptions{WaitUntil: "domcontentloaded", Timeout: 30 * time.Second}); err != nil {
			return fetchResult{}, err
		}
	}
	result, err := s.page.EvaluateInternal(ctx, mcpFetchExpression, map[string]any{
		"url":      opts.URL,
		"method":   opts.Method,
		"headers":  fetchHeadersForEvaluation(opts.Headers),
		"body":     string(opts.Body),
		"maxBytes": opts.MaxBytes,
	})
	if err != nil {
		return fetchResult{}, err
	}
	var payload struct {
		OK        bool              `json:"ok"`
		URL       string            `json:"url"`
		Status    int               `json:"status"`
		Headers   map[string]string `json:"headers"`
		Body      string            `json:"body"`
		Message   string            `json:"message"`
		Truncated bool              `json:"truncated"`
	}
	if err := decodeJSONValue(result, &payload); err != nil {
		return fetchResult{}, err
	}
	if !payload.OK {
		return fetchResult{}, errors.New(payload.Message)
	}
	return fetchResult{URL: payload.URL, Status: payload.Status, Headers: payload.Headers, Body: []byte(payload.Body), Truncated: payload.Truncated}, nil
}

func (s *gomoufoxSession) Cookies(ctx context.Context, opts cookieOptions) (cookieResult, error) {
	if s.context == nil {
		return cookieResult{}, ErrInvalidCall
	}
	switch opts.Action {
	case "get":
		cookies, err := s.context.Cookies(ctx, opts.URLs...)
		return cookieResult{Cookies: fromGomoufoxCookies(cookies)}, err
	case "set":
		return cookieResult{Count: len(opts.Cookies)}, s.context.AddCookies(ctx, toGomoufoxCookies(opts.Cookies)...)
	case "clear":
		return cookieResult{}, s.context.ClearCookies(ctx)
	default:
		return cookieResult{}, ErrInvalidCall
	}
}

func (s *gomoufoxSession) SaveStorageState(ctx context.Context, path string) (*gomoufox.StorageState, error) {
	if s.context == nil {
		return &gomoufox.StorageState{}, nil
	}
	return s.context.StorageState(ctx, path)
}

func (s *gomoufoxSession) LoadStorageState(ctx context.Context, state *gomoufox.StorageState) error {
	if s.browser == nil || s.context == nil || s.page == nil || state == nil {
		return ErrInvalidCall
	}
	nextContext, err := s.browser.NewContext(ctx, gomoufox.WithStorageState(state))
	if err != nil {
		if errors.Is(err, gomoufox.ErrPersistentContextLimit) {
			return ErrInvalidCall
		}
		return err
	}
	nextPage, err := nextContext.NewPage(ctx)
	if err != nil {
		_ = nextContext.Close()
		return err
	}
	if err := verifyMCPInternalEvaluation(ctx, nextPage); err != nil {
		_ = nextPage.Close()
		_ = nextContext.Close()
		return err
	}
	oldPage := s.page
	oldContext := s.context
	s.page = nextPage
	s.context = nextContext
	s.refs.Clear()
	s.ensureObservations().resetAll()
	s.attachObservers(ctx)
	_ = oldPage.Close()
	_ = oldContext.Close()
	return nil
}

func (s *gomoufoxSession) Close() error {
	var err error
	if s.observations != nil {
		s.observations.resetAll()
	}
	if s.page != nil {
		err = s.page.Close()
	}
	if s.context != nil {
		if cerr := s.context.Close(); err == nil {
			err = cerr
		}
	}
	if s.closeBrowser && s.browser != nil {
		if berr := s.browser.Close(); err == nil {
			err = berr
		}
	}
	return err
}

func (s *gomoufoxSession) locator(ctx context.Context, target elementTarget) (gomoufox.Locator, error) {
	if target.Selector != "" {
		return s.page.Locator(target.Selector), nil
	}
	current, err := s.snapshotElements(ctx, 0, false, false)
	if err != nil {
		return nil, err
	}
	el, err := s.refs.Resolve(target.Ref, current)
	if err != nil {
		return nil, err
	}
	return s.page.Locator(el.Resolver), nil
}

func (s *gomoufoxSession) snapshotElements(ctx context.Context, maxElements int, interactiveOnly bool, includeValues bool) ([]a11y.Element, error) {
	result, err := s.page.EvaluateInternal(ctx, snapshotExpression, map[string]any{"max": maxElements, "interactiveOnly": interactiveOnly, "includeValues": includeValues})
	if err != nil {
		return nil, err
	}
	var elements []a11y.Element
	if err := decodeJSONValue(result, &elements); err != nil {
		return nil, err
	}
	return elements, nil
}

func (s *gomoufoxSession) viewport(ctx context.Context) (int, int) {
	result, err := s.page.EvaluateInternal(ctx, viewportMetricsExpression)
	if err != nil {
		return 0, 0
	}
	var payload struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	if err := decodeJSONValue(result, &payload); err != nil {
		return 0, 0
	}
	return payload.Width, payload.Height
}

func (s *gomoufoxSession) selectorMetrics(ctx context.Context, selector string) (int, int) {
	var payload struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	result, err := s.page.EvaluateInternal(ctx, selectorMetricsExpression, map[string]any{"selector": selector})
	if err == nil {
		_ = decodeJSONValue(result, &payload)
	}
	return payload.Width, payload.Height
}

func (s *gomoufoxSession) fullPageMetrics(ctx context.Context) (int, int) {
	var payload struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	result, err := s.page.EvaluateInternal(ctx, fullPageMetricsExpression)
	if err == nil {
		_ = decodeJSONValue(result, &payload)
	}
	return payload.Width, payload.Height
}

func screenshotPixelBytes(width, height int) int64 {
	if width <= 0 || height <= 0 {
		return 0
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	w := int64(width)
	h := int64(height)
	if w > maxInt64/h/4 {
		return maxInt64
	}
	return w * h * 4
}

const (
	screenshotAcquisitionMultiplier = int64(16)
	maxScreenshotAcquisitionBytes   = int64(128 * 1024 * 1024)
)

func screenshotWorkLimitBytes(maxBytes int) int64 {
	if maxBytes <= 0 {
		return 0
	}
	limit := int64(maxBytes) * screenshotAcquisitionMultiplier
	if limit > maxScreenshotAcquisitionBytes {
		return maxScreenshotAcquisitionBytes
	}
	return limit
}

func contextOptions(opts sessionOptions) ([]gomoufox.ContextOption, error) {
	out := []gomoufox.ContextOption{}
	if opts.locale != "" {
		out = append(out, gomoufox.WithContextLocale(opts.locale))
	}
	if opts.storageStatePath != "" {
		data, err := fileRead(opts.storageStatePath)
		if err != nil {
			return nil, err
		}
		var state gomoufox.StorageState
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, err
		}
		out = append(out, gomoufox.WithStorageState(&state))
	}
	return out, nil
}

func proxyConfig(raw string) (camoufoxcfg.ProxyConfig, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return camoufoxcfg.ProxyConfig{}, err
	}
	cfg := camoufoxcfg.ProxyConfig{Server: parsed.Scheme + "://" + parsed.Host}
	if parsed.User != nil {
		cfg.Username = parsed.User.Username()
		cfg.Password, _ = parsed.User.Password()
	}
	return cfg, nil
}

func timeoutContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func decodeJSONValue(value any, dst any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

var (
	errInternalEvaluationProbeFailed = errors.New("internal helper evaluation probe could not patch page world")
	errInternalEvaluationUnavailable = errors.New("internal helper evaluation shares page world")
)

func verifyMCPInternalEvaluation(ctx context.Context, page mcpPage) (err error) {
	patchResult, err := page.Evaluate(ctx, mainWorldExpression(mcpInternalProbePatchExpression))
	if err != nil {
		return err
	}
	defer func() {
		if _, restoreErr := page.Evaluate(ctx, mainWorldExpression(mcpInternalProbeRestoreExpression)); err == nil && restoreErr != nil {
			err = restoreErr
		}
	}()
	var patchPayload struct {
		OK           bool   `json:"ok"`
		QueryTag     string `json:"queryTag"`
		CSS          string `json:"css"`
		CSSAvailable bool   `json:"cssAvailable"`
	}
	if err := decodeJSONValue(patchResult, &patchPayload); err != nil {
		return err
	}
	if !patchPayload.OK || patchPayload.QueryTag != "GOMOUFOX_PATCHED" || (patchPayload.CSSAvailable && patchPayload.CSS != "gomoufox-patched") {
		return fmt.Errorf("%w: ok=%t query_tag=%q css=%q css_available=%t", errInternalEvaluationProbeFailed, patchPayload.OK, patchPayload.QueryTag, patchPayload.CSS, patchPayload.CSSAvailable)
	}
	result, err := page.EvaluateInternal(ctx, mcpInternalProbeExpression)
	if err != nil {
		return err
	}
	var payload struct {
		OK           bool `json:"ok"`
		HostFlag     bool `json:"hostFlag"`
		QueryPatched bool `json:"queryPatched"`
		CSSPatched   bool `json:"cssPatched"`
	}
	if err := decodeJSONValue(result, &payload); err != nil {
		return err
	}
	if !payload.OK {
		return fmt.Errorf("%w: host_flag=%t query_patched=%t css_patched=%t", errInternalEvaluationUnavailable, payload.HostFlag, payload.QueryPatched, payload.CSSPatched)
	}
	return nil
}

func fetchHeadersForEvaluation(headers map[string]string) map[string]any {
	out := make(map[string]any, len(headers))
	for key, value := range headers {
		out[key] = value
	}
	return out
}

func fromGomoufoxCookies(cookies []gomoufox.Cookie) []cookie {
	out := make([]cookie, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: c.Expires, HTTPOnly: c.HTTPOnly, Secure: c.Secure, SameSite: c.SameSite})
	}
	return out
}

func toGomoufoxCookies(cookies []cookie) []gomoufox.Cookie {
	out := make([]gomoufox.Cookie, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, gomoufox.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: c.Expires, HTTPOnly: c.HTTPOnly, Secure: c.Secure, SameSite: c.SameSite})
	}
	return out
}

var _ BrowserFactory = (*gomoufoxFactory)(nil)
