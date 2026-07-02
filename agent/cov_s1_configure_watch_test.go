package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// configureWatch rejects an empty target before any normalization.
func TestS1Cov_configureWatch_EmptyTarget(t *testing.T) {
	jm := newTestJM(t)
	if _, err := jm.configureWatch(watchArgs{Target: ""}); err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("empty target err = %v, want target-is-required", err)
	}
}

// A clear request against a not-found, non-terminal target returns the original
// target error rather than a no-op success.
func TestS1Cov_configureWatch_ClearMissingTargetReturnsError(t *testing.T) {
	jm := newTestJM(t)
	if _, err := jm.configureWatch(watchArgs{Target: "job_missing", Clear: true}); err == nil {
		t.Fatal("clear on a missing target must surface the target error")
	}
}

// A clear request against a valid session target with no active watch is an
// idempotent no-op success.
func TestS1Cov_configureWatch_ClearSessionTargetNoActiveWatch(t *testing.T) {
	jm := newTestJM(t)
	res, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, Clear: true})
	if err != nil {
		t.Fatalf("clear caller watch: %v", err)
	}
	if res.Watching {
		t.Fatalf("cleared watch must report Watching=false; got %+v", res)
	}
}

// A durable watch-registered append failure fails the install. (job.notification
// rather than a self-generated kind: under the old create-time forbid the
// communicate shape errored BEFORE the append, passing this test vacuously.)
func TestS1Cov_configureWatch_RegisterAppendFailure(t *testing.T) {
	jm := newTestJM(t)
	// The registry append prefers the batch seam; drop to the singular seam so
	// failAppendN (which wraps appendEvent) can inject the failure.
	jm.appendEvents = nil
	failAppendN(jm, jobstore.EventWatchRegistered, 1)
	if _, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}}); err == nil {
		t.Fatal("configureWatch must surface the watch-registered append failure")
	}
}
