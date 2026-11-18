package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestAReopenedWatchRowIsNotAdmittedAsAFreshFrame pins review 53026's medium 1.
//
// A user stops an attention run before it presents its stable watch frame. The
// stop reopens the frame's row under /reopened/<generation>, so the reopened
// identity still carries the rail's "watch:" prefix — the frame's lineage, not
// its admission state. If the restore's admission pass keys only on that
// prefix, it reads the reopen as a frame nothing has admitted yet and journals
// a release for the very stop that reopened it. The stopped work then restarts
// with no new event.
//
// The two facts the prefix conflates: an original frame is durable in a cold
// transcript before any arm of its own runs, so admission must catch up with
// it; a reopen is minted by a stop, so its eligibility is decided by its own
// generation against the release floor and by nothing else.
func TestAReopenedWatchRowIsNotAdmittedAsAFreshFrame(t *testing.T) {
	t.Parallel()
	sess, _, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// The reopen a stop mints from a watch frame: same prefix, stop's own
	// generation in its segment.
	const frameID = "watch:wd_delivery:1"
	reopenedID := delegateAttentionReopenID(frameID, 1)
	if !isStableWatchAttentionID(reopenedID) {
		t.Fatalf("premise broken: %q does not carry the frame prefix", reopenedID)
	}
	if isOriginalStableWatchFrame(reopenedID) {
		t.Fatalf("predicate broken: the reopen %q reads as an original frame, so the restore would admit a release for its own stop", reopenedID)
	}
	if !isOriginalStableWatchFrame(frameID) {
		t.Fatalf("predicate broken: the original frame %q does not read as an original frame", frameID)
	}
	if _, err := sess.appendDelegateNotificationDurably(reopenedID, "the report the stop reopened"); err != nil {
		t.Fatalf("append the reopened watch row: %v", err)
	}
	controller.mu.Lock()
	state := controller.attention["dlg_hold"]
	admitted := false
	if state != nil {
		_, admitted = state.wakeIDs[reopenedID]
	}
	controller.mu.Unlock()
	if admitted {
		t.Fatalf("premise broken: the reopen is already an admitted wake")
	}

	sess.deriveHeldDelegateAttentionFromOutcome()

	// The decisive reading: no release may name the generation this row's own
	// stop settled. A release at generation 1 says a newer event superseded
	// that stop, and admitting the reopen wrote exactly that.
	if floor := controller.attentionReleaseFloor("dlg_hold"); floor >= 1 {
		t.Fatalf("the restore admitted the reopened watch row as a fresh frame: the release floor is %d, so a release now claims the stop at generation 1 was superseded by a newer event that never happened, and the stopped work restarts.\nreopened=%q", floor, reopenedID)
	}
}
