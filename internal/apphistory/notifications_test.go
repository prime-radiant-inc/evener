package apphistory

import (
	"encoding/json"
	"strconv"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestContentFilterDoesNotRetainNoticeSlots(t *testing.T) {
	t.Parallel()
	no := false
	f := NewFilter(appwire.ThreadContentParams{IncludeDiagnostics: &no}.Selection())
	f.Seed([]appwire.OverlayItem{{Kind: appwire.OverlayNotice, Key: "seed", Item: appwire.ThreadItem{Type: "systemMessage"}}})
	for i := range 200 {
		_, keep, err := f.Notification(notification(appwire.NotifyOverlayUpserted, appwire.OverlayUpsertedParams{Item: appwire.OverlayItem{Kind: appwire.OverlayNotice, Key: strconv.Itoa(i), Item: appwire.ThreadItem{Type: "systemMessage"}}}))
		if err != nil || !keep {
			t.Fatal("notice was not delivered", err)
		}
	}
	if len(f.overlays) != 0 {
		t.Fatalf("retained %d notice identities without delta streams", len(f.overlays))
	}
}

func notification(method string, params any) appwire.Notification {
	return *appwire.NotificationMessage(method, params).Notification
}

func TestContentFilterHistoryAndOverlayDeltas(t *testing.T) {
	t.Parallel()
	selection := conversationParams().Selection()
	f := NewFilter(selection)
	f.Seed([]appwire.OverlayItem{{Key: "stream", StreamID: "s", Item: appwire.ThreadItem{Type: "agentMessage"}}})
	if _, keep, err := f.Notification(notification(appwire.NotifyEvenerThreadResync, appwire.ThreadResyncParams{ThreadID: "thread"})); err != nil || !keep {
		t.Fatal("resync was not delivered", err)
	}
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

// A notification whose params cannot decode is an error, not a silently
// dropped event: the filter hands the decode failure back to the caller
// for every overlay method.
func TestContentFilterRejectsUndecodableNotificationParams(t *testing.T) {
	t.Parallel()
	no := false
	f := NewFilter(appwire.ThreadContentParams{IncludeDiagnostics: &no}.Selection())
	for _, method := range []string{
		appwire.NotifyOverlayUpserted,
		appwire.NotifyOverlayDelta,
	} {
		n := appwire.Notification{Method: method, Params: json.RawMessage(`not json`)}
		if _, _, err := f.Notification(n); err == nil {
			t.Fatalf("%s: undecodable params were accepted", method)
		}
	}
}
