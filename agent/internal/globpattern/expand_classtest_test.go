package globpattern

import (
	"strings"
	"testing"
)

// TestExpandCharClassEscapedCloseBracket exercises the character-class scanner
// in findExpandableGroup when an escaped close-bracket (\\]) appears inside
// [...], hitting the `\\` skip branch (lines 79-85). The escaped ] is literal,
// so the class continues until the next unescaped ].
func TestExpandCharClassEscapedCloseBracket(t *testing.T) {
	// [a\]b] is a char class containing 'a', ']', 'b'. The \] is an escaped
	// close bracket, so the class doesn't close until the second ].
	// The brace group after it should still expand.
	got, err := Expand(`[a\]b]{x,y}`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{`[a\]b]x`, `[a\]b]y`}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want[%d]=%q", i, got[i], i, want[i])
		}
	}
}

// TestExpandCharClassEscapedBackslash covers the escape-skip within a
// character class where the escaped char is a backslash itself, exercising
// the `i++` after `\\` detection followed by a normal `]` close (lines 80-85).
func TestExpandCharClassEscapedBackslash(t *testing.T) {
	// [\\] is a char class containing a backslash; the brace group after it
	// should still expand.
	got, err := Expand(`[\\]{a,b}c`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{`[\\]ac`, `[\\]bc`}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want[%d]=%q", i, got[i], i, want[i])
		}
	}
}

// TestExpandCharClassTrailingEscape covers the case where the character-class
// scan hits an escape as the very last byte, triggering the i>=len break at
// line 81 (findExpandableGroup), line 127 (hasTopLevelComma), and line 170
// (splitAlternatives). The char class never closes, so the brace group is
// swallowed as literal class content — no expansion.
func TestExpandCharClassTrailingEscape(t *testing.T) {
	got, err := Expand(`foo[\{ts,tsx}`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(got) != 1 || got[0] != `foo[\{ts,tsx}` {
		t.Fatalf("got %#v, want single literal pattern", got)
	}
}

// TestExpandCommaInCharClass covers the hasTopLevelComma and splitAlternatives
// character-class skip paths (lines 122-138, 164-181) by putting commas and
// braces inside [...], which must be treated as literal.
func TestExpandCommaInCharClass(t *testing.T) {
	// The comma inside the character class must not split the alternative;
	// only the top-level comma in {x,y} should expand.
	got, err := Expand(`file[{,}].{x,y}`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{`file[{,}].x`, `file[{,}].y`}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want[%d]=%q", i, got[i], i, want[i])
		}
	}
}

// TestExpandUnmatchedCloseInCharClass ensures a closing brace that appears
// inside a character class is not counted as an unmatched closing brace.
func TestExpandUnmatchedCloseInCharClass(t *testing.T) {
	// The } inside [] is literal, so there is no unmatched closing brace.
	got, err := Expand(`x[}]{a,b}`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{`x[}]a`, `x[}]b`}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want[%d]=%q", i, got[i], i, want[i])
		}
	}
}

// TestExpandNestedCharClassWithBrace covers a character class containing both
// a comma and a brace inside a brace-group alternative, exercising
// splitAlternatives' char-class skip on a group body (lines 165-181).
func TestExpandNestedCharClassWithBrace(t *testing.T) {
	// The comma inside [a,b] must not split the alternative.
	got, err := Expand(`{[a,b],[c,d]}`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{`[a,b]`, `[c,d]`}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want[%d]=%q", i, got[i], i, want[i])
		}
	}
}

// TestExpandCharClassWithEscapedCloseInGroup covers the hasTopLevelComma
// char-class skip with an escaped close bracket inside the content (lines
// 125-131).
func TestExpandCharClassWithEscapedCloseInGroup(t *testing.T) {
	// Inside the brace group, [a\]b,] has an escaped ] — the comma after the
	// escaped class should still be a top-level comma, so this expands.
	got, err := Expand(`{[a\]b,],x}`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{`[a\]b,]`, `x`}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want[%d]=%q", i, got[i], i, want[i])
		}
	}
}

// TestExpandDepthLimit covers the recursion-depth check (line 37-38) that
// fires before any group is found, by nesting deeply enough to exceed
// MaxExpansions on depth alone.
func TestExpandDepthLimit(t *testing.T) {
	// Each level of nesting adds one to depth. With MaxExpansions=256,
	// nesting deeper than that triggers the depth guard.
	deep := strings.Repeat("{a,", MaxExpansions+1) + strings.Repeat("}", MaxExpansions+1)
	_, err := Expand(deep)
	if err == nil {
		t.Fatal("expected depth-limit error, got nil")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error = %v, want limit message", err)
	}
}

// TestExpandResultCountLimit covers the accumulated result-count cap (line
// 56-57) which fires when the total expanded patterns exceed MaxExpansions
// even though depth is fine.
func TestExpandResultCountLimit(t *testing.T) {
	// 3 alternatives per group, 6 groups = 3^6 = 729 > MaxExpansions(256).
	pattern := strings.Repeat("{a,b,c}", 6)
	_, err := Expand(pattern)
	if err == nil {
		t.Fatal("expected result-count limit error, got nil")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error = %v, want limit message", err)
	}
}

// TestExpandDedup covers the dedup logic in Expand (lines 26-32) where
// duplicate expansion results are removed.
func TestExpandDedup(t *testing.T) {
	// {a,a} produces two "a"s, which should dedup to one.
	got, err := Expand(`{a,a}.go`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("got %#v, want [a.go]", got)
	}
}

// TestExpandCharClassWithBraceInside covers a character class containing a
// literal { which must not be treated as an opening brace by the scanner
// (line 96 path), exercising the char-class skip in findExpandableGroup.
func TestExpandCharClassWithBraceInside(t *testing.T) {
	// [{] is a char class containing a literal {. The { must not open a brace
	// group. The real group {b,c} after it should expand.
	got, err := Expand(`[{]a{b,c}`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{`[{]ab`, `[{]ac`}
	if len(got) != len(want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want[%d]=%q", i, got[i], i, want[i])
		}
	}
}
