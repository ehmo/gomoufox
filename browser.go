package gomoufox

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ehmo/gomoufox/internal/harcapture"
	"github.com/ehmo/gomoufox/internal/pwbridge"
)

type Browser struct {
	cfg     launchConfig
	sidecar sidecarHandle
	session pwbridge.Session
	raw     pwbridge.Browser

	mu                 sync.Mutex
	closeOnce          sync.Once
	closeErr           error
	closed             bool
	persistentReturned bool
	done               chan struct{}
	disconnected       []func()
	harContexts        map[*Context]struct{}
}

func (b *Browser) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		harContexts := make([]*Context, 0, len(b.harContexts))
		for browserContext := range b.harContexts {
			harContexts = append(harContexts, browserContext)
		}
		b.mu.Unlock()
		for _, browserContext := range harContexts {
			b.closeErr = errors.Join(b.closeErr, browserContext.Close())
		}
		if b.session != nil {
			b.closeErr = errors.Join(b.closeErr, b.session.Stop())
		}
		if b.sidecar != nil {
			b.sidecar.Stop(context.Background())
		}
		close(b.done)
	})
	return b.closeErr
}

func (b *Browser) NewContext(ctx context.Context, opts ...ContextOption) (*Context, error) {
	return b.newContext(ctx, false, opts...)
}

func (b *Browser) newContext(ctx context.Context, provisionalHAR bool, opts ...ContextOption) (*Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := buildContextConfig(opts...)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrSessionClosed
	}
	if b.cfg.persistentCtx {
		if cfg.HAR != nil {
			b.mu.Unlock()
			return nil, ErrHARPersistentContext
		}
		if b.persistentReturned {
			b.mu.Unlock()
			return nil, ErrPersistentContextLimit
		}
		rawContexts := b.raw.Contexts()
		if len(rawContexts) == 0 {
			b.mu.Unlock()
			return nil, fmt.Errorf("%w: persistent context unavailable after connect", ErrSidecarStart)
		}
		b.persistentReturned = true
		b.mu.Unlock()
		return &Context{browser: b, raw: rawContexts[0]}, nil
	}
	b.mu.Unlock()

	bridgeOptions := toPWBridgeContextOptions(cfg)
	var recorder *harcapture.Recorder
	if cfg.HAR != nil {
		var err error
		recorder, bridgeOptions.HAR, err = prepareHAR(*cfg.HAR)
		if err != nil {
			return nil, err
		}
	}
	raw, err := b.raw.NewContext(bridgeOptions)
	if err != nil {
		_ = recorder.Discard()
		return nil, err
	}
	browserContext := &Context{browser: b, raw: raw, har: recorder, harProvisional: recorder != nil && provisionalHAR}
	if recorder != nil {
		if !b.trackHARContext(browserContext) {
			_ = browserContext.Abort()
			return nil, ErrSessionClosed
		}
	}
	return browserContext, nil
}

func (b *Browser) commitHARContext(browserContext *Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	return browserContext.commitHAR()
}

func (b *Browser) trackHARContext(browserContext *Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	if b.harContexts == nil {
		b.harContexts = make(map[*Context]struct{})
	}
	b.harContexts[browserContext] = struct{}{}
	return true
}

func (b *Browser) untrackHARContext(browserContext *Context) {
	b.mu.Lock()
	delete(b.harContexts, browserContext)
	b.mu.Unlock()
}

func (b *Browser) NewPage(ctx context.Context, opts ...ContextOption) (*Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.cfg.persistentCtx {
		c, err := b.NewContext(ctx, opts...)
		if err != nil && err != ErrPersistentContextLimit {
			return nil, err
		}
		if err == ErrPersistentContextLimit {
			b.mu.Lock()
			rawContexts := b.raw.Contexts()
			b.mu.Unlock()
			if len(rawContexts) == 0 {
				return nil, err
			}
			c = &Context{browser: b, raw: rawContexts[0]}
		}
		return c.NewPage(ctx)
	}
	c, err := b.newContext(ctx, true, opts...)
	if err != nil {
		return nil, err
	}
	p, err := c.NewPage(ctx)
	if err != nil {
		_ = c.Abort()
		return nil, err
	}
	if c.har != nil && !b.commitHARContext(c) {
		_ = c.Abort()
		return nil, ErrSessionClosed
	}
	p.ownsContext = true
	return p, nil
}

func (b *Browser) IsConnected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed && b.raw != nil && b.raw.IsConnected()
}

func (b *Browser) OnDisconnected(fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.disconnected = append(b.disconnected, fn)
}

func (b *Browser) Sidecar() SidecarInfo {
	if b.sidecar == nil {
		return SidecarInfo{}
	}
	return b.sidecar.Info()
}

func (b *Browser) fireDisconnected() {
	b.mu.Lock()
	handlers := append([]func(){}, b.disconnected...)
	b.mu.Unlock()
	for _, fn := range handlers {
		go fn()
	}
}

func buildContextConfig(opts ...ContextOption) contextConfig {
	var cfg contextConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}
