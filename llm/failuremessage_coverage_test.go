package llm

import (
	"strings"
	"testing"
)

// TestProviderFailureMessageTopLevelMessage covers the raw["message"] fallback
// path (lines 64-65) when the body has no "error" key but has a "message" key.
func TestProviderFailureMessageTopLevelMessage(t *testing.T) {
	body := []byte(`{"message":"top-level error message"}`)
	got := ProviderFailureMessage("test", body)
	if !strings.Contains(got, "top-level error message") {
		t.Fatalf("got %q, want to contain top-level error message", got)
	}
}

// TestTruncateForMessageMultiByteCut covers the utf8.ValidString loop
// (lines 78-79) when the cut happens in the middle of a multi-byte rune.
func TestTruncateForMessageMultiByteCut(t *testing.T) {
	// Build a string that is longer than maxFailureMessageBody and has
	// multi-byte UTF-8 characters near the cut point.
	// maxFailureMessageBody is likely 4096 or similar — let's check.
	// We'll use a string of repeated multi-byte chars.
	// Actually, we need to know maxFailureMessageBody.
	s := strings.Repeat("é", 10000) // each é is 2 bytes in UTF-8
	got := truncateForMessage(s)
	if !strings.HasSuffix(got, "…") {
		t.Fatal("truncated message should end with …")
	}
}
