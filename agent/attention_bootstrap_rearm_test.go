package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestDeriveEndsItsOwnHoldWhenTheWakeIsAlreadyNoted pins review 53026's medium 4.
//
// A daemon restart that must recover a descendant's accepted start rebuilds its
// idle ancestors before bootstrap reconciles the transcript's wakes. An ancestor
// whose report a stop quieted therefore derives a hold while the controller
// still has no wake noted for that report. Bootstrap notes it afterwards, and
// that note is the restart's re-arm: the report is meant to present. But
// presentation reconciles against release generations only, and a noted wake
// journals no release when the run it supersedes never settled — so nothing
// ever ends the hold, and the ancestor keeps suppressing the very report the
// restart re-armed.
//
// The invariant: the derivation decides from evidence, so the pass that can see
// a noted wake must also undo a hold an earlier pass installed without it. The
// noted set is read, never predicted, which is what keeps an ordinary
// same-process reclaim — where the noted set is the live one and the hold is
// the stop's current quiet — deciding exactly as it did before.
func TestDeriveEndsItsOwnHoldWhenTheWakeIsAlreadyNoted(t *testing.T) {
	t.Parallel()
	sess, reopenedID, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// The earlier pass: derive with no wake noted, which installs the quiet.
	sess.deriveHeldDelegateAttentionFromOutcome()
	sess.attentionMu.Lock()
	heldGeneration, installed := sess.attentionAutomaticDriveHolds[reopenedID]
	sess.attentionMu.Unlock()
	if !installed {
		t.Fatalf("premise broken: the first derivation installed no hold for %q, so there is no stale hold to end", reopenedID)
	}
	// Bootstrap reconciles the transcript and notes the row's wake. The note is
	// the restart's re-arm: the report is meant to present now.
	controller.mu.Lock()
	controller.noteDelegateAttentionLocked("dlg_hold", reopenedID)
	noted, isRider := func() (bool, bool) {
		state := controller.attention["dlg_hold"]
		_, wake := state.wakeIDs[reopenedID]
		_, rider := state.riderIDs[reopenedID]
		return wake, rider
	}()
	controller.mu.Unlock()
	if !noted || isRider {
		t.Fatalf("premise broken: the re-arm did not note the wake (noted=%t rider=%t)", noted, isRider)
	}

	// The pass that can see the note.
	sess.deriveHeldDelegateAttentionFromOutcome()

	automatic, err := sess.automaticDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("automatic attention: %v", err)
	}
	for _, id := range automatic {
		if id == reopenedID {
			return
		}
	}
	sess.attentionMu.Lock()
	holds := map[string]uint64{}
	for id, generation := range sess.attentionAutomaticDriveHolds {
		holds[id] = generation
	}
	sess.attentionMu.Unlock()
	t.Fatalf("the re-armed report %q is still withheld after bootstrap noted its wake: the ancestor keeps suppressing the report the restart re-armed (hold=%d installed, holds now %v, automatic=%v)", reopenedID, heldGeneration, holds, automatic)
}
