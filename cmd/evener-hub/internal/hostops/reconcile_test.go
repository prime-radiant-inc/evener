package hostops

// The generation-mirror reconciliation (spec 08b §4, §7). These tests pin the
// store half of the bidirectional boot pass: the rollback and its torn-write
// recovery, the push-forward, the preserve arms, and the one-atomic-write
// discipline.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedStoreForTest mutates the store's state and commits it through the store's
// own atomic write, so a fixture the writers never emit still has to pass the
// same validation a real write passes.
func seedStoreForTest(t *testing.T, store *Store, mutate func(*snapshot)) {
	t.Helper()
	store.cell.mu.Lock()
	defer store.cell.mu.Unlock()
	next := cloneSnapshot(store.cell.state)
	mutate(&next)
	if _, err := store.commitLocked(next); err != nil {
		t.Fatalf("seed store state: %v", err)
	}
}

// TestReconcileMirrorRollsBackATornStoreMirror pins §4's rollback: a store
// mirror newer than the file's mark with no matching commit marker rolls back
// to the file's identity, the discarded number survives as the name's
// generation (the high-water the caller writes back), the discarded
// generation's in-flight record transitions to interrupted naming the torn
// write, its dedup tombstone drops, and the terminal record and the current
// generation's tombstone stay.
func TestReconcileMirrorRollsBackATornStoreMirror(t *testing.T) {
	store, path := openTestStore(t)
	if err := store.MirrorBoundaries(map[string]Boundary{
		"m4": {Generation: 9, IncarnationID: "inc-9", PresenceEpoch: 5},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	inflight, err := store.Create(NewRecord{ClientOperationID: "op-1", Host: "m4", Kind: KindDeploy, Generation: 9, IncarnationID: "inc-9"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	terminal, err := store.Create(NewRecord{ClientOperationID: "op-2", Host: "m4", Kind: KindRestart, Generation: 9, IncarnationID: "inc-9"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(terminal.ID, StateComplete, func(record *Record) { record.Result = &Result{OK: true, Message: "done"} }); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	current, err := store.Create(NewRecord{ClientOperationID: "op-3", Host: "m4", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-7"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	discardedTombstone := tombstoneOf(Record{
		ID: formatAllocatorID(42), ClientOperationID: "op-42", Host: "m4", Kind: KindDeploy,
		State: StateComplete, Generation: 9, IncarnationID: "inc-9",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Result: &Result{OK: true, Message: "done"},
	}, time.Now().UTC(), 1)
	keptTombstone := tombstoneOf(Record{
		ID: formatAllocatorID(43), ClientOperationID: "op-43", Host: "m4", Kind: KindRestart,
		State: StateComplete, Generation: 7, IncarnationID: "inc-7",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Result: &Result{OK: true, Message: "done"},
	}, time.Now().UTC(), 1)
	seedStoreForTest(t, store, func(state *snapshot) {
		state.CompactSeq = 1
		state.Tombstones = []Tombstone{discardedTombstone, keptTombstone}
	})

	result, err := store.ReconcileMirror(MirrorView{
		Marks:   map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 4}},
		Live:    map[string]struct{}{"m4": {}},
		Commits: nil,
	})
	if err != nil {
		t.Fatalf("ReconcileMirror: %v", err)
	}
	if len(result.RolledBack) != 1 || result.RolledBack[0] != "m4" {
		t.Fatalf("RolledBack = %v, want [m4]", result.RolledBack)
	}
	want := Boundary{Generation: 9, IncarnationID: "inc-7", PresenceEpoch: 4}
	if result.HighWater["m4"] != want {
		t.Fatalf("HighWater[m4] = %+v, want %+v", result.HighWater["m4"], want)
	}
	if result.RecordsMoved != 1 || result.TombstonesDropped != 1 {
		t.Fatalf("moved=%d dropped=%d, want 1/1", result.RecordsMoved, result.TombstonesDropped)
	}
	// Everything is durable: a reopen reads the same state.
	reopened := reopenFresh(t, path)
	if boundary, ok := reopened.Boundary("m4"); !ok || boundary != want {
		t.Fatalf("mirror after the rollback = %+v, want %+v", boundary, want)
	}
	moved, ok := reopened.Record(inflight.ID)
	if !ok || moved.State != StateInterrupted || moved.Result == nil || moved.Result.Message != TornWriteNote {
		t.Fatalf("discarded generation's in-flight record = %+v, want interrupted with the torn-write note", moved)
	}
	if moved.Sequence == 0 {
		t.Fatal("the interrupted record carries no sequence stamp")
	}
	still, ok := reopened.Record(terminal.ID)
	if !ok || still.State != StateComplete {
		t.Fatalf("a terminal record naming the discarded generation moved: %+v", still)
	}
	untouched, ok := reopened.Record(current.ID)
	if !ok || untouched.State != StatePending || untouched.HostRemoved {
		t.Fatalf("a record of the file's own generation moved: %+v", untouched)
	}
	tombstones := reopened.Tombstones()
	if len(tombstones) != 1 || tombstones[0].ID != keptTombstone.ID {
		t.Fatalf("tombstones after the rollback = %+v, want only %s", tombstones, keptTombstone.ID)
	}
	// A second pass finds nothing left to do and writes nothing.
	before := storeFileBytesForTest(t, path)
	if result, err := store.ReconcileMirror(MirrorView{
		Marks: map[string]Boundary{"m4": {Generation: 9, IncarnationID: "inc-7", PresenceEpoch: 4}},
		Live:  map[string]struct{}{"m4": {}},
	}); err != nil || len(result.RolledBack) != 0 {
		t.Fatalf("second ReconcileMirror = %+v/%v, want no rollback", result, err)
	}
	if after := storeFileBytesForTest(t, path); after != before {
		t.Fatal("a converged mirror pass rewrote the store")
	}
}

// TestReconcileMirrorRollbackMovesEveryInFlightRecord pins the rollback's
// record transition: the mirrored generation is discarded and every in-flight
// record for the name moves to interrupted with the torn-write note.
func TestReconcileMirrorRollbackMovesEveryInFlightRecord(t *testing.T) {
	store, _ := openTestStore(t)
	if err := store.MirrorBoundaries(map[string]Boundary{
		"m4": {Generation: 9, IncarnationID: "inc-9", PresenceEpoch: 5},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	first, err := store.Create(NewRecord{ClientOperationID: "op-fenced", Host: "m4", Kind: KindDeploy, Generation: 9, IncarnationID: "inc-9"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := store.Create(NewRecord{ClientOperationID: "op-plain", Host: "m4", Kind: KindDeploy, Generation: 9, IncarnationID: "inc-9"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	result, err := store.ReconcileMirror(MirrorView{
		Marks: map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 4}},
		Live:  map[string]struct{}{"m4": {}},
	})
	if err != nil {
		t.Fatalf("ReconcileMirror: %v", err)
	}
	if result.RecordsMoved != 2 {
		t.Fatalf("RecordsMoved = %d, want both in-flight records moved", result.RecordsMoved)
	}
	stored, _ := store.Record(first.ID)
	if stored.State != StateInterrupted {
		t.Fatalf("the first record's state = %q, want interrupted", stored.State)
	}
	if other, _ := store.Record(second.ID); other.State != StateInterrupted {
		t.Fatalf("the second record's state = %q, want interrupted", other.State)
	}
}

// TestReconcileMirrorPushesAFileMarkForward pins §4's other direction: a file
// mark newer than the store mirror advances the mirror (or creates it), and a
// converged pair changes nothing.
func TestReconcileMirrorPushesAFileMarkForward(t *testing.T) {
	store, path := openTestStore(t)
	if err := store.MirrorBoundaries(map[string]Boundary{
		"m4": {Generation: 5, IncarnationID: "inc-5", PresenceEpoch: 1},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	mark := Boundary{Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 2}
	result, err := store.ReconcileMirror(MirrorView{
		Marks: map[string]Boundary{"m4": mark, "m7": {Generation: 3, IncarnationID: "inc-3", PresenceEpoch: 1}},
		Live:  map[string]struct{}{"m4": {}, "m7": {}},
	})
	if err != nil {
		t.Fatalf("ReconcileMirror: %v", err)
	}
	if len(result.PushedForward) != 2 {
		t.Fatalf("PushedForward = %v, want both names", result.PushedForward)
	}
	reopened := reopenFresh(t, path)
	if boundary, ok := reopened.Boundary("m4"); !ok || boundary != mark {
		t.Fatalf("m4 mirror = %+v, want the file mark %+v", boundary, mark)
	}
	if _, ok := reopened.Boundary("m7"); !ok {
		t.Fatal("the missing mirror was not created from the file mark")
	}
	// Converged: a second pass is a no-op.
	before := storeFileBytesForTest(t, path)
	if _, err := reopened.ReconcileMirror(MirrorView{
		Marks: map[string]Boundary{"m4": mark, "m7": {Generation: 3, IncarnationID: "inc-3", PresenceEpoch: 1}},
		Live:  map[string]struct{}{"m4": {}, "m7": {}},
	}); err != nil {
		t.Fatalf("second ReconcileMirror: %v", err)
	}
	if after := storeFileBytesForTest(t, path); after != before {
		t.Fatal("a converged mirror pass rewrote the store")
	}
}

// TestReconcileMirrorPreservesAMirrorWithoutAFileEntry pins the preserve arm:
// a name hub.toml carries no entry for keeps its mirror, and the surviving
// mirror is read as the high-water mark.
func TestReconcileMirrorPreservesAMirrorWithoutAFileEntry(t *testing.T) {
	store, _ := openTestStore(t)
	mirror := Boundary{Generation: 11, IncarnationID: "inc-11", PresenceEpoch: 3}
	if err := store.MirrorBoundaries(map[string]Boundary{"m4": mirror}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	result, err := store.ReconcileMirror(MirrorView{})
	if err != nil {
		t.Fatalf("ReconcileMirror: %v", err)
	}
	if len(result.RolledBack) != 0 {
		t.Fatalf("RolledBack = %v, want none", result.RolledBack)
	}
	if result.HighWater["m4"] != mirror {
		t.Fatalf("HighWater[m4] = %+v, want the surviving mirror %+v", result.HighWater["m4"], mirror)
	}
	if boundary, ok := store.Boundary("m4"); !ok || boundary != mirror {
		t.Fatalf("mirror = %+v, want it preserved as %+v", boundary, mirror)
	}
	// A newer mirror with only a bare generations mark (no live entry, no
	// tombstone) is preserved too: the rollback needs a live entry or tombstone.
	result, err = store.ReconcileMirror(MirrorView{Marks: map[string]Boundary{"m4": {Generation: 9, IncarnationID: "inc-9", PresenceEpoch: 2}}})
	if err != nil {
		t.Fatalf("ReconcileMirror(bare mark): %v", err)
	}
	if len(result.RolledBack) != 0 || result.HighWater["m4"] != mirror {
		t.Fatalf("bare-mark pass = %+v, want the mirror preserved as high-water", result)
	}
}

// TestReconcileMirrorKeepsAMarkerAuthorizedMirror pins the marker's authority:
// a mirror the file's own commit marker names stands, even where it is newer
// than the file's mark.
func TestReconcileMirrorKeepsAMarkerAuthorizedMirror(t *testing.T) {
	store, path := openTestStore(t)
	mirror := Boundary{Generation: 9, IncarnationID: "inc-9", PresenceEpoch: 5}
	if err := store.MirrorBoundaries(map[string]Boundary{"m4": mirror}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	result, err := store.ReconcileMirror(MirrorView{
		Marks:   map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 4}},
		Live:    map[string]struct{}{"m4": {}},
		Commits: map[string]MirrorCommit{"m4": {HubTOMLGeneration: 7, StoreGeneration: 9}},
	})
	if err != nil {
		t.Fatalf("ReconcileMirror: %v", err)
	}
	if len(result.RolledBack) != 0 || len(result.HighWater) != 0 {
		t.Fatalf("marker-authorized pass = %+v, want no change", result)
	}
	if boundary, ok := store.Boundary("m4"); !ok || boundary != mirror {
		t.Fatalf("mirror = %+v, want the authorized %+v", boundary, mirror)
	}
	// The pass wrote nothing.
	before := storeFileBytesForTest(t, path)
	if _, err := store.ReconcileMirror(MirrorView{
		Marks:   map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 4}},
		Live:    map[string]struct{}{"m4": {}},
		Commits: map[string]MirrorCommit{"m4": {HubTOMLGeneration: 7, StoreGeneration: 9}},
	}); err != nil {
		t.Fatalf("second ReconcileMirror: %v", err)
	}
	if after := storeFileBytesForTest(t, path); after != before {
		t.Fatal("an authorized mirror pass rewrote the store")
	}
}

// TestReconcileMirrorIsOneAtomicWrite pins the fault seam: a failure before the
// rename leaves the file (and memory) exactly as they were.
func TestReconcileMirrorIsOneAtomicWrite(t *testing.T) {
	store, path := openTestStore(t)
	if err := store.MirrorBoundaries(map[string]Boundary{
		"m4": {Generation: 9, IncarnationID: "inc-9", PresenceEpoch: 5},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	before := storeFileBytesForTest(t, path)
	store.faults.beforeRename = func() error { return errors.New("injected crash before the rename") }
	if _, err := store.ReconcileMirror(MirrorView{
		Marks: map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 4}},
		Live:  map[string]struct{}{"m4": {}},
	}); err == nil {
		t.Fatal("the failing pass reported success")
	}
	if after := storeFileBytesForTest(t, path); after != before {
		t.Fatal("a pre-rename failure changed the store file")
	}
	if boundary, ok := store.Boundary("m4"); !ok || boundary.IncarnationID != "inc-9" {
		t.Fatalf("memory adopted a rollback the file never got: %+v", boundary)
	}
	store.faults.beforeRename = nil
	if _, err := store.ReconcileMirror(MirrorView{
		Marks: map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 4}},
		Live:  map[string]struct{}{"m4": {}},
	}); err != nil {
		t.Fatalf("retried ReconcileMirror: %v", err)
	}
	if boundary, ok := store.Boundary("m4"); !ok || boundary.IncarnationID != "inc-7" {
		t.Fatalf("mirror after the retry = %+v, want the rolled-back identity", boundary)
	}
}

// TestReconcileMirrorRefusesAMalformedView pins the schema: a mark outside the
// boundary schema or a commit marker with a zero generation is refused with
// nothing written.
func TestReconcileMirrorRefusesAMalformedView(t *testing.T) {
	store, _ := openTestStore(t)
	for name, view := range map[string]MirrorView{
		"zero generation mark": {Marks: map[string]Boundary{"m4": {Generation: 0, IncarnationID: "inc", PresenceEpoch: 1}}},
		"empty incarnation":    {Marks: map[string]Boundary{"m4": {Generation: 7, PresenceEpoch: 1}}},
		"zero marker":          {Commits: map[string]MirrorCommit{"m4": {HubTOMLGeneration: 7}}},
	} {
		if _, err := store.ReconcileMirror(view); err == nil {
			t.Fatalf("ReconcileMirror(%s) succeeded, want a refusal", name)
		}
	}
}

// TestReconcileMirrorPushForwardSkipsAnExpiredRemovedName pins the shared skip
// rule: a removed name past its horizon with no records keeps no mirror, so a
// boot cannot recreate a boundary the next mutation's write would prune again.
func TestReconcileMirrorPushForwardSkipsAnExpiredRemovedName(t *testing.T) {
	store, _ := openTestStore(t)
	if err := store.MirrorHostState(HostMirror{
		Removed: map[string]RemovedHost{"gone": {
			RemovedAt:     time.Now().UTC().Add(-8 * 24 * time.Hour),
			Generation:    7,
			IncarnationID: "inc-7",
		}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	result, err := store.ReconcileMirror(MirrorView{
		Marks: map[string]Boundary{"gone": {Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 2}},
	})
	if err != nil {
		t.Fatalf("ReconcileMirror: %v", err)
	}
	if len(result.PushedForward) != 0 {
		t.Fatalf("PushedForward = %v, want the expired removed name skipped", result.PushedForward)
	}
	if _, ok := store.Boundary("gone"); ok {
		t.Fatal("the boot recreated a removed name's pruned boundary")
	}
}

// TestReconcileMirrorRequiresBothMarkerGenerationsToMatch pins the marker's
// authority: a marker authorizes a mirror only when BOTH of its generations
// match the file's current mark and the store mirror — a stale hub.toml
// generation naming the same store value authorizes nothing.
func TestReconcileMirrorRequiresBothMarkerGenerationsToMatch(t *testing.T) {
	store, _ := openTestStore(t)
	if err := store.MirrorBoundaries(map[string]Boundary{
		"m4": {Generation: 9, IncarnationID: "inc-9", PresenceEpoch: 5},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	result, err := store.ReconcileMirror(MirrorView{
		Marks:   map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-7", PresenceEpoch: 4}},
		Live:    map[string]struct{}{"m4": {}},
		Commits: map[string]MirrorCommit{"m4": {HubTOMLGeneration: 5, StoreGeneration: 9}},
	})
	if err != nil {
		t.Fatalf("ReconcileMirror: %v", err)
	}
	if len(result.RolledBack) != 1 {
		t.Fatalf("a stale marker authorized the mirror: %+v", result)
	}
	if boundary, ok := store.Boundary("m4"); !ok || boundary.IncarnationID != "inc-7" {
		t.Fatalf("mirror after the rollback = %+v, want the file mark's identity", boundary)
	}
}

// TestClearHostRemovedMarksReversesOnlyTheRestoredPair pins §4's reversal for a
// compensation: only the restored incarnation's records are unmarked, another
// incarnation's mark stands, and a pass with nothing to clear writes nothing.
func TestClearHostRemovedMarksReversesOnlyTheRestoredPair(t *testing.T) {
	store, path := openTestStore(t)
	restored, err := store.Create(NewRecord{ClientOperationID: "op-restored", Host: "m4", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-7"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	other, err := store.Create(NewRecord{ClientOperationID: "op-other", Host: "m4", Kind: KindRestart, Generation: 9, IncarnationID: "inc-9"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := store.ApplyHostRemovedPass(map[string]HostRemovedMark{
		"m4": {Generation: 7, IncarnationID: "inc-7"},
	}); err != nil {
		t.Fatalf("ApplyHostRemovedPass(7): %v", err)
	}
	if _, _, err := store.ApplyHostRemovedPass(map[string]HostRemovedMark{
		"m4": {Generation: 9, IncarnationID: "inc-9"},
	}); err != nil {
		t.Fatalf("ApplyHostRemovedPass(9): %v", err)
	}
	cleared, err := store.ClearHostRemovedMarks("m4", HostRemovedMark{Generation: 7, IncarnationID: "inc-7"})
	if err != nil {
		t.Fatalf("ClearHostRemovedMarks: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("ClearHostRemovedMarks cleared %d, want 1", cleared)
	}
	reopened := reopenFresh(t, path)
	back, ok := reopened.Record(restored.ID)
	if !ok || back.HostRemoved {
		t.Fatalf("the restored incarnation's record = %+v/%v, want its mark cleared", back, ok)
	}
	still, ok := reopened.Record(other.ID)
	if !ok || !still.HostRemoved {
		t.Fatalf("another incarnation's record = %+v/%v, want its mark kept", still, ok)
	}
	// Idempotent: a second clear finds nothing and writes nothing.
	before := storeFileBytesForTest(t, path)
	if cleared, err := reopened.ClearHostRemovedMarks("m4", HostRemovedMark{Generation: 7, IncarnationID: "inc-7"}); err != nil || cleared != 0 {
		t.Fatalf("second ClearHostRemovedMarks = %d/%v, want 0/nil", cleared, err)
	}
	if after := storeFileBytesForTest(t, path); after != before {
		t.Fatal("a no-op mark clear rewrote the store")
	}
}

// TestLoadRefusesNonCanonicalPendingCompensationKeys pins the store's key rule
// for the new section: the compensation record's own keys and its preimage
// rows' keys are canonical, so a case variant (which the decoder matches
// case-insensitively and would silently rewrite) is refused at load, exactly
// as it is for every other object this store decodes.
func TestLoadRefusesNonCanonicalPendingCompensationKeys(t *testing.T) {
	value, err := newTokenValue()
	if err != nil {
		t.Fatalf("newTokenValue: %v", err)
	}
	row := tokenRowJSON("m4", value, tokenEpoch, tokenEpoch.Add(5*time.Minute), tokenEpoch)
	for name, raw := range map[string]string{
		"record key": `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],` +
			`"pendingCompensation":{"m4":{"Host":"m4","phase":"compensating-armed","rows":[` + row + `],"stashReference":"stash","generation":7}}}`,
		"row key": `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],` +
			`"pendingCompensation":{"m4":{"host":"m4","phase":"compensating-armed","rows":[` + strings.Replace(row, `"host":"m4"`, `"Host":"m4"`, 1) + `],"stashReference":"stash","generation":7}}}`,
	} {
		path := StorePath(t.TempDir())
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatalf("write store: %v", err)
		}
		if _, err := Open(path); err == nil {
			t.Fatalf("the store loaded a pendingCompensation %s outside the canonical spelling", name)
		}
	}
}
