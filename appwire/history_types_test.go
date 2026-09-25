package appwire

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestHistoryUpdatedParamsJSONRoundTrip covers the wire vocabulary added for
// versioned history: ThreadItemPosition.Sub (omitted at zero),
// ThreadItem.Version/RoundID, Turn.Version, and the history/updated envelope
// itself.
func TestHistoryUpdatedParamsJSONRoundTrip(t *testing.T) {
	position := &ThreadItemPosition{Entry: 3, Item: NoticeAnchorItem, Sub: 2}
	positionRaw, err := json.Marshal(position)
	if err != nil {
		t.Fatal(err)
	}
	if string(positionRaw) != `{"entry":3,"item":1073741824,"sub":2}` {
		t.Fatalf("position with sub = %s", positionRaw)
	}

	zeroSub := &ThreadItemPosition{Entry: 3, Item: NoticeAnchorItem}
	zeroSubRaw, err := json.Marshal(zeroSub)
	if err != nil {
		t.Fatal(err)
	}
	if string(zeroSubRaw) != `{"entry":3,"item":1073741824}` {
		t.Fatalf("position with zero sub must omit it: %s", zeroSubRaw)
	}

	item := ThreadItem{
		Type:     "notice",
		ID:       "notice-1",
		Version:  4,
		RoundID:  "round_1",
		Position: position,
	}
	turn := Turn{ID: "turn_3", Version: 4}
	params := HistoryUpdatedParams{
		ThreadID: "thread",
		Ref:      "local:thread",
		Epoch:    2,
		Snapshot: SnapshotIdentity{Incarnation: "incarnation-1", Length: 17},
		Turns:    []Turn{turn},
		Items:    []ThreadItem{item},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"version":4`,
		`"roundId":"round_1"`,
		`"incarnation":"incarnation-1"`,
		`"length":17`,
		`"epoch":2`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("HistoryUpdatedParams JSON %s missing %s", raw, want)
		}
	}
	// Turns carry no Items on the wire.
	if strings.Contains(string(raw), `"items"`) && strings.Count(string(raw), `"items"`) != 1 {
		t.Fatalf("HistoryUpdatedParams turns must carry no items: %s", raw)
	}

	var decoded HistoryUpdatedParams
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Epoch != 2 || decoded.Snapshot.Incarnation != "incarnation-1" || decoded.Snapshot.Length != 17 {
		t.Fatalf("round trip snapshot/epoch = %+v", decoded)
	}
	if len(decoded.Turns) != 1 || decoded.Turns[0].Version != 4 {
		t.Fatalf("round trip turns = %+v", decoded.Turns)
	}
	if len(decoded.Items) != 1 || decoded.Items[0].Version != 4 || decoded.Items[0].RoundID != "round_1" {
		t.Fatalf("round trip items = %+v", decoded.Items)
	}
	if decoded.Items[0].Position == nil || decoded.Items[0].Position.Sub != 2 || decoded.Items[0].Position.Item != NoticeAnchorItem {
		t.Fatalf("round trip item position = %+v", decoded.Items[0].Position)
	}

	if NotifyHistoryUpdated != "history/updated" {
		t.Fatalf("NotifyHistoryUpdated = %q", NotifyHistoryUpdated)
	}
}

// TestOverlayItemJSONRoundTripByKind covers every OverlayKind's round trip
// through the shared OverlayItem envelope.
func TestOverlayItemJSONRoundTripByKind(t *testing.T) {
	anchor := &ThreadItemPosition{Entry: 5, Item: NoticeAnchorItem, Sub: 1}
	for _, tc := range []struct {
		name string
		item OverlayItem
	}{
		{"stream", OverlayItem{Key: "stream:s1:agentMessage", Kind: OverlayStream, TurnID: "turn_1", RoundID: "round_1", StreamID: "s1", Item: ThreadItem{Type: "agentMessage", ID: "s1"}}},
		{"preview", OverlayItem{Key: "preview:call-1", Kind: OverlayPreview, TurnID: "turn_1", CallID: "call-1", Item: ThreadItem{Type: "toolCall", ID: "call-1"}}},
		{"tool", OverlayItem{Key: "tool:entry-1", Kind: OverlayTool, TurnID: "turn_1", CallID: "call-1", HistoryKey: "entry-1", Item: ThreadItem{Type: "toolCall", ID: "call-1"}}},
		{"notice", OverlayItem{Key: "notice:1", Kind: OverlayNotice, Anchor: anchor, Item: ThreadItem{Type: "notice", ID: "notice-1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.item)
			if err != nil {
				t.Fatal(err)
			}
			var decoded OverlayItem
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Key != tc.item.Key || decoded.Kind != tc.item.Kind {
				t.Fatalf("round trip %s = %+v, want %+v", tc.name, decoded, tc.item)
			}
			if tc.item.Anchor != nil {
				if decoded.Anchor == nil || *decoded.Anchor != *tc.item.Anchor {
					t.Fatalf("round trip %s anchor = %+v, want %+v", tc.name, decoded.Anchor, tc.item.Anchor)
				}
			}
		})
	}

	upserted := OverlayUpsertedParams{ThreadID: "thread", Ref: "local:thread", Item: OverlayItem{Key: "stream:s1:agentMessage", Kind: OverlayStream, Item: ThreadItem{Type: "agentMessage", ID: "s1"}}}
	raw, err := json.Marshal(upserted)
	if err != nil {
		t.Fatal(err)
	}
	var decodedUpserted OverlayUpsertedParams
	if err := json.Unmarshal(raw, &decodedUpserted); err != nil {
		t.Fatal(err)
	}
	if decodedUpserted.Item.Key != upserted.Item.Key {
		t.Fatalf("OverlayUpsertedParams round trip = %+v", decodedUpserted)
	}

	delta := OverlayDeltaParams{ThreadID: "thread", Key: "stream:s1:agentMessage", Field: OverlayDeltaText, Delta: "hello"}
	raw, err = json.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	var decodedDelta OverlayDeltaParams
	if err := json.Unmarshal(raw, &decodedDelta); err != nil {
		t.Fatal(err)
	}
	if decodedDelta.Field != OverlayDeltaText || decodedDelta.Delta != "hello" {
		t.Fatalf("OverlayDeltaParams round trip = %+v", decodedDelta)
	}

	reset := OverlayResetParams{ThreadID: "thread", StreamID: "s1"}
	raw, err = json.Marshal(reset)
	if err != nil {
		t.Fatal(err)
	}
	var decodedReset OverlayResetParams
	if err := json.Unmarshal(raw, &decodedReset); err != nil {
		t.Fatal(err)
	}
	if decodedReset.StreamID != "s1" {
		t.Fatalf("OverlayResetParams round trip = %+v", decodedReset)
	}

	end := OverlayEndParams{ThreadID: "thread", RoundID: "round_1"}
	raw, err = json.Marshal(end)
	if err != nil {
		t.Fatal(err)
	}
	var decodedEnd OverlayEndParams
	if err := json.Unmarshal(raw, &decodedEnd); err != nil {
		t.Fatal(err)
	}
	if decodedEnd.RoundID != "round_1" {
		t.Fatalf("OverlayEndParams round trip = %+v", decodedEnd)
	}

	for name, method := range map[string]string{
		"upserted": NotifyOverlayUpserted,
		"delta":    NotifyOverlayDelta,
		"reset":    NotifyOverlayReset,
		"end":      NotifyOverlayEnd,
	} {
		if method == "" {
			t.Errorf("%s notification method is empty", name)
		}
	}
}

// TestThreadReadResponseHistoryVocabularyJSONRoundTrip covers the additive
// ThreadReadResponse fields (Snapshot, Epoch, RequestGeneration, Overlay,
// Authoritative, Changes) and confirms the response still validates through
// the existing item-mode response validator.
func TestThreadReadResponseHistoryVocabularyJSONRoundTrip(t *testing.T) {
	position := &ThreadItemPosition{Entry: 1, Item: 0}
	item := ThreadItem{Type: "agentMessage", ID: "item-1", TranscriptKey: "key-1", Position: position}
	turn := Turn{ID: "turn_1", Items: []ThreadItem{item}, ItemsView: TurnItemsViewFragment}
	response := ThreadReadResponse{
		Thread:            Thread{ID: "thread", Turns: []Turn{turn}},
		RequestGeneration: 9,
		Epoch:             2,
		Snapshot:          &SnapshotIdentity{Incarnation: "incarnation-1", Length: 17},
		Overlay: []OverlayItem{
			{Key: "stream:s1:agentMessage", Kind: OverlayStream, Item: ThreadItem{Type: "agentMessage", ID: "s1"}},
		},
		Authoritative: true,
		Changes: &HistoryChanges{
			Turns: []Turn{{ID: "turn_0", Version: 1}},
			Items: []ThreadItem{{Type: "agentMessage", ID: "item-0", Version: 1}},
		},
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"requestGeneration":9`,
		`"epoch":2`,
		`"snapshot":{"incarnation":"incarnation-1","length":17}`,
		`"overlay":[`,
		`"authoritative":true`,
		`"changes":{`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("ThreadReadResponse JSON %s missing %s", raw, want)
		}
	}

	var decoded ThreadReadResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RequestGeneration != 9 || decoded.Epoch != 2 {
		t.Fatalf("round trip generation/epoch = %+v", decoded)
	}
	if decoded.Snapshot == nil || decoded.Snapshot.Incarnation != "incarnation-1" || decoded.Snapshot.Length != 17 {
		t.Fatalf("round trip snapshot = %+v", decoded.Snapshot)
	}
	if len(decoded.Overlay) != 1 || decoded.Overlay[0].Kind != OverlayStream {
		t.Fatalf("round trip overlay = %+v", decoded.Overlay)
	}
	if !decoded.Authoritative {
		t.Fatalf("round trip authoritative = %+v", decoded)
	}
	if decoded.Changes == nil || len(decoded.Changes.Turns) != 1 || len(decoded.Changes.Items) != 1 {
		t.Fatalf("round trip changes = %+v", decoded.Changes)
	}

	if err := ValidateThreadReadItemResponse(response); err != nil {
		t.Fatalf("history vocabulary response still validates: %v", err)
	}

	// ThreadReadParams additions.
	readParams := ThreadReadParams{
		Ref:               "local:thread",
		RequestGeneration: 3,
		HeldSnapshot:      &SnapshotIdentity{Incarnation: "incarnation-1", Length: 5},
	}
	raw, err = json.Marshal(readParams)
	if err != nil {
		t.Fatal(err)
	}
	var decodedParams ThreadReadParams
	if err := json.Unmarshal(raw, &decodedParams); err != nil {
		t.Fatal(err)
	}
	if decodedParams.RequestGeneration != 3 || decodedParams.HeldSnapshot == nil || decodedParams.HeldSnapshot.Incarnation != "incarnation-1" {
		t.Fatalf("round trip ThreadReadParams = %+v", decodedParams)
	}

	// ThreadTurnsListParams/Response additions.
	listParams := ThreadTurnsListParams{Ref: "local:thread", Cursor: "opaque", RequestGeneration: 4}
	raw, err = json.Marshal(listParams)
	if err != nil {
		t.Fatal(err)
	}
	var decodedListParams ThreadTurnsListParams
	if err := json.Unmarshal(raw, &decodedListParams); err != nil {
		t.Fatal(err)
	}
	if decodedListParams.RequestGeneration != 4 {
		t.Fatalf("round trip ThreadTurnsListParams = %+v", decodedListParams)
	}

	listResponse := ThreadTurnsListResponse{
		Data:              []Turn{turn},
		RequestGeneration: 4,
		Epoch:             2,
		Snapshot:          &SnapshotIdentity{Incarnation: "incarnation-1", Length: 17},
		Authoritative:     true,
	}
	raw, err = json.Marshal(listResponse)
	if err != nil {
		t.Fatal(err)
	}
	var decodedListResponse ThreadTurnsListResponse
	if err := json.Unmarshal(raw, &decodedListResponse); err != nil {
		t.Fatal(err)
	}
	if decodedListResponse.RequestGeneration != 4 || decodedListResponse.Epoch != 2 || !decodedListResponse.Authoritative {
		t.Fatalf("round trip ThreadTurnsListResponse = %+v", decodedListResponse)
	}
	if decodedListResponse.Snapshot == nil || decodedListResponse.Snapshot.Incarnation != "incarnation-1" {
		t.Fatalf("round trip ThreadTurnsListResponse.Snapshot = %+v", decodedListResponse.Snapshot)
	}

	// ThreadResyncParams.Epoch, ThreadStatusChangedParams.ActiveTurnID,
	// ThreadForkParams.SourceItemKey.
	resync := ThreadResyncParams{ThreadID: "thread", Ref: "local:thread", Epoch: 3}
	raw, err = json.Marshal(resync)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"epoch":3`) {
		t.Fatalf("ThreadResyncParams JSON %s missing epoch", raw)
	}

	statusChanged := ThreadStatusChangedParams{ThreadID: "thread", Ref: "local:thread", ActiveTurnID: "turn_1"}
	raw, err = json.Marshal(statusChanged)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"activeTurnId":"turn_1"`) {
		t.Fatalf("ThreadStatusChangedParams JSON %s missing activeTurnId", raw)
	}

	fork := ThreadForkParams{Ref: "local:thread", SourceTurnID: "5", SourceItemKey: "item-key-1"}
	raw, err = json.Marshal(fork)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"sourceItemKey":"item-key-1"`) {
		t.Fatalf("ThreadForkParams JSON %s missing sourceItemKey", raw)
	}
}
