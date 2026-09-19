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

// TestRemoteThreadCacheRemoveSourceHoldsBackInFlightRefreshPublish pins the
// round-6 M2 finding: RemoveSource pruned only the current snapshot, so a
// remote-thread refresh that started before the remove could finish afterwards
// and republish the removed source's rows — making the removed host's sessions
// visible again until the next refresh cycle. The removal now also marks the
// source as removed, and the publish filters it, so a late publish is a no-op
// rather than a resurrection.
func TestRemoteThreadCacheRemoveSourceHoldsBackInFlightRefreshPublish(t *testing.T) {
	c := &RemoteThreadCache{}
	var calls atomic.Int32
	c.SetOnChange(func() { calls.Add(1) })
	seed := RemoteThreadSnapshot{
		Threads: []appwire.Thread{
			{ID: "a1", Source: "host-a"},
			{ID: "a2", Source: "", Evener: appwire.EvenerThread{Ref: "host-a:a2"}},
			{ID: "b1", Source: "host-b"},
		},
		Complete: true,
		Sources: map[string]RemoteSourceSnapshot{
			"host-a": {Threads: []appwire.Thread{{ID: "a1", Source: "host-a"}}, Complete: true},
			"host-b": {Threads: []appwire.Thread{{ID: "b1", Source: "host-b"}}, Complete: true},
		},
	}
	c.StoreSnapshotData(seed)
	if calls.Load() != 1 {
		t.Fatalf("onChange fired %d times after the seed, want 1", calls.Load())
	}
	// The refresh that started before the remove: it captured the full
	// pre-remove walk, host-a's rows included.
	inFlight := c.Snapshot()

	// The remove commits while that refresh is still walking.
	c.RemoveSource("host-a")
	afterPrune := c.Snapshot()
	if len(afterPrune.Threads) != 1 || afterPrune.Threads[0].ID != "b1" {
		t.Fatalf("threads after prune = %+v, want only host-b's row", afterPrune.Threads)
	}

	// The refresh finishes and publishes its pre-remove walk. The publish
	// must not resurrect the removed source's rows or per-source snapshot.
	c.StoreSnapshotData(inFlight)

	got := c.Snapshot()
	for _, thread := range got.Threads {
		if remoteThreadOwnedBySource(thread, "host-a") {
			t.Fatalf("in-flight refresh resurrected thread %q for the removed source", thread.ID)
		}
	}
	if _, ok := got.Sources["host-a"]; ok {
		t.Fatal("in-flight refresh resurrected the removed source's per-source snapshot")
	}
	if len(got.Threads) != 1 || got.Threads[0].ID != "b1" {
		t.Fatalf("threads after the late publish = %+v, want host-b's row intact and nothing else", got.Threads)
	}
	if _, ok := got.Sources["host-b"]; !ok {
		t.Fatal("late publish dropped host-b's per-source snapshot")
	}
	// The filtered publish matches what the prune already published, so it is
	// a no-op: no new generation, no change hook.
	if got.Generation != afterPrune.Generation {
		t.Fatalf("late publish bumped generation to %d, want the prune's %d", got.Generation, afterPrune.Generation)
	}
	if calls.Load() != 2 {
		t.Fatalf("onChange fired %d times, want the seed's and the prune's only", calls.Load())
	}
}

// TestRemoteThreadCacheRestoreSourceLiftsTheRemovalRecord pins the other half
// of the round-6 M2 fix: the removal record RestoreSource clears is a hold,
// not a permanent ban. A remove/re-add churn re-registers the source, so the
// re-added name's next refresh must publish its rows again — the hold cannot
// outlive the registration it guards.
func TestRemoteThreadCacheRestoreSourceLiftsTheRemovalRecord(t *testing.T) {
	c := &RemoteThreadCache{}
	seed := RemoteThreadSnapshot{
		Threads: []appwire.Thread{
			{ID: "a1", Source: "host-a"},
			{ID: "b1", Source: "host-b"},
		},
		Complete: true,
		Sources: map[string]RemoteSourceSnapshot{
			"host-a": {Threads: []appwire.Thread{{ID: "a1", Source: "host-a"}}, Complete: true},
			"host-b": {Threads: []appwire.Thread{{ID: "b1", Source: "host-b"}}, Complete: true},
		},
	}
	c.StoreSnapshotData(seed)
	inFlight := c.Snapshot()
	c.RemoveSource("host-a")

	// The hold is active: the same publish carries nothing for host-a.
	c.StoreSnapshotData(inFlight)
	for _, thread := range c.Snapshot().Threads {
		if remoteThreadOwnedBySource(thread, "host-a") {
			t.Fatalf("held publish resurrected thread %q for the removed source", thread.ID)
		}
	}

	// The churn re-registers the source: the record lifts, and the next
	// publish — the same walk again — carries the re-added host's rows.
	c.RestoreSource("host-a")
	c.StoreSnapshotData(inFlight)
	got := c.Snapshot()
	var hostA int
	for _, thread := range got.Threads {
		if remoteThreadOwnedBySource(thread, "host-a") {
			hostA++
		}
	}
	if hostA != 1 || len(got.Threads) != 2 {
		t.Fatalf("threads after restore = %+v, want host-a's row published beside host-b's", got.Threads)
	}
	if _, ok := got.Sources["host-a"]; !ok {
		t.Fatal("restore did not re-admit host-a's per-source snapshot")
	}

	// A restore for a name that was never removed is a no-op.
	c.RestoreSource("host-b")
	if same := c.Snapshot(); same.Generation != got.Generation {
		t.Fatalf("no-op restore changed the snapshot: generation %d, want %d", same.Generation, got.Generation)
	}
}
