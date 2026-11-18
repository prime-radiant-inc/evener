package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestProjectionReturnsAWritableHoldSetWithNoHoldInstalled pins the nil-map
// panic CI caught on the ordinary cold path.
//
// attentionAutomaticDriveHolds is only allocated by the hold installer. A
// session that has never installed a hold therefore reaches the projection with
// a nil map, and maps.Clone of a nil map is nil. The projection then adds every
// row withheld for lack of admission to that set, so the assignment panicked on
// any cold session that had a pending attention turn in history — before any
// attention drive could run at all.
//
// The invariant: the projection's returned hold set is always writable, because
// this function adds to it. Anything reading the set treats empty and nil the
// same (filterHeldDelegateAttentionTurns returns early on len == 0), so making
// it non-nil is invisible to every consumer but the writer.
func TestProjectionReturnsAWritableHoldSetWithNoHoldInstalled(t *testing.T) {
	t.Parallel()
	sess, _, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	// The cold path's premise: nothing has installed a hold on this session, so
	// the map is nil rather than merely empty.
	sess.attentionMu.Lock()
	sess.attentionAutomaticDriveHolds = nil
	sess.attentionMu.Unlock()
	// A pending turn for a frame whose wake no arm has reached: the one kind of
	// row the projection writes into the set.
	const frameID = "watch:wd_nil_hold:1"
	if _, err := sess.appendDelegateNotificationDurably(frameID, "a frame awaiting its arm"); err != nil {
		t.Fatalf("append the watch frame: %v", err)
	}
	if !sess.delegateAttentionAwaitingAdmissionLocked(frameID) {
		t.Fatalf("premise broken: %q does not read as awaiting admission, so the projection writes nothing and the panic cannot be reached", frameID)
	}

	projection, holds := sess.delegateAttentionModelProjection()

	if holds == nil {
		t.Fatalf("the projection returned a nil hold set with no hold installed: it adds withheld rows to this set, so the next assignment panics on the ordinary cold path")
	}
	carried := false
	for _, turn := range projection.turns {
		if turn.AttentionID == frameID {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("premise broken: the frame's turn is not in the projected history, so nothing exercises the write")
	}
	if _, withheld := holds[frameID]; !withheld {
		t.Fatalf("the projection's hold set %v does not carry the awaiting-admission frame %q: the request would present a row selection withholds", holds, frameID)
	}
}
