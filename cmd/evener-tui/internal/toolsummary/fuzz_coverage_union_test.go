//go:build serffuzz

package toolsummary

import "testing"

func init() {
	fuzzCoverageUnion = func(t *testing.T) {
		TestFuzzCoverageUnion(t)
		TestHighlightDiffFallbacks(t)
	}
}
