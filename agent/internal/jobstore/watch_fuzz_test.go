package jobstore

import (
	"bytes"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// FuzzOutputMatcher drives OutputMatcher — the streaming byte-window regexp
// matcher that watches a job's output. Input is an arbitrary regexp pattern plus
// an arbitrary output blob. The blob is fed as a single Feed() chunk, so the scan
// window is exactly the blob and the result is fully determined by it.
//
// A second leg feeds the SAME blob as TWO chunks split at a fuzz-chosen offset.
// Without it the window is always exactly the blob and windowStart is always 0,
// so the carry, the seam, and the reported-range dedup this task introduced sit
// entirely outside the differential. The chunked leg asserts the two properties
// that mechanism owes: reported extents never overlap (the same occurrence is
// never delivered twice, however the window slid onto it), and no occurrence is
// lost (every match the whole-blob scan finds overlaps something the chunked
// feed reported).
//
// Oracle: a hand-written reference windowed scanner that mirrors the matcher's
// documented policy (run the pattern over the window with CRLF rewritten so $
// anchors at a CRLF line end; skip any match longer than outputMatchWindowBytes;
// report the line the match begins on, capped at outputMatchWindowBytes and
// stripped of one trailing '\r'). The fuzzer asserts:
//   - Feed() returns EXACTLY the reference match list (a real
//     reference-implementation differential, not a no-panic floor).
//   - ScanRetained returns the LAST reference match and a matched flag that agree
//     with the reference, cross-checking the independent scan-path entry point.
//   - Every returned excerpt is within the window bound and contains no newline.
//   - The matcher is deterministic across two fresh runs.
func FuzzOutputMatcher(f *testing.F) {
	f.Add(`error`, "ok\nerror here\nfine\n", 3)
	f.Add(`^WARN`, "WARN: a\r\nnot a warn\nWARNING xyz", 9)
	f.Add(`.*`, "", 0)
	f.Add(`x`, "no newline tail x", 8)
	f.Add(`a`, "a\n\na\r\n", 2)
	f.Add(``, "anything\n", 4)
	f.Add(`(?s).`, "\n\n\n", 1)
	f.Add(`[`, "broken pattern\n", 7)
	f.Add(`\d+`, "exit_code=42\ncode\n", 5)
	f.Add(`x+READY`, strings.Repeat("x", 40)+"READY"+strings.Repeat("x", 40), 60)
	f.Add(`READY.*`, strings.Repeat("x", 40)+"READY"+strings.Repeat("x", 40), 50)

	f.Fuzz(func(t *testing.T, pattern, blob string, split int) {
		if len(pattern) > 200 {
			return // keep regexp compilation off the pathological end
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return // not a valid pattern: the matcher is never built from one
		}

		data := []byte(blob)
		want := referenceMatches(re, data)

		m := NewOutputMatcher(re)
		got := m.Feed(data)

		// Normalize nil vs empty so DeepEqual compares contents, not nilness.
		if len(got) == 0 {
			got = nil
		}
		if len(want) == 0 {
			want = nil
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Feed mismatch\n pattern=%q\n blob=%q\n got =%#v\n want=%#v", pattern, blob, got, want)
		}

		// Every reported excerpt stays inside the stated bound and on one line.
		for _, excerpt := range got {
			if len(excerpt) > outputMatchWindowBytes {
				t.Fatalf("excerpt %d bytes exceeds the window bound %d", len(excerpt), outputMatchWindowBytes)
			}
			if bytes.IndexByte([]byte(excerpt), '\n') >= 0 {
				t.Fatalf("reported excerpt contains a newline: %q", excerpt)
			}
		}

		// ScanRetained: independent entry point, cross-checked against the same
		// reference list (it reports only the LAST match).
		last, matched := NewOutputMatcher(re).ScanRetained(data)
		wantMatched := len(want) > 0
		if matched != wantMatched {
			t.Fatalf("ScanRetained matched=%v want=%v (pattern=%q blob=%q)", matched, wantMatched, pattern, blob)
		}
		if wantMatched && last != want[len(want)-1] {
			t.Fatalf("ScanRetained last=%q want=%q", last, want[len(want)-1])
		}

		// Determinism: a fresh matcher fed identically yields the same result.
		got2 := NewOutputMatcher(re).Feed(data)
		if len(got2) == 0 {
			got2 = nil
		}
		if !reflect.DeepEqual(got, got2) {
			t.Fatalf("matcher not deterministic for pattern=%q blob=%q", pattern, blob)
		}

		// Chunked leg. Keep the blob inside one window so the carry holds the whole
		// first chunk and the final window is the whole blob — that is what makes
		// the no-loss property below exact.
		if len(data) > outputMatchWindowBytes {
			return
		}
		at := ((split % (len(data) + 1)) + len(data) + 1) % (len(data) + 1)
		chunked := feedInTwoChunks(re, data, at)

		// No occurrence is delivered twice: reported extents never overlap.
		sorted := append([]OutputMatch(nil), chunked...)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].Start != sorted[j].Start {
				return sorted[i].Start < sorted[j].Start
			}
			return sorted[i].End < sorted[j].End
		})
		for i := 1; i < len(sorted); i++ {
			if sorted[i].Start < sorted[i-1].End {
				t.Fatalf("chunked feed reported overlapping extents %+v and %+v (pattern=%q blob=%q split=%d)",
					sorted[i-1], sorted[i], pattern, blob, at)
			}
		}

		// No occurrence is lost: everything the whole-blob scan finds is covered.
		for _, whole := range NewOutputMatcher(re).FeedAtWithProvenance(data, int64(len(data)), nil) {
			covered := false
			for _, part := range chunked {
				if whole.Start < part.End && part.Start < whole.End || whole.Start == part.Start && whole.End == part.End {
					covered = true
					break
				}
			}
			if !covered {
				t.Fatalf("chunked feed lost occurrence %+v (pattern=%q blob=%q split=%d) reported=%+v",
					whole, pattern, blob, at, chunked)
			}
		}

		// The chunked feed is deterministic too.
		if again := feedInTwoChunks(re, data, at); !reflect.DeepEqual(chunked, again) {
			t.Fatalf("chunked feed not deterministic for pattern=%q blob=%q split=%d", pattern, blob, at)
		}
	})
}

// feedInTwoChunks feeds data as two chunks split at at, returning every reported
// match in order.
func feedInTwoChunks(re *regexp.Regexp, data []byte, at int) []OutputMatch {
	m := NewOutputMatcher(re)
	out := append([]OutputMatch(nil), m.FeedAtWithProvenance(data[:at], int64(at), nil)...)
	return append(out, m.FeedAtWithProvenance(data[at:], int64(len(data)), nil)...)
}

// referenceMatches reimplements OutputMatcher's window policy independently of
// the production code for the single-chunk case, where the scan window is the
// whole blob and nothing has been scanned before.
func referenceMatches(re *regexp.Regexp, data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var out []string
	for _, loc := range re.FindAllIndex(referenceAnchorText(data), -1) {
		if loc[1]-loc[0] > outputMatchWindowBytes {
			continue
		}
		out = append(out, referenceExcerpt(data, loc[0]))
	}
	return out
}

// referenceAnchorText mirrors the CRLF rewrite the scanner applies before
// running the pattern: a '\r' that precedes a '\n' becomes a '\n', so a CRLF
// line ends where $ expects it to and indices still address the original bytes.
func referenceAnchorText(data []byte) []byte {
	out := append([]byte(nil), data...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == '\r' && out[i+1] == '\n' {
			out[i] = '\n'
		}
	}
	return out
}

// referenceExcerpt mirrors the reported text: the line the match begins on,
// capped at outputMatchWindowBytes, with one trailing '\r' stripped.
func referenceExcerpt(data []byte, start int) string {
	lo := bytes.LastIndexByte(data[:start], '\n') + 1
	hi := len(data)
	if i := bytes.IndexByte(data[start:], '\n'); i >= 0 {
		hi = start + i
	}
	if hi-lo > outputMatchWindowBytes {
		lo = start
		if hi > start+outputMatchWindowBytes {
			hi = start + outputMatchWindowBytes
		}
	}
	line := data[lo:hi]
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return string(line)
}
