package sidecar

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Callers hold the cache install lock until publication and cleanup finish.
// The previous directory remains available for rollback until commit.
type assetReplacement struct {
	source, target, backup string
	published              bool
}

func (r *assetReplacement) publish() error {
	if _, err := runtimeAssetFiles.lstat(r.target); err == nil {
		placeholder, err := runtimeAssetFiles.mkdirTemp(filepath.Dir(r.target), ".runtime-asset-backup-")
		if err != nil {
			return err
		}
		if err := runtimeAssetFiles.remove(placeholder); err != nil {
			return err
		}
		if err := runtimeAssetFiles.rename(r.target, placeholder); err != nil {
			return err
		}
		r.backup = placeholder
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := runtimeAssetFiles.rename(r.source, r.target); err != nil {
		if r.backup != "" {
			if restoreErr := runtimeAssetFiles.rename(r.backup, r.target); restoreErr != nil {
				return fmt.Errorf("replace runtime asset %s: %v; restore previous asset from %s: %w", r.target, err, r.backup, restoreErr)
			}
			r.backup = ""
		}
		return fmt.Errorf("replace runtime asset %s: %w", r.target, err)
	}
	r.published = true
	return nil
}

func (r *assetReplacement) rollback() error {
	if !r.published {
		return nil
	}
	if err := runtimeAssetFiles.rename(r.target, r.source); err != nil {
		return fmt.Errorf("move replacement aside; previous asset retained at %s: %w", r.backup, err)
	}
	if r.backup != "" {
		if err := runtimeAssetFiles.rename(r.backup, r.target); err != nil {
			return fmt.Errorf("restore previous asset from %s: %w", r.backup, err)
		}
	}
	r.backup, r.published = "", false
	return nil
}

func (r *assetReplacement) commit() {
	if r.backup != "" {
		// A cleanup failure leaves a redundant backup. Publication already
		// succeeded and must not be reported as a failed replacement.
		_ = runtimeAssetFiles.removeAll(r.backup)
	}
	r.backup = ""
}

type preparedRuntime struct {
	root        RuntimeRoot
	replacement *assetReplacement
}

func (p *preparedRuntime) publish() error {
	if p.replacement == nil {
		return nil
	}
	return p.replacement.publish()
}

func (p *preparedRuntime) rollback() error {
	if p.replacement == nil {
		return nil
	}
	return p.replacement.rollback()
}

func (p *preparedRuntime) commit() {
	if p.replacement != nil {
		p.replacement.commit()
	}
}

func (p *preparedRuntime) cleanup() {
	if p.replacement != nil {
		_ = runtimeAssetFiles.removeAll(p.replacement.source)
	}
}

func prepareRuntimeAssets(ctx context.Context, opts InstallOptions) (*preparedRuntime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !runtimeAssetPlatformSupported(sidecarGOOS, sidecarGOARCH) {
		return nil, fmt.Errorf("%w: runtime assets unsupported for %s/%s", ErrNotInstalled, sidecarGOOS, sidecarGOARCH)
	}
	live := RuntimeAssetCacheRoot(opts.VenvDir, sidecarGOOS, sidecarGOARCH)
	if !opts.ForceReinstall {
		if root, _, err := ResolveRuntimeAssets(opts.VenvDir); err == nil {
			return &preparedRuntime{root: root}, nil
		}
	}
	if err := runtimeAssetFiles.mkdirAll(filepath.Dir(live.Root), 0700); err != nil {
		return nil, err
	}
	stagePath, err := runtimeAssetFiles.mkdirTemp(filepath.Dir(live.Root), ".runtime-stage-")
	if err != nil {
		return nil, err
	}
	prepared := &preparedRuntime{root: live, replacement: &assetReplacement{source: stagePath, target: live.Root}}
	complete := false
	defer func() {
		if !complete {
			prepared.cleanup()
		}
	}()
	stage := runtimeRootAtPath(stagePath, sidecarGOOS)
	for _, dir := range []string{stage.PlaywrightPackageDir, filepath.Dir(stage.LaunchServerJS), stage.BrowserResourcesDir} {
		if err := runtimeAssetFiles.mkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	if !opts.ForceReinstall {
		if err := reuseVerifiedRuntimeAssets(live, stage); err != nil {
			return nil, err
		}
	}
	if err := installPlaywrightDriverForRuntime(ctx, stage, opts); err != nil {
		return nil, err
	}
	if err := writeRuntimeLaunchServer(stage); err != nil {
		return nil, err
	}
	if err := ensureRuntimePlaywrightCoreModule(stage, sidecarGOOS); err != nil {
		return nil, err
	}
	if err := installCamoufoxBrowserForRuntime(ctx, stage, opts); err != nil {
		return nil, err
	}
	m := NewRuntimeAssetManifest(stage, sidecarGOOS, sidecarGOARCH)
	if err := PopulateRuntimeAssetManifest(stage, &m); err != nil {
		return nil, err
	}
	if err := WriteRuntimeAssetManifest(stage.ManifestPath, m); err != nil {
		return nil, err
	}
	if err := VerifyRuntimeAssets(stage, m, sidecarGOOS, sidecarGOARCH); err != nil {
		return nil, err
	}
	if err := VerifyRuntimeLaunchServerFresh(stage); err != nil {
		return nil, err
	}
	if err := verifyRuntimePlaywrightCoreModule(stage, sidecarGOOS); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := runtimeAssetFiles.writeFile(stage.ReadyMarkerPath, []byte("manifest-ready\n"), 0600); err != nil {
		return nil, err
	}
	complete = true
	return prepared, nil
}

func reuseVerifiedRuntimeAssets(live, stage RuntimeRoot) error {
	m, err := LoadRuntimeAssetManifest(live.ManifestPath)
	if err == nil && ValidateRuntimeAssetManifest(m, sidecarGOOS, sidecarGOARCH) == nil {
		var driver []RuntimeAssetRecord
		for _, a := range m.Assets {
			if a.GOOS == sidecarGOOS && a.GOARCH == sidecarGOARCH && (a.Kind == RuntimeAssetNodeJS || a.Kind == RuntimeAssetPlaywrightPackage) {
				if err := verifyRuntimeAssetRecord(live, a); err != nil {
					driver = nil
					break
				}
				driver = append(driver, a)
			}
		}
		if len(driver) == 2 {
			if err := copyTree(live.NodeJS, stage.NodeJS); err != nil {
				return err
			}
			if err := copyTree(live.PlaywrightPackageDir, stage.PlaywrightPackageDir); err != nil {
				return err
			}
			for _, a := range driver {
				if err := verifyRuntimeAssetRecord(stage, a); err != nil {
					return err
				}
			}
		}
	}
	// The compiled browser manifest can verify a browser independently of a
	// missing or interrupted runtime manifest. A matching version string alone
	// cannot establish that a driver or browser is safe to reuse.
	if validateCamoufoxBrowserDir(live.BrowserResourcesDir) == nil && verifyCamoufoxManifest(live.BrowserResourcesDir) == nil {
		return copyRuntimeCamoufoxBrowser(live.BrowserResourcesDir, stage)
	}
	return nil
}
