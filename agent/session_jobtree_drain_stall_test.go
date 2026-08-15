package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// collectStallWarnings drains the buffered event channel and returns warning
// events. The stall warning is emitted synchronously before the drain returns,
// so once the drain has returned it is already in the buffer.
func collectStallWarnings(sess *Session) []events.SessionEvent {
	var collected []events.SessionEvent
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind == events.EventWarning {
				collected = append(collected, ev)
			}
		default:
			return collected
		}
	}
}

func stallProcess(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
	return "", nil
}

// stallDriver deterministically single-steps the drain loop under a frozen fake
// clock. The injected kick blocks at the TOP of every loop iteration until the
// test releases it; because the loop's stall check (which reads the clock) runs
// AFTER the kick, releasing a kick only after advancing the clock guarantees the
// iteration reads the advanced time — no race between the clock Advance and the
// loop's Now() read. It does NOT rematerialize durable pendings, so a seeded
// h8mq-style stranded state stays stalled (a FUTURE unknown stranding class the
// self-heal path cannot reach) instead of self-healing.
type stallDriver struct {
	top     chan struct{}
	release chan struct{}
	recheck chan time.Time
	done    chan struct{}
	res     string
	err     error
}

func newStallDriver(ctx context.Context, sess *Session) *stallDriver {
	d := &stallDriver{
		top:     make(chan struct{}),
		release: make(chan struct{}),
		recheck: make(chan time.Time),
		done:    make(chan struct{}),
	}
	kick := func(context.Context) error {
		d.top <- struct{}{}
		<-d.release
		return nil
	}
	go func() {
		d.res, d.err = sess.drainJobTreeWith(ctx, d.recheck, kick, stallProcess)
		close(d.done)
	}()
	return d
}

// releaseKick unblocks the next iteration's kick so the iteration proceeds to
// its stall check. The caller advances the clock BEFORE calling this so the
// iteration's stall check reads the advanced time.
func (d *stallDriver) releaseKick(t *testing.T) {
	t.Helper()
	select {
	case <-d.top:
	case <-d.done:
		t.Fatal("drain returned before reaching the next iteration's kick")
	}
	d.release <- struct{}{}
}

// TestDrainStallWatchdogFiresOnGenuineStall verifies the defense-in-depth
// backstop: a subtree that stays outstanding with no live/deliverable component
// (h8mq-style durable-only pending, self-heal disabled) is cut once it has been
// continuously stalled past drainStallTimeout. The drain must RETURN (not hang),
// emit ONE warning naming the stuck job, and yield the last result.
func TestDrainStallWatchdogFiresOnGenuineStall(t *testing.T) {
	for _, tt := range []struct {
		name  string
		jobID string
		typ   jobstore.JobType
	}{
		{name: "shell", jobID: "shell-wedge", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clk := agenttest.NewFakeClock()
			sess := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true}))
			seedOwnedDurablePending(t, sess.jobManager, tt.jobID, tt.typ)

			if stalled, err := sess.drainSubtreeIsStalled(); err != nil || !stalled {
				t.Fatalf("precondition: expected a genuine stall, got stalled=%v err=%v", stalled, err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			d := newStallDriver(ctx, sess)
			d.releaseKick(t)
			d.assertParked(t, "iteration 1 must park, not fire before the timeout")

			clk.Advance(drainStallTimeout + time.Second)
			d.recheck <- time.Time{}
			d.releaseKick(t)

			select {
			case <-d.done:
			case <-time.After(5 * time.Second):
				t.Fatal("drain did not return after the stall timeout; watchdog failed to fire")
			}
			if d.err != nil {
				t.Fatalf("stall watchdog must return nil error so run.go keeps the result, got %v", d.err)
			}
			if d.res != "" {
				t.Fatalf("expected empty last result (no drain turn ran), got %q", d.res)
			}

			gotEvents := collectStallWarnings(sess)
			if len(gotEvents) != 1 {
				t.Fatalf("stall watchdog events = %d, want 1", len(gotEvents))
			}
			warning, ok := gotEvents[0].Data.(events.WarningData)
			if !ok || !strings.Contains(warning.Message, tt.jobID) {
				t.Fatalf("stall warning must name the stuck job %s, got %+v", tt.jobID, gotEvents[0])
			}
		})
	}
}

// assertParked fails the test unless the drain goroutine is still blocked in
// waitDrainWake, i.e. this iteration kept waiting rather than firing or
// returning. A short window with no done signal means it is still parked.
func (d *stallDriver) assertParked(t *testing.T, msg string) {
	t.Helper()
	select {
	case <-d.done:
		t.Fatalf("drain returned when it should have kept waiting: %s", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestDrainStallWatchdogSparesRunningDrainJob verifies an owned running managed
// job remains live work even well past the timeout, so the drain keeps waiting.
func TestDrainStallWatchdogSparesRunningDrainJob(t *testing.T) {
	for _, tt := range []struct {
		name string
		typ  jobstore.JobType
	}{
		{name: "shell", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clk := agenttest.NewFakeClock()
			sess := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true}))
			jm := sess.jobManager
			rec := &jobstore.JobRecord{JobID: "live-" + tt.name, Type: tt.typ, Status: jobstore.StatusRunning, OwnerSessionID: sess.ID()}
			jm.mu.Lock()
			jm.running[rec.JobID] = &runningJob{rec: rec}
			jm.mu.Unlock()
			defer func() {
				jm.mu.Lock()
				delete(jm.running, rec.JobID)
				jm.mu.Unlock()
			}()

			if stalled, err := sess.drainSubtreeIsStalled(); err != nil || stalled {
				t.Fatalf("an owned running managed job must not be a stall, got stalled=%v err=%v", stalled, err)
			}
			assertDrainNotCut(t, sess, clk)
		})
	}
}

// TestDrainStallWatchdogSpareDrivingChild verifies a driving child keeps the
// drain alive past the timeout.
func TestDrainStallWatchdogSpareDrivingChild(t *testing.T) {
	clk := agenttest.NewFakeClock()
	root := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true}))
	child := newSession(t, withConfig(SessionConfig{NoProjectPrompts: true}))
	childID := child.ID()

	// The root owes work (a durable-only pending) AND has a driving child: the
	// driving child is live, so the tree is not stalled.
	seedOwnedDurablePending(t, root.jobManager, "shell-root", jobstore.JobShell)
	root.subagents.mu.Lock()
	root.subagents.subs[childID] = &subagent{id: childID, sess: child, driving: true}
	root.subagents.mu.Unlock()
	defer func() {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, childID)
		root.subagents.mu.Unlock()
	}()

	if stalled, err := root.drainSubtreeIsStalled(); err != nil || stalled {
		t.Fatalf("a driving child must not be a stall, got stalled=%v err=%v", stalled, err)
	}
	assertDrainNotCut(t, root, clk)
}

// TestDrainStallWatchdogSparePendingWatchSend verifies a pending caller-targeted
// watch send (deliverable) keeps the drain alive past the timeout.
func TestDrainStallWatchdogSparePendingWatchSend(t *testing.T) {
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true}))

	jm := sess.jobManager
	wk := watchKey{VisibleSessionID: jm.sessionID, Target: "t1"}
	jm.mu.Lock()
	jm.watches[wk] = &watchConfig{pendingOrder: []jobstore.WatchSendKey{{VisibleSessionID: jm.sessionID}}}
	jm.mu.Unlock()
	defer func() {
		jm.mu.Lock()
		delete(jm.watches, wk)
		jm.mu.Unlock()
	}()

	if !jm.hasPendingWatchSends() {
		t.Fatal("precondition: expected a pending watch send")
	}
	if stalled, err := sess.drainSubtreeIsStalled(); err != nil || stalled {
		t.Fatalf("a pending watch send must not be a stall, got stalled=%v err=%v", stalled, err)
	}
	assertDrainNotCut(t, sess, clk)
}

// assertDrainNotCut drives the drain well past drainStallTimeout and asserts it
// keeps blocking (does not return, emits no stall warning). Live work must never
// be cut. It then cancels the context to let the drain goroutine exit cleanly.
func assertDrainNotCut(t *testing.T, sess *Session, clk *agenttest.FakeClock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := newStallDriver(ctx, sess)

	// Iteration 1 at T0: not stalled (live work) so the stall clock never starts.
	d.releaseKick(t)
	d.assertParked(t, "drain must keep waiting on live work")

	// Advance far past the timeout and run another iteration: live work still
	// resets the stall clock, so the drain must NOT fire.
	clk.Advance(drainStallTimeout * 10)
	d.recheck <- time.Time{}
	d.releaseKick(t)
	d.assertParked(t, "live work must never be cut even past the timeout")

	if gotEvents := collectStallWarnings(sess); len(gotEvents) != 0 {
		t.Fatalf("live work must emit no stall warning, got %v", gotEvents)
	}

	cancel()
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not exit after context cancellation")
	}
}
