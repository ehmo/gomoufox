package a11y

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestElementUnmarshalKeepsResolver(t *testing.T) {
	payload := `[{"role":"link","name":"Learn more","href":"https://example.org","resolver":"html > body:nth-of-type(1) > a:nth-of-type(1)"}]`
	var elements []Element
	if err := json.Unmarshal([]byte(payload), &elements); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(elements) != 1 {
		t.Fatalf("expected 1 element, got %d", len(elements))
	}
	if elements[0].Resolver != "html > body:nth-of-type(1) > a:nth-of-type(1)" {
		t.Fatalf("resolver dropped during decode: %q", elements[0].Resolver)
	}
}

func TestElementMarshalHidesResolver(t *testing.T) {
	out, err := json.Marshal(Element{Role: "link", Name: "Learn more", Resolver: "#secret"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "resolver") || strings.Contains(string(out), "#secret") {
		t.Fatalf("resolver leaked into output: %s", out)
	}
}

func TestCaptureAndResolveUseDecodedResolver(t *testing.T) {
	store := NewStore()
	nodes := []Element{{Role: "heading", Name: "Title", Resolver: "h1:nth-of-type(1)"}, {Role: "link", Name: "Learn more", Resolver: "a:nth-of-type(1)"}}
	snap := store.Capture("https://example.com", nodes)
	if snap.Items[1].Resolver != "a:nth-of-type(1)" {
		t.Fatalf("capture replaced real resolver: %q", snap.Items[1].Resolver)
	}
	got, err := store.Resolve("e2", nodes)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Resolver != "a:nth-of-type(1)" {
		t.Fatalf("resolved element has wrong resolver: %q", got.Resolver)
	}
	if strings.HasPrefix(got.Resolver, "index:") {
		t.Fatalf("invalid index fallback used: %q", got.Resolver)
	}
}
