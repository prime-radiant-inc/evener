package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// This file fuzzes the pure rune-bounded text clamps shared by the render surface:
//
//   - truncRunes (transcript_render.go): rune-safe truncate + ellipsis.
//   - firstLineClamp (transcript_util.go): first non-empty line, whitespace
//     flattened, clamped to a rune limit.
//   - makeSnippet (session_tools_find.go): a ~width-rune excerpt centred on a
//     query match, newlines collapsed.
//   - truncateBody (transcript_render.go): head+tail line truncation with a
//     per-line width clamp for a tool-result code block.
//
// Every one of these must never exceed its declared rune cap on adversarial
// input, must stay valid UTF-8, and must be deterministic. Inputs are pure
// strings built in memory.
//
// ALLOW-LIST: the limit/width arguments are constrained to the non-negative range
// their real call sites use (constants like resultLineMaxRunes=300,
// snippetWidth=200). A negative limit would index runes[:limit] out of range, but
// no production caller ever passes one, so the fuzzer explores only the reachable
// domain.

// FuzzArTruncRunes drives truncRunes over fuzzed text and a bounded limit.
// Oracles: BOUNDED (≤ limit+1 runes, the +1 being the ellipsis), VALID UTF-8,
// PASSTHROUGH (inputs already within the limit are returned byte-identical), and
// DETERMINISM.
func FuzzArTruncRunes(f *testing.F) {
	seeds := []struct {
		s     string
		limit uint16
	}{
		{"", 10},
		{"short", 10},
		{"exactlyten", 10},
		{"this is a longer string than the limit", 10},
		{"héllo wörld 世界 🌍", 5},
		{strings.Repeat("é", 5000), 300},
		{"\x00\x01\x02control", 4},
	}
	for _, s := range seeds {
		f.Add(s.s, s.limit)
	}

	f.Fuzz(func(t *testing.T, s string, limit16 uint16) {
		limit := int(limit16)
		out := truncRunes(s, limit)

		if n := utf8.RuneCountInString(out); n > limit+1 {
			t.Fatalf("truncRunes exceeded limit+1: %d > %d (limit=%d)", n, limit+1, limit)
		}
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("truncRunes emitted invalid UTF-8 from valid input")
		}
		if utf8.RuneCountInString(s) <= limit && out != s {
			t.Fatalf("truncRunes mangled an in-limit string: %q → %q", s, out)
		}
		if out2 := truncRunes(s, limit); out != out2 {
			t.Fatalf("truncRunes non-deterministic")
		}
	})
}

// FuzzArFirstLineClamp drives firstLineClamp over fuzzed multi-line text.
// Oracles: BOUNDED (≤ limit+1 runes), SINGLE LINE (no embedded newline — a
// clamped title can never span rows), NO SURROUNDING WHITESPACE (fields are
// flattened), VALID UTF-8, and DETERMINISM.
func FuzzArFirstLineClamp(f *testing.F) {
	seeds := []struct {
		s     string
		limit uint16
	}{
		{"", 20},
		{"one line", 20},
		{"   \n\n  first real line  \nsecond", 20},
		{"a very long single line that must be clamped down to size", 10},
		{"\t\t\n   spaced    out    words   \n", 100},
		{strings.Repeat("世界 ", 500), 50},
		{"line with\ttabs and    runs", 120},
	}
	for _, s := range seeds {
		f.Add(s.s, s.limit)
	}

	f.Fuzz(func(t *testing.T, s string, limit16 uint16) {
		limit := int(limit16)
		out := firstLineClamp(s, limit)

		if strings.ContainsAny(out, "\n\r") {
			t.Fatalf("firstLineClamp produced a multi-line result: %q", out)
		}
		if n := utf8.RuneCountInString(out); n > limit+1 {
			t.Fatalf("firstLineClamp exceeded limit+1: %d > %d", n, limit+1)
		}
		if out != strings.TrimSpace(out) {
			t.Fatalf("firstLineClamp left surrounding whitespace: %q", out)
		}
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("firstLineClamp emitted invalid UTF-8 from valid input")
		}
		if out2 := firstLineClamp(s, limit); out != out2 {
			t.Fatalf("firstLineClamp non-deterministic")
		}
	})
}

// FuzzArMakeSnippet drives makeSnippet over fuzzed text and query. Oracles:
// BOUNDED (≤ width+2 runes — the excerpt plus at most two ellipses), SINGLE LINE
// (Fields collapse every newline to a space), VALID UTF-8, and DETERMINISM.
func FuzzArMakeSnippet(f *testing.F) {
	seeds := []struct {
		text, query string
		width       uint16
	}{
		{"the quick brown fox jumps over the lazy dog", "brown", 20},
		{"no match here at all", "zzz", 20},
		{"", "x", 10},
		{"match\nacross\nlines\nquery\nhere", "query", 15},
		{strings.Repeat("word ", 5000) + "needle" + strings.Repeat(" word", 5000), "needle", 200},
		{"UNICODE 世界 needle 🌍 tail", "needle", 12},
		{"edge", "", 5},
	}
	for _, s := range seeds {
		f.Add(s.text, s.query, s.width)
	}

	f.Fuzz(func(t *testing.T, text, query string, width16 uint16) {
		width := int(width16)%10000 + 1 // real call site uses snippetWidth=200; keep positive
		out := makeSnippet(text, query, width)

		if strings.ContainsAny(out, "\n\r") {
			t.Fatalf("makeSnippet produced a multi-line result: %q", out)
		}
		if n := utf8.RuneCountInString(out); n > width+2 {
			t.Fatalf("makeSnippet exceeded width+2: %d > %d (width=%d)", n, width+2, width)
		}
		if utf8.ValidString(text) && utf8.ValidString(query) && !utf8.ValidString(out) {
			t.Fatalf("makeSnippet emitted invalid UTF-8 from valid input")
		}
		if out2 := makeSnippet(text, query, width); out != out2 {
			t.Fatalf("makeSnippet non-deterministic")
		}
	})
}

// FuzzArTruncateBody drives truncateBody (the tool-result code-block clamp) over
// fuzzed body text in the non-full (condensed) mode. Oracles:
//
//   - PER-LINE WIDTH BOUND: every emitted line is at most resultLineMaxRunes plus
//     a small indent/ellipsis allowance — one pathological line cannot dominate a
//     card.
//   - ELISION MARKER: when the body has more than resultBodyWholeMax non-empty
//     lines, the "lines elided" marker is present (evidence was condensed).
//   - VALID UTF-8 and DETERMINISM.
//
// The full=true path is also driven for the never-panic floor (verbatim, no clamp).
func FuzzArTruncateBody(f *testing.F) {
	seeds := []string{
		"",
		"single line",
		"a\nb\nc\n",
		strings.Repeat("line\n", resultBodyWholeMax+50),
		strings.Repeat("x", resultLineMaxRunes+200) + "\nshort",
		"  \n\n\t\n   \n", // all-blank
		strings.Repeat("世界é🌍 ", resultLineMaxRunes) + "\n" + strings.Repeat("row\n", 40),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// Small indent (2 spaces) + ellipsis rune + a little slack.
	const perLineAllowance = 5

	f.Fuzz(func(t *testing.T, body string) {
		// full=true: never-panic floor only.
		_ = truncateBody(body, true)

		out := truncateBody(body, false)

		for _, line := range strings.Split(out, "\n") {
			if n := utf8.RuneCountInString(line); n > resultLineMaxRunes+perLineAllowance {
				t.Fatalf("truncateBody line exceeds clamp: %d runes > %d\n  line=%q", n, resultLineMaxRunes+perLineAllowance, line)
			}
		}

		if len(nonEmptyLines(body)) > resultBodyWholeMax && !strings.Contains(out, "lines elided") {
			t.Fatalf("truncateBody condensed a >%d-line body but has no elision marker:\n%s", resultBodyWholeMax, out)
		}

		if utf8.ValidString(body) && !utf8.ValidString(out) {
			t.Fatalf("truncateBody emitted invalid UTF-8 from valid input")
		}
		if out2 := truncateBody(body, false); out != out2 {
			t.Fatalf("truncateBody non-deterministic")
		}
	})
}
