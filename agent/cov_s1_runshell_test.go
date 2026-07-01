package agent

import (
	"context"
	"io"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// s1cov_instantExitExecutor completes synchronously so the foreground runShell
// path settles through the returned settle closure.
type s1cov_instantExitExecutor struct{}

func (s1cov_instantExitExecutor) StreamCommand(_ context.Context, _ string, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	_, _ = out.Write([]byte("done output\n"))
	return &execenv.StreamHandle{
		Wait:   func() (int, error) { return 0, nil },
		Signal: func() {},
	}, nil
}

// A closing job manager can't stand up the delayed shell, so runShell fails to
// start.
func TestS1Cov_runShell_ClosingFailsToStart(t *testing.T) {
	jm := newTestJM(t)
	jm.mu.Lock()
	jm.closing = true
	jm.mu.Unlock()
	res := runShell(context.Background(), jm, s1cov_instantExitExecutor{}, shellArgs{Command: "x", BlockTimeoutMS: 5000})
	if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" {
		t.Fatalf("res = %+v, want failed/start_failed", res)
	}
}

// The foreground settle closure discards the job when its deferred durable start
// commit fails.
func TestS1Cov_runShell_SettleCommitFailureDiscards(t *testing.T) {
	jm := newTestJM(t)
	res := runShell(context.Background(), jm, s1cov_instantExitExecutor{}, shellArgs{Command: "x", BlockTimeoutMS: 5000})
	if res.settle == nil {
		t.Fatalf("foreground completion must return a settle closure; res = %+v", res)
	}
	// The deferred start-event commit fails: settle keeps=true → commit error →
	// discard → empty job id.
	failAppendN(jm, jobstore.EventJobStarted, 1)
	if id := res.settle(true); id != "" {
		t.Fatalf("settle after commit failure = %q, want empty", id)
	}
}

// The foreground settle closure still returns the job id when the terminal write
// fails, handing the retry to a durable finalize goroutine.
func TestS1Cov_runShell_SettleFinalizeFailureRetries(t *testing.T) {
	jm := newTestJM(t)
	res := runShell(context.Background(), jm, s1cov_instantExitExecutor{}, shellArgs{Command: "x", BlockTimeoutMS: 5000})
	if res.settle == nil {
		t.Fatalf("foreground completion must return a settle closure; res = %+v", res)
	}
	// Commit (start event) succeeds; the terminal finalize append fails once, so
	// settle spawns the durable retry and still reports the kept job id.
	failAppendN(jm, jobstore.EventJobFinished, 1)
	id := res.settle(true)
	if id == "" {
		t.Fatal("settle after finalize failure must still report the kept job id")
	}
	// Let the durable-retry goroutine reach quiescence on the healed seam.
	waitForShellDone(t, jm, id)
}
