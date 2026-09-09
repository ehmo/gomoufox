package cli

import "testing"

func TestGetAndEvalViewportFlags(t *testing.T) {
	for _, size := range []string{"0", "-1", "100001", "bad"} {
		if _, err := parseGet([]string{"https://example.com", "--width", size}); err == nil {
			t.Fatalf("get accepted width %s", size)
		}
		if _, err := parseEval([]string{"https://example.com", "--script", "innerWidth", "--height", size}); err == nil {
			t.Fatalf("eval accepted height %s", size)
		}
	}
	if _, err := parseGet([]string{"https://example.com", "--width", "390", "--height", "844"}); err != nil {
		t.Fatal(err)
	}
	if _, err := parseEval([]string{"https://example.com", "--script", "innerWidth", "--width", "390", "--height", "844"}); err != nil {
		t.Fatal(err)
	}
}
