package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestAnUnavailableFoldStillPresentsTheAcceptedTrigger pins that a transient
// transcript read failure cannot consume a committed watch trigger silently.
//
// Acceptance resolves the trigger and CommitStart clears the controller's wake
// set, so afterwards the two pieces of evidence a request can consult are the
// pending fold and the accepted-trigger latch. A fold read that fails answers
// neither "pending" nor "resolved": it is unavailable. Falling back to the wake
// set in that state answers the monotonic question — was this row ever admitted
// — from a set that now reports only what is still owed, so the generation's own
// trigger reads as unadmitted and the projection withholds it. The run then
// finishes successfully having durably consumed a report the model never
// received. The latch is the record of that admission, so it answers what the
// unavailable fold could not.
func TestAnUnavailableFoldStillPresentsTheAcceptedTrigger(t *testing.T) {
	t.Parallel()
	sess, fixtureReopenID, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// The fixture's own reopened row would otherwise be the second pending row
	// and mask the sole-trigger case.
	if err := sess.resolveAttentionDurablyForGeneration([]string{fixtureReopenID}, delegateAttentionConsumed, 0); err != nil {
		t.Fatalf("clear the fixture's pending row: %v", err)
	}
	sess.mu.Lock()
	sess.history = nil
	sess.mu.Unlock()

	const frameID = "watch:wd_foldfail:1"
	if _, err := sess.appendDelegateNotificationDurably(frameID, "the trigger this generation commits to"); err != nil {
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

	// The premise, and without it this test cannot exercise the defect: the
	// commit must have cleared the controller's wake set for the trigger, so the
	// only surviving evidence of its admission is the latch. The fold answers
	// empty here, which is the state a read failure also produces.
	sess.attentionMu.Lock()
	latched := sess.attentionAcceptedID
	pendingIDs := sess.delegateAttentionPendingIDsLocked()
	sess.attentionMu.Unlock()
	if latched != frameID {
		t.Fatalf("premise broken: the accepted latch is %q, want %q", latched, frameID)
	}
	if len(pendingIDs) != 0 {
		t.Fatalf("premise broken: the fold still reports %v pending, so the trigger is not sole", pendingIDs)
	}
	controller.mu.Lock()
	state := controller.attention["dlg_hold"]
	inWake, inRider := false, false
	if state != nil {
		_, inWake = state.wakeIDs[frameID]
		_, inRider = state.riderIDs[frameID]
	}
	controller.mu.Unlock()
	if inWake || inRider {
		t.Fatalf("premise broken: the wake set still names the trigger (wake=%t rider=%t), so the fallback would find it and no latch is needed", inWake, inRider)
	}

	// The defect: with the fold unavailable, the wake set no longer names the
	// trigger, so a gate that falls back to it withholds the row the generation
	// just accepted.
	sess.attentionMu.Lock()
	withheldOnFallback := sess.delegateAttentionAwaitingAdmissionLocked(frameID, delegateAttentionPendingSet(nil))
	sess.attentionMu.Unlock()
	if withheldOnFallback {
		t.Fatalf("an unavailable fold withheld the generation's own accepted trigger %q: the wake set no longer names it, so the request drops a report acceptance durably consumed", frameID)
	}

	// And the latch must speak only for its own trigger: a foreign unadmitted
	// frame in the same delegate stays withheld.
	const foreignID = "watch:wd_foldfail:2"
	if _, err := sess.appendDelegateNotificationDurably(foreignID, "a frame nothing admitted"); err != nil {
		t.Fatalf("append the foreign frame: %v", err)
	}
	sess.attentionMu.Lock()
	foreignWithheld := sess.delegateAttentionAwaitingAdmissionLocked(foreignID, delegateAttentionPendingSet(nil))
	sess.attentionMu.Unlock()
	if !foreignWithheld {
		t.Fatalf("a foreign unadmitted frame %q was admitted: the latch must speak only for the trigger it holds", foreignID)
	}
}
