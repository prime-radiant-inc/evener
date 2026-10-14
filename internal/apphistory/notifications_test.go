package apphistory

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

func notification(method string, params any) appwire.Notification {
	return *appwire.NotificationMessage(method, params).Notification
}

func TestContentFilterHistoryAndOverlayDeltas(t *testing.T) {
	t.Parallel()
	selection := conversationParams().Selection()
	f := NewFilter(selection)
	f.Seed([]appwire.OverlayItem{{Key: "stream", StreamID: "s", Item: appwire.ThreadItem{Type: "agentMessage"}}})
	for _, tc := range []struct {
		key   string
		field appwire.OverlayDeltaField
		want  bool
	}{{"stream", appwire.OverlayDeltaText, true}, {"hidden", appwire.OverlayDeltaText, false}} {
		_, keep, err := f.Notification(notification(appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{Key: tc.key, Field: tc.field, Delta: "chunk-data"}))
		if err != nil || keep != tc.want {
			t.Fatalf("delta key=%s keep=%v err=%v", tc.key, keep, err)
		}
	}
	input := appwire.HistoryUpdatedParams{ThreadID: "thread", Snapshot: appwire.SnapshotIdentity{Incarnation: "inc", Length: 42}, Items: []appwire.ThreadItem{{ID: "a", Type: "agentMessage"}, {ID: "r", Type: "reasoning"}}}
	n, keep, err := f.Notification(notification(appwire.NotifyHistoryUpdated, input))
	if err != nil || !keep {
		t.Fatal(err)
	}
	var got appwire.HistoryUpdatedParams
	if err = json.Unmarshal(n.Params, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "a" || got.Snapshot.Incarnation == input.Snapshot.Incarnation {
		t.Fatal("history selection/identity not applied")
	}
	_, _, err = f.Notification(notification(appwire.NotifyOverlayReset, appwire.OverlayResetParams{StreamID: "s"}))
	if err != nil {
		t.Fatal(err)
	}
	_, keep, err = f.Notification(notification(appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{Key: "stream", Field: appwire.OverlayDeltaText, Delta: "late-data"}))
	if err != nil || keep {
		t.Fatal("reset retained overlay")
	}
}

func TestContentFilterIntentOnlySuppressesToolOutput(t *testing.T) {
	t.Parallel()
	no := false
	f := NewFilter(appwire.ThreadContentParams{IncludeToolCalls: &no}.Selection())
	upsert := appwire.OverlayUpsertedParams{Item: appwire.OverlayItem{Key: "tool", RoundID: "round", Item: appwire.ThreadItem{Type: "commandExecution", Description: "intent-data", ArgumentsJSON: `{"x":1}`, Output: "payload"}}}
	n, keep, err := f.Notification(notification(appwire.NotifyOverlayUpserted, upsert))
	if err != nil || !keep {
		t.Fatal(err)
	}
	var got appwire.OverlayUpsertedParams
	if err = json.Unmarshal(n.Params, &got); err != nil {
		t.Fatal(err)
	}
	if got.Item.Item.Description != upsert.Item.Item.Description || got.Item.Item.Output != "" || got.Item.Item.ArgumentsJSON != "" {
		t.Fatal("intent projection contains output")
	}
	_, keep, err = f.Notification(notification(appwire.NotifyOverlayDelta, appwire.OverlayDeltaParams{Key: "tool", Field: appwire.OverlayDeltaOutput, Delta: "payload"}))
	if err != nil || keep {
		t.Fatal("tool output delta leaked")
	}
	_, keep, err = f.Notification(notification(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "thread"}))
	if err != nil || !keep {
		t.Fatal("essential status suppressed")
	}
}
