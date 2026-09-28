package hostops

// Retention, compaction, and dedup-tombstone tests (deploy pipeline 08b §4,
// §11, §12's compaction/tombstone rows). S6's store-level half: the per-host
// and global bounds, the terminal-age bound, removed-host-first ordering past
// the tombstoneRetention horizon, the bounded dedup tombstones and their
// compacted:true replay rule.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// openRetentionStore opens a store under a fresh temp root with the policy.
func openRetentionStore(t *testing.T, policy RetentionPolicy) (*Store, string) {
	t.Helper()
	path := StorePath(t.TempDir())
	store, err := OpenWithRetention(path, policy)
	if err != nil {
		t.Fatalf("OpenWithRetention(%s): %v", path, err)
	}
	return store, path
}

// terminalChange is the change callback every terminal transition in these
// tests carries: spec §10 requires a terminal record's result, so a nil
// callback is no longer a legal terminal move.
func terminalChange(ok bool) func(*Record) {
	return func(r *Record) { r.Result = &Result{OK: ok, Message: "test outcome"} }
}

// createOp persists one pending deploy record and returns it.
func createOp(t *testing.T, store *Store, host, clientOperationID string) Record {
	t.Helper()
	return createOpPair(t, store, host, clientOperationID, 7, "inc-"+host)
}

// createOpPair persists one pending deploy record pinned to an explicit pair.
func createOpPair(t *testing.T, store *Store, host, clientOperationID string, generation uint64, incarnationID string) Record {
	t.Helper()
	record, err := store.Create(NewRecord{
		ClientOperationID: clientOperationID,
		Host:              host,
		Kind:              KindDeploy,
		Generation:        generation,
		IncarnationID:     incarnationID,
	})
	if err != nil {
		t.Fatalf("Create(%s/%s): %v", host, clientOperationID, err)
	}
	return record
}

// finish moves a record to complete, landing its terminal state.
func finish(t *testing.T, store *Store, id string) Record {
	t.Helper()
	record, err := store.Transition(id, StateComplete, func(r *Record) {
		r.Result = &Result{OK: true, Message: "done"}
	})
	if err != nil {
		t.Fatalf("Transition(%s): %v", id, err)
	}
	return record
}

// TestCompactionBoundsTerminalRecordsPerHostAndLeavesTombstones pins §4:
// "the store keeps at most 50 terminal records per host ... A write that would
// exceed a global bound first compacts oldest-terminal-first ... Compaction
// leaves a bounded store dedup tombstone per compacted record". The per-host
// bound is lowered so the test can watch one write remove one record.
func TestCompactionBoundsTerminalRecordsPerHostAndLeavesTombstones(t *testing.T) {
	store, path := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 2})

	first := createOp(t, store, "m4", "op-1")
	second := createOp(t, store, "m4", "op-2")
	third := createOp(t, store, "m4", "op-3")
	finish(t, store, first.ID)
	finish(t, store, second.ID)
	if got := len(store.Records()); got != 3 {
		t.Fatalf("records after two terminal lands = %d, want 3", got)
	}
	finish(t, store, third.ID)

	records := store.Records()
	if len(records) != 2 {
		t.Fatalf("terminal records after the over-bound land = %d, want 2", len(records))
	}
	if _, ok := store.Record(first.ID); ok {
		t.Fatalf("the oldest terminal record %s survived the per-host bound", first.ID)
	}
	tombstones := store.Tombstones()
	if len(tombstones) != 1 {
		t.Fatalf("tombstones = %d, want 1", len(tombstones))
	}
	tombstone := tombstones[0]
	if tombstone.ID != first.ID || tombstone.ClientOperationID != "op-1" || tombstone.Host != "m4" ||
		tombstone.Kind != KindDeploy || tombstone.Generation != 7 || tombstone.IncarnationID != "inc-m4" ||
		tombstone.State != StateComplete {
		t.Fatalf("tombstone = %+v, want the compacted first record's full scope", tombstone)
	}
	if tombstone.CompactedAt.IsZero() {
		t.Fatal("tombstone carries no compactedAt")
	}
	if store.CursorEpoch().CompactSeq != 1 {
		t.Fatalf("compactSeq = %d, want 1 after one compacting write", store.CursorEpoch().CompactSeq)
	}

	// §4: "A replay naming a tombstoned ID returns the full retained record
	// with `compacted: true` instead of opening a fresh operation" — the pinned
	// pair still equals the live comparison pair (the host did not move).
	replayed, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "op-1",
		Host:              "m4",
		Kind:              KindDeploy,
		Current:           OperationPair{Generation: 7, IncarnationID: "inc-m4"},
	})
	if err != nil || !hit {
		t.Fatalf("LookupOperation(tombstoned op-1) = hit %v, err %v; want a replay", hit, err)
	}
	if !replayed.Compacted {
		t.Fatal("the replayed record does not carry compacted: true")
	}
	if replayed.ID != first.ID || replayed.State != StateComplete {
		t.Fatalf("replayed record = %+v, want the retained tombstone record", replayed)
	}

	// The file holds exactly what the live store serves: a fresh load keeps the
	// tombstones and the compactSeq.
	reloaded := reopenFresh(t, path)
	if got := len(reloaded.Tombstones()); got != 1 {
		t.Fatalf("reloaded tombstones = %d, want 1", got)
	}
	if reloaded.CursorEpoch().CompactSeq != 1 {
		t.Fatalf("reloaded compactSeq = %d, want 1", reloaded.CursorEpoch().CompactSeq)
	}
	replayed, hit, err = reloaded.LookupOperation(OperationDedupQuery{
		ClientOperationID: "op-1", Host: "m4", Kind: KindDeploy,
		Current: OperationPair{Generation: 7, IncarnationID: "inc-m4"},
	})
	if err != nil || !hit || !replayed.Compacted {
		t.Fatalf("reloaded replay = hit %v compacted %v err %v; want the tombstone replay", hit, replayed.Compacted, err)
	}
}

// TestCompactionNeverRemovesNonTerminalRecords pins §4: the bounds are on
// "terminal records ... plus every non-terminal record regardless of count".
func TestCompactionNeverRemovesNonTerminalRecords(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	pending := createOp(t, store, "m4", "op-pending")
	a := createOp(t, store, "m4", "op-a")
	b := createOp(t, store, "m4", "op-b")
	finish(t, store, a.ID)
	finish(t, store, b.ID)

	if _, ok := store.Record(pending.ID); !ok {
		t.Fatal("a non-terminal record was compacted")
	}
	if _, ok := store.Record(a.ID); ok {
		t.Fatal("the oldest terminal record survived the per-host bound")
	}
	if _, ok := store.Record(b.ID); !ok {
		t.Fatal("the newest terminal record was compacted")
	}
}

// TestCompactionNeverTouchesHostRemovedMarksOfRetainedRecords pins §4:
// "Compaction never touches `host-removed` marks of retained records." A
// retained record's mark survives its neighbours' compaction, and a compacted
// record's tombstone carries the mark it had.
func TestCompactionNeverTouchesHostRemovedMarksOfRetainedRecords(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 3})
	// The mark write path the store owns: a transition's change callback
	// carries the host-removed mark, exactly as the boot pass applies it.
	compactedMarked := createOp(t, store, "m4", "op-compacted")
	if _, err := store.Transition(compactedMarked.ID, StateComplete, func(r *Record) { r.HostRemoved = true; r.Result = &Result{OK: true, Message: "done"} }); err != nil {
		t.Fatalf("Transition(compactedMarked): %v", err)
	}
	newest := createOp(t, store, "m4", "op-newest")
	finish(t, store, newest.ID)
	retainedMarked := createOp(t, store, "m4", "op-retained")
	if _, err := store.Transition(retainedMarked.ID, StateComplete, func(r *Record) { r.HostRemoved = true; r.Result = &Result{OK: true, Message: "done"} }); err != nil {
		t.Fatalf("Transition(retainedMarked): %v", err)
	}
	// The fourth terminal land exceeds the per-host bound: the oldest terminal
	// record (compactedMarked) compacts; the marked retained one must not be
	// touched.
	third := createOp(t, store, "m4", "op-third")
	finish(t, store, third.ID)

	if _, ok := store.Record(compactedMarked.ID); ok {
		t.Fatal("the oldest terminal record survived the per-host bound")
	}
	retained, ok := store.Record(retainedMarked.ID)
	if !ok {
		t.Fatal("the marked retained record was compacted")
	}
	if !retained.HostRemoved {
		t.Fatal("compaction cleared a retained record's host-removed mark")
	}
	tombstones := store.Tombstones()
	if len(tombstones) != 1 || !tombstones[0].HostRemoved {
		t.Fatalf("tombstones = %+v, want the compacted record's mark carried over", tombstones)
	}
}

// TestRemovedHostHistoryCompactsOnlyPastTheHorizon pins §4: "Safe compaction
// of removed-host history: once a removed host's records sit past the
// `tombstoneRetention` horizon its terminal records compact ... Only past that
// horizon — the documented, owner-visible horizon of the lost-response retry
// contract — does a replay open fresh", with registry spec §15's 7-day default
// horizon.
func TestRemovedHostHistoryCompactsOnlyPastTheHorizon(t *testing.T) {
	policy := RetentionPolicy{
		TerminalPerHost:    50,
		TerminalStoreWide:  50,
		TombstonesPerHost:  1,
		RemovedHostHorizon: 7 * 24 * time.Hour,
	}
	store, _ := openRetentionStore(t, policy)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }

	liveRecord := createOp(t, store, "live", "live-old")
	finish(t, store, liveRecord.ID)
	removedOld := createOp(t, store, "gone", "gone-old")
	finish(t, store, removedOld.ID)
	removedNew := createOp(t, store, "gone", "gone-new")
	finish(t, store, removedNew.ID)

	// The removal lands two days before base: inside the 7-day horizon.
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{
			"live": {Generation: 7, IncarnationID: "inc-live", PresenceEpoch: 1},
			"gone": {Generation: 7, IncarnationID: "inc-gone", PresenceEpoch: 2},
		},
		Live:    []string{"live"},
		Removed: map[string]RemovedHost{"gone": {RemovedAt: base.Add(-48 * time.Hour), Generation: 7, IncarnationID: "inc-gone"}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	// A later write runs the pass with no bound exceeded: nothing compacts, and
	// the inside-horizon removed host's history stays whole.
	liveNew := createOp(t, store, "live", "live-new")
	finish(t, store, liveNew.ID)
	for _, record := range []Record{liveRecord, removedOld, removedNew} {
		if _, ok := store.Record(record.ID); !ok {
			t.Fatalf("record %s compacted inside the removed host's horizon with no bound exceeded", record.ID)
		}
	}
	if got := store.CursorEpoch().CompactSeq; got != 0 {
		t.Fatalf("compactSeq = %d, want 0: no bound was exceeded and the horizon had not passed", got)
	}
	// §4's boundary sentence: while records remain, the removed host's
	// historical boundary stays in the store's per-host boundary record.
	if _, ok := store.Boundary("gone"); !ok {
		t.Fatal("the removed host's boundary was dropped while records remained")
	}

	// Past the horizon the removed host's terminal records compact even though
	// no bound is exceeded and the live host keeps every record.
	store.clock = func() time.Time { return base.Add(8 * 24 * time.Hour) }
	next := createOp(t, store, "live", "live-next")
	finish(t, store, next.ID)
	if _, ok := store.Record(removedOld.ID); ok {
		t.Fatal("a past-horizon removed host's terminal record survived compaction")
	}
	if _, ok := store.Record(removedNew.ID); ok {
		t.Fatal("a past-horizon removed host's terminal records did not all compact")
	}
	// §4: "its older tombstones drop oldest-first" past the per-host bound: the
	// newest tombstone survives (TombstonesPerHost is 1) and replays with
	// compacted: true while its pinned pair equals the tombstone's own removed
	// pair; the dropped older one opens fresh.
	tombstones := store.Tombstones()
	if len(tombstones) != policy.TombstonesPerHost {
		t.Fatalf("removed host holds %d tombstones, want the %d-per-host bound", len(tombstones), policy.TombstonesPerHost)
	}
	if tombstones[0].ClientOperationID != "gone-new" {
		t.Fatalf("surviving tombstone = %s, want the newest gone-new", tombstones[0].ClientOperationID)
	}
	replayed, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "gone-new", Host: "gone", Kind: KindDeploy,
		Current: OperationPair{Generation: 7, IncarnationID: "inc-gone"},
	})
	if err != nil || !hit || !replayed.Compacted {
		t.Fatalf("removed-host replay = hit %v compacted %v err %v; want a compacted replay",
			hit, replayed.Compacted, err)
	}
	_, hit, err = store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "gone-old", Host: "gone", Kind: KindDeploy,
		Current: OperationPair{Generation: 7, IncarnationID: "inc-gone"},
	})
	if err != nil {
		t.Fatalf("LookupOperation(dropped tombstone): %v", err)
	}
	if hit {
		t.Fatal("a dropped tombstone replayed after the horizon")
	}
	// The host's last record compacted above, so the boundary goes with it.
	if _, ok := store.Boundary("gone"); ok {
		t.Fatal("the boundary outlived the removed host's last record")
	}
}

// TestRemovedHostTombstonesSurviveInsideTheHorizon pins §4's retry contract:
// inside the horizon a removed host's tombstones are never dropped, even past
// the per-host bound — "Only past that horizon ... does a replay open fresh".
func TestRemovedHostTombstonesSurviveInsideTheHorizon(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{
		TerminalPerHost:    1,
		TombstonesPerHost:  1,
		RemovedHostHorizon: 7 * 24 * time.Hour,
	})
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"gone": {Generation: 7, IncarnationID: "inc-gone", PresenceEpoch: 2}},
		Removed:    map[string]RemovedHost{"gone": {RemovedAt: base.Add(-24 * time.Hour), Generation: 7, IncarnationID: "inc-gone"}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	for i := range 4 {
		record := createOp(t, store, "gone", fmt.Sprintf("gone-%d", i))
		finish(t, store, record.ID)
	}
	// Four terminal lands under a one-record bound leave three compacted
	// records — and the horizon exempts all three tombstones from the
	// one-per-host bound.
	if got := len(store.Tombstones()); got != 3 {
		t.Fatalf("inside-horizon removed host holds %d tombstones, want all 3 kept", got)
	}
	replayed, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "gone-0", Host: "gone", Kind: KindDeploy,
		Current: OperationPair{Generation: 7, IncarnationID: "inc-gone"},
	})
	if err != nil || !hit || !replayed.Compacted {
		t.Fatalf("oldest inside-horizon replay = hit %v compacted %v err %v; want the retained replay",
			hit, replayed.Compacted, err)
	}
}

// TestCompactionDropsTheBoundaryWithTheLastRecord pins §4's sentence: "the
// historical (generation, incarnation id, presenceEpoch) boundary persists in
// the store's per-host boundary record until the host's last record compacts".
func TestCompactionDropsTheBoundaryWithTheLastRecord(t *testing.T) {
	policy := RetentionPolicy{TerminalPerHost: 5, RemovedHostHorizon: time.Hour}
	store, _ := openRetentionStore(t, policy)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }

	record := createOp(t, store, "gone", "gone-1")
	finish(t, store, record.ID)
	// The removal is inside its horizon when the mirror lands: the boundary and
	// the record both stay.
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"gone": {Generation: 7, IncarnationID: "inc-gone", PresenceEpoch: 2}},
		Removed:    map[string]RemovedHost{"gone": {RemovedAt: base.Add(-30 * time.Minute), Generation: 7, IncarnationID: "inc-gone"}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	if _, ok := store.Boundary("gone"); !ok {
		t.Fatal("the mirror did not persist the boundary")
	}
	if _, ok := store.Record(record.ID); !ok {
		t.Fatal("an inside-horizon removed host's record compacted before its horizon")
	}
	// Past the horizon a later write runs the pass: the host's last record
	// compacts, and the boundary goes with it.
	store.clock = func() time.Time { return base.Add(2 * time.Hour) }
	other := createOp(t, store, "live", "live-1")
	finish(t, store, other.ID)
	if _, ok := store.Record(record.ID); ok {
		t.Fatal("the removed host's last record did not compact past the horizon")
	}
	if _, ok := store.Boundary("gone"); ok {
		t.Fatal("the boundary outlived the host's last record")
	}
}

// TestCompactionEnforcesTheAgeAndGlobalBounds pins the remaining §4 bounds:
// "at most 500 terminal records store-wide, at most 64 MiB of serialized store
// bytes, and at most 30 days of terminal-record age".
func TestCompactionEnforcesTheAgeAndGlobalBounds(t *testing.T) {
	policy := RetentionPolicy{
		TerminalPerHost:    100,
		TerminalStoreWide:  2,
		StoreMaxBytes:      1 << 20,
		TerminalMaxAge:     30 * 24 * time.Hour,
		TombstonesPerHost:  50,
		RemovedHostHorizon: 7 * 24 * time.Hour,
	}
	store, _ := openRetentionStore(t, policy)

	// Store-wide count: a third host's terminal land drops the oldest across
	// hosts.
	a := createOp(t, store, "a", "op-a")
	finish(t, store, a.ID)
	b := createOp(t, store, "b", "op-b")
	finish(t, store, b.ID)
	c := createOp(t, store, "c", "op-c")
	finish(t, store, c.ID)
	if _, ok := store.Record(a.ID); ok {
		t.Fatal("the oldest terminal record survived the store-wide count bound")
	}

	// Age: a terminal record older than the bound compacts on the next write.
	// The terminal timestamps are display-only and written from the wall clock,
	// so the test backdates the stored record instead of moving the clock under
	// the writer.
	old := createOp(t, store, "d", "op-old")
	finish(t, store, old.ID)
	for i := range store.cell.state.Records {
		if store.cell.state.Records[i].ID == old.ID {
			store.cell.state.Records[i].UpdatedAt = time.Now().UTC().Add(-31 * 24 * time.Hour)
		}
	}
	fresh := createOp(t, store, "e", "op-fresh")
	finish(t, store, fresh.ID)
	if _, ok := store.Record(old.ID); ok {
		t.Fatal("a terminal record past the age bound survived")
	}
	if _, ok := store.Record(fresh.ID); !ok {
		t.Fatal("the newest terminal record was compacted by the age bound")
	}

	// Bytes: a cap that cannot fit any record compacts every removable terminal
	// record — "compacts ... until the new record fits" has no smaller set to
	// leave behind — while a roomy cap leaves the same records alone.
	tight, tightPath := openRetentionStore(t, RetentionPolicy{
		TerminalPerHost: 100, TerminalStoreWide: 100,
		StoreMaxBytes: 1, TerminalMaxAge: 30 * 24 * time.Hour, TombstonesPerHost: 50,
	})
	for i := range 3 {
		record := createOp(t, tight, "m4", fmt.Sprintf("byte-%d", i))
		finish(t, tight, record.ID)
	}
	if got := len(tight.Records()); got != 0 {
		t.Fatalf("records under the unfittable byte cap = %d, want every terminal record compacted", got)
	}
	if tight.CursorEpoch().CompactSeq == 0 {
		t.Fatal("the byte-bound compaction advanced no compactSeq")
	}
	// The file cannot fit by construction; the write still lands durably and
	// the store does not lose the state it must keep.
	if raw := mustReadFile(t, tightPath); len(raw) == 0 {
		t.Fatal("the byte-bound write left no store file")
	}

	roomy, _ := openRetentionStore(t, RetentionPolicy{
		TerminalPerHost: 100, TerminalStoreWide: 100,
		StoreMaxBytes: 64 << 20, TerminalMaxAge: 30 * 24 * time.Hour, TombstonesPerHost: 50,
	})
	for i := range 3 {
		record := createOp(t, roomy, "m4", fmt.Sprintf("roomy-%d", i))
		finish(t, roomy, record.ID)
	}
	if got := len(roomy.Records()); got != 3 {
		t.Fatalf("records under the roomy byte cap = %d, want 3", got)
	}
}

// TestTombstoneReplayCleanSlateReAddWins pins §4: "A tombstone pinned to a
// superseded pair on a live host never replays; the clean-slate re-add rule
// wins over the tombstone."
func TestTombstoneReplayCleanSlateReAddWins(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	old := createOp(t, store, "m4", "op-reused")
	finish(t, store, old.ID)
	newer := createOp(t, store, "m4", "op-other")
	finish(t, store, newer.ID)
	if len(store.Tombstones()) != 1 {
		t.Fatalf("tombstones = %d, want 1", len(store.Tombstones()))
	}
	// The registry re-added the name at a fresh pair: the tombstone pinned to
	// the superseded pair never replays, so the dedup lookup opens fresh.
	_, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "op-reused", Host: "m4", Kind: KindDeploy,
		Current: OperationPair{Generation: 8, IncarnationID: "inc-new"},
	})
	if err != nil {
		t.Fatalf("LookupOperation(re-added): %v", err)
	}
	if hit {
		t.Fatal("a tombstone pinned to a superseded pair replayed on a live host")
	}
}

// TestCompactionLandsAtomicallyWithTheTerminalState pins §4: "Exceeding the
// cap compacts oldest-terminal-first in the same atomic write that lands the
// new terminal state." The fault-injected write refuses before the rename:
// neither the terminal state nor the compaction may be visible.
func TestCompactionLandsAtomicallyWithTheTerminalState(t *testing.T) {
	path := StorePath(t.TempDir())
	store, err := openFSWithRetention(afero.NewOsFs(), path, storeFaults{}, RetentionPolicy{TerminalPerHost: 1})
	if err != nil {
		t.Fatalf("openFSWithRetention: %v", err)
	}
	first := createOp(t, store, "m4", "op-atomic-1")
	finish(t, store, first.ID)
	second := createOp(t, store, "m4", "op-atomic-2")
	store.faults.beforeRename = func() error { return errors.New("injected: before rename") }
	if _, err := store.Transition(second.ID, StateComplete, terminalChange(true)); err == nil {
		t.Fatal("the injected write failure was not reported")
	}
	if _, ok := store.Record(first.ID); !ok {
		t.Fatal("the failed write compacted a record before its rename")
	}
	if failed, ok := store.Record(second.ID); !ok || failed.State != StatePending {
		t.Fatalf("record after the refused write = %+v (present %v), want still pending", failed, ok)
	}
	if got := store.CursorEpoch().CompactSeq; got != 0 {
		t.Fatalf("compactSeq = %d after the refused write, want 0", got)
	}
}

// TestTerminalRecordsAndTombstonesRequireTheirResult pins spec §10's "`result`
// is present exactly on terminal records": a terminal transition without an
// outcome is refused, and a tombstone can never replay a terminal record
// missing the result it retained.
func TestTerminalRecordsAndTombstonesRequireTheirResult(t *testing.T) {
	store, _ := openTestStore(t)
	record := createOp(t, store, "m4", "op-1")
	if _, err := store.Transition(record.ID, StateComplete, nil); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("terminal transition without a result = %v, want ErrInvalidRecord", err)
	}
	if got, _ := store.Record(record.ID); got.State != StatePending {
		t.Fatalf("record after the refused transition = %q, want pending", got.State)
	}

	now := time.Now().UTC()
	tombstone := Tombstone{
		ID: formatAllocatorID(1), ClientOperationID: "op-1", Host: "m4", Kind: KindDeploy,
		Generation: 7, IncarnationID: "inc-m4", State: StateComplete,
		CreatedAt: now, UpdatedAt: now, CompactedAt: now, CompactedSeq: 1,
	}
	if err := validateTombstone(tombstone, 1); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("terminal tombstone without a result = %v, want ErrInvalidRecord", err)
	}
	tombstone.Result = &Result{OK: true, Message: "done"}
	if err := validateTombstone(tombstone, 1); err != nil {
		t.Fatalf("terminal tombstone with a result: %v", err)
	}
}

// TestRemovedHostReplayMatchesTheActiveRemovedPair pins §4's comparison pair
// for a removed host: the store's retained historical boundary for the name —
// the removal marker mirroring the registry tombstone — so after remove →
// re-add → remove again a tombstone pinned to the older removal never replays,
// and the newer incarnation's replay still does.
func TestRemovedHostReplayMatchesTheActiveRemovedPair(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }

	// First incarnation (7/inc-m4): op-old is compacted into a tombstone.
	old := createOp(t, store, "m4", "op-old")
	finish(t, store, old.ID)
	other := createOp(t, store, "m4", "op-other")
	finish(t, store, other.ID)
	if _, ok := store.Record(old.ID); ok {
		t.Fatal("the per-host bound did not compact the first incarnation's record")
	}

	// Remove at pair 7/inc-m4, re-add at 8/inc-new, then remove again.
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-m4", PresenceEpoch: 2}},
		Removed:    map[string]RemovedHost{"m4": {RemovedAt: base, Generation: 7, IncarnationID: "inc-m4"}},
	}); err != nil {
		t.Fatalf("MirrorHostState(remove 7): %v", err)
	}
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"m4": {Generation: 8, IncarnationID: "inc-new", PresenceEpoch: 3}},
		Live:       []string{"m4"},
	}); err != nil {
		t.Fatalf("MirrorHostState(re-add): %v", err)
	}

	// Second incarnation (8/inc-new): op-new is compacted into its own
	// tombstone.
	newer := createOpPair(t, store, "m4", "op-new", 8, "inc-new")
	finish(t, store, newer.ID)
	next := createOpPair(t, store, "m4", "op-next", 8, "inc-new")
	finish(t, store, next.ID)
	if _, ok := store.Record(newer.ID); ok {
		t.Fatal("the per-host bound did not compact the second incarnation's record")
	}
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"m4": {Generation: 8, IncarnationID: "inc-new", PresenceEpoch: 4}},
		Removed:    map[string]RemovedHost{"m4": {RemovedAt: base, Generation: 8, IncarnationID: "inc-new"}},
	}); err != nil {
		t.Fatalf("MirrorHostState(remove 8): %v", err)
	}

	// The older incarnation's tombstone never replays: its pair is not the
	// active removed pair.
	_, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "op-old", Host: "m4", Kind: KindDeploy,
		Current: OperationPair{Generation: 8, IncarnationID: "inc-new"},
	})
	if err != nil {
		t.Fatalf("LookupOperation(op-old): %v", err)
	}
	if hit {
		t.Fatal("a tombstone pinned to a superseded removal replayed after re-add/remove again")
	}
	// The active incarnation's replay still answers with compacted: true.
	replayed, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "op-new", Host: "m4", Kind: KindDeploy,
		Current: OperationPair{Generation: 8, IncarnationID: "inc-new"},
	})
	if err != nil || !hit || !replayed.Compacted {
		t.Fatalf("active removed-pair replay = hit %v compacted %v err %v; want a compacted replay",
			hit, replayed.Compacted, err)
	}
}

// TestMirrorWriteMeetsTheByteBound pins §4's bound on every mutating commit
// path: a boundary/removal-marker mirror write that would leave the store over
// the serialized bound compacts in that same atomic write.
func TestMirrorWriteMeetsTheByteBound(t *testing.T) {
	roomy, storePath := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 50})
	first := createOp(t, roomy, "m4", "op-1")
	finish(t, roomy, first.ID)
	second := createOp(t, roomy, "m4", "op-2")
	finish(t, roomy, second.ID)
	if got := len(roomy.Records()); got != 2 {
		t.Fatalf("records before the mirror write = %d, want 2", got)
	}
	before := roomy.CursorEpoch().CompactSeq

	// An unfittable cap: the mirror write itself must compact every removable
	// terminal record, not defer the bound to a later record write.
	roomy.retention.StoreMaxBytes = 1
	if err := roomy.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-m4", PresenceEpoch: 9}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	if got := len(roomy.Records()); got != 0 {
		t.Fatalf("records after the over-cap mirror write = %d, want the mirror write itself to compact", got)
	}
	if roomy.CursorEpoch().CompactSeq <= before {
		t.Fatalf("compactSeq = %d after the over-cap mirror write, want above %d", roomy.CursorEpoch().CompactSeq, before)
	}
	if len(mustReadFile(t, storePath)) == 0 {
		t.Fatal("the mirror write left no store file")
	}
}

// TestByteBoundCompactionMeasuresTheCommittedSize pins the byte bound's exact
// measurement: the victims are chosen against the bytes the write actually
// commits — the compaction-ledger marks and the advanced compactSeq included —
// so a store that can fit once a large record compacts does fit in that same
// write. The fencing epoch is deliberately large and is not a replay field, so
// the record's tombstone is much smaller than the record.
func TestByteBoundCompactionMeasuresTheCommittedSize(t *testing.T) {
	store, path := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 50})
	pad := strings.Repeat("x", 20000)
	bigFence := json.RawMessage(`{"bootId":"boot","opSeq":1,"pad":"` + pad + `"}`)
	big := createOp(t, store, "m4", "op-big")
	if _, err := store.Transition(big.ID, StateComplete, func(r *Record) {
		r.Result = &Result{OK: true, Message: "done"}
		r.FencingEpoch = bigFence
	}); err != nil {
		t.Fatalf("Transition(big): %v", err)
	}
	small := createOp(t, store, "m4", "op-small")
	finish(t, store, small.ID)

	before := int64(len(mustReadFile(t, path)))
	store.retention.StoreMaxBytes = before - int64(len(pad))/2
	next := createOp(t, store, "m4", "op-next")
	finish(t, store, next.ID)

	if _, ok := store.Record(big.ID); ok {
		t.Fatal("the byte-bound compaction did not remove the over-cap record")
	}
	if _, ok := store.Record(small.ID); !ok {
		t.Fatal("the compaction removed more than the bound needed")
	}
	if got := int64(len(mustReadFile(t, path))); got > store.retention.StoreMaxBytes {
		t.Fatalf("committed store = %d bytes, over the %d-byte cap", got, store.retention.StoreMaxBytes)
	}
}

// TestByteBoundMeasurementPassesAreBounded pins the byte loop's cost: the
// victim search measures per candidate cheaply and takes only a bounded number
// of full-store measurements (never one marshal per removed row inside the
// store mutex). With 301 removable candidates the bound is ~2*log2(N+1)+2;
// the loop must stay under it while still bringing the store under the cap.
func TestByteBoundMeasurementPassesAreBounded(t *testing.T) {
	store, path := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 10000, TerminalStoreWide: 10000, TombstonesPerHost: 10000})
	pad := strings.Repeat("x", 1024)
	seed := func(i int) {
		record := createOp(t, store, "m4", fmt.Sprintf("op-%03d", i))
		fence := json.RawMessage(fmt.Sprintf(`{"bootId":"boot","opSeq":%d,"pad":%q}`, i+1, pad))
		if _, err := store.Transition(record.ID, StateComplete, func(r *Record) {
			r.Result = &Result{OK: true, Message: "done"}
			r.FencingEpoch = fence
		}); err != nil {
			t.Fatalf("Transition(%s): %v", record.ID, err)
		}
	}
	const candidates = 300
	for i := range candidates {
		seed(i)
	}
	before := int64(len(mustReadFile(t, path)))
	store.retention.StoreMaxBytes = before / 2

	measurements := 0
	compactionMeasureHook = func() { measurements++ }
	defer func() { compactionMeasureHook = nil }()
	seed(candidates) // the byte-bound compaction rides this commit.

	bound := 2*int(math.Ceil(math.Log2(candidates+1))) + 2
	if measurements > bound {
		t.Fatalf("byte-bound measurements = %d, want at most %d for %d candidates (never one marshal per removed row)",
			measurements, bound, candidates+1)
	}
	if got := int64(len(mustReadFile(t, path))); got > store.retention.StoreMaxBytes {
		t.Fatalf("committed store = %d bytes, over the %d-byte cap", got, store.retention.StoreMaxBytes)
	}
	if got := len(store.Records()); got == 0 {
		t.Fatal("the byte-bound compaction removed every record instead of stopping at the fit")
	}
}

// TestByteBoundNeverCommitsOverTheCapWhenTheBudgetIsExhausted pins the
// measurement budget's safety: when the full-store measurement budget is spent,
// the per-candidate estimate is a provable upper bound, so whatever prefix the
// loop keeps still commits at or below StoreMaxBytes. The budget is lowered so
// a small store reaches the path.
func TestByteBoundNeverCommitsOverTheCapWhenTheBudgetIsExhausted(t *testing.T) {
	store, path := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 10000, TerminalStoreWide: 10000, TombstonesPerHost: 10000})
	pad := strings.Repeat("y", 512)
	seed := func(i int) {
		record := createOp(t, store, "m4", fmt.Sprintf("op-%03d", i))
		fence := json.RawMessage(fmt.Sprintf(`{"bootId":"boot","opSeq":%d,"pad":%q}`, i+1, pad))
		if _, err := store.Transition(record.ID, StateComplete, func(r *Record) {
			r.Result = &Result{OK: true, Message: "done"}
			r.FencingEpoch = fence
		}); err != nil {
			t.Fatalf("Transition(%s): %v", record.ID, err)
		}
	}
	for i := range 40 {
		seed(i)
	}
	// One pending record stays out of the bound until its terminal transition:
	// that single commit is the one whose byte loop this test observes.
	pending := createOp(t, store, "m4", "op-pending")
	before := int64(len(mustReadFile(t, path)))
	store.retention.StoreMaxBytes = before / 2

	measurements := 0
	compactionMeasureHook = func() { measurements++ }
	defer func() { compactionMeasureHook = nil }()
	saved := maxByteMeasurements
	maxByteMeasurements = 1
	defer func() { maxByteMeasurements = saved }()
	if _, err := store.Transition(pending.ID, StateComplete, func(r *Record) {
		r.Result = &Result{OK: true, Message: "done"}
		r.FencingEpoch = json.RawMessage(fmt.Sprintf(`{"bootId":"boot","opSeq":99,"pad":%q}`, pad))
	}); err != nil {
		t.Fatalf("Transition(%s): %v", pending.ID, err)
	}

	// The budget is spent, so the keep path must have taken its mandatory
	// final exact measurement (baseline plus the final check): an estimate-only
	// return is exactly the hole this guards.
	if measurements < 2 {
		t.Fatalf("byte-bound measurements = %d with the budget exhausted, want the baseline plus the final exact check", measurements)
	}
	if got := int64(len(mustReadFile(t, path))); got > store.retention.StoreMaxBytes {
		t.Fatalf("committed store = %d bytes, over the %d-byte cap with the measurement budget exhausted",
			got, store.retention.StoreMaxBytes)
	}
	if got := len(store.Records()); got == 0 {
		t.Fatal("the byte-bound compaction removed every record instead of stopping at the fit")
	}
}

// TestTombstoneValidationBoundsOversizedFields pins M-A: a tombstone can never
// carry an oversized host name, too many progress entries, or an oversized
// progress or result message — tombstones are never compaction victims, so a
// malformed store must not load a value the byte bound could never reclaim.
func TestTombstoneValidationBoundsOversizedFields(t *testing.T) {
	now := time.Now().UTC()
	base := Tombstone{
		ID: formatAllocatorID(1), ClientOperationID: "op-1", Host: "m4", Kind: KindDeploy,
		Generation: 7, IncarnationID: "inc-m4", State: StateComplete,
		CreatedAt: now, UpdatedAt: now, CompactedAt: now, CompactedSeq: 1,
		Result: &Result{OK: true, Message: "done"},
	}
	if err := validateTombstone(base, 1); err != nil {
		t.Fatalf("valid tombstone: %v", err)
	}
	oversizedMessage := strings.Repeat("x", MaxOperationMessageBytes+1)
	cases := map[string]struct {
		mutate func(*Tombstone)
		want   string
	}{
		"host name": {func(tm *Tombstone) { tm.Host = strings.Repeat("h", MaxHostNameBytes+1) }, "host name"},
		"progress count": {func(tm *Tombstone) {
			for range MaxProgressEntries + 1 {
				tm.Progress = append(tm.Progress, ProgressEntry{TS: now, Message: "step"})
			}
		}, "progress entries"},
		"progress message": {func(tm *Tombstone) { tm.Progress = []ProgressEntry{{TS: now, Message: oversizedMessage}} }, "progress message"},
		"result message":   {func(tm *Tombstone) { tm.Result = &Result{OK: true, Message: oversizedMessage} }, "result message"},
	}
	for name, tc := range cases {
		tombstone := base
		tc.mutate(&tombstone)
		err := validateTombstone(tombstone, 1)
		if !errors.Is(err, ErrInvalidRecord) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want ErrInvalidRecord naming %q", name, err, tc.want)
		}
	}
}

// TestOpenRefusesAnOversizedTombstone pins the same rule at the load: a store
// file carrying an oversized tombstone is refused, the refusal naming the
// field.
func TestOpenRefusesAnOversizedTombstone(t *testing.T) {
	body := `{"version":1,"sequence":1,"allocatorHighWaterMark":1,"compactSeq":1,"records":[],"tombstones":[` +
		`{"id":"00000000000000000001","clientOperationId":"op-1","host":"` + strings.Repeat("h", MaxHostNameBytes+1) +
		`","kind":"deploy","state":"complete","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,"result":{"ok":true,"message":"done"},"compactedAt":"2026-09-26T00:00:00Z","compactedSeq":1}]}`
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, body)
	_, err := Open(path)
	if !errors.Is(err, ErrStoreCorrupt) || !strings.Contains(err.Error(), "host name") {
		t.Fatalf("Open(oversized tombstone) = %v, want ErrStoreCorrupt naming the host name", err)
	}
}

// TestStaleRemovalMarkerDoesNotCompactLiveHistory pins M-B: a re-add whose
// clear mirror write trailed leaves the marker stale; once the old horizon
// passes, the live host's terminal records must survive the bounds they would
// normally survive and its boundary must stay, and the live history stays
// replayable for the live pair.
func TestStaleRemovalMarkerDoesNotCompactLiveHistory(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 50, RemovedHostHorizon: time.Hour})
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }
	// The removal at pair 7 landed, then the re-add's mirror write failed: the
	// boundary and the marker both stay at the old pair.
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-old", PresenceEpoch: 2}},
		Removed:    map[string]RemovedHost{"m4": {RemovedAt: base.Add(-30 * time.Minute), Generation: 7, IncarnationID: "inc-old"}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	live := createOpPair(t, store, "m4", "live-1", 8, "inc-new")
	finish(t, store, live.ID)
	// Past the old horizon a later write must treat the host as live.
	store.clock = func() time.Time { return base.Add(2 * time.Hour) }
	other := createOp(t, store, "x", "x-1")
	finish(t, store, other.ID)

	if _, ok := store.Record(live.ID); !ok {
		t.Fatal("a stale removal marker compacted the live host's terminal history")
	}
	if _, ok := store.Boundary("m4"); !ok {
		t.Fatal("a stale removal marker dropped the live host's boundary")
	}
	replayed, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "live-1", Host: "m4", Kind: KindDeploy,
		Current: OperationPair{Generation: 8, IncarnationID: "inc-new"},
	})
	if err != nil || !hit || replayed.ID != live.ID || replayed.Compacted {
		t.Fatalf("live replay = %+v hit %v compacted %v err %v; want the retained live record",
			replayed, hit, replayed.Compacted, err)
	}
}

// TestMirrorDoesNotReproposeADroppedRemovedHostBoundary pins the Low: after a
// removed host's last record compacts and §4 drops its boundary, a later
// hub.toml mutation proposing the same boundary must perform no operation-store
// write, not a write that changes nothing.
func TestMirrorDoesNotReproposeADroppedRemovedHostBoundary(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 50, RemovedHostHorizon: time.Hour})
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"gone": {Generation: 7, IncarnationID: "inc-gone", PresenceEpoch: 2}},
		Removed:    map[string]RemovedHost{"gone": {RemovedAt: base.Add(-30 * time.Minute), Generation: 7, IncarnationID: "inc-gone"}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	record := createOp(t, store, "gone", "gone-1")
	finish(t, store, record.ID)
	store.clock = func() time.Time { return base.Add(2 * time.Hour) }
	// An unfittable cap removes every terminal record in one write, taking the
	// removed host's boundary with its last record; the cap then returns to
	// roomy so the mirror write below cannot compact for its own sake.
	store.retention.StoreMaxBytes = 1
	victim := createOp(t, store, "x", "x-1")
	finish(t, store, victim.ID)
	if _, ok := store.Record(record.ID); ok {
		t.Fatal("test setup: the removed host's record survived")
	}
	if _, ok := store.Boundary("gone"); ok {
		t.Fatal("test setup: the boundary survived the last record")
	}
	store.retention.StoreMaxBytes = 64 << 20

	writes := 0
	store.faults.beforeRename = func() error { writes++; return nil }
	// The hub re-proposes the high-water record's triple on its next mutation.
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"gone": {Generation: 7, IncarnationID: "inc-gone", PresenceEpoch: 2}},
	}); err != nil {
		t.Fatalf("re-propose: %v", err)
	}
	if writes != 0 {
		t.Fatalf("the re-proposal performed %d operation-store write(s), want none", writes)
	}
	if _, ok := store.Boundary("gone"); ok {
		t.Fatal("the re-proposal put the dropped boundary back")
	}
	// A live name's boundary still commits.
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"x": {Generation: 7, IncarnationID: "inc-x", PresenceEpoch: 1}},
	}); err != nil {
		t.Fatalf("live mirror: %v", err)
	}
	if writes != 1 {
		t.Fatalf("live mirror writes = %d, want 1", writes)
	}

	// A removed host that never held an operation record is in the same state:
	// its marker lands, but no boundary §4 would discard is created, and a
	// later re-proposal is a no-op rather than an oscillation.
	neverMirror := HostMirror{
		Boundaries: map[string]Boundary{"never": {Generation: 9, IncarnationID: "inc-never", PresenceEpoch: 1}},
		Removed:    map[string]RemovedHost{"never": {RemovedAt: base.Add(-30 * time.Minute), Generation: 9, IncarnationID: "inc-never"}},
	}
	if err := store.MirrorHostState(neverMirror); err != nil {
		t.Fatalf("first mirror: %v", err)
	}
	if writes != 2 {
		t.Fatalf("marker mirror writes = %d, want 2 (the marker still lands)", writes)
	}
	if _, ok := store.Boundary("never"); ok {
		t.Fatal("a boundary was created for a record-less removed host")
	}
	if err := store.MirrorHostState(neverMirror); err != nil {
		t.Fatalf("re-proposal: %v", err)
	}
	if writes != 2 {
		t.Fatalf("re-proposal writes = %d, want no new write", writes)
	}
}

// TestByteBoundLeavesARecordSetThatAlreadyFits pins the baseline
// short-circuit: a store already within StoreMaxBytes must not walk the victim
// list at all. Without it, each host's first ledger entry charge can cross the
// cap and, because the estimate only falls again when tombstones are smaller
// than their records, the walk marks every terminal candidate and drops the
// whole retained history on a write that never needed to compact.
func TestByteBoundLeavesARecordSetThatAlreadyFits(t *testing.T) {
	store, path := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 50, TombstonesPerHost: 50})
	for i := range 3 {
		record := createOp(t, store, "m4", fmt.Sprintf("op-%d", i))
		finish(t, store, record.ID)
	}
	// A non-terminal transition changes the serialized size by ~nothing and
	// leaves the three terminal candidates in place.
	pending := createOp(t, store, "m4", "op-pending")
	if _, err := store.Transition(pending.ID, StateRunning, nil); err != nil {
		t.Fatalf("Transition(running): %v", err)
	}
	roomy := int64(len(mustReadFile(t, path)))
	// The cap sits just above the current size: the fitting baseline must
	// short-circuit before the first ledger charge (~len(host)+len(id)+overhead)
	// could push the estimate over.
	store.retention.StoreMaxBytes = roomy + 60
	if _, err := store.Transition(pending.ID, StateComplete, func(r *Record) {
		r.Result = &Result{OK: true, Message: "done"}
	}); err != nil {
		t.Fatalf("Transition(complete): %v", err)
	}
	if got := len(store.Records()); got != 4 {
		t.Fatalf("records after the fitting write = %d, want all 4 retained", got)
	}
	if got := store.CursorEpoch().CompactSeq; got != 0 {
		t.Fatalf("compactSeq = %d after a write that already fit, want 0", got)
	}
}

// TestOpenRefusesACaseVariantRemovedHostKey pins the Low: the per-name removal
// markers are objects this store decodes, so a key outside the canonical
// spelling — including the case variants Go's decoder accepts — is refused on
// load rather than silently rewritten on the next save.
func TestOpenRefusesACaseVariantRemovedHostKey(t *testing.T) {
	body := `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],"removedHosts":{"m4":{"RemovedAt":"2026-09-26T00:00:00Z","generation":7,"incarnationId":"inc-1"}}}`
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, body)
	_, err := Open(path)
	if !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("Open(case-variant removal marker key) = %v, want ErrStoreCorrupt", err)
	}
	if !strings.Contains(err.Error(), "removedHosts") && !strings.Contains(err.Error(), "RemovedAt") {
		t.Fatalf("refusal = %v, want it to name the offending key", err)
	}
}
