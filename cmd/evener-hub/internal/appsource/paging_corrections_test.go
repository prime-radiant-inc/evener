package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

func correctionItems(count int) []appwire.ThreadItem {
	items := make([]appwire.ThreadItem, count)
	for i := range items {
		p := appwire.ThreadItemPosition{Item: uint32(i)}
		items[i] = appwire.ThreadItem{ID: fmt.Sprint(i), Type: "agentMessage", TurnID: "turn-1", TranscriptKey: fmt.Sprint("key-", i), Position: &p}
	}
	return items
}

func correctionRead(items []appwire.ThreadItem, cursor string) appwire.ThreadReadResponse {
	return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "thread", Turns: []appwire.Turn{{ID: "turn-1", Items: items, ItemsView: appwire.TurnItemsViewFragment}}}, PageUnit: appwire.TranscriptPageUnitItem, OlderCursor: cursor}
}

func TestCompleteToBoundedAuthenticatesHistory(t *testing.T) {
	for _, tc := range []struct {
		name               string
		old, total, newest int
		rewrite            bool
	}{
		{"overlapping_hidden_rewrite", 4, 5, 3, true},
		{"large_append_gap", 2, 102, 40, false},
		{"public_page_limit", 41, 81, 40, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			all := correctionItems(tc.total)
			source, _ := newLocalDaemonItemTransitionSource(t, nil)
			first, err := source.ItemCandidatesFromRead(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"}, correctionRead(all[:tc.old], ""))
			if err != nil {
				t.Fatal(err)
			}
			if tc.rewrite {
				all[0].TranscriptKey = "rewritten"
				all[0].ID = "rewritten"
			}
			nativeIdentity := appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "native", ProjectionVersion: 1}
			cursor := func(p appwire.ThreadItemPosition) string {
				c, e := appitempaging.EncodeCursor(nativeIdentity, p)
				if e != nil {
					t.Fatal(e)
				}
				return c
			}
			source.dial = func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
				transport := respondingTransport(func(string) (any, error) {
					return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
				})
				transport.send = func(_ context.Context, msg appwire.Message) error {
					if msg.Request == nil {
						return nil
					}
					req := msg.Request
					if req.Method != appwire.MethodThreadTurnsList {
						transport.recv <- appwire.ResponseMessage(req.ID, appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion})
						return nil
					}
					var params appwire.ThreadTurnsListParams
					if e := json.Unmarshal(req.Params, &params); e != nil {
						return e
					}
					if e := appwire.ValidateThreadTurnsListParams(params); e != nil {
						transport.recv <- appwire.ErrorMessage(req.ID, appwire.InvalidParams(e.Error()))
						return nil
					}
					before, e := appitempaging.DecodeCursor(params.Cursor, nativeIdentity)
					if e != nil {
						return e
					}
					candidates, e := localDaemonItemCandidates([]appwire.Turn{{ID: "turn-1", Items: all}})
					if e != nil {
						return e
					}
					selected, older, e := appitempaging.SelectCandidates(candidates, &before, params.ItemLimit)
					if e != nil {
						return e
					}
					items := make([]appwire.ThreadItem, len(selected))
					for i, c := range selected {
						items[i] = c.Item
					}
					next := ""
					if older {
						next = cursor(selected[0].Position)
					}
					transport.recv <- appwire.ResponseMessage(req.ID, appwire.ThreadTurnsListResponse{Data: []appwire.Turn{{ID: "turn-1", Items: items, ItemsView: appwire.TurnItemsViewFragment}}, PageUnit: appwire.TranscriptPageUnitItem, NextCursor: next})
					return nil
				}
				return transport, nil
			}
			boundary := tc.total - tc.newest
			next, err := source.ItemCandidatesFromRead(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"}, correctionRead(all[boundary:], cursor(*all[boundary].Position)))
			if err != nil {
				t.Fatalf("valid bounded observation failed: %v", err)
			}
			if (next.Identity == first.Identity) == tc.rewrite {
				t.Fatalf("identity preserved=%v, hidden rewrite=%v", next.Identity == first.Identity, tc.rewrite)
			}
			if !tc.rewrite {
				state, _ := source.itemSnapshots.get("local:thread")
				candidates, e := localDaemonItemCandidates([]appwire.Turn{{ID: "turn-1", Items: all}})
				if e != nil {
					t.Fatal(e)
				}
				if !itemSnapshotStateMatchesCompleteCandidates(state, candidates) {
					t.Fatalf("retained summary is not contiguous full history: count=%d want=%d", state.ItemCount, len(all))
				}
			}
		})
	}
}

func TestCanceledSourceConversionPreservesState(t *testing.T) {
	source, _ := newLocalDaemonItemTransitionSource(t, nil)
	params := appwire.ThreadReadParams{Ref: "local:thread"}
	if _, err := source.ItemCandidatesFromRead(t.Context(), params, correctionRead(correctionItems(1), "")); err != nil {
		t.Fatal(err)
	}
	before, _ := source.itemSnapshots.get("local:thread")
	source.itemSnapshots.put("other", itemSnapshotState{ThreadRef: "other"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rewritten := correctionItems(1)
	rewritten[0].TranscriptKey = "rewritten"
	_, err := source.ItemCandidatesFromRead(ctx, params, correctionRead(rewritten, ""))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled conversion error=%v", err)
	}
	source.itemSnapshots.mu.Lock()
	defer source.itemSnapshots.mu.Unlock()
	after := source.itemSnapshots.entries["local:thread"].Value.(itemSnapshotStateEntry).state
	if !reflect.DeepEqual(before, after) {
		t.Error("canceled conversion changed cached state")
	}
	if source.itemSnapshots.order.Front().Value.(itemSnapshotStateEntry).key != "other" {
		t.Error("canceled conversion touched LRU")
	}
}

func TestCanceledSourceReadAndListPreserveLRU(t *testing.T) {
	for _, operation := range []string{"read", "list"} {
		t.Run(operation, func(t *testing.T) {
			source, _ := newLocalDaemonItemTransitionSource(t, correctionItems(2))
			if _, err := source.ItemCandidatesFromRead(t.Context(), appwire.ThreadReadParams{Ref: "local:thread"}, correctionRead(correctionItems(2), "")); err != nil {
				t.Fatal(err)
			}
			before, _ := source.itemSnapshots.peek("local:thread")
			source.itemSnapshots.put("other", itemSnapshotState{ThreadRef: "other"})
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var err error
			if operation == "read" {
				_, err = source.ReadItemCandidates(ctx, appwire.ThreadReadParams{Ref: "local:thread"})
			} else {
				_, err = source.ListItemCandidates(ctx, appwire.ThreadTurnsListParams{Ref: "local:thread"})
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			after, _ := source.itemSnapshots.peek("local:thread")
			if before != after {
				t.Fatal("cancellation changed state")
			}
			if source.itemSnapshots.order.Front().Value.(itemSnapshotStateEntry).key != "other" {
				t.Fatal("cancellation touched LRU")
			}
		})
	}
}
