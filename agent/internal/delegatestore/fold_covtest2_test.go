package delegatestore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestApplyNilState covers Apply with a nil state (fold.go:29-30).
func TestApplyNilState(t *testing.T) {
	err := Apply(nil, Event{Kind: EventDelegateCreated, DelegateID: "x", Seq: 1})
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("expected nil state error, got %v", err)
	}
}

// TestApplyEventUnknownKind covers the default branch of applyEvent
// (fold.go:88-89).
func TestApplyEventUnknownKind(t *testing.T) {
	state := make(State)
	err := Apply(state, Event{Kind: "unknown_kind", DelegateID: "x", Seq: 1})
	if err == nil || !strings.Contains(err.Error(), "unknown delegate event kind") {
		t.Fatalf("expected unknown kind error, got %v", err)
	}
}

// TestApplyCreatedAlreadyExists covers applyCreated when the delegate
// already exists (fold.go:94-95).
func TestApplyCreatedAlreadyExists(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""))
	err := Apply(state, createdEvent("dlg_a", ""))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already-exists error, got %v", err)
	}
}

// TestApplyCreatedParentMissing covers applyCreated when the parent does
// not exist (fold.go:101-102).
func TestApplyCreatedParentMissing(t *testing.T) {
	state := make(State)
	evt := createdEvent("dlg_child", "dlg_nobody")
	evt.Seq = 1
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "parent") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected parent-missing error, got %v", err)
	}
}

// TestApplyRunStartedMissingDelegate covers applyRunStarted requireAggregate
// error (fold.go:119-120).
func TestApplyRunStartedMissingDelegate(t *testing.T) {
	state := make(State)
	err := Apply(state, startedEvent("dlg_nobody", 1, TriggerInitial))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected missing-delegate error, got %v", err)
	}
}

// TestApplyRunStartedWrongGeneration covers generation mismatch
// (fold.go:122-123).
func TestApplyRunStartedWrongGeneration(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""))
	err := Apply(state, startedEvent("dlg_a", 99, TriggerInitial))
	if err == nil || !strings.Contains(err.Error(), "generation") {
		t.Fatalf("expected generation error, got %v", err)
	}
}

// TestApplyRunStartedNotResumable covers the not-resumable check
// (fold.go:128-129). A delegate that is resumable but then has resumability
// closed (still PhaseIdle) before starting.
func TestApplyRunStartedNotResumable(t *testing.T) {
	// Close resumability so delegate is PhaseClosed with Resumable=false.
	// That triggers the phase check, not the not-resumable check.
	// To reach the not-resumable check (line 128-129), we need Phase==Idle
	// but Resumable==false. The only way to get that: apply created with
	// Resumable=false, which sets Phase=PhaseClosed (not Idle). So the
	// not-resumable check at line 128-129 is only reachable when Phase is
	// Idle and Resumable is false — but applyCreated sets Phase=Closed
	// when Resumable is false. This branch is effectively unreachable
	// through normal event ordering.
	// Document: unreachable — applyCreated sets Phase=Closed when
	// Resumable=false, so Phase is never Idle when Resumable is false.
	t.Skip("not-resumable check at fold.go:128-129 is unreachable: applyCreated sets Phase=Closed when Resumable=false")
}

// TestApplyRunStartedPendingStop covers pending stop check (fold.go:131-132).
// Document: unreachable — applySubtreeStopRequested sets Phase=Stopping,
// so the Phase != PhaseIdle check at line 125 fires first. The pending
// stop check at line 131 is only reachable when Phase==Idle AND
// PendingStopSeq!=0, but stop requests always set Phase=Stopping.
func TestApplyRunStartedPendingStop(t *testing.T) {
	t.Skip("pending stop check at fold.go:131-132 is unreachable: stop request sets Phase=Stopping, so phase check fires first")
}

// TestApplyRunStartedInvalidTrigger covers invalid trigger (fold.go:134-135).
func TestApplyRunStartedInvalidTrigger(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""))
	err := Apply(state, startedEvent("dlg_a", 1, "bogus_trigger"))
	if err == nil || !strings.Contains(err.Error(), "invalid run trigger") {
		t.Fatalf("expected invalid trigger error, got %v", err)
	}
}

// TestApplyRunStartedZeroStartedAt covers zero start time (fold.go:137-138).
func TestApplyRunStartedZeroStartedAt(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""))
	evt := Event{
		Kind:       EventDelegateRunStarted,
		DelegateID: "dlg_a",
		RunStarted: &RunStarted{Generation: 1, Trigger: TriggerInitial},
	}
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "zero") {
		t.Fatalf("expected zero-time error, got %v", err)
	}
}

// TestApplyRunStartedNotIdle covers phase-not-idle (fold.go:125-126).
func TestApplyRunStartedNotIdle(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial))
	err := Apply(state, startedEvent("dlg_a", 2, TriggerInitial))
	if err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("expected cannot-start error, got %v", err)
	}
}

// TestApplyTerminalPreparedNotRunning covers PhaseRunning check for
// terminal prepared when the delegate is not running (fold.go:155-156).
// After a successful terminal-prepared, Phase=Settling; preparing again
// with the same generation hits the phase check.
func TestApplyTerminalPreparedNotRunning(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial))
	if err := Apply(state, preparedEvent("dlg_a", 1, reportedPacket("ok"))); err != nil {
		t.Fatal(err)
	}
	// Phase is now Settling; preparing again with the same generation hits
	// the "cannot prepare terminal" phase check.
	err := Apply(state, preparedEvent("dlg_a", 1, reportedPacket("ok")))
	if err == nil || !strings.Contains(err.Error(), "cannot prepare terminal") {
		t.Fatalf("expected cannot-prepare error, got %v", err)
	}
}

// TestApplyTerminalPreparedBadPacket covers invalid terminal packet
// (fold.go:158-159).
func TestApplyTerminalPreparedBadPacket(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial))
	evt := preparedEvent("dlg_a", 1, TerminalPacket{Kind: "bad", Message: json.RawMessage(`"x"`)})
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "terminal packet") {
		t.Fatalf("expected terminal packet error, got %v", err)
	}
}

// TestApplyRunFinishedBadPhase covers phase check (fold.go:172-173).
// Document: unreachable — requireExactOpenRun checks CurrentRunOpen before
// the phase check, and CurrentRunOpen is only true when Phase is Running,
// Settling, or Stopping, all of which pass the phase check.
func TestApplyRunFinishedBadPhase(t *testing.T) {
	t.Skip("phase check at fold.go:172-173 is unreachable: requireExactOpenRun ensures CurrentRunOpen, which is only true for Running/Settling/Stopping phases")
}

// TestApplyRunFinishedInvalidOutcome covers invalid outcome status
// (fold.go:175-176).
func TestApplyRunFinishedInvalidOutcome(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial))
	pkt := reportedPacket("ok")
	err := Apply(state, finishedEvent("dlg_a", 1, "bogus", DispositionReported, "dlg_a/delivery/1", &pkt))
	if err == nil || !strings.Contains(err.Error(), "invalid outcome") {
		t.Fatalf("expected invalid outcome error, got %v", err)
	}
}

// TestApplyRunFinishedZeroEndedAt covers zero end time (fold.go:181-182).
func TestApplyRunFinishedZeroEndedAt(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial))
	pkt := reportedPacket("ok")
	evt := Event{
		Kind:       EventDelegateRunFinished,
		DelegateID: "dlg_a",
		RunFinished: &RunFinished{
			Generation:  1,
			Outcome:     Outcome{Status: OutcomeCompleted},
			Disposition: DispositionReported,
			DeliveryID:  "dlg_a/delivery/1",
			Packet:      &pkt,
		},
	}
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "zero") {
		t.Fatalf("expected zero-time error, got %v", err)
	}
}

// TestApplyRunFinishedInvalidDisposition covers invalid disposition
// (fold.go:184-185).
func TestApplyRunFinishedInvalidDisposition(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial))
	pkt := reportedPacket("ok")
	err := Apply(state, finishedEvent("dlg_a", 1, OutcomeCompleted, "bogus_disposition", "dlg_a/delivery/1", &pkt))
	if err == nil || !strings.Contains(err.Error(), "invalid disposition") {
		t.Fatalf("expected invalid disposition error, got %v", err)
	}
}

// TestApplyRunFinishedNonExhaustedCarriesExhaustion covers validateOutcome
// for a non-exhausted outcome carrying exhaustion metadata (fold.go:625-626).
func TestApplyRunFinishedNonExhaustedCarriesExhaustion(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial))
	pkt := reportedPacket("ok")
	resumable := true
	evt := Event{
		Kind:       EventDelegateRunFinished,
		DelegateID: "dlg_a",
		RunFinished: &RunFinished{
			Generation:  1,
			Outcome:     Outcome{Status: OutcomeCompleted, EndedAt: time.Now(), ExhaustionBudget: "tool_rounds", Resumable: &resumable},
			Disposition: DispositionReported,
			DeliveryID:  "dlg_a/delivery/1",
			Packet:      &pkt,
		},
	}
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "exhaustion metadata") {
		t.Fatalf("expected exhaustion-metadata error, got %v", err)
	}
}

// TestApplyRunFinishedExhaustedBadReason covers validateOutcome for
// tool_round budget with wrong reason (fold.go:638-639).
func TestApplyRunFinishedExhaustedBadReason(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial))
	pkt := reportedPacket("ok")
	resumable := true
	evt := Event{
		Kind:       EventDelegateRunFinished,
		DelegateID: "dlg_a",
		RunFinished: &RunFinished{
			Generation:  1,
			Outcome:     Outcome{Status: OutcomeExhausted, EndedAt: time.Now(), ExhaustionBudget: ExhaustionBudgetToolRounds, ExhaustionLimit: 5, Resumable: &resumable, Reason: "wrong"},
			Disposition: DispositionTerminalError,
			DeliveryID:  "dlg_a/delivery/1",
			Packet:      &pkt,
		},
	}
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "tool-round exhaustion reason") {
		t.Fatalf("expected bad-reason error, got %v", err)
	}
}

// TestApplyRunFinishedExhaustedInvalidBudget covers validateOutcome for
// an unknown exhaustion budget (fold.go:651-652).
func TestApplyRunFinishedExhaustedInvalidBudget(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial))
	pkt := reportedPacket("ok")
	resumable := true
	evt := Event{
		Kind:       EventDelegateRunFinished,
		DelegateID: "dlg_a",
		RunFinished: &RunFinished{
			Generation:  1,
			Outcome:     Outcome{Status: OutcomeExhausted, EndedAt: time.Now(), ExhaustionBudget: "bogus_budget", ExhaustionLimit: 5, Resumable: &resumable, Reason: "tool_round_budget_exhausted"},
			Disposition: DispositionTerminalError,
			DeliveryID:  "dlg_a/delivery/1",
			Packet:      &pkt,
		},
	}
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "invalid exhaustion budget") {
		t.Fatalf("expected invalid budget error, got %v", err)
	}
}

// TestApplyRunFinishedStoppingObserverCallback covers the stopping branch
// observer-callback error (fold.go:195-196).
func TestApplyRunFinishedStoppingObserverCallback(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial), stopRequestedEvent("dlg_a"))
	pkt := stoppedPacket()
	evt := Event{
		Kind:       EventDelegateRunFinished,
		DelegateID: "dlg_a",
		RunFinished: &RunFinished{
			Generation:                1,
			Outcome:                   Outcome{Status: OutcomeStopped, EndedAt: time.Now(), Reason: "stopped_by_parent"},
			Disposition:               DispositionTerminalError,
			ObserverCallbackDelivered: true,
			Packet:                    pkt,
		},
	}
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "observer callback") {
		t.Fatalf("expected observer-callback error, got %v", err)
	}
}

// TestApplyRunFinishedCompletedNoAction covers the completed_no_action
// disposition path, which ends with Phase=Idle for a resumable delegate.
func TestApplyRunFinishedCompletedNoAction(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerAttention))
	evt := Event{
		Kind:       EventDelegateRunFinished,
		DelegateID: "dlg_a",
		RunFinished: &RunFinished{
			Generation:  1,
			Outcome:     Outcome{Status: OutcomeCompleted, EndedAt: time.Now()},
			Disposition: DispositionCompletedNoAction,
		},
	}
	if err := Apply(state, evt); err != nil {
		t.Fatalf("completed_no_action finish failed: %v", err)
	}
	if state["dlg_a"].Phase != PhaseIdle {
		t.Fatalf("phase = %s, want Idle after completed_no_action", state["dlg_a"].Phase)
	}
}

// TestApplyResumabilityClosedEmptyReason covers the empty-reason check
// (fold.go:243-244).
func TestApplyResumabilityClosedEmptyReason(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""))
	evt := Event{
		Kind:               EventDelegateResumabilityClosed,
		DelegateID:         "dlg_a",
		ResumabilityClosed: &ResumabilityClosed{Reason: ""},
	}
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "reason is empty") {
		t.Fatalf("expected empty-reason error, got %v", err)
	}
}

// TestApplyResumabilityClosedAlreadyClosed covers the already-closed check
// (fold.go:246-247).
func TestApplyResumabilityClosedAlreadyClosed(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""))
	evt1 := Event{
		Kind:               EventDelegateResumabilityClosed,
		DelegateID:         "dlg_a",
		ResumabilityClosed: &ResumabilityClosed{Reason: "done"},
	}
	if err := Apply(state, evt1); err != nil {
		t.Fatal(err)
	}
	err := Apply(state, evt1)
	if err == nil || !strings.Contains(err.Error(), "already closed") {
		t.Fatalf("expected already-closed error, got %v", err)
	}
}

// TestApplySubtreeStopRequestedAlreadyPending covers the pending-stop
// check (fold.go:274-275).
func TestApplySubtreeStopRequestedAlreadyPending(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), createdEvent("dlg_b", "dlg_a"), stopRequestedEvent("dlg_a"))
	// The second stop request must have a non-zero Seq to pass the
	// event.Seq==0 guard in applySubtreeStopRequested.
	evt := stopRequestedEvent("dlg_b")
	evt.Seq = 4
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already-exists error, got %v", err)
	}
}

// TestApplySubtreeStopCompletedNoMembers covers the pending-stop-sequence
// mismatch (fold.go:300-301). The no-members check at fold.go:313-314 is
// unreachable: if target.PendingStopSeq == RequestSeq, the target itself
// is always a covered member.
func TestApplySubtreeStopCompletedNoMembers(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""))
	// Complete a stop that was never requested.
	evt := Event{
		Kind:                 EventDelegateSubtreeStopCompleted,
		DelegateID:           "dlg_a",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 99},
	}
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "pending stop sequence") {
		t.Fatalf("expected pending-stop-sequence error, got %v", err)
	}
}

// TestApplySubtreeStopCompletedRunStillOpen covers the run-open check
// (fold.go:308-309).
func TestApplySubtreeStopCompletedRunStillOpen(t *testing.T) {
	state := applyEvents(t, createdEvent("dlg_a", ""), startedEvent("dlg_a", 1, TriggerInitial), stopRequestedEvent("dlg_a"))
	evt := Event{
		Kind:                 EventDelegateSubtreeStopCompleted,
		DelegateID:           "dlg_a",
		SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: 3},
	}
	err := Apply(state, evt)
	if err == nil || !strings.Contains(err.Error(), "still open") {
		t.Fatalf("expected still-open error, got %v", err)
	}
}

// TestValidateFinishPacketBadPacket covers the nil-packet and
// bad-packet paths in validateFinishPacket (fold.go:523-524, 526-527).
func TestValidateFinishPacketBadPacket(t *testing.T) {
	aggregate := &Aggregate{DelegateID: "dlg_a", Trigger: TriggerInitial}
	// Disposition is not CompletedNoAction, so packet is required.
	err := validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionReported, DeliveryID: "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no terminal packet") {
		t.Fatalf("expected no-packet error, got %v", err)
	}
	// Bad packet.
	badPkt := &TerminalPacket{Kind: "bad", Message: json.RawMessage(`"x"`)}
	err = validateFinishPacket(aggregate, &RunFinished{Disposition: DispositionReported, DeliveryID: "x"}, badPkt)
	if err == nil || !strings.Contains(err.Error(), "finish packet") {
		t.Fatalf("expected finish-packet error, got %v", err)
	}
}

// TestIsDelegateOrDescendantNilAggregate covers the nil-aggregate return
// in isDelegateOrDescendant (fold.go:602-603).
func TestIsDelegateOrDescendantNilAggregate(t *testing.T) {
	state := State{"dlg_a": nil}
	if isDelegateOrDescendant(state, "dlg_a", "dlg_b") {
		t.Fatal("expected false for nil aggregate in state")
	}
}

// TestValidOutcomeStatusDefault covers the default branch
// (fold.go:618-619).
func TestValidOutcomeStatusDefault(t *testing.T) {
	if validOutcomeStatus("bogus") {
		t.Fatal("expected bogus status to be invalid")
	}
}

// TestFoldSequenceMismatch covers Fold's sequence check (fold.go:17-18).
func TestFoldSequenceMismatch(t *testing.T) {
	evt := createdEvent("dlg_a", "")
	evt.Seq = 99
	_, err := Fold([]Event{evt})
	if err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("expected sequence error, got %v", err)
	}
}
