package pwbridge

import (
	"context"
	"time"
)

type Connector interface {
	Connect(ctx context.Context, endpoint string, opts ConnectOptions) (Session, error)
}

type ConnectOptions struct {
	Timeout time.Duration
}

type Session interface {
	Browser() Browser
	Stop() error
}

type Browser interface {
	Close() error
	IsConnected() bool
	OnDisconnected(func())
	Contexts() []BrowserContext
	NewContext(ContextOptions) (BrowserContext, error)
	NewPage(ContextOptions) (Page, error)
	Version() string
}

type ContextOptions struct {
	Viewport         *Viewport
	StorageState     *StorageState
	Proxy            *Proxy
	Locale           string
	TimezoneID       string
	ExtraHTTPHeaders map[string]string
	HTTPCredentials  *HTTPCredentials
}

type Viewport struct {
	Width  int
	Height int
}

type Proxy struct {
	Server   string
	Username string
	Password string
}

type HTTPCredentials struct {
	Username string
	Password string
}

type StorageState struct {
	Cookies []Cookie `json:"cookies"`
	Origins []Origin `json:"origins"`
}

type Origin struct {
	Origin       string    `json:"origin"`
	LocalStorage []LSEntry `json:"localStorage"`
}

type LSEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"sameSite"`
}

type BrowserContext interface {
	NewPage() (Page, error)
	Pages() []Page
	Cookies(urls ...string) ([]Cookie, error)
	AddCookies(cookies ...Cookie) error
	ClearCookies() error
	StorageState() (*StorageState, error)
	Route(urlPattern string, handler RouteHandler) error
	Unroute(urlPattern string, handler RouteHandler) error
	OnRequest(func(Request))
	OnResponse(func(Response))
	Close() error
	Raw() any
}

type Page interface {
	Goto(url string, opts GotoOptions) (Response, error)
	GoBack(opts NavigateOptions) (Response, error)
	GoForward(opts NavigateOptions) (Response, error)
	Reload(opts NavigateOptions) (Response, error)
	RunAndWaitForNavigation(action func() error, opts NavigateOptions) error
	Evaluate(expression string, arg ...any) (any, error)
	EvaluateInternal(expression string, arg ...any) (any, error)
	AddInitScript(script string) error
	Content() (string, error)
	SetContent(html string, opts GotoOptions) error
	Title() (string, error)
	URL() string
	WaitForLoadState(state string, timeout time.Duration) error
	WaitForSelector(selector string, opts WaitForSelectorOptions) (ElementHandle, error)
	WaitForURL(urlPattern string, opts GotoOptions) error
	Screenshot(opts ScreenshotOptions) ([]byte, error)
	PDF(opts PDFOptions) ([]byte, error)
	Cookies(urls ...string) ([]Cookie, error)
	Route(urlPattern string, handler RouteHandler) error
	Unroute(urlPattern string, handler RouteHandler) error
	OnRequest(func(Request))
	OnRequestFailed(func(Request))
	OnResponse(func(Response))
	OnPageError(func(error))
	OnConsole(func(ConsoleMessage))
	OnDialog(func(Dialog))
	Wheel(deltaX, deltaY float64) error
	Locator(selector string) Locator
	Close() error
	Raw() any
}

type GotoOptions struct {
	WaitUntil string
	Referer   string
	Timeout   time.Duration
}

type NavigateOptions struct {
	WaitUntil string
	Timeout   time.Duration
}

type WaitForSelectorOptions struct {
	Timeout time.Duration
	State   string
}

type ScreenshotOptions struct {
	FullPage bool
	Type     string
	Quality  int
	Clip     *Rect
}

type Rect struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

type PDFOptions struct {
	Format string
}

type ElementHandle interface {
	Raw() any
}

type RouteHandler func(Route)

type Route interface {
	Request() Request
	Continue(*ContinueOptions) error
	Fulfill(*FulfillOptions) error
	Abort(errorCode string) error
	Fetch(*FetchOptions) (Response, error)
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

type Request interface {
	URL() string
	Method() string
	Headers() map[string]string
	PostData() string
	PostDataBytes() []byte
	ResourceType() string
	IsNavigationRequest() bool
}

type Response interface {
	URL() string
	Status() int
	StatusText() string
	Headers() map[string]string
	Body() ([]byte, error)
	Text() (string, error)
	OK() bool
	Request() Request
}

type ConsoleMessage struct {
	Type string
	Text string
	Args []string
}

type Dialog interface {
	Type() string
	Message() string
	DefaultValue() string
	Accept(promptText ...string) error
	Dismiss() error
}

type Locator interface {
	Click(LocatorClickOptions) error
	Fill(value string, opts LocatorFillOptions) error
	Type(value string, opts LocatorTypeOptions) error
	Press(key string, opts LocatorPressOptions) error
	Hover(LocatorHoverOptions) error
	ScrollIntoViewIfNeeded(LocatorOptions) error
	SelectOption(LocatorSelectOptions) ([]string, error)
	SetChecked(checked bool, opts LocatorSetCheckedOptions) error
	SetInputFiles(files []string, opts LocatorSetInputFilesOptions) error
	TextContent(LocatorOptions) (string, error)
	InnerHTML(LocatorOptions) (string, error)
	GetAttribute(name string, opts LocatorOptions) (string, error)
	IsVisible(LocatorOptions) (bool, error)
	Count() (int, error)
	First() Locator
	Last() Locator
	Nth(index int) Locator
	WaitFor(LocatorWaitForOptions) error
	Screenshot(ScreenshotOptions) ([]byte, error)
}

type LocatorOptions struct {
	Timeout time.Duration
}

type LocatorClickOptions struct {
	Timeout    time.Duration
	Force      bool
	Button     string
	ClickCount int
}

type LocatorFillOptions struct {
	Timeout time.Duration
	Force   bool
}

type LocatorTypeOptions struct {
	Timeout time.Duration
	Delay   time.Duration
}

type LocatorPressOptions struct {
	Timeout time.Duration
}

type LocatorHoverOptions struct {
	Timeout time.Duration
	Force   bool
}

type LocatorSelectOptions struct {
	Timeout time.Duration
	Force   bool
	Values  []string
	Labels  []string
	Indexes []int
}

type LocatorSetCheckedOptions struct {
	Timeout time.Duration
	Force   bool
}

type LocatorSetInputFilesOptions struct {
	Timeout time.Duration
}

type LocatorWaitForOptions struct {
	Timeout time.Duration
	State   string
}
