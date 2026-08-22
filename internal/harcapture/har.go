package harcapture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ehmo/gomoufox/internal/safefile"
)

const (
	DefaultMaxBytes   int64 = 64 * 1024 * 1024
	HardMaxBytes      int64 = 256 * 1024 * 1024
	MaxFilterBytes          = 2048
	RouteSummaryLimit       = 100
	RedactedValue           = "<redacted>"
)

var (
	ErrInvalidOptions    = errors.New("invalid HAR options")
	ErrInvalidHAR        = errors.New("invalid HAR")
	ErrTooLarge          = errors.New("HAR exceeds size limit")
	ErrDestination       = errors.New("HAR destination rejected")
	ErrDestinationExists = errors.New("HAR destination exists")
	ErrDiscarded         = errors.New("HAR recording discarded")
)

var (
	harAbsPath      = filepath.Abs
	harMkdirAll     = os.MkdirAll
	harEvalSymlinks = filepath.EvalSymlinks
	harMkdirTemp    = os.MkdirTemp
	harChmod        = os.Chmod
	harOpenFile     = func(path string, flag int, perm os.FileMode) (io.Closer, error) {
		return os.OpenFile(path, flag, perm)
	}
	harLstat                  = os.Lstat
	harOpen                   = func(path string) (io.ReadCloser, error) { return os.Open(path) }
	harReadAll                = io.ReadAll
	harValidateRawContentSize = validateRawContentSize
	harRemoveAll              = os.RemoveAll
	harWriteFile              = safefile.WriteFile0600
)

type Capture string

const (
	CaptureMetadata Capture = "metadata"
	CaptureFull     Capture = "full"
)

type Options struct {
	Destination string
	Capture     Capture
	URLFilter   string
	MaxBytes    int64
	Overwrite   bool
}

type NativeOptions struct {
	Path               string
	Mode               string
	Content            string
	OmitRequestContent bool
	URLFilter          string
}

type Route struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Status int    `json:"status"`
}

type Result struct {
	Path            string
	Capture         Capture
	Bytes           int64
	Entries         int
	Routes          []Route
	RoutesTruncated bool
}

type Recorder struct {
	mu sync.Mutex

	opts               Options
	requestedPath      string
	destinationPath    string
	requestedParent    string
	canonicalParent    string
	temporaryDirectory string
	rawPath            string

	done      bool
	discarded bool
	result    Result
	err       error
}

func Prepare(opts Options) (*Recorder, error) {
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	requestedPath, err := harAbsPath(normalized.Destination)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve destination: %v", ErrDestination, err)
	}
	requestedParent := filepath.Dir(requestedPath)
	if err := harMkdirAll(requestedParent, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create destination directory: %v", ErrDestination, err)
	}
	canonicalParent, err := harEvalSymlinks(requestedParent)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve destination directory: %v", ErrDestination, err)
	}
	destinationPath := filepath.Join(canonicalParent, filepath.Base(requestedPath))
	if err := validateDestination(destinationPath, normalized.Overwrite); err != nil {
		return nil, err
	}
	temporaryDirectory, err := harMkdirTemp(canonicalParent, ".gomoufox-har-")
	if err != nil {
		return nil, fmt.Errorf("%w: create private temporary directory: %v", ErrDestination, err)
	}
	cleanup := func() { _ = harRemoveAll(temporaryDirectory) }
	if err := harChmod(temporaryDirectory, 0o700); err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: secure private temporary directory: %v", ErrDestination, err)
	}
	rawPath := filepath.Join(temporaryDirectory, "capture.har")
	raw, err := harOpenFile(rawPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: create private temporary file: %v", ErrDestination, err)
	}
	if err := raw.Close(); err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: close private temporary file: %v", ErrDestination, err)
	}
	return &Recorder{
		opts:               normalized,
		requestedPath:      normalized.Destination,
		destinationPath:    destinationPath,
		requestedParent:    requestedParent,
		canonicalParent:    canonicalParent,
		temporaryDirectory: temporaryDirectory,
		rawPath:            rawPath,
	}, nil
}

func normalizeOptions(opts Options) (Options, error) {
	if strings.TrimSpace(opts.Destination) == "" {
		return Options{}, fmt.Errorf("%w: destination is required", ErrInvalidOptions)
	}
	if opts.Capture == "" {
		opts.Capture = CaptureMetadata
	}
	if opts.Capture != CaptureMetadata && opts.Capture != CaptureFull {
		return Options{}, fmt.Errorf("%w: capture must be metadata or full", ErrInvalidOptions)
	}
	if len(opts.URLFilter) > MaxFilterBytes {
		return Options{}, fmt.Errorf("%w: URL filter exceeds %d bytes", ErrInvalidOptions, MaxFilterBytes)
	}
	if strings.IndexByte(opts.URLFilter, 0) >= 0 {
		return Options{}, fmt.Errorf("%w: URL filter contains NUL", ErrInvalidOptions)
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.MaxBytes < 1 || opts.MaxBytes > HardMaxBytes {
		return Options{}, fmt.Errorf("%w: max bytes must be between 1 and %d", ErrInvalidOptions, HardMaxBytes)
	}
	return opts, nil
}

func validateDestination(path string, overwrite bool) error {
	st, err := harLstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect destination: %v", ErrDestination, err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: destination is a symlink", ErrDestination)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%w: destination is not a regular file", ErrDestination)
	}
	if !overwrite {
		return fmt.Errorf("%w: destination exists", ErrDestinationExists)
	}
	return nil
}

func (r *Recorder) NativeOptions() NativeOptions {
	if r == nil {
		return NativeOptions{}
	}
	native := NativeOptions{Path: r.rawPath, URLFilter: r.opts.URLFilter}
	if r.opts.Capture == CaptureFull {
		native.Mode = "full"
		native.Content = "embed"
		return native
	}
	native.Mode = "minimal"
	native.Content = "omit"
	native.OmitRequestContent = true
	return native
}

func (r *Recorder) Finalize() (Result, error) {
	if r == nil {
		return Result{}, fmt.Errorf("%w: recorder is nil", ErrInvalidOptions)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return r.result, r.err
	}
	if r.discarded {
		r.done = true
		r.err = ErrDiscarded
		return Result{}, r.err
	}
	r.done = true
	data, err := readRawHAR(r.rawPath)
	if err != nil {
		r.err = err
		_ = harRemoveAll(r.temporaryDirectory)
		return Result{}, err
	}
	processed, entries, routes, truncated, err := process(data, r.opts.Capture, RouteSummaryLimit)
	if err != nil {
		r.err = err
		_ = harRemoveAll(r.temporaryDirectory)
		return Result{}, err
	}
	if int64(len(processed)) > r.opts.MaxBytes {
		r.err = fmt.Errorf("%w: final size %d exceeds %d", ErrTooLarge, len(processed), r.opts.MaxBytes)
		_ = harRemoveAll(r.temporaryDirectory)
		return Result{}, r.err
	}
	if err := harRemoveAll(r.temporaryDirectory); err != nil {
		r.err = fmt.Errorf("%w: remove private temporary directory: %v", ErrDestination, err)
		return Result{}, r.err
	}
	currentParent, err := harEvalSymlinks(r.requestedParent)
	if err != nil || currentParent != r.canonicalParent {
		if err == nil {
			err = errors.New("destination directory changed during capture")
		}
		r.err = fmt.Errorf("%w: revalidate destination directory: %v", ErrDestination, err)
		return Result{}, r.err
	}
	if err := validateDestination(r.destinationPath, r.opts.Overwrite); err != nil {
		r.err = err
		return Result{}, err
	}
	if err := harWriteFile(r.destinationPath, processed, r.opts.Overwrite); err != nil {
		r.err = fmt.Errorf("%w: promote artifact: %v", ErrDestination, err)
		return Result{}, r.err
	}
	r.result = Result{
		Path:            r.requestedPath,
		Capture:         r.opts.Capture,
		Bytes:           int64(len(processed)),
		Entries:         entries,
		Routes:          routes,
		RoutesTruncated: truncated,
	}
	return r.result, nil
}

func (r *Recorder) Discard() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done || r.discarded {
		return nil
	}
	r.discarded = true
	if err := harRemoveAll(r.temporaryDirectory); err != nil {
		return fmt.Errorf("%w: remove private temporary directory: %v", ErrDestination, err)
	}
	return nil
}

func readRawHAR(path string) ([]byte, error) {
	st, err := harLstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect completed artifact: %v", ErrInvalidHAR, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: completed artifact is not a regular file", ErrInvalidHAR)
	}
	if st.Size() > HardMaxBytes {
		return nil, fmt.Errorf("%w: raw size %d exceeds %d", ErrTooLarge, st.Size(), HardMaxBytes)
	}
	if err := harChmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("%w: secure completed artifact: %v", ErrDestination, err)
	}
	f, err := harOpen(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open completed artifact: %v", ErrInvalidHAR, err)
	}
	defer f.Close()
	data, err := harReadAll(io.LimitReader(f, HardMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read completed artifact: %v", ErrInvalidHAR, err)
	}
	if err := harValidateRawContentSize(int64(len(data)), HardMaxBytes); err != nil {
		return nil, err
	}
	return data, nil
}

func validateRawContentSize(size, limit int64) error {
	if size > limit {
		return fmt.Errorf("%w: raw content exceeds %d", ErrTooLarge, limit)
	}
	return nil
}

// InspectFile validates a completed HAR and returns a bounded, redacted route
// summary. It never returns headers, cookies, query values, or bodies.
func InspectFile(path string, routeLimit int) (int, []Route, bool, error) {
	data, err := readRawHAR(path)
	if err != nil {
		return 0, nil, false, err
	}
	raw, err := decodeArchive(data)
	if err != nil {
		return 0, nil, false, err
	}
	routes, truncated := projectRoutes(raw.Entries, routeLimit)
	return len(raw.Entries), routes, truncated, nil
}

type rawArchive struct {
	Log rawLog `json:"log"`
}

type rawLog struct {
	Version string     `json:"version"`
	Creator rawCreator `json:"creator"`
	Entries []rawEntry `json:"entries"`
}

type rawCreator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type rawEntry struct {
	StartedDateTime string      `json:"startedDateTime"`
	Time            float64     `json:"time"`
	Request         rawRequest  `json:"request"`
	Response        rawResponse `json:"response"`
	Cache           any         `json:"cache"`
	Timings         rawTimings  `json:"timings"`
	ServerIPAddress string      `json:"serverIPAddress"`
	Connection      string      `json:"connection"`
}

type rawRequest struct {
	Method      string         `json:"method"`
	URL         string         `json:"url"`
	HTTPVersion string         `json:"httpVersion"`
	Cookies     []rawNameValue `json:"cookies"`
	Headers     []rawNameValue `json:"headers"`
	QueryString []rawNameValue `json:"queryString"`
	PostData    *rawPostData   `json:"postData"`
	HeadersSize int64          `json:"headersSize"`
	BodySize    int64          `json:"bodySize"`
}

type rawResponse struct {
	Status      int            `json:"status"`
	StatusText  string         `json:"statusText"`
	HTTPVersion string         `json:"httpVersion"`
	Cookies     []rawNameValue `json:"cookies"`
	Headers     []rawNameValue `json:"headers"`
	Content     rawContent     `json:"content"`
	RedirectURL string         `json:"redirectURL"`
	HeadersSize int64          `json:"headersSize"`
	BodySize    int64          `json:"bodySize"`
}

type rawNameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type rawPostData struct {
	MimeType string         `json:"mimeType"`
	Text     string         `json:"text"`
	Params   []rawNameValue `json:"params"`
}

type rawContent struct {
	Size        int64  `json:"size"`
	Compression int64  `json:"compression,omitempty"`
	MimeType    string `json:"mimeType"`
	Text        string `json:"text"`
	Encoding    string `json:"encoding"`
}

type rawTimings struct {
	Blocked *float64 `json:"blocked,omitempty"`
	DNS     *float64 `json:"dns,omitempty"`
	Connect *float64 `json:"connect,omitempty"`
	Send    float64  `json:"send"`
	Wait    float64  `json:"wait"`
	Receive float64  `json:"receive"`
	SSL     *float64 `json:"ssl,omitempty"`
}

type metadataArchive struct {
	Log metadataLog `json:"log"`
}

type metadataLog struct {
	Version string          `json:"version"`
	Creator metadataCreator `json:"creator"`
	Entries []metadataEntry `json:"entries"`
}

type metadataCreator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type metadataEntry struct {
	StartedDateTime string           `json:"startedDateTime"`
	Time            float64          `json:"time"`
	Request         metadataRequest  `json:"request"`
	Response        metadataResponse `json:"response"`
	Cache           map[string]any   `json:"cache"`
	Timings         rawTimings       `json:"timings"`
}

type metadataRequest struct {
	Method      string         `json:"method"`
	URL         string         `json:"url"`
	HTTPVersion string         `json:"httpVersion"`
	Cookies     []rawNameValue `json:"cookies"`
	Headers     []rawNameValue `json:"headers"`
	QueryString []rawNameValue `json:"queryString"`
	HeadersSize int64          `json:"headersSize"`
	BodySize    int64          `json:"bodySize"`
}

type metadataResponse struct {
	Status      int             `json:"status"`
	StatusText  string          `json:"statusText"`
	HTTPVersion string          `json:"httpVersion"`
	Cookies     []rawNameValue  `json:"cookies"`
	Headers     []rawNameValue  `json:"headers"`
	Content     metadataContent `json:"content"`
	RedirectURL string          `json:"redirectURL"`
	HeadersSize int64           `json:"headersSize"`
	BodySize    int64           `json:"bodySize"`
}

type metadataContent struct {
	Size        int64  `json:"size"`
	Compression int64  `json:"compression,omitempty"`
	MimeType    string `json:"mimeType"`
}

func process(data []byte, capture Capture, routeLimit int) ([]byte, int, []Route, bool, error) {
	raw, err := decodeArchive(data)
	if err != nil {
		return nil, 0, nil, false, err
	}
	routes, truncated := projectRoutes(raw.Entries, routeLimit)
	if capture == CaptureFull {
		return data, len(raw.Entries), routes, truncated, nil
	}
	projected, err := projectMetadata(raw)
	if err != nil {
		return nil, 0, nil, false, err
	}
	out, _ := json.MarshalIndent(metadataArchive{Log: projected}, "", "  ")
	out = append(out, '\n')
	return out, len(raw.Entries), routes, truncated, nil
}

func decodeArchive(data []byte) (rawLog, error) {
	var archive rawArchive
	if err := json.Unmarshal(data, &archive); err != nil {
		return rawLog{}, fmt.Errorf("%w: decode archive: %v", ErrInvalidHAR, err)
	}
	log := archive.Log
	if log.Version == "" || log.Entries == nil {
		return rawLog{}, fmt.Errorf("%w: log version and entries are required", ErrInvalidHAR)
	}
	for i, entry := range log.Entries {
		if entry.StartedDateTime == "" || entry.Request.Method == "" || entry.Request.URL == "" {
			return rawLog{}, fmt.Errorf("%w: entry %d is incomplete", ErrInvalidHAR, i)
		}
	}
	return log, nil
}

func projectMetadata(raw rawLog) (metadataLog, error) {
	out := metadataLog{
		Version: raw.Version,
		Creator: metadataCreator{Name: "gomoufox", Version: "metadata"},
		Entries: make([]metadataEntry, 0, len(raw.Entries)),
	}
	for i, entry := range raw.Entries {
		requestURL, err := redactURL(entry.Request.URL)
		if err != nil {
			return metadataLog{}, fmt.Errorf("%w: entry %d request URL: %v", ErrInvalidHAR, i, err)
		}
		redirectURL := ""
		if entry.Response.RedirectURL != "" {
			redirectURL, err = redactURL(entry.Response.RedirectURL)
			if err != nil {
				return metadataLog{}, fmt.Errorf("%w: entry %d redirect URL: %v", ErrInvalidHAR, i, err)
			}
		}
		out.Entries = append(out.Entries, metadataEntry{
			StartedDateTime: entry.StartedDateTime,
			Time:            entry.Time,
			Request: metadataRequest{
				Method:      entry.Request.Method,
				URL:         requestURL,
				HTTPVersion: entry.Request.HTTPVersion,
				Cookies:     redactPairs(entry.Request.Cookies),
				Headers:     redactPairs(entry.Request.Headers),
				QueryString: redactPairs(entry.Request.QueryString),
				HeadersSize: entry.Request.HeadersSize,
				BodySize:    entry.Request.BodySize,
			},
			Response: metadataResponse{
				Status:      entry.Response.Status,
				StatusText:  http.StatusText(entry.Response.Status),
				HTTPVersion: entry.Response.HTTPVersion,
				Cookies:     redactPairs(entry.Response.Cookies),
				Headers:     redactPairs(entry.Response.Headers),
				Content: metadataContent{
					Size:        entry.Response.Content.Size,
					Compression: entry.Response.Content.Compression,
					MimeType:    metadataMIMEType(entry.Response.Content.MimeType),
				},
				RedirectURL: redirectURL,
				HeadersSize: entry.Response.HeadersSize,
				BodySize:    entry.Response.BodySize,
			},
			Cache:   map[string]any{},
			Timings: entry.Timings,
		})
	}
	return out, nil
}

func projectRoutes(entries []rawEntry, limit int) ([]Route, bool) {
	if limit < 0 {
		limit = 0
	}
	routes := make([]Route, 0, min(limit, len(entries)))
	seen := make(map[string]struct{}, min(limit, len(entries)))
	for _, entry := range entries {
		u, err := redactURL(entry.Request.URL)
		if err != nil {
			continue
		}
		key := entry.Request.Method + "\x00" + u + "\x00" + strconv.Itoa(entry.Response.Status)
		if _, ok := seen[key]; ok {
			continue
		}
		if len(routes) >= limit {
			return routes, true
		}
		seen[key] = struct{}{}
		routes = append(routes, Route{Method: entry.Request.Method, URL: u, Status: entry.Response.Status})
	}
	return routes, false
}

func metadataMIMEType(raw string) string {
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return ""
	}
	return mediaType
}

func redactPairs(in []rawNameValue) []rawNameValue {
	out := make([]rawNameValue, 0, len(in))
	for _, pair := range in {
		out = append(out, rawNameValue{Name: pair.Name, Value: RedactedValue})
	}
	return out
}

func redactURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		if err == nil {
			err = errors.New("URL scheme is missing")
		}
		return "", err
	}
	u.User = nil
	u.Fragment = ""
	if u.RawQuery != "" {
		// Go intentionally rejects raw semicolons as query separators. Browsers
		// still encounter legacy semicolon-delimited URLs, so normalize them
		// before parsing rather than failing finalization or retaining a value.
		values, err := url.ParseQuery(strings.ReplaceAll(u.RawQuery, ";", "&"))
		if err != nil {
			return "", err
		}
		for name, items := range values {
			for i := range items {
				items[i] = RedactedValue
			}
			values[name] = items
		}
		u.RawQuery = values.Encode()
	}
	return u.String(), nil
}
