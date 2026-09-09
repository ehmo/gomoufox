package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContentReportsExtractionAndTruncationSources(t *testing.T) {
	for _, tc := range []struct {
		page                    pageContent
		format, method, sources string
		cap                     int
	}{
		{pageContent{HTML: "<p>partial", Text: "Reader body", HTMLTruncated: true, Truncated: true}, "markdown", "reader_fallback", "html_acquisition", 200},
		{pageContent{Text: "日本語", TextTruncated: true, Truncated: true}, "text", "reader_text", "text_acquisition,output", 4},
		{pageContent{Text: "body", Truncated: true}, "text", "reader_text", "acquisition", 200},
		{pageContent{HTML: "<article><p>" + strings.Repeat("Article prose. ", 20) + "</p></article>"}, "markdown", "article", "", 1000},
		{pageContent{HTML: "<p>whole</p>"}, "html", "dom_html", "", 200},
	} {
		cfg := defaultTestConfig(t)
		cfg.Policy.ContentWarning = false
		cfg.BrowserFactory = &fakeBrowserFactory{session: &fakeBrowserSession{contentResult: tc.page}}
		s := newTestServer(t, cfg)
		r := s.Handle(context.Background(), "browser_get_content", mustRaw(t, map[string]any{"format": tc.format, "max_bytes": tc.cap}))
		if r.IsError || r.Payload["extraction_method"] != tc.method || strings.Join(r.Payload["truncation_sources"].([]string), ",") != tc.sources {
			t.Fatal(r)
		}
		body := r.Payload["content"].(string)
		if !utf8.ValidString(body) || len(body) > tc.cap || r.Payload["bytes"] != len(body) || r.Payload["truncated"] != (tc.sources != "") {
			t.Fatal(r)
		}
	}
}

func TestHTMLAcquisitionBoundsEncodingAndPreservesUTF8(t *testing.T) {
	out := runNodeExpression(t, `
const read=`+boundedContentExpression+`;
const NativeEncoder=TextEncoder;
globalThis.TextEncoder=class extends NativeEncoder { encode(value) { if(value.length>25000) throw new Error('unbounded encoding'); return super.encode(value); }};
const text={nodeType:3,nodeValue:'日本語'};
const root={nodeType:1,tagName:'p',attributes:[],firstChild:text};
globalThis.location={href:'https://example.com'};
globalThis.document={documentElement:root};
const unicode=read({maxBytes:7,includeText:false});
text.nodeValue='&'.repeat(1000000);
const huge=read({maxBytes:50,includeText:false});
root.attributes=[{name:'title',value:'"'.repeat(1000000)}];
const attribute=read({maxBytes:50,includeText:false});
console.log(JSON.stringify({unicode,huge,attribute}));
`)
	var results map[string]struct {
		HTML          string
		HTMLTruncated bool `json:"html_truncated"`
		TextTruncated bool `json:"text_truncated"`
	}
	if err := json.Unmarshal(out, &results); err != nil {
		t.Fatal(err)
	}
	for name, r := range results {
		if !r.HTMLTruncated || r.TextTruncated || !utf8.ValidString(r.HTML) || len(r.HTML) > 50 {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	if results["unicode"].HTML != "<p>日" {
		t.Fatal(results["unicode"])
	}
}
