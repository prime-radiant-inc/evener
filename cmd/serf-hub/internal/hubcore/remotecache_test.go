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
