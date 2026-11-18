//go:build !windows

package mergeq

import (
	"context"
	"errors"
	"testing"
	"time"
)

// dropStub is a Client whose QueueDrop answers with a fixed event and error,
// whatever the context says.
//
// It exists to state the exact shape the drain bound produces: a read that
// returns a complete, valid answer while the context it was given has already
// ended. orphanpipe lets a call do that -- the bytes arrived and only a
// descendant kept the pipe open -- so "the answer is good" and "the operator is
// still waiting" are separate facts, and this type makes them separately
// settable.
type dropStub struct {
	Client
	event *DropEvent
	err   error
}

func (s dropStub) QueueDrop(context.Context, string, string) (*DropEvent, error) {
	return s.event, s.err
}

func (s dropStub) MergeGroupRuns(context.Context, string) ([]GroupRun, error) {
	return nil, nil
}

// TestQueueDropCancelledReadIsNotADrop pins that a cancelled wait cannot report
// a drop.
//
// The context is checked before the error, and on both paths. Checking it only
// when the read failed leaves the one shape this test covers: QueueDrop returns a
// valid event, so there is no error to branch on, and the call prints
// `MERGEQ dropped` with re-enqueue advice for a PR whose wait was cancelled --
// while the launcher reports 130 for the same invocation. Two exit statuses for
// one run, and the operator-facing one is the wrong of the two.
//
// The other direction is checked too: a read that genuinely fails with the
// context still live is still a drop with an unreadable reason, because the run
// it names is what the operator needs. Fixing the first must not swallow that.
func TestQueueDropCancelledReadIsNotADrop(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	fresh := &DropEvent{CreatedAt: now.Add(-time.Minute), Reason: "failed_checks"}
	opts := Options{Repo: "example.invalid/none", PR: "1"}
	st := Status{State: "OPEN"}

	t.Run("a valid answer during cancellation is not a drop", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		// Cancelled before the call, so no scheduling is involved: this is the
		// state the drain leaves behind, with the answer already in hand.
		cancel()
		out, exitErr := dropOutcome(ctx, opts, st, dropStub{event: fresh}, func() time.Time { return now })
		if exitErr == nil {
			t.Fatalf("a cancelled read that returned %+v was reported as a %v with %q: the operator cancelled, and a drop with re-enqueue advice for a PR nobody is asking about sends them to look for a queue problem that may not exist", out.DropClass, out.Kind, out.Reason)
		}
		if exitErr.Kind != Interrupted {
			t.Fatalf("kind = %v, want Interrupted: a cancelled wait exits %d, and any other kind reports a verdict the run did not reach", exitErr.Kind, InterruptedExit)
		}
	})
	t.Run("a failed read during cancellation is not a drop either", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		out, exitErr := dropOutcome(ctx, opts, st, dropStub{err: errors.New("context canceled")}, func() time.Time { return now })
		if exitErr == nil || exitErr.Kind != Interrupted {
			t.Fatalf("cancelled read with an error gave (%v, %v), want Interrupted: this is the case the original check covered, and it must keep working", out.Kind, exitErr)
		}
	})
	t.Run("a failed read with a live context is still a drop", func(t *testing.T) {
		t.Parallel()
		out, exitErr := dropOutcome(context.Background(), opts, st, dropStub{err: errors.New("boom")}, func() time.Time { return now })
		if exitErr != nil {
			t.Fatalf("a failed read under a live context returned %v, want a drop whose reason is unreadable: the run it names is what the operator needs", exitErr)
		}
		if out.Kind != Dropped {
			t.Fatalf("kind = %v, want Dropped", out.Kind)
		}
		if out.DropClass != DropReasonUnreadable {
			t.Fatalf("class = %v, want DropReasonUnreadable", out.DropClass)
		}
	})
}

// TestADropIsReportedWhenItsDiagnosticReadsStall pins the other limit on the
// drop path: the two reads that only EXPLAIN a drop must not be able to hold the
// run past its own limit.
//
// The drop is already decided when dropOutcome is called -- the status read saw
// the queue entry gone. Everything after that is a diagnostic: which merge-group
// run to read, and what the queue's removal reason was. On the operator's
// signal-only context those reads carry no deadline at all, so a stalled
// `gh run list` or removal-event query could keep the command running past
// `--wait` and past the final observation's allowance, and the operator would
// never be told about the drop the run had already observed.
//
// The assertion is on the allowance each read RECEIVES, not on waiting one out:
// a read that carries a deadline is bounded, and one carrying the run's
// signal-only context is the unbounded call this finding is about. Waiting for a
// 30-second allowance to expire would cost the suite what it is checking.
func TestADropIsReportedWhenItsDiagnosticReadsStall(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c := &deadlineRecordingDiagnostics{}
	// A base on the status, because the run reference is built from it: without
	// one mergeGroupRef answers from the base alone and never makes the list call,
	// so only one of the two diagnostics would be exercised.
	out, exitErr := dropOutcome(context.Background(), Options{Repo: "example.invalid/none", PR: "1"}, Status{State: "OPEN", Base: "main"}, c, func() time.Time { return now })
	if exitErr != nil {
		t.Fatalf("dropOutcome returned %v, want the drop it had already observed: the diagnostic reads explain a drop, they do not decide it", exitErr)
	}
	if out.Kind != Dropped {
		t.Fatalf("kind = %v, want Dropped: the queue entry was gone, and that is the outcome", out.Kind)
	}
	if len(c.hadDeadline) != 2 {
		t.Fatalf("%d diagnostic reads were made, want both of them asked: the run reference and the removal reason", len(c.hadDeadline))
	}
	for i, had := range c.hadDeadline {
		if !had {
			t.Fatalf("diagnostic read %d of 2 was issued on the run's signal-only context, which carries no deadline: a stalled gh call there keeps the command running past --wait, and the operator never learns about the drop the run already saw", i+1)
		}
	}
	// The fallback when a diagnostic does not arrive: the reason is reported as
	// unavailable, which is the same answer an unreadable reason already gets.
	if out.DropClass != DropReasonUnavailable {
		t.Fatalf("class = %v, want DropReasonUnavailable: the removal reason read did not answer, and that is a drop whose reason is unavailable", out.DropClass)
	}
	if out.Reason == "" {
		t.Fatal("the drop was reported with no reason at all: an unavailable reason still names the run the operator should read")
	}
}

// deadlineRecordingDiagnostics records the allowance each diagnostic read is
// given and answers nothing, which is the shape of a read that has not come back
// when its allowance runs out.
type deadlineRecordingDiagnostics struct {
	Client
	hadDeadline []bool
}

func (c *deadlineRecordingDiagnostics) record(ctx context.Context) {
	_, ok := ctx.Deadline()
	c.hadDeadline = append(c.hadDeadline, ok)
}

func (c *deadlineRecordingDiagnostics) QueueDrop(ctx context.Context, _, _ string) (*DropEvent, error) {
	c.record(ctx)
	return nil, ctx.Err()
}

func (c *deadlineRecordingDiagnostics) MergeGroupRuns(ctx context.Context, _ string) ([]GroupRun, error) {
	c.record(ctx)
	return nil, ctx.Err()
}
