package gomoufox

import (
	"encoding/json"
	"testing"
)

func TestFileFetchBudgetStaysSeparateFromTextCap(t *testing.T) {
	for _, opts := range []FetchBytesOptions{{MaxFileBytes: -1}, {MaxFileBytes: MaxTransferFileBytes + 1}, {MaxBytes: 10, MaxFileBytes: 20}} {
		if _, err := fetchMaxBytes(opts); err == nil {
			t.Fatalf("invalid file budget accepted: %#v", opts)
		}
	}
	if n, err := fetchMaxBytes(FetchBytesOptions{MaxFileBytes: MaxTransferFileBytes}); err != nil || n != MaxTransferFileBytes {
		t.Fatalf("file budget=%d, %v", n, err)
	}
	if _, err := fetchMaxBytes(FetchBytesOptions{MaxBytes: MaxTransferFileBytes}); err == nil {
		t.Fatal("text budget accepted file limit")
	}
}

func TestFileFetchVerifiesEOFBeyondExactLimit(t *testing.T) {
	output := runRootNodeExpression(t, `
 const read = `+browserFetchExpression+`;
 globalThis.fetch = async () => ({url:"https://example.com/file",status:200,headers:{forEach:()=>{}},body:{getReader:()=>{
 let count=0;
 return {read:async()=>++count===1?{done:false,value:new Uint8Array([0,255,1])}:{done:true},cancel:async()=>{}};
 }}});
 read({url:"https://example.com/file",method:"GET",headers:{},maxBytes:3,verifyComplete:true}).then(r=>console.log(JSON.stringify(r)));
 `)
	var got map[string]any
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	if got["body_base64"] != "AP8B" || got["truncated"] != false {
		t.Fatalf("complete exact-sized binary: %#v", got)
	}
}
