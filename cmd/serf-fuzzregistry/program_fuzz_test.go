package main

import "testing"

// FuzzRegistryProgram replays deterministic parsing, discovery, AST, focus,
// and CLI operation programs selected by fuzz input.
func FuzzRegistryProgram(f *testing.F) {
	cases := []func(*testing.T){
		scenarioRunRegistryAllPipelineFailures, scenarioRegistryMainAndReaderWriterErrors,
		scenarioRegistryFocusAndEmitValidationFailures, scenarioCanonicalAndHelperRejections,
		scenarioASTHelperBranches, scenarioRegistryInjectedFilesystemFailures,
		scenarioReadWorkspaceModuleFailureMatrix, scenarioDiscoverWorkspaceMalformedFilesAndRapidIssues,
		scenarioParseRegistry, scenarioCheckFocusSpecsAcceptsResolvableSpecs,
		scenarioCheckFocusSpecsReportsUnresolvableSpecs, scenarioDiscoverWorkspaceFindsNativeAndMarkedRapidTargets,
		scenarioDiscoverWorkspaceUsesLogicalLabelForSymlinkedModule,
		scenarioDiscoverWorkspaceRejectsSymlinkedModuleOutsideRepository,
		scenarioDiscoverWorkspaceRejectsDuplicateResolvedModuleDirectories,
		scenarioCheckTargetsReportsMissingNativeFuzzer, scenarioCheckTargetsReportsStalePackageRow,
		scenarioCheckTargetsReportsDuplicateIdentity, scenarioCheckTargetsDistinguishesColonContainingTupleFields,
		scenarioDiscoverWorkspaceIgnoresFuzzLikeProductionDeclaration,
		scenarioDiscoverWorkspaceHonorsGoBuildTestFileSelection,
		scenarioDiscoverWorkspaceRecognizesAliasedAndUnnamedNativeFuzzers,
		scenarioDiscoverWorkspaceIgnoresMalformedNativeFuzzers,
		scenarioDiscoverWorkspaceRejectsUnmarkedRapidTest,
		scenarioEmitPlanSortsCoverageTargetsAndExcludesSupportTests,
	}
	for i := range cases {
		f.Add(byte(i))
	}
	f.Fuzz(func(t *testing.T, program byte) { cases[int(program)%len(cases)](t) })
}
