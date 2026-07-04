package hubcore

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestRemoteThreadCacheReadReturnsLastStored(t *testing.T) {
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
