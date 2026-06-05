package sidecar

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestVersionPinsMatchGoModAndDocs(t *testing.T) {
	root := repoRoot(t)
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\s*github\.com/playwright-community/playwright-go\s+(\S+)`)
	match := re.FindSubmatch(goMod)
	if match == nil {
		t.Fatalf("go.mod missing playwright-go requirement\n%s", goMod)
	}
	if string(match[1]) != PlaywrightGoVersion {
		t.Fatalf("PlaywrightGoVersion = %s, go.mod requires %s", PlaywrightGoVersion, match[1])
	}

	if len(VersionMatrix) != 1 {
		t.Fatalf("VersionMatrix length = %d want 1", len(VersionMatrix))
	}
	entry := VersionMatrix[0]
	if entry.CamoufoxVersion != RequiredCamoufox || entry.PlaywrightProto != RequiredPlaywright || entry.PlaywrightGo != PlaywrightGoVersion {
		t.Fatalf("VersionMatrix does not mirror constants: %#v", entry)
	}

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	wantRow := fmt.Sprintf("| v0.1.x | %s | %s | %s | %s |", PlaywrightGoVersion, RequiredPlaywright, RequiredCamoufox, CamoufoxBinaryVersion)
	if !strings.Contains(string(readme), wantRow) {
		t.Fatalf("README compatibility table missing %q", wantRow)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
