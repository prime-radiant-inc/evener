package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// A descendant's interrupted end can be queued behind a newer execution's own
// start. It must not settle the thread over the execution that now owns it,
// and the newer execution's end must still settle it.
func TestDescendantInterruptedEndDoesNotSupersedeRunningExecution(t *testing.T) {
	s := NewServer(ServerConfig{})
	t.Cleanup(s.Close)
	childPath := writeDelegateTranscript(t, "child", "child history")
	s.SetDescendantTranscriptPathFunc(func(threadID string) string {
		if threadID == "child" {
			return childPath
		}
		return ""
	})
	serveRootWithoutHistory(t, s, "root")

	emit := func(data events.EventData) {
		t.Helper()
		s.RecordDescendantAppEvent("root", threadEvent("child", data))
	}
	read := func() appwire.Thread {
		return s.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:child"}).Thread
	}

	emit(events.SessionStartData{})
	emit(events.ExecutionStartedData{TurnID: "t_new"})
	cursor := s.appNotifier.CurrentSequence()

	emit(events.SessionEndData{Interrupted: true, State: "idle", Reason: "interrupted"})

	thread := read()
	if thread.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("interrupted end on a descendant left status %q, want %q", thread.Status.Type, appwire.ThreadStatusActive)
	}
	if thread.Evener.ActiveTurnID != "t_new" {
		t.Fatalf("active turn after interrupted end = %q, want t_new", thread.Evener.ActiveTurnID)
	}
	if frames := statusNotificationsAfter(t, s, cursor, "child"); len(frames) != 0 {
		t.Fatalf("a superseded interrupted end published status changes: %+v", frames)
	}

	emit(events.ExecutionEndedData{TurnID: "t_new"})

	thread = read()
	if thread.Status.Type != appwire.ThreadStatusIdle || thread.Evener.ActiveTurnID != "" {
		t.Fatalf("the newer turn's end settled to status %q active %q, want idle and no active turn", thread.Status.Type, thread.Evener.ActiveTurnID)
	}
}

// A descendant with no transcript path has no history overlay, but its
// projector still tracks the running execution. A superseded interrupted end
// must not settle that execution there either.
func TestDescendantInterruptedEndWithoutHistoryDoesNotSupersedeRunningExecution(t *testing.T) {
	s := NewServer(ServerConfig{})
	t.Cleanup(s.Close)
	serveRootWithoutHistory(t, s, "root")

	emit := func(data events.EventData) {
		t.Helper()
		s.RecordDescendantAppEvent("root", threadEvent("child", data))
	}
	read := func() appwire.Thread {
		return s.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:child"}).Thread
	}

	emit(events.SessionStartData{})
	emit(events.ExecutionStartedData{TurnID: "t_new"})
	cursor := s.appNotifier.CurrentSequence()

	emit(events.SessionEndData{Interrupted: true, State: "idle", Reason: "interrupted"})

	if thread := read(); thread.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("interrupted end on a historyless descendant left status %q, want %q", thread.Status.Type, appwire.ThreadStatusActive)
	}
	if frames := statusNotificationsAfter(t, s, cursor, "child"); len(frames) != 0 {
		t.Fatalf("a superseded interrupted end published status changes: %+v", frames)
	}

	emit(events.ExecutionEndedData{TurnID: "t_new"})

	if thread := read(); thread.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("the newer turn's end left status %q, want idle", thread.Status.Type)
	}
	frames := statusNotificationsAfter(t, s, cursor, "child")
	if len(frames) != 1 || frames[0].Status.Type != appwire.ThreadStatusIdle || frames[0].EndedTurnID != "t_new" {
		t.Fatalf("the newer turn's end published %+v, want one idle change naming t_new", frames)
	}
}

// A session end that is not an interrupt settles the execution the projector
// holds and names it; only an interrupted end whose own execution end was
// already discarded is stale.
func TestDescendantSessionEndSettlesRunningExecution(t *testing.T) {
	s := NewServer(ServerConfig{})
	t.Cleanup(s.Close)
	serveRootWithoutHistory(t, s, "root")

	emit := func(data events.EventData) {
		t.Helper()
		s.RecordDescendantAppEvent("root", threadEvent("child", data))
	}
	read := func() appwire.Thread {
		return s.appThreadReadSnapshot(appwire.ThreadReadParams{Ref: "local:child"}).Thread
	}

	emit(events.SessionStartData{})
	emit(events.ExecutionStartedData{TurnID: "t_run"})
	cursor := s.appNotifier.CurrentSequence()

	emit(events.SessionEndData{State: "idle", Reason: "input_complete"})

	if thread := read(); thread.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("a completed input left status %q, want idle", thread.Status.Type)
	}
	frames := statusNotificationsAfter(t, s, cursor, "child")
	if len(frames) != 1 || frames[0].Status.Type != appwire.ThreadStatusIdle || frames[0].EndedTurnID != "t_run" {
		t.Fatalf("a completed input published %+v, want one idle change naming t_run", frames)
	}
}
