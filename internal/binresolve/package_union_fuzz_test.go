//go:build serffuzz

package binresolve

import "testing"

// FuzzPackageUnion replays every deterministic package scenario under fuzz coverage.

func FuzzPackageUnion(f *testing.F) {

	f.Add(uint8(0))

	f.Fuzz(func(t *testing.T, _ uint8) {

		t.Run("TestResolvePrefersExplicitPath", TestResolvePrefersExplicitPath)
		t.Run("TestResolveRejectsNonExecutableExplicit", TestResolveRejectsNonExecutableExplicit)
		t.Run("TestResolvePrefersSiblingBeforePath", TestResolvePrefersSiblingBeforePath)
		t.Run("TestResolveSurfacesLookPathErrorWhenNothingFound", TestResolveSurfacesLookPathErrorWhenNothingFound)
		t.Run("TestResolveResolvesRelativeCurrentExecutable", TestResolveResolvesRelativeCurrentExecutable)
		t.Run("TestResolveFollowsSymlinkedExecutable", TestResolveFollowsSymlinkedExecutable)
		t.Run("TestResolveMissingSiblingFallsThroughToPath", TestResolveMissingSiblingFallsThroughToPath)
		t.Run("TestSiblingDirHandlesEmptyAndMissing", TestSiblingDirHandlesEmptyAndMissing)
		t.Run("TestIsExecutable_NonExistent", TestIsExecutable_NonExistent)
		t.Run("TestIsExecutable_Directory", TestIsExecutable_Directory)
		t.Run("TestResolve_SiblingExistsButNotExecutable_FallsThroughToPATH", TestResolve_SiblingExistsButNotExecutable_FallsThroughToPATH)
		t.Run("TestSiblingDir_AbsFailsWithVeryLongPath", TestSiblingDir_AbsFailsWithVeryLongPath)
		t.Run("TestInjectedPlatformAndAbsFailures", TestInjectedPlatformAndAbsFailures)
		t.Run("TestResolveWithNilLookPath", TestResolveWithNilLookPath)
	})
}
