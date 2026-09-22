package agent

import (
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

	// Only the inherited turn earns the pass. A third start arriving while the
	// accepted follow-up names the active turn is still refused: the web
	// composer's routing contract depends on that answer.
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-third-prompt",
		Input:            []appwire.InputItem{{Type: "text", Text: "a third prompt"}},
	}); !isClientMutationConflict(err) {
		t.Fatalf("third turn/start error = %v, want Conflict(\"turn is already active\")", err)
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
