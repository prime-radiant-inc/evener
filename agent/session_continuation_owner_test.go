package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestUnnamedContinuationOwnsItsSteering pins #4599: a client turn/start can
// reserve the durable active-turn name in the window before a goal continuation
// is admitted, which makes the continuation run unnamed (the refusal itself is
// pinned by TestMintRunningTurnIDRefusesWhenATurnIsAlreadyNamed). Steering
// recorded during that run must name the execution that is actually running,
// not the reserved turn that is not. Both recording sites are covered: a queued
// steer drained by the continuation's acceptance and a steer appended directly
// mid-turn.
func TestUnnamedContinuationOwnsItsSteering(t *testing.T) {
	t.Parallel()
	const drainedText = "daemon steer drained by the continuation"
	const appendedText = "daemon steer appended during the continuation"

	var sess *Session
	var runningID string
	step := func(llm.Request) llm.Response {
		sess.mu.Lock()
		runningID = sess.execution.turnID
		sess.mu.Unlock()
		sess.appendSteeringTurn(appendedText, events.SteeringKindTaskInactive)
		return agenttest.FinalResponse("continued")
	}
	sess = newTestSessionForEnvctx(t, withSteps(step))
	serveSessionWithMutationStore(t, sess)
	if err := sess.steerKind(drainedText, events.SteeringKindTaskInactive); err != nil {
		t.Fatalf("queue the steer the continuation drains: %v", err)
	}

	// The real admission path reserves the slot the way the daemon does: the
	// start is accepted but never claimed, so ActiveTurnID names a turn that is
	// not running when the continuation is admitted.
	accepted, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-continuation-owner",
		Input:            []appwire.InputItem{{Type: "text", Text: "reserve the active turn"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	held := accepted.Turn.ID
	if held == "" {
		t.Fatal("turn/start reserved no active turn; the test is not in the state it means to be")
	}

	if _, err := sess.ProcessInputKind(context.Background(), "keep going", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind(EntryContinuation): %v", err)
	}

	owners := make(map[string]string)
	sess.mu.Lock()
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && (turn.Message.Text() == drainedText || turn.Message.Text() == appendedText) {
			owners[turn.Message.Text()] = turn.OwningTurnID
		}
	}
	sess.mu.Unlock()

	if runningID == "" || runningID == held {
		t.Fatalf("continuation ran under %q, want its own execution id distinct from the held %q", runningID, held)
	}
	for _, text := range []string{drainedText, appendedText} {
		if owners[text] != runningID {
			t.Fatalf("steering %q OwningTurnID = %q, want the running execution %q (held turn %q)",
				text, owners[text], runningID, held)
		}
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != held {
		t.Fatalf("ActiveTurnID = %q after the continuation, want the held %q untouched", got, held)
	}
}

// TestStopDuringUnnamedContinuationTargetsRunningTurn pins #5802: a Stop
// accepted while an unnamed continuation runs must fence the execution that is
// running, not the reserved mutation turn that holds the durable active-turn
// slot. The fence is captured from inside the running step, before the turn can
// finalize.
func TestStopDuringUnnamedContinuationTargetsRunningTurn(t *testing.T) {
	t.Parallel()

	var sess *Session
	var runningID, fencedID string
	step := func(llm.Request) llm.Response {
		sess.mu.Lock()
		runningID = sess.execution.turnID
		sess.mu.Unlock()
		// The fence is read inside cancelAndWait: it is persisted before the
		// cancellation and finalized after it, so this is the only point where
		// it is still visible.
		cancelAndWait := func() {
			if fence := sess.clientMutations.snapshot().InterruptFence; fence != nil {
				fencedID = fence.ExpectedTurnID
			}
		}
		if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
			ClientMutationID: "cm-stop-during-continuation",
		}, cancelAndWait); err != nil {
			t.Errorf("InterruptClientMutation during the continuation: %v", err)
		}
		return agenttest.FinalResponse("continued")
	}
	sess = newTestSessionForEnvctx(t, withSteps(step))
	serveSessionWithMutationStore(t, sess)

	accepted, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-reserved-for-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "reserve the active turn"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	held := accepted.Turn.ID
	if held == "" {
		t.Fatal("turn/start reserved no active turn; the test is not in the state it means to be")
	}

	if _, err := sess.ProcessInputKind(context.Background(), "keep going", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind(EntryContinuation): %v", err)
	}

	if runningID == "" || runningID == held {
		t.Fatalf("continuation ran under %q, want its own execution id distinct from the held %q", runningID, held)
	}
	if fencedID != runningID {
		t.Fatalf("Stop fenced turn %q, want the running execution %q (held reserved turn %q)", fencedID, runningID, held)
	}
}

// TestStopDuringRecoveredLegacyTurnFencesThePendingMutation pins the other half
// of #5802's fence contract: a recovered legacy turn runs under a fresh t_* id
// while PendingExecutions keeps the recorded turn_<n> name, and that pending
// name -- not the execution id -- is what finalization and recovery match. A
// fence naming the execution id would skip the stopped mutation, leaving it to
// rerun after the restart.
func TestStopDuringRecoveredLegacyTurnFencesThePendingMutation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	start := appwire.TurnStartParams{
		ClientMutationID: "start-legacy-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "a turn recorded with a legacy name"}},
	}
	if _, err := sess.AcceptClientMutationStart(start); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if err := sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		nil,
		false,
	); err != nil {
		t.Fatalf("incorporate start: %v", err)
	}
	// Rewrite the incorporated start to the legacy spelling a pre-identity
	// store carries: the pending mutation keeps turn_11 while a resumed
	// execution mints a fresh t_* id.
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending := snapshot.PendingExecutions[start.ClientMutationID]
		pending.TurnID = "turn_11"
		snapshot.PendingExecutions[start.ClientMutationID] = pending
		record := snapshot.Journal[start.ClientMutationID]
		record.StableTurnID = "turn_11"
		snapshot.Journal[start.ClientMutationID] = record
		snapshot.ActiveTurnID = "turn_11"
		return nil
	}); err != nil {
		t.Fatalf("rewrite to legacy identity: %v", err)
	}

	sess.beginExecution("turn_11")
	sess.mu.Lock()
	runningID := sess.execution.turnID
	sess.mu.Unlock()
	if runningID == "" || runningID == "turn_11" {
		t.Fatalf("legacy execution ran under %q, want a fresh id distinct from turn_11", runningID)
	}

	injected := errors.New("crash after the legacy interrupt fence")
	sess.clientMutations.faults.AfterReservation = func() error { return injected }
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-legacy-recovery",
	}
	if _, err := sess.InterruptClientMutation(context.Background(), interrupt, func() {}); !errors.Is(err, injected) {
		t.Fatalf("InterruptClientMutation error = %v, want %v", err, injected)
	}
	fenced := sess.clientMutations.snapshot()
	if fenced.InterruptFence == nil {
		t.Fatal("no interrupt fence was persisted")
	}
	if got := fenced.InterruptFence.ExpectedTurnID; got != "turn_11" {
		t.Fatalf("fence names %q, want the pending legacy mutation turn_11 (running execution %q)", got, runningID)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	recovered := restored.clientMutations.snapshot()
	if got := recovered.Journal[start.ClientMutationID].ExecutionState; got != "interrupted" {
		t.Fatalf("recovered target execution state = %q, want interrupted", got)
	}
	if _, still := recovered.PendingExecutions[start.ClientMutationID]; still {
		t.Fatal("recovered stopped legacy turn is still pending and can rerun")
	}
	if recovered.ActiveTurnID != "" || recovered.InterruptFence != nil {
		t.Fatalf("recovered terminal state: active=%q fence=%#v", recovered.ActiveTurnID, recovered.InterruptFence)
	}
	if claimed, ok, err := restored.claimClientMutationStart(); err != nil || ok {
		t.Fatalf("stopped legacy turn remained claimable after recovery: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
}

// TestStopAfterSamplingFencesTheSuccessorClaim covers the window between the
// interrupt's execution sample and its fence reservation. If the sampled turn
// finishes and a successor start is claimed in that window, the fence must name
// the claimed successor: a stale execution id would finalize the wrong
// mutation and leave the successor claimed until restart.
func TestStopAfterSamplingFencesTheSuccessorClaim(t *testing.T) {
	t.Parallel()

	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	running := appwire.TurnStartParams{
		ClientMutationID: "start-sampled-running",
		Input:            []appwire.InputItem{{Type: "text", Text: "the turn sampled by the Stop"}},
	}
	if _, err := sess.AcceptClientMutationStart(running); err != nil {
		t.Fatalf("AcceptClientMutationStart(running): %v", err)
	}
	claimedRunning, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claimClientMutationStart(running): claimed=%#v ok=%v err=%v", claimedRunning, ok, err)
	}
	sess.beginExecution(claimedRunning.StableTurnID)
	if err := sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimedRunning),
		claimedRunning.Text,
		claimedRunning.Images,
		nil,
		false,
	); err != nil {
		t.Fatalf("incorporate running start: %v", err)
	}
	sess.mu.Lock()
	runningID := sess.execution.turnID
	sess.mu.Unlock()

	successor := appwire.TurnStartParams{
		ClientMutationID: "start-claimed-successor",
		Input:            []appwire.InputItem{{Type: "text", Text: "claimed after the Stop sampled"}},
	}

	var successorID string
	sess.cfg.testOnly.interruptSampled = func() {
		sess.cfg.testOnly.interruptSampled = nil
		sess.completeExecution(schema.TurnCompleted)
		if err := sess.completeClientMutationTurn(claimedRunning.ClientMutationID); err != nil {
			t.Errorf("completeClientMutationTurn(running) in the sampled window: %v", err)
			return
		}
		if _, err := sess.AcceptClientMutationStart(successor); err != nil {
			t.Errorf("AcceptClientMutationStart(successor) in the sampled window: %v", err)
			return
		}
		claimed, ok, err := sess.claimClientMutationStart()
		if err != nil || !ok {
			t.Errorf("claimClientMutationStart(successor) in the sampled window: claimed=%#v ok=%v err=%v", claimed, ok, err)
			return
		}
		successorID = claimed.StableTurnID
	}
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-after-sampling",
	}
	if _, err := sess.InterruptClientMutation(context.Background(), interrupt, nil); err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}
	if successorID == "" {
		t.Fatal("the successor was not claimed; the test did not exercise the sampling-to-reservation window")
	}
	if successorID == runningID {
		t.Fatalf("successor claimed the running turn's id %q; the window was not exercised", successorID)
	}
	snapshot := sess.clientMutations.snapshot()
	if got := snapshot.Journal[interrupt.ClientMutationID].StableTurnID; got != successorID {
		t.Fatalf("interrupt finalized against %q, want the claimed successor %q (sampled execution %q)", got, successorID, runningID)
	}
	if got := snapshot.Journal[successor.ClientMutationID].ExecutionState; got != "interrupted" {
		t.Fatalf("claimed successor state = %q, want interrupted", got)
	}
	if _, still := snapshot.PendingExecutions[successor.ClientMutationID]; still {
		t.Fatal("claimed successor remained pending after the Stop finalized")
	}
}

// TestStopDuringContinuationAdmissionKeepsTheReservedStart covers the window
// after the session marks itself processing but before beginExecution registers
// the continuation: the execution sample is empty while the durable slot names
// an accepted-but-unrun start. The fence must not name that bystander, because
// finalizing would delete the accepted input it never ran.
func TestStopDuringContinuationAdmissionKeepsTheReservedStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	reserved := appwire.TurnStartParams{
		ClientMutationID: "start-reserved-beside-continuation",
		Input:            []appwire.InputItem{{Type: "text", Text: "input that must survive the Stop"}},
	}
	if _, err := sess.AcceptClientMutationStart(reserved); err != nil {
		t.Fatalf("AcceptClientMutationStart(reserved): %v", err)
	}
	// processOneInput has entered SessionProcessing, but beginExecution has not
	// registered the continuation yet, so the execution sample is empty.
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()
	sess.cfg.testOnly.interruptSampled = func() {
		sess.cfg.testOnly.interruptSampled = nil
		// The continuation starts before the fence is reserved, but its id was
		// not in the sample. The fence must still leave the accepted start
		// alone: it never ran, and the cancellation ends the continuation.
		sess.beginExecution("")
	}
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-during-continuation-admission",
	}
	if _, err := sess.InterruptClientMutation(context.Background(), interrupt, nil); err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}
	snapshot := sess.clientMutations.snapshot()
	pending, ok := snapshot.PendingExecutions[reserved.ClientMutationID]
	if !ok {
		t.Fatal("the accepted start was deleted by an interrupt that never ran it")
	}
	if pending.ExecutionState != "accepted" {
		t.Fatalf("accepted start state = %q, want accepted", pending.ExecutionState)
	}
	if len(snapshot.Journal[reserved.ClientMutationID].Payload) == 0 {
		t.Fatal("the accepted start's payload was cleared by an interrupt that never ran it")
	}
	if got := snapshot.Journal[interrupt.ClientMutationID].StableTurnID; got != "" {
		t.Fatalf("interrupt targeted %q, want no durable target during continuation admission", got)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	recovered := restored.clientMutations.snapshot()
	recoveredPending, ok := recovered.PendingExecutions[reserved.ClientMutationID]
	if !ok || recoveredPending.ExecutionState != "accepted" {
		t.Fatalf("recovered accepted start = %#v, present=%v; want an accepted pending execution", recoveredPending, ok)
	}
	if len(recovered.Journal[reserved.ClientMutationID].Payload) == 0 {
		t.Fatal("recovery lost the accepted start's payload")
	}
	claimed, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("reserved start not claimable after recovery: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.ClientMutationID != reserved.ClientMutationID {
		t.Fatalf("recovered claim = %q, want the reserved start %q", claimed.ClientMutationID, reserved.ClientMutationID)
	}
}
