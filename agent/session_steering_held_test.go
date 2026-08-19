package agent

import (
	"context"
	"sync"
	"testing"

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
