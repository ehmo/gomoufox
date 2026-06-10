package gomoufox

import (
	"context"
	"fmt"

	"github.com/ehmo/gomoufox/internal/policy"
	"github.com/ehmo/gomoufox/internal/pwbridge"
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
	configureConnectorForRuntime(&cfg)
	if cfg.autoInstall {
		if err := EnsureInstalled(ctx,
			func(o *InstallOptions) {
				o.PythonBin = cfg.pythonBin
				o.VenvDir = cfg.venvDir
				o.Runtime = cfg.sidecarRuntime
			},
		); err != nil {
			return nil, err
		}
	}
	return newBrowser(ctx, cfg)
}

func configureConnectorForRuntime(cfg *launchConfig) {
	if installRuntime(cfg.sidecarRuntime) != SidecarRuntimeNodeDirect {
		return
	}
	driverDir := nodeDirectPlaywrightDriverDir(cfg.venvDir)
	switch connector := cfg.connector.(type) {
	case pwbridge.RealConnector:
		if connector.DriverDirectory == "" {
			connector.DriverDirectory = driverDir
			cfg.connector = connector
		}
	case *pwbridge.RealConnector:
		if connector != nil && connector.DriverDirectory == "" {
			connector.DriverDirectory = driverDir
		}
	}
}

func newBrowser(ctx context.Context, cfg launchConfig) (*Browser, error) {
	manager, err := cfg.sidecar(cfg)
	if err != nil {
		return nil, err
	}
	prepareCh, cancelPrepare := prepareConnector(ctx, cfg.connector)
	defer cancelPrepare()
	endpoint, err := manager.Start(ctx)
	if err != nil {
		stopPreparedConnector(prepareCh, cancelPrepare)
		return nil, mapSidecarError(err)
	}
	connector := cfg.connector
	var prepared pwbridge.PreparedConnector
	if prepareCh != nil {
		preparedResult := <-prepareCh
		if preparedResult.err != nil {
			manager.Stop(context.Background())
			return nil, fmt.Errorf("%w: %s", ErrConnect, policy.Redact(preparedResult.err.Error()))
		}
		prepared = preparedResult.connector
		connector = prepared
	}
	session, err := connector.Connect(ctx, endpoint, connectOptions(cfg))
	if err != nil {
		manager.Stop(context.Background())
		if prepared != nil {
			_ = prepared.Stop()
		}
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

type connectorPrepareResult struct {
	connector pwbridge.PreparedConnector
	err       error
}

func prepareConnector(ctx context.Context, connector pwbridge.Connector) (<-chan connectorPrepareResult, context.CancelFunc) {
	cancel := func() {}
	preparer, ok := connector.(pwbridge.PreparableConnector)
	if !ok {
		return nil, cancel
	}
	prepareCtx, cancelPrepare := context.WithCancel(ctx)
	ch := make(chan connectorPrepareResult, 1)
	go func() {
		prepared, err := preparer.Prepare(prepareCtx)
		ch <- connectorPrepareResult{connector: prepared, err: err}
	}()
	return ch, cancelPrepare
}

func stopPreparedConnector(ch <-chan connectorPrepareResult, cancel context.CancelFunc) {
	cancel()
	if ch == nil {
		return
	}
	result := <-ch
	if result.connector != nil {
		_ = result.connector.Stop()
	}
}
