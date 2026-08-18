package main

import "testing"

func FuzzInternalCheckCoverage(f *testing.F) {
	for scenario := range uint8(5) {
		f.Add(scenario)
	}
	f.Fuzz(func(t *testing.T, scenario uint8) {
		switch scenario % 5 {
		case 0:
			t.Run("results", TestRunWithAllResults)
		case 1:
			t.Run("main", TestMainUsesExit)
		case 2:
			t.Run("shapes", TestWalkTypeAllShapesAndObjects)
		case 3:
			t.Run("loader", TestFindLeaksLoaderFailuresAndLeak)
		case 4:
			t.Run("type-arguments", TestWalkTypePublicNamedTypeArguments)
		}
		t.Run("libraries", TestLibrariesHaveNoInternalLeaks)
		t.Run("predicate", TestIsSerfInternal)
		t.Run("detect", TestWalkTypeDetectsInternalNamed)
		t.Run("ignore", TestWalkTypeIgnoresNonInternalNamed)
		t.Run("object", TestCheckObjectStructFieldExposesInternal)
	})
}
