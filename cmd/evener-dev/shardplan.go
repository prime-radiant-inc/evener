package dev

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// testCost is one top-level test and its surveyed wall-clock cost.
type testCost struct {
	name string
	cost float64
}

var surveyLine = regexp.MustCompile(`^--- (?:PASS|SKIP): (\S+) \(([0-9.]+)s\)`)

// parseSurvey extracts top-level test costs from `go test -v` output: one
// entry per "--- PASS:"/"--- SKIP:" line whose name has no subtest slash,
// in file order, later duplicates updating the earlier entry in place.
func parseSurvey(output string) []testCost {
	var costs []testCost
	index := map[string]int{}
	for line := range strings.SplitSeq(output, "\n") {
		m := surveyLine.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) < 3 || strings.Contains(m[1], "/") {
			continue
		}
		var cost float64
		if _, err := fmt.Sscanf(m[2], "%f", &cost); err != nil {
			continue
		}
		if at, seen := index[m[1]]; seen {
			costs[at].cost = cost
			continue
		}
		index[m[1]] = len(costs)
		costs = append(costs, testCost{name: m[1], cost: cost})
	}
	return costs
}

// equalWeights builds a uniform-cost test set from `-test.list` output,
// keeping only ^(Test|Example) names: correct partitioning, no balance.
func equalWeights(listOutput string) []testCost {
	var costs []testCost
	seen := map[string]bool{}
	for line := range strings.SplitSeq(listOutput, "\n") {
		name := strings.TrimSpace(line)
		if !strings.HasPrefix(name, "Test") && !strings.HasPrefix(name, "Example") {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		costs = append(costs, testCost{name: name, cost: 1.0})
	}
	return costs
}

// packShards partitions costs into n cost-balanced bins, longest processing
// time first, and proves the partition is a bijection over the test set
// before anything runs: a filter bug that dropped tests would otherwise
// present as a faster, still-green suite.
func packShards(costs []testCost, n int) (bins [][]string, loads []float64, err error) {
	if len(costs) == 0 {
		return nil, nil, errors.New("found no tests to shard")
	}
	byCost := make([]testCost, len(costs))
	copy(byCost, costs)
	sort.SliceStable(byCost, func(i, j int) bool { return byCost[i].cost > byCost[j].cost })

	bins = make([][]string, n)
	loads = make([]float64, n)
	for _, tc := range byCost {
		at := 0
		for i, load := range loads {
			if load < loads[at] {
				at = i
			}
		}
		bins[at] = append(bins[at], tc.name)
		loads[at] += tc.cost
	}

	placed := 0
	want := map[string]bool{}
	for _, tc := range costs {
		want[tc.name] = true
	}
	seen := map[string]bool{}
	for _, bin := range bins {
		for _, name := range bin {
			if seen[name] || !want[name] {
				return nil, nil, errors.New("partition is not a bijection over the test set")
			}
			seen[name] = true
			placed++
		}
	}
	if placed != len(want) {
		return nil, nil, errors.New("partition is not a bijection over the test set")
	}
	nonEmpty := 0
	for _, bin := range bins {
		if len(bin) > 0 {
			nonEmpty++
		}
	}
	if nonEmpty != n {
		return nil, nil, fmt.Errorf("asked for %d shards but only %d are non-empty; lower AGENT_SHARD_COUNT", n, nonEmpty)
	}
	return bins, loads, nil
}

// nameRegex anchors an alternation over names so no name can prefix-match a
// different test.
func nameRegex(names []string) string {
	escaped := make([]string, len(names))
	for i, name := range names {
		escaped[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

// goFlag is a caller's flag in one spelling. Go's flag package reads --tags and
// -tags as the same flag, so a reader that knows only one of them silently drops
// the other; everything here compares the normalised form.
func goFlag(f string) string {
	if strings.HasPrefix(f, "--") && len(f) > 2 {
		return f[1:]
	}
	return f
}

// The four tables below are the flags `go help build` and `go help testflag`
// document on the pinned toolchain, split by where each one has to go and
// whether its value is the next argument. Together they are exhaustive: a flag
// in none of them is refused by name rather than dropped, because dropping one
// builds or runs something the caller did not ask for -- `-cover` and
// `-toolexec` used to vanish this way, leaving a binary that was not the one
// requested and no line of output saying so.

// buildValueFlags take their value as the next argument when it is not written
// inline. They change what gets compiled, so they belong to `go test -c`.
var buildValueFlags = map[string]bool{
	// -p is the build's own parallelism, which `go help build` documents with
	// the rest of them; it belongs to `go test -c`, not to the shards.
	"-p": true, "-asmflags": true, "-buildmode": true, "-compiler": true,
	"-covermode": true, "-coverpkg": true, "-gccgoflags": true,
	"-gcflags": true, "-installsuffix": true, "-ldflags": true, "-mod": true,
	"-modfile": true, "-overlay": true, "-pgo": true, "-pkgdir": true,
	"-tags": true, "-toolexec": true,
}

// buildBareFlags stand alone. The booleans among them (-race=false,
// -buildvcs=false) carry their value inline, which is forwarded as written.
var buildBareFlags = map[string]bool{
	"-a": true, "-asan": true, "-buildvcs": true, "-cover": true,
	"-linkshared": true, "-modcacherw": true, "-msan": true, "-n": true,
	"-race": true, "-trimpath": true, "-work": true, "-x": true,
}

// testValueFlags take their value as the next argument when it is not written
// inline. Consuming the pair is what keeps `-run -race` from putting the
// caller's regex into the build. Apart from -count, the runner sets these for
// itself -- it chooses each shard's tests, its own -test.count and
// -test.parallel -- so a caller's value is consumed and goes no further.
var testValueFlags = map[string]bool{
	"-bench": true, "-benchtime": true, "-blockprofile": true,
	"-blockprofilerate": true, "-count": true, "-coverprofile": true,
	"-cpu": true, "-cpuprofile": true, "-exec": true, "-fuzz": true,
	"-fuzzminimizetime": true, "-fuzztime": true, "-gocoverdir": true,
	"-list": true, "-memprofile": true, "-memprofilerate": true,
	"-mutexprofile": true, "-mutexprofilefraction": true, "-o": true,
	"-outputdir": true, "-parallel": true, "-run": true, "-shuffle": true,
	"-skip": true, "-timeout": true, "-trace": true, "-vet": true,
}

// testBareFlags stand alone on the test side. -short and -v are the two the
// shards are given; the rest are the caller's own affair and stop here.
var testBareFlags = map[string]bool{
	"-artifacts": true, "-benchmem": true, "-c": true, "-failfast": true,
	"-fullpath": true, "-json": true, "-short": true, "-v": true,
}

// parsedFlags is what one walk of a caller's flags says about them: which go to
// the build, which go to the test binary, and whether short mode was asked for.
type parsedFlags struct {
	build []string
	test  []string
	short bool
}

// parseFlags walks a caller's `go test` flags once and classifies them.
//
// Once, and every reader takes its answer from here. Four readers used to walk
// the arguments themselves and only some of them knew a value from a flag, so
// `-run -race` put the caller's regex into the build and `-run -short` was a
// short run. A value is consumed with its flag before anything is classified,
// and a value taken from the next argument is forwarded exactly as the caller
// wrote it, since a value is data and normalising it would rewrite a path or a
// regex.
//
// Short mode is the last occurrence, parsed with strconv.ParseBool, which is
// what go's flag package does with a boolean. A value ParseBool refuses is one
// `go test` would refuse too, and it is refused here, at parse time: read as
// short it would survey the whole suite, build the binary, and only then kill
// every shard in flag parsing.
//
// A flag in none of the tables is refused by name. Dropping it silently is how
// `-cover` and `-toolexec` used to produce a binary that was not the one the
// caller asked for.
//
// -C is refused rather than forwarded: it changes directory before the command
// runs, and everything here is built and tested from the module's own directory.
func parseFlags(flags []string) (parsedFlags, error) {
	var out parsedFlags
	for i := 0; i < len(flags); i++ {
		f := goFlag(flags[i])
		name := f
		inline := ""
		hasInline := false
		if j := strings.IndexByte(f, '='); j > 0 {
			name, inline, hasInline = f[:j], f[j+1:], true
		}
		if name == "-C" {
			return parsedFlags{}, errors.New("-C is not supported here: every module is built and tested from its own directory, and a -C would move both somewhere this runner does not expect")
		}
		value := ""
		hasValue := hasInline
		if hasInline {
			value = inline
		} else if buildValueFlags[name] || testValueFlags[name] {
			if i+1 >= len(flags) {
				return parsedFlags{}, fmt.Errorf("%s was given with nothing after it, and its value decides what runs", name)
			}
			i++
			value = flags[i]
			hasValue = true
		}
		switch {
		case buildValueFlags[name]:
			if hasInline {
				out.build = append(out.build, f)
				continue
			}
			out.build = append(out.build, name, value)
		case buildBareFlags[name]:
			out.build = append(out.build, f)
		case name == "-short":
			out.short = true
			if hasInline {
				parsed, err := strconv.ParseBool(inline)
				if err != nil {
					return parsedFlags{}, fmt.Errorf("-short=%s is not a boolean, and every shard binary would refuse it after the survey had already run", inline)
				}
				out.short = parsed
				out.test = append(out.test, "-test.short="+inline)
				continue
			}
			out.test = append(out.test, "-test.short")
		case name == "-v":
			if hasInline {
				out.test = append(out.test, "-test.v="+inline)
				continue
			}
			out.test = append(out.test, "-test.v")
		case name == "-count":
			if hasValue {
				out.test = append(out.test, "-test.count="+value)
			}
		case testValueFlags[name] || testBareFlags[name]:
			// Known, and the runner's own to decide: consumed above and
			// deliberately not passed on.
		case !strings.HasPrefix(name, "-"):
			return parsedFlags{}, fmt.Errorf("%q is not a flag: this runner chooses the packages it builds and runs, and takes only `go test` flags", flags[i])
		default:
			return parsedFlags{}, fmt.Errorf("%s is not a flag `go help build` or `go help testflag` documents; refusing it rather than dropping it, which would build or run something other than what was asked for", name)
		}
	}
	return out, nil
}

// testSetKey is the survey cache key: the identity of the sorted test list.
// Add, rename, or remove a test and the key changes; otherwise every run
// reuses the cached survey and pays nothing.
func testSetKey(listOutput string) string {
	lines := strings.Split(strings.TrimRight(listOutput, "\n"), "\n")
	sort.Strings(lines)
	payload := strings.Join(lines, "\n") + "\n"
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:16]
}
