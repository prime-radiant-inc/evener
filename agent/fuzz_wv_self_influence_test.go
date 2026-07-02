//go:build serffuzz

package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/provenance"
)

// FuzzWvSelfInfluenceNotice drives selfInfluenceNotice — the breaker's pure
// worker-facing wording seam that replaced the deleted validateWatchDeliveryLoop
// create-time forbid (the runtime breaker now bounds self-delivery loops).
//
// Oracles (beyond never-panic):
//   - determinism: the same inputs yield the same line;
//   - empty exactly when not self-influenced;
//   - every non-empty line is a single <system-reminder>-wrapped line;
//   - truncated always wins the depth-less wording (no "~N" leaks);
//   - a depth >= 2 line names the depth, shallower lines never do.
func FuzzWvSelfInfluenceNotice(f *testing.F) {
	f.Add(false, 0, false)
	f.Add(true, 0, false)
	f.Add(true, 1, false)
	f.Add(true, 3, false)
	f.Add(true, 5, true)
	f.Add(true, -2, false)

	f.Fuzz(func(t *testing.T, self bool, gradientDepth int, truncated bool) {
		got := selfInfluenceNotice(self, gradientDepth, truncated)
		if got2 := selfInfluenceNotice(self, gradientDepth, truncated); got != got2 {
			t.Fatalf("non-deterministic: %q vs %q", got, got2)
		}
		if !self {
			if got != "" {
				t.Fatalf("not self-influenced must be empty, got %q", got)
			}
			return
		}
		if got == "" {
			t.Fatal("self-influenced notice must be non-empty")
		}
		if !strings.HasPrefix(got, "<system-reminder>") || !strings.HasSuffix(got, "</system-reminder>") {
			t.Fatalf("notice must be system-reminder wrapped: %q", got)
		}
		if strings.Contains(got, "\n") {
			t.Fatalf("notice must be a single line: %q", got)
		}
		hasDepth := strings.Contains(got, "~")
		if truncated && hasDepth {
			t.Fatalf("truncated notice must not name a depth: %q", got)
		}
		if !truncated && gradientDepth >= 2 && !hasDepth {
			t.Fatalf("deep notice must name the depth: %q", got)
		}
		if !truncated && gradientDepth < 2 && hasDepth {
			t.Fatalf("shallow notice must not name a depth: %q", got)
		}
	})
}

// FuzzWvSelfInfluenceDepth drives provenance.SelfInfluenceDepth — the breaker's
// pure depth metric — over fuzzed chains and delivered-sets.
//
// Oracles (beyond never-panic):
//   - determinism;
//   - depth is never negative and never exceeds the chain length;
//   - generation-scoped depth (gradient) never exceeds the generation-agnostic
//     depth (fuse) for the same watch;
//   - an empty delivered-set always yields depth 0 (a never-delivered prior
//     cannot inflate depth — the coalescing rule).
func FuzzWvSelfInfluenceDepth(f *testing.F) {
	f.Add("w1", "g1", "w1", "g1", "wd_1", true, "w1", "g2", "wd_2", false)
	f.Add("w1", "", "w2", "g1", "wd_1", false, "w1", "g1", "wd_1", true)
	f.Add("", "", "", "", "", false, "", "", "", false)

	f.Fuzz(func(t *testing.T, watchID, generation string,
		w1, g1, d1 string, del1 bool,
		w2, g2, d2 string, del2 bool) {

		p := &provenance.Causal{
			Chain: []provenance.Entry{
				{Kind: "watch", WatchID: w1, WatchGeneration: g1, DeliveryID: d1},
				{Kind: "watch", WatchID: w2, WatchGeneration: g2, DeliveryID: d2},
			},
		}
		delivered := map[string]bool{}
		if del1 {
			delivered[d1] = true
		}
		if del2 {
			delivered[d2] = true
		}
		oracle := func(id string) bool { return delivered[id] }

		scoped := provenance.SelfInfluenceDepth(p, watchID, generation, oracle)
		if scoped2 := provenance.SelfInfluenceDepth(p, watchID, generation, oracle); scoped != scoped2 {
			t.Fatalf("non-deterministic: %d vs %d", scoped, scoped2)
		}
		agnostic := provenance.SelfInfluenceDepth(p, watchID, "", oracle)
		if scoped < 0 || agnostic < 0 {
			t.Fatalf("negative depth: scoped=%d agnostic=%d", scoped, agnostic)
		}
		if scoped > len(p.Chain) || agnostic > len(p.Chain) {
			t.Fatalf("depth exceeds chain length: scoped=%d agnostic=%d", scoped, agnostic)
		}
		if scoped > agnostic {
			t.Fatalf("generation-scoped depth %d exceeds generation-agnostic %d", scoped, agnostic)
		}
		none := func(string) bool { return false }
		if got := provenance.SelfInfluenceDepth(p, watchID, generation, none); got != 0 {
			t.Fatalf("empty delivered-set must yield 0, got %d", got)
		}
	})
}
