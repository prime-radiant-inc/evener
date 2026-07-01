package agent

import (
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// An output_match-only watch on a non-running target must load the store to
// decide terminal catch-up eligibility. When that load fails (corrupt log), the
// store error propagates out of configureWatch rather than being swallowed.
func TestW2Watch_configureWatchOutputMatchStoreLoadError(t *testing.T) {
	jm := newTestJM(t)
	// createShell forces the jobs.jsonl to exist before we append garbage.
	if _, err := jm.createShell(createShellOpts{Command: "x"}); err != nil {
		t.Fatalf("createShell: %v", err)
	}
	s1cov_corruptJobLog(t, filepath.Join(jm.dir, "jobs.jsonl"))

	_, err := jm.configureWatch(watchArgs{Target: "job_absent", OutputMatch: "ready"})
	if err == nil {
		t.Fatal("configureWatch on corrupt store succeeded, want the store load error")
	}
}

// An output_match-only watch on an already-terminal target is served as a
// one-shot catch-up, but a bad send target must still be rejected: the terminal
// branch re-validates the send target and returns its error.
func TestW2Watch_configureWatchTerminalCatchupRejectsBadSendTarget(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	jobID := terminalShellWithOutput(t, jm, "ready\n")

	_, err := jm.configureWatch(watchArgs{
		Target:      jobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_not_a_delegate", Message: "m"},
	})
	if err == nil {
		t.Fatal("terminal catch-up with a job_ send target succeeded, want send-target rejection")
	}
}

// A brand-new session watch install must durably append its WatchRegistered
// event; when that append fails the install aborts with the error and the watch
// is not added to the live set.
func TestW2Watch_configureWatchRegisteredAppendError(t *testing.T) {
	jm := newTestJM(t)
	realAppendEvents := jm.appendEvents
	appendErr := errors.New("registered append failed")
	jm.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventWatchRegistered {
				return appendErr
			}
		}
		return realAppendEvents(events)
	}

	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
	}); !errors.Is(err, appendErr) {
		t.Fatalf("configureWatch error = %v, want the registered append failure", err)
	}

	jm.mu.Lock()
	installed := len(jm.watches)
	jm.mu.Unlock()
	if installed != 0 {
		t.Fatalf("watch installed despite append failure: %d live watches", installed)
	}
}
