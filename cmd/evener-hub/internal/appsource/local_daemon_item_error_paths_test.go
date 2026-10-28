package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// scriptedItemDaemon answers the local daemon's item-mode RPCs from injected
// closures, one fresh scripted transport per RPC, mirroring inverseNativeFixture.
// Non-wire errors become transport failures, which the source maps to
// SessionUnavailable; wire errors frame as application error responses.
type scriptedItemDaemon struct {
	mu    sync.Mutex
	reads int
	pages int
	read  func(call int, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error)
	page  func(call int, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error)
}

func (d *scriptedItemDaemon) nextReadCall() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	call := d.reads
	d.reads++
	return call
}

func (d *scriptedItemDaemon) nextPageCall() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	call := d.pages
	d.pages++
	return call
}

func (d *scriptedItemDaemon) dial(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
	transport := newScriptedAppwireTransport(nil)
	transport.send = func(_ context.Context, msg appwire.Message) error {
		if msg.Request == nil {
			return nil
		}
		request := msg.Request
		reply := func(response any, err error) error {
			if err != nil {
				if wire, ok := errors.AsType[appwire.WireError](err); ok {
					transport.recv <- appwire.ErrorMessage(request.ID, wire)
					return nil
				}
				return err
			}
			transport.recv <- appwire.ResponseMessage(request.ID, response)
			return nil
		}
		switch request.Method {
		case appwire.MethodThreadRead:
			var params appwire.ThreadReadParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return err
			}
			response, err := d.read(d.nextReadCall(), params)
			return reply(response, err)
		case appwire.MethodThreadTurnsList:
			var params appwire.ThreadTurnsListParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return err
			}
			response, err := d.page(d.nextPageCall(), params)
			return reply(response, err)
		default:
			transport.recv <- appwire.ResponseMessage(request.ID, appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion})
			return nil
		}
	}
	return transport, nil
}

func newScriptedItemSource(t *testing.T, daemon *scriptedItemDaemon, threadID string) *LocalDaemonSource {
	t.Helper()
	source := newLocalItemReadConversionSource(t, threadID)
	source.dial = daemon.dial
	return source
}

func itemFragmentTurn(items []appwire.ThreadItem) appwire.Turn {
	return appwire.Turn{ID: "turn-1", Items: items, ItemsView: appwire.TurnItemsViewFragment}
}

func itemReadResponse(items []appwire.ThreadItem, olderCursor string) appwire.ThreadReadResponse {
	return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread-1", Turns: []appwire.Turn{itemFragmentTurn(items)}}, OlderCursor: olderCursor}
}

func itemPageResponse(items []appwire.ThreadItem, nextCursor string) appwire.ThreadTurnsListResponse {
	return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{itemFragmentTurn(items)}, NextCursor: nextCursor}
}

// positionedItems builds keys key-<n> at entry positions first..last.
func positionedItems(first, last uint64) []appwire.ThreadItem {
	items := make([]appwire.ThreadItem, 0, last-first+1)
	for position := first; position <= last; position++ {
		items = append(items, appwire.ThreadItem{
			ID: keyForPosition(position), TurnID: "turn-1", TranscriptKey: keyForPosition(position),
			Position: &appwire.ThreadItemPosition{Entry: position},
		})
	}
	return items
}

func keyForPosition(position uint64) string {
	return "key-" + uitoa(position)
}

func uitoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func itemPosition(entry uint64) appwire.ThreadItemPosition {
	return appwire.ThreadItemPosition{Entry: entry}
}

// decreasingItems holds two items whose positions fall, which the candidate
// validators refuse.
func decreasingItems() []appwire.ThreadItem {
	return []appwire.ThreadItem{
		{ID: keyForPosition(2), TurnID: "turn-1", TranscriptKey: keyForPosition(2), Position: &appwire.ThreadItemPosition{Entry: 2}},
		{ID: keyForPosition(1), TurnID: "turn-1", TranscriptKey: keyForPosition(1), Position: &appwire.ThreadItemPosition{Entry: 1}},
	}
}

var itemNativeIdentity = appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "native-v1", ProjectionVersion: 1}

// requireCanceled asserts err is the caller's context cancellation.
func requireCanceled(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestItemPagingRefusesUnresolvableThreadRef(t *testing.T) {
	t.Parallel()
	daemon := &scriptedItemDaemon{
		read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return itemReadResponse(positionedItems(1, 2), ""), nil
		},
		page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(nil, ""), nil
		},
	}
	source := newScriptedItemSource(t, daemon, "thread")
	ctx := t.Context()
	badRef := "local:missing"
	if _, err := source.ItemCandidatesFromRead(ctx, appwire.ThreadReadParams{Ref: badRef}, itemReadResponse(positionedItems(1, 2), "")); err == nil {
		t.Fatal("item candidates from read accepted an unresolvable ref")
	}
	if _, err := source.ReadItemCandidates(ctx, appwire.ThreadReadParams{Ref: badRef}); err == nil {
		t.Fatal("read item candidates accepted an unresolvable ref")
	}
	if _, err := source.ListItemCandidates(ctx, appwire.ThreadTurnsListParams{Ref: badRef}); err == nil {
		t.Fatal("list item candidates accepted an unresolvable ref")
	}
	if got := retainedPagingStateCount(t, source.itemSnapshots); got != 0 {
		t.Fatalf("refused calls retained %d paging states, want 0", got)
	}
}

func TestItemPagingRejectsOutOfRangeItemLimit(t *testing.T) {
	t.Parallel()
	daemon := &scriptedItemDaemon{
		read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return itemReadResponse(positionedItems(1, 2), ""), nil
		},
		page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(nil, ""), nil
		},
	}
	source := newScriptedItemSource(t, daemon, "thread")
	ctx := t.Context()
	if _, err := source.ReadItemCandidates(ctx, appwire.ThreadReadParams{Ref: "local:thread", ItemLimit: appwire.TranscriptItemPageLimit + 1}); err == nil {
		t.Fatal("read item candidates accepted an out-of-range item limit")
	}
	if _, err := source.ListItemCandidates(ctx, appwire.ThreadTurnsListParams{Ref: "local:thread", ItemLimit: appwire.TranscriptItemPageLimit + 1, Before: &appwire.ThreadItemPosition{}}); err == nil {
		t.Fatal("list item candidates accepted an out-of-range item limit")
	}
}

func TestItemPagingReadThreadFailuresSurface(t *testing.T) {
	t.Parallel()
	t.Run("transport failure", func(t *testing.T) {
		t.Parallel()
		readFailure := errors.New("daemon read failed")
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{}, readFailure
			},
			page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
			},
		}
		source := newScriptedItemSource(t, daemon, "thread")
		_, err := source.ReadItemCandidates(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"})
		if !errors.Is(err, readFailure) {
			t.Fatalf("read failure = %v, want the daemon's own error", err)
		}
	})
	t.Run("turn without id", func(t *testing.T) {
		t.Parallel()
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				items := positionedItems(1, 2)
				return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread-1", Turns: []appwire.Turn{{ID: "", Items: items, ItemsView: appwire.TurnItemsViewFragment}}}}, nil
			},
			page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
			},
		}
		source := newScriptedItemSource(t, daemon, "thread")
		if _, err := source.ReadItemCandidates(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"}); err == nil {
			t.Fatal("turn without an id was accepted as item candidates")
		}
	})
	t.Run("non-increasing positions", func(t *testing.T) {
		t.Parallel()
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return itemReadResponse(decreasingItems(), ""), nil
			},
			page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
			},
		}
		source := newScriptedItemSource(t, daemon, "thread")
		if _, err := source.ReadItemCandidates(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"}); err == nil {
			t.Fatal("non-increasing read positions were accepted as item candidates")
		}
	})
}

func TestItemPagingRefreshCancellationSurfaces(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	daemon := &scriptedItemDaemon{
		read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			cancel()
			return itemReadResponse(positionedItems(1, 2), ""), nil
		},
		page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
		},
	}
	source := newScriptedItemSource(t, daemon, "thread")
	_, err := source.ReadItemCandidates(ctx, appwire.ThreadReadParams{Ref: "local:thread"})
	requireCanceled(t, err)
}

func TestItemPagingReadFillsItemTurnIDFromTurn(t *testing.T) {
	t.Parallel()
	daemon := &scriptedItemDaemon{
		read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			items := positionedItems(1, 2)
			items[0].TurnID = ""
			items[1].TurnID = ""
			return itemReadResponse(items, ""), nil
		},
		page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
		},
	}
	source := newScriptedItemSource(t, daemon, "thread")
	result, err := source.ReadItemCandidates(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range result.Candidates.Candidates {
		if candidate.Item.TurnID != "turn-1" {
			t.Fatalf("item %q kept empty turn id", candidate.Item.ID)
		}
	}
}

func TestItemCandidatesFromReadCursorArms(t *testing.T) {
	t.Parallel()
	validCursor := inverseCursor(t, itemNativeIdentity, itemPosition(1))
	otherCursor := inverseCursor(t, itemNativeIdentity, itemPosition(2))
	noPage := func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
	}
	params := appwire.ThreadReadParams{Ref: "local:thread"}

	t.Run("retained garbage native cursor", func(t *testing.T) {
		t.Parallel()
		source := newScriptedItemSource(t, &scriptedItemDaemon{page: noPage, read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{}, errors.New("unexpected read call")
		}}, "thread")
		if _, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(1, 2, "garbage-cursor")); err != nil {
			t.Fatal(err)
		}
		_, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(2, 3, otherCursor))
		requireInverseStale(t, err)
	})
	t.Run("response garbage native cursor", func(t *testing.T) {
		t.Parallel()
		source := newScriptedItemSource(t, &scriptedItemDaemon{page: noPage, read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{}, errors.New("unexpected read call")
		}}, "thread")
		if _, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(1, 2, validCursor)); err != nil {
			t.Fatal(err)
		}
		_, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(2, 3, "garbage-cursor"))
		requireInverseStale(t, err)
	})
	t.Run("equal native cursors keep the retained token", func(t *testing.T) {
		t.Parallel()
		source := newScriptedItemSource(t, &scriptedItemDaemon{page: noPage, read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{}, errors.New("unexpected read call")
		}}, "thread")
		if _, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(1, 2, validCursor)); err != nil {
			t.Fatal(err)
		}
		second, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(2, 3, validCursor))
		if err != nil {
			t.Fatal(err)
		}
		state, ok := source.itemSnapshots.get("local:thread")
		if !ok || state.NativeCursor != validCursor {
			t.Fatalf("retained native cursor = %q, want the same-generation token", state.NativeCursor)
		}
		if second.Identity.Incarnation == "" {
			t.Fatal("sliding window lost its incarnation")
		}
	})
	t.Run("empty sliding windows are stale", func(t *testing.T) {
		t.Parallel()
		source := newScriptedItemSource(t, &scriptedItemDaemon{page: noPage, read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{}, errors.New("unexpected read call")
		}}, "thread")
		if _, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponseFor(nil, nil, validCursor)); err != nil {
			t.Fatal(err)
		}
		_, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponseFor(nil, nil, otherCursor))
		requireInverseStale(t, err)
	})
	t.Run("hidden history page failure", func(t *testing.T) {
		t.Parallel()
		pageFailure := errors.New("hidden history page failed")
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{}, errors.New("unexpected read call")
			},
			page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				return appwire.ThreadTurnsListResponse{}, pageFailure
			},
		}
		source := newScriptedItemSource(t, daemon, "thread")
		if _, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(1, 2, "")); err != nil {
			t.Fatal(err)
		}
		_, err := source.ItemCandidatesFromRead(t.Context(), params, positionedItemReadResponse(2, 3, validCursor))
		if !errors.Is(err, pageFailure) {
			t.Fatalf("hidden history failure = %v, want the page's own error", err)
		}
	})
}

// establishBoundedItemState drives one bounded ReadItemCandidates so the
// retained state carries a native cursor, and returns the hub identity and a
// cursor under it the next list call can continue from.
func establishBoundedItemState(t *testing.T, source *LocalDaemonSource, olderCursor string) (appitempaging.CursorIdentity, string) {
	t.Helper()
	first, err := source.ReadItemCandidates(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"})
	if err != nil {
		t.Fatalf("bounded initial read: %v", err)
	}
	if first.Exhausted {
		t.Fatal("bounded initial read reported exhaustion")
	}
	cursor := inverseCursor(t, first.Identity, first.Candidates.Candidates[0].Position)
	return first.Identity, cursor
}

func TestListItemCandidatesBeforeBoundaryArms(t *testing.T) {
	t.Parallel()
	before := itemPosition(2)
	t.Run("garbage cursor cannot be rebased onto a before boundary", func(t *testing.T) {
		t.Parallel()
		source := newScriptedItemSource(t, &scriptedItemDaemon{}, "thread")
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: "garbage-cursor", Before: &before})
		requireInverseStale(t, err)
	})
	t.Run("refresh failure surfaces", func(t *testing.T) {
		t.Parallel()
		readFailure := errors.New("daemon read failed")
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{}, readFailure
			},
			page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
			},
		}
		source := newScriptedItemSource(t, daemon, "thread")
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Before: &before})
		if !errors.Is(err, readFailure) {
			t.Fatalf("refresh failure = %v, want the daemon's own error", err)
		}
	})
	t.Run("complete snapshot pages before the boundary", func(t *testing.T) {
		t.Parallel()
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return itemReadResponse(positionedItems(1, 3), ""), nil
			},
			page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
			},
		}
		source := newScriptedItemSource(t, daemon, "thread")
		if _, err := source.ReadItemCandidates(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"}); err != nil {
			t.Fatal(err)
		}
		result, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Before: &before})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Candidates.Candidates) != 1 || result.Candidates.Candidates[0].Item.ID != keyForPosition(1) {
			t.Fatalf("before-boundary page = %+v, want the single older item", result.Candidates.Candidates)
		}
		if !result.Exhausted {
			t.Fatal("page before the transcript's first item is not exhausted")
		}
	})
}

func TestListItemCandidatesFreshSnapshotArms(t *testing.T) {
	t.Parallel()
	t.Run("refresh failure without a cursor", func(t *testing.T) {
		t.Parallel()
		readFailure := errors.New("daemon read failed")
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{}, readFailure
			},
			page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
			},
		}
		source := newScriptedItemSource(t, daemon, "thread")
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread"})
		if !errors.Is(err, readFailure) {
			t.Fatalf("refresh failure = %v, want the daemon's own error", err)
		}
	})
	t.Run("cursor without a retained snapshot is stale", func(t *testing.T) {
		t.Parallel()
		source := newScriptedItemSource(t, &scriptedItemDaemon{}, "thread")
		cursor := inverseCursor(t, itemNativeIdentity, itemPosition(1))
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireInverseStale(t, err)
	})
	// Each subtest needs its own complete snapshot, so each wires the full read
	// script and re-establishes it.
	newCompleteSnapshotSource := func(t *testing.T) (*LocalDaemonSource, *scriptedItemDaemon, string) {
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return itemReadResponse(positionedItems(1, 2), ""), nil
			},
			page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
			},
		}
		source := newScriptedItemSource(t, daemon, "thread")
		first, err := source.ReadItemCandidates(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"})
		if err != nil {
			t.Fatal(err)
		}
		return source, daemon, inverseCursor(t, first.Identity, itemPosition(2))
	}
	t.Run("refresh failure against a complete snapshot surfaces", func(t *testing.T) {
		t.Parallel()
		source, daemon, cursor := newCompleteSnapshotSource(t)
		readFailure := errors.New("daemon read failed")
		daemon.read = func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{}, readFailure
		}
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		if !errors.Is(err, readFailure) {
			t.Fatalf("refresh failure = %v, want the daemon's own error", err)
		}
	})
	t.Run("garbage cursor against a complete snapshot is stale", func(t *testing.T) {
		t.Parallel()
		source, _, _ := newCompleteSnapshotSource(t)
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: "garbage-cursor"})
		requireInverseStale(t, err)
	})
	t.Run("cursor pages from the complete snapshot", func(t *testing.T) {
		t.Parallel()
		source, _, cursor := newCompleteSnapshotSource(t)
		result, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Candidates.Candidates) != 1 || result.Candidates.Candidates[0].Item.ID != keyForPosition(1) {
			t.Fatalf("snapshot window = %+v", result.Candidates.Candidates)
		}
	})
	t.Run("cursor against a bounded snapshot without a prefix is stale", func(t *testing.T) {
		t.Parallel()
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return itemReadResponse(positionedItems(1, 2), inverseCursor(t, itemNativeIdentity, itemPosition(1))), nil
			},
			page: func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
				return appwire.ThreadTurnsListResponse{}, errors.New("unexpected page call")
			},
		}
		source := newScriptedItemSource(t, daemon, "thread")
		identity, _ := establishBoundedItemState(t, source, inverseCursor(t, itemNativeIdentity, itemPosition(1)))
		state, ok := source.itemSnapshots.get("local:thread")
		if !ok {
			t.Fatal("bounded read retained no paging state")
		}
		state.Prefix = false
		state.NativeCursor = ""
		source.itemSnapshots.put("local:thread", state)
		cursor := inverseCursor(t, identity, itemPosition(1))
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireInverseStale(t, err)
	})
}

func TestListItemCandidatesNativeWindowArms(t *testing.T) {
	t.Parallel()
	olderCursor := inverseCursor(t, itemNativeIdentity, itemPosition(1))
	pageOf := func(items []appwire.ThreadItem, next string) func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		return func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(items, next), nil
		}
	}
	// Each subtest establishes the same bounded state, so each needs its own
	// read script; setup wires the bounded read and the subtest's page script.
	newBoundedSource := func(t *testing.T, page func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error)) (*LocalDaemonSource, string) {
		t.Helper()
		daemon := &scriptedItemDaemon{
			read: func(int, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				// The window's first item is the continuation boundary, so the
				// page below it must serve older items than the window holds.
				return itemReadResponse(positionedItems(2, 3), olderCursor), nil
			},
			page: page,
		}
		source := newScriptedItemSource(t, daemon, "thread")
		_, cursor := establishBoundedItemState(t, source, olderCursor)
		return source, cursor
	}

	t.Run("page before the boundary", func(t *testing.T) {
		t.Parallel()
		source, cursor := newBoundedSource(t, pageOf(positionedItems(1, 1), ""))
		result, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Candidates.Candidates) != 1 || result.Candidates.Candidates[0].Item.ID != keyForPosition(1) {
			t.Fatalf("native window = %+v", result.Candidates.Candidates)
		}
		if !result.Exhausted {
			t.Fatal("final native page is not exhausted")
		}
	})
	t.Run("page transport failure", func(t *testing.T) {
		t.Parallel()
		pageFailure := errors.New("native page failed")
		source, cursor := newBoundedSource(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return appwire.ThreadTurnsListResponse{}, pageFailure
		})
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		if !errors.Is(err, pageFailure) {
			t.Fatalf("native page failure = %v, want the page's own error", err)
		}
	})
	t.Run("page without a fragment view", func(t *testing.T) {
		t.Parallel()
		source, cursor := newBoundedSource(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{{ID: "turn-1", Items: positionedItems(1, 1)}}}, nil
		})
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireWireError(t, err, appwire.CodeInvalidParams)
	})
	t.Run("page turn without an id", func(t *testing.T) {
		t.Parallel()
		source, cursor := newBoundedSource(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{{ID: "", Items: positionedItems(1, 1), ItemsView: appwire.TurnItemsViewFragment}}}, nil
		})
		if _, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor}); err == nil {
			t.Fatal("page turn without an id was accepted")
		}
	})
	t.Run("page with non-increasing positions", func(t *testing.T) {
		t.Parallel()
		source, cursor := newBoundedSource(t, pageOf(decreasingItems(), ""))
		if _, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor}); err == nil {
			t.Fatal("non-increasing page positions were accepted")
		}
	})
	t.Run("empty page with a continuation is stale", func(t *testing.T) {
		t.Parallel()
		source, cursor := newBoundedSource(t, pageOf(nil, olderCursor))
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireInverseStale(t, err)
	})
	t.Run("garbage continuation cursor is stale", func(t *testing.T) {
		t.Parallel()
		source, cursor := newBoundedSource(t, pageOf(positionedItems(1, 1), "garbage-cursor"))
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireInverseStale(t, err)
	})
	t.Run("continuation cursor naming another boundary is stale", func(t *testing.T) {
		t.Parallel()
		wrongBoundary := inverseCursor(t, itemNativeIdentity, itemPosition(2))
		source, cursor := newBoundedSource(t, pageOf(positionedItems(1, 1), wrongBoundary))
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireInverseStale(t, err)
	})
	t.Run("canceled empty final page", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source, cursor := newBoundedSource(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			cancel()
			return itemPageResponse(nil, ""), nil
		})
		_, err := source.ListItemCandidates(ctx, appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireCanceled(t, err)
	})
	t.Run("page without older items is stale", func(t *testing.T) {
		t.Parallel()
		source, cursor := newBoundedSource(t, pageOf(positionedItems(2, 2), ""))
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireInverseStale(t, err)
	})
	t.Run("canceled page with items", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		nextCursor := inverseCursor(t, itemNativeIdentity, itemPosition(1))
		source, cursor := newBoundedSource(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			cancel()
			return itemPageResponse(positionedItems(1, 1), nextCursor), nil
		})
		_, err := source.ListItemCandidates(ctx, appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireCanceled(t, err)
	})
	t.Run("retained garbage native cursor is stale", func(t *testing.T) {
		t.Parallel()
		source, cursor := newBoundedSource(t, pageOf(positionedItems(1, 1), ""))
		state, ok := source.itemSnapshots.get("local:thread")
		if !ok {
			t.Fatal("bounded read retained no paging state")
		}
		state.NativeCursor = "garbage-cursor"
		source.itemSnapshots.put("local:thread", state)
		_, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: cursor})
		requireInverseStale(t, err)
	})
}

func TestLocalDaemonHiddenHistoryArms(t *testing.T) {
	t.Parallel()
	olderCursor := inverseCursor(t, itemNativeIdentity, itemPosition(1))
	// A complete first read anchors the prefix; a bounded second read makes the
	// refresh reconstruct the hidden history through the scripted page.
	newHiddenHistoryCase := func(t *testing.T, page func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error)) *LocalDaemonSource {
		t.Helper()
		daemon := &scriptedItemDaemon{
			read: func(call int, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				if call == 0 {
					return itemReadResponse(positionedItems(1, 2), ""), nil
				}
				return itemReadResponse(positionedItems(2, 3), olderCursor), nil
			},
			page: page,
		}
		source := newScriptedItemSource(t, daemon, "thread")
		first, err := source.ReadItemCandidates(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"})
		if err != nil {
			t.Fatalf("complete initial read: %v", err)
		}
		if !first.Exhausted {
			t.Fatal("complete initial read is not exhausted")
		}
		return source
	}
	readAgain := func(t *testing.T, source *LocalDaemonSource, ctx context.Context) (ItemCandidateResult, error) {
		return source.ReadItemCandidates(ctx, appwire.ThreadReadParams{Ref: "local:thread"})
	}

	t.Run("continuation to the transcript start", func(t *testing.T) {
		t.Parallel()
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(positionedItems(1, 1), ""), nil
		})
		first, _ := source.itemSnapshots.get("local:thread")
		result, err := readAgain(t, source, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Candidates.Candidates) != 3 {
			t.Fatalf("reconstructed history = %d items, want 3", len(result.Candidates.Candidates))
		}
		state, _ := source.itemSnapshots.get("local:thread")
		if state.Incarnation != first.Incarnation {
			t.Fatalf("authenticated continuation rotated incarnation: %q -> %q", first.Incarnation, state.Incarnation)
		}
	})
	t.Run("page transport failure", func(t *testing.T) {
		t.Parallel()
		pageFailure := errors.New("hidden page failed")
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return appwire.ThreadTurnsListResponse{}, pageFailure
		})
		_, err := readAgain(t, source, t.Context())
		if !errors.Is(err, pageFailure) {
			t.Fatalf("hidden page failure = %v, want the page's own error", err)
		}
	})
	t.Run("page without a fragment view", func(t *testing.T) {
		t.Parallel()
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{{ID: "turn-1", Items: positionedItems(1, 1)}}}, nil
		})
		_, err := readAgain(t, source, t.Context())
		requireWireError(t, err, appwire.CodeInvalidParams)
	})
	t.Run("page turn without an id", func(t *testing.T) {
		t.Parallel()
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return appwire.ThreadTurnsListResponse{Data: []appwire.Turn{{ID: "", Items: positionedItems(1, 1), ItemsView: appwire.TurnItemsViewFragment}}}, nil
		})
		if _, err := readAgain(t, source, t.Context()); err == nil {
			t.Fatal("hidden page turn without an id was accepted")
		}
	})
	t.Run("page with non-increasing positions", func(t *testing.T) {
		t.Parallel()
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(decreasingItems(), ""), nil
		})
		if _, err := readAgain(t, source, t.Context()); err == nil {
			t.Fatal("non-increasing hidden page positions were accepted")
		}
	})
	t.Run("empty page without a continuation", func(t *testing.T) {
		t.Parallel()
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(nil, ""), nil
		})
		if _, err := readAgain(t, source, t.Context()); err == nil {
			t.Fatal("empty hidden page without a continuation was accepted")
		}
	})
	t.Run("page overlapping the current window is stale", func(t *testing.T) {
		t.Parallel()
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(positionedItems(3, 4), ""), nil
		})
		_, err := readAgain(t, source, t.Context())
		requireInverseStale(t, err)
	})
	t.Run("garbage continuation cursor is stale", func(t *testing.T) {
		t.Parallel()
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(positionedItems(1, 1), "garbage-cursor"), nil
		})
		_, err := readAgain(t, source, t.Context())
		requireInverseStale(t, err)
	})
	t.Run("continuation cursor naming another boundary is stale", func(t *testing.T) {
		t.Parallel()
		wrongBoundary := inverseCursor(t, itemNativeIdentity, itemPosition(2))
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(positionedItems(1, 1), wrongBoundary), nil
		})
		_, err := readAgain(t, source, t.Context())
		requireInverseStale(t, err)
	})
	t.Run("repeated continuation cursor is a cycle", func(t *testing.T) {
		t.Parallel()
		source := newHiddenHistoryCase(t, func(_ int, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(positionedItems(1, 1), params.Cursor), nil
		})
		if _, err := readAgain(t, source, t.Context()); err == nil {
			t.Fatal("repeated continuation cursor was accepted as progress")
		}
	})
	t.Run("canceled continuation page", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			cancel()
			return itemPageResponse(positionedItems(1, 1), olderCursor), nil
		})
		_, err := readAgain(t, source, ctx)
		requireCanceled(t, err)
	})
	t.Run("canceled final page", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := newHiddenHistoryCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			cancel()
			return itemPageResponse(positionedItems(1, 1), ""), nil
		})
		_, err := readAgain(t, source, ctx)
		requireCanceled(t, err)
	})
}

func TestAdvanceItemSnapshotArms(t *testing.T) {
	t.Parallel()
	olderCursor := inverseCursor(t, itemNativeIdentity, itemPosition(1))
	// A bounded first read retains a native cursor; a complete second read makes
	// advanceLocalDaemonItemSnapshot authenticate the prefix through the page.
	newAdvanceCase := func(t *testing.T, page func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error)) *LocalDaemonSource {
		t.Helper()
		daemon := &scriptedItemDaemon{
			read: func(call int, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				if call == 0 {
					return itemReadResponse(positionedItems(1, 2), olderCursor), nil
				}
				return itemReadResponse(positionedItems(1, 2), ""), nil
			},
			page: page,
		}
		source := newScriptedItemSource(t, daemon, "thread")
		if _, err := source.ReadItemCandidates(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"}); err != nil {
			t.Fatalf("bounded initial read: %v", err)
		}
		return source
	}
	readAgain := func(t *testing.T, source *LocalDaemonSource, ctx context.Context) (ItemCandidateResult, error) {
		return source.ReadItemCandidates(ctx, appwire.ThreadReadParams{Ref: "local:thread"})
	}

	t.Run("authenticated bounded to complete transition", func(t *testing.T) {
		t.Parallel()
		source := newAdvanceCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(positionedItems(1, 1), ""), nil
		})
		before, _ := source.itemSnapshots.get("local:thread")
		result, err := readAgain(t, source, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if result.Identity.Incarnation != before.Incarnation {
			t.Fatalf("authenticated transition rotated incarnation: %q -> %q", before.Incarnation, result.Identity.Incarnation)
		}
	})
	t.Run("stale continuation rotates without failing", func(t *testing.T) {
		t.Parallel()
		source := newAdvanceCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(positionedItems(3, 4), ""), nil
		})
		before, _ := source.itemSnapshots.get("local:thread")
		result, err := readAgain(t, source, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if result.Identity.Incarnation == before.Incarnation {
			t.Fatal("unauthenticated transition kept the retained incarnation")
		}
	})
	t.Run("page transport failure surfaces", func(t *testing.T) {
		t.Parallel()
		pageFailure := errors.New("advance page failed")
		source := newAdvanceCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return appwire.ThreadTurnsListResponse{}, pageFailure
		})
		_, err := readAgain(t, source, t.Context())
		if !errors.Is(err, pageFailure) {
			t.Fatalf("authentication page failure = %v, want the page's own error", err)
		}
	})
	t.Run("canceled authentication page", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := newAdvanceCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			cancel()
			return itemPageResponse(positionedItems(1, 1), ""), nil
		})
		_, err := readAgain(t, source, ctx)
		requireCanceled(t, err)
	})
	t.Run("garbage retained native cursor", func(t *testing.T) {
		t.Parallel()
		source := newAdvanceCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(positionedItems(1, 1), ""), nil
		})
		state, ok := source.itemSnapshots.get("local:thread")
		if !ok {
			t.Fatal("bounded read retained no paging state")
		}
		state.NativeCursor = "garbage-cursor"
		source.itemSnapshots.put("local:thread", state)
		_, err := readAgain(t, source, t.Context())
		requireInverseStale(t, err)
	})
	t.Run("retained last position outside the complete read", func(t *testing.T) {
		t.Parallel()
		source := newAdvanceCase(t, func(int, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
			return itemPageResponse(positionedItems(1, 1), ""), nil
		})
		state, ok := source.itemSnapshots.get("local:thread")
		if !ok {
			t.Fatal("bounded read retained no paging state")
		}
		state.ItemCount = 0
		state.LastPosition = appwire.ThreadItemPosition{}
		state.Prefix = true
		state.NativeCursor = olderCursor
		source.itemSnapshots.put("local:thread", state)
		before := state
		result, err := readAgain(t, source, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if result.Identity.Incarnation == before.Incarnation {
			t.Fatal("transition past an unobservable last position kept the retained incarnation")
		}
	})
}
