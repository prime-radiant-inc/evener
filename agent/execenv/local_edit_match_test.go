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
	spans, _, _, _ := findFuzzyMatchSpans(content, oldString, true, len(content)+1)
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
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, "foo bar", false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 || blankTolerant {
			t.Fatalf("findFuzzyMatchSpans single-line = (%v, %v, %v, %v), want one non-blank-tolerant span", spans, ambigLines, limitExceeded, blankTolerant)
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
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, "foo bar\nbaz qux", false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 || blankTolerant {
			t.Fatalf("findFuzzyMatchSpans multi-line = (%v, %v, %v, %v), want one non-blank-tolerant span", spans, ambigLines, limitExceeded, blankTolerant)
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
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, "\t  ", replaceAll, 10)
		if spans != nil || ambigLines != nil || limitExceeded || blankTolerant {
			t.Fatalf("findFuzzyMatchSpans(whitespace-only, replaceAll=%v) = (%v, %v, %v, %v), want (nil, nil, false, false)",
				replaceAll, spans, ambigLines, limitExceeded, blankTolerant)
		}
	}
}

// TestFindFuzzyMatchSpans_AmbiguousLineNumbers pins the single-match contract:
// two or more distinct whitespace variants are ambiguous, and the candidates
// name their 1-based file lines without rescanning.
func TestFindFuzzyMatchSpans_AmbiguousLineNumbers(t *testing.T) {
	t.Parallel()
	content := "  foo bar\nmiddle\n\tfoo\tbar\nother\nfoo   bar\n"
	spans, ambigLines, limitExceeded, _ := findFuzzyMatchSpans(content, "foo bar", false, 10)
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
	spans, ambigLines, limitExceeded, _ = findFuzzyMatchSpans(many.String(), "foo bar", false, 100)
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
		spans, ambigLines, limitExceeded, _ := findFuzzyMatchSpans(content, "foo bar", true, 100)
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
		spans, ambigLines, limitExceeded, _ := findFuzzyMatchSpans(content, "a\na", true, 100)
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
		spans, ambigLines, limitExceeded, _ := findFuzzyMatchSpans(content, "a\na", true, 100)
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
		spans, ambigLines, limitExceeded, _ := findFuzzyMatchSpans("foo bar\n", "foo bar", false, 0)
		if !limitExceeded || spans != nil || ambigLines != nil {
			t.Fatalf("maxSpans=0 = (%v, %v, %v), want (nil, nil, true)", spans, ambigLines, limitExceeded)
		}
	})

	t.Run("two span budget three matches", func(t *testing.T) {
		t.Parallel()
		content := "foo bar\nmid\nfoo bar\nmid\nfoo bar\n"
		spans, ambigLines, limitExceeded, _ := findFuzzyMatchSpans(content, "foo bar", true, 2)
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
		spans, ambigLines, limitExceeded, _ := findFuzzyMatchSpans(content, "foo bar", false, 1)
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
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, old, false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 || !blankTolerant {
			t.Fatalf("findFuzzyMatchSpans = (%v, %v, %v, %v), want one blank-tolerant span", spans, ambigLines, limitExceeded, blankTolerant)
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
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, old, false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 || !blankTolerant {
			t.Fatalf("findFuzzyMatchSpans = (%v, %v, %v, %v), want one blank-tolerant span", spans, ambigLines, limitExceeded, blankTolerant)
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
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, old, false, 10)
		if limitExceeded || ambigLines != nil || len(spans) != 1 || !blankTolerant {
			t.Fatalf("findFuzzyMatchSpans = (%v, %v, %v, %v), want one blank-tolerant span", spans, ambigLines, limitExceeded, blankTolerant)
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
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, old, false, 10)
		if spans != nil || limitExceeded || blankTolerant {
			t.Fatalf("ambiguous blank-line fallback = (%v, %v, %v, %v), want nil spans without overflow", spans, ambigLines, limitExceeded, blankTolerant)
		}
		if want := []int{1, 5}; !slices.Equal(ambigLines, want) {
			t.Fatalf("ambiguous lines = %v, want %v", ambigLines, want)
		}
	})

	t.Run("self-overlapping blank-separated repeats are ambiguous", func(t *testing.T) {
		t.Parallel()
		content := "foo()\n\nfoo()\n\nfoo()\n"
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, "foo()\nfoo()", false, 10)
		if spans != nil || limitExceeded || blankTolerant {
			t.Fatalf("self-overlapping match = (%v, %v, %v, %v), want ambiguity", spans, ambigLines, limitExceeded, blankTolerant)
		}
		if want := []int{1, 3}; !slices.Equal(ambigLines, want) {
			t.Fatalf("ambiguous lines = %v, want %v", ambigLines, want)
		}
	})

	t.Run("replaceAll disables blank-line fallback", func(t *testing.T) {
		t.Parallel()
		content := "alpha\n}\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		old := "}\nfunction boundaryRank(x) {"
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, old, true, 10)
		if spans != nil || ambigLines != nil || limitExceeded || blankTolerant {
			t.Fatalf("replaceAll blank-line fallback = (%v, %v, %v, %v), want (nil, nil, false, false)", spans, ambigLines, limitExceeded, blankTolerant)
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
			spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, old, false, 10)
			if spans != nil || ambigLines != nil || limitExceeded || blankTolerant {
				t.Fatalf("boundary-blank old=%q = (%v, %v, %v, %v), want (nil, nil, false, false)", old, spans, ambigLines, limitExceeded, blankTolerant)
			}
		}
	})

	t.Run("maxSpans budget respected on fallback", func(t *testing.T) {
		t.Parallel()
		content := "alpha\n}\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		old := "}\nfunction boundaryRank(x) {"
		spans, ambigLines, limitExceeded, blankTolerant := findFuzzyMatchSpans(content, old, false, 0)
		if !limitExceeded || spans != nil || ambigLines != nil || blankTolerant {
			t.Fatalf("maxSpans=0 fallback = (%v, %v, %v, %v), want (nil, nil, true, false)", spans, ambigLines, limitExceeded, blankTolerant)
		}

		dup := "}\n\nfunction f() {\n}\n\nfunction f() {\n"
		spans, ambigLines, limitExceeded, blankTolerant = findFuzzyMatchSpans(dup, "}\nfunction f() {", false, 1)
		if limitExceeded || spans != nil || blankTolerant || !slices.Equal(ambigLines, []int{1, 4}) {
			t.Fatalf("maxSpans=1 ambiguous fallback = (%v, %v, %v, %v), want ambig [1 4]", spans, ambigLines, limitExceeded, blankTolerant)
		}
	})
}

func TestReconcileBlankTolerantReplacement(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		matched string
		old     string
		new     string
		want    string
	}{
		{
			name:    "entire block changed returns newString verbatim",
			matched: "a\n\nb",
			old:     "a\nb",
			new:     "x\ny",
			want:    "x\ny",
		},
		{
			name:    "identical old and new returns matched verbatim",
			matched: "a\n\nb",
			old:     "a\nb",
			new:     "a\nb",
			want:    "a\n\nb",
		},
		{
			name:    "explicit blank count expansion rolls back shared prefix blank",
			matched: "Overview\n\n\nVersion 1",
			old:     "Overview\n\nVersion 1",
			new:     "Overview\n\n\n\nVersion 2",
			want:    "Overview\n\n\n\nVersion 2",
		},
		{
			name:    "explicit blank count expansion rolls back shared suffix blank",
			matched: "Version 1\n\n\nFooter",
			old:     "Version 1\n\nFooter",
			new:     "Version 2\n\n\n\nFooter",
			want:    "Version 2\n\n\n\nFooter",
		},
		{
			name:    "insertion after shared blank keeps file gap before insertion",
			matched: "a\n\n\n\nb",
			old:     "a\n\nb",
			new:     "a\n\nmid\nb",
			want:    "a\n\n\n\nmid\nb",
		},
		{
			name:    "insertion before shared blank keeps file gap after insertion",
			matched: "a\n\n\n\nb",
			old:     "a\n\nb",
			new:     "a\nmid\n\nb",
			want:    "a\nmid\n\n\n\nb",
		},
		{
			name:    "pure deletion of trailing line leaves no orphan blank",
			matched: "a\n\ndrop",
			old:     "a\ndrop",
			new:     "a",
			want:    "a",
		},
		{
			name:    "pure deletion of leading line leaves no orphan blank",
			matched: "drop\n\nb",
			old:     "drop\nb",
			new:     "b",
			want:    "b",
		},
		{
			name:    "pure deletion between prefix and suffix falls back to postGap",
			matched: "a\ndrop\n\nb",
			old:     "a\ndrop\nb",
			new:     "a\nb",
			want:    "a\n\nb",
		},
		{
			name:    "changed first and last lines preserve interior paragraph breaks",
			matched: "Status 1\n\nP1\n\nP2\n\nOwner 1",
			old:     "Status 1\nP1\nP2\nOwner 1",
			new:     "Status 2\nP1\nP2\nOwner 2",
			want:    "Status 2\n\nP1\n\nP2\n\nOwner 2",
		},
		{
			name:    "explicit boundary blank reduction to zero is honored",
			matched: "Heading\n\nOld text\n\nFooter",
			old:     "Heading\n\nOld text\nFooter",
			new:     "Heading\nNew text\nFooter",
			want:    "Heading\nNew text\n\nFooter",
		},
		{
			name:    "explicit blank removal between unchanged lines is honored",
			matched: "Heading\n\nBody\n\nFooter",
			old:     "Heading\n\nBody\nFooter",
			new:     "Heading\nBody\nFooter",
			want:    "Heading\nBody\n\nFooter",
		},
		{
			name:    "explicit blank removal on pure deletion is honored",
			matched: "a\n\ndrop\nb\n\nc",
			old:     "a\n\ndrop\nb\nc",
			new:     "a\nb\nc",
			want:    "a\nb\n\nc",
		},
		{
			name:    "explicit blank removal on pure insertion is honored",
			matched: "a\n\nb\n\nc",
			old:     "a\n\nb\nc",
			new:     "a\nmid\nb\nc",
			want:    "a\nmid\nb\n\nc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := reconcileBlankTolerantReplacement(tc.matched, tc.old, tc.new)
			if !ok || got != tc.want {
				t.Fatalf("reconcileBlankTolerantReplacement(%q, %q, %q) = (%q, %v), want (%q, true)", tc.matched, tc.old, tc.new, got, ok, tc.want)
			}
		})
	}

	t.Run("oversized unaligned interior returns ok=false while localized large edit returns ok=true", func(t *testing.T) {
		t.Parallel()
		var oldBody, newBody []string
		for i := range 130 {
			oldBody = append(oldBody, "old"+strings.Repeat("x", i%5+1))
			newBody = append(newBody, "new"+strings.Repeat("y", i%5+1))
		}
		matched := "Head1\n\nHead2\n\n" + strings.Join(oldBody, "\n") + "\n\nTail1\n\nTail2"
		oldStr := "Head1\nHead2\n" + strings.Join(oldBody, "\n") + "\nTail1\nTail2"
		newStr := "Head1\nHead2\n" + strings.Join(newBody, "\n") + "\nTail1\nTail2"
		if got, ok := reconcileBlankTolerantReplacement(matched, oldStr, newStr); ok || got != "" {
			t.Fatalf("oversized interior = (%q, %v), want (\"\", false)", got, ok)
		}

		localBody := append([]string(nil), oldBody...)
		localBody[65] = "changed middle line"
		localNew := "Head1\nHead2\n" + strings.Join(localBody, "\n") + "\nTail1\nTail2"
		want := "Head1\n\nHead2\n\n" + strings.Join(localBody, "\n") + "\n\nTail1\n\nTail2"
		got, ok := reconcileBlankTolerantReplacement(matched, oldStr, localNew)
		if !ok || got != want {
			t.Fatalf("localized large-block reconciliation = (%q, %v), want (%q, true)", got, ok, want)
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
