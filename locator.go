package gomoufox

import (
	"context"
	"time"

	"github.com/ehmo/gomoufox/internal/pwbridge"
)

type Locator interface {
	Click(ctx context.Context, opts ...LocatorClickOption) error
	Fill(ctx context.Context, value string, opts ...LocatorFillOption) error
	Type(ctx context.Context, value string, opts ...LocatorTypeOption) error
	Press(ctx context.Context, key string, opts ...LocatorPressOption) error
	Hover(ctx context.Context, opts ...LocatorHoverOption) error
	ScrollIntoViewIfNeeded(ctx context.Context, opts ...LocatorOption) error
	SelectOption(ctx context.Context, opts ...LocatorSelectOption) ([]string, error)
	SetChecked(ctx context.Context, checked bool, opts ...LocatorSetCheckedOption) error
	SetInputFiles(ctx context.Context, files []string, opts ...LocatorSetInputFilesOption) error
	TextContent(ctx context.Context, opts ...LocatorTextContentOption) (string, error)
	InnerHTML(ctx context.Context, opts ...LocatorOption) (string, error)
	GetAttribute(ctx context.Context, name string, opts ...LocatorOption) (string, error)
	IsVisible(ctx context.Context, opts ...LocatorOption) (bool, error)
	Count(ctx context.Context) (int, error)
	First() Locator
	Last() Locator
	Nth(index int) Locator
	WaitFor(ctx context.Context, opts ...LocatorWaitForOption) error
	Screenshot(ctx context.Context, opts ...ScreenshotOption) ([]byte, error)
}

type locator struct{ raw pwbridge.Locator }

func (l *locator) Click(ctx context.Context, opts ...LocatorClickOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := locatorClickConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.Click(pwbridge.LocatorClickOptions{Timeout: cfg.timeout, Force: cfg.force, Button: cfg.button, ClickCount: cfg.clickCount})
}

func (l *locator) Fill(ctx context.Context, value string, opts ...LocatorFillOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := locatorFillConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.Fill(value, pwbridge.LocatorFillOptions{Timeout: cfg.timeout, Force: cfg.force})
}

func (l *locator) Type(ctx context.Context, value string, opts ...LocatorTypeOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := locatorTypeConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.Type(value, pwbridge.LocatorTypeOptions{Timeout: cfg.timeout, Delay: cfg.delay})
}

func (l *locator) Press(ctx context.Context, key string, opts ...LocatorPressOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := locatorPressConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.Press(key, pwbridge.LocatorPressOptions{Timeout: cfg.timeout})
}

func (l *locator) Hover(ctx context.Context, opts ...LocatorHoverOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := locatorHoverConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.Hover(pwbridge.LocatorHoverOptions{Timeout: cfg.timeout, Force: cfg.force})
}

func (l *locator) ScrollIntoViewIfNeeded(ctx context.Context, opts ...LocatorOption) error {
	cfg := buildLocatorOption(ctx, opts...)
	return l.raw.ScrollIntoViewIfNeeded(pwbridge.LocatorOptions{Timeout: cfg.timeout})
}

func (l *locator) SelectOption(ctx context.Context, opts ...LocatorSelectOption) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := locatorSelectConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.SelectOption(pwbridge.LocatorSelectOptions{Timeout: cfg.timeout, Force: cfg.force, Values: cfg.values, Labels: cfg.labels, Indexes: cfg.indexes})
}

func (l *locator) SetChecked(ctx context.Context, checked bool, opts ...LocatorSetCheckedOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := locatorSetCheckedConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.SetChecked(checked, pwbridge.LocatorSetCheckedOptions{Timeout: cfg.timeout, Force: cfg.force})
}

func (l *locator) SetInputFiles(ctx context.Context, files []string, opts ...LocatorSetInputFilesOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := locatorSetInputFilesConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.SetInputFiles(files, pwbridge.LocatorSetInputFilesOptions{Timeout: cfg.timeout})
}

func (l *locator) TextContent(ctx context.Context, opts ...LocatorTextContentOption) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cfg := locatorOptionConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.TextContent(pwbridge.LocatorOptions{Timeout: cfg.timeout})
}

func (l *locator) InnerHTML(ctx context.Context, opts ...LocatorOption) (string, error) {
	cfg := buildLocatorOption(ctx, opts...)
	return l.raw.InnerHTML(pwbridge.LocatorOptions{Timeout: cfg.timeout})
}

func (l *locator) GetAttribute(ctx context.Context, name string, opts ...LocatorOption) (string, error) {
	cfg := buildLocatorOption(ctx, opts...)
	return l.raw.GetAttribute(name, pwbridge.LocatorOptions{Timeout: cfg.timeout})
}

func (l *locator) IsVisible(ctx context.Context, opts ...LocatorOption) (bool, error) {
	cfg := buildLocatorOption(ctx, opts...)
	return l.raw.IsVisible(pwbridge.LocatorOptions{Timeout: cfg.timeout})
}

func (l *locator) Count(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return l.raw.Count()
}

func (l *locator) First() Locator { return &locator{raw: l.raw.First()} }
func (l *locator) Last() Locator  { return &locator{raw: l.raw.Last()} }
func (l *locator) Nth(index int) Locator {
	return &locator{raw: l.raw.Nth(index)}
}

func (l *locator) WaitFor(ctx context.Context, opts ...LocatorWaitForOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := locatorWaitForConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.WaitFor(pwbridge.LocatorWaitForOptions{Timeout: cfg.timeout, State: cfg.state})
}

func (l *locator) Screenshot(ctx context.Context, opts ...ScreenshotOption) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := screenshotConfig{typ: "png"}
	for _, opt := range opts {
		opt(&cfg)
	}
	return l.raw.Screenshot(cfg.toBridge())
}

type LocatorClickOption func(*locatorClickConfig)
type LocatorFillOption func(*locatorFillConfig)
type LocatorTypeOption func(*locatorTypeConfig)
type LocatorPressOption func(*locatorPressConfig)
type LocatorHoverOption func(*locatorHoverConfig)
type LocatorSelectOption func(*locatorSelectConfig)
type LocatorSetCheckedOption func(*locatorSetCheckedConfig)
type LocatorSetInputFilesOption func(*locatorSetInputFilesConfig)
type LocatorTextContentOption func(*locatorOptionConfig)
type LocatorOption func(*locatorOptionConfig)
type LocatorWaitForOption func(*locatorWaitForConfig)

type locatorClickConfig struct {
	timeout    time.Duration
	force      bool
	button     string
	clickCount int
}
type locatorFillConfig struct {
	timeout time.Duration
	force   bool
}
type locatorTypeConfig struct {
	timeout time.Duration
	delay   time.Duration
}
type locatorPressConfig struct {
	timeout time.Duration
}
type locatorHoverConfig struct {
	timeout time.Duration
	force   bool
}
type locatorSelectConfig struct {
	timeout time.Duration
	force   bool
	values  []string
	labels  []string
	indexes []int
}
type locatorSetCheckedConfig struct {
	timeout time.Duration
	force   bool
}
type locatorSetInputFilesConfig struct {
	timeout time.Duration
}
type locatorOptionConfig struct {
	timeout time.Duration
}
type locatorWaitForConfig struct {
	timeout time.Duration
	state   string
}

func LocatorTimeout(d time.Duration) LocatorOption {
	return func(c *locatorOptionConfig) { c.timeout = d }
}

func LocatorClickTimeout(d time.Duration) LocatorClickOption {
	return func(c *locatorClickConfig) { c.timeout = d }
}

func LocatorClickForce(force bool) LocatorClickOption {
	return func(c *locatorClickConfig) { c.force = force }
}

func LocatorClickButton(button string) LocatorClickOption {
	return func(c *locatorClickConfig) { c.button = button }
}

func LocatorClickCount(count int) LocatorClickOption {
	return func(c *locatorClickConfig) { c.clickCount = count }
}

func LocatorFillTimeout(d time.Duration) LocatorFillOption {
	return func(c *locatorFillConfig) { c.timeout = d }
}

func LocatorFillForce(force bool) LocatorFillOption {
	return func(c *locatorFillConfig) { c.force = force }
}

func LocatorTypeTimeout(d time.Duration) LocatorTypeOption {
	return func(c *locatorTypeConfig) { c.timeout = d }
}

func LocatorTypeDelay(d time.Duration) LocatorTypeOption {
	return func(c *locatorTypeConfig) { c.delay = d }
}

func LocatorPressTimeout(d time.Duration) LocatorPressOption {
	return func(c *locatorPressConfig) { c.timeout = d }
}

func LocatorHoverTimeout(d time.Duration) LocatorHoverOption {
	return func(c *locatorHoverConfig) { c.timeout = d }
}

func LocatorHoverForce(force bool) LocatorHoverOption {
	return func(c *locatorHoverConfig) { c.force = force }
}

func LocatorSelectTimeout(d time.Duration) LocatorSelectOption {
	return func(c *locatorSelectConfig) { c.timeout = d }
}

func LocatorSelectForce(force bool) LocatorSelectOption {
	return func(c *locatorSelectConfig) { c.force = force }
}

func LocatorSelectValues(values ...string) LocatorSelectOption {
	return func(c *locatorSelectConfig) { c.values = append([]string(nil), values...) }
}

func LocatorSelectLabels(labels ...string) LocatorSelectOption {
	return func(c *locatorSelectConfig) { c.labels = append([]string(nil), labels...) }
}

func LocatorSelectIndexes(indexes ...int) LocatorSelectOption {
	return func(c *locatorSelectConfig) { c.indexes = append([]int(nil), indexes...) }
}

func LocatorSetCheckedTimeout(d time.Duration) LocatorSetCheckedOption {
	return func(c *locatorSetCheckedConfig) { c.timeout = d }
}

func LocatorSetCheckedForce(force bool) LocatorSetCheckedOption {
	return func(c *locatorSetCheckedConfig) { c.force = force }
}

func LocatorSetInputFilesTimeout(d time.Duration) LocatorSetInputFilesOption {
	return func(c *locatorSetInputFilesConfig) { c.timeout = d }
}

func LocatorTextTimeout(d time.Duration) LocatorTextContentOption {
	return func(c *locatorOptionConfig) { c.timeout = d }
}

func LocatorWaitTimeout(d time.Duration) LocatorWaitForOption {
	return func(c *locatorWaitForConfig) { c.timeout = d }
}

func LocatorWaitState(state string) LocatorWaitForOption {
	return func(c *locatorWaitForConfig) { c.state = state }
}

func buildLocatorOption(ctx context.Context, opts ...LocatorOption) locatorOptionConfig {
	cfg := locatorOptionConfig{timeout: deadlineTimeout(ctx, 0)}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}
