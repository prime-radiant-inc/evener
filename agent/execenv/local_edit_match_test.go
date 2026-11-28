package execenv

import (
	"slices"
	"strings"
	"testing"
)

// findFuzzyMatch returns the file's first whitespace-normalized match for
// oldString, or "" if no match. It delegates to findFuzzyMatchSpans so
// existing direct test callers (line_window_test.go,
// local_edge_program_fuzz_test.go) continue to exercise findFuzzyMatchSpans.
func findFuzzyMatch(content, oldString string) string {
	// Every accepted span is non-empty and non-overlapping, so content cannot
	// hold more than len(content) of them.
	spans, _, _ := findFuzzyMatchSpans(content, oldString, true, len(content)+1)
	if len(spans) == 0 {
		return ""
	}
	return content[spans[0].start:spans[0].end]
}

// TestFindFuzzyMatchSpans_SingleAndMultiLineOffsets pins that every accepted
// span is an exact byte range of the file content: the returned offsets must
// slice the file's own whitespace variant of the needle, not a normalized copy.
func TestFindFuzzyMatchSpans_SingleAndMultiLineOffsets(t *testing.T) {
	t.Parallel()
	content := "alpha\n  foo    bar  \n\tbaz\tqux\nomega\n"

	t.Run("single line", func(t *testing.T) {
		t.Parallel()
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "foo bar", false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 {
			t.Fatalf("findFuzzyMatchSpans single-line = (%v, %v, %v), want one span", spans, ambigLines, limitExceeded)
		}
		want := "  foo    bar  "
		start := strings.Index(content, want)
		if got := content[spans[0].start:spans[0].end]; got != want {
			t.Fatalf("single-line span = %q, want %q", got, want)
		}
		if spans[0].start != start || spans[0].end != start+len(want) {
			t.Fatalf("single-line offsets = [%d,%d), want [%d,%d)", spans[0].start, spans[0].end, start, start+len(want))
		}
	})

	t.Run("multi line", func(t *testing.T) {
		t.Parallel()
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "foo bar\nbaz qux", false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 {
			t.Fatalf("findFuzzyMatchSpans multi-line = (%v, %v, %v), want one span", spans, ambigLines, limitExceeded)
		}
		want := "  foo    bar  \n\tbaz\tqux"
		start := strings.Index(content, want)
		if got := content[spans[0].start:spans[0].end]; got != want {
			t.Fatalf("multi-line span = %q, want %q", got, want)
		}
		if spans[0].start != start || spans[0].end != start+len(want) {
			t.Fatalf("multi-line offsets = [%d,%d), want [%d,%d)", spans[0].start, spans[0].end, start, start+len(want))
		}
	})
}

// TestFindFuzzyMatchSpans_EmptyNormalizedNeedle guards the whitespace-only
// needle: normalizing to "" must match nothing, in either collection mode, so
// a tab-only old_string never selects an empty line.
func TestFindFuzzyMatchSpans_EmptyNormalizedNeedle(t *testing.T) {
	t.Parallel()
	content := "one\n\nthree\n"
	for _, replaceAll := range []bool{false, true} {
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "\t  ", replaceAll, 10)
		if spans != nil || ambigLines != nil || limitExceeded {
			t.Fatalf("findFuzzyMatchSpans(whitespace-only, replaceAll=%v) = (%v, %v, %v), want (nil, nil, false)",
				replaceAll, spans, ambigLines, limitExceeded)
		}
	}
}

// TestFindFuzzyMatchSpans_AmbiguousLineNumbers pins the single-match contract:
// two or more distinct whitespace variants are ambiguous, and the candidates
// name their 1-based file lines without rescanning.
func TestFindFuzzyMatchSpans_AmbiguousLineNumbers(t *testing.T) {
	t.Parallel()
	content := "  foo bar\nmiddle\n\tfoo\tbar\nother\nfoo   bar\n"
	spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "foo bar", false, 10)
	if spans != nil {
		t.Fatalf("ambiguous match returned spans %v, want nil", spans)
	}
	if limitExceeded {
		t.Fatalf("ambiguous match reported a span-limit overflow")
	}
	if want := []int{1, 3, 5}; !slices.Equal(ambigLines, want) {
		t.Fatalf("ambiguous line numbers = %v, want %v", ambigLines, want)
	}

	// The candidate list stops at ten lines even when more variants match.
	var many strings.Builder
	for i := range 12 {
		if i > 0 {
			many.WriteString("middle\n")
		}
		many.WriteString("  foo bar\n")
	}
	spans, ambigLines, limitExceeded = findFuzzyMatchSpans(many.String(), "foo bar", false, 100)
	if spans != nil || limitExceeded {
		t.Fatalf("many-variant match = (%v, %v, %v), want nil spans without overflow", spans, ambigLines, limitExceeded)
	}
	if want := []int{1, 3, 5, 7, 9, 11, 13, 15, 17, 19}; !slices.Equal(ambigLines, want) {
		t.Fatalf("capped ambiguous line numbers = %v, want %v", ambigLines, want)
	}
}

// TestFindFuzzyMatchSpans_ReplaceAllNonOverlapping pins replace_all collection:
// every whitespace variant is returned by offset, and a matched multi-line
// window is consumed so its overlapping successor windows are not emitted.
func TestFindFuzzyMatchSpans_ReplaceAllNonOverlapping(t *testing.T) {
	t.Parallel()

	t.Run("single line", func(t *testing.T) {
		t.Parallel()
		content := "  foo bar\nmiddle\n\tfoo\tbar\nfoo   bar\n"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "foo bar", true, 100)
		if limitExceeded || ambigLines != nil {
			t.Fatalf("replace_all single-line = (%v, %v, %v)", spans, ambigLines, limitExceeded)
		}
		want := []string{"  foo bar", "\tfoo\tbar", "foo   bar"}
		if len(spans) != len(want) {
			t.Fatalf("replace_all single-line spans = %v, want %d", spans, len(want))
		}
		for i, w := range want {
			if got := content[spans[i].start:spans[i].end]; got != w {
				t.Fatalf("replace_all single-line span %d = %q, want %q", i, got, w)
			}
		}
	})

	t.Run("multi line window consumed", func(t *testing.T) {
		t.Parallel()
		content := "a\na\na"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "a\na", true, 100)
		if limitExceeded || ambigLines != nil {
			t.Fatalf("replace_all multi-line = (%v, %v, %v)", spans, ambigLines, limitExceeded)
		}
		if len(spans) != 1 {
			t.Fatalf("replace_all overlapping windows spans = %v, want exactly one", spans)
		}
		if got := content[spans[0].start:spans[0].end]; got != "a\na" {
			t.Fatalf("replace_all multi-line span = %q, want %q", got, "a\na")
		}
	})

	t.Run("multi line non-overlapping sequence", func(t *testing.T) {
		t.Parallel()
		content := "a\na\nmid\na\na"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "a\na", true, 100)
		if limitExceeded || ambigLines != nil {
			t.Fatalf("replace_all multi-line sequence = (%v, %v, %v)", spans, ambigLines, limitExceeded)
		}
		if len(spans) != 2 {
			t.Fatalf("replace_all multi-line sequence spans = %v, want two", spans)
		}
		for i, want := range []string{"a\na", "a\na"} {
			if got := content[spans[i].start:spans[i].end]; got != want {
				t.Fatalf("replace_all multi-line sequence span %d = %q, want %q", i, got, want)
			}
		}
	})
}

// TestFindFuzzyMatchSpans_MaxSpansLimit pins the span budget: a budget that is
// already used up rejects the first match, and exceeding the budget discards
// every collected span rather than returning a partial result.
func TestFindFuzzyMatchSpans_MaxSpansLimit(t *testing.T) {
	t.Parallel()

	t.Run("zero budget single match", func(t *testing.T) {
		t.Parallel()
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans("foo bar\n", "foo bar", false, 0)
		if !limitExceeded || spans != nil || ambigLines != nil {
			t.Fatalf("maxSpans=0 = (%v, %v, %v), want (nil, nil, true)", spans, ambigLines, limitExceeded)
		}
	})

	t.Run("two span budget three matches", func(t *testing.T) {
		t.Parallel()
		content := "foo bar\nmid\nfoo bar\nmid\nfoo bar\n"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "foo bar", true, 2)
		if !limitExceeded || spans != nil || ambigLines != nil {
			t.Fatalf("maxSpans=2 with three matches = (%v, %v, %v), want (nil, nil, true)", spans, ambigLines, limitExceeded)
		}
	})

	// Single-match mode never adds a second span, so a one-span budget must
	// not turn a second distinct match into an overflow: ambiguity outranks
	// the budget and reports its candidate lines instead.
	t.Run("one span budget two matches reports ambiguity", func(t *testing.T) {
		t.Parallel()
		content := "  foo bar\nmiddle\n\tfoo\tbar\n"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "foo bar", false, 1)
		if limitExceeded || spans != nil {
			t.Fatalf("maxSpans=1 with two matches = (%v, %v, %v), want ambiguity without overflow", spans, ambigLines, limitExceeded)
		}
		if want := []int{1, 3}; !slices.Equal(ambigLines, want) {
			t.Fatalf("ambiguous line numbers = %v, want %v", ambigLines, want)
		}
	})
}

func TestFindFuzzyMatchSpans_BlankLineTolerantUniqueFallback(t *testing.T) {
	t.Parallel()

	t.Run("missing blank line unique match", func(t *testing.T) {
		t.Parallel()
		content := "alpha\n}\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		old := "}\nfunction boundaryRank(x) {"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, old, false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 {
			t.Fatalf("findFuzzyMatchSpans = (%v, %v, %v), want one span", spans, ambigLines, limitExceeded)
		}
		want := "}\n\nfunction boundaryRank(x) {"
		if got := content[spans[0].start:spans[0].end]; got != want {
			t.Fatalf("matched span = %q, want %q", got, want)
		}
	})

	t.Run("extra blank line in oldString unique match", func(t *testing.T) {
		t.Parallel()
		content := "alpha\n}\nfunction boundaryRank(x) {\n  return x\n}\n"
		old := "}\n\nfunction boundaryRank(x) {"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, old, false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 {
			t.Fatalf("findFuzzyMatchSpans = (%v, %v, %v), want one span", spans, ambigLines, limitExceeded)
		}
		want := "}\nfunction boundaryRank(x) {"
		if got := content[spans[0].start:spans[0].end]; got != want {
			t.Fatalf("matched span = %q, want %q", got, want)
		}
	})

	t.Run("missing blank line combined with tabs vs spaces", func(t *testing.T) {
		t.Parallel()
		content := "pkg\n\t}\n\n\tfunction   boundaryRank(x) {\n\t\treturn x\n\t}\n"
		old := "  }\n  function boundaryRank(x) {"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, old, false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 {
			t.Fatalf("findFuzzyMatchSpans = (%v, %v, %v), want one span", spans, ambigLines, limitExceeded)
		}
		want := "\t}\n\n\tfunction   boundaryRank(x) {"
		if got := content[spans[0].start:spans[0].end]; got != want {
			t.Fatalf("matched span = %q, want %q", got, want)
		}
	})

	t.Run("ambiguous duplicate reports candidate lines", func(t *testing.T) {
		t.Parallel()
		content := "}\n\nfunction f() {\n  return 1\n}\n\nfunction f() {\n  return 2\n}\n"
		old := "}\nfunction f() {"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, old, false, 10)
		if spans != nil || limitExceeded {
			t.Fatalf("ambiguous blank-line fallback = (%v, %v, %v), want nil spans without overflow", spans, ambigLines, limitExceeded)
		}
		if want := []int{1, 5}; !slices.Equal(ambigLines, want) {
			t.Fatalf("ambiguous lines = %v, want %v", ambigLines, want)
		}
	})

	t.Run("self-overlapping blank-separated repeats are ambiguous", func(t *testing.T) {
		t.Parallel()
		content := "foo()\n\nfoo()\n\nfoo()\n"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, "foo()\nfoo()", false, 10)
		if spans != nil || limitExceeded {
			t.Fatalf("self-overlapping match = (%v, %v, %v), want ambiguity", spans, ambigLines, limitExceeded)
		}
		if want := []int{1, 3}; !slices.Equal(ambigLines, want) {
			t.Fatalf("ambiguous lines = %v, want %v", ambigLines, want)
		}
	})

	t.Run("replaceAll disables blank-line fallback", func(t *testing.T) {
		t.Parallel()
		content := "alpha\n}\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		old := "}\nfunction boundaryRank(x) {"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, old, true, 10)
		if spans != nil || ambigLines != nil || limitExceeded {
			t.Fatalf("replaceAll blank-line fallback = (%v, %v, %v), want (nil, nil, false)", spans, ambigLines, limitExceeded)
		}
	})

	t.Run("leading or trailing blank line in oldString disables fallback", func(t *testing.T) {
		t.Parallel()
		content := "alpha\n}\n\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		for _, old := range []string{
			"\n}\nfunction boundaryRank(x) {",
			"}\nfunction boundaryRank(x) {\n",
			"   \n}\nfunction boundaryRank(x) {",
			"}\nfunction boundaryRank(x) {\n\t ",
		} {
			spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, old, false, 10)
			if spans != nil || ambigLines != nil || limitExceeded {
				t.Fatalf("boundary-blank old=%q = (%v, %v, %v), want (nil, nil, false)", old, spans, ambigLines, limitExceeded)
			}
		}
	})

	t.Run("maxSpans budget respected on fallback", func(t *testing.T) {
		t.Parallel()
		content := "alpha\n}\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		old := "}\nfunction boundaryRank(x) {"
		spans, ambigLines, limitExceeded := findFuzzyMatchSpans(content, old, false, 0)
		if !limitExceeded || spans != nil || ambigLines != nil {
			t.Fatalf("maxSpans=0 fallback = (%v, %v, %v), want (nil, nil, true)", spans, ambigLines, limitExceeded)
		}

		dup := "}\n\nfunction f() {\n}\n\nfunction f() {\n"
		spans, ambigLines, limitExceeded = findFuzzyMatchSpans(dup, "}\nfunction f() {", false, 1)
		if limitExceeded || spans != nil || !slices.Equal(ambigLines, []int{1, 4}) {
			t.Fatalf("maxSpans=1 ambiguous fallback = (%v, %v, %v), want ambig [1 4]", spans, ambigLines, limitExceeded)
		}
	})
}

// TestNearestFileRegion_IncludesOneBasedLineNumber pins the near-miss
// diagnostic's line number: the snippet must say which 1-based file line the
// region starts on, not just show its text.
func TestNearestFileRegion_IncludesOneBasedLineNumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		old     string
		want    string
	}{
		{name: "second line", content: "one\ntwo\nthree\n", old: "two\nthree", want: "L2:\ntwo\nthree"},
		{name: "fourth line", content: "zero\nzero\nzero\ntarget line\n", old: "target line", want: "L4:\ntarget line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := nearestFileRegion(tc.content, tc.old); got != tc.want {
				t.Fatalf("nearestFileRegion(%q, %q) = %q, want %q", tc.content, tc.old, got, tc.want)
			}
		})
	}
}
