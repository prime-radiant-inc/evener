package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestACommittedWatchTriggerCarriesItsFrameInTheFirstRequest pins review
// `6b2d02a`'s high 2: a durably consumed report the model never saw.
//
// The admission gate asks the controller's wake set whether a frame's wake ever
// reached it. That set reports what the delegate still owes, and it shrinks:
// acceptance resolves the trigger before its generation runs, and CommitStart
// replaces the wake set with the rows still outstanding. A committed trigger has
// therefore left the set by the time its generation builds its first request.
//
// Reading that absence as "no arm admitted this row" is the defect. It answers a
// monotonic question — was this row ever admitted — from evidence that only
// reports what is still pending, so the projection withholds the very turn the
// generation accepted. The run then settles successfully: acceptDelegateAttention
// has already written the consumed marker, so the row reads delivered, and the
// request that spent it carried no report. The delegate runs on nothing.
//
// The invariant: the admission gate is asked only about rows the transcript still
// holds pending. A committed trigger is not pending — it is history whose order
// the release path owns — so the first request of a watch-triggered generation
// carries the frame it was started for.
func TestACommittedWatchTriggerCarriesItsFrameInTheFirstRequest(t *testing.T) {
	t.Parallel()
	sess, _, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// The generation under test starts from a clean history: the fixture's own
	// trigger is not part of what this request is expected to carry.
	sess.mu.Lock()
	sess.history = nil
	sess.mu.Unlock()

	// A stable watch frame lands in the transcript, and its own arm admits it.
	// That is the state the drive selects a trigger from: an original frame whose
	// wake passed the gate.
	const frameID = "watch:wd_committed:1"
	if _, err := sess.appendDelegateNotificationDurably(frameID, "a stable watch frame the drive will trigger on"); err != nil {
		t.Fatalf("append the watch frame: %v", err)
	}
	if _, _, err := openDelegateAttentionForTest(controller, "dlg_hold", frameID); err != nil {
		t.Fatalf("admit the frame's wake: %v", err)
	}

	// The drive binds the frame: acceptance writes the consumed marker and puts
	// the frame's turn in history before the marker can land.
	reservation, err := controller.ReserveAttention(sess, frameID)
	if err != nil {
		t.Fatalf("reserve the frame as the generation's trigger: %v", err)
	}
	if err := sess.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept the frame: %v", err)
	}
	// CommitStart publishes the generation, replacing the wake set with the rows
	// still owed. The trigger it just consumed is not one of them.
	if _, err := controller.CommitStart(reservation); err != nil {
		t.Fatalf("commit the generation: %v", err)
	}

	// The request that settles this generation, built by the production pair: the
	// projection capture and the hold filter.
	projection, holds := sess.delegateAttentionModelProjection()
	requested, _ := filterHeldDelegateAttentionTurns(projection.turns, holds)
	if indexOfAttentionTurn(requested, frameID) < 0 {
		t.Fatalf("the request that settles this generation does not carry the frame it was started for: the marker landed on a report the model never reads, so the delegate runs on nothing.\nholds=%v\nrequested=%v", holds, requested)
	}
}
