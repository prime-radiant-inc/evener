//go:build serffuzz

package agent

import (
	"errors"
	"testing"
)

// FuzzTranscriptRangeSpec drives the transcript read-range grammar, whose input
// is a string the MODEL writes into a tool call. It has two entry points with a
// deliberate difference — one falls back, one reports — and the pair is easy to
// let drift apart.
//
// Oracles come from the documented grammar rather than from re-deriving it:
//
//   - parseRange never fails. It is the lenient entry point, so whatever the
//     model writes it must yield usable bounds; a malformed spec resolves to
//     exactly what an empty spec resolves to, which is the documented fallback.
//   - Bounds are always inside the entry list. These indexes are used to slice
//     entries, so an unclamped value is an out-of-range panic in a tool call,
//     and clampRange is the only thing standing between the model and that.
//   - The two entry points agree whenever the strict one accepts. If they
//     diverge, the tool layer reports one range and renders another.
//   - An empty entry list is not an error; it is the empty range (0, -1).
//   - The grammar's anchors hold: "last:N" always ends at the final entry, and
//     "start:N" always begins at the first.
func FuzzTranscriptRangeSpec(f *testing.F) {
	f.Add("", 10)
	f.Add("last:3", 10)
	f.Add("start:2", 10)
	f.Add("2-5", 10)
	f.Add("5-2", 10)
	f.Add("last:0", 10)
	f.Add("last:-1", 10)
	f.Add("start:999999", 3)
	f.Add("999999-999999", 3)
	f.Add("-", 10)
	f.Add("--", 10)
	f.Add("1-", 10)
	f.Add("garbage", 10)
	f.Add("last:3", 0)
	f.Add("2-5", -1)

	f.Fuzz(func(t *testing.T, spec string, entryCount int) {
		if len(spec) > 256 {
			t.Skip()
		}
		// Keep the count in the range a transcript can actually reach; the
		// interesting behaviour is at the boundaries, not at arithmetic overflow.
		if entryCount > 1<<20 {
			entryCount %= 1 << 20
		}
		if entryCount < -16 {
			entryCount = -16
		}

		start, end := parseRange(spec, entryCount)

		if entryCount <= 0 {
			if start != 0 || end != -1 {
				t.Fatalf("parseRange(%q, %d) = (%d, %d), want the empty range (0, -1)", spec, entryCount, start, end)
			}
			return
		}

		if start < 0 || start > entryCount-1 {
			t.Fatalf("parseRange(%q, %d) start %d is outside [0, %d]", spec, entryCount, start, entryCount-1)
		}
		if end < 0 || end > entryCount-1 {
			t.Fatalf("parseRange(%q, %d) end %d is outside [0, %d]", spec, entryCount, end, entryCount-1)
		}

		strictStart, strictEnd, err := parseRangeErr(spec, entryCount)
		if err != nil {
			if !errors.Is(err, errBadRange) {
				t.Fatalf("parseRangeErr(%q, %d) failed with an unexpected error: %v", spec, entryCount, err)
			}
			// The lenient path must land exactly where an empty spec lands.
			defStart, defEnd, defErr := parseRangeErr("", entryCount)
			if defErr != nil {
				t.Fatalf("parseRangeErr(\"\", %d) errored: %v", entryCount, defErr)
			}
			if start != defStart || end != defEnd {
				t.Fatalf("malformed %q resolved to (%d, %d), want the default (%d, %d)",
					spec, start, end, defStart, defEnd)
			}
			return
		}

		if start != strictStart || end != strictEnd {
			t.Fatalf("parseRange(%q, %d) = (%d, %d) but parseRangeErr gave (%d, %d)",
				spec, entryCount, start, end, strictStart, strictEnd)
		}

		// Grammar anchors: these two forms are defined relative to an end of the
		// list, so clamping must never move them off it.
		if n, ok := parsePositiveInt(trimPrefixOrEmpty(spec, "last:")); ok && n > 0 {
			if end != entryCount-1 {
				t.Fatalf("%q ended at %d, want the final entry %d", spec, end, entryCount-1)
			}
		}
		if n, ok := parsePositiveInt(trimPrefixOrEmpty(spec, "start:")); ok && n > 0 {
			if start != 0 {
				t.Fatalf("%q started at %d, want the first entry 0", spec, start)
			}
		}
	})
}

// trimPrefixOrEmpty returns s without prefix, or "" when it does not carry it,
// so the caller's parse attempt fails rather than matching the wrong form.
func trimPrefixOrEmpty(s, prefix string) string {
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return ""
	}
	return s[len(prefix):]
}
