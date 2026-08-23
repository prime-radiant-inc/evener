package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
)

// seedStopRequestedDelegate seeds a delegate on root that is mid-run with a
// subtree stop already REQUESTED against it and never completed — the shape
// job_stop leaves behind when it degrades to stop_pending because the target is
// inside a tool call it cannot be interrupted out of. lastActivity is the
// delegate's latest parent-observable activity. It returns the running subagent
// row, tracked on root, plus the delegate id.
func seedStopRequestedDelegate(t *testing.T, root *Session, id string, lastActivity time.Time) (childID, delegateID string) {
	t.Helper()
	child := newSession(t, withConfig(SessionConfig{NoProjectPrompts: true}))
	descriptor := stableToolDescriptor(root, id, "")
	descriptor.ChildSessionID = child.ID()

	root.delegateController.mu.Lock()
	_, err := root.delegateController.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: id,
			Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
		},
		delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, lastActivity),
		delegatestore.Event{
			Kind:                 delegatestore.EventDelegateSubtreeStopRequested,
			DelegateID:           id,
			SubtreeStopRequested: &delegatestore.SubtreeStopRequested{TargetDelegateID: id},
		},
	)
	root.delegateController.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// The delegate's runtime is still "running" as far as the parent can see:
	// nothing in the child has finished, so the subagent row keeps the drain's
	// outstanding check true, exactly as it did in the field.
	root.subagents.mu.Lock()
	root.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child, running: true}
	root.subagents.mu.Unlock()
	t.Cleanup(func() {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, child.ID())
		root.subagents.mu.Unlock()
	})
	return child.ID(), id
}

// stampDelegateActivity records parent-observable activity for a delegate at
// `at`, the way a transcript turn, an API attempt or a state write does through
// the controller's ReportActivity path.
func stampDelegateActivity(root *Session, delegateID string, at time.Time) {
	c := root.delegateController
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live[delegateID]
	if live == nil {
		live = &delegateLiveState{}
		c.live[delegateID] = live
	}
	live.activityAt = at
}

// TestDrainAbandonsStopRequestedDelegateGoneUnresponsive is the drain half of
// the 109-minute field hang (#317, #369): the root asked to stop a delegate,
// the delegate was wedged inside an uninterruptible tool call so the stop
// degraded to stop_pending forever, and the drain kept counting the delegate as
// live work until an external killer fired. Once a stop-requested delegate has
// shown no activity for the stall bound it is undisposable, and the drain must
// announce and give up rather than waiting for the process to be killed.
func TestDrainAbandonsStopRequestedDelegateGoneUnresponsive(t *testing.T) {
	clk := agenttest.NewFakeClock()
	root := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true, TurnEndsProcess: true}))
	_, delegateID := seedStopRequestedDelegate(t, root, "dlg_wedged", clk.Now())

	outstanding, err := root.treeHasOutstandingWork()
	if err != nil || !outstanding {
		t.Fatalf("precondition: a running delegate must be outstanding work, got outstanding=%v err=%v", outstanding, err)
	}
	if stalled, err := root.drainSubtreeIsStalled(); err != nil || stalled {
		t.Fatalf("a stop-requested delegate that just showed activity must still be live, got stalled=%v err=%v", stalled, err)
	}

	// No activity for the whole bound: the stop can never be honoured.
	clk.Advance(drainStallTimeout + time.Second)
	if stalled, err := root.drainSubtreeIsStalled(); err != nil || !stalled {
		t.Fatalf("a stop-requested delegate silent past the bound must stop counting as live, got stalled=%v err=%v", stalled, err)
	}

	// TRIPWIRE: the driver single-steps a frozen fake clock with hand-
	// synchronized channels; nothing here waits on real I/O or a real clock.
	// 30s only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d := newStallDriver(ctx, root)
	d.releaseKick(t)
	d.assertParked(t, "the first pass only starts the stall clock")

	clk.Advance(drainStallTimeout + time.Second)
	d.recheck <- time.Time{}
	d.releaseKick(t)

	select {
	case <-d.done:
	// TRIPWIRE: awaits d.done, the drain goroutine's own completion signal.
	// 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("drain never gave up on a stop-requested, unresponsive delegate")
	}
	if d.err != nil {
		t.Fatalf("give-up must return nil error so the run keeps its result, got %v", d.err)
	}

	warnings := collectStallWarnings(root)
	if len(warnings) != 1 {
		t.Fatalf("abandonment warnings = %d, want 1: %+v", len(warnings), warnings)
	}
	msg := warnings[0].Data.(events.WarningData).Message
	if !strings.Contains(msg, delegateID) {
		t.Fatalf("abandonment warning must name the delegate it gave up on, got %q", msg)
	}
}

// TestDrainWaitsOnStopRequestedDelegateStillMakingProgress is the other half of
// the contract: a stop request is not itself permission to abandon. A delegate
// that keeps showing activity inside the bound is winding down normally, and
// the drain must keep waiting on it however long the stop has been pending.
func TestDrainWaitsOnStopRequestedDelegateStillMakingProgress(t *testing.T) {
	clk := agenttest.NewFakeClock()
	root := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true, TurnEndsProcess: true}))
	_, delegateID := seedStopRequestedDelegate(t, root, "dlg_winding_down", clk.Now())

	// Well past the bound in absolute terms, but the delegate reported activity
	// throughout, so it was never silent for the bound.
	for range 4 {
		clk.Advance(drainStallTimeout - time.Second)
		stampDelegateActivity(root, delegateID, clk.Now())
		if stalled, err := root.drainSubtreeIsStalled(); err != nil || stalled {
			t.Fatalf("a stop-requested delegate still reporting activity must be waited on, got stalled=%v err=%v", stalled, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	d := newStallDriver(ctx, root)
	d.releaseKick(t)
	d.assertParked(t, "a progressing delegate must never be cut")

	clk.Advance(drainStallTimeout - time.Second)
	stampDelegateActivity(root, delegateID, clk.Now())
	d.recheck <- time.Time{}
	d.releaseKick(t)
	d.assertParked(t, "activity inside the bound must keep resetting the stall clock")

	if warnings := collectStallWarnings(root); len(warnings) != 0 {
		t.Fatalf("a progressing delegate must emit no abandonment warning, got %+v", warnings)
	}

	cancel()
	select {
	case <-d.done:
	// TRIPWIRE: awaits d.done after cancel(); the goroutine should observe
	// ctx.Done() and return immediately. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("drain did not exit after context cancellation")
	}
}
