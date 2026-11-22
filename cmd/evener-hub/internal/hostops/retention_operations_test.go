package hostops

// Retention-backed operations read tests (deploy pipeline 08b §4, §8, §10):
// the compacted-record detail replay, the tombstone-once filter behavior, and
// the removal-marker replay rules. The cursor-invalidated machinery these tests
// once pinned is retired with the pagination cursor.

import (
	"testing"
	"time"
)

// TestOperationsDetailResolvesACompactedRecord pins §4's retrieval path: the
// `id` detail filter (and the operationId filter) resolves a compacted record
// out of its dedup tombstone with `compacted: true`, while listing pages stay
// record-only.
func TestOperationsDetailResolvesACompactedRecord(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	mirrorTestBoundary(t, store, "m4", 7, "inc-m4", 3)
	compacted := createOp(t, store, "m4", "op-compacted")
	finish(t, store, compacted.ID)
	kept := createOp(t, store, "m4", "op-kept")
	finish(t, store, kept.ID) // compacts the first record.

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", ID: compacted.ID})
	if err != nil {
		t.Fatalf("ReadOperations(id detail): %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("id detail listed %d records, want the compacted replay", len(page.Records))
	}
	replay := page.Records[0]
	if replay.ID != compacted.ID || !replay.Compacted || replay.State != StateComplete {
		t.Fatalf("id detail replay = %+v, want the compacted complete record %s", replay, compacted.ID)
	}
	if replay.Result == nil || !replay.Result.OK || replay.Result.Message == "" {
		t.Fatalf("id detail replay result = %+v, want the retained outcome", replay.Result)
	}

	page, err = store.ReadOperations(OperationsQuery{Host: "m4", ClientOperationID: "op-compacted"})
	if err != nil {
		t.Fatalf("ReadOperations(operationId filter): %v", err)
	}
	found := false
	for _, record := range page.Records {
		if record.ID == compacted.ID && record.Compacted {
			found = true
		}
	}
	if !found {
		t.Fatalf("operationId filter listed %+v, want the compacted replay", page.Records)
	}

	// Listing pages never surface tombstones: the replay is a detail
	// resolution, not a listed row.
	page, err = store.ReadOperations(OperationsQuery{Host: "m4"})
	if err != nil {
		t.Fatalf("ReadOperations(list): %v", err)
	}
	for _, record := range page.Records {
		if record.ID == compacted.ID {
			t.Fatalf("list page surfaced the compacted record %s", compacted.ID)
		}
	}
}

// TestOperationIDFilterListsATombstoneOnce pins that the operationId filter's
// tombstone replay is served exactly once: a listing is one bounded window, so
// the same read never appends the tombstone twice.
func TestOperationIDFilterListsATombstoneOnce(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	mirrorTestBoundary(t, store, "m4", 7, "inc-m4", 3)
	r1 := createOp(t, store, "m4", "op-a")
	finish(t, store, r1.ID)
	r2 := createOp(t, store, "m4", "op-b")
	finish(t, store, r2.ID) // compacts op-a into a tombstone.

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", ClientOperationID: "op-a", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(op-a): %v", err)
	}
	if len(page.Records) != 1 || !page.Records[0].Compacted || page.Records[0].ID != r1.ID {
		t.Fatalf("page = %+v, want the compacted op-a replay once", page.Records)
	}
}

// TestOperationsDetailResolvesACompactedRecordWithoutABoundary pins the direct
// id lookup over a removed host whose last record compacted past the horizon:
// the boundary drops with the record while the tombstone stays, and a direct
// `id` lookup must address the retained tombstone anyway — the unfiltered and
// host-pinned spellings agree, both returning the compacted: true replay.
func TestOperationsDetailResolvesACompactedRecordWithoutABoundary(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 5, RemovedHostHorizon: time.Hour})
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }
	if err := store.MirrorBoundaries(map[string]Boundary{
		"gone": {Generation: 7, IncarnationID: "inc-gone", PresenceEpoch: 2},
		"live": {Generation: 7, IncarnationID: "inc-live", PresenceEpoch: 1},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	record := createOp(t, store, "gone", "gone-1")
	finish(t, store, record.ID)
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"gone": {Generation: 7, IncarnationID: "inc-gone", PresenceEpoch: 2}},
		Removed:    map[string]RemovedHost{"gone": {RemovedAt: base.Add(-30 * time.Minute), Generation: 7, IncarnationID: "inc-gone"}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	store.clock = func() time.Time { return base.Add(2 * time.Hour) }
	other := createOp(t, store, "live", "live-1")
	finish(t, store, other.ID)
	if _, ok := store.Boundary("gone"); ok {
		t.Fatal("test setup: the removed host's boundary was not dropped")
	}
	if len(store.Tombstones()) == 0 {
		t.Fatal("test setup: the removed host kept no tombstone")
	}

	unfiltered, err := store.ReadOperations(OperationsQuery{ID: record.ID})
	if err != nil {
		t.Fatalf("unfiltered detail: %v", err)
	}
	pinned, err := store.ReadOperations(OperationsQuery{Host: "gone", ID: record.ID})
	if err != nil {
		t.Fatalf("pinned detail: %v", err)
	}
	for name, page := range map[string]OperationsPage{"unfiltered": unfiltered, "pinned": pinned} {
		if len(page.Records) != 1 {
			t.Fatalf("%s detail listed %d records, want the compacted replay", name, len(page.Records))
		}
		replay := page.Records[0]
		if replay.ID != record.ID || !replay.Compacted || replay.Result == nil || !replay.Result.OK {
			t.Fatalf("%s detail = %+v, want the compacted replay with its outcome", name, replay)
		}
	}
}
