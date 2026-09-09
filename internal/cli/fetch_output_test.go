package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox/internal/daemon"
	"github.com/ehmo/gomoufox/internal/policy"
)

func TestFetchTruncationCannotLookLikeCompleteBinary(t *testing.T) {
	restore := fakeOpenPage(t, &fakeLocalPage{fetchStatus: 200, fetchBody: []byte{0, 255, 1, 2}})
	defer restore()
	resp, err := localFetch(context.Background(), LocalCommandRequest{Command: "fetch", Args: []string{"https://example.com/file"}, Flags: map[string]any{"max_bytes": "2"}})
	if err == nil || len(resp.Stdout) != 0 || !strings.Contains(err.Error(), "--out") {
		t.Fatalf("truncated binary looked successful: %#v, %v", resp, err)
	}
}

func TestFetchFileOutputIsCompleteOrPreservesDestination(t *testing.T) {
	data := strings.Repeat("x", policy.HardMaxResponseBytes+1)
	page := &fakeLocalPage{fetchStatus: 200, fetchBody: []byte(data)}
	restore := fakeOpenPage(t, page)
	defer restore()
	path := filepath.Join(t.TempDir(), "artifact.bin")
	req := LocalCommandRequest{Command: "fetch", Args: []string{"https://example.com/file"}, JSON: true, Flags: map[string]any{"out": path, "max_file_bytes": "1048576"}}
	resp, err := localFetch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != data {
		t.Fatalf("file length=%d, %v", len(got), err)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Stdout, &result); err != nil || result["saved"] != true || result["body"] != nil || result["sha256"] == "" {
		t.Fatalf("result=%s, %v", resp.Stdout, err)
	}
	req.Flags["overwrite"] = true
	req.Flags["max_file_bytes"] = "3"
	if _, err := localFetch(context.Background(), req); err == nil {
		t.Fatal("truncated file succeeded")
	}
	got, _ = os.ReadFile(path)
	if string(got) != data {
		t.Fatal("truncation replaced the existing destination")
	}
	page.fetchStatus = 404
	req.Flags["max_file_bytes"] = "1048576"
	if _, err := localFetch(context.Background(), req); err == nil {
		t.Fatal("HTTP error saved as artifact")
	}
	got, _ = os.ReadFile(path)
	if string(got) != data {
		t.Fatal("HTTP error replaced the existing destination")
	}
}

func TestFetchFileFlagsValidateAndForward(t *testing.T) {
	for _, args := range [][]string{
		{"--max-file-bytes", "1"}, {"--overwrite"}, {"--out", ""},
		{"--out", "file", "--raw"}, {"--out", "file", "--max-bytes", "10"},
		{"--out", "file", "--max-file-bytes", "52428801"}, {"--out", "file", "--max-file-bytes", "0"},
	} {
		if _, err := parseFetch(append([]string{"https://example.com/file"}, args...)); err == nil {
			t.Fatalf("invalid file flags accepted: %v", args)
		}
	}
	flags, err := normalizeDaemonFlags("fetch", map[string]any{"out": "file.bin", "max_file_bytes": "1048576", "overwrite": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDaemonCommandSyntax(LocalCommandRequest{Command: "fetch", Args: []string{"https://example.com/file"}, Flags: flags}); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonFetchRejectsFilePathEscapes(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "payload.bin")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	traversal, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"absolute": outside, "traversal": traversal}
	if err := os.Symlink(outsideDir, filepath.Join(root, "linked-dir")); err == nil {
		paths["parent-symlink"] = filepath.Join("linked-dir", "payload.bin")
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-file")); err == nil {
		paths["file-symlink"] = "linked-file"
	}
	for flag := range map[string]bool{"out": true, "data_file": true, "form_file": true, "cookies_file": true} {
		for name, path := range paths {
			t.Run(flag+"/"+name, func(t *testing.T) {
				if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
				page := &fakeLocalPage{fetchStatus: 200, fetchBody: []byte("replacement")}
				t.Cleanup(fakeOpenPage(t, page))
				flags := map[string]any{flag: path}
				if flag == "out" {
					flags["overwrite"] = true
				}
				if flag == "form_file" {
					flags[flag] = []string{"file=" + path}
				}
				result := executeDaemonLocalCommand(context.Background(), ServeRequest{SessionDir: root}, "fetch", daemon.Envelope{Args: []string{"http://93.184.216.34/file"}, Flags: flags})
				got, err := os.ReadFile(outside)
				if err != nil || string(got) != "original" {
					t.Fatalf("outside file changed: %q, %v; daemon=%#v", got, err, result)
				}
				assertDaemonPathRejected(t, result, root, outsideDir)
				if page.fetchURL != "" {
					t.Fatalf("rejected file path reached fetch: %s", page.fetchURL)
				}
			})
		}
	}
}

func TestDaemonFetchUsesConfinedFiles(t *testing.T) {
	root := t.TempDir()
	data := []byte{0, 255, 1, 128}
	if err := os.WriteFile(filepath.Join(root, "upload.bin"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cookies.json"), []byte(`{"cookies":[],"origins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	page := &fakeLocalPage{fetchStatus: 200, fetchBody: data}
	t.Cleanup(fakeOpenPage(t, page))
	serve := ServeRequest{SessionDir: root}
	envelope := daemon.Envelope{Args: []string{"http://93.184.216.34/file"}, JSON: true, Flags: map[string]any{"out": "download.bin", "data_file": "upload.bin", "cookies_file": "cookies.json"}}
	result := executeDaemonLocalCommand(context.Background(), serve, "fetch", envelope)
	assertDaemonJSONPath(t, result, "download.bin", root)
	got, err := os.ReadFile(filepath.Join(root, "download.bin"))
	if err != nil || !bytes.Equal(got, data) || !bytes.Equal(page.fetchRequest, data) {
		t.Fatalf("file transfer got=%v uploaded=%v err=%v", got, page.fetchRequest, err)
	}
	page.fetchBody = []byte("replacement")
	result = executeDaemonLocalCommand(context.Background(), serve, "fetch", envelope)
	assertDaemonPathRejected(t, result, root)
	got, err = os.ReadFile(filepath.Join(root, "download.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("refused overwrite changed file: %v, %v", got, err)
	}
	envelope.Flags["overwrite"] = true
	result = executeDaemonLocalCommand(context.Background(), serve, "fetch", envelope)
	assertDaemonJSONPath(t, result, "download.bin", root)
	got, err = os.ReadFile(filepath.Join(root, "download.bin"))
	if err != nil || string(got) != "replacement" {
		t.Fatalf("permitted overwrite: %q, %v", got, err)
	}

	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	envelope.Flags = map[string]any{"form_file": []string{"first=upload.bin", "second=" + filepath.Join(absRoot, "upload.bin")}, "out": filepath.Join(absRoot, "multipart.bin")}
	result = executeDaemonLocalCommand(context.Background(), serve, "fetch", envelope)
	assertDaemonJSONPath(t, result, "multipart.bin", root, absRoot)
	_, params, err := mime.ParseMediaType(page.fetchHeaders["Content-Type"])
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(bytes.NewReader(page.fetchRequest), params["boundary"])
	for _, name := range []string{"first", "second"} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(part)
		if err != nil || part.FormName() != name || part.FileName() != "upload.bin" || !bytes.Equal(got, data) {
			t.Fatalf("multipart part=%#v data=%v err=%v", part, got, err)
		}
	}
}

func TestDaemonFetchPathValidationHandlesMissingAndMalformedFiles(t *testing.T) {
	root := t.TempDir()
	for _, flags := range []map[string]any{
		{"form_file": []string{"missing-separator"}},
		{"form_file": []string{"field="}},
		{"form_file": []string{"field=missing"}},
		{"data_file": "missing"},
		{"cookies_file": "missing"},
		{"out": "missing/output.bin"},
	} {
		result := executeDaemonLocalCommand(context.Background(), ServeRequest{SessionDir: root}, "fetch", daemon.Envelope{Args: []string{"http://93.184.216.34/file"}, Flags: flags})
		assertDaemonPathRejected(t, result, root)
	}
	page := &fakeLocalPage{fetchStatus: 200, fetchBody: []byte("ok")}
	t.Cleanup(fakeOpenPage(t, page))
	badRoot := filepath.Join(root, "file")
	if err := os.WriteFile(badRoot, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := executeDaemonLocalCommand(context.Background(), ServeRequest{SessionDir: badRoot}, "fetch", daemon.Envelope{Args: []string{"http://93.184.216.34/file"}})
	if result.ExitCode != ExitOK || result.Stdout != "ok" {
		t.Fatalf("fetch without file flags required a file directory: %#v", result)
	}
}
