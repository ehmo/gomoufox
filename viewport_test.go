package gomoufox

import (
	"context"
	"errors"
	"github.com/ehmo/gomoufox/internal/pwbridge"
	"testing"
)

type viewportTestPage struct {
	*fakePage
	width, height int
	err           error
}

func (p *viewportTestPage) SetViewportSize(width, height int) error {
	p.width, p.height = width, height
	return p.err
}

func TestPersistentViewportAppliesToEachNewPage(t *testing.T) {
	raw := &viewportTestPage{fakePage: &fakePage{}}
	c := &fakeContext{newPageFunc: func() (pwbridge.Page, error) { return raw, nil }}
	b := &Browser{cfg: launchConfig{persistentCtx: true}, raw: &fakeBrowser{contexts: []pwbridge.BrowserContext{c}}}
	for _, width := range []int{390, 1440} {
		p, err := b.NewPage(context.Background(), WithViewport(width, 844))
		if err != nil || p == nil || raw.width != width || raw.height != 844 {
			t.Fatalf("viewport %d: %v, %+v", width, err, raw)
		}
	}
	raw.err = errors.New("resize failed")
	if _, err := b.NewPage(context.Background(), WithViewport(390, 844)); !errors.Is(err, raw.err) || raw.closeCalls != 1 {
		t.Fatalf("failed page was not closed: %v, calls=%d", err, raw.closeCalls)
	}
	if _, err := b.NewPage(context.Background(), WithViewport(0, 844)); err == nil {
		t.Fatal("invalid viewport accepted")
	}
}

func TestPageViewportRejectsUnsupportedInvalidAndCanceledCalls(t *testing.T) {
	p := &Page{raw: &fakePage{}}
	if err := p.SetViewportSize(context.Background(), 390, 844); err == nil {
		t.Fatal("unsupported resize succeeded")
	}
	if err := p.SetViewportSize(context.Background(), 0, 844); err == nil {
		t.Fatal("zero width accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.SetViewportSize(ctx, 390, 844); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled resize: %v", err)
	}
}
