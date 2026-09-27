package agent

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestClaimOrdersALegacyInheritedTurnBeforeTheFollowUp keeps the rule the
// inherited priority exists for: an inherited turn whose id is a legacy
// "turn_11" spelling carries no reserved sequence to compare, so only the
// explicit priority orders it ahead of a follow-up admitted behind it.
func TestClaimOrdersALegacyInheritedTurnBeforeTheFollowUp(t *testing.T) {
	t.Parallel()
	restored, _, deadMutationID := recoveredTurnSession(t)

	const legacy = "turn_11"
	if err := restored.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending := snapshot.PendingExecutions[deadMutationID]
		pending.TurnID = legacy
		snapshot.PendingExecutions[deadMutationID] = pending
		record := snapshot.Journal[deadMutationID]
		record.StableTurnID = legacy
		snapshot.Journal[deadMutationID] = record
		snapshot.ActiveTurnID = legacy
		return nil
	}); err != nil {
		t.Fatalf("rewrite the inherited turn id: %v", err)
	}
	restored.recoveredTurnID = legacy

	// The follow-up is admitted while the inherited turn is claimed, and the
	// inherited claim is then handed back -- the state the recovered give-back
	// leaves -- so both starts are claimable together.
	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the legacy inherited turn: ok=%v err=%v", ok, err)
	}
	acceptFollowUp(t, restored, "cm-follow-up", "the follow-up")
	if err := restored.returnClaimedClientMutationStart(deadMutationID); err != nil {
		t.Fatalf("hand the legacy inherited claim back: %v", err)
	}

	claimed, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != legacy {
		t.Fatalf("claim = %q, want the legacy inherited turn %q first", claimed.StableTurnID, legacy)
	}
}

// TestClaimOrdersTheOlderFollowUpWhenANewerOneIsCapturedAsInherited pins the
// crash the unconditional inherited priority caused. Once the recovered turn
// completes, the accept side names the slot only when it is FREE, so a NEWER
// follow-up can hold the freed slot while an OLDER follow-up is still pending
// accepted. If the process dies before the claim re-names the slot, restore
// captures that NEWER turn as the inherited one. Prioritising the inherited turn
// unconditionally then runs the newer prompt before the older one -- the very
// out-of-order replay this ordering exists to prevent. Sequence order already
// puts a genuinely inherited turn first (it was reserved earlier), so the
// priority must apply only when the inherited id has no sequence to compare.
func TestClaimOrdersTheOlderFollowUpWhenANewerOneIsCapturedAsInherited(t *testing.T) {
	t.Parallel()
	restored, _, deadMutationID := recoveredTurnSession(t)

	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the inherited turn: ok=%v err=%v", ok, err)
	}
	older := acceptFollowUp(t, restored, "cm-follow-up-older", "the older follow-up")
	newer := acceptFollowUp(t, restored, "cm-follow-up-newer", "the newer follow-up")
	if older.Turn.ID == newer.Turn.ID {
		t.Fatalf("fixture: both follow-ups have turn id %q", older.Turn.ID)
	}

	// The recovered turn finished and released the slot; a crash before the claim
	// re-named it left the NEWER follow-up holding the slot, so restore captures
	// it as the inherited turn.
	if err := restored.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		delete(snapshot.PendingExecutions, deadMutationID)
		delete(snapshot.Journal, deadMutationID)
		snapshot.ActiveTurnID = newer.Turn.ID
		return nil
	}); err != nil {
		t.Fatalf("model the crash capture: %v", err)
	}
	restored.recoveredTurnID = newer.Turn.ID

	claimed, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != older.Turn.ID {
		t.Fatalf("claim = %q, want the OLDER follow-up %q before the newer %q captured as inherited",
			claimed.StableTurnID, older.Turn.ID, newer.Turn.ID)
	}
}

// TestRunnableNamesTheStartTheClaimTakesWhenAQueueEntryIsAlsoClaimable pins the
// runnable predicate to the claim path's precedence. With a claimable start AND
// a claimable queue entry present, the claim path takes the ordered start head;
// a predicate that answered from the map's iteration order could name the queue
// turn instead, arming cancellation for a turn the claim does not take.
func TestRunnableNamesTheStartTheClaimTakesWhenAQueueEntryIsAlsoClaimable(t *testing.T) {
	t.Parallel()
	sess := newQueuePersistTestSession(t, t.TempDir())
	t.Cleanup(sess.Close)

	// A claimable start.
	started, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-pending-start",
		Input:            []appwire.InputItem{{Type: "text", Text: "the pending start"}},
	})
	if err != nil {
		t.Fatalf("accept the start: %v", err)
	}
	// A queue entry the transcript already holds: queued, claimed through the
	// queue path, then marked incorporated -- the queue branch's qualifying shape.
	queueOneMutation(t, sess, "cm-incorporated-queue", "a queued message")
	queued, refusal := sess.popQueueHeadRefusingPoison()
	if refusal != nil || queued.ClientMutationID != "cm-incorporated-queue" {
		t.Fatalf("claim the queued message: queued=%#v refusal=%v", queued, refusal)
	}
	queueTurnID := queued.StableTurnID
	if queueTurnID == "" {
		t.Fatal("fixture: the claimed queued message has no stable turn id")
	}
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		pending := snapshot.PendingExecutions["cm-incorporated-queue"]
		pending.ExecutionState = "incorporated"
		snapshot.PendingExecutions["cm-incorporated-queue"] = pending
		record := snapshot.Journal["cm-incorporated-queue"]
		record.ExecutionState = "incorporated"
		snapshot.Journal["cm-incorporated-queue"] = record
		snapshot.ActiveTurnID = queueTurnID
		return nil
	}); err != nil {
		t.Fatalf("mark the queued mutation incorporated: %v", err)
	}
	startTurnID := started.Turn.ID

	named, runnable := sess.runnableClientMutationStartTurnID()
	if !runnable {
		t.Fatal("the claimable start was not reported runnable")
	}
	if named != startTurnID {
		t.Fatalf("runnable named %q, want the claimable start %q, not the queue entry %q", named, startTurnID, queueTurnID)
	}

	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != named {
		t.Fatalf("the claim took %q but the runnable predicate named %q", claimed.StableTurnID, named)
	}
	if claimed.StableTurnID != startTurnID {
		t.Fatalf("claim = %q, want the start %q the predicate named", claimed.StableTurnID, startTurnID)
	}
}
