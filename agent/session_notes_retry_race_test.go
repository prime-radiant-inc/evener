package agent

import (
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestSetHumanNoteSameIDRetryAfterFreshIDResaveSteersOnce verifies the
// retry/last-writer-wins serialization: after a fence-failed save A (delivery
// pending) and a successful save B, a fresh-ID save back to A delivers A and
// clears A's pending markers. A same-ID retry of A that observed the pending
// markers BEFORE the fresh save took the update lock must journal success
// WITHOUT steering when it resumes after — the agent wakes exactly once for
// the text. The test drives resumeNotesHumanSetDelivery directly with the
// retry's generation-2 lease to deterministically reproduce the interleaving
// (pre-lock check saw pending; lock acquired after the fresh save cleared
// it), which goroutine scheduling cannot reproduce without flakiness.
func TestSetHumanNoteSameIDRetryAfterFreshIDResaveSteersOnce(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	// Fence-failed save A: storage holds A with delivery pending, no steer.
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "fence-race"}
		return nil
	}); err != nil {
		t.Fatalf("seed fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-race-a", "note A"); err == nil {
		t.Fatalf("save A under fence err = nil, want steer refusal")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = nil
		return nil
	}); err != nil {
		t.Fatalf("clear fence: %v", err)
	}
	// Successful save B under a fresh ID: store moves to B, steers once.
	if _, err := s.SetHumanNote("outer-race-b", "note B"); err != nil {
		t.Fatalf("save B: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	// The hub retry of A takes the record over (generation 2) and observes
	// the still-pending delivery BEFORE the fresh-ID re-save runs — the
	// pre-lock check in completeNotesHumanSet. The lease stays open while
	// the re-save below takes the update lock first.
	retryRequest, err := newClientMutationRequest(clientMutationMethodNotesHumanSet, "outer-race-a", struct {
		Note string
	}{Note: "note A"})
	if err != nil {
		t.Fatalf("retry request: %v", err)
	}
	retryLookup, err := s.clientMutations.reservePrepared(retryRequest, nil)
	if err != nil {
		t.Fatalf("retry reserve: %v", err)
	}
	if retryLookup.Lease == nil {
		t.Fatalf("no owner lease for the simulated retry")
	}
	stored, _, ok := s.pendingNotesHumanIntent("outer-race-a")
	if !ok {
		var journalOK bool
		stored, journalOK = s.notesDeliveryPending("outer-race-a")
		if !journalOK {
			t.Fatalf("retry observes no pending delivery for A, want the committed intent or journal marker")
		}
	}
	// Fresh-ID save back to A: last-writer-wins delivers A (second steer
	// overall, first for this text) and clears A's pending markers.
	rewritten, err := s.SetHumanNote("outer-race-c", "note A")
	if err != nil {
		t.Fatalf("re-save A: %v", err)
	}
	if rewritten != "note A" {
		t.Fatalf("re-save returned = %q, want %q", rewritten, "note A")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	// The waiting retry now resumes under its own lock acquisition, after the
	// markers are gone: it must journal success without steering or emitting.
	got, err := s.resumeNotesHumanSetDelivery(retryLookup.Lease, "outer-race-a", stored)
	if err != nil {
		t.Fatalf("retry resume: %v", err)
	}
	if got != "note A" {
		t.Fatalf("retry resume returned = %q, want %q", got, "note A")
	}
	s.mu.Lock()
	current := s.humanNote
	n := len(s.steeringQueue)
	steersForA := 0
	for _, entry := range s.steeringQueue {
		if entry.Text == "human updated their whiteboard: note A" {
			steersForA++
		}
	}
	s.mu.Unlock()
	if current != "note A" {
		t.Fatalf("stored note = %q, want %q", current, "note A")
	}
	if steersForA != 1 {
		t.Fatalf("steers for the re-saved text = %d, want exactly 1 (retry must not deliver twice)", steersForA)
	}
	if n != 2 {
		t.Fatalf("steering queue length = %d, want 2 (B plus one A)", n)
	}
	if rec := s.clientMutations.snapshot().Journal["outer-race-a"]; rec.OperationState != clientMutationOperationApplied {
		t.Fatalf("retry record state = %q, want applied (success journaled without steering)", rec.OperationState)
	}
	select {
	case ev := <-s.Events():
		t.Fatalf("retry resume emitted %s, want silence (fresh save already delivered)", ev.Kind)
	default:
	}
}
