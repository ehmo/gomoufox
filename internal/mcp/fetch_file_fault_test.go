package mcp

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehmo/gomoufox/internal/policy"
)

func TestFetchFileRejectsPathsChangedAfterPreflight(t *testing.T) {
	for _, phase := range []string{"confined", "root removed", "file removed"} {
		t.Run(phase, func(t *testing.T) {
			server := newTestServer(t, defaultTestConfig(t))
			path := filepath.Join(server.jail.Root, "body.bin")
			if err := os.WriteFile(path, []byte("body"), 0600); err != nil {
				t.Fatal(err)
			}
			confined := mcpConfinedPath
			t.Cleanup(func() { mcpConfinedPath = confined })
			mcpConfinedPath = func(j policy.Jail, resolved string) (string, error) {
				rel, err := confined(j, resolved)
				if err != nil {
					return "", err
				}
				switch phase {
				case "confined":
					return "", errors.New("confinement failed")
				case "root removed":
					err = os.RemoveAll(j.Root)
				case "file removed":
					err = os.Remove(resolved)
				}
				if err != nil {
					t.Fatal(err)
				}
				return rel, nil
			}
			if data, code := server.readFetchBodyFile("body.bin"); code != "path_rejected" || data != nil {
				t.Fatalf("changed path = %q %s", data, code)
			}
		})
	}
}

type fetchBodyReader struct {
	io.Reader
	info    os.FileInfo
	statErr error
}

func (f fetchBodyReader) Stat() (os.FileInfo, error) { return f.info, f.statErr }

type fetchBodyReadError struct{}

func (fetchBodyReadError) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type fetchBodySize struct {
	os.FileInfo
	size int64
}

func (f fetchBodySize) Size() int64 { return f.size }

func TestFetchFileRechecksTypeSizeAndStreamingLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body")
	if err := os.WriteFile(path, []byte("body"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, code string
		input      fetchBodyReader
	}{
		{"stat", "path_rejected", fetchBodyReader{statErr: errors.New("stat failed")}},
		{"directory", "path_rejected", fetchBodyReader{info: directory}},
		{"grew before stat", "file_too_large", fetchBodyReader{info: fetchBodySize{info, maxUploadFileBytes + 1}}},
		{"read", "file_read_failed", fetchBodyReader{info: info, Reader: fetchBodyReadError{}}},
		{"grew after stat", "file_too_large", fetchBodyReader{info: info, Reader: io.LimitReader(zeroBodyReader{}, maxUploadFileBytes+1)}},
		{"success", "", fetchBodyReader{info: info, Reader: bytes.NewReader([]byte("body"))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, code := readFetchBody(tc.input)
			if code != tc.code || (code != "" && data != nil) || (code == "" && string(data) != "body") {
				t.Fatalf("file read = %d bytes, %s", len(data), code)
			}
		})
	}
}

type zeroBodyReader struct{}

func (zeroBodyReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
