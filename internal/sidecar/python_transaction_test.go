package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonRepairFailurePreservesCurrentEnvironment(t *testing.T) {
	site := fakePlaywrightPackage(t, RequiredPlaywright)
	cache := fakeCompatibleVenv(t, site, RequiredCamoufox)
	marker := filepath.Join(cache, "package-state")
	if err := os.WriteFile(marker, []byte("usable"), 0600); err != nil {
		t.Fatal(err)
	}
	find, venv, packages := findPythonForInstall, ensureVenvForInstall, ensureCamoufoxForInstall
	t.Cleanup(func() { findPythonForInstall, ensureVenvForInstall, ensureCamoufoxForInstall = find, venv, packages })
	findPythonForInstall = func(string) (string, error) { return "python", nil }
	ensureVenvForInstall = func(_ context.Context, _, dir string) error { return os.MkdirAll(dir, 0700) }
	failure := errors.New("pip failed after writing a package")
	ensureCamoufoxForInstall = func(_ context.Context, dir string, _ InstallOptions) error {
		if err := os.WriteFile(filepath.Join(dir, "package-state"), []byte("partial upgrade"), 0600); err != nil {
			return err
		}
		return failure
	}
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: cache, Runtime: RuntimePython, ForceReinstall: true}); !errors.Is(err, failure) {
		t.Fatalf("install = %v", err)
	}
	if state, err := os.ReadFile(marker); err != nil || string(state) != "usable" {
		t.Fatalf("failed pip upgrade changed active environment: %q, %v", state, err)
	}
}

func TestPythonMarkerReadFailuresFailClosed(t *testing.T) {
	cache := t.TempDir()
	marker := filepath.Join(cache, pythonEnvironmentMarker)
	if err := os.WriteFile(marker, []byte("env-missing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cache, pythonEnvironmentsDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := pythonEnvironmentDir(cache); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing active environment = %v", err)
	}
	files := runtimeAssetFiles
	t.Cleanup(func() { runtimeAssetFiles = files })
	failure := errors.New("marker access failed")
	for _, phase := range []string{"stat", "open", "read", "disappeared"} {
		t.Run(phase, func(t *testing.T) {
			runtimeAssetFiles = files
			switch phase {
			case "stat":
				runtimeAssetFiles.lstat = func(string) (os.FileInfo, error) { return nil, failure }
			case "open":
				runtimeAssetFiles.open = func(string) (*os.File, error) { return nil, failure }
			case "read":
				runtimeAssetFiles.open = func(string) (*os.File, error) { return os.Open(cache) }
			case "disappeared":
				runtimeAssetFiles.open = func(string) (*os.File, error) { return nil, os.ErrNotExist }
			}
			got, err := pythonEnvironmentDir(cache)
			if phase == "disappeared" {
				if got != cache || err != nil {
					t.Fatalf("removed marker should select legacy path: %q %v", got, err)
				}
			} else if err == nil || got != "" {
				t.Fatalf("unreadable marker selected environment: %q %v", got, err)
			}
			if phase == "stat" {
				if _, err := VenvPip(cache); !errors.Is(err, failure) {
					t.Fatalf("pip discarded marker error: %v", err)
				}
			}
		})
	}
}

func TestPythonStagingFilesystemFailuresPreservePreviousEnvironment(t *testing.T) {
	for _, phase := range []string{"parent mkdir", "parent invalid", "environment mkdir", "pointer mkdir", "pointer write", "cancel", "publish", "rollback"} {
		t.Run(phase, func(t *testing.T) {
			cache, root := inspectionFixture(t)
			stubStagedPythonEnvironment(t, cache)
			previous, _ := VenvPython(cache)
			before, err := treeSHA256(root.Root)
			if err != nil {
				t.Fatal(err)
			}
			files := runtimeAssetFiles
			t.Cleanup(func() { runtimeAssetFiles = files })
			failure := errors.New("injected " + phase + " failure")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch phase {
			case "parent mkdir":
				runtimeAssetFiles.mkdirAll = func(path string, mode os.FileMode) error {
					if filepath.Base(path) == pythonEnvironmentsDirectory {
						return failure
					}
					return files.mkdirAll(path, mode)
				}
			case "parent invalid":
				if err := os.Symlink(t.TempDir(), filepath.Join(cache, pythonEnvironmentsDirectory)); err != nil {
					t.Fatal(err)
				}
			case "environment mkdir", "pointer mkdir":
				runtimeAssetFiles.mkdirTemp = func(dir, pattern string) (string, error) {
					if (phase == "environment mkdir" && pattern == "env-") || (phase == "pointer mkdir" && pattern == ".python-env-pointer-") {
						return "", failure
					}
					return files.mkdirTemp(dir, pattern)
				}
			case "pointer write", "cancel":
				runtimeAssetFiles.writeFile = func(path string, data []byte, mode os.FileMode) error {
					if phase == "pointer write" && filepath.Base(path) == "active" {
						return failure
					}
					if phase == "cancel" && filepath.Base(path) == filepath.Base(root.ReadyMarkerPath) {
						cancel()
					}
					return files.writeFile(path, data, mode)
				}
			case "publish", "rollback":
				runtimeAssetFiles.rename = func(from, to string) error {
					if phase == "publish" && to == root.Root && strings.HasPrefix(filepath.Base(from), ".runtime-stage-") {
						return failure
					}
					if phase == "rollback" && (to == filepath.Join(cache, pythonEnvironmentMarker) || (from == root.Root && strings.HasPrefix(filepath.Base(to), ".runtime-stage-"))) {
						return failure
					}
					return files.rename(from, to)
				}
			}
			err = EnsureInstalled(ctx, InstallOptions{VenvDir: cache, Runtime: RuntimePython, ForceReinstall: true})
			if err == nil {
				t.Fatal("failed staging reported success")
			}
			if phase == "rollback" && !strings.Contains(err.Error(), "previous asset retained at") {
				t.Fatalf("recovery backup missing from error: %v", err)
			}
			runtimeAssetFiles = files
			if got, err := VenvPython(cache); err != nil || got != previous {
				t.Fatalf("old Python selection changed: %q %v", got, err)
			}
			if _, err := os.Stat(previous); err != nil {
				t.Fatal(err)
			}
			if phase != "rollback" {
				after, err := treeSHA256(root.Root)
				if err != nil || after != before {
					t.Fatalf("old runtime changed: %v", err)
				}
			}
		})
	}
}

func stubStagedPythonEnvironment(t *testing.T, cache string) *[]string {
	t.Helper()
	source := fakeCompatibleVenv(t, fakePlaywrightPackage(t, RequiredPlaywright), RequiredCamoufox)
	if err := copyTree(filepath.Join(source, "bin"), filepath.Join(cache, "bin")); err != nil {
		t.Fatal(err)
	}
	find, venv, packages := findPythonForInstall, ensureVenvForInstall, ensureCamoufoxForInstall
	t.Cleanup(func() { findPythonForInstall, ensureVenvForInstall, ensureCamoufoxForInstall = find, venv, packages })
	var created []string
	findPythonForInstall = func(string) (string, error) { return "python", nil }
	ensureVenvForInstall = func(_ context.Context, _, dir string) error {
		created = append(created, dir)
		return copyTree(filepath.Join(source, "bin"), filepath.Join(dir, "bin"))
	}
	ensureCamoufoxForInstall = func(context.Context, string, InstallOptions) error { return nil }
	return &created
}

func TestPythonEnvironmentPublicationRetainsInterpreterPathsAndOldEnvironments(t *testing.T) {
	cache, _ := inspectionFixture(t)
	created := stubStagedPythonEnvironment(t, cache)
	previous, err := VenvPython(cache)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: cache, Runtime: RuntimePython, ForceReinstall: true}); err != nil {
			t.Fatal(err)
		}
		python, err := VenvPython(cache)
		if err != nil || python != filepath.Join((*created)[i], "bin", "python") || python == previous {
			t.Fatalf("published interpreter = %q, %v; created = %v", python, err, *created)
		}
		pip, err := VenvPip(cache)
		if err != nil || pip != filepath.Join((*created)[i], "bin", "pip") {
			t.Fatalf("published pip = %q, %v", pip, err)
		}
		if _, err := os.Stat(previous); err != nil {
			t.Fatalf("previous environment was removed: %v", err)
		}
		if err := CheckCompatibility(context.Background(), python); err != nil {
			t.Fatalf("published interpreter unusable: %v", err)
		}
		previous = python
	}
	if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: cache, Runtime: RuntimePython}); err != nil {
		t.Fatal(err)
	}
	if len(*created) != 2 {
		t.Fatalf("compatible environment unnecessarily replaced: %v", *created)
	}
}

func TestPythonEnvironmentFailureDoesNotPublishPackagesOrRuntime(t *testing.T) {
	for _, phase := range []string{"packages", "runtime", "pointer"} {
		t.Run(phase, func(t *testing.T) {
			cache, root := inspectionFixture(t)
			created := stubStagedPythonEnvironment(t, cache)
			if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: cache, Runtime: RuntimePython, ForceReinstall: true}); err != nil {
				t.Fatal(err)
			}
			before, err := treeSHA256(root.Root)
			if err != nil {
				t.Fatal(err)
			}
			oldPython, _ := VenvPython(cache)
			oldPointer, err := os.ReadFile(filepath.Join(cache, pythonEnvironmentMarker))
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("injected " + phase + " failure")
			files := runtimeAssetFiles
			t.Cleanup(func() { runtimeAssetFiles = files })
			switch phase {
			case "packages":
				ensureCamoufoxForInstall = func(context.Context, string, InstallOptions) error { return failure }
			case "runtime":
				installPlaywrightDriverForRuntime = func(context.Context, RuntimeRoot, InstallOptions) error { return failure }
			case "pointer":
				runtimeAssetFiles.rename = func(from, to string) error {
					if to == filepath.Join(cache, pythonEnvironmentMarker) {
						return failure
					}
					return os.Rename(from, to)
				}
			}
			if err := EnsureInstalled(context.Background(), InstallOptions{VenvDir: cache, Runtime: RuntimePython, ForceReinstall: true}); !errors.Is(err, failure) {
				t.Fatalf("install = %v", err)
			}
			if python, err := VenvPython(cache); err != nil || python != oldPython {
				t.Fatalf("active interpreter changed: %q, %v", python, err)
			}
			if pointer, err := os.ReadFile(filepath.Join(cache, pythonEnvironmentMarker)); err != nil || string(pointer) != string(oldPointer) {
				t.Fatalf("active marker changed: %q, %v", pointer, err)
			}
			if after, err := treeSHA256(root.Root); err != nil || before != after {
				t.Fatalf("runtime changed: %s != %s, %v", before, after, err)
			}
			if _, _, err := ResolveRuntimeAssets(cache); err != nil {
				t.Fatalf("previous runtime unusable: %v", err)
			}
			if len(*created) != 2 {
				t.Fatalf("created = %v", *created)
			}
			if _, err := os.Stat((*created)[1]); !os.IsNotExist(err) {
				t.Fatalf("unpublished environment retained: %v", err)
			}
			if matches, _ := filepath.Glob(filepath.Join(cache, ".python-env-pointer-*")); len(matches) != 0 {
				t.Fatalf("unpublished markers retained: %v", matches)
			}
		})
	}
}

func TestPythonEnvironmentMarkerValidation(t *testing.T) {
	for _, value := range []string{"", "../env-outside", "env-../escape", "env-\\escape", "env-\x00", strings.Repeat("env-x", 300), "env-missing"} {
		t.Run(strings.ReplaceAll(value[:min(len(value), 25)], "\x00", "NUL"), func(t *testing.T) {
			cache := t.TempDir()
			if err := os.WriteFile(filepath.Join(cache, pythonEnvironmentMarker), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := pythonEnvironmentDir(cache); err == nil {
				t.Fatal("invalid marker accepted")
			}
		})
	}
	for _, component := range []string{"marker", "parent", "environment"} {
		t.Run(component+" symlink", func(t *testing.T) {
			cache := t.TempDir()
			parent := filepath.Join(cache, pythonEnvironmentsDirectory)
			environment := filepath.Join(parent, "env-active")
			marker := filepath.Join(cache, pythonEnvironmentMarker)
			if err := os.MkdirAll(environment, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte("env-active\n"), 0600); err != nil {
				t.Fatal(err)
			}
			path := map[string]string{"marker": marker, "parent": parent, "environment": environment}[component]
			if err := os.Rename(path, path+"-old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+"-old", path); err != nil {
				t.Fatal(err)
			}
			if _, err := pythonEnvironmentDir(cache); err == nil {
				t.Fatal("symlink accepted")
			}
		})
	}
}
