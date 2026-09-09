package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestSetHumanNoteSameIDRetryAfterAdoptionKeepsNewer covers the adopted-write
// tombstone: the persist of A lands but the client never sees success (the
// owner lease releases without journaling an applied result); a fresh-ID save
// adopts A's still-pending delivery; a newer edit D lands; then the hub
// retries A with the SAME ID. The retry must replay A's recorded result
// without rewriting the store, so D stands.
func TestSetHumanNoteSameIDRetryAfterAdoptionKeepsNewer(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	// Persist of A lands at the store/journal level, but the owner goes away
	// before the applied result journals (disconnect/fence/crash): the record
	// stands InFlight with delivery pending for A.
	request, err := newClientMutationRequest(clientMutationMethodNotesHumanSet, "outer-tomb-a", struct {
		Note string
	}{Note: "note A"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	lookup, err := s.clientMutations.reservePrepared(request, nil)
	if err != nil {
		t.Fatalf("reserve A: %v", err)
	}
	if lookup.Lease == nil {
		t.Fatalf("no owner lease for the simulated first attempt")
	}
	s.notesUpdateMu.Lock()
	stored, changed := s.setHumanNote("note A")
	if err := s.persistNotesMetaWithIntent("outer-tomb-a", stored, changed); err != nil {
		s.notesUpdateMu.Unlock()
		t.Fatalf("persist A: %v", err)
	}
	if err := s.markNotesDeliveryPending("outer-tomb-a", stored); err != nil {
		s.notesUpdateMu.Unlock()
		t.Fatalf("mark delivery pending A: %v", err)
	}
	s.notesUpdateMu.Unlock()
	lookup.Lease.Release()
	// A fresh-ID save of the same text adopts A's pending delivery and spends
	// its markers, leaving the tombstone; then a newer edit D lands.
	if _, err := s.SetHumanNote("outer-tomb-b", "note A"); err != nil {
		t.Fatalf("fresh-ID adoption save: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if _, err := s.SetHumanNote("outer-tomb-d", "note D"); err != nil {
		t.Fatalf("save D: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	// The hub retries A with the same ID: it must replay A's recorded result
	// without restoring A over D.
	replayed, err := s.SetHumanNote("outer-tomb-a", "note A")
	if err != nil {
		t.Fatalf("retry A: %v", err)
	}
	if replayed != "note A" {
		t.Fatalf("retry A replayed = %q, want recorded %q", replayed, "note A")
	}
	s.mu.Lock()
	current := s.humanNote
	s.mu.Unlock()
	if current != "note D" {
		t.Fatalf("stored note after retry A = %q, want D to stand", current)
	}
	if rec := s.clientMutations.snapshot().Journal["outer-tomb-a"]; rec.OperationState != clientMutationOperationApplied {
		t.Fatalf("retry record state = %q, want applied", rec.OperationState)
	}
	// No event on the tombstone replay: the stream must stay quiet.
	select {
	case ev := <-s.Events():
		t.Fatalf("retry A emitted %s, want silence", ev.Kind)
	default:
	}
}

// TestSetHumanNoteSameIDRetryAfterRewriteKeepsNewer covers the supersede
// branch (live != note): A's delivery is still pending when a fresh-ID save
// rewrites the same text A over an intervening newer save X
// (last-writer-wins), spending A's markers. A later edit D lands, then the
// hub retries A with the same ID. The retry must replay A's recorded result
// without restoring A over D.
func TestSetHumanNoteSameIDRetryAfterRewriteKeepsNewer(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "fence-tomb-rw"}
		return nil
	}); err != nil {
		t.Fatalf("seed fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-tomb-rw-a", "note A"); err == nil {
		t.Fatalf("save A under fence err = nil, want steer refusal")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = nil
		return nil
	}); err != nil {
		t.Fatalf("clear fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-tomb-rw-x", "note X"); err != nil {
		t.Fatalf("save X: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	// Fresh-ID rewrite of the older text: last-writer-wins lands A again and
	// spends A's pending markers, leaving the tombstone.
	if _, err := s.SetHumanNote("outer-tomb-rw-c", "note A"); err != nil {
		t.Fatalf("rewrite A: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if _, err := s.SetHumanNote("outer-tomb-rw-d", "note D"); err != nil {
		t.Fatalf("save D: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	replayed, err := s.SetHumanNote("outer-tomb-rw-a", "note A")
	if err != nil {
		t.Fatalf("retry A: %v", err)
	}
	if replayed != "note A" {
		t.Fatalf("retry A replayed = %q, want recorded %q", replayed, "note A")
	}
	s.mu.Lock()
	current := s.humanNote
	s.mu.Unlock()
	if current != "note D" {
		t.Fatalf("stored note after retry A = %q, want D to stand", current)
	}
	if rec := s.clientMutations.snapshot().Journal["outer-tomb-rw-a"]; rec.OperationState != clientMutationOperationApplied {
		t.Fatalf("retry record state = %q, want applied", rec.OperationState)
	}
	select {
	case ev := <-s.Events():
		t.Fatalf("retry A emitted %s, want silence", ev.Kind)
	default:
	}
}

// TestSetHumanNoteSameIDRetryAfterSupersedePersistFailureKeepsNewer covers
// the persist-failure-after-marker-clear variant: the superseding fresh-ID
// rewrite spends A's markers and then its own metadata persist fails (so it
// rolls back to the intervening save X). A's retry must still recognize the
// landed-then-spent write via the tombstone instead of looking like an
// unfinished persist that rewrites the store over X.
func TestSetHumanNoteSameIDRetryAfterSupersedePersistFailureKeepsNewer(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "fence-tomb-pf"}
		return nil
	}); err != nil {
		t.Fatalf("seed fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-tomb-pf-a", "note A"); err == nil {
		t.Fatalf("save A under fence err = nil, want steer refusal")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = nil
		return nil
	}); err != nil {
		t.Fatalf("clear fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-tomb-pf-x", "note X"); err != nil {
		t.Fatalf("save X: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	injected := errors.New("injected persist failure after marker clear")
	s.cfg.testOnly.notesAutoSaveFault = func() error { return injected }
	if _, err := s.SetHumanNote("outer-tomb-pf-c", "note A"); !errors.Is(err, injected) {
		t.Fatalf("rewrite A err = %v, want injected failure", err)
	}
	s.cfg.testOnly.notesAutoSaveFault = nil
	// The failed rewrite emits nothing and rolls back to X.
	replayed, err := s.SetHumanNote("outer-tomb-pf-a", "note A")
	if err != nil {
		t.Fatalf("retry A: %v", err)
	}
	if replayed != "note A" {
		t.Fatalf("retry A replayed = %q, want recorded %q", replayed, "note A")
	}
	s.mu.Lock()
	current := s.humanNote
	s.mu.Unlock()
	if current != "note X" {
		t.Fatalf("stored note after retry A = %q, want X to stand", current)
	}
	if rec := s.clientMutations.snapshot().Journal["outer-tomb-pf-a"]; rec.OperationState != clientMutationOperationApplied {
		t.Fatalf("retry record state = %q, want applied", rec.OperationState)
	}
	select {
	case ev := <-s.Events():
		t.Fatalf("retry A emitted %s, want silence", ev.Kind)
	default:
	}
}
