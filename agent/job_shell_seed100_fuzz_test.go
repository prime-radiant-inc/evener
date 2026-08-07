//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzCovJobShellSeed100 is a deterministic coverage seed for job_shell.go.
// It composes the behavioral shell tests and then drives the small defensive
// states that are awkward to reach through a process launcher.
//
// Registry: native:agent:.:FuzzCovJobShellSeed100::job_shell.go
func FuzzCovJobShellSeed100(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		t.Run("foreground_ephemeral", TestRunShellForegroundEphemeral)
		t.Run("foreground_full_output", TestRunShellForegroundEphemeralReturnsFullOutput)
		t.Run("foreground_wait_error", TestRunShellForegroundWaitErrorFailsJob)
		t.Run("background_wait_error", TestRunShellBackgroundWaitErrorFinalizesFailed)
		t.Run("close_during_start", TestRunShellBackgroundCloseDuringStartDoesNotCommitJob)
		t.Run("commit_after_close", TestCommitDelayedShellAfterCloseAbandonmentFails)
		t.Run("discard_after_abandon", TestDiscardDelayedShellAfterAbandonDoesNotPanic)
		t.Run("foreground_promotion", TestRunShellPromotesOnTimeout)
		t.Run("foreground_runtime_timeout", TestRunShellForegroundMaxRuntimeCreatesDurableStoppedJob)
		t.Run("foreground_finalize_retry", TestRunShellForegroundMaxRuntimeFinalizerFailureConvergesDetached)
		t.Run("background", TestRunShellBackgroundReturnsImmediately)
		t.Run("background_working_dir", TestRunShellBackgroundRecordsLaunchWorkingDirForLiveWorkGuard)
		t.Run("stop_precedence", TestRunShellBackgroundStopWinsOverLaterRuntimeTimeout)
		t.Run("background_detaches_context", TestRunShellBackgroundSurvivesToolContextCancellation)
		t.Run("foreground_cancel", TestRunShellForegroundCancelsBeforePromotion)
		t.Run("start_context_precancel", TestStartOnlyContextSeesPreCanceledParent)
		t.Run("start_context_detach", TestStartOnlyContextIgnoresCancellationAfterDetach)
		t.Run("detached_finalize_retry", TestRunShellDetachedFinalizerRetriesUntilDurable)

		t.Run("defensive_states", covJobShellDefensiveStates)
	})
}

func covJobShellDefensiveStates(t *testing.T) {
	jm := newTestJM(t)
	defer func() { _ = jm.close() }()

	// nil context and a launcher failure exercise start-only context creation
	// without depending on a real process.
	res := runShell(nil, jm, &shfz_fakeExecutor{startErr: errFuzzShellStart}, shellArgs{Command: "x"})
	if res.Reason != "start_failed" {
		t.Fatalf("nil-context start failure = %+v", res)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res = runShell(ctx, jm, &shfz_fakeExecutor{}, shellArgs{Command: "x"})
	if res.Reason != "start_failed" {
		t.Fatalf("pre-cancelled start = %+v", res)
	}

	origCreate := jm.createOutput
	jm.createOutput = func(string, int64) (*jobstore.OutputStore, error) {
		return nil, errors.New("open output")
	}
	res = runShell(context.Background(), jm, &shfz_fakeExecutor{}, shellArgs{Command: "x"})
	jm.createOutput = origCreate
	if res.Reason != "start_failed" {
		t.Fatalf("output-open failure = %+v", res)
	}

	for _, timeout := range []int{0, -1, minShellBlockTimeoutMS, maxShellBlockTimeoutMS + 1} {
		_ = clampShellBlockTimeoutMS(timeout)
	}

	startCtx, _ := newStartOnlyContext(nil)
	_, _ = startCtx.Deadline()
	_ = startCtx.Value(struct{}{})
	startCtx.cancel(nil)
	startCtx.cancel(context.Canceled)
	parent, parentCancel := context.WithCancel(context.Background())
	parentCtx, _ := newStartOnlyContext(parent)
	parentCancel()
	<-parentCtx.Done()
	detachedCtx, detach := newStartOnlyContext(context.Background())
	detach()
	detachedCtx.cancel(context.Canceled)

	run, err := jm.newDelayedShell(shellArgs{Command: "closed-output"})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.output.Close(); err != nil {
		t.Fatal(err)
	}
	if err := jobstore.RemoveOutputArtifacts(run.rec.OutputPath); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fullOutput(run.output); err == nil {
		t.Fatal("fullOutput on removed store succeeded")
	}
	jm.discardDelayedShell(run)

	empty, err := jm.newDelayedShell(shellArgs{Command: "empty-output"})
	if err != nil {
		t.Fatal(err)
	}
	if got, total, _, err := fullOutput(empty.output); err != nil || got != "" || total != 0 {
		t.Fatalf("empty fullOutput = %q/%d/%v", got, total, err)
	}
	jm.discardDelayedShell(empty)

	run, err = jm.newDelayedShell(shellArgs{Command: "idempotent-commit"})
	if err != nil {
		t.Fatal(err)
	}
	if err := jm.commitDelayedShell(run); err != nil {
		t.Fatal(err)
	}
	if err := jm.commitDelayedShell(run); err != nil {
		t.Fatal(err)
	}
	jm.discardDelayedShell(run)
	if err := jm.commitDelayedShell(run); err != nil {
		t.Fatalf("commit of absent non-closing run: %v", err)
	}

	closing := newTestJM(t)
	closing.mu.Lock()
	closing.closing = true
	closing.mu.Unlock()
	if _, err := closing.newDelayedShell(shellArgs{Command: "closing"}); !errors.Is(err, errJobManagerClosing) {
		t.Fatalf("newDelayedShell while closing: %v", err)
	}
	_ = closing.close()

	for _, wait := range []shellWaitResult{{exitCode: 0}, {exitCode: 9}, {exitCode: 1, err: errors.New("wait")}} {
		r, err := jm.newDelayedShell(shellArgs{Command: "settle"})
		if err != nil {
			t.Fatal(err)
		}
		status, reason, code := jm.shellTerminal(r, wait.exitCode, false, wait.err)
		if err := jm.commitDelayedShell(r); err != nil {
			t.Fatal(err)
		}
		if err := jm.finalizeKeptSync(r, status, reason, code); err != nil {
			t.Fatal(err)
		}
	}

	settleJM := newTestJM(t)
	settled := runShell(context.Background(), settleJM, &shfz_fakeExecutor{exitCode: 0}, shellArgs{Command: "settle"})
	if settled.settle == nil || settled.settle(true) == "" {
		t.Fatal("healthy foreground settle did not keep job")
	}
	failedSettle := runShell(context.Background(), settleJM, &shfz_fakeExecutor{exitCode: 0}, shellArgs{Command: "settle-fail"})
	failAppendN(settleJM, jobstore.EventJobStarted, 1)
	if failedSettle.settle == nil || failedSettle.settle(true) != "" {
		t.Fatal("faulted foreground settle kept job")
	}
	retriedSettle := runShell(context.Background(), settleJM, &shfz_fakeExecutor{exitCode: 0}, shellArgs{Command: "settle-retry"})
	failAppendN(settleJM, jobstore.EventJobFinished, 1)
	var retriedID string
	if retriedSettle.settle != nil {
		retriedID = retriedSettle.settle(true)
	}
	if retriedID == "" {
		t.Fatal("retrying foreground settle lost job")
	}
	waitForShellDone(t, settleJM, retriedID)
	_ = settleJM.close()

	// Direct timeout finalization keeps fault classification deterministic.
	for _, mode := range []string{"append", "forward", "terminal"} {
		timeoutJM := newTestJM(t)
		if mode == "append" {
			failAppendN(timeoutJM, jobstore.EventJobStarted, 1)
		} else {
			timeoutJM.parentJobID = "parent"
			timeoutJM.forward = func(jobstore.Event) error { return errors.New("forward") }
			if mode == "terminal" {
				failAppendN(timeoutJM, jobstore.EventJobFinished, 1)
			}
		}
		r, err := timeoutJM.newDelayedShell(shellArgs{Command: "timeout-fault"})
		if err != nil {
			t.Fatal(err)
		}
		got := timeoutJM.finishForegroundRuntimeTimeout(r, shellWaitResult{exitCode: 143})
		if got.Reason != "start_failed" {
			t.Fatalf("timeout %s result = %+v", mode, got)
		}
		if mode == "terminal" {
			timeoutJM.discardDelayedShell(r)
		}
		_ = timeoutJM.close()
	}

	backgroundFaultJM := newTestJM(t)
	backgroundFaultJM.parentJobID = "parent"
	backgroundFaultJM.forward = func(jobstore.Event) error { return errors.New("forward") }
	failAppendN(backgroundFaultJM, jobstore.EventJobFinished, 1)
	backgroundFault := runShell(context.Background(), backgroundFaultJM, &shfz_fakeExecutor{}, shellArgs{Command: "background-terminal", Background: true})
	if backgroundFault.Reason != "start_failed" {
		t.Fatalf("background terminal-forward fault = %+v", backgroundFault)
	}
	_ = backgroundFaultJM.close()

	for _, mode := range []string{"append", "forward", "terminal"} {
		promotionJM := newTestJM(t)
		promotionClock := agenttest.NewFakeClock()
		promotionJM.clock = promotionClock
		if mode == "append" {
			failAppendN(promotionJM, jobstore.EventJobStarted, 1)
		} else {
			promotionJM.parentJobID = "parent"
			promotionJM.forward = func(jobstore.Event) error { return errors.New("forward") }
			if mode == "terminal" {
				failAppendN(promotionJM, jobstore.EventJobFinished, 1)
			}
		}
		exec := newSignalCompletesStreamingExecutor()
		result := make(chan shellResult, 1)
		go func() {
			result <- runShell(context.Background(), promotionJM, exec, shellArgs{Command: mode, BlockTimeoutMS: 1})
		}()
		promotionClock.BlockUntil(1)
		promotionClock.Advance(time.Second)
		if got := <-result; got.Reason != "start_failed" {
			t.Fatalf("promotion %s = %+v", mode, got)
		}
		_ = promotionJM.close()
	}

	// A fake-clock runtime timeout covers the timer goroutine itself.
	runtimeJM := newTestJM(t)
	runtimeJM.clock = agenttest.NewFakeClock()
	runtimeExec := newSignalCompletesStreamingExecutor()
	resultCh := make(chan shellResult, 1)
	go func() {
		resultCh <- runShell(context.Background(), runtimeJM, runtimeExec, shellArgs{Command: "runtime", MaxRuntimeMS: 1, BlockTimeoutMS: 5000})
	}()
	runtimeJM.clock.(*agenttest.FakeClock).BlockUntil(2)
	runtimeJM.clock.(*agenttest.FakeClock).Advance(time.Millisecond)
	res = <-resultCh
	if res.Reason != "run_timeout" {
		t.Fatalf("runtime timeout = %+v", res)
	}
	_ = runtimeJM.close()

	// Resolve both select/timeout races through manager-local test hooks.
	waitRaceJM := newTestJM(t)
	waitRaceJM.shellBeforeWaitTimeoutDecision = func(timedOut *atomic.Bool) { timedOut.Store(true) }
	waitRace := runShell(context.Background(), waitRaceJM, &shfz_fakeExecutor{exitCode: 143}, shellArgs{Command: "wait-race"})
	if waitRace.Reason != "run_timeout" {
		t.Fatalf("wait timeout race = %+v", waitRace)
	}
	_ = waitRaceJM.close()

	blockRaceJM := newTestJM(t)
	blockClock := agenttest.NewFakeClock()
	blockRaceJM.clock = blockClock
	blockExec := newSignalCompletesStreamingExecutor()
	blockRaceJM.shellBeforeBlockTimeoutDecision = func(timedOut *atomic.Bool) {
		timedOut.Store(true)
		blockExec.once.Do(func() { close(blockExec.done) })
	}
	blockResult := make(chan shellResult, 1)
	go func() {
		blockResult <- runShell(context.Background(), blockRaceJM, blockExec, shellArgs{Command: "block-race", BlockTimeoutMS: 1})
	}()
	blockClock.BlockUntil(1)
	blockClock.Advance(time.Second)
	if got := <-blockResult; got.Reason != "run_timeout" {
		t.Fatalf("block timeout race = %+v", got)
	}
	_ = blockRaceJM.close()

	// Drive both start-forward classifications through the real commit path.
	for _, terminalFails := range []bool{false, true} {
		forwardJM := newTestJM(t)
		forwardJM.parentJobID = "parent"
		forwardJM.forward = func(jobstore.Event) error { return errors.New("forward") }
		if terminalFails {
			failAppendN(forwardJM, jobstore.EventJobFinished, 1)
		}
		r, err := forwardJM.newDelayedShell(shellArgs{Command: "forward-failure"})
		if err != nil {
			t.Fatal(err)
		}
		err = forwardJM.commitDelayedShell(r)
		if terminalFails != errors.Is(err, errDelayedShellStartForwardTerminalFailed) {
			t.Fatalf("terminalFails=%v commit error=%v", terminalFails, err)
		}
		if terminalFails {
			forwardJM.discardDelayedShell(r)
		}
		_ = forwardJM.close()
	}

	// The retry loop's store-closed exit is a distinct durable shutdown path.
	retryJM := newTestJM(t)
	r, err := retryJM.newDelayedShell(shellArgs{Command: "closed-retry"})
	if err != nil {
		t.Fatal(err)
	}
	_ = retryJM.store.Close()
	retryJM.finalizeKeptSyncUntilDurable(r, jobstore.StatusFailed, "closed", nil)
	retryJM.discardDelayedShell(r)

	retrySuccessJM := newTestJM(t)
	r, err = retrySuccessJM.newDelayedShell(shellArgs{Command: "retry-success"})
	if err != nil {
		t.Fatal(err)
	}
	if err := retrySuccessJM.commitDelayedShell(r); err != nil {
		t.Fatal(err)
	}
	failAppendN(retrySuccessJM, jobstore.EventJobFinished, 1)
	retrySuccessJM.finalizeKeptSyncUntilDurable(r, jobstore.StatusFailed, "retry", nil)
	_ = retrySuccessJM.close()

	// Exercise every backoff bucket without wall-clock sleeps.
	for _, attempt := range []int{0, 1, 2, 20} {
		if got := shellFinalizeBackoff(attempt); got <= 0 || got > 50*time.Millisecond {
			t.Fatalf("backoff(%d) = %v", attempt, got)
		}
	}
}
