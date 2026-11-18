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
