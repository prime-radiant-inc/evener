//go:build serffuzz

package agent

import (
	"errors"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzFc2ShellTerminalDecision drives shellTerminalDecision — the pure
// terminal-classification core lifted out of jobManager.shellTerminal — over the
// full cross-product of stop request, runtime timeout, wait error, and exit code.
// It is the non-timing decision arm of runShell: the wrapper only adds the jm.mu
// read of the run's stop fields and the exit-code passthrough. Oracles (beyond
// never-panic):
//   - determinism: the same inputs yield the same (status, reason);
//   - precedence: an explicit stop dominates everything and passes its status and
//     reason through verbatim; below it, a timeout beats a wait error beats the
//     exit code;
//   - totality: the result status is always a terminal status, and (when no stop
//     is set) the reason is one of the fixed non-empty literals.
func FuzzFc2ShellTerminalDecision(f *testing.F) {
	f.Add(uint8(0), "", 0, false, false)              // clean exit
	f.Add(uint8(0), "", 1, false, false)              // nonzero exit
	f.Add(uint8(0), "", 0, true, false)               // runtime timeout
	f.Add(uint8(0), "", 0, false, true)               // wait error
	f.Add(uint8(1), "stopped_by_user", 0, true, true) // stop wins over all

	f.Fuzz(func(t *testing.T, stopSel uint8, stopReason string, exitCode int, timedOut, hasWaitErr bool) {
		// stopSel==0 means "no stop"; otherwise pick a terminal stop status, mirroring
		// what a real jobManager records in run.stopStatus.
		stopStatuses := []jobstore.Status{"", jobstore.StatusStopped, jobstore.StatusCancelled, jobstore.StatusFailed}
		stopStatus := stopStatuses[int(stopSel)%len(stopStatuses)]

		var waitErr error
		if hasWaitErr {
			waitErr = errors.New("wait failed")
		}

		status, reason := shellTerminalDecision(stopStatus, stopReason, exitCode, timedOut, waitErr)

		status2, reason2 := shellTerminalDecision(stopStatus, stopReason, exitCode, timedOut, waitErr)
		if status != status2 || reason != reason2 {
			t.Fatalf("non-deterministic: (%q,%q) vs (%q,%q)", status, reason, status2, reason2)
		}

		if stopStatus != "" {
			if status != stopStatus || reason != stopReason {
				t.Fatalf("stop request must pass through verbatim: got (%q,%q), want (%q,%q)", status, reason, stopStatus, stopReason)
			}
			return
		}

		// No stop: verify the precedence ladder and that reason is a fixed literal.
		var wantStatus jobstore.Status
		var wantReason string
		switch {
		case timedOut:
			wantStatus, wantReason = jobstore.StatusStopped, "run_timeout"
		case waitErr != nil:
			wantStatus, wantReason = jobstore.StatusFailed, "wait_failed"
		case exitCode == 0:
			wantStatus, wantReason = jobstore.StatusCompleted, "exit_zero"
		default:
			wantStatus, wantReason = jobstore.StatusFailed, "exit_nonzero"
		}
		if status != wantStatus || reason != wantReason {
			t.Fatalf("precedence: inputs(timedOut=%v,waitErr=%v,exit=%d) got (%q,%q), want (%q,%q)",
				timedOut, waitErr != nil, exitCode, status, reason, wantStatus, wantReason)
		}
		if !status.IsTerminal() {
			t.Fatalf("no-stop result status %q is not terminal", status)
		}
		if reason == "" {
			t.Fatalf("no-stop result reason is empty")
		}
	})
}