package agent

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestRetainedMatchesAccumulatorParity proves the O(1) running-accumulator
// size accounting used by searchRetainedOutput is IDENTICAL to
// retainedMatchesSerializedSize (and to the actual wire bytes) at every step.
func TestRetainedMatchesAccumulatorParity(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	fragments := []string{
		"plain line",
		`quote " and backslash \ mix`,
		"unicode é☃ tail",
		"control\x01\x02 chars",
		"trailing space ",
		"",
		strings.Repeat("x", 300),
	}
	var matches []retainedSearchMatch
	acc := 0 // running Σ(len(encoded_i)+1) over matches
	rnd := func() string {
		s := fragments[rng.Intn(len(fragments))]
		if rng.Intn(2) == 0 {
			s += fmt.Sprintf(" #%d", rng.Intn(100000))
		}
		return s
	}
	for k := range 150 {
		m := retainedSearchMatch{
			LineStartByte: int64(k * 37),
			Before:        []string{rnd(), rnd()},
			Line:          rnd(),
			After:         []string{rnd()},
		}
		cand, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal candidate %d: %v", k, err)
		}
		// Wire truth: marshal the full slice with the candidate appended.
		wire, err := json.Marshal(append(append([]retainedSearchMatch(nil), matches...), m))
		if err != nil {
			t.Fatalf("marshal wire %d: %v", k, err)
		}
		if got := retainedMatchesSerializedSize(nil, len(cand)); got != 2+len(cand) {
			t.Fatalf("step %d: nil-size = %d, want %d", k, got, 2+len(cand))
		}
		if got := retainedMatchesSerializedSize(matches, len(cand)); got != len(wire) {
			t.Fatalf("step %d: function = %d, wire = %d", k, got, len(wire))
		}
		if got := 2 + len(cand) + acc; got != retainedMatchesSerializedSize(matches, len(cand)) {
			t.Fatalf("step %d: accumulator = %d, function = %d", k, got, retainedMatchesSerializedSize(matches, len(cand)))
		}
		matches = append(matches, m)
		acc += len(cand) + 1
	}
}

// BenchmarkSearchRetainedOutputManyMatches exercises the per-candidate
// serialized-size accounting with ~100 matches so the O(matches²)
// re-marshal cost (before) vs the O(1) accumulator (after) is visible.
func BenchmarkSearchRetainedOutputManyMatches(b *testing.B) {
	var sb strings.Builder
	for i := range 120 {
		fmt.Fprintf(&sb, "match line %04d with some padding text to resemble log output\n", i)
	}
	data := []byte(sb.String())
	// The nested literal stays: retainedSearchOptions embeds
	// jobstore.SearchOptions, so its fields cannot be keyed directly.
	// (modernize's embedlit suggestion does not compile here.)
	opts := retainedSearchOptions{
		Regexp: regexp.MustCompile(`match line`),
		SearchOptions: jobstore.SearchOptions{
			MaxMatches:         100,
			MaxSerializedBytes: 1 << 20,
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env, err := searchRetainedOutput(newMemorySearchSource(data, 0), opts)
		if err != nil {
			b.Fatal(err)
		}
		if len(env.Matches) != 100 {
			b.Fatalf("matches = %d, want 100", len(env.Matches))
		}
	}
}
