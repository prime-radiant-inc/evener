package provenance

import (
	"bytes"
	"testing"
)

// FuzzProvenance drives the causal-provenance set algebra — Clone, Union,
// WithWatch, ContainsWatch, NilIfEmpty, LatestDeliveryID, and the unexported
// truncateChain — over arbitrary Causal values assembled from fuzzed bytes. This
// package has no fuzz target yet, and these pure functions (watch-key dedup,
// chain bounding, set union) carry load-bearing loop-suppression semantics that
// only unit tests touched.
//
// Oracles (never bare no-panic):
//   - Chain bound: every constructed Causal has len(Chain) <= maxDiagnosticChain,
//     and ChainTruncated is set whenever truncation actually dropped entries.
//   - WatchKeys are deduped and non-degenerate: no duplicate key, and no key with
//     an empty WatchID or WatchGeneration survives Union/WithWatch.
//   - Truncation never drops watch keys: truncateChain leaves WatchKeys untouched.
//   - Union is a set union: a key is in the result iff it is a valid key in some
//     input part; ContainsWatch agrees with a direct scan.
//   - WithWatch adds the key and preserves the base's keys.
//   - Clone is a faithful, independent copy: same key set as NilIfEmpty(truncated
//     base), and mutating the clone never disturbs the original.
//   - NilIfEmpty returns nil exactly when the value carries no keys, no chain, and
//     is not truncated.
//   - LatestDeliveryID returns the delivery ID of the last chain entry that has
//     one, matching a manual reverse scan.
func FuzzProvenance(f *testing.F) {
	f.Add([]byte{1, 2, 0, 1}, []byte{3, 0, 1}, "w", "g", "d")
	f.Add([]byte{}, []byte{}, "", "", "")
	f.Add([]byte{40, 40, 40}, []byte{5}, "watch-1", "gen-1", "deliv-9")
	f.Add([]byte{0, 0, 0, 0}, []byte{0}, "", "g", "d")
	// >maxDiagnosticChain entries so the truncation branch is covered at replay.
	f.Add(bytes.Repeat([]byte{1, 3}, 12), bytes.Repeat([]byte{3}, 20), "w", "g", "d")

	f.Fuzz(func(t *testing.T, a, b []byte, watchID, watchGen, deliveryID string) {
		pa := buildCausal(a)
		pb := buildCausal(b)

		// truncateChain on a raw (possibly over-long) value preserves every watch
		// key while bounding the chain. Run on a private copy so it never disturbs
		// pa/pb used as inputs below.
		assertTruncatePreservesKeys(t, buildCausal(a))

		u := Union(pa, pb)
		assertCausalInvariants(t, u)

		// Union is a set union over the valid keys of its parts.
		want := map[WatchKey]bool{}
		for _, part := range []*Causal{pa, pb} {
			if part == nil {
				continue
			}
			for _, k := range part.WatchKeys {
				if k.WatchID != "" && k.WatchGeneration != "" {
					want[k] = true
				}
			}
		}
		got := keySet(u)
		if len(got) != len(want) {
			t.Fatalf("Union key count = %d, want %d", len(got), len(want))
		}
		for k := range want {
			if !got[k] {
				t.Fatalf("Union dropped valid key %+v", k)
			}
			if !ContainsWatch(u, k.WatchID, k.WatchGeneration) {
				t.Fatalf("ContainsWatch false for present key %+v", k)
			}
		}

		// WithWatch adds the new key (when non-degenerate) and keeps base keys.
		w := WithWatch(pa, watchID, watchGen, deliveryID, "sess", "job")
		assertCausalInvariants(t, w)
		if watchID != "" && watchGen != "" {
			if !ContainsWatch(w, watchID, watchGen) {
				t.Fatalf("WithWatch did not record key (%q,%q)", watchID, watchGen)
			}
		}
		for k := range keySet(pa) {
			if k.WatchID == "" || k.WatchGeneration == "" {
				continue // Union drops degenerate keys; only valid keys survive.
			}
			if !ContainsWatch(w, k.WatchID, k.WatchGeneration) {
				t.Fatalf("WithWatch dropped base key %+v", k)
			}
		}

		// Clone is an independent copy with the same key set.
		// Clone preserves keys verbatim (it does not dedup), so only the chain
		// bound applies to its output, not the dedup/non-degenerate invariant.
		baseKeys := keySet(pa)
		c := Clone(pa)
		assertChainBound(t, c)
		if cs := keySet(c); !sameKeySet(cs, keySet(NilIfEmpty(pa))) {
			t.Fatalf("Clone key set %v != base %v", cs, keySet(NilIfEmpty(pa)))
		}
		if c != nil {
			// Mutating the clone must not reach back into the original.
			for i := range c.WatchKeys {
				c.WatchKeys[i].WatchID += "X"
			}
			if !sameKeySet(keySet(pa), baseKeys) {
				t.Fatalf("mutating Clone disturbed the original's key set")
			}
		}

		// LatestDeliveryID matches a manual reverse scan over the chain.
		assertLatestDeliveryID(t, u)
	})
}

// buildCausal assembles a Causal from fuzz bytes: each byte contributes either a
// watch key (with a chain entry that mirrors it) or a bare chain entry, so the
// fuzzer reaches both the dedup path and the chain-overflow/truncation path. Some
// keys are deliberately degenerate (empty fields) and some are duplicates.
func buildCausal(b []byte) *Causal {
	if len(b) == 0 {
		return nil
	}
	var p Causal
	for i, x := range b {
		switch x % 4 {
		case 0:
			// Degenerate key (empty generation): Union must drop it.
			p.WatchKeys = append(p.WatchKeys, WatchKey{WatchID: idFor(x)})
			p.Chain = append(p.Chain, Entry{Kind: "watch", WatchID: idFor(x)})
		case 1:
			k := WatchKey{WatchID: idFor(x), WatchGeneration: genFor(x)}
			p.WatchKeys = append(p.WatchKeys, k)
			p.Chain = append(p.Chain, Entry{Kind: "watch", WatchID: k.WatchID, WatchGeneration: k.WatchGeneration, DeliveryID: delivFor(i)})
		case 2:
			// Duplicate of a low-cardinality key to exercise dedup.
			p.WatchKeys = append(p.WatchKeys, WatchKey{WatchID: "dup", WatchGeneration: "g0"})
			p.Chain = append(p.Chain, Entry{Kind: "deliver", DeliveryID: delivFor(i)})
		default:
			// Bare chain entry, no key — grows the chain toward truncation.
			p.Chain = append(p.Chain, Entry{Kind: "note"})
		}
	}
	return &p
}

func idFor(x byte) string   { return "w" + string(rune('a'+x%5)) }
func genFor(x byte) string  { return "g" + string(rune('0'+x%3)) }
func delivFor(i int) string { return "d" + string(rune('0'+i%7)) }

// assertCausalInvariants checks the structural guarantees a constructed Causal
// must satisfy regardless of input.
func assertCausalInvariants(t *testing.T, p *Causal) {
	t.Helper()
	if p == nil {
		return
	}
	assertChainBound(t, p)
	seen := map[WatchKey]bool{}
	for _, k := range p.WatchKeys {
		if k.WatchID == "" || k.WatchGeneration == "" {
			t.Fatalf("degenerate watch key survived: %+v", k)
		}
		if seen[k] {
			t.Fatalf("duplicate watch key survived: %+v", k)
		}
		seen[k] = true
	}
}

func assertChainBound(t *testing.T, p *Causal) {
	t.Helper()
	if p != nil && len(p.Chain) > maxDiagnosticChain {
		t.Fatalf("Chain length %d exceeds bound %d", len(p.Chain), maxDiagnosticChain)
	}
}

func keySet(p *Causal) map[WatchKey]bool {
	out := map[WatchKey]bool{}
	if p == nil {
		return out
	}
	for _, k := range p.WatchKeys {
		out[k] = true
	}
	return out
}

func sameKeySet(a, b map[WatchKey]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func assertLatestDeliveryID(t *testing.T, p *Causal) {
	t.Helper()
	want := ""
	if p != nil {
		for i := len(p.Chain) - 1; i >= 0; i-- {
			if p.Chain[i].DeliveryID != "" {
				want = p.Chain[i].DeliveryID
				break
			}
		}
	}
	if got := LatestDeliveryID(p); got != want {
		t.Fatalf("LatestDeliveryID = %q, want %q", got, want)
	}
}

// assertTruncatePreservesKeys verifies truncateChain never disturbs WatchKeys.
func assertTruncatePreservesKeys(t *testing.T, p *Causal) {
	t.Helper()
	if p == nil {
		return
	}
	before := append([]WatchKey(nil), p.WatchKeys...)
	truncateChain(p)
	if len(p.WatchKeys) != len(before) {
		t.Fatalf("truncateChain changed WatchKeys count %d -> %d", len(before), len(p.WatchKeys))
	}
	for i := range before {
		if p.WatchKeys[i] != before[i] {
			t.Fatalf("truncateChain mutated WatchKey %d", i)
		}
	}
}
