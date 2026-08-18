//go:build serffuzz

package tuitext_test

import "testing"

func init() {
	fuzzCoverageUnion = func(t *testing.T) {
		TestLimitFirstLines(t)
		TestMultilineLines(t)
		TestNonEmptyStrings(t)
		TestShellSectionLineCount(t)
		TestTruncateMultilineText(t)
		TestTruncateText(t)
	}
}
