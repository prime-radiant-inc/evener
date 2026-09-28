package fuzzregistry

import (
	"reflect"
	"strconv"
	"testing"
)

// registryProgramScenarios is the deterministic parsing, discovery, AST, and
// CLI scenario table. Every entry replays a fixed behavioral program with fixed
// fixtures, so it is an ordinary table test rather than a fuzz target:
// TestRegistryProgramScenarios runs all of them, and the fuzz estate keeps
// replaying the table as a support row.
func registryProgramScenarios() []func(*testing.T) {
	return []func(*testing.T){
		scenarioRunRegistryAllPipelineFailures, scenarioRegistryMainAndReaderWriterErrors,
		scenarioRegistryEmitValidationFailures, scenarioCanonicalAndHelperRejections,
		scenarioASTHelperBranches, scenarioRegistryInjectedFilesystemFailures,
		scenarioReadWorkspaceModuleFailureMatrix, scenarioDiscoverWorkspaceMalformedFilesAndRapidIssues,
		scenarioParseRegistry, scenarioDiscoverWorkspaceFindsNativeAndMarkedRapidTargets,
		scenarioDiscoverWorkspaceUsesLogicalLabelForSymlinkedModule,
		scenarioDiscoverWorkspaceRejectsSymlinkedModuleOutsideRepository,
		scenarioDiscoverWorkspaceRejectsDuplicateResolvedModuleDirectories,
		scenarioCheckTargetsReportsMissingNativeFuzzer, scenarioCheckTargetsReportsStalePackageRow,
		scenarioCheckSupportTargetsValidatesPackagesAndFunctionNames,
		scenarioCheckTargetsReportsDuplicateIdentity, scenarioCheckTargetsDistinguishesColonContainingTupleFields,
		scenarioDiscoverWorkspaceIgnoresFuzzLikeProductionDeclaration,
		scenarioDiscoverWorkspaceHonorsGoBuildTestFileSelection,
		scenarioDiscoverWorkspaceRecognizesAliasedAndUnnamedNativeFuzzers,
		scenarioDiscoverWorkspaceIgnoresMalformedNativeFuzzers,
		scenarioDiscoverWorkspaceRejectsUnmarkedRapidTest,
		scenarioEmitPlanSortsCoverageTargetsAndExcludesSupportTests,
	}
}

// TestRegistryProgramScenarios replays every deterministic scenario. It replaces
// the selector byte FuzzRegistryProgram used to carry: the scenarios are fixed
// programs, so selecting one by fuzz byte added no input domain.
func TestRegistryProgramScenarios(t *testing.T) {
	for i, scenario := range registryProgramScenarios() {
		t.Run(strconv.Itoa(i), scenario)
	}
}

// FuzzRegistryProgram discovers native and explicitly marked rapid fuzz targets
// from arbitrary Go test source. The payload becomes the content of the
// workspace module's x_test.go; the oracle is the never-panic floor plus
// determinism and a non-empty identity for every discovered target. Fixed
// scenario coverage lives in TestRegistryProgramScenarios, not in a selector
// byte, so fuzz bytes are a real discovery input.
func FuzzRegistryProgram(f *testing.F) {
	for _, seed := range []string{
		"",
		"package m\n",
		"package m\n\nfunc (",
		"package m\n\nimport \"testing\"\n\nfunc FuzzX(f *testing.F) {}\n",
		"package m\n\nimport \"testing\"\n\nfunc FuzzX(f *testing.F) {}\nfunc FuzzY(f *testing.F) {}\n",
		"package m\n\nimport (\n\t\"testing\"\n\t\"pgregory.net/rapid\"\n)\n\n// evener:fuzz rapid\nfunc TestR(t *testing.T) { rapid.Check(t, func(rt *rapid.T) {}) }\n",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, source []byte) {
		if len(source) > 1<<16 {
			return
		}
		first := registryProgramDiscover(t, source)
		again := registryProgramDiscover(t, source)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("discovery is non-deterministic:\nfirst:  %+v\nsecond: %+v", first, again)
		}
		for _, target := range first {
			if target.Kind == "" || target.Module == "" || target.Package == "" || target.Name == "" {
				t.Fatalf("discovery yielded an empty identity field: %+v", target)
			}
		}
	})
}

// registryProgramDiscover writes source as the only test file of a throwaway
// workspace module and returns the targets DiscoverWorkspace finds. A parse or
// discovery error yields no targets, so malformed source still exercises the
// error paths rather than being rejected before the parser sees it.
func registryProgramDiscover(t *testing.T, source []byte) []Target {
	t.Helper()
	root := newWorkspace(t, map[string]string{
		"go.work":     "go 1.25.0\n\nuse ./m\n",
		"m/go.mod":    "module example.test/m\n\ngo 1.25.0\n",
		"m/x_test.go": string(source),
	})
	targets, err := DiscoverWorkspace(root)
	if err != nil {
		return nil
	}
	return targets
}

// TestRegistryProgramExploresSourceInput proves the payload is a real input
// domain, not a selector: two distinct source programs make discovery report
// different target kinds, and malformed source reaches the parser's error path.
// Before the input-domain change the target ignored every byte but the case
// index, so no payload could change the outcome.
func TestRegistryProgramExploresSourceInput(t *testing.T) {
	onlyNative := registryProgramDiscover(t, []byte("package m\n\nimport \"testing\"\n\nfunc FuzzOne(f *testing.F) {}\n"))
	if len(onlyNative) != 1 || onlyNative[0].Kind != "native" || onlyNative[0].Name != "FuzzOne" {
		t.Fatalf("native source discovery = %+v", onlyNative)
	}
	onlyRapid := registryProgramDiscover(t, []byte("package m\n\nimport (\n\t\"testing\"\n\t\"pgregory.net/rapid\"\n)\n\n// evener:fuzz rapid\nfunc TestOne(t *testing.T) { rapid.Check(t, func(rt *rapid.T) {}) }\n"))
	if len(onlyRapid) != 1 || onlyRapid[0].Kind != "rapid" || onlyRapid[0].Name != "TestOne" {
		t.Fatalf("rapid source discovery = %+v", onlyRapid)
	}
	if got := registryProgramDiscover(t, []byte("package m\n\nfunc (")); len(got) != 0 {
		t.Fatalf("malformed source discovery = %+v", got)
	}
}
