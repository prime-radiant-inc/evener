package hub

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
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

func TestResolveTranscriptWindowContinuationKeepsUpstreamWithheldRangeOpen(t *testing.T) {
	const ref = "local:resolver"
	params := appwire.ThreadTurnsListParams{
		Ref:        ref,
		TurnWindow: &appwire.ThreadTurnWindow{Latest: 3},
		ItemTypes:  []string{"messages"},
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

	// The resumed range is itself a bounded transfer page: four turns are
	// withheld upstream beyond the one it delivered.
	stable := testResolverLoadedRange(t, identity, []appwire.Turn{testResolverTurn("turn-a", "item-a", 1)})
	stable.Result.Exhausted = false
	stable.Result.WithheldTurns = 4
	stable.Result.FirstWithheldTurnID = "turn-withheld-first"
	stable.Result.LastWithheldTurnID = "turn-withheld-last"
	loader := func(_ context.Context, selector *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
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

	got, _, err := resolveTranscriptWindowContinuation(t.Context(), params, itemLimit, loader)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result.Exhausted {
		t.Fatal("resolved range marked exhausted while four turns remain withheld upstream")
	}
	if got.Result.WithheldTurns != 4 || got.Result.FirstWithheldTurnID != "turn-withheld-first" {
		t.Fatalf("resolved range lost upstream withheld turns: %+v", got.Result)
	}
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

func TestResolveCompatibilityTurnWindowKeepsInterleavedFragments(t *testing.T) {
	turns := []appwire.Turn{
		{ID: "turn-a", Items: []appwire.ThreadItem{{ID: "a-first"}}},
		{ID: "turn-b", Items: []appwire.ThreadItem{{ID: "b-only"}}},
		{ID: "turn-a", Items: []appwire.ThreadItem{{ID: "a-second"}}},
	}
	got, resolved := resolveCompatibilityTurnWindow(turns, &appwire.ThreadTurnWindow{Latest: 2}, true)
	if !resolved {
		t.Fatal("interleaved latest window was not resolved")
	}
	var ids []string
	for _, turn := range got {
		ids = append(ids, turn.ID)
	}
	if want := []string{"turn-a", "turn-b", "turn-a"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("turn IDs = %v, want every fragment of the selected logical turns %v", ids, want)
	}
}

// testWindowTranscript is a static transcript for the continuation matrix.
// Fragments are listed in physical order; logical names the turn order
// independently, so a reopened turn can have a later fragment after another
// turn's items (finding: physical A1, B1, C1, B2).
type testWindowTranscript struct {
	identity  appitempaging.CursorIdentity
	logical   []string
	shells    map[string]appwire.Turn
	fragments []appwire.Turn
}

type testWindowTranscriptFragment struct {
	turnID string
	status string
	items  []appwire.ThreadItem
}

func testWindowTranscriptSpec(logical []string, fragments ...testWindowTranscriptFragment) testWindowTranscript {
	transcript := testWindowTranscript{
		identity: appitempaging.CursorIdentity{
			ThreadRef: "local:thread", Incarnation: "window-matrix",
			ProjectionVersion: appitempaging.TranscriptItemProjectionVersion,
		},
		logical: logical,
		shells:  make(map[string]appwire.Turn, len(logical)),
	}
	entry := uint64(0)
	for _, fragment := range fragments {
		status := fragment.status
		if status == "" {
			status = appwire.TurnStatusCompleted
		}
		turn := appwire.Turn{ID: fragment.turnID, Status: status, ItemsView: appwire.TurnItemsViewFragment, Version: 1}
		if _, exists := transcript.shells[fragment.turnID]; !exists {
			transcript.shells[fragment.turnID] = turn
		}
		for _, item := range fragment.items {
			position := appwire.ThreadItemPosition{Entry: entry}
			entry++
			item.Position = &position
			item.TurnID = fragment.turnID
			if item.Status == "" {
				item.Status = appwire.TurnStatusCompleted
			}
			if item.TranscriptKey == "" {
				item.TranscriptKey = "key-" + item.ID
			}
			turn.Items = append(turn.Items, item)
		}
		transcript.fragments = append(transcript.fragments, turn)
	}
	return transcript
}

func testWindowMessage(id string, textBytes int) appwire.ThreadItem {
	return appwire.ThreadItem{ID: id, Type: "agentMessage", Text: strings.Repeat("x", textBytes), Version: 1}
}

func (tr testWindowTranscript) allCandidates(t *testing.T) []appitempaging.TranscriptItemCandidate {
	t.Helper()
	candidates, err := appitempaging.CandidatesFromTurns(tr.fragments)
	if err != nil {
		t.Fatalf("fixture candidates: %v", err)
	}
	return candidates
}

func (tr testWindowTranscript) candidatesFor(t *testing.T, selected []string) []appitempaging.TranscriptItemCandidate {
	t.Helper()
	member := make(map[string]bool, len(selected))
	for _, turnID := range selected {
		member[turnID] = true
	}
	var candidates []appitempaging.TranscriptItemCandidate
	for _, candidate := range tr.allCandidates(t) {
		if member[candidate.TurnID] {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

// turnShells returns the physical fragments of the selected turns plus a real
// shell for every selected turn with no positioned item, placed at the turn's
// logical position. A source delivers a checkpoint shell's metadata; the
// packer's digest covers it.
func (tr testWindowTranscript) turnShells(t *testing.T, selected []string) []appwire.Turn {
	t.Helper()
	turns, err := appitempaging.RegroupTurnFragments(tr.candidatesFor(t, selected))
	if err != nil {
		t.Fatalf("fixture turn shells: %v", err)
	}
	present := make(map[string]bool, len(turns))
	for _, turn := range turns {
		present[turn.ID] = true
	}
	order := make(map[string]int, len(selected))
	for index, turnID := range selected {
		if _, exists := order[turnID]; !exists {
			order[turnID] = index
		}
	}
	for _, turnID := range selected {
		if present[turnID] {
			continue
		}
		at := len(turns)
		for index, turn := range turns {
			if turnOrder, ok := order[turn.ID]; ok && turnOrder > order[turnID] {
				at = index
				break
			}
		}
		turns = slices.Insert(turns, at, tr.shells[turnID])
		present[turnID] = true
	}
	return turns
}

func (tr testWindowTranscript) logicalSubset(served []string) []string {
	member := make(map[string]bool, len(served))
	for _, turnID := range served {
		member[turnID] = true
	}
	var selected []string
	for _, turnID := range tr.logical {
		if member[turnID] {
			selected = append(selected, turnID)
		}
	}
	return selected
}

// delivered is the bounded transfer's result for one page: whole served turns,
// the upstream withheld turn population, and no turn from both.
func (tr testWindowTranscript) delivered(t *testing.T, served []string, withheld int, firstWithheld, lastWithheld string) transcriptItemCandidateResult {
	t.Helper()
	selected := tr.logicalSubset(served)
	return transcriptItemCandidateResult{
		Candidates:          appitempaging.TranscriptItemWindow{Candidates: tr.candidatesFor(t, selected)},
		Identity:            tr.identity,
		TurnIDs:             selected,
		TurnShells:          tr.turnShells(t, selected),
		Exhausted:           withheld == 0,
		WithheldTurns:       withheld,
		FirstWithheldTurnID: firstWithheld,
		LastWithheldTurnID:  lastWithheld,
	}
}

func (tr testWindowTranscript) resolve(t *testing.T, selector *appwire.ThreadTurnWindow) []string {
	t.Helper()
	switch {
	case selector == nil:
		t.Fatal("fixture loader got a nil selector")
	case selector.Turn != "":
		if !slices.Contains(tr.logical, selector.Turn) {
			t.Fatalf("turn %q is not in the fixture transcript", selector.Turn)
		}
		return []string{selector.Turn}
	case selector.Since != "":
		index := slices.Index(tr.logical, selector.Since)
		if index < 0 {
			t.Fatalf("since turn %q is not in the fixture transcript", selector.Since)
		}
		turnIDs := tr.logical[index+1:]
		if selector.Limit > 0 && len(turnIDs) > selector.Limit {
			turnIDs = turnIDs[:selector.Limit]
		}
		return slices.Clone(turnIDs)
	case selector.Before != nil:
		index := slices.Index(tr.logical, selector.Before.TurnID)
		if index < 0 {
			t.Fatalf("before turn %q is not in the fixture transcript", selector.Before.TurnID)
		}
		turnIDs := tr.logical[:index]
		if selector.Before.Limit > 0 && len(turnIDs) > selector.Before.Limit {
			turnIDs = turnIDs[len(turnIDs)-selector.Before.Limit:]
		}
		return slices.Clone(turnIDs)
	default:
		t.Fatalf("fixture loader got an unsupported selector %+v", selector)
	}
	return nil
}

func (tr testWindowTranscript) rangeExhausted(selector *appwire.ThreadTurnWindow, selected []string) bool {
	if len(selected) == 0 {
		return true
	}
	switch {
	case selector.Since != "":
		return selected[len(selected)-1] == tr.logical[len(tr.logical)-1]
	case selector.Before != nil:
		return selected[0] == tr.logical[0]
	default:
		return true
	}
}

func (tr testWindowTranscript) load(t *testing.T, selector *appwire.ThreadTurnWindow) transcriptWindowLoadedRange {
	t.Helper()
	selected := tr.resolve(t, selector)
	shells := tr.turnShells(t, selected)
	return transcriptWindowLoadedRange{
		Result: transcriptItemCandidateResult{
			Candidates: appitempaging.TranscriptItemWindow{Candidates: tr.candidatesFor(t, selected)},
			Identity:   tr.identity,
			TurnIDs:    selected,
			TurnShells: shells,
			Exhausted:  tr.rangeExhausted(selector, selected),
		},
		Thread: appwire.Thread{Turns: shells},
	}
}

func (tr testWindowTranscript) loader(t *testing.T) transcriptWindowRangeLoader {
	t.Helper()
	return func(_ context.Context, selector *appwire.ThreadTurnWindow) (transcriptWindowLoadedRange, error) {
		return tr.load(t, selector), nil
	}
}

type testWindowServedPage struct {
	turnIDs []string
	itemIDs []string
	cursor  string
}

// testSpendTranscriptWindowContinuations packs a delivered transfer page, then
// follows each continuation through the resolver and packs what it loaded,
// exactly as a client holding the same params does. It fails on the first
// continuation that does not spend.
func testSpendTranscriptWindowContinuations(t *testing.T, transcript testWindowTranscript, result transcriptItemCandidateResult, params appwire.ThreadWindowParams) []testWindowServedPage {
	t.Helper()
	const itemLimit = transcriptWindowDefaultItemLimit
	var pages []testWindowServedPage
	for step := 0; step < 16; step++ {
		options := transcriptWindowOptionsFor(
			result, params, itemLimit, transcriptWindowAppliedByServingDaemon,
			appitempaging.FetchCursorOriginServingDaemon,
		)
		packed, err := packThreadTurnsWindowItemCandidates(result, nil, options)
		if err != nil {
			t.Fatalf("step %d: pack window: %v", step, err)
		}
		page := testWindowServedPage{}
		for _, turn := range packed.Data {
			page.turnIDs = append(page.turnIDs, turn.ID)
			for _, item := range turn.Items {
				page.itemIDs = append(page.itemIDs, item.ID)
			}
		}
		if packed.Window != nil {
			page.cursor = packed.Window.ContinuationCursor
		}
		pages = append(pages, page)
		if page.cursor == "" {
			return pages
		}
		resolveParams := appwire.ThreadTurnsListParams{
			Ref:                transcript.identity.ThreadRef,
			ThreadWindowParams: params,
			Cursor:             page.cursor,
		}
		loaded, _, err := resolveTranscriptWindowContinuation(context.Background(), resolveParams, itemLimit, transcript.loader(t))
		if err != nil {
			t.Fatalf("step %d: spend continuation for %+v: %v", step, params.TurnWindow, err)
		}
		result = loaded.Result
	}
	t.Fatal("continuation did not exhaust after 16 pages")
	return nil
}

// TestTranscriptWindowWholeTurnContinuationSpendsUnchangedTranscript pins the
// single structural rule: a whole-turn continuation carries the resolved
// logical range and spends on its anchor, while position slicing applies only
// inside one turn. The matrix covers interleaved and non-interleaved physical
// order, every turn selector, a partial upstream transfer, local trimming that
// withholds only empty checkpoint shells, and an all-empty range whose digest
// covers everything it resumes.
func TestTranscriptWindowWholeTurnContinuationSpendsUnchangedTranscript(t *testing.T) {
	tests := []struct {
		name                string
		transcript          testWindowTranscript
		served              []string
		withheld            int
		firstWithheld       string
		lastWithheld        string
		selector            *appwire.ThreadTurnWindow
		budgetBytes         int
		wantFirstPageTurns  []string
		wantSecondPageTurns []string
		wantDigest          bool
	}{
		{
			name: "since interleaved bounded transfer withholds a turn",
			transcript: testWindowTranscriptSpec(
				[]string{"turn-a", "turn-b", "turn-c"},
				testWindowTranscriptFragment{turnID: "turn-a", items: []appwire.ThreadItem{testWindowMessage("a-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-b", items: []appwire.ThreadItem{testWindowMessage("b-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-c", items: []appwire.ThreadItem{testWindowMessage("c-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-b", items: []appwire.ThreadItem{testWindowMessage("b-2", 8)}},
			),
			served:              []string{"turn-a", "turn-b"},
			withheld:            1,
			firstWithheld:       "turn-c",
			lastWithheld:        "turn-c",
			selector:            &appwire.ThreadTurnWindow{Since: "turn-seed", Limit: 3},
			wantFirstPageTurns:  []string{"turn-a", "turn-b"},
			wantSecondPageTurns: []string{"turn-c"},
		},
		{
			name: "since non-interleaved bounded transfer withholds two turns",
			transcript: testWindowTranscriptSpec(
				[]string{"turn-a", "turn-b", "turn-c", "turn-d"},
				testWindowTranscriptFragment{turnID: "turn-a", items: []appwire.ThreadItem{testWindowMessage("a-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-b", items: []appwire.ThreadItem{testWindowMessage("b-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-c", items: []appwire.ThreadItem{testWindowMessage("c-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-d", items: []appwire.ThreadItem{testWindowMessage("d-1", 8)}},
			),
			served:              []string{"turn-a", "turn-b"},
			withheld:            2,
			firstWithheld:       "turn-c",
			lastWithheld:        "turn-d",
			selector:            &appwire.ThreadTurnWindow{Since: "turn-seed", Limit: 4},
			wantFirstPageTurns:  []string{"turn-a", "turn-b"},
			wantSecondPageTurns: []string{"turn-c", "turn-d"},
		},
		{
			name: "latest interleaved bounded transfer withholds two turns",
			transcript: testWindowTranscriptSpec(
				[]string{"turn-u1", "turn-u2", "turn-x", "turn-y"},
				testWindowTranscriptFragment{turnID: "turn-u1", items: []appwire.ThreadItem{testWindowMessage("u1-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-u2", items: []appwire.ThreadItem{testWindowMessage("u2-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-x", items: []appwire.ThreadItem{testWindowMessage("x-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-y", items: []appwire.ThreadItem{testWindowMessage("y-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-x", items: []appwire.ThreadItem{testWindowMessage("x-2", 8)}},
			),
			served:              []string{"turn-x", "turn-y"},
			withheld:            2,
			firstWithheld:       "turn-u1",
			lastWithheld:        "turn-u2",
			selector:            &appwire.ThreadTurnWindow{Latest: 4},
			wantFirstPageTurns:  []string{"turn-x", "turn-y"},
			wantSecondPageTurns: []string{"turn-u1", "turn-u2"},
		},
		{
			// Finding 2, backward: the locally withheld turns are empty
			// checkpoint shells while two upstream turns remain unread.
			name: "latest interleaved empty shells plus partial upstream",
			transcript: testWindowTranscriptSpec(
				[]string{"turn-u1", "turn-u2", "turn-e1", "turn-x", "turn-y"},
				testWindowTranscriptFragment{turnID: "turn-u1", items: []appwire.ThreadItem{testWindowMessage("u1-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-u2", items: []appwire.ThreadItem{testWindowMessage("u2-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-e1", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-x", items: []appwire.ThreadItem{testWindowMessage("x-1", 3_000)}},
				testWindowTranscriptFragment{turnID: "turn-y", items: []appwire.ThreadItem{testWindowMessage("y-1", 3_000)}},
				testWindowTranscriptFragment{turnID: "turn-x", items: []appwire.ThreadItem{testWindowMessage("x-2", 3_000)}},
			),
			served:             []string{"turn-e1", "turn-x", "turn-y"},
			withheld:           2,
			firstWithheld:      "turn-u1",
			lastWithheld:       "turn-u2",
			selector:           &appwire.ThreadTurnWindow{Latest: 5},
			budgetBytes:        1,
			wantFirstPageTurns: []string{"turn-x", "turn-y"},
		},
		{
			name: "before non-interleaved empty shells plus partial upstream",
			transcript: testWindowTranscriptSpec(
				[]string{"turn-u1", "turn-u2", "turn-e1", "turn-x", "turn-y"},
				testWindowTranscriptFragment{turnID: "turn-u1", items: []appwire.ThreadItem{testWindowMessage("u1-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-u2", items: []appwire.ThreadItem{testWindowMessage("u2-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-e1", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-x", items: []appwire.ThreadItem{testWindowMessage("x-1", 4_000)}},
				testWindowTranscriptFragment{turnID: "turn-y", items: []appwire.ThreadItem{testWindowMessage("y-1", 4_000)}},
			),
			served:             []string{"turn-e1", "turn-x", "turn-y"},
			withheld:           2,
			firstWithheld:      "turn-u1",
			lastWithheld:       "turn-u2",
			selector:           &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-after", Limit: 5}},
			budgetBytes:        1,
			wantFirstPageTurns: []string{"turn-x", "turn-y"},
		},
		{
			name: "since non-interleaved empty shells plus partial upstream",
			transcript: testWindowTranscriptSpec(
				[]string{"turn-x", "turn-e1", "turn-e2", "turn-u1", "turn-u2"},
				testWindowTranscriptFragment{turnID: "turn-x", items: []appwire.ThreadItem{testWindowMessage("x-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-e1", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e2", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-u1", items: []appwire.ThreadItem{testWindowMessage("u1-1", 8)}},
				testWindowTranscriptFragment{turnID: "turn-u2", items: []appwire.ThreadItem{testWindowMessage("u2-1", 8)}},
			),
			served:             []string{"turn-x", "turn-e1", "turn-e2"},
			withheld:           2,
			firstWithheld:      "turn-u1",
			lastWithheld:       "turn-u2",
			selector:           &appwire.ThreadTurnWindow{Since: "turn-seed", Limit: 5},
			budgetBytes:        1,
			wantFirstPageTurns: []string{"turn-x"},
		},
		{
			name: "since all-empty range digest covers the whole resumed range",
			transcript: testWindowTranscriptSpec(
				[]string{"turn-e1", "turn-e2", "turn-e3", "turn-e4", "turn-e5", "turn-e6"},
				testWindowTranscriptFragment{turnID: "turn-e1", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e2", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e3", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e4", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e5", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e6", status: strings.Repeat("checkpoint-", 1_000)},
			),
			served:             []string{"turn-e1", "turn-e2", "turn-e3", "turn-e4", "turn-e5", "turn-e6"},
			selector:           &appwire.ThreadTurnWindow{Since: "turn-seed", Limit: 6},
			budgetBytes:        1,
			wantFirstPageTurns: []string{"turn-e1"},
			wantDigest:         true,
		},
		{
			name: "latest all-empty range digest covers the whole resumed range",
			transcript: testWindowTranscriptSpec(
				[]string{"turn-e1", "turn-e2", "turn-e3", "turn-e4", "turn-e5", "turn-e6"},
				testWindowTranscriptFragment{turnID: "turn-e1", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e2", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e3", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e4", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e5", status: strings.Repeat("checkpoint-", 1_000)},
				testWindowTranscriptFragment{turnID: "turn-e6", status: strings.Repeat("checkpoint-", 1_000)},
			),
			served:             []string{"turn-e1", "turn-e2", "turn-e3", "turn-e4", "turn-e5", "turn-e6"},
			selector:           &appwire.ThreadTurnWindow{Latest: 6},
			budgetBytes:        1,
			wantFirstPageTurns: []string{"turn-e6"},
			wantDigest:         true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.transcript.delivered(t, test.served, test.withheld, test.firstWithheld, test.lastWithheld)
			params := appwire.ThreadWindowParams{TurnWindow: test.selector, WindowBudgetBytes: test.budgetBytes}
			pages := testSpendTranscriptWindowContinuations(t, test.transcript, result, params)
			if len(pages) == 0 {
				t.Fatal("no page was served")
			}
			if !reflect.DeepEqual(pages[0].turnIDs, test.wantFirstPageTurns) {
				t.Fatalf("first page turns = %v, want %v", pages[0].turnIDs, test.wantFirstPageTurns)
			}
			if test.wantSecondPageTurns != nil {
				if len(pages) < 2 || !reflect.DeepEqual(pages[1].turnIDs, test.wantSecondPageTurns) {
					t.Fatalf("second page turns = %v, want %v", testWindowPageTurns(pages), test.wantSecondPageTurns)
				}
			}
			var servedTurns []string
			var servedItems []string
			for _, page := range pages {
				servedTurns = append(servedTurns, page.turnIDs...)
				servedItems = append(servedItems, page.itemIDs...)
			}
			for _, turnID := range test.transcript.logical {
				if !slices.Contains(servedTurns, turnID) {
					t.Fatalf("turn %q was never served: pages %v", turnID, testWindowPageTurns(pages))
				}
			}
			wantItems := make([]string, 0, len(test.transcript.allCandidates(t)))
			for _, candidate := range test.transcript.allCandidates(t) {
				wantItems = append(wantItems, candidate.Item.ID)
			}
			slices.Sort(wantItems)
			slices.Sort(servedItems)
			if !slices.Equal(servedItems, wantItems) {
				t.Fatalf("served items = %v, want every positioned item %v", servedItems, wantItems)
			}

			identity := transcriptWindowCursorIdentity(test.transcript.identity, params, transcriptWindowDefaultItemLimit)
			direction := appitempaging.WindowCursorDirectionBackward
			if test.selector.Since != "" {
				direction = appitempaging.WindowCursorDirectionForward
			}
			cursor, err := appitempaging.DecodeWindowCursor(pages[0].cursor, identity, direction)
			if err != nil {
				t.Fatalf("decode first continuation: %v", err)
			}
			if test.wantDigest {
				if cursor.EmptyRangeDigest == "" || cursor.AnchorTurnID != "" {
					t.Fatalf("first continuation proof = %+v, want the whole-range digest", cursor)
				}
				whole := test.transcript.load(t, cursor.ResumeWindow)
				want, err := emptyTranscriptWindowDigest(whole.Result)
				if err != nil {
					t.Fatalf("digest the whole resumed range: %v", err)
				}
				if want != cursor.EmptyRangeDigest {
					t.Fatalf("digest = %s, want the digest of the whole resumed range %s", cursor.EmptyRangeDigest, want)
				}
				return
			}
			if cursor.EmptyRangeDigest != "" || cursor.AnchorTurnID == "" {
				t.Fatalf("first continuation proof = %+v, want the served-edge anchor while turns remain", cursor)
			}
		})
	}
}

func testWindowPageTurns(pages []testWindowServedPage) [][]string {
	turns := make([][]string, len(pages))
	for index, page := range pages {
		turns[index] = page.turnIDs
	}
	return turns
}
