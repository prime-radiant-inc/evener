package hubcore

import (
	"fmt"
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
	// The sources register the way the hub's host manager registers them, so
	// the cache tracks the identity generations a refresh walk captures.
	c.RegisterSource("host-a")
	c.RegisterSource("host-b")
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
	// The refresh that started before the remove: it captured the walk's
	// identity generations first, then the full pre-remove walk, host-a's
	// rows included.
	captured := c.SourceGenerations()
	inFlight := c.Snapshot()

	// The remove commits while that refresh is still walking.
	c.RemoveSource("host-a")
	afterPrune := c.Snapshot()
	if len(afterPrune.Threads) != 1 || afterPrune.Threads[0].ID != "b1" {
		t.Fatalf("threads after prune = %+v, want only host-b's row", afterPrune.Threads)
	}

	// The refresh finishes and publishes its pre-remove walk, generations
	// included. The publish must not resurrect the removed source's rows or
	// per-source snapshot: host-a's captured generation mismatches its absent
	// registration.
	c.StoreWalkSnapshot(inFlight, captured)

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

// TestRemoteThreadCacheReAddRejectsStaleWalkAdmitsFreshWalk pins the round-7
// M1 finding, which narrowed the round-6 hold: a remove/re-add churn
// re-registers the source, and the re-added name's next refresh must publish
// its rows again — the hold cannot outlive the registration it guards — but
// only the walks captured under the NEW registration may. A refresh that
// started before the remove captured the OLD registration's generation, and
// publishing it after the re-add would put the old host's sessions under the
// re-added host's identity, so the generation comparison must keep rejecting
// it while admitting a walk that captured the re-add.
func TestRemoteThreadCacheReAddRejectsStaleWalkAdmitsFreshWalk(t *testing.T) {
	c := &RemoteThreadCache{}
	c.RegisterSource("host-a")
	c.RegisterSource("host-b")
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
	// The walk starts before the remove and captures the old identity.
	staleCapture := c.SourceGenerations()
	inFlight := c.Snapshot()
	c.RemoveSource("host-a")
	if len(c.Snapshot().Threads) != 1 {
		t.Fatalf("threads after prune = %+v, want only host-b's row", c.Snapshot().Threads)
	}

	// The churn completes: the name registers again, under a new generation.
	c.RegisterSource("host-a")

	// The in-flight walk finishes and publishes its pre-remove capture. The
	// old registration's rows must not publish under the re-added identity.
	c.StoreWalkSnapshot(inFlight, staleCapture)
	for _, thread := range c.Snapshot().Threads {
		if remoteThreadOwnedBySource(thread, "host-a") {
			t.Fatalf("stale walk's thread %q published under the re-added identity", thread.ID)
		}
	}
	if _, ok := c.Snapshot().Sources["host-a"]; ok {
		t.Fatal("stale walk re-admitted host-a's per-source snapshot under the re-added identity")
	}

	// The re-added host's own walk captures the new generation and publishes.
	freshCapture := c.SourceGenerations()
	if freshCapture["host-a"] == staleCapture["host-a"] {
		t.Fatal("re-add kept the removed registration's generation; the churn must assign a new one")
	}
	fresh := RemoteThreadSnapshot{
		Threads: []appwire.Thread{
			{ID: "a2", Source: "host-a"},
			{ID: "b1", Source: "host-b"},
		},
		Complete: true,
		Sources: map[string]RemoteSourceSnapshot{
			"host-a": {Threads: []appwire.Thread{{ID: "a2", Source: "host-a"}}, Complete: true},
			"host-b": {Threads: []appwire.Thread{{ID: "b1", Source: "host-b"}}, Complete: true},
		},
	}
	c.StoreWalkSnapshot(fresh, freshCapture)
	got := c.Snapshot()
	var hostA int
	for _, thread := range got.Threads {
		if remoteThreadOwnedBySource(thread, "host-a") {
			hostA++
		}
	}
	if hostA != 1 || len(got.Threads) != 2 {
		t.Fatalf("threads after the fresh walk = %+v, want the re-added host's row published beside host-b's", got.Threads)
	}
	if _, ok := got.Sources["host-a"]; !ok {
		t.Fatal("fresh walk did not re-admit host-a's per-source snapshot")
	}

}

// TestRemoteThreadCacheRemovalLeavesNoPerNameState pins the round-7 M2
// finding: the round-6 fix recorded every removal as a tombstone that lived as
// long as the cache, so a hub churning distinct host names grew the removal set
// — and every publish's filter scan — without bound. Removal now deletes the
// source's registration generation instead of recording the name, so the
// cache's per-name bookkeeping is bounded by the live source set: a remove
// that is never re-added leaves nothing behind, and the late-walk rejection
// the round-6 semantics demand still holds, because an absent source mismatches
// every generation a walk can capture.
func TestRemoteThreadCacheRemovalLeavesNoPerNameState(t *testing.T) {
	c := &RemoteThreadCache{}
	const names = 32
	for i := range names {
		id := fmt.Sprintf("host-%02d", i)
		c.RegisterSource(id)
		c.StoreSnapshot([]appwire.Thread{{ID: "t1", Source: id}}, true)
		c.RemoveSource(id)
	}
	if got := len(c.generations); got != 0 {
		t.Fatalf("cache retains %d generation entries after %d removed sources; removal must leave no per-name state", got, names)
	}
	if got := len(c.Snapshot().Threads); got != 0 {
		t.Fatalf("threads after the churn = %d, want none", got)
	}
	// The bounded state still rejects a late walk for a removed source.
	c.RegisterSource("late")
	c.StoreSnapshot([]appwire.Thread{{ID: "t1", Source: "late"}}, true)
	captured := c.SourceGenerations()
	inFlight := c.Snapshot()
	c.RemoveSource("late")
	c.StoreWalkSnapshot(inFlight, captured)
	for _, thread := range c.Snapshot().Threads {
		if thread.Source == "late" {
			t.Fatalf("late walk resurrected thread %q for the removed source", thread.ID)
		}
	}
	if got := len(c.generations); got != 0 {
		t.Fatalf("cache retains %d generation entries after the late removal, want none", got)
	}
}

// TestRemoteThreadCacheWalkDropsSourceRemovedAfterCapture pins the round-9 M2
// finding: sourceStale read "the walk did not capture this source" as "its
// rows belong to the current registration" — but a source can register after
// the capture and be removed again before the publish, and a registration
// that no longer exists cannot own live rows. The late publish used to
// republish the removed host's rows until the next refresh tick rewrote the
// cache. The publish now treats an uncaptured source absent from the live
// generations as stale: it registered after the capture and was removed
// before the publish, so no live registration can claim its rows.
func TestRemoteThreadCacheWalkDropsSourceRemovedAfterCapture(t *testing.T) {
	c := &RemoteThreadCache{}
	// host-a is live before the walk starts, so the capture is non-empty and
	// the publish filters (a cache with nothing registered at the capture is
	// a different shape, pinned below).
	c.RegisterSource("host-a")
	captured := c.SourceGenerations()
	// The host registers after the capture — mid-walk — and the walk reads
	// its rows before the remove commits, the way a real enumeration of the
	// source registry would.
	c.RegisterSource("host-late")
	walk := RemoteThreadSnapshot{
		Threads: []appwire.Thread{
			{ID: "a1", Source: "host-a"},
			{ID: "l1", Source: "host-late"},
		},
		Complete: true,
		Sources: map[string]RemoteSourceSnapshot{
			"host-a":    {Threads: []appwire.Thread{{ID: "a1", Source: "host-a"}}, Complete: true},
			"host-late": {Threads: []appwire.Thread{{ID: "l1", Source: "host-late"}}, Complete: true},
		},
	}
	// The remove commits while the walk is still in flight: the registration
	// generation goes with the registration.
	c.RemoveSource("host-late")

	// The walk finishes and publishes what it read.
	c.StoreWalkSnapshot(walk, captured)

	got := c.Snapshot()
	for _, thread := range got.Threads {
		if thread.Source == "host-late" {
			t.Fatalf("late walk resurrected thread %q for the removed host", thread.ID)
		}
	}
	if _, ok := got.Sources["host-late"]; ok {
		t.Fatal("late walk resurrected the removed host's per-source snapshot")
	}
	if len(got.Threads) != 1 || got.Threads[0].ID != "a1" {
		t.Fatalf("threads after the late publish = %+v, want host-a's row alone", got.Threads)
	}
	if _, ok := got.Sources["host-a"]; !ok {
		t.Fatal("late publish dropped the still-live source's per-source snapshot")
	}
}

// TestRemoteThreadCacheWalkDropsSourceRemovedAndReAddedAfterCapture pins
// the round-10 finding: round 9 admitted an uncaptured-but-live source on
// the premise that a source the walk did not capture registered after the
// capture, so its rows belonged to the current registration — but the walk
// reads the rows under the registration live at the READ, and a remove and
// re-add can replace that registration before the publish. The first
// registration's rows must not publish under the re-added identity (the
// round-7 M1 harm through the uncaptured path), so an uncaptured source is
// now stale unconditionally: no read-time capture claims its rows, so no
// registration is known to own them.
func TestRemoteThreadCacheWalkDropsSourceRemovedAndReAddedAfterCapture(t *testing.T) {
	c := &RemoteThreadCache{}
	// host-a is live before the walk starts, so the walk's capture is
	// non-empty and the publish filters.
	c.RegisterSource("host-a")
	captured := c.SourceGenerations()
	// host-late registers mid-walk — after the capture, the way a runtime add
	// lands between the walk's start and its enumeration — and the walk reads
	// its row under that registration.
	c.RegisterSource("host-late")
	walk := RemoteThreadSnapshot{
		Threads: []appwire.Thread{
			{ID: "a1", Source: "host-a"},
			{ID: "l1", Source: "host-late"},
		},
		Complete: true,
		Sources: map[string]RemoteSourceSnapshot{
			"host-a":    {Threads: []appwire.Thread{{ID: "a1", Source: "host-a"}}, Complete: true},
			"host-late": {Threads: []appwire.Thread{{ID: "l1", Source: "host-late"}}, Complete: true},
		},
	}
	// The churn completes before the walk publishes: the remove drops the
	// registration the walk read, and the re-add registers the name under a
	// strictly newer generation.
	c.RemoveSource("host-late")
	c.RegisterSource("host-late")

	// The walk finishes and publishes what it read under the removed
	// registration. Nothing of host-late's may come back under the re-added
	// identity.
	c.StoreWalkSnapshot(walk, captured)

	got := c.Snapshot()
	for _, thread := range got.Threads {
		if thread.Source == "host-late" {
			t.Fatalf("late walk published thread %q under the re-added identity", thread.ID)
		}
	}
	if _, ok := got.Sources["host-late"]; ok {
		t.Fatal("late walk published the re-added name's per-source snapshot from the removed registration's rows")
	}
	if len(got.Threads) != 1 || got.Threads[0].ID != "a1" {
		t.Fatalf("threads after the late publish = %+v, want host-a's row alone", got.Threads)
	}
	if _, ok := got.Sources["host-a"]; !ok {
		t.Fatal("late publish dropped the captured still-live source's per-source snapshot")
	}
}

// TestRemoteThreadCacheWalkKeepsStillLiveSourceAddedMidWalk guards the
// immediacy the read-time capture preserves (round 10's fix (b)): a source
// that registers while the walk is already running is captured when the walk
// reads it — under the registration that owns the rows it is about to read —
// and that capture admits the rows at the publish, so a host added mid-walk
// still appears on that tick. Round 9 served this case through the
// "uncaptured but live" rule instead; the add → read → remove → re-add
// sequence broke that rule, and the read-time capture keeps the same visible
// behavior without it.
func TestRemoteThreadCacheWalkKeepsStillLiveSourceAddedMidWalk(t *testing.T) {
	c := &RemoteThreadCache{}
	c.RegisterSource("host-a")
	// The walk starts and reads host-a first, capturing its generation at
	// read time the way the background walk does.
	readGenerations := map[string]uint64{}
	generation, ok := c.SourceGeneration("host-a")
	if !ok {
		t.Fatal("registered source carries no generation at read time")
	}
	readGenerations["host-a"] = generation
	// host-late registers mid-walk, before the walk reaches it.
	c.RegisterSource("host-late")
	// The walk reaches host-late and captures the registration that owns the
	// rows it is about to read.
	generation, ok = c.SourceGeneration("host-late")
	if !ok {
		t.Fatal("mid-walk-registered source carries no generation at read time")
	}
	readGenerations["host-late"] = generation
	walk := RemoteThreadSnapshot{
		Threads: []appwire.Thread{
			{ID: "a1", Source: "host-a"},
			{ID: "l1", Source: "host-late"},
		},
		Complete: true,
		Sources: map[string]RemoteSourceSnapshot{
			"host-a":    {Threads: []appwire.Thread{{ID: "a1", Source: "host-a"}}, Complete: true},
			"host-late": {Threads: []appwire.Thread{{ID: "l1", Source: "host-late"}}, Complete: true},
		},
	}
	c.StoreWalkSnapshot(walk, readGenerations)
	got := c.Snapshot()
	var late int
	for _, thread := range got.Threads {
		if thread.Source == "host-late" {
			late++
		}
	}
	if late != 1 || len(got.Threads) != 2 {
		t.Fatalf("threads after the publish = %+v, want both sources' rows: the still-live source added mid-walk must publish", got.Threads)
	}
	if _, ok := got.Sources["host-late"]; !ok {
		t.Fatal("publish dropped the still-live source's per-source snapshot")
	}
}

// TestRemoteThreadCacheEmptyCaptureWalkStillFiltersRemovedSource pins the
// fresh-hub variant of the round-9 M2 finding: a walk that captured nothing
// (no source was registered yet) is still a walk, and its publish must not
// skip the staleness filter — a host added and removed inside that one walk
// would otherwise republish. The empty capture differs from the walk-free
// publish (StoreSnapshotData), which passes no capture at all and stays
// unfiltered.
func TestRemoteThreadCacheEmptyCaptureWalkStillFiltersRemovedSource(t *testing.T) {
	c := &RemoteThreadCache{}
	// The walk starts on a hub with nothing registered: the capture is
	// empty, but it exists — SourceGenerations returns a map, never nil.
	captured := c.SourceGenerations()
	if captured == nil {
		t.Fatal("SourceGenerations returned nil; the fixture needs an empty capture, not a walk-free publish")
	}
	c.RegisterSource("host-late")
	walk := RemoteThreadSnapshot{
		Threads:  []appwire.Thread{{ID: "l1", Source: "host-late"}},
		Complete: true,
		Sources: map[string]RemoteSourceSnapshot{
			"host-late": {Threads: []appwire.Thread{{ID: "l1", Source: "host-late"}}, Complete: true},
		},
	}
	c.RemoveSource("host-late")
	c.StoreWalkSnapshot(walk, captured)
	if got := c.Snapshot().Threads; len(got) != 0 {
		t.Fatalf("threads after the late publish = %+v, want none: an empty capture is still a walk's capture", got)
	}
	if _, ok := c.Snapshot().Sources["host-late"]; ok {
		t.Fatal("late walk resurrected the removed host's per-source snapshot")
	}
}
