package mcp

import (
	"context"
	"time"

	"github.com/ehmo/gomoufox"
)

type BrowserFactory interface {
	NewBrowserSession(context.Context, sessionOptions) (browserSession, error)
}

type browserSession interface {
	Navigate(context.Context, string, navigateOptions) (navigateResult, error)
	PageContent(context.Context, pageContentOptions) (pageContent, error)
	Evaluate(context.Context, string, any, evaluateOptions) (any, error)
	Click(context.Context, elementTarget, clickOptions) error
	Type(context.Context, elementTarget, string, typeOptions) error
	PressKey(context.Context, elementTarget, string, pressOptions) error
	Hover(context.Context, elementTarget, hoverOptions) error
	Scroll(context.Context, scrollOptions) error
	SelectOption(context.Context, elementTarget, selectOptionOptions) ([]string, error)
	SetChecked(context.Context, elementTarget, bool, checkedOptions) error
	UploadFile(context.Context, elementTarget, []string, uploadOptions) error
	Dialog(context.Context, dialogOptions) (dialogResult, error)
	WaitFor(context.Context, waitCondition) error
	Screenshot(context.Context, screenshotOptions) (screenshotResult, error)
	Snapshot(context.Context, snapshotOptions) (snapshotResult, error)
	Fetch(context.Context, fetchOptions) (fetchResult, error)
	ConsoleMessages(context.Context, observeOptions) (consoleMessagesResult, error)
	NetworkRequests(context.Context, observeOptions) (networkRequestsResult, error)
	PerformanceSnapshot(context.Context) (performanceSnapshot, error)
	Cookies(context.Context, cookieOptions) (cookieResult, error)
	SaveStorageState(context.Context, string) (*gomoufox.StorageState, error)
	// LoadStorageState replaces the active non-persistent browser context.
	// Implementations must leave the current context active if replacement setup fails.
	LoadStorageState(context.Context, *gomoufox.StorageState) error
	Close() error
}

type navigateOptions struct {
	WaitUntil string
	Timeout   time.Duration
}

type navigateResult struct {
	URL    string
	Title  string
	Status int
}

type pageContent struct {
	URL       string
	Title     string
	HTML      string
	Text      string
	Truncated bool
}

type pageContentOptions struct {
	Selector    string
	MaxBytes    int
	IncludeHTML bool
	IncludeText bool
}

type evaluateOptions struct {
	Timeout time.Duration
}

type elementTarget struct {
	Ref      string
	Selector string
}

type clickOptions struct {
	Button            string
	ClickCount        int
	WaitForNavigation bool
	Timeout           time.Duration
}

type typeOptions struct {
	ClearFirst      bool
	PressEnterAfter bool
	Delay           time.Duration
	Timeout         time.Duration
}

type pressOptions struct {
	Timeout time.Duration
}

type hoverOptions struct {
	Timeout time.Duration
	Force   bool
}

type scrollOptions struct {
	Target  elementTarget
	DeltaX  float64
	DeltaY  float64
	Timeout time.Duration
}

type selectOptionOptions struct {
	Timeout time.Duration
	Force   bool
	Values  []string
	Labels  []string
	Indexes []int
}

type checkedOptions struct {
	Timeout time.Duration
	Force   bool
}

type uploadOptions struct {
	Timeout time.Duration
}

type dialogOptions struct {
	Action     string
	Policy     string
	PromptText string
	MaxEvents  int
	Clear      bool
}

type dialogResult struct {
	Policy  string
	Dialogs []map[string]any
	Dropped int
}

type waitCondition struct {
	Kind    string
	Value   string
	Timeout time.Duration
}

type screenshotOptions struct {
	FullPage bool
	Selector string
	MaxBytes int
}

type screenshotResult struct {
	URL    string
	Width  int
	Height int
	Data   []byte
}

type snapshotOptions struct {
	MaxElements     int
	InteractiveOnly bool
	IncludeValues   bool
}

const maxSnapshotValueLength = 120

const (
	dialogActionHistory   = "history"
	dialogActionSetPolicy = "set_policy"
	dialogPolicyDismiss   = "dismiss"
	dialogPolicyAccept    = "accept"
)

type snapshotResult struct {
	URL      string
	Title    string
	Elements []map[string]any
}

type fetchOptions struct {
	URL           string
	Method        string
	Headers       map[string]string
	Body          []byte
	NavigateFirst string
	MaxBytes      int
}

type fetchResult struct {
	URL       string
	Status    int
	Headers   map[string]string
	Body      []byte
	Truncated bool
}

type observeOptions struct {
	MaxEvents int
	Clear     bool
}

type consoleMessagesResult struct {
	Messages          []map[string]any
	PageErrors        []map[string]any
	ConsoleDropped    int
	PageErrorsDropped int
}

type networkRequestsResult struct {
	Requests []map[string]any
	Dropped  int
}

type performanceSnapshot struct {
	URL          string
	Title        string
	Navigation   map[string]any
	Resources    map[string]any
	Memory       map[string]any
	Viewport     map[string]any
	SampledAtUTC string
}

type cookieOptions struct {
	Action        string
	URLs          []string
	Cookies       []cookie
	IncludeValues bool
}

type cookieResult struct {
	Cookies []cookie
	Count   int
}

type cookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	Expires  float64
	HTTPOnly bool
	Secure   bool
	SameSite string
}
