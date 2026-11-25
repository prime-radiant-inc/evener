package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// configureWatch rejects an empty target before any normalization.
func TestS1Cov_configureWatch_EmptyTarget(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	if _, err := jm.configureWatch(watchArgs{Target: ""}); err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("empty target err = %v, want target-is-required", err)
	}
}

// A clear request for a watch ID with no active watch is an idempotent no-op
// success.
func TestS1Cov_clearWatchByID_NoActiveWatch(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	res, err := jm.clearWatchByID("wch_missing")
	if err != nil {
		t.Fatalf("clear missing watch: %v", err)
	}
	if res.Watching {
		t.Fatalf("cleared watch must report Watching=false; got %+v", res)
	}
}

// A durable watch-registered append failure fails the install. (job.notification
// rather than a self-generated kind: under the old create-time forbid the
// communicate shape errored BEFORE the append, passing this test vacuously.)
func TestS1Cov_configureWatch_RegisterAppendFailure(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	// The registry append prefers the batch seam; drop to the singular seam so
	// failAppendN (which wraps appendEvent) can inject the failure.
	jm.appendEvents = nil
	failAppendN(jm, jobstore.EventWatchRegistered, 1)
	if _, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}}); err == nil {
		t.Fatal("configureWatch must surface the watch-registered append failure")
	}
}
