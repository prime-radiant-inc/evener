package hubcore

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

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
