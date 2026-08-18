package main

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
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
	for _, line := range strings.Split(output, "\n") {
		m := surveyLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || strings.Contains(m[1], "/") {
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
	for _, line := range strings.Split(listOutput, "\n") {
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
		return nil, nil, fmt.Errorf("found no tests to shard")
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
				return nil, nil, fmt.Errorf("partition is not a bijection over the test set")
			}
			seen[name] = true
			placed++
		}
	}
	if placed != len(want) {
		return nil, nil, fmt.Errorf("partition is not a bijection over the test set")
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

// translateFlags converts the caller's `go test` flag spellings into the
// compiled binary's -test.* spellings. Flags outside the table are dropped,
// the way the script's case statement dropped them.
func translateFlags(flags []string) []string {
	var out []string
	for _, f := range flags {
		switch {
		case f == "-short" || f == "-race" || f == "-v":
			out = append(out, "-test."+strings.TrimPrefix(f, "-"))
		case strings.HasPrefix(f, "-count="):
			out = append(out, "-test.count="+strings.TrimPrefix(f, "-count="))
		}
	}
	return out
}

// testSetKey is the survey cache key: the identity of the sorted test list.
// Add, rename, or remove a test and the key changes; otherwise every run
// reuses the cached survey and pays nothing.
func testSetKey(listOutput string) string {
	lines := strings.Split(strings.TrimRight(listOutput, "\n"), "\n")
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return fmt.Sprintf("%x", sum)[:16]
}
