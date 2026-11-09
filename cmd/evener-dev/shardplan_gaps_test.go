package dev

import (
	"reflect"
	"testing"
)

// TestParseSurveyMalformedCostSkipped covers the float-parse error branch:
// the line matches surveyLine's `[0-9.]+` cost field but is not a float.
func TestParseSurveyMalformedCostSkipped(t *testing.T) {
	got := parseSurvey("--- PASS: TestFoo (1.2.3s)\n")
	if len(got) != 0 {
		t.Fatalf("malformed cost should be skipped, got %v", got)
	}
}

// TestEqualWeightsDuplicateNamesDeduped covers the seen[name] skip
// branch (line 57) in equalWeights.
func TestEqualWeightsDuplicateNamesDeduped(t *testing.T) {
	got := equalWeights("TestAlpha\nTestAlpha\nExampleBeta\n")
	want := []testCost{
		{"TestAlpha", 1.0},
		{"ExampleBeta", 1.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("equalWeights with duplicates = %v, want %v", got, want)
	}
}
