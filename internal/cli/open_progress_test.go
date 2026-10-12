package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox"
	"github.com/playwright-community/playwright-go"
)

func TestLocalOpenWritesReadyLineBeforeClose(t *testing.T) {
	for _, tc := range []struct {
		name string
		json bool
		want string
	}{
		{name: "plain", want: "https://example.com/final\n"},
		{name: "json", json: true, want: `{"event":"window_ready","title":"Final","url":"https://example.com/final"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := &fakeLocalPage{
				url:         "https://example.com/final",
				title:       "Final",
				closeWait:   make(chan struct{}),
				waitEntered: make(chan struct{}),
			}
			entered := page.waitEntered
			t.Cleanup(fakeOpenPage(t, page))

			var progress bytes.Buffer
			type result struct {
				resp LocalCommandResponse
				err  error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
					Command:  "open",
					Args:     []string{"https://example.com"},
					Flags:    map[string]any{},
					JSON:     tc.json,
					Progress: &progress,
				})
				done <- result{resp, err}
			}()

			<-entered
			if progress.String() != tc.want {
				t.Fatalf("progress before close = %q, want %q", progress.String(), tc.want)
			}

			close(page.closeWait)
			got := <-done
			if got.err != nil {
				t.Fatal(got.err)
			}
			if bytes.Contains(got.resp.Stdout, []byte("window_ready")) || bytes.Contains(got.resp.Stdout, []byte("https://example.com/final\n")) && !tc.json {
				t.Fatalf("ready line repeated at close: %q", got.resp.Stdout)
			}
		})
	}
}

func TestLocalOpenJSONCloseEvent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "state.json")
	t.Cleanup(fakeOpenPage(t, &fakeLocalPage{url: "https://example.com/final"}))
	var progress bytes.Buffer
	resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command:  "open",
		Args:     []string{"https://example.com"},
		Flags:    map[string]any{"save_session": out},
		JSON:     true,
		Progress: &progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(resp.Stdout, &event); err != nil {
		t.Fatalf("close output %q is not JSON: %v", resp.Stdout, err)
	}
	if event["event"] != "closed" || event["session_path"] != out || event["url"] != "https://example.com/final" {
		t.Fatalf("close event = %v", event)
	}
}

func TestLocalOpenWithoutProgressKeepsBufferedOutput(t *testing.T) {
	t.Cleanup(fakeOpenPage(t, &fakeLocalPage{url: "https://example.com/final"}))
	resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "open",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{},
	})
	if err != nil || string(resp.Stdout) != "https://example.com/final\n" {
		t.Fatalf("stdout = %q err = %v", resp.Stdout, err)
	}
}

func TestExecuteLocalCommandStreamsOpenProgress(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := Runner{Hooks: Hooks{LocalCommand: func(_ context.Context, req LocalCommandRequest) (LocalCommandResponse, error) {
		if req.Progress != &stdout {
			t.Errorf("progress writer = %v, want stdout", req.Progress)
		}
		return LocalCommandResponse{ExitCode: ExitOK}, nil
	}}}
	parsed, err := parseOpen([]string{"https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if code := runner.executeLocalCommand(context.Background(), globalFlags{}, "open", parsed, Streams{Stdout: &stdout, Stderr: &stderr}); code != ExitOK {
		t.Fatalf("code = %d stderr = %q", code, stderr.String())
	}
}

// A browser quit (Cmd+Q on macOS) closes the page before StorageState runs,
// and Playwright then fails with "window is null" while opening helper pages.
func TestSaveSessionFallsBackToOpenSnapshot(t *testing.T) {
	snapshot := &gomoufox.StorageState{
		Cookies: []gomoufox.Cookie{{Name: "sid", Value: "authenticated", Domain: "127.0.0.1", Path: "/", Expires: -1}},
		Origins: []gomoufox.Origin{{Origin: "http://127.0.0.1:18765", LocalStorage: []gomoufox.LSEntry{{Name: "fixture-login", Value: "authenticated"}}}},
	}
	for _, command := range []string{"open", "record"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "state.json")
			page := &fakeLocalPage{
				url:        "http://127.0.0.1:18765/",
				storageErr: errors.New("playwright: Protocol error (Browser.newPage): window is null"),
				snapshot:   snapshot,
			}
			flags := map[string]any{"save_session": out}
			if command == "record" {
				harPath := filepath.Join(dir, "capture.har")
				if err := os.WriteFile(harPath, []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				flags["out"] = harPath
			}
			t.Cleanup(fakeOpenPage(t, page))

			resp, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
				Command: command,
				Args:    []string{"http://127.0.0.1:18765/"},
				Flags:   flags,
			})
			if err != nil {
				t.Fatalf("%s save-session err = %v", command, err)
			}
			if !strings.Contains(resp.Stderr, "window is null") {
				t.Fatalf("stderr = %q, want the fallback note", resp.Stderr)
			}

			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			var saved gomoufox.StorageState
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if len(saved.Cookies) != 1 || saved.Cookies[0].Value != "authenticated" || len(saved.Origins) != 1 || saved.Origins[0].LocalStorage[0].Name != "fixture-login" {
				t.Fatalf("saved state = %s", data)
			}
		})
	}
}

func TestMergeSessionSnapshot(t *testing.T) {
	older := &gomoufox.StorageState{
		Cookies: []gomoufox.Cookie{{Name: "old"}},
		Origins: []gomoufox.Origin{
			{Origin: "https://a.example", LocalStorage: []gomoufox.LSEntry{{Name: "a", Value: "1"}}},
			{Origin: "https://b.example", LocalStorage: []gomoufox.LSEntry{{Name: "b", Value: "1"}}},
		},
	}
	newer := &gomoufox.StorageState{
		Cookies: []gomoufox.Cookie{{Name: "new"}},
		Origins: []gomoufox.Origin{{Origin: "https://b.example"}},
	}
	merged := mergeSessionSnapshot(older, newer)
	if len(merged.Cookies) != 1 || merged.Cookies[0].Name != "new" {
		t.Fatalf("cookies = %v", merged.Cookies)
	}
	if len(merged.Origins) != 1 || merged.Origins[0].Origin != "https://a.example" {
		t.Fatalf("origins = %v, want b cleared and a kept", merged.Origins)
	}
	if got := mergeSessionSnapshot(nil, newer); len(got.Cookies) != 1 {
		t.Fatalf("merge into nil = %v", got)
	}
}

func TestRealSessionSnapshot(t *testing.T) {
	cookies := []playwright.Cookie{{Name: "sid", Value: "1", Domain: "example.com", Path: "/", Expires: -1}}
	storage := &fakePlaywrightStorageContext{cookies: cookies}
	page := &fakeGomoufoxPage{
		raw:           &fakePlaywrightPageRaw{context: storage},
		evaluateValue: map[string]any{"origin": "https://example.com", "entries": []any{[]any{"token", "abc"}}},
	}
	state, err := (realLocalPage{p: page}).SessionSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Cookies) != 1 || state.Cookies[0].Name != "sid" {
		t.Fatalf("cookies = %v", state.Cookies)
	}
	if len(state.Origins) != 1 || state.Origins[0].Origin != "https://example.com" || state.Origins[0].LocalStorage[0].Value != "abc" {
		t.Fatalf("origins = %v", state.Origins)
	}

	// A closed page or a non-HTTP origin still yields cookies, without origins.
	for _, tc := range []struct {
		name  string
		value any
		err   error
	}{
		{name: "evaluate error", err: errors.New("target closed")},
		{name: "opaque origin", value: map[string]any{"origin": "null", "entries": []any{}}},
		{name: "bad shape", value: map[string]any{"origin": 7}},
		{name: "unencodable", value: func() {}},
	} {
		page.evaluateValue, page.evaluateErr = tc.value, tc.err
		state, err := (realLocalPage{p: page}).SessionSnapshot(context.Background())
		if err != nil || len(state.Cookies) != 1 || len(state.Origins) != 0 {
			t.Fatalf("%s: state = %v err = %v", tc.name, state, err)
		}
	}

	storage.cookies = []playwright.Cookie{{Name: "bad", Expires: math.NaN()}}
	if _, err := (realLocalPage{p: page}).SessionSnapshot(context.Background()); err == nil {
		t.Fatal("unencodable cookie accepted")
	}

	storage.cookiesErr = errors.New("context closed")
	if _, err := (realLocalPage{p: page}).SessionSnapshot(context.Background()); !errors.Is(err, storage.cookiesErr) {
		t.Fatalf("cookies err = %v", err)
	}
	if _, err := (realLocalPage{p: &fakeGomoufoxPage{raw: struct{}{}}}).SessionSnapshot(context.Background()); err == nil {
		t.Fatal("unsupported raw succeeded")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (realLocalPage{p: page}).SessionSnapshot(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled err = %v", err)
	}
}

func TestSaveSessionWithoutSnapshotReturnsExportError(t *testing.T) {
	exportErr := errors.New("window is null")
	page := &fakeLocalPage{url: "https://example.com", storageErr: exportErr, snapshotErr: errors.New("closed")}
	t.Cleanup(fakeOpenPage(t, page))
	_, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "open",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"save_session": filepath.Join(t.TempDir(), "state.json")},
	})
	if !errors.Is(err, exportErr) {
		t.Fatalf("err = %v, want export error", err)
	}

	page = &fakeLocalPage{url: "https://example.com", waitClosedErr: errors.New("wait failed")}
	t.Cleanup(fakeOpenPage(t, page))
	if _, err := defaultLocalCommand(context.Background(), LocalCommandRequest{
		Command: "open",
		Args:    []string{"https://example.com"},
		Flags:   map[string]any{"save_session": filepath.Join(t.TempDir(), "state.json")},
	}); err == nil || err.Error() != "wait failed" {
		t.Fatalf("wait err = %v", err)
	}
}
