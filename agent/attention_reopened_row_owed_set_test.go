package agent

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestAFailedLaunchReopenStaysInTheSuccessorsOwedSet pins the other reopen a
// generation cannot classify on its own.
//
// A committed attention start that then fails to launch reopens its trigger
// through reopenFailedDelegateAttention, which passes quietAt zero: nothing
// quieted that row, so there is no hold to wait for. The row still names a
// generation that began — RunStarted with TriggerAttention — so the capture's
// generation clauses see exactly what a user stop's reopen looks like: started,
// attention-triggered, and not reached by any release. They drop it, and the
// successor's commit clears the wake set while the report is still pending.
//
// The hold is the fact that separates the two. A stop installs its quiet before
// the append that publishes the reopen, so a reopen with no hold was quieted by
// nothing and is eligible now. The second direction is the stop's own reopen:
// it carries a hold, and it must stay out of the owed set until a release
// supersedes it.
func TestAFailedLaunchReopenStaysInTheSuccessorsOwedSet(t *testing.T) {
	t.Parallel()
	const (
		sessionID = "child-dlg_target"
		freshID   = "delegate:delivery-attention-fresh"
	)
	const failedGeneration = 1
	reopenedID := delegateAttentionReopenID("delegate:delivery-attention-1", failedGeneration)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, freshID)
	// Generation 1 was an attention-triggered start: it committed and its
	// RunStarted landed, so "did this generation begin" is true for the row it
	// reopened — the question the capture cannot answer on its own.
	// A committed start that fails to launch settles OutcomeFailed, not
	// cancelled: nothing quieted the row it reopened, and the newest-attention-
	// stop index must not record it as a stop.
	failed := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"launch_failed"`)}
	c.mu.Lock()
	_, err := c.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: "dlg_target",
			RunStarted: &delegatestore.RunStarted{Generation: failedGeneration, Trigger: delegatestore.TriggerAttention, StartedAt: time.Unix(10, 0).UTC()},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_target",
			RunFinished: &delegatestore.RunFinished{
				Generation:  failedGeneration,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeFailed, Reason: "launch_failed", EndedAt: time.Unix(20, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  delegateDeliveryID("dlg_target", failedGeneration),
				Packet:      &failed,
			},
		},
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("journal the attention start that never launched: %v", err)
	}
	if _, err := runtime.appendDelegateNotificationDurably(reopenedID, "the report a failed launch reopened"); err != nil {
		t.Fatalf("append the reopened row: %v", err)
	}
	// Premise: the generation began under an attention trigger, no release
	// reached it, and no hold quiets the row. That is precisely the state the
	// old capture read as a stop's own quiet.
	if !runtime.delegateAttentionGenerationStartedLocked("dlg_target", failedGeneration) {
		t.Fatalf("premise broken: generation %d never began, so this is not the failed-launch case", failedGeneration)
	}
	if trigger := runtime.delegateAttentionGenerationTriggerLocked("dlg_target", failedGeneration); trigger != delegatestore.TriggerAttention {
		t.Fatalf("premise broken: generation %d began with trigger %v, want attention", failedGeneration, trigger)
	}
	if floor := c.attentionReleaseFloor("dlg_target"); floor >= failedGeneration {
		t.Fatalf("premise broken: the release floor is %d, so a release already made the row eligible", floor)
	}
	runtime.attentionMu.Lock()
	_, quieted := runtime.attentionAutomaticDriveHolds[reopenedID]
	runtime.attentionMu.Unlock()
	if quieted {
		t.Fatalf("premise broken: the reopened row carries a hold, so this is a stop's own reopen and not a failed launch's")
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
		t.Fatalf("the successor's owed set is %v and the failed launch's reopen %q is not in it: its generation began under an attention trigger but nothing quieted it, so no release is owed and the commit clears the wake set while the report is still pending.\nfloor=%d reopened=%q", owed, reopenedID, c.attentionReleaseFloor("dlg_target"), reopenedID)
	}
}

// TestAStoppedReopenStaysOutOfTheSuccessorsOwedSet is the other direction: a
// stop's own reopen carries its quiet as a hold, and that hold keeps the row
// out of the successor's owed set until a release supersedes it.
func TestAStoppedReopenStaysOutOfTheSuccessorsOwedSet(t *testing.T) {
	t.Parallel()
	const (
		sessionID = "child-dlg_target"
		freshID   = "delegate:delivery-attention-fresh"
	)
	const stoppedGeneration = 1
	reopenedID := delegateAttentionReopenID("delegate:delivery-attention-1", stoppedGeneration)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, freshID)
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"stopped"`)}
	c.mu.Lock()
	_, err := c.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: "dlg_target",
			RunStarted: &delegatestore.RunStarted{Generation: stoppedGeneration, Trigger: delegatestore.TriggerAttention, StartedAt: time.Unix(10, 0).UTC()},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_target",
			RunFinished: &delegatestore.RunFinished{
				Generation:  stoppedGeneration,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCancelled, Reason: "stopped", EndedAt: time.Unix(20, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  delegateDeliveryID("dlg_target", stoppedGeneration),
				Packet:      &stopped,
			},
		},
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("journal the stopped generation: %v", err)
	}
	if _, err := runtime.appendDelegateNotificationDurably(reopenedID, "the report the stop reopened"); err != nil {
		t.Fatalf("append the reopened row: %v", err)
	}
	// The stop installs its quiet before publishing the reopen; the setter takes
	// attentionMu itself.
	runtime.holdDelegateAttentionFromAutomaticDriveAt(reopenedID, stoppedGeneration)
	if floor := c.attentionReleaseFloor("dlg_target"); floor >= stoppedGeneration {
		t.Fatalf("premise broken: the release floor is %d, so the stop's quiet is already superseded", floor)
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
		if id == reopenedID {
			t.Fatalf("the successor's owed set is %v and it carries %q: the user stop's quiet still stands, so the successor must not present the report the stop exists to withhold", owed, reopenedID)
		}
	}
}

// TestAReleasedReopenedRowSurvivesTheSuccessorStart pins the rule that a row a
// release made presentable stays in the successor's owed set.
//
// The sequence the finding describes: a user stop settles generation 1 and
// reopens its trigger under /reopened/1, so that row carries no wake ID of its
// own; a newer event then releases it — the durable floor reaches generation 1
// — which makes it eligible again. The next automatic generation starts from a
// different, fresh row.
//
// The suspect is the capture in acceptDelegateAttention: it keeps the trigger
// plus any pending row the controller has already admitted as a wake or a
// rider. A released reopen has neither — it was minted by the stop and never
// armed — so the filter drops it from the successor's owed set. That set is the
// generation's attentionPendingIDs, which two things read: the committed batch
// computes attentionNeeded from it (delegate_tree_start.go:803) and the commit
// replaces the controller's wake state with it (:858). With the row dropped,
// the batch appends AttentionChanged{NeedsAttention:false} and then throws the
// controller's wake state away, so a stop landing before presentation has
// nothing to rearm and the released report waits for an unrelated event.
func TestAReleasedReopenedRowSurvivesTheSuccessorStart(t *testing.T) {
	const (
		sessionID = "child-dlg_target"
		freshID   = "delegate:delivery-attention-fresh"
	)
	reopenedID := delegateAttentionReopenID("delegate:delivery-attention-1", 1)
	c, runtime, _ := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, freshID)
	// Generation 1 was a deliberate user stop: attention-triggered and settled
	// cancelled. Only a settled generation can be named by a release.
	c.mu.Lock()
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"stopped"`)}
	_, err := c.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: "dlg_target",
			RunStarted: &delegatestore.RunStarted{Generation: 1, Trigger: delegatestore.TriggerAttention, StartedAt: time.Unix(10, 0).UTC()},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_target",
			RunFinished: &delegatestore.RunFinished{
				Generation:  1,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCancelled, Reason: "stopped", EndedAt: time.Unix(20, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  delegateDeliveryID("dlg_target", 1),
				Packet:      &stopped,
			},
		},
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("settle generation 1 as an attention stop: %v", err)
	}
	// The stop reopens its report. The reopen is minted, never armed, and then
	// a newer event releases it: the durable floor reaches generation 1.
	if _, err := runtime.appendDelegateNotificationDurably(reopenedID, "the report the stop reopened"); err != nil {
		t.Fatalf("append the reopened row: %v", err)
	}
	c.mu.Lock()
	_, err = c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionHoldReleased,
		DelegateID: "dlg_target",
		AttentionHoldReleased: &delegatestore.AttentionHoldReleased{
			Generation: 1,
		},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("release the reopened row's quiet: %v", err)
	}
	if floor := c.attentionReleaseFloor("dlg_target"); floor != 1 {
		t.Fatalf("premise broken: the release floor is %d, want 1", floor)
	}
	// The reopened row must be neither an admitted wake nor a rider: that is
	// what makes the wake set alone unable to say it is eligible.
	c.mu.Lock()
	state := c.attention["dlg_target"]
	_, isWake := state.wakeIDs[reopenedID]
	_, isRider := state.riderIDs[reopenedID]
	delete(state.wakeIDs, reopenedID)
	delete(state.riderIDs, reopenedID)
	c.mu.Unlock()
	if isRider {
		t.Fatalf("premise broken: the reopened row %q is already a rider", reopenedID)
	}
	_ = isWake

	pending, err := runtime.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("pending attention: %v", err)
	}
	reopenedPending := false
	for _, id := range pending {
		if id == reopenedID {
			reopenedPending = true
		}
	}
	if !reopenedPending {
		t.Fatalf("premise broken: the reopened row is not pending (%v)", pending)
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
		t.Fatalf("the successor's owed set is %v and the released row %q is not in it: a pending row that no wake has admitted is dropped from the generation that must present it, so a stop landing before presentation has nothing to rearm.\nrelease floor=1 reopened=%q", owed, reopenedID, reopenedID)
	}
}
