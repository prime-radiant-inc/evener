package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// statusFrames decodes the thread/status/changed frames recorded after
// cursor: statusNotifications with a starting cursor, so a test reads only
// what its own setup published.
func statusFrames(t *testing.T, srv *Server, cursor uint64, threadID string) []appwire.ThreadStatusChangedParams {
	t.Helper()
	return statusNotificationsAfter(t, srv, cursor, threadID)
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

// The same settlement through the bridge's own drain: EXECUTION_ENDED and
// SESSION_END both arrive before SetProcessing(false), the production
func TestSettlementFrameNamesTheEndedTurnWhenBothEndEventsDrainFirst(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_drained_ends")
	// The execution runs the way production runs one: published
	// server-side, and projected so the projector holds the turn.
	srv.SetProcessingTurn("turn_drained")
	srv.RecordAppEvent(threadEvent("th_drained_ends", events.ExecutionStartedData{TurnID: "turn_drained"}))
	cursor := srv.appNotifier.CurrentSequence()

	BridgeEvent(srv, threadEvent("th_drained_ends", events.ExecutionEndedData{TurnID: "turn_drained", Status: "completed"}), nil)
	BridgeEvent(srv, threadEvent("th_drained_ends", events.SessionEndData{Reason: "input_complete", State: appwire.ThreadStatusIdle}), nil)
	srv.SetProcessing(false)

	frames := statusFrames(t, srv, cursor, "th_drained_ends")
	if len(frames) != 1 {
		t.Fatalf("settlement published %+v status frames, want the session end's one idle", frames)
	}
	if frames[0].Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("settlement status = %q, want idle", frames[0].Status.Type)
	}
	if frames[0].EndedTurnID != "turn_drained" {
		t.Fatalf("session end frame endedTurnId = %q, want the turn the input ran", frames[0].EndedTurnID)
	}
}

// A delayed SESSION_END queued behind a newer published turn defers and
// replays when the carrier is abandoned, naming only what it truthfully
// ended. The guard's own promise — never the newer turn — is what the
// deferral exists for; the ownership the frame supplies beside it is the
// old input's own turn when its execution drained (the client whose
// snapshot still runs that turn applies it; one that adopted the newer
// turn does not, exactly as the spec's pre-cut termination rule reads a
// name that is not the snapshot's running turn), and nothing when no
// execution of the old input ever ended.
func TestDelayedSessionEndDoesNotNameTheNewerTurn(t *testing.T) {
	for name, wantEnded := range map[string]string{
		"with the old execution drained": "turn_old",
		"before any execution end":       "",
	} {
		t.Run(name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", "th_guard")
			srv.SetProcessingTurn("turn_new")
			cursor := srv.appNotifier.CurrentSequence()

			if wantEnded != "" {
				BridgeEvent(srv, threadEvent("th_guard", events.ExecutionStartedData{TurnID: "turn_old"}), nil)
				BridgeEvent(srv, threadEvent("th_guard", events.ExecutionEndedData{TurnID: "turn_old", Status: "completed"}), nil)
			}
			BridgeEvent(srv, threadEvent("th_guard", events.SessionEndData{Reason: "turn_failed", State: appwire.ThreadStatusIdle}), nil)
			if frames := statusFrames(t, srv, cursor, "th_guard"); len(frames) != 0 {
				t.Fatalf("a superseded end published %+v, want it deferred", frames)
			}

			srv.SetProcessing(false)
			frames := statusFrames(t, srv, cursor, "th_guard")
			if len(frames) != 1 || frames[0].Status.Type != appwire.ThreadStatusIdle {
				t.Fatalf("the abandoned carrier's deferred frame = %+v, want one idle", frames)
			}
			if frames[0].EndedTurnID == "turn_new" {
				t.Fatalf("a delayed end named the newer turn %q; the deferral guard exists to prevent exactly that", frames[0].EndedTurnID)
			}
			if frames[0].EndedTurnID != wantEnded {
				t.Fatalf("a delayed end named turn %q, want its own input's %q", frames[0].EndedTurnID, wantEnded)
			}
		})
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
