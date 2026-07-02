package agent

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// This file fuzzes the head+tail output-digest renderers in job_output_digest.go:
//
//   - shellInlineDigest: compact ~1 KiB digest of a completed command's full
//     output. The flagship bound oracle — feed a multi-megabyte output and the
//     inline digest must stay small (each end capped at shellDigestHalfBytes).
//   - assembleOutputDigest: stitches head + elision-marker + tail with an honest
//     byte-elision count.
//   - humanBytes: the byte-count formatter the marker uses.
//
// All are pure data→bounded-text: the "full" output is an in-memory string.

// digestMarkerBudget bounds the fixed marker/formatting overhead
// shellInlineDigest adds around the two ~shellDigestHalfBytes slices: the elision
// sentence plus two humanBytes renderings and a stray newline. 512 is a generous
// ceiling on that overhead.
const digestMarkerBudget = 512

// FuzzArShellInlineDigest drives shellInlineDigest over fuzzed full output and
// fuzzed total/dropped counters. Oracles beyond never-panic:
//
//   - BOUNDED: the digest is never longer than 2*shellDigestHalfBytes plus the
//     fixed marker overhead, regardless of how large the input is. This is the
//     load-bearing property for a truncation helper.
//   - ELISION MARKER PRESENT: when the input exceeds what the two ends can hold,
//     the digest carries the "elided" marker (evidence was dropped, so the reader
//     must be told).
//   - VALID UTF-8: a valid-UTF-8 input yields valid-UTF-8 output.
//   - DETERMINISM.
func FuzzArShellInlineDigest(f *testing.F) {
	type seed struct {
		full           string
		total, dropped int64
	}
	seeds := []seed{
		{"", 0, 0},
		{"one line no newline", 19, 0},
		{"a\nb\nc\n", 6, 0},
		{strings.Repeat("x\n", 1000), 2000, 0},
		{strings.Repeat("y", 100000), 100000, 5000},
		// A long output with no newlines at all (partial-line trim never fires).
		{strings.Repeat("z", 4096), 4096, 0},
		{"héllo\n世界\n" + strings.Repeat("é", 5000), 999999, 12345},
	}
	for _, s := range seeds {
		f.Add(s.full, s.total, s.dropped)
	}

	f.Fuzz(func(t *testing.T, full string, total, dropped int64) {
		out := shellInlineDigest(full, total, dropped)

		// BOUNDED — independent of input size.
		limit := 2*shellDigestHalfBytes + digestMarkerBudget
		if len(out) > limit {
			t.Fatalf("shellInlineDigest exceeded bound: %d bytes > %d (input %d bytes)", len(out), limit, len(full))
		}

		// ELISION MARKER: when the digest is strictly smaller than the source, the
		// middle was dropped and must be announced.
		if len(out) < len(full) && !strings.Contains(out, "elided") {
			t.Fatalf("shellInlineDigest dropped bytes (%d→%d) but has no elision marker:\n%s", len(full), len(out), out)
		}

		// VALID UTF-8 preservation.
		if utf8.ValidString(full) && !utf8.ValidString(out) {
			t.Fatalf("shellInlineDigest produced invalid UTF-8 from valid input")
		}

		// DETERMINISM.
		if out2 := shellInlineDigest(full, total, dropped); out != out2 {
			t.Fatalf("shellInlineDigest non-deterministic")
		}
	})
}

// FuzzArAssembleOutputDigest drives assembleOutputDigest over fuzzed head/tail
// byte slices and counters. Oracles beyond never-panic:
//
//   - STRUCTURE: the output contains the head bytes, the tail bytes, and exactly
//     one elision marker between them.
//   - NON-NEGATIVE ELISION: the marker never reports a negative byte count — the
//     elided figure is clamped at zero even when head+tail exceed total.
//   - DETERMINISM.
func FuzzArAssembleOutputDigest(f *testing.F) {
	type seed struct {
		head, tail     string
		total, dropped int64
	}
	seeds := []seed{
		{"", "", 0, 0},
		{"head\n", "tail\n", 100, 0},
		{"head no newline", "tail\n", 50, 10},
		{"h", "t", 1 << 40, 1 << 20},
		{"héad\n", "täil\n", -5, -5}, // negative counters must not panic
		{strings.Repeat("a", 600), strings.Repeat("b", 600), 2000, 500},
	}
	for _, s := range seeds {
		f.Add(s.head, s.tail, s.total, s.dropped)
	}

	f.Fuzz(func(t *testing.T, head, tail string, total, dropped int64) {
		out := assembleOutputDigest([]byte(head), []byte(tail), total, dropped)

		if !strings.Contains(out, "elided") {
			t.Fatalf("assembleOutputDigest has no elision marker:\n%s", out)
		}
		if !strings.Contains(out, head) {
			t.Fatalf("assembleOutputDigest dropped the head bytes")
		}
		if !strings.Contains(out, tail) {
			t.Fatalf("assembleOutputDigest dropped the tail bytes")
		}
		// The marker must never advertise a negative elided count. humanBytes of a
		// negative number would begin with '-'; the clamp forbids it.
		if strings.Contains(out, "-") && !strings.Contains(head, "-") && !strings.Contains(tail, "-") {
			t.Fatalf("assembleOutputDigest reported a negative byte count:\n%s", out)
		}

		if out2 := assembleOutputDigest([]byte(head), []byte(tail), total, dropped); out != out2 {
			t.Fatalf("assembleOutputDigest non-deterministic")
		}
	})
}

// FuzzArHumanBytes drives humanBytes over arbitrary counts. Oracles:
//
//   - never panics (including at the int64 extremes);
//   - DETERMINISM;
//   - SUB-KIB EXACTNESS: any value below 1024 renders as "<n> B" exactly, so the
//     small-count path stays lossless.
func FuzzArHumanBytes(f *testing.F) {
	for _, n := range []int64{0, 1, 1023, 1024, 1025, 1 << 20, 1 << 40, 9223372036854775807, -1} {
		f.Add(n)
	}
	f.Fuzz(func(t *testing.T, n int64) {
		s := humanBytes(n)
		if s == "" {
			t.Fatalf("humanBytes(%d) returned empty", n)
		}
		if s2 := humanBytes(n); s != s2 {
			t.Fatalf("humanBytes non-deterministic for %d", n)
		}
		if n >= 0 && n < 1024 {
			if s != strconv.FormatInt(n, 10)+" B" {
				t.Fatalf("humanBytes(%d) = %q, want exact %q", n, s, strconv.FormatInt(n, 10)+" B")
			}
		}
	})
}
