package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserFetchFilePathIsBoundedAndGated(t *testing.T) {
	cfg := defaultTestConfig(t)
	cfg.Validator = &fakeValidator{}
	cfg.Policy.AllowBrowserFetch = true
	cfg.Policy.AllowedHosts = []string{"example.com"}
	session := &fakeBrowserSession{fetchResult: fetchResult{Status: 200, Body: []byte("ok")}}
	cfg.BrowserFactory = &fakeBrowserFactory{session: session}
	data := bytes.Repeat([]byte{0, 255, 1, 128}, 512*1024)
	path := filepath.Join(cfg.SessionDir, "upload.bin")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	args := raw(`{"url":"https://example.com","method":"POST","body_path":"upload.bin"}`)
	server := newTestServer(t, cfg)
	assertError(t, server.Handle(context.Background(), "browser_fetch", args), "browser_file_fetch_disabled")
	if len(session.fetchCalls) != 0 {
		t.Fatal("disabled file fetch reached browser")
	}
	cfg.Policy.AllowBrowserFileFetch = true
	server = newTestServer(t, cfg)
	response := server.Handle(context.Background(), "browser_fetch", args)
	if response.IsError || len(session.fetchCalls) != 1 || !bytes.Equal(session.fetchCalls[0].Body, data) {
		t.Fatalf("file fetch=%#v", response)
	}
	for _, test := range []struct{ body, code string }{
		{`{"body":"inline","body_path":"upload.bin"}`, "invalid_arguments"},
		{`{"body_path":"../outside.bin"}`, "path_rejected"},
		{`{"body_path":""}`, "invalid_arguments"},
		{`{"body_path":"upload.bin","timeout_ms":120001}`, "invalid_arguments"},
	} {
		var fields map[string]any
		_ = json.Unmarshal([]byte(test.body), &fields)
		fields["url"] = "https://example.com"
		in, _ := json.Marshal(fields)
		assertError(t, server.Handle(context.Background(), "browser_fetch", in), test.code)
	}
	if err := os.Truncate(path, maxUploadFileBytes+1); err != nil {
		t.Fatal(err)
	}
	assertError(t, server.Handle(context.Background(), "browser_fetch", args), "file_too_large")
}

func TestMCPFetchPreservesBinaryRequestAndResponse(t *testing.T) {
	output := runNodeExpression(t, `
const run = `+mcpFetchExpression+`;
let sent;
globalThis.fetch = async (url, options) => {
  sent = Array.from(options.body);
  let read = false;
  return {url,status:200,headers:{forEach:()=>{}},body:{getReader:()=>({
    read:async()=>{if(read)return {done:true};read=true;return {done:false,value:new Uint8Array([255,0,1])};},cancel:async()=>{}
  })}};
};
run({url:"https://example.com",method:"POST",headers:{},bodyBase64:"/wAB",maxBytes:10}).then(result=>console.log(JSON.stringify({sent,result})));
`)
	var payload struct {
		Sent   []int
		Result struct {
			Body string `json:"body_base64"`
		}
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		t.Fatal(err)
	}
	got, err := base64.StdEncoding.DecodeString(payload.Result.Body)
	if err != nil || !bytes.Equal(got, []byte{255, 0, 1}) || len(payload.Sent) != 3 || payload.Sent[0] != 255 {
		t.Fatalf("binary round trip = %s", output)
	}
	page := &fakeMCPPage{fetchPayload: map[string]any{"ok": true, "body_base64": "/wAB", "status": 200}}
	session := &gomoufoxSession{page: page}
	result, err := session.Fetch(context.Background(), fetchOptions{URL: "https://example.com", Method: "POST", Body: []byte{255, 0, 1}})
	if err != nil || !bytes.Equal(result.Body, got) || page.internalEvaluateArg.(map[string]any)["bodyBase64"] != "/wAB" {
		t.Fatalf("adapter result=%#v, err=%v", result, err)
	}
}
