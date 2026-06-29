package server

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

// notificationMethods is the set of methods appTurnsFromNotifications reduces
// over. Indexing into a fixed list lets the fuzzer pick a method cheaply while
// still driving the real reducer's per-method json.Unmarshal of params.
var notificationMethods = []string{
	appwire.NotifyTurnStarted,
	appwire.NotifyItemStarted,
	appwire.NotifyItemCompleted,
	appwire.NotifyAgentMessageDelta,
	appwire.NotifyReasoningSummaryDelta,
	appwire.NotifyToolOutputDelta,
	appwire.NotifyTurnCompleted,
	"some/unhandled/method",
}

// FuzzAppTurnsFromNotifications drives the real server.appTurnsFromNotifications
// seam, which replays a sequence of recorded AppWire notifications into a set of
// AppWire turns (the web reload path's "rebuild from the notification log"
// branch). Each record's params are json.Unmarshal'd per-method, then merged via
// ensureTurn/upsertItem/itemForDelta/mergeAppThreadItem. Two records are built
// per input so the cross-record merge logic (an item started then completed, a
// delta accumulating onto an existing item) is exercised, not just a single
// insert. The oracle is floor "no panic" plus re-serializability of the result.
func FuzzAppTurnsFromNotifications(f *testing.F) {
	f.Add(0, []byte(`{"turn":{"id":"turn_1","status":"inProgress","itemsView":"full"}}`),
		2, []byte(`{"turnId":"turn_1","item":{"id":"i1","type":"agentMessage","text":"hi","status":"completed"}}`))
	f.Add(1, []byte(`{"turnId":"turn_1","item":{"id":"i1","type":"commandExecution","toolName":"shell"}}`),
		3, []byte(`{"turnId":"turn_1","itemId":"i1","delta":"out"}`))
	f.Add(5, []byte(`{"turnId":"turn_1","itemId":"i9","callId":"c9","delta":"x"}`),
		6, []byte(`{"turn":{"id":"turn_1","status":"completed","items":[{"id":"i9","type":"commandExecution"}]}}`))
	f.Add(7, []byte(`null`), 0, []byte(`not json`))

	f.Fuzz(func(t *testing.T, m1 int, p1 []byte, m2 int, p2 []byte) {
		records := []appserver.SequencedNotification{
			newRecord(1, m1, p1),
			newRecord(2, m2, p2),
		}
		turns := appTurnsFromNotifications(records)
		if _, err := json.Marshal(turns); err != nil {
			t.Fatalf("rebuilt turns failed to marshal: %v\n m1=%d p1=%q\n m2=%d p2=%q", err, m1, p1, m2, p2)
		}
	})
}

func newRecord(seq uint64, methodIdx int, params []byte) appserver.SequencedNotification {
	idx := methodIdx % len(notificationMethods)
	if idx < 0 {
		idx += len(notificationMethods)
	}
	return appserver.SequencedNotification{
		Seq: seq,
		Notification: appwire.Notification{
			Method: notificationMethods[idx],
			Params: json.RawMessage(params),
		},
	}
}
