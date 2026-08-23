package openaicompat

import (
	"math"
	"testing"
)

// TestPerTokenToPerMillion covers the edge cases in perTokenCostToPerMillion
// (lines 200-201, 203-204, 220, 226-227).
func TestPerTokenCostToPerMillion(t *testing.T) {
	// Empty string returns nil (line 195-196).
	if got := perTokenCostToPerMillion(""); got != nil {
		t.Fatal("empty string should return nil")
	}
	// Invalid float returns nil (lines 200-201).
	if got := perTokenCostToPerMillion("not-a-number"); got != nil {
		t.Fatal("invalid float should return nil")
	}
	// NaN returns nil (lines 203-204).
	if got := perTokenCostToPerMillion("NaN"); got != nil {
		t.Fatal("NaN should return nil")
	}
	// Inf returns nil (lines 203-204).
	if got := perTokenCostToPerMillion("Inf"); got != nil {
		t.Fatal("Inf should return nil")
	}
	// Valid value returns per-million.
	got := perTokenCostToPerMillion("0.001")
	if got == nil || *got != 1000 {
		t.Fatalf("perTokenCostToPerMillion(0.001) = %v, want 1000", got)
	}
}

// TestDedupStringsEmpty covers the empty input (line 213) and all-empty output
// (lines 225-227).
func TestDedupStringsEmpty(t *testing.T) {
	if got := dedupStrings(nil); got != nil {
		t.Fatal("nil input should return nil")
	}
	if got := dedupStrings([]string{"", "  "}); got != nil {
		t.Fatal("all-empty input should return nil")
	}
}

// TestDedupStringsTrimsAndDedupes covers trimming and dedup (lines 218-220).
func TestDedupStringsTrimsAndDedupes(t *testing.T) {
	got := dedupStrings([]string{" a ", "a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("dedupStrings = %v, want [a b]", got)
	}
}

// suppress unused import
var _ = math.NaN
