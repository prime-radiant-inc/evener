package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestARestoredRuntimeAdmitsAWatchFrameBeforeItsTurnIsVisible pins review
// 52973's medium.
//
// The sequence: a user's stop settles generation 1 and reopens its trigger
// under /reopened/1, and the stop's quiet now covers that reopened report. A
// stable watch frame then lands in the same delegate's transcript. The watch
// rail writes the frame durably and holds no controller, so at that instant
// the transcript carries a pending row whose wake nothing has admitted — and
// the frame's own wake is exactly the newer evidence that releases the stop's
// quiet.
//
// If the restore publishes every pending turn before any arm of the frame's
// own runs, the two facts the request needs disagree with each other: history
// carries the frame, while the map presentation filters against still holds the
// stopped report out and the release that should have freed it has not been
// journaled. An older wake can then start a generation that presents and
// consumes the frame while omitting the report the frame was admitted to
// release, and the delayed arm finds the frame resolved and skips the release
// entirely. The report then waits for an unrelated event.
//
// The invariant the fix establishes: a stable watch frame's wake is admitted
// before its turn is visible, so nothing can be selected that has not passed
// the gate that also journals its release.
func TestARestoredRuntimeAdmitsAWatchFrameBeforeItsTurnIsVisible(t *testing.T) {
	t.Parallel()
	sess, reopenedID, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// The frame the watch rail writes: durable, named by the rail's own
	// identity, and admitted by nobody yet. This is the whole premise — a row
	// that exists in the transcript before its wake exists in the controller.
	const frameID = "watch:wd_delivery:1"
	if !isStableWatchAttentionID(frameID) {
		t.Fatalf("premise broken: %q does not read as a stable watch frame", frameID)
	}
	if _, err := sess.appendDelegateNotificationDurably(frameID, "a stable watch frame"); err != nil {
		t.Fatalf("append the watch frame: %v", err)
	}
	controller.mu.Lock()
	state := controller.attention["dlg_hold"]
	alreadyAdmitted, alreadyRider := false, false
	if state != nil {
		_, alreadyAdmitted = state.wakeIDs[frameID]
		_, alreadyRider = state.riderIDs[frameID]
	}
	controller.mu.Unlock()
	if alreadyAdmitted || alreadyRider {
		t.Fatalf("premise broken: the frame is already admitted (wake=%t rider=%t), so no unadmitted row exists", alreadyAdmitted, alreadyRider)
	}

	sess.deriveHeldDelegateAttentionFromOutcome()

	// The frame's wake was admitted, and admitting it journaled the release
	// that ends the stop's quiet. Both are read through production accessors.
	if floor := controller.attentionReleaseFloor("dlg_hold"); floor < 1 {
		t.Fatalf("the restore left the release floor at %d after a watch frame whose wake releases generation 1: the frame's admission never ran, so the stopped report stays quiet and the frame presents alone.\nreopened=%q frame=%q", floor, reopenedID, frameID)
	}
	// The frame's turn is visible only because its wake is: the admission and
	// the visibility are one decision, so a row the map would hold out can
	// never be the row history ends on.
	sess.mu.Lock()
	sawFrame, sawReopened := false, false
	for _, turn := range sess.history {
		if turn.AttentionID == frameID {
			sawFrame = true
		}
		if turn.AttentionID == reopenedID {
			sawReopened = true
		}
	}
	sess.mu.Unlock()
	if !sawFrame {
		t.Fatalf("the watch frame's turn is not in the restored history, so the generation that presents it has nothing to show")
	}
	// The stopped report is released by the same admission, so it is
	// presentable rather than held behind the frame that released it.
	sess.attentionMu.Lock()
	holds := map[string]uint64{}
	for id, generation := range sess.attentionAutomaticDriveHolds {
		holds[id] = generation
	}
	sess.attentionMu.Unlock()
	if _, held := holds[reopenedID]; held && sawReopened {
		t.Fatalf("the stopped report %q is in history but still held (%v): the frame's own wake released it, so the hold must end with the admission that made the frame visible", reopenedID, holds)
	}
	// The decisive reading: the automatic list is what the drive selects from
	// and what the accept's rider capture reads. A frame whose wake was not
	// admitted has no business being selected ahead of the report it releases.
	automatic, err := sess.automaticDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("automatic attention: %v", err)
	}
	var joined strings.Builder
	joined.WriteString(strings.Join(automatic, " "))
	if !strings.Contains(joined.String(), frameID) {
		t.Fatalf("the restored runtime does not offer the watch frame to the automatic drive (%v): a frame whose wake was admitted must present", automatic)
	}
	// The same admission released the stop's quiet, so the stopped report is
	// eligible too: the request that carries the frame must be able to carry
	// the report it released, which is the pair a resident release produces.
	if !strings.Contains(joined.String(), reopenedID) {
		t.Fatalf("the stopped report %q is released (floor=%d) but the automatic list still holds it out (%v): the frame's wake released it, so selecting the frame alone re-creates the omission the finding describes", reopenedID, controller.attentionReleaseFloor("dlg_hold"), automatic)
	}
}
