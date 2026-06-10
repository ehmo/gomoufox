package gomoufox

import (
	"context"
	"errors"
	"fmt"

	"github.com/ehmo/gomoufox/internal/pwbridge"
	"github.com/ehmo/gomoufox/internal/sidecar"
)

type InstallOptions struct {
	PythonBin       string
	VenvDir         string
	Runtime         SidecarRuntime
	CamoufoxVersion string
	SkipBinaryFetch bool
	CamoufoxPath    string
	Verbose         bool
	ForceReinstall  bool
}

var (
	sidecarEnsureInstalled = sidecar.EnsureInstalled
	pwbridgeEnsureDriver   = pwbridge.EnsureDriver
)

func EnsureInstalled(ctx context.Context, opts ...func(*InstallOptions)) error {
	var cfg InstallOptions
	for _, opt := range opts {
		opt(&cfg)
	}
	if err := sidecarEnsureInstalled(ctx, sidecar.InstallOptions{
		PythonBin:       cfg.PythonBin,
		VenvDir:         cfg.VenvDir,
		Runtime:         string(cfg.Runtime),
		CamoufoxVersion: cfg.CamoufoxVersion,
		SkipBinaryFetch: cfg.SkipBinaryFetch,
		CamoufoxPath:    cfg.CamoufoxPath,
		Verbose:         cfg.Verbose,
		ForceReinstall:  cfg.ForceReinstall,
	}); err != nil {
		return mapSidecarError(err)
	}
	if installRuntime(cfg.Runtime) == SidecarRuntimeNodeDirect {
		return nil
	}
	if err := pwbridgeEnsureDriver(""); err != nil {
		return fmt.Errorf("%w: playwright driver install failed: %v", ErrNotInstalled, err)
	}
	return nil
}

func installRuntime(runtime SidecarRuntime) SidecarRuntime {
	if runtime == "" {
		return SidecarRuntimeNodeDirect
	}
	return runtime
}

func nodeDirectPlaywrightDriverDir(venvDir string) string {
	return sidecar.RuntimeAssetCacheRoot(venvDir, "", "").PlaywrightDriverDir
}

func mapSidecarError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, sidecar.ErrNotInstalled):
		return fmt.Errorf("%w: %v", ErrNotInstalled, err)
	case errors.Is(err, sidecar.ErrVersionMismatch):
		return fmt.Errorf("%w: %v", ErrVersionMismatch, err)
	case errors.Is(err, sidecar.ErrTimeout):
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	case errors.Is(err, sidecar.ErrSidecarStart), errors.Is(err, sidecar.ErrProfileInUse):
		return fmt.Errorf("%w: %v", ErrSidecarStart, err)
	default:
		return err
	}
}
