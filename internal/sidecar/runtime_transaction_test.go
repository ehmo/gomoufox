package sidecar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRepairFailurePreservesUsableInstallation(t *testing.T) {
	cache, root := inspectionFixture(t)
	before, err := treeSHA256(root.Root)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("replacement browser copy failed")
	installCamoufoxBrowserForRuntime = func(_ context.Context, stage RuntimeRoot, _ InstallOptions) error {
		if err := os.WriteFile(filepath.Join(stage.BrowserResourcesDir, "camoufox"), []byte("partly copied replacement"), 0700); err != nil {
			return err
		}
		return failure
	}
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cache, ForceReinstall: true}); !errors.Is(err, failure) {
		t.Fatalf("repair failure = %v", err)
	}
	if _, _, err := ResolveRuntimeAssets(cache); err != nil {
		t.Fatalf("failed repair broke previous runtime: %v", err)
	}
	after, err := treeSHA256(root.Root)
	if err != nil || before != after {
		t.Fatalf("previous runtime changed: before=%s after=%s err=%v", before, after, err)
	}
}

func TestRuntimePublicationFailureRestoresPreviousDirectory(t *testing.T) {
	cache, root := inspectionFixture(t)
	before, err := treeSHA256(root.Root)
	if err != nil {
		t.Fatal(err)
	}
	files := runtimeAssetFiles
	t.Cleanup(func() { runtimeAssetFiles = files })
	failure := errors.New("publish rejected")
	runtimeAssetFiles.rename = func(from, to string) error {
		if to == root.Root && strings.HasPrefix(filepath.Base(from), ".runtime-stage-") {
			return failure
		}
		return os.Rename(from, to)
	}
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cache, ForceReinstall: true}); !errors.Is(err, failure) {
		t.Fatalf("publish error = %v", err)
	}
	if _, _, err := ResolveRuntimeAssets(cache); err != nil {
		t.Fatalf("rollback did not restore runtime: %v", err)
	}
	after, err := treeSHA256(root.Root)
	if err != nil || before != after {
		t.Fatalf("rollback changed previous files: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(root.Root))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(root.Root) {
		t.Fatalf("unpublished staging left behind: %v", entries)
	}
}

func TestPreparedRuntimeCanRollbackAfterPublication(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			cache, root := inspectionFixture(t)
			before, err := treeSHA256(root.Root)
			if err != nil {
				t.Fatal(err)
			}
			if !existing {
				if err := os.RemoveAll(root.Root); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := prepareRuntimeAssets(context.Background(), InstallOptions{VenvDir: cache, ForceReinstall: true})
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.cleanup()
			if err := prepared.publish(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ResolveRuntimeAssets(cache); err != nil {
				t.Fatal(err)
			}
			if err := prepared.rollback(); err != nil {
				t.Fatal(err)
			}
			if existing {
				after, err := treeSHA256(root.Root)
				if err != nil || before != after {
					t.Fatalf("rollback mismatch: %v", err)
				}
			} else if _, err := os.Stat(root.Root); !os.IsNotExist(err) {
				t.Fatalf("rollback did not remove new installation: %v", err)
			}
		})
	}
}

func TestRuntimeCommitCleanupFailureKeepsPublishedAssets(t *testing.T) {
	cache, root := inspectionFixture(t)
	prepared, err := prepareRuntimeAssets(context.Background(), InstallOptions{VenvDir: cache, ForceReinstall: true})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.cleanup()
	if err := prepared.publish(); err != nil {
		t.Fatal(err)
	}
	backup := prepared.replacement.backup
	files := runtimeAssetFiles
	t.Cleanup(func() { runtimeAssetFiles = files })
	runtimeAssetFiles.removeAll = func(path string) error {
		if path == backup {
			return errors.New("cleanup blocked")
		}
		return os.RemoveAll(path)
	}
	prepared.commit()
	if _, _, err := ResolveRuntimeAssets(cache); err != nil {
		t.Fatalf("cleanup failure affected published assets: %v", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("previous assets were not retained: %v", err)
	}
	if prepared.root.Root != root.Root {
		t.Fatalf("wrong published root: %#v", prepared.root)
	}
}

func TestRuntimeManifestRejectsAmbiguousAssetRecords(t *testing.T) {
	_, root := inspectionFixture(t)
	original, err := LoadRuntimeAssetManifest(root.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RuntimeAssetManifest){
		func(m *RuntimeAssetManifest) { m.Assets = append(m.Assets, m.Assets[0]) },
		func(m *RuntimeAssetManifest) { m.Assets[0].Kind = "other" },
		func(m *RuntimeAssetManifest) { m.Assets[0].Path = "other/node" },
		func(m *RuntimeAssetManifest) { m.Assets[0].SHA256 = "" },
		func(m *RuntimeAssetManifest) { m.Assets[0].SHA256 = strings.Repeat("x", 64) },
	} {
		m := original
		m.Assets = append([]RuntimeAssetRecord(nil), original.Assets...)
		mutate(&m)
		if err := VerifyRuntimeAssets(root, m, sidecarGOOS, sidecarGOARCH); err == nil {
			t.Fatal("ambiguous or unverified assets accepted")
		}
	}
}

func TestRuntimeRepairDoesNotRebaselineCorruptDriver(t *testing.T) {
	cache, root := inspectionFixture(t)
	if err := os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "cli.js"), []byte("CLI"), 0600); err != nil {
		t.Fatal(err)
	}
	refreshInspectionManifest(t, root)
	manifestBefore, err := os.ReadFile(root.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "index.js"), []byte("corrupt but version string still matches"), 0600); err != nil {
		t.Fatal(err)
	}
	installPlaywrightDriverForRuntime = installRuntimePlaywrightDriver
	t.Setenv("GOMOUFOX_SKIP_FETCH", "1")
	if _, err := EnsureRuntimeAssets(context.Background(), InstallOptions{VenvDir: cache}); err == nil {
		t.Fatal("repair accepted a corrupt driver by generating a new checksum")
	}
	after, err := os.ReadFile(root.ManifestPath)
	if err != nil || string(after) != string(manifestBefore) {
		t.Fatalf("failed repair rewrote manifest: %v", err)
	}
}

func TestRuntimeStagingFaultsPreservePublishedTree(t *testing.T) {
	for _, phase := range []string{"stage mkdir", "asset mkdir", "launcher validation", "ready write", "cancel before ready", "cancel after ready", "copy node", "copy package", "verify copied driver"} {
		t.Run(phase, func(t *testing.T) {
			cache, root := inspectionFixture(t)
			// A stale launcher requires repair while the driver remains reusable.
			if err := os.WriteFile(root.LaunchServerJS, []byte("old launcher"), 0600); err != nil {
				t.Fatal(err)
			}
			refreshInspectionManifest(t, root)
			before, err := treeSHA256(root.Root)
			if err != nil {
				t.Fatal(err)
			}
			files := runtimeAssetFiles
			t.Cleanup(func() { runtimeAssetFiles = files })
			failure := errors.New("injected " + phase)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch phase {
			case "stage mkdir":
				runtimeAssetFiles.mkdirTemp = func(dir, pattern string) (string, error) { return "", failure }
			case "asset mkdir":
				runtimeAssetFiles.mkdirAll = func(path string, mode os.FileMode) error {
					if strings.Contains(path, ".runtime-stage-") {
						return failure
					}
					return files.mkdirAll(path, mode)
				}
			case "launcher validation":
				runtimeAssetFiles.readFile = func(path string) ([]byte, error) {
					if strings.Contains(path, ".runtime-stage-") && filepath.Base(path) == filepath.Base(root.LaunchServerJS) {
						return nil, failure
					}
					return files.readFile(path)
				}
			case "ready write", "cancel after ready":
				runtimeAssetFiles.writeFile = func(path string, data []byte, mode os.FileMode) error {
					if strings.Contains(path, ".runtime-stage-") && filepath.Base(path) == filepath.Base(root.ReadyMarkerPath) {
						if phase == "ready write" {
							return failure
						}
						cancel()
					}
					return files.writeFile(path, data, mode)
				}
			case "cancel before ready":
				browser := installCamoufoxBrowserForRuntime
				installCamoufoxBrowserForRuntime = func(ctx context.Context, stage RuntimeRoot, opts InstallOptions) error {
					err := browser(ctx, stage, opts)
					cancel()
					return err
				}
			case "copy node", "copy package":
				runtimeAssetFiles.openFile = func(path string, flags int, mode os.FileMode) (*os.File, error) {
					if strings.Contains(path, ".runtime-stage-") && ((phase == "copy node" && filepath.Base(path) == filepath.Base(root.NodeJS)) || (phase == "copy package" && filepath.Base(path) == "package.json")) {
						return nil, failure
					}
					return files.openFile(path, flags, mode)
				}
			case "verify copied driver":
				runtimeAssetFiles.open = func(path string) (*os.File, error) {
					if strings.Contains(path, ".runtime-stage-") && filepath.Base(path) == filepath.Base(root.NodeJS) {
						return nil, failure
					}
					return files.open(path)
				}
			}
			if _, err := EnsureRuntimeAssets(ctx, InstallOptions{VenvDir: cache}); err == nil {
				t.Fatal("failed repair reported success")
			}
			runtimeAssetFiles = files
			after, err := treeSHA256(root.Root)
			if err != nil || before != after {
				t.Fatalf("published runtime changed: %v", err)
			}
		})
	}
}

func TestRuntimeRollbackFailuresRetainRecoveryBackup(t *testing.T) {
	if err := (&assetReplacement{}).rollback(); err != nil {
		t.Fatal(err)
	}
	if err := (&preparedRuntime{}).rollback(); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"move", "restore"} {
		t.Run(phase, func(t *testing.T) {
			cache, _ := inspectionFixture(t)
			p, err := prepareRuntimeAssets(context.Background(), InstallOptions{VenvDir: cache, ForceReinstall: true})
			if err != nil {
				t.Fatal(err)
			}
			defer p.cleanup()
			if err := p.publish(); err != nil {
				t.Fatal(err)
			}
			backup := p.replacement.backup
			files := runtimeAssetFiles
			t.Cleanup(func() { runtimeAssetFiles = files })
			failure := errors.New("rollback denied")
			runtimeAssetFiles.rename = func(from, to string) error {
				if (phase == "move" && from == p.root.Root) || (phase == "restore" && from == backup) {
					return failure
				}
				return files.rename(from, to)
			}
			if err := p.rollback(); !errors.Is(err, failure) || !strings.Contains(err.Error(), backup) {
				t.Fatalf("rollback = %v", err)
			}
			if _, err := os.Stat(backup); err != nil {
				t.Fatalf("lost recovery backup: %v", err)
			}
		})
	}
}

func TestBrowserCopyStagingFailuresAndVerifiedReuse(t *testing.T) {
	cache, root := inspectionFixture(t)
	key := camoufoxManifestKey(CamoufoxBinaryVersion, sidecarGOOS, sidecarGOARCH)
	old := camoufoxManifestSHA256[key]
	t.Cleanup(func() { camoufoxManifestSHA256[key] = old })
	sum, err := camoufoxBrowserManifestSHA256(root.BrowserResourcesDir)
	if err != nil {
		t.Fatal(err)
	}
	camoufoxManifestSHA256[key] = []string{sum}
	stage := runtimeRootAtPath(filepath.Join(cache, "reuse"), sidecarGOOS)
	if err := os.MkdirAll(stage.PlaywrightPackageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := reuseVerifiedRuntimeAssets(root, stage); err != nil {
		t.Fatal(err)
	}
	if err := verifyCamoufoxManifest(stage.BrowserResourcesDir); err != nil {
		t.Fatal(err)
	}
	files := runtimeAssetFiles
	t.Cleanup(func() { runtimeAssetFiles = files })
	for _, phase := range []string{"mkdir", "temp", "digest"} {
		t.Run(phase, func(t *testing.T) {
			runtimeAssetFiles = files
			failure := errors.New("browser copy failure")
			switch phase {
			case "mkdir":
				runtimeAssetFiles.mkdirAll = func(string, os.FileMode) error { return failure }
			case "temp":
				runtimeAssetFiles.mkdirTemp = func(string, string) (string, error) { return "", failure }
			case "digest":
				camoufoxManifestSHA256[key] = []string{strings.Repeat("0", 64)}
			}
			if err := copyRuntimeCamoufoxBrowser(root.BrowserResourcesDir, stage); err == nil {
				t.Fatal("bad staged browser accepted")
			}
		})
	}
}

func TestRuntimeAssetRecordRejectsUnsafePathsAndKeepsValidModule(t *testing.T) {
	_, root := inspectionFixture(t)
	for _, path := range []string{"", "/outside", "../outside"} {
		if err := verifyRuntimeAssetRecord(root, RuntimeAssetRecord{Path: path, SHA256: strings.Repeat("0", 64)}); !errors.Is(err, ErrVersionMismatch) {
			t.Fatalf("unsafe path accepted: %q %v", path, err)
		}
	}
	if err := ensureRuntimePlaywrightCoreModule(root, sidecarGOOS); err != nil {
		t.Fatal(err)
	}
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: t.TempDir(), Runtime: "unsupported"}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("unsupported runtime = %v", err)
	}
}
