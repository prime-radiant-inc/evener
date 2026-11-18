package agent

import (
	"testing"
)

// TestAFailedStartReopenStaysInTheSuccessorsOwedSet pins the rule that a reopen
// whose generation never began is eligible on its own evidence.
//
// A busy commit reopens an unpresented report and another pending row starts
// next. The reopen claims the generation its failed start never completed, so
// no RunStarted was ever journaled for it: nothing quieted that row and no event
// owes it a release. A capture that keeps only rows the controller has admitted
// and rows the release floor has reached therefore drops it.
//
// Dropping it is not cosmetic. The committed batch computes attentionNeeded
// from that owed set and then replaces the controller's wake state with it, so
// the successor's commit clears NeedsAttention and the wake set while the report
// is still pending. If the user then stops the successor before presentation,
// there is nothing left to rearm and the report waits for unrelated work.
//
// The row this must not admit is the fresh watch frame: a frame whose wake no
// arm has reached is withheld until its own gate passes, and that stays true.
func TestAFailedStartReopenStaysInTheSuccessorsOwedSet(t *testing.T) {
	t.Parallel()
	const (
		sessionID = "child-dlg_target"
		freshID   = "delegate:delivery-attention-fresh"
	)
	// The reopen of a row whose presentation start failed: the start consumed
	// the row, never presented it, and re-delivered it under the failed
	// generation's key. No run ever began at that generation.
	sourceID := "delegate:delivery-attention-1"
	const failedGeneration = 7
	reopenedID := delegateAttentionReopenID(sourceID, failedGeneration)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, freshID)
	if _, err := runtime.appendDelegateNotificationDurably(reopenedID, "the report a failed start reopened"); err != nil {
		t.Fatalf("append the reopened row: %v", err)
	}
	// Preconditions the finding's premise needs: the row is neither admitted
	// nor released, and no run ever began at its generation.
	c.mu.Lock()
	state := c.attention["dlg_target"]
	_, isWake := state.wakeIDs[reopenedID]
	_, isRider := state.riderIDs[reopenedID]
	_, generationStarted := c.runStarts[delegateLease{delegateID: "dlg_target", generation: failedGeneration}]
	c.mu.Unlock()
	if isWake || isRider {
		t.Fatalf("premise broken: the reopened row is already admitted (wake=%t rider=%t)", isWake, isRider)
	}
	if generationStarted {
		t.Fatalf("premise broken: generation %d did begin, so this row is a stop's own reopen and not a failed start's", failedGeneration)
	}
	if floor := c.attentionReleaseFloor("dlg_target"); floor >= failedGeneration {
		t.Fatalf("premise broken: the release floor is %d, so the row is already released", floor)
	}

	reservation, err := c.ReserveAttention(runtime, freshID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept the fresh row: %v", err)
	}
	c.mu.Lock()
	var owed []string
	for _, record := range c.reservations {
		if record != nil && record.delegateID == "dlg_target" {
			owed = append(owed, record.attentionPendingIDs...)
		}
	}
	c.mu.Unlock()
	kept := false
	for _, id := range owed {
		if id == reopenedID {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("the successor's owed set is %v and the failed start's reopen %q is not in it: its generation never began, so no release is owed to make it eligible, and the commit will clear NeedsAttention and the wake set while the report is still pending.\nfloor=%d reopened=%q", owed, reopenedID, c.attentionReleaseFloor("dlg_target"), reopenedID)
	}
}

// TestAFreshWatchFrameStaysOutOfTheSuccessorsOwedSet is the other direction of
// the same capture: a frame whose wake no arm has reached must not ride a
// generation that never admitted it.
func TestAFreshWatchFrameStaysOutOfTheSuccessorsOwedSet(t *testing.T) {
	t.Parallel()
	const (
		sessionID = "child-dlg_target"
		freshID   = "delegate:delivery-attention-fresh"
	)
	const frameID = "watch:wd_unadmitted:1"
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, freshID)
	if _, err := runtime.appendDelegateNotificationDurably(frameID, "a frame no arm has admitted"); err != nil {
		t.Fatalf("append the watch frame: %v", err)
	}
	c.mu.Lock()
	state := c.attention["dlg_target"]
	admitted := false
	if state != nil {
		_, admitted = state.wakeIDs[frameID]
	}
	c.mu.Unlock()
	if admitted {
		t.Fatalf("premise broken: the frame is already admitted")
	}

	reservation, err := c.ReserveAttention(runtime, freshID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept the fresh row: %v", err)
	}
	c.mu.Lock()
	var owed []string
	for _, record := range c.reservations {
		if record != nil && record.delegateID == "dlg_target" {
			owed = append(owed, record.attentionPendingIDs...)
		}
	}
	c.mu.Unlock()
	for _, id := range owed {
		if id == frameID {
			t.Fatalf("the successor's owed set is %v and it carries the unadmitted frame %q: the frame's wake never passed its gate, so the generation must not publish it", owed, frameID)
		}
	}
}
