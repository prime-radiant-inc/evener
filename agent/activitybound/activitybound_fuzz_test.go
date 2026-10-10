package activitybound

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// FuzzReportPreview drives the bounded decode seam that turns a reported
// packet's raw message into a delegate's report preview. The raw bytes probe
// the window for panics and bound violations; the same bytes re-encoded as a
// JSON string probe the trim, rune cap and proof-of-truncation rule against the
// value the durable packet holds.
func FuzzReportPreview(f *testing.F) {
	f.Add([]byte(`"a settled report"`))
	f.Add([]byte(``))
	f.Add([]byte(`"`))
	f.Add([]byte(`"\ud83d\ude00 and more"`))
	f.Add([]byte(" \n\t\"  spaced  \""))
	f.Add([]byte(`1234`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		window, _ := BoundMessage(raw)
		if len(window) > MaxReportPreviewBytes {
			t.Fatalf("window is %d bytes, over the %d-byte bound", len(window), MaxReportPreviewBytes)
		}
		preview, truncated := ReportPreview(raw)
		if runes := utf8.RuneCountInString(preview); runes > MaxDelegateProseRunes {
			t.Fatalf("preview is %d runes, over the %d-rune cap", runes, MaxDelegateProseRunes)
		}
		if !utf8.ValidString(preview) {
			t.Fatalf("preview is not valid UTF-8")
		}
		// A value re-encoded as a JSON string literal is whole, so its preview
		// must be exactly the capped form of that value. Invalid bytes are
		// re-encoded as replacement runes and so are no longer the value that
		// decodes back; the raw assertions above already cover those.
		encoded, err := json.Marshal(string(raw))
		if err != nil || !utf8.Valid(raw) || len(encoded) > MaxReportPreviewBytes {
			return
		}
		value := string(raw)
		got, gotTruncated := ReportPreview(encoded)
		want := Truncate(value, MaxDelegateProseRunes)
		if got != want {
			t.Fatalf("ReportPreview(%q) = %q, want %q", value, got, want)
		}
		if gotTruncated != (want != value) {
			t.Fatalf("truncated = %v for %q, want %v", gotTruncated, value, want != value)
		}
		_ = truncated
	})
}
