package appsource

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// itemPageWithPositions builds one item-mode turn fragment carrying the given
// entry positions, optionally naming a remote continuation cursor.
func itemPageWithPositions(cursor string, entries ...uint64) appwire.ThreadTurnsListResponse {
	items := make([]appwire.ThreadItem, 0, len(entries))
	for index, entry := range entries {
		items = append(items, appwire.ThreadItem{
			Type:          "text",
			ID:            fmt.Sprintf("item-%d", index),
			TranscriptKey: fmt.Sprintf("key-%d", index),
			Position:      &appwire.ThreadItemPosition{Entry: entry},
		})
	}
	return appwire.ThreadTurnsListResponse{
		Data:       []appwire.Turn{{ID: "turn-1", Items: items}},
		NextCursor: cursor,
	}
}

// itemPageWithCursor builds one item-mode turn page carrying a positioned item.
func itemPageWithCursor(entry uint64, cursor string) appwire.ThreadTurnsListResponse {
	return appwire.ThreadTurnsListResponse{
		Data: []appwire.Turn{{
			ID: "turn-1",
			Items: []appwire.ThreadItem{{
				Type:          "text",
				ID:            "item-1",
				TranscriptKey: "key-1",
				Position:      &appwire.ThreadItemPosition{Entry: entry},
			}},
		}},
		NextCursor: cursor,
	}
}

func remoteItemCursor(t *testing.T, entry uint64) string {
	t.Helper()
	cursor, err := appitempaging.EncodeCursor(appitempaging.CursorIdentity{
		ThreadRef:         "local:t1",
		Incarnation:       "remote-incarnation-1",
		ProjectionVersion: 1,
	}, appwire.ThreadItemPosition{Entry: entry})
	if err != nil {
		t.Fatalf("encode remote cursor: %v", err)
	}
	return cursor
}

// RemoteHubSource must own the item cursor identity the controller consumes,
// retaining the remote hub's own opaque cursor behind it so a long transcript
// can page instead of failing the packing step.
func TestRemoteHubSourceListItemCandidatesPaginates(t *testing.T) {
	second := remoteItemCursor(t, 10)
	source, calls := newScriptedRemote(t, "host", func(_ string, params json.RawMessage) scriptedReply {
		var remote appwire.ThreadTurnsListParams
		if err := json.Unmarshal(params, &remote); err != nil {
			t.Errorf("decode turns params: %v", err)
		}
		if remote.Cursor == "" {
			return scriptedReply{result: itemPageWithCursor(10, second)}
		}
		if remote.Cursor != second {
			t.Errorf("continuation cursor = %q, want the remote cursor %q", remote.Cursor, second)
		}
		return scriptedReply{result: itemPageWithCursor(5, "")}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Exhausted || first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live cursor", first)
	}
	if first.Identity.ThreadRef == "" || first.Identity.Incarnation == "" || first.Identity.ProjectionVersion == 0 {
		t.Fatalf("identity = %+v, want a complete controller-owned identity", first.Identity)
	}
	before, err := appitempaging.DecodeCursor(first.Candidates.OlderCursor, first.Identity)
	if err != nil {
		t.Fatalf("decode first cursor: %v", err)
	}
	if before != first.Candidates.Candidates[0].Position {
		t.Fatalf("cursor boundary = %+v, want %+v", before, first.Candidates.Candidates[0].Position)
	}

	continuation, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref:       "host:t1",
		Cursor:    first.Candidates.OlderCursor,
		ItemsView: "fragment",
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if !continuation.Exhausted || continuation.Candidates.OlderCursor != "" {
		t.Fatalf("second page = %+v, want an exhausted page with no cursor", continuation)
	}
	if len(calls()) == 0 {
		t.Fatal("no remote turn list call recorded")
	}
	last := lastMethodCall(t, calls(), appwire.MethodThreadTurnsList)
	var advanced appwire.ThreadTurnsListParams
	if err := json.Unmarshal(last, &advanced); err != nil {
		t.Fatalf("decode continuation params: %v", err)
	}
	if advanced.Cursor != second {
		t.Fatalf("continuation remote cursor = %q, want %q", advanced.Cursor, second)
	}
}

// The read path mints the same controller-owned identity from the read
// response's OlderCursor so a thread/read page can be continued through
// thread/turns/list.
func TestRemoteHubSourceItemCandidatesFromReadPaginates(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	response := appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: itemPageWithCursor(10, "").Data},
		OlderCursor: remoteItemCursor(t, 10),
	}
	result, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, response)
	if err != nil {
		t.Fatalf("ItemCandidatesFromRead: %v", err)
	}
	if result.Exhausted || result.Candidates.OlderCursor == "" {
		t.Fatalf("result = %+v, want a live cursor", result)
	}
	if _, err := appitempaging.DecodeCursor(result.Candidates.OlderCursor, result.Identity); err != nil {
		t.Fatalf("decode read cursor: %v", err)
	}
}

// A remote page the hub returned in full can still be continued: the hub's size
// packer mints a cursor from the returned identity when it drops the oldest
// selected items, and that continuation must be served from the retained page
// instead of failing as stale.
func TestRemoteHubSourceCompletePageContinuationServedLocally(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(_ string, params json.RawMessage) scriptedReply {
		var remote appwire.ThreadTurnsListParams
		if err := json.Unmarshal(params, &remote); err != nil {
			t.Errorf("decode turns params: %v", err)
		}
		if remote.Cursor != "" {
			t.Errorf("complete-page continuation reached the remote with cursor %q", remote.Cursor)
		}
		return scriptedReply{result: itemPageWithPositions("", 1, 2, 3)}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("complete first page: %v", err)
	}
	if !first.Exhausted || first.Candidates.OlderCursor != "" {
		t.Fatalf("complete first page = %+v, want exhaustion with no cursor", first)
	}
	// The hub's packer drops the oldest selected items for size and re-mints a
	// controller cursor at the retained boundary.
	cursor, err := appitempaging.EncodeCursor(first.Identity, first.Candidates.Candidates[1].Position)
	if err != nil {
		t.Fatalf("encode dropped-boundary cursor: %v", err)
	}

	continuation, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref:       "host:t1",
		Cursor:    cursor,
		ItemsView: "fragment",
	})
	if err != nil {
		t.Fatalf("complete-page continuation: %v", err)
	}
	if len(continuation.Candidates.Candidates) != 1 || continuation.Candidates.Candidates[0].Position.Entry != 1 {
		t.Fatalf("continuation candidates = %+v, want the dropped oldest item", continuation.Candidates.Candidates)
	}
	if !continuation.Exhausted || continuation.Candidates.OlderCursor != "" {
		t.Fatalf("continuation = %+v, want exhaustion after the retained page", continuation)
	}
	turnsCalls := 0
	for _, call := range calls() {
		if call.method == appwire.MethodThreadTurnsList {
			turnsCalls++
		}
	}
	if turnsCalls != 1 {
		t.Fatalf("remote turns/list calls = %d, want exactly the first page", turnsCalls)
	}
}

// A fresh read of a thread whose transcript did not shrink must reuse the
// retained controller incarnation, so a cursor minted before the read still
// decodes afterwards. An untouched or appended transcript is compatible; a page
// that starts before the retained head is a rewrite and rotates the identity.
func TestRemoteHubSourceReusesIdentityWhileTranscriptDoesNotShrink(t *testing.T) {
	remoteCursor := remoteItemCursor(t, 10)
	source, _ := newScriptedRemote(t, "host", func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: itemPageWithPositions(remoteCursor, 8, 10)}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live cursor", first)
	}

	// An unchanged re-read (same newest position) keeps the incarnation.
	unchanged, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: itemPageWithPositions("", 8, 10).Data},
		OlderCursor: remoteCursor,
	})
	if err != nil {
		t.Fatalf("unchanged read: %v", err)
	}
	if unchanged.Identity != first.Identity {
		t.Fatalf("unchanged read identity = %+v, want the retained %+v", unchanged.Identity, first.Identity)
	}
	if _, err := appitempaging.DecodeCursor(first.Candidates.OlderCursor, unchanged.Identity); err != nil {
		t.Fatalf("cursor from before the re-read no longer decodes: %v", err)
	}

	// An append (newer head) is compatible too, so live clients keep paging.
	appended, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: itemPageWithPositions("", 11).Data},
		OlderCursor: remoteItemCursor(t, 11),
	})
	if err != nil {
		t.Fatalf("appended read: %v", err)
	}
	if appended.Identity != first.Identity {
		t.Fatalf("appended read identity = %+v, want the retained %+v", appended.Identity, first.Identity)
	}

	// A page whose newest position precedes the retained head is a rewrite: the
	// incarnation rotates so outstanding cursors fail closed.
	divergent, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: itemPageWithPositions("", 3).Data},
		OlderCursor: remoteItemCursor(t, 3),
	})
	if err != nil {
		t.Fatalf("divergent read: %v", err)
	}
	if divergent.Identity == first.Identity {
		t.Fatalf("divergent read kept identity %+v, want a fresh incarnation", divergent.Identity)
	}
}

// newConcurrentScriptedRemote answers each request on its own goroutine, unlike
// newScriptedRemote whose single receive loop serializes requests at the
// transport. It reports whether two non-initialize requests were ever in flight
// at the same time, so a caller can assert that the source serialized them.
// No SSH, no network, no host.
func newConcurrentScriptedRemote(t *testing.T, result any) (*RemoteHubSource, func() bool) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	var inFlight atomic.Int64
	var overlapped atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			request := *msg.Request
			go func() {
				if request.Method == appwire.MethodInitialize {
					data, _ := json.Marshal(appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"})
					_ = server.Send(ctx, appwire.ResponseMessage(request.ID, json.RawMessage(data)))
					return
				}
				if inFlight.Add(1) > 1 {
					overlapped.Store(true)
				}
				// Hold the request open long enough that a second unsynchronized
				// request for the same thread would definitely overlap it.
				time.Sleep(50 * time.Millisecond)
				inFlight.Add(-1)
				data, err := json.Marshal(result)
				if err != nil {
					return
				}
				_ = server.Send(ctx, appwire.ResponseMessage(request.ID, json.RawMessage(data)))
			}()
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		cancel()
		t.Fatalf("initialize scripted remote: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	return source, overlapped.Load
}

// Concurrent item pages for the same remote thread must not interleave: the
// retained remote cursor and its controller identity are one read-modify-write
// unit, so the source serializes them per thread exactly as LocalDaemonSource
// does. Without the per-thread lock, two fresh pages race into the remote at the
// same time and one request's RebaseCursor/put can be applied to the other's
// retained state.
func TestRemoteHubSourceSerializesPerThreadItemPaging(t *testing.T) {
	cursor := remoteItemCursor(t, 10)
	source, overlapped := newConcurrentScriptedRemote(t, itemPageWithCursor(10, cursor))

	const workers = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			<-start
			if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
				Ref:       "host:t1",
				ItemsView: "fragment",
			}); err != nil {
				t.Errorf("concurrent page: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()

	if overlapped() {
		t.Fatal("two item pages for one remote thread reached the remote hub at once; per-thread paging must be serialized")
	}
}

// itemPageWithKeyedEntries builds an item-mode page whose transcript key is
// derived from the entry position, so pages fetched from one transcript stay
// distinguishable across pages (unlike itemPageWithPositions, whose keys are
// per-page ordinals).
func itemPageWithKeyedEntries(cursor string, entries ...uint64) appwire.ThreadTurnsListResponse {
	items := make([]appwire.ThreadItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, appwire.ThreadItem{
			Type:          "text",
			ID:            fmt.Sprintf("item-%d", entry),
			TranscriptKey: fmt.Sprintf("key-%d", entry),
			Position:      &appwire.ThreadItemPosition{Entry: entry},
		})
	}
	return appwire.ThreadTurnsListResponse{
		Data:       []appwire.Turn{{ID: "turn-1", Items: items}},
		NextCursor: cursor,
	}
}

// Replaying a cursor minted before the oldest page was reached must still be
// answered once the continuation runs out of older pages. The remote hub returns
// only a tail page per request, so a complete page is the oldest page, not the
// whole transcript: retaining only it would make SelectCandidates reject a
// boundary minted for a newer page as stale even though nothing changed. The
// source retains the whole observed window instead, so a retried or duplicated
// request is idempotent across every boundary it ever minted.
func TestRemoteHubSourceCompleteContinuationAnswersEarlierBoundaries(t *testing.T) {
	page1Cursor := remoteItemCursor(t, 10)
	page2Cursor := remoteItemCursor(t, 7)
	source, _ := newScriptedRemote(t, "host", func(_ string, params json.RawMessage) scriptedReply {
		var remote appwire.ThreadTurnsListParams
		if err := json.Unmarshal(params, &remote); err != nil {
			t.Errorf("decode turns params: %v", err)
		}
		switch remote.Cursor {
		case "":
			return scriptedReply{result: itemPageWithKeyedEntries(page1Cursor, 10, 11, 12)}
		case page1Cursor:
			return scriptedReply{result: itemPageWithKeyedEntries(page2Cursor, 7, 8, 9)}
		case page2Cursor:
			return scriptedReply{result: itemPageWithKeyedEntries("", 4, 5, 6)}
		default:
			t.Errorf("unexpected remote cursor %q", remote.Cursor)
			return scriptedReply{result: appwire.ThreadTurnsListResponse{}}
		}
	})

	page1, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if page1.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live cursor", page1)
	}
	page2, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: page1.Candidates.OlderCursor,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if page2.Candidates.OlderCursor == "" {
		t.Fatalf("second page = %+v, want a live cursor", page2)
	}
	page3, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: page2.Candidates.OlderCursor,
	})
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if !page3.Exhausted || page3.Candidates.OlderCursor != "" {
		t.Fatalf("third page = %+v, want exhaustion", page3)
	}

	replayed, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: page2.Candidates.OlderCursor,
	})
	if err != nil {
		t.Fatalf("replayed cursor: %v", err)
	}
	if len(replayed.Candidates.Candidates) != 3 || replayed.Candidates.Candidates[0].Position.Entry != 4 {
		t.Fatalf("replayed candidates = %+v, want the oldest page [4,5,6]", replayed.Candidates.Candidates)
	}
	if !replayed.Exhausted || replayed.Candidates.OlderCursor != "" {
		t.Fatalf("replayed page = %+v, want exhaustion", replayed)
	}
}

// A fresh observation that re-reports an already-observed position with a
// different item is a rewrite even when the newest position is unchanged: the
// incarnation must rotate so cursors minted against the replaced history fail
// closed instead of being rebased onto the changed transcript.
func TestRemoteHubSourceRotatesIdentityWhenObservedItemRewritten(t *testing.T) {
	remoteCursor := remoteItemCursor(t, 10)
	source, _ := newScriptedRemote(t, "host", func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: itemPageWithKeyedEntries(remoteCursor, 8, 10)}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live cursor", first)
	}

	// The newest position (10) is unchanged, but the item at position 8 was
	// replaced, so the retained history is not demonstrably compatible.
	rewritten := appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: itemPageWithKeyedEntries("", 8, 10).Data},
		OlderCursor: remoteCursor,
	}
	rewritten.Thread.Turns[0].Items[0].TranscriptKey = "key-8-rewritten"
	result, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, rewritten)
	if err != nil {
		t.Fatalf("rewritten read: %v", err)
	}
	if result.Identity == first.Identity {
		t.Fatalf("rewritten read kept identity %+v, want a fresh incarnation", result.Identity)
	}
	if _, err := appitempaging.DecodeCursor(first.Candidates.OlderCursor, result.Identity); err == nil {
		t.Fatal("a cursor minted before the rewrite still decodes under the new incarnation")
	}
}

// An actively paged thread must not lose its retained continuation to a newer
// thread's insert; eviction reclaims the least recently committed continuation,
// matching itemSnapshotStateCache's MoveToFront-on-put.
func TestRemoteItemPagingCacheEvictsLeastRecentlyCommitted(t *testing.T) {
	var cache remoteItemPagingCache
	for index := range remoteItemPagingCapacity {
		cache.put(fmt.Sprintf("k%02d", index), remoteItemPagingState{native: fmt.Sprintf("n%d", index)})
	}
	cache.put("k00", remoteItemPagingState{native: "touched"})
	cache.put("overflow", remoteItemPagingState{native: "new"})

	if _, ok := cache.peek("k00"); !ok {
		t.Fatal("the most recently committed continuation was evicted")
	}
	if _, ok := cache.peek("k01"); ok {
		t.Fatal("the least recently committed continuation survived eviction")
	}
	if _, ok := cache.peek("overflow"); !ok {
		t.Fatal("the newly inserted continuation was evicted")
	}
}

// A forward page that shares no position with the retained window and cannot
// prove it abuts its newest item is not a rewrite: the source keeps the
// incarnation, so a cursor minted before the append stays live, and it records
// the span the page may have skipped instead of rotating. What must never happen
// is a local answer that spans the unobserved middle: once the remote cursor is
// exhausted, a complete window serves only the contiguous prefix below the
// oldest recorded span, a boundary above it fails closed as stale, and a boundary
// below it is still answered locally.
//
// This replaces the round-eight answer, TestRemoteHubSourceRejectsGappedForwardAppend,
// which rotated the incarnation and failed the pre-append boundary stale. That
// rule assumed the observed page could only be contiguous if it resumed at
// Entry+1 of the retained newest, but entry ordinals skip for logical groups with
// no visible items (TestEmptyLogicalGroupReservesOrdinalInFullAndIndexedProjections),
// so it staled live cursors on ordinary appends.
func TestRemoteHubSourceRecordsUnobservedForwardSpanWithoutRotating(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(_ string, params json.RawMessage) scriptedReply {
		var remote appwire.ThreadTurnsListParams
		if err := json.Unmarshal(params, &remote); err != nil {
			t.Errorf("decode turns params: %v", err)
		}
		if remote.Cursor == "" {
			return scriptedReply{result: itemPageWithKeyedEntries("", 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)}
		}
		return scriptedReply{result: itemPageWithKeyedEntries("", 1, 2, 3, 4)}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !first.Exhausted {
		t.Fatalf("first page = %+v, want a complete page", first)
	}
	// The hub's size packer can mint a continuation at an interior boundary of a
	// complete page.
	replay, err := appitempaging.EncodeCursor(first.Identity, first.Candidates.Candidates[4].Position)
	if err != nil {
		t.Fatalf("encode replay cursor: %v", err)
	}

	// The transcript grows past the retained window: a fresh read returns a much
	// newer tail that shares no position with the retained window and does not
	// resume at the successor of its newest item (entry 40 does not succeed entry
	// 10, and an empty logical group between them cannot be ruled out either).
	grown, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: itemPageWithKeyedEntries("", 40, 41, 42, 43, 44, 45, 46, 47, 48, 49).Data},
		OlderCursor: remoteItemCursor(t, 40),
	})
	if err != nil {
		t.Fatalf("grown read: %v", err)
	}
	if grown.Identity != first.Identity {
		t.Fatalf("disjoint append rotated the identity: got %+v, want the retained %+v", grown.Identity, first.Identity)
	}
	if _, err := appitempaging.DecodeCursor(replay, grown.Identity); err != nil {
		t.Fatalf("a cursor minted before the append no longer decodes: %v", err)
	}

	// The pre-append boundary stays live and is answered through the remote cursor,
	// whose authority covers the span the retained window never observed.
	served, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: replay,
	})
	if err != nil {
		t.Fatalf("replayed boundary: %v", err)
	}
	if got := served.Candidates.Candidates; len(got) != 4 || got[0].Position.Entry != 1 {
		t.Fatalf("replayed candidates = %+v, want the remote's page [1,2,3,4]", got)
	}
	if !served.Exhausted {
		t.Fatalf("replayed page = %+v, want exhaustion", served)
	}
	var forwarded appwire.ThreadTurnsListParams
	if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadTurnsList), &forwarded); err != nil {
		t.Fatalf("decode replayed params: %v", err)
	}
	if forwarded.Cursor == "" {
		t.Fatal("the replayed boundary was answered locally from a gapped window instead of through the remote cursor")
	}

	// The remote cursor is exhausted now and the window still holds no proof of
	// the middle, so a boundary above the unobserved span must fail closed rather
	// than be served from a window that silently omits it.
	aboveSpan, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 45})
	if err != nil {
		t.Fatalf("encode boundary above the span: %v", err)
	}
	if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: aboveSpan,
	}); err == nil {
		t.Fatal("a boundary above the unobserved span was served from the gapped window")
	} else {
		requireInverseStale(t, err)
	}

	// A boundary below the span is still served locally from the contiguous
	// prefix, so the retained window keeps answering every boundary it can prove.
	belowSpan, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 8})
	if err != nil {
		t.Fatalf("encode boundary below the span: %v", err)
	}
	below, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: belowSpan,
	})
	if err != nil {
		t.Fatalf("boundary below the unobserved span: %v", err)
	}
	if got := below.Candidates.Candidates; len(got) != 7 || got[0].Position.Entry != 1 || got[len(got)-1].Position.Entry != 7 {
		t.Fatalf("boundary below the span = %+v, want the contiguous prefix [1..7]", got)
	}
	if !below.Exhausted {
		t.Fatalf("boundary below the span = %+v, want exhaustion", below)
	}
}

// An ordinary append can advance the visible entry ordinal by more than one:
// every logical group consumes an entry ordinal, including one that projects no
// visible item, so the next visible turn after entry 10 can be entry 12. A fresh
// page that resumes at that later entry is not a rewrite; rotating the
// incarnation over it would stale the cursor a client minted before the append.
func TestRemoteHubSourceKeepsIdentityAcrossSkippedEntryAppend(t *testing.T) {
	older := remoteItemCursor(t, 4)
	rebased := remoteItemCursor(t, 8)
	source, calls := newScriptedRemote(t, "host", func(_ string, params json.RawMessage) scriptedReply {
		var remote appwire.ThreadTurnsListParams
		if err := json.Unmarshal(params, &remote); err != nil {
			t.Errorf("decode turns params: %v", err)
		}
		switch remote.Cursor {
		case "":
			return scriptedReply{result: itemPageWithKeyedEntries(older, 8, 10)}
		case rebased:
			return scriptedReply{result: itemPageWithKeyedEntries("", 4, 5, 6)}
		default:
			t.Errorf("unexpected remote cursor %q", remote.Cursor)
			return scriptedReply{result: appwire.ThreadTurnsListResponse{}}
		}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live cursor", first)
	}

	// Ordinal 11 is a logical group with no visible items, so the appended tail
	// resumes at entry 12.
	appended, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: itemPageWithKeyedEntries("", 12, 13).Data},
		OlderCursor: remoteItemCursor(t, 12),
	})
	if err != nil {
		t.Fatalf("appended read: %v", err)
	}
	if appended.Identity != first.Identity {
		t.Fatalf("skipped-entry append rotated the identity: got %+v, want the retained %+v", appended.Identity, first.Identity)
	}
	if _, err := appitempaging.DecodeCursor(first.Candidates.OlderCursor, appended.Identity); err != nil {
		t.Fatalf("a cursor minted before the append no longer decodes: %v", err)
	}

	// The cursor stays live, and its continuation is answered from the remote: the
	// retained window never observed the span between the two pages, so the remote
	// cursor is the authority for what lies between them.
	continuation, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: first.Candidates.OlderCursor,
	})
	if err != nil {
		t.Fatalf("continuation of a pre-append cursor: %v", err)
	}
	if got := continuation.Candidates.Candidates; len(got) != 3 || got[0].Position.Entry != 4 {
		t.Fatalf("continuation = %+v, want the remote's older page [4,5,6]", got)
	}
	if !continuation.Exhausted {
		t.Fatalf("continuation = %+v, want exhaustion", continuation)
	}
	var advanced appwire.ThreadTurnsListParams
	if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadTurnsList), &advanced); err != nil {
		t.Fatalf("decode continuation params: %v", err)
	}
	if advanced.Cursor != rebased {
		t.Fatalf("continuation remote cursor = %q, want the boundary rebased to %q", advanced.Cursor, rebased)
	}
}

// A decoded cursor boundary is a fence, not free-form input: it must still name
// an item the retained window observed before it is rebased and forwarded. A
// caller that preserves a valid identity but moves the boundary would otherwise
// page the remote from a position this incarnation never proved.
func TestRemoteHubSourceRejectsCursorBoundaryOutsideRetainedWindow(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(_ string, params json.RawMessage) scriptedReply {
		var remote appwire.ThreadTurnsListParams
		if err := json.Unmarshal(params, &remote); err != nil {
			t.Errorf("decode turns params: %v", err)
		}
		if remote.Cursor != "" {
			t.Errorf("tampered boundary reached the remote: %q", remote.Cursor)
		}
		return scriptedReply{result: itemPageWithKeyedEntries(remoteItemCursor(t, 10), 10)}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live cursor", first)
	}
	tampered, err := appitempaging.EncodeCursor(first.Identity, appwire.ThreadItemPosition{Entry: 9999})
	if err != nil {
		t.Fatalf("encode tampered cursor: %v", err)
	}

	if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: tampered,
	}); err == nil {
		t.Fatal("a cursor boundary outside the retained window was rebased and forwarded")
	} else {
		requireInverseStale(t, err)
	}
	for _, call := range calls() {
		if call.method != appwire.MethodThreadTurnsList {
			continue
		}
		var remote appwire.ThreadTurnsListParams
		if err := json.Unmarshal(call.params, &remote); err != nil {
			t.Fatalf("decode turns params: %v", err)
		}
		if remote.Cursor != "" {
			t.Fatalf("the tampered boundary reached the remote: %q", remote.Cursor)
		}
	}
}

// Traversing a long transcript merges one page per continuation into the
// retained window. The bound must cap that window: past it the source rotates
// the incarnation and keeps only the newest page, so the cursor just returned
// stays live while older boundaries fail closed as stale instead of being
// answered from a silently truncated history.
func TestRemoteItemPagingBoundsRetainedCandidates(t *testing.T) {
	const pageSize = 40
	pages := remoteItemPagingCandidateCapacity/pageSize + 4
	native := remoteItemCursor(t, 1)
	var served atomic.Int64
	source, _ := newScriptedRemote(t, "host", func(_ string, _ json.RawMessage) scriptedReply {
		page := int(served.Add(1))
		base := uint64(2_000_000 - page*100)
		entries := make([]uint64, pageSize)
		for index := range entries {
			entries[index] = base - pageSize + 1 + uint64(index)
		}
		return scriptedReply{result: itemPageWithKeyedEntries(native, entries...)}
	})

	cursor := ""
	firstCursor := ""
	for page := range pages {
		result, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
			Ref: "host:t1", ItemsView: "fragment", Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if result.Candidates.OlderCursor == "" {
			t.Fatalf("page %d = %+v, want a live cursor", page, result)
		}
		if page == 0 {
			firstCursor = result.Candidates.OlderCursor
		}
		cursor = result.Candidates.OlderCursor
	}
	if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: firstCursor,
	}); err == nil {
		t.Fatal("a cursor older than the bounded retained window was still served")
	} else {
		requireInverseStale(t, err)
	}
}

// itemPageWithItemPositions builds one item-mode turn fragment carrying the
// given explicit (entry, item) positions, so a page can begin mid-entry.
func itemPageWithItemPositions(cursor string, positions ...appwire.ThreadItemPosition) appwire.ThreadTurnsListResponse {
	items := make([]appwire.ThreadItem, 0, len(positions))
	for _, position := range positions {
		pos := position
		items = append(items, appwire.ThreadItem{
			Type:          "text",
			ID:            fmt.Sprintf("item-%d-%d", position.Entry, position.Item),
			TranscriptKey: fmt.Sprintf("key-%d-%d", position.Entry, position.Item),
			Position:      &pos,
		})
	}
	return appwire.ThreadTurnsListResponse{
		Data:       []appwire.Turn{{ID: "turn-1", Items: items}},
		NextCursor: cursor,
	}
}

// A single entry can project several items, so a forward page whose first item
// is at a non-zero item index of the next entry leaves the earlier items of that
// entry (and any later items of the previous entry) unobserved. It is therefore
// not contiguous with the retained newest item and must rotate the incarnation
// instead of being unioned into a holed window. Regression guard for
// remotePositionsAdjacent treating any (entry+1) as the successor.
func TestRemoteHubSourceRejectsForwardPageBeginningMidEntry(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: itemPageWithItemPositions(remoteItemCursor(t, 10), appwire.ThreadItemPosition{Entry: 10})}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live cursor", first)
	}

	// Entry 11 is observed from item 5 onward: items 0..4 of entry 11 were never
	// returned, so the page does not abut the retained newest item (10, 0).
	grown, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: itemPageWithItemPositions("", appwire.ThreadItemPosition{Entry: 11, Item: 5}, appwire.ThreadItemPosition{Entry: 11, Item: 6}).Data},
		OlderCursor: remoteItemCursor(t, 11),
	})
	if err != nil {
		t.Fatalf("grown read: %v", err)
	}
	if grown.Identity == first.Identity {
		t.Fatalf("non-abutting forward page kept identity %+v, want a fresh incarnation", grown.Identity)
	}
}

// itemPageEndingMidTurn builds one item-mode page whose single turn fragment
// carries HasLaterItems, i.e. the remote returned only the leading items of a
// turn that continues after the page.
func itemPageEndingMidTurn(cursor string, positions ...appwire.ThreadItemPosition) appwire.ThreadTurnsListResponse {
	page := itemPageWithItemPositions(cursor, positions...)
	page.Data[0].HasLaterItems = true
	return page
}

// A page that ends mid-turn leaves later items of that turn unobserved. A fresh
// page that resumes at the next entry is position-adjacent, but the retained
// fragment's HasLaterItems records that items in between exist: unioning the two
// would retain a holed window whose later completion silently omits those items.
// The incarnation must rotate instead, so the pre-append boundary fails closed.
func TestRemoteHubSourceRejectsForwardMergeAcrossMidTurnBoundary(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: itemPageEndingMidTurn(remoteItemCursor(t, 5),
			appwire.ThreadItemPosition{Entry: 5, Item: 0},
			appwire.ThreadItemPosition{Entry: 5, Item: 1},
		)}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live cursor", first)
	}

	// The transcript grew: the fresh tail starts at the next entry (position
	// (6,0)), position-adjacent to the retained newest (5,1) but skipping the
	// remainder of entry 5 that the retained fragment's flag says exists.
	grown, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: itemPageWithItemPositions("", appwire.ThreadItemPosition{Entry: 6, Item: 0}).Data},
		OlderCursor: remoteItemCursor(t, 6),
	})
	if err != nil {
		t.Fatalf("grown read: %v", err)
	}
	if grown.Identity == first.Identity {
		t.Fatalf("a mid-turn boundary was merged as contiguous: identity %+v reused", grown.Identity)
	}
	if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: first.Candidates.OlderCursor,
	}); err == nil {
		t.Fatal("a cursor minted before the rotation was still served")
	} else {
		requireInverseStale(t, err)
	}
}

// A remote page that alone exceeds the retained window bound must not be kept
// wholesale: the rotated window keeps only the newest candidates that fit, so
// the documented bound holds even for one oversized page.
func TestRemoteItemPagingTrimsOversizedRotatedPage(t *testing.T) {
	oversized := remoteItemPagingCandidateCapacity + 25
	entries := make([]uint64, oversized)
	for index := range entries {
		entries[index] = uint64(index + 1)
	}
	source, _ := newScriptedRemote(t, "host", func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: itemPageWithKeyedEntries(remoteItemCursor(t, uint64(oversized)), entries...)}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	state, ok := source.itemPaging.peek(remoteItemPagingKey("host", "t1"))
	if !ok {
		t.Fatal("no retained paging state")
	}
	if len(state.candidates) != remoteItemPagingCandidateCapacity {
		t.Fatalf("retained candidates = %d, want the bound %d", len(state.candidates), remoteItemPagingCandidateCapacity)
	}
	if got := state.candidates[len(state.candidates)-1].Position.Entry; got != uint64(oversized) {
		t.Fatalf("retained newest entry = %d, want %d (the newest page item)", got, oversized)
	}
	if remoteItemCandidatesBytes(state.candidates) > remoteItemPagingByteCapacity {
		t.Fatalf("retained window bytes = %d, want <= %d", remoteItemCandidatesBytes(state.candidates), remoteItemPagingByteCapacity)
	}
	// The rotation trimmed the page, so the continuation it minted must name a
	// position the retained window actually holds; otherwise the caller's first
	// continuation fails ValidateCursorBoundary as stale.
	if first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live continuation cursor", first)
	}
	before, err := appitempaging.DecodeCursor(first.Candidates.OlderCursor, first.Identity)
	if err != nil {
		t.Fatalf("decode first cursor: %v", err)
	}
	if len(first.Candidates.Candidates) == 0 || before != first.Candidates.Candidates[0].Position {
		t.Fatalf("cursor boundary = %+v, want the returned window's oldest %+v", before, first.Candidates.Candidates)
	}
	continuation, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: first.Candidates.OlderCursor,
	})
	if err != nil {
		t.Fatalf("continuation after trimming the rotated page = %v, want it to resume from a retained boundary", err)
	}
	if continuation.Candidates.OlderCursor == "" {
		t.Fatalf("continuation = %+v, want a live cursor", continuation)
	}
}

// The byte bound is enforced on a single page too: the newest candidates that
// fit the budget are retained, and the newest candidate is kept even when it
// alone exceeds the budget so the window is never emptied.
func TestRemoteItemPagingTrimsOversizedRotatedPageByBytes(t *testing.T) {
	const itemBytes = 2 << 20
	page := itemPageWithLargeText(remoteItemCursor(t, 6), 5, itemBytes)
	source, _ := newScriptedRemote(t, "host", func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: page}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	state, ok := source.itemPaging.peek(remoteItemPagingKey("host", "t1"))
	if !ok {
		t.Fatal("no retained paging state")
	}
	if got := remoteItemCandidatesBytes(state.candidates); got > remoteItemPagingByteCapacity {
		t.Fatalf("retained window bytes = %d, want <= %d", got, remoteItemPagingByteCapacity)
	}
	if len(state.candidates) != 4 {
		t.Fatalf("retained candidates = %d, want the newest 4 that fit the byte budget", len(state.candidates))
	}
	if got := state.candidates[len(state.candidates)-1].Position.Entry; got != 5 {
		t.Fatalf("retained newest entry = %d, want 5", got)
	}
	// As above: the trimmed rotation must mint its continuation from the retained
	// suffix, or the first continuation is served a never-retained boundary.
	if first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live continuation cursor", first)
	}
	before, err := appitempaging.DecodeCursor(first.Candidates.OlderCursor, first.Identity)
	if err != nil {
		t.Fatalf("decode first cursor: %v", err)
	}
	if len(first.Candidates.Candidates) == 0 || before != first.Candidates.Candidates[0].Position {
		t.Fatalf("cursor boundary = %+v, want the returned window's oldest %+v", before, first.Candidates.Candidates)
	}
	if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{
		Ref: "host:t1", ItemsView: "fragment", Cursor: first.Candidates.OlderCursor,
	}); err != nil {
		t.Fatalf("continuation after trimming the rotated page = %v, want it to resume from a retained boundary", err)
	}
}

// itemPageWithLargeText builds one item-mode page of count items whose payload
// is size bytes each, so a single page can exceed the byte bound.
func itemPageWithLargeText(cursor string, count, size int) appwire.ThreadTurnsListResponse {
	items := make([]appwire.ThreadItem, 0, count)
	for index := range count {
		items = append(items, appwire.ThreadItem{
			Type:          "text",
			ID:            fmt.Sprintf("item-%d", index),
			TranscriptKey: fmt.Sprintf("key-%d", index),
			Position:      &appwire.ThreadItemPosition{Entry: uint64(index + 1)},
			Text:          strings.Repeat("x", size),
		})
	}
	return appwire.ThreadTurnsListResponse{
		Data:       []appwire.Turn{{ID: "turn-1", Items: items}},
		NextCursor: cursor,
	}
}

// The retained byte bound must account for the nested image payloads of an item,
// not just its string fields: a page of base64 input images whose visible text is
// short would otherwise be retained far past the documented 8 MiB.
func TestRemoteItemPagingBoundsRetainedImageBytes(t *testing.T) {
	// Three 3 MiB images cannot fit two-to-a-page inside the 8 MiB bound, so the
	// newest two are retained and the third is dropped.
	const imageBytes = 3 << 20
	page := itemPageWithImages(remoteItemCursor(t, 5), 5, imageBytes)
	source, _ := newScriptedRemote(t, "host", func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: page}
	})

	first, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Candidates.OlderCursor == "" {
		t.Fatalf("first page = %+v, want a live continuation cursor", first)
	}
	state, ok := source.itemPaging.peek(remoteItemPagingKey("host", "t1"))
	if !ok {
		t.Fatal("no retained paging state")
	}
	if got := remoteItemCandidatesBytes(state.candidates); got > remoteItemPagingByteCapacity {
		t.Fatalf("retained window bytes = %d, want <= %d", got, remoteItemPagingByteCapacity)
	}
	if len(state.candidates) != 2 {
		t.Fatalf("retained candidates = %d, want the newest 2 that fit the byte budget", len(state.candidates))
	}
	if got := state.candidates[len(state.candidates)-1].Position.Entry; got != 5 {
		t.Fatalf("retained newest entry = %d, want 5", got)
	}
	// As with the text-heavy page, the trimmed rotation must mint its continuation
	// from the retained suffix.
	before, err := appitempaging.DecodeCursor(first.Candidates.OlderCursor, first.Identity)
	if err != nil {
		t.Fatalf("decode first cursor: %v", err)
	}
	if len(first.Candidates.Candidates) == 0 || before != first.Candidates.Candidates[0].Position {
		t.Fatalf("cursor boundary = %+v, want the returned window's oldest %+v", before, first.Candidates.Candidates)
	}
}

// itemPageWithImages builds one item-mode page of count items whose payload is an
// input image of size bytes each, so the page's retention cost lives in a field a
// string-only estimate cannot see.
func itemPageWithImages(cursor string, count, size int) appwire.ThreadTurnsListResponse {
	items := make([]appwire.ThreadItem, 0, count)
	for index := range count {
		items = append(items, appwire.ThreadItem{
			Type:          "text",
			ID:            fmt.Sprintf("item-%d", index),
			TranscriptKey: fmt.Sprintf("key-%d", index),
			Position:      &appwire.ThreadItemPosition{Entry: uint64(index + 1)},
			Images: []appwire.InputItem{{
				Type:      "image",
				MediaType: "image/png",
				Data:      make([]byte, size),
			}},
		})
	}
	return appwire.ThreadTurnsListResponse{
		Data:       []appwire.Turn{{ID: "turn-1", Items: items}},
		NextCursor: cursor,
	}
}

// A remote hub is a separate process and may be a different version, so its item
// fragments must satisfy the contract the local daemon source validates before
// anything is merged or cached: strictly increasing, uniquely-keyed candidates.
// An out-of-order page must fail closed rather than poison the retained cursor
// identity for later continuations.
func TestRemoteHubSourceRejectsUnorderedRemoteItemPage(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: itemPageWithKeyedEntries(remoteItemCursor(t, 8), 10, 8)}
	})
	if _, err := source.ListItemCandidates(context.Background(), appwire.ThreadTurnsListParams{Ref: "host:t1", ItemsView: "fragment"}); err == nil {
		t.Fatal("an out-of-order remote item page was accepted")
	} else if !strings.Contains(err.Error(), "strictly increasing") {
		t.Fatalf("error = %v, want a strict-order rejection", err)
	}
	if _, ok := source.itemPaging.peek(remoteItemPagingKey("host", "t1")); ok {
		t.Fatal("a rejected page was retained")
	}
}

// The already-materialized read path validates the same contract: a page whose
// fragments repeat a transcript key must fail closed before an identity is minted
// over a window the paging package can never regroup.
func TestRemoteHubSourceRejectsDuplicateKeyRemoteItemRead(t *testing.T) {
	source := NewRemoteHubSource("host", nil, nil)
	page := itemPageWithKeyedEntries("", 4, 5)
	page.Data[0].Items[1].TranscriptKey = page.Data[0].Items[0].TranscriptKey
	_, err := source.ItemCandidatesFromRead(context.Background(), appwire.ThreadReadParams{Ref: "host:t1"}, appwire.ThreadReadResponse{
		Thread:      appwire.Thread{Turns: page.Data},
		OlderCursor: remoteItemCursor(t, 4),
	})
	if err == nil {
		t.Fatal("a remote item read with a duplicated transcript key was accepted")
	} else if !strings.Contains(err.Error(), "repeats transcript key") {
		t.Fatalf("error = %v, want a duplicate-key rejection", err)
	}
	if _, ok := source.itemPaging.peek(remoteItemPagingKey("host", "t1")); ok {
		t.Fatal("a rejected read was retained")
	}
}
