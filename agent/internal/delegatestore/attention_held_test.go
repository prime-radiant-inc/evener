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

// TestAttentionResolvedSetStaysDisjointFromTheQueue pins the folded memory a
// cold restore needs: a resolution records its id even after the row is gone,
// and a later queue of the same id takes it back out, so the two sets can never
// both name one attention row.
func TestAttentionResolvedSetStaysDisjointFromTheQueue(t *testing.T) {
	t.Parallel()
	state := attentionTestState(t,
		createdEvent("dlg_hold", ""),
		attentionQueuedEvent("dlg_hold", "att_one", true),
		attentionResolvedEvent("dlg_hold", "att_one"),
	)
	aggregate := state["dlg_hold"]
	if _, ok := aggregate.ResolvedAttention["att_one"]; !ok {
		t.Fatalf("resolved set = %v, want att_one recorded", aggregate.ResolvedAttention)
	}
	if len(aggregate.PendingAttention) != 0 {
		t.Fatalf("resolved row stayed queued: %v", attentionRows(aggregate))
	}
	// A requeue of the same id (a redelivery) leaves the resolved set.
	state = attentionTestState(t,
		createdEvent("dlg_hold", ""),
		attentionQueuedEvent("dlg_hold", "att_one", true),
		attentionResolvedEvent("dlg_hold", "att_one"),
		attentionQueuedEvent("dlg_hold", "att_one", true),
	)
	aggregate = state["dlg_hold"]
	if _, ok := aggregate.ResolvedAttention["att_one"]; ok {
		t.Fatalf("requeued id stayed resolved: %v", aggregate.ResolvedAttention)
	}
	if got := attentionRows(aggregate); !reflect.DeepEqual(got, []string{"att_one"}) {
		t.Fatalf("requeued row = %v, want att_one pending", got)
	}
}
