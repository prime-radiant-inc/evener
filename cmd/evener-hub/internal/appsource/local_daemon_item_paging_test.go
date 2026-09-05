package appsource

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/rendezvous"
)

func TestLocalItemPagingPreservesCursorAcrossSlidingNewestWindow(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "sliding-window")
	params := appwire.ThreadReadParams{Ref: "local:sliding-window", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40}
	nativeIdentity := appitempaging.CursorIdentity{ThreadRef: params.Ref, Incarnation: "native-sliding-window", ProjectionVersion: 1}
	firstNativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 1})
	if err != nil {
		t.Fatalf("encode first native cursor: %v", err)
	}
	shiftedNativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 2})
	if err != nil {
		t.Fatalf("encode shifted native cursor: %v", err)
	}

	first, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(1, 40, firstNativeCursor))
	if err != nil {
		t.Fatalf("first item response: %v", err)
	}
	oldCursor, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 20})
	if err != nil {
		t.Fatalf("encode retained cursor: %v", err)
	}
	second, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(2, 41, shiftedNativeCursor))
	if err != nil {
		t.Fatalf("sliding item response: %v", err)
	}
	if second.Identity.Incarnation != first.Identity.Incarnation {
		t.Fatalf("sliding 1..40 -> 2..41 rotated incarnation: first=%q second=%q", first.Identity.Incarnation, second.Identity.Incarnation)
	}
	if _, err := appitempaging.DecodeCursor(oldCursor, second.Identity); err != nil {
		t.Fatalf("cursor from overlapping retained window became stale after append: %v", err)
	}
	state, ok := source.itemSnapshots.get(params.Ref)
	if !ok || state.NativeCursor != firstNativeCursor {
		t.Fatalf("retained native cursor = %q, want original same-generation token", state.NativeCursor)
	}
}

func TestLocalItemPagingPreservesCursorAcrossDisjointAppendedNewestWindow(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "disjoint-window")
	params := appwire.ThreadReadParams{Ref: "local:disjoint-window", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 40}
	nativeIdentity := appitempaging.CursorIdentity{ThreadRef: params.Ref, Incarnation: "native-disjoint-window", ProjectionVersion: 1}
	firstNativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 1})
	if err != nil {
		t.Fatalf("encode first native cursor: %v", err)
	}
	secondNativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 41})
	if err != nil {
		t.Fatalf("encode second native cursor: %v", err)
	}

	first, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(1, 40, firstNativeCursor))
	if err != nil {
		t.Fatalf("first item response: %v", err)
	}
	retainedCursor, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 20})
	if err != nil {
		t.Fatalf("encode retained cursor: %v", err)
	}
	second, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(41, 80, secondNativeCursor))
	if err != nil {
		t.Fatalf("disjoint appended item response: %v", err)
	}
	if second.Identity.Incarnation != first.Identity.Incarnation {
		t.Fatalf("append-only 1..40 -> 41..80 rotated incarnation: first=%q second=%q", first.Identity.Incarnation, second.Identity.Incarnation)
	}
	if _, err := appitempaging.DecodeCursor(retainedCursor, second.Identity); err != nil {
		t.Fatalf("cursor from earlier window became stale after disjoint append: %v", err)
	}
}

func TestLocalItemPagingPreservesCursorAcrossUnchangedBoundedToCompleteRead(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "bounded-to-complete")
	params := appwire.ThreadReadParams{Ref: "local:bounded-to-complete", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 1}
	nativeIdentity := appitempaging.CursorIdentity{ThreadRef: params.Ref, Incarnation: "native-bounded-to-complete", ProjectionVersion: 1}
	fixture := inverseNativeFixture{items: positionedItemReadResponse(1, 2, "").Thread.Turns[0].Items, identity: nativeIdentity}
	source.dial = fixture.dial
	nativeCursor, err := appitempaging.EncodeCursor(nativeIdentity, appwire.ThreadItemPosition{Entry: 2})
	if err != nil {
		t.Fatalf("encode native cursor: %v", err)
	}

	first, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(2, 2, nativeCursor))
	if err != nil {
		t.Fatalf("bounded item response: %v", err)
	}
	retainedCursor, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 2})
	if err != nil {
		t.Fatalf("encode retained cursor: %v", err)
	}
	second, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(1, 2, ""))
	if err != nil {
		t.Fatalf("complete item response: %v", err)
	}
	if second.Identity.Incarnation != first.Identity.Incarnation {
		t.Fatalf("unchanged bounded [2] -> complete [1,2] rotated incarnation: first=%q second=%q", first.Identity.Incarnation, second.Identity.Incarnation)
	}
	if _, err := appitempaging.DecodeCursor(retainedCursor, second.Identity); err != nil {
		t.Fatalf("cursor from bounded read became stale after complete read: %v", err)
	}
}

func TestLocalItemPagingIdentityBindsEveryItemPosition(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "position-rewrite")
	params := appwire.ThreadReadParams{Ref: "local:position-rewrite", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 4}
	old := positionedItemReadResponseFor([]string{"A", "B", "C", "D"}, []uint64{0, 10, 15, 20}, "")
	first, err := source.ItemCandidatesFromRead(t.Context(), params, old)
	if err != nil {
		t.Fatalf("first item response: %v", err)
	}
	oldCursor, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 10})
	if err != nil {
		t.Fatalf("encode old B cursor: %v", err)
	}

	rewritten := positionedItemReadResponseFor([]string{"A", "B", "C", "D"}, []uint64{0, 5, 10, 20}, "")
	second, err := source.ItemCandidatesFromRead(t.Context(), params, rewritten)
	if err != nil {
		t.Fatalf("rewritten item response: %v", err)
	}
	if second.Identity.Incarnation == first.Identity.Incarnation {
		t.Fatalf("middle-position rewrite preserved incarnation %q", first.Identity.Incarnation)
	}
	if _, err := appitempaging.DecodeCursor(oldCursor, second.Identity); err == nil {
		t.Fatal("cursor before old B@10 remained valid after B@5,C@10 rewrite")
	}
}

func TestLocalItemPagingRejectsNonIncreasingCandidatesBeforeStateMutation(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "non-increasing")
	params := appwire.ThreadReadParams{Ref: "local:non-increasing", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2}
	response := positionedItemReadResponseFor([]string{"A", "B"}, []uint64{2, 1}, "native-older")
	if _, err := source.ItemCandidatesFromRead(t.Context(), params, response); err == nil {
		t.Fatal("non-increasing candidates returned nil error")
	}
	if got := retainedPagingStateCount(t, source.itemSnapshots); got != 0 {
		t.Fatalf("invalid candidates retained %d paging states, want 0", got)
	}
}

func newLocalItemReadConversionSource(t *testing.T, threadID string) *LocalDaemonSource {
	t.Helper()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, SourceID: "local", ThreadID: threadID, SessionID: threadID,
		WorkspaceRef: "local:" + threadID, InstanceID: "instance-1", Endpoint: "ws://unused.test",
	}
	return NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, nil)
}

func positionedItemReadResponse(first, last uint64, olderCursor string) appwire.ThreadReadResponse {
	keys := make([]string, 0, last-first+1)
	positions := make([]uint64, 0, last-first+1)
	for position := first; position <= last; position++ {
		keys = append(keys, fmt.Sprintf("key-%02d", position))
		positions = append(positions, position)
	}
	return positionedItemReadResponseFor(keys, positions, olderCursor)
}

func positionedItemReadResponseFor(keys []string, positions []uint64, olderCursor string) appwire.ThreadReadResponse {
	items := make([]appwire.ThreadItem, len(keys))
	for index, key := range keys {
		position := appwire.ThreadItemPosition{Entry: positions[index]}
		items[index] = appwire.ThreadItem{
			ID: key, TurnID: "turn_positioned", TranscriptKey: key, Position: &position,
		}
	}
	return appwire.ThreadReadResponse{
		Thread:   appwire.Thread{ID: "positioned", Turns: []appwire.Turn{{ID: "turn_positioned", Items: items}}},
		PageUnit: appwire.TranscriptPageUnitItem, OlderCursor: olderCursor,
	}
}
