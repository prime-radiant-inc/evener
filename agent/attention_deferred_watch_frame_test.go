package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestADeferredWatchFrameStaysOutOfSelectionAndTheRequest pins review 53026's
// medium 2.
//
// A cold watch frame is already in the resumed transcript when the restore runs,
// and its wake admission can fail there — the delegate's lane may be held, or
// the row's gate may be blocked by a reservation still settling. Skipping a
// second retain is not enough: the shared restore has already loaded the
// frame's turn into history, so automatic selection still offers its ID and the
// projection still carries its turn. After the journal recovers, an older wake
// can start a generation that presents and consumes the frame while the stopped
// report the frame was meant to release stays held, and the delayed arm then
// finds the row resolved and skips its release.
//
// The invariant: a frame whose wake has not passed its gate is withheld from
// both answers — selection and the request — because they must never disagree
// about whether the row is presentable. The row's durable entry stays, so its
// own arm still publishes it.
func TestADeferredWatchFrameStaysOutOfSelectionAndTheRequest(t *testing.T) {
	t.Parallel()
	sess, _, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	const frameID = "watch:wd_pending:1"
	if _, err := sess.appendDelegateNotificationDurably(frameID, "a stable watch frame whose arm has not run"); err != nil {
		t.Fatalf("append the watch frame: %v", err)
	}
	// The frame's wake is blocked: a reservation for this delegate is still
	// settling, which is exactly the state that makes admission return a
	// blocker instead of admitting. The restore must then defer the row.
	blocker := make(chan struct{})
	controller.mu.Lock()
	controller.reservations[99] = &delegateStartRecord{
		delegateID: "dlg_hold",
		generation: 2,
		trigger:    delegatestore.TriggerAttention,
		done:       blocker,
	}
	state := controller.attention["dlg_hold"]
	admitted := false
	if state != nil {
		_, admitted = state.wakeIDs[frameID]
	}
	controller.mu.Unlock()
	if admitted {
		t.Fatalf("premise broken: the frame is already admitted")
	}

	sess.deriveHeldDelegateAttentionFromOutcome()

	// The frame's wake could not be admitted, so it was deferred. Read the two
	// answers production reads: the automatic list the drive selects from, and
	// the projection-plus-filter pair a request is built from.
	automatic, err := sess.automaticDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("automatic attention: %v", err)
	}
	for _, id := range automatic {
		if id == frameID {
			t.Fatalf("automatic selection offers the deferred frame %q (%v): its wake never passed its gate, so a generation that claims it consumes a row no request may present", frameID, automatic)
		}
	}
	projection, holds := sess.delegateAttentionModelProjection()
	requested, _ := filterHeldDelegateAttentionTurns(projection.turns, holds)
	for _, turn := range requested {
		if turn.AttentionID == frameID {
			t.Fatalf("the request carries the deferred frame %q even though selection withheld it: the two answers disagree, which is the split this finding names.\nrequested=%v", frameID, requested)
		}
	}
	// The other direction: the deferred frame must not take the stopped report
	// down with it. A row quieted by its own stop is a different reason, and the
	// report stays pending (not presented) rather than being dropped from the
	// row set entirely.
	pending, err := sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("pending attention: %v", err)
	}
	found := false
	for _, id := range pending {
		if id == frameID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the deferred frame's durable row is gone from the pending set: it must stay for its own arm to publish (%v)", pending)
	}
}
