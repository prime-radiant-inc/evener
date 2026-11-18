package server

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// statusFrames decodes the thread/status/changed frames recorded after
// cursor, paired with the notification cursor each one leaves.
func statusFrames(t *testing.T, srv *Server, cursor uint64, threadID string) []appwire.ThreadStatusChangedParams {
	t.Helper()
	var out []appwire.ThreadStatusChangedParams
	for _, sequenced := range srv.AppNotificationsAfter(cursor, threadID) {
		if sequenced.Notification.Method != appwire.NotifyThreadStatusChanged {
			continue
		}
		var params appwire.ThreadStatusChangedParams
		if err := json.Unmarshal(sequenced.Notification.Params, &params); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		out = append(out, params)
	}
	return out
}

// A Stop that flows through the root's settlement path must name the turn
// it ended: finishProcessing clears the active turn id before building its
// fallback frame, so the id must be captured before the clear. Without it a
// real root Stop arrives looking like an older-daemon frame and no client
// rule can tell whose end it was.
func TestSettlementFrameNamesTheEndedTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_settlement")
	srv.SetProcessingTurn("turn_settled")
	cursor := srv.appNotifier.CurrentSequence()

	srv.SetProcessing(false)

	frames := statusFrames(t, srv, cursor, "th_settlement")
	if len(frames) != 1 {
		t.Fatalf("settlement published %+v status frames, want the one idle fallback", frames)
	}
	if frames[0].Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("settlement status = %q, want idle", frames[0].Status.Type)
	}
	if frames[0].EndedTurnID != "turn_settled" {
		t.Fatalf("settlement frame endedTurnId = %q, want the published turn it ended", frames[0].EndedTurnID)
	}
}

// A delegate's Stop flows through the projector paths, not the root's
// settlement: its terminal status names the ended turn from the event data
// (EXECUTION_ENDED) or from the running execution the projector held
// (SESSION_END without one, the root's own suppressed-end shape).
func TestDescendantStopFramesNameTheEndedTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")

	// The delegate's own projector path: an execution ends while running.
	srv.RecordDescendantAppEvent("root", threadEvent("child-1", events.ExecutionStartedData{TurnID: "child-turn-1"}))
	cursor := srv.appNotifier.CurrentSequence()
	srv.RecordDescendantAppEvent("root", threadEvent("child-1", events.ExecutionEndedData{TurnID: "child-turn-1", Status: "completed"}))
	frames := statusFrames(t, srv, cursor, "child-1")
	if len(frames) != 1 || frames[0].EndedTurnID != "child-turn-1" {
		t.Fatalf("delegate execution end = %+v, want one frame naming child-turn-1", frames)
	}

	// A closing delegate whose EXECUTION_ENDED never arrives: the session
	// end still names the turn the projector held.
	srv.RecordDescendantAppEvent("root", threadEvent("child-2", events.ExecutionStartedData{TurnID: "child-turn-2"}))
	cursor = srv.appNotifier.CurrentSequence()
	srv.RecordDescendantAppEvent("root", threadEvent("child-2", events.SessionEndData{State: appwire.ThreadStatusClosed}))
	frames = statusFrames(t, srv, cursor, "child-2")
	if len(frames) != 1 || frames[0].EndedTurnID != "child-turn-2" {
		t.Fatalf("delegate session end = %+v, want one frame naming child-turn-2", frames)
	}
}
