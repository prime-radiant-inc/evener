package server

import (
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

// TestAppTurnsFromNotifications_DecodesEveryCatalogedNotification is a
// characterization test for appTurnsFromNotifications' per-method decode
// (kata vbp3): every case in its switch now decodes into a declared appwire
// catalog Payload/Params type instead of a hand-rolled anonymous struct. This
// drives one record per case with realistic JSON and asserts the resulting
// appwire.Turn/ThreadItem fields, so a field-name mistake made while
// converting a case (the exact failure mode vbp3 exists to catch — see
// TestNotificationCatalog_TurnStartedMatchesProducedShape below) fails here
// instead of silently decoding a zero value.
func TestAppTurnsFromNotifications_DecodesEveryCatalogedNotification(t *testing.T) {
	records := []appserver.SequencedNotification{
		{Notification: appwire.Notification{
			Method: appwire.NotifyTurnStarted,
			Params: []byte(`{"threadId":"01T","ref":"local:01T","turn":{"id":"turn_1","status":"inProgress","itemsView":"full"}}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyItemStarted,
			Params: []byte(`{"threadId":"01T","turnId":"turn_1","item":{"id":"i1","type":"commandExecution","toolName":"shell","status":"inProgress"}}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyToolOutputDelta,
			Params: []byte(`{"turnId":"turn_1","itemId":"i1","callId":"c1","delta":"hello "}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyToolOutputDelta,
			Params: []byte(`{"turnId":"turn_1","itemId":"i1","callId":"c1","delta":"world"}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyItemCompleted,
			Params: []byte(`{"threadId":"01T","turnId":"turn_1","item":{"id":"i1","type":"commandExecution","toolName":"shell","status":"completed"}}`),
		}},
		{Notification: appwire.Notification{
			Method: appwire.NotifyTurnCompleted,
			Params: []byte(`{"turnId":"turn_1","turn":{"id":"turn_1","status":"completed"}}`),
		}},
	}

	turns := appTurnsFromNotifications(records)
	if len(turns) != 1 {
		t.Fatalf("turns = %+v, want exactly 1", turns)
	}
	turn := turns[0]
	if turn.ID != "turn_1" || turn.Status != appwire.TurnStatusCompleted {
		t.Fatalf("turn = %+v, want ID turn_1, Status completed", turn)
	}
	if len(turn.Items) != 1 {
		t.Fatalf("turn.Items = %+v, want exactly 1", turn.Items)
	}
	item := turn.Items[0]
	if item.ID != "i1" || item.CallID != "c1" || item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("item = %+v, want ID i1, CallID c1, Status completed", item)
	}
	if item.Output != "hello world" {
		t.Fatalf("item.Output = %q, want accumulated %q", item.Output, "hello world")
	}
}
