package pwbridge

import (
	"math"
	"testing"
	"time"
)

func TestContextAndNewPageOptionConversion(t *testing.T) {
	state := &StorageState{
		Cookies: []Cookie{{Name: "sid", Value: "v", Domain: ".example.com", Path: "/", Secure: true, SameSite: "Lax"}},
		Origins: []Origin{{Origin: "https://example.com", LocalStorage: []LSEntry{{
			Name: "k", Value: "v",
		}}}},
	}
	opts := ContextOptions{
		Viewport:         &Viewport{Width: 1200, Height: 800},
		StorageState:     state,
		Proxy:            &Proxy{Server: "http://proxy.example:8080", Username: "u", Password: "p"},
		Locale:           "fr-FR",
		TimezoneID:       "Europe/Paris",
		ExtraHTTPHeaders: map[string]string{"x-test": "1"},
		HTTPCredentials:  &HTTPCredentials{Username: "user", Password: "pass"},
	}
	pw := toBrowserContextOptions(opts)
	if pw.Viewport == nil || pw.Viewport.Width != 1200 || pw.Viewport.Height != 800 {
		t.Fatalf("viewport = %#v", pw.Viewport)
	}
	if pw.Locale == nil || *pw.Locale != "fr-FR" || pw.TimezoneId == nil || *pw.TimezoneId != "Europe/Paris" {
		t.Fatalf("locale/timezone = %#v/%#v", pw.Locale, pw.TimezoneId)
	}
	if pw.ExtraHttpHeaders["x-test"] != "1" || pw.HttpCredentials.Username != "user" {
		t.Fatalf("headers/creds = %#v/%#v", pw.ExtraHttpHeaders, pw.HttpCredentials)
	}
	if pw.Proxy == nil || pw.Proxy.Server != "http://proxy.example:8080" || *pw.Proxy.Username != "u" || *pw.Proxy.Password != "p" {
		t.Fatalf("proxy = %#v", pw.Proxy)
	}
	if pw.StorageState == nil {
		t.Fatalf("missing storage state")
	}
	page := toBrowserNewPageOptions(opts)
	if page.Locale == nil || *page.Locale != "fr-FR" {
		t.Fatalf("new page options = %#v", page)
	}
}

func TestStorageAndCookieConversion(t *testing.T) {
	state := &StorageState{Cookies: []Cookie{{Name: "a", Value: "b"}}, Origins: []Origin{{Origin: "https://x"}}}
	pwState := toPWStorageState(state)
	if pwState == nil {
		t.Fatalf("nil pw storage")
	}
	if got := fromPWStorageState(nil); len(got.Cookies) != 0 || len(got.Origins) != 0 {
		t.Fatalf("nil storage conversion = %#v", got)
	}
	cookies := toPWOptionalCookies([]Cookie{{Name: "sid", Value: "secret", Domain: ".example.com", Path: "/", Expires: 42, HTTPOnly: true, Secure: true, SameSite: "Strict"}})
	if len(cookies) != 1 || cookies[0].Name != "sid" || cookies[0].Domain == nil || *cookies[0].Domain != ".example.com" {
		t.Fatalf("cookies = %#v", cookies)
	}
	if cookies[0].SameSite == nil || string(*cookies[0].SameSite) != "Strict" {
		t.Fatalf("sameSite = %#v", cookies[0].SameSite)
	}
	if sameSiteString(nil) != "" {
		t.Fatalf("nil sameSite should be empty")
	}
}

func TestNavigationScreenshotPDFAndRouteOptions(t *testing.T) {
	timeout := 1500 * time.Millisecond
	gotoOpts := toPWGotoOptions(GotoOptions{WaitUntil: "domcontentloaded", Referer: "https://ref.example", Timeout: timeout})
	if gotoOpts.WaitUntil == nil || string(*gotoOpts.WaitUntil) != "domcontentloaded" || gotoOpts.Referer == nil || *gotoOpts.Timeout != 1500 {
		t.Fatalf("goto opts = %#v", gotoOpts)
	}
	if string(*toWaitUntil("")) != "load" || string(*toLoadState("")) != "load" || toWaitForSelectorState("") != nil {
		t.Fatalf("default wait conversions failed")
	}
	if toPWGoBackOptions(NavigateOptions{WaitUntil: "load", Timeout: timeout}).Timeout == nil {
		t.Fatalf("back timeout missing")
	}
	if toPWGoForwardOptions(NavigateOptions{WaitUntil: "load", Timeout: timeout}).Timeout == nil {
		t.Fatalf("forward timeout missing")
	}
	if toPWReloadOptions(NavigateOptions{WaitUntil: "load", Timeout: timeout}).Timeout == nil {
		t.Fatalf("reload timeout missing")
	}
	if toPWSetContentOptions(GotoOptions{Timeout: timeout}).Timeout == nil || toPWWaitForURLOptions(GotoOptions{Timeout: timeout}).Timeout == nil {
		t.Fatalf("set content / wait URL timeout missing")
	}

	shot := toPWScreenshotOptions(ScreenshotOptions{FullPage: true, Type: "jpeg", Quality: 80, Clip: &Rect{X: 1, Y: 2, Width: 3, Height: 4}})
	if shot.FullPage == nil || !*shot.FullPage || shot.Type == nil || string(*shot.Type) != "jpeg" || shot.Quality == nil || *shot.Quality != 80 {
		t.Fatalf("screenshot opts = %#v", shot)
	}
	if shot.Clip == nil || shot.Clip.Width != 3 {
		t.Fatalf("clip = %#v", shot.Clip)
	}
	locShot := toPWLocatorScreenshotOptions(ScreenshotOptions{Type: "png", Quality: 70})
	if locShot.Type == nil || locShot.Quality == nil {
		t.Fatalf("locator shot opts = %#v", locShot)
	}
	if toPWPDFOptions(PDFOptions{}).Format != nil || *toPWPDFOptions(PDFOptions{Format: "A4"}).Format != "A4" {
		t.Fatalf("pdf opts mismatch")
	}

	if toPWRouteContinueOptions(nil).URL != nil {
		t.Fatalf("nil continue should be empty")
	}
	if toPWRouteFulfillOptions(nil).Status != nil {
		t.Fatalf("nil fulfill should be empty")
	}
	cont := toPWRouteContinueOptions(&ContinueOptions{URL: "https://example.com", Method: "POST", Headers: map[string]string{"x": "y"}, PostData: []byte("body")})
	if cont.URL == nil || *cont.URL != "https://example.com" || cont.Headers["x"] != "y" || string(cont.PostData.([]byte)) != "body" {
		t.Fatalf("continue opts = %#v", cont)
	}
	fulfill := toPWRouteFulfillOptions(&FulfillOptions{Status: 201, ContentType: "text/plain", BodyString: "ignored", Body: []byte("body"), Path: "/tmp/x"})
	if fulfill.Status == nil || *fulfill.Status != 201 || fulfill.Body != "body" || fulfill.Path == nil {
		t.Fatalf("fulfill opts = %#v", fulfill)
	}
	fetch := toPWRouteFetchOptions(&FetchOptions{URL: "https://api.example", Method: "GET", Headers: map[string]string{"a": "b"}, PostData: []byte("q")})
	if fetch.URL == nil || fetch.Headers["a"] != "b" || string(fetch.PostData.([]byte)) != "q" {
		t.Fatalf("fetch opts = %#v", fetch)
	}
}

func TestPointerHelpersAndMustJSON(t *testing.T) {
	if timeoutPtr(0) != nil || *timeoutPtr(2 * time.Second) != 2000 {
		t.Fatalf("timeout ptr mismatch")
	}
	if stringPtr("") != nil || *stringPtr("x") != "x" {
		t.Fatalf("string ptr mismatch")
	}
	if boolPtr(false) != nil || *boolPtr(true) != true {
		t.Fatalf("bool ptr mismatch")
	}
	if got := mustJSON(map[string]int{"a": 1}); got != `{"a":1}` {
		t.Fatalf("mustJSON = %q", got)
	}
	if got := mustJSON(math.Inf(1)); got != "+Inf" {
		t.Fatalf("mustJSON fallback = %q", got)
	}
}
