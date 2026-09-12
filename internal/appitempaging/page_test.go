package appitempaging

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestCandidatesFromTurnsNormalizesSharedProjection(t *testing.T) {
	firstPosition := appwire.ThreadItemPosition{Entry: 3, Item: 0}
	secondPosition := appwire.ThreadItemPosition{Entry: 3, Item: 1}
	turns := []appwire.Turn{{
		ID: "turn_shared", HasEarlierItems: true, HasLaterItems: true,
		Items: []appwire.ThreadItem{
			{ID: "first", TranscriptKey: "key-first", Position: &firstPosition},
			{ID: "second", TranscriptKey: "key-second", Position: &secondPosition},
		},
	}}
	candidates, err := CandidatesFromTurns(turns)
	if err != nil {
		t.Fatalf("convert turns: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(candidates))
	}
	for index, candidate := range candidates {
		if candidate.TurnID != "turn_shared" || candidate.Item.TurnID != "" {
			t.Fatalf("candidate %d turn identity = %q/%q, want candidate turn_shared and unchanged item", index, candidate.TurnID, candidate.Item.TurnID)
		}
		if !candidate.HasEarlierItems || !candidate.HasLaterItems {
			t.Fatalf("candidate %d completeness = earlier %v/later %v, want true/true", index, candidate.HasEarlierItems, candidate.HasLaterItems)
		}
	}
}

func TestCandidatesFromTurnsUsesSourceNeutralErrors(t *testing.T) {
	_, err := CandidatesFromTurns([]appwire.Turn{{ID: "turn", Items: []appwire.ThreadItem{{ID: "unpositioned"}}}})
	if err == nil {
		t.Fatal("unpositioned item returned nil error")
	}
	if strings.Contains(strings.ToLower(err.Error()), "local daemon") {
		t.Fatalf("shared conversion error is source-specific: %v", err)
	}
}

func TestSelectCandidatesLatestAndBackfill(t *testing.T) {
	candidates := makeCandidates(45, "turn_1")
	latest, hasOlder, err := SelectCandidates(candidates, nil, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 40 || !hasOlder {
		t.Fatalf("latest = %d candidates, hasOlder=%v; want 40,true", len(latest), hasOlder)
	}
	if latest[0].Item.TranscriptKey != "key-5" || latest[len(latest)-1].Item.TranscriptKey != "key-44" {
		t.Fatalf("latest keys = %q..%q, want key-5..key-44", latest[0].Item.TranscriptKey, latest[len(latest)-1].Item.TranscriptKey)
	}

	before := latest[0].Position
	backfill, hasOlder, err := SelectCandidates(candidates, &before, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(backfill) != 5 || hasOlder {
		t.Fatalf("backfill = %d candidates, hasOlder=%v; want 5,false", len(backfill), hasOlder)
	}
	if backfill[0].Item.TranscriptKey != "key-0" || backfill[4].Item.TranscriptKey != "key-4" {
		t.Fatalf("backfill keys = %q..%q, want key-0..key-4", backfill[0].Item.TranscriptKey, backfill[4].Item.TranscriptKey)
	}
}

func TestSelectCandidatesRejectsInvalidSource(t *testing.T) {
	cases := []struct {
		name string
		edit func([]TranscriptItemCandidate)
	}{
		{name: "duplicate position", edit: func(c []TranscriptItemCandidate) { c[1].Position = c[0].Position }},
		{name: "reversed position", edit: func(c []TranscriptItemCandidate) { c[1].Position, c[0].Position = c[0].Position, c[1].Position }},
		{name: "empty key", edit: func(c []TranscriptItemCandidate) { c[1].Item.TranscriptKey = "" }},
		{name: "duplicate key", edit: func(c []TranscriptItemCandidate) { c[1].Item.TranscriptKey = c[0].Item.TranscriptKey }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidates := makeCandidates(3, "turn_1")
			tc.edit(candidates)
			if _, _, err := SelectCandidates(candidates, nil, 40); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
	if selected, hasOlder, err := SelectCandidates(nil, nil, 40); err != nil || len(selected) != 0 || hasOlder {
		t.Fatalf("empty projection = %d candidates, hasOlder=%v, err=%v", len(selected), hasOlder, err)
	}
}

func TestRegroupTurnFragments(t *testing.T) {
	candidates := makeCandidates(6, "turn_1")
	for i := range candidates {
		candidates[i].Turn.HasEarlierItems = true
		candidates[i].Turn.HasLaterItems = true
	}
	candidates[0].HasEarlierItems = true
	candidates[5].HasLaterItems = true
	candidates[3].TurnID = "turn_2"
	candidates[3].Turn.ID = "turn_2"
	turns, err := RegroupTurnFragments(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 3 {
		t.Fatalf("regrouped turns = %d, want 3", len(turns))
	}
	if len(turns[0].Items) != 3 || len(turns[1].Items) != 1 || len(turns[2].Items) != 2 {
		t.Fatalf("fragment item counts = %d,%d,%d, want 3,1,2", len(turns[0].Items), len(turns[1].Items), len(turns[2].Items))
	}
	if turns[0].ItemsView != appwire.TurnItemsViewFragment || !turns[0].HasEarlierItems || turns[0].HasLaterItems {
		t.Fatalf("first fragment metadata = view=%q earlier=%v later=%v", turns[0].ItemsView, turns[0].HasEarlierItems, turns[0].HasLaterItems)
	}
	if turns[2].ItemsView != appwire.TurnItemsViewFragment || turns[2].HasEarlierItems || !turns[2].HasLaterItems {
		t.Fatalf("last fragment metadata = view=%q earlier=%v later=%v", turns[2].ItemsView, turns[2].HasEarlierItems, turns[2].HasLaterItems)
	}
}

func TestRegroupTurnFragmentsPreservesAtomicItemsAcrossThreePages(t *testing.T) {
	candidates := makeCandidates(5, "turn_1")
	candidates[0].Item.Type = "toolResult"
	candidates[0].Item.Text = "all text"
	candidates[0].Item.Delta = "all delta"
	candidates[0].Item.Images = []appwire.InputItem{{Type: "image", Text: "input", Data: []byte("bytes"), Metadata: map[string]string{"key": "value"}}}
	candidates[0].Item.OutputImages = []appwire.OutputImage{{Source: "output", URL: "https://example.test/image"}}
	candidates[0].Item.Raw = []byte(`{"nested":true}`)
	candidates[1].Item.Images = []appwire.InputItem{}
	candidates[1].Item.OutputImages = []appwire.OutputImage{}
	candidates[1].Item.Raw = []byte{}
	candidates[0].Item.EventKind = appwire.ThreadItemEventKindError
	candidates[0].Item.Source = "user"

	var all []appwire.ThreadItem
	before := (*appwire.ThreadItemPosition)(nil)
	for page := range 3 {
		selected, hasOlder, err := SelectCandidates(candidates, before, 2)
		if err != nil {
			t.Fatal(err)
		}
		turns, err := RegroupTurnFragments(selected)
		if err != nil {
			t.Fatal(err)
		}
		if len(turns) != 1 || turns[0].ItemsView != appwire.TurnItemsViewFragment {
			t.Fatalf("page %d = %+v, want one fragment", page, turns)
		}
		all = append(append([]appwire.ThreadItem(nil), turns[0].Items...), all...)
		if page == 2 {
			if hasOlder {
				t.Fatal("oldest page reports older items")
			}
			break
		}
		if !hasOlder {
			t.Fatalf("page %d exhausted early", page)
		}
		boundary := selected[0].Position
		before = &boundary
	}
	if len(all) != len(candidates) {
		t.Fatalf("three pages returned %d items, want %d", len(all), len(candidates))
	}
	for i := range candidates {
		if !reflect.DeepEqual(all[i], candidates[i].Item) {
			t.Fatalf("item %d changed across fragments: got=%+v want=%+v", i, all[i], candidates[i].Item)
		}
	}
	if turns, err := RegroupTurnFragments([]TranscriptItemCandidate{candidates[0]}); err != nil {
		t.Fatal(err)
	} else if turns[0].HasEarlierItems || turns[0].HasLaterItems {
		t.Fatalf("source completeness flags leaked into fragment: %+v", turns[0])
	}
}

func TestRegroupTurnFragmentsEmptyAndCopiesScalars(t *testing.T) {
	if turns, err := RegroupTurnFragments(nil); err != nil || len(turns) != 0 {
		t.Fatalf("empty regroup = %v, %v", turns, err)
	}
	candidates := makeCandidates(1, "turn_1")
	candidates[0].Turn.Status = appwire.TurnStatusFailed
	candidates[0].Turn.Items = []appwire.ThreadItem{{ID: "source-item"}}
	turns, err := RegroupTurnFragments(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if turns[0].Status != appwire.TurnStatusFailed || turns[0].Items[0].ID != candidates[0].Item.ID {
		t.Fatalf("scalar/items not copied: %+v", turns[0])
	}
	turns[0].Items[0].ID = "mutated"
	if candidates[0].Item.ID == "mutated" {
		t.Fatal("regroup mutated candidate item")
	}
}

func TestNormalizeProjectedItemCompletenessPreservesPartialFragmentsAndItems(t *testing.T) {
	position := func(entry, item uint32) *appwire.ThreadItemPosition {
		return &appwire.ThreadItemPosition{Entry: uint64(entry), Item: item}
	}
	original := []TranscriptItemCandidate{
		{
			TurnID:          "turn_tools",
			HasEarlierItems: true,
			Item: appwire.ThreadItem{
				ID:       "call-1",
				Type:     "commandExecution",
				Text:     "run command",
				Output:   "stdout",
				Position: position(4, 0),
			},
			Position: appwire.ThreadItemPosition{Entry: 4, Item: 0},
		},
		{
			TurnID:        "turn_tools",
			HasLaterItems: true,
			Item: appwire.ThreadItem{
				ID:       "result-1",
				Type:     "toolResult",
				Text:     "tool result",
				Error:    "tool error",
				Position: position(4, 1),
			},
			Position: appwire.ThreadItemPosition{Entry: 4, Item: 1},
		},
		{
			TurnID:          "turn_other",
			HasEarlierItems: true,
			HasLaterItems:   true,
			Item: appwire.ThreadItem{
				ID:       "item-2",
				Type:     "agentMessage",
				Text:     "partial fragment",
				Position: position(5, 0),
			},
			Position: appwire.ThreadItemPosition{Entry: 5, Item: 0},
		},
	}
	wantInput := append([]TranscriptItemCandidate(nil), original...)

	normalized := NormalizeProjectedItemCompleteness(original)
	if len(normalized) != len(original) {
		t.Fatalf("normalized length = %d, want %d", len(normalized), len(original))
	}
	if normalized[0].HasEarlierItems != true || normalized[0].HasLaterItems {
		t.Fatalf("first fragment completeness = (%v,%v), want (true,false)", normalized[0].HasEarlierItems, normalized[0].HasLaterItems)
	}
	if normalized[1].HasEarlierItems || !normalized[1].HasLaterItems {
		t.Fatalf("last fragment completeness = (%v,%v), want (false,true)", normalized[1].HasEarlierItems, normalized[1].HasLaterItems)
	}
	if !normalized[2].HasEarlierItems || !normalized[2].HasLaterItems {
		t.Fatalf("single partial fragment completeness = (%v,%v), want (true,true)", normalized[2].HasEarlierItems, normalized[2].HasLaterItems)
	}
	if normalized[0].Item.Type != "commandExecution" || normalized[0].Item.Output != "stdout" || normalized[1].Item.Error != "tool error" {
		t.Fatalf("tool call/result fields changed: call=%+v result=%+v", normalized[0].Item, normalized[1].Item)
	}
	if !reflect.DeepEqual(original, wantInput) {
		t.Fatalf("normalization mutated input: got=%+v want=%+v", original, wantInput)
	}
	normalized[0].HasEarlierItems = false
	if !original[0].HasEarlierItems {
		t.Fatal("normalization result aliases input candidate flags")
	}
}

func makeCandidates(n int, turnID string) []TranscriptItemCandidate {
	turn := appwire.Turn{ID: turnID, Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFull}
	out := make([]TranscriptItemCandidate, n)
	for i := range out {
		out[i] = TranscriptItemCandidate{
			TurnID: turnID,
			Turn:   turn,
			Item: appwire.ThreadItem{
				Type:          "agentMessage",
				ID:            fmt.Sprintf("display-%d", i),
				TranscriptKey: fmt.Sprintf("key-%d", i),
				Position:      &appwire.ThreadItemPosition{Entry: 10, Item: uint32(i)},
			},
			Position: appwire.ThreadItemPosition{Entry: 10, Item: uint32(i)},
		}
	}
	return out
}
