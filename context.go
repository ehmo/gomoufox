package gomoufox

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"

	"github.com/ehmo/gomoufox/internal/pwbridge"
)

type Context struct {
	browser *Browser
	raw     pwbridge.BrowserContext
	mu      sync.Mutex
	routes  map[routeKey]pwbridge.RouteHandler
}

func (c *Context) NewPage(ctx context.Context) (*Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := c.raw.NewPage()
	if err != nil {
		return nil, err
	}
	return &Page{browser: c.browser, context: c, raw: raw}, nil
}

func (c *Context) Pages() []*Page {
	raw := c.raw.Pages()
	out := make([]*Page, 0, len(raw))
	for _, p := range raw {
		out = append(out, &Page{browser: c.browser, context: c, raw: p})
	}
	return out
}

func (c *Context) Cookies(ctx context.Context, urls ...string) ([]Cookie, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return fromBridgeCookies(c.raw.Cookies(urls...))
}

func (c *Context) AddCookies(ctx context.Context, cookies ...Cookie) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.raw.AddCookies(toBridgeCookies(cookies)...)
}

func (c *Context) ClearCookies(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.raw.ClearCookies()
}

func (c *Context) StorageState(ctx context.Context, path string) (*StorageState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, err := c.raw.StorageState()
	if err != nil {
		return nil, err
	}
	out := fromBridgeStorageState(state)
	if path != "" {
		if err := writeJSON0600(path, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Context) Route(ctx context.Context, urlPattern string, handler RouteHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wrapped := wrapRouteHandler(handler)
	if handler != nil {
		c.mu.Lock()
		if c.routes == nil {
			c.routes = make(map[routeKey]pwbridge.RouteHandler)
		}
		key := newRouteKey(urlPattern, handler)
		c.routes[key] = wrapped
		c.mu.Unlock()
	}
	if err := c.raw.Route(urlPattern, wrapped); err != nil {
		if handler != nil {
			c.mu.Lock()
			delete(c.routes, newRouteKey(urlPattern, handler))
			c.mu.Unlock()
		}
		return err
	}
	return nil
}

func (c *Context) Unroute(ctx context.Context, urlPattern string, handler RouteHandler) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if handler == nil {
		c.mu.Lock()
		for key := range c.routes {
			if key.pattern == urlPattern {
				delete(c.routes, key)
			}
		}
		c.mu.Unlock()
		return c.raw.Unroute(urlPattern, nil)
	}
	key := newRouteKey(urlPattern, handler)
	c.mu.Lock()
	wrapped := c.routes[key]
	delete(c.routes, key)
	c.mu.Unlock()
	if wrapped == nil {
		return nil
	}
	return c.raw.Unroute(urlPattern, wrapped)
}

func (c *Context) OnRequest(handler func(*Request)) {
	c.raw.OnRequest(func(r pwbridge.Request) { handler(&Request{raw: r}) })
}

func (c *Context) OnResponse(handler func(*Response)) {
	c.raw.OnResponse(func(r pwbridge.Response) { handler(&Response{raw: r}) })
}

func (c *Context) Close() error { return c.raw.Close() }
func (c *Context) Raw() any     { return c.raw.Raw() }

func writeJSON0600(path string, value any) error {
	if err := fileMkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := fileCreateTemp(filepath.Dir(path), ".gomoufox-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = fileRemove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return fileRename(tmpName, path)
}
