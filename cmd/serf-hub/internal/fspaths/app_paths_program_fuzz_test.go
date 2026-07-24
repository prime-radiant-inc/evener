package fspaths_test

import "testing"

func FuzzAppPathsBehaviorProgram(f *testing.F) {
	checks := []func(*testing.T){
		checkCompletePaths,
		checkCompletePaths_TraversalReturnsNoSuggestions,
		checkValidateLaunchPath,
	}
	for i := range checks {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, selector uint8) {
		checks[int(selector)%len(checks)](t)
	})
}
