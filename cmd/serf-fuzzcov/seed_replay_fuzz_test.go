//go:build serffuzz

package main

import "testing"

// FuzzCoverageSeedScenarios promotes the reporter's deterministic behavioral
// fixtures into the native fuzz corpus. The selector lets the fuzz engine
// continuously recombine which complete filesystem/reporting scenario runs.
func FuzzCoverageSeedScenarios(f *testing.F) {
	scenarios := []func(*testing.T){
		TestReadGlobalProfiles,
		TestReportGlobalUnionsExactPackageBlocks,
		TestReportGlobalRequiresStrictRawThreshold,
		TestGlobalFloorsNeverLowerAndNeverBlessFailure,
		TestGlobalBlessRoundTripDoesNotRegressExactRatio,
		TestReportGlobalRejectsBlocksOutsideResolvedProfileOwnership,
		TestGlobalBlessDoesNotWaiveOrLowerAFloor,
		TestRunGlobalModeRejectsSubTargetMinimumForCheckAndBless,
		TestRunGlobalModeRejectsSubTargetMinimumBeforeAdvisoryReporting,
		TestGlobalJSONRemainsValidWhenBlessing,
		TestGeneratedExclusionRequiresHeaderAndRemovesProfileBlocks,
		TestPlatformExclusionMustBeUnavailableAndExclusionsRejectInvalidRows,
		TestGlobalReportPrintsAppliedExclusionsInTextAndJSON,
		TestParseBlock,
		TestParseProfileRejectsNonSetMode,
		TestParseFocus,
		TestJoinImportAndPkgSubdir,
		TestReadIgnoreRequiresReason,
		TestWriteFloorsUpwardOnly,
		TestRunGlobalModeCheckUsesStrictThreshold,
		TestGapMap,
		TestReadRegistry,
		TestReadRegistryRejectsShortLine,
		TestStaticFuzzedPackages,
		TestComputeTargetFocusAttribution,
		TestMatchSignature,
		TestFuzzedPackages,
		TestTruncate,
		TestScanUniverse,
		TestReadManifest,
		TestReadManifestRejectsShortLine,
		TestCheckExit,
		TestPrintReportMarks,
		TestParseBlockErrors,
		TestParseProfileErrors,
		TestFuncLineRangeErrors,
		TestReadModulePathsErrors,
		TestReadFloorsMissingAndMalformed,
		TestRunGapOnly,
	}
	for i := range scenarios {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, selector uint8) {
		scenarios[int(selector)%len(scenarios)](t)
	})
}
