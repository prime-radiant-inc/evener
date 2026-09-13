package dev

import (
	"os"
	"path/filepath"
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
		"ok  \tprimeradiant.com/evener/agent\t2.0s",
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

func TestParseFlagsReadsShortInEverySpelling(t *testing.T) {
	for _, tc := range []struct {
		flags []string
		want  bool
	}{
		{flags: []string{"-short"}, want: true},
		{flags: []string{"--short"}, want: true},
		{flags: []string{"-short=true"}, want: true},
		{flags: []string{"-short=f"}, want: false},
		{flags: []string{"-short=FALSE"}, want: false},
		{flags: []string{"-short=0"}, want: false},
		// Last occurrence wins, as go's flag package reads them.
		{flags: []string{"-short=false", "-short"}, want: true},
		{flags: []string{"-short", "-short=false"}, want: false},
		// A value go itself would refuse: read as short rather than dropped.
		{flags: []string{"-short=yes"}, want: true},
		// A flag's value is a value, not a flag.
		{flags: []string{"-run", "-short"}, want: false},
		{flags: nil, want: false},
	} {
		parsed, err := parseFlags(tc.flags)
		if err != nil {
			t.Fatalf("parseFlags(%v) = %v", tc.flags, err)
		}
		if parsed.short != tc.want {
			t.Fatalf("parseFlags(%v).short = %v, want %v", tc.flags, parsed.short, tc.want)
		}
	}
}

func TestParseFlagsRefusesWhatItCannotHonour(t *testing.T) {
	for _, flags := range [][]string{
		{"-C", "/tmp"},
		{"-C=/tmp"},
		{"--C", "/tmp"},
		{"-run"},
		{"-tags"},
	} {
		if _, err := parseFlags(flags); err == nil {
			t.Fatalf("parseFlags(%v) = no error, want one", flags)
		}
	}
	// A -C that is another flag's value is a value.
	parsed, err := parseFlags([]string{"-run", "-C", "-short"})
	if err != nil {
		t.Fatalf("parseFlags(-run -C -short) = %v", err)
	}
	if parsed.short != true || len(parsed.build) != 0 {
		t.Fatalf("parseFlags(-run -C -short) = %+v, want short with no build flags", parsed)
	}
}

func TestShellAndGoForwardTheSameBuildFlags(t *testing.T) {
	// The two tables are one rule in two languages (#1247). This reads the
	// shell's and fails on any difference in either direction, so neither can
	// drift without the other hearing about it.
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "gate", "run-module-tests.sh"))
	if err != nil {
		t.Fatalf("reading the gate script: %v", err)
	}
	body := string(source)
	start := strings.Index(body, "flag_is_build() {")
	if start < 0 {
		t.Fatal("flag_is_build is gone from the gate script; this check needs updating with it")
	}
	end := strings.Index(body[start:], "\n}\n")
	shell := map[string]bool{}
	for _, line := range strings.Split(body[start:start+end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-") || !strings.HasSuffix(line, ")") {
			continue
		}
		for _, name := range strings.Split(strings.TrimSuffix(line, ")"), "|") {
			if name = strings.TrimSpace(name); name != "" {
				shell[name] = true
			}
		}
	}
	golang := map[string]bool{}
	for name := range buildValueFlags {
		golang[name] = true
	}
	for name := range buildBareFlags {
		golang[name] = true
	}
	for name := range shell {
		if !golang[name] {
			t.Errorf("the gate script forwards %s to the enumeration and this package does not", name)
		}
	}
	for name := range golang {
		if !shell[name] {
			t.Errorf("this package forwards %s to the build and the gate script does not", name)
		}
	}
}

func TestParseFlagsSendsBuildFlagsToTheBuild(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		build []string
		test  []string
	}{
		{
			name:  "the old table, with -race now a build flag",
			flags: []string{"-short", "-count=2", "-v", "-race", "-run", "TestNope", "-timeout=30s"},
			build: []string{"-race"},
			// -run and -timeout fall outside both tables and are dropped, as
			// the script's case statement dropped them.
			test: []string{"-test.short", "-test.count=2", "-test.v"},
		},
		{
			name:  "a value flag takes the argument after it",
			flags: []string{"-tags", "integration", "-short"},
			build: []string{"-tags", "integration"},
			test:  []string{"-test.short"},
		},
		{
			name:  "the inline spelling of the same flag",
			flags: []string{"-tags=integration", "-count=1"},
			build: []string{"-tags=integration"},
			test:  []string{"-test.count=1"},
		},
		{
			name:  "double dashes are the same flags",
			flags: []string{"--race", "--tags=integration", "--short", "--count=3"},
			build: []string{"-race", "-tags=integration"},
			test:  []string{"-test.short", "-test.count=3"},
		},
		{
			name:  "the sanitisers and trimpath build, they do not run",
			flags: []string{"-msan", "-asan", "-trimpath", "-v"},
			build: []string{"-msan", "-asan", "-trimpath"},
			test:  []string{"-test.v"},
		},
		{
			// The shell's forwarded set, in both spellings, since the two
			// tables have to agree until #1247 makes them one.
			name: "every build flag the shell forwards, inline value form",
			flags: []string{
				"-tags=a", "-mod=mod", "-modfile=go.mod", "-overlay=o.json",
				"-pgo=off", "-compiler=gc", "-trimpath", "-race=true",
				"-msan=true", "-asan=true",
			},
			build: []string{
				"-tags=a", "-mod=mod", "-modfile=go.mod", "-overlay=o.json",
				"-pgo=off", "-compiler=gc", "-trimpath", "-race=true",
				"-msan=true", "-asan=true",
			},
			test: nil,
		},
		{
			name: "the same set with double dashes",
			flags: []string{
				"--tags=a", "--mod=mod", "--modfile=go.mod", "--overlay=o.json",
				"--pgo=off", "--compiler=gc", "--trimpath", "--race=true",
				"--msan=true", "--asan=true",
			},
			build: []string{
				"-tags=a", "-mod=mod", "-modfile=go.mod", "-overlay=o.json",
				"-pgo=off", "-compiler=gc", "-trimpath", "-race=true",
				"-msan=true", "-asan=true",
			},
			test: nil,
		},
		{
			name:  "the =true spelling of a test flag keeps its value",
			flags: []string{"-short=true", "-v=false", "--short=true"},
			test:  []string{"-test.short=true", "-test.v=false", "-test.short=true"},
		},
		{
			// The value of a test flag is a value, not a flag: `-run -race` is
			// a regex, and reading it as a build flag built the wrong binary.
			name:  "a test flag consumes its value",
			flags: []string{"-run", "-race", "-short"},
			build: nil,
			test:  []string{"-test.short"},
		},
		{
			name:  "a build flag's value is forwarded as the caller wrote it",
			flags: []string{"-tags", "--foo", "-v"},
			build: []string{"-tags", "--foo"},
			test:  []string{"-test.v"},
		},
		{
			name:  "-count in its two-argument form",
			flags: []string{"-count", "3", "-race"},
			build: []string{"-race"},
			test:  []string{"-test.count=3"},
		},
		{
			name:  "a value that looks like a build flag is still a value",
			flags: []string{"-timeout", "-trimpath", "-count=1"},
			build: nil,
			test:  []string{"-test.count=1"},
		},
		{
			name:  "-p is the build's parallelism, in both spellings",
			flags: []string{"-p", "4", "--p=6", "-short"},
			build: []string{"-p", "4", "-p=6"},
			test:  []string{"-test.short"},
		},
		{
			name:  "nothing at all",
			flags: nil,
			build: nil,
			test:  nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseFlags(tc.flags)
			if err != nil {
				t.Fatalf("parseFlags(%v) = %v", tc.flags, err)
			}
			if !reflect.DeepEqual(parsed.build, tc.build) {
				t.Fatalf("build flags = %v, want %v", parsed.build, tc.build)
			}
			if !reflect.DeepEqual(parsed.test, tc.test) {
				t.Fatalf("test flags = %v, want %v", parsed.test, tc.test)
			}
		})
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
