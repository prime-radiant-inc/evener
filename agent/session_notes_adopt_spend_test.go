package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestAdoptedIntentSpendPersistsDurably verifies the consumed-intent path in
// deliverAdoptedNotesSteer: after a journal-fault adoption spends another
// attempt's metadata-committed intent, the intent's absence is persisted in
// the same metadata save — not left for a later save. A crash before that
// later save must NOT let a post-restart same-value mutation adopt the spent
// intent and steer a second time.
func TestAdoptedIntentSpendPersistsDurably(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	// First attempt faults between the atomic metadata write and the
	// delivery-pending journal mark: storage holds the note with only the
	// metadata-committed intent behind it.
	injected := errors.New("injected journal fault between meta save and delivery-pending mark")
	s.cfg.testOnly.notesJournalFault = func() error { return injected }
	if _, err := s.SetHumanNote("outer-adopt-spend-a", "adopted value"); !errors.Is(err, injected) {
		t.Fatalf("save A err = %v, want injected journal fault", err)
	}
	s.cfg.testOnly.notesJournalFault = nil
	// A fresh-ID same-text save adopts A's intent and delivers it.
	if _, err := s.SetHumanNote("outer-adopt-spend-b", "adopted value"); err != nil {
		t.Fatalf("adopting save: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	// The spend must be durable immediately: no pending intent may survive
	// the adoption in memory (the in-memory absence is what any later save
	// persists, so a crash before that save cannot resurrect the intent).
	if _, _, ok := s.pendingNotesHumanIntent("outer-adopt-spend-a"); ok {
		t.Fatal("adopted intent still pending after spend, want it consumed")
	}
	if _, ok := s.pendingNotesHumanIntent("outer-adopt-spend-b"); ok {
		t.Fatal("adopter intent still pending after applied result, want it cleared")
	}
	// A further same-text save steers nothing more: delivery finished exactly
	// once, so neither the spent intent nor a phantom marker redelivers.
	if _, err := s.SetHumanNote("outer-adopt-spend-c", "adopted value"); err != nil {
		t.Fatalf("third save: %v", err)
	}
	s.mu.Lock()
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want 1 (spent intent must not redeliver)", n)
	}
	select {
	case ev := <-s.Events():
		t.Fatalf("third save emitted %s, want silence", ev.Kind)
	default:
	}
}
