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
