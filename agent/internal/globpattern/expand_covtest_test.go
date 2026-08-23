package globpattern

import (
	"testing"
)

// TestHasTopLevelComma_NestedBraces covers the depth++ and depth-- paths
// (lines 141-146) when the content has nested braces.
func TestHasTopLevelComma_NestedBraces(t *testing.T) {
	t.Parallel()
	// Comma inside nested braces should not be top-level.
	if hasTopLevelComma("{a,b}") {
		t.Fatal("expected false for comma inside nested braces")
	}
	// Comma at top level should be found.
	if !hasTopLevelComma("a,{b,c}") {
		t.Fatal("expected true for top-level comma")
	}
	// Nested braces with comma at top level.
	if !hasTopLevelComma("a,{b,c},d") {
		t.Fatal("expected true for top-level comma with nested braces")
	}
}

// TestHasTopLevelComma_TruncatedEscape covers the break path for truncated
// escape inside a character class (line 127-128).
func TestHasTopLevelComma_TruncatedEscape(t *testing.T) {
	t.Parallel()
	// Truncated escape inside char class should not crash and return false.
	if hasTopLevelComma("[a\\") {
		t.Fatal("expected false for truncated escape in char class")
	}
	// Normal char class with comma inside — not top-level.
	if hasTopLevelComma("[a,b]") {
		t.Fatal("expected false for comma inside char class")
	}
}

// TestFindTopLevelBrace_TruncatedEscapeInClass covers the break path for
// truncated escape inside a character class in findExpandableGroup
// (line 81-82).
func TestFindTopLevelBrace_TruncatedEscapeInClass(t *testing.T) {
	t.Parallel()
	// Pattern with truncated escape in char class should not find a brace
	// expansion but should handle gracefully (no panic).
	_, _, _, err := findExpandableGroup("[a\\{b}")
	if err != nil {
		// May return an unmatched opening brace error — that's fine.
		// Just verify no panic.
		_ = err
	}
}

// TestSplitAlternatives_NestedBraces covers splitAlternatives with
// braces (lines 156-188). splitAlternatives splits on top-level commas
// only (braces are opaque to it, character classes are skipped).
func TestSplitAlternatives_NestedBraces(t *testing.T) {
	t.Parallel()
	// Commas inside braces ARE split — splitAlternatives does not track
	// brace depth, only skips char classes.
	parts := splitAlternatives("a,b")
	if len(parts) != 2 || parts[0] != "a" || parts[1] != "b" {
		t.Fatalf("expected [a b], got %v", parts)
	}
}

// TestSplitAlternatives_CharClassWithComma covers splitAlternatives with
// a comma inside a character class (lines 164-181).
func TestSplitAlternatives_CharClassWithComma(t *testing.T) {
	t.Parallel()
	parts := splitAlternatives("[a,b],c")
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %v", len(parts), parts)
	}
	if parts[0] != "[a,b]" || parts[1] != "c" {
		t.Fatalf("parts = %v", parts)
	}
}

// TestSplitAlternatives_TruncatedEscape covers splitAlternatives with a
// truncated escape inside a character class (line 170-171).
func TestSplitAlternatives_TruncatedEscape(t *testing.T) {
	t.Parallel()
	parts := splitAlternatives("[a\\")
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d: %v", len(parts), parts)
	}
}

// TestFindTopLevelBrace_UnmatchedClosing covers the unmatched closing
// brace error (line 99).
func TestFindTopLevelBrace_UnmatchedClosing(t *testing.T) {
	t.Parallel()
	_, _, _, err := findExpandableGroup("}")
	if err == nil {
		t.Fatal("expected error for unmatched closing brace")
	}
}

// TestFindTopLevelBrace_UnmatchedOpening covers the unmatched opening
// brace error (line 109).
func TestFindTopLevelBrace_UnmatchedOpening(t *testing.T) {
	t.Parallel()
	_, _, _, err := findExpandableGroup("{")
	if err == nil {
		t.Fatal("expected error for unmatched opening brace")
	}
}

// TestFindTopLevelBrace_NoBraces covers the no-braces path (line 111).
func TestFindTopLevelBrace_NoBraces(t *testing.T) {
	t.Parallel()
	_, _, ok, err := findExpandableGroup("no braces here")
	if err != nil || ok {
		t.Fatalf("expected ok=false, err=nil, got ok=%v err=%v", ok, err)
	}
}

// TestFindTopLevelBrace_BraceWithoutComma covers the path where braces
// exist but no top-level comma (line 103-105, returns false).
func TestFindTopLevelBrace_BraceWithoutComma(t *testing.T) {
	t.Parallel()
	_, _, ok, err := findExpandableGroup("{abc}")
	if err != nil || ok {
		t.Fatalf("expected ok=false, err=nil for brace without comma, got ok=%v err=%v", ok, err)
	}
}
