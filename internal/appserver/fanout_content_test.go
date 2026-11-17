package appserver

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestFilteredFanoutDeliversEachSubscribersOwnFrame pins the frames one
// recorded notification fans out to several content selections: which items a
// view keeps, what a tool call is reduced to, the identity a view fences on,
// and the ordered overlay state machine that decides which live deltas reach
// which reader. An upsert must be remembered before a delta that depends on
// it, and a reset must forget it, so a fanout that decodes a payload once and
// shares it across subscribers must not reorder those transitions.
//
// The incarnation literals below are wire contract, not production logic
// restated: a selected view fences on "content1:<selection key in hex>:" +
// the base64url source incarnation ("inc" encodes to "aW5j"). Spelling them
// out is what lets the test fail when the encoding changes.
func TestFilteredFanoutDeliversEachSubscribersOwnFrame(t *testing.T) {
	s := NewServer(ServerConfig{})
	no := false
	subscribers := []struct {
		id        string
		selection appwire.ThreadContentParams
	}{
		{"full", appwire.ThreadContentParams{}},
		{"compact", appwire.ThreadContentParams{IncludeThinking: &no, IncludeToolCalls: &no}},
		{"intent", appwire.ThreadContentParams{IncludeToolCalls: &no, IncludeToolCallIntents: &no}},
		{"thinking", appwire.ThreadContentParams{IncludeConversation: &no}},
	}
	conns := map[string]*Connection{}
	for _, subscriber := range subscribers {
		conn := s.NewConnection(subscriber.id)
		conn.setInitialized()
		s.registerConnection(conn)
		conns[subscriber.id] = conn
	}
	HandleThreadHistory(s.Router(), func(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if !CaptureSubscriptionWithHandoff(ctx, p.ReplaceSubscription, func() string { return "thread" }, func() uint64 { return s.routedSeq }, func() bool { return true }, CaptureSubscriptionHandoff{}) {
			t.Error("fixture capture rejected")
			return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("fixture")
		}
		return appwire.ThreadReadResponse{
			Thread:   appwire.Thread{ID: "thread"},
			Snapshot: &appwire.SnapshotIdentity{Incarnation: "inc", Length: 0},
		}, nil
	}, func(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		t.Error("fixture paged a turns list")
		return appwire.ThreadTurnsListResponse{}, nil
	})
	for i, subscriber := range subscribers {
		conn := conns[subscriber.id]
		response := conn.HandleMessage(t.Context(), appwire.RequestMessage(
			appwire.NewIntID(int64(i+1)),
			appwire.MethodThreadRead,
			appwire.ThreadReadParams{ThreadContentParams: subscriber.selection, ThreadID: "thread", IncludeTurns: true, Subscribe: true},
		))
		if response.Error != nil {
			t.Fatalf("%s subscribe: %v", subscriber.id, response.Error)
		}
		if err := conn.enqueueResponse(t.Context(), response); err != nil {
			t.Fatalf("%s subscribe response: %v", subscriber.id, err)
		}
		<-conn.send
	}

	// Every delivery below runs synchronously inside Broadcast, so each
	// connection's channel already holds exactly what that subscriber keeps by
	// the time Broadcast returns. Draining without waiting keeps absence
	// deterministic rather than a timing guess.
	drained := map[string][]appwire.Notification{}
	drain := func() {
		for id, conn := range conns {
		connDrained:
			for {
				select {
				case frame := <-conn.send:
					if frame.Notification == nil {
						t.Fatalf("%s received a non-notification frame", id)
					}
					drained[id] = append(drained[id], *frame.Notification)
				default:
					break connDrained
				}
			}
		}
	}

	s.Broadcast("thread", appwire.NotifyHistoryUpdated, appwire.HistoryUpdatedParams{
		ThreadID: "thread",
		Ref:      "local:thread",
		Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 2},
		Items: []appwire.ThreadItem{
			{Type: "userMessage", ID: "u1", Text: "hi"},
			{Type: "agentMessage", ID: "a1", Text: "hello"},
			{Type: "reasoning", ID: "r1", Text: "think"},
			{Type: "commandExecution", ID: "c1", ToolName: "shell", CallID: "call1", Description: "list", ArgumentsJSON: `{"cmd":"ls"}`, Output: "out"},
		},
	})
	drain()

	streamUpsert := appwire.OverlayUpsertedParams{
		ThreadID: "thread",
		Ref:      "local:thread",
		Item:     appwire.OverlayItem{Key: "stream:s1:agentMessage", Kind: appwire.OverlayStream, StreamID: "s1", RoundID: "round1", Item: appwire.ThreadItem{Type: "agentMessage", ID: "a1", Text: "chunk"}},
	}
	s.Broadcast("thread", appwire.NotifyOverlayUpserted, streamUpsert)
	drain()
	s.Broadcast("thread", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{ThreadID: "thread", Key: streamUpsert.Item.Key, Field: appwire.OverlayDeltaText, Delta: "more"})
	drain()
	s.Broadcast("thread", appwire.NotifyOverlayReset, appwire.OverlayResetParams{ThreadID: "thread", StreamID: "s1"})
	drain()
	s.Broadcast("thread", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{ThreadID: "thread", Key: streamUpsert.Item.Key, Field: appwire.OverlayDeltaText, Delta: "after-reset"})
	drain()

	toolUpsert := appwire.OverlayUpsertedParams{
		ThreadID: "thread",
		Ref:      "local:thread",
		Item:     appwire.OverlayItem{Key: "tool:c1", Kind: appwire.OverlayTool, RoundID: "round1", Item: appwire.ThreadItem{Type: "commandExecution", ID: "c1", ToolName: "shell", CallID: "call1", Description: "list", ArgumentsJSON: `{"cmd":"ls"}`, Output: "out"}},
	}
	s.Broadcast("thread", appwire.NotifyOverlayUpserted, toolUpsert)
	drain()
	s.Broadcast("thread", appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{ThreadID: "thread", Key: toolUpsert.Item.Key, Field: appwire.OverlayDeltaOutput, Delta: "tail"})
	drain()

	// Each subscriber's ordered method list: which notifications it keeps, in
	// the order it keeps them. The stream delta after the reset reaches nobody,
	// and the tool output delta reaches only the views that kept the running
	// tool. A zero selection has no filter at all (apphistory.NewFilter returns
	// nil), so the full view is a passthrough: it sees the post-reset delta the
	// filtered views drop, and it never reduces a tool call.
	wantMethods := map[string][]string{
		"full":     {appwire.NotifyHistoryUpdated, appwire.NotifyOverlayUpserted, appwire.NotifyOverlayDelta, appwire.NotifyOverlayReset, appwire.NotifyOverlayDelta, appwire.NotifyOverlayUpserted, appwire.NotifyOverlayDelta},
		"compact":  {appwire.NotifyHistoryUpdated, appwire.NotifyOverlayUpserted, appwire.NotifyOverlayDelta, appwire.NotifyOverlayReset, appwire.NotifyOverlayUpserted},
		"intent":   {appwire.NotifyHistoryUpdated, appwire.NotifyOverlayUpserted, appwire.NotifyOverlayDelta, appwire.NotifyOverlayReset},
		"thinking": {appwire.NotifyHistoryUpdated, appwire.NotifyOverlayReset, appwire.NotifyOverlayUpserted, appwire.NotifyOverlayDelta},
	}
	for id, want := range wantMethods {
		got := drained[id]
		if len(got) != len(want) {
			t.Fatalf("%s received %v, want %v", id, methodList(got), want)
		}
		for i := range want {
			if got[i].Method != want[i] {
				t.Fatalf("%s received %v, want %v", id, methodList(got), want)
			}
		}
	}

	// The history frame each view receives: which items survive, what the tool
	// call is reduced to, and the identity the view fences on.
	type wantItem struct {
		id          string
		description string
		arguments   string
		output      string
	}
	history := map[string]struct {
		items       []wantItem
		incarnation string
	}{
		"full": {
			items: []wantItem{
				{id: "u1"}, {id: "a1"}, {id: "r1"},
				{id: "c1", description: "list", arguments: `{"cmd":"ls"}`, output: "out"},
			},
			incarnation: "inc",
		},
		"compact": {
			items: []wantItem{
				{id: "u1"}, {id: "a1"},
				{id: "c1", description: "list"},
			},
			incarnation: "content1:6:aW5j",
		},
		"intent": {
			items:       []wantItem{{id: "u1"}, {id: "a1"}, {id: "r1"}},
			incarnation: "content1:c:aW5j",
		},
		"thinking": {
			items: []wantItem{
				{id: "r1"},
				{id: "c1", description: "list", arguments: `{"cmd":"ls"}`, output: "out"},
			},
			incarnation: "content1:1:aW5j",
		},
	}
	for id, want := range history {
		var params appwire.HistoryUpdatedParams
		if err := json.Unmarshal(drained[id][0].Params, &params); err != nil {
			t.Fatalf("%s history params: %v", id, err)
		}
		if len(params.Items) != len(want.items) {
			t.Fatalf("%s history items = %+v, want %+v", id, params.Items, want.items)
		}
		for i, item := range want.items {
			got := params.Items[i]
			if got.ID != item.id || got.Description != item.description || got.ArgumentsJSON != item.arguments || got.Output != item.output {
				t.Fatalf("%s history item %d = %+v, want id=%s description=%q arguments=%q output=%q", id, i, got, item.id, item.description, item.arguments, item.output)
			}
		}
		if params.Snapshot.Incarnation != want.incarnation {
			t.Fatalf("%s history incarnation = %q, want %q", id, params.Snapshot.Incarnation, want.incarnation)
		}
	}

	// The stream upsert and the delta that follows it. The thinking view keeps
	// no agent message, so it never remembered the stream's identity and gets
	// neither frame.
	stream := map[string]int{"full": 1, "compact": 1, "intent": 1, "thinking": -1}
	for id, upsertIndex := range stream {
		if upsertIndex < 0 {
			continue
		}
		got := drained[id]
		var upsert appwire.OverlayUpsertedParams
		if err := json.Unmarshal(got[upsertIndex].Params, &upsert); err != nil {
			t.Fatalf("%s stream upsert params: %v", id, err)
		}
		if upsert.Item.Key != streamUpsert.Item.Key || upsert.Item.Item.ID != "a1" {
			t.Fatalf("%s stream upsert = %+v", id, upsert.Item)
		}
		var delta appwire.OverlayDeltaParams
		if err := json.Unmarshal(got[upsertIndex+1].Params, &delta); err != nil {
			t.Fatalf("%s stream delta params: %v", id, err)
		}
		if delta.Key != streamUpsert.Item.Key || delta.Delta != "more" {
			t.Fatalf("%s stream delta = %+v", id, delta)
		}
	}

	// The tool upsert and its output delta. The intent view dropped the call,
	// so it gets neither; the compact view kept the intent only, so it gets the
	// upsert without arguments or output and no output delta.
	tool := map[string]int{"full": 5, "compact": 4, "intent": -1, "thinking": 2}
	for id, upsertIndex := range tool {
		if upsertIndex < 0 {
			continue
		}
		got := drained[id]
		var upsert appwire.OverlayUpsertedParams
		if err := json.Unmarshal(got[upsertIndex].Params, &upsert); err != nil {
			t.Fatalf("%s tool upsert params: %v", id, err)
		}
		wantArguments, wantOutput := `{"cmd":"ls"}`, "out"
		if id == "compact" {
			wantArguments, wantOutput = "", ""
		}
		if upsert.Item.Item.ArgumentsJSON != wantArguments || upsert.Item.Item.Output != wantOutput || upsert.Item.Item.Description != "list" {
			t.Fatalf("%s tool upsert item = %+v, want arguments=%q output=%q", id, upsert.Item.Item, wantArguments, wantOutput)
		}
		if id == "compact" {
			if upsertIndex+1 < len(got) {
				t.Fatalf("%s received a tool output delta after an intent-only upsert: %v", id, methodList(got[upsertIndex:]))
			}
			continue
		}
		if upsertIndex+1 >= len(got) {
			t.Fatalf("%s dropped the tool output delta", id)
		}
		var delta appwire.OverlayDeltaParams
		if err := json.Unmarshal(got[upsertIndex+1].Params, &delta); err != nil {
			t.Fatalf("%s tool delta params: %v", id, err)
		}
		if delta.Key != toolUpsert.Item.Key || delta.Delta != "tail" {
			t.Fatalf("%s tool delta = %+v", id, delta)
		}
	}
}

func methodList(notifications []appwire.Notification) []string {
	out := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		out = append(out, notification.Method)
	}
	return out
}
