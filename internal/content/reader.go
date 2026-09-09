package content

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// ReaderText extracts body text, retaining block boundaries and excluding code
// and explicitly hidden subtrees. It does not infer visibility from stylesheets.
func ReaderText(rawHTML string) string {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return ""
	}
	root := findElement(doc, "body")
	if root == nil {
		return ""
	}
	var out strings.Builder
	space, line := false, false
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, pre bool) {
		if n.Type == html.TextNode {
			for _, r := range n.Data {
				if unicode.IsSpace(r) {
					if pre && (r == '\n' || r == '\r') {
						line = true
					} else {
						space = true
					}
					continue
				}
				if out.Len() > 0 {
					if line {
						out.WriteByte('\n')
					} else if space {
						out.WriteByte(' ')
					}
				}
				line, space = false, false
				out.WriteRune(r)
			}
			return
		}
		if n.Type != html.ElementNode || readerIgnored(n) {
			return
		}
		block := readerBlock(n.Data)
		line = line || block || n.Data == "br"
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, pre || n.Data == "pre")
		}
		line = line || block
		space = space || n.Data == "td" || n.Data == "th"
	}
	walk(root, false)
	return out.String()
}

func readerIgnored(n *html.Node) bool {
	switch n.Data {
	case "head", "script", "style", "noscript", "template":
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "hidden" || (a.Key == "aria-hidden" && a.Val == "true") {
			return true
		}
	}
	return false
}

func readerBlock(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "dd", "details", "div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main", "nav", "ol", "p", "pre", "section", "summary", "table", "tr", "ul":
		return true
	}
	return false
}
