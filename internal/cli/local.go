package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"

	gomoufox "github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/camoufoxcfg"
	"github.com/ehmo/gomoufox/internal/content"
	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/safefile"
)

func defaultLocalCommand(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
	switch req.Command {
	case "get":
		return localGet(ctx, req)
	case "screenshot":
		return localScreenshot(ctx, req)
	case "eval":
		return localEval(ctx, req)
	case "fetch":
		return localFetch(ctx, req)
	case "open":
		return localOpen(ctx, req)
	case "session import":
		return localSessionImport(req)
	case "session export":
		return localSessionExport(ctx, req)
	default:
		return LocalCommandResponse{ExitCode: ExitRuntime}, fmt.Errorf("unsupported local command: %s", req.Command)
	}
}

var (
	openPageForLocal    = openRealPage
	openBrowserForLocal = openRealBrowser
	newGomoufoxForLocal = gomoufox.New
	newBrowserForLocal  = func(ctx context.Context, opts ...gomoufox.Option) (gomoufoxBrowserForLocal, error) {
		b, err := newGomoufoxForLocal(ctx, opts...)
		if err != nil {
			return nil, err
		}
		return gomoufoxBrowserAdapter{b: b}, nil
	}
	closeGomoufoxBrowserForLocal = (*gomoufox.Browser).Close
	extractContentForLocal       = content.Extract
	unmarshalStorageStateJSON    = json.Unmarshal
)

type localPage interface {
	Goto(context.Context, string, ...gomoufox.GotoOption) error
	WaitForSelector(context.Context, string) error
	Content(context.Context) (string, error)
	BodyText(context.Context) (string, error)
	Title(context.Context) (string, error)
	URL() string
	Screenshot(context.Context, ...gomoufox.ScreenshotOption) ([]byte, error)
	Evaluate(context.Context, string, ...any) (any, error)
	FetchBytes(context.Context, string, string, map[string]string, []byte) (int, []byte, error)
	FetchBytesWithOptions(context.Context, string, string, map[string]string, []byte, gomoufox.FetchBytesOptions) (gomoufox.FetchBytesResult, error)
	WaitClosed(context.Context) error
	StorageState(context.Context) (*gomoufox.StorageState, error)
	Close() error
}

type localStorageBrowser interface {
	StorageState(context.Context, string) (*gomoufox.StorageState, error)
	Close() error
}

type gomoufoxBrowserForLocal interface {
	NewPage(context.Context, ...gomoufox.ContextOption) (gomoufoxPageForLocal, error)
	NewContext(context.Context, ...gomoufox.ContextOption) (gomoufoxStorageContextForLocal, error)
	Close() error
}

type gomoufoxPageForLocal interface {
	Goto(context.Context, string, ...gomoufox.GotoOption) (*gomoufox.Response, error)
	WaitForSelector(context.Context, string, ...gomoufox.WaitForSelectorOption) (*gomoufox.ElementHandle, error)
	Content(context.Context) (string, error)
	Locator(string) gomoufox.Locator
	Title(context.Context) (string, error)
	URL() string
	Screenshot(context.Context, ...gomoufox.ScreenshotOption) ([]byte, error)
	Evaluate(context.Context, string, ...any) (any, error)
	FetchBytes(context.Context, string, string, map[string]string, []byte) (int, []byte, error)
	FetchBytesWithOptions(context.Context, string, string, map[string]string, []byte, gomoufox.FetchBytesOptions) (gomoufox.FetchBytesResult, error)
	Raw() any
	Close() error
}

type gomoufoxStorageContextForLocal interface {
	StorageState(context.Context, string) (*gomoufox.StorageState, error)
	Close() error
}

type gomoufoxBrowserAdapter struct {
	b *gomoufox.Browser
}

func (b gomoufoxBrowserAdapter) NewPage(ctx context.Context, opts ...gomoufox.ContextOption) (gomoufoxPageForLocal, error) {
	return b.b.NewPage(ctx, opts...)
}

func (b gomoufoxBrowserAdapter) NewContext(ctx context.Context, opts ...gomoufox.ContextOption) (gomoufoxStorageContextForLocal, error) {
	return b.b.NewContext(ctx, opts...)
}

func (b gomoufoxBrowserAdapter) Close() error { return closeGomoufoxBrowserForLocal(b.b) }

func localGet(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
	p, closeAll, err := openPageForLocal(ctx, req, nil)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	defer closeAll()
	target := req.Args[0]
	if err := p.Goto(ctx, target, gomoufox.WaitUntil(flagString(req, "wait_load_state", "domcontentloaded")), gomoufox.WithTimeout(flagDuration(req, "timeout", 30*time.Second))); err != nil {
		return LocalCommandResponse{}, err
	}
	if selector := flagString(req, "wait_selector", ""); selector != "" {
		if err := p.WaitForSelector(ctx, selector); err != nil {
			return LocalCommandResponse{}, fmt.Errorf("%w: %v", gomoufox.ErrElementNotFound, err)
		}
	}
	html, err := p.Content(ctx)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	body, _ := p.BodyText(ctx)
	title, _ := p.Title(ctx)
	format := content.FormatMarkdown
	if localBool(req, "html") {
		format = content.FormatHTML
	} else if localBool(req, "text") {
		format = content.FormatText
	}
	extracted, err := extractContentForLocal(html, body, p.URL(), format, flagInt(req, "max_bytes", 512*1024))
	if err != nil {
		return LocalCommandResponse{}, err
	}
	if req.JSON {
		return jsonResponse(struct {
			URL             string         `json:"url"`
			FinalURL        string         `json:"final_url"`
			Title           string         `json:"title"`
			Content         string         `json:"content"`
			Format          content.Format `json:"format"`
			MarkdownQuality string         `json:"markdown_quality,omitempty"`
			Bytes           int            `json:"bytes"`
			Truncated       bool           `json:"truncated"`
		}{
			URL: target, FinalURL: p.URL(), Title: title, Content: extracted.Content,
			Format: extracted.Format, MarkdownQuality: extracted.MarkdownQuality,
			Bytes: extracted.Bytes, Truncated: extracted.Truncated,
		})
	}
	return LocalCommandResponse{ExitCode: ExitOK, Stdout: []byte(extracted.Content)}, nil
}

func localScreenshot(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
	width := flagInt(req, "width", 1280)
	height := flagInt(req, "height", 800)
	p, closeAll, err := openPageForLocal(ctx, req, []gomoufox.ContextOption{gomoufox.WithViewport(width, height)})
	if err != nil {
		return LocalCommandResponse{}, err
	}
	defer closeAll()
	if err := p.Goto(ctx, req.Args[0], gomoufox.WaitUntil(flagString(req, "wait_load_state", "load")), gomoufox.WithTimeout(flagDuration(req, "timeout", 30*time.Second))); err != nil {
		return LocalCommandResponse{}, err
	}
	if selector := flagString(req, "wait_selector", ""); selector != "" {
		if err := p.WaitForSelector(ctx, selector); err != nil {
			return LocalCommandResponse{}, fmt.Errorf("%w: %v", gomoufox.ErrElementNotFound, err)
		}
	}
	opts := []gomoufox.ScreenshotOption{gomoufox.FullPage(localBool(req, "full_page"))}
	if out := flagString(req, "out", ""); strings.HasSuffix(strings.ToLower(out), ".jpg") || strings.HasSuffix(strings.ToLower(out), ".jpeg") {
		opts = append(opts, gomoufox.ScreenshotType("jpeg"))
	}
	if quality := flagInt(req, "quality", 0); quality > 0 {
		opts = append(opts, gomoufox.JPEGQuality(quality))
	}
	if clip := flagString(req, "clip", ""); clip != "" {
		parsed, err := parseClip(clip)
		if err != nil {
			return LocalCommandResponse{}, err
		}
		opts = append(opts, gomoufox.Clip(parsed[0], parsed[1], parsed[2], parsed[3]))
	}
	data, err := p.Screenshot(ctx, opts...)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	if capBytes := flagInt(req, "max_bytes", 0); capBytes > 0 && len(data) > capBytes {
		return LocalCommandResponse{}, fmt.Errorf("screenshot exceeds max bytes: %d > %d", len(data), capBytes)
	}
	if out := flagString(req, "out", ""); out != "" {
		if err := writeFile0600(out, data, true); err != nil {
			return LocalCommandResponse{}, err
		}
		if req.JSON {
			return jsonResponse(map[string]any{"path": out, "width": width, "height": height, "bytes": len(data), "format": screenshotFormat(out)})
		}
		return LocalCommandResponse{ExitCode: ExitOK, Stdout: []byte(out + "\n")}, nil
	}
	return LocalCommandResponse{ExitCode: ExitOK, Stdout: data}, nil
}

func localEval(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
	p, closeAll, err := openPageForLocal(ctx, req, nil)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	defer closeAll()
	if err := p.Goto(ctx, req.Args[0], gomoufox.WaitUntil(flagString(req, "wait_load_state", "domcontentloaded")), gomoufox.WithTimeout(flagDuration(req, "timeout", 30*time.Second))); err != nil {
		return LocalCommandResponse{}, err
	}
	if selector := flagString(req, "wait_selector", ""); selector != "" {
		if err := p.WaitForSelector(ctx, selector); err != nil {
			return LocalCommandResponse{}, fmt.Errorf("%w: %v", gomoufox.ErrElementNotFound, err)
		}
	}
	script, err := scriptFromFlags(req)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	var args []any
	if rawArg := flagString(req, "arg", ""); rawArg != "" {
		var arg any
		if err := json.Unmarshal([]byte(rawArg), &arg); err != nil {
			return LocalCommandResponse{}, err
		}
		args = append(args, arg)
	}
	result, err := p.Evaluate(ctx, script, args...)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	return jsonResponse(result)
}

func localFetch(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
	p, closeAll, err := openPageForLocal(ctx, req, nil)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	defer closeAll()
	if nav := flagString(req, "navigate_first", ""); nav != "" {
		if err := p.Goto(ctx, nav, gomoufox.WithTimeout(flagDuration(req, "timeout", 30*time.Second))); err != nil {
			return LocalCommandResponse{}, err
		}
	}
	body, err := bodyFromFlags(req)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	headers := headersFromFlags(req)
	if ct := flagString(req, "content_type", ""); ct != "" {
		headers["Content-Type"] = ct
	} else if len(body) > 0 && json.Valid(body) && headers["Content-Type"] == "" {
		headers["Content-Type"] = "application/json"
	}
	method := strings.ToUpper(flagString(req, "method", "GET"))
	capBytes := flagInt(req, "max_bytes", 512*1024)
	result, err := p.FetchBytesWithOptions(ctx, req.Args[0], method, headers, body, gomoufox.FetchBytesOptions{MaxBytes: capBytes})
	if err != nil {
		return LocalCommandResponse{}, err
	}
	status := result.StatusCode
	data := result.Body
	bodyBytes := len(data)
	if result.Truncated {
		data = append(data, []byte("\n<!-- truncated -->")...)
	}
	if !req.JSON {
		return LocalCommandResponse{ExitCode: ExitOK, Stdout: data}, nil
	}
	var payload any = string(data)
	if !localBool(req, "raw") && json.Valid(data) {
		_ = json.Unmarshal(data, &payload)
	}
	return jsonResponse(map[string]any{"url": req.Args[0], "status": status, "body": payload, "bytes": bodyBytes, "truncated": result.Truncated})
}

func localOpen(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
	p, closeAll, err := openPageForLocal(ctx, req, nil)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	defer closeAll()
	if err := p.Goto(ctx, req.Args[0], gomoufox.WithTimeout(flagDuration(req, "timeout", 30*time.Second))); err != nil {
		return LocalCommandResponse{}, err
	}
	stdout := []byte(p.URL() + "\n")
	if err := p.WaitClosed(ctx); err != nil {
		return LocalCommandResponse{}, err
	}
	if out := flagString(req, "save_session", ""); out != "" {
		state, err := p.StorageState(ctx)
		if err != nil {
			return LocalCommandResponse{}, err
		}
		if err := writeFile0600(out, mustJSON(state), true); err != nil {
			return LocalCommandResponse{}, err
		}
		stdout = append(stdout, []byte(out+"\n")...)
	}
	return LocalCommandResponse{ExitCode: ExitOK, Stdout: stdout}, nil
}

func localSessionImport(req LocalCommandRequest) (LocalCommandResponse, error) {
	src := flagString(req, "file", "")
	dst := flagString(req, "out", "")
	displayDst := dst
	if req.DisplayOut != "" {
		displayDst = req.DisplayOut
	}
	data, err := readCappedFileBytes(src, policy.InlineSessionLoadBytes)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	var state gomoufox.StorageState
	if err := json.Unmarshal(data, &state); err != nil {
		return LocalCommandResponse{}, err
	}
	if err := writeSessionStateFile(dst, &state, localBool(req, "overwrite")); err != nil {
		return LocalCommandResponse{}, err
	}
	if req.JSON {
		return jsonResponse(map[string]any{"cookies": len(state.Cookies), "origins": len(state.Origins), "path": displayDst})
	}
	return LocalCommandResponse{ExitCode: ExitOK, Stdout: []byte(fmt.Sprintf("Imported %d cookies, %d origins into %s\n", len(state.Cookies), len(state.Origins), displayDst))}, nil
}

func localSessionExport(ctx context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
	out := flagString(req, "out", "")
	displayOut := out
	if req.DisplayOut != "" {
		displayOut = req.DisplayOut
	}
	profile := flagString(req, "from_profile", req.Profile)
	if profile == "" {
		return LocalCommandResponse{}, errors.New("session export requires --from-profile or --profile")
	}
	req.Profile = profile
	b, closeAll, err := openBrowserForLocal(ctx, req)
	if err != nil {
		return LocalCommandResponse{}, err
	}
	defer closeAll()
	state, err := b.StorageState(ctx, "")
	if err != nil {
		return LocalCommandResponse{}, err
	}
	if err := writeSessionStateFile(out, state, false); err != nil {
		return LocalCommandResponse{}, err
	}
	if state == nil {
		state = &gomoufox.StorageState{}
	}
	if req.JSON {
		return jsonResponse(map[string]any{"path": displayOut, "cookies": len(state.Cookies), "origins": len(state.Origins)})
	}
	return LocalCommandResponse{ExitCode: ExitOK, Stdout: []byte(displayOut + "\n")}, nil
}

func openRealPage(ctx context.Context, req LocalCommandRequest, ctxOpts []gomoufox.ContextOption) (localPage, func(), error) {
	opts, err := browserOptions(req)
	if err != nil {
		return nil, nil, err
	}
	b, err := newBrowserForLocal(ctx, opts...)
	if err != nil {
		return nil, nil, err
	}
	ctxOpts, err = contextOptionsForLocal(req, ctxOpts)
	if err != nil {
		_ = b.Close()
		return nil, nil, err
	}
	p, err := b.NewPage(ctx, ctxOpts...)
	if err != nil {
		_ = b.Close()
		return nil, nil, err
	}
	closeAll := func() {
		_ = p.Close()
		_ = b.Close()
	}
	return realLocalPage{p: p}, closeAll, nil
}

func openRealBrowser(ctx context.Context, req LocalCommandRequest) (localStorageBrowser, func(), error) {
	opts, err := browserOptions(req)
	if err != nil {
		return nil, nil, err
	}
	b, err := newBrowserForLocal(ctx, opts...)
	if err != nil {
		return nil, nil, err
	}
	return realLocalBrowser{b: b}, func() { _ = b.Close() }, nil
}

func contextOptionsForLocal(req LocalCommandRequest, base []gomoufox.ContextOption) ([]gomoufox.ContextOption, error) {
	opts := append([]gomoufox.ContextOption(nil), base...)
	if path := flagString(req, "cookies_file", ""); path != "" {
		state, err := readStorageStateFile(path)
		if err != nil {
			return nil, err
		}
		opts = append(opts, gomoufox.WithStorageState(state))
	}
	return opts, nil
}

func readStorageStateFile(path string) (*gomoufox.StorageState, error) {
	data, err := readCappedFileBytes(path, policy.InlineSessionLoadBytes)
	if err != nil {
		return nil, err
	}
	var state gomoufox.StorageState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

type realLocalPage struct {
	p gomoufoxPageForLocal
}

func (p realLocalPage) Goto(ctx context.Context, url string, opts ...gomoufox.GotoOption) error {
	_, err := p.p.Goto(ctx, url, opts...)
	return err
}
func (p realLocalPage) WaitForSelector(ctx context.Context, selector string) error {
	_, err := p.p.WaitForSelector(ctx, selector)
	return err
}
func (p realLocalPage) Content(ctx context.Context) (string, error) { return p.p.Content(ctx) }
func (p realLocalPage) BodyText(ctx context.Context) (string, error) {
	return p.p.Locator("body").TextContent(ctx)
}
func (p realLocalPage) Title(ctx context.Context) (string, error) { return p.p.Title(ctx) }
func (p realLocalPage) URL() string                               { return p.p.URL() }
func (p realLocalPage) Screenshot(ctx context.Context, opts ...gomoufox.ScreenshotOption) ([]byte, error) {
	return p.p.Screenshot(ctx, opts...)
}
func (p realLocalPage) Evaluate(ctx context.Context, script string, args ...any) (any, error) {
	return p.p.Evaluate(ctx, script, args...)
}
func (p realLocalPage) FetchBytes(ctx context.Context, url, method string, headers map[string]string, body []byte) (int, []byte, error) {
	return p.p.FetchBytes(ctx, url, method, headers, body)
}
func (p realLocalPage) FetchBytesWithOptions(ctx context.Context, url, method string, headers map[string]string, body []byte, opts gomoufox.FetchBytesOptions) (gomoufox.FetchBytesResult, error) {
	return p.p.FetchBytesWithOptions(ctx, url, method, headers, body, opts)
}
func (p realLocalPage) WaitClosed(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, ok := p.p.Raw().(interface {
		WaitForEvent(string, ...playwright.PageWaitForEventOptions) (any, error)
	})
	if !ok {
		return errors.New("page close wait is not supported")
	}
	done := make(chan error, 1)
	go func() {
		_, err := raw.WaitForEvent("close", playwright.PageWaitForEventOptions{Timeout: playwright.Float(0)})
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p realLocalPage) StorageState(ctx context.Context) (*gomoufox.StorageState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, ok := p.p.Raw().(interface {
		Context() playwright.BrowserContext
	})
	if !ok {
		return nil, errors.New("page storage state is not supported")
	}
	state, err := raw.Context().StorageState()
	if err != nil {
		return nil, err
	}
	return storageStateFromPlaywright(state)
}
func (p realLocalPage) Close() error { return p.p.Close() }

type realLocalBrowser struct {
	b gomoufoxBrowserForLocal
}

func (b realLocalBrowser) StorageState(ctx context.Context, path string) (*gomoufox.StorageState, error) {
	c, err := b.b.NewContext(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.StorageState(ctx, path)
}
func (b realLocalBrowser) Close() error { return b.b.Close() }

func browserOptions(req LocalCommandRequest) ([]gomoufox.Option, error) {
	opts := []gomoufox.Option{}
	if req.Profile != "" {
		opts = append(opts, gomoufox.WithPersistentContext(req.Profile))
	}
	if req.Command == "open" || localBool(req, "headful") {
		opts = append(opts, gomoufox.WithHeadless(camoufoxcfg.HeadlessFalse))
	} else if raw, ok := req.Flags["headless"].(bool); ok {
		if raw {
			opts = append(opts, gomoufox.WithHeadless(camoufoxcfg.HeadlessTrue))
		} else {
			opts = append(opts, gomoufox.WithHeadless(camoufoxcfg.HeadlessFalse))
		}
	}
	if proxy := flagString(req, "proxy", ""); proxy != "" {
		cfg, err := proxyConfig(proxy)
		if err != nil {
			return nil, err
		}
		opts = append(opts, gomoufox.WithProxy(cfg))
	}
	if len(req.AllowedOrigins) > 0 {
		opts = append(opts, gomoufox.WithAllowedOrigins(req.AllowedOrigins...))
	}
	if len(req.AllowedHosts) > 0 {
		opts = append(opts, gomoufox.WithAllowedHosts(req.AllowedHosts...))
	}
	if locale := flagString(req, "locale", ""); locale != "" {
		opts = append(opts, gomoufox.WithLocale(locale))
	}
	if osName := flagString(req, "os", ""); osName != "" {
		opts = append(opts, gomoufox.WithOS(camoufoxcfg.OS(osName)))
	}
	enabled, duration, err := humanizeForLocal(req)
	if err != nil {
		return nil, err
	}
	if enabled {
		opts = append(opts, gomoufox.WithHumanize(duration))
	}
	return opts, nil
}

func humanizeForLocal(req LocalCommandRequest) (bool, time.Duration, error) {
	value, ok := req.Flags["humanize"]
	if !ok {
		return req.Command == "open", 0, nil
	}
	raw, _ := value.(string)
	switch strings.ToLower(raw) {
	case "", "1", "true", "yes", "on":
		return true, 0, nil
	case "0", "false", "no", "off":
		return false, 0, nil
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || seconds < 0 {
		return false, 0, errors.New("--humanize must be true, false, or non-negative seconds")
	}
	return true, time.Duration(seconds * float64(time.Second)), nil
}

func storageStateFromPlaywright(state *playwright.StorageState) (*gomoufox.StorageState, error) {
	if state == nil {
		return &gomoufox.StorageState{}, nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var out gomoufox.StorageState
	if err := unmarshalStorageStateJSON(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
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

func scriptFromFlags(req LocalCommandRequest) (string, error) {
	if script := flagString(req, "script", ""); script != "" {
		return script, nil
	}
	return readCappedFile(flagString(req, "script_file", ""), 64*1024)
}

func bodyFromFlags(req LocalCommandRequest) ([]byte, error) {
	if body := flagString(req, "data", ""); body != "" {
		return []byte(body), nil
	}
	if path := flagString(req, "data_file", ""); path != "" {
		return readCappedFileBytes(path, 1024*1024)
	}
	return nil, nil
}

func headersFromFlags(req LocalCommandRequest) map[string]string {
	out := map[string]string{}
	for _, raw := range flagStringList(req, "header") {
		key, value, ok := strings.Cut(raw, ":")
		if ok {
			out[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return out
}

func flagString(req LocalCommandRequest, name, fallback string) string {
	if value, ok := req.Flags[name]; ok {
		switch v := value.(type) {
		case string:
			if v != "" {
				return v
			}
		case []string:
			if len(v) > 0 && v[len(v)-1] != "" {
				return v[len(v)-1]
			}
		}
	}
	return fallback
}

func flagStringList(req LocalCommandRequest, name string) []string {
	if value, ok := req.Flags[name]; ok {
		switch v := value.(type) {
		case string:
			return []string{v}
		case []string:
			return append([]string{}, v...)
		}
	}
	return nil
}

func localBool(req LocalCommandRequest, name string) bool {
	value, _ := req.Flags[name].(bool)
	return value
}

func flagInt(req LocalCommandRequest, name string, fallback int) int {
	raw := flagString(req, name, "")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func flagDuration(req LocalCommandRequest, name string, fallback time.Duration) time.Duration {
	raw := flagString(req, name, "")
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func jsonResponse(value any) (LocalCommandResponse, error) {
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(value); err != nil {
		return LocalCommandResponse{}, err
	}
	return LocalCommandResponse{ExitCode: ExitOK, Stdout: b.Bytes()}, nil
}

func parseClip(raw string) ([4]float64, error) {
	var out [4]float64
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return out, errors.New("--clip must be x,y,width,height")
	}
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return out, err
		}
		out[i] = value
	}
	return out, nil
}

func screenshotFormat(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "jpeg"
	default:
		return "png"
	}
}

func readCappedFile(path string, cap int) (string, error) {
	data, err := readCappedFileBytes(path, cap)
	return string(data), err
}

func readCappedFileBytes(path string, cap int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if cap > 0 && len(data) > cap {
		return nil, fmt.Errorf("file exceeds %d bytes", cap)
	}
	return data, nil
}

func mustJSON(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return data
}

func writeFile0600(path string, data []byte, overwrite bool) error {
	return safefile.WriteFile0600(path, data, overwrite)
}

func writeSessionStateFile(path string, state *gomoufox.StorageState, overwrite bool) error {
	if state == nil {
		state = &gomoufox.StorageState{}
	}
	data := mustJSON(state)
	if len(data) > policy.InlineSessionLoadBytes {
		return fmt.Errorf("state exceeds %d bytes", policy.InlineSessionLoadBytes)
	}
	return writeFile0600(path, data, overwrite)
}
