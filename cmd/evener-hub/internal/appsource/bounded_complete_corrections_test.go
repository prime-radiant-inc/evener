package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// inverseNativeFixture serves exclusive native item pages and complete legacy
// materializations, with a fresh transport per RPC and real cursor validation.
type inverseNativeFixture struct {
	items      []appwire.ThreadItem
	complete   []appwire.ThreadItem
	identity   appitempaging.CursorIdentity
	requests   []appwire.ThreadTurnsListParams
	failure    error
	cancel     context.CancelFunc
	jsonErrors bool
}

func (f *inverseNativeFixture) dial(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
	transport := newScriptedAppwireTransport(nil)
	transport.send = func(_ context.Context, msg appwire.Message) error {
		if msg.Request == nil {
			return nil
		}
		request := msg.Request
		if request.Method != appwire.MethodThreadTurnsList {
			transport.recv <- appwire.ResponseMessage(request.ID, appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion})
			return nil
		}
		var params appwire.ThreadTurnsListParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return err
		}
		response, err := f.page(params)
		if err != nil {
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok {
				return err
			}
			message := appwire.ErrorMessage(request.ID, wire)
			if f.jsonErrors {
				encoded, err := json.Marshal(message)
				if err != nil {
					return err
				}
				var decoded appwire.Message
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					return err
				}
				message = decoded
			}
			transport.recv <- message
		} else {
			transport.recv <- appwire.ResponseMessage(request.ID, response)
		}
		return nil
	}
	return transport, nil
}

// page returns native RPC errors; dial frames them without treating a valid
// error response as a failed transport send.
func (f *inverseNativeFixture) page(params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	if err := appwire.ValidateThreadTurnsListParams(params); err != nil {
		return appwire.ThreadTurnsListResponse{}, appwire.InvalidParams(err.Error())
	}
	items := f.items
	next := ""
	view := appwire.TurnItemsViewFull
	if params.PageUnit != appwire.TranscriptPageUnitItem && f.complete != nil {
		items = f.complete
	}
	if params.PageUnit == appwire.TranscriptPageUnitItem {
		f.requests = append(f.requests, params)
		if f.cancel != nil {
			f.cancel()
		}
		if f.failure != nil {
			return appwire.ThreadTurnsListResponse{}, f.failure
		}
		before, err := appitempaging.DecodeCursor(params.Cursor, f.identity)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		candidates, err := localDaemonItemCandidates([]appwire.Turn{{ID: "turn-1", Items: items}})
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		selected, older, err := appitempaging.SelectCandidates(candidates, &before, params.ItemLimit)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		items = make([]appwire.ThreadItem, len(selected))
		for i, c := range selected {
			items[i] = c.Item
		}
		if older {
			next, err = appitempaging.EncodeCursor(f.identity, selected[0].Position)
			if err != nil {
				return appwire.ThreadTurnsListResponse{}, err
			}
		}
		view = appwire.TurnItemsViewFragment
	}
	return appwire.ThreadTurnsListResponse{PageUnit: params.PageUnit, Data: []appwire.Turn{{ID: "turn-1", Items: items, ItemsView: view}}, NextCursor: next}, nil
}

func inverseCursor(t *testing.T, identity appitempaging.CursorIdentity, position appwire.ThreadItemPosition) string {
	t.Helper()
	cursor, err := appitempaging.EncodeCursor(identity, position)
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

func requireInverseStale(t *testing.T, err error) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("want typed stale, got %v", err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale {
		t.Fatalf("want item cursor stale, got %+v", wire)
	}
}

func TestInverseNativeFixtureControls(t *testing.T) {
	all := correctionItems(5)
	source, _ := newLocalDaemonItemTransitionSource(t, nil)
	fixture := inverseNativeFixture{items: all, identity: appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "v1", ProjectionVersion: 1}}
	source.dial = fixture.dial
	params := appwire.ThreadTurnsListParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2, Cursor: inverseCursor(t, fixture.identity, *all[4].Position)}
	first, err := source.ListTurns(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Data[0].Items) != 2 || first.Data[0].Items[0].ID != "2" || first.NextCursor == "" {
		t.Fatalf("exclusive first page=%+v", first)
	}
	params.Cursor = first.NextCursor
	second, err := source.ListTurns(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Data[0].Items) != 2 || second.Data[0].Items[0].ID != "0" || second.NextCursor != "" {
		t.Fatalf("exhausted second page=%+v", second)
	}
	params.ItemLimit = 41
	_, err = source.ListTurns(t.Context(), params)
	if err == nil {
		t.Fatal("fixture accepted oversized native request")
	}
	params.ItemLimit = 2
	fixture.identity.Incarnation = "v2"
	_, err = source.ListTurns(t.Context(), params)
	requireInverseStale(t, err)
	fixture.jsonErrors = true
	_, err = source.ListTurns(t.Context(), params)
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("want JSON-decoded stale wire error, got %v", err)
	}
	data, ok := wire.Data.(map[string]any)
	if !ok || data["evenerErrorInfo"] != string(appwire.ErrorTranscriptItemCursorStale) {
		t.Fatalf("want JSON-decoded stale metadata, got %T: %+v", wire.Data, wire.Data)
	}
}

func TestBoundedToCompleteAuthenticatesNativeHistory(t *testing.T) {
	for _, caller := range []string{"conversion", "materialized"} {
		for _, change := range []string{"append", "large_append", "retired_native", "retired_native_json", "hidden_disagreement", "hidden_insertion", "hidden_deletion", "span_rewrite"} {
			t.Run(caller+"/"+change, func(t *testing.T) {
				count, start, end := 5, 2, 4
				appendOnly := change == "append" || change == "large_append"
				if change == "large_append" {
					count, start, end = 85, 42, 82
				}
				all := correctionItems(count)
				if change == "hidden_insertion" {
					for i := range all {
						all[i].Position.Item *= 2
					}
				}
				source, _ := newLocalDaemonItemTransitionSource(t, nil)
				fixture := inverseNativeFixture{items: all, identity: appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "v1", ProjectionVersion: 1}}
				source.dial = fixture.dial
				params := appwire.ThreadReadParams{Ref: "local:thread", ItemLimit: 40}
				first, err := source.ItemCandidatesFromRead(t.Context(), params, correctionRead(all[start:end], inverseCursor(t, fixture.identity, *all[start].Position)))
				if err != nil {
					t.Fatal(err)
				}
				outward := inverseCursor(t, first.Identity, *all[start].Position)
				current := append([]appwire.ThreadItem(nil), all...)
				if !appendOnly {
					current = current[:4]
					current[0].TranscriptKey = "rewritten"
				}
				if change == "hidden_insertion" {
					inserted := correctionItems(1)[0]
					inserted.ID, inserted.TranscriptKey = "inserted", "key-inserted"
					inserted.Position.Item = 1
					current = append([]appwire.ThreadItem{all[0], inserted}, all[1:4]...)
				}
				if change == "hidden_deletion" {
					current = all[1:4]
				}
				if change == "retired_native" || change == "retired_native_json" {
					fixture.identity.Incarnation = "v2"
					fixture.items = current
					fixture.jsonErrors = change == "retired_native_json"
				}
				if change == "span_rewrite" {
					current[2].TranscriptKey = "rewritten-span"
					fixture.items = current
				}
				var next ItemCandidateResult
				if caller == "conversion" {
					next, err = source.ItemCandidatesFromRead(t.Context(), params, correctionRead(current, ""))
				} else {
					fixture.complete = current
					next, err = source.ReadItemCandidates(t.Context(), params)
				}
				if err != nil {
					t.Fatal(err)
				}
				if appendOnly {
					if next.Identity != first.Identity {
						t.Error("append-only complete materialization rotated outward identity")
					}
					if change == "large_append" && len(fixture.requests) < 3 {
						t.Error("large inverse proof did not traverse all native pages")
					}
					if len(fixture.requests) == 0 {
						t.Error("complete transition skipped native proof")
					}
					_, err = source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: outward, ItemLimit: 2})
					if err != nil {
						t.Fatalf("valid old continuation: %v", err)
					}
				} else {
					if next.Identity == first.Identity {
						t.Error("rewritten complete materialization retained outward identity")
					}
					_, err = source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: outward, ItemLimit: 2})
					requireInverseStale(t, err)
				}
			})
		}
	}
}

func TestSecondDisjointThenCompleteAppendPreservesIdentity(t *testing.T) {
	all := correctionItems(7)
	source, _ := newLocalDaemonItemTransitionSource(t, nil)
	fixture := inverseNativeFixture{items: all, identity: appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "v1", ProjectionVersion: 1}}
	source.dial = fixture.dial
	params := appwire.ThreadReadParams{Ref: "local:thread", ItemLimit: 40}
	initial, err := source.ItemCandidatesFromRead(t.Context(), params, correctionRead(all[:2], ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range []int{2, 4} {
		next, err := source.ItemCandidatesFromRead(t.Context(), params, correctionRead(all[start:start+2], inverseCursor(t, fixture.identity, *all[start].Position)))
		if err != nil || next.Identity != initial.Identity {
			t.Fatalf("bounded control: %+v %v", next.Identity, err)
		}
	}
	outward := inverseCursor(t, initial.Identity, *all[4].Position)
	if _, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: outward, ItemLimit: 4}); err != nil {
		t.Fatalf("disjoint control continuation: %v", err)
	}
	complete, err := source.ItemCandidatesFromRead(t.Context(), params, correctionRead(all, ""))
	if err != nil {
		t.Fatal(err)
	}
	if complete.Identity != initial.Identity {
		t.Fatal("complete append after disjoint observation rotated identity")
	}
	if _, err := source.ListItemCandidates(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", Cursor: outward, ItemLimit: 4}); err != nil {
		t.Fatalf("complete continuation: %v", err)
	}
}

func TestInverseNativeProofErrorPreservesSnapshot(t *testing.T) {
	for _, caller := range []string{"conversion", "materialized"} {
		for _, failure := range []string{"cancellation", "transport", "protocol", "transport_json", "protocol_json", "wrong_code", "wrong_code_json", "wrong_info_json", "message_only_json"} {
			t.Run(caller+"/"+failure, func(t *testing.T) {
				all := correctionItems(4)
				source, _ := newLocalDaemonItemTransitionSource(t, nil)
				fixture := inverseNativeFixture{items: all, identity: appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "v1", ProjectionVersion: 1}}
				source.dial = fixture.dial
				params := appwire.ThreadReadParams{Ref: "local:thread", ItemLimit: 40}
				if _, err := source.ItemCandidatesFromRead(t.Context(), params, correctionRead(all[2:], inverseCursor(t, fixture.identity, *all[2].Position))); err != nil {
					t.Fatal(err)
				}
				before, _ := source.itemSnapshots.peek("local:thread")
				source.itemSnapshots.put("other", itemSnapshotState{ThreadRef: "other"})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if failure == "cancellation" {
					fixture.cancel = cancel
				} else {
					fixture.jsonErrors = strings.HasSuffix(failure, "_json")
					fixture.failure = appwire.WireError{Code: appwire.CodeUnavailable, Message: "native proof unavailable"}
					switch strings.TrimSuffix(failure, "_json") {
					case "protocol":
						fixture.failure = appwire.InvalidParams("native proof invalid")
					case "wrong_code":
						wire := appwire.TranscriptItemCursorStale()
						wire.Code = appwire.CodeUnavailable
						fixture.failure = wire
					case "wrong_info":
						wire := appwire.TranscriptItemCursorStale()
						wire.Data = map[string]any{"evenerErrorInfo": "notCursorStale", "detail": "preserve me"}
						fixture.failure = wire
					case "message_only":
						wire := appwire.TranscriptItemCursorStale()
						wire.Data = nil
						fixture.failure = wire
					}
				}
				var proofError error
				if failure != "cancellation" {
					_, proofError = source.ListTurns(t.Context(), appwire.ThreadTurnsListParams{Ref: "local:thread", PageUnit: appwire.TranscriptPageUnitItem, ItemLimit: 2, Cursor: inverseCursor(t, fixture.identity, *all[2].Position)})
					if proofError == nil {
						t.Fatal("native error control succeeded")
					}
				}
				var err error
				if caller == "conversion" {
					_, err = source.ItemCandidatesFromRead(ctx, params, correctionRead(all, ""))
				} else {
					_, err = source.ReadItemCandidates(ctx, params)
				}
				if err == nil {
					t.Error("failed native proof succeeded")
				}
				if failure != "cancellation" {
					var got, want appwire.WireError
					if !errors.As(err, &got) || !errors.As(proofError, &want) || !reflect.DeepEqual(got, want) {
						t.Errorf("native proof error changed: %v, want %v", err, proofError)
					}
				}
				if failure == "cancellation" && !errors.Is(err, context.Canceled) {
					t.Errorf("want cancellation, got %v", err)
				}
				after, _ := source.itemSnapshots.peek("local:thread")
				if before != after || source.itemSnapshots.order.Front().Value.(itemSnapshotStateEntry).key != "other" {
					t.Error("failed native proof changed snapshot or LRU")
				}
			})
		}
	}
}
