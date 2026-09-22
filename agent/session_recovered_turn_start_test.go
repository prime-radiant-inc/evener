package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestAcceptClientMutationStartBehindRecoveredTurn is the daemon-side delivery
// for the reported flow: a prompt sent to a session whose daemon died mid-turn.
//
// The durable snapshot keeps the dead turn's ActiveTurnID because the pending
// turn/start that owns it is reclaimed and re-run by restore. The caller's NEW
// turn/start must be accepted behind that recovered turn -- the prompt is the
// user speaking and the recovered turn is the session's own work -- rather than
// refused with Conflict("turn is already active"), which strands the text in
// the composer and makes the user send it a second time.
func TestAcceptClientMutationStartBehindRecoveredTurn(t *testing.T) {
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()

	dead := appwire.TurnStartParams{
		ClientMutationID: "cm-dead-turn",
		Input:            []appwire.InputItem{{Type: "text", Text: "the prompt that died mid-turn"}},
	}
	start, err := crashed.AcceptClientMutationStart(dead)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart (dead turn): %v", err)
	}
	// The process dies here: the turn/start is durably accepted and names the
	// active turn, but nothing ever ran it.
	crashed.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	if got := restored.recoveredTurnID; got != start.Turn.ID {
		t.Fatalf("recoveredTurnID = %q, want the inherited turn %q", got, start.Turn.ID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != start.Turn.ID {
		t.Fatalf("restored ActiveTurnID = %q, want the inherited turn %q", got, start.Turn.ID)
	}

	params := appwire.TurnStartParams{
		ClientMutationID: "cm-new-prompt",
		Input:            []appwire.InputItem{{Type: "text", Text: "the prompt sent after the crash"}},
	}
	response, err := restored.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("turn/start behind the recovered turn was refused: %v", err)
	}
	if response.Receipt.ClientMutationID != params.ClientMutationID {
		t.Fatalf("receipt clientMutationId = %q, want %q", response.Receipt.ClientMutationID, params.ClientMutationID)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("receipt disposition = %q, want applied", response.Receipt.Disposition)
	}
	pending, ok := restored.clientMutations.snapshot().PendingExecutions[params.ClientMutationID]
	if !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("new prompt pending = %#v ok=%v, want durably accepted", pending, ok)
	}
	if len(pending.Input) != 1 || pending.Input[0].Text != "the prompt sent after the crash" {
		t.Fatalf("durable new prompt input = %#v, want the caller's text", pending.Input)
	}

	// A third start is admitted the same way, because the follow-up accepted
	// above did NOT take the active slot: the slot still names the inherited,
	// still-running turn, and the admission precondition keeps comparing against
	// it for as long as it runs. The refusal that protects the web composer's
	// routing contract -- "turn is already active" -- belongs to a turn THIS
	// process started, and TestAcceptClientMutationStartStillRefusedForLiveTurn
	// pins it.
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-third-prompt",
		Input:            []appwire.InputItem{{Type: "text", Text: "a third prompt"}},
	}); err != nil {
		t.Fatalf("third turn/start behind the still-running recovered turn = %v, want accepted", err)
	}
}

// TestAcceptClientMutationStartStillRefusedForLiveTurn pins the other half of
// the rule: a turn the session started in THIS process is not inherited work,
// so a turn/start while it is active keeps today's refusal.
func TestAcceptClientMutationStartStillRefusedForLiveTurn(t *testing.T) {
	s := newTestSession(t)
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-live-turn",
		Input:            []appwire.InputItem{{Type: "text", Text: "the turn this process started"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if s.recoveredTurnID != "" {
		t.Fatalf("recoveredTurnID = %q, want empty for a process-local turn", s.recoveredTurnID)
	}
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-refused",
		Input:            []appwire.InputItem{{Type: "text", Text: "a prompt while a live turn runs"}},
	}); !isClientMutationConflict(err) {
		t.Fatalf("turn/start during a process-local turn = %v, want Conflict(\"turn is already active\")", err)
	}
}

func isClientMutationConflict(err error) bool {
	var wire appwire.WireError
	return errors.As(err, &wire) && wire.Code == appwire.CodeConflict
}

// recoveredTurnSession builds the reported flow's durable state: a turn/start
// accepted into a session whose process then died before the turn ever ran. It
// returns the restored session (closed by t.Cleanup), the stable id of the turn
// this process inherited, and the client mutation id the dead prompt was
// accepted under.
func recoveredTurnSession(t *testing.T) (restored *Session, inheritedTurnID, deadMutationID string) {
	t.Helper()
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	deadMutationID = "cm-dead-turn"
	started, err := crashed.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: deadMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "the prompt that died mid-turn"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart (dead turn): %v", err)
	}
	// The process dies here: the turn/start is durably accepted and names the
	// active turn, but nothing ever ran it.
	crashed.Close()

	restored = restoreQueuePersistTestSession(t, dir, id)
	t.Cleanup(restored.Close)
	if got := restored.recoveredTurnID; got != started.Turn.ID {
		t.Fatalf("recoveredTurnID = %q, want the inherited turn %q", got, started.Turn.ID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != started.Turn.ID {
		t.Fatalf("restored ActiveTurnID = %q, want the inherited turn %q", got, started.Turn.ID)
	}
	return restored, started.Turn.ID, deadMutationID
}

// TestAcceptBehindRecoveredTurnKeepsTheRunningName pins the ownership rule the
// review finding broke: an accepted follow-up does NOT rename the active slot.
//
// The slot names the turn that is RUNNING, not merely one a client mutation
// reserved. Repointing it at an accepted-but-not-yet-running follow-up aims a
// Stop at the wrong turn and clears the guard the follow-up was admitted
// through, so the follow-up names the slot when it is CLAIMED instead.
func TestAcceptBehindRecoveredTurnKeepsTheRunningName(t *testing.T) {
	restored, inheritedTurnID, _ := recoveredTurnSession(t)

	response, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-new-prompt",
		Input:            []appwire.InputItem{{Type: "text", Text: "the prompt sent after the crash"}},
	})
	if err != nil {
		t.Fatalf("turn/start behind the recovered turn was refused: %v", err)
	}
	if response.Turn.ID == inheritedTurnID {
		t.Fatalf("the follow-up reused the inherited turn id %q", inheritedTurnID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != inheritedTurnID {
		t.Fatalf("ActiveTurnID after accepting the follow-up = %q, want the inherited running turn %q", got, inheritedTurnID)
	}
}

// TestClaimBehindRecoveredTurnClaimsInReservedTurnOrder pins FIFO claim order.
//
// Ranging over a Go map has no ordering guarantee, so the inherited turn and
// the follow-up admitted behind it were both claimable in arbitrary order --
// and the serve loop runs whatever it claims. The reserved turn sequence is the
// order the user spoke: the inherited turn's id was reserved before the crash,
// so it is claimed first and the follow-up runs after it.
func TestClaimBehindRecoveredTurnClaimsInReservedTurnOrder(t *testing.T) {
	restored, inheritedTurnID, _ := recoveredTurnSession(t)

	response, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "the follow-up"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart(follow-up): %v", err)
	}
	followUpTurnID := response.Turn.ID

	first, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("first claim: claimed=%#v ok=%v err=%v", first, ok, err)
	}
	if first.StableTurnID != inheritedTurnID {
		t.Fatalf("first claim = %q, want the inherited turn %q claimed first", first.StableTurnID, inheritedTurnID)
	}
	second, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("second claim: claimed=%#v ok=%v err=%v", second, ok, err)
	}
	if second.StableTurnID != followUpTurnID {
		t.Fatalf("second claim = %q, want the follow-up %q claimed next", second.StableTurnID, followUpTurnID)
	}
}

// TestClaimAfterRecoveredTurnReleasedNamesTheFollowUpActive pins the other half
// of the ownership rule: a turn claimed after the previous one released the
// slot names itself active. Without it the follow-up would run with an empty
// slot, where a concurrent turn/start is wrongly admitted and a Stop finds
// nothing to fence.
func TestClaimAfterRecoveredTurnReleasedNamesTheFollowUpActive(t *testing.T) {
	restored, inheritedTurnID, deadMutationID := recoveredTurnSession(t)

	response, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "the follow-up"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart(follow-up): %v", err)
	}
	followUpTurnID := response.Turn.ID

	first, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("first claim: claimed=%#v ok=%v err=%v", first, ok, err)
	}
	if first.StableTurnID != inheritedTurnID {
		t.Fatalf("first claim = %q, want the inherited turn %q", first.StableTurnID, inheritedTurnID)
	}

	// The inherited turn runs and settles through the ordinary path: the
	// transcript incorporation mark, then the completion hook the serve loop
	// calls when the turn returns.
	if err := restored.markClaimedUserTranscriptIncorporated(deadMutationID); err != nil {
		t.Fatalf("mark inherited turn incorporated: %v", err)
	}
	if err := restored.completeClientMutationTurn(deadMutationID); err != nil {
		t.Fatalf("complete inherited turn: %v", err)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID after the inherited turn settled = %q, want the slot released", got)
	}

	second, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("second claim: claimed=%#v ok=%v err=%v", second, ok, err)
	}
	if second.StableTurnID != followUpTurnID {
		t.Fatalf("second claim = %q, want the follow-up %q", second.StableTurnID, followUpTurnID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != followUpTurnID {
		t.Fatalf("ActiveTurnID after claiming the follow-up = %q, want it named active as %q", got, followUpTurnID)
	}
	// The named follow-up keeps the admission guard: a turn/start arriving while
	// it runs is refused exactly as it is during any other process-local turn.
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-during-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "a prompt while the follow-up runs"}},
	}); !isClientMutationConflict(err) {
		t.Fatalf("turn/start while the claimed follow-up runs = %v, want Conflict(\"turn is already active\")", err)
	}
}

// TestInterruptWhileRecoveredTurnRunsFencesTheInheritedTurn pins the review's
// second finding: the accepted follow-up must not steal the Stop.
//
// The interrupt names no turn; it fences whatever the durable slot names. If
// accepting the follow-up renamed the slot while the inherited turn was still
// in flight, a Stop would fence the follow-up -- cancelling the running turn
// while marking the user's brand-new pending start interrupted and clearing the
// slot, silently dropping the prompt they just sent.
func TestInterruptWhileRecoveredTurnRunsFencesTheInheritedTurn(t *testing.T) {
	restored, inheritedTurnID, _ := recoveredTurnSession(t)

	response, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "the follow-up"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart(follow-up): %v", err)
	}
	followUpTurnID := response.Turn.ID

	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the inherited turn: ok=%v err=%v", ok, err)
	}

	cancels := 0
	interrupt, err := restored.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "cm-stop",
	}, func() { cancels++ })
	if err != nil {
		t.Fatalf("interrupt while the inherited turn runs: %v", err)
	}
	if cancels != 1 {
		t.Fatalf("interrupt cancelled %d times, want 1", cancels)
	}
	if interrupt.Receipt.TurnID != inheritedTurnID {
		t.Fatalf("Stop fenced %q, want the inherited running turn %q (the follow-up is %q)",
			interrupt.Receipt.TurnID, inheritedTurnID, followUpTurnID)
	}
	snapshot := restored.clientMutations.snapshot()
	if record := snapshot.Journal["cm-dead-turn"]; record.ExecutionState != "interrupted" {
		t.Fatalf("inherited turn record after the Stop = %#v, want it interrupted", record)
	}
	if _, still := snapshot.PendingExecutions["cm-dead-turn"]; still {
		t.Fatal("the interrupted inherited turn remained a pending execution")
	}
	followUp, ok := snapshot.PendingExecutions["cm-follow-up"]
	if !ok || followUp.ExecutionState != "accepted" {
		t.Fatalf("follow-up pending after the Stop = %#v ok=%v, want it still accepted", followUp, ok)
	}
	if record := snapshot.Journal["cm-follow-up"]; record.ExecutionState == "interrupted" ||
		record.OperationState == clientMutationOperationTerminal {
		t.Fatalf("the Stop retired the follow-up's pending start: %#v", record)
	}
}

// TestClaimDuringInterruptFenceLeavesTheFollowUpPending is the race the review
// found: a Stop that lands between the claim's armed callback and the claim
// itself.
//
// An interrupt fence names the turn a Stop is ending and is finalized against
// that turn alone. A start claim that slips in behind the fence would take the
// follow-up admitted behind the fenced turn: the Stop's cancellation unwinds the
// fenced turn, finalization never reaches the follow-up, and the claimed start is
// left with no runner to run it -- claimed, undelivered, and with no interrupt to
// release it. The claim must refuse while the fence exists, and the follow-up
// must stay pending until the fence is finalized.
func TestClaimDuringInterruptFenceLeavesTheFollowUpPending(t *testing.T) {
	restored, inheritedTurnID, _ := recoveredTurnSession(t)

	response, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "the follow-up"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart(follow-up): %v", err)
	}
	followUpTurnID := response.Turn.ID

	// The Stop races this claim. Its fence is durable -- set by the accept below
	// -- while the claim is already in flight, and it finalizes only after the
	// claim has run. cancelAndWait is what holds it open, and the hook is the
	// claim's OWN armed callback, so the fence is provably set in the window
	// between that callback and the claim's serializer.
	fenceSet := make(chan struct{})
	releaseCancel := make(chan struct{})
	stopDone := make(chan error, 1)
	restored.cfg.testOnly.clientMutationStartClaiming = func() {
		restored.cfg.testOnly.clientMutationStartClaiming = nil
		go func() {
			_, stopErr := restored.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
				ClientMutationID: "cm-stop",
			}, func() {
				close(fenceSet)
				<-releaseCancel
			})
			stopDone <- stopErr
		}()
		<-fenceSet
	}

	claimed, ok, err := restored.claimClientMutationStart()
	if err != nil {
		t.Fatalf("claim during the interrupt fence: %v", err)
	}
	if ok {
		t.Fatalf("claim during the interrupt fence claimed %q, want nothing claimed", claimed.StableTurnID)
	}

	snapshot := restored.clientMutations.snapshot()
	if snapshot.InterruptFence == nil {
		t.Fatal("the fence was not set when the claim ran; the race was not reached")
	}
	followUp, ok := snapshot.PendingExecutions["cm-follow-up"]
	if !ok || followUp.ExecutionState != "accepted" {
		t.Fatalf("follow-up during the fence = %#v ok=%v, want it still accepted and unclaimed", followUp, ok)
	}
	if record := snapshot.Journal["cm-follow-up"]; record.ExecutionState != "accepted" {
		t.Fatalf("follow-up record during the fence = %#v, want it untouched", record)
	}
	if got := snapshot.ActiveTurnID; got != inheritedTurnID {
		t.Fatalf("ActiveTurnID during the fence = %q, want the fenced turn %q still running", got, inheritedTurnID)
	}

	// The Stop finalizes. It retires the fenced turn alone, so the follow-up is
	// still there, still accepted, and claimable the moment the fence is gone.
	close(releaseCancel)
	if err := <-stopDone; err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	snapshot = restored.clientMutations.snapshot()
	if snapshot.InterruptFence != nil {
		t.Fatalf("fence after the Stop finalized = %#v, want nil", snapshot.InterruptFence)
	}
	followUp, ok = snapshot.PendingExecutions["cm-follow-up"]
	if !ok || followUp.ExecutionState != "accepted" {
		t.Fatalf("follow-up after the fence finalized = %#v ok=%v, want it still accepted", followUp, ok)
	}

	claimed, ok, err = restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim after the fence finalized: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != followUpTurnID {
		t.Fatalf("claim after the fence finalized = %q, want the follow-up %q", claimed.StableTurnID, followUpTurnID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != followUpTurnID {
		t.Fatalf("ActiveTurnID after the follow-up is claimed = %q, want %q", got, followUpTurnID)
	}
}

// TestInterruptAfterAFreeSlotGrabFencesTheClaimedStart pins the free-slot grab
// race and the claim's authority over the slot.
//
// Accept names the slot only when it is free, so once the recovered turn settles
// and clears the slot, a LATER follow-up can be accepted into it and take the
// name before the serve loop claims the OLDEST pending start. The claim is the
// authority: it names the turn it is about to run. Without that, the older start
// runs with the slot pointed at the newer follow-up, and a Stop fences the
// follow-up -- dropping its prompt -- while the turn actually running is never
// stopped.
func TestInterruptAfterAFreeSlotGrabFencesTheClaimedStart(t *testing.T) {
	restored, _, deadMutationID := recoveredTurnSession(t)

	// The older follow-up is admitted behind the still-running inherited turn,
	// so it does NOT take the slot.
	olderResponse, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-older-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "the older follow-up"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart(older follow-up): %v", err)
	}
	olderTurnID := olderResponse.Turn.ID

	// The inherited turn runs and settles through the ordinary path, releasing
	// the slot.
	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the inherited turn: ok=%v err=%v", ok, err)
	}
	if err := restored.markClaimedUserTranscriptIncorporated(deadMutationID); err != nil {
		t.Fatalf("mark inherited turn incorporated: %v", err)
	}
	if err := restored.completeClientMutationTurn(deadMutationID); err != nil {
		t.Fatalf("complete inherited turn: %v", err)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID after the inherited turn settled = %q, want the slot released", got)
	}

	// The newer follow-up is accepted into the now-free slot, and the accept-side
	// rule names it -- the slot would otherwise be empty, which would drop the
	// admission guard for a concurrent turn/start.
	newerResponse, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-newer-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "the newer follow-up accepted into the free slot"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart(newer follow-up): %v", err)
	}
	newerTurnID := newerResponse.Turn.ID
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != newerTurnID {
		t.Fatalf("ActiveTurnID after the newer follow-up was accepted into the free slot = %q, want %q", got, newerTurnID)
	}

	// The claim takes the OLDEST pending start and is authoritative about the
	// slot: it re-names it for the turn about to run.
	claimed, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim the older follow-up: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != olderTurnID {
		t.Fatalf("claim = %q, want the OLDEST pending start %q", claimed.StableTurnID, olderTurnID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != olderTurnID {
		t.Fatalf("ActiveTurnID after the claim = %q, want the claimed turn %q (the free-slot grab named %q)",
			got, olderTurnID, newerTurnID)
	}

	// A Stop now fences the turn that is actually running.
	interrupt, err := restored.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "cm-stop",
	}, func() {})
	if err != nil {
		t.Fatalf("interrupt after the claim: %v", err)
	}
	if interrupt.Receipt.TurnID != olderTurnID {
		t.Fatalf("Stop fenced %q, want the claimed running turn %q", interrupt.Receipt.TurnID, olderTurnID)
	}
	snapshot := restored.clientMutations.snapshot()
	if record := snapshot.Journal["cm-older-follow-up"]; record.ExecutionState != "interrupted" {
		t.Fatalf("older follow-up after the Stop = %#v, want it interrupted", record)
	}
	if _, still := snapshot.PendingExecutions["cm-older-follow-up"]; still {
		t.Fatal("the interrupted older follow-up remained a pending execution")
	}
	newer, ok := snapshot.PendingExecutions["cm-newer-follow-up"]
	if !ok || newer.ExecutionState != "accepted" {
		t.Fatalf("newer follow-up after the Stop = %#v ok=%v, want it still accepted", newer, ok)
	}
	if inherit := snapshot.Journal[deadMutationID].ExecutionState; inherit == "interrupted" {
		t.Fatalf("the Stop retired the already-settled inherited turn: %q", inherit)
	}
}

// TestClaimOrdersALegacyInheritedTurnBeforeTheFollowUp pins recovery order when
// the resumed session's inherited turn carries a legacy id.
//
// A persisted turn id such as "turn_11" predates the "turn_m<N>" sequence
// spelling and does not parse, which sorts it LAST against any reservable id. A
// resumed session carrying one would then run the newly submitted prompt first
// and the user's older inherited prompt after it, inverting the order the user
// spoke. The inherited turn is prioritised by identity (s.recoveredTurnID), not
// by a sequence its legacy id does not carry.
func TestClaimOrdersALegacyInheritedTurnBeforeTheFollowUp(t *testing.T) {
	const deadMutationID = "cm-legacy-turn"
	const legacyTurnID = "turn_11"

	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	if _, err := crashed.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: deadMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "the prompt that died mid-turn"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart (dead turn): %v", err)
	}
	// Rewrite the accepted start's turn id to the legacy spelling a snapshot
	// written before the sequence scheme carries, exactly as it would load.
	if err := crashed.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending := snapshot.PendingExecutions[deadMutationID]
		pending.TurnID = legacyTurnID
		snapshot.PendingExecutions[deadMutationID] = pending
		record := snapshot.Journal[deadMutationID]
		record.StableTurnID = legacyTurnID
		snapshot.Journal[deadMutationID] = record
		reservation := snapshot.BudgetReservations[deadMutationID]
		reservation.TurnID = legacyTurnID
		snapshot.BudgetReservations[deadMutationID] = reservation
		snapshot.ActiveTurnID = legacyTurnID
		return nil
	}); err != nil {
		t.Fatalf("rewrite the accepted start to a legacy turn id: %v", err)
	}
	// The process dies here, with the legacy-id turn accepted but never run.
	crashed.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	t.Cleanup(restored.Close)
	if got := restored.recoveredTurnID; got != legacyTurnID {
		t.Fatalf("recoveredTurnID = %q, want the legacy inherited id %q", got, legacyTurnID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != legacyTurnID {
		t.Fatalf("restored ActiveTurnID = %q, want the legacy inherited turn %q", got, legacyTurnID)
	}

	response, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "the follow-up"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart(follow-up): %v", err)
	}
	followUpTurnID := response.Turn.ID
	if _, parses := clientMutationStartSequence(followUpTurnID); !parses {
		t.Fatalf("follow-up turn id %q does not parse; the test does not exercise legacy-vs-sequence ordering", followUpTurnID)
	}

	first, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("first claim: claimed=%#v ok=%v err=%v", first, ok, err)
	}
	if first.StableTurnID != legacyTurnID {
		t.Fatalf("first claim = %q, want the legacy inherited turn %q claimed first", first.StableTurnID, legacyTurnID)
	}
	second, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("second claim: claimed=%#v ok=%v err=%v", second, ok, err)
	}
	if second.StableTurnID != followUpTurnID {
		t.Fatalf("second claim = %q, want the follow-up %q claimed next", second.StableTurnID, followUpTurnID)
	}
}
