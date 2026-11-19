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

// A delayed SESSION_END queued behind a newer published turn defers and,
// when the carrier is abandoned, replays beside the settlement of the turn
// processing cleared. The guard's own promise — the DEFERRED frame never
// names the newer turn — is what the deferral exists for; the ownership the
// frame supplies is the old input's own turn when its execution drained
// (the client whose snapshot still runs that turn applies it; one that
// adopted the newer turn does not, exactly as the spec's pre-cut
// termination rule reads a name that is not the snapshot's running turn),
// and nothing when no execution of the old input ever ended. The
// settlement beside it names the cleared turn truthfully: the published
// turn was running, and processing ended it.
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
			if len(frames) != 2 || frames[0].Status.Type != appwire.ThreadStatusIdle || frames[1].Status.Type != appwire.ThreadStatusIdle {
				t.Fatalf("the abandoned carrier's frames = %+v, want the deferred replay and the settlement, both idle", frames)
			}
			if frames[0].EndedTurnID != wantEnded {
				t.Fatalf("a delayed end named turn %q, want its own input's %q", frames[0].EndedTurnID, wantEnded)
			}
			if frames[1].EndedTurnID != "turn_new" {
				t.Fatalf("the settlement named turn %q, want the cleared turn_new", frames[1].EndedTurnID)
			}
		})
	}
}

// A deferred end must not swallow the published turn's own end. Turn A's
// SESSION_END queues behind turn B's publication (the bridge defers it), B
// then ends without a SESSION_END of its own — a notification wake that
// found no work emits none, and the root discards execution-end statuses —
// so finishProcessing is B's only publisher. It settles naming the turn it
// cleared, beside the deferred frame that still names A: a client holding
// a snapshot that still runs B reads the deferred A frame as an older
// turn's end (and retires nothing, per the pre-cut rule) and the settlement
// as B's.
func TestDeferredSettlementNamesTheTurnItCleared(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_deferred_settlement")
	// Turn A runs: published, started, ended. The root discards the
	// execution end's own status the way it discards every boundary.
	srv.SetProcessingTurn("turn_a")
	srv.RecordAppEvent(threadEvent("th_deferred_settlement", events.ExecutionStartedData{TurnID: "turn_a"}))
	srv.RecordAppEvent(threadEvent("th_deferred_settlement", events.ExecutionEndedData{TurnID: "turn_a", Status: "completed"}))

	// Turn B publishes while A's end is still behind it in the drain; A's
	// SESSION_END defers behind B's pending start.
	srv.SetProcessingTurn("turn_b")
	BridgeEvent(srv, threadEvent("th_deferred_settlement", events.SessionEndData{Reason: "turn_failed", State: appwire.ThreadStatusIdle}), nil)
	// The window under test begins after B's publication and A's deferral.
	cursor := srv.appNotifier.CurrentSequence()

	// B's wake found no work: no SESSION_END of its own arrives, and
	// processing ends with the deferral still held.
	srv.SetProcessing(false)

	frames := statusFrames(t, srv, cursor, "th_deferred_settlement")
	if len(frames) != 2 {
		t.Fatalf("settlement published %+v frames, want A's deferred replay and B's settlement", frames)
	}
	if frames[0].Status.Type != appwire.ThreadStatusIdle || frames[0].EndedTurnID != "turn_a" {
		t.Fatalf("the deferred frame = %+v, want A's replay naming turn_a", frames[0])
	}
	if frames[1].Status.Type != appwire.ThreadStatusIdle || frames[1].EndedTurnID != "turn_b" {
		t.Fatalf("the settlement = %+v, want idle naming turn_b, the turn processing cleared", frames[1])
	}
	read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_deferred_settlement"})
	if read.Thread.Status.Type != appwire.ThreadStatusIdle || read.Thread.Evener.ActiveTurnID != "" {
		t.Fatalf("read after settlement = %q/%q, want idle with no running turn", read.Thread.Status.Type, read.Thread.Evener.ActiveTurnID)
	}
}

// An ACTIVE forecast is void once processing ends, wherever it came from.
// Turn A ends with a queued watch notification, so its SESSION_END states
// active (work will follow); the bridge defers that end behind turn B's
// publication, and B — a notification wake that discards the stale watch —
// finishes before its own execution start drains, so finishProcessing is
// B's only publisher. The deferred frame supplied the state, but a forecast
// is not a rest: the settlement must say idle, or thread/read keeps the
// session looking working (Send withheld) with nothing running. Genuine
// resting states a deferred frame states (awaiting, systemError) still
// stand, and the settlement keeps naming the cleared turn.
func TestDeferredActiveForecastNormalizesWhenProcessingEnds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deferred string
		want     string
	}{
		{"active forecast", appwire.ThreadStatusActive, appwire.ThreadStatusIdle},
		{"awaiting rest", appwire.ThreadStatusAwaiting, appwire.ThreadStatusAwaiting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", "th_forecast")
			srv.SetProcessingTurn("turn_a")
			srv.RecordAppEvent(threadEvent("th_forecast", events.ExecutionStartedData{TurnID: "turn_a"}))

			// B publishes before A's end drains; A's end defers behind B's
			// pending start. B's own execution start never drains (the wake
			// found no work), so the deferral holds to the end.
			srv.SetProcessingTurn("turn_b")
			BridgeEvent(srv, threadEvent("th_forecast", events.SessionEndData{Reason: "turn_failed", State: tc.deferred}), nil)
			cursor := srv.appNotifier.CurrentSequence()
			srv.SetProcessing(false)

			frames := statusFrames(t, srv, cursor, "th_forecast")
			if len(frames) != 2 {
				t.Fatalf("settlement published %+v frames, want A's deferred replay and B's settlement", frames)
			}
			// The deferred replay is A's own terminal word, kept as deferred.
			if frames[0].Status.Type != tc.deferred || frames[0].EndedTurnID != "turn_a" {
				t.Fatalf("the deferred frame = %+v, want A's replay stating %q", frames[0], tc.deferred)
			}
			// The settlement is the current word: the cleared turn's id and
			// the state that stands once processing has ended.
			if frames[1].Status.Type != tc.want {
				t.Fatalf("the settlement states %q, want %q: a forecast is void once processing ends", frames[1].Status.Type, tc.want)
			}
			if frames[1].EndedTurnID != "turn_b" {
				t.Fatalf("the settlement named turn %q, want the cleared turn_b", frames[1].EndedTurnID)
			}
			read := srv.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:th_forecast"})
			if read.Thread.Status.Type != tc.want || read.Thread.Evener.ActiveTurnID != "" {
				t.Fatalf("read after settlement = %q/%q, want %q with no running turn", read.Thread.Status.Type, read.Thread.Evener.ActiveTurnID, tc.want)
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
