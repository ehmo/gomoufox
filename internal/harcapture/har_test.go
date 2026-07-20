package harcapture

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const testHAR = `{
  "log": {
    "version": "1.2",
    "creator": {"name": "Playwright", "version": "1.57"},
    "comment": "extension-secret",
    "pages": [{"id":"page_1","title":"page-secret"}],
    "entries": [{
      "startedDateTime": "2026-07-19T00:00:00.000Z",
      "time": 12.5,
      "request": {
        "method": "POST",
        "url": "https://user:pass@example.com/api/items?q=query-secret#fragment-secret",
        "httpVersion": "HTTP/2",
        "cookies": [{"name":"sid","value":"cookie-secret"}],
        "headers": [{"name":"authorization","value":"header-secret"}],
        "queryString": [{"name":"q","value":"query-secret"}],
        "postData": {"mimeType":"application/json","text":"body-secret","params":[{"name":"p","value":"form-secret"}]},
        "headersSize": 20,
        "bodySize": 11,
        "_secretExtension": "request-extension-secret"
      },
      "response": {
        "status": 200,
        "statusText": "status-text-secret",
        "httpVersion": "HTTP/2",
        "cookies": [{"name":"response_sid","value":"response-cookie-secret"}],
        "headers": [{"name":"set-cookie","value":"response-header-secret"}],
        "content": {"size": 99,"mimeType":"application/json; boundary=mime-secret","text":"response-body-secret","encoding":"base64"},
        "redirectURL": "https://example.com/next?token=redirect-secret",
        "headersSize": 30,
        "bodySize": 99,
        "_secretExtension": "response-extension-secret"
      },
      "cache": {"secret":"cache-secret"},
      "timings": {"send":1,"wait":10,"receive":1.5},
      "serverIPAddress": "192.0.2.1",
      "connection": "connection-secret",
      "_secretExtension": "entry-extension-secret"
    }]
  },
  "_secretExtension": "envelope-extension-secret"
}`

func TestMetadataCaptureRedactsAndAllowLists(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "nested", "capture.har")
	recorder, err := Prepare(Options{Destination: destination, URLFilter: "**/api/**"})
	if err != nil {
		t.Fatal(err)
	}
	native := recorder.NativeOptions()
	if native.Mode != "minimal" || native.Content != "omit" || !native.OmitRequestContent || native.URLFilter != "**/api/**" {
		t.Fatalf("native options = %#v", native)
	}
	temporaryDirectory := filepath.Dir(native.Path)
	if st, err := os.Stat(temporaryDirectory); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("temporary directory mode = %v err=%v", st, err)
	}
	if st, err := os.Stat(native.Path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("raw file mode = %v err=%v", st, err)
	}
	if err := os.WriteFile(native.Path, []byte(testHAR), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := recorder.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if result.Capture != CaptureMetadata || result.Entries != 1 || result.Bytes == 0 || len(result.Routes) != 1 || result.RoutesTruncated {
		t.Fatalf("result = %#v", result)
	}
	if result.Routes[0].URL != "https://example.com/api/items?q=%3Credacted%3E" || result.Routes[0].Method != "POST" || result.Routes[0].Status != 200 {
		t.Fatalf("route = %#v", result.Routes[0])
	}
	if _, err := os.Stat(temporaryDirectory); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains: %v", err)
	}
	st, err := os.Stat(destination)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("destination mode = %v err=%v", st, err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"user:pass", "query-secret", "fragment-secret", "cookie-secret", "header-secret",
		"body-secret", "form-secret", "extension-secret", "page-secret", "cache-secret",
		"connection-secret", "status-text-secret", "mime-secret", "192.0.2.1",
	} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("metadata contains %q: %s", secret, data)
		}
	}
	for _, expected := range []string{`"authorization"`, `"set-cookie"`, `redacted`, `"statusText": "OK"`, `"mimeType": "application/json"`} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("metadata missing %q: %s", expected, data)
		}
	}
	if entries, routes, truncated, err := InspectFile(destination, 1); err != nil || entries != 1 || len(routes) != 1 || truncated {
		t.Fatalf("inspect entries=%d routes=%#v truncated=%t err=%v", entries, routes, truncated, err)
	}
}

func TestRouteProjectionStopsAtBoundedUniqueSummary(t *testing.T) {
	entries := make([]rawEntry, RouteSummaryLimit+1000)
	for i := range entries {
		entries[i] = rawEntry{
			Request:  rawRequest{Method: "GET", URL: "https://example.com/api/" + strconv.Itoa(i)},
			Response: rawResponse{Status: http.StatusOK},
		}
	}
	routes, truncated := projectRoutes(entries, RouteSummaryLimit)
	if !truncated || len(routes) != RouteSummaryLimit {
		t.Fatalf("routes=%d truncated=%t", len(routes), truncated)
	}
	if routes[0].URL != "https://example.com/api/0" || routes[len(routes)-1].URL != "https://example.com/api/99" {
		t.Fatalf("route bounds = first %#v last %#v", routes[0], routes[len(routes)-1])
	}
	if routes, truncated := projectRoutes(entries[:1], 0); len(routes) != 0 || !truncated {
		t.Fatalf("zero-limit routes=%#v truncated=%t", routes, truncated)
	}
}

func TestFullCapturePreservesBytes(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "full.har")
	recorder, err := Prepare(Options{Destination: destination, Capture: CaptureFull})
	if err != nil {
		t.Fatal(err)
	}
	native := recorder.NativeOptions()
	if native.Mode != "full" || native.Content != "embed" || native.OmitRequestContent {
		t.Fatalf("native options = %#v", native)
	}
	if err := os.WriteFile(native.Path, []byte(testHAR), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := recorder.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != testHAR || result.Bytes != int64(len(testHAR)) || result.Capture != CaptureFull {
		t.Fatalf("full capture changed: result=%#v bytes=%d", result, len(data))
	}
	second, err := recorder.Finalize()
	if err != nil || !reflect.DeepEqual(second, result) {
		t.Fatalf("second finalize = %#v err=%v", second, err)
	}
}

func TestMetadataCaptureRedactsLegacySemicolonQuery(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "legacy-query.har")
	recorder, err := Prepare(Options{Destination: destination})
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(testHAR,
		"https://user:pass@example.com/api/items?q=query-secret#fragment-secret",
		"https://example.com/api/items?token=url-secret;next=other-secret",
		1,
	)
	if err := os.WriteFile(recorder.NativeOptions().Path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := recorder.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "url-secret") || strings.Contains(string(data), "other-secret") {
		t.Fatalf("legacy query leaked: %s", data)
	}
	if len(result.Routes) != 1 || !strings.Contains(result.Routes[0].URL, "next=%3Credacted%3E") || !strings.Contains(result.Routes[0].URL, "token=%3Credacted%3E") {
		t.Fatalf("legacy route = %#v", result.Routes)
	}
}

func TestCaptureRejectsInvalidOptionsAndDestinations(t *testing.T) {
	dir := t.TempDir()
	tooLongFilter := strings.Repeat("x", MaxFilterBytes+1)
	for _, opts := range []Options{
		{},
		{Destination: filepath.Join(dir, "unknown.har"), Capture: "unknown"},
		{Destination: filepath.Join(dir, "filter.har"), URLFilter: tooLongFilter},
		{Destination: filepath.Join(dir, "nul.har"), URLFilter: "x\x00y"},
		{Destination: filepath.Join(dir, "small.har"), MaxBytes: -1},
		{Destination: filepath.Join(dir, "large.har"), MaxBytes: HardMaxBytes + 1},
	} {
		if recorder, err := Prepare(opts); err == nil || recorder != nil {
			t.Fatalf("Prepare(%#v) = %#v, %v", opts, recorder, err)
		}
	}
	existing := filepath.Join(dir, "existing.har")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(Options{Destination: existing}); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("existing error = %v", err)
	}
	if _, err := Prepare(Options{Destination: dir, Overwrite: true}); !errors.Is(err, ErrDestination) {
		t.Fatalf("directory error = %v", err)
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(dir, "link.har")
		if err := os.Symlink(existing, link); err == nil {
			if _, err := Prepare(Options{Destination: link, Overwrite: true}); !errors.Is(err, ErrDestination) {
				t.Fatalf("symlink error = %v", err)
			}
		}
	}
}

func TestCaptureFailsClosedOnCapInvalidHARAndDestinationRace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string
		max     int64
		wantErr error
	}{
		{name: "cap", data: testHAR, max: 1, wantErr: ErrTooLarge},
		{name: "invalid", data: `{}`, max: DefaultMaxBytes, wantErr: ErrInvalidHAR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "capture.har")
			recorder, err := Prepare(Options{Destination: destination, MaxBytes: tc.max})
			if err != nil {
				t.Fatal(err)
			}
			temporaryDirectory := filepath.Dir(recorder.NativeOptions().Path)
			if err := os.WriteFile(recorder.NativeOptions().Path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := recorder.Finalize(); !errors.Is(err, tc.wantErr) {
				t.Fatalf("finalize error = %v, want %v", err, tc.wantErr)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("destination published: %v", err)
			}
			if _, err := os.Stat(temporaryDirectory); !os.IsNotExist(err) {
				t.Fatalf("temporary directory remains: %v", err)
			}
		})
	}

	destination := filepath.Join(t.TempDir(), "raced.har")
	recorder, err := Prepare(Options{Destination: destination})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recorder.NativeOptions().Path, []byte(testHAR), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("winner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Finalize(); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("race error = %v", err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "winner" {
		t.Fatalf("race destination = %q err=%v", data, err)
	}
}

func TestDiscardRemovesPrivateArtifact(t *testing.T) {
	recorder, err := Prepare(Options{Destination: filepath.Join(t.TempDir(), "capture.har")})
	if err != nil {
		t.Fatal(err)
	}
	temporaryDirectory := filepath.Dir(recorder.NativeOptions().Path)
	if err := recorder.Discard(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporaryDirectory); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains: %v", err)
	}
	if _, err := recorder.Finalize(); !errors.Is(err, ErrDiscarded) {
		t.Fatalf("finalize after discard = %v", err)
	}
}

func TestMalformedHARAndProjectionFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "invalid JSON", data: `{`},
		{name: "missing log", data: `{}`},
		{name: "missing entries", data: `{"log":{"version":"1.2"}}`},
		{name: "incomplete entry", data: `{"log":{"version":"1.2","entries":[{}]}}`},
		{name: "relative request URL", data: strings.Replace(testHAR, "https://user:pass@example.com/api/items?q=query-secret#fragment-secret", "/relative", 1)},
		{name: "invalid request query", data: strings.Replace(testHAR, "https://user:pass@example.com/api/items?q=query-secret#fragment-secret", "https://example.com/api?bad=%zz", 1)},
		{name: "relative redirect URL", data: strings.Replace(testHAR, "https://example.com/next?token=redirect-secret", "/relative", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, _, err := process([]byte(tc.data), CaptureMetadata, RouteSummaryLimit); !errors.Is(err, ErrInvalidHAR) {
				t.Fatalf("process error = %v", err)
			}
		})
	}

	entries := []rawEntry{
		{Request: rawRequest{Method: "GET", URL: "/relative"}, Response: rawResponse{Status: 200}},
		{Request: rawRequest{Method: "GET", URL: "https://example.com/api?q=one"}, Response: rawResponse{Status: 200}},
		{Request: rawRequest{Method: "GET", URL: "https://example.com/api?q=two"}, Response: rawResponse{Status: 200}},
	}
	routes, truncated := projectRoutes(entries, 10)
	if truncated || len(routes) != 1 || routes[0].URL != "https://example.com/api?q=%3Credacted%3E" {
		t.Fatalf("deduplicated routes = %#v truncated=%t", routes, truncated)
	}
	if routes, truncated := projectRoutes(entries[1:], -1); len(routes) != 0 || !truncated {
		t.Fatalf("negative-limit routes = %#v truncated=%t", routes, truncated)
	}
	if got := metadataMIMEType("not a media type;"); got != "" {
		t.Fatalf("invalid MIME type = %q", got)
	}
	if _, err := redactURL("/relative"); err == nil {
		t.Fatal("relative URL was accepted")
	}
	if _, err := redactURL("%"); err == nil {
		t.Fatal("malformed URL was accepted")
	}
}

func TestRawArtifactSafetyChecks(t *testing.T) {
	var recorder *Recorder
	if got := recorder.NativeOptions(); got != (NativeOptions{}) {
		t.Fatalf("nil native options = %#v", got)
	}
	if _, err := recorder.Finalize(); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("nil finalize error = %v", err)
	}
	if err := recorder.Discard(); err != nil {
		t.Fatalf("nil discard = %v", err)
	}

	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.har")
	if _, err := readRawHAR(missing); !errors.Is(err, ErrInvalidHAR) {
		t.Fatalf("missing raw error = %v", err)
	}
	if _, err := readRawHAR(dir); !errors.Is(err, ErrInvalidHAR) {
		t.Fatalf("directory raw error = %v", err)
	}
	large := filepath.Join(dir, "large.har")
	if err := os.WriteFile(large, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(large, HardMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := readRawHAR(large); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large raw error = %v", err)
	}
	if _, _, _, err := InspectFile(missing, RouteSummaryLimit); !errors.Is(err, ErrInvalidHAR) {
		t.Fatalf("inspect missing error = %v", err)
	}
	invalid := filepath.Join(dir, "invalid.har")
	if err := os.WriteFile(invalid, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := InspectFile(invalid, RouteSummaryLimit); !errors.Is(err, ErrInvalidHAR) {
		t.Fatalf("inspect invalid error = %v", err)
	}

	missingRaw, err := Prepare(Options{Destination: filepath.Join(dir, "missing-raw.har")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missingRaw.NativeOptions().Path); err != nil {
		t.Fatal(err)
	}
	if _, err := missingRaw.Finalize(); !errors.Is(err, ErrInvalidHAR) {
		t.Fatalf("finalize missing raw error = %v", err)
	}
}

func TestFinalizeRevalidatesDestinationAndSupportsOverwrite(t *testing.T) {
	t.Run("overwrite regular file", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "capture.har")
		if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		recorder, err := Prepare(Options{Destination: destination, Overwrite: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(recorder.NativeOptions().Path, []byte(testHAR), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := recorder.Finalize(); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(destination); err != nil || string(data) == "old" {
			t.Fatalf("overwrite destination = %q err=%v", data, err)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *Recorder)
	}{
		{
			name: "parent identity changed",
			mutate: func(t *testing.T, recorder *Recorder) {
				other := filepath.Join(t.TempDir(), "other")
				if err := os.Mkdir(other, 0o700); err != nil {
					t.Fatal(err)
				}
				recorder.requestedParent = other
			},
		},
		{
			name: "parent disappeared",
			mutate: func(t *testing.T, recorder *Recorder) {
				recorder.requestedParent = filepath.Join(t.TempDir(), "absent")
			},
		},
		{
			name: "destination became directory",
			mutate: func(t *testing.T, recorder *Recorder) {
				if err := os.Mkdir(recorder.destinationPath, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "destination parent became a file",
			mutate: func(t *testing.T, recorder *Recorder) {
				parent := filepath.Join(t.TempDir(), "parent")
				if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				recorder.destinationPath = filepath.Join(parent, "capture.har")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder, err := Prepare(Options{Destination: filepath.Join(t.TempDir(), "capture.har")})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(recorder.NativeOptions().Path, []byte(testHAR), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, recorder)
			if _, err := recorder.Finalize(); !errors.Is(err, ErrDestination) {
				t.Fatalf("finalize error = %v", err)
			}
		})
	}

	parentFile := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parentFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(Options{Destination: filepath.Join(parentFile, "capture.har")}); !errors.Is(err, ErrDestination) {
		t.Fatalf("non-directory parent error = %v", err)
	}
}

func TestFilesystemFailuresFailClosed(t *testing.T) {
	boom := errors.New("filesystem failure")
	originalAbsPath := harAbsPath
	originalMkdirAll := harMkdirAll
	originalEvalSymlinks := harEvalSymlinks
	originalMkdirTemp := harMkdirTemp
	originalChmod := harChmod
	originalOpenFile := harOpenFile
	originalRemoveAll := harRemoveAll
	originalWriteFile := harWriteFile
	reset := func() {
		harAbsPath = originalAbsPath
		harMkdirAll = originalMkdirAll
		harEvalSymlinks = originalEvalSymlinks
		harMkdirTemp = originalMkdirTemp
		harChmod = originalChmod
		harOpenFile = originalOpenFile
		harRemoveAll = originalRemoveAll
		harWriteFile = originalWriteFile
	}
	defer reset()

	for _, tc := range []struct {
		name string
		hook func()
	}{
		{name: "absolute path", hook: func() { harAbsPath = func(string) (string, error) { return "", boom } }},
		{name: "create parent", hook: func() { harMkdirAll = func(string, os.FileMode) error { return boom } }},
		{name: "canonical parent", hook: func() { harEvalSymlinks = func(string) (string, error) { return "", boom } }},
		{name: "private directory", hook: func() { harMkdirTemp = func(string, string) (string, error) { return "", boom } }},
		{name: "secure private directory", hook: func() { harChmod = func(string, os.FileMode) error { return boom } }},
		{name: "private file", hook: func() {
			harOpenFile = func(string, int, os.FileMode) (*os.File, error) { return nil, boom }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			defer reset()
			tc.hook()
			if recorder, err := Prepare(Options{Destination: filepath.Join(t.TempDir(), "capture.har")}); recorder != nil || !errors.Is(err, ErrDestination) {
				t.Fatalf("Prepare = %#v, %v", recorder, err)
			}
		})
	}

	t.Run("secure completed file", func(t *testing.T) {
		reset()
		defer reset()
		path := filepath.Join(t.TempDir(), "capture.har")
		if err := os.WriteFile(path, []byte(testHAR), 0o600); err != nil {
			t.Fatal(err)
		}
		harChmod = func(string, os.FileMode) error { return boom }
		if _, err := readRawHAR(path); !errors.Is(err, ErrDestination) {
			t.Fatalf("read raw error = %v", err)
		}
	})

	t.Run("remove private directory during finalize", func(t *testing.T) {
		reset()
		defer reset()
		recorder, err := Prepare(Options{Destination: filepath.Join(t.TempDir(), "capture.har")})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(recorder.NativeOptions().Path, []byte(testHAR), 0o600); err != nil {
			t.Fatal(err)
		}
		temporaryDirectory := recorder.temporaryDirectory
		harRemoveAll = func(string) error { return boom }
		if _, err := recorder.Finalize(); !errors.Is(err, ErrDestination) {
			t.Fatalf("finalize error = %v", err)
		}
		reset()
		if err := os.RemoveAll(temporaryDirectory); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("remove private directory during discard", func(t *testing.T) {
		reset()
		defer reset()
		recorder, err := Prepare(Options{Destination: filepath.Join(t.TempDir(), "capture.har")})
		if err != nil {
			t.Fatal(err)
		}
		temporaryDirectory := recorder.temporaryDirectory
		harRemoveAll = func(string) error { return boom }
		if err := recorder.Discard(); !errors.Is(err, ErrDestination) {
			t.Fatalf("discard error = %v", err)
		}
		reset()
		if err := os.RemoveAll(temporaryDirectory); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("promote artifact", func(t *testing.T) {
		reset()
		defer reset()
		destination := filepath.Join(t.TempDir(), "capture.har")
		recorder, err := Prepare(Options{Destination: destination})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(recorder.NativeOptions().Path, []byte(testHAR), 0o600); err != nil {
			t.Fatal(err)
		}
		harWriteFile = func(string, []byte, bool) error { return boom }
		if _, err := recorder.Finalize(); !errors.Is(err, ErrDestination) {
			t.Fatalf("finalize error = %v", err)
		}
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatalf("failed promotion published destination: %v", err)
		}
	})
}
