//go:build serffuzz

package provenance

import (
	"testing"
)

// FuzzWvSelfInfluenceDepth drives SelfInfluenceDepth — the breaker's
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

		p := &Causal{
			Chain: []Entry{
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

		scoped := SelfInfluenceDepth(p, watchID, generation, oracle)
		if scoped2 := SelfInfluenceDepth(p, watchID, generation, oracle); scoped != scoped2 {
			t.Fatalf("non-deterministic: %d vs %d", scoped, scoped2)
		}
		agnostic := SelfInfluenceDepth(p, watchID, "", oracle)
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
		if got := SelfInfluenceDepth(p, watchID, generation, none); got != 0 {
			t.Fatalf("empty delivered-set must yield 0, got %d", got)
		}
	})
}
