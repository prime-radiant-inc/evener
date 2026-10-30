package hubcore

import "testing"

func fuzzScenarioInputsVersionBump(t *testing.T) {
	iv := &InputsVersion{}
	if iv.Load() != 0 {
		t.Fatal("fresh version should be 0")
	}
	iv.Bump()
	iv.Bump()
	if iv.Load() != 2 {
		t.Fatalf("want 2 after two bumps, got %d", iv.Load())
	}
}
