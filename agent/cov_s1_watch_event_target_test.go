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
	if !watchEventMatchesTarget("caller", events.JobStartedData{JobID: "job_x"}) {
		t.Fatal("session target must match any event")
	}
	// Concrete targets match only the same job id.
	if !watchEventMatchesTarget("job_x", events.JobStartedData{JobID: "job_x"}) {
		t.Fatal("start event for the target job must match")
	}
	if watchEventMatchesTarget("job_x", events.JobFinishedData{JobID: "job_y"}) {
		t.Fatal("finish event for a different job must not match")
	}
	// Non job-lifecycle events default to matching a concrete target.
	if !watchEventMatchesTarget("job_x", events.CommunicateData{}) {
		t.Fatal("non-lifecycle event defaults to match")
	}
}
