package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
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
	hint    func()
	done    chan struct{}
	res     string
	err     error
}

// stallHandshakeTimeout bounds ONE blocking step of a stallDriver handshake. The
// driver is deterministic (frozen fake clock, hand-synchronized channels), so a
// step that has not completed within this window is a genuine hang in the drain
// loop, never scheduler contention. The driver's context is deliberately
// deadline-free (#2680: drainJobTreeWith returns ctx.Err() when the caller's
// context ends, so a wall-clock deadline turns a slow handshake into a spurious
// behavior failure), which means these per-step bounds — not the context — are
// what keep a wedged driver from hanging the whole test process: t.Context() is
// only cancelled at cleanup, which a hung handshake never reaches. Generous by
// construction: a correct single step completes in microseconds.
const stallHandshakeTimeout = 30 * time.Second

func newStallDriver(ctx context.Context, sess *Session) *stallDriver {
	return newStallDriverWithProcess(ctx, sess, stallProcess)
}

func newStallDriverWithProcess(ctx context.Context, sess *Session, process func(context.Context, string, []ImageAttachment, EntryKind) (string, error)) *stallDriver {
	d := &stallDriver{
		top:     make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	wake := drainWakeForTest(sess)
	d.hint = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	kick := func(context.Context) error {
		d.top <- struct{}{}
		<-d.release
		return nil
	}
	go func() {
		d.res, d.err = sess.drainJobTreeWith(ctx, wake, nil, kick, process)
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
	case <-time.After(stallHandshakeTimeout):
		t.Fatal("drain did not reach the next iteration's kick")
	}
	d.releaseKickSend(t)
}

// releaseKickSend completes a handshake step after the caller has observed
// d.top. The kick goroutine (newStallDriverWithProcess) has finished its
// `d.top <- struct{}{}` and its very next statement is `<-d.release`, so this is
// an immediate rendezvous in any correct run. It is still bounded, so a kick
// goroutine that is never scheduled again fails the test cleanly instead of
// hanging the process — the same guard stallHandshakeTimeout gives every step.
func (d *stallDriver) releaseKickSend(t *testing.T) {
	t.Helper()
	select {
	case d.release <- struct{}{}:
	case <-d.done:
		t.Fatal("drain returned before the kick was released")
	case <-time.After(stallHandshakeTimeout):
		t.Fatal("drain did not accept the kick release")
	}
}

// advanceClock holds an existing producer pass while time moves, then raises
// one confirming hint. Grace classification precedes kick, so the confirming
// pass reads the advanced time. The held edge also prevents a deadline return
// from racing the test's next barrier. Neither hint is a periodic pump.
func (d *stallDriver) advanceClock(t *testing.T, sess *Session, clk *agenttest.FakeClock, delta time.Duration) {
	t.Helper()
	sess.notify()
	select {
	case <-d.top:
	case <-d.done:
		t.Fatal("drain returned before the controlled resource deadline step")
	case <-time.After(stallHandshakeTimeout):
		t.Fatal("drain did not reach the held producer pass")
	}
	clk.Advance(delta)
	sess.notify()
	d.releaseKickSend(t)
	d.releaseKick(t)
}

// TestDrainStallWatchdogFiresOnGenuineStall verifies the defense-in-depth
// backstop: a subtree that stays outstanding with no live/deliverable component
// (h8mq-style durable-only pending, self-heal disabled) is cut once it has been
// continuously stalled past DrainStallTimeout. The drain must RETURN (not hang),
// emit ONE warning naming the stuck job, and yield the last result.
func TestDrainStallWatchdogFiresOnGenuineStall(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		jobID string
		typ   jobstore.JobType
	}{
		{name: "shell", jobID: "shell-wedge", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clk := agenttest.NewFakeClock()
			sess := newSession(t, withConfig(SessionConfig{clock: clk}))
			seedOwnedDurablePending(t, sess.jobManager, tt.jobID, tt.typ)

			if stalled, err := sess.drainSubtreeIsStalled(); err != nil || !stalled {
				t.Fatalf("precondition: expected a genuine stall, got stalled=%v err=%v", stalled, err)
			}

			// TRIPWIRE: the driver single-steps a frozen fake clock with
			// hand-synchronized channels; nothing here waits on real I/O or a
			// real clock. 30s only fires on a genuine hang.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			d := newStallDriver(ctx, sess)
			d.releaseKick(t)
			d.assertParked(t, "iteration 1 must park, not fire before the timeout")

			clk.Advance(DrainStallTimeout - time.Nanosecond)
			d.assertParked(t, "the actual resource deadline has not elapsed")
			// The production resource timer, not a synthetic notification tick,
			// wakes the expired pass and returns without another producer kick.
			clk.Advance(time.Nanosecond)

			select {
			case <-d.done:
			// TRIPWIRE: awaits d.done, the drain goroutine's own completion
			// signal; the fake-clock driver above should return immediately
			// once its stall check trips. 30s only fires on a genuine hang.
			case <-time.After(30 * time.Second):
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
	// TRIPWIRE: not a completion-signal wait -- this deliberately blocks for a
	// short window to positively confirm the goroutine stayed parked instead
	// of firing early. releaseKick has already synchronized to the iteration
	// boundary, so 50ms is generous slack for the negative result, not a
	// budget for real work to finish.
	case <-time.After(50 * time.Millisecond):
	}
}

// TestDrainStallWatchdogSparesRunningDrainJob verifies an owned running managed
// job remains live work even well past the timeout, so the drain keeps waiting.
func TestDrainStallWatchdogSparesRunningDrainJob(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		typ  jobstore.JobType
	}{
		{name: "shell", typ: jobstore.JobShell},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clk := agenttest.NewFakeClock()
			sess := newSession(t, withConfig(SessionConfig{clock: clk}))
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
	t.Parallel()
	clk := agenttest.NewFakeClock()
	root := newSession(t, withConfig(SessionConfig{clock: clk}))
	child := newSession(t, withConfig(SessionConfig{}))
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
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk}))

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

// TestDrainStallWatchdogSparePendingDelegateDelivery verifies a pending delegate
// delivery (deliverable work, flushed by kickDriveTree every pass) keeps the
// drain alive past the timeout. subtreeHasLiveComponent counts it as a live
// component (session_jobtree_drain.go), so a queue entry that sits unconsumed
// under a kick which deliberately never flushes must never be cut as a genuine
// wedge. Its three siblings cover a running managed job, a driving child, and a
// pending watch send; this is the hasPendingDelegateDeliveries signal.
func TestDrainStallWatchdogSparePendingDelegateDelivery(t *testing.T) {
	// Serial: seedPendingDeliverySource captures process-wide diagnostics.
	clk := agenttest.NewFakeClock()
	adapter := &fakeAdapter{name: "openai"}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{clock: clk}))
	sess.stopLaneResidueSweepTimer()

	deliveryID := seedPendingDeliverySource(t, sess, clk)
	c := sess.delegateController
	c.mu.Lock()
	descriptor := c.durable["dlg_pending"].Descriptor
	c.mu.Unlock()
	path, _, err := delegateTranscriptPathFromRef(c.stateDir, descriptor.TranscriptRef)
	if err != nil {
		t.Fatal(err)
	}
	// Scoped wake recovery may inspect this receiver while the fake clock
	// advances. No accepted attention exists, so a real empty fold must resolve
	// that hint without trying to reconstruct a nonexistent runtime.
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: descriptor.ChildSessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if !sess.hasPendingDelegateDeliveries() {
		t.Fatal("precondition: expected a pending delegate delivery")
	}
	if stalled, err := sess.drainSubtreeIsStalled(); err != nil || stalled {
		t.Fatalf("a pending delegate delivery must not be a stall, got stalled=%v err=%v", stalled, err)
	}
	assertDrainNotCut(t, sess, clk)
	aggregate := delegateAggregateSnapshot(t, c, "dlg_pending")
	if len(aggregate.PendingDeliveries) != 1 || aggregate.PendingDeliveries[0].DeliveryID != deliveryID || len(adapter.Requests()) != 0 {
		t.Fatalf("injected scans changed source custody or ran model: pending=%+v requests=%d", aggregate.PendingDeliveries, len(adapter.Requests()))
	}
}

// TestDrainEntryRepairsOrphanNotificationRetryHint characterizes a hint with no
// durable notification or executor-release obligation. The pre-entry predicates
// count it as live, but receipt reconciliation must repair that projection,
// not manufacture a provider request or keep an empty drain alive.
func TestDrainEntryRepairsOrphanNotificationRetryHint(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	adapter := &fakeAdapter{name: "openai"}
	sess := newSession(t, withAdapter(adapter), withConfig(SessionConfig{clock: clk, StateDir: t.TempDir()}))

	// Deliberately seed only the disposable hint, not a failed release or an
	// accepted message. The common schedule is guarded by sess.mu.
	sess.mu.Lock()
	sess.notificationSchedule.retry.active = true
	sess.mu.Unlock()

	if !sess.notificationWorkPending() {
		t.Fatal("precondition: expected a pending delegate attention arm retry")
	}
	if stalled, err := sess.drainSubtreeIsStalled(); err != nil || stalled {
		t.Fatalf("a pending delegate attention arm retry must not be a stall, got stalled=%v err=%v", stalled, err)
	}
	if outstanding, err := sess.treeHasOutstandingWork(); err != nil || !outstanding {
		t.Fatalf("a pending delegate attention arm retry must keep the tree outstanding, got outstanding=%v err=%v", outstanding, err)
	}
	if result, err := sess.DrainJobTree(t.Context(), drainWakeForTest(sess)); err != nil || result != "" {
		t.Fatalf("empty drain = %q, %v, want quiet success", result, err)
	}
	sess.mu.Lock()
	retrying := sess.notificationSchedule.retry.active
	sess.mu.Unlock()
	if outstanding, err := sess.treeHasOutstandingWork(); err != nil || outstanding || retrying || len(adapter.Requests()) != 0 {
		t.Fatalf("reconciled empty drain: outstanding=%t err=%v retrying=%t requests=%d", outstanding, err, retrying, len(adapter.Requests()))
	}
	for _, entry := range notificationRawEntries(t, sess.TranscriptPath()) {
		if entry.Turn.AttentionID != "" || len(entry.Turn.ObservedNotificationIDs) != 0 || entry.Turn.AttentionResolution != nil {
			t.Fatal("orphan hint produced notification custody, observation or resolution")
		}
	}
	if warnings := collectStallWarnings(sess); len(warnings) != 0 {
		t.Fatalf("empty drain emitted warnings: %+v", warnings)
	}
}

// TestDrainCountsStrandedDelegateFinalizationRecoveryAsOutstanding pins issue
// #179 item (b): after a FinishGeneration append failure the delegate's own run
// flags read quiet (not running/finalizing/driving), but the controller still
// holds the open generation behind finalizationRecoveryRequired. Drain must
// report that as outstanding work — a stall the watchdog can give up on — not
// quiescence, or the drain and Close() proceed with the generation unsettled.
func TestDrainCountsStrandedDelegateFinalizationRecoveryAsOutstanding(t *testing.T) {
	t.Parallel()
	const delegateID = "dlg_stranded_recovery"
	for _, tt := range []struct {
		name string
		live *delegateLiveState
		want bool
	}{
		{name: "recovery latched", live: &delegateLiveState{recoveryRequired: true, finalizationRecoveryRequired: true}, want: true},
		{name: "no recovery", live: &delegateLiveState{}, want: false},
		{name: "delegate unknown", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clk := agenttest.NewFakeClock()
			root := newSession(t, withConfig(SessionConfig{clock: clk}))
			child := newSession(t, withConfig(SessionConfig{}))
			child.owningDelegateID = delegateID
			childID := child.ID()

			// Seed the session's own controller, the way a failed finish append
			// leaves it: the delegate is in live with the recovery latch set, or
			// indistinguishable from a delegate the controller never heard of.
			// This is the minimal latch drain reads — a real append failure also
			// leaves live.binding and recoveryRunnerPending set, neither of which
			// the drain signal consults (see
			// TestDelegateControllerFinishNoActionAppendFailureRetainsRecoveryState
			// for the full post-failure state).
			controller := root.delegateController
			if controller == nil {
				t.Fatal("precondition: a session must own a delegate controller")
			}
			controller.mu.Lock()
			if tt.live != nil {
				controller.live[delegateID] = tt.live
			}
			controller.mu.Unlock()
			t.Cleanup(func() {
				controller.mu.Lock()
				delete(controller.live, delegateID)
				controller.mu.Unlock()
			})

			root.subagents.mu.Lock()
			root.subagents.subs[childID] = &subagent{id: childID, sess: child}
			root.subagents.mu.Unlock()

			outstanding, err := root.treeHasOutstandingWork()
			if err != nil || outstanding != tt.want {
				t.Fatalf("treeHasOutstandingWork (%s) = %v err=%v, want %v", tt.name, outstanding, err, tt.want)
			}
			if !tt.want {
				return
			}
			if stalled, err := root.drainSubtreeIsStalled(); err != nil || !stalled {
				t.Fatalf("stranded finalization recovery must be outstanding-but-stalled, got stalled=%v err=%v", stalled, err)
			}
		})
	}
}

// assertDrainNotCut drives the drain well past DrainStallTimeout and asserts it
// keeps blocking (does not return, emits no stall warning). Live work must never
// be cut. It then cancels the context to let the drain goroutine exit cleanly.
func assertDrainNotCut(t *testing.T, sess *Session, clk *agenttest.FakeClock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := newStallDriver(ctx, sess)

	// Iteration 1 at T0: not stalled (live work) so the stall clock never starts.
	d.releaseKick(t)
	d.assertParked(t, "drain must keep waiting on live work")

	// Advance far past the timeout and hint the injected scan directly. This
	// predicate fixture deliberately does not dispatch its seeded source; the
	// production transport/readiness path is checked by notification tests.
	clk.Advance(DrainStallTimeout * 10)
	d.hint()
	d.releaseKick(t)
	d.assertParked(t, "live work must never be cut even past the timeout")

	if gotEvents := collectStallWarnings(sess); len(gotEvents) != 0 {
		t.Fatalf("live work must emit no stall warning, got %v", gotEvents)
	}

	cancel()
	select {
	case <-d.done:
	// TRIPWIRE: awaits d.done, the drain goroutine's own exit signal, after
	// cancel(); the goroutine should observe ctx.Done() and return
	// immediately. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("drain did not exit after context cancellation")
	}
}

// TestDrainStallGiveUpRechecksTheWakeEdge pins the PRI-2441 B1 protocol on the
// stall watchdog's give-up path. The stall verdict is assembled across eight
// signals read in sequence, so it is not a snapshot; a wake raised while the
// pass scanned means the tree moved mid-verdict (e.g. a delegate packet armed
// root attention between the early and late reads). Returning on a torn verdict
// lets Close() SIGKILL an armed completion. The give-up must re-run the pass
// when the wake edge is set, exactly as the quiescence return already does.
func TestDrainStallGiveUpRechecksTheWakeEdge(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk}))
	seedOwnedDurablePending(t, sess.jobManager, "shell-wedge", jobstore.JobShell)

	// The driver single-steps a frozen fake clock with hand-synchronized
	// channels; nothing here waits on real I/O or a real clock. The context is
	// deliberately deadline-free: drainJobTreeWith returns ctx.Err() when the
	// caller's context ends, so a wall-clock deadline spanning all of this
	// test's handshake steps lets host-load scheduling turn the give-up's nil
	// return into a spurious "context deadline exceeded" at the d.err check
	// below (#2680). Every blocking handshake is instead bounded by
	// stallHandshakeTimeout (releaseKick, the recheck send, and the selects), so
	// a wedged driver still fails the test rather than hanging the process.
	d := newStallDriver(t.Context(), sess)
	d.releaseKick(t)
	d.assertParked(t, "iteration 1 must park, not fire before the timeout")

	// Enter a producer-driven pass before advancing time. Its existing kick
	// holds the scan while the real deadline expires, so no timer can race the
	// barrier and return before the mid-pass wake is installed.
	sess.notify()
	select {
	case <-d.top:
	case <-d.done:
		t.Fatal("drain returned before the resource deadline")
	case <-time.After(stallHandshakeTimeout):
		t.Fatal("drain did not reach the held producer pass")
	}
	clk.Advance(DrainStallTimeout - time.Nanosecond)
	select {
	case <-d.done:
		t.Fatal("drain returned before the resource deadline")
	default:
	}
	clk.Advance(time.Nanosecond)
	// Raise the wake after takeDrainWake and before the expired stall verdict.
	sess.notify()
	d.releaseKickSend(t)

	// The pass saw a mid-scan wake: it must re-run, not return.
	select {
	case <-d.top:
	case <-d.done:
		t.Fatal("stall give-up returned on a torn verdict: a wake was raised mid-pass and the drain did not re-check it")
	// waits for the drain to arrive at the NEXT iteration's kick, which the
	// fake-clock driver reaches immediately after the continue.
	case <-time.After(stallHandshakeTimeout):
		t.Fatal("drain neither returned nor re-ran the pass")
	}
	d.releaseKickSend(t)

	// The confirming pass finds the edge clear and the stall persisting: NOW it
	// gives up, with the one warning. This select names the state if it does not:
	// returned, re-ran, or neither. The giving-up pass only continues instead of
	// returning by consuming a fresh wake edge in its own stall-branch re-check
	// (nothing else can skip it here), so a re-run names a wake that landed
	// between this pass's top-of-loop take and that re-check — where the old
	// handshake could only report a bare 30s timeout (#4204).
	select {
	case <-d.done:
	case <-d.top:
		// The drain re-ran the confirming pass instead of returning. Hand the
		// unexpected pass its release (the same bounded rendezvous every other
		// step of this driver uses) so the drain goroutine is not left parked on
		// <-d.release, then report the named state rather than the ambiguous
		// timeout the old handshake gave.
		d.releaseKickSend(t)
		t.Fatal("drain re-ran the confirming pass instead of giving up: a wake landed between its top-of-loop take and its stall check")
	case <-time.After(stallHandshakeTimeout):
		t.Fatal("drain neither returned nor re-ran on the confirming pass: it is parked in waitDrainWake or blocked inside the pass")
	}
	if d.err != nil {
		t.Fatalf("give-up must return nil error, got %v", d.err)
	}
	warnings := collectStallWarnings(sess)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Data.(events.WarningData).Message, "shell-wedge") {
		t.Fatalf("want one stall warning naming shell-wedge, got %+v", warnings)
	}
}
