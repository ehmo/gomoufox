package content

import (
	"bytes"
	"io"
	"net/url"
	"strings"

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
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err == nil {
		baseURL, _ := url.Parse(pageURL)
		if article := findArticleNode(doc); article != nil {
			markdown := renderMarkdownNode(article, baseURL)
			if markdown != "" {
				return markdown, "article"
			}
		}
	}
	return strings.TrimSpace(bodyText), "fallback"
}

func renderMarkdown(fragment string) string {
	return renderMarkdownFromReader(fragment, strings.NewReader("<html><body>"+fragment+"</body></html>"))
}

func renderMarkdownNode(node *html.Node, baseURL *url.URL) string {
	var b strings.Builder
	renderNodeWithBase(&b, node, 0, baseURL)
	return normalizeBlankLines(b.String())
}

func renderMarkdownFromReader(fallback string, r io.Reader) string {
	doc, err := html.Parse(r)
	if err != nil {
		return strings.TrimSpace(fallback)
	}
	return renderMarkdownDocument(doc, fallback)
}

func renderMarkdownDocument(doc *html.Node, fallback string) string {
	return renderMarkdownDocumentWithBase(doc, fallback, nil)
}

func renderMarkdownDocumentWithBase(doc *html.Node, fallback string, baseURL *url.URL) string {
	var b strings.Builder
	body := findElement(doc, "body")
	if body == nil {
		return strings.TrimSpace(fallback)
	}
	for node := body.FirstChild; node != nil; node = node.NextSibling {
		renderNodeWithBase(&b, node, 0, baseURL)
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

func findArticleNode(doc *html.Node) *html.Node {
	var best *html.Node
	bestScore := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil || ignoredElement(n) {
			return
		}
		if n.Type == html.ElementNode {
			score := articleScore(n)
			if score > 0 {
				textLen := readableTextLen(n)
				if textLen >= 100 && score+textLen > bestScore {
					best = n
					bestScore = score + textLen
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return best
}

func articleScore(n *html.Node) int {
	switch n.Data {
	case "article":
		return 1000
	case "main":
		return 900
	}
	if attr(n, "role") == "main" {
		return 850
	}
	idClass := strings.ToLower(attr(n, "id") + " " + attr(n, "class"))
	for _, marker := range []string{"article", "content", "post", "entry", "story"} {
		if strings.Contains(idClass, marker) {
			return 500
		}
	}
	return 0
}

func readableTextLen(n *html.Node) int {
	if n == nil || ignoredElement(n) {
		return 0
	}
	if n.Type == html.TextNode {
		return len(strings.Join(strings.Fields(n.Data), " "))
	}
	total := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		total += readableTextLen(c)
	}
	return total
}

func ignoredElement(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "script", "style", "noscript", "template", "nav", "header", "footer", "aside", "form":
		return true
	default:
		return false
	}
}

func renderNode(b *strings.Builder, n *html.Node, depth int) {
	renderNodeWithBase(b, n, depth, nil)
}

func renderNodeWithBase(b *strings.Builder, n *html.Node, depth int, baseURL *url.URL) {
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
			renderNodeWithBase(b, c, depth, baseURL)
		}
		return
	}
	if ignoredElement(n) {
		return
	}
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.Data[1] - '0')
		b.WriteString("\n\n")
		b.WriteString(strings.Repeat("#", level))
		b.WriteByte(' ')
		renderChildrenWithBase(b, n, depth, baseURL)
		b.WriteString("\n\n")
	case "p", "div", "section", "article":
		b.WriteString("\n\n")
		renderChildrenWithBase(b, n, depth, baseURL)
		b.WriteString("\n\n")
	case "br":
		b.WriteByte('\n')
	case "ul", "ol":
		b.WriteString("\n")
		renderChildrenWithBase(b, n, depth+1, baseURL)
		b.WriteString("\n")
	case "li":
		b.WriteString("\n- ")
		renderChildrenWithBase(b, n, depth, baseURL)
	case "a":
		text := childText(n)
		href := resolvedHref(n, baseURL)
		if text != "" && href != "" {
			b.WriteString("[")
			b.WriteString(text)
			b.WriteString("](")
			b.WriteString(href)
			b.WriteString(") ")
			return
		}
		renderChildrenWithBase(b, n, depth, baseURL)
	case "strong", "b":
		b.WriteString("**")
		b.WriteString(childText(n))
		b.WriteString("** ")
	case "em", "i":
		b.WriteString("_")
		b.WriteString(childText(n))
		b.WriteString("_ ")
	default:
		renderChildrenWithBase(b, n, depth, baseURL)
	}
}

func renderChildren(b *strings.Builder, n *html.Node, depth int) {
	renderChildrenWithBase(b, n, depth, nil)
}

func renderChildrenWithBase(b *strings.Builder, n *html.Node, depth int, baseURL *url.URL) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNodeWithBase(b, c, depth, baseURL)
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

func resolvedHref(n *html.Node, baseURL *url.URL) string {
	href := strings.TrimSpace(attr(n, "href"))
	if href == "" || baseURL == nil {
		return href
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return href
	}
	return baseURL.ResolveReference(parsed).String()
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
