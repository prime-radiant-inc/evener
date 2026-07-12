//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
)

func FuzzJobDelegateExactTailFinalize(f *testing.F) {
	f.Add(false, "retry complete")
	f.Add(true, "")

	f.Fuzz(func(t *testing.T, armNotification bool, result string) {
		parent := newTestSession(t)
		child := newTestSession(t)
		jm := parent.jobManager
		clk := agenttest.NewFakeClock()
		jm.clock = clk
		jm.now = clk.Now

		sub := completedDelegateSubagent(child, result)
		parent.subagents.track(sub)
		run, err := parent.attachDelegateJob(jm, child.ID(), "retry finalization", sub)
		if err != nil {
			t.Fatalf("attach delegate: %v", err)
		}
		if err := run.output.Close(); err != nil {
			t.Fatalf("close output: %v", err)
		}

		done := make(chan error, 1)
		go func() {
			done <- parent.finalizeDelegateWithNotification(run.rec.JobID, child.ID(), sub, armNotification)
		}()
		clk.BlockUntil(1)

		reopened, err := jobstore.OpenOutput(run.rec.OutputPath, 0)
		if err != nil {
			t.Fatalf("reopen output: %v", err)
		}
		jm.mu.Lock()
		run.output = reopened
		jm.mu.Unlock()
		t.Cleanup(func() { _ = reopened.Close() })

		clk.Advance(delegateFinalizeRetryDelay)
		if err := <-done; err != nil {
			t.Fatalf("finalize after retry: %v", err)
		}
		waitForShellDone(t, jm, run.rec.JobID)
		if rec := loadShellRecord(t, jm, run.rec.JobID); rec.Status != jobstore.StatusCompleted {
			t.Fatalf("status = %q, want completed", rec.Status)
		}
	})
}
