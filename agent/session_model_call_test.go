package agent

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestShiftReplayBoundaryForDropsCountsAdjacentDrops pins the off-by-one that
// made a turn's own first response read as prior history (#2340 review).
//
// droppedAt holds ORIGINAL indices while the boundary is decremented as the loop
// runs. Two adjacent rows immediately before the boundary sit at boundary-2 and
// boundary-1: after the first drop the boundary equals the second row's index,
// so comparing the original index against the MOVING boundary undercounts. The
// boundary then stays one too far on, the turn's own assistant response loses
// the in-flight replay exemption, and its thinking blocks are stripped.
func TestShiftReplayBoundaryForDropsCountsAdjacentDrops(t *testing.T) {
	t.Parallel()
	// Two adjacent rows before boundary 5 must move it by TWO, to 3.
	if got := shiftReplayBoundaryForDrops(5, []int{3, 4}); got != 3 {
		t.Fatalf("two adjacent drops before the boundary moved it to %d, want 3: a boundary left one too far on makes the turn's own first response read as prior history, losing the thinking the in-flight exemption keeps", got)
	}
	// Non-vacuity: a drop AFTER the boundary does not move it, so the arm above
	// cannot pass by shifting for every entry.
	if got := shiftReplayBoundaryForDrops(5, []int{5, 6}); got != 5 {
		t.Fatalf("a drop at or after the boundary moved it to %d, want 5: the boundary sits before those rows and must not move", got)
	}
	// A mixed set counts only the drops strictly before the boundary: two of
	// [1 4 9] precede 5, so it moves to 3.
	if got := shiftReplayBoundaryForDrops(5, []int{1, 4, 9}); got != 3 {
		t.Fatalf("drops at [1 4 9] moved boundary 5 to %d, want 3 (two of them precede it)", got)
	}
}

// TestDropDiscardedWatchAttentionTurnsKeepsNonWatchHistory pins the second half
// of the projection decision: a DISCARDED watch row must not reach the model,
// and no other turn may be touched (#2340 review).
//
// The live path deletes the resident turn when it discards. A restart rebuilds
// history from the transcript, where the turn is still recorded, so a
// superseded or stop-dropped row came back after every resume.
func TestDropDiscardedWatchAttentionTurnsKeepsNonWatchHistory(t *testing.T) {
	t.Parallel()
	watchTurn := func(id string) schema.Turn {
		return schema.Turn{Kind: schema.TurnSteering, AttentionID: id}
	}
	discarded := map[string]struct{}{
		"watchdiag:watch:a:job:a:abc": {},
		"watch:dlv_discarded:7":       {},
	}
	turns := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("ordinary history")},
		watchTurn("watchdiag:watch:a:job:a:abc"),
		watchTurn("watch:dlv_discarded:7"),
		// A CONSUMED (not discarded) watch row stays: the model has still to
		// read it, and only the discard says it never should.
		watchTurn("watch:dlv_kept:3"),
		// A non-watch attention turn is a different authority's business.
		watchTurn("delegate:dlg_x:report"),
	}
	kept, droppedAt := dropDiscardedWatchAttentionTurns(turns, discarded)
	if len(droppedAt) != 2 {
		t.Fatalf("dropped %d turn(s) at %v, want the two discarded watch rows", len(droppedAt), droppedAt)
	}
	if len(kept) != 3 {
		t.Fatalf("kept %d turn(s), want 3: the ordinary turn, the unread watch row and the delegate attention turn", len(kept))
	}
	for _, turn := range kept {
		if _, gone := discarded[turn.AttentionID]; gone {
			t.Fatalf("discarded watch row %q survived the projection: the model is told to act on a notice it was told to forget", turn.AttentionID)
		}
	}
	// nil report keeps every turn, so a caller with nothing discarded pays
	// nothing and loses nothing.
	if got, at := dropDiscardedWatchAttentionTurns(turns, nil); len(got) != len(turns) || at != nil {
		t.Fatalf("an empty discard set dropped %d turn(s): the filter must be inert", len(turns)-len(got))
	}
}

// TestDiscardedWatchAttentionIDsNamesTheDiscardedRows pins the READER behind the
// projection filter: the identity of a discarded watch row is recoverable from
// the transcript after a restart (#2340 review).
//
// The live path drops the resident turn, so a resident-only test cannot see this
// half. The durable resolution is the only record a resume has, which is why the
// reader is what the projection consults.
func TestDiscardedWatchAttentionIDsNamesTheDiscardedRows(t *testing.T) {
	t.Parallel()
	sess, _ := newCompactableSession(t)
	dropped := stableWatchAttentionIDPrefix + "watch:discarded:job:discarded"
	kept := stableWatchAttentionIDPrefix + "watch:kept:job:kept"
	for _, id := range []string{dropped, kept} {
		if _, err := sess.appendDelegateNotificationDurably(id, "a watch notice: "+id); err != nil {
			t.Fatalf("persist %q: %v", id, err)
		}
	}
	if err := sess.resolveAttentionDurably([]string{dropped}, delegateAttentionDiscarded); err != nil {
		t.Fatalf("discard %q durably: %v", dropped, err)
	}

	discarded := sess.discardedWatchAttentionIDs()
	if _, ok := discarded[dropped]; !ok {
		t.Fatal("a durably discarded watch row is not reported as discarded: after a restart the model reads a notice it was told to forget, and the unleased projection admits it because it is no longer pending")
	}
	// Non-vacuity: an unresolved watch row is NOT discarded, or the filter would
	// hide every watch notice instead of the discarded ones.
	if _, ok := discarded[kept]; ok {
		t.Fatalf("an unresolved watch row %q was reported discarded: the filter would hide unread notices from the model", kept)
	}
	pending, err := sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	if !slices.Contains(pending, kept) {
		t.Fatal("setup: the kept row is not pending, so this fixture cannot tell a discarded row from an unread one")
	}
}

// TestAttentionIDCoveredSeesTheInFlightRound pins that a row the CURRENT round's
// request already staged counts as read, before the call settles (#2340 review).
//
// Promotion into attentionCoveredIDs waits for the round's call to settle, but
// the model has already been shown the row. Counting it unread folded its fire
// count into a newer tick and retired it, so the next request presented a
// reminder counting a fire the model had already seen.
func TestAttentionIDCoveredSeesTheInFlightRound(t *testing.T) {
	t.Parallel()
	sess, _ := newCompactableSession(t)
	stagedID := "watchdiag:watch:tick:job:tick:staged"
	unrelated := "watchdiag:watch:tick:job:tick:other"

	sess.attentionMu.Lock()
	sess.attentionStagedIDs = map[string]struct{}{stagedID: {}}
	stagedCovered := sess.attentionIDCoveredLocked(stagedID)
	unrelatedCovered := sess.attentionIDCoveredLocked(unrelated)
	sess.attentionStagedIDs = nil
	sess.attentionMu.Unlock()

	if !stagedCovered {
		t.Fatal("a row the in-flight request staged was counted unread: its fire is folded into the next tick and the model sees a count including a fire it already read")
	}
	// Non-vacuity: clearing the staged set makes it unread again, so a failed
	// round can still fold it.
	if unrelatedCovered {
		t.Fatal("a row the round never staged was reported read: the fold would be blind to genuinely new ticks")
	}
	if sess.attentionIDCoveredLocked(stagedID) {
		t.Fatal("a staged row still counted as read after the round's staging was cleared: a failed request would hide its own tick forever")
	}
}
