package main

import "testing"

// FuzzHarvestProgram replays deterministic filesystem, parser, sanitizer,
// emitter, and CLI operation programs selected by fuzz input.
func FuzzHarvestProgram(f *testing.F) {
	cases := []func(*testing.T){
		scenarioHarvestRunFailuresAndMain, scenarioMixedSurfaceFixtures, scenarioSanitizerRemainingPrimitiveBranches,
		scenarioGitleaksScanOutcomes, scenarioHarvestLeakExitAndSmallHelpers, scenarioEmitterWriteFileFailure,
		scenarioHarvesterInjectedSanitizeAndEmitFailures, scenarioReverseHTTPNoMatchAndBadQuery,
		scenarioForEachJSONLineOpenAndEmpty, scenarioRemainingFilesystemAndGateBranches,
		scenarioRunLogAndPersonalKeepNote, scenarioToolArgsSecondDecodeAndSanitizeFailure,
		scenarioHarvestRunInjectedOutcomes, scenarioRunnerFailureAccountingAndHelpers,
		scenarioEncodeBytesSeedRoundTrips, scenarioEmitWritesDedupsAndIsIdempotent,
		scenarioEmitDropsOversize, scenarioEmitDryRunWritesNothing, scenarioEmitIntBytesShape,
		scenarioHarvestEndToEnd, scenarioHarvestPersonalSourceForcesScrub,
		scenarioDiscoverSourcesFindsCanonicalSessionLogs, scenarioSplitSSEEvents,
		scenarioSSESeedWindows, scenarioSSESeedWindows_SkipsOversizedEvent,
		scenarioShapeScrubStripsPlantedSecret, scenarioAbortGateCatchesSecretInEnumValue,
		scenarioKeepValuesRedactsKnownSecret, scenarioKeepValuesEntropyQuarantineAborts,
		scenarioScrubSSEPreservesFraming, scenarioShapeScrubDeterministicAndCollapsing,
		scenarioShapeScrubKeepsTimestampsDecodable,
	}
	for i := range cases {
		f.Add(byte(i))
	}
	f.Fuzz(func(t *testing.T, program byte) { cases[int(program)%len(cases)](t) })
}
