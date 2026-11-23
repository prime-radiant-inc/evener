package delegatestore

import (
	"reflect"
	"testing"
)

func attentionHeldEvent(delegateID, attentionID string, generation uint64) Event {
	return Event{
		Kind:       EventDelegateAttentionHeld,
		DelegateID: delegateID,
		AttentionHeld: &AttentionHeld{
			AttentionID: attentionID,
			Generation:  generation,
		},
	}
}

// TestAttentionHeldLowersProjectionAndKeepsRow pins the stop quiet: a held row
// stays durably queued with its stamp, but it raises no NeedsAttention and is
// not runnable work.
func TestAttentionHeldLowersProjectionAndKeepsRow(t *testing.T) {
	t.Parallel()
	state := attentionTestState(t,
		createdEvent("dlg_hold", ""),
		attentionQueuedEvent("dlg_hold", "att_one", true),
		attentionHeldEvent("dlg_hold", "att_one", 3),
	)
	aggregate := state["dlg_hold"]
	if got := attentionRows(aggregate); !reflect.DeepEqual(got, []string{"att_one"}) {
		t.Fatalf("held row left the queue: %v", got)
	}
	if aggregate.PendingAttention[0].HeldThrough != 3 {
		t.Fatalf("HeldThrough = %d, want 3", aggregate.PendingAttention[0].HeldThrough)
	}
	if aggregate.NeedsAttention {
		t.Fatal("held row raised NeedsAttention")
	}
	// A second hold at an older generation never lowers the newer stamp.
	state = attentionTestState(t,
		createdEvent("dlg_hold", ""),
		attentionQueuedEvent("dlg_hold", "att_one", true),
		attentionHeldEvent("dlg_hold", "att_one", 3),
		attentionHeldEvent("dlg_hold", "att_one", 2),
	)
	if got := state["dlg_hold"].PendingAttention[0].HeldThrough; got != 3 {
		t.Fatalf("HeldThrough after older hold = %d, want 3", got)
	}
}

// TestAttentionHeldReleasesOnFreshWake pins that a committed wake is newer
// evidence than any stop: it clears every hold on the delegate and the row
// becomes runnable again.
func TestAttentionHeldReleasesOnFreshWake(t *testing.T) {
	t.Parallel()
	state := attentionTestState(t,
		createdEvent("dlg_hold", ""),
		attentionQueuedEvent("dlg_hold", "att_one", true),
		attentionQueuedEvent("dlg_hold", "att_two", true),
		attentionHeldEvent("dlg_hold", "att_one", 2),
		attentionHeldEvent("dlg_hold", "att_two", 2),
		attentionQueuedEvent("dlg_hold", "att_three", true),
	)
	aggregate := state["dlg_hold"]
	if !aggregate.NeedsAttention {
		t.Fatal("fresh committed wake did not restore NeedsAttention")
	}
	for _, row := range aggregate.PendingAttention {
		if row.HeldThrough != 0 {
			t.Fatalf("row %s still held through %d after fresh wake", row.AttentionID, row.HeldThrough)
		}
	}
}

// TestAttentionHeldReleasesThroughGeneration pins the release form: it ends
// every hold at or below the named generation and leaves a newer hold standing,
// while advancing the legacy release floor.
func TestAttentionHeldReleasesThroughGeneration(t *testing.T) {
	t.Parallel()
	state := attentionTestState(t,
		createdEvent("dlg_hold", ""),
		attentionQueuedEvent("dlg_hold", "att_old", true),
		attentionQueuedEvent("dlg_hold", "att_new", true),
		attentionHeldEvent("dlg_hold", "att_old", 1),
		attentionHeldEvent("dlg_hold", "att_new", 4),
		attentionHeldEvent("dlg_hold", "", 2),
	)
	aggregate := state["dlg_hold"]
	if got := aggregate.PendingAttention[0].HeldThrough; got != 0 {
		t.Fatalf("att_old HeldThrough = %d, want released", got)
	}
	if got := aggregate.PendingAttention[1].HeldThrough; got != 4 {
		t.Fatalf("att_new HeldThrough = %d, want 4 (newer than the release)", got)
	}
	if aggregate.AttentionHoldReleasedGeneration != 2 {
		t.Fatalf("release floor = %d, want 2", aggregate.AttentionHoldReleasedGeneration)
	}
	if !aggregate.NeedsAttention {
		t.Fatal("released row did not raise NeedsAttention in the same Apply")
	}
}

// TestAttentionHeldDoesNotResurrectResolvedRow pins that a hold racing a
// resolution is a no-op: it never re-adds a row the queue no longer holds.
func TestAttentionHeldDoesNotResurrectResolvedRow(t *testing.T) {
	t.Parallel()
	state := attentionTestState(t,
		createdEvent("dlg_hold", ""),
		attentionQueuedEvent("dlg_hold", "att_one", true),
		attentionResolvedEvent("dlg_hold", "att_one"),
		attentionHeldEvent("dlg_hold", "att_one", 2),
	)
	aggregate := state["dlg_hold"]
	if len(aggregate.PendingAttention) != 0 || aggregate.NeedsAttention {
		t.Fatalf("hold resurrected a resolved row: %v needs=%t", attentionRows(aggregate), aggregate.NeedsAttention)
	}
}

// TestAttentionHeldRejectsZeroGeneration pins the fold's validation: a hold
// names a settled generation, never zero.
func TestAttentionHeldRejectsZeroGeneration(t *testing.T) {
	t.Parallel()
	events := []Event{
		createdEvent("dlg_hold", ""),
		attentionHeldEvent("dlg_hold", "att_one", 0),
	}
	for i := range events {
		events[i].Seq = uint64(i + 1)
	}
	if _, err := Fold(events); err == nil {
		t.Fatal("Fold accepted a hold with generation zero")
	}
}
