package policy

import (
	"bytes"
	"io"
	"net/url"
	"regexp"
	"strings"
)

var (
	bearerRE     = regexp.MustCompile(`(?i)(authorization|proxy-authorization)\s*[:=]\s*bearer\s+[A-Za-z0-9._~+/=-]+`)
	cookieRE     = regexp.MustCompile(`(?i)(cookie|set-cookie)\s*[:=]\s*[^,\n\r]+`)
	userinfoURL  = regexp.MustCompile(`([a-z][a-z0-9+.-]*://)([^/@\s:]+):([^/@\s]+)@`)
	wsPathRE     = regexp.MustCompile(`(wss?://)([^/\s]+)/[^\s]+`)
	tokenRE      = regexp.MustCompile(`(?i)(token|api[_-]?key|password|secret)=([^&\s]+)`)
	jsonSecretRE = regexp.MustCompile(`(?i)"(value|cookies|origins|localStorage|token|password|secret)"\s*:\s*"[^"]*"`)
)

const maxRedactLineBytes = 64 * 1024

func Redact(text string) string {
	text = redactURLs(text)
	text = userinfoURL.ReplaceAllString(text, `${1}<redacted>@`)
	text = bearerRE.ReplaceAllString(text, `$1: <redacted>`)
	text = cookieRE.ReplaceAllString(text, `$1: <redacted>`)
	text = wsPathRE.ReplaceAllString(text, `${1}${2}/<redacted>`)
	text = tokenRE.ReplaceAllString(text, `$1=<redacted>`)
	text = jsonSecretRE.ReplaceAllString(text, `"$1":"<redacted>"`)
	return text
}

func NewRedactWriter(w io.Writer) *RedactingWriter {
	return &RedactingWriter{w: w}
}

func RedactWriter(w io.Writer) io.Writer {
	return NewRedactWriter(w)
}

type RedactingWriter struct {
	w   io.Writer
	buf []byte
}

func (w *RedactingWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if err := w.writeRedacted(w.buf[:i+1]); err != nil {
			return 0, err
		}
		w.buf = append(w.buf[:0], w.buf[i+1:]...)
	}
	if len(w.buf) > maxRedactLineBytes {
		if err := w.writeRedacted(w.buf); err != nil {
			return 0, err
		}
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

func (w *RedactingWriter) Flush() error {
	if len(w.buf) == 0 {
		return nil
	}
	err := w.writeRedacted(w.buf)
	w.buf = w.buf[:0]
	return err
}

func (w *RedactingWriter) writeRedacted(p []byte) error {
	_, err := w.w.Write([]byte(Redact(string(p))))
	return err
}

func redactURLs(text string) string {
	fields := strings.Fields(text)
	for _, field := range fields {
		if !strings.Contains(field, "://") || !strings.Contains(field, "@") {
			continue
		}
		trimmed := strings.Trim(field, `"'(),;`)
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.User == nil {
			continue
		}
		parsed.User = url.User("<redacted>")
		text = strings.ReplaceAll(text, trimmed, parsed.String())
	}
	return text
}
