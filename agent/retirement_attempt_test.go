package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// startReportingRetirementRun drives Run with a consumer and collects every
// attempt Run reports, so a test can see which outcomes reached the log.
func startReportingRetirementRun(t *testing.T, ctrl *RetirementController, consume func(context.Context, *RetirementClaim) error) chan RetirementAttempt {
	t.Helper()
	reports := make(chan RetirementAttempt, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	go func() {
		defer close(done)
		_ = ctrl.Run(ctx, consume, func(attempt RetirementAttempt) { reports <- attempt })
	}()
	return reports
}

func awaitRetirementReport(t *testing.T, reports chan RetirementAttempt) RetirementAttempt {
	t.Helper()
	select {
	case attempt := <-reports:
		return attempt
	case <-time.After(10 * time.Second): // TRIPWIRE: the report follows a test-driven tick with no wall-clock dependency.
		t.Fatal("no retirement attempt reported")
		return RetirementAttempt{}
	}
}

// awaitLastAttemptAt tripwire-polls until the snapshot's last attempt is the
// one made at at and Run has re-established eligibility from that same
// instant. Run reports an attempt before recording it, so any report it
// produced is already in the channel; and waiting for the re-armed interval
// keeps a later Advance from landing before Run re-evaluates, which would
// start the next interval late and make the next tick early.
func awaitLastAttemptAt(t *testing.T, ctrl *RetirementController, at time.Time) *RetirementAttempt {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap := ctrl.Snapshot()
		if last := snap.LastAttempt; last != nil && last.At.Equal(at) && snap.EligibleSince.Equal(at) {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot = %+v, want the attempt at %v and eligibility re-established then", snap, at)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertNoRetirementReport(t *testing.T, reports chan RetirementAttempt) {
	t.Helper()
	select {
	case attempt := <-reports:
		t.Fatalf("unexpected report of an unchanged outcome: %+v", attempt)
	default:
	}
}

// TestRetirementTimerRecordsRefusedAttempt pins that an automatic attempt
// refused on full evidence leaves its blockers in the status snapshot, even
// though those blockers are not live leases, and that the refusal is reported
// once: a later tick refused for the same reason updates the attempt's time
// without reporting it again.
func TestRetirementTimerRecordsRefusedAttempt(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	reports := startReportingRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	// A queued input is evidence the root reports without an admission lease,
	// so the live snapshot alone would show no blocker at all.
	root.mu.Lock()
	root.inputQueue = []queuedInput{{ID: "held", Text: "held input"}}
	root.mu.Unlock()
	if err := ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	clk.Advance(time.Hour)
	firstAt := clk.Now()
	clk.fire(t)
	report := awaitRetirementReport(t, reports)
	if !hasRetirementBlocker(report.Blockers, "input") || report.Error != "" {
		t.Fatalf("reported attempt = %+v, want the queued-input refusal", report)
	}
	if last := awaitLastAttemptAt(t, ctrl, firstAt); !hasRetirementBlocker(last.Blockers, "input") {
		t.Fatalf("snapshot last attempt = %+v, want the queued-input refusal", last)
	}

	clk.Advance(time.Hour)
	secondAt := clk.Now()
	clk.fire(t)
	awaitLastAttemptAt(t, ctrl, secondAt)
	assertNoRetirementReport(t, reports)
}

// TestRetirementTimerRecordsPreparationError pins that the consumer's error
// from a failed automatic retirement reaches the status snapshot and is
// reported, and that a change of outcome is reported again.
func TestRetirementTimerRecordsPreparationError(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	failures := []string{"lane read failed", "lane read failed", "job store closed"}
	consume := func(_ context.Context, claim *RetirementClaim) error {
		if err := ctrl.Abort(claim, "prepare_failed"); err != nil {
			t.Errorf("Abort: %v", err)
		}
		failure := failures[0]
		failures = failures[1:]
		return errors.New(failure)
	}
	reports := startReportingRetirementRun(t, ctrl, consume)
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	if err := ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	clk.Advance(time.Hour)
	clk.fire(t)
	if report := awaitRetirementReport(t, reports); report.Error != "lane read failed" || len(report.Blockers) != 0 {
		t.Fatalf("reported attempt = %+v, want the preparation error", report)
	}
	if last := awaitLastAttemptAt(t, ctrl, clk.Now()); last.Error != "lane read failed" {
		t.Fatalf("snapshot last attempt = %+v, want the preparation error", last)
	}

	clk.Advance(time.Hour)
	clk.fire(t)
	awaitLastAttemptAt(t, ctrl, clk.Now())
	assertNoRetirementReport(t, reports)

	clk.Advance(time.Hour)
	clk.fire(t)
	if report := awaitRetirementReport(t, reports); report.Error != "job store closed" {
		t.Fatalf("reported attempt = %+v, want the changed preparation error", report)
	}
}

// TestRetirementTimerEarlyTickRecordsNoAttempt pins that a tick before the
// deadline is not an attempt: it neither reports nor replaces the last
// attempt, so status never shows a refusal with no reason.
func TestRetirementTimerEarlyTickRecordsNoAttempt(t *testing.T) {
	t.Parallel()
	clk := newRetirementAckClock()
	ctrl, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatalf("NewRetirementController: %v", err)
	}
	reports := startReportingRetirementRun(t, ctrl, commitClaim(ctrl))
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	if err := ctrl.AttachRoot(root); err != nil {
		t.Fatalf("AttachRoot: %v", err)
	}
	clk.awaitArm(t)

	clk.Advance(30 * time.Minute)
	clk.fire(t)
	if d := clk.awaitArm(t); d != 30*time.Minute {
		t.Fatalf("re-arm after early tick = %v, want the 30m remainder", d)
	}
	assertNoRetirementReport(t, reports)
	if last := ctrl.Snapshot().LastAttempt; last != nil {
		t.Fatalf("snapshot last attempt = %+v, want none before the deadline", last)
	}
}
