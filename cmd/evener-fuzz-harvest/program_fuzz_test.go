package fuzzharvest

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// harvestProgramScenarios is the deterministic filesystem, parser, sanitizer,
// emitter, and CLI scenario table. Every entry replays a fixed behavioral
// program with fixed fixtures, so it is an ordinary table test rather than a
// fuzz target: TestHarvestProgramScenarios runs all of them, and the fuzz
// estate keeps replaying the table as a support row.
func harvestProgramScenarios() []func(*testing.T) {
	return []func(*testing.T){
		scenarioHarvestRunFailuresAndMain, scenarioMixedSurfaceFixtures, scenarioSanitizerRemainingPrimitiveBranches,
		scenarioGitleaksScanOutcomes, scenarioHarvestLeakExitAndSmallHelpers, scenarioEmitterWriteFileFailure,
		scenarioHarvesterInjectedSanitizeAndEmitFailures, scenarioReverseHTTPNoMatchAndBadQuery,
		scenarioReverseHTTPAuthPathTokenNeverHarvested, scenarioReverseHTTPLegitimateSessionSuffixNotRedacted,
		scenarioReverseHTTPAuthPathTokenCaseInsensitive, scenarioIsAuthBootstrapPathShapes,
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
}

// TestHarvestProgramScenarios replays every deterministic scenario. It replaces
// the selector byte FuzzHarvestProgram used to carry: the scenarios are fixed
// programs, so selecting one by fuzz byte added no input domain.
func TestHarvestProgramScenarios(t *testing.T) {
	for i, scenario := range harvestProgramScenarios() {
		t.Run(strconv.Itoa(i), scenario)
	}
}

const (
	// harvestProgramSurfaces is the number of per-surface record parsers the
	// fuzz payload routes between.
	harvestProgramSurfaces = 5
	// harvestProgramMaxBytes caps the payload so a fuzz iteration stays cheap.
	harvestProgramMaxBytes = 1 << 16
)

// FuzzHarvestProgram feeds arbitrary recorded-traffic bytes through the
// harvester's per-surface record parsers. The first byte selects which recorder
// file the rest of the payload is treated as — SSE api log, appwire log, HTTP
// recorder log, jobs log, or transcript — and the payload becomes that file's
// content. The oracle is the never-panic floor plus the harvester's never-leak
// contract: re-reading everything it emitted must find no gated secret. Fixed
// scenario coverage lives in TestHarvestProgramScenarios, not in a selector
// byte, so fuzz bytes are a real parsing input.
func FuzzHarvestProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{0},
		append([]byte{0}, []byte("data: {\"type\":\"x\"}\n\n")...),
		append([]byte{1}, []byte(`{"frame":"bad"}`)...),
		append([]byte{2}, []byte(`{"method":"GET","path":"/health"}`)...),
		append([]byte{3}, []byte(`{"kind":"job_started"}`)...),
		append([]byte{4}, []byte(`{"kind":"entry"}`)...),
		{0, 0xff, 0x00},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) == 0 || len(program) > harvestProgramMaxBytes {
			return
		}
		harvestProgramRun(t, program[0]%harvestProgramSurfaces, program[1:])
	})
}

// harvestProgramRun routes payload through one harvester surface into a fresh
// corpus root and fails if any emitted seed carries a gated secret. It returns
// the emitted corpus (relative path -> file bytes) so callers can assert the
// payload actually steers the outcome.
func harvestProgramRun(t *testing.T, surface byte, payload []byte) map[string]string {
	t.Helper()
	source := t.TempDir()
	path := filepath.Join(source, "records.jsonl")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	r := newRunner(out, NewEmitter(false, defaultMaxSeedBytes), nil)
	san := &Sanitizer{}
	switch surface {
	case 0:
		harvestSSE(r, san, []string{path})
	case 1:
		harvestAppwire(r, san, []string{path})
	case 2:
		harvestHTTP(r, []string{path})
	case 3:
		harvestJobs(r, san, []string{path})
	default:
		harvestToolArgs(r, san, []string{path}, []string{"known"})
	}
	return harvestProgramCorpus(t, out)
}

// harvestProgramCorpus reads every emitted corpus file and enforces the
// never-leak contract independently of the emitter: a gated secret must never
// reach disk.
func harvestProgramCorpus(t *testing.T, root string) map[string]string {
	t.Helper()
	corpus := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if finding := detectSecret(data, false); finding != "" {
			t.Fatalf("harvester emitted a seed carrying a gated secret (%s): %s", finding, path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		corpus[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return corpus
}

// TestHarvestProgramExploresRecordBytes proves the payload is a real input
// domain, not a selector: two distinct api-log records make the harvester emit
// different corpora, while a non-record payload emits none. Before the
// input-domain change the target ignored every byte but the case index, so no
// payload could change the outcome.
func TestHarvestProgramExploresRecordBytes(t *testing.T) {
	first := harvestProgramRun(t, 0, []byte(canonicalAPIAttemptLine(t, "openai", []byte("data: {\"a\":1}\n\n"))+"\n"))
	second := harvestProgramRun(t, 0, []byte(canonicalAPIAttemptLine(t, "openai", []byte("data: {\"b\":2}\n\n"))+"\n"))
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("accepted records emitted no seeds: first=%v second=%v", first, second)
	}
	if reflect.DeepEqual(first, second) {
		t.Fatalf("distinct payloads produced identical corpora: %v", first)
	}
	if third := harvestProgramRun(t, 0, []byte("not-an-api-attempt-record\n")); len(third) != 0 {
		t.Fatalf("malformed record emitted seeds: %v", third)
	}
}
