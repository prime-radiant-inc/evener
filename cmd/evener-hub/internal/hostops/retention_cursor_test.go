package hostops

// Cursor-invalidated tests (deploy pipeline 08b §8's refusal, §11's catalog
// entry, §12's "Cursor-invalidated" row). S6's store-level half: a compaction
// that removed rows at or before the cursor's pos since the cursor was minted
// refuses typed cursor-invalidated naming the compacting compactSeq plus the
// affected host's stored bounds entry; an unchanged compactSeq keeps the
// existing stale re-list.

import (
	"errors"
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
		Removed:    map[string]time.Time{"gone": base.Add(-2 * time.Hour)},
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
