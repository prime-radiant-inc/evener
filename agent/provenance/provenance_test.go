package provenance

import (
	"fmt"
	"testing"
)

func TestUnionDedupesWatchKeysAndKeepsStableOrder(t *testing.T) {
	a := &Causal{WatchKeys: []WatchKey{
		{WatchID: "watch_A", WatchGeneration: "wg_1"},
		{WatchID: "watch_B", WatchGeneration: "wg_1"},
	}}
	b := &Causal{WatchKeys: []WatchKey{
		{WatchID: "watch_B", WatchGeneration: "wg_1"},
		{WatchID: "watch_A", WatchGeneration: "wg_2"},
	}}

	got := Union(a, b)
	want := []WatchKey{
		{WatchID: "watch_A", WatchGeneration: "wg_1"},
		{WatchID: "watch_B", WatchGeneration: "wg_1"},
		{WatchID: "watch_A", WatchGeneration: "wg_2"},
	}
	if len(got.WatchKeys) != len(want) {
		t.Fatalf("watch key count = %d, want %d: %+v", len(got.WatchKeys), len(want), got.WatchKeys)
	}
	for i := range want {
		if got.WatchKeys[i] != want[i] {
			t.Fatalf("watch key %d = %+v, want %+v", i, got.WatchKeys[i], want[i])
		}
	}
}

func TestUnionDedupesSharedChainEntriesAndKeepsDistinctOnes(t *testing.T) {
	shared := Entry{Kind: "watch", WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_1"}
	a := &Causal{Chain: []Entry{shared, {Kind: "message", SessionID: "child", DeliveryID: "update_a"}}}
	b := &Causal{Chain: []Entry{shared, {Kind: "message", SessionID: "child", DeliveryID: "update_b"}}}

	got := Union(a, b)
	want := []Entry{shared, a.Chain[1], b.Chain[1]}
	if len(got.Chain) != len(want) {
		t.Fatalf("chain length = %d, want %d: %+v", len(got.Chain), len(want), got.Chain)
	}
	for i := range want {
		if got.Chain[i] != want[i] {
			t.Fatalf("chain entry %d = %+v, want %+v", i, got.Chain[i], want[i])
		}
	}

	// An entry differing in any field is a genuinely new cause, never a
	// duplicate: it must survive the union whatever it shares.
	fresh := Entry{Kind: "watch", WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_2"}
	merged := Union(a, &Causal{Chain: []Entry{shared, fresh}})
	if len(merged.Chain) != 3 {
		t.Fatalf("chain length with a distinct fire = %d, want 3: %+v", len(merged.Chain), merged.Chain)
	}
}

func TestUnionMergesAndBoundsFireKeys(t *testing.T) {
	a := &Causal{FireKeys: []FireKey{{WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_1"}}}
	b := &Causal{FireKeys: []FireKey{
		{WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_1"},
		{WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_2"},
	}}

	got := Union(a, b)
	if len(got.FireKeys) != 2 || got.FireKeys[0].DeliveryID != "wd_1" || got.FireKeys[1].DeliveryID != "wd_2" {
		t.Fatalf("fire keys = %+v, want the two distinct fires in first-seen order", got.FireKeys)
	}

	var long *Causal
	for i := range maxCausalFiresPerWatch + 5 {
		long = WithWatch(long, "watch_A", "wg_1", fmt.Sprintf("wd_%03d", i), "session", "")
	}
	if len(long.FireKeys) != maxCausalFiresPerWatch {
		t.Fatalf("fire keys = %d, want bounded at %d", len(long.FireKeys), maxCausalFiresPerWatch)
	}
	if long.FireKeys[0].DeliveryID != "wd_005" {
		t.Fatalf("oldest retained fire = %q, want the bound to keep the newest", long.FireKeys[0].DeliveryID)
	}
	if depth := SelfInfluenceDepth(long, "watch_A", "", func(string) bool { return true }); depth < maxCausalFiresPerWatch {
		t.Fatalf("depth past the bound = %d, want at least the %d newest distinct fires to remain countable", depth, maxCausalFiresPerWatch)
	}
}

// TestFireBoundKeepsEveryWatchsRunawayDepth proves the fire record stays
// usable for the runaway fuse, which counts fires per watch. A chain that
// cycles through many watches must still reach the fuse depth for each one it
// passes through; a single global cap hands the newest watches every slot and
// leaves the older ones with too few retained fires to ever trip.
func TestFireBoundKeepsEveryWatchsRunawayDepth(t *testing.T) {
	const (
		watches    = 16
		fuseDepth  = 8
		cycleCount = fuseDepth + 4
	)
	var chain *Causal
	for round := range cycleCount {
		for w := range watches {
			chain = WithWatch(chain, fmt.Sprintf("watch_%02d", w), "wg_1", fmt.Sprintf("wd_%02d_%02d", round, w), "session", "")
		}
	}
	// The record may hold each watch's own bound; a global cap is what starved
	// the older watches.
	if maxTotal := watches * maxCausalFiresPerWatch; len(chain.FireKeys) > maxTotal {
		t.Fatalf("fire keys = %d, want bounded at %d (%d watches x %d)", len(chain.FireKeys), maxTotal, watches, maxCausalFiresPerWatch)
	}
	delivered := func(string) bool { return true }
	for w := range watches {
		watchID := fmt.Sprintf("watch_%02d", w)
		if depth := SelfInfluenceDepth(chain, watchID, "", delivered); depth < fuseDepth {
			t.Fatalf("watch %s exposes %d of its own delivered fires, want at least the fuse depth %d after %d full cycles through %d watches",
				watchID, depth, fuseDepth, cycleCount, watches)
		}
	}
}

func TestContainsWatchRequiresGenerationMatch(t *testing.T) {
	p := &Causal{WatchKeys: []WatchKey{{WatchID: "watch_A", WatchGeneration: "wg_1"}}}
	if !ContainsWatch(p, "watch_A", "wg_1") {
		t.Fatal("same generation should match")
	}
	if ContainsWatch(p, "watch_A", "wg_2") {
		t.Fatal("different generation must not match")
	}
	if ContainsWatch(nil, "watch_A", "wg_1") {
		t.Fatal("nil provenance must not match")
	}
}

func TestWithWatchAddsLoadBearingKeyAndDiagnosticEntry(t *testing.T) {
	root := &Causal{WatchKeys: []WatchKey{{WatchID: "watch_root", WatchGeneration: "wg_root"}}}
	got := WithWatch(root, "watch_A", "wg_1", "wd_1", "session_1", "job_1")

	if !ContainsWatch(got, "watch_root", "wg_root") || !ContainsWatch(got, "watch_A", "wg_1") {
		t.Fatalf("provenance keys = %+v, want root and added watch", got.WatchKeys)
	}
	if len(got.Chain) != 1 {
		t.Fatalf("chain length = %d, want 1", len(got.Chain))
	}
	entry := got.Chain[0]
	if entry.Kind != "watch" || entry.WatchID != "watch_A" || entry.WatchGeneration != "wg_1" ||
		entry.DeliveryID != "wd_1" || entry.SessionID != "session_1" || entry.JobID != "job_1" {
		t.Fatalf("entry = %+v, want watch diagnostic entry", entry)
	}
}

func TestDiagnosticChainTruncatesWithoutDroppingWatchKeys(t *testing.T) {
	p := &Causal{}
	for i := range maxDiagnosticChain + 5 {
		p = WithWatch(p, "watch_"+string(rune('A'+i)), "wg_1", "wd", "session", "job")
	}
	if len(p.WatchKeys) != maxDiagnosticChain+5 {
		t.Fatalf("watch keys = %d, want all keys retained", len(p.WatchKeys))
	}
	if len(p.Chain) > maxDiagnosticChain {
		t.Fatalf("chain length = %d, want at most %d", len(p.Chain), maxDiagnosticChain)
	}
	if !p.ChainTruncated {
		t.Fatal("chain_truncated should be true")
	}
}

func TestCloneTruncatesOverlongDiagnosticChain(t *testing.T) {
	if Clone(nil) != nil {
		t.Fatal("Clone(nil) must return nil")
	}

	p := &Causal{}
	for range maxDiagnosticChain + 5 {
		p.Chain = append(p.Chain, Entry{Kind: "manual", DeliveryID: "wd"})
	}

	got := Clone(p)
	if len(got.Chain) > maxDiagnosticChain {
		t.Fatalf("clone chain length = %d, want at most %d", len(got.Chain), maxDiagnosticChain)
	}
	if !got.ChainTruncated {
		t.Fatal("clone should mark an overlong diagnostic chain truncated")
	}
}

func TestNilIfEmpty(t *testing.T) {
	if NilIfEmpty(nil) != nil {
		t.Fatal("nil stays nil")
	}
	if NilIfEmpty(&Causal{}) != nil {
		t.Fatal("empty provenance should serialize as nil")
	}
	if NilIfEmpty(&Causal{WatchKeys: []WatchKey{{WatchID: "watch_A", WatchGeneration: "wg_1"}}}) == nil {
		t.Fatal("non-empty provenance should survive (WatchKeys)")
	}
	if NilIfEmpty(&Causal{Chain: []Entry{{Kind: "x"}}}) == nil {
		t.Fatal("non-empty provenance should survive (Chain)")
	}
	if NilIfEmpty(&Causal{ChainTruncated: true}) == nil {
		t.Fatal("non-empty provenance should survive (ChainTruncated)")
	}
}

func TestLatestDeliveryID(t *testing.T) {
	if LatestDeliveryID(nil) != "" {
		t.Fatal("nil provenance should return empty string")
	}
	if LatestDeliveryID(&Causal{}) != "" {
		t.Fatal("empty chain should return empty string")
	}

	single := &Causal{Chain: []Entry{{Kind: "watch", DeliveryID: "wd_1"}}}
	if got := LatestDeliveryID(single); got != "wd_1" {
		t.Fatalf("single entry: got %q, want %q", got, "wd_1")
	}

	// Last non-empty DeliveryID is not the last element.
	gapped := &Causal{Chain: []Entry{
		{Kind: "watch", DeliveryID: "wd_1"},
		{Kind: "watch", DeliveryID: "wd_2"},
		{Kind: "watch", DeliveryID: ""},
	}}
	if got := LatestDeliveryID(gapped); got != "wd_2" {
		t.Fatalf("gapped chain: got %q, want %q", got, "wd_2")
	}

	// Only the last element has a non-empty DeliveryID.
	lastOnly := &Causal{Chain: []Entry{
		{Kind: "watch", DeliveryID: ""},
		{Kind: "watch", DeliveryID: "wd_3"},
	}}
	if got := LatestDeliveryID(lastOnly); got != "wd_3" {
		t.Fatalf("last-only chain: got %q, want %q", got, "wd_3")
	}
}

func TestSelfInfluenceDepth(t *testing.T) {
	chain := func(entries ...Entry) *Causal { return &Causal{Chain: entries} }
	watchHop := func(watchID, generation, deliveryID string) Entry {
		return Entry{Kind: "watch", WatchID: watchID, WatchGeneration: generation, DeliveryID: deliveryID}
	}

	tests := []struct {
		name       string
		p          *Causal
		watchID    string
		generation string
		delivered  map[string]bool
		want       int
	}{
		{
			name:    "nil provenance",
			p:       nil,
			watchID: "watch_A",
			want:    0,
		},
		{
			name:    "empty chain",
			p:       &Causal{},
			watchID: "watch_A",
			want:    0,
		},
		{
			name:      "empty watch id",
			p:         chain(watchHop("watch_A", "wg_1", "wd_1")),
			watchID:   "",
			delivered: map[string]bool{"wd_1": true},
			want:      0,
		},
		{
			name:      "one delivered self hop",
			p:         chain(watchHop("watch_A", "wg_1", "wd_1")),
			watchID:   "watch_A",
			delivered: map[string]bool{"wd_1": true},
			want:      1,
		},
		{
			name:      "coalesced-away self hop is not counted",
			p:         chain(watchHop("watch_A", "wg_1", "wd_1")),
			watchID:   "watch_A",
			delivered: map[string]bool{"wd_1": false},
			want:      0,
		},
		{
			name:      "same delivery id twice dedupes",
			p:         chain(watchHop("watch_A", "wg_1", "wd_1"), watchHop("watch_A", "wg_1", "wd_1")),
			watchID:   "watch_A",
			delivered: map[string]bool{"wd_1": true},
			want:      1,
		},
		{
			name: "a fire recorded past the chain bound still counts",
			p: &Causal{FireKeys: []FireKey{
				{WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_1"},
				{WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_2"},
			}},
			watchID:   "watch_A",
			delivered: map[string]bool{"wd_1": true, "wd_2": true},
			want:      2,
		},
		{
			name: "fire record and chain count once together",
			p: &Causal{
				Chain:    []Entry{watchHop("watch_A", "wg_1", "wd_1")},
				FireKeys: []FireKey{{WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_1"}},
			},
			watchID:   "watch_A",
			delivered: map[string]bool{"wd_1": true},
			want:      1,
		},
		{
			name: "fire record respects the generation scope",
			p: &Causal{FireKeys: []FireKey{
				{WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_1"},
				{WatchID: "watch_A", WatchGeneration: "wg_2", DeliveryID: "wd_2"},
			}},
			watchID:    "watch_A",
			generation: "wg_1",
			delivered:  map[string]bool{"wd_1": true, "wd_2": true},
			want:       1,
		},
		{
			name:       "generation filter counts only that generation",
			p:          chain(watchHop("watch_A", "g1", "wd_1"), watchHop("watch_A", "g2", "wd_2")),
			watchID:    "watch_A",
			generation: "g1",
			delivered:  map[string]bool{"wd_1": true, "wd_2": true},
			want:       1,
		},
		{
			name:       "no generation filter counts across generations",
			p:          chain(watchHop("watch_A", "g1", "wd_1"), watchHop("watch_A", "g2", "wd_2")),
			watchID:    "watch_A",
			generation: "",
			delivered:  map[string]bool{"wd_1": true, "wd_2": true},
			want:       2,
		},
		{
			name:      "different watch id is ignored",
			p:         chain(watchHop("watch_B", "wg_1", "wd_1")),
			watchID:   "watch_A",
			delivered: map[string]bool{"wd_1": true},
			want:      0,
		},
		{
			name:      "non-watch kind is ignored",
			p:         chain(Entry{Kind: "manual", WatchID: "watch_A", WatchGeneration: "wg_1", DeliveryID: "wd_1"}),
			watchID:   "watch_A",
			delivered: map[string]bool{"wd_1": true},
			want:      0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			delivered := func(id string) bool { return tc.delivered[id] }
			if got := SelfInfluenceDepth(tc.p, tc.watchID, tc.generation, delivered); got != tc.want {
				t.Fatalf("SelfInfluenceDepth = %d, want %d", got, tc.want)
			}
		})
	}
}
