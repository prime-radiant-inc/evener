package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestSetHumanNoteFreshIDResaveDeliversPendingSteer verifies L1: after a save
// whose derived steer is refused (interrupt fence), storage holds the note
// with delivery pending, and a re-save under a FRESH outer ID with the same
// text completes that pending delivery instead of converging on a silent
// no-op success. The agent is woken exactly once, and a further same-text
// save steers nothing more.
func TestSetHumanNoteFreshIDResaveDeliversPendingSteer(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "fence-l1"}
		return nil
	}); err != nil {
		t.Fatalf("seed fence: %v", err)
	}
	stored, err := s.SetHumanNote("outer-l1-first", "wake me")
	if err == nil {
		t.Fatalf("save under fence err = nil, want steer refusal")
	}
	if stored != "wake me" {
		t.Fatalf("stored = %q, want %q", stored, "wake me")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = nil
		return nil
	}); err != nil {
		t.Fatalf("clear fence: %v", err)
	}
	// The hub mints a fresh clientMutationId per save: the re-save must still
	// deliver the pending steer for the stored value.
	replayed, err := s.SetHumanNote("outer-l1-retry", "wake me")
	if err != nil {
		t.Fatalf("fresh-ID re-save: %v", err)
	}
	if replayed != "wake me" {
		t.Fatalf("re-save stored = %q, want %q", replayed, "wake me")
	}
	s.mu.Lock()
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want 1 (re-save delivers the pending steer)", n)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	// A further same-text save is a true no-op: no second steer, no emission.
	if _, err := s.SetHumanNote("outer-l1-third", "wake me"); err != nil {
		t.Fatalf("third save: %v", err)
	}
	s.mu.Lock()
	n = len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want still 1 (no duplicate steer)", n)
	}
	select {
	case ev := <-s.Events():
		t.Fatalf("third save emitted %s, want silence", ev.Kind)
	default:
	}
}

// TestSetHumanNoteFreshIDResaveAfterJournalFaultDelivers verifies L1's second
// half: a journal fault between the atomic metadata write and the
// delivery-pending mark leaves only the metadata-committed intent, and a
// fresh-ID same-text re-save still completes delivery for that value.
func TestSetHumanNoteFreshIDResaveAfterJournalFaultDelivers(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	injected := errors.New("injected journal fault between meta save and delivery-pending mark")
	s.cfg.testOnly.notesJournalFault = func() error { return injected }
	stored, err := s.SetHumanNote("outer-l1j-first", "faulted note")
	if !errors.Is(err, injected) {
		t.Fatalf("save err = %v, want injected journal fault", err)
	}
	if stored != "faulted note" {
		t.Fatalf("stored = %q, want %q", stored, "faulted note")
	}
	s.cfg.testOnly.notesJournalFault = nil
	replayed, err := s.SetHumanNote("outer-l1j-retry", "faulted note")
	if err != nil {
		t.Fatalf("fresh-ID re-save: %v", err)
	}
	if replayed != "faulted note" {
		t.Fatalf("re-save stored = %q, want %q", replayed, "faulted note")
	}
	s.mu.Lock()
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want 1 (re-save delivers the pending steer)", n)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
}

// TestSetHumanNoteFreshIDDifferentValueKeepsNewer verifies the adoption
// boundary: a fresh-ID save carrying text that MATCHES an older still-pending
// delivery but DIFFERS from the live store is a fresh write intending to
// change the note back (last-writer-wins). It must land in the store, return
// the new value, and steer for it — never adopt (skip the store write and
// journal) the older value while leaving the store unchanged. Otherwise the
// RPC would return the older value while the store holds the newer one, and
// the hub would commit the response note over the divergent store.
func TestSetHumanNoteFreshIDDifferentValueKeepsNewer(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "fence-l1b"}
		return nil
	}); err != nil {
		t.Fatalf("seed fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-l1n-a", "note A"); err == nil {
		t.Fatalf("save A under fence err = nil, want steer refusal")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = nil
		return nil
	}); err != nil {
		t.Fatalf("clear fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-l1n-b", "note B"); err != nil {
		t.Fatalf("save B: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	// A fresh-ID save of the older text is a new write intending to change
	// the note back to A: it must land in the store, return A, and steer for
	// A, clearing the stale pending marker for the first A attempt.
	rewritten, err := s.SetHumanNote("outer-l1n-c", "note A")
	if err != nil {
		t.Fatalf("re-save A text: %v", err)
	}
	if rewritten != "note A" {
		t.Fatalf("re-save returned = %q, want %q", rewritten, "note A")
	}
	s.mu.Lock()
	current := s.humanNote
	n := len(s.steeringQueue)
	var lastText string
	if n > 0 {
		lastText = s.steeringQueue[n-1].Text
	}
	s.mu.Unlock()
	if current != "note A" {
		t.Fatalf("stored note after re-save = %q, want A to land (last-writer-wins)", current)
	}
	if n != 2 {
		t.Fatalf("steering queue length = %d, want 2 (re-save steers for the new value)", n)
	}
	if lastText != "human updated their whiteboard: note A" {
		t.Fatalf("last steer text = %q, want the re-saved value's steer", lastText)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
}
