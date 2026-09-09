package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestSetHumanNoteJournalFaultRetryDeliversWithoutRewrite verifies K1: a
// failure injected BETWEEN the atomic metadata write (note + pending intent)
// and the delivery-pending journal mark still recovers delivery from the
// metadata-committed intent. The retry with the same outer ID delivers the
// RECORDED note without rewriting the store: the post-clamp text is returned,
// exactly one steer lands, and the emission fires.
func TestSetHumanNoteJournalFaultRetryDeliversWithoutRewrite(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	injected := errors.New("injected journal fault between meta save and delivery-pending mark")
	s.cfg.testOnly.notesJournalFault = func() error { return injected }
	stored, err := s.SetHumanNote("outer-k1a", "note k1")
	if !errors.Is(err, injected) {
		t.Fatalf("save err = %v, want injected journal fault", err)
	}
	if stored != "note k1" {
		t.Fatalf("stored = %q, want %q", stored, "note k1")
	}
	// The atomic write landed: the note stands in memory and the pending
	// intent is committed alongside it — unlike a metadata-save failure,
	// there is nothing to roll back.
	s.mu.Lock()
	current := s.humanNote
	_, _, intentOK := s.pendingNotesHumanIntentLocked("outer-k1a")
	s.mu.Unlock()
	if current != "note k1" {
		t.Fatalf("stored note after journal fault = %q, want %q (atomic write must stand)", current, "note k1")
	}
	if !intentOK {
		t.Fatal("pending intent missing after journal fault (atomic write must commit it)")
	}
	if rec := s.clientMutations.snapshot().Journal["outer-k1a"]; rec.NotesDeliveryPending {
		t.Fatal("delivery-pending journaled despite injected fault before the mark")
	}
	s.cfg.testOnly.notesJournalFault = nil
	replayed, err := s.SetHumanNote("outer-k1a", "note k1")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if replayed != "note k1" {
		t.Fatalf("retry stored = %q, want %q", replayed, "note k1")
	}
	s.mu.Lock()
	current = s.humanNote
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if current != "note k1" {
		t.Fatalf("stored note after retry = %q, want %q", current, "note k1")
	}
	if n != 1 {
		t.Fatalf("steering queue length = %d, want 1 (retry delivers once)", n)
	}
	if _, _, ok := s.pendingNotesHumanIntent("outer-k1a"); ok {
		t.Fatal("pending intent not cleared after applied result journaled")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
}

// TestSetHumanNoteJournalFaultInterveningSaveKeepsNewer verifies K1's second
// half: after the between-writes failure, an intervening save under a
// different outer ID lands, and the first mutation's retry completes delivery
// for ITS recorded value without rewriting (and clobbering) the newer note.
// The retry returns its recorded value; the store keeps the newer note.
func TestSetHumanNoteJournalFaultInterveningSaveKeepsNewer(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	injected := errors.New("injected journal fault between meta save and delivery-pending mark")
	s.cfg.testOnly.notesJournalFault = func() error { return injected }
	if _, err := s.SetHumanNote("outer-k1b", "note A"); !errors.Is(err, injected) {
		t.Fatalf("save A err = %v, want injected journal fault", err)
	}
	s.cfg.testOnly.notesJournalFault = nil
	if _, err := s.SetHumanNote("outer-k1c", "note B"); err != nil {
		t.Fatalf("save B: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	replayed, err := s.SetHumanNote("outer-k1b", "note A")
	if err != nil {
		t.Fatalf("retry A: %v", err)
	}
	if replayed != "note A" {
		t.Fatalf("retry A replayed = %q, want recorded %q", replayed, "note A")
	}
	s.mu.Lock()
	current := s.humanNote
	s.mu.Unlock()
	if current != "note B" {
		t.Fatalf("stored note after retry A = %q, want B to stand", current)
	}
}

// pendingNotesHumanIntentLocked reads the committed intent; the caller holds
// s.mu. It exists so the journal-fault test can assert the atomic write
// staged the intent without re-acquiring the lock.
func (s *Session) pendingNotesHumanIntentLocked(outerID string) (string, bool, bool) {
	pending, ok := s.pendingNotesHuman[outerID]
	if !ok {
		return "", false, false
	}
	return pending.Note, pending.Changed, true
}
