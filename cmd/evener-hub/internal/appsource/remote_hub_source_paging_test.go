package appsource

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

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
