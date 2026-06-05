package a11y

import (
	"errors"
	"testing"
)

func TestCaptureAssignsRefsAndOccurrences(t *testing.T) {
	store := NewStore()
	snap := store.Capture("https://example.com", []Element{
		{Role: "button", Name: "Save", Resolver: "css:#a"},
		{Role: "button", Name: "Save", Resolver: "css:#b"},
	})
	if snap.Counter != 1 || snap.Items[0].Ref != "e1" || snap.Items[1].Ref != "e2" {
		t.Fatalf("snapshot = %#v", snap)
	}
	if snap.Items[0].Occurrence != 1 || snap.Items[1].Occurrence != 2 {
		t.Fatalf("occurrences = %#v", snap.Items)
	}
}

func TestCaptureDoesNotStoreSnapshotValuesInRefs(t *testing.T) {
	store := NewStore()
	snap := store.Capture("https://example.com", []Element{
		{Role: "textbox", Name: "Email", Value: "user@example.com", ValueKind: "safe", Resolver: "#email"},
	})
	if snap.Items[0].Value != "user@example.com" || snap.Items[0].ValueKind != "safe" {
		t.Fatalf("snapshot item lost display value = %#v", snap.Items[0])
	}
	if got := store.refs["e1"]; got.Value != "" || got.ValueKind != "" {
		t.Fatalf("stored ref retained value = %#v", got)
	}
}

func TestResolveDetectsStaleAndAmbiguous(t *testing.T) {
	store := NewStore()
	store.Capture("https://example.com", []Element{{Role: "button", Name: "Save", Resolver: "stable"}})
	if got, err := store.Resolve("e1", []Element{{Role: "button", Name: "Save", Resolver: "stable"}}); err != nil || got.Ref != "e1" {
		t.Fatalf("resolve = %#v, %v", got, err)
	}
	if _, err := store.Resolve("e1", []Element{{Role: "button", Name: "Save", Resolver: "other"}}); !errors.Is(err, ErrStaleRef) {
		t.Fatalf("stale err = %v", err)
	}
	if _, err := store.Resolve("missing", []Element{}); !errors.Is(err, ErrUnknownRef) {
		t.Fatalf("unknown err = %v", err)
	}
	store.refs["e1"] = Element{Role: "button", Name: "Save", Resolver: "same"}
	if _, err := store.Resolve("e1", []Element{
		{Role: "button", Name: "Save", Resolver: "same"},
		{Role: "button", Name: "Save", Resolver: "same", Occurrence: 1},
	}); !errors.Is(err, ErrAmbiguousRef) {
		t.Fatalf("ambiguous err = %v", err)
	}
}

func TestResolveUsesDefaultResolverAndRejectsOccurrenceDrift(t *testing.T) {
	store := NewStore()
	store.Capture("https://example.com", []Element{{Role: "link", Name: "Docs"}})
	if got, err := store.Resolve("e1", []Element{{Role: "link", Name: "Docs"}}); err != nil || got.Resolver != "index:0" {
		t.Fatalf("default resolver resolve = %#v, %v", got, err)
	}

	store.Capture("https://example.com", []Element{
		{Role: "button", Name: "Save", Resolver: "same"},
		{Role: "button", Name: "Save", Resolver: "same"},
	})
	if _, err := store.Resolve("e2", []Element{{Role: "button", Name: "Save", Resolver: "same"}}); !errors.Is(err, ErrStaleRef) {
		t.Fatalf("occurrence drift err = %v", err)
	}
}

func TestClearInvalidatesRefs(t *testing.T) {
	store := NewStore()
	store.Capture("https://example.com", []Element{{Role: "link", Name: "A"}})
	store.Clear()
	if _, err := store.Resolve("e1", []Element{{Role: "link", Name: "A"}}); !errors.Is(err, ErrUnknownRef) {
		t.Fatalf("err = %v", err)
	}
}
