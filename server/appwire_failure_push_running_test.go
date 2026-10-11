package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// A long execution accumulates failures a watching client cannot see until the
// execution ends -- kata 895d's live measurement: a single turn with 3 shell
// failures spanned ~26s wall-clock. So while an execution is published, a
// change in the running failure count is pushed at once as the execution's own
// thread/status/changed{active}, which carries the count; an unchanged count
// pushes nothing, so a turn with no failures adds no notifications (the volume
// objection the kata itself raised).
func TestRunningExecutionPushesTheFailureCountWhenItChanges(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishFailureCount(srv, 0)
	startExecution(srv, "th_1", "t_go")
	toolEnded := func() {
		srv.RecordAppEvent(threadEvent("th_1", events.ToolCallEndData{ToolName: "shell", CallID: "call"}))
	}

	toolEnded() // unchanged since the execution's own status
	publishFailureCount(srv, 1)
	toolEnded()
	toolEnded() // unchanged since the last push

	statuses := statusNotifications(t, srv, "th_1")
	if len(statuses) != 2 {
		t.Fatalf("statuses = %+v, want the execution's active status and one push for the moved count", statuses)
	}
	if first := statuses[0]; first.FailedToolCalls == nil || *first.FailedToolCalls != 0 {
		t.Fatalf("execution status FailedToolCalls = %v, want a measured 0", first.FailedToolCalls)
	}
	pushed := statuses[1]
	if pushed.Status.Type != appwire.ThreadStatusActive || pushed.ActiveTurnID != "t_go" {
		t.Fatalf("pushed status = %+v, want active naming the running t_go", pushed)
	}
	if pushed.FailedToolCalls == nil || *pushed.FailedToolCalls != 1 {
		t.Fatalf("pushed FailedToolCalls = %v, want 1 (the count just moved)", pushed.FailedToolCalls)
	}
}

// With no execution published the count waits for the next status transition,
// as it always has.
func TestAnIdleThreadDoesNotPushTheFailureCount(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishFailureCount(srv, 2)
	srv.RecordAppEvent(threadEvent("th_1", events.ToolCallEndData{ToolName: "shell", CallID: "call"}))
	if statuses := statusNotifications(t, srv, "th_1"); len(statuses) != 0 {
		t.Fatalf("idle thread pushed %+v, want nothing", statuses)
	}
}

func TestRunningExecutionOmitsAnUnmeasuredFailureCount(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.failedToolCalls = 0; e.failuresMeasured = false })
	startExecution(srv, "th_1", "t_go")
	srv.RecordAppEvent(threadEvent("th_1", events.ToolCallEndData{ToolName: "shell", CallID: "call"}))

	statuses := statusNotifications(t, srv, "th_1")
	if len(statuses) != 1 {
		t.Fatalf("statuses = %+v, want only the execution's own", statuses)
	}
	if statuses[0].FailedToolCalls != nil {
		t.Fatalf("status carried %d for an unmeasured session, want absent", *statuses[0].FailedToolCalls)
	}
}

func TestRunningExecutionOmitsTheCountOnADaemonThatNeverWiredIt(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	startExecution(srv, "th_1", "t_go")
	srv.RecordAppEvent(threadEvent("th_1", events.ToolCallEndData{ToolName: "shell", CallID: "call"}))

	statuses := statusNotifications(t, srv, "th_1")
	if len(statuses) != 1 || statuses[0].FailedToolCalls != nil {
		t.Fatalf("statuses = %+v, want only the execution's own, with no count", statuses)
	}
}

// A new identity starts with no memory of the previous session's last pushed
// count: its own figure is compared with what its own statuses carried.
func TestTheLastPushedFailureCountResetsOnNewIdentity(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	publishFailureCount(srv, 3)
	startExecution(srv, "th_1", "t_one")

	srv.SetAppIdentity("local", "th_2")
	srv.mu.RLock()
	remembered := srv.appPushedFailedToolCalls
	srv.mu.RUnlock()
	if remembered != nil {
		t.Fatalf("the replaced session's pushed count %d survived the identity change", *remembered)
	}
	publishFailureCount(srv, 3)
	startExecution(srv, "th_2", "t_two")
	srv.RecordAppEvent(threadEvent("th_2", events.ToolCallEndData{ToolName: "shell", CallID: "call"}))
	publishFailureCount(srv, 4)
	srv.RecordAppEvent(threadEvent("th_2", events.ToolCallEndData{ToolName: "shell", CallID: "call"}))

	statuses := statusNotifications(t, srv, "th_2")
	if len(statuses) != 2 {
		t.Fatalf("th_2 statuses = %+v, want its execution's status and one push", statuses)
	}
	if statuses[0].FailedToolCalls == nil || *statuses[0].FailedToolCalls != 3 {
		t.Fatalf("th_2's execution status FailedToolCalls = %v, want its own 3", statuses[0].FailedToolCalls)
	}
	if statuses[1].FailedToolCalls == nil || *statuses[1].FailedToolCalls != 4 {
		t.Fatalf("th_2's push FailedToolCalls = %v, want 4", statuses[1].FailedToolCalls)
	}
}
