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

// TestAdoptedSpendFailureRetriesFinishSpend verifies the idempotent
// accepted-steer/intent-spend transition: when the durable spend fails after
// the adopter's steer accepted, the adopter's record links the adopted intent
// to the accepted steer. A same-ID retry of the adopter finishes the marker
// clear and the spend (no second steer) and journals its applied result,
// instead of re-accepting the steer or losing the delivery.
func TestAdoptedSpendFailureRetriesFinishSpend(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	injected := errors.New("injected journal fault between meta save and delivery-pending mark")
	s.cfg.testOnly.notesJournalFault = func() error { return injected }
	if _, err := s.SetHumanNote("outer-adopt-link-a", "adopted value"); !errors.Is(err, injected) {
		t.Fatalf("save A err = %v, want injected journal fault", err)
	}
	s.cfg.testOnly.notesJournalFault = nil
	// The adopter faults on the durable spend AFTER its steer accepted: fail
	// every metadata save from here until the link is journaled, then let the
	// spend itself fail once.
	autosave := errors.New("injected autosave fault on adopted spend")
	s.cfg.testOnly.notesAutoSaveFault = func() error { return autosave }
	if _, err := s.SetHumanNote("outer-adopt-link-b", "adopted value"); !errors.Is(err, autosave) {
		t.Fatalf("adopting save err = %v, want injected autosave fault", err)
	}
	s.cfg.testOnly.notesAutoSaveFault = nil
	// The steer landed exactly once, and the adopted intent still stands for
	// the retry to finish spending.
	s.mu.Lock()
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want 1 (spend failure must not duplicate the steer)", n)
	}
	if _, _, ok := s.pendingNotesHumanIntent("outer-adopt-link-a"); !ok {
		t.Fatal("adopted intent missing after spend failure, want it pending for the retry")
	}
	// Same-ID retry of the adopter finishes the spend without re-steering and
	// journals its applied result.
	if _, err := s.SetHumanNote("outer-adopt-link-b", "adopted value"); err != nil {
		t.Fatalf("retry adopter: %v", err)
	}
	s.mu.Lock()
	n = len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want still 1 (retry finishes the spend, not the steer)", n)
	}
	if _, _, ok := s.pendingNotesHumanIntent("outer-adopt-link-a"); ok {
		t.Fatal("adopted intent still pending after retry, want it spent")
	}
	if rec := s.clientMutations.snapshot().Journal["outer-adopt-link-b"]; rec.OperationState != clientMutationOperationApplied {
		t.Fatalf("adopter record state = %q, want applied", rec.OperationState)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
}

// TestAdoptedIntentSurvivesRefusedSteer verifies the retry-loss half of the
// adopted-delivery path: when the adopter's steer is refused (interrupt
// fence), the adopted intent is NOT consumed — nothing was delivered, so the
// intent must stand for the retry. A same-ID retry after the fence lifts
// resumes the recorded delivery and steers exactly once, instead of
// converging on a silent no-op for a note nobody was told about.
func TestAdoptedIntentSurvivesRefusedSteer(t *testing.T) {
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
	if _, err := s.SetHumanNote("outer-adopt-refuse-a", "adopted value"); !errors.Is(err, injected) {
		t.Fatalf("save A err = %v, want injected journal fault", err)
	}
	s.cfg.testOnly.notesJournalFault = nil
	// The adopter's steer is refused: the fence makes acceptNotesSteer fail,
	// so the adoption must leave A's intent (and the delivery) pending.
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "fence-adopt-refuse"}
		return nil
	}); err != nil {
		t.Fatalf("seed fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-adopt-refuse-b", "adopted value"); err == nil {
		t.Fatalf("adopting save under fence err = nil, want steer refusal")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if _, _, ok := s.pendingNotesHumanIntent("outer-adopt-refuse-a"); !ok {
		t.Fatal("adopted intent consumed despite refused steer, want it pending for the retry")
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = nil
		return nil
	}); err != nil {
		t.Fatalf("clear fence: %v", err)
	}
	// Same-ID retry of the adopter resumes the recorded delivery (not a
	// silent no-op): the store already holds the value, the steer lands now.
	if _, err := s.SetHumanNote("outer-adopt-refuse-b", "adopted value"); err != nil {
		t.Fatalf("retry adopter: %v", err)
	}
	s.mu.Lock()
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want 1 (refused adoption must deliver on retry)", n)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
}
