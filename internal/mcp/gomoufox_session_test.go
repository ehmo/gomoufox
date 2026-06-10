package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/a11y"
	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/pwbridge"
)

func TestGomoufoxFactoryCreatesSharedAndDedicatedSessions(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"cookies":[{"name":"sid","value":"1"}],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sharedPage := &fakeMCPPage{title: "Shared", url: "https://example.com"}
	sharedContext := &fakeMCPContext{page: sharedPage}
	sharedBrowser := &fakeMCPBrowser{context: sharedContext}
	dedicatedPage := &fakeMCPPage{title: "Dedicated", url: "https://profile.example"}
	dedicatedContext := &fakeMCPContext{
		page:    dedicatedPage,
		cookies: []gomoufox.Cookie{{Name: "profile", Value: "1", Domain: ".example.com", Path: "/"}},
		storage: &gomoufox.StorageState{Cookies: []gomoufox.Cookie{{Name: "profile", Value: "1"}}},
	}
	dedicatedBrowser := &fakeMCPBrowser{context: dedicatedContext}
	launcher := &fakeGomoufoxLauncher{browsers: []mcpBrowser{sharedBrowser, dedicatedBrowser}}
	factory := &gomoufoxFactory{launcher: launcher}

	session, err := factory.NewBrowserSession(context.Background(), sessionOptions{
		id: "one", locale: "en-US", storageStatePath: statePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session == nil || len(launcher.calls) != 1 || launcher.calls[0].dedicated {
		t.Fatalf("shared launcher calls = %#v", launcher.calls)
	}
	if sharedBrowser.newContextCalls != 1 || sharedContext.newPageCalls != 1 {
		t.Fatalf("shared browser/context calls = %d/%d", sharedBrowser.newContextCalls, sharedContext.newPageCalls)
	}
	if sharedPage.evaluateCalls != 2 || sharedPage.internalEvaluateCalls != 1 {
		t.Fatalf("shared session creation probe calls page=%d internal=%d", sharedPage.evaluateCalls, sharedPage.internalEvaluateCalls)
	}
	second, err := factory.NewBrowserSession(context.Background(), sessionOptions{id: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || len(launcher.calls) != 1 || sharedBrowser.newContextCalls != 2 {
		t.Fatalf("shared reuse calls launcher=%#v newContext=%d", launcher.calls, sharedBrowser.newContextCalls)
	}

	persistent, err := factory.NewBrowserSession(context.Background(), sessionOptions{id: "profile", os: "linux", profilePath: filepath.Join(dir, "profile")})
	if err != nil {
		t.Fatal(err)
	}
	if persistent == nil || len(launcher.calls) != 2 || !launcher.calls[1].dedicated || launcher.calls[1].opts.os != "linux" || launcher.calls[1].opts.profilePath == "" {
		t.Fatalf("dedicated calls = %#v", launcher.calls)
	}
	if dedicatedBrowser.newContextCalls != 1 || dedicatedContext.newPageCalls != 1 || dedicatedBrowser.newPageCalls != 0 {
		t.Fatalf("dedicated browser/context calls = newContext:%d contextNewPage:%d browserNewPage:%d", dedicatedBrowser.newContextCalls, dedicatedContext.newPageCalls, dedicatedBrowser.newPageCalls)
	}
	if dedicatedPage.evaluateCalls != 2 || dedicatedPage.internalEvaluateCalls != 1 {
		t.Fatalf("dedicated session creation probe calls page=%d internal=%d", dedicatedPage.evaluateCalls, dedicatedPage.internalEvaluateCalls)
	}
	profileSession := persistent.(*gomoufoxSession)
	cookies, err := profileSession.Cookies(context.Background(), cookieOptions{Action: "get", URLs: []string{"https://example.com"}})
	if err != nil || len(cookies.Cookies) != 1 || cookies.Cookies[0].Name != "profile" {
		t.Fatalf("profile cookies = %#v err=%v", cookies, err)
	}
	state, err := profileSession.SaveStorageState(context.Background(), "")
	if err != nil || len(state.Cookies) != 1 || state.Cookies[0].Name != "profile" {
		t.Fatalf("profile storage state = %#v err=%v", state, err)
	}
}

func TestGomoufoxSessionBrowserOperations(t *testing.T) {
	page := &fakeMCPPage{
		title:          "Example",
		url:            "https://example.com/start",
		content:        "<main><button>Sign in</button><p>Hello</p></main>",
		bodyText:       "Sign in\nHello",
		screenshotData: []byte("page-shot"),
		viewport:       map[string]any{"width": float64(1024), "height": float64(768)},
		snapshot: []map[string]any{
			{"role": "button", "name": "Sign in", "resolver": "button"},
		},
		fetchPayload: map[string]any{
			"ok": true, "url": "https://api.example.com/me", "status": float64(200),
			"headers": map[string]any{"content-type": "application/json"}, "body": `{"ok":true}`,
		},
	}
	browserContext := &fakeMCPContext{
		page:    page,
		cookies: []gomoufox.Cookie{{Name: "sid", Value: "secret", Domain: ".example.com", Path: "/"}},
		storage: &gomoufox.StorageState{Cookies: []gomoufox.Cookie{{Name: "sid", Value: "secret"}}, Origins: []gomoufox.Origin{{Origin: "https://example.com"}}},
	}
	browser := &fakeMCPBrowser{}
	session := &gomoufoxSession{browser: browser, context: browserContext, page: page, refs: a11y.NewStore(), closeBrowser: true}

	nav, err := session.Navigate(context.Background(), "https://example.com", navigateOptions{WaitUntil: "load", Timeout: time.Second})
	if err != nil || nav.Status != 0 || nav.Title != "Example" || nav.URL != "https://example.com/start" {
		t.Fatalf("navigate = %#v err=%v", nav, err)
	}
	if page.gotoURL != "https://example.com" {
		t.Fatalf("goto call url=%q", page.gotoURL)
	}

	contentResult, err := session.PageContent(context.Background(), pageContentOptions{MaxBytes: 1024, IncludeHTML: true, IncludeText: true})
	if err != nil || contentResult.Text != page.bodyText || contentResult.HTML != page.content {
		t.Fatalf("page content = %#v err=%v", contentResult, err)
	}
	if arg, ok := page.internalEvaluateArg.(map[string]any); !ok || arg["selector"] != "" || arg["maxBytes"] != 1024 || arg["includeHTML"] != true || arg["includeText"] != true {
		t.Fatalf("content internal evaluate arg = %#v", page.internalEvaluateArg)
	}
	contentResult, err = session.PageContent(context.Background(), pageContentOptions{Selector: "main", MaxBytes: 64, IncludeText: true})
	if err != nil || contentResult.Text != "locator text" || contentResult.HTML != "" {
		t.Fatalf("selector content = %#v err=%v", contentResult, err)
	}
	if arg, ok := page.internalEvaluateArg.(map[string]any); !ok || arg["selector"] != "main" || arg["maxBytes"] != 64 || arg["includeHTML"] != false || arg["includeText"] != true {
		t.Fatalf("selector content internal evaluate arg = %#v", page.internalEvaluateArg)
	}

	result, err := session.Evaluate(context.Background(), "script", map[string]any{"x": true}, evaluateOptions{Timeout: time.Second})
	if err != nil || result == nil || page.evaluateExpression != "mw:script" {
		t.Fatalf("evaluate result=%#v expr=%q err=%v", result, page.evaluateExpression, err)
	}
	if err := session.Click(context.Background(), elementTarget{Selector: "button"}, clickOptions{Timeout: time.Second}); err != nil {
		t.Fatalf("click selector: %v", err)
	}
	if page.locator.clicks != 1 {
		t.Fatalf("locator clicks = %d", page.locator.clicks)
	}
	if err := session.Click(context.Background(), elementTarget{Selector: "button"}, clickOptions{Button: "right", ClickCount: 2, WaitForNavigation: true, Timeout: time.Second}); err != nil {
		t.Fatalf("click wait navigation: %v", err)
	}
	if page.waitNavigationCalls != 1 || page.locator.clicks != 2 {
		t.Fatalf("navigation click calls nav=%d clicks=%d", page.waitNavigationCalls, page.locator.clicks)
	}
	snap, err := session.Snapshot(context.Background(), snapshotOptions{MaxElements: 5, InteractiveOnly: true})
	if err != nil || len(snap.Elements) != 1 || snap.Elements[0]["ref"] != "e1" {
		t.Fatalf("snapshot = %#v err=%v", snap, err)
	}
	if err := session.Click(context.Background(), elementTarget{Ref: "e1"}, clickOptions{Timeout: time.Second}); err != nil {
		t.Fatalf("click ref: %v", err)
	}
	page.locator.resetTyping()
	page.locator.currentValue = "abc"
	if err := session.Type(context.Background(), elementTarget{Selector: "input"}, "X", typeOptions{ClearFirst: true, Timeout: time.Second}); err != nil {
		t.Fatalf("type: %v", err)
	}
	if strings.Join(page.locator.typingOps, ",") != "fill:,type:X" || page.locator.currentValue != "X" || page.locator.typeOptCount != 2 {
		t.Fatalf("clear type ops=%v current=%q fill=%q type=%q typeOpts=%d", page.locator.typingOps, page.locator.currentValue, page.locator.fillValue, page.locator.typeValue, page.locator.typeOptCount)
	}
	page.locator.resetTyping()
	page.locator.currentValue = "abc"
	if err := session.Type(context.Background(), elementTarget{Selector: "input"}, "X", typeOptions{ClearFirst: false, PressEnterAfter: true, Delay: 25 * time.Millisecond, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("append type: %v", err)
	}
	if strings.Join(page.locator.typingOps, ",") != "type:X,press:Enter" || page.locator.currentValue != "abcX" || page.locator.fillValue != "" || page.locator.typeOptCount != 2 || page.locator.pressOptCount != 1 {
		t.Fatalf("append type ops=%v current=%q fill=%q type=%q typeOpts=%d press=%q pressOpts=%d", page.locator.typingOps, page.locator.currentValue, page.locator.fillValue, page.locator.typeValue, page.locator.typeOptCount, page.locator.pressKey, page.locator.pressOptCount)
	}
	if err := session.PressKey(context.Background(), elementTarget{Selector: "input"}, "Control+A", pressOptions{Timeout: 1500 * time.Millisecond}); err != nil {
		t.Fatalf("press key: %v", err)
	}
	if page.locator.pressKey != "Control+A" || page.locator.pressOptCount != 1 || len(page.locator.typingOps) == 0 || page.locator.typingOps[len(page.locator.typingOps)-1] != "press:Control+A" {
		t.Fatalf("press key ops=%v key=%q pressOpts=%d", page.locator.typingOps, page.locator.pressKey, page.locator.pressOptCount)
	}
	if err := session.Hover(context.Background(), elementTarget{Selector: "button"}, hoverOptions{Timeout: time.Second, Force: true}); err != nil {
		t.Fatalf("hover: %v", err)
	}
	if page.locator.hoverCalls != 1 || page.locator.hoverOptCount != 2 {
		t.Fatalf("hover calls=%d opts=%d", page.locator.hoverCalls, page.locator.hoverOptCount)
	}
	if err := session.Scroll(context.Background(), scrollOptions{Target: elementTarget{Selector: "#bottom"}, Timeout: time.Second}); err != nil {
		t.Fatalf("scroll target: %v", err)
	}
	if page.locator.scrollCalls != 1 || page.locator.scrollOptCount != 1 {
		t.Fatalf("scroll target calls=%d opts=%d", page.locator.scrollCalls, page.locator.scrollOptCount)
	}
	if err := session.Scroll(context.Background(), scrollOptions{DeltaX: 4, DeltaY: 8}); err != nil {
		t.Fatalf("scroll wheel: %v", err)
	}
	if page.wheelX != 4 || page.wheelY != 8 {
		t.Fatalf("wheel deltas=%v/%v", page.wheelX, page.wheelY)
	}
	page.locator.selectResult = []string{"us"}
	selected, err := session.SelectOption(context.Background(), elementTarget{Selector: "select"}, selectOptionOptions{Values: []string{"us"}, Timeout: time.Second, Force: true})
	if err != nil || strings.Join(selected, ",") != "us" || page.locator.selectOptCount != 3 {
		t.Fatalf("select selected=%v opts=%d err=%v", selected, page.locator.selectOptCount, err)
	}
	if err := session.SetChecked(context.Background(), elementTarget{Selector: "input[type=checkbox]"}, true, checkedOptions{Timeout: time.Second, Force: true}); err != nil {
		t.Fatalf("set checked: %v", err)
	}
	if !page.locator.checked || page.locator.checkedOptCount != 2 {
		t.Fatalf("checked=%v opts=%d", page.locator.checked, page.locator.checkedOptCount)
	}
	if err := session.UploadFile(context.Background(), elementTarget{Selector: "input[type=file]"}, []string{"a.txt"}, uploadOptions{Timeout: time.Second}); err != nil {
		t.Fatalf("upload file: %v", err)
	}
	if strings.Join(page.locator.inputFiles, ",") != "a.txt" || page.locator.inputOptCount != 1 {
		t.Fatalf("input files=%v opts=%d", page.locator.inputFiles, page.locator.inputOptCount)
	}

	dialogPage := &fakeMCPPage{}
	dialogSession := newGomoufoxSession(context.Background(), nil, nil, dialogPage, false)
	if _, err := dialogSession.Dialog(context.Background(), dialogOptions{Action: dialogActionSetPolicy, Policy: dialogPolicyAccept, PromptText: "ok"}); err != nil {
		t.Fatalf("dialog policy: %v", err)
	}
	dialog := gomoufox.Dialog{}
	rawDialog := &fakeMCPDialog{typ: "prompt", message: "hello", defaultValue: "secret"}
	setField(&dialog, "raw", rawDialog)
	dialogPage.onDialog(dialog)
	history, err := dialogSession.Dialog(context.Background(), dialogOptions{Action: dialogActionHistory, MaxEvents: 10, Clear: true})
	if err != nil || history.Policy != dialogPolicyAccept || len(history.Dialogs) != 1 || !rawDialog.accepted || rawDialog.acceptText != "ok" || history.Dialogs[0]["default_value_present"] != true {
		t.Fatalf("dialog history=%#v raw=%#v err=%v", history, rawDialog, err)
	}
	dialogPage.onDialog(gomoufox.Dialog{})
	rawErrDialog := &fakeMCPDialog{typ: "prompt", message: "fail", acceptErr: errors.New("dialog boom")}
	errDialog := gomoufox.Dialog{}
	setField(&errDialog, "raw", rawErrDialog)
	dialogPage.onDialog(errDialog)
	consoleAfterDialogErr, err := dialogSession.ConsoleMessages(context.Background(), observeOptions{MaxEvents: 10})
	if err != nil || len(consoleAfterDialogErr.PageErrors) != 1 {
		t.Fatalf("dialog error console=%#v err=%v", consoleAfterDialogErr, err)
	}
	if _, err := dialogSession.Dialog(context.Background(), dialogOptions{Action: dialogActionSetPolicy, Policy: "bad"}); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("bad dialog policy err = %v", err)
	}
	if _, err := dialogSession.Dialog(context.Background(), dialogOptions{Action: "bad"}); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("bad dialog action err = %v", err)
	}
	if policy, prompt := (&gomoufoxSession{}).dialogPolicySnapshot(); policy != dialogPolicyDismiss || prompt != "" {
		t.Fatalf("empty dialog policy snapshot = %q/%q", policy, prompt)
	}
	acceptDialog := &fakeMCPDialog{typ: "confirm", message: "yes?"}
	wrappedAccept := gomoufox.Dialog{}
	setField(&wrappedAccept, "raw", acceptDialog)
	if err := handleDialog(wrappedAccept, dialogPolicyAccept, ""); err != nil || !acceptDialog.accepted || acceptDialog.acceptText != "" {
		t.Fatalf("accept dialog raw=%#v err=%v", acceptDialog, err)
	}
	dismissDialog := &fakeMCPDialog{typ: "alert", message: "dismiss"}
	wrappedDismiss := gomoufox.Dialog{}
	setField(&wrappedDismiss, "raw", dismissDialog)
	if err := handleDialog(wrappedDismiss, dialogPolicyDismiss, "ignored"); err != nil || !dismissDialog.dismissed {
		t.Fatalf("dismiss dialog raw=%#v err=%v", dismissDialog, err)
	}
	acceptErrDialog := &fakeMCPDialog{typ: "prompt", message: "fail", acceptErr: errors.New("accept secret")}
	wrappedAcceptErr := gomoufox.Dialog{}
	setField(&wrappedAcceptErr, "raw", acceptErrDialog)
	if err := handleDialog(wrappedAcceptErr, dialogPolicyAccept, "x"); err == nil {
		t.Fatalf("expected accept error")
	}
	errorObservation := dialogObservation(wrappedAcceptErr, dialogPolicyAccept, errors.New("boom"))
	if errorObservation["handled"] != false || errorObservation["error"] != "boom" {
		t.Fatalf("dialog error observation = %#v", errorObservation)
	}

	for _, condition := range []waitCondition{
		{Kind: "selector", Value: "#ready", Timeout: time.Second},
		{Kind: "text", Value: "Done", Timeout: time.Second},
		{Kind: "url_contains", Value: "/done", Timeout: time.Second},
		{Kind: "load_state", Value: "networkidle", Timeout: time.Second},
	} {
		if err := session.WaitFor(context.Background(), condition); err != nil {
			t.Fatalf("wait %#v: %v", condition, err)
		}
	}
	if err := session.WaitFor(context.Background(), waitCondition{Kind: "bad"}); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("bad wait err = %v", err)
	}

	shot, err := session.Screenshot(context.Background(), screenshotOptions{FullPage: true})
	if err != nil || string(shot.Data) != "page-shot" || shot.Width != 1024 || shot.Height != 768 {
		t.Fatalf("page screenshot = %#v err=%v", shot, err)
	}
	shot, err = session.Screenshot(context.Background(), screenshotOptions{Selector: "main"})
	if err != nil || string(shot.Data) != "locator-shot" {
		t.Fatalf("selector screenshot = %#v err=%v", shot, err)
	}
	hugeSelectorPage := &fakeMCPPage{
		url:             "https://example.com/huge-selector",
		selectorMetrics: map[string]any{"width": float64(2000), "height": float64(2000)},
	}
	hugeSelectorSession := &gomoufoxSession{page: hugeSelectorPage, refs: a11y.NewStore()}
	if _, err := hugeSelectorSession.Screenshot(context.Background(), screenshotOptions{Selector: "main", MaxBytes: 1024}); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("huge selector screenshot err = %v", err)
	}
	if hugeSelectorPage.locatorCalls != 0 || hugeSelectorPage.locator.screenshotCalls != 0 {
		t.Fatalf("huge selector screenshot captured before cap check: locators=%d shots=%d", hugeSelectorPage.locatorCalls, hugeSelectorPage.locator.screenshotCalls)
	}
	hugePage := &fakeMCPPage{
		url:         "https://example.com/huge",
		pageMetrics: map[string]any{"width": float64(2000), "height": float64(2000)},
	}
	hugeSession := &gomoufoxSession{page: hugePage, refs: a11y.NewStore()}
	if _, err := hugeSession.Screenshot(context.Background(), screenshotOptions{FullPage: true, MaxBytes: 1024}); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("huge full-page screenshot err = %v", err)
	}
	if hugePage.screenshotCalls != 0 {
		t.Fatalf("huge full-page screenshot captured before cap check: %d", hugePage.screenshotCalls)
	}

	fetched, err := session.Fetch(context.Background(), fetchOptions{URL: "https://api.example.com/me", Method: "GET", Headers: map[string]string{"Accept": "application/json"}, NavigateFirst: "https://example.com"})
	if err != nil || fetched.Status != 200 || fetched.Headers["content-type"] != "application/json" || string(fetched.Body) != `{"ok":true}` {
		t.Fatalf("fetch = %#v err=%v", fetched, err)
	}
	if arg, ok := page.internalEvaluateArg.(map[string]any); !ok || arg["maxBytes"] != 0 || arg["headers"].(map[string]any)["Accept"] != "application/json" {
		t.Fatalf("fetch internal evaluate arg = %#v", page.internalEvaluateArg)
	}
	page.fetchPayload = map[string]any{
		"ok": true, "url": "https://api.example.com/large", "status": float64(200),
		"headers": map[string]any{}, "body": "abcd", "truncated": true,
	}
	fetched, err = session.Fetch(context.Background(), fetchOptions{URL: "https://api.example.com/large", Method: "GET", MaxBytes: 4})
	if err != nil || string(fetched.Body) != "abcd" || !fetched.Truncated {
		t.Fatalf("capped fetch = %#v err=%v", fetched, err)
	}
	if arg, ok := page.internalEvaluateArg.(map[string]any); !ok || arg["maxBytes"] != 4 {
		t.Fatalf("capped fetch internal evaluate arg = %#v", page.internalEvaluateArg)
	}
	cookies, err := session.Cookies(context.Background(), cookieOptions{Action: "get", URLs: []string{"https://example.com"}})
	if err != nil || len(cookies.Cookies) != 1 || cookies.Cookies[0].Value != "secret" {
		t.Fatalf("cookies get = %#v err=%v", cookies, err)
	}
	if _, err := session.Cookies(context.Background(), cookieOptions{Action: "set", Cookies: []cookie{{Name: "new", Value: "v"}}}); err != nil {
		t.Fatalf("cookies set: %v", err)
	}
	if _, err := session.Cookies(context.Background(), cookieOptions{Action: "clear"}); err != nil || browserContext.clearCalls == 0 {
		t.Fatalf("cookies clear err=%v clearCalls=%d", err, browserContext.clearCalls)
	}
	clearCalls := browserContext.clearCalls
	if _, err := session.Cookies(context.Background(), cookieOptions{Action: "delete"}); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("delete cookies err = %v", err)
	}
	if browserContext.clearCalls != clearCalls {
		t.Fatalf("delete cleared cookies: before=%d after=%d", clearCalls, browserContext.clearCalls)
	}
	if _, err := session.Cookies(context.Background(), cookieOptions{Action: "unknown"}); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("unknown cookies err = %v", err)
	}
	perf, err := session.PerformanceSnapshot(context.Background())
	if err != nil {
		t.Fatalf("performance snapshot: %v", err)
	}
	if perf.URL != "https://example.com/start" || perf.Title != "Example" || perf.Memory["go_alloc_bytes"] == nil || perf.SampledAtUTC == "" {
		t.Fatalf("performance snapshot = %#v", perf)
	}

	state, err := session.SaveStorageState(context.Background(), filepath.Join(t.TempDir(), "state.json"))
	if err != nil || len(state.Cookies) != 1 || browserContext.storagePath == "" {
		t.Fatalf("save state = %#v path=%q err=%v", state, browserContext.storagePath, err)
	}
	replacementPage := &fakeMCPPage{title: "Loaded", url: "https://loaded.example"}
	replacementContext := &fakeMCPContext{page: replacementPage}
	browser.contexts = []*fakeMCPContext{replacementContext}
	newContextCallsBeforeLoad := browser.newContextCalls
	if err := session.LoadStorageState(context.Background(), &gomoufox.StorageState{
		Cookies: []gomoufox.Cookie{{Name: "loaded", Value: "1"}},
		Origins: []gomoufox.Origin{{Origin: "https://example.com", LocalStorage: []gomoufox.LSEntry{{Name: "theme", Value: "dark"}}}},
	}); err != nil {
		t.Fatalf("load state: %v", err)
	}
	if browser.newContextCalls != newContextCallsBeforeLoad+1 || browser.contextOptionCounts[len(browser.contextOptionCounts)-1] == 0 {
		t.Fatalf("load state new contexts=%d before=%d optionCounts=%#v", browser.newContextCalls, newContextCallsBeforeLoad, browser.contextOptionCounts)
	}
	if session.context != replacementContext || session.page != replacementPage || replacementContext.newPageCalls != 1 {
		t.Fatalf("load state did not swap to replacement context/page")
	}
	if page.closeCalls != 1 || browserContext.closeCalls != 1 {
		t.Fatalf("old session resources not closed page=%d context=%d", page.closeCalls, browserContext.closeCalls)
	}
	if len(browserContext.addedCookies) != 1 || browserContext.addedCookies[0].Name != "new" {
		t.Fatalf("load state mutated old cookies: %#v", browserContext.addedCookies)
	}
	if err := session.Close(); err != nil || replacementPage.closeCalls != 1 || replacementContext.closeCalls != 1 || browser.closeCalls != 1 {
		t.Fatalf("close err=%v page=%d context=%d browser=%d", err, replacementPage.closeCalls, replacementContext.closeCalls, browser.closeCalls)
	}
}

func TestGomoufoxSessionAdditionalInteractionErrors(t *testing.T) {
	page := &fakeMCPPage{}
	session := newGomoufoxSession(context.Background(), nil, nil, page, false)
	boom := errors.New("boom")
	missingRef := elementTarget{Ref: "missing"}

	if err := session.Hover(context.Background(), missingRef, hoverOptions{Timeout: time.Second}); err == nil {
		t.Fatal("missing ref hover succeeded")
	}
	if err := session.Scroll(context.Background(), scrollOptions{Target: missingRef, Timeout: time.Second}); err == nil {
		t.Fatal("missing ref scroll succeeded")
	}
	if _, err := session.SelectOption(context.Background(), missingRef, selectOptionOptions{Values: []string{"x"}, Timeout: time.Second}); err == nil {
		t.Fatal("missing ref select succeeded")
	}
	if err := session.SetChecked(context.Background(), missingRef, true, checkedOptions{Timeout: time.Second}); err == nil {
		t.Fatal("missing ref set checked succeeded")
	}
	if err := session.UploadFile(context.Background(), missingRef, []string{"a.txt"}, uploadOptions{Timeout: time.Second}); err == nil {
		t.Fatal("missing ref upload succeeded")
	}

	page.locator.hoverErr = boom
	if err := session.Hover(context.Background(), elementTarget{Selector: "button"}, hoverOptions{Timeout: time.Second}); !errors.Is(err, boom) {
		t.Fatalf("hover err = %v", err)
	}
	page.locator.hoverErr = nil
	page.locator.scrollErr = boom
	if err := session.Scroll(context.Background(), scrollOptions{Target: elementTarget{Selector: "#bottom"}, Timeout: time.Second}); !errors.Is(err, boom) {
		t.Fatalf("scroll target err = %v", err)
	}
	page.locator.scrollErr = nil
	page.wheelErr = boom
	if err := session.Scroll(context.Background(), scrollOptions{DeltaY: 1, Timeout: time.Second}); !errors.Is(err, boom) {
		t.Fatalf("wheel err = %v", err)
	}
	page.wheelErr = nil
	if err := session.Scroll(context.Background(), scrollOptions{Timeout: time.Second}); err != nil {
		t.Fatalf("zero scroll err = %v", err)
	}

	page.locator.selectResult = []string{"by-label"}
	if selected, err := session.SelectOption(context.Background(), elementTarget{Selector: "select"}, selectOptionOptions{Labels: []string{"United States"}, Timeout: time.Second}); err != nil || strings.Join(selected, ",") != "by-label" {
		t.Fatalf("select labels selected=%v err=%v", selected, err)
	}
	page.locator.selectResult = []string{"by-index"}
	if selected, err := session.SelectOption(context.Background(), elementTarget{Selector: "select"}, selectOptionOptions{Indexes: []int{1}, Timeout: time.Second}); err != nil || strings.Join(selected, ",") != "by-index" {
		t.Fatalf("select indexes selected=%v err=%v", selected, err)
	}
	page.locator.selectErr = boom
	if _, err := session.SelectOption(context.Background(), elementTarget{Selector: "select"}, selectOptionOptions{Values: []string{"x"}, Timeout: time.Second}); !errors.Is(err, boom) {
		t.Fatalf("select err = %v", err)
	}
	page.locator.selectErr = nil
	page.locator.checkedErr = boom
	if err := session.SetChecked(context.Background(), elementTarget{Selector: "input"}, true, checkedOptions{Timeout: time.Second}); !errors.Is(err, boom) {
		t.Fatalf("checked err = %v", err)
	}
	page.locator.checkedErr = nil
	page.locator.inputErr = boom
	if err := session.UploadFile(context.Background(), elementTarget{Selector: "input"}, []string{"a.txt"}, uploadOptions{Timeout: time.Second}); !errors.Is(err, boom) {
		t.Fatalf("upload err = %v", err)
	}
}

func TestGomoufoxSessionObservabilityAttachesBuffersAndReattachesAfterLoad(t *testing.T) {
	oldPage := &fakeMCPPage{title: "Old", url: "https://old.example"}
	oldContext := &fakeMCPContext{page: oldPage}
	newPage := &fakeMCPPage{title: "New", url: "https://new.example"}
	newContext := &fakeMCPContext{page: newPage}
	browser := &fakeMCPBrowser{context: newContext}
	session := newGomoufoxSession(context.Background(), browser, oldContext, oldPage, false)

	if oldPage.onConsole == nil || oldPage.onPageError == nil || oldPage.onRequest == nil || oldPage.onRequestFailed == nil || oldPage.onResponse == nil || len(oldPage.initScripts) != 1 {
		t.Fatalf("old page observers not attached: scripts=%d", len(oldPage.initScripts))
	}
	oldPage.onConsole(gomoufox.ConsoleMessage{Type: "log", Text: "token=old-secret"})
	oldPage.onPageError(errors.New("Authorization: Bearer old-error"))
	oldPage.onRequest(gomoufoxRequestForTest("https://user:pass@old.example/api?token=request-secret#frag", "POST", map[string]string{"x-api-key": "request-key"}))
	oldPage.onRequestFailed(gomoufoxRequestForTest("https://old.example/fail?code=failed-secret", "GET", map[string]string{"authorization": "Bearer failed"}))
	oldPage.onResponse(gomoufoxResponseForTest("https://old.example/response?secret=response-secret", 202, map[string]string{"set-cookie": "sid=response-secret"}))
	got, err := session.ConsoleMessages(context.Background(), observeOptions{MaxEvents: 10})
	if err != nil {
		t.Fatal(err)
	}
	network, err := session.NetworkRequests(context.Background(), observeOptions{MaxEvents: 10, Clear: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.Requests) != 3 {
		t.Fatalf("network observations = %#v", network)
	}
	if len(got.Messages) != 1 || len(got.PageErrors) != 1 {
		t.Fatalf("old observations = %#v", got)
	}
	encoded := mustJSONText(map[string]any{"messages": got.Messages, "page_errors": got.PageErrors, "network": network.Requests})
	for _, secret := range []string{"old-secret", "old-error", "user:pass", "request-secret", "failed-secret", "response-secret", "request-key"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("observations leaked %q: %s", secret, encoded)
		}
	}
	network, err = session.NetworkRequests(context.Background(), observeOptions{MaxEvents: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(network.Requests) != 0 {
		t.Fatalf("network clear failed = %#v", network)
	}

	if err := session.LoadStorageState(context.Background(), &gomoufox.StorageState{}); err != nil {
		t.Fatal(err)
	}
	if newPage.onConsole == nil || newPage.onPageError == nil || newPage.onRequest == nil || newPage.onRequestFailed == nil || newPage.onResponse == nil || len(newPage.initScripts) != 1 {
		t.Fatalf("new page observers not attached: scripts=%d", len(newPage.initScripts))
	}
	oldPage.onConsole(gomoufox.ConsoleMessage{Type: "log", Text: "late-old"})
	newPage.onConsole(gomoufox.ConsoleMessage{Type: "log", Text: "new"})
	got, err = session.ConsoleMessages(context.Background(), observeOptions{MaxEvents: 10, Clear: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 || got.Messages[0]["text"] != "new" || len(got.PageErrors) != 0 {
		t.Fatalf("post-load observations = %#v", got)
	}
	got, err = session.ConsoleMessages(context.Background(), observeOptions{MaxEvents: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 0 || len(got.PageErrors) != 0 {
		t.Fatalf("clear failed = %#v", got)
	}
}

func TestGomoufoxSessionObservabilityFailureBranches(t *testing.T) {
	boom := errors.New("boom")
	(&gomoufoxSession{}).attachObservers(context.Background())
	if err := (&gomoufoxSession{}).installPageErrorObserver(context.Background()); err != nil {
		t.Fatalf("nil page install err = %v", err)
	}
	if err := (&gomoufoxSession{}).drainPageErrors(context.Background(), false); err != nil {
		t.Fatalf("nil page drain err = %v", err)
	}
	if err := (&gomoufoxSession{page: &fakeMCPPage{initErr: boom}}).installPageErrorObserver(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("install err = %v", err)
	}
	if err := (&gomoufoxSession{page: &fakeMCPPage{internalEvalErr: boom}}).drainPageErrors(context.Background(), false); !errors.Is(err, boom) {
		t.Fatalf("drain eval err = %v", err)
	}
	if err := (&gomoufoxSession{page: &fakeMCPPage{pageErrorPayload: func() {}}}).drainPageErrors(context.Background(), false); err == nil {
		t.Fatal("drain decode error was nil")
	}
	drainSession := &gomoufoxSession{page: &fakeMCPPage{pageErrorPayload: map[string]any{
		"errors":  []map[string]any{{"type": "error", "message": "token=drained-secret"}},
		"dropped": float64(2),
	}}}
	if err := drainSession.drainPageErrors(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	drained := drainSession.ensureObservations().consoleMessages(10, false)
	if len(drained.PageErrors) != 1 || drained.PageErrorsDropped != 2 || strings.Contains(mustJSONText(map[string]any{"errors": drained.PageErrors}), "drained-secret") {
		t.Fatalf("drained page errors = %#v", drained)
	}
	if _, err := (&gomoufoxSession{page: &fakeMCPPage{internalEvalErr: boom}}).ConsoleMessages(context.Background(), observeOptions{MaxEvents: 1}); !errors.Is(err, boom) {
		t.Fatalf("console drain err = %v", err)
	}
	if _, err := (&gomoufoxSession{page: &fakeMCPPage{internalEvalErr: boom}}).PerformanceSnapshot(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("performance eval err = %v", err)
	}
	if _, err := (&gomoufoxSession{page: &fakeMCPPage{performancePayload: func() {}}}).PerformanceSnapshot(context.Background()); err == nil {
		t.Fatal("performance decode error was nil")
	}
	perf, err := (&gomoufoxSession{page: &fakeMCPPage{performancePayload: map[string]any{
		"url":       "https://example.com/?token=secret",
		"title":     "token=title-secret",
		"resources": map[string]any{"count": float64(0)},
	}}}).PerformanceSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if perf.Memory["go_alloc_bytes"] == nil || perf.Viewport == nil || strings.Contains(mustJSONText(map[string]any{"perf": perf}), "secret") {
		t.Fatalf("performance nil-map/default redaction = %#v", perf)
	}
}

func TestGomoufoxSessionInternalHelpersUseInternalEvaluation(t *testing.T) {
	pageWorldErr := errors.New("page-world helper execution")
	page := &fakeMCPPage{
		title:                "Hostile",
		url:                  "https://example.com/hostile",
		content:              "<main><button>Trusted</button><p>Real body</p></main>",
		bodyText:             "Trusted\nReal body",
		pageWorldInternalErr: pageWorldErr,
		screenshotData:       []byte("full-shot"),
		viewport:             map[string]any{"width": float64(800), "height": float64(600)},
		pageMetrics:          map[string]any{"width": float64(64), "height": float64(64)},
		selectorMetrics:      map[string]any{"width": float64(32), "height": float64(32)},
		snapshot: []map[string]any{
			{"role": "button", "name": "Trusted", "resolver": "#trusted"},
		},
		fetchPayload: map[string]any{
			"ok": true, "url": "https://api.example.com/truth", "status": float64(200),
			"headers": map[string]any{"x-source": "server"}, "body": "truth",
		},
	}
	session := &gomoufoxSession{page: page, refs: a11y.NewStore()}

	if _, err := session.PageContent(context.Background(), pageContentOptions{Selector: "main", MaxBytes: 1024, IncludeHTML: true, IncludeText: true}); err != nil {
		t.Fatalf("content used page-world helper path: %v", err)
	}
	if _, err := session.Snapshot(context.Background(), snapshotOptions{MaxElements: 10, InteractiveOnly: true}); err != nil {
		t.Fatalf("snapshot used page-world helper path: %v", err)
	}
	if _, err := session.Fetch(context.Background(), fetchOptions{URL: "https://api.example.com/truth", Method: "GET", MaxBytes: 64}); err != nil {
		t.Fatalf("fetch used page-world helper path: %v", err)
	}
	if _, err := session.Screenshot(context.Background(), screenshotOptions{FullPage: true, MaxBytes: 4096}); err != nil {
		t.Fatalf("full-page screenshot metrics used page-world helper path: %v", err)
	}
	if _, err := session.Screenshot(context.Background(), screenshotOptions{Selector: "#trusted", MaxBytes: 4096}); err != nil {
		t.Fatalf("selector screenshot metrics used page-world helper path: %v", err)
	}
	if _, err := session.Evaluate(context.Background(), "() => window.__patched === true", nil, evaluateOptions{}); err != nil {
		t.Fatalf("browser evaluate failed: %v", err)
	}

	if page.evaluateCalls != 1 || page.evaluateExpression != "mw:() => window.__patched === true" {
		t.Fatalf("page-world evaluate calls=%d expr=%q", page.evaluateCalls, page.evaluateExpression)
	}
	if page.internalEvaluateCalls != 7 {
		t.Fatalf("internal evaluate calls = %d, want 7", page.internalEvaluateCalls)
	}
}

func TestVerifyMCPInternalEvaluationFailureModes(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name string
		page *fakeMCPPage
		want error
	}{
		{name: "patch", page: &fakeMCPPage{evalErr: boom}, want: boom},
		{name: "patch-decode", page: &fakeMCPPage{patchPayload: func() {}}, want: &json.UnsupportedTypeError{}},
		{name: "patch-failed", page: &fakeMCPPage{patchPayload: map[string]any{"ok": true, "queryTag": "HTML", "css": "gomoufox-patched", "cssAvailable": true}}, want: errInternalEvaluationProbeFailed},
		{name: "internal", page: &fakeMCPPage{internalEvalErr: boom}, want: boom},
		{name: "decode", page: &fakeMCPPage{probePayload: func() {}}, want: &json.UnsupportedTypeError{}},
		{name: "leak", page: &fakeMCPPage{probePayload: map[string]any{"ok": false, "hostFlag": true}}, want: errInternalEvaluationUnavailable},
		{name: "restore", page: &fakeMCPPage{restoreErr: boom}, want: boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyMCPInternalEvaluation(context.Background(), tc.page)
			switch want := tc.want.(type) {
			case *json.UnsupportedTypeError:
				if !errors.As(err, &want) {
					t.Fatalf("verify err = %v, want unsupported type", err)
				}
			default:
				if !errors.Is(err, tc.want) {
					t.Fatalf("verify err = %v, want %v", err, tc.want)
				}
			}
		})
	}
}

func TestMCPFetchExpressionUsesBoundedStreamReader(t *testing.T) {
	if strings.Contains(mcpFetchExpression, "response.text()") {
		t.Fatal("mcp fetch expression materializes full response text")
	}
	for _, token := range []string{"getReader", "cancel", "maxBytes", "TextDecoder"} {
		if !strings.Contains(mcpFetchExpression, token) {
			t.Fatalf("mcp fetch expression missing %q", token)
		}
	}
}

func TestMCPExpressionsDoNotExposePageVisibleHelper(t *testing.T) {
	for name, expression := range map[string]string{
		"fetch":    mcpFetchExpression,
		"content":  boundedContentExpression,
		"snapshot": snapshotExpression,
		"selector": selectorMetricsExpression,
		"page":     fullPageMetricsExpression,
		"viewport": viewportMetricsExpression,
	} {
		if strings.Contains(expression, "__gomoufoxMCPNative") || strings.Contains(expression, "gomoufoxMCP") {
			t.Fatalf("%s expression exposes MCP helper state: %s", name, expression)
		}
	}
}

func TestMCPBrowserScriptsAreEmbeddedAssets(t *testing.T) {
	entries, err := mcpScripts.ReadDir("scripts")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, entry := range entries {
		got[entry.Name()] = true
	}
	scripts := mcpBrowserScriptExpressions()
	if len(got) != len(scripts) {
		t.Fatalf("embedded browser script set mismatch got %#v want %#v", got, scriptNameSet(scripts))
	}
	for name := range scripts {
		if !got[name] {
			t.Fatalf("missing embedded browser script %s in %#v", name, got)
		}
		source, err := mcpScripts.ReadFile("scripts/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(strings.TrimSpace(string(source))) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
	if strings.Contains(snapshotExpression, "__MAX_SNAPSHOT_VALUE_LENGTH__") || !strings.Contains(snapshotExpression, fmt.Sprintf("MAX_VALUE_LENGTH = %d", maxSnapshotValueLength)) {
		t.Fatalf("snapshot expression template was not rendered: %s", snapshotExpression)
	}
}

func TestMustMCPBrowserScriptPanicsForMissingAsset(t *testing.T) {
	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("expected missing script panic")
		}
		message := fmt.Sprint(got)
		if !strings.Contains(message, "missing MCP browser script missing.js") {
			t.Fatalf("unexpected panic: %s", message)
		}
	}()
	_ = mustMCPBrowserScript("missing.js")
}

func TestMCPBrowserScriptsValidateSyntaxWithNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	for name, expression := range mcpBrowserScriptExpressions() {
		path := filepath.Join(t.TempDir(), strings.TrimSuffix(name, ".js")+".check.js")
		wrapped := "const __gomoufoxExpression = (" + expression + ");\nvoid __gomoufoxExpression;\n"
		if err := os.WriteFile(path, []byte(wrapped), 0o600); err != nil {
			t.Fatalf("write syntax check for %s: %v", name, err)
		}
		if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
			t.Fatalf("%s syntax check failed: %v\n%s", name, err, output)
		}
	}
}

func scriptNameSet(scripts map[string]string) map[string]bool {
	out := make(map[string]bool, len(scripts))
	for name := range scripts {
		out[name] = true
	}
	return out
}

func mcpBrowserScriptExpressions() map[string]string {
	return map[string]string{
		"bounded_content.js":             boundedContentExpression,
		"fetch.js":                       mcpFetchExpression,
		"full_page_metrics.js":           fullPageMetricsExpression,
		"internal_probe.js":              mcpInternalProbeExpression,
		"internal_probe_patch.js":        mcpInternalProbePatchExpression,
		"internal_probe_restore.js":      mcpInternalProbeRestoreExpression,
		"page_error_observer_drain.js":   pageErrorObserverDrainExpression,
		"page_error_observer_install.js": pageErrorObserverInstallExpression,
		"performance_snapshot.js":        performanceSnapshotExpression,
		"selector_metrics.js":            selectorMetricsExpression,
		"snapshot.js":                    snapshotExpression,
		"viewport_metrics.js":            viewportMetricsExpression,
	}
}

func TestMCPFetchExpressionStreamsWithDirectFetch(t *testing.T) {
	output := runNodeExpression(t, `
const mcpFetchExpression = `+mcpFetchExpression+`;
const chunks = [new Uint8Array([110, 97, 116, 105, 118, 101])];
globalThis.fetch = async (url) => ({
  url,
  status: 203,
  headers: {forEach: (callback) => callback("direct", "x-source")},
  body: {getReader: () => ({
    read: async () => chunks.length ? {done: false, value: chunks.shift()} : {done: true},
    cancel: async () => {}
  })}
});
mcpFetchExpression({url: "https://api.example.com/data", method: "GET", headers: {}, body: "", maxBytes: 64})
  .then(result => console.log(JSON.stringify({result})))
  .catch(error => { console.error(error); process.exit(1); });
`)
	var got struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if got.Result["body"] != "native" || got.Result["status"] != float64(203) || got.Result["headers"].(map[string]any)["x-source"] != "direct" {
		t.Fatalf("fetch streamed wrong response: %#v", got)
	}
}

func TestMCPFetchExpressionCancelsAtExactCapWithoutReadAhead(t *testing.T) {
	output := runNodeExpression(t, `
const mcpFetchExpression = `+mcpFetchExpression+`;
let cancelCalls = 0;
let readCalls = 0;
globalThis.fetch = async (url) => ({
  url,
  status: 200,
  headers: {forEach: () => {}},
  body: {getReader: () => ({
    read: async () => {
      readCalls++;
      if (readCalls > 1) throw new Error("read after cap");
      return {done: false, value: new Uint8Array([97, 98, 99, 100, 101])};
    },
    cancel: async () => { cancelCalls++; }
  })}
});
mcpFetchExpression({url: "https://api.example.com/exact", method: "GET", headers: {}, body: "", maxBytes: 5})
  .then(result => console.log(JSON.stringify({result, cancelCalls, readCalls})))
  .catch(error => { console.error(error); process.exit(1); });
`)
	var got struct {
		Result      map[string]any `json:"result"`
		CancelCalls int            `json:"cancelCalls"`
		ReadCalls   int            `json:"readCalls"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if got.Result["body"] != "abcde" || got.Result["truncated"] != true || got.CancelCalls != 1 || got.ReadCalls != 1 {
		t.Fatalf("exact-cap mcp result = %#v", got)
	}
}

func TestMCPPerformanceSnapshotExpressionAggregatesResources(t *testing.T) {
	output := runNodeExpression(t, `
const performanceSnapshotExpression = `+performanceSnapshotExpression+`;
globalThis.location = {href: "https://example.com/report?token=secret"};
globalThis.document = {title: "Report"};
globalThis.window = {innerWidth: 1024, innerHeight: 768, devicePixelRatio: 2};
globalThis.performance = {
  memory: {jsHeapSizeLimit: 100, totalJSHeapSize: 50, usedJSHeapSize: 25},
  getEntriesByType: (type) => {
    if (type === "navigation") return [{type: "navigate", startTime: 0, domContentLoadedEventEnd: 12.4, loadEventEnd: 30.2, transferSize: 1000, encodedBodySize: 800}];
    if (type === "resource") return [
      {initiatorType: "script", transferSize: 10, encodedBodySize: 8},
      {initiatorType: "script", transferSize: 20, encodedBodySize: 18},
      {initiatorType: "img", transferSize: 30, encodedBodySize: 28}
    ];
    return [];
  }
};
console.log(JSON.stringify(performanceSnapshotExpression()));
`)
	var got map[string]any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	resources := got["resources"].(map[string]any)
	byType := resources["by_initiator_type"].(map[string]any)
	if resources["count"] != float64(3) || resources["transfer_size"] != float64(60) || resources["encoded_body_size"] != float64(54) || byType["script"] != float64(2) || byType["img"] != float64(1) {
		t.Fatalf("resource aggregate = %#v", resources)
	}
	navigation := got["navigation"].(map[string]any)
	if navigation["dom_content_loaded_ms"] != float64(12) || navigation["load_event_ms"] != float64(30) {
		t.Fatalf("navigation aggregate = %#v", navigation)
	}
	if _, ok := resources["entries"]; ok {
		t.Fatalf("raw resource entries leaked = %#v", resources)
	}
}

func TestMCPContentExpressionUsesBoundedDOMWalk(t *testing.T) {
	for _, forbidden := range []string{".innerHTML", ".outerHTML", ".innerText", ".textContent"} {
		if strings.Contains(boundedContentExpression, forbidden) {
			t.Fatalf("bounded content expression uses full DOM materialization token %q", forbidden)
		}
	}
	for _, token := range []string{"TreeWalker", "TextEncoder", "maxBytes", "nodeValue"} {
		if !strings.Contains(boundedContentExpression, token) {
			t.Fatalf("bounded content expression missing %q", token)
		}
	}
}

func TestMCPContentExpressionWalksSelectedDOM(t *testing.T) {
	output := runNodeExpression(t, `
const boundedContentExpression = `+boundedContentExpression+`;
const text = (value) => ({nodeType: 3, nodeValue: value, nextSibling: null});
const element = (tag, value) => {
  const child = text(value);
  return {nodeType: 1, tagName: tag, attributes: [], firstChild: child};
};
const trustedRoot = element("main", "trusted");
const walkerFor = (root) => {
  let done = false;
  return {nextNode: () => {
    if (done) return null;
    done = true;
    return root.firstChild;
  }};
};
globalThis.Node = {TEXT_NODE: 3, ELEMENT_NODE: 1};
globalThis.NodeFilter = {SHOW_TEXT: 4};
globalThis.TextEncoder = TextEncoder;
globalThis.TextDecoder = TextDecoder;
globalThis.location = {href: "https://example.com"};
globalThis.document = {
  documentElement: trustedRoot,
  querySelector: () => trustedRoot,
  createTreeWalker: walkerFor
};
const result = boundedContentExpression({selector: "main", maxBytes: 64});
console.log(JSON.stringify(result));
`)
	var got map[string]any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if got["text"] != "trusted" || !strings.Contains(fmt.Sprint(got["html"]), "trusted") {
		t.Fatalf("content did not walk selected DOM: %#v", got)
	}
}

func TestMCPContentExpressionSkipsUnusedFormatWalks(t *testing.T) {
	output := runNodeExpression(t, `
const boundedContentExpression = `+boundedContentExpression+`;
const text = (value) => ({nodeType: 3, nodeValue: value, nextSibling: null});
const htmlRoot = {nodeType: 1, tagName: "MAIN", attributes: [], firstChild: text("html only")};
const textRoot = {
  nodeType: 1,
  tagName: "MAIN",
  attributes: [],
  get firstChild() { throw new Error("html walk used during text-only extraction"); }
};
let root = textRoot;
globalThis.Node = {TEXT_NODE: 3, ELEMENT_NODE: 1};
globalThis.NodeFilter = {SHOW_TEXT: 4};
globalThis.TextEncoder = TextEncoder;
globalThis.TextDecoder = TextDecoder;
globalThis.location = {href: "https://example.com"};
globalThis.document = {
  documentElement: root,
  querySelector: () => root,
  createTreeWalker: (node) => {
    let done = false;
    return {nextNode: () => {
      if (done) return null;
      done = true;
      return text("text only");
    }};
  }
};
const textOnly = boundedContentExpression({selector: "main", maxBytes: 64, includeHTML: false, includeText: true});
root = htmlRoot;
globalThis.document.createTreeWalker = () => { throw new Error("text walk used during html-only extraction"); };
const htmlOnly = boundedContentExpression({selector: "main", maxBytes: 64, includeHTML: true, includeText: false});
console.log(JSON.stringify({textOnly, htmlOnly}));
`)
	var got struct {
		TextOnly struct {
			HTML      string `json:"html"`
			Text      string `json:"text"`
			Truncated bool   `json:"truncated"`
		} `json:"textOnly"`
		HTMLOnly struct {
			HTML      string `json:"html"`
			Text      string `json:"text"`
			Truncated bool   `json:"truncated"`
		} `json:"htmlOnly"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if got.TextOnly.HTML != "" || got.TextOnly.Text != "text only" || got.TextOnly.Truncated {
		t.Fatalf("text-only result = %#v", got.TextOnly)
	}
	if got.HTMLOnly.Text != "" || !strings.Contains(got.HTMLOnly.HTML, "html only") || got.HTMLOnly.Truncated {
		t.Fatalf("html-only result = %#v", got.HTMLOnly)
	}
}

func TestMCPSnapshotExpressionRedactsSensitiveValues(t *testing.T) {
	if strings.Contains(snapshotExpression, "|| el.value ||") {
		t.Fatal("snapshot expression uses element value as accessible name")
	}
	for _, token := range []string{fmt.Sprintf("MAX_VALUE_LENGTH = %d", maxSnapshotValueLength), "includeValues", "getClientRects", "password", "hidden", "token", "secret", "csrf", "otp", "jwt", "value.length", "value_kind"} {
		if !strings.Contains(snapshotExpression, token) {
			t.Fatalf("snapshot expression missing redaction token %q", token)
		}
	}
}

func TestMCPSnapshotExpressionRedactsDOMValues(t *testing.T) {
	script := `
const snapshotExpression = ` + snapshotExpression + `;
class Element {
  constructor(tag, attrs, value, text) {
    this.tagName = tag.toUpperCase();
    this.attrs = attrs || {};
    this.value = value || "";
    this.innerText = text || "";
    this.id = this.attrs.id || "";
    this.hidden = !!this.attrs.hidden;
    this.nodeType = 1;
    this.parentElement = null;
    this.children = [];
  }
  getAttribute(name) {
    return this.attrs[name] || "";
  }
  getClientRects() {
    return this.attrs.zeroRect ? [] : [{}];
  }
}
const visible = {display: "block", visibility: "visible"};
const nodes = [
  new Element("input", {id: "email", type: "email", "aria-label": "Email"}, "user@example.com"),
  new Element("input", {id: "password", type: "password", "aria-label": "Password"}, "secret"),
  new Element("input", {id: "hidden", type: "hidden", "aria-label": "Hidden"}, "hidden-secret"),
  new Element("input", {id: "hidden-text", type: "text", "aria-label": "HiddenText", hidden: true}, "hidden-text"),
  new Element("input", {id: "css-hidden", type: "text", "aria-label": "CSSHidden", style: {display: "none", visibility: "visible"}}, "css-hidden"),
  new Element("input", {id: "aria-hidden", type: "text", "aria-label": "AriaHidden", "aria-hidden": "true"}, "aria-hidden"),
  new Element("input", {id: "zero", type: "text", "aria-label": "ZeroRect", zeroRect: true}, "zero"),
  new Element("input", {id: "csrf", type: "text", name: "csrf"}, "csrf-token"),
  new Element("input", {id: "otp", type: "text", autocomplete: "one-time-code", "aria-label": "OTP"}, "123456"),
  new Element("input", {id: "jwt", type: "text", "aria-label": "JWT"}, "aaa.bbb.ccc"),
  new Element("textarea", {id: "notes", "aria-label": "Notes"}, "` + strings.Repeat("x", maxSnapshotValueLength+1) + `"),
  new Element("input", {id: "submit", type: "submit"}, "Sign in")
];
globalThis.window = {getComputedStyle: (el) => el.attrs.style || visible};
globalThis.getComputedStyle = window.getComputedStyle;
globalThis.CSS = {escape: (value) => String(value)};
globalThis.document = {querySelectorAll: () => nodes};
const enabled = snapshotExpression({max: 0, interactiveOnly: false, includeValues: true});
const disabled = snapshotExpression({max: 0, interactiveOnly: false, includeValues: false});
console.log(JSON.stringify({enabled, disabled}));
`
	output := runNodeExpression(t, script)
	var got struct {
		Enabled  []map[string]any `json:"enabled"`
		Disabled []map[string]any `json:"disabled"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	enabled := elementsByName(got.Enabled)
	if enabled["Email"]["value"] != "user@example.com" {
		t.Fatalf("safe email not returned: %#v", got.Enabled)
	}
	if enabled["Sign in"]["role"] != "button" {
		t.Fatalf("submit input not labeled as button: %#v", got.Enabled)
	}
	if _, ok := enabled["Sign in"]["value"]; ok {
		t.Fatalf("submit button value leaked as form value: %#v", enabled["Sign in"])
	}
	for _, name := range []string{"Password", "csrf", "OTP", "JWT", "Notes"} {
		if _, ok := enabled[name]["value"]; ok {
			t.Fatalf("%s leaked value: %#v", name, got.Enabled)
		}
	}
	for _, hidden := range []string{"Hidden", "HiddenText", "CSSHidden", "AriaHidden", "ZeroRect"} {
		if _, ok := enabled[hidden]; ok {
			t.Fatalf("hidden element %s was included: %#v", hidden, got.Enabled)
		}
	}
	for _, element := range got.Disabled {
		if _, ok := element["value"]; ok {
			t.Fatalf("disabled value capture leaked: %#v", got.Disabled)
		}
	}
}

func TestMCPSnapshotExpressionUsesDirectQuerySelectorAll(t *testing.T) {
	output := runNodeExpression(t, `
const snapshotExpression = `+snapshotExpression+`;
class Element {
  constructor(tag, attrs, value, text) {
    this.tagName = tag.toUpperCase();
    this.attrs = attrs || {};
    this.value = value || "";
    this.innerText = text || "";
    this.id = this.attrs.id || "";
    this.hidden = false;
    this.nodeType = 1;
    this.parentElement = null;
    this.children = [];
  }
  getAttribute(name) { return this.attrs[name] || ""; }
  getClientRects() { return [{}]; }
}
const trusted = [new Element("button", {id: "trusted", "aria-label": "Trusted"}, "", "")];
globalThis.window = {getComputedStyle: () => ({display: "block", visibility: "visible"})};
globalThis.CSS = {escape: (value) => String(value)};
globalThis.document = {querySelectorAll: () => trusted};
const result = snapshotExpression({max: 0, interactiveOnly: true, includeValues: false});
console.log(JSON.stringify(result));
`)
	var got []map[string]any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node output %q: %v", output, err)
	}
	if len(got) != 1 || got[0]["name"] != "Trusted" {
		t.Fatalf("snapshot used wrong selector result: %#v", got)
	}
}

func runNodeExpression(t *testing.T, script string) []byte {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	output, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node expression failed: %v\n%s", err, output)
	}
	return output
}

func TestMainWorldExpressionPrefixesOnlyOnce(t *testing.T) {
	if got := mainWorldExpression("() => 1"); got != "mw:() => 1" {
		t.Fatalf("mainWorldExpression plain = %q", got)
	}
	if got := mainWorldExpression("mw:() => 1"); got != "mw:() => 1" {
		t.Fatalf("mainWorldExpression prefixed = %q", got)
	}
}

func elementsByName(elements []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, element := range elements {
		if name, ok := element["name"].(string); ok {
			out[name] = element
		}
	}
	return out
}

func TestGomoufoxSessionSnapshotValuesRequireOptIn(t *testing.T) {
	page := &fakeMCPPage{
		title: "Form",
		url:   "https://example.com/form",
		snapshot: []map[string]any{
			{"role": "textbox", "name": "Email", "value": "user@example.com", "value_kind": "safe", "resolver": "input"},
		},
	}
	session := &gomoufoxSession{page: page, refs: a11y.NewStore()}

	snap, err := session.Snapshot(context.Background(), snapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.Elements[0]["value"]; ok {
		t.Fatalf("default snapshot leaked value: %#v", snap.Elements)
	}
	if arg, ok := page.internalEvaluateArg.(map[string]any); !ok || arg["includeValues"] != false {
		t.Fatalf("default snapshot internal arg = %#v", page.internalEvaluateArg)
	}

	snap, err = session.Snapshot(context.Background(), snapshotOptions{IncludeValues: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Elements[0]["value"] != "user@example.com" {
		t.Fatalf("include-values snapshot = %#v", snap.Elements)
	}
	if arg, ok := page.internalEvaluateArg.(map[string]any); !ok || arg["includeValues"] != true {
		t.Fatalf("include-values snapshot internal arg = %#v", page.internalEvaluateArg)
	}
}

func TestGomoufoxSessionSnapshotOnlySafeShortTextboxesExposeValues(t *testing.T) {
	page := &fakeMCPPage{
		title: "Mixed",
		url:   "https://example.com/form",
		snapshot: []map[string]any{
			{"role": "button", "name": "Sign in", "value": "button-value", "value_kind": "safe", "resolver": "button"},
			{"role": "textbox", "name": "Email", "value": strings.Repeat("e", maxSnapshotValueLength), "value_kind": "safe", "resolver": "#email"},
			{"role": "textbox", "name": "Notes", "value": strings.Repeat("x", maxSnapshotValueLength+1), "value_kind": "safe", "resolver": "#notes"},
		},
	}
	session := &gomoufoxSession{page: page, refs: a11y.NewStore()}

	snap, err := session.Snapshot(context.Background(), snapshotOptions{IncludeValues: true})
	if err != nil {
		t.Fatal(err)
	}
	byName := elementsByName(snap.Elements)
	if _, ok := byName["Sign in"]["value"]; ok {
		t.Fatalf("button value leaked: %#v", snap.Elements)
	}
	if byName["Email"]["value"] != strings.Repeat("e", maxSnapshotValueLength) {
		t.Fatalf("safe boundary value missing: %#v", snap.Elements)
	}
	if _, ok := byName["Notes"]["value"]; ok {
		t.Fatalf("overlong value leaked: %#v", snap.Elements)
	}
}

func TestGomoufoxSessionSnapshotDropsSensitiveValuesEvenWithOptIn(t *testing.T) {
	page := &fakeMCPPage{
		title: "Sensitive",
		url:   "https://example.com/form",
		snapshot: []map[string]any{
			{"role": "button", "name": "Sign in", "resolver": "button"},
			{"role": "textbox", "name": "Password", "value": "secret", "value_kind": "password", "resolver": "#password"},
			{"role": "textbox", "name": "Hidden", "value": "hidden-secret", "value_kind": "hidden", "resolver": "#hidden"},
			{"role": "textbox", "name": "API token", "value": "token-secret", "value_kind": "token", "resolver": "#token"},
			{"role": "textbox", "name": "Notes", "value": strings.Repeat("x", 121), "value_kind": "long", "resolver": "#notes"},
		},
	}
	session := &gomoufoxSession{page: page, refs: a11y.NewStore()}

	snap, err := session.Snapshot(context.Background(), snapshotOptions{IncludeValues: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Elements) != 5 || snap.Elements[0]["name"] != "Sign in" {
		t.Fatalf("snapshot elements = %#v", snap.Elements)
	}
	for _, element := range snap.Elements {
		if _, ok := element["value"]; ok {
			t.Fatalf("sensitive snapshot leaked value: %#v", snap.Elements)
		}
	}
}

func TestScreenshotPixelBytesCapsOverflow(t *testing.T) {
	const maxInt64 = int64(^uint64(0) >> 1)
	for _, tc := range []struct {
		name   string
		width  int
		height int
		want   int64
	}{
		{name: "zero", width: 0, height: 100, want: 0},
		{name: "negative", width: 100, height: -1, want: 0},
		{name: "normal", width: 3, height: 4, want: 48},
		{name: "overflow", width: int(^uint(0) >> 1), height: int(^uint(0) >> 1), want: maxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := screenshotPixelBytes(tc.width, tc.height)
			if got != tc.want {
				t.Fatalf("screenshotPixelBytes(%d,%d) = %d, want %d", tc.width, tc.height, got, tc.want)
			}
		})
	}

	if got := screenshotWorkLimitBytes(0); got != 0 {
		t.Fatalf("zero screenshot work limit = %d", got)
	}
	if got := screenshotWorkLimitBytes(1024); got != 16*1024 {
		t.Fatalf("small screenshot work limit = %d", got)
	}
	if got := screenshotWorkLimitBytes(policy.FullPageScreenshotBytes); got != maxScreenshotAcquisitionBytes {
		t.Fatalf("full-page screenshot work limit = %d", got)
	}
}

func TestGomoufoxAdaptersAndLauncherSeams(t *testing.T) {
	ctx := context.Background()
	page := &fakeMCPPage{title: "T", url: "https://example.com", screenshotData: []byte("shot")}
	browserContext := &fakeMCPContext{page: page}
	locator := &fakeMCPLocator{}

	browser := browserAdapter{
		newContext: func(context.Context, ...gomoufox.ContextOption) (mcpContext, error) { return browserContext, nil },
		newPage:    func(context.Context, ...gomoufox.ContextOption) (mcpPage, error) { return page, nil },
		close:      func() error { return nil },
	}
	if got, err := browser.NewContext(ctx); err != nil || got != browserContext {
		t.Fatalf("browser NewContext = %#v err=%v", got, err)
	}
	if got, err := browser.NewPage(ctx); err != nil || got != page {
		t.Fatalf("browser NewPage = %#v err=%v", got, err)
	}
	if err := browser.Close(); err != nil {
		t.Fatalf("browser Close: %v", err)
	}

	contextAdapter := contextAdapter{
		newPage: func(context.Context) (mcpPage, error) { return page, nil },
		cookies: func(context.Context, ...string) ([]gomoufox.Cookie, error) {
			return []gomoufox.Cookie{{Name: "sid"}}, nil
		},
		addCookies:   func(context.Context, ...gomoufox.Cookie) error { return nil },
		clearCookies: func(context.Context) error { return nil },
		storageState: func(context.Context, string) (*gomoufox.StorageState, error) {
			return &gomoufox.StorageState{Cookies: []gomoufox.Cookie{{Name: "sid"}}}, nil
		},
		close: func() error { return nil },
	}
	if got, err := contextAdapter.NewPage(ctx); err != nil || got != page {
		t.Fatalf("context NewPage = %#v err=%v", got, err)
	}
	if got, err := contextAdapter.Cookies(ctx, "https://example.com"); err != nil || got[0].Name != "sid" {
		t.Fatalf("context Cookies = %#v err=%v", got, err)
	}
	if err := contextAdapter.AddCookies(ctx, gomoufox.Cookie{Name: "sid"}); err != nil {
		t.Fatalf("context AddCookies: %v", err)
	}
	if err := contextAdapter.ClearCookies(ctx); err != nil {
		t.Fatalf("context ClearCookies: %v", err)
	}
	if got, err := contextAdapter.StorageState(ctx, "state.json"); err != nil || got.Cookies[0].Name != "sid" {
		t.Fatalf("context StorageState = %#v err=%v", got, err)
	}
	if err := contextAdapter.Close(); err != nil {
		t.Fatalf("context Close: %v", err)
	}

	adapterPage := pageAdapter{
		gotoFunc: func(context.Context, string, ...gomoufox.GotoOption) (*gomoufox.Response, error) { return nil, nil },
		waitNavigation: func(_ context.Context, action func() error, _ ...gomoufox.NavigateOption) error {
			return action()
		},
		title:            func(context.Context) (string, error) { return "Title", nil },
		url:              func() string { return "https://example.com" },
		content:          func(context.Context) (string, error) { return "<p>x</p>", nil },
		evaluate:         func(context.Context, string, ...any) (any, error) { return map[string]any{"ok": true}, nil },
		evaluateInternal: func(context.Context, string, ...any) (any, error) { return map[string]any{"internal": true}, nil },
		addInitScript:    func(context.Context, string) error { return nil },
		waitLoadState:    func(context.Context, string) error { return nil },
		waitSelector: func(context.Context, string, ...gomoufox.WaitForSelectorOption) (*gomoufox.ElementHandle, error) {
			return nil, nil
		},
		waitURL:    func(context.Context, string, ...gomoufox.GotoOption) error { return nil },
		screenshot: func(context.Context, ...gomoufox.ScreenshotOption) ([]byte, error) { return []byte("shot"), nil },
		locator:    func(string) gomoufox.Locator { return locator },
		wheel:      func(context.Context, float64, float64) error { return nil },
		fetchBytes: func(context.Context, string, string, map[string]string, []byte) (int, []byte, error) {
			return 200, []byte("ok"), nil
		},
		onRequest:       func(fn func(*gomoufox.Request)) { fn(nil) },
		onRequestFailed: func(fn func(*gomoufox.Request)) { fn(nil) },
		onResponse:      func(fn func(*gomoufox.Response)) { fn(nil) },
		onPageError:     func(fn func(error)) { fn(nil) },
		onConsole:       func(fn func(gomoufox.ConsoleMessage)) { fn(gomoufox.ConsoleMessage{}) },
		onDialog:        func(fn func(gomoufox.Dialog)) { fn(gomoufox.Dialog{}) },
		close:           func() error { return nil },
	}
	if _, err := adapterPage.Goto(ctx, "https://example.com"); err != nil {
		t.Fatalf("page Goto: %v", err)
	}
	if err := adapterPage.RunAndWaitForNavigation(ctx, func() error { return nil }); err != nil {
		t.Fatalf("page RunAndWaitForNavigation: %v", err)
	}
	if title, _ := adapterPage.Title(ctx); title != "Title" {
		t.Fatalf("page title = %q", title)
	}
	if adapterPage.URL() != "https://example.com" {
		t.Fatalf("page url = %q", adapterPage.URL())
	}
	if content, _ := adapterPage.Content(ctx); content != "<p>x</p>" {
		t.Fatalf("page content = %q", content)
	}
	if result, _ := adapterPage.Evaluate(ctx, "script"); result == nil {
		t.Fatal("page evaluate returned nil")
	}
	if result, _ := adapterPage.EvaluateInternal(ctx, "script"); result == nil {
		t.Fatal("page internal evaluate returned nil")
	}
	if err := adapterPage.AddInitScript(ctx, "script"); err != nil {
		t.Fatalf("page add init script: %v", err)
	}
	if _, err := (pageAdapter{}).EvaluateInternal(ctx, "script"); err == nil {
		t.Fatal("missing internal evaluator returned nil error")
	}
	if err := adapterPage.WaitForLoadState(ctx, "load"); err != nil {
		t.Fatalf("page wait load: %v", err)
	}
	if _, err := adapterPage.WaitForSelector(ctx, "#x"); err != nil {
		t.Fatalf("page wait selector: %v", err)
	}
	if err := adapterPage.WaitForURL(ctx, "**/x"); err != nil {
		t.Fatalf("page wait url: %v", err)
	}
	if data, _ := adapterPage.Screenshot(ctx); string(data) != "shot" {
		t.Fatalf("page screenshot = %q", data)
	}
	if adapterPage.Locator("button") != locator {
		t.Fatal("page locator mismatch")
	}
	if err := adapterPage.Wheel(ctx, 1, 2); err != nil {
		t.Fatalf("page wheel: %v", err)
	}
	if status, body, _ := adapterPage.FetchBytes(ctx, "https://example.com", "GET", nil, nil); status != 200 || string(body) != "ok" {
		t.Fatalf("page fetch status=%d body=%q", status, body)
	}
	adapterPage.OnRequest(func(*gomoufox.Request) {})
	adapterPage.OnRequestFailed(func(*gomoufox.Request) {})
	adapterPage.OnResponse(func(*gomoufox.Response) {})
	adapterPage.OnPageError(func(error) {})
	adapterPage.OnConsole(func(gomoufox.ConsoleMessage) {})
	adapterPage.OnDialog(func(gomoufox.Dialog) {})
	if err := adapterPage.Close(); err != nil {
		t.Fatalf("page close: %v", err)
	}

	_ = adaptBrowser(&gomoufox.Browser{})
	_ = adaptContext(&gomoufox.Context{})
	_ = adaptPage(&gomoufox.Page{})

	oldNew := newGomoufoxForMCP
	t.Cleanup(func() { newGomoufoxForMCP = oldNew })
	newGomoufoxForMCP = func(context.Context, ...gomoufox.Option) (*gomoufox.Browser, error) {
		return nil, errors.New("launch")
	}
	if _, err := (realGomoufoxLauncher{}).Launch(ctx, sessionOptions{}, false); err == nil {
		t.Fatal("expected launch error")
	}
	if _, err := (realGomoufoxLauncher{}).Launch(ctx, sessionOptions{proxy: "://bad"}, true); err == nil {
		t.Fatal("expected proxy error")
	}
	launchOptionCount := 0
	newGomoufoxForMCP = func(_ context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
		launchOptionCount = len(opts)
		return &gomoufox.Browser{}, nil
	}
	if got, err := (realGomoufoxLauncher{}).Launch(ctx, sessionOptions{}, false); err != nil || got == nil {
		t.Fatalf("default launch = %#v err=%v", got, err)
	}
	if launchOptionCount != 1 {
		t.Fatalf("default launch option count = %d, want only main-world eval so gomoufox keeps its node-direct runtime default", launchOptionCount)
	}
	launchOptionCount = 0
	newGomoufoxForMCP = func(_ context.Context, opts ...gomoufox.Option) (*gomoufox.Browser, error) {
		launchOptionCount = len(opts)
		return &gomoufox.Browser{}, nil
	}
	launcher := realGomoufoxLauncher{policy: policy.Config{AllowedOrigins: []string{"https://example.com"}, AllowedHosts: []string{"example.com"}}}
	if got, err := launcher.Launch(ctx, sessionOptions{os: "linux", locale: "en-US", proxy: "http://proxy.example:8080", profilePath: t.TempDir()}, true); err != nil || got == nil {
		t.Fatalf("launch success = %#v err=%v", got, err)
	}
	if launchOptionCount != 7 {
		t.Fatalf("launch option count = %d, want main-world eval, persona, proxy, profile, and allowlist options", launchOptionCount)
	}

	sessionProxyBrowser := &fakeMCPBrowser{context: &fakeMCPContext{page: &fakeMCPPage{}}}
	sessionProxyLauncher := &fakeGomoufoxLauncher{browsers: []mcpBrowser{sessionProxyBrowser}}
	if got, err := (&gomoufoxFactory{launcher: sessionProxyLauncher}).NewBrowserSession(context.Background(), sessionOptions{proxy: "http://proxy.example:8080"}); err != nil || got == nil {
		t.Fatalf("session proxy launch = %#v err=%v", got, err)
	}
	if len(sessionProxyLauncher.calls) != 1 || !sessionProxyLauncher.calls[0].dedicated || sessionProxyLauncher.calls[0].opts.proxy != "http://proxy.example:8080" {
		t.Fatalf("session proxy launcher calls = %#v", sessionProxyLauncher.calls)
	}
	if len(sessionProxyBrowser.contextOptionCounts) != 1 || sessionProxyBrowser.contextOptionCounts[0] != 0 {
		t.Fatalf("session proxy became context option counts = %#v", sessionProxyBrowser.contextOptionCounts)
	}
}

func TestRealAdaptersWrapRootObjectsWithBridgeFakes(t *testing.T) {
	ctx := context.Background()
	pwPage := &fakePWPage{title: "Title", url: "https://example.com", content: "<p>x</p>", screenshot: []byte("shot"), locator: &fakePWLocator{}}
	pwContext := &fakePWContext{page: pwPage, cookies: []pwbridge.Cookie{{Name: "sid", Value: "1"}}, storage: &pwbridge.StorageState{Cookies: []pwbridge.Cookie{{Name: "sid", Value: "1"}}}}
	pwBrowser := &fakePWBrowser{context: pwContext, page: pwPage}
	rootBrowser := &gomoufox.Browser{}
	setField(rootBrowser, "raw", pwBrowser)
	setField(rootBrowser, "done", make(chan struct{}))
	rootContext := &gomoufox.Context{}
	setField(rootContext, "raw", pwContext)
	rootPage := &gomoufox.Page{}
	setField(rootPage, "raw", pwPage)

	adaptedBrowser := adaptBrowser(rootBrowser)
	if got, err := adaptedBrowser.NewContext(ctx); err != nil || got == nil {
		t.Fatalf("adapted browser NewContext = %#v err=%v", got, err)
	}
	if got, err := adaptedBrowser.NewPage(ctx); err != nil || got == nil {
		t.Fatalf("adapted browser NewPage = %#v err=%v", got, err)
	}
	if err := adaptedBrowser.Close(); err != nil {
		t.Fatalf("adapted browser Close: %v", err)
	}

	adaptedContext := adaptContext(rootContext)
	if got, err := adaptedContext.NewPage(ctx); err != nil || got == nil {
		t.Fatalf("adapted context NewPage = %#v err=%v", got, err)
	}
	if got, err := adaptedContext.Cookies(ctx); err != nil || got[0].Name != "sid" {
		t.Fatalf("adapted context Cookies = %#v err=%v", got, err)
	}
	if err := adaptedContext.AddCookies(ctx, gomoufox.Cookie{Name: "new"}); err != nil {
		t.Fatalf("adapted context AddCookies: %v", err)
	}
	if err := adaptedContext.ClearCookies(ctx); err != nil {
		t.Fatalf("adapted context ClearCookies: %v", err)
	}
	if got, err := adaptedContext.StorageState(ctx, ""); err != nil || got.Cookies[0].Name != "sid" {
		t.Fatalf("adapted context StorageState = %#v err=%v", got, err)
	}
	if err := adaptedContext.Close(); err != nil {
		t.Fatalf("adapted context Close: %v", err)
	}

	adaptedPage := adaptPage(rootPage)
	if _, err := adaptedPage.Goto(ctx, "https://example.com"); err != nil {
		t.Fatalf("adapted page Goto: %v", err)
	}
	if got, err := adaptedPage.Title(ctx); err != nil || got != "Title" {
		t.Fatalf("adapted page Title = %q err=%v", got, err)
	}
	if adaptedPage.URL() != "https://example.com" {
		t.Fatalf("adapted page URL = %q", adaptedPage.URL())
	}
	if got, err := adaptedPage.Content(ctx); err != nil || got != "<p>x</p>" {
		t.Fatalf("adapted page Content = %q err=%v", got, err)
	}
	if got, err := adaptedPage.Evaluate(ctx, "expr"); err != nil || got == nil {
		t.Fatalf("adapted page Evaluate = %#v err=%v", got, err)
	}
	if got, err := adaptedPage.EvaluateInternal(ctx, "internal"); err != nil || got == nil {
		t.Fatalf("adapted page EvaluateInternal = %#v err=%v", got, err)
	}
	if err := adaptedPage.WaitForLoadState(ctx, "load"); err != nil {
		t.Fatalf("adapted page WaitForLoadState: %v", err)
	}
	if _, err := adaptedPage.WaitForSelector(ctx, "#x"); err != nil {
		t.Fatalf("adapted page WaitForSelector: %v", err)
	}
	if err := adaptedPage.WaitForURL(ctx, "**/x"); err != nil {
		t.Fatalf("adapted page WaitForURL: %v", err)
	}
	if got, err := adaptedPage.Screenshot(ctx); err != nil || string(got) != "shot" {
		t.Fatalf("adapted page Screenshot = %q err=%v", got, err)
	}
	if adaptedPage.Locator("button") == nil {
		t.Fatal("adapted page Locator nil")
	}
	adaptedSession := &gomoufoxSession{page: adaptedPage, refs: a11y.NewStore()}
	if err := adaptedSession.Click(ctx, elementTarget{Selector: "button"}, clickOptions{Button: "right", ClickCount: 2, Timeout: time.Second}); err != nil {
		t.Fatalf("adapted session click options: %v", err)
	}
	if pwPage.locator.clickOptions.Button != "right" || pwPage.locator.clickOptions.ClickCount != 2 || pwPage.locator.clickOptions.Timeout != time.Second {
		t.Fatalf("adapted click options = %#v", pwPage.locator.clickOptions)
	}
	if err := adaptedSession.Click(ctx, elementTarget{Selector: "button"}, clickOptions{Button: "middle", ClickCount: 3, WaitForNavigation: true, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("adapted session click wait navigation: %v", err)
	}
	if pwPage.waitNavigationCalls != 1 || pwPage.waitNavigationOptions.Timeout != 2*time.Second || pwPage.waitNavigationOptions.WaitUntil != "domcontentloaded" || pwPage.locator.clickOptions.Button != "middle" || pwPage.locator.clickOptions.ClickCount != 3 {
		t.Fatalf("adapted navigation click calls=%d nav=%#v click=%#v", pwPage.waitNavigationCalls, pwPage.waitNavigationOptions, pwPage.locator.clickOptions)
	}
	if status, body, err := adaptedPage.FetchBytes(ctx, "https://api.example.com", "GET", nil, nil); err != nil || status != 200 || string(body) != "ok" {
		t.Fatalf("adapted page FetchBytes status=%d body=%q err=%v", status, body, err)
	}
	if err := adaptedPage.Close(); err != nil {
		t.Fatalf("adapted page Close: %v", err)
	}

	errBrowser := &gomoufox.Browser{}
	setField(errBrowser, "raw", &fakePWBrowser{context: pwContext, ctxErr: errors.New("ctx")})
	setField(errBrowser, "done", make(chan struct{}))
	if _, err := adaptBrowser(errBrowser).NewContext(ctx); err == nil {
		t.Fatal("expected adapted browser NewContext error")
	}
	pageErrBrowser := &gomoufox.Browser{}
	setField(pageErrBrowser, "raw", &fakePWBrowser{context: &fakePWContext{pageErr: errors.New("page")}})
	setField(pageErrBrowser, "done", make(chan struct{}))
	if _, err := adaptBrowser(pageErrBrowser).NewPage(ctx); err == nil {
		t.Fatal("expected adapted browser NewPage error")
	}
	errContext := &gomoufox.Context{}
	setField(errContext, "raw", &fakePWContext{pageErr: errors.New("ctx-page")})
	if _, err := adaptContext(errContext).NewPage(ctx); err == nil {
		t.Fatal("expected adapted context NewPage error")
	}
}

func TestGomoufoxSessionErrorBranches(t *testing.T) {
	boom := errors.New("boom")
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{}}).NewBrowserSession(context.Background(), sessionOptions{}); err == nil {
		t.Fatal("expected shared launch error")
	}
	browser := &fakeMCPBrowser{contextErr: boom}
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{browsers: []mcpBrowser{browser}}}).NewBrowserSession(context.Background(), sessionOptions{}); !errors.Is(err, boom) {
		t.Fatalf("context err = %v", err)
	}
	contextWithPageErr := &fakeMCPContext{newPageErr: boom}
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{browsers: []mcpBrowser{&fakeMCPBrowser{context: contextWithPageErr}}}}).NewBrowserSession(context.Background(), sessionOptions{}); !errors.Is(err, boom) {
		t.Fatalf("new page err = %v", err)
	}
	profileContextErr := &fakeMCPBrowser{contextErr: boom}
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{browsers: []mcpBrowser{profileContextErr}}}).NewBrowserSession(context.Background(), sessionOptions{profilePath: t.TempDir()}); !errors.Is(err, boom) || profileContextErr.closeCalls != 1 {
		t.Fatalf("profile context err=%v browserClose=%d", err, profileContextErr.closeCalls)
	}
	profilePageErrContext := &fakeMCPContext{newPageErr: boom}
	profilePageErrBrowser := &fakeMCPBrowser{context: profilePageErrContext}
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{browsers: []mcpBrowser{profilePageErrBrowser}}}).NewBrowserSession(context.Background(), sessionOptions{profilePath: t.TempDir()}); !errors.Is(err, boom) || profilePageErrContext.closeCalls != 1 || profilePageErrBrowser.closeCalls != 1 {
		t.Fatalf("profile page err=%v contextClose=%d browserClose=%d", err, profilePageErrContext.closeCalls, profilePageErrBrowser.closeCalls)
	}
	profileProbeErrPage := &fakeMCPPage{probePayload: map[string]any{"ok": false, "hostFlag": true}}
	profileProbeErrContext := &fakeMCPContext{page: profileProbeErrPage}
	profileProbeErrBrowser := &fakeMCPBrowser{context: profileProbeErrContext}
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{browsers: []mcpBrowser{profileProbeErrBrowser}}}).NewBrowserSession(context.Background(), sessionOptions{profilePath: t.TempDir()}); !errors.Is(err, errInternalEvaluationUnavailable) || profileProbeErrPage.closeCalls != 1 || profileProbeErrContext.closeCalls != 1 || profileProbeErrBrowser.closeCalls != 1 {
		t.Fatalf("profile probe err=%v pageClose=%d contextClose=%d browserClose=%d", err, profileProbeErrPage.closeCalls, profileProbeErrContext.closeCalls, profileProbeErrBrowser.closeCalls)
	}
	closeOnOptionErr := &fakeMCPBrowser{}
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{browsers: []mcpBrowser{closeOnOptionErr}}}).NewBrowserSession(context.Background(), sessionOptions{os: "linux", storageStatePath: filepath.Join(t.TempDir(), "missing.json")}); err == nil || closeOnOptionErr.closeCalls != 1 {
		t.Fatalf("option err=%v closeCalls=%d", err, closeOnOptionErr.closeCalls)
	}
	closeOnContextErr := &fakeMCPBrowser{contextErr: boom}
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{browsers: []mcpBrowser{closeOnContextErr}}}).NewBrowserSession(context.Background(), sessionOptions{os: "linux"}); !errors.Is(err, boom) || closeOnContextErr.closeCalls != 1 {
		t.Fatalf("dedicated context err=%v closeCalls=%d", err, closeOnContextErr.closeCalls)
	}
	closeContext := &fakeMCPContext{newPageErr: boom}
	closeOnPageErr := &fakeMCPBrowser{context: closeContext}
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{browsers: []mcpBrowser{closeOnPageErr}}}).NewBrowserSession(context.Background(), sessionOptions{os: "linux"}); !errors.Is(err, boom) || closeContext.closeCalls != 1 || closeOnPageErr.closeCalls != 1 {
		t.Fatalf("dedicated page err=%v contextClose=%d browserClose=%d", err, closeContext.closeCalls, closeOnPageErr.closeCalls)
	}
	closeProbeErrPage := &fakeMCPPage{probePayload: map[string]any{"ok": false, "queryPatched": true}}
	closeProbeErrContext := &fakeMCPContext{page: closeProbeErrPage}
	closeOnProbeErr := &fakeMCPBrowser{context: closeProbeErrContext}
	if _, err := (&gomoufoxFactory{launcher: &fakeGomoufoxLauncher{browsers: []mcpBrowser{closeOnProbeErr}}}).NewBrowserSession(context.Background(), sessionOptions{os: "linux"}); !errors.Is(err, errInternalEvaluationUnavailable) || closeProbeErrPage.closeCalls != 1 || closeProbeErrContext.closeCalls != 1 || closeOnProbeErr.closeCalls != 1 {
		t.Fatalf("dedicated probe err=%v pageClose=%d contextClose=%d browserClose=%d", err, closeProbeErrPage.closeCalls, closeProbeErrContext.closeCalls, closeOnProbeErr.closeCalls)
	}
	if _, err := contextOptions(sessionOptions{storageStatePath: filepath.Join(t.TempDir(), "missing.json")}); err == nil {
		t.Fatal("missing storage state succeeded")
	}
	badState := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badState, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := contextOptions(sessionOptions{storageStatePath: badState}); err == nil {
		t.Fatal("bad storage state succeeded")
	}
	proxy, err := proxyConfig("http://proxy.example:8080")
	if err != nil || proxy.Server != "http://proxy.example:8080" || proxy.Username != "" || proxy.Password != "" {
		t.Fatalf("proxy config = %#v err=%v", proxy, err)
	}
	proxy, err = proxyConfig("http://user:pass@proxy.example:8080")
	if err != nil || proxy.Server != "http://proxy.example:8080" || proxy.Username != "user" || proxy.Password != "pass" {
		t.Fatalf("auth proxy config = %#v err=%v", proxy, err)
	}

	session := &gomoufoxSession{page: &fakeMCPPage{title: "T", url: "u", locator: fakeMCPLocator{}}, refs: a11y.NewStore()}
	if _, err := session.Cookies(context.Background(), cookieOptions{Action: "get"}); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("nil context cookies err = %v", err)
	}
	if state, err := session.SaveStorageState(context.Background(), ""); err != nil || state == nil {
		t.Fatalf("nil context save state=%#v err=%v", state, err)
	}
	if err := session.LoadStorageState(context.Background(), &gomoufox.StorageState{}); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("nil context load err = %v", err)
	}
	if err := (&gomoufoxSession{browser: &fakeMCPBrowser{}, context: &fakeMCPContext{}, page: &fakeMCPPage{}}).LoadStorageState(context.Background(), nil); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("nil state load err = %v", err)
	}
	oldPage := &fakeMCPPage{}
	oldContext := &fakeMCPContext{page: oldPage}
	if err := (&gomoufoxSession{browser: &fakeMCPBrowser{contextErr: boom}, context: oldContext, page: oldPage, refs: a11y.NewStore()}).LoadStorageState(context.Background(), &gomoufox.StorageState{}); !errors.Is(err, boom) || oldPage.closeCalls != 0 || oldContext.closeCalls != 0 {
		t.Fatalf("new context err load=%v oldPageClose=%d oldContextClose=%d", err, oldPage.closeCalls, oldContext.closeCalls)
	}
	newPageErrContext := &fakeMCPContext{newPageErr: boom}
	if err := (&gomoufoxSession{browser: &fakeMCPBrowser{context: newPageErrContext}, context: oldContext, page: oldPage, refs: a11y.NewStore()}).LoadStorageState(context.Background(), &gomoufox.StorageState{}); !errors.Is(err, boom) || newPageErrContext.closeCalls != 1 || oldPage.closeCalls != 0 || oldContext.closeCalls != 0 {
		t.Fatalf("new page err load=%v newContextClose=%d oldPageClose=%d oldContextClose=%d", err, newPageErrContext.closeCalls, oldPage.closeCalls, oldContext.closeCalls)
	}
	probeErrOldPage := &fakeMCPPage{}
	probeErrOldContext := &fakeMCPContext{page: probeErrOldPage}
	probeErrNewPage := &fakeMCPPage{probePayload: map[string]any{"ok": false, "cssPatched": true}}
	probeErrNewContext := &fakeMCPContext{page: probeErrNewPage}
	if err := (&gomoufoxSession{browser: &fakeMCPBrowser{context: probeErrNewContext}, context: probeErrOldContext, page: probeErrOldPage, refs: a11y.NewStore()}).LoadStorageState(context.Background(), &gomoufox.StorageState{}); !errors.Is(err, errInternalEvaluationUnavailable) || probeErrNewPage.closeCalls != 1 || probeErrNewContext.closeCalls != 1 || probeErrOldPage.closeCalls != 0 || probeErrOldContext.closeCalls != 0 {
		t.Fatalf("load state probe err=%v newPageClose=%d newContextClose=%d oldPageClose=%d oldContextClose=%d", err, probeErrNewPage.closeCalls, probeErrNewContext.closeCalls, probeErrOldPage.closeCalls, probeErrOldContext.closeCalls)
	}
	if err := (&gomoufoxSession{browser: &fakeMCPBrowser{contextErr: gomoufox.ErrPersistentContextLimit}, context: oldContext, page: oldPage, refs: a11y.NewStore()}).LoadStorageState(context.Background(), &gomoufox.StorageState{}); !errors.Is(err, ErrInvalidCall) {
		t.Fatalf("persistent load err = %v", err)
	}
	resp := &gomoufox.Response{}
	setField(resp, "raw", fakePWResponse{status: 207})
	statusSession := &gomoufoxSession{page: &fakeMCPPage{title: "T", url: "u", gotoResp: resp}, refs: a11y.NewStore()}
	if nav, err := statusSession.Navigate(context.Background(), "https://example.com", navigateOptions{}); err != nil || nav.Status != 207 {
		t.Fatalf("navigate status = %#v err=%v", nav, err)
	}
	if _, err := statusSession.Evaluate(context.Background(), "script", nil, evaluateOptions{}); err != nil {
		t.Fatalf("nil arg evaluate err = %v", err)
	}
	if err := (&gomoufoxSession{page: &fakeMCPPage{evalErr: boom}, refs: a11y.NewStore()}).Type(context.Background(), elementTarget{Ref: "e1"}, "x", typeOptions{}); !errors.Is(err, boom) {
		t.Fatalf("type locator err = %v", err)
	}
	if err := (&gomoufoxSession{page: &fakeMCPPage{evalErr: boom}, refs: a11y.NewStore()}).PressKey(context.Background(), elementTarget{Ref: "e1"}, "Escape", pressOptions{}); !errors.Is(err, boom) {
		t.Fatalf("press key locator err = %v", err)
	}
	fillErrPage := &fakeMCPPage{}
	fillErrPage.locator.fillErr = boom
	if err := (&gomoufoxSession{page: fillErrPage, refs: a11y.NewStore()}).Type(context.Background(), elementTarget{Selector: "input"}, "x", typeOptions{ClearFirst: true}); !errors.Is(err, boom) {
		t.Fatalf("type fill err = %v", err)
	}
	typeErrPage := &fakeMCPPage{}
	typeErrPage.locator.typeErr = boom
	if err := (&gomoufoxSession{page: typeErrPage, refs: a11y.NewStore()}).Type(context.Background(), elementTarget{Selector: "input"}, "x", typeOptions{}); !errors.Is(err, boom) {
		t.Fatalf("type err = %v", err)
	}
	pressErrPage := &fakeMCPPage{}
	pressErrPage.locator.pressErr = boom
	if err := (&gomoufoxSession{page: pressErrPage, refs: a11y.NewStore()}).Type(context.Background(), elementTarget{Selector: "input"}, "x", typeOptions{PressEnterAfter: true}); !errors.Is(err, boom) {
		t.Fatalf("type press err = %v", err)
	}
	if err := (&gomoufoxSession{page: pressErrPage, refs: a11y.NewStore()}).PressKey(context.Background(), elementTarget{Selector: "input"}, "Escape", pressOptions{}); !errors.Is(err, boom) {
		t.Fatalf("press key err = %v", err)
	}
	if _, err := (&gomoufoxSession{page: &fakeMCPPage{screenshotErr: boom}, refs: a11y.NewStore()}).Screenshot(context.Background(), screenshotOptions{}); !errors.Is(err, boom) {
		t.Fatalf("screenshot err = %v", err)
	}
	if _, err := (&gomoufoxSession{page: &fakeMCPPage{titleErr: boom}, refs: a11y.NewStore()}).Snapshot(context.Background(), snapshotOptions{}); !errors.Is(err, boom) {
		t.Fatalf("snapshot title err = %v", err)
	}
	clickErrPage := &fakeMCPPage{}
	clickErrPage.locator.clickErr = boom
	if err := (&gomoufoxSession{page: clickErrPage, refs: a11y.NewStore()}).Click(context.Background(), elementTarget{Selector: "button"}, clickOptions{}); !errors.Is(err, boom) {
		t.Fatalf("click err = %v", err)
	}
	if err := (&gomoufoxSession{page: clickErrPage, refs: a11y.NewStore()}).Click(context.Background(), elementTarget{Selector: "button"}, clickOptions{WaitForNavigation: true}); !errors.Is(err, boom) {
		t.Fatalf("navigation click action err = %v", err)
	}
	navErrPage := &fakeMCPPage{waitNavigationErr: boom}
	if err := (&gomoufoxSession{page: navErrPage, refs: a11y.NewStore()}).Click(context.Background(), elementTarget{Selector: "button"}, clickOptions{WaitForNavigation: true}); !errors.Is(err, boom) || navErrPage.locator.clicks != 1 {
		t.Fatalf("navigation click err=%v clicks=%d", err, navErrPage.locator.clicks)
	}
	if _, err := (&gomoufoxSession{page: &fakeMCPPage{gotoErr: boom}, refs: a11y.NewStore()}).Fetch(context.Background(), fetchOptions{URL: "https://api.example.com", Method: "GET", NavigateFirst: "https://example.com"}); !errors.Is(err, boom) {
		t.Fatalf("fetch navigate_first err = %v", err)
	}
	for name, page := range map[string]*fakeMCPPage{
		"goto":            {gotoErr: boom},
		"title":           {titleErr: boom},
		"content-title":   {titleErr: boom},
		"content":         {title: "T", contentErr: boom},
		"content-eval":    {title: "T", evalErr: boom},
		"content-decode":  {title: "T", contentPayload: func() {}},
		"content-failed":  {title: "T", contentPayload: map[string]any{"ok": false, "message": "selector not found"}},
		"snapshot":        {title: "T", evalErr: boom},
		"snapshot-decode": {title: "T", snapshot: func() {}},
		"snapshot-rich":   {title: "T", url: "u", snapshot: []map[string]any{{"role": "textbox", "name": "H", "level": float64(1), "value": "v", "value_kind": "safe", "href": "/h", "required": true, "resolver": "h1"}}},
		"viewport":        {title: "T", url: "u", screenshotData: []byte("x"), evalErr: boom},
		"viewport-decode": {title: "T", url: "u", screenshotData: []byte("x"), viewport: func() {}},
		"fetch-eval":      {evalErr: boom},
		"fetch-decode":    {fetchPayload: func() {}},
		"fetch-failed":    {fetchPayload: map[string]any{"ok": false, "message": "blocked"}},
	} {
		t.Run(name, func(t *testing.T) {
			s := &gomoufoxSession{page: page, refs: a11y.NewStore()}
			switch name {
			case "goto", "title":
				if _, err := s.Navigate(context.Background(), "https://example.com", navigateOptions{}); err == nil {
					t.Fatal("expected navigate error")
				}
			case "content-title", "content", "content-eval", "content-decode", "content-failed":
				if _, err := s.PageContent(context.Background(), pageContentOptions{MaxBytes: 1024, IncludeHTML: true, IncludeText: true}); err == nil {
					t.Fatal("expected content error")
				}
			case "snapshot", "snapshot-decode":
				if _, err := s.Snapshot(context.Background(), snapshotOptions{}); err == nil {
					t.Fatal("expected snapshot error")
				}
			case "snapshot-rich":
				got, err := s.Snapshot(context.Background(), snapshotOptions{IncludeValues: true})
				if err != nil || got.Elements[0]["level"] == nil || got.Elements[0]["value"] == nil || got.Elements[0]["href"] == nil || got.Elements[0]["required"] != true {
					t.Fatalf("rich snapshot = %#v err=%v", got, err)
				}
			case "viewport", "viewport-decode":
				shot, err := s.Screenshot(context.Background(), screenshotOptions{})
				if err != nil || shot.Width != 0 || shot.Height != 0 {
					t.Fatalf("viewport fallback shot=%#v err=%v", shot, err)
				}
			case "fetch-eval", "fetch-decode", "fetch-failed":
				if _, err := s.Fetch(context.Background(), fetchOptions{URL: "https://api.example.com", Method: "GET"}); err == nil {
					t.Fatal("expected fetch error")
				}
			}
			if err := session.Click(context.Background(), elementTarget{Ref: "missing"}, clickOptions{}); err == nil {
				t.Fatal("expected missing ref click error")
			}
			if _, err := (&gomoufoxSession{page: &fakeMCPPage{title: "T", evalErr: boom}, refs: a11y.NewStore()}).snapshotElements(context.Background(), 0, false, false); !errors.Is(err, boom) {
				t.Fatalf("snapshotElements eval err = %v", err)
			}
		})
	}
}

type fakeGomoufoxLauncher struct {
	browsers []mcpBrowser
	calls    []launcherCall
}

type launcherCall struct {
	opts      sessionOptions
	dedicated bool
}

func (f *fakeGomoufoxLauncher) Launch(_ context.Context, opts sessionOptions, dedicated bool) (mcpBrowser, error) {
	f.calls = append(f.calls, launcherCall{opts: opts, dedicated: dedicated})
	if len(f.browsers) == 0 {
		return nil, errors.New("no browser")
	}
	browser := f.browsers[0]
	f.browsers = f.browsers[1:]
	return browser, nil
}

type fakeMCPBrowser struct {
	context             *fakeMCPContext
	contexts            []*fakeMCPContext
	page                *fakeMCPPage
	contextErr          error
	pageErr             error
	newContextCalls     int
	newPageCalls        int
	contextOptionCounts []int
	closeCalls          int
}

func (b *fakeMCPBrowser) NewContext(_ context.Context, opts ...gomoufox.ContextOption) (mcpContext, error) {
	b.newContextCalls++
	b.contextOptionCounts = append(b.contextOptionCounts, len(opts))
	if b.contextErr != nil {
		return nil, b.contextErr
	}
	if len(b.contexts) > 0 {
		context := b.contexts[0]
		b.contexts = b.contexts[1:]
		return context, nil
	}
	return b.context, nil
}

func (b *fakeMCPBrowser) NewPage(_ context.Context, opts ...gomoufox.ContextOption) (mcpPage, error) {
	b.newPageCalls++
	if b.pageErr != nil {
		return nil, b.pageErr
	}
	return b.page, nil
}

func (b *fakeMCPBrowser) Close() error {
	b.closeCalls++
	return nil
}

type fakeMCPContext struct {
	page         *fakeMCPPage
	newPageErr   error
	newPageCalls int
	cookies      []gomoufox.Cookie
	addedCookies []gomoufox.Cookie
	clearCalls   int
	clearErr     error
	storage      *gomoufox.StorageState
	storagePath  string
	closeCalls   int
}

func (c *fakeMCPContext) NewPage(context.Context) (mcpPage, error) {
	c.newPageCalls++
	if c.newPageErr != nil {
		return nil, c.newPageErr
	}
	return c.page, nil
}
func (c *fakeMCPContext) Cookies(context.Context, ...string) ([]gomoufox.Cookie, error) {
	return c.cookies, nil
}
func (c *fakeMCPContext) AddCookies(_ context.Context, cookies ...gomoufox.Cookie) error {
	c.addedCookies = append(c.addedCookies, cookies...)
	return nil
}
func (c *fakeMCPContext) ClearCookies(context.Context) error {
	c.clearCalls++
	return c.clearErr
}
func (c *fakeMCPContext) StorageState(_ context.Context, path string) (*gomoufox.StorageState, error) {
	c.storagePath = path
	if c.storage == nil {
		return &gomoufox.StorageState{}, nil
	}
	return c.storage, nil
}
func (c *fakeMCPContext) Close() error {
	c.closeCalls++
	return nil
}

type fakeMCPPage struct {
	title                 string
	url                   string
	content               string
	bodyText              string
	gotoErr               error
	gotoResp              *gomoufox.Response
	titleErr              error
	contentErr            error
	evalErr               error
	internalEvalErr       error
	initErr               error
	restoreErr            error
	screenshotErr         error
	gotoURL               string
	evaluateExpression    string
	evaluateArg           any
	internalExpression    string
	internalEvaluateArg   any
	initScripts           []string
	evaluateCalls         int
	internalEvaluateCalls int
	pageWorldInternalErr  error
	screenshotData        []byte
	viewport              any
	pageMetrics           any
	selectorMetrics       any
	waitNavigationCalls   int
	waitNavigationErr     error
	snapshot              any
	patchPayload          any
	probePayload          any
	contentPayload        any
	fetchPayload          any
	pageErrorPayload      any
	performancePayload    any
	locator               fakeMCPLocator
	locatorCalls          int
	screenshotCalls       int
	closeCalls            int
	onRequest             func(*gomoufox.Request)
	onRequestFailed       func(*gomoufox.Request)
	onResponse            func(*gomoufox.Response)
	onPageError           func(error)
	onConsole             func(gomoufox.ConsoleMessage)
	onDialog              func(gomoufox.Dialog)
	wheelX                float64
	wheelY                float64
	wheelErr              error
}

func (p *fakeMCPPage) Goto(_ context.Context, rawURL string, opts ...gomoufox.GotoOption) (*gomoufox.Response, error) {
	p.gotoURL = rawURL
	if p.gotoErr != nil {
		return nil, p.gotoErr
	}
	if p.gotoResp != nil {
		return p.gotoResp, nil
	}
	return nil, nil
}

func (p *fakeMCPPage) RunAndWaitForNavigation(_ context.Context, action func() error, _ ...gomoufox.NavigateOption) error {
	p.waitNavigationCalls++
	if err := action(); err != nil {
		return err
	}
	if p.waitNavigationErr != nil {
		return p.waitNavigationErr
	}
	return nil
}

func (p *fakeMCPPage) Title(context.Context) (string, error) {
	if p.titleErr != nil {
		return "", p.titleErr
	}
	return p.title, nil
}
func (p *fakeMCPPage) URL() string { return p.url }
func (p *fakeMCPPage) Content(context.Context) (string, error) {
	if p.contentErr != nil {
		return "", p.contentErr
	}
	return p.content, nil
}
func (p *fakeMCPPage) Evaluate(_ context.Context, expression string, arg ...any) (any, error) {
	if expression == mainWorldExpression(mcpInternalProbeRestoreExpression) && p.restoreErr != nil {
		return nil, p.restoreErr
	}
	if p.evalErr != nil {
		return nil, p.evalErr
	}
	p.evaluateCalls++
	p.evaluateExpression = expression
	if len(arg) > 0 {
		p.evaluateArg = arg[0]
	} else {
		p.evaluateArg = nil
	}
	if expression == mainWorldExpression(mcpInternalProbePatchExpression) {
		if p.patchPayload != nil {
			return p.patchPayload, nil
		}
		return map[string]any{"ok": true, "queryTag": "GOMOUFOX_PATCHED", "css": "gomoufox-patched", "cssAvailable": true}, nil
	}
	if isMCPInternalExpression(expression) && p.pageWorldInternalErr != nil {
		return nil, p.pageWorldInternalErr
	}
	return map[string]any{"ok": true}, nil
}

func (p *fakeMCPPage) EvaluateInternal(_ context.Context, expression string, arg ...any) (any, error) {
	if p.internalEvalErr != nil {
		return nil, p.internalEvalErr
	}
	if p.evalErr != nil {
		return nil, p.evalErr
	}
	p.internalEvaluateCalls++
	p.internalExpression = expression
	if len(arg) > 0 {
		p.internalEvaluateArg = arg[0]
	} else {
		p.internalEvaluateArg = nil
	}
	switch expression {
	case pageErrorObserverDrainExpression:
		if p.pageErrorPayload != nil {
			return p.pageErrorPayload, nil
		}
		return map[string]any{"errors": []map[string]any{}, "dropped": float64(0)}, nil
	case boundedContentExpression:
		if p.contentErr != nil {
			return nil, p.contentErr
		}
		if p.contentPayload != nil {
			return p.contentPayload, nil
		}
		includeHTML := true
		includeText := true
		if argMap, ok := p.internalEvaluateArg.(map[string]any); ok {
			includeHTML = argMap["includeHTML"] != false
			includeText = argMap["includeText"] != false
		}
		if argMap, ok := p.internalEvaluateArg.(map[string]any); ok && argMap["selector"] == "main" {
			return map[string]any{"ok": true, "url": p.url, "html": contentField(includeHTML, "<button>Sign in</button>"), "text": contentField(includeText, "locator text")}, nil
		}
		return map[string]any{"ok": true, "url": p.url, "html": contentField(includeHTML, p.content), "text": contentField(includeText, p.bodyText)}, nil
	case snapshotExpression:
		return p.snapshot, nil
	case mcpFetchExpression:
		return p.fetchPayload, nil
	case performanceSnapshotExpression:
		if p.performancePayload != nil {
			return p.performancePayload, nil
		}
		return map[string]any{
			"url":        p.url,
			"title":      p.title,
			"navigation": map[string]any{"dom_content_loaded_ms": float64(1), "load_event_ms": float64(2)},
			"resources":  map[string]any{"count": float64(0), "by_initiator_type": map[string]any{}},
			"memory":     map[string]any{},
			"viewport":   map[string]any{"width": float64(1024), "height": float64(768), "device_pixel_ratio": float64(1)},
		}, nil
	case mcpInternalProbeExpression:
		if p.probePayload != nil {
			return p.probePayload, nil
		}
		return map[string]any{"ok": true}, nil
	case viewportMetricsExpression:
		return p.viewport, nil
	case fullPageMetricsExpression:
		return p.pageMetrics, nil
	case selectorMetricsExpression:
		return p.selectorMetrics, nil
	}
	return nil, fmt.Errorf("unexpected internal evaluation expression %q", expression)
}

func (p *fakeMCPPage) AddInitScript(_ context.Context, script string) error {
	if p.initErr != nil {
		return p.initErr
	}
	p.initScripts = append(p.initScripts, script)
	return nil
}

func isMCPInternalExpression(expression string) bool {
	switch expression {
	case boundedContentExpression, snapshotExpression, mcpFetchExpression, mcpInternalProbeExpression, viewportMetricsExpression, fullPageMetricsExpression, selectorMetricsExpression:
		return true
	default:
		return false
	}
}

func contentField(include bool, value string) string {
	if !include {
		return ""
	}
	return value
}

func (p *fakeMCPPage) WaitForLoadState(context.Context, string) error { return nil }
func (p *fakeMCPPage) WaitForSelector(context.Context, string, ...gomoufox.WaitForSelectorOption) (*gomoufox.ElementHandle, error) {
	return nil, nil
}
func (p *fakeMCPPage) WaitForURL(context.Context, string, ...gomoufox.GotoOption) error {
	return nil
}
func (p *fakeMCPPage) Screenshot(context.Context, ...gomoufox.ScreenshotOption) ([]byte, error) {
	p.screenshotCalls++
	if p.screenshotErr != nil {
		return nil, p.screenshotErr
	}
	return p.screenshotData, nil
}
func (p *fakeMCPPage) Locator(selector string) gomoufox.Locator {
	p.locatorCalls++
	p.locator.selector = selector
	return &p.locator
}
func (p *fakeMCPPage) Wheel(_ context.Context, deltaX, deltaY float64) error {
	p.wheelX = deltaX
	p.wheelY = deltaY
	return p.wheelErr
}
func (p *fakeMCPPage) FetchBytes(context.Context, string, string, map[string]string, []byte) (int, []byte, error) {
	return 200, []byte("ok"), nil
}
func (p *fakeMCPPage) OnRequest(fn func(*gomoufox.Request))       { p.onRequest = fn }
func (p *fakeMCPPage) OnRequestFailed(fn func(*gomoufox.Request)) { p.onRequestFailed = fn }
func (p *fakeMCPPage) OnResponse(fn func(*gomoufox.Response))     { p.onResponse = fn }
func (p *fakeMCPPage) OnPageError(fn func(error))                 { p.onPageError = fn }
func (p *fakeMCPPage) OnConsole(fn func(gomoufox.ConsoleMessage)) { p.onConsole = fn }
func (p *fakeMCPPage) OnDialog(fn func(gomoufox.Dialog))          { p.onDialog = fn }
func (p *fakeMCPPage) Close() error {
	p.closeCalls++
	return nil
}

type fakeMCPLocator struct {
	selector        string
	clicks          int
	clickErr        error
	currentValue    string
	fillValue       string
	fillErr         error
	typeValue       string
	typeErr         error
	pressKey        string
	pressErr        error
	hoverCalls      int
	hoverOptCount   int
	hoverErr        error
	scrollCalls     int
	scrollOptCount  int
	scrollErr       error
	selectOptCount  int
	selectResult    []string
	selectErr       error
	checked         bool
	checkedOptCount int
	checkedErr      error
	inputFiles      []string
	inputOptCount   int
	inputErr        error
	typeOptCount    int
	pressOptCount   int
	typingOps       []string
	innerErr        error
	textErr         error
	screenshotCalls int
}

func (l *fakeMCPLocator) Click(context.Context, ...gomoufox.LocatorClickOption) error {
	l.clicks++
	return l.clickErr
}
func (l *fakeMCPLocator) Fill(_ context.Context, value string, opts ...gomoufox.LocatorFillOption) error {
	if l.fillErr != nil {
		return l.fillErr
	}
	l.currentValue = value
	l.fillValue = value
	l.typingOps = append(l.typingOps, "fill:"+value)
	return nil
}
func (l *fakeMCPLocator) Type(_ context.Context, value string, opts ...gomoufox.LocatorTypeOption) error {
	if l.typeErr != nil {
		return l.typeErr
	}
	l.currentValue += value
	l.typeValue = value
	l.typeOptCount = len(opts)
	l.typingOps = append(l.typingOps, "type:"+value)
	return nil
}
func (l *fakeMCPLocator) Press(_ context.Context, key string, opts ...gomoufox.LocatorPressOption) error {
	if l.pressErr != nil {
		return l.pressErr
	}
	l.pressKey = key
	l.pressOptCount = len(opts)
	l.typingOps = append(l.typingOps, "press:"+key)
	return nil
}
func (l *fakeMCPLocator) Hover(_ context.Context, opts ...gomoufox.LocatorHoverOption) error {
	if l.hoverErr != nil {
		return l.hoverErr
	}
	l.hoverCalls++
	l.hoverOptCount = len(opts)
	return nil
}
func (l *fakeMCPLocator) ScrollIntoViewIfNeeded(_ context.Context, opts ...gomoufox.LocatorOption) error {
	if l.scrollErr != nil {
		return l.scrollErr
	}
	l.scrollCalls++
	l.scrollOptCount = len(opts)
	return nil
}
func (l *fakeMCPLocator) SelectOption(_ context.Context, opts ...gomoufox.LocatorSelectOption) ([]string, error) {
	if l.selectErr != nil {
		return nil, l.selectErr
	}
	l.selectOptCount = len(opts)
	return l.selectResult, nil
}
func (l *fakeMCPLocator) SetChecked(_ context.Context, checked bool, opts ...gomoufox.LocatorSetCheckedOption) error {
	if l.checkedErr != nil {
		return l.checkedErr
	}
	l.checked = checked
	l.checkedOptCount = len(opts)
	return nil
}
func (l *fakeMCPLocator) SetInputFiles(_ context.Context, files []string, opts ...gomoufox.LocatorSetInputFilesOption) error {
	if l.inputErr != nil {
		return l.inputErr
	}
	l.inputFiles = append([]string(nil), files...)
	l.inputOptCount = len(opts)
	return nil
}
func (l *fakeMCPLocator) resetTyping() {
	l.currentValue = ""
	l.fillValue = ""
	l.typeValue = ""
	l.pressKey = ""
	l.typeOptCount = 0
	l.pressOptCount = 0
	l.typingOps = nil
}
func (l *fakeMCPLocator) TextContent(context.Context, ...gomoufox.LocatorTextContentOption) (string, error) {
	if l.textErr != nil {
		return "", l.textErr
	}
	return "locator text", nil
}
func (l *fakeMCPLocator) InnerHTML(context.Context, ...gomoufox.LocatorOption) (string, error) {
	if l.innerErr != nil {
		return "", l.innerErr
	}
	return "<button>Sign in</button>", nil
}
func (l *fakeMCPLocator) GetAttribute(context.Context, string, ...gomoufox.LocatorOption) (string, error) {
	return "", nil
}
func (l *fakeMCPLocator) IsVisible(context.Context, ...gomoufox.LocatorOption) (bool, error) {
	return true, nil
}
func (l *fakeMCPLocator) Count(context.Context) (int, error) { return 1, nil }
func (l *fakeMCPLocator) First() gomoufox.Locator            { return l }
func (l *fakeMCPLocator) Last() gomoufox.Locator             { return l }
func (l *fakeMCPLocator) Nth(int) gomoufox.Locator           { return l }
func (l *fakeMCPLocator) WaitFor(context.Context, ...gomoufox.LocatorWaitForOption) error {
	return nil
}
func (l *fakeMCPLocator) Screenshot(context.Context, ...gomoufox.ScreenshotOption) ([]byte, error) {
	l.screenshotCalls++
	return []byte("locator-shot"), nil
}

func setField(target any, name string, value any) {
	field := reflect.ValueOf(target).Elem().FieldByName(name)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

type fakePWBrowser struct {
	context *fakePWContext
	page    *fakePWPage
	closed  bool
	ctxErr  error
}

func (b *fakePWBrowser) Close() error          { b.closed = true; return nil }
func (b *fakePWBrowser) IsConnected() bool     { return !b.closed }
func (b *fakePWBrowser) OnDisconnected(func()) {}
func (b *fakePWBrowser) Contexts() []pwbridge.BrowserContext {
	return []pwbridge.BrowserContext{b.context}
}
func (b *fakePWBrowser) NewContext(pwbridge.ContextOptions) (pwbridge.BrowserContext, error) {
	if b.ctxErr != nil {
		return nil, b.ctxErr
	}
	return b.context, nil
}
func (b *fakePWBrowser) NewPage(pwbridge.ContextOptions) (pwbridge.Page, error) {
	return b.page, nil
}
func (b *fakePWBrowser) Version() string { return "fake" }

type fakePWContext struct {
	page    *fakePWPage
	cookies []pwbridge.Cookie
	storage *pwbridge.StorageState
	pageErr error
}

func (c *fakePWContext) NewPage() (pwbridge.Page, error) {
	if c.pageErr != nil {
		return nil, c.pageErr
	}
	return c.page, nil
}
func (c *fakePWContext) Pages() []pwbridge.Page                        { return []pwbridge.Page{c.page} }
func (c *fakePWContext) Cookies(...string) ([]pwbridge.Cookie, error)  { return c.cookies, nil }
func (c *fakePWContext) AddCookies(...pwbridge.Cookie) error           { return nil }
func (c *fakePWContext) ClearCookies() error                           { return nil }
func (c *fakePWContext) StorageState() (*pwbridge.StorageState, error) { return c.storage, nil }
func (c *fakePWContext) Route(string, pwbridge.RouteHandler) error     { return nil }
func (c *fakePWContext) Unroute(string, pwbridge.RouteHandler) error   { return nil }
func (c *fakePWContext) OnRequest(func(pwbridge.Request))              {}
func (c *fakePWContext) OnResponse(func(pwbridge.Response))            {}
func (c *fakePWContext) Close() error                                  { return nil }
func (c *fakePWContext) Raw() any                                      { return c }

type fakePWPage struct {
	title                 string
	url                   string
	content               string
	screenshot            []byte
	locator               *fakePWLocator
	initScript            string
	waitNavigationCalls   int
	waitNavigationOptions pwbridge.NavigateOptions
}

func (p *fakePWPage) Goto(string, pwbridge.GotoOptions) (pwbridge.Response, error) {
	return fakePWResponse{status: 200}, nil
}
func (p *fakePWPage) GoBack(pwbridge.NavigateOptions) (pwbridge.Response, error) {
	return fakePWResponse{status: 200}, nil
}
func (p *fakePWPage) GoForward(pwbridge.NavigateOptions) (pwbridge.Response, error) {
	return fakePWResponse{status: 200}, nil
}
func (p *fakePWPage) Reload(pwbridge.NavigateOptions) (pwbridge.Response, error) {
	return fakePWResponse{status: 200}, nil
}
func (p *fakePWPage) RunAndWaitForNavigation(action func() error, opts pwbridge.NavigateOptions) error {
	p.waitNavigationCalls++
	p.waitNavigationOptions = opts
	if err := action(); err != nil {
		return err
	}
	return nil
}
func (p *fakePWPage) Evaluate(expression string, arg ...any) (any, error) {
	if expression == mcpFetchExpression || (strings.Contains(expression, "fetch(url") && strings.Contains(expression, "maxBytes")) {
		return map[string]any{"ok": true, "url": "https://api.example.com", "status": float64(200), "headers": map[string]any{}, "body": "ok"}, nil
	}
	return map[string]any{"ok": true}, nil
}
func (p *fakePWPage) EvaluateInternal(expression string, arg ...any) (any, error) {
	return p.Evaluate(expression, arg...)
}
func (p *fakePWPage) AddInitScript(script string) error {
	p.initScript = script
	return nil
}
func (p *fakePWPage) Content() (string, error)                      { return p.content, nil }
func (p *fakePWPage) SetContent(string, pwbridge.GotoOptions) error { return nil }
func (p *fakePWPage) Title() (string, error)                        { return p.title, nil }
func (p *fakePWPage) URL() string                                   { return p.url }
func (p *fakePWPage) WaitForLoadState(string, time.Duration) error  { return nil }
func (p *fakePWPage) WaitForSelector(string, pwbridge.WaitForSelectorOptions) (pwbridge.ElementHandle, error) {
	return fakePWElement{}, nil
}
func (p *fakePWPage) WaitForURL(string, pwbridge.GotoOptions) error         { return nil }
func (p *fakePWPage) Screenshot(pwbridge.ScreenshotOptions) ([]byte, error) { return p.screenshot, nil }
func (p *fakePWPage) PDF(pwbridge.PDFOptions) ([]byte, error)               { return nil, nil }
func (p *fakePWPage) Cookies(...string) ([]pwbridge.Cookie, error)          { return nil, nil }
func (p *fakePWPage) Route(string, pwbridge.RouteHandler) error             { return nil }
func (p *fakePWPage) Unroute(string, pwbridge.RouteHandler) error           { return nil }
func (p *fakePWPage) OnRequest(func(pwbridge.Request))                      {}
func (p *fakePWPage) OnRequestFailed(func(pwbridge.Request))                {}
func (p *fakePWPage) OnResponse(func(pwbridge.Response))                    {}
func (p *fakePWPage) OnPageError(func(error))                               {}
func (p *fakePWPage) OnConsole(func(pwbridge.ConsoleMessage))               {}
func (p *fakePWPage) OnDialog(func(pwbridge.Dialog))                        {}
func (p *fakePWPage) Wheel(float64, float64) error                          { return nil }
func (p *fakePWPage) Locator(string) pwbridge.Locator                       { return p.locator }
func (p *fakePWPage) Close() error                                          { return nil }
func (p *fakePWPage) Raw() any                                              { return p }

type fakePWElement struct{}

func (fakePWElement) Raw() any { return nil }

func gomoufoxRequestForTest(rawURL, method string, headers map[string]string) *gomoufox.Request {
	request := &gomoufox.Request{}
	setField(request, "raw", fakeObservationRequest{url: rawURL, method: method, headers: headers})
	return request
}

func gomoufoxResponseForTest(rawURL string, status int, headers map[string]string) *gomoufox.Response {
	response := &gomoufox.Response{}
	setField(response, "raw", fakeObservationResponse{url: rawURL, status: status, headers: headers, request: fakeObservationRequest{url: rawURL, method: "GET"}})
	return response
}

type fakeObservationRequest struct {
	url     string
	method  string
	headers map[string]string
}

func (r fakeObservationRequest) URL() string                { return r.url }
func (r fakeObservationRequest) Method() string             { return r.method }
func (r fakeObservationRequest) Headers() map[string]string { return r.headers }
func (r fakeObservationRequest) PostData() string           { return "must-not-read" }
func (r fakeObservationRequest) PostDataBytes() []byte      { return []byte("must-not-read") }
func (r fakeObservationRequest) ResourceType() string       { return "document" }
func (r fakeObservationRequest) IsNavigationRequest() bool  { return true }

type fakeObservationResponse struct {
	url     string
	status  int
	headers map[string]string
	request pwbridge.Request
}

func (r fakeObservationResponse) URL() string                { return r.url }
func (r fakeObservationResponse) Status() int                { return r.status }
func (r fakeObservationResponse) StatusText() string         { return "OK" }
func (r fakeObservationResponse) Headers() map[string]string { return r.headers }
func (r fakeObservationResponse) Body() ([]byte, error)      { return []byte("must-not-read"), nil }
func (r fakeObservationResponse) Text() (string, error)      { return "must-not-read", nil }
func (r fakeObservationResponse) OK() bool                   { return r.status >= 200 && r.status < 300 }
func (r fakeObservationResponse) Request() pwbridge.Request  { return r.request }

type fakePWResponse struct{ status int }

func (r fakePWResponse) URL() string                { return "https://example.com" }
func (r fakePWResponse) Status() int                { return r.status }
func (r fakePWResponse) StatusText() string         { return "OK" }
func (r fakePWResponse) Headers() map[string]string { return map[string]string{} }
func (r fakePWResponse) Body() ([]byte, error)      { return []byte("ok"), nil }
func (r fakePWResponse) Text() (string, error)      { return "ok", nil }
func (r fakePWResponse) OK() bool                   { return r.status >= 200 && r.status < 300 }
func (r fakePWResponse) Request() pwbridge.Request  { return nil }

type fakeMCPDialog struct {
	typ          string
	message      string
	defaultValue string
	acceptText   string
	accepted     bool
	dismissed    bool
	acceptErr    error
	dismissErr   error
}

func (d *fakeMCPDialog) Type() string         { return d.typ }
func (d *fakeMCPDialog) Message() string      { return d.message }
func (d *fakeMCPDialog) DefaultValue() string { return d.defaultValue }
func (d *fakeMCPDialog) Accept(promptText ...string) error {
	d.accepted = true
	if len(promptText) > 0 {
		d.acceptText = promptText[0]
	}
	return d.acceptErr
}
func (d *fakeMCPDialog) Dismiss() error {
	d.dismissed = true
	return d.dismissErr
}

type fakePWLocator struct {
	clickOptions   pwbridge.LocatorClickOptions
	typeOptions    pwbridge.LocatorTypeOptions
	pressOptions   pwbridge.LocatorPressOptions
	hoverOptions   pwbridge.LocatorHoverOptions
	scrollOptions  pwbridge.LocatorOptions
	selectOptions  pwbridge.LocatorSelectOptions
	checked        bool
	checkedOptions pwbridge.LocatorSetCheckedOptions
	inputFiles     []string
	inputOptions   pwbridge.LocatorSetInputFilesOptions
}

func (l *fakePWLocator) Click(opts pwbridge.LocatorClickOptions) error {
	l.clickOptions = opts
	return nil
}
func (l *fakePWLocator) Fill(string, pwbridge.LocatorFillOptions) error { return nil }
func (l *fakePWLocator) Type(_ string, opts pwbridge.LocatorTypeOptions) error {
	l.typeOptions = opts
	return nil
}
func (l *fakePWLocator) Press(_ string, opts pwbridge.LocatorPressOptions) error {
	l.pressOptions = opts
	return nil
}
func (l *fakePWLocator) Hover(opts pwbridge.LocatorHoverOptions) error {
	l.hoverOptions = opts
	return nil
}
func (l *fakePWLocator) ScrollIntoViewIfNeeded(opts pwbridge.LocatorOptions) error {
	l.scrollOptions = opts
	return nil
}
func (l *fakePWLocator) SelectOption(opts pwbridge.LocatorSelectOptions) ([]string, error) {
	l.selectOptions = opts
	return []string{"selected"}, nil
}
func (l *fakePWLocator) SetChecked(checked bool, opts pwbridge.LocatorSetCheckedOptions) error {
	l.checked = checked
	l.checkedOptions = opts
	return nil
}
func (l *fakePWLocator) SetInputFiles(files []string, opts pwbridge.LocatorSetInputFilesOptions) error {
	l.inputFiles = append([]string(nil), files...)
	l.inputOptions = opts
	return nil
}
func (l *fakePWLocator) TextContent(pwbridge.LocatorOptions) (string, error) { return "text", nil }
func (l *fakePWLocator) InnerHTML(pwbridge.LocatorOptions) (string, error)   { return "<p>text</p>", nil }
func (l *fakePWLocator) GetAttribute(string, pwbridge.LocatorOptions) (string, error) {
	return "", nil
}
func (l *fakePWLocator) IsVisible(pwbridge.LocatorOptions) (bool, error) { return true, nil }
func (l *fakePWLocator) Count() (int, error)                             { return 1, nil }
func (l *fakePWLocator) First() pwbridge.Locator                         { return l }
func (l *fakePWLocator) Last() pwbridge.Locator                          { return l }
func (l *fakePWLocator) Nth(int) pwbridge.Locator                        { return l }
func (l *fakePWLocator) WaitFor(pwbridge.LocatorWaitForOptions) error    { return nil }
func (l *fakePWLocator) Screenshot(pwbridge.ScreenshotOptions) ([]byte, error) {
	return []byte("shot"), nil
}
