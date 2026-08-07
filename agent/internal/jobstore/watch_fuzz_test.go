package jobstore

import (
	"bytes"
	"reflect"
	"regexp"
	"testing"
)

// FuzzOutputMatcher drives OutputMatcher — the streaming byte-window regexp
// matcher that watches a job's output. Input is an arbitrary regexp pattern plus
// an arbitrary output blob. The blob is fed as a single Feed() chunk, so the scan
// window is exactly the blob and the result is fully determined by it.
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
	f.Add(`error`, "ok\nerror here\nfine\n")
	f.Add(`^WARN`, "WARN: a\r\nnot a warn\nWARNING xyz")
	f.Add(`.*`, "")
	f.Add(`x`, "no newline tail x")
	f.Add(`a`, "a\n\na\r\n")
	f.Add(``, "anything\n")
	f.Add(`(?s).`, "\n\n\n")
	f.Add(`[`, "broken pattern\n")
	f.Add(`\d+`, "exit_code=42\ncode\n")

	f.Fuzz(func(t *testing.T, pattern, blob string) {
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
	})
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
