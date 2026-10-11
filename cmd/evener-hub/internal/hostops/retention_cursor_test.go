package hostops

// Cursor-invalidated tests (deploy pipeline 08b §8's refusal, §11's catalog
// entry, §12's "Cursor-invalidated" row). S6's store-level half: a compaction
// that removed rows at or before the cursor's pos since the cursor was minted
// refuses typed cursor-invalidated naming the compacting compactSeq plus the
// affected host's stored bounds entry; an unchanged compactSeq keeps the
// existing stale re-list.

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// cursorBoundary mirrors one host's boundary and returns the triple it wrote.
func cursorBoundary(t *testing.T, store *Store, host string, generation uint64, incarnationID string, presenceEpoch uint64) Boundary {
	t.Helper()
	boundary := Boundary{Generation: generation, IncarnationID: incarnationID, PresenceEpoch: presenceEpoch}
	if err := store.MirrorHostState(HostMirror{Boundaries: map[string]Boundary{host: boundary}}); err != nil {
		t.Fatalf("MirrorHostState(%s): %v", host, err)
	}
	return boundary
}

// TestCursorInvalidatedByMidPaginationCompaction pins §8: "A compaction that
// removed rows at or before the cursor's `pos` since the cursor was minted
// surfaces a typed `cursor-invalidated` refusal naming the compacting
// `compactSeq` (the envelope-global value) plus the affected host's `bounds`
// entry (`[generation, incarnationId, presenceEpoch]` as stored at mint, or
// `"absent"`)".
func TestCursorInvalidatedByMidPaginationCompaction(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 2})
	boundary := cursorBoundary(t, store, "m4", 7, "inc-m4", 3)
	r1 := createOp(t, store, "m4", "op-1")
	finish(t, store, r1.ID)
	r2 := createOp(t, store, "m4", "op-2")
	finish(t, store, r2.ID)
	r3 := createOp(t, store, "m4", "op-3")
	finish(t, store, r3.ID) // per-host bound: r1 compacted, compactSeq 1.

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != r2.ID {
		t.Fatalf("page 1 listed %v, want the first surviving record %s", page.Records, r2.ID)
	}

	// Land a terminal record: the pass compacts the oldest terminal record,
	// which is the cursor's `pos`.
	r4 := createOp(t, store, "m4", "op-4")
	finish(t, store, r4.ID)
	if _, ok := store.Record(r2.ID); ok {
		t.Fatal("the setup write did not compact the row at the cursor's pos")
	}

	_, err = store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page.NextCursor})
	invalidated, ok := errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("continuation error = %v (%T), want *CursorInvalidatedError", err, err)
	}
	if invalidated.CompactSeq != 2 {
		t.Fatalf("refusal compactSeq = %d, want the compacting write's 2", invalidated.CompactSeq)
	}
	if invalidated.Host != "m4" {
		t.Fatalf("refusal host = %q, want the pinned host m4", invalidated.Host)
	}
	if invalidated.Bound.Absent || invalidated.Bound.Boundary != boundary {
		t.Fatalf("refusal bound = %+v, want the bounds entry as stored at mint %+v", invalidated.Bound, boundary)
	}
}

// TestUnfilteredCursorInvalidatedNamesTheCompactedHost pins §8: "A host-pinned
// page names the single listed host's entry; an unfiltered cross-host page
// names the compacted host's entry."
func TestUnfilteredCursorInvalidatedNamesTheCompactedHost(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 2})
	boundaryA := cursorBoundary(t, store, "a", 7, "inc-a", 1)
	cursorBoundary(t, store, "b", 7, "inc-b", 1)
	a1 := createOp(t, store, "a", "op-a1")
	finish(t, store, a1.ID)
	b1 := createOp(t, store, "b", "op-b1")
	finish(t, store, b1.ID)

	page, err := store.ReadOperations(OperationsQuery{Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != a1.ID {
		t.Fatalf("page 1 listed %v, want %s", page.Records, a1.ID)
	}
	// A terminal land on b pushes the store-wide count over its bound: the
	// oldest terminal record across hosts (a1, at the cursor's pos) compacts.
	b2 := createOp(t, store, "b", "op-b2")
	finish(t, store, b2.ID)
	if _, ok := store.Record(a1.ID); ok {
		t.Fatal("the store-wide bound did not compact the oldest terminal record")
	}

	_, err = store.ReadOperations(OperationsQuery{Cursor: page.NextCursor})
	invalidated, ok := errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("unfiltered continuation error = %v (%T), want *CursorInvalidatedError", err, err)
	}
	if invalidated.Host != "a" {
		t.Fatalf("refusal host = %q, want the compacted host a", invalidated.Host)
	}
	if invalidated.Bound.Absent || invalidated.Bound.Boundary != boundaryA {
		t.Fatalf("refusal bound = %+v, want a's entry as stored at mint %+v", invalidated.Bound, boundaryA)
	}
}

// TestCursorRefusalNamesTheCompactingSeqNotTheLiveOne pins §8's "(the
// envelope-global value)": when a later compaction advances the live counter
// further, the refusal still names the write that removed the cursor's row.
func TestCursorRefusalNamesTheCompactingSeqNotTheLiveOne(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	cursorBoundary(t, store, "m4", 7, "inc-m4", 3)
	r1 := createOp(t, store, "m4", "op-1")
	finish(t, store, r1.ID) // bound 1: nothing to compact yet (one record).
	r2 := createOp(t, store, "m4", "op-2")
	finish(t, store, r2.ID) // compacts r1, compactSeq 1.

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if page.Records[0].ID != r2.ID {
		t.Fatalf("page 1 listed %s, want %s", page.Records[0].ID, r2.ID)
	}
	r3 := createOp(t, store, "m4", "op-3")
	finish(t, store, r3.ID) // compacts r2 (the cursor's pos), compactSeq 2.
	r4 := createOp(t, store, "m4", "op-4")
	finish(t, store, r4.ID) // compacts r3, compactSeq 3.
	if got := store.CursorEpoch().CompactSeq; got != 3 {
		t.Fatalf("live compactSeq = %d, want 3", got)
	}

	_, err = store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page.NextCursor})
	invalidated, ok := errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("continuation error = %v (%T), want *CursorInvalidatedError", err, err)
	}
	if invalidated.CompactSeq != 2 {
		t.Fatalf("refusal compactSeq = %d, want the compacting write's 2 (never the live 3)", invalidated.CompactSeq)
	}
}

// TestCursorSurvivesCompactionPastItsPosition pins the boundary of the
// refusal: a compaction that removed only rows after the cursor's `pos` never
// trips it.
func TestCursorSurvivesCompactionPastItsPosition(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 50, RemovedHostHorizon: time.Hour})
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }
	cursorBoundary(t, store, "live", 7, "inc-live", 1)
	cursorBoundary(t, store, "gone", 7, "inc-gone", 2)

	r1 := createOp(t, store, "live", "op-live-1")
	finish(t, store, r1.ID)
	r2 := createOp(t, store, "gone", "op-gone-1")
	finish(t, store, r2.ID)
	r3 := createOp(t, store, "gone", "op-gone-2")
	finish(t, store, r3.ID)
	r5 := createOp(t, store, "gone", "op-gone-kept") // non-terminal: never a victim.

	page, err := store.ReadOperations(OperationsQuery{Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != r1.ID {
		t.Fatalf("page 1 listed %v, want %s", page.Records, r1.ID)
	}
	// The removed host is past its horizon: the next pass compacts its terminal
	// records, all of which sit after the cursor's pos.
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"gone": {Generation: 7, IncarnationID: "inc-gone", PresenceEpoch: 2}},
		Removed:    map[string]RemovedHost{"gone": {RemovedAt: base.Add(-2 * time.Hour), Generation: 7, IncarnationID: "inc-gone"}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	r4 := createOp(t, store, "live", "op-live-2")
	finish(t, store, r4.ID)
	if _, ok := store.Record(r2.ID); ok {
		t.Fatal("the removed host's terminal records did not compact past the horizon")
	}
	if _, ok := store.Record(r5.ID); !ok {
		t.Fatal("the non-terminal record was compacted")
	}

	continued, err := store.ReadOperations(OperationsQuery{Cursor: page.NextCursor, Limit: 5})
	if err != nil {
		t.Fatalf("continuation after a past-pos compaction: %v", err)
	}
	var ids []string
	for _, record := range continued.Records {
		ids = append(ids, record.ID)
	}
	if len(ids) != 2 || ids[0] != r5.ID || ids[1] != r4.ID {
		t.Fatalf("continuation listed %v, want [%s %s] in ascending id order", ids, r5.ID, r4.ID)
	}
}

// TestAnchorMissWithoutCompactionStaysStale pins the other arm: an unchanged
// compactSeq keeps the existing stale re-list for a missing anchor.
func TestAnchorMissWithoutCompactionStaysStale(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50})
	cursorBoundary(t, store, "m4", 7, "inc-m4", 3)
	r1 := createOp(t, store, "m4", "op-1")
	finish(t, store, r1.ID)

	forged, err := EncodeCursor(CursorEnvelope{
		Version:         2,
		Position:        formatAllocatorID(99),
		CompactSeq:      store.CursorEpoch().CompactSeq,
		QuarantineEpoch: 0,
		Bounds: map[string]CursorBound{
			"m4": {Boundary: Boundary{Generation: 7, IncarnationID: "inc-m4", PresenceEpoch: 3}},
		},
		Window: CursorWindowHost,
	})
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}
	_, err = store.ReadOperations(OperationsQuery{Host: "m4", Cursor: forged})
	stale, ok := errors.AsType[*CursorStaleError](err)
	if !ok {
		t.Fatalf("forged-anchor continuation error = %v (%T), want *CursorStaleError", err, err)
	}
	if stale.Reason == "" {
		t.Fatal("the stale refusal carries no reason")
	}
}

// TestCursorInvalidatedRefusalNamesTheCompactedHostsMintedEntry pins the data:
// the refusal names the affected host and its bounds entry as stored at mint.
func TestCursorInvalidatedRefusalNamesTheCompactedHostsMintedEntry(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 1})
	cursorBoundary(t, store, "a", 7, "inc-a", 1)
	cursorBoundary(t, store, "b", 7, "inc-b", 1)
	a1 := createOp(t, store, "a", "op-a1")
	finish(t, store, a1.ID)
	page, err := store.ReadOperations(OperationsQuery{Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	b1 := createOp(t, store, "b", "op-b1")
	finish(t, store, b1.ID) // store-wide count 2 > 1: compacts a1.
	_, err = store.ReadOperations(OperationsQuery{Cursor: page.NextCursor})
	invalidated, ok := errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("continuation error = %v (%T), want *CursorInvalidatedError", err, err)
	}
	want := Boundary{Generation: 7, IncarnationID: "inc-a", PresenceEpoch: 1}
	if invalidated.Host != "a" || invalidated.Bound.Absent || invalidated.Bound.Boundary != want {
		t.Fatalf("refusal = host %q bound %+v; want host a with its minted triple %+v",
			invalidated.Host, invalidated.Bound, want)
	}
}

// TestCursorInvalidationSurvivesTombstoneEviction pins the fix for the
// reviewer's evicted-evidence finding: the refusal's evidence is the
// compaction ledger, independent of the bounded dedup tombstones, so evicting
// the tombstone that recorded the cursor row's compaction does not downgrade
// the continuation to a stale re-list.
func TestCursorInvalidationSurvivesTombstoneEviction(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1, TombstonesPerHost: 1})
	cursorBoundary(t, store, "m4", 7, "inc-m4", 3)
	r1 := createOp(t, store, "m4", "op-1")
	finish(t, store, r1.ID)
	r2 := createOp(t, store, "m4", "op-2")
	finish(t, store, r2.ID) // compacts r1, compactSeq 1.

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != r2.ID {
		t.Fatalf("page 1 listed %v, want %s", page.Records, r2.ID)
	}
	r3 := createOp(t, store, "m4", "op-3")
	finish(t, store, r3.ID) // compacts r2 (the cursor's row), compactSeq 2.
	r4 := createOp(t, store, "m4", "op-4")
	finish(t, store, r4.ID) // compactSeq 3; the one-per-host tombstone bound evicts r2's evidence.
	for _, tombstone := range store.Tombstones() {
		if tombstone.ID == r2.ID {
			t.Fatal("test setup: the cursor row's tombstone survived the bound")
		}
	}

	_, err = store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page.NextCursor})
	invalidated, ok := errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("continuation after evidence eviction = %v (%T), want *CursorInvalidatedError", err, err)
	}
	if invalidated.CompactSeq != 2 {
		t.Fatalf("refusal compactSeq = %d, want the compacting write's 2", invalidated.CompactSeq)
	}
	if invalidated.Host != "m4" {
		t.Fatalf("refusal host = %q, want m4", invalidated.Host)
	}
}

// TestOperationsDetailResolvesACompactedRecord pins §4's retrieval path: the
// `id` detail filter (and the operationId filter) resolves a compacted record
// out of its dedup tombstone with `compacted: true`, while listing pages stay
// record-only.
func TestOperationsDetailResolvesACompactedRecord(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	cursorBoundary(t, store, "m4", 7, "inc-m4", 3)
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

// TestCursorInvalidatedNamesTheNonArgminPinnedHost pins the per-host ledger:
// one compacting write removed an age-expired row on host "a" and the cursor
// row on host "h" (the globally smallest removed id belongs to "a"), so an
// h-pinned continuation must still refuse cursor-invalidated naming h — never
// serve on because the write's single mark named another host.
func TestCursorInvalidatedNamesTheNonArgminPinnedHost(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 50})
	cursorBoundary(t, store, "a", 7, "inc-a", 1)
	cursorBoundary(t, store, "h", 7, "inc-h", 1)
	a1 := createOp(t, store, "a", "a-1")
	finish(t, store, a1.ID)
	h1 := createOp(t, store, "h", "h-1")
	finish(t, store, h1.ID)
	h2 := createOp(t, store, "h", "h-2")
	finish(t, store, h2.ID)

	page, err := store.ReadOperations(OperationsQuery{Host: "h", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != h1.ID {
		t.Fatalf("page 1 listed %v, want %s", page.Records, h1.ID)
	}
	// Backdate a1 and h1 past the age bound: the next write's single compaction
	// pass removes both, and a1 carries the globally smallest removed id.
	past := time.Now().UTC().Add(-31 * 24 * time.Hour)
	for i := range store.cell.state.Records {
		record := &store.cell.state.Records[i]
		if record.ID == a1.ID || record.ID == h1.ID {
			record.UpdatedAt = past
		}
	}
	a2 := createOp(t, store, "a", "a-2")
	finish(t, store, a2.ID)
	if _, ok := store.Record(h1.ID); ok {
		t.Fatal("the multi-host pass did not remove the cursor row")
	}
	if _, ok := store.Record(a1.ID); ok {
		t.Fatal("the multi-host pass did not remove the other host's row")
	}

	_, err = store.ReadOperations(OperationsQuery{Host: "h", Cursor: page.NextCursor})
	invalidated, ok := errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("continuation = %v (%T), want *CursorInvalidatedError naming h", err, err)
	}
	if invalidated.Host != "h" {
		t.Fatalf("refusal host = %q, want the pinned host h", invalidated.Host)
	}
}

// TestCursorInvalidationRefusesCoarselyWhenEvidenceIsDropped pins the
// dropped-marks floor: once marks above the cursor's pin are gone, the check
// must refuse (the oldest retained in-window mark, or the live value with the
// window's own host), never serve on.
func TestCursorInvalidationRefusesCoarselyWhenEvidenceIsDropped(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 50})
	cursorBoundary(t, store, "h", 7, "inc-h", 1)
	cursorBoundary(t, store, "a", 7, "inc-a", 1)
	h1 := createOp(t, store, "h", "h-1")
	finish(t, store, h1.ID)
	h2 := createOp(t, store, "h", "h-2")
	finish(t, store, h2.ID)
	page, err := store.ReadOperations(OperationsQuery{Host: "h", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != h1.ID {
		t.Fatalf("page 1 listed %v, want %s", page.Records, h1.ID)
	}
	// A compaction on host a (age-bound) removes no row of the pinned window;
	// the pinned h cursor is still pageable.
	a1 := createOp(t, store, "a", "a-1")
	finish(t, store, a1.ID)
	a2 := createOp(t, store, "a", "a-2")
	past := time.Now().UTC().Add(-31 * 24 * time.Hour)
	for i := range store.cell.state.Records {
		if store.cell.state.Records[i].ID == a1.ID {
			store.cell.state.Records[i].UpdatedAt = past
		}
	}
	finish(t, store, a2.ID) // the pass compacts the age-expired a1.
	if store.CursorEpoch().CompactSeq == 0 {
		t.Fatal("test setup: no compaction landed on host a")
	}
	if _, err := store.ReadOperations(OperationsQuery{Host: "h", Cursor: page.NextCursor}); err != nil {
		t.Fatalf("continuation before the floor moved: %v", err)
	}
	// The floor above the cursor's pin says marks in its range were dropped:
	// the store cannot know which rows that write removed, so it refuses
	// coarsely with the window's own host rather than skipping.
	store.cell.state.CompactionFloor = store.cell.state.CompactSeq
	_, err = store.ReadOperations(OperationsQuery{Host: "h", Cursor: page.NextCursor})
	invalidated, ok := errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("continuation with lost evidence = %v (%T), want the coarse *CursorInvalidatedError", err, err)
	}
	if invalidated.Host != "h" {
		t.Fatalf("coarse refusal host = %q, want the pinned host h", invalidated.Host)
	}
}

// TestOperationIDFilterPagesATombstoneOnce pins the continuation guard: a page
// whose only result is a tombstone replay must not re-append it on the next
// page (which would mint the identical cursor forever).
func TestOperationIDFilterPagesATombstoneOnce(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	cursorBoundary(t, store, "m4", 7, "inc-m4", 3)
	r1 := createOp(t, store, "m4", "op-a")
	finish(t, store, r1.ID)
	r2 := createOp(t, store, "m4", "op-b")
	finish(t, store, r2.ID) // compacts op-a into a tombstone.

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", ClientOperationID: "op-a", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(op-a page 1): %v", err)
	}
	if len(page.Records) != 1 || !page.Records[0].Compacted || page.Records[0].ID != r1.ID {
		t.Fatalf("page 1 = %+v, want the compacted op-a replay", page.Records)
	}
	if page.NextCursor == "" {
		t.Fatal("the replay page minted no continuation")
	}
	continued, err := store.ReadOperations(OperationsQuery{
		Host: "m4", ClientOperationID: "op-a", Limit: 1, Cursor: page.NextCursor,
	})
	if err != nil {
		t.Fatalf("ReadOperations(op-a page 2): %v", err)
	}
	if len(continued.Records) != 0 {
		t.Fatalf("page 2 = %+v, want the replay served once", continued.Records)
	}
	if continued.NextCursor != "" {
		t.Fatalf("page 2 minted another cursor (%q); the page loops", continued.NextCursor)
	}
}

// TestCursorRefusalNamesTheEarliestInvalidatingWrite pins §8's "the compacting
// compactSeq": with two invalidating writes after the pin, the refusal names
// the earliest one, never the latest.
func TestCursorRefusalNamesTheEarliestInvalidatingWrite(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 2, TombstonesPerHost: 50})
	cursorBoundary(t, store, "m4", 7, "inc-m4", 3)
	var seeded []Record
	for i := 1; i <= 5; i++ {
		record := createOp(t, store, "m4", fmt.Sprintf("op-%d", i))
		finish(t, store, record.ID)
		seeded = append(seeded, record)
	}
	page, err := store.ReadOperations(OperationsQuery{Host: "m4", Limit: 5})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page.Records) != 2 || page.Records[1].ID != seeded[4].ID {
		t.Fatalf("page 1 = %+v, want the two retained records ending at %s", page.Records, seeded[4].ID)
	}
	r6 := createOp(t, store, "m4", "op-6")
	finish(t, store, r6.ID) // compacts r4: an id at or before pos.
	r7 := createOp(t, store, "m4", "op-7")
	finish(t, store, r7.ID) // compacts r5: the cursor's own row.
	_, err = store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page.NextCursor})
	invalidated, ok := errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("continuation = %v (%T), want *CursorInvalidatedError", err, err)
	}
	if invalidated.CompactSeq != 4 {
		t.Fatalf("refusal compactSeq = %d, want the earliest invalidating write's 4", invalidated.CompactSeq)
	}
}

// TestLingeringMarkerStillReplaysTheLiveIncarnationsTombstone pins the
// companion arm of the stale-marker rule: the marker may suppress the OLD
// removal's tombstone, but a tombstone pinned to the live current pair replays
// with compacted: true — the marker must not swallow the live incarnation's
// lost-response replay.
func TestLingeringMarkerStillReplaysTheLiveIncarnationsTombstone(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-old", PresenceEpoch: 2}},
		Removed:    map[string]RemovedHost{"m4": {RemovedAt: base, Generation: 7, IncarnationID: "inc-old"}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	first := createOpPair(t, store, "m4", "live-1", 8, "inc-new")
	finish(t, store, first.ID)
	second := createOpPair(t, store, "m4", "live-2", 8, "inc-new")
	finish(t, store, second.ID) // compacts live-1 into a tombstone pinned to 8/inc-new.
	if _, ok := store.Record(first.ID); ok {
		t.Fatal("test setup: the live record was not compacted")
	}

	replayed, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "live-1", Host: "m4", Kind: KindDeploy,
		Current: OperationPair{Generation: 8, IncarnationID: "inc-new"},
	})
	if err != nil {
		t.Fatalf("LookupOperation: %v", err)
	}
	if !hit || !replayed.Compacted {
		t.Fatalf("live-incarnation replay = hit %v compacted %v, want the compacted: true replay", hit, replayed.Compacted)
	}
}

// TestLingeringRemovalMarkerNeverReplaysAgainstANewPair pins the re-add
// clean-slate rule over a marker whose clear mirror write trailed: the request
// resolves the name's current pair as the new incarnation, so the old
// removal's tombstone must not replay even though the marker still names it.
func TestLingeringRemovalMarkerNeverReplaysAgainstANewPair(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return base }
	old := createOpPair(t, store, "m4", "op-old", 7, "inc-m4")
	finish(t, store, old.ID)
	other := createOpPair(t, store, "m4", "op-other", 7, "inc-m4")
	finish(t, store, other.ID) // compacts op-old into a tombstone pinned to 7/inc-m4.
	if err := store.MirrorHostState(HostMirror{
		Boundaries: map[string]Boundary{"m4": {Generation: 7, IncarnationID: "inc-m4", PresenceEpoch: 2}},
		Removed:    map[string]RemovedHost{"m4": {RemovedAt: base, Generation: 7, IncarnationID: "inc-m4"}},
	}); err != nil {
		t.Fatalf("MirrorHostState: %v", err)
	}
	// The name is live again at 8/inc-new (the clear mirror failed and was
	// logged); the request's current pair is the new incarnation's.
	_, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: "op-old", Host: "m4", Kind: KindDeploy,
		Current: OperationPair{Generation: 8, IncarnationID: "inc-new"},
	})
	if err != nil {
		t.Fatalf("LookupOperation: %v", err)
	}
	if hit {
		t.Fatal("a lingering removal marker replayed the old removal against the new pair")
	}
}

// TestCursorRefusalUsesTheCoarseArmWhenEvidenceIsDropped pins the floor-first
// ordering: with evidence lost (the floor above the pin), the refusal is the
// coarse arm — the oldest affecting retained in-window mark — even when a
// later retained mark would match exactly. The exact arm must never present a
// later retained write as "the compacting" write when an earlier one's marks
// were evicted.
func TestCursorRefusalUsesTheCoarseArmWhenEvidenceIsDropped(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 50, TerminalStoreWide: 50})
	cursorBoundary(t, store, "y", 7, "inc-y", 1)
	cursorBoundary(t, store, "x", 7, "inc-x", 1)
	y1 := createOp(t, store, "y", "y-1")
	finish(t, store, y1.ID)
	x0 := createOp(t, store, "x", "x-0")          // pending: keeps x's minted entry a triple.
	anchor := createOp(t, store, "y", "y-anchor") // pending: never a victim.

	page, err := store.ReadOperations(OperationsQuery{Limit: 5})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page.Records) != 3 || page.Records[2].ID != anchor.ID {
		t.Fatalf("page 1 = %+v, want the anchor %s last", page.Records, anchor.ID)
	}
	// x1 lands after the mint, so its id sits after the cursor's position.
	x1 := createOp(t, store, "x", "x-1")
	finish(t, store, x1.ID)
	// Mark A (seq 1): x1's removal sits after the cursor's position.
	past := time.Now().UTC().Add(-31 * 24 * time.Hour)
	for i := range store.cell.state.Records {
		if store.cell.state.Records[i].ID == x1.ID {
			store.cell.state.Records[i].UpdatedAt = past
		}
	}
	x2 := createOp(t, store, "x", "x-2") // the pass compacts x1 (id > pos).
	// Mark B (seq 2): y1's removal sits at or before the cursor's position.
	for i := range store.cell.state.Records {
		if store.cell.state.Records[i].ID == y1.ID {
			store.cell.state.Records[i].UpdatedAt = past
		}
	}
	y2 := createOp(t, store, "y", "y-2") // the pass compacts y1 (id <= pos).
	if _, ok := store.Record(x1.ID); ok {
		t.Fatal("test setup: x1 was not compacted")
	}
	if _, ok := store.Record(y1.ID); ok {
		t.Fatal("test setup: y1 was not compacted")
	}
	_ = x0
	_ = x2
	_ = y2

	// Exact coverage: the refusal names the write whose row sat at or before
	// the position (seq 2 on y).
	_, err = store.ReadOperations(OperationsQuery{Cursor: page.NextCursor, Limit: 5})
	invalidated, ok := errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("continuation = %v (%T), want *CursorInvalidatedError", err, err)
	}
	if invalidated.CompactSeq != 2 {
		t.Fatalf("exact refusal compactSeq = %d, want 2", invalidated.CompactSeq)
	}
	// Evidence dropped above the pin: the coarse arm names the oldest
	// affecting retained mark (seq 1 on x), never the later exact-looking one.
	store.cell.state.CompactionFloor = 2
	_, err = store.ReadOperations(OperationsQuery{Cursor: page.NextCursor, Limit: 5})
	invalidated, ok = errors.AsType[*CursorInvalidatedError](err)
	if !ok {
		t.Fatalf("coarse continuation = %v (%T), want *CursorInvalidatedError", err, err)
	}
	if invalidated.CompactSeq != 1 || invalidated.Host != "x" {
		t.Fatalf("coarse refusal = compactSeq %d host %q, want the oldest affecting mark (1, x)",
			invalidated.CompactSeq, invalidated.Host)
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
