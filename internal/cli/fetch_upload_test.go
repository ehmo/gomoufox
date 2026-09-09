package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/policy"
)

func TestFetchFileInputBypassesInlineTextBudget(t *testing.T) {
	data := bytes.Repeat([]byte{0, 255, 1, 128}, 512*1024)
	path := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := bodyFromFlags(LocalCommandRequest{Flags: map[string]any{"data_file": path}})
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("binary bytes=%d, err=%v", len(got), err)
	}
	if err := os.Truncate(path, gomoufox.MaxTransferFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := bodyFromFlags(LocalCommandRequest{Flags: map[string]any{"data_file": path}}); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestOversizedScriptsAndBodiesPointToFileRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.js")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 65537)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := scriptFromFlags(LocalCommandRequest{Flags: map[string]any{"script_file": path}}); err == nil || !strings.Contains(err.Error(), "--form-file") || !strings.Contains(err.Error(), "65536") {
		t.Fatalf("script hint=%v", err)
	}
	if _, err := parseFetch([]string{"https://example.com", "--data", strings.Repeat("x", 1024*1024+1)}); err == nil || !strings.Contains(err.Error(), "--data-file") {
		t.Fatalf("body hint=%v", err)
	}
}

func TestFetchMultipartPreservesBinaryAndRepeatedFields(t *testing.T) {
	data := bytes.Repeat([]byte{0, 255, 1, 128}, 512*1024)
	path := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	page := &fakeLocalPage{fetchStatus: 200, fetchBody: []byte("ok")}
	restore := fakeOpenPage(t, page)
	defer restore()
	_, err := localFetch(context.Background(), LocalCommandRequest{Command: "fetch", Args: []string{"https://example.com/upload"}, Flags: map[string]any{"form": []string{"tag=first", "tag=second"}, "form_file": []string{"image=" + path}}})
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(page.fetchHeaders["Content-Type"])
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(bytes.NewReader(page.fetchRequest), params["boundary"])
	for _, want := range []string{"first", "second"} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(part)
		if part.FormName() != "tag" || string(got) != want {
			t.Fatalf("field=%q %q", part.FormName(), got)
		}
	}
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(part)
	if part.FormName() != "image" || part.FileName() != "upload.bin" || !bytes.Equal(got, data) || page.fetchMethod != "POST" {
		t.Fatalf("file name=%q bytes=%d method=%s", part.FileName(), len(got), page.fetchMethod)
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatalf("trailing multipart: %v", err)
	}
}

func TestMultipartRejectsConflictingHeadersAndBodyModes(t *testing.T) {
	for _, extra := range [][]string{
		{"--data", "inline"}, {"--data-file", "file"}, {"--content-type", "multipart/form-data"},
		{"--header", "cOnTent-TyPe: multipart/form-data"}, {"--method", "GET"}, {"--method", "HEAD"},
		{"--form", "missing-separator"}, {"--form", "bad\r\nname=value"},
	} {
		args := append([]string{"https://example.com", "--form-file", "image=file"}, extra...)
		if _, err := parseFetch(args); err == nil {
			t.Fatalf("invalid multipart accepted: %q", args)
		}
	}
	if _, err := parseFetch([]string{"https://example.com", "--form", "a=b", "--form-file", "image=file"}); err != nil {
		t.Fatal(err)
	}
	buf := &boundedFormBuffer{}
	if _, err := buf.Write(make([]byte, gomoufox.MaxTransferFileBytes)); err != nil {
		t.Fatal(err)
	}
	if _, err := buf.Write([]byte{0}); err == nil {
		t.Fatal("multipart exceeded the aggregate budget")
	}
}

func TestMultipartRejectsInvalidInputBeforeFetch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "part.bin")
	if err := os.WriteFile(file, []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, flags := range []map[string]any{
		{"form": make([]string, 129)}, {"form_file": make([]string, 33)},
		{"form": []string{"bad"}}, {"form": []string{"a=" + strings.Repeat("x", policy.HardMaxInputBytes)}},
		{"form_file": []string{"bad"}}, {"form_file": []string{"a="}},
		{"form_file": []string{"a=bad\nname"}}, {"form_file": []string{"a=" + file + "-missing"}},
		{"form_file": []string{"a=" + filepath.Dir(file)}},
	} {
		if _, _, err := multipartBody(LocalCommandRequest{Flags: flags}); err == nil {
			t.Fatalf("invalid input accepted: %v", flags)
		}
	}
	args := []string{"https://example.com"}
	for i := 0; i < 129; i++ {
		args = append(args, "--form", "a=b")
	}
	if _, err := parseFetch(args); err == nil {
		t.Fatal("too many fields accepted by CLI")
	}
	page := &fakeLocalPage{fetchStatus: 200, fetchBody: []byte("ok")}
	t.Cleanup(fakeOpenPage(t, page))
	if _, err := localFetch(context.Background(), LocalCommandRequest{Args: []string{"https://example.com"}, Flags: map[string]any{"form_file": []string{"a=" + file + "-missing"}}}); err == nil {
		t.Fatal("file input failure swallowed")
	}
	if _, err := localFetch(context.Background(), LocalCommandRequest{Args: []string{"https://example.com"}, Flags: map[string]any{"out": filepath.Dir(file)}}); err == nil {
		t.Fatal("download output failure swallowed")
	}
}

type failingMultipartWriter struct {
	remaining int
	target    io.Writer
	err       error
}

func (w *failingMultipartWriter) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		return 0, w.err
	}
	w.remaining--
	return w.target.Write(p)
}

func TestMultipartReportsEncodingAndFileFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "part.bin")
	if err := os.WriteFile(path, []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	writer, open, closeInput := newMultipartWriter, localOpenFile, closeMultipartInput
	t.Cleanup(func() { newMultipartWriter, localOpenFile, closeMultipartInput = writer, open, closeInput })
	failure := errors.New("multipart I/O failed")
	for _, phase := range []string{"field write", "file header", "file write", "body close", "file stat", "file close", "file read"} {
		t.Run(phase, func(t *testing.T) {
			newMultipartWriter, localOpenFile, closeMultipartInput = writer, open, closeInput
			flags := map[string]any{"form_file": []string{"a=" + path}}
			switch phase {
			case "field write", "file header", "file write", "body close":
				remaining := 0
				if phase == "field write" {
					flags = map[string]any{"form": []string{"a=b"}}
				}
				if phase == "file write" {
					remaining = 1
				}
				if phase == "body close" {
					flags = nil
				}
				newMultipartWriter = func(w io.Writer) *multipart.Writer {
					return multipart.NewWriter(&failingMultipartWriter{remaining: remaining, target: w, err: failure})
				}
			case "file stat":
				localOpenFile = func(path string) (*os.File, error) {
					f, err := os.Open(path)
					if err == nil {
						_ = f.Close()
					}
					return f, err
				}
			case "file close":
				closeMultipartInput = func(f *os.File) error { _ = f.Close(); return failure }
			case "file read":
				localOpenFile = func(path string) (*os.File, error) { return os.OpenFile(path, os.O_WRONLY, 0600) }
			}
			if _, _, err := multipartBody(LocalCommandRequest{Flags: flags}); err == nil {
				t.Fatal("multipart failure reported success")
			}
		})
	}
}
