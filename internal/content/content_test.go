package content

import (
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestExtractMarkdownArticleAndFallback(t *testing.T) {
	long := strings.Repeat("This is a substantial article sentence with enough words to pass extraction. ", 4)
	html := `<html><head><title>T</title></head><body><article><h1>Title</h1><p>` + long + `</p></article></body></html>`
	got, err := Extract(html, "fallback body", "https://example.com/post", FormatMarkdown, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.MarkdownQuality != "article" {
		t.Fatalf("quality = %q content=%q", got.MarkdownQuality, got.Content)
	}
	if !strings.Contains(got.Content, "# Title") {
		t.Fatalf("markdown = %q", got.Content)
	}

	mainHTML := `<html><body><nav>skip this</nav><main><p>` + long + `</p><a href="/next">Next</a></main></body></html>`
	got, err = Extract(mainHTML, "fallback body", "https://example.com/post", FormatMarkdown, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.MarkdownQuality != "article" || strings.Contains(got.Content, "skip this") || !strings.Contains(got.Content, "[Next](https://example.com/next)") {
		t.Fatalf("main extraction = %#v", got)
	}

	got, err = Extract("<html><body><p>tiny</p></body></html>", "body text", "https://example.com", FormatMarkdown, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.MarkdownQuality != "fallback" || got.Content != "body text" {
		t.Fatalf("fallback = %#v", got)
	}
}

func TestExtractFormatsAndTruncates(t *testing.T) {
	html := "<html><body>Hello</body></html>"
	got, err := Extract(html, "Hello text", "https://example.com", FormatHTML, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || !strings.Contains(got.Content, "truncated") {
		t.Fatalf("html truncated = %#v", got)
	}
	got, err = Extract(html, "Hello text", "https://example.com", FormatText, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "Hello text" || got.Format != FormatText {
		t.Fatalf("text = %#v", got)
	}

	got, err = Extract(html, "ignored", "https://example.com", Format("raw"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != html || got.Format != Format("raw") || got.Bytes != len([]byte(html)) {
		t.Fatalf("custom format = %#v", got)
	}
}

func TestRenderMarkdownInlineElements(t *testing.T) {
	md := renderMarkdown(`<h2>Hi</h2><p>Go <strong>bold</strong> <a href="/x">link</a></p><ul><li>one</li><li>two</li></ul>`)
	for _, want := range []string{"## Hi", "**bold**", "[link](/x)", "- one", "- two"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q in %q", want, md)
		}
	}
}

func TestRenderMarkdownAdditionalElementsAndFallbacks(t *testing.T) {
	md := renderMarkdown(`<section><!-- skip --><p>before<br>after <em>soft</em> <a>plain link</a> <span>wrapped</span></p><ol><li>first</li></ol></section>`)
	for _, want := range []string{"before", "after", "_soft_", "plain link", "wrapped", "- first"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q in %q", want, md)
		}
	}

	if got := renderMarkdownFromReader(" fallback ", errorReader{}); got != "fallback" {
		t.Fatalf("parse fallback = %q", got)
	}
	if got := renderMarkdownDocument(&html.Node{}, " no body "); got != "no body" {
		t.Fatalf("body fallback = %q", got)
	}

	var b strings.Builder
	parent := &html.Node{Type: html.DocumentNode}
	parent.AppendChild(&html.Node{Type: html.TextNode, Data: "document text"})
	renderNode(&b, parent, 0)
	if !strings.Contains(b.String(), "document text") {
		t.Fatalf("non-element render = %q", b.String())
	}

	var children strings.Builder
	renderChildren(&children, parent, 0)
	if !strings.Contains(children.String(), "document text") {
		t.Fatalf("renderChildren = %q", children.String())
	}
}

func TestArticleScoringAndLinkEdges(t *testing.T) {
	long := strings.Repeat("enough words for extraction ", 8)
	got, err := Extract(`<html><body><div role="main"><p>`+long+`</p></div></body></html>`, "fallback", "https://example.com", FormatMarkdown, 0)
	if err != nil || got.MarkdownQuality != "article" {
		t.Fatalf("role main extraction = %#v err=%v", got, err)
	}
	got, err = Extract(`<html><body><div class="post-content"><p>`+long+`</p></div></body></html>`, "fallback", "https://example.com", FormatMarkdown, 0)
	if err != nil || got.MarkdownQuality != "article" {
		t.Fatalf("class content extraction = %#v err=%v", got, err)
	}

	doc, err := html.Parse(strings.NewReader(`<html><body><aside>hidden</aside><p>visible</p><a href="://bad">bad</a></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := articleScore(doc); got != 0 {
		t.Fatalf("document score = %d", got)
	}
	if got := readableTextLen(nil); got != 0 {
		t.Fatalf("nil text len = %d", got)
	}
	if got := renderMarkdownDocumentWithBase(doc, "", mustURL("https://example.com/root")); strings.Contains(got, "hidden") || !strings.Contains(got, "[bad](://bad)") {
		t.Fatalf("ignored/link render = %q", got)
	}
}

func TestAttrAndReadLineEdges(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<a href="/x">x</a>`))
	if err != nil {
		t.Fatal(err)
	}
	link := findElement(doc, "a")
	if got := attr(link, "missing"); got != "" {
		t.Fatalf("missing attr = %q", got)
	}

	empty := ""
	line, err := readLine(&empty)
	if err != io.EOF || line != "" {
		t.Fatalf("empty readLine = %q, %v", line, err)
	}

	one := "last"
	line, err = readLine(&one)
	if err != io.EOF || line != "last" || one != "" {
		t.Fatalf("single readLine = %q, rest=%q err=%v", line, one, err)
	}

	two := "first\nsecond"
	line, err = readLine(&two)
	if err != nil || line != "first" || two != "second" {
		t.Fatalf("multi readLine = %q, rest=%q err=%v", line, two, err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func mustURL(raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return parsed
}
