//go:build serffuzz

package agent

import (
	"bytes"
	"regexp"
	"testing"
)

// This file fuzzes scanSegment (session_tools_jobs.go), the streaming job-output
// grep primitive that decides, line by line, whether a regexp matches a chunk of
// a job's output. It applies the snapshot grep's line semantics: a complete line
// matches without its trailing "\n" (and a "\r" before it), a line whose content
// exceeds maxLineBytes can never match, and a trailing unterminated line is
// matched as-is.
//
// The oracle is a DIFFERENTIAL against an independent line decomposition
// (bytes.Split) that applies the same documented semantics. scanSegment walks the
// buffer with a manual IndexByte loop and a "dead line" fast-path; the reference
// splits the whole buffer up front. The two must agree on whether any line
// matched — a drift here would silently corrupt the "output matched" signal that
// gates job_wait grep completion.

// referenceSegmentMatch reports whether a fresh single-shot scan of seg matches
// re under scanSegment's line semantics, decomposed independently via bytes.Split.
func referenceSegmentMatch(seg []byte, re *regexp.Regexp, maxLineBytes int) bool {
	if len(seg) == 0 {
		return false
	}
	parts := bytes.Split(seg, []byte("\n"))
	for i, part := range parts {
		if i == len(parts)-1 {
			// Trailing unterminated remainder: matched as-is when within the width
			// cap. (A remainder wider than maxLineBytes can never match, which also
			// subsumes scanSegment's dead-line fast-path.)
			if len(part) == 0 {
				continue // seg ended on a newline: no trailing line
			}
			if len(part) <= maxLineBytes && re.Match(part) {
				return true
			}
			continue
		}
		// Complete line: drop a single trailing carriage return before matching.
		line := part
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) <= maxLineBytes && re.Match(line) {
			return true
		}
	}
	return false
}

// FuzzArScanSegment drives scanSegment over a fuzzed buffer, pattern, and width
// cap. Oracles: never panics; DIFFERENTIAL — scanSegment on a fresh scan state
// agrees with referenceSegmentMatch on whether any line matched.
func FuzzArScanSegment(f *testing.F) {
	type seed struct {
		seg     string
		pattern string
		maxLine uint8
	}
	seeds := []seed{
		{"hello\nworld\n", "world", 10},
		{"hello\r\nworld", "hello", 10},
		{"no newline here", "here", 20},
		{"", "x", 5},
		{"\n\n\n", "", 5},
		{"aaaaaaaaaaaaaaaaaaaa\nb", "a", 5}, // first line exceeds a small cap
		{"prefix\nmatch\nsuffix", "^match$", 40},
		{"tab\ttab\nline", `\t`, 40},
		{"line1\nline2\nline3\n", "line2", 40},
		{"верх\nниз", "низ", 40},
	}
	for _, s := range seeds {
		f.Add(s.seg, s.pattern, s.maxLine)
	}

	f.Fuzz(func(t *testing.T, seg, pattern string, maxLine8 uint8) {
		if len(pattern) > 200 {
			return // keep regexp compilation cheap and bounded
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return // an invalid pattern never reaches scanSegment in production
		}
		maxLineBytes := int(maxLine8)

		g := &jobGrepScan{}
		got := g.scanSegment([]byte(seg), re, maxLineBytes)
		want := referenceSegmentMatch([]byte(seg), re, maxLineBytes)

		if got != want {
			t.Fatalf("scanSegment disagreed with reference: got=%v want=%v\n seg=%q pattern=%q maxLine=%d",
				got, want, seg, pattern, maxLineBytes)
		}
	})
}