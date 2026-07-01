package agent

import (
	"context"
	"regexp"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// w2conc_fakeClockSession builds a session whose job-manager clock is a
// deterministically-advanceable FakeClock, so the wait* timer/ticker arms fire
// on virtual time with no wall-clock sleeps.
func w2conc_fakeClockSession(t *testing.T) (*Session, *agenttest.FakeClock) {
	t.Helper()
	clk := agenttest.NewFakeClock()
	s := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1, clock: clk}))
	return s, clk
}

// w2conc_runningJob creates a synthetic running shell job that stays running
// (its done channel open) until the test finalizes it, so the wait* loops park
// on the clock instead of returning early. Cleanup finalizes it.
func w2conc_runningJob(t *testing.T, s *Session) *jobstore.JobRecord {
	t.Helper()
	rec, err := s.jobManager.createShell(createShellOpts{Command: "w2conc running job"})
	if err != nil {
		t.Fatalf("create shell job: %v", err)
	}
	t.Cleanup(func() {
		_ = s.jobManager.finalize(rec.JobID, jobstore.StatusCancelled, "w2conc_cleanup", nil)
	})
	return rec
}

// TestW2Conc_WaitForJobDoneNotRunningReturnsTrue pins the fast-path arm: a
// wait on a job that is not running (unknown / already terminal) returns true
// without arming a timer.
func TestW2Conc_WaitForJobDoneNotRunningReturnsTrue(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	if !waitForJobDone(context.Background(), s.jobManager, "job_missing", time.Second) {
		t.Fatal("waitForJobDone on a non-running job = false, want true")
	}
}

// TestW2Conc_WaitForJobDoneTimeout pins the timer arm: while the job stays
// running, advancing virtual time past the timeout fires timer.C and the wait
// reports false (timed out).
func TestW2Conc_WaitForJobDoneTimeout(t *testing.T) {
	s, clk := w2conc_fakeClockSession(t)
	rec := w2conc_runningJob(t, s)

	baseline := clk.BlockedCount()
	res := make(chan bool, 1)
	go func() {
		res <- waitForJobDone(context.Background(), s.jobManager, rec.JobID, time.Second)
	}()

	clk.BlockUntil(baseline + 1) // wait until the timer is armed
	clk.Advance(time.Second)

	select {
	case got := <-res:
		if got {
			t.Fatal("waitForJobDone timed out but returned true, want false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForJobDone did not return after the timer fired")
	}
}

// TestW2Conc_WaitForJobDoneOrOutputNotRunningReturns pins the fast-path arm.
func TestW2Conc_WaitForJobDoneOrOutputNotRunningReturns(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	done := make(chan struct{})
	go func() {
		waitForJobDoneOrOutput(context.Background(), s.jobManager, "job_missing", time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForJobDoneOrOutput on a non-running job did not return")
	}
}

// TestW2Conc_WaitForJobDoneOrOutputTimeout pins the timer arm: with no new
// output, advancing past the timeout fires timer.C and the wait returns.
func TestW2Conc_WaitForJobDoneOrOutputTimeout(t *testing.T) {
	s, clk := w2conc_fakeClockSession(t)
	rec := w2conc_runningJob(t, s)

	baseline := clk.BlockedCount()
	done := make(chan struct{})
	go func() {
		waitForJobDoneOrOutput(context.Background(), s.jobManager, rec.JobID, time.Second)
		close(done)
	}()

	clk.BlockUntil(baseline + 2) // timer + ticker armed
	clk.Advance(time.Second)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForJobDoneOrOutput did not return after the timer fired")
	}
}

// TestW2Conc_WaitForJobDoneOrOutputCtxCancel pins the ctx.Done arm: cancelling
// the context ends the wait even though the clock never advances.
func TestW2Conc_WaitForJobDoneOrOutputCtxCancel(t *testing.T) {
	s, clk := w2conc_fakeClockSession(t)
	rec := w2conc_runningJob(t, s)

	baseline := clk.BlockedCount()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		waitForJobDoneOrOutput(ctx, s.jobManager, rec.JobID, time.Second)
		close(done)
	}()

	clk.BlockUntil(baseline + 2) // timer + ticker armed; only ctx can end the wait
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForJobDoneOrOutput did not return on ctx cancel")
	}
}

// TestW2Conc_WaitForJobGrepMatchNotRunningReturns pins the fast-path arm: a
// running job absent from the manager returns without arming a timer.
func TestW2Conc_WaitForJobGrepMatchNotRunningReturns(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	re := regexp.MustCompile("ready")
	done := make(chan struct{})
	go func() {
		waitForJobGrepMatch(context.Background(), s.jobManager, "job_missing", re, time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForJobGrepMatch on a non-running job did not return")
	}
}

// TestW2Conc_WaitForJobGrepMatchCtxCancel pins the ctx.Done arm: with no
// matching output and the clock frozen, cancelling the context ends the wait.
func TestW2Conc_WaitForJobGrepMatchCtxCancel(t *testing.T) {
	s, clk := w2conc_fakeClockSession(t)
	rec := w2conc_runningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "no signal here\n")
	re := regexp.MustCompile("ready")

	baseline := clk.BlockedCount()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		waitForJobGrepMatch(ctx, s.jobManager, rec.JobID, re, time.Second)
		close(done)
	}()

	clk.BlockUntil(baseline + 2) // timer + ticker armed; no match, so only ctx ends it
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForJobGrepMatch did not return on ctx cancel")
	}
}

// TestW2Conc_ScanSegmentStripsCarriageReturn pins the CRLF arm of scanSegment:
// a complete line matches with its trailing \r stripped, like the snapshot grep.
func TestW2Conc_ScanSegmentStripsCarriageReturn(t *testing.T) {
	t.Parallel()
	g := &jobGrepScan{}
	re := regexp.MustCompile("^hello$")
	if !g.scanSegment([]byte("hello\r\n"), re, maxJobGrepLineBytes) {
		t.Fatal("scanSegment did not strip the trailing carriage return before matching")
	}
}

// TestW2Conc_ScanSegmentDeadLineNoNewline pins the in-dead-line arm: while
// inside a line already too long to ever match, a segment with no newline is
// skipped wholesale (scanned advances, no match).
func TestW2Conc_ScanSegmentDeadLineNoNewline(t *testing.T) {
	t.Parallel()
	g := &jobGrepScan{inDeadLine: true, scanned: 10}
	re := regexp.MustCompile("ready")
	seg := []byte("ready-but-inside-a-dead-line")
	if g.scanSegment(seg, re, maxJobGrepLineBytes) {
		t.Fatal("scanSegment matched inside a dead line, want no match")
	}
	if g.scanned != 10+int64(len(seg)) {
		t.Fatalf("scanned = %d, want %d (whole dead segment consumed)", g.scanned, 10+int64(len(seg)))
	}
	if !g.inDeadLine {
		t.Fatal("inDeadLine cleared before the dead line's newline arrived")
	}
}
