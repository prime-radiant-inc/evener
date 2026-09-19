package hubcore

import (
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestRemoteThreadCacheEquivalentEmptyFormsAreNoOp(t *testing.T) {
	c := &RemoteThreadCache{}
	var calls atomic.Int32
	c.SetOnChange(func() { calls.Add(1) })
	c.StoreSnapshotData(RemoteThreadSnapshot{Complete: true, Threads: []appwire.Thread{}, Sources: map[string]RemoteSourceSnapshot{}})
	first := c.Snapshot().Generation
	c.StoreSnapshotData(RemoteThreadSnapshot{Complete: true, Threads: nil, Sources: nil})
	if got := c.Snapshot().Generation; got != first || calls.Load() != 1 {
		t.Fatalf("equivalent snapshot changed: generation=%d calls=%d", got, calls.Load())
	}
}

func TestRemoteThreadCacheGenerationAvoidsSnapshotContract(t *testing.T) {
	c := &RemoteThreadCache{}
	c.Store([]appwire.Thread{{ID: "t1"}})
	if got, want := c.Generation(), c.Snapshot().Generation; got != want {
		t.Fatalf("generation=%d, snapshot generation=%d", got, want)
	}
}

func fuzzScenarioRemoteThreadCacheReadReturnsLastStored(t *testing.T) {
	c := &RemoteThreadCache{}
	if got := c.Get(); got != nil {
		t.Fatalf("empty cache should return nil, got %v", got)
	}
	threads := []appwire.Thread{{ID: "t1"}, {ID: "t2"}}
	c.Store(threads)
	got := c.Get()
	if len(got) != 2 || got[0].ID != "t1" {
		t.Fatalf("cache should return stored threads, got %+v", got)
	}
	// Get returns a copy — mutating it must not corrupt the cache.
	got[0].ID = "mutated"
	if c.Get()[0].ID != "t1" {
		t.Fatal("Get must return a defensive copy")
	}
}

func fuzzScenarioRemoteThreadCacheSnapshotTracksAuthorityGeneration(t *testing.T) {
	c := &RemoteThreadCache{}
	c.StoreSnapshot([]appwire.Thread{{ID: "stale"}}, false)
	first := c.Snapshot()
	if first.Complete {
		t.Fatal("failed source snapshot must not be marked complete")
	}
	if first.Generation != 1 || len(first.Threads) != 1 || first.Threads[0].ID != "stale" {
		t.Fatalf("first snapshot = %+v, want generation 1 with stale row", first)
	}

	c.Store([]appwire.Thread{{ID: "fresh"}})
	second := c.Snapshot()
	if !second.Complete || second.Generation != 2 || len(second.Threads) != 1 || second.Threads[0].ID != "fresh" {
		t.Fatalf("second snapshot = %+v, want complete generation 2 with fresh row", second)
	}
}

func TestRemoteThreadCacheSnapshotDefensivelyCopiesSourceAuthority(t *testing.T) {
	thread := appwire.Thread{ID: "thread", Source: "remote"}
	incompleteID := "remote:bad"
	cache := &RemoteThreadCache{}
	snapshot := RemoteThreadSnapshot{
		Threads:  []appwire.Thread{thread},
		Complete: false,
		Sources: map[string]RemoteSourceSnapshot{
			"remote": {Threads: []appwire.Thread{thread}, Complete: false, IncompleteIDs: []string{incompleteID}},
		},
	}
	cache.StoreSnapshotData(snapshot)
	snapshot.Threads[0].ID = "changed"
	snapshot.Sources["remote"].Threads[0].ID = "changed"
	snapshot.Sources["remote"].IncompleteIDs[0] = "changed"

	got := cache.Snapshot()
	if got.Generation != 1 || got.Complete || got.Threads[0].ID != "thread" {
		t.Fatalf("snapshot metadata = %+v", got)
	}
	if got.Sources["remote"].Threads[0].ID != "thread" || got.Sources["remote"].IncompleteIDs[0] != incompleteID {
		t.Fatalf("source authority was not defensively copied: %+v", got.Sources)
	}
	got.Sources["remote"].IncompleteIDs[0] = "mutated"
	if again := cache.Snapshot(); again.Sources["remote"].IncompleteIDs[0] != incompleteID {
		t.Fatalf("mutating a returned source snapshot changed cache state: %+v", again.Sources)
	}
}

func TestRemoteThreadCacheRemoveSourceDropsOwnedRowsAndPerSourceSnapshot(t *testing.T) {
	c := &RemoteThreadCache{}
	var calls atomic.Int32
	c.SetOnChange(func() { calls.Add(1) })
	c.StoreSnapshot([]appwire.Thread{
		{ID: "a1", Source: "host-a"},
		{ID: "a2", Source: "", Evener: appwire.EvenerThread{Ref: "host-a:a2"}},
		{ID: "b1", Source: "host-b"},
	}, true)
	before := c.Snapshot()
	if before.Generation != 1 {
		t.Fatalf("seed generation = %d, want 1", before.Generation)
	}

	c.RemoveSource("host-a")

	after := c.Snapshot()
	if after.Generation != before.Generation+1 {
		t.Fatalf("prune generation = %d, want %d", after.Generation, before.Generation+1)
	}
	// Both rows the snapshot walk attributed to host-a go — the one its own
	// Source names and the one only its parsed ref does — and only those.
	if len(after.Threads) != 1 || after.Threads[0].ID != "b1" {
		t.Fatalf("threads after prune = %+v, want only host-b's row", after.Threads)
	}
	if _, ok := after.Sources["host-a"]; ok {
		t.Fatal("prune kept host-a's per-source snapshot")
	}
	if _, ok := after.Sources["host-b"]; !ok {
		t.Fatal("prune dropped host-b's per-source snapshot")
	}
	if calls.Load() != 2 {
		t.Fatalf("onChange fired %d times, want the store's and the prune's", calls.Load())
	}

	// A prune with nothing to drop is a no-op: no generation, no change hook.
	c.RemoveSource("host-a")
	c.RemoveSource("")
	if got := c.Snapshot().Generation; got != after.Generation {
		t.Fatalf("no-op prune bumped generation to %d, want %d", got, after.Generation)
	}
	if calls.Load() != 2 {
		t.Fatalf("onChange fired %d times, want still the store's and the prune's", calls.Load())
	}
}
