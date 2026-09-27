package appwire

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// Every run of whitespace or control characters, line breaks included,
// becomes one space and the ends are trimmed: a row's why line is one line
// (S1).
func TestExcerptIsOneLine(t *testing.T) {
	got := Excerpt("  Keep or drop\n\nthe implied\toptions?\r\n\x00 ", 200)
	if want := "Keep or drop the implied options?"; got != want {
		t.Fatalf("Excerpt = %q, want %q", got, want)
	}
	if got := Excerpt(" \n\t ", 200); got != "" {
		t.Fatalf("whitespace alone excerpted to %q, want empty", got)
	}
}

// Text at the bound is kept whole; longer text is cut to the bound, ending in
// an ellipsis at the last word break in its second half.
func TestExcerptCutsLongTextAtAWordBreak(t *testing.T) {
	exact := strings.Repeat("a", 20)
	if got := Excerpt(exact, 20); got != exact {
		t.Fatalf("text at the bound = %q, want it whole", got)
	}
	for _, tc := range []struct {
		text string
		max  int
		want string
	}{
		{"the quick brown fox jumps", 12, "the quick…"},
		// The rune past the kept text is a space, so the kept text already
		// ends on a whole word.
		{"hello world again", 12, "hello world…"},
		// No word break in the second half: the word is cut.
		{"a " + strings.Repeat("b", 30), 10, "a bbbbbbb…"},
		{"supercalifragilistic", 8, "superca…"},
		{"ab", 1, "…"},
	} {
		if got := Excerpt(tc.text, tc.max); got != tc.want {
			t.Errorf("Excerpt(%q, %d) = %q, want %q", tc.text, tc.max, got, tc.want)
		}
	}
}

// Invalid UTF-8 is repaired, so the wire never carries a malformed string.
func TestExcerptRepairsInvalidUTF8(t *testing.T) {
	if got, want := Excerpt("ok\xffgo", 200), "ok\ufffdgo"; got != want {
		t.Fatalf("Excerpt = %q, want %q", got, want)
	}
}

func TestExcerptOfNothingIsEmpty(t *testing.T) {
	if got := Excerpt("anything", 0); got != "" {
		t.Fatalf("Excerpt with no room = %q, want empty", got)
	}
}

// Whatever it is given, an excerpt is valid UTF-8, one line with no leading or
// trailing space, never longer than its bound, and its own excerpt: the hub
// schema accepts a value only when Excerpt leaves it unchanged, so a value the
// projector cut always passes.
func TestExcerptNeverExceedsItsBound(t *testing.T) {
	inputs := []string{
		"",
		"short",
		strings.Repeat("word ", 400),
		strings.Repeat("😀", 300),
		"line one\nline two\n\nline three",
		"\xff\xfe" + strings.Repeat("x y ", 100),
		"para\u2028graph\u00a0with\x00control\u0085runes " + strings.Repeat("é ", 30),
		strings.Repeat("a", 1<<20),
	}
	for _, text := range inputs {
		for maxRunes := 1; maxRunes <= 40; maxRunes++ {
			got := Excerpt(text, maxRunes)
			if !utf8.ValidString(got) || strings.ContainsAny(got, "\r\n\t") ||
				got != strings.TrimSpace(got) || utf8.RuneCountInString(got) > maxRunes ||
				Excerpt(got, maxRunes) != got {
				t.Fatalf("Excerpt(%.20q…, %d) = %q, want one trimmed valid line of at most %d runes that is its own excerpt", text, maxRunes, got, maxRunes)
			}
		}
	}
}

// A question leaves the session, and reaches a row, as one bounded line with
// at most five one-line labels, none of them empty (S1b).
func TestBoundedPendingQuestionCutsTheQuestionToTheWireBounds(t *testing.T) {
	got := BoundedPendingQuestion(
		"Keep or drop\nthe implied options? "+strings.Repeat("Fourteen descriptions mention flags. ", 20),
		[]string{"Drop them", " \n ", strings.Repeat("x", 200), "B", "C", "D", "E"},
		2,
	)
	if !strings.HasPrefix(got.Question, "Keep or drop the implied options? Fourteen") || !strings.HasSuffix(got.Question, "…") ||
		utf8.RuneCountInString(got.Question) > MaxQuestionTextRunes {
		t.Fatalf("question = %q, want one line cut to %d runes", got.Question, MaxQuestionTextRunes)
	}
	want := []string{"Drop them", strings.Repeat("x", MaxQuestionOptionRunes-1) + "…", "B", "C", "D"}
	if !slices.Equal(got.Options, want) {
		t.Fatalf("options = %q, want %q", got.Options, want)
	}
	if got.Count != 2 {
		t.Fatalf("count = %d, want the count it was given", got.Count)
	}
}
