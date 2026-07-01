package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// TestW2Conc_RunShellForegroundBlockTimeoutCommitFails pins the foreground
// block-timeout promotion arm where committing the delayed job to durable
// background fails: the block timer fires while the process is still running
// (no runtime timeout), commitDelayedShell's durable EventJobStarted append is
// injected to fail, and runShell must signal the process and return
// failed/start_failed. Virtual time (jm.clock FakeClock) fires the block timer
// deterministically while the real subprocess stays alive.
func TestW2Conc_RunShellForegroundBlockTimeoutCommitFails(t *testing.T) {
	jm, se := newShellTestRig(t)
	clk := agenttest.NewFakeClock()
	jm.clock = clk

	// Fail the first durable EventJobStarted append; for a foreground delayed
	// shell that first append happens inside commitDelayedShell.
	failAppendN(jm, jobstore.EventJobStarted, 1)

	resCh := make(chan shellResult, 1)
	go func() {
		resCh <- runShell(context.Background(), jm, se, shellArgs{
			Command:        "sleep 30",
			BlockTimeoutMS: 1000,
		})
	}()

	clk.BlockUntil(1) // block timer armed
	clk.Advance(time.Second)

	select {
	case res := <-resCh:
		if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" {
			t.Fatalf("res = %+v, want failed/start_failed on commit failure", res)
		}
		if res.JobID != "" || res.RunningInBackground {
			t.Fatalf("res = %+v, want no durable/background job on commit failure", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runShell did not return after the block timer fired")
	}
}
