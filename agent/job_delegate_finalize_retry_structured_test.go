package agent

import (
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// TestDelegateFinalizeRetryPreservesFirstCapturedStructuredResult reproduces
// kata nbb2: a delegate's terminal communicate() carries a substantive,
// schema-validated structured_result, but the finalize that should persist it
// fails to append durably (a transient jobstore error) and retries. If, in the
// window between the failed attempt and the retry, the delegate's own session
// processes another turn that overwrites its live communicate state (e.g. a
// driveSubagentNotificationTurn reacting to a late, unrelated notification,
// ending with a second, empty terminal communicate("late notification changed
// nothing")), the retry re-derives the job's structured_result from that now-
// stale-and-empty live state instead of the value it already captured on the
// first attempt — silently discarding the real finding.
//
// finalizeDelegateOnce's prepare() closure is invoked fresh on every retry
// (jm.finalizeWithRunMode calls prepare(run) again whenever run.terminal is
// still nil), and unlike the output-append and resumability-persist steps in
// the same closure (which are explicitly guarded so a retry does not redo
// them), the structured-result capture
// (`structured = childSess.CommunicateStructured()`) has no such guard: it
// unconditionally re-reads live session state on every attempt.
//
// RED at HEAD: rec.StructuredResult ends up {} (the second, empty payload)
// instead of the original {"finding":"xss bug","severity":"high"}.
func TestDelegateFinalizeRetryPreservesFirstCapturedStructuredResult(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)

	substantive := map[string]any{"severity": "high", "finding": "xss bug"}
	child.mu.Lock()
	child.comm = communicateResult{
		called:     true,
		text:       "found a high-severity xss bug",
		structured: substantive,
	}
	child.mu.Unlock()

	resultSchema := map[string]any{"type": "object"}
	sub := &subagent{
		id:     child.ID(),
		sess:   child,
		status: SubagentCompleted,
		result: "found a high-severity xss bug",
		done:   make(chan struct{}),
	}
	jobID, err := parent.jobManager.newJobID(parent.jobManager.sessionID)
	if err != nil {
		t.Fatalf("newJobID: %v", err)
	}
	run, err := parent.attachDelegateJobWithID(parent.jobManager, child.ID(), "task", sub, jobID, resultSchema, false)
	if err != nil {
		t.Fatalf("attachDelegateJobWithID: %v", err)
	}
	t.Cleanup(func() {
		parent.jobManager.abandonRunningJob(run.rec.JobID)
	})

	// Inject exactly one transient failure on the terminal EventJobFinished
	// append. When it fires, simulate the late-notification drive turn's
	// second, empty terminal communicate landing on the child session before
	// finalizeDelegateOnce's retry re-reads live communicate state.
	var failedOnce atomic.Bool
	orig := parent.jobManager.appendEvent
	parent.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished && failedOnce.CompareAndSwap(false, true) {
			child.mu.Lock()
			child.comm = communicateResult{
				called:     true,
				text:       "late notification changed nothing",
				structured: map[string]any{},
			}
			child.mu.Unlock()
			return errors.New("injected transient append failure")
		}
		return orig(e)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalizeDelegate: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)

	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	got, ok := rec.StructuredResult.(map[string]any)
	if !ok || got["severity"] != "high" || got["finding"] != "xss bug" {
		t.Fatalf("rec.StructuredResult = %#v, want the first-captured substantive result {severity:high, finding:xss bug} preserved across the finalize retry", rec.StructuredResult)
	}
}
