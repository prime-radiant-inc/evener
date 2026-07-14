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

// seedDurableOnlyPending writes an owned delegate whose owner notification
// survives ONLY in the durable ledger (NotifyPending) with nothing queued in
// memory — the h8mq-style stranded state. With no re-materialize kick this stays
// outstanding forever, so it is a genuine stall the watchdog must eventually cut.
func seedDurableOnlyPending(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobDelegate, OwnerSessionID: jm.sessionID, VisibleToSession: jm.sessionID, StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID, Status: jobstore.StatusCompleted, Reason: "communicated", EndedAt: &ended, TerminalGen: "gen-" + jobID},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: jobID, TerminalGen: "gen-" + jobID},
	} {
		if err := jm.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}
}

// collectWarnings drains the buffered event channel and returns every warning
// message currently queued. The stall warning is emitted synchronously before
// the drain returns, so once the drain has returned it is already in the buffer.
func collectWarnings(sess *Session) []string {
	var msgs []string
	for {
		select {
		case ev := <-sess.Events():
			if ev.Kind == events.EventWarning {
				if w, ok := ev.Data.(events.WarningData); ok {
					msgs = append(msgs, w.Message)
				}
			}
		default:
			return msgs
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
// emit ONE warning naming the stuck delegate, and yield the last result.
func TestDrainStallWatchdogFiresOnGenuineStall(t *testing.T) {
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true}))
	seedDurableOnlyPending(t, sess.jobManager, "del-wedge")

	if stalled, err := sess.drainSubtreeIsStalled(); err != nil || !stalled {
		t.Fatalf("precondition: expected a genuine stall, got stalled=%v err=%v", stalled, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := newStallDriver(ctx, sess)

	// Iteration 1 (clock at T0): establishes the stall start, then parks on recheck.
	d.releaseKick(t)
	d.assertParked(t, "iteration 1 must park, not fire before the timeout")

	// Advance past the timeout, then wake and run iteration 2, which reads the
	// advanced clock and must fire the watchdog.
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

	var found string
	for _, m := range collectWarnings(sess) {
		if strings.Contains(m, "drain stalled") {
			found = m
		}
	}
	if found == "" {
		t.Fatal("expected a stall warning naming the stuck delegate")
	}
	if !strings.Contains(found, "del-wedge") {
		t.Fatalf("stall warning must name the stuck delegate del-wedge, got %q", found)
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

// TestDrainStallWatchdogSpareRunningDelegate verifies a delegate still in the
// running map (live work — a long build) is NEVER cut, even well past the
// timeout: the drain keeps waiting.
func TestDrainStallWatchdogSpareRunningDelegate(t *testing.T) {
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true}))

	jm := sess.jobManager
	jm.mu.Lock()
	jm.running["del-live"] = &runningJob{rec: &jobstore.JobRecord{
		JobID:  "del-live",
		Type:   jobstore.JobDelegate,
		Status: jobstore.StatusRunning,
	}}
	jm.mu.Unlock()
	defer func() {
		jm.mu.Lock()
		delete(jm.running, "del-live")
		jm.mu.Unlock()
	}()

	if stalled, err := sess.drainSubtreeIsStalled(); err != nil || stalled {
		t.Fatalf("a running delegate must not be a stall, got stalled=%v err=%v", stalled, err)
	}
	assertDrainNotCut(t, sess, clk)
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
	seedDurableOnlyPending(t, root.jobManager, "del-root")
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

	if warnings := collectWarnings(sess); len(warnings) != 0 {
		t.Fatalf("live work must emit no stall warning, got %v", warnings)
	}

	cancel()
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not exit after context cancellation")
	}
}
