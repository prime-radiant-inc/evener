//go:build serffuzz

package modeldisplay

import "testing"

func init() {
	fuzzCoverageUnion = func(t *testing.T) {
		TestAbbreviateModel(t)
		TestAbbreviatePath(t)
	}
}
