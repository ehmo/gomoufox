package mcp

import (
	"encoding/json"
	"testing"
)

func TestBoundedReaderSkipsCodePreservesBlocksAndStopsAcquisition(t *testing.T) {
	output := runNodeExpression(t, `
const read = `+boundedContentExpression+`;
const text = value => ({nodeType:3, nodeValue:value});
const element = (tag, ...children) => {
  for (let i=0;i<children.length-1;i++) children[i].nextSibling=children[i+1];
  return {nodeType:1,tagName:tag,attributes:[],firstChild:children[0]};
};
const body = element('body', element('h1',text('Visible fixture heading')),
  element('p',text('Visible fixture paragraph')),element('script',text('SCRIPT_SENTINEL')),
  element('p',text('Hello '),element('b',text('world')),text('!')));
globalThis.location={href:'https://example.com'};
globalThis.document={body,documentElement:element('html',element('head',element('style',text('x'.repeat(600000)))),body),querySelector:()=>body};
const full=read({maxBytes:1000,includeHTML:false});
const unread={get nodeType(){throw new Error('read past cap')}};
body.firstChild=element('p',text('日本語'.repeat(1000)));
body.firstChild.nextSibling=unread;
const bounded=read({maxBytes:10,includeHTML:false});
console.log(JSON.stringify({full,bounded}));
`)
	var result struct {
		Full struct {
			Text      string
			Truncated bool
		}
		Bounded struct {
			Text      string
			Truncated bool
		}
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.Full.Text != "Visible fixture heading\nVisible fixture paragraph\nHello world!" || result.Full.Truncated {
		t.Fatal(result)
	}
	if result.Bounded.Text != "日本語" || !result.Bounded.Truncated || len(result.Bounded.Text) > 10 {
		t.Fatal(result)
	}
}
