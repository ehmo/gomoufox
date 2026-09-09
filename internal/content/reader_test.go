package content

import (
	"strings"
	"testing"
)

func TestReaderTextPreservesBoundariesWithoutCode(t *testing.T) {
	for _, tc := range []struct{ html, want string }{
		{`<h1>Visible fixture heading</h1><p>Visible fixture paragraph</p><label>Name <input></label><button>Update</button><script>window.SCRIPT_SENTINEL='private code'</script>`, "Visible fixture heading\nVisible fixture paragraph\nName Update"},
		{`<div>Hello <b>world</b>!</div>After<br>line`, "Hello world!\nAfter\nline"},
		{`<p>one</p><style>style sentinel</style><p>two</p><template>template sentinel</template><noscript>noscript sentinel</noscript><div hidden>hidden sentinel</div><span aria-hidden="true">aria sentinel</span>`, "one\ntwo"},
		{`<ul><li>one</li><li>two</li></ul><table><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></table>`, "one\ntwo\na b\nc d"},
		{"<pre>first\n  second\r\nthird</pre><p>日本語 中文 한국어</p>", "first\nsecond\nthird\n日本語 中文 한국어"},
		{`<head><style>` + strings.Repeat("x", 600000) + `</style></head><body><h1>BODY_AFTER_LARGE_HEAD</h1><p>reader content</p></body>`, "BODY_AFTER_LARGE_HEAD\nreader content"},
		{"", ""},
		{`<html><frameset><frame src="https://example.com"></frameset></html>`, ""},
		{strings.Repeat("<div>", 600), ""},
	} {
		if got := ReaderText(tc.html); got != tc.want {
			t.Fatalf("reader text = %q; want %q", got, tc.want)
		}
	}
}
