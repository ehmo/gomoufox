package content

import (
	"testing"
	"unicode/utf8"
)

func TestTextTruncationHonorsByteBudgetAndCodePoints(t *testing.T) {
	for _, value := range []string{"", "ascii", "é界x", "日本語", "a🙂b"} {
		for limit := -1; limit <= len(value)+1; limit++ {
			out, truncated := TruncateUTF8(value, limit)
			if !utf8.ValidString(out) || (limit >= 0 && len(out) > limit) || truncated != (limit >= 0 && len(value) > limit) {
				t.Fatalf("value=%q limit=%d out=%q truncated=%v", value, limit, out, truncated)
			}
		}
	}
	result, err := Extract("", "日本語", "", FormatText, 4)
	if err != nil || result.Content != "日" || result.Bytes != 3 || !result.Truncated || result.ExtractionMethod != "reader_text" {
		t.Fatalf("bounded extraction: %+v %v", result, err)
	}
}
