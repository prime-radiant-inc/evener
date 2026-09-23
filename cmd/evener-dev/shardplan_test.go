package dev

import (
	"fmt"
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
	bins, loads, err := packShards(costs, 2, "AGENT")
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

// TestPackShardsSpreadsTestsTheSurveyCalledFree is the long-pole regression:
// -test.v reports 10ms steps, so most tests survey as 0.00s, and greedy packing
// never raised the least-loaded shard's load for them — every one of them
// landed in the same shard (1500 of evener-hub's 2129, 3561 of agent's). Each
// test is charged at least half a reporting step, so they spread.
func TestPackShardsSpreadsTestsTheSurveyCalledFree(t *testing.T) {
	costs := []testCost{{"slow", 1}}
	for i := range 400 {
		costs = append(costs, testCost{fmt.Sprintf("free%d", i), 0})
	}
	bins, _, err := packShards(costs, 4, "AGENT")
	if err != nil {
		t.Fatalf("packShards: %v", err)
	}
	for i, bin := range bins {
		if len(bin) > 200 {
			t.Fatalf("shard %d holds %d of 401 tests; zero-cost tests piled into one shard: %v", i, len(bin), lens(bins))
		}
	}
}

func lens(bins [][]string) []int {
	out := make([]int, len(bins))
	for i, bin := range bins {
		out[i] = len(bin)
	}
	return out
}

func TestPackShardsTiesKeepInputOrder(t *testing.T) {
	costs := []testCost{{"first", 1}, {"second", 1}, {"third", 1}}
	bins, _, err := packShards(costs, 3, "AGENT")
	if err != nil {
		t.Fatalf("packShards: %v", err)
	}
	want := [][]string{{"first"}, {"second"}, {"third"}}
	if !reflect.DeepEqual(bins, want) {
		t.Fatalf("packShards = %v, want %v", bins, want)
	}
}

func TestPackShardsRefusesEmptyTestSet(t *testing.T) {
	_, _, err := packShards(nil, 4, "AGENT")
	if err == nil || !strings.Contains(err.Error(), "found no tests to shard") {
		t.Fatalf("packShards(nil) err = %v, want found-no-tests refusal", err)
	}
}

func TestPackShardsRefusesEmptyBins(t *testing.T) {
	costs := []testCost{{"a", 1}, {"b", 1}, {"c", 1}}
	_, _, err := packShards(costs, 5, "AGENT")
	want := "asked for 5 shards but only 3 are non-empty; lower AGENT_SHARD_COUNT"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("packShards err = %v, want %q", err, want)
	}
}

func TestPackShardsProvesBijection(t *testing.T) {
	costs := []testCost{{"dup", 1}, {"dup", 2}}
	_, _, err := packShards(costs, 1, "AGENT")
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
		// A flag's value is a value, not a flag.
		{flags: []string{"-tags", "-short"}, want: false},
		{flags: nil, want: false},
	} {
		parsed, err := parseFlags(tc.flags, "AGENT")
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
		// A value go itself would refuse: refused here, not after the survey.
		{"-short=yes"},
		// Documented by neither `go help build` nor `go help testflag`.
		{"-nonsense"},
		{"-nonsense=1"},
		{"--nonsense"},
		// Not a flag at all: this runner picks its own packages.
		{"./..."},
		// A boolean the shards would refuse after the survey had run.
		{"-v=maybe"},
		{"-count=lots"},
		// Documented `go test` flags this runner cannot honour, because it
		// builds one binary, runs it as shards it selects itself, and writes
		// its own logs.
		{"-run", "TestFoo"},
		{"-skip", "TestFoo"},
		{"-parallel", "4"},
		{"-json"},
		{"-bench", "."},
		{"-benchmem"},
		{"-fuzz", "FuzzFoo"},
		{"-fuzztime", "10s"},
		{"-cover"},
		{"-covermode", "atomic"},
		{"-coverpkg", "./..."},
		{"-coverprofile", "c.out"},
		{"-cpuprofile", "cpu.out"},
		{"-exec", "wrap"},
		{"-o", "bin"},
		{"-c"},
		{"-args", "whatever"},
		// -n prints the build instead of running it: no binary, no shards.
		{"-n"},
	} {
		if _, err := parseFlags(flags, "AGENT"); err == nil {
			t.Fatalf("parseFlags(%v) = no error, want one", flags)
		}
	}
	// A -C that is another flag's value is a value.
	parsed, err := parseFlags([]string{"-tags", "-C", "-short"}, "AGENT")
	if err != nil {
		t.Fatalf("parseFlags(-tags -C -short) = %v", err)
	}
	if !parsed.short || !reflect.DeepEqual(parsed.build, []string{"-tags", "-C"}) {
		t.Fatalf("parseFlags(-tags -C -short) = %+v, want short with -tags taking -C as its value", parsed)
	}
}

func TestParseFlagsSendsBuildFlagsToTheBuild(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		build []string
		test  []string
		// err is the substring a refusal must carry; cases that set it expect
		// no classification at all.
		err string
	}{
		{
			name:  "the build's flags to the build, the binary's to the binary",
			flags: []string{"-short", "-count=2", "-v", "-race", "-timeout=30s"},
			build: []string{"-race"},
			test:  []string{"-test.short", "-test.count=2", "-test.v", "-test.timeout=30s"},
		},
		{
			name:  "the ones the shards can honour that used to be dropped",
			flags: []string{"-timeout", "5m", "-shuffle", "on", "-failfast", "-fullpath"},
			build: nil,
			test:  []string{"-test.timeout=5m", "-test.shuffle=on", "-test.failfast", "-test.fullpath"},
		},
		{
			// The value is consumed as a value, and then read: a caller who
			// wrote -timeout and then forgot its value gets told here, not
			// after the survey.
			name:  "a forwarded flag's value is checked, not guessed at",
			flags: []string{"-timeout", "-race", "-short"},
			err:   "-timeout=-race is not a duration",
		},
		{
			name:  "the values the shards would take",
			flags: []string{"-timeout", "20m", "-shuffle", "42", "-count", "2"},
			test:  []string{"-test.timeout=20m", "-test.shuffle=42", "-test.count=2"},
		},
		{
			name:  "-count is a count, not any number",
			flags: []string{"-count=-1"},
			err:   "-count=-1 is not a count",
		},
		{
			name:  "nor a signed one",
			flags: []string{"-count=+3"},
			err:   "-count=+3 is not a count",
		},
		{
			// flag.Uint parses in base 0, so these are counts wherever the
			// caller writes them.
			name:  "the bases and separators go's own flag package takes",
			flags: []string{"-count=0x2"},
			test:  []string{"-test.count=0x2"},
		},
		{
			name:  "binary",
			flags: []string{"-count=0b10"},
			test:  []string{"-test.count=0b10"},
		},
		{
			name:  "and an underscore separator",
			flags: []string{"-count=1_0"},
			test:  []string{"-test.count=1_0"},
		},
		{
			name:  "a seed or off or on, and nothing else, shuffles",
			flags: []string{"-shuffle=maybe"},
			err:   "-shuffle=maybe is not off, on, or a seed",
		},
		{
			name:  "the build flags that used to be dropped without a word",
			flags: []string{"-toolexec", "wrap", "-buildvcs=false", "-pkgdir", "/tmp/pkg"},
			build: []string{"-toolexec", "wrap", "-buildvcs=false", "-pkgdir", "/tmp/pkg"},
			test:  nil,
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
			name: "every build flag, inline value form",
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
			// Canonicalised, because the test binary's -v takes true, false or
			// test2json and nothing else, while go's flag package reads -v=1,
			// -v=t and -v=TRUE as true.
			name:  "boolean spellings become the two the shards accept",
			flags: []string{"-short=true", "-v=false", "--short=t", "-failfast=0"},
			test:  []string{"-test.short", "-test.v=false", "-test.short", "-test.failfast=false"},
		},
		{
			name:  "-v=t is -test.v, not a value the binary would refuse",
			flags: []string{"-v=t"},
			test:  []string{"-test.v"},
		},
		{
			// -v is documented by `go help build` as well, and belongs to the
			// shards: they are what run the tests.
			name:  "-v goes to the shards, never to the build",
			flags: []string{"-v", "-race"},
			build: []string{"-race"},
			test:  []string{"-test.v"},
		},
		{
			name:  "-v=test2json is a real value this runner cannot use",
			flags: []string{"-v=test2json"},
			err:   "cannot read JSON",
		},
		{
			// `go test -test.short` is `go test -short`, and so is this.
			name:  "the test binary's own spelling on the command line",
			flags: []string{"-test.short", "-test.count=2", "-test.v=false"},
			test:  []string{"-test.short", "-test.count=2", "-test.v=false"},
		},
		{
			name:  "and it is still refused when the runner cannot honour it",
			flags: []string{"-test.run", "TestFoo"},
			err:   "-run is not supported",
		},
		{
			// -test.race is not -race: the binary has no such flag, and
			// walking the prefix off would hand the build a sanitiser nobody
			// asked for.
			name:  "a -test. name the binary does not have is not a build flag",
			flags: []string{"-test.race"},
			err:   "-test.race",
		},
		{
			name:  "-vet configures the vet that runs during the build",
			flags: []string{"-vet=off", "-short"},
			build: []string{"-vet=off"},
			test:  []string{"-test.short"},
		},
		{
			// The value of a refused flag is still consumed with it, so it
			// cannot be read as a flag of its own; the refusal names -skip,
			// not the -race that follows it.
			name:  "a refused flag consumes its value before anything is read",
			flags: []string{"-skip", "-race", "-short"},
			err:   "-skip is not supported",
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
			name:  "a build flag's value that looks like a flag is still a value",
			flags: []string{"-tags", "-trimpath", "-count=1"},
			build: []string{"-tags", "-trimpath"},
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
			parsed, err := parseFlags(tc.flags, "AGENT")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("parseFlags(%v) = %v, want a refusal naming %q", tc.flags, err, tc.err)
				}
				return
			}
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
	a := testSetKey("TestB\nTestA\n", parsedFlags{}, "", "")
	b := testSetKey("TestA\nTestB\n", parsedFlags{}, "", "")
	if a != b {
		t.Fatalf("testSetKey is order sensitive: %q vs %q", a, b)
	}
	if c := testSetKey("TestA\nTestC\n", parsedFlags{}, "", ""); c == a {
		t.Fatalf("testSetKey did not change with the test set: %q", c)
	}
	// The survey measures costs, and the build is what the costs are of.
	race := testSetKey("TestA\nTestB\n", parsedFlags{build: []string{"-race"}}, "", "")
	if race == a {
		t.Fatalf("a -race survey shares the plain build's key: %q", race)
	}
	short := testSetKey("TestA\nTestB\n", parsedFlags{short: true}, "", "")
	if short == a || short == race {
		t.Fatalf("a short-mode survey shares another key: %q", short)
	}
	// GOFLAGS reaches the build without passing through any argument list.
	goflags := testSetKey("TestA\nTestB\n", parsedFlags{}, "-race", "")
	if goflags == a || goflags == race || goflags == short {
		t.Fatalf("a survey under GOFLAGS shares another key: %q", goflags)
	}
	// A survey taken without the skip measured the skipped test as well, and
	// its cost would land in a shard that will not run it.
	skipped := testSetKey("TestA\nTestB\n", parsedFlags{}, "", "^TestA$")
	if skipped == a || skipped == goflags {
		t.Fatalf("a survey under a skip shares another key: %q", skipped)
	}
}

func TestCheckGoflagsRefusesWhatTheShardsWouldNeverSee(t *testing.T) {
	for _, tc := range []struct {
		name    string
		goflags string
		err     string
		// advice is the sentence the refusal has to carry: where the flag does
		// work, or -- for a name no binary has -- what is wrong with it.
		advice string
	}{
		{name: "nothing set", goflags: ""},
		{name: "build flags flow to the compile", goflags: "-mod=mod -trimpath -tags=integration"},
		{name: "a test flag reaches neither half", goflags: "-mod=mod -short", err: "-short"},
		{name: "and in either spelling", goflags: "--count=2", err: "-count"},
		{name: "including one this runner refuses outright", goflags: "-run=TestFoo", err: "-run", advice: "selects each shard's tests"},
		// `go test` takes the binary's own spelling too, so it is normalised
		// away first and refused by the same rule under its own name.
		{name: "the test binary's spelling, bare", goflags: "-test.v", err: "-v"},
		{name: "with a value", goflags: "-mod=mod -test.timeout=1s", err: "-timeout"},
		{name: "and one the command line would have taken", goflags: "-test.count=5", err: "-count"},
		// A two-token value is a value: the flag before it decides, and
		// neither of these is a test-side flag.
		{name: "a build flag whose value looks like one", goflags: "-ldflags -short"},
		{name: "and the same for tags", goflags: "-tags -short"},
		{name: "but the flag itself is still refused", goflags: "-short", err: "-short"},
		// Through GOFLAGS the same two names get the same two answers.
		{name: "the binary's spelling of a flag it has", goflags: "-test.short", err: "-short"},
		{name: "and of one it does not", goflags: "-test.race", err: "-test.race", advice: "not a flag the test binary has"},
		// The toolchain reads GOFLAGS with quoting, so a quoted flag reaches
		// the build and has to reach these tables too.
		{name: "a quoted test flag is still a test flag", goflags: `-mod=mod "-short"`, err: "-short"},
		{name: "single quotes likewise", goflags: `'-count=2'`, err: "-count"},
		// A quote that does not open the field does not quote: go itself
		// answers `non-flag "b\""` for this one, and what matters here is
		// that -tags is still seen as the build flag it is.
		{name: "a quote inside a field is part of it", goflags: `-tags="a b"`},
		{name: "an unterminated quote still classifies", goflags: `"-short`, err: "-short"},
		// -C moves the build out from under a runner already standing in the
		// module's directory, whichever route it arrives by.
		{name: "-C through GOFLAGS", goflags: "-C /tmp", err: "-C is not supported here", advice: "built and tested from its own directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkGoflags(tc.goflags, "AGENT")
			if tc.err == "" {
				if err != nil {
					t.Fatalf("checkGoflags(%q) = %v, want nothing", tc.goflags, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("checkGoflags(%q) = %v, want a refusal naming %s", tc.goflags, err, tc.err)
			}
			advice := tc.advice
			if advice == "" {
				advice = "command line"
			}
			if !strings.Contains(err.Error(), advice) {
				t.Fatalf("checkGoflags(%q) = %v, want it to say %q", tc.goflags, err, advice)
			}
		})
	}
}
