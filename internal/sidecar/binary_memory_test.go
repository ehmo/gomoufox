package sidecar

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCamoufoxBrowserManifestBoundsLargeFileAllocation(t *testing.T) {
	const fileSize = 32 << 20
	root := t.TempDir()
	file, err := os.Create(filepath.Join(root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(fileSize); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	contentHash := sha256.New()
	var zeros [32 << 10]byte
	for written := 0; written < fileSize; written += len(zeros) {
		_, _ = contentHash.Write(zeros[:])
	}
	record := fmt.Sprintf("%04o %d %x large.bin", info.Mode().Perm(), fileSize, contentHash.Sum(nil))
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(record)))

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := camoufoxBrowserManifestSHA256(root)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("manifest checksum = %s, want %s", got, want)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("hashing %d bytes allocated %d bytes", fileSize, allocated)
	if allocated > 4<<20 {
		t.Fatalf("hashing a %d-byte browser file allocated %d bytes; budget is 4 MiB", fileSize, allocated)
	}
}
