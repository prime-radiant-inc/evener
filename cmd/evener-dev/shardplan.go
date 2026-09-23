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
	"time"
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

// minTestCost is the least a test is charged when packing. `go test -v`
// reports durations in 10ms steps, so most tests survey as 0.00s; charged at
// zero they never raise the least-loaded shard's load, and greedy packing
// piles every one of them into that shard. Half a step is what a "0.00s"
// test costs on average.
const minTestCost = 0.005

// packShards partitions costs into n cost-balanced bins, longest processing
// time first, and proves the partition is a bijection over the test set
// before anything runs: a filter bug that dropped tests would otherwise
// present as a faster, still-green suite.
func packShards(costs []testCost, n int, envPrefix string) (bins [][]string, loads []float64, err error) {
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
		loads[at] += max(tc.cost, minTestCost)
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
		return nil, nil, fmt.Errorf("asked for %d shards but only %d are non-empty; lower %s_SHARD_COUNT", n, nonEmpty, envPrefix)
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
		f = f[1:]
	}
	// `go test` takes the test binary's own spelling too -- `go test
	// -test.short` is `go test -short` -- so the prefix is dropped here,
	// before anything classifies the flag, and everything downstream sees one
	// spelling whether the flag came from the command line or from GOFLAGS.
	//
	// Only for names the test binary actually has, though: `-test.race` is not
	// `-race`. Stripping the prefix from anything would walk a test-side
	// spelling into the build tables, which is how a caller would get a
	// sanitiser it never asked for. An unknown `-test.something` keeps its
	// name and is refused as the unknown flag it is.
	if after, ok := strings.CutPrefix(f, "-test."); ok && after != "" {
		if stripped := "-" + after; isTestSideFlag(stripped) {
			f = stripped
		}
	}
	return f
}

// isTestSideFlag reports whether a name is one the test binary takes, in
// either direction: forwarded to the shards, or refused because this runner
// sets it itself.
func isTestSideFlag(name string) bool {
	if j := strings.IndexByte(name, '='); j > 0 {
		name = name[:j]
	}
	return testForwardValueFlags[name] || testForwardBareFlags[name] ||
		testRefusedValueFlags[name] || testRefusedBareFlags[name]
}

// The tables below are the flags `go help build` and `go help testflag`
// document on the pinned toolchain, split by where each one has to go and
// whether its value is the next argument. Together they are exhaustive, and a
// flag in none of them is refused by name: dropping one builds or runs
// something the caller did not ask for.
//
// Three destinations, and every flag has exactly one:
//
//   - the build (`go test -c`): -tags, -mod, -race and the rest of the
//     compile-affecting set, which decide what binary the shards run;
//   - the shard invocation, as -test.*: -short, -v, -count, -timeout,
//     -shuffle and -failfast, each of which the compiled binary honours
//     unchanged;
//   - refused: everything the runner cannot honour, because it builds one
//     binary and runs it as several shards. It chooses each shard's tests, so
//     -run and -skip are not its caller's to set (AGENT_SHARD_SKIP is);
//     it sets -test.parallel from AGENT_SHARD_PARALLEL; it writes its own
//     logs, so -json, the profile flags and the coverage flags would be
//     written several times over the same paths; and a benchmark or fuzz run
//     is not what this runner is for.

// buildValueFlags take their value as the next argument when it is not written
// inline. They change what gets compiled, so they belong to `go test -c`.
var buildValueFlags = map[string]bool{
	// -p is the build's own parallelism, which `go help build` documents with
	// the rest of them; it belongs to `go test -c`, not to the shards.
	"-p": true, "-asmflags": true, "-buildmode": true, "-compiler": true,
	"-gccgoflags": true, "-gcflags": true, "-installsuffix": true,
	"-ldflags": true, "-mod": true, "-modfile": true, "-overlay": true,
	// -C is here so that its value is consumed with it; both readers refuse
	// the flag itself, in errUnsupportedC.
	"-C":   true,
	"-pgo": true, "-pkgdir": true, "-tags": true, "-toolexec": true,
	// -vet chooses the vet checks `go test -c` runs over the package before it
	// compiles it, so it belongs to the build and not to the shards.
	"-vet": true,
}

// buildBareFlags stand alone. The booleans among them (-race=false,
// -buildvcs=false) carry their value inline, which is forwarded as written.
var buildBareFlags = map[string]bool{
	"-a": true, "-asan": true, "-buildvcs": true, "-linkshared": true,
	"-modcacherw": true, "-msan": true, "-race": true, "-trimpath": true,
	"-work": true, "-x": true,
}

// testForwardValueFlags are handed to every shard as -test.<name>=<value>:
// the binary honours each one as written, and one shard honouring it means
// all of them do. -timeout goes to the survey as well as to every shard,
// because `go test -timeout` bounds one full run of the package and each of
// those is such a run -- the survey most of all, being the whole suite in one
// binary.
var testForwardValueFlags = map[string]bool{
	"-count": true, "-timeout": true, "-shuffle": true,
}

// testForwardBareFlags are the same, without a value of their own.
var testForwardBareFlags = map[string]bool{
	// -fullpath changes how the binary prints file names in failures, which
	// is the same thing in one shard as in all of them.
	"-failfast": true, "-fullpath": true, "-short": true, "-v": true,
}

// testRefusedValueFlags take a value and are refused: consuming the pair first
// is what keeps `-run -race` from reading the caller's regex as a build flag.
var testRefusedValueFlags = map[string]bool{
	"-args": true, "-bench": true, "-benchtime": true, "-blockprofile": true,
	"-blockprofilerate": true, "-covermode": true, "-coverpkg": true,
	"-coverprofile": true, "-cpu": true, "-cpuprofile": true, "-exec": true,
	"-fuzz": true, "-fuzzminimizetime": true, "-fuzztime": true,
	"-gocoverdir": true, "-list": true, "-memprofile": true,
	"-memprofilerate": true, "-mutexprofile": true,
	"-mutexprofilefraction": true, "-o": true, "-outputdir": true,
	"-parallel": true, "-run": true, "-skip": true, "-trace": true,
}

// testRefusedBareFlags stand alone and are refused for the same reasons.
var testRefusedBareFlags = map[string]bool{
	"-artifacts": true, "-benchmem": true, "-c": true, "-cover": true,
	"-json": true,
	// -n prints the commands instead of running them, so the build it
	// describes never produces the binary the shards are handed.
	"-n": true,
}

// refusalReason is the sentence that goes with the flag's name, for the few
// where the runner has a direct answer to "then how do I do this?". {PREFIX}
// is the runner's environment prefix (AGENT, HUB).
var refusalReason = map[string]string{
	"-run":      "this runner selects each shard's tests from the survey",
	"-skip":     "use {PREFIX}_SHARD_SKIP, which the survey and the shards both read",
	"-parallel": "use {PREFIX}_SHARD_PARALLEL; the runner sets -test.parallel per shard",
	"-args":     "the shards are launched by this runner with the flags it needs, so there is no argument list to append to",
	"-c":        "this runner already compiles the binary itself",
	"-o":        "this runner names the binary it compiles",
	"-n":        "it prints the build instead of running it, so no binary is produced",
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

// flagToken is one of a caller's flags after normalisation, with its value
// already taken from the next argument when that is where it lives.
type flagToken struct {
	// whole is the flag as it will be forwarded: normalised, with any inline
	// value still attached.
	whole string
	// name is the flag without its value, and value is the value, from either
	// spelling.
	name, value string
	// inline says the value was written with an =, and hasValue that there is
	// one at all.
	inline, hasValue bool
}

// walkFlags walks a `go test` argument list once, normalising each flag and
// consuming the value of any flag whose value is the next argument, and hands
// each result to visit. Consuming the pair here is what keeps `-run -race` and
// `-ldflags -short` from being read as two flags -- the second word is a
// value, and only the tables know which flags take one.
//
// Both routes into this package use it: the caller's own flags, and the
// entries in GOFLAGS.
func walkFlags(flags []string, visit func(flagToken) error) error {
	for i := 0; i < len(flags); i++ {
		tok := flagToken{whole: goFlag(flags[i])}
		tok.name = tok.whole
		if j := strings.IndexByte(tok.whole, '='); j > 0 {
			tok.name, tok.value, tok.inline, tok.hasValue = tok.whole[:j], tok.whole[j+1:], true, true
		}
		if !tok.inline && (buildValueFlags[tok.name] || testForwardValueFlags[tok.name] || testRefusedValueFlags[tok.name]) {
			if i+1 >= len(flags) {
				return fmt.Errorf("%s was given with nothing after it, and its value decides what runs", tok.name)
			}
			i++
			tok.value, tok.hasValue = flags[i], true
		}
		if err := visit(tok); err != nil {
			return err
		}
	}
	return nil
}

func parseFlags(flags []string, envPrefix string) (parsedFlags, error) {
	var out parsedFlags
	err := walkFlags(flags, func(tok flagToken) error {
		f, name, inline, value, hasValue := tok.whole, tok.name, tok.value, tok.value, tok.hasValue
		if !tok.inline {
			inline = ""
		}
		hasInline := tok.inline
		if name == "-C" {
			return errUnsupportedC()
		}
		// The test side is classified first, and that order is the rule: a
		// name `go help testflag` documents is this runner's test flag even
		// when `go help build` documents it too. -v is the case today -- both
		// pages list it, and it belongs to the shards, which are what run the
		// tests. A build-first order would send it to `go test -c` and the
		// shards would never be verbose.
		switch {
		case name == "-short":
			parsed, err := boolFlagValue(name, inline, hasInline)
			if err != nil {
				return err
			}
			out.short = parsed
			out.test = append(out.test, testBoolFlag(name, parsed))
		case testForwardBareFlags[name]:
			parsed, err := boolFlagValue(name, inline, hasInline)
			if err != nil {
				return err
			}
			out.test = append(out.test, testBoolFlag(name, parsed))
		case testForwardValueFlags[name]:
			if hasValue {
				if err := checkForwardedValue(name, value); err != nil {
					return err
				}
				out.test = append(out.test, "-test."+strings.TrimPrefix(name, "-")+"="+value)
			}
		case testRefusedValueFlags[name] || testRefusedBareFlags[name]:
			return refusalFor(name, envPrefix)
		case buildValueFlags[name]:
			if hasInline {
				out.build = append(out.build, f)
				return nil
			}
			out.build = append(out.build, name, value)
		case buildBareFlags[name]:
			out.build = append(out.build, f)
		case !strings.HasPrefix(name, "-"):
			return fmt.Errorf("%q is not a flag: this runner chooses the packages it builds and runs, and takes only `go test` flags", f)
		default:
			return fmt.Errorf("%s is not a flag `go help build` or `go help testflag` documents; refusing it rather than dropping it, which would build or run something other than what was asked for", name)
		}
		return nil
	})
	if err != nil {
		return parsedFlags{}, err
	}
	return out, nil
}

// errUnsupportedC is the one refusal both readers give for -C, whether it
// arrives on the command line or in GOFLAGS: the toolchain applies GOFLAGS to
// `go test -c` too, so a -C there moves the build out from under a runner that
// is already standing in the module's own directory.
func errUnsupportedC() error {
	return errors.New("-C is not supported here: every module is built and tested from its own directory, and a -C would move both somewhere this runner does not expect")
}

// refusalFor is the one refusal a flag this runner cannot honour gets, on
// whichever route it arrived by: its own reason where there is one to give,
// and otherwise the shape of the runner that makes it meaningless here.
func refusalFor(name, envPrefix string) error {
	if reason, ok := refusalReason[name]; ok {
		return fmt.Errorf("%s is not supported by this shard runner: %s", name, strings.ReplaceAll(reason, "{PREFIX}", envPrefix))
	}
	return fmt.Errorf("%s is not supported by this shard runner: it builds one binary, runs it as several shards, and writes its own logs, so this flag cannot mean here what it means to `go test`", name)
}

// boolFlagValue reads a boolean flag's inline value the way go's flag package
// does, and refuses at parse time what `go test` would refuse at the door: a
// value read as true here would survey the whole suite and build the binary
// before every shard died in flag parsing.
func boolFlagValue(name, inline string, hasInline bool) (bool, error) {
	if !hasInline {
		return true, nil
	}
	if name == "-v" && inline == "test2json" {
		// A real value of -test.v, and one this runner cannot take: the survey
		// reads "--- PASS:" lines to weigh each test, and test2json turns them
		// into JSON objects, so the costs it packs shards from would all be
		// zero.
		return false, errors.New("-v=test2json is not supported by this shard runner: the survey reads the test binary's plain output to weigh each test, and cannot read JSON")
	}
	parsed, err := strconv.ParseBool(inline)
	if err != nil {
		return false, fmt.Errorf("%s=%s is not a boolean, and every shard binary would refuse it after the survey had already run", name, inline)
	}
	return parsed, nil
}

// testBoolFlag is the shard binary's spelling of a boolean flag. The value is
// canonicalised rather than passed on as the caller wrote it: go's flag
// package reads -v=1, -v=t and -v=TRUE as true, but the test binary's own -v
// is not a plain boolean -- it takes true, false or test2json -- so those
// spellings reach it as an error after the survey, the build and the launch.
// true becomes the bare flag and false the explicit =false, which every
// boolean the shards are given accepts.
func testBoolFlag(name string, value bool) string {
	spelled := "-test." + strings.TrimPrefix(name, "-")
	if value {
		return spelled
	}
	return spelled + "=false"
}

// checkForwardedValue refuses at parse time what the shard binary would refuse
// at its own door, with the same reasoning as -short: the alternative is a
// survey, a build and a launch of every shard before the flag is read.
func checkForwardedValue(name, value string) error {
	switch name {
	case "-count":
		// -test.count is a flag.Uint, which is ParseUint in base 0 at the
		// platform's int size: 0x2, 0b10 and 1_0 are counts, -1 and +3 are not.
		if _, err := strconv.ParseUint(value, 0, strconv.IntSize); err != nil {
			return fmt.Errorf("-count=%s is not a count the shard binaries would take, and they would refuse it after the survey had already run", value)
		}
	case "-timeout":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("-timeout=%s is not a duration, and the survey would have run before any shard said so", value)
		}
	case "-shuffle":
		if value != "off" && value != "on" {
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return fmt.Errorf("-shuffle=%s is not off, on, or a seed, and the survey would have run before any shard said so", value)
			}
		}
	}
	return nil
}

// goflagsEntries splits GOFLAGS the way the toolchain does, which is
// cmd/internal/quoted.Split: fields separated by whitespace, and a quote only
// quotes when it opens the field. `GOFLAGS='"-short"'` is one entry, -short;
// `GOFLAGS='-tags="a b"'` is not one entry, and go itself rejects it --
// measured on go1.27.0, which answers `parsing $GOFLAGS: non-flag "b\""`.
// There is no escaping inside a quoted element, and an unterminated quote is
// an error there; here it is left as written, because refusing GOFLAGS this
// runner cannot parse is go's job and it will do it a moment later.
func goflagsEntries(goflags string) []string {
	var entries []string
	for len(goflags) > 0 {
		for len(goflags) > 0 && isGoflagsSpace(goflags[0]) {
			goflags = goflags[1:]
		}
		if len(goflags) == 0 {
			break
		}
		if quote := goflags[0]; quote == '\'' || quote == '"' {
			rest := goflags[1:]
			if end := strings.IndexByte(rest, quote); end >= 0 {
				entries = append(entries, rest[:end])
				goflags = rest[end+1:]
				continue
			}
			// Unterminated: go will refuse this GOFLAGS itself. Take what is
			// there so the flag is still classified rather than hidden.
			entries = append(entries, rest)
			break
		}
		end := 0
		for end < len(goflags) && !isGoflagsSpace(goflags[end]) {
			end++
		}
		entries = append(entries, goflags[:end])
		goflags = goflags[end:]
	}
	return entries
}

func isGoflagsSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// checkGoflags refuses a test-side flag that arrives through GOFLAGS. The
// toolchain applies GOFLAGS to `go test -c`, which compiles the binary; this
// runner then launches that binary itself, so a -short or -count sitting in
// GOFLAGS reaches neither the build nor the shards and silently does nothing.
// Build-side entries are another matter: those do reach the compile, and pass
// through untouched.
func checkGoflags(goflags, envPrefix string) error {
	return walkFlags(goflagsEntries(goflags), func(tok flagToken) error {
		if tok.name == "-C" {
			return errUnsupportedC()
		}
		// A -test. name that survived normalisation is not one the binary has,
		// so `go test` would hand it over and the binary would refuse it.
		if strings.HasPrefix(tok.name, "-test.") {
			return fmt.Errorf("GOFLAGS carries %s, which is not a flag the test binary has; `go test` would hand it over and every shard would refuse it", tok.name)
		}
		if testForwardValueFlags[tok.name] || testForwardBareFlags[tok.name] || tok.name == "-short" {
			return fmt.Errorf("GOFLAGS carries %s, which is a flag for the test binary, not for the build: this runner compiles with `go test -c` and launches the shards itself, so a flag there reaches neither. Pass it on the command line instead, where this runner does forward it", tok.name)
		}
		if testRefusedValueFlags[tok.name] || testRefusedBareFlags[tok.name] {
			// The same answer it gets on the command line: this runner cannot
			// honour it at all, so where it was written changes nothing.
			return fmt.Errorf("GOFLAGS carries %w", refusalFor(tok.name, envPrefix))
		}
		return nil
	})
}

// testSetKey is the survey cache key: the identity of the sorted test list
// together with the build the survey measured and whether it ran short. Add,
// rename, or remove a test and the key changes; otherwise every run reuses the
// cached survey and pays nothing.
//
// The key holds the test list, the classified build flags, short mode,
// GOFLAGS and the skip: the things that decide what was measured. The skip is
// there because a survey taken without it measured the skipped test too, and
// its cost then lands in a shard that will not run it. -timeout and the other
// forwarded test flags are not in it, because they bound or order a run
// without changing what it costs -- a survey is as valid under a 20m timeout
// as under 10m, and keying on one would throw away a measurement that still
// applies.
//
// The build belongs in the key because the survey is a cost measurement and
// the build decides the costs: a -race binary is several times slower than a
// plain one, and a survey taken from one would pack the other's shards from
// numbers that never applied. Short mode does the same on a smaller scale, by
// changing which tests run at all. GOFLAGS is in there for the same reason one
// step further out: it reaches the build without appearing in anyone's
// argument list, so `GOFLAGS=-race make test` would otherwise read a survey
// measured without it.
func testSetKey(listOutput string, parsed parsedFlags, goflags, skip string) string {
	lines := strings.Split(strings.TrimRight(listOutput, "\n"), "\n")
	sort.Strings(lines)
	payload := strings.Join(lines, "\n") + "\n" +
		"build\x00" + strings.Join(parsed.build, "\x00") + "\n" +
		"short\x00" + strconv.FormatBool(parsed.short) + "\n" +
		"goflags\x00" + goflags + "\n" +
		"skip\x00" + skip + "\n"
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:16]
}
