package appsource

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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
