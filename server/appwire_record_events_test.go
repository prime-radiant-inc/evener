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
