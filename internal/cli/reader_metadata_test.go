package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox"
)

func TestCLIReaderReportsMethodAndPlainTruncation(t *testing.T) {
	old := openPageForLocal
	t.Cleanup(func() { openPageForLocal = old })
	openPageForLocal = func(context.Context, LocalCommandRequest, []gomoufox.ContextOption) (localPage, func(), error) {
		return &fakeLocalPage{html: "<p>日本語</p>"}, func() {}, nil
	}
	req := LocalCommandRequest{Args: []string{"https://example.com"}, Flags: map[string]any{"text": true, "max_bytes": "4"}}
	plain, err := localGet(context.Background(), req)
	if err != nil || string(plain.Stdout) != "日" || !strings.Contains(plain.Stderr, "truncated to 3 bytes") {
		t.Fatalf("plain=%+v err=%v", plain, err)
	}
	req.JSON = true
	response, err := localGet(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Content   string
		Bytes     int
		Truncated bool
		Method    string `json:"extraction_method"`
	}
	if err := json.Unmarshal(response.Stdout, &result); err != nil {
		t.Fatal(err)
	}
	if result.Content != "日" || result.Bytes != 3 || !result.Truncated || result.Method != "reader_text" || response.Stderr != "" {
		t.Fatalf("JSON=%s", response.Stdout)
	}
}
