package pwbridge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	playwright "github.com/playwright-community/playwright-go"
)

func TestRealConnectorConnectReturnsContextErrorBeforeStartingDriver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := (RealConnector{}).Connect(ctx, "ws://127.0.0.1", ConnectOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got != nil {
		t.Fatalf("session = %#v, want nil", got)
	}
}

func TestRealConnectorConnectUsesInjectedPlaywrightRuntime(t *testing.T) {
	raw := &fakeBrowser{connected: true}
	browserType := &fakePlaywrightBrowserType{browser: raw}
	stopCalls := 0
	restore := replacePlaywrightRunner(t, func(opts *playwright.RunOptions) (playwrightRuntime, error) {
		if opts.DriverDirectory != "/driver" {
			t.Fatalf("driver directory = %q", opts.DriverDirectory)
		}
		return playwrightRuntime{firefox: browserType, stop: func() error {
			stopCalls++
			return nil
		}}, nil
	})
	defer restore()

	session, err := (RealConnector{DriverDirectory: "/driver"}).Connect(context.Background(), "ws://127.0.0.1:1234", ConnectOptions{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if browserType.endpoint != "ws://127.0.0.1:1234" || len(browserType.options) != 1 || browserType.options[0].Timeout == nil || *browserType.options[0].Timeout != 2000 {
		t.Fatalf("connect endpoint=%q options=%#v", browserType.endpoint, browserType.options)
	}
	if !session.Browser().IsConnected() {
		t.Fatal("session browser is not connected")
	}
	if err := session.Stop(); err != nil {
		t.Fatal(err)
	}
	if raw.closeCalls != 1 || stopCalls != 1 {
		t.Fatalf("close calls browser=%d runtime=%d", raw.closeCalls, stopCalls)
	}
}

func TestRealConnectorConnectInjectedRuntimeErrors(t *testing.T) {
	runErr := errors.New("run failed")
	restore := replacePlaywrightRunner(t, func(*playwright.RunOptions) (playwrightRuntime, error) {
		return playwrightRuntime{}, runErr
	})
	if session, err := (RealConnector{}).Connect(context.Background(), "ws://127.0.0.1", ConnectOptions{}); !errors.Is(err, runErr) || session != nil {
		t.Fatalf("run error session=%#v err=%v", session, err)
	}
	restore()

	connectErr := errors.New("connect failed")
	browserType := &fakePlaywrightBrowserType{err: connectErr}
	stopCalls := 0
	restore = replacePlaywrightRunner(t, func(*playwright.RunOptions) (playwrightRuntime, error) {
		return playwrightRuntime{firefox: browserType, stop: func() error {
			stopCalls++
			return nil
		}}, nil
	})
	defer restore()
	if session, err := (RealConnector{}).Connect(context.Background(), "ws://127.0.0.1", ConnectOptions{}); !errors.Is(err, connectErr) || session != nil {
		t.Fatalf("connect error session=%#v err=%v", session, err)
	}
	if stopCalls != 1 {
		t.Fatalf("runtime stop calls = %d", stopCalls)
	}
	if browserType.options[0].Timeout == nil || *browserType.options[0].Timeout != 30000 {
		t.Fatalf("default timeout options = %#v", browserType.options)
	}
}

func TestRunPlaywrightDefaultClosureUsesInjectedRunner(t *testing.T) {
	oldRun := playwrightRun
	t.Cleanup(func() { playwrightRun = oldRun })

	runErr := errors.New("run failed")
	playwrightRun = func(options ...*playwright.RunOptions) (*playwright.Playwright, error) {
		if len(options) != 1 || options[0].DriverDirectory != "/driver" {
			t.Fatalf("run options = %#v", options)
		}
		return nil, runErr
	}
	if runtime, err := runPlaywright(&playwright.RunOptions{DriverDirectory: "/driver"}); !errors.Is(err, runErr) || runtime.firefox != nil {
		t.Fatalf("run error runtime=%#v err=%v", runtime, err)
	}

	playwrightRun = func(options ...*playwright.RunOptions) (*playwright.Playwright, error) {
		return &playwright.Playwright{}, nil
	}
	runtime, err := runPlaywright(&playwright.RunOptions{})
	if err != nil || runtime.stop == nil {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
}

func TestEnsureDriverSkipsBrowserDownloads(t *testing.T) {
	oldInstall := playwrightInstall
	t.Cleanup(func() { playwrightInstall = oldInstall })
	called := false
	playwrightInstall = func(options ...*playwright.RunOptions) error {
		called = true
		if len(options) != 1 {
			t.Fatalf("install options = %#v", options)
		}
		if options[0].DriverDirectory != "/driver" || !options[0].SkipInstallBrowsers {
			t.Fatalf("install option = %#v", options[0])
		}
		return nil
	}
	if err := EnsureDriver("/driver"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("playwright install not called")
	}

	installErr := errors.New("install failed")
	playwrightInstall = func(...*playwright.RunOptions) error { return installErr }
	if err := EnsureDriver(""); !errors.Is(err, installErr) {
		t.Fatalf("install err = %v", err)
	}
}

func TestRealSessionStopClosesBrowser(t *testing.T) {
	raw := &fakeBrowser{}
	session := &realSession{browser: &realBrowser{raw: raw}}

	if got := session.Browser(); got != session.browser {
		t.Fatalf("Browser() = %#v, want wrapped browser", got)
	}
	if err := session.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if raw.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want 1", raw.closeCalls)
	}
}

func TestRealSessionStopAllowsNilBrowser(t *testing.T) {
	if err := (&realSession{}).Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestRealBrowserDelegatesObservableBehavior(t *testing.T) {
	ctxA := &fakeBrowserContext{}
	ctxB := &fakeBrowserContext{}
	newCtx := &fakeBrowserContext{}
	page := &fakePage{}
	raw := &fakeBrowser{
		connected:      true,
		version:        "123.4",
		contexts:       []playwright.BrowserContext{ctxA, ctxB},
		newContextResp: newCtx,
		newPageResp:    page,
	}
	browser := &realBrowser{raw: raw}

	if err := browser.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !browser.IsConnected() {
		t.Fatalf("IsConnected() = false, want true")
	}
	if browser.Version() != "123.4" {
		t.Fatalf("Version() = %q", browser.Version())
	}

	disconnected := false
	browser.OnDisconnected(func() { disconnected = true })
	if raw.onDisconnected == nil {
		t.Fatalf("OnDisconnected callback was not registered")
	}
	raw.onDisconnected(raw)
	if !disconnected {
		t.Fatalf("registered OnDisconnected callback did not run")
	}

	contexts := browser.Contexts()
	if len(contexts) != 2 {
		t.Fatalf("Contexts() length = %d, want 2", len(contexts))
	}
	if contexts[0].Raw() != ctxA || contexts[1].Raw() != ctxB {
		t.Fatalf("Contexts() did not wrap raw contexts")
	}

	gotCtx, err := browser.NewContext(ContextOptions{Locale: "en-GB"})
	if err != nil {
		t.Fatalf("NewContext() error = %v", err)
	}
	if gotCtx.Raw() != newCtx {
		t.Fatalf("NewContext() raw = %#v, want new context", gotCtx.Raw())
	}
	if raw.newContextOptions.Locale == nil || *raw.newContextOptions.Locale != "en-GB" {
		t.Fatalf("NewContext options = %#v", raw.newContextOptions)
	}

	gotPage, err := browser.NewPage(ContextOptions{TimezoneID: "Europe/London"})
	if err != nil {
		t.Fatalf("NewPage() error = %v", err)
	}
	if gotPage.Raw() != page {
		t.Fatalf("NewPage() raw = %#v, want page", gotPage.Raw())
	}
	if raw.newPageOptions.TimezoneId == nil || *raw.newPageOptions.TimezoneId != "Europe/London" {
		t.Fatalf("NewPage options = %#v", raw.newPageOptions)
	}
}

func TestRealBrowserPropagatesCreationErrors(t *testing.T) {
	boom := errors.New("boom")
	raw := &fakeBrowser{newContextErr: boom, newPageErr: boom}
	browser := &realBrowser{raw: raw}

	ctx, err := browser.NewContext(ContextOptions{Locale: "en-US"})
	if !errors.Is(err, boom) {
		t.Fatalf("NewContext() error = %v, want boom", err)
	}
	if ctx != nil {
		t.Fatalf("NewContext() = %#v, want nil", ctx)
	}
	if raw.newContextOptions.Locale == nil || *raw.newContextOptions.Locale != "en-US" {
		t.Fatalf("NewContext options = %#v", raw.newContextOptions)
	}

	page, err := browser.NewPage(ContextOptions{TimezoneID: "UTC"})
	if !errors.Is(err, boom) {
		t.Fatalf("NewPage() error = %v, want boom", err)
	}
	if page != nil {
		t.Fatalf("NewPage() = %#v, want nil", page)
	}
	if raw.newPageOptions.TimezoneId == nil || *raw.newPageOptions.TimezoneId != "UTC" {
		t.Fatalf("NewPage options = %#v", raw.newPageOptions)
	}
}

func TestRealContextDelegatesObservableBehavior(t *testing.T) {
	pageA := &fakePage{}
	pageB := &fakePage{}
	newPage := &fakePage{}
	sameSite := playwright.SameSiteAttribute("Lax")
	raw := &fakeBrowserContext{
		newPageResp: newPage,
		pages:       []playwright.Page{pageA, pageB},
		cookiesResp: []playwright.Cookie{{
			Name: "sid", Value: "v", Domain: ".example.com", Path: "/", Expires: 1,
			HttpOnly: true, Secure: true, SameSite: &sameSite,
		}},
		storageStateResp: &playwright.StorageState{
			Cookies: []playwright.Cookie{{Name: "a", Value: "b"}},
			Origins: []playwright.Origin{{
				Origin: "https://example.com",
				LocalStorage: []playwright.NameValue{
					{Name: "k", Value: "v"},
				},
			}},
		},
	}
	ctx := &realContext{raw: raw}

	gotPage, err := ctx.NewPage()
	if err != nil {
		t.Fatalf("NewPage() error = %v", err)
	}
	if gotPage.Raw() != newPage {
		t.Fatalf("NewPage() raw = %#v, want new page", gotPage.Raw())
	}

	pages := ctx.Pages()
	if len(pages) != 2 || pages[0].Raw() != pageA || pages[1].Raw() != pageB {
		t.Fatalf("Pages() = %#v", pages)
	}

	cookies, err := ctx.Cookies("https://example.com")
	if err != nil {
		t.Fatalf("Cookies() error = %v", err)
	}
	if !reflect.DeepEqual(raw.cookieURLs, []string{"https://example.com"}) {
		t.Fatalf("Cookies URLs = %#v", raw.cookieURLs)
	}
	if len(cookies) != 1 || cookies[0].SameSite != "Lax" || !cookies[0].HTTPOnly {
		t.Fatalf("Cookies() = %#v", cookies)
	}

	if err := ctx.AddCookies(Cookie{Name: "new", Value: "cookie", SameSite: "Strict"}); err != nil {
		t.Fatalf("AddCookies() error = %v", err)
	}
	if len(raw.addedCookies) != 1 || raw.addedCookies[0].Name != "new" || string(*raw.addedCookies[0].SameSite) != "Strict" {
		t.Fatalf("added cookies = %#v", raw.addedCookies)
	}

	if err := ctx.ClearCookies(); err != nil {
		t.Fatalf("ClearCookies() error = %v", err)
	}
	state, err := ctx.StorageState()
	if err != nil {
		t.Fatalf("StorageState() error = %v", err)
	}
	if len(state.Cookies) != 1 || len(state.Origins) != 1 || state.Origins[0].LocalStorage[0].Name != "k" {
		t.Fatalf("StorageState() = %#v", state)
	}

	routeCalled := false
	if err := ctx.Route("**/*", func(route Route) {
		routeCalled = true
		if route.Request().URL() != "https://route.example" {
			t.Fatalf("route request URL = %q", route.Request().URL())
		}
	}); err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if raw.routePattern != "**/*" || raw.routeHandler == nil {
		t.Fatalf("Route registration = %q, handler registered: %t", raw.routePattern, raw.routeHandler != nil)
	}
	raw.routeHandler(&fakeRoute{request: &fakeRequest{url: "https://route.example"}})
	if !routeCalled {
		t.Fatalf("route handler did not run")
	}

	if err := ctx.Unroute("**/*", nil); err != nil {
		t.Fatalf("Unroute() error = %v", err)
	}
	if raw.unroutePattern != "**/*" {
		t.Fatalf("Unroute pattern = %q", raw.unroutePattern)
	}

	requestSeen := ""
	ctx.OnRequest(func(request Request) { requestSeen = request.URL() })
	raw.onRequest(&fakeRequest{url: "https://request.example"})
	if requestSeen != "https://request.example" {
		t.Fatalf("OnRequest saw %q", requestSeen)
	}

	responseSeen := 0
	ctx.OnResponse(func(response Response) { responseSeen = response.Status() })
	raw.onResponse(&fakeResponse{status: 204})
	if responseSeen != 204 {
		t.Fatalf("OnResponse saw %d", responseSeen)
	}

	if err := ctx.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if raw.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want 1", raw.closeCalls)
	}
	if ctx.Raw() != raw {
		t.Fatalf("Raw() = %#v, want raw context", ctx.Raw())
	}
}

func TestRealContextPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	raw := &fakeBrowserContext{
		newPageErr:      boom,
		cookiesErr:      boom,
		storageStateErr: boom,
	}
	ctx := &realContext{raw: raw}

	page, err := ctx.NewPage()
	if !errors.Is(err, boom) {
		t.Fatalf("NewPage() error = %v, want boom", err)
	}
	if page != nil {
		t.Fatalf("NewPage() = %#v, want nil", page)
	}

	cookies, err := ctx.Cookies("https://example.com")
	if !errors.Is(err, boom) {
		t.Fatalf("Cookies() error = %v, want boom", err)
	}
	if cookies != nil {
		t.Fatalf("Cookies() = %#v, want nil", cookies)
	}

	state, err := ctx.StorageState()
	if !errors.Is(err, boom) {
		t.Fatalf("StorageState() error = %v, want boom", err)
	}
	if state != nil {
		t.Fatalf("StorageState() = %#v, want nil", state)
	}
}

func TestRealPageDelegatesObservableBehavior(t *testing.T) {
	response := &fakeResponse{url: "https://response.example", status: 200, ok: true}
	element := &fakeElement{}
	locator := &fakeLocator{}
	mainFrame := &fakeFrame{}
	context := &fakeBrowserContext{
		cookiesResp: []playwright.Cookie{{Name: "page", Value: "cookie"}},
	}
	raw := &fakePage{
		gotoResp:           response,
		goBackResp:         response,
		goForwardResp:      response,
		reloadResp:         response,
		evaluateResp:       "eval",
		contentResp:        "<html></html>",
		titleResp:          "Title",
		url:                "https://page.example",
		waitForSelectorRes: element,
		screenshotResp:     []byte("png"),
		pdfResp:            []byte("pdf"),
		contextResp:        context,
		locatorResp:        locator,
		mainFrame:          mainFrame,
	}
	page := &realPage{raw: raw}

	got, err := page.Goto("https://target.example", GotoOptions{WaitUntil: "networkidle", Referer: "https://ref.example", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("Goto() error = %v", err)
	}
	if got.URL() != "https://response.example" || raw.gotoURL != "https://target.example" {
		t.Fatalf("Goto() response/url = %q/%q", got.URL(), raw.gotoURL)
	}
	if raw.gotoOptions.WaitUntil == nil || string(*raw.gotoOptions.WaitUntil) != "networkidle" || *raw.gotoOptions.Timeout != 2000 {
		t.Fatalf("Goto options = %#v", raw.gotoOptions)
	}

	for name, call := range map[string]func(NavigateOptions) (Response, error){
		"GoBack":    page.GoBack,
		"GoForward": page.GoForward,
		"Reload":    page.Reload,
	} {
		got, err := call(NavigateOptions{WaitUntil: "load", Timeout: time.Second})
		if err != nil {
			t.Fatalf("%s() error = %v", name, err)
		}
		if got.Status() != 200 {
			t.Fatalf("%s() status = %d", name, got.Status())
		}
	}
	actionCalled := false
	err = page.RunAndWaitForNavigation(func() error {
		actionCalled = true
		return nil
	}, NavigateOptions{WaitUntil: "domcontentloaded", Timeout: 1500 * time.Millisecond})
	if err != nil {
		t.Fatalf("RunAndWaitForNavigation() error = %v", err)
	}
	if !actionCalled || raw.expectEventName != "framenavigated" || raw.expectEventOptions.Timeout == nil || *raw.expectEventOptions.Timeout != 1500 || !raw.expectEventPredicateHit || raw.expectEventPredicateMiss || raw.waitForLoadOptions.State == nil || string(*raw.waitForLoadOptions.State) != "domcontentloaded" || raw.waitForLoadOptions.Timeout == nil || *raw.waitForLoadOptions.Timeout != 1500 {
		t.Fatalf("RunAndWaitForNavigation action=%t event=%q opts=%#v hit=%t miss=%t load=%#v", actionCalled, raw.expectEventName, raw.expectEventOptions, raw.expectEventPredicateHit, raw.expectEventPredicateMiss, raw.waitForLoadOptions)
	}

	evaluated, err := page.Evaluate("() => 1", "arg")
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if evaluated != "eval" || raw.evaluateExpression != "() => 1" || raw.evaluateArgs[0] != "arg" {
		t.Fatalf("Evaluate() = %#v, expr=%q args=%#v", evaluated, raw.evaluateExpression, raw.evaluateArgs)
	}
	internalEvaluated, err := page.EvaluateInternal("() => 2", "internal")
	if err != nil {
		t.Fatalf("EvaluateInternal() error = %v", err)
	}
	if internalEvaluated != "eval" || raw.evaluateExpression != "() => 2" || raw.evaluateArgs[0] != "internal" {
		t.Fatalf("EvaluateInternal() = %#v, expr=%q args=%#v", internalEvaluated, raw.evaluateExpression, raw.evaluateArgs)
	}

	if err := page.AddInitScript("window.ready = true"); err != nil {
		t.Fatalf("AddInitScript() error = %v", err)
	}
	if raw.initScript.Content == nil || *raw.initScript.Content != "window.ready = true" {
		t.Fatalf("init script = %#v", raw.initScript)
	}
	if content, err := page.Content(); err != nil || content != "<html></html>" {
		t.Fatalf("Content() = %q, %v", content, err)
	}
	if err := page.SetContent("<main></main>", GotoOptions{Timeout: time.Second}); err != nil {
		t.Fatalf("SetContent() error = %v", err)
	}
	if raw.setContentHTML != "<main></main>" || raw.setContentOptions.Timeout == nil {
		t.Fatalf("SetContent = %q/%#v", raw.setContentHTML, raw.setContentOptions)
	}
	if title, err := page.Title(); err != nil || title != "Title" {
		t.Fatalf("Title() = %q, %v", title, err)
	}
	if page.URL() != "https://page.example" {
		t.Fatalf("URL() = %q", page.URL())
	}

	if err := page.WaitForLoadState("domcontentloaded", time.Second); err != nil {
		t.Fatalf("WaitForLoadState() error = %v", err)
	}
	if raw.waitForLoadOptions.State == nil || string(*raw.waitForLoadOptions.State) != "domcontentloaded" || *raw.waitForLoadOptions.Timeout != 1000 {
		t.Fatalf("WaitForLoadState options = %#v", raw.waitForLoadOptions)
	}

	handle, err := page.WaitForSelector("#app", WaitForSelectorOptions{State: "visible", Timeout: time.Second})
	if err != nil {
		t.Fatalf("WaitForSelector() error = %v", err)
	}
	if handle.Raw() != element || raw.waitForSelectorSelector != "#app" {
		t.Fatalf("WaitForSelector handle/selector = %#v/%q", handle.Raw(), raw.waitForSelectorSelector)
	}
	if raw.waitForSelectorOptions.State == nil || string(*raw.waitForSelectorOptions.State) != "visible" {
		t.Fatalf("WaitForSelector options = %#v", raw.waitForSelectorOptions)
	}

	if err := page.WaitForURL("**/done", GotoOptions{WaitUntil: "commit", Timeout: time.Second}); err != nil {
		t.Fatalf("WaitForURL() error = %v", err)
	}
	if raw.waitForURLPattern != "**/done" || raw.waitForURLOptions.WaitUntil == nil {
		t.Fatalf("WaitForURL = %#v/%#v", raw.waitForURLPattern, raw.waitForURLOptions)
	}

	if shot, err := page.Screenshot(ScreenshotOptions{FullPage: true, Type: "png"}); err != nil || string(shot) != "png" {
		t.Fatalf("Screenshot() = %q, %v", shot, err)
	}
	if raw.screenshotOptions.FullPage == nil || !*raw.screenshotOptions.FullPage {
		t.Fatalf("Screenshot options = %#v", raw.screenshotOptions)
	}
	if pdf, err := page.PDF(PDFOptions{Format: "A4"}); err != nil || string(pdf) != "pdf" {
		t.Fatalf("PDF() = %q, %v", pdf, err)
	}
	if raw.pdfOptions.Format == nil || *raw.pdfOptions.Format != "A4" {
		t.Fatalf("PDF options = %#v", raw.pdfOptions)
	}

	cookies, err := page.Cookies("https://page.example")
	if err != nil {
		t.Fatalf("Cookies() error = %v", err)
	}
	if len(cookies) != 1 || cookies[0].Name != "page" || context.cookieURLs[0] != "https://page.example" {
		t.Fatalf("Cookies() = %#v urls=%#v", cookies, context.cookieURLs)
	}

	routeCalled := false
	if err := page.Route("**/*", func(route Route) {
		routeCalled = true
		if route.Request().URL() != "https://page-route.example" {
			t.Fatalf("route URL = %q", route.Request().URL())
		}
	}); err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	raw.routeHandler(&fakeRoute{request: &fakeRequest{url: "https://page-route.example"}})
	if raw.routePattern != "**/*" || !routeCalled {
		t.Fatalf("Route registration/call = %q/%t", raw.routePattern, routeCalled)
	}
	if err := page.Unroute("**/*", nil); err != nil {
		t.Fatalf("Unroute() error = %v", err)
	}
	if raw.unroutePattern != "**/*" {
		t.Fatalf("Unroute pattern = %q", raw.unroutePattern)
	}

	requestSeen := ""
	page.OnRequest(func(request Request) { requestSeen = request.URL() })
	raw.onRequest(&fakeRequest{url: "https://page-request.example"})
	if requestSeen != "https://page-request.example" {
		t.Fatalf("OnRequest saw %q", requestSeen)
	}
	requestFailedSeen := ""
	page.OnRequestFailed(func(request Request) { requestFailedSeen = request.URL() })
	raw.onRequestFailed(&fakeRequest{url: "https://page-failed.example"})
	if requestFailedSeen != "https://page-failed.example" {
		t.Fatalf("OnRequestFailed saw %q", requestFailedSeen)
	}
	responseSeen := 0
	page.OnResponse(func(response Response) { responseSeen = response.Status() })
	raw.onResponse(&fakeResponse{status: 201})
	if responseSeen != 201 {
		t.Fatalf("OnResponse saw %d", responseSeen)
	}
	pageErrorSeen := ""
	page.OnPageError(func(err error) { pageErrorSeen = err.Error() })
	raw.onPageError(errors.New("page boom"))
	if pageErrorSeen != "page boom" {
		t.Fatalf("OnPageError saw %q", pageErrorSeen)
	}
	consoleSeen := ConsoleMessage{}
	page.OnConsole(func(message ConsoleMessage) { consoleSeen = message })
	raw.onConsole(&fakeConsoleMessage{typ: "warning", text: "hello"})
	if consoleSeen.Type != "warning" || consoleSeen.Text != "hello" || len(consoleSeen.Args) != 0 {
		t.Fatalf("OnConsole saw %#v", consoleSeen)
	}
	dialog := &fakePWDialog{typ: "prompt", message: "hello", defaultValue: "name"}
	dialogSeen := ""
	page.OnDialog(func(d Dialog) {
		dialogSeen = d.Type() + ":" + d.Message() + ":" + d.DefaultValue()
		_ = d.Accept("ok")
	})
	raw.onDialog(dialog)
	if dialogSeen != "prompt:hello:name" || !dialog.accepted || dialog.acceptText != "ok" {
		t.Fatalf("OnDialog saw %q dialog=%#v", dialogSeen, dialog)
	}
	dismissDialog := &fakePWDialog{typ: "alert"}
	if err := (&realDialog{raw: dismissDialog}).Dismiss(); err != nil || !dismissDialog.dismissed {
		t.Fatalf("Dismiss dialog=%#v err=%v", dismissDialog, err)
	}
	if err := page.Wheel(3, 7); err != nil || raw.wheelX != 3 || raw.wheelY != 7 {
		t.Fatalf("Wheel = %v deltas=%v/%v", err, raw.wheelX, raw.wheelY)
	}

	gotLocator := page.Locator("#submit")
	wrappedLocator, ok := gotLocator.(*realLocator)
	if !ok {
		t.Fatalf("Locator() = %T, want *realLocator", gotLocator)
	}
	if wrappedLocator.raw != locator || raw.locatorSelector != "#submit" {
		t.Fatalf("Locator raw/selector = %#v/%q", wrappedLocator.raw, raw.locatorSelector)
	}

	if err := page.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if raw.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want 1", raw.closeCalls)
	}
	if page.Raw() != raw {
		t.Fatalf("Raw() = %#v, want raw page", page.Raw())
	}
}

func TestRealPageNilNavigationResponsesReturnNilWithoutError(t *testing.T) {
	page := &realPage{raw: &fakePage{}}

	if got, err := page.Goto("https://example.com", GotoOptions{}); got != nil || err != nil {
		t.Fatalf("Goto() = %#v, %v; want nil, nil", got, err)
	}
	if got, err := page.GoBack(NavigateOptions{}); got != nil || err != nil {
		t.Fatalf("GoBack() = %#v, %v; want nil, nil", got, err)
	}
	if got, err := page.GoForward(NavigateOptions{}); got != nil || err != nil {
		t.Fatalf("GoForward() = %#v, %v; want nil, nil", got, err)
	}
	if got, err := page.Reload(NavigateOptions{}); got != nil || err != nil {
		t.Fatalf("Reload() = %#v, %v; want nil, nil", got, err)
	}
	if got, err := page.WaitForSelector("#missing", WaitForSelectorOptions{}); got != nil || err != nil {
		t.Fatalf("WaitForSelector() = %#v, %v; want nil, nil", got, err)
	}
}

func TestRealPageRunAndWaitForNavigationCommitSkipsLoadState(t *testing.T) {
	raw := &fakePage{mainFrame: &fakeFrame{}}
	page := &realPage{raw: raw}

	if err := page.RunAndWaitForNavigation(func() error { return nil }, NavigateOptions{WaitUntil: "commit"}); err != nil {
		t.Fatalf("RunAndWaitForNavigation() error = %v", err)
	}
	if raw.waitForLoadOptions.State != nil || raw.waitForLoadOptions.Timeout != nil {
		t.Fatalf("load state options = %#v, want zero value", raw.waitForLoadOptions)
	}

	actionErr := errors.New("action failed")
	if err := page.RunAndWaitForNavigation(func() error { return actionErr }, NavigateOptions{}); !errors.Is(err, actionErr) {
		t.Fatalf("RunAndWaitForNavigation action error = %v", err)
	}
}

func TestRealLocatorDelegatesObservableBehavior(t *testing.T) {
	firstRaw := &fakeLocator{}
	lastRaw := &fakeLocator{}
	nthRaw := &fakeLocator{}
	raw := &fakeLocator{
		textContentResp: "text",
		innerHTMLResp:   "<strong>text</strong>",
		attributeResp:   "button",
		isVisibleResp:   true,
		countResp:       3,
		firstResp:       firstRaw,
		lastResp:        lastRaw,
		nthResp:         nthRaw,
		screenshotResp:  []byte("locator-png"),
	}
	locator := &realLocator{raw: raw}

	if err := locator.Click(LocatorClickOptions{Timeout: 2 * time.Second, Force: true, Button: "right", ClickCount: 2}); err != nil {
		t.Fatalf("Click() error = %v", err)
	}
	if raw.clickOptions.Timeout == nil || *raw.clickOptions.Timeout != 2000 || raw.clickOptions.Force == nil || !*raw.clickOptions.Force || raw.clickOptions.Button == nil || string(*raw.clickOptions.Button) != "right" || raw.clickOptions.ClickCount == nil || *raw.clickOptions.ClickCount != 2 {
		t.Fatalf("Click options = %#v", raw.clickOptions)
	}
	if err := locator.Click(LocatorClickOptions{}); err != nil {
		t.Fatalf("Click() default options error = %v", err)
	}
	if raw.clickOptions.Timeout != nil || raw.clickOptions.Force != nil || raw.clickOptions.Button != nil || raw.clickOptions.ClickCount != nil {
		t.Fatalf("Click default options = %#v, want nil pointers", raw.clickOptions)
	}

	if err := locator.Fill("value", LocatorFillOptions{Timeout: 3 * time.Second, Force: true}); err != nil {
		t.Fatalf("Fill() error = %v", err)
	}
	if raw.fillValue != "value" || raw.fillOptions.Timeout == nil || *raw.fillOptions.Timeout != 3000 || raw.fillOptions.Force == nil || !*raw.fillOptions.Force {
		t.Fatalf("Fill value/options = %q/%#v", raw.fillValue, raw.fillOptions)
	}

	if err := locator.Type("typed", LocatorTypeOptions{Timeout: 4 * time.Second, Delay: 25 * time.Millisecond}); err != nil {
		t.Fatalf("Type() error = %v", err)
	}
	if raw.typeValue != "typed" || raw.pressSequentiallyOptions.Timeout == nil || *raw.pressSequentiallyOptions.Timeout != 4000 || raw.pressSequentiallyOptions.Delay == nil || *raw.pressSequentiallyOptions.Delay != 25 {
		t.Fatalf("Type value/options = %q/%#v", raw.typeValue, raw.pressSequentiallyOptions)
	}
	if err := locator.Press("Enter", LocatorPressOptions{Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("Press() error = %v", err)
	}
	if raw.pressKey != "Enter" || raw.pressOptions.Timeout == nil || *raw.pressOptions.Timeout != 5000 {
		t.Fatalf("Press key/options = %q/%#v", raw.pressKey, raw.pressOptions)
	}
	if err := locator.Hover(LocatorHoverOptions{Timeout: 6 * time.Second, Force: true}); err != nil {
		t.Fatalf("Hover() error = %v", err)
	}
	if raw.hoverOptions.Timeout == nil || *raw.hoverOptions.Timeout != 6000 || raw.hoverOptions.Force == nil || !*raw.hoverOptions.Force {
		t.Fatalf("Hover options = %#v", raw.hoverOptions)
	}
	if err := locator.ScrollIntoViewIfNeeded(LocatorOptions{Timeout: 7 * time.Second}); err != nil {
		t.Fatalf("ScrollIntoViewIfNeeded() error = %v", err)
	}
	if raw.scrollOptions.Timeout == nil || *raw.scrollOptions.Timeout != 7000 {
		t.Fatalf("Scroll options = %#v", raw.scrollOptions)
	}
	raw.selectResp = []string{"us"}
	selected, err := locator.SelectOption(LocatorSelectOptions{Timeout: 8 * time.Second, Force: true, Values: []string{"us"}})
	if err != nil || strings.Join(selected, ",") != "us" || raw.selectOptions.Timeout == nil || *raw.selectOptions.Timeout != 8000 || raw.selectOptions.Force == nil || !*raw.selectOptions.Force || raw.selectValues.Values == nil || strings.Join(*raw.selectValues.Values, ",") != "us" {
		t.Fatalf("SelectOption = %v %#v %#v %v", selected, raw.selectOptions, raw.selectValues, err)
	}
	selected, err = locator.SelectOption(LocatorSelectOptions{Labels: []string{"United States"}, Indexes: []int{1}})
	if err != nil || raw.selectValues.Labels == nil || strings.Join(*raw.selectValues.Labels, ",") != "United States" || raw.selectValues.Indexes == nil || len(*raw.selectValues.Indexes) != 1 || (*raw.selectValues.Indexes)[0] != 1 {
		t.Fatalf("Select labels/indexes = %v %#v %v", selected, raw.selectValues, err)
	}
	if err := locator.SetChecked(true, LocatorSetCheckedOptions{Timeout: 9 * time.Second, Force: true}); err != nil {
		t.Fatalf("SetChecked() error = %v", err)
	}
	if !raw.checked || raw.setCheckedOptions.Timeout == nil || *raw.setCheckedOptions.Timeout != 9000 || raw.setCheckedOptions.Force == nil || !*raw.setCheckedOptions.Force {
		t.Fatalf("SetChecked options = checked:%v %#v", raw.checked, raw.setCheckedOptions)
	}
	if err := locator.SetInputFiles([]string{"a.txt", "b.txt"}, LocatorSetInputFilesOptions{Timeout: 10 * time.Second}); err != nil {
		t.Fatalf("SetInputFiles() error = %v", err)
	}
	if strings.Join(raw.inputFiles, ",") != "a.txt,b.txt" || raw.setInputFilesOptions.Timeout == nil || *raw.setInputFilesOptions.Timeout != 10000 {
		t.Fatalf("SetInputFiles = %#v %#v", raw.inputFiles, raw.setInputFilesOptions)
	}

	text, err := locator.TextContent(LocatorOptions{Timeout: 4 * time.Second})
	if err != nil || text != "text" {
		t.Fatalf("TextContent() = %q, %v", text, err)
	}
	if raw.textContentOptions.Timeout == nil || *raw.textContentOptions.Timeout != 4000 {
		t.Fatalf("TextContent options = %#v", raw.textContentOptions)
	}

	html, err := locator.InnerHTML(LocatorOptions{Timeout: 5 * time.Second})
	if err != nil || html != "<strong>text</strong>" {
		t.Fatalf("InnerHTML() = %q, %v", html, err)
	}
	if raw.innerHTMLOptions.Timeout == nil || *raw.innerHTMLOptions.Timeout != 5000 {
		t.Fatalf("InnerHTML options = %#v", raw.innerHTMLOptions)
	}

	attr, err := locator.GetAttribute("role", LocatorOptions{Timeout: 6 * time.Second})
	if err != nil || attr != "button" {
		t.Fatalf("GetAttribute() = %q, %v", attr, err)
	}
	if raw.attributeName != "role" || raw.getAttributeOptions.Timeout == nil || *raw.getAttributeOptions.Timeout != 6000 {
		t.Fatalf("GetAttribute name/options = %q/%#v", raw.attributeName, raw.getAttributeOptions)
	}

	visible, err := locator.IsVisible(LocatorOptions{Timeout: 7 * time.Second})
	if err != nil || !visible {
		t.Fatalf("IsVisible() = %t, %v", visible, err)
	}
	if raw.isVisibleOptions.Timeout == nil || *raw.isVisibleOptions.Timeout != 7000 { //nolint:staticcheck // Verifies bridge option passthrough for the current Playwright API.
		t.Fatalf("IsVisible options = %#v", raw.isVisibleOptions)
	}

	count, err := locator.Count()
	if err != nil || count != 3 {
		t.Fatalf("Count() = %d, %v", count, err)
	}

	first, ok := locator.First().(*realLocator)
	if !ok || first.raw != firstRaw {
		t.Fatalf("First() = %#v, want wrapped first locator", first)
	}
	last, ok := locator.Last().(*realLocator)
	if !ok || last.raw != lastRaw {
		t.Fatalf("Last() = %#v, want wrapped last locator", last)
	}
	nth, ok := locator.Nth(2).(*realLocator)
	if !ok || nth.raw != nthRaw || raw.nthIndex != 2 {
		t.Fatalf("Nth() = %#v index=%d, want wrapped nth locator", nth, raw.nthIndex)
	}

	if err := locator.WaitFor(LocatorWaitForOptions{State: "visible", Timeout: 8 * time.Second}); err != nil {
		t.Fatalf("WaitFor() error = %v", err)
	}
	if raw.waitOptions.State == nil || string(*raw.waitOptions.State) != "visible" || raw.waitOptions.Timeout == nil || *raw.waitOptions.Timeout != 8000 {
		t.Fatalf("WaitFor options = %#v", raw.waitOptions)
	}

	shot, err := locator.Screenshot(ScreenshotOptions{Type: "png", Quality: 60})
	if err != nil || string(shot) != "locator-png" {
		t.Fatalf("Screenshot() = %q, %v", shot, err)
	}
	if raw.screenshotOptions.Type == nil || string(*raw.screenshotOptions.Type) != "png" || raw.screenshotOptions.Quality == nil || *raw.screenshotOptions.Quality != 60 {
		t.Fatalf("Screenshot options = %#v", raw.screenshotOptions)
	}
}

func TestRealRouteDelegatesAndWrapsFetchResponse(t *testing.T) {
	raw := &fakeRoute{request: &fakeRequest{url: "https://request.example"}, fetchResp: &fakeAPIResponse{status: 202, ok: true}}
	route := &realRoute{raw: raw}

	if route.Request().URL() != "https://request.example" {
		t.Fatalf("Request URL = %q", route.Request().URL())
	}
	if err := route.Continue(&ContinueOptions{URL: "https://continue.example", Method: "PATCH", Headers: map[string]string{"x": "1"}, PostData: []byte("body")}); err != nil {
		t.Fatalf("Continue() error = %v", err)
	}
	if raw.continueOptions.URL == nil || *raw.continueOptions.URL != "https://continue.example" || string(raw.continueOptions.PostData.([]byte)) != "body" {
		t.Fatalf("Continue options = %#v", raw.continueOptions)
	}
	if err := route.Fulfill(&FulfillOptions{Status: 206, BodyString: "ok"}); err != nil {
		t.Fatalf("Fulfill() error = %v", err)
	}
	if raw.fulfillOptions.Status == nil || *raw.fulfillOptions.Status != 206 || raw.fulfillOptions.Body != "ok" {
		t.Fatalf("Fulfill options = %#v", raw.fulfillOptions)
	}
	if err := route.Abort(""); err != nil {
		t.Fatalf("Abort(default) error = %v", err)
	}
	if raw.abortCode != "failed" {
		t.Fatalf("Abort default code = %q", raw.abortCode)
	}
	if err := route.Abort("timedout"); err != nil {
		t.Fatalf("Abort(custom) error = %v", err)
	}
	if raw.abortCode != "timedout" {
		t.Fatalf("Abort custom code = %q", raw.abortCode)
	}

	resp, err := route.Fetch(&FetchOptions{URL: "https://fetch.example", Method: "GET"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if resp.Status() != 202 || !resp.OK() || resp.Request() != nil {
		t.Fatalf("Fetch response = status %d ok %t request %#v", resp.Status(), resp.OK(), resp.Request())
	}
	if raw.fetchOptions.URL == nil || *raw.fetchOptions.URL != "https://fetch.example" {
		t.Fatalf("Fetch options = %#v", raw.fetchOptions)
	}
}

func TestRealRouteFetchPropagatesNilAndErrors(t *testing.T) {
	boom := errors.New("boom")
	resp, err := (&realRoute{raw: &fakeRoute{fetchErr: boom}}).Fetch(&FetchOptions{URL: "https://api.example"})
	if !errors.Is(err, boom) {
		t.Fatalf("Fetch() error = %v, want boom", err)
	}
	if resp != nil {
		t.Fatalf("Fetch() response = %#v, want nil", resp)
	}

	resp, err = (&realRoute{raw: &fakeRoute{}}).Fetch(&FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch(nil response) error = %v", err)
	}
	if resp != nil {
		t.Fatalf("Fetch(nil response) = %#v, want nil", resp)
	}
}

func TestRealRequestResponseAndAPIResponseExposeRawValues(t *testing.T) {
	rawReq := &fakeRequest{
		url: "https://request.example", method: "POST", headers: map[string]string{"x": "1"},
		postData: "text", postDataBytes: []byte("bytes"), resourceType: "document", nav: true,
	}
	req := &realRequest{raw: rawReq}
	if req.URL() != rawReq.url || req.Method() != rawReq.method || req.Headers()["x"] != "1" || req.PostData() != "text" {
		t.Fatalf("request scalar values mismatch")
	}
	if string(req.PostDataBytes()) != "bytes" || req.ResourceType() != "document" || !req.IsNavigationRequest() {
		t.Fatalf("request body/type/navigation mismatch")
	}

	rawResp := &fakeResponse{
		url: "https://response.example", status: 201, statusText: "Created",
		headers: map[string]string{"y": "2"}, body: []byte("body"), text: "body", ok: true, request: rawReq,
	}
	resp := &realResponse{raw: rawResp}
	if resp.URL() != rawResp.url || resp.Status() != 201 || resp.StatusText() != "Created" || resp.Headers()["y"] != "2" || !resp.OK() {
		t.Fatalf("response scalar values mismatch")
	}
	if body, err := resp.Body(); err != nil || string(body) != "body" {
		t.Fatalf("Body() = %q, %v", body, err)
	}
	if text, err := resp.Text(); err != nil || text != "body" {
		t.Fatalf("Text() = %q, %v", text, err)
	}
	if resp.Request().URL() != rawReq.url {
		t.Fatalf("Response request URL = %q", resp.Request().URL())
	}

	rawAPI := &fakeAPIResponse{
		url: "https://api.example", status: 204, statusText: "No Content",
		headers: map[string]string{"z": "3"}, body: []byte("api-body"), text: "api-body", ok: true,
	}
	apiResp := &realAPIResponse{raw: rawAPI}
	if apiResp.URL() != rawAPI.url || apiResp.Status() != 204 || apiResp.StatusText() != "No Content" || apiResp.Headers()["z"] != "3" || !apiResp.OK() {
		t.Fatalf("api response scalar values mismatch")
	}
	if body, err := apiResp.Body(); err != nil || string(body) != "api-body" {
		t.Fatalf("API Body() = %q, %v", body, err)
	}
	if text, err := apiResp.Text(); err != nil || text != "api-body" {
		t.Fatalf("API Text() = %q, %v", text, err)
	}
	if apiResp.Request() != nil {
		t.Fatalf("API Request() = %#v, want nil", apiResp.Request())
	}
}

func TestConversionHelpersCoverEmptyAndBodyStringCases(t *testing.T) {
	if toPWStorageState(nil) != nil {
		t.Fatalf("nil storage should stay nil")
	}
	state := &playwright.StorageState{
		Cookies: []playwright.Cookie{{Name: "sid", Value: "v"}},
		Origins: []playwright.Origin{{
			Origin: "https://example.com",
			LocalStorage: []playwright.NameValue{
				{Name: "k", Value: "v"},
			},
		}},
	}
	got := fromPWStorageState(state)
	if len(got.Cookies) != 1 || len(got.Origins) != 1 || got.Origins[0].LocalStorage[0].Value != "v" {
		t.Fatalf("fromPWStorageState() = %#v", got)
	}

	cookies := fromPWCookies([]playwright.Cookie{{Name: "a", Value: "b"}})
	if len(cookies) != 1 || cookies[0].SameSite != "" {
		t.Fatalf("fromPWCookies() = %#v", cookies)
	}

	fulfill := toPWRouteFulfillOptions(&FulfillOptions{Status: 200, BodyString: "text"})
	if fulfill.Body != "text" {
		t.Fatalf("fulfill body string = %q", fulfill.Body)
	}
	if toPWRouteFetchOptions(nil).URL != nil {
		t.Fatalf("nil fetch options should be empty")
	}
	if state := toWaitForSelectorState("hidden"); state == nil || string(*state) != "hidden" {
		t.Fatalf("selector state = %#v", state)
	}
}

type fakeBrowser struct {
	playwright.Browser

	closeCalls        int
	connected         bool
	version           string
	contexts          []playwright.BrowserContext
	newContextResp    playwright.BrowserContext
	newContextErr     error
	newContextOptions playwright.BrowserNewContextOptions
	newPageResp       playwright.Page
	newPageErr        error
	newPageOptions    playwright.BrowserNewPageOptions
	onDisconnected    func(playwright.Browser)
}

type fakePlaywrightBrowserType struct {
	browser  playwright.Browser
	err      error
	endpoint string
	options  []playwright.BrowserTypeConnectOptions
}

func (b *fakePlaywrightBrowserType) Connect(endpoint string, options ...playwright.BrowserTypeConnectOptions) (playwright.Browser, error) {
	b.endpoint = endpoint
	b.options = options
	if b.err != nil {
		return nil, b.err
	}
	return b.browser, nil
}

func replacePlaywrightRunner(t *testing.T, fn func(*playwright.RunOptions) (playwrightRuntime, error)) func() {
	t.Helper()
	orig := runPlaywright
	runPlaywright = fn
	restore := func() { runPlaywright = orig }
	t.Cleanup(restore)
	return restore
}

func (b *fakeBrowser) Close(options ...playwright.BrowserCloseOptions) error {
	b.closeCalls++
	return nil
}

func (b *fakeBrowser) IsConnected() bool { return b.connected }

func (b *fakeBrowser) OnDisconnected(fn func(playwright.Browser)) {
	b.onDisconnected = fn
}

func (b *fakeBrowser) Contexts() []playwright.BrowserContext { return b.contexts }

func (b *fakeBrowser) NewContext(options ...playwright.BrowserNewContextOptions) (playwright.BrowserContext, error) {
	if len(options) > 0 {
		b.newContextOptions = options[0]
	}
	return b.newContextResp, b.newContextErr
}

func (b *fakeBrowser) NewPage(options ...playwright.BrowserNewPageOptions) (playwright.Page, error) {
	if len(options) > 0 {
		b.newPageOptions = options[0]
	}
	return b.newPageResp, b.newPageErr
}

func (b *fakeBrowser) Version() string { return b.version }

type fakeBrowserContext struct {
	playwright.BrowserContext

	newPageResp      playwright.Page
	newPageErr       error
	pages            []playwright.Page
	cookiesResp      []playwright.Cookie
	cookiesErr       error
	cookieURLs       []string
	addedCookies     []playwright.OptionalCookie
	clearCalls       int
	storageStateResp *playwright.StorageState
	storageStateErr  error
	routePattern     any
	routeHandler     func(playwright.Route)
	unroutePattern   any
	onRequest        func(playwright.Request)
	onResponse       func(playwright.Response)
	closeCalls       int
}

func (c *fakeBrowserContext) NewPage() (playwright.Page, error) {
	return c.newPageResp, c.newPageErr
}

func (c *fakeBrowserContext) Pages() []playwright.Page { return c.pages }

func (c *fakeBrowserContext) Cookies(urls ...string) ([]playwright.Cookie, error) {
	c.cookieURLs = urls
	return c.cookiesResp, c.cookiesErr
}

func (c *fakeBrowserContext) AddCookies(cookies []playwright.OptionalCookie) error {
	c.addedCookies = cookies
	return nil
}

func (c *fakeBrowserContext) ClearCookies(options ...playwright.BrowserContextClearCookiesOptions) error {
	c.clearCalls++
	return nil
}

func (c *fakeBrowserContext) StorageState(path ...string) (*playwright.StorageState, error) {
	return c.storageStateResp, c.storageStateErr
}

func (c *fakeBrowserContext) Route(url any, handler func(playwright.Route), times ...int) error {
	c.routePattern = url
	c.routeHandler = handler
	return nil
}

func (c *fakeBrowserContext) Unroute(url any, handler ...func(playwright.Route)) error {
	c.unroutePattern = url
	return nil
}

func (c *fakeBrowserContext) OnRequest(fn func(playwright.Request)) {
	c.onRequest = fn
}

func (c *fakeBrowserContext) OnResponse(fn func(playwright.Response)) {
	c.onResponse = fn
}

func (c *fakeBrowserContext) Close(options ...playwright.BrowserContextCloseOptions) error {
	c.closeCalls++
	return nil
}

type fakePage struct {
	playwright.Page

	gotoURL                  string
	gotoOptions              playwright.PageGotoOptions
	gotoResp                 playwright.Response
	gotoErr                  error
	goBackResp               playwright.Response
	goForwardResp            playwright.Response
	reloadResp               playwright.Response
	mainFrame                playwright.Frame
	expectEventName          string
	expectEventOptions       playwright.PageExpectEventOptions
	expectEventPredicateHit  bool
	expectEventPredicateMiss bool
	evaluateExpression       string
	evaluateArgs             []any
	evaluateResp             any
	initScript               playwright.Script
	contentResp              string
	setContentHTML           string
	setContentOptions        playwright.PageSetContentOptions
	titleResp                string
	url                      string
	waitForLoadOptions       playwright.PageWaitForLoadStateOptions
	waitForSelectorSelector  string
	waitForSelectorOptions   playwright.PageWaitForSelectorOptions
	waitForSelectorRes       playwright.ElementHandle
	waitForURLPattern        any
	waitForURLOptions        playwright.PageWaitForURLOptions
	screenshotOptions        playwright.PageScreenshotOptions
	screenshotResp           []byte
	pdfOptions               playwright.PagePdfOptions
	pdfResp                  []byte
	contextResp              playwright.BrowserContext
	routePattern             any
	routeHandler             func(playwright.Route)
	unroutePattern           any
	onRequest                func(playwright.Request)
	onRequestFailed          func(playwright.Request)
	onResponse               func(playwright.Response)
	onPageError              func(error)
	onConsole                func(playwright.ConsoleMessage)
	onDialog                 func(playwright.Dialog)
	wheelX                   float64
	wheelY                   float64
	wheelErr                 error
	locatorSelector          string
	locatorResp              playwright.Locator
	closeCalls               int
}

func (p *fakePage) Goto(url string, options ...playwright.PageGotoOptions) (playwright.Response, error) {
	p.gotoURL = url
	if len(options) > 0 {
		p.gotoOptions = options[0]
	}
	return p.gotoResp, p.gotoErr
}

func (p *fakePage) GoBack(options ...playwright.PageGoBackOptions) (playwright.Response, error) {
	return p.goBackResp, nil
}

func (p *fakePage) GoForward(options ...playwright.PageGoForwardOptions) (playwright.Response, error) {
	return p.goForwardResp, nil
}

func (p *fakePage) Reload(options ...playwright.PageReloadOptions) (playwright.Response, error) {
	return p.reloadResp, nil
}

func (p *fakePage) MainFrame() playwright.Frame {
	if p.mainFrame == nil {
		p.mainFrame = &fakeFrame{}
	}
	return p.mainFrame
}

func (p *fakePage) ExpectEvent(event string, action func() error, options ...playwright.PageExpectEventOptions) (any, error) {
	p.expectEventName = event
	if len(options) > 0 {
		p.expectEventOptions = options[0]
		if predicate, ok := options[0].Predicate.(func(any) bool); ok {
			p.expectEventPredicateMiss = predicate(&fakeFrame{})
			p.expectEventPredicateHit = predicate(p.MainFrame())
		}
	}
	if err := action(); err != nil {
		return nil, err
	}
	return p.MainFrame(), nil
}

func (p *fakePage) Evaluate(expression string, arg ...any) (any, error) {
	p.evaluateExpression = expression
	p.evaluateArgs = arg
	return p.evaluateResp, nil
}

func (p *fakePage) AddInitScript(script playwright.Script) error {
	p.initScript = script
	return nil
}

func (p *fakePage) Content() (string, error) { return p.contentResp, nil }

func (p *fakePage) SetContent(html string, options ...playwright.PageSetContentOptions) error {
	p.setContentHTML = html
	if len(options) > 0 {
		p.setContentOptions = options[0]
	}
	return nil
}

func (p *fakePage) Title() (string, error) { return p.titleResp, nil }
func (p *fakePage) URL() string            { return p.url }

func (p *fakePage) WaitForLoadState(options ...playwright.PageWaitForLoadStateOptions) error {
	if len(options) > 0 {
		p.waitForLoadOptions = options[0]
	}
	return nil
}

func (p *fakePage) WaitForSelector(selector string, options ...playwright.PageWaitForSelectorOptions) (playwright.ElementHandle, error) {
	p.waitForSelectorSelector = selector
	if len(options) > 0 {
		p.waitForSelectorOptions = options[0]
	}
	return p.waitForSelectorRes, nil
}

func (p *fakePage) WaitForURL(url any, options ...playwright.PageWaitForURLOptions) error {
	p.waitForURLPattern = url
	if len(options) > 0 {
		p.waitForURLOptions = options[0]
	}
	return nil
}

func (p *fakePage) Screenshot(options ...playwright.PageScreenshotOptions) ([]byte, error) {
	if len(options) > 0 {
		p.screenshotOptions = options[0]
	}
	return p.screenshotResp, nil
}

func (p *fakePage) PDF(options ...playwright.PagePdfOptions) ([]byte, error) {
	if len(options) > 0 {
		p.pdfOptions = options[0]
	}
	return p.pdfResp, nil
}

func (p *fakePage) Context() playwright.BrowserContext { return p.contextResp }

func (p *fakePage) Route(url any, handler func(playwright.Route), times ...int) error {
	p.routePattern = url
	p.routeHandler = handler
	return nil
}

func (p *fakePage) Unroute(url any, handler ...func(playwright.Route)) error {
	p.unroutePattern = url
	return nil
}

func (p *fakePage) OnRequest(fn func(playwright.Request)) {
	p.onRequest = fn
}

func (p *fakePage) OnRequestFailed(fn func(playwright.Request)) {
	p.onRequestFailed = fn
}

func (p *fakePage) OnResponse(fn func(playwright.Response)) {
	p.onResponse = fn
}

func (p *fakePage) OnPageError(fn func(error)) {
	p.onPageError = fn
}

func (p *fakePage) OnConsole(fn func(playwright.ConsoleMessage)) {
	p.onConsole = fn
}
func (p *fakePage) OnDialog(fn func(playwright.Dialog)) {
	p.onDialog = fn
}
func (p *fakePage) Mouse() playwright.Mouse {
	return &fakeMouse{page: p}
}

func (p *fakePage) Locator(selector string, options ...playwright.PageLocatorOptions) playwright.Locator {
	p.locatorSelector = selector
	return p.locatorResp
}

func (p *fakePage) Close(options ...playwright.PageCloseOptions) error {
	p.closeCalls++
	return nil
}

type fakeMouse struct {
	playwright.Mouse
	page *fakePage
}

func (m *fakeMouse) Wheel(deltaX, deltaY float64) error {
	m.page.wheelX = deltaX
	m.page.wheelY = deltaY
	return m.page.wheelErr
}

type fakePWDialog struct {
	playwright.Dialog
	typ          string
	message      string
	defaultValue string
	acceptText   string
	accepted     bool
	dismissed    bool
	acceptErr    error
	dismissErr   error
}

func (d *fakePWDialog) Type() string         { return d.typ }
func (d *fakePWDialog) Message() string      { return d.message }
func (d *fakePWDialog) DefaultValue() string { return d.defaultValue }
func (d *fakePWDialog) Accept(promptText ...string) error {
	d.accepted = true
	if len(promptText) > 0 {
		d.acceptText = promptText[0]
	}
	return d.acceptErr
}
func (d *fakePWDialog) Dismiss() error {
	d.dismissed = true
	return d.dismissErr
}

type fakeElement struct {
	playwright.ElementHandle
}

type fakeFrame struct {
	playwright.Frame
}

type fakeRoute struct {
	playwright.Route

	request         playwright.Request
	continueOptions playwright.RouteContinueOptions
	fulfillOptions  playwright.RouteFulfillOptions
	abortCode       string
	fetchOptions    playwright.RouteFetchOptions
	fetchResp       playwright.APIResponse
	fetchErr        error
}

func (r *fakeRoute) Request() playwright.Request { return r.request }

func (r *fakeRoute) Continue(options ...playwright.RouteContinueOptions) error {
	if len(options) > 0 {
		r.continueOptions = options[0]
	}
	return nil
}

func (r *fakeRoute) Fulfill(options ...playwright.RouteFulfillOptions) error {
	if len(options) > 0 {
		r.fulfillOptions = options[0]
	}
	return nil
}

func (r *fakeRoute) Abort(errorCode ...string) error {
	if len(errorCode) > 0 {
		r.abortCode = errorCode[0]
	}
	return nil
}

func (r *fakeRoute) Fetch(options ...playwright.RouteFetchOptions) (playwright.APIResponse, error) {
	if len(options) > 0 {
		r.fetchOptions = options[0]
	}
	return r.fetchResp, r.fetchErr
}

type embeddedLocator interface {
	playwright.Locator
}

type fakeLocator struct {
	embeddedLocator

	clickOptions             playwright.LocatorClickOptions
	fillValue                string
	fillOptions              playwright.LocatorFillOptions
	typeValue                string
	pressSequentiallyOptions playwright.LocatorPressSequentiallyOptions
	pressKey                 string
	pressOptions             playwright.LocatorPressOptions
	hoverOptions             playwright.LocatorHoverOptions
	scrollOptions            playwright.LocatorScrollIntoViewIfNeededOptions
	selectOptions            playwright.LocatorSelectOptionOptions
	selectValues             playwright.SelectOptionValues
	selectResp               []string
	checked                  bool
	setCheckedOptions        playwright.LocatorSetCheckedOptions
	inputFiles               []string
	setInputFilesOptions     playwright.LocatorSetInputFilesOptions
	textContentResp          string
	textContentOptions       playwright.LocatorTextContentOptions
	innerHTMLResp            string
	innerHTMLOptions         playwright.LocatorInnerHTMLOptions
	attributeName            string
	attributeResp            string
	getAttributeOptions      playwright.LocatorGetAttributeOptions
	isVisibleResp            bool
	isVisibleOptions         playwright.LocatorIsVisibleOptions
	countResp                int
	firstResp                playwright.Locator
	lastResp                 playwright.Locator
	nthIndex                 int
	nthResp                  playwright.Locator
	waitOptions              playwright.LocatorWaitForOptions
	screenshotResp           []byte
	screenshotOptions        playwright.LocatorScreenshotOptions
}

func (l *fakeLocator) Click(options ...playwright.LocatorClickOptions) error {
	if len(options) > 0 {
		l.clickOptions = options[0]
	}
	return nil
}

func (l *fakeLocator) Fill(value string, options ...playwright.LocatorFillOptions) error {
	l.fillValue = value
	if len(options) > 0 {
		l.fillOptions = options[0]
	}
	return nil
}

func (l *fakeLocator) PressSequentially(value string, options ...playwright.LocatorPressSequentiallyOptions) error {
	l.typeValue = value
	if len(options) > 0 {
		l.pressSequentiallyOptions = options[0]
	}
	return nil
}

func (l *fakeLocator) Press(key string, options ...playwright.LocatorPressOptions) error {
	l.pressKey = key
	if len(options) > 0 {
		l.pressOptions = options[0]
	}
	return nil
}

func (l *fakeLocator) Hover(options ...playwright.LocatorHoverOptions) error {
	if len(options) > 0 {
		l.hoverOptions = options[0]
	}
	return nil
}

func (l *fakeLocator) ScrollIntoViewIfNeeded(options ...playwright.LocatorScrollIntoViewIfNeededOptions) error {
	if len(options) > 0 {
		l.scrollOptions = options[0]
	}
	return nil
}

func (l *fakeLocator) SelectOption(values playwright.SelectOptionValues, options ...playwright.LocatorSelectOptionOptions) ([]string, error) {
	l.selectValues = values
	if len(options) > 0 {
		l.selectOptions = options[0]
	}
	return l.selectResp, nil
}

func (l *fakeLocator) SetChecked(checked bool, options ...playwright.LocatorSetCheckedOptions) error {
	l.checked = checked
	if len(options) > 0 {
		l.setCheckedOptions = options[0]
	}
	return nil
}

func (l *fakeLocator) SetInputFiles(files any, options ...playwright.LocatorSetInputFilesOptions) error {
	if paths, ok := files.([]string); ok {
		l.inputFiles = append([]string(nil), paths...)
	}
	if len(options) > 0 {
		l.setInputFilesOptions = options[0]
	}
	return nil
}

func (l *fakeLocator) TextContent(options ...playwright.LocatorTextContentOptions) (string, error) {
	if len(options) > 0 {
		l.textContentOptions = options[0]
	}
	return l.textContentResp, nil
}

func (l *fakeLocator) InnerHTML(options ...playwright.LocatorInnerHTMLOptions) (string, error) {
	if len(options) > 0 {
		l.innerHTMLOptions = options[0]
	}
	return l.innerHTMLResp, nil
}

func (l *fakeLocator) GetAttribute(name string, options ...playwright.LocatorGetAttributeOptions) (string, error) {
	l.attributeName = name
	if len(options) > 0 {
		l.getAttributeOptions = options[0]
	}
	return l.attributeResp, nil
}

func (l *fakeLocator) IsVisible(options ...playwright.LocatorIsVisibleOptions) (bool, error) {
	if len(options) > 0 {
		l.isVisibleOptions = options[0]
	}
	return l.isVisibleResp, nil
}

func (l *fakeLocator) Count() (int, error) { return l.countResp, nil }

func (l *fakeLocator) First() playwright.Locator { return l.firstResp }

func (l *fakeLocator) Last() playwright.Locator { return l.lastResp }

func (l *fakeLocator) Locator(selectorOrLocator any, options ...playwright.LocatorLocatorOptions) playwright.Locator {
	return l
}

func (l *fakeLocator) Nth(index int) playwright.Locator {
	l.nthIndex = index
	return l.nthResp
}

func (l *fakeLocator) WaitFor(options ...playwright.LocatorWaitForOptions) error {
	if len(options) > 0 {
		l.waitOptions = options[0]
	}
	return nil
}

func (l *fakeLocator) Screenshot(options ...playwright.LocatorScreenshotOptions) ([]byte, error) {
	if len(options) > 0 {
		l.screenshotOptions = options[0]
	}
	return l.screenshotResp, nil
}

type fakeRequest struct {
	playwright.Request

	url           string
	method        string
	headers       map[string]string
	postData      string
	postDataBytes []byte
	resourceType  string
	nav           bool
}

func (r *fakeRequest) URL() string                { return r.url }
func (r *fakeRequest) Method() string             { return r.method }
func (r *fakeRequest) Headers() map[string]string { return r.headers }
func (r *fakeRequest) PostData() (string, error)  { return r.postData, nil }
func (r *fakeRequest) PostDataBuffer() ([]byte, error) {
	return r.postDataBytes, nil
}
func (r *fakeRequest) ResourceType() string      { return r.resourceType }
func (r *fakeRequest) IsNavigationRequest() bool { return r.nav }

type fakeResponse struct {
	playwright.Response

	url        string
	status     int
	statusText string
	headers    map[string]string
	body       []byte
	text       string
	ok         bool
	request    playwright.Request
}

func (r *fakeResponse) URL() string                 { return r.url }
func (r *fakeResponse) Status() int                 { return r.status }
func (r *fakeResponse) StatusText() string          { return r.statusText }
func (r *fakeResponse) Headers() map[string]string  { return r.headers }
func (r *fakeResponse) Body() ([]byte, error)       { return r.body, nil }
func (r *fakeResponse) Text() (string, error)       { return r.text, nil }
func (r *fakeResponse) Ok() bool                    { return r.ok }
func (r *fakeResponse) Request() playwright.Request { return r.request }

type fakeAPIResponse struct {
	playwright.APIResponse

	url        string
	status     int
	statusText string
	headers    map[string]string
	body       []byte
	text       string
	ok         bool
}

func (r *fakeAPIResponse) URL() string                { return r.url }
func (r *fakeAPIResponse) Status() int                { return r.status }
func (r *fakeAPIResponse) StatusText() string         { return r.statusText }
func (r *fakeAPIResponse) Headers() map[string]string { return r.headers }
func (r *fakeAPIResponse) Body() ([]byte, error)      { return r.body, nil }
func (r *fakeAPIResponse) Text() (string, error)      { return r.text, nil }
func (r *fakeAPIResponse) Ok() bool                   { return r.ok }

type fakeConsoleMessage struct {
	playwright.ConsoleMessage

	typ  string
	text string
}

func (m *fakeConsoleMessage) Type() string { return m.typ }
func (m *fakeConsoleMessage) Text() string { return m.text }
func (m *fakeConsoleMessage) Args() []playwright.JSHandle {
	return nil
}
