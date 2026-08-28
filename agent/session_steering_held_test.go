package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// TestStopParksPendingUserSteering is the #174 rail: a Stop that lands at a
// turn boundary is Applied, and without the SteeringHeld gate the still-OPEN
// steering rail restarts the session and delivers the steer to the model
// anyway. The steer must stay parked -- no model round, no restart -- until the
// user asks for something to run.
//
// The steer stays in PendingExecutions/SteeringOrder where it already durably
// lives, so its causal provenance is never at risk (issue #146, Option C —
// park in place). Delivery stays a steering injection, not a converted new
// instruction.
func TestStopParksPendingUserSteering(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	// A turn is running, the user steers it, and the steer has not been injected
	// yet -- injection happens at a round boundary, so this is the state during
	// any long tool call.
	turnID := runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("the steer did not land as pending user steering; this test is not in the state it means to be")
	}

	cancels := 0
	response, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-pending-steering",
	}, func() { cancels++ })
	if err != nil {
		t.Fatalf("Stop with a steer in flight was refused: %v", err)
	}
	if cancels != 1 {
		t.Fatalf("Stop cancelled %d times, want exactly 1", cancels)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("Stop disposition = %q, want %q", response.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	if response.Receipt.TurnID != turnID {
		t.Fatalf("Stop receipt turn = %q, want the running turn %q", response.Receipt.TurnID, turnID)
	}

	// The gate is set: the steer is parked behind SteeringHeld.
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("Stop did not set SteeringHeld; the steering rail is still open and the steer will be delivered anyway")
	}

	// The pending-user-input wake is what would restart the session to deliver
	// the steer. Install it AFTER the Stop so the count reflects only post-Stop
	// wakes: at install time the steer is pending and the gate is set, so a
	// wake that respects the gate fires zero times here.
	var mu sync.Mutex
	wakes := 0
	sess.SetPendingUserInputWakeFunc(func() {
		mu.Lock()
		wakes++
		mu.Unlock()
	})

	// The steer is still durably pending -- it never moved storage, so its
	// causal provenance is intact (issue #146).
	if !sess.hasPendingUserSteering() {
		t.Fatal("the Stop consumed the user's steering: they typed it, it never reached the model, and it is gone")
	}
	snapshot := sess.clientMutations.snapshot()
	if pending, ok := snapshot.PendingExecutions["steer-mid-round"]; !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("the parked steer is not still accepted in PendingExecutions: %#v", snapshot.PendingExecutions)
	}

	// wakeForPendingSteering must NOT fire while held: a fire here would restart
	// the session and deliver the steer to the model anyway -- the open
	// steering rail this gate exists to close.
	sess.wakeForPendingSteering()
	mu.Lock()
	if wakes != 0 {
		t.Fatalf("wakeForPendingSteering fired %d time(s) while SteeringHeld is set; the parked steer restarts the session", wakes)
	}
	mu.Unlock()

	// claimSteeringCarrierTurn must refuse to claim while held: claiming would
	// hand the steer to a turn the Stop just ended.
	if claimedID, ok := sess.claimSteeringCarrierTurn(); ok {
		t.Fatalf("claimSteeringCarrierTurn claimed turn %q while SteeringHeld is set; the parked steer is delivered to a turn the Stop just ended", claimedID)
	}

	// A parked steer must not make the session look busy: WireState reports
	// idle, because nothing will move the steer until the user acts.
	if got := sess.WireState(); got != string(SessionIdle) {
		t.Fatalf("WireState with a parked steer = %q, want %q; a parked steer is waiting on the user, not work in progress", got, SessionIdle)
	}
}

// TestTurnStartReleasesTheParkedSteer is the release half: a user-initiated
// turn/start clears SteeringHeld, so the opening turn drains the parked steer
// at its boundary and delivers it to the model.
func TestTurnStartReleasesTheParkedSteer(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	cancels := 0
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-pending-steering",
	}, func() { cancels++ }); err != nil {
		t.Fatalf("Stop with a steer in flight was refused: %v", err)
	}
	if cancels != 1 {
		t.Fatalf("Stop cancelled %d times, want exactly 1", cancels)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("precondition: Stop did not set SteeringHeld")
	}

	// The user speaks again. turn/start is the user asking for something to
	// run, so the wait the Stop started is over -- for the queue AND the steer.
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "next turn"}},
	}); err != nil {
		t.Fatalf("turn/start after the Stop: %v", err)
	}

	// The gate is cleared.
	if sess.clientMutations.steeringHeld() {
		t.Fatal("turn/start did not clear SteeringHeld; the parked steer stays parked even after the user asked for a turn to run")
	}

	// wakeForPendingSteering now fires again -- the steer is no longer parked,
	// so the opening turn will drain it at its boundary.
	var mu sync.Mutex
	wakes := 0
	sess.SetPendingUserInputWakeFunc(func() {
		mu.Lock()
		wakes++
		mu.Unlock()
	})
	sess.wakeForPendingSteering()
	mu.Lock()
	if wakes == 0 {
		t.Fatal("wakeForPendingSteering did not fire after SteeringHeld was cleared; the parked steer is never delivered")
	}
	mu.Unlock()
}

// TestStopParksPendingUserSteering_GuardMutation pins that the SteeringHeld
// guard in wakeForPendingSteering is load-bearing: deleting it turns this test
// red. Run with the guard removed from wakeForPendingSteering and the wake
// fires while held, which the assertion rejects.
//
// This is the mutation test for requirement (c): deleting the SteeringHeld
// guard turns a test red.
func TestStopParksPendingUserSteering_GuardMutation(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	cancels := 0
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-pending-steering",
	}, func() { cancels++ }); err != nil {
		t.Fatalf("Stop with a steer in flight was refused: %v", err)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("precondition: Stop did not set SteeringHeld")
	}

	var mu sync.Mutex
	wakes := 0
	sess.SetPendingUserInputWakeFunc(func() {
		mu.Lock()
		wakes++
		mu.Unlock()
	})

	// This is the call site the guard protects: the interrupt's tail calls it
	// unconditionally, and so does every steer/drain/promote path. With the guard
	// in place it no-ops while held; without the guard it fires and the parked
	// steer restarts the session.
	sess.wakeForPendingSteering()

	mu.Lock()
	defer mu.Unlock()
	if wakes != 0 {
		t.Fatalf("the SteeringHeld guard in wakeForPendingSteering is missing: the wake fired %d time(s) while the steer is parked, restarting the session and delivering the steer the user just stopped", wakes)
	}
}

// TestTurnStartClearMutation pins that the SteeringHeld clear on turn/start is
// load-bearing: deleting it turns this test red. Run with the
// `snapshot.SteeringHeld = false` line removed from AcceptClientMutationStart
// and the gate stays set after turn/start, which the assertion rejects.
//
// This is the mutation test for requirement (d): deleting a clear turns a test
// red.
func TestTurnStartClearMutation(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	cancels := 0
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-pending-steering",
	}, func() { cancels++ }); err != nil {
		t.Fatalf("Stop with a steer in flight was refused: %v", err)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("precondition: Stop did not set SteeringHeld")
	}

	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "next turn"}},
	}); err != nil {
		t.Fatalf("turn/start after the Stop: %v", err)
	}

	if sess.clientMutations.steeringHeld() {
		t.Fatal("the SteeringHeld clear on turn/start is missing: the gate stays set after the user asked for a turn to run, so the parked steer is never delivered")
	}
}

// TestASecondSteerWhileHeldStaysParked is the RCA's named trap (#146
// Option C, "the one a naive implementation gets wrong"): a naive "clear
// SteeringHeld everywhere QueueHeld is cleared" reading would clear it on
// clientMutationSteer's own accept path, and that path calls
// wakeForPendingSteering unconditionally -- reproducing the #146 restart,
// retriggered by a second steer instead of by Stop. A steer arriving while
// held must append to SteeringOrder and stay parked with the rest.
func TestASecondSteerWhileHeldStaysParked(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-one",
		Input:            []appwire.InputItem{{Type: "text", Text: "first redirect"}},
	}); err != nil {
		t.Fatalf("first steer: %v", err)
	}
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-steering",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("precondition: Stop did not set SteeringHeld")
	}

	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-two",
		Input:            []appwire.InputItem{{Type: "text", Text: "second redirect"}},
	}); err != nil {
		t.Fatalf("second steer: %v", err)
	}

	if !sess.clientMutations.steeringHeld() {
		t.Fatal("a second steer accepted while held cleared SteeringHeld: this re-triggers the #146 restart bug via a second steer instead of Stop")
	}
	if claimedID, ok := sess.claimSteeringCarrierTurn(); ok {
		t.Fatalf("claimSteeringCarrierTurn claimed turn %q after a second steer arrived while held", claimedID)
	}
}

// TestDrainAsSteerReleasesTheParkedSteer is drainAsSteer's half of "sending
// again releases a held steer" (RCA clear-trigger list). The queued entry is
// added BEFORE the Stop so this test exercises drainAsSteer's own clear, not
// turn/queue's.
func TestDrainAsSteerReleasesTheParkedSteer(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	queueOneMutation(t, sess, "queued-behind", "and then this")
	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-both",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !sess.clientMutations.steeringHeld() || !sess.clientMutations.queueHeld() {
		t.Fatal("precondition: Stop did not park both the queue and the steer")
	}

	revision := sess.clientMutations.snapshot().QueueRevision
	if _, err := sess.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{
		ClientMutationID:      "drain-after-stop",
		ExpectedQueueRevision: revision,
	}); err != nil {
		t.Fatalf("drainAsSteer: %v", err)
	}

	if sess.clientMutations.steeringHeld() {
		t.Fatal("drainAsSteer left SteeringHeld set: the user asked for the queue to run, and the parked steer should release with it")
	}
}

// TestPromoteQueuedAsSteerReleasesTheParkedSteer is promoteQueuedAsSteer's
// half of the same clear-trigger list.
func TestPromoteQueuedAsSteerReleasesTheParkedSteer(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	queueOneMutation(t, sess, "queued-behind", "and then this")
	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-both",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !sess.clientMutations.steeringHeld() || !sess.clientMutations.queueHeld() {
		t.Fatal("precondition: Stop did not park both the queue and the steer")
	}

	if _, err := sess.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{
		ClientMutationID: "promote-after-stop",
		Index:            0,
	}); err != nil {
		t.Fatalf("promoteQueuedAsSteer: %v", err)
	}

	if sess.clientMutations.steeringHeld() {
		t.Fatal("promoteQueuedAsSteer left SteeringHeld set: the user picked something to run now, and the parked steer should release with it")
	}
}

// TestDelegateSteerCallerUnaffectedByHold is the scope-predicate proof (#146):
// enqueueDelegateCallerSteeringDurably (real Provenance, Source=="") never
// touches PendingExecutions/SteeringOrder, the store SteeringHeld gates, so a
// hold on the user's own steering must not block, clear, or otherwise
// interact with delegate SteerCaller traffic.
func TestDelegateSteerCallerUnaffectedByHold(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-steering",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("precondition: Stop did not set SteeringHeld")
	}

	before := len(sess.SteeringQueueSnapshot())
	prov := &provenance.Causal{Chain: []provenance.Entry{{Kind: "job", JobID: "job-123"}}}
	if err := sess.enqueueDelegateCallerSteeringDurably("delegate says redirect", prov); err != nil {
		t.Fatalf("enqueueDelegateCallerSteeringDurably was blocked while SteeringHeld: %v", err)
	}

	after := sess.SteeringQueueSnapshot()
	if len(after) != before+1 {
		t.Fatalf("delegate steering did not land in the in-memory queue while held: got %d entries, want %d", len(after), before+1)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("a delegate steer while held cleared SteeringHeld: the store is scoped to user steering only and must not react to delegate traffic")
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("hasPendingUserSteering went false after a delegate steer landed; the user's own held steer must still count")
	}

	// The delegate entry is delivered by the ungated injectDrainedSteering
	// regardless of the hold -- proving delivery is not blocked structurally.
	sess.injectDrainedSteering()
	found := false
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && turn.Message.Text() == "delegate says redirect" {
			found = true
		}
	}
	if !found {
		t.Fatal("the delegate steer was never delivered into the transcript even though SteeringHeld only gates the durable user-steering claim path")
	}
}

// TestAHeldSteerSurvivesRestart pins restart safety (#174): SteeringHeld is a
// plain field on the durable snapshot, deliberately with no process-local
// mirror (the QueueHeld review found the first attempt at that pattern
// drifting on three of four writers). It must survive a full daemon restart
// through the production resume path, and the claim gate must still refuse
// after restore.
func TestAHeldSteerSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-steering",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("precondition: Stop did not set SteeringHeld")
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	if !restored.clientMutations.steeringHeld() {
		t.Fatal("SteeringHeld did NOT survive a restart: the parked steer will be delivered on the next wake after restore")
	}
	if !restored.hasPendingUserSteering() {
		t.Fatal("the restored session lost the parked user steer entirely")
	}
	if claimedID, ok := restored.claimSteeringCarrierTurn(); ok {
		t.Fatalf("claimSteeringCarrierTurn claimed turn %q on a freshly restored session with SteeringHeld set", claimedID)
	}
}

// TestParkedSteerStillProjectsAsAccepted is the wire-visibility half of
// Option C (#146): the parked steer must keep projecting as a
// PendingMutation with ExecutionState=="accepted" (never "claimed"), the
// state ClientMutationProjection's own exclusion only drops for
// method==turn/start||turn/queue AND state==incorporated -- so PendingChips
// keeps rendering it.
func TestParkedSteerStillProjectsAsAccepted(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-steering",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	_, pending := sess.ClientMutationProjection()
	var found *appwire.PendingMutation
	for i := range pending {
		if pending[i].ClientMutationID == "steer-mid-round" {
			found = &pending[i]
		}
	}
	if found == nil {
		t.Fatal("the parked steer disappeared from ClientMutationProjection's PendingMutations -- PendingChips has nothing to render")
	}
	if found.ExecutionState != "accepted" {
		t.Fatalf("parked steer ExecutionState = %q, want %q (never claimed while held)", found.ExecutionState, "accepted")
	}
}

// TestDrainJobTreeDoesNotHangWithAHeldSteer guards the exit path: DrainJobTree
// must not hang or spin forever with SteeringHeld set and a steer durably
// pending, since neither treeHasOutstandingWork nor drainSubtreeIsStalled
// have any notion of queue/steering state -- the drain must simply see
// nothing outstanding and return.
func TestDrainJobTreeDoesNotHangWithAHeldSteer(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-steering",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("precondition: Stop did not set SteeringHeld")
	}

	// TRIPWIRE: this test awaits the real completion signal (done, below) --
	// the bound is a hang guard only, sized well above the sub-millisecond
	// work this session actually has (no managed jobs, no delegates), so it
	// fires only if DrainJobTree genuinely wedges.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	var result string
	var err error
	go func() {
		result, err = sess.DrainJobTree(ctx)
		close(done)
	}()
	select {
	case <-done:
		if err != nil {
			t.Fatalf("DrainJobTree returned an error with steering held: %v", err)
		}
		if result != "" {
			t.Fatalf("DrainJobTree ran a turn (%q) it should not have with nothing outstanding but a held steer", result)
		}
	case <-ctx.Done():
		t.Fatal("DrainJobTree hung with SteeringHeld set and a pending user steer -- exit would SIGKILL the child instead of returning")
	}
}

// TestStopWithNothingToParkLeavesTheSteeringRailOpen is issue #710: the Stop
// in the live turn-control e2e had no pending user steering to park, armed
// SteeringHeld anyway, and the armed gate then swallowed the steer the user
// sent AFTERWARDS -- accepted with an Applied receipt, never delivered to the
// model, never written to the transcript a user reads the session back from.
//
// The hold exists to stop a Stop's own steer being delivered anyway (#174).
// With nothing pending there is no such steer, so arming the gate can only
// catch one the Stop never saw.
func TestStopWithNothingToParkLeavesTheSteeringRailOpen(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if sess.hasPendingUserSteering() {
		t.Fatal("this test is not in the state it means to be: user steering is already pending before the Stop")
	}

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-with-nothing-to-park",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if sess.clientMutations.steeringHeld() {
		t.Fatal("a Stop with no pending user steering armed SteeringHeld: the gate now parks whatever the user steers next, which is not what the Stop cancelled")
	}

	// The pending-user-input wake is the only thing that runs a steer with no
	// turn to land in. Install it after the Stop so the count reflects only
	// the steer sent afterwards.
	var mu sync.Mutex
	wakes := 0
	sess.SetPendingUserInputWakeFunc(func() {
		mu.Lock()
		wakes++
		mu.Unlock()
	})

	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-after-the-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "now do it this way instead"}},
	}); err != nil {
		t.Fatalf("steer after the Stop: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if wakes == 0 {
		t.Fatal("nothing woke for a steer sent after the Stop: the daemon accepted it and it will never reach the model or the transcript")
	}
}

// TestARestoredHoldOverAnEmptyRailIsReleased is #710's upgrade half. The
// unconditional Stop parked steering with nothing to park, and that hold
// outlives the fix on disk: restore reads it back and every steer the session
// accepts from then on is parked behind a gate naming nothing, delivered
// never. Setting the flag conditionally is not enough -- it has to be
// normalized from what is actually pending, on Stop and on restore.
func TestARestoredHoldOverAnEmptyRailIsReleased(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	serveSession(t, sess)

	// Exactly what the old `snapshot.SteeringHeld = true` left on disk after a
	// Stop with an empty steering rail.
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.SteeringHeld = true
		return nil
	}); err != nil {
		t.Fatalf("persist the stale hold: %v", err)
	}
	if sess.hasPendingUserSteering() {
		t.Fatal("this test is not in the state it means to be: user steering is pending, so the hold would not be stale")
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	if restored.clientMutations.steeringHeld() {
		t.Fatal("a persisted hold naming no pending steering survived restore: every steer this session accepts from here is parked behind it and never delivered")
	}

	// And the consequence the flag governs: a steer sent after the restore can
	// claim a carrier turn, so something will actually run it.
	if _, err := restored.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-after-the-restore",
		Input:            []appwire.InputItem{{Type: "text", Text: "do it this way instead"}},
	}); err != nil {
		t.Fatalf("steer after the restore: %v", err)
	}
	if _, ok := restored.claimSteeringCarrierTurn(); !ok {
		t.Fatal("the steer sent after the restore cannot claim a carrier turn: it was accepted and will never reach the model or the transcript")
	}
}

// TestStopParksSteeringClaimedButNotYetIncorporated closes the window between
// a steer's durable claim and its transcript append. popSteeringHead commits
// ExecutionState "claimed" before consumeSteeringMessage writes the entry, and
// restore returns a claim that never landed to "accepted" -- so a steer sitting
// in that window is still deliverable across a restart, and a Stop that reads
// only "accepted" steering arms no hold and lets it through anyway (#174).
func TestStopParksSteeringClaimedButNotYetIncorporated(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	serveSession(t, sess)

	runningStartTurn(t, sess, "running-turn", "do the thing")
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}

	// The window itself: claimed durably, transcript entry not yet appended.
	msg, ok := sess.popSteeringHead()
	if !ok {
		t.Fatal("popSteeringHead found no steering to claim; this test is not in the state it means to be")
	}
	if msg.ClientMutationID != "steer-mid-round" {
		t.Fatalf("popSteeringHead claimed %q, want the user's steer", msg.ClientMutationID)
	}
	if state := sess.clientMutations.snapshot().PendingExecutions["steer-mid-round"].ExecutionState; state != "claimed" {
		t.Fatalf("the claimed steer reads %q, want %q; this test is not in the window it means to be", state, "claimed")
	}

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-claimed-steering",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("a Stop landing while the steer was claimed but not yet incorporated armed no hold: restore returns the claim to accepted, so the steer the user stopped is delivered on the next wake anyway")
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	if !restored.hasPendingUserSteering() {
		t.Fatal("restore did not return the never-appended claim to the pending queue; this test no longer covers the window it names")
	}
	if !restored.clientMutations.steeringHeld() {
		t.Fatal("the hold did not survive the restart, so the returned claim is delivered on the next wake")
	}
	if claimedID, ok := restored.claimSteeringCarrierTurn(); ok {
		t.Fatalf("claimSteeringCarrierTurn claimed turn %q after restore: the steer the Stop should have parked runs anyway", claimedID)
	}
}

// TestStopOnASteeringCarrierTurnArmsNoHoldForItsOwnSteer is the exclusion the
// claimed window needs. The steer whose reserved id IS the turn being
// cancelled is not a passenger the Stop has to hold back -- it is the turn.
// Its record disappears the moment its transcript append finalizes, so parking
// for it leaves a hold naming nothing, and a hold naming nothing swallows the
// next steer the user sends (#710).
func TestStopOnASteeringCarrierTurnArmsNoHoldForItsOwnSteer(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	serveSession(t, sess)

	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-with-no-turn",
		Input:            []appwire.InputItem{{Type: "text", Text: "do it this way"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	carrier, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatal("the steer could not claim a carrier turn; this test is not in the state it means to be")
	}
	msg, ok := sess.popSteeringHead()
	if !ok || msg.ClientMutationID != "steer-with-no-turn" {
		t.Fatalf("popSteeringHead claimed %q (ok=%v), want the carrier's own steer", msg.ClientMutationID, ok)
	}
	if state := sess.clientMutations.snapshot().PendingExecutions["steer-with-no-turn"].ExecutionState; state != "claimed" {
		t.Fatalf("the carrier's steer reads %q, want %q; this test is not in the window it means to be", state, "claimed")
	}

	response, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-the-carrier",
	}, func() {})
	if err != nil {
		t.Fatalf("stop against the carrier turn: %v", err)
	}
	if response.Receipt.TurnID != carrier {
		t.Fatalf("the Stop cancelled turn %q, want the carrier turn %q; the exclusion this test names would not apply", response.Receipt.TurnID, carrier)
	}
	if sess.clientMutations.steeringHeld() {
		t.Fatal("the Stop armed a hold for the very steer whose carrier turn it cancelled: that record vanishes when its append finalizes, leaving a hold naming nothing that swallows the user's next steer")
	}
}
