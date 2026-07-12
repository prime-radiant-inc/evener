//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzDelegateFinalizeReportProgram drives the deterministic terminal wait and
// reporting helpers used after a delegate has been attached. It deliberately
// stops at the fake clock, in-memory channel, and temporary job-store
// boundaries: no provider, network, process, shell, or Git command is used.
//
// Semantic oracles:
//   - finalization failures, cancellation, and timeout retain delegate identity;
//   - resumed waits retain both the target and prior job linkage;
//   - helper guard paths remain nil-safe and cancellation remains idempotent; and
//   - failed results are consistently classified as background delegate jobs.
func FuzzDelegateFinalizeReportProgram(f *testing.F) {
	for i := byte(0); i < 8; i++ {
		f.Add(i)
	}

	f.Fuzz(func(t *testing.T, op byte) {
		jm := newTestJM(t)
		clk := agenttest.NewFakeClock()
		jm.clock = clk
		jm.now = clk.Now
		run := &runningJob{
			rec: &jobstore.JobRecord{
				JobID:         "job_finalize",
				DelegateID:    "dlg_finalize",
				Type:          jobstore.JobDelegate,
				Status:        jobstore.StatusRunning,
				TranscriptRef: encodeRef("", "child_finalize"),
			},
			done: make(chan struct{}),
		}
		output, err := jm.openOutput(filepath.Join(jm.dir, "jobs", "job_finalize.log"), maxJobOutputRetentionBytes)
		if err != nil {
			t.Fatalf("open output: %v", err)
		}
		run.output = output
		t.Cleanup(func() { _ = output.Close() })

		switch op % 8 {
		case 0:
			finalizeErr := make(chan error, 1)
			finalizeErr <- errors.New("persist failed")
			res := waitForDelegateFinalization(context.Background(), nil, jm, run, finalizeErr)
			assertDelegateFinalizeFailure(t, res, "finalize_failed")
		case 1:
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			res := waitForDelegateFinalization(ctx, nil, jm, run, make(chan error))
			assertDelegateFinalizeFailure(t, res, "cancelled")
		case 2:
			result := make(chan delegateResult, 1)
			blocked := clk.BlockedCount()
			go func() {
				result <- waitForDelegateFinalization(context.Background(), nil, jm, run, make(chan error))
			}()
			clk.BlockUntil(blocked + 1)
			clk.Advance(delegateFinalizeWaitTimeout)
			assertDelegateFinalizeFailure(t, <-result, "finalize_timeout")
		case 3:
			finalizeErr := make(chan error, 1)
			finalizeErr <- errors.New("resume persist failed")
			res := waitForResumedDelegateResult(context.Background(), nil, jm, "dlg_finalize", "job_old", run, finalizeErr, 100)
			assertResumedDelegateFinalizeFailure(t, res, "finalize_failed")
		case 4:
			result := make(chan sendMessageResult, 1)
			blocked := clk.BlockedCount()
			go func() {
				result <- waitForResumedDelegateResult(context.Background(), nil, jm, "dlg_finalize", "job_old", run, make(chan error), 100)
			}()
			clk.BlockUntil(blocked + 1)
			clk.Advance(time.Duration(clampShellBlockTimeoutMS(100)) * time.Millisecond)
			res := <-result
			if !res.TimedOut || res.Status != jobstore.StatusRunning || res.Reason != "foreground_timeout" || res.Action != "started" {
				t.Fatalf("resumed timeout = %+v", res)
			}
			assertDelegateCoordinates(t, res.DelegateID, res.JobID, res.TranscriptRef)
		case 5:
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			res := waitForResumedDelegateResult(ctx, nil, jm, "dlg_finalize", "job_old", run, make(chan error), 100)
			assertResumedDelegateFinalizeFailure(t, res, "cancelled")
		case 6:
			s := &Session{}
			if delegateSandboxReportForSession(nil) != nil || s.isolatedDelegateWorktreeReport(nil) != nil {
				t.Fatal("nil report guards returned a report")
			}
			if s.isolatedDelegateWorktreeReport(&jobstore.DelegateRestoreDescriptor{Isolation: "worktree"}) != nil {
				t.Fatal("empty worktree path returned a report")
			}
			relinkDelegateChildToJob(nil, "job_ignored")
			cancelDelegateSub(nil)
		case 7:
			cancelled := false
			sub := &subagent{cancel: func() { cancelled = true }}
			cancelDelegateSub(sub)
			if !cancelled || !sub.cancelRequested {
				t.Fatalf("cancel state = (%v, %v), want true, true", cancelled, sub.cancelRequested)
			}
			uncloneable := make(chan int)
			if got := cloneDelegateResultSchema(uncloneable); got != uncloneable {
				t.Fatalf("uncloneable schema = %#v, want original channel", got)
			}
		}
	})
}

func assertDelegateFinalizeFailure(t *testing.T, res delegateResult, reason string) {
	t.Helper()
	if res.Status != jobstore.StatusFailed || res.Reason != reason || res.Err == nil || !res.RunningInBackground {
		t.Fatalf("finalize result = %+v, want failed/%s background result", res, reason)
	}
	assertDelegateCoordinates(t, res.DelegateID, res.JobID, res.TranscriptRef)
}

func assertResumedDelegateFinalizeFailure(t *testing.T, res sendMessageResult, reason string) {
	t.Helper()
	if res.Status != jobstore.StatusFailed || res.Reason != reason || res.Err == nil || res.Action != "started" || res.ResumedFromJobID != "job_old" {
		t.Fatalf("resumed finalize result = %+v, want failed/%s started result", res, reason)
	}
	assertDelegateCoordinates(t, res.DelegateID, res.JobID, res.TranscriptRef)
}

func assertDelegateCoordinates(t *testing.T, delegateID, jobID, transcriptRef string) {
	t.Helper()
	if delegateID != "dlg_finalize" || jobID != "job_finalize" || transcriptRef != encodeRef("", "child_finalize") {
		t.Fatalf("delegate coordinates = (%q, %q, %q)", delegateID, jobID, transcriptRef)
	}
}
