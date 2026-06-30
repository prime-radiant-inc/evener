package jobstore

import (
	"bytes"
	"reflect"
	"regexp"
	"testing"
)

// FuzzOutputMatcher drives OutputMatcher — the streaming carry/regexp line
// matcher that watches a job's output and fires on completed lines. Input is an
// arbitrary regexp pattern plus an arbitrary output blob. The blob is fed as a
// single Feed() chunk and then Flush()ed, so every chunk boundary coincides with
// the blob itself and the result is fully determined by the line structure.
//
// Oracle: a hand-written reference line-splitter that mirrors the matcher's
// documented policy (split on '\n'; a completed line is the bytes before a '\n'
// with a single trailing '\r' stripped before matching; lines longer than
// maxOutputMatcherLineBytes — with a +1 budget when the fragment ends in '\r' —
// are skipped; Flush emits the unterminated tail un-stripped only when it is not
// overlong and at most maxOutputMatcherLineBytes long). The fuzzer asserts:
//   - Feed()+Flush() returns EXACTLY the reference match list (a real
//     reference-implementation differential, not a no-panic floor).
//   - ScanRetained returns the LAST completed match and a matched flag that agree
//     with the reference, cross-checking the independent scan-path loop.
//   - Every returned line genuinely matches the regexp and contains no newline.
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
		wantCompleted, wantFlush := referenceMatches(re, data)
		wantAll := append(append([]string{}, wantCompleted...), wantFlush...)

		m := NewOutputMatcher(re)
		got := append(append([]string{}, m.Feed(data)...), m.Flush()...)

		// Normalize nil vs empty so DeepEqual compares contents, not nilness.
		if len(got) == 0 {
			got = nil
		}
		if len(wantAll) == 0 {
			wantAll = nil
		}
		if !reflect.DeepEqual(got, wantAll) {
			t.Fatalf("Feed+Flush mismatch\n pattern=%q\n blob=%q\n got =%#v\n want=%#v", pattern, blob, got, wantAll)
		}

		// Every reported line must really match and carry no newline.
		for _, line := range got {
			if !re.MatchString(line) {
				t.Fatalf("reported line %q does not match pattern %q", line, pattern)
			}
			if bytes.IndexByte([]byte(line), '\n') >= 0 {
				t.Fatalf("reported line contains a newline: %q", line)
			}
		}

		// ScanRetained: independent scan-path loop, cross-checked against the
		// reference's completed-line list (it ignores the unterminated tail).
		last, matched := NewOutputMatcher(re).ScanRetained(data)
		wantMatched := len(wantCompleted) > 0
		if matched != wantMatched {
			t.Fatalf("ScanRetained matched=%v want=%v (pattern=%q blob=%q)", matched, wantMatched, pattern, blob)
		}
		if wantMatched && last != wantCompleted[len(wantCompleted)-1] {
			t.Fatalf("ScanRetained last=%q want=%q", last, wantCompleted[len(wantCompleted)-1])
		}

		// Determinism: a fresh matcher fed identically yields the same result.
		m2 := NewOutputMatcher(re)
		got2 := append(append([]string{}, m2.Feed(data)...), m2.Flush()...)
		if len(got2) == 0 {
			got2 = nil
		}
		if !reflect.DeepEqual(got, got2) {
			t.Fatalf("matcher not deterministic for pattern=%q blob=%q", pattern, blob)
		}
	})
}

// referenceMatches reimplements OutputMatcher's line policy independently of the
// production code, returning the completed-line matches (in order) and any
// Flush-emitted unterminated-tail match. It mirrors appendLineFragment's overlong
// budget, completedLine's single trailing-'\r' strip, and Flush's raw,
// un-stripped, 4096-capped tail handling.
func referenceMatches(re *regexp.Regexp, data []byte) (completed, flush []string) {
	pieces := bytes.Split(data, []byte("\n"))
	// Every piece but the last precedes a '\n' and is a completed line; the
	// final piece is the unterminated tail Flush handles (empty if data ended
	// in '\n' or is empty).
	for _, frag := range pieces[:len(pieces)-1] {
		if overlongFragment(frag) {
			continue
		}
		line := stripTrailingCR(frag)
		if re.Match(line) {
			completed = append(completed, string(line))
		}
	}

	tail := pieces[len(pieces)-1]
	if len(tail) == 0 || overlongFragment(tail) {
		return completed, nil
	}
	// Flush does NOT strip a trailing '\r' and caps at maxOutputMatcherLineBytes.
	if len(tail) > maxOutputMatcherLineBytes {
		return completed, nil
	}
	if re.Match(tail) {
		flush = append(flush, string(tail))
	}
	return completed, flush
}

// overlongFragment mirrors appendLineFragment's drop condition for a single
// fragment appended onto an empty carry: a trailing '\r' grants one extra byte.
func overlongFragment(frag []byte) bool {
	if len(frag) == 0 {
		return false
	}
	limit := maxOutputMatcherLineBytes
	if frag[len(frag)-1] == '\r' {
		limit++
	}
	return len(frag) > limit
}

func stripTrailingCR(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}
