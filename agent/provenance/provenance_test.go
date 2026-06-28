package provenance

import "testing"

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
	for i := 0; i < maxDiagnosticChain+5; i++ {
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
	for i := 0; i < maxDiagnosticChain+5; i++ {
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
