package main

import "testing"

func FuzzDocsCheckCoverage(f *testing.F) {
	for scenario := uint8(0); scenario < 3; scenario++ {
		f.Add(scenario)
	}
	f.Fuzz(func(t *testing.T, scenario uint8) {
		switch scenario % 3 {
		case 0:
			TestRunDocsCheckAllPaths(t)
		case 1:
			TestCheckPackageEveryDeclarationKind(t)
		case 2:
			TestMainUsesExit(t)
		}
	})
}
