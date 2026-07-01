package jobstore

import (
	"regexp"
	"testing"

	"primeradiant.com/serf/fuzz/oracle"
)

// TestOutputMatcherOracleDemo demonstrates the fuzz/oracle combinators against
// the SAME property FuzzOutputMatcher checks by hand — that OutputMatcher agrees
// with the independent referenceMatches splitter, and is deterministic. It shows
// the ergonomics: the hand-written "run both, normalize nil, reflect.DeepEqual,
// Fatalf" block collapses to a single oracle.AgreesWith call, and the "fresh
// matcher fed identically" block to a single oracle.Deterministic call. The
// original FuzzOutputMatcher is unchanged; this is the one-line form a NEW
// differential target would use.
func TestOutputMatcherOracleDemo(t *testing.T) {
	cases := []struct {
		pattern string
		blob    string
	}{
		{`error`, "ok\nerror here\nfine\n"},
		{`^WARN`, "WARN: a\r\nnot a warn\nWARNING xyz"},
		{`.*`, ""},
		{`x`, "no newline tail x"},
		{`a`, "a\n\na\r\n"},
		{`\d+`, "exit_code=42\ncode\n"},
	}

	for _, tc := range cases {
		re := regexp.MustCompile(tc.pattern)
		data := []byte(tc.blob)

		// The production path and the reference splitter as two pure funcs of the
		// blob, so the differential and determinism oracles apply directly.
		matcher := func(b []byte) []string {
			m := NewOutputMatcher(re)
			return normalizeMatches(append(append([]string{}, m.Feed(b)...), m.Flush()...))
		}
		reference := func(b []byte) []string {
			completed, flush := referenceMatches(re, b)
			return normalizeMatches(append(append([]string{}, completed...), flush...))
		}

		// The WS3 workhorse: "the two agree" is the whole oracle.
		oracle.AgreesWith(t, matcher, reference, data, oracle.DeepEqual[[]string])
		// A fresh matcher fed identically yields the same result.
		oracle.Deterministic(t, matcher, data, oracle.DeepEqual[[]string])
	}
}

// normalizeMatches collapses an empty slice to nil so DeepEqual compares
// contents, not nilness — the same normalization FuzzOutputMatcher applies.
func normalizeMatches(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
