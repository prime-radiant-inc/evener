package agent

import (
	"testing"

	"primeradiant.com/serf/agent/events"
)

func TestS1Cov_watchEventWatchedIdentity(t *testing.T) {
	// A concrete target resolves to itself regardless of data.
	if got := watchEventWatchedIdentity("job_x", events.JobStartedData{JobID: "job_y"}); got != "job_x" {
		t.Fatalf("concrete target identity = %q, want job_x", got)
	}
	// The wildcard target resolves to the event's job id for start/finish events.
	if got := watchEventWatchedIdentity("*", events.JobStartedData{JobID: "job_s"}); got != "job_s" {
		t.Fatalf("wildcard start identity = %q, want job_s", got)
	}
	if got := watchEventWatchedIdentity("*", events.JobFinishedData{JobID: "job_f"}); got != "job_f" {
		t.Fatalf("wildcard finish identity = %q, want job_f", got)
	}
	// The wildcard target on any other event falls back to the target itself.
	if got := watchEventWatchedIdentity("*", events.CommunicateData{}); got != "*" {
		t.Fatalf("wildcard other-event identity = %q, want *", got)
	}
}

func TestS1Cov_watchEventMatchesTarget(t *testing.T) {
	// Session targets always match.
	if !watchEventMatchesTarget("caller", "", events.SessionEvent{Data: events.JobStartedData{JobID: "job_x"}}) {
		t.Fatal("session target must match any event")
	}
	// Concrete targets match only the same job id.
	if !watchEventMatchesTarget("job_x", "", events.SessionEvent{Data: events.JobStartedData{JobID: "job_x"}}) {
		t.Fatal("start event for the target job must match")
	}
	if watchEventMatchesTarget("job_x", "", events.SessionEvent{Data: events.JobFinishedData{JobID: "job_y"}}) {
		t.Fatal("finish event for a different job must not match")
	}
	// Non job-lifecycle events are scoped by originating session: only the
	// watched job's child session matches; watcher-origin and identity-less
	// events do not (they would echo the watcher back at itself).
	if !watchEventMatchesTarget("job_x", "sess_child", events.SessionEvent{SessionID: "sess_child", Data: events.CommunicateData{}}) {
		t.Fatal("watched job's session event must match")
	}
	if watchEventMatchesTarget("job_x", "sess_child", events.SessionEvent{SessionID: "sess_watcher", Data: events.CommunicateData{}}) {
		t.Fatal("watcher-origin non-lifecycle event must not match")
	}
	if watchEventMatchesTarget("job_x", "", events.SessionEvent{Data: events.CommunicateData{}}) {
		t.Fatal("identity-less non-lifecycle event must not match")
	}
}
