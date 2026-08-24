package msgrender

import (
	"testing"
)

// TestShellSyntheticBoundaryEndTrailingWhitespaceEOF covers the case
// where the boundary is at EOF after trailing whitespace (returns -1).
func TestShellSyntheticBoundaryEndTrailingWhitespaceEOF(t *testing.T) {
	if got := shellSyntheticBoundaryEnd("echo ok   ", 7); got != -1 {
		t.Fatalf("trailing whitespace to EOF should return -1, got %d", got)
	}
}

// TestShellSyntheticBoundaryEndNewline covers the case where the next
// non-whitespace char is a newline (returns -1).
func TestShellSyntheticBoundaryEndNewline(t *testing.T) {
	if got := shellSyntheticBoundaryEnd("echo \nrest", 4); got != -1 {
		t.Fatalf("newline after whitespace should return -1, got %d", got)
	}
}

// TestShellSyntheticBoundaryEndBackslashNewline covers the case where
// the next non-whitespace is a backslash-newline continuation (returns -1).
func TestShellSyntheticBoundaryEndBackslashNewline(t *testing.T) {
	if got := shellSyntheticBoundaryEnd("echo \\\nrest", 4); got != -1 {
		t.Fatalf("backslash-newline after whitespace should return -1, got %d", got)
	}
}

// TestShellSyntheticBoundaryEndNormalChar covers the normal return.
func TestShellSyntheticBoundaryEndNormalChar(t *testing.T) {
	if got := shellSyntheticBoundaryEnd("echo ok", 4); got != 5 {
		t.Fatalf("normal char should return its index, got %d", got)
	}
}

// TestShellCommentAtLineStart covers the index == lineStart case.
func TestShellCommentAtLineStart(t *testing.T) {
	if !shellCommentAt("cmd", 0, 0) {
		t.Fatal("comment at line start should return true")
	}
}

// TestShellCommentAtAfterNonWhitespace covers the case where the
// previous char is not space or tab (returns false).
func TestShellCommentAtAfterNonWhitespace(t *testing.T) {
	if shellCommentAt("echo#hi", 4, 0) {
		t.Fatal("comment after non-whitespace should return false")
	}
}

// TestShellCommentAtAfterSpace covers the case where the previous
// char is a space and backslash count is even (returns true).
func TestShellCommentAtAfterSpace(t *testing.T) {
	if !shellCommentAt("echo #hi", 5, 0) {
		t.Fatal("comment after space with even backslashes should return true")
	}
}

// TestShellCommentAtAfterSpaceOddBackslashes covers the case where
// there are an odd number of preceding backslashes (returns false).
func TestShellCommentAtAfterSpaceOddBackslashes(t *testing.T) {
	if shellCommentAt("echo \\#hi", 6, 0) {
		t.Fatal("comment after odd backslashes should return false")
	}
}
