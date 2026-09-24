package server

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// TestAppTurnSnapshotApplyCommittedReadsDeltasFromTypedParams pins that a
// streamed delta the caller still holds typed is applied from those params,
// not by decoding the JSON the notifier just encoded them into: deltas are
// most of a turn's notifications, and every one was being decoded again. The
// committed records here carry params no decoder could read, so only the
// typed path can produce the text.
func TestAppTurnSnapshotApplyCommittedReadsDeltasFromTypedParams(t *testing.T) {
	undecodable := []byte(`not json`)
	snapshot := &appTurnSnapshot{}
	snapshot.ApplyCommitted(appserver.SequencedNotification{Notification: appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: undecodable}},
		appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_assistant_1", Delta: "hel"})
	snapshot.ApplyCommitted(appserver.SequencedNotification{Notification: appwire.Notification{Method: appwire.NotifyAgentMessageDelta, Params: undecodable}},
		appwire.AgentMessageDeltaParams{TurnID: "turn_1", ItemID: "item_assistant_1", Delta: "lo"})
	snapshot.ApplyCommitted(appserver.SequencedNotification{Notification: appwire.Notification{Method: appwire.NotifyReasoningSummaryDelta, Params: undecodable}},
		appwire.ReasoningSummaryDeltaParams{TurnID: "turn_1", ItemID: "item_reasoning_1", Delta: "think"})
	snapshot.ApplyCommitted(appserver.SequencedNotification{Notification: appwire.Notification{Method: appwire.NotifyToolOutputDelta, Params: undecodable}},
		appwire.ToolOutputDeltaParams{TurnID: "turn_1", ItemID: "item_tool_1", CallID: "call_1", Delta: "out"})

	turns := snapshot.Snapshot()
	if len(turns) != 1 || len(turns[0].Items) != 3 {
		t.Fatalf("turns = %+v, want one turn with three items", turns)
	}
	items := turns[0].Items
	if items[0].ID != "item_assistant_1" || items[0].Text != "hello" {
		t.Fatalf("agent message item = %+v, want text %q", items[0], "hello")
	}
	if items[1].ID != "item_reasoning_1" || items[1].Text != "think" {
		t.Fatalf("reasoning item = %+v, want text %q", items[1], "think")
	}
	if items[2].ID != "item_tool_1" || items[2].CallID != "call_1" || items[2].Output != "out" {
		t.Fatalf("tool item = %+v, want call_1 output %q", items[2], "out")
	}
}
