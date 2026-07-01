package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestW2Tail_lineAt_Bounds(t *testing.T) {
	lines := []string{"a", "b"}
	if lineAt(lines, -1) != "" {
		t.Errorf("negative idx should return empty")
	}
	if lineAt(lines, 2) != "" {
		t.Errorf("out-of-range idx should return empty")
	}
	if lineAt(lines, 1) != "b" {
		t.Errorf("in-range idx wrong")
	}
}

func TestW2Tail_lineMatchesMode_AllModes(t *testing.T) {
	if !lineMatchesMode("x", "x", matchExact) {
		t.Errorf("matchExact equal failed")
	}
	if !lineMatchesMode("x  ", "x", matchTrimRight) {
		t.Errorf("matchTrimRight failed")
	}
	if !lineMatchesMode("  x  ", "x", matchTrimBoth) {
		t.Errorf("matchTrimBoth failed")
	}
	if !lineMatchesMode(" x ", "x", matchUnicodeNormalized) {
		t.Errorf("matchUnicodeNormalized failed")
	}
	if !lineMatchesMode("x", "x", matchFuzzyLine) {
		t.Errorf("matchFuzzyLine failed")
	}
	// Unknown mode falls through to the default false branch.
	if lineMatchesMode("x", "x", lineMatchMode(999)) {
		t.Errorf("unknown mode should return false")
	}
}

func TestW2Tail_hintFromHunk_NoHint(t *testing.T) {
	if got := hintFromHunk([]string{" one", "-two", "+TWO"}); got != "" {
		t.Errorf("hintFromHunk with no @@ = %q, want empty", got)
	}
	if got := hintFromHunk([]string{"@@ funcSig", " one"}); got != "funcSig" {
		t.Errorf("hintFromHunk = %q", got)
	}
}

func TestW2Tail_formatExpectedLines_Truncates(t *testing.T) {
	many := make([]string, 15)
	for i := range many {
		many[i] = "line"
	}
	out := formatExpectedLines(many)
	if !strings.Contains(out, "3 more lines omitted") {
		t.Errorf("expected omission note, got:\n%s", out)
	}

	short := formatExpectedLines([]string{"only"})
	if strings.Contains(short, "omitted") {
		t.Errorf("short list should not omit: %s", short)
	}
}

func TestW2Tail_mismatchLineForMissingSequence_Fallbacks(t *testing.T) {
	// No fuzzy match anywhere: falls back to min(searchStart+1, len).
	if got := mismatchLineForMissingSequence([]string{"a", "b", "c"}, "zzz", 1); got != 2 {
		t.Errorf("no-match fallback = %d, want 2", got)
	}
	// Empty file returns 0.
	if got := mismatchLineForMissingSequence(nil, "x", 0); got != 0 {
		t.Errorf("empty-file = %d, want 0", got)
	}
	// A match returns its 1-based line.
	if got := mismatchLineForMissingSequence([]string{"a", "target", "c"}, "target", 0); got != 2 {
		t.Errorf("match = %d, want 2", got)
	}
}

// ApplyPatch returns an error when an Update targets a file that does not
// exist (ReadFile error path in updateFileOp.apply).
func TestW2Tail_ApplyPatch_UpdateMissingFile(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Update File: nope.txt\n@@\n one\n-two\n+TWO\n three\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatalf("expected error updating a missing file")
	}
}

// ApplyPatch rejects a Delete whose path escapes the root (safeJoin error in
// deleteFileOp.apply).
func TestW2Tail_ApplyPatch_DeleteTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Delete File: ../escape.txt\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatalf("expected traversal delete to be rejected")
	}
	// Sanity: a normal delete of an existing file succeeds.
	_ = os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("x\n"), 0o644)
	if _, err := ApplyPatch(dir, "*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch\n"); err != nil {
		t.Fatalf("normal delete failed: %v", err)
	}
}
