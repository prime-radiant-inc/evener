package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// wedgedDelegateFixture is a root with exactly one direct stable delegate that
// is mid-run and tracked as a running subagent — the shape a delegate parked
// inside an uninterruptible tool call presents to its parent.
//
// The controller's clock IS the session's clock (production wires
// now: s.sclock().Now), so a stop's recorded request time and the drain's
// reading of it come from one source and the fake clock governs both.
type wedgedDelegateFixture struct {
	root       *Session
	child      *Session
	delegateID string
	lease      delegateLease
	clk        *agenttest.FakeClock
}

func newWedgedDelegateFixture(t *testing.T, delegateID string) *wedgedDelegateFixture {
	t.Helper()
	return newWedgedDelegateFixtureIn(t, delegateID, true)
}

// newWedgedDelegateFixtureIn builds the fixture in a one-shot run
// (turnEndsProcess) or in a long-lived serve session.
func newWedgedDelegateFixtureIn(t *testing.T, delegateID string, turnEndsProcess bool) *wedgedDelegateFixture {
	t.Helper()
	// Registered BEFORE the sessions, so this restore runs AFTER their closes
	// (t.Cleanup is LIFO). A wedged delegate's stop can never settle — that is
	// the fixture's whole point — so every teardown here would otherwise spend
	// the full shipped close budget waiting for a stop that cannot complete.
	oldBudget := LaneClosePassBudget
	LaneClosePassBudget = 10 * time.Millisecond
	t.Cleanup(func() { LaneClosePassBudget = oldBudget })

	clk := agenttest.NewFakeClock()
	root := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true, TurnEndsProcess: turnEndsProcess}))
	child := newSession(t, withConfig(SessionConfig{clock: clk, NoProjectPrompts: true, TurnEndsProcess: turnEndsProcess}))

	descriptor := stableToolDescriptor(root, delegateID, "")
	descriptor.ChildSessionID = child.ID()
	c := root.delegateController
	c.mu.Lock()
	_, err := c.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: delegateID,
			Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
		},
		delegateControllerRunStartedEvent(delegateID, 1, delegatestore.TriggerInitial, clk.Now()),
	)
	lease := delegateLease{delegateID: delegateID, generation: 1}
	// A ready binding is what makes the lease ADMISSIBLE, which is the whole
	// point: without it every ReportActivity would be refused as a stale lease
	// and the stop fence this fixture exists to demonstrate would be invisible.
	c.live[delegateID] = &delegateLiveState{
		runtime:    child,
		binding:    &delegateRuntimeBinding{lease: lease, runtime: child, cancel: func() {}, ready: true},
		activityAt: clk.Now(),
	}
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	root.subagents.mu.Lock()
	root.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child, running: true}
	root.subagents.mu.Unlock()
	t.Cleanup(func() {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, child.ID())
		root.subagents.mu.Unlock()
	})
	return &wedgedDelegateFixture{root: root, child: child, delegateID: delegateID, lease: lease, clk: clk}
}

// requestStop runs the REAL stop path — the one job_stop reaches — so the
// pending-stop timestamp the drain reads is the one production writes.
func (f *wedgedDelegateFixture) requestStop(t *testing.T) {
	t.Helper()
	if _, _, _, err := f.root.delegateController.StopSubtree(rootDelegateActor(f.root.ID()), f.delegateID); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
}

// completeStop settles the stop the way a target that CAN honour it does: the
// run finishes as stopped_by_parent through the controller's own finish path,
// then the subtree stop completes.
func (f *wedgedDelegateFixture) completeStop(t *testing.T) {
	t.Helper()
	c := f.root.delegateController
	requestSeq := f.pendingStopSeq(t)
	if _, err := c.FinishGeneration(f.lease, delegateFinish{
		outcome: delegatestore.OutcomeStopped,
		reason:  "stopped_by_parent",
		endedAt: f.clk.Now(),
	}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.live, f.delegateID)
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:                 delegatestore.EventDelegateSubtreeStopCompleted,
		DelegateID:           f.delegateID,
		SubtreeStopCompleted: &delegatestore.SubtreeStopCompleted{RequestSeq: requestSeq},
	}); err != nil {
		t.Fatalf("complete stop: %v", err)
	}
}

// reportAndLeaveStopPending is the race a stop always has with the work it is
// trying to stop: the delegate finishes REPORTING, with its terminal packet
// queued for the parent, while the stop request is still outstanding.
func (f *wedgedDelegateFixture) reportAndLeaveStopPending(t *testing.T) {
	t.Helper()
	packet := delegatestore.TerminalPacket{
		Kind:    delegatestore.PacketReported,
		Message: json.RawMessage(`"child report"`),
	}
	if _, err := f.root.delegateController.FinishGeneration(f.lease, delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionReported,
		packet:      &packet,
		endedAt:     f.clk.Now(),
	}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if seq := f.pendingStopSeq(t); seq == 0 {
		t.Fatal("the stop must still be pending after the run finishes")
	}
}

func (f *wedgedDelegateFixture) snapshot(t *testing.T) delegateSnapshot {
	t.Helper()
	row, ok := f.root.directStableDelegateForChildSession(f.child.ID())
	if !ok {
		t.Fatalf("delegate %s is not a direct stable delegate of the root", f.delegateID)
	}
	return row
}

// setSubagentRunning sets the parent's view of whether the child is mid-run.
func (f *wedgedDelegateFixture) setSubagentRunning(running bool) {
	f.root.subagents.mu.Lock()
	sub := f.root.subagents.subs[f.child.ID()]
	f.root.subagents.mu.Unlock()
	sub.mu.Lock()
	sub.running = running
	sub.mu.Unlock()
}

func (f *wedgedDelegateFixture) pendingStopSeq(t *testing.T) uint64 {
	t.Helper()
	c := f.root.delegateController
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[f.delegateID]
	if aggregate == nil {
		t.Fatalf("delegate %s has no aggregate", f.delegateID)
	}
	return aggregate.PendingStopSeq
}

// TestStopRequestedDelegateCannotReportActivity is the finding the fix rests on:
// a delegate under a pending subtree stop CANNOT stamp parent-observable
// activity, because admitLeaseLocked refuses every lease while PendingStopSeq is
// set. #378 built its "unresponsive" test on the opposite assumption — that a
// winding-down delegate keeps stamping and so keeps counting as live — and that
// invariant is unreachable through the real admission path.
//
// This is the reason the shipped predicate is time-since-stop-requested rather
// than time-since-activity: the activity clock cannot move after a stop, so
// reading it was reading the last PRE-stop stamp and calling it liveness.
func TestStopRequestedDelegateCannotReportActivity(t *testing.T) {
	f := newWedgedDelegateFixture(t, "dlg_fenced")
	c := f.root.delegateController

	if err := c.ReportActivity(f.lease, f.clk.Now()); err != nil {
		t.Fatalf("precondition: a running delegate must be able to report activity, got %v", err)
	}
	f.requestStop(t)
	preStop := f.clk.Now()

	for i := range 5 {
		f.clk.Advance(30 * time.Second)
		err := c.ReportActivity(f.lease, f.clk.Now())
		if !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("post-stop ReportActivity %d = %v, want %v: if this ever succeeds, a stop-requested delegate CAN stamp activity and the predicate should read it again", i, err, errDelegateTargetBusy)
		}
	}

	c.mu.Lock()
	stamped := c.live[f.delegateID].activityAt
	c.mu.Unlock()
	if !stamped.Equal(preStop) {
		t.Fatalf("activity stamp = %s, want the pre-stop stamp %s: the fence must have frozen it", stamped, preStop)
	}
}

// TestDrainAbandonsStopRequestedDelegateGoneUnresponsive is the drain half of
// the 109-minute field hang (#317, #369): the root asked to stop a delegate, the
// delegate was wedged inside an uninterruptible tool call so the stop degraded
// to stop_pending forever, and the drain kept counting the delegate as live work
// until an external killer fired. Once the stop has gone unanswered for the
// bound the delegate is undisposable, and the drain must announce and stop
// waiting rather than waiting for the process to be killed.
//
// It drives the whole verdict through the drain loop — markDrainAbandonedDelegates
// is what stamps the abandonment, off the real snapshot, from the real stop
// request — rather than asking a predicate a question production never asks it.
func TestDrainAbandonsStopRequestedDelegateGoneUnresponsive(t *testing.T) {
	f := newWedgedDelegateFixture(t, "dlg_wedged")
	f.requestStop(t)

	outstanding, err := f.root.treeHasOutstandingWork()
	if err != nil || !outstanding {
		t.Fatalf("precondition: a running delegate must be outstanding work, got outstanding=%v err=%v", outstanding, err)
	}
	if stalled, err := f.root.drainSubtreeIsStalled(); err != nil || stalled {
		t.Fatalf("a delegate whose stop was just requested must still be live, got stalled=%v err=%v", stalled, err)
	}

	// TRIPWIRE: the driver single-steps a frozen fake clock with hand-
	// synchronized channels; nothing here waits on real I/O or a real clock.
	// 30s only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d := newStallDriver(ctx, f.root)
	d.releaseKick(t)
	d.assertParked(t, "an unexpired stop request is not permission to abandon")

	// The stop has now gone unanswered for the whole bound.
	f.clk.Advance(DrainStallTimeout + time.Second)
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
	if d.res != "" {
		t.Fatalf("drain result = %q, want the last turn's result — empty here because no drain turn ran; this value is what run.go prints", d.res)
	}
	if !f.root.childDrainAbandoned(f.child.ID()) {
		t.Fatal("the drain returned without recording the abandonment, so its drive paths and its warning cannot agree with its liveness check")
	}

	warnings := collectStallWarnings(f.root)
	if len(warnings) == 0 {
		t.Fatal("abandoning a delegate must be announced, never silent")
	}
	named := false
	for _, w := range warnings {
		warning, ok := w.Data.(events.WarningData)
		if ok && warning.Code == events.WarningCodeDelegateAbandonedByDrain && warning.DelegateID == f.delegateID {
			named = true
		}
	}
	if !named {
		t.Fatalf("no warning named the abandoned delegate %s: %+v", f.delegateID, warnings)
	}
}

// TestDrainKeepsWaitingOnADelegateWhoseStopCompletes is the regression guard the
// requirement calls for, rebuilt to pin a state production can actually reach.
//
// #378's version stamped activity by writing live[id].activityAt directly, which
// the admission fence forbids after a stop — so it pinned a state nothing could
// produce and passed with the fix reverted. The reachable version of "a stop is
// not by itself permission to abandon" is this: a delegate that HONOURS its stop
// inside the bound settles, and the drain must never have abandoned it, however
// far past the bound the clock then runs.
func TestDrainKeepsWaitingOnADelegateWhoseStopCompletes(t *testing.T) {
	f := newWedgedDelegateFixture(t, "dlg_winding_down")
	f.requestStop(t)
	if f.pendingStopSeq(t) == 0 {
		t.Fatal("precondition: the stop request must be pending")
	}

	ctx, cancel := context.WithCancel(context.Background())
	d := newStallDriver(ctx, f.root)
	d.releaseKick(t)
	d.assertParked(t, "a stop still inside its bound must be waited on")

	// The delegate winds down and honours the stop with time to spare.
	f.clk.Advance(DrainStallTimeout - time.Second)
	f.completeStop(t)

	// Well past the bound in absolute time; the stop is settled, so there is
	// nothing left to abandon.
	f.clk.Advance(DrainStallTimeout * 4)
	d.recheck <- time.Time{}
	d.releaseKick(t)

	if f.root.childDrainAbandoned(f.child.ID()) {
		t.Fatal("a delegate that honoured its stop inside the bound was abandoned anyway")
	}
	for _, w := range collectStallWarnings(f.root) {
		if warning, ok := w.Data.(events.WarningData); ok && warning.Code == events.WarningCodeDelegateAbandonedByDrain {
			t.Fatalf("a delegate that honoured its stop must emit no abandonment event, got %#v", warning)
		}
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

// TestDrainArmsBothEscapesWithAShellAndAWedgedDelegate is #382 item 3 — the
// #310/#311 intersection both reviews reproduced independently. One running
// background shell kept subtreeHasLiveComponent true, so the stall watchdog
// never armed; the same tree's wedged delegate kept
// treeHasOutstandingWorkBesidesOwnJobs true, so the undisposed-background-job
// ladder's "these jobs are the SOLE reason I am waiting" test never passed and
// IT never armed either. Sixty simulated minutes, both escapes dead, forever.
//
// Threading the abandonment verdict into the sole computation is what breaks it:
// once the delegate stops counting, the shell IS the sole reason to wait and the
// announce ladder runs.
func TestDrainArmsBothEscapesWithAShellAndAWedgedDelegate(t *testing.T) {
	f := newWedgedDelegateFixture(t, "dlg_wedged_beside_shell")
	f.requestStop(t)
	seedRunningBackgroundShell(t, f.root.jobManager, "shell_bg", f.clk.Now())

	if _, sole, err := f.root.undisposedBackgroundDrainJobs(); err != nil || sole {
		t.Fatalf("precondition: a live delegate is a real second reason to wait, got sole=%v err=%v", sole, err)
	}
	if live, err := f.root.subtreeHasLiveComponent(); err != nil || !live {
		t.Fatalf("precondition: the running shell is a live component, got live=%v err=%v", live, err)
	}

	// WATCHED RED before the sole computation learned about abandonment: the
	// two families suppressed each other and this stayed false forever.
	drainStartedAt := f.clk.Now()
	f.clk.Advance(DrainStallTimeout + time.Second)
	f.root.markDrainAbandonedDelegates(f.clk.Now(), drainStartedAt)
	undisposed, sole, err := f.root.undisposedBackgroundDrainJobs()
	if err != nil {
		t.Fatalf("backgroundDrainState: %v", err)
	}
	if !sole {
		t.Fatal("with the wedged delegate abandoned the background shell is the SOLE reason the drain still waits, so the undisposed-job ladder must arm")
	}
	if len(undisposed) != 1 || undisposed[0] != "shell_bg" {
		t.Fatalf("undisposed = %v, want just the background shell", undisposed)
	}
}

// TestDrainNeverAbandonsADelegateWhoseRunFinished pins the "with no
// run-completion" half of the predicate, on a snapshot taken from the real
// store after a real finish. #378 abandoned on a pending stop plus a stale
// activity stamp alone, and said nothing about whether the run had ended — so a
// delegate that had ALREADY FINISHED under a still-pending stop qualified, and
// the drain called deliverable terminal work a wedge (#382 item 4's
// queued-notification shape).
//
// A finished run is not a wedge; there is a packet to hand to the parent. The
// clause is asserted at ten bounds past the stop request, where nothing but
// currentRunOpen can be keeping the answer false.
func TestDrainNeverAbandonsADelegateWhoseRunFinished(t *testing.T) {
	f := newWedgedDelegateFixture(t, "dlg_finished_under_stop")
	f.requestStop(t)
	drainStartedAt := f.clk.Now()

	open := f.snapshot(t)
	if !open.currentRunOpen {
		t.Fatal("precondition: the run must be open before the finish")
	}
	f.clk.Advance(DrainStallTimeout * 10)
	if !delegateAbandonedByDrain(open, f.clk.Now(), drainStartedAt) {
		t.Fatal("precondition: an open run under a long-unanswered stop IS abandonable, or this test proves nothing")
	}

	f.reportAndLeaveStopPending(t)
	finished := f.snapshot(t)
	if finished.currentRunOpen {
		t.Fatal("the finish did not close the run")
	}
	if delegateAbandonedByDrain(finished, f.clk.Now(), drainStartedAt) {
		t.Fatal("a delegate whose run has FINISHED was abandoned: its terminal packet is deliverable work, not a wedge")
	}
}

// TestDrainDoesNotDriveAChildItHasAbandoned is #382 item 4's other half: the
// drain must not declare a child not-live and then keep kicking it. #378 put
// the abandonment gate only on the liveness walk, so the drive loop went on
// trying to deliver into a subtree the drain had already given up on and the
// give-up warning named children it was still driving.
//
// The drive's own observable is settleDrivenChildForwardedPendings, which runs
// synchronously on a successful handoff: a forwarded pending left NotifyPending
// is proof no drive was launched.
func TestDrainDoesNotDriveAChildItHasAbandoned(t *testing.T) {
	f := newWedgedDelegateFixture(t, "dlg_abandoned_but_driven")
	f.requestStop(t)
	// The parent's row goes idle while the delegate's run is still open — the
	// hand-off straddle the drain loop's own wake protocol documents — so the
	// drive is not blocked by the row's busy flags and the gate is what decides.
	f.setSubagentRunning(false)
	seedForwardedChildPending(t, f.root.jobManager, "job_child_tail", f.child.ID())
	f.child.enqueueJobNotification(jobNotification{JobID: "job_child_tail", Status: "completed"})

	f.clk.Advance(DrainStallTimeout + time.Second)
	f.root.markDrainAbandonedDelegates(f.clk.Now(), f.clk.Now().Add(-DrainStallTimeout-time.Second))
	if !f.root.childDrainAbandoned(f.child.ID()) {
		t.Fatal("precondition: the wedged delegate must be abandoned")
	}

	f.root.driveChildrenWithUndeliveredAttention()
	if state := forwardedPendingNotifyState(t, f.root.jobManager, "job_child_tail"); state != jobstore.NotifyPending {
		t.Fatalf("forwarded pending notify state = %q, want %q: the drain drove a child it had already abandoned", state, jobstore.NotifyPending)
	}
	if live, err := f.root.subtreeHasLiveComponent(); err != nil || live {
		t.Fatalf("the same child must read not-live to the liveness walk, got live=%v err=%v", live, err)
	}
	if got := f.root.subtreeUndisposableStoppedDelegates(); len(got) != 1 || got[0] != f.delegateID {
		t.Fatalf("give-up warning would name %v, want exactly the abandoned delegate %s", got, f.delegateID)
	}
}

// TestServeSessionAbandonsAStopRequestedDelegateAfterBound is the serve-mode
// policy: an explicit stop request is bounded in a long-lived session too. The
// one-shot-only fallback for a delegate the root never stopped remains covered
// by the one-shot control below.
func TestServeSessionAbandonsAStopRequestedDelegateAfterBound(t *testing.T) {
	f := newWedgedDelegateFixtureIn(t, "dlg_serve_wedged", false)
	f.requestStop(t)

	drainStartedAt := f.clk.Now()
	f.clk.Advance(DrainStallTimeout * 30)
	f.root.markDrainAbandonedDelegates(f.clk.Now(), drainStartedAt)

	if !f.root.childDrainAbandoned(f.child.ID()) {
		t.Fatal("a serve session kept waiting on a stop-requested delegate past the bound")
	}
	if live, err := f.root.subtreeHasLiveComponent(); err != nil || live {
		t.Fatalf("the abandoned delegate must no longer count as live, got live=%v err=%v", live, err)
	}
	// The same fixture in a one-shot run is abandoned as well.
	oneShot := newWedgedDelegateFixture(t, "dlg_oneshot_wedged")
	oneShot.requestStop(t)
	oneShotStart := oneShot.clk.Now()
	oneShot.clk.Advance(DrainStallTimeout * 30)
	oneShot.root.markDrainAbandonedDelegates(oneShot.clk.Now(), oneShotStart)
	if !oneShot.root.childDrainAbandoned(oneShot.child.ID()) {
		t.Fatal("control: the same shape in a one-shot run must be abandoned")
	}
}

func TestDrainAbandonmentDoesNotFenceSuccessorGeneration(t *testing.T) {
	f := newWedgedDelegateFixtureIn(t, "dlg_abandonment_successor", false)
	f.requestStop(t)
	drainStartedAt := f.clk.Now()
	f.clk.Advance(DrainStallTimeout + time.Second)
	f.root.markDrainAbandonedDelegates(f.clk.Now(), drainStartedAt)
	if !f.root.childDrainAbandoned(f.child.ID()) {
		t.Fatal("precondition: generation one must be abandoned")
	}

	result, err := f.root.delegateController.FinishGeneration(f.lease, delegateFinish{
		outcome: delegatestore.OutcomeStopped,
		reason:  "stopped_by_parent",
		endedAt: f.clk.Now(),
	})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if err := f.root.executeDelegateMutationPlans(result); err != nil {
		t.Fatalf("execute finish plans: %v", err)
	}
	if _, err := f.root.delegateController.Reconcile(emptyDelegateReconcileEvidence(f.root.delegateController)); err != nil {
		t.Fatalf("Reconcile stop completion: %v", err)
	}

	reservation, err := f.root.delegateController.ReserveStart(rootDelegateActor(f.root.ID()), f.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart successor: %v", err)
	}
	started, err := f.root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart successor: %v", err)
	}
	if err := f.root.delegateController.AttachRuntime(started.lease, f.child); err != nil {
		t.Fatalf("AttachRuntime successor: %v", err)
	}
	if _, err := f.root.delegateController.BeginStartInput(started.lease); err != nil {
		t.Fatalf("BeginStartInput successor: %v", err)
	}

	row := f.snapshot(t)
	if row.generation != started.lease.generation || !row.currentRunOpen || row.pendingStopSeq != 0 {
		t.Fatalf("successor row = %#v, want a new running generation", row)
	}
	if f.root.childDrainAbandoned(f.child.ID()) {
		t.Fatal("generation-one abandonment fenced the resumed generation")
	}
	if outstanding, err := f.root.treeHasOutstandingWork(); err != nil || !outstanding {
		t.Fatalf("successor generation was omitted from drain accounting: outstanding=%v err=%v", outstanding, err)
	}
}

// seedForwardedChildPending records, in the PARENT's job store, a terminal
// managed job owned by the child and still owing a notification — the drive
// signal driveChildrenWithUndeliveredAttention settles on a successful handoff.
func seedForwardedChildPending(t *testing.T, jm *jobManager, jobID, childSessionID string) {
	t.Helper()
	now := jm.now()
	gen := "gen-" + jobID
	for _, event := range []jobstore.Event{
		{
			Kind: jobstore.EventJobStarted, TS: now, JobID: jobID, Type: jobstore.JobShell,
			OwnerSessionID: childSessionID, VisibleToSession: jm.sessionID, StartedAt: &now,
			Command: "true", Description: "forwarded child shell",
		},
		{Kind: jobstore.EventJobFinished, TS: now, JobID: jobID, Status: jobstore.StatusCompleted, Reason: "completed", EndedAt: &now, TerminalGen: gen},
		{Kind: jobstore.EventJobNotificationPending, TS: now, JobID: jobID, TerminalGen: gen},
	} {
		if err := jm.appendEvent(event); err != nil {
			t.Fatalf("seed forwarded pending %q: %v", jobID, err)
		}
	}
}

func forwardedPendingNotifyState(t *testing.T, jm *jobManager, jobID string) jobstore.NotifyState {
	t.Helper()
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load job store: %v", err)
	}
	rec := recs[jobID]
	if rec == nil {
		t.Fatalf("job %s is not in the store", jobID)
	}
	return rec.NotifyState
}

// seedRunningBackgroundShell puts a live background shell in the running map,
// the way launching one with mode="background" does.
func seedRunningBackgroundShell(t *testing.T, jm *jobManager, id string, startedAt time.Time) {
	t.Helper()
	rec := &jobstore.JobRecord{
		JobID: id, Type: jobstore.JobShell, Status: jobstore.StatusRunning,
		OwnerSessionID: jm.sessionID, VisibleToSession: jm.sessionID,
		Command: "sleep forever", StartedAt: startedAt, Background: true,
	}
	jm.mu.Lock()
	jm.running[id] = &runningJob{rec: rec}
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, id)
		jm.mu.Unlock()
	})
}
