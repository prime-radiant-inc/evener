package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestBoundedRefreshDoesNotWalkHiddenHistory pins how much paging work one live
// read performs on a session whose transcript grew past the daemon's page limit.
//
// The sequence is the one a long-running session goes through. Its first read
// returns the whole transcript, so the hub records a complete snapshot. The
// transcript then grows, and every later read returns only a bounded page, so the
// prefix retained from the complete snapshot is now hidden. Authenticating that
// hidden prefix by walking it costs one daemon round trip per page, so an
// unbounded walk grows with the session's length and repeats on the read after
// each growth. A bounded read instead walks at most a small fixed number of pages
// and rotates the snapshot identity past that bound, which turns every retained
// cursor into a typed stale error. Either way the read's paging cost stays
// independent of the transcript's length.
func TestBoundedRefreshDoesNotWalkHiddenHistory(t *testing.T) {
	t.Parallel()
	const (
		itemCount = 20_000
		pageSize  = uint64(40)
		// A read may authenticate a short hidden prefix, because a session that
		// just grew past one page is the common case and the walk is cheap there.
		// Anything approaching a page per session page is the unbounded walk.
		maxPagingRoundTripsPerRead = 8
	)
	all := positionedItems(1, itemCount)
	newest := boundedPageItems(itemCount, pageSize)
	olderCursor := inverseCursor(t, itemNativeIdentity, itemPosition(itemCount-pageSize+1))
	daemon := &scriptedItemDaemon{
		read: func(call int, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			if call == 0 {
				return itemReadResponse(all, ""), nil
			}
			return itemReadResponse(newest, olderCursor), nil
		},
		page: nativeHistoryPage(nativeHistoryCandidates(t, all), itemNativeIdentity),
	}
	source := newScriptedItemSource(t, daemon, "thread")
	params := appwire.ThreadReadParams{Ref: "local:thread", IncludeTurns: true, ItemLimit: 20}
	ctx := context.Background()

	complete, err := source.ReadThread(ctx, params)
	if err != nil {
		t.Fatalf("ReadThread (complete): %v", err)
	}
	if complete.OlderCursor != "" {
		t.Fatalf("fixture: the first read must be complete, got cursor %q", complete.OlderCursor)
	}
	if _, err := source.ItemCandidatesFromRead(ctx, params, complete); err != nil {
		t.Fatalf("ItemCandidatesFromRead (complete): %v", err)
	}

	for read := 1; read <= 2; read++ {
		bounded, err := source.ReadThread(ctx, params)
		if err != nil {
			t.Fatalf("ReadThread (bounded, read %d): %v", read, err)
		}
		if bounded.OlderCursor == "" {
			t.Fatalf("fixture: bounded read %d must carry an older cursor", read)
		}
		before := daemon.pageCalls()
		if _, err := source.ItemCandidatesFromRead(ctx, params, bounded); err != nil {
			t.Fatalf("ItemCandidatesFromRead (bounded, read %d): %v", read, err)
		}
		if walked := daemon.pageCalls() - before; walked > maxPagingRoundTripsPerRead {
			t.Fatalf("bounded read %d walked %d hidden-history pages on a %d-item session, want at most %d: the read's paging cost must not grow with the transcript",
				read, walked, itemCount, maxPagingRoundTripsPerRead)
		}
	}
}
