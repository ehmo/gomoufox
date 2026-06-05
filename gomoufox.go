package gomoufox

import (
	"context"
	"fmt"

	"github.com/ehmo/gomoufox/internal/policy"
)

// New launches a Camoufox-backed Firefox browser and returns a connected
// Browser handle.
func New(ctx context.Context, opts ...Option) (*Browser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := defaultLaunchConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.autoInstall {
		if err := EnsureInstalled(ctx,
			func(o *InstallOptions) {
				o.PythonBin = cfg.pythonBin
				o.VenvDir = cfg.venvDir
			},
		); err != nil {
			return nil, err
		}
	}
	return newBrowser(ctx, cfg)
}

func newBrowser(ctx context.Context, cfg launchConfig) (*Browser, error) {
	manager, err := cfg.sidecar(cfg)
	if err != nil {
		return nil, err
	}
	endpoint, err := manager.Start(ctx)
	if err != nil {
		return nil, mapSidecarError(err)
	}
	session, err := cfg.connector.Connect(ctx, endpoint, connectOptions(cfg))
	if err != nil {
		manager.Stop(context.Background())
		return nil, fmt.Errorf("%w: %s", ErrConnect, policy.Redact(err.Error()))
	}
	b := &Browser{
		cfg:     cfg,
		sidecar: manager,
		session: session,
		raw:     session.Browser(),
		done:    make(chan struct{}),
	}
	b.raw.OnDisconnected(b.fireDisconnected)
	go func() {
		<-ctx.Done()
		_ = b.Close()
	}()
	return b, nil
}
