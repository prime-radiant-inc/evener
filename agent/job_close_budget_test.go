package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
)

// TestCloseRuntimeStateSettlesOnInheritedCloseBudget pins LIFE-04: the
// running-job wait must honor the shared close-cascade context passed in from
// the session close, rather than minting a fresh closeGrace window that a serial
// child teardown could renew once per child. Virtual time never advances, so
// only the inherited budget (a real context, cancelled here) can settle the
// wait; a regression that ignored ctx would hang on closeGrace instead.
func TestCloseRuntimeStateSettlesOnInheritedCloseBudget(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	clk := agenttest.NewFakeClock()
	jm.clock = clk
	// A long local grace: only the inherited deadline may settle the wait, so a
	// regression cannot pass by merely waiting out a short closeGrace.
	jm.closeGrace = time.Hour

	held := newDelayedSuccessStreamingExecutor()
	releaseHeld := sync.OnceFunc(func() { close(held.release) })
	t.Cleanup(releaseHeld)
	res := runShell(context.Background(), jm, held, shellArgs{Command: "held", Background: true})
	if res.JobID == "" || !res.RunningInBackground {
		t.Fatalf("res = %+v, want a held background job", res)
	}
	if _, live := shellDoneChannel(jm, res.JobID); !live {
		t.Fatalf("job %s is not in the running map before close", res.JobID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blockedBefore := clk.BlockedCount()
	resultCh := make(chan error, 1)
	go func() { resultCh <- jm.closeRuntimeState(ctx) }()
	// Wait until closeRuntimeState has parked on its closeGrace timer. Virtual
	// time never advances, so without the inherited context the wait can never
	// settle and the select below is the only escape.
	clk.BlockUntil(blockedBefore + 1)
	select {
	case err := <-resultCh:
		t.Fatalf("closeRuntimeState settled before the inherited budget expired: %v", err)
	default:
	}

	cancel()
	select {
	case err := <-resultCh:
		if err == nil || !strings.Contains(err.Error(), "timed out waiting for running jobs") {
			t.Fatalf("closeRuntimeState = %v, want inherited-budget abandon", err)
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: cancel propagates in microseconds; only a genuine hang reaches this
		releaseHeld()
		t.Fatal("closeRuntimeState did not settle on the inherited close budget")
	}

	jm.mu.Lock()
	_, stillRunning := jm.running[res.JobID]
	jm.mu.Unlock()
	if stillRunning {
		t.Fatalf("job %s was not abandoned when the inherited budget expired", res.JobID)
	}
}
