package main

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestParseSurveyReadsTopLevelPassAndSkipLines(t *testing.T) {
	survey := strings.Join([]string{
		"=== RUN   TestAlpha",
		"--- PASS: TestAlpha (1.50s)",
		"    --- PASS: TestAlpha/subtest (1.00s)", // subtest: excluded
		"--- SKIP: TestBeta (0.00s)",
		"--- FAIL: TestGamma (0.25s)", // failed: not a weight
		"--- PASS: ExampleDelta (0.10s)",
		"ok  \tprimeradiant.com/serf/agent\t2.0s",
	}, "\n")
	got := parseSurvey(survey)
	want := []testCost{
		{"TestAlpha", 1.5},
		{"TestBeta", 0},
		{"ExampleDelta", 0.1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSurvey = %v, want %v", got, want)
	}
}

func TestParseSurveyIndentedTopLevelLinesStillCount(t *testing.T) {
	// The shell parser stripped each line before matching, so a leading
	// space does not hide a top-level result.
	got := parseSurvey("  --- PASS: TestAlpha (0.30s)\n")
	want := []testCost{{"TestAlpha", 0.3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSurvey = %v, want %v", got, want)
	}
}

func TestParseSurveyDuplicateNamesKeepOnePlace(t *testing.T) {
	// dict semantics in the original: last value wins, first position kept,
	// and downstream packing never sees a duplicate.
	survey := "--- PASS: TestAlpha (1.00s)\n--- PASS: TestBeta (2.00s)\n--- PASS: TestAlpha (3.00s)\n"
	got := parseSurvey(survey)
	want := []testCost{
		{"TestAlpha", 3.0},
		{"TestBeta", 2.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSurvey = %v, want %v", got, want)
	}
}

func TestEqualWeightsFiltersToTestsAndExamples(t *testing.T) {
	list := "TestAlpha\nBenchmarkNope\nExampleBeta\nFuzzNope\n\nok\tmodule\t0.01s\n"
	got := equalWeights(list)
	want := []testCost{
		{"TestAlpha", 1.0},
		{"ExampleBeta", 1.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("equalWeights = %v, want %v", got, want)
	}
}

func TestPackShardsBalancesLongestProcessingTimeFirst(t *testing.T) {
	costs := []testCost{
		{"a", 5}, {"b", 4}, {"c", 3}, {"d", 3}, {"e", 3},
	}
	bins, loads, err := packShards(costs, 2)
	if err != nil {
		t.Fatalf("packShards: %v", err)
	}
	// LPT walk: a→bin0(5), b→bin1(4), c→bin1(7), d→bin0(8), e→bin1(10) —
	// the same assignment the original's stable sort + first-minimum
	// produced.
	wantBins := [][]string{{"a", "d"}, {"b", "c", "e"}}
	wantLoads := []float64{8, 10}
	if !reflect.DeepEqual(bins, wantBins) || !reflect.DeepEqual(loads, wantLoads) {
		t.Fatalf("packShards = %v %v, want %v %v", bins, loads, wantBins, wantLoads)
	}
}

func TestPackShardsTiesKeepInputOrder(t *testing.T) {
	costs := []testCost{{"first", 1}, {"second", 1}, {"third", 1}}
	bins, _, err := packShards(costs, 3)
	if err != nil {
		t.Fatalf("packShards: %v", err)
	}
	want := [][]string{{"first"}, {"second"}, {"third"}}
	if !reflect.DeepEqual(bins, want) {
		t.Fatalf("packShards = %v, want %v", bins, want)
	}
}

func TestPackShardsRefusesEmptyTestSet(t *testing.T) {
	_, _, err := packShards(nil, 4)
	if err == nil || !strings.Contains(err.Error(), "found no tests to shard") {
		t.Fatalf("packShards(nil) err = %v, want found-no-tests refusal", err)
	}
}

func TestPackShardsRefusesEmptyBins(t *testing.T) {
	costs := []testCost{{"a", 1}, {"b", 1}, {"c", 1}}
	_, _, err := packShards(costs, 5)
	want := "asked for 5 shards but only 3 are non-empty; lower AGENT_SHARD_COUNT"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("packShards err = %v, want %q", err, want)
	}
}

func TestPackShardsProvesBijection(t *testing.T) {
	costs := []testCost{{"dup", 1}, {"dup", 2}}
	_, _, err := packShards(costs, 1)
	if err == nil || !strings.Contains(err.Error(), "partition is not a bijection over the test set") {
		t.Fatalf("packShards with duplicate names err = %v, want bijection refusal", err)
	}
}

func TestNameRegexAnchorsAndEscapes(t *testing.T) {
	got := nameRegex([]string{"TestAlpha", "TestAl", "Example$odd"})
	re, err := regexp.Compile(got)
	if err != nil {
		t.Fatalf("nameRegex output does not compile: %v", err)
	}
	for name, want := range map[string]bool{
		"TestAlpha":    true,
		"TestAl":       true,
		"Example$odd":  true,
		"TestAlphaFox": false, // no prefix match past an anchor
		"TestA":        false,
		"Exampleodd":   false, // $ must be literal
	} {
		if re.MatchString(name) != want {
			t.Fatalf("regex %q match %q = %v, want %v", got, name, !want, want)
		}
	}
}

func TestTranslateFlagsPortsTheTableVerbatim(t *testing.T) {
	got := translateFlags([]string{"-short", "-count=2", "-v", "-race", "-run", "TestNope", "-timeout=30s"})
	// -run and -timeout fall outside the table and are dropped — the
	// script's wart, preserved.
	want := []string{"-test.short", "-test.count=2", "-test.v", "-test.race"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("translateFlags = %v, want %v", got, want)
	}
}

func TestTestSetKeyIsOrderInsensitiveAndStable(t *testing.T) {
	a := testSetKey("TestB\nTestA\n")
	b := testSetKey("TestA\nTestB\n")
	if a != b {
		t.Fatalf("key differs across orderings: %q vs %q", a, b)
	}
	if len(a) != 16 || strings.ToLower(a) != a {
		t.Fatalf("key %q is not 16 lowercase hex chars", a)
	}
	if c := testSetKey("TestA\nTestC\n"); c == a {
		t.Fatalf("different test sets share key %q", c)
	}
}
