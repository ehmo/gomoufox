package gomoufox

import (
	"context"
	"fmt"
	"sync"

	"github.com/ehmo/gomoufox/internal/pwbridge"
)

type Browser struct {
	cfg     launchConfig
	sidecar sidecarHandle
	session pwbridge.Session
	raw     pwbridge.Browser

	mu                 sync.Mutex
	closeOnce          sync.Once
	closed             bool
	persistentReturned bool
	done               chan struct{}
	disconnected       []func()
}

func (b *Browser) Close() error {
	var err error
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		if b.session != nil {
			err = b.session.Stop()
		}
		if b.sidecar != nil {
			b.sidecar.Stop(context.Background())
		}
		close(b.done)
	})
	return err
}

func (b *Browser) NewContext(ctx context.Context, opts ...ContextOption) (*Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrSessionClosed
	}
	if b.cfg.persistentCtx {
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

	raw, err := b.raw.NewContext(toPWBridgeContextOptions(buildContextConfig(opts...)))
	if err != nil {
		return nil, err
	}
	return &Context{browser: b, raw: raw}, nil
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
	c, err := b.NewContext(ctx, opts...)
	if err != nil {
		return nil, err
	}
	p, err := c.NewPage(ctx)
	if err != nil {
		_ = c.Close()
		return nil, err
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
