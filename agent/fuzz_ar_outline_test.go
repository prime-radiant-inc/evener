package agent

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/serf/llm"
)

// This file fuzzes the outline text-transform surface in session_outline.go:
//
//   - boundOutline: joins outline lines into a single block bounded by
//     convBudgetChars, splicing an "… N turns elided …" marker when it drops the
//     middle. It is the flagship bound oracle here — feed pathological lines and
//     the rendered block must never exceed the budget.
//   - resultSizeNote / anyLineWiderThan: the per-round result-size summary that a
//     truncated/width-clamped result must flag.
//
// These are pure data→bounded-text functions: no disk, no network, no clock. The
// inputs are built entirely in memory.

// FuzzArBoundOutline drives boundOutline over a fuzzed set of outline lines.
// Oracles beyond never-panic:
//
//   - BOUNDED: the rendered content is never longer than convBudgetChars runes.
//     This is the load-bearing property — a truncation helper that blows its own
//     declared cap on adversarial input is a real bug.
//   - ELISION HONESTY: truncated ⇔ elidedTurns > 0, and whenever truncated the
//     "… N turns elided …" marker with the reported N is actually present.
//   - LINE FIDELITY: every non-marker line of the output is one of the input
//     lines (boundOutline may drop lines but must never synthesize or mangle a
//     surviving one), and the surviving lines are a head+tail slice in order.
//   - DETERMINISM: the same lines render identically twice.
//   - VALID UTF-8: valid-UTF-8 line inputs yield valid-UTF-8 output.
func FuzzArBoundOutline(f *testing.F) {
	seeds := []string{
		"",
		"0 · User · hi",
		"0 · User · hi\n1 · Assistant · shell · ok · 3 lines",
		strings.Repeat("x", 100),
		// A wall far over the budget so the elision path is exercised.
		strings.Repeat("7 · Assistant · a very long outline line indeed\n", 2000),
		// One single line that is itself over budget.
		strings.Repeat("y", convBudgetChars+5000),
		"a\n\n\nb\n\n",
		"héllo · ünïcode · 世界\nмир · 🌍",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, joined string) {
		// Split the fuzz input into outline lines. boundOutline is documented to
		// operate on a []string of already-rendered lines.
		lines := strings.Split(joined, "\n")

		content, truncated, elided := boundOutline(lines)

		// BOUNDED: never exceed the declared budget.
		if got := utf8.RuneCountInString(content); got > convBudgetChars {
			t.Fatalf("boundOutline exceeded convBudgetChars: %d > %d\n#lines=%d", got, convBudgetChars, len(lines))
		}

		// ELISION HONESTY.
		if truncated != (elided > 0) {
			t.Fatalf("boundOutline truncated=%v but elided=%d (must agree)", truncated, elided)
		}
		if truncated {
			marker := markerFor(elided)
			if !strings.Contains(content, marker) {
				t.Fatalf("boundOutline truncated but marker %q absent from output:\n%s", marker, content)
			}
			assertHeadTailSlice(t, lines, content, elided)
		} else if content != strings.Join(lines, "\n") {
			// Not truncated: the output is exactly the joined non-elided lines.
			t.Fatalf("boundOutline not truncated but content != join(lines)")
		}

		// VALID UTF-8 preservation: a valid-UTF-8 input must not become invalid.
		if utf8.ValidString(joined) && !utf8.ValidString(content) {
			t.Fatalf("boundOutline emitted invalid UTF-8 for valid input")
		}

		// DETERMINISM.
		content2, truncated2, elided2 := boundOutline(strings.Split(joined, "\n"))
		if content != content2 || truncated != truncated2 || elided != elided2 {
			t.Fatalf("boundOutline non-deterministic:\n a=(%q,%v,%d)\n b=(%q,%v,%d)", content, truncated, elided, content2, truncated2, elided2)
		}
	})
}

// markerFor is the exact elision marker boundOutline splices in for n dropped
// lines. Kept in lockstep with boundOutline's Fprintf format.
func markerFor(n int) string {
	return fmt.Sprintf("… %d turns elided — read a range to see them …", n)
}

// assertHeadTailSlice verifies that the surviving lines of a truncated outline
// are a contiguous prefix of the input (head) followed by a contiguous suffix
// (tail). boundOutline writes each head line with a trailing newline, then the
// marker followed by a newline, then the tail lines joined by newlines with no
// trailing newline — so splitting on the exact "marker\n" separator recovers head
// and tail unambiguously. These prefix/suffix relations catch any synthesis,
// reordering, or mangling of a surviving line; they never over-constrain (an
// empty line rendered as an empty tail is still a valid suffix).
func assertHeadTailSlice(t *testing.T, lines []string, content string, elided int) {
	t.Helper()
	before, after, ok := strings.Cut(content, markerFor(elided)+"\n")
	if !ok {
		return // marker presence already asserted by the caller
	}
	head := headLinesOf(before)
	tail := tailLinesOf(after)
	for i, h := range head {
		if i >= len(lines) || h != lines[i] {
			t.Fatalf("head line %d is not a prefix of the input: got %q", i, h)
		}
	}
	for i, tl := range tail {
		want := len(lines) - len(tail) + i
		if want < 0 || want >= len(lines) || tl != lines[want] {
			t.Fatalf("tail line %d is not a suffix of the input: got %q", i, tl)
		}
	}
}

// headLinesOf recovers the head lines from the "before marker" segment, where
// each head line carries a trailing newline (so the segment ends with one).
func headLinesOf(before string) []string {
	if before == "" {
		return nil
	}
	parts := strings.Split(before, "\n")
	return parts[:len(parts)-1] // drop the trailing empty from the final newline
}

// tailLinesOf recovers the tail lines from the "after marker" segment, which is
// the tail joined by newlines with no trailing newline (empty ⇒ no tail).
func tailLinesOf(after string) []string {
	if after == "" {
		return nil
	}
	return strings.Split(after, "\n")
}

// FuzzArResultSizeNote drives resultSizeNote (and, through it, anyLineWiderThan)
// over fuzzed tool-result bodies. Oracles:
//
//   - never panics on arbitrary result content;
//   - DETERMINISM: the note is a pure function of the paired results;
//   - TRUNCATION-FLAG HONESTY: the "[truncated]" suffix appears exactly when some
//     result body has more than resultBodyWholeMax non-empty lines OR a line wider
//     than resultLineMaxRunes runes — the same predicate the markdown renderer
//     uses, recomputed independently here.
func FuzzArResultSizeNote(f *testing.F) {
	seeds := []string{
		"",
		"one line",
		"a\nb\nc",
		strings.Repeat("line\n", resultBodyWholeMax+3),
		strings.Repeat("x", resultLineMaxRunes+5),
		"short\n" + strings.Repeat("w", resultLineMaxRunes+1) + "\nshort",
		"  \n\t\n   \n", // all-blank lines
		"héllo\n世界\n" + strings.Repeat("é", resultLineMaxRunes+2),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, body string) {
		call := &llm.ToolCallData{ID: "call-1", Name: "shell"}
		idx := &resultIndex{
			byCallID: map[string]pairedResult{
				"call-1": {result: &llm.ToolResultData{ToolCallID: "call-1", Content: body}},
			},
		}
		calls := []*llm.ToolCallData{call}

		note := resultSizeNote(calls, idx)

		// DETERMINISM.
		if note2 := resultSizeNote(calls, idx); note != note2 {
			t.Fatalf("resultSizeNote non-deterministic: %q vs %q", note, note2)
		}

		// TRUNCATION-FLAG HONESTY — recompute the predicate independently.
		n := len(nonEmptyLines(body))
		wantTrunc := n > resultBodyWholeMax || anyLineWiderThan(body, resultLineMaxRunes)
		gotTrunc := strings.HasSuffix(note, "[truncated]")
		if gotTrunc != wantTrunc {
			t.Fatalf("resultSizeNote truncation flag = %v, want %v (n=%d, note=%q)", gotTrunc, wantTrunc, n, note)
		}
		// The note always names the line count when a result is paired.
		if !strings.HasPrefix(note, strconv.Itoa(n)+" lines") {
			t.Fatalf("resultSizeNote = %q, expected to start with %q", note, strconv.Itoa(n)+" lines")
		}
	})
}
