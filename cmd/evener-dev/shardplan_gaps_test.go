package dev

import (
	"reflect"
	"testing"
)

// TestParseSurveyMalformedCostSkipped covers the Sscanf error branch
// (line 34) where a survey line matches the regex but the cost field
// cannot be parsed as a float.
func TestParseSurveyMalformedCostSkipped(t *testing.T) {
	got := parseSurvey("--- PASS: TestFoo (not-a-number)\n")
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
