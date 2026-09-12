package appsource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// TestFlagDayLocalDaemonDefaultReadUsesOneBoundedNativePage is deliberately
// driven through the real AppWire client and a scripted transport. The daemon
// returns one truthful bounded page and an opaque continuation; a source that
// materializes the full history will make a second request and report an
// exhausted snapshot, which is the legacy behavior this test prevents.
func TestFlagDayLocalDaemonDefaultReadUsesOneBoundedNativePage(t *testing.T) {
	const (
		threadRef   = "local:thread"
		incarnation = "native-flag-day"
	)
	items := flagDayPositionedItems(10, 49, "item")
	nativeCursor, err := appitempaging.EncodeCursor(appitempaging.CursorIdentity{
		ThreadRef: threadRef, Incarnation: incarnation, ProjectionVersion: 1,
	}, *items[0].Position)
	if err != nil {
		t.Fatalf("encode native cursor: %v", err)
	}

	type request struct {
		method string
		params map[string]any
	}
	var requests []request
	nativeCalls := 0
	newTransport := func() *scriptedAppwireTransport {
		var transport *scriptedAppwireTransport
		transport = newScriptedAppwireTransport(func(_ context.Context, message appwire.Message) error {
			if message.Request == nil {
				return nil
			}
			params := map[string]any{}
			if len(message.Request.Params) != 0 {
				if err := json.Unmarshal(message.Request.Params, &params); err != nil {
					return fmt.Errorf("decode %s params: %w", message.Request.Method, err)
				}
			}
			requests = append(requests, request{method: message.Request.Method, params: params})
			switch message.Request.Method {
			case appwire.MethodInitialize:
				transport.recv <- appwire.ResponseMessage(message.Request.ID, appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion})
			case appwire.MethodThreadTurnsList:
				nativeCalls++
				// The second response is only present to make the old full-history
				// walk terminate cleanly; the assertion below rejects that walk.
				if nativeCalls == 1 {
					transport.recv <- appwire.ResponseMessage(message.Request.ID, appwire.ThreadTurnsListResponse{
						Data: []appwire.Turn{{ID: "turn", Items: items, ItemsView: appwire.TurnItemsViewFragment}}, NextCursor: nativeCursor,
					})
				} else {
					transport.recv <- appwire.ResponseMessage(message.Request.ID, appwire.ThreadTurnsListResponse{})
				}
			case appwire.MethodThreadRead:
				nativeCalls++
				transport.recv <- appwire.ResponseMessage(message.Request.ID, appwire.ThreadReadResponse{
					Thread: appwire.Thread{ID: "thread", Turns: []appwire.Turn{{ID: "turn", Items: items, ItemsView: appwire.TurnItemsViewFragment}}}, OlderCursor: nativeCursor,
				})
			default:
				return fmt.Errorf("unexpected daemon method %q", message.Request.Method)
			}
			return nil
		})
		return transport
	}

	source := newLocalItemReadConversionSource(t, "thread")
	source.dial = func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return newTransport(), nil
	}
	result, err := source.ReadItemCandidates(context.Background(), appwire.ThreadReadParams{
		Ref: threadRef, IncludeTurns: true,
	})
	if err != nil {
		t.Fatalf("default item read: %v", err)
	}
	if len(result.Candidates.Candidates) != len(items) {
		t.Fatalf("default item candidates = %d, want bounded page %d", len(result.Candidates.Candidates), len(items))
	}
	if nativeCalls != 1 {
		t.Fatalf("default item read made %d native daemon requests, want one bounded native request: %+v", nativeCalls, requests)
	}
	var params map[string]any
	for _, request := range requests {
		if request.method != appwire.MethodInitialize {
			params = request.params
			break
		}
	}
	if pageUnit, _ := params["pageUnit"].(string); pageUnit == "turn" {
		t.Fatalf("default item read issued legacy turn-mode request: %+v", params)
	}
	for _, retired := range []string{"turnLimit", "limit"} {
		if _, present := params[retired]; present {
			t.Fatalf("default item read sent retired %s field: %+v", retired, params)
		}
	}
	if got, ok := params["itemLimit"].(float64); !ok || int(got) != appwire.TranscriptItemPageLimit {
		t.Fatalf("default item read itemLimit = %#v, want %d", params["itemLimit"], appwire.TranscriptItemPageLimit)
	}
	if result.Exhausted {
		t.Fatal("default bounded item read reported exhausted despite native continuation")
	}
}

func TestFlagDayLocalDaemonItemOnlyPreservesBoundedAppendAndRewriteIdentity(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "thread")
	params := appwire.ThreadReadParams{Ref: "local:thread", IncludeTurns: true, ItemLimit: 40}

	firstItems := flagDayPositionedItems(10, 49, "item")
	firstCursor := flagDayCursor(t, *firstItems[0].Position)
	first, err := source.ItemCandidatesFromRead(t.Context(), params, flagDayItemResponse(firstItems, firstCursor))
	if err != nil {
		t.Fatalf("initial bounded item read: %v", err)
	}
	if first.Exhausted || len(first.Candidates.Candidates) != 40 {
		t.Fatalf("initial bounded result = %+v, want 40 non-exhausted items", first)
	}
	oldCursor, err := appitempaging.EncodeCursor(first.Identity, first.Candidates.Candidates[0].Position)
	if err != nil {
		t.Fatalf("encode outward cursor: %v", err)
	}

	appendedItems := flagDayPositionedItems(11, 50, "item")
	appended, err := source.ItemCandidatesFromRead(t.Context(), params, flagDayItemResponse(appendedItems, flagDayCursor(t, *appendedItems[0].Position)))
	if err != nil {
		t.Fatalf("bounded append: %v", err)
	}
	if appended.Identity != first.Identity {
		t.Fatalf("append rotated identity from %+v to %+v", first.Identity, appended.Identity)
	}
	if _, err := appitempaging.DecodeCursor(oldCursor, appended.Identity); err != nil {
		t.Fatalf("outward cursor did not survive append: %v", err)
	}

	rewrittenItems := flagDayPositionedItems(11, 50, "item")
	rewrittenItems[20].TranscriptKey = "rewritten-key"
	rewrittenItems[20].ID = "rewritten-id"
	rewritten, err := source.ItemCandidatesFromRead(t.Context(), params, flagDayItemResponse(rewrittenItems, flagDayCursor(t, *rewrittenItems[0].Position)))
	if err != nil {
		t.Fatalf("bounded rewrite: %v", err)
	}
	if rewritten.Identity == appended.Identity {
		t.Fatalf("rewrite preserved identity %+v", rewritten.Identity)
	}
	if _, err := appitempaging.DecodeCursor(oldCursor, rewritten.Identity); err == nil {
		t.Fatal("cursor from pre-rewrite snapshot remained valid")
	}
}

func TestFlagDayLocalDaemonCompleteItemSnapshotIsExhausted(t *testing.T) {
	source := newLocalItemReadConversionSource(t, "thread")
	items := flagDayPositionedItems(0, 2, "complete")
	result, err := source.ItemCandidatesFromRead(t.Context(), appwire.ThreadReadParams{
		Ref: "local:thread", IncludeTurns: true,
	}, flagDayItemResponse(items, ""))
	if err != nil {
		t.Fatalf("complete item read: %v", err)
	}
	if !result.Exhausted {
		t.Fatal("complete item snapshot reported an older page")
	}
	if result.Candidates.OlderCursor != "" {
		t.Fatalf("complete item snapshot older cursor = %q, want empty", result.Candidates.OlderCursor)
	}
	if len(result.Candidates.Candidates) != len(items) {
		t.Fatalf("complete item snapshot candidates = %d, want %d", len(result.Candidates.Candidates), len(items))
	}
}

func flagDayItemResponse(items []appwire.ThreadItem, olderCursor string) appwire.ThreadReadResponse {
	return appwire.ThreadReadResponse{
		Thread:      appwire.Thread{ID: "thread", Turns: []appwire.Turn{{ID: "turn", Items: items, ItemsView: appwire.TurnItemsViewFragment}}},
		OlderCursor: olderCursor,
	}
}

func flagDayPositionedItems(first, last uint64, prefix string) []appwire.ThreadItem {
	items := make([]appwire.ThreadItem, 0, last-first+1)
	for position := first; position <= last; position++ {
		itemPosition := appwire.ThreadItemPosition{Entry: 0, Item: uint32(position)}
		items = append(items, appwire.ThreadItem{
			ID: fmt.Sprintf("%s-id-%02d", prefix, position), TranscriptKey: fmt.Sprintf("%s-key-%02d", prefix, position),
			TurnID: "turn", Position: &itemPosition, Type: "agentMessage",
		})
	}
	return items
}

func flagDayCursor(t *testing.T, position appwire.ThreadItemPosition) string {
	t.Helper()
	cursor, err := appitempaging.EncodeCursor(appitempaging.CursorIdentity{
		ThreadRef: "local:thread", Incarnation: "native-flag-day", ProjectionVersion: 1,
	}, position)
	if err != nil {
		t.Fatalf("encode native cursor at %+v: %v", position, err)
	}
	return cursor
}
