package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestASolePendingWatchTriggerIsConsumedAndStillRequested pins the rule that a
// successful empty fold is an answer, not a missing one.
//
// A committed trigger carries two facts at once when it is the delegate's only
// pending row: acceptance resolves it, and the transcript fold then reports an
// EMPTY pending set. That empty answer is not a missing one — it says the
// transcript owes nothing, which is exactly what a generation that consumed its
// sole row produces.
//
// Treating it as missing is the defect. A nil set means "the fold could not
// answer", so the admission gate falls back to the controller's wake set; the
// trigger has left that set too, so the gate concludes no arm ever admitted it
// and withholds it. The generation finishes with its trigger durably consumed
// and absent from the request — the delegate runs on nothing.
//
// The invariant: a successful empty fold is an answer and must be passed through
// as one. Only a fold that could not run leaves the gate to its fallback.
func TestASolePendingWatchTriggerIsConsumedAndStillRequested(t *testing.T) {
	t.Parallel()
	sess, fixtureReopenID, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// The fixture leaves its own reopened row pending. Resolve it so the watch
	// frame below is the delegate's ONLY pending row: with a second row present a
	// non-empty set would carry the gate and this case would never be reached.
	if err := sess.resolveAttentionDurablyForGeneration([]string{fixtureReopenID}, delegateAttentionConsumed, 0); err != nil {
		t.Fatalf("clear the fixture's pending row: %v", err)
	}
	pendingBefore, err := sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("pending attention before the frame: %v", err)
	}
	if len(pendingBefore) != 0 {
		t.Fatalf("premise broken: %d row(s) still pending before the frame (%v), so the frame would not be the sole one", len(pendingBefore), pendingBefore)
	}

	sess.mu.Lock()
	sess.history = nil
	sess.mu.Unlock()

	const frameID = "watch:wd_sole:1"
	if _, err := sess.appendDelegateNotificationDurably(frameID, "the only pending row this generation will consume"); err != nil {
		t.Fatalf("append the watch frame: %v", err)
	}
	if _, _, err := openDelegateAttentionForTest(controller, "dlg_hold", frameID); err != nil {
		t.Fatalf("admit the frame's wake: %v", err)
	}
	reservation, err := controller.ReserveAttention(sess, frameID)
	if err != nil {
		t.Fatalf("reserve the frame as the trigger: %v", err)
	}
	if err := sess.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept the frame: %v", err)
	}
	if _, err := controller.CommitStart(reservation); err != nil {
		t.Fatalf("commit the generation: %v", err)
	}

	// The generation consumed its sole pending row; the fold now answers empty.
	pendingAfter, err := sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("pending attention after the commit: %v", err)
	}
	if len(pendingAfter) != 0 {
		t.Fatalf("premise broken: the fold still reports %v pending, so this case is not the empty answer it pins", pendingAfter)
	}

	projection, holds := sess.delegateAttentionModelProjection()
	requested, _ := filterHeldDelegateAttentionTurns(projection.turns, holds)
	if indexOfAttentionTurn(requested, frameID) < 0 {
		t.Fatalf("the sole pending trigger was consumed and is absent from the request: the empty fold read as unavailable, the gate fell back to the wake set, and the row it just consumed was withheld.\nholds=%v\nrequested=%v", holds, requested)
	}
}
