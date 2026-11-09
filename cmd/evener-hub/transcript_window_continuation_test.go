package hub

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

func TestResolveTranscriptWindowContinuationLoadsStableRangeAndIndependentAnchor(t *testing.T) {
	const ref = "local:resolver"
	params := appwire.ThreadTurnsListParams{
		Ref:               ref,
		TurnWindow:        &appwire.ThreadTurnWindow{Latest: 3},
		ItemTypes:         []string{"messages"},
		WindowBudgetBytes: 1,
	}
	const itemLimit = 10
	identity := appitempaging.CursorIdentity{
		ThreadRef: ref, Incarnation: "resolver-view", ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
	}
	resume := &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-b", Limit: 1}}
	anchorRange := testResolverLoadedRange(t, identity, []appwire.Turn{testResolverTurn("turn-c", "item-c", 7)})
	anchor := anchorRange.Result.Candidates.Candidates[0]
	cursor, err := appitempaging.EncodeWindowCursor(
		transcriptWindowCursorIdentity(identity, params.ThreadWindowParams, itemLimit),
		appitempaging.WindowCursorDirectionBackward,
		appitempaging.WindowCursor{
			AnchorTurnID: anchor.TurnID, Position: anchor.Position, ItemVersion: anchor.Item.Version, ResumeWindow: resume,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	params.Cursor = cursor

	stable := testResolverLoadedRange(t, identity, []appwire.Turn{testResolverTurn("turn-a", "item-a", 1)})
	var selectors []appwire.ThreadTurnWindow
	loader := func(_ context.Context, selector *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
		selectors = append(selectors, *selector)
		switch {
		case reflect.DeepEqual(selector, resume):
			return stable, nil
		case selector.Turn == "turn-c":
			return anchorRange, nil
		default:
			t.Fatalf("loader selector = %+v, want stable resume or exact anchor", selector)
			return transcriptWindowLoadedRange{}, nil
		}
	}

	got, direction, err := resolveTranscriptWindowContinuation(t.Context(), params, itemLimit, loader)
	if err != nil {
		t.Fatal(err)
	}
	if direction != appitempaging.WindowCursorDirectionBackward {
		t.Fatalf("direction = %q, want backward", direction)
	}
	if gotIDs := testCandidateIDs(got.Result.Candidates.Candidates); !reflect.DeepEqual(gotIDs, []string{"item-a"}) {
		t.Fatalf("resolved candidates = %v, want only the stable resume range", gotIDs)
	}
	if !got.Result.Exhausted {
		t.Fatal("resolved range is not selection-relative exhausted")
	}
	wantSelectors := []appwire.ThreadTurnWindow{*resume, {Turn: "turn-c"}}
	if !reflect.DeepEqual(selectors, wantSelectors) {
		t.Fatalf("loader selectors = %+v, want %+v", selectors, wantSelectors)
	}
	for _, selector := range selectors {
		if selector.Latest != 0 {
			t.Fatalf("loader reused moving selector %+v", selector)
		}
	}
}

func TestResolveTranscriptWindowContinuationRejectsInvalidItemProofs(t *testing.T) {
	const ref = "local:resolver"
	baseParams := appwire.ThreadTurnsListParams{
		Ref:        ref,
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 2},
		ItemTypes:  []string{"messages"},
	}
	const itemLimit = 10
	identity := appitempaging.CursorIdentity{
		ThreadRef: ref, Incarnation: "resolver-view", ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
	}
	resume := &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-b", Limit: 1}}
	anchorRange := testResolverLoadedRange(t, identity, []appwire.Turn{testResolverTurn("turn-c", "item-c", 7)})
	anchor := anchorRange.Result.Candidates.Candidates[0]
	encode := func(t *testing.T, direction appitempaging.WindowCursorDirection, proof appitempaging.WindowCursor) string {
		t.Helper()
		encoded, err := appitempaging.EncodeWindowCursor(
			transcriptWindowCursorIdentity(identity, baseParams.ThreadWindowParams, itemLimit), direction, proof,
		)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}

	t.Run("missing anchor", func(t *testing.T) {
		params := baseParams
		params.Cursor = encode(t, appitempaging.WindowCursorDirectionBackward, appitempaging.WindowCursor{
			AnchorTurnID: anchor.TurnID, Position: anchor.Position, ItemVersion: anchor.Item.Version, ResumeWindow: resume,
		})
		stable := testResolverLoadedRange(t, identity, []appwire.Turn{testResolverTurn("turn-a", "item-a", 1)})
		loader := func(_ context.Context, selector *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
			if selector.Turn == anchor.TurnID {
				return testResolverLoadedRange(t, identity, []appwire.Turn{{ID: anchor.TurnID, ItemsView: appwire.TurnItemsViewFragment}}), nil
			}
			return stable, nil
		}
		got, _, err := resolveTranscriptWindowContinuation(t.Context(), params, itemLimit, loader)
		if !errors.Is(err, appwire.TranscriptItemCursorStale()) {
			t.Fatalf("missing anchor error = %v, want stale", err)
		}
		if len(got.Result.Candidates.Candidates) != 0 || len(got.Thread.Turns) != 0 {
			t.Fatalf("missing anchor returned content = %+v", got)
		}
	})

	t.Run("changed version", func(t *testing.T) {
		params := baseParams
		params.Cursor = encode(t, appitempaging.WindowCursorDirectionBackward, appitempaging.WindowCursor{
			AnchorTurnID: anchor.TurnID, Position: anchor.Position, ItemVersion: anchor.Item.Version, ResumeWindow: resume,
		})
		stable := testResolverLoadedRange(t, identity, []appwire.Turn{testResolverTurn("turn-a", "item-a", 1)})
		changed := anchorRange.Thread.Turns[0]
		changed.Items[0].Version++
		loader := func(_ context.Context, selector *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
			if selector.Turn == anchor.TurnID {
				return testResolverLoadedRange(t, identity, []appwire.Turn{changed}), nil
			}
			return stable, nil
		}
		got, _, err := resolveTranscriptWindowContinuation(t.Context(), params, itemLimit, loader)
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("changed version error = %T %v, want item changed", err, err)
		}
		data, ok := wire.Data.(appwire.TranscriptItemChangedErrorData)
		if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemChanged || data.NewVersion != anchor.Item.Version+1 {
			t.Fatalf("changed version error = %+v, want item changed version %d", wire, anchor.Item.Version+1)
		}
		if len(got.Result.Candidates.Candidates) != 0 || len(got.Thread.Turns) != 0 {
			t.Fatalf("changed version returned content = %+v", got)
		}
	})

	t.Run("wrong identity", func(t *testing.T) {
		params := baseParams
		params.Cursor = encode(t, appitempaging.WindowCursorDirectionBackward, appitempaging.WindowCursor{
			AnchorTurnID: anchor.TurnID, Position: anchor.Position, ItemVersion: anchor.Item.Version, ResumeWindow: resume,
		})
		wrong := identity
		wrong.Incarnation = "changed-view"
		loader := func(_ context.Context, _ *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
			return testResolverLoadedRange(t, wrong, []appwire.Turn{testResolverTurn("turn-a", "item-a", 1)}), nil
		}
		got, _, err := resolveTranscriptWindowContinuation(t.Context(), params, itemLimit, loader)
		if !errors.Is(err, appwire.TranscriptItemCursorStale()) {
			t.Fatalf("wrong identity error = %v, want stale", err)
		}
		if len(got.Result.Candidates.Candidates) != 0 || len(got.Thread.Turns) != 0 {
			t.Fatalf("wrong identity returned content = %+v", got)
		}
	})

	t.Run("wrong direction", func(t *testing.T) {
		params := baseParams
		params.Cursor = encode(t, appitempaging.WindowCursorDirectionForward, appitempaging.WindowCursor{
			AnchorTurnID: anchor.TurnID, Position: anchor.Position, ItemVersion: anchor.Item.Version,
			ResumeWindow: &appwire.ThreadTurnWindow{Since: "turn-b", Limit: 1},
		})
		loads := 0
		loader := func(_ context.Context, _ *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
			loads++
			return transcriptWindowLoadedRange{}, nil
		}
		got, _, err := resolveTranscriptWindowContinuation(t.Context(), params, itemLimit, loader)
		if !errors.Is(err, appwire.TranscriptItemCursorStale()) {
			t.Fatalf("wrong direction error = %v, want stale", err)
		}
		if loads != 0 {
			t.Fatalf("wrong direction invoked loader %d times, want 0", loads)
		}
		if len(got.Result.Candidates.Candidates) != 0 || len(got.Thread.Turns) != 0 {
			t.Fatalf("wrong direction returned content = %+v", got)
		}
	})
}

func TestResolveTranscriptWindowContinuationValidatesAllEmptyDigest(t *testing.T) {
	const ref = "local:resolver"
	params := appwire.ThreadTurnsListParams{
		Ref:        ref,
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 2},
	}
	const itemLimit = 10
	identity := appitempaging.CursorIdentity{
		ThreadRef: ref, Incarnation: "resolver-view", ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
	}
	originalTurns := []appwire.Turn{
		{ID: "turn-a", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment},
		{ID: "turn-b", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment},
	}
	original := testResolverLoadedRange(t, identity, originalTurns)
	digest, err := emptyTranscriptWindowDigest(original.Result)
	if err != nil {
		t.Fatal(err)
	}
	resume := &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}}
	cursor, err := appitempaging.EncodeWindowCursor(
		transcriptWindowCursorIdentity(identity, params.ThreadWindowParams, itemLimit),
		appitempaging.WindowCursorDirectionBackward,
		appitempaging.WindowCursor{EmptyRangeDigest: digest, ResumeWindow: resume},
	)
	if err != nil {
		t.Fatal(err)
	}
	params.Cursor = cursor

	t.Run("unchanged", func(t *testing.T) {
		loads := 0
		loader := func(_ context.Context, selector *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
			loads++
			if !reflect.DeepEqual(selector, resume) {
				t.Fatalf("selector = %+v, want %+v", selector, resume)
			}
			return original, nil
		}
		got, direction, err := resolveTranscriptWindowContinuation(t.Context(), params, itemLimit, loader)
		if err != nil {
			t.Fatal(err)
		}
		if direction != appitempaging.WindowCursorDirectionBackward || loads != 1 {
			t.Fatalf("direction = %q, loads = %d; want backward, 1", direction, loads)
		}
		if gotIDs := turnIDsFromTurns(got.Thread.Turns); !reflect.DeepEqual(gotIDs, []string{"turn-a", "turn-b"}) {
			t.Fatalf("unchanged digest turns = %v", gotIDs)
		}
	})

	mutations := map[string]func([]appwire.Turn) []appwire.Turn{
		"add positioned item": func(turns []appwire.Turn) []appwire.Turn {
			position := appwire.ThreadItemPosition{Entry: 1}
			turns[0].Items = []appwire.ThreadItem{{
				ID: "item-a", Type: "agentMessage", TurnID: turns[0].ID, TranscriptKey: "item-a",
				Text: "new", Position: &position, Version: 1,
			}}
			turns[0].ItemsView = appwire.TurnItemsViewFull
			return turns
		},
		"change shell": func(turns []appwire.Turn) []appwire.Turn {
			turns[0].Status = appwire.TurnStatusInProgress
			return turns
		},
		"reorder shells": func(turns []appwire.Turn) []appwire.Turn {
			turns[0], turns[1] = turns[1], turns[0]
			return turns
		},
		"insert shell": func(turns []appwire.Turn) []appwire.Turn {
			return append(turns[:1], append([]appwire.Turn{{ID: "turn-inserted", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment}}, turns[1:]...)...)
		},
		"remove shell": func(turns []appwire.Turn) []appwire.Turn {
			return turns[:1]
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			turns := cloneTranscriptTurnShells(originalTurns)
			mutated := testResolverLoadedRange(t, identity, mutate(turns))
			loader := func(_ context.Context, _ *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
				return mutated, nil
			}
			got, _, err := resolveTranscriptWindowContinuation(t.Context(), params, itemLimit, loader)
			if !errors.Is(err, appwire.TranscriptItemCursorStale()) {
				t.Fatalf("digest mutation error = %v, want stale", err)
			}
			if len(got.Result.Candidates.Candidates) != 0 || len(got.Thread.Turns) != 0 {
				t.Fatalf("digest mutation returned content = %+v", got)
			}
		})
	}
}

func TestResolveCompatibilityTurnWindow(t *testing.T) {
	turns := []appwire.Turn{
		{ID: "turn-a"},
		{ID: "turn-b", Items: []appwire.ThreadItem{{ID: "b-older"}}},
		{ID: "turn-b", Items: []appwire.ThreadItem{{ID: "b-newer"}}},
		{ID: "turn-c"},
	}
	tests := []struct {
		name      string
		window    *appwire.ThreadTurnWindow
		wantIDs   []string
		wantItems []string
	}{
		{name: "latest counts logical turns", window: &appwire.ThreadTurnWindow{Latest: 2}, wantIDs: []string{"turn-b", "turn-b", "turn-c"}, wantItems: []string{"b-older", "b-newer"}},
		{name: "exact retains every fragment", window: &appwire.ThreadTurnWindow{Turn: "turn-b"}, wantIDs: []string{"turn-b", "turn-b"}, wantItems: []string{"b-older", "b-newer"}},
		{name: "before limit counts logical turns", window: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 1}}, wantIDs: []string{"turn-b", "turn-b"}, wantItems: []string{"b-older", "b-newer"}},
		{name: "since limit counts logical turns", window: &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 1}, wantIDs: []string{"turn-b", "turn-b"}, wantItems: []string{"b-older", "b-newer"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, resolved := resolveCompatibilityTurnWindow(turns, test.window, true)
			if !resolved {
				t.Fatal("window was not resolved")
			}
			gotIDs := make([]string, len(got))
			for i := range got {
				gotIDs[i] = got[i].ID
			}
			if !reflect.DeepEqual(gotIDs, test.wantIDs) {
				t.Fatalf("turn IDs = %v, want %v", gotIDs, test.wantIDs)
			}
			var gotItems []string
			for _, turn := range got {
				for _, item := range turn.Items {
					gotItems = append(gotItems, item.ID)
				}
			}
			if !reflect.DeepEqual(gotItems, test.wantItems) {
				t.Fatalf("item IDs = %v, want %v", gotItems, test.wantItems)
			}
		})
	}

	incomplete := []appwire.Turn{
		{ID: "turn-b", HasEarlierItems: true},
		{ID: "turn-c"},
	}
	if got, resolved := resolveCompatibilityTurnWindow(incomplete, &appwire.ThreadTurnWindow{Latest: 2}, false); resolved || got != nil {
		t.Fatalf("incomplete oldest fragment = (%+v, %v), want unresolved", got, resolved)
	}
}

func testResolverLoadedRange(t *testing.T, identity appitempaging.CursorIdentity, turns []appwire.Turn) transcriptWindowLoadedRange {
	t.Helper()
	result, err := itemCandidateResultFromTurns(turns, "")
	if err != nil {
		t.Fatal(err)
	}
	result.Identity = identity
	result.Exhausted = true
	return transcriptWindowLoadedRange{Result: result, Thread: appwire.Thread{Turns: turns}}
}

func testResolverTurn(turnID, itemID string, version uint64) appwire.Turn {
	position := appwire.ThreadItemPosition{Entry: uint64(len(turnID))}
	return appwire.Turn{
		ID: turnID, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull,
		Items: []appwire.ThreadItem{{
			ID: itemID, Type: "agentMessage", TurnID: turnID, TranscriptKey: itemID,
			Text: itemID, Position: &position, Version: version,
		}},
	}
}
