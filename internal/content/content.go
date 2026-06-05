package content

import (
	"bytes"
	"io"
	"net/url"
	"strings"

	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
)

type Format string

const (
	FormatHTML     Format = "html"
	FormatText     Format = "text"
	FormatMarkdown Format = "markdown"
)

type Result struct {
	Content         string `json:"content"`
	Format          Format `json:"format"`
	MarkdownQuality string `json:"markdown_quality,omitempty"`
	Bytes           int    `json:"bytes"`
	Truncated       bool   `json:"truncated"`
}

func Extract(rawHTML, bodyText, pageURL string, format Format, maxBytes int) (Result, error) {
	var result Result
	switch format {
	case FormatHTML:
		result = Result{Content: rawHTML, Format: FormatHTML}
	case FormatText:
		result = Result{Content: strings.TrimSpace(bodyText), Format: FormatText}
	case "", FormatMarkdown:
		md, quality := markdownFromHTML(rawHTML, bodyText, pageURL)
		result = Result{Content: md, Format: FormatMarkdown, MarkdownQuality: quality}
	default:
		result = Result{Content: rawHTML, Format: format}
	}
	result.Bytes = len([]byte(result.Content))
	if maxBytes > 0 && result.Bytes > maxBytes {
		data := []byte(result.Content)
		result.Content = string(data[:maxBytes]) + "\n<!-- truncated -->"
		result.Bytes = len([]byte(result.Content))
		result.Truncated = true
	}
	return result, nil
}

func markdownFromHTML(rawHTML, bodyText, pageURL string) (string, string) {
	parsedURL, _ := url.Parse(pageURL)
	article, err := readability.FromReader(strings.NewReader(rawHTML), parsedURL)
	if err == nil && len(strings.TrimSpace(article.TextContent)) >= 100 {
		return renderMarkdown(article.Content), "readability"
	}
	return strings.TrimSpace(bodyText), "fallback"
}

func renderMarkdown(fragment string) string {
	return renderMarkdownFromReader(fragment, strings.NewReader("<html><body>"+fragment+"</body></html>"))
}

func renderMarkdownFromReader(fallback string, r io.Reader) string {
	doc, err := html.Parse(r)
	if err != nil {
		return strings.TrimSpace(fallback)
	}
	return renderMarkdownDocument(doc, fallback)
}

func renderMarkdownDocument(doc *html.Node, fallback string) string {
	var b strings.Builder
	body := findElement(doc, "body")
	if body == nil {
		return strings.TrimSpace(fallback)
	}
	for node := body.FirstChild; node != nil; node = node.NextSibling {
		renderNode(&b, node, 0)
	}
	return normalizeBlankLines(b.String())
}

func findElement(n *html.Node, name string) *html.Node {
	if n.Type == html.ElementNode && n.Data == name {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := findElement(c, name); got != nil {
			return got
		}
	}
	return nil
}

func renderNode(b *strings.Builder, n *html.Node, depth int) {
	if n.Type == html.TextNode {
		text := strings.Join(strings.Fields(n.Data), " ")
		if text != "" {
			b.WriteString(text)
			b.WriteByte(' ')
		}
		return
	}
	if n.Type != html.ElementNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderNode(b, c, depth)
		}
		return
	}
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.Data[1] - '0')
		b.WriteString("\n\n")
		b.WriteString(strings.Repeat("#", level))
		b.WriteByte(' ')
		renderChildren(b, n, depth)
		b.WriteString("\n\n")
	case "p", "div", "section", "article":
		b.WriteString("\n\n")
		renderChildren(b, n, depth)
		b.WriteString("\n\n")
	case "br":
		b.WriteByte('\n')
	case "ul", "ol":
		b.WriteString("\n")
		renderChildren(b, n, depth+1)
		b.WriteString("\n")
	case "li":
		b.WriteString("\n- ")
		renderChildren(b, n, depth)
	case "a":
		text := childText(n)
		href := attr(n, "href")
		if text != "" && href != "" {
			b.WriteString("[")
			b.WriteString(text)
			b.WriteString("](")
			b.WriteString(href)
			b.WriteString(") ")
			return
		}
		renderChildren(b, n, depth)
	case "strong", "b":
		b.WriteString("**")
		b.WriteString(childText(n))
		b.WriteString("** ")
	case "em", "i":
		b.WriteString("_")
		b.WriteString(childText(n))
		b.WriteString("_ ")
	default:
		renderChildren(b, n, depth)
	}
}

func renderChildren(b *strings.Builder, n *html.Node, depth int) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(b, c, depth)
	}
}

func childText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func attr(n *html.Node, name string) string {
	for _, attr := range n.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

func normalizeBlankLines(s string) string {
	var out bytes.Buffer
	prevBlank := false
	for {
		line, err := readLine(&s)
		blank := strings.TrimSpace(line) == ""
		if blank {
			if !prevBlank {
				out.WriteByte('\n')
			}
		} else {
			out.WriteString(strings.TrimSpace(line))
			out.WriteByte('\n')
		}
		prevBlank = blank
		if err == io.EOF {
			break
		}
	}
	return strings.TrimSpace(out.String())
}

func readLine(s *string) (string, error) {
	if *s == "" {
		return "", io.EOF
	}
	idx := strings.IndexByte(*s, '\n')
	if idx < 0 {
		line := *s
		*s = ""
		return line, io.EOF
	}
	line := (*s)[:idx]
	*s = (*s)[idx+1:]
	return line, nil
}
