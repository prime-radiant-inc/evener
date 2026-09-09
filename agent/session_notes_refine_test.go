package agent

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// TestSetHumanNoteFailedSteerRetryDeliversWithoutRewrite verifies F1's first
// half: a save whose derived steer is refused stores durably, and the retry
// with unchanged input completes the pending delivery WITHOUT repeating the
// storage write.
func TestSetHumanNoteFailedSteerRetryDeliversWithoutRewrite(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	// Refuse the first steer via an interrupt fence, then lift it.
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "fence-1"}
		return nil
	}); err != nil {
		t.Fatalf("seed fence: %v", err)
	}
	stored, err := s.SetHumanNote("outer-f1a", "note one")
	if err == nil {
		t.Fatalf("save under fence err = nil, want steer refusal")
	}
	if stored != "note one" {
		t.Fatalf("stored = %q, want %q", stored, "note one")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = nil
		return nil
	}); err != nil {
		t.Fatalf("clear fence: %v", err)
	}
	replayed, err := s.SetHumanNote("outer-f1a", "note one")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if replayed != "note one" {
		t.Fatalf("retry stored = %q, want %q", replayed, "note one")
	}
	s.mu.Lock()
	current := s.humanNote
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if current != "note one" {
		t.Fatalf("stored note after retry = %q, want %q", current, "note one")
	}
	if n != 1 {
		t.Fatalf("steering queue length = %d, want 1 (retry delivers once)", n)
	}
	// The retry re-emits the committed snapshot: drain both change events.
	nextNotesEvent(t, s, events.EventNotesUpdated)
}

// TestSetHumanNoteInterveningSaveThenRetryKeepsNewer verifies F1's second
// half: after a failed steer, an intervening save lands, and the first
// mutation's retry completes delivery for ITS committed value without
// rewriting (and clobbering) the newer note.
func TestSetHumanNoteInterveningSaveThenRetryKeepsNewer(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "fence-1"}
		return nil
	}); err != nil {
		t.Fatalf("seed fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-f1b", "note A"); err == nil {
		t.Fatalf("save A under fence err = nil, want steer refusal")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = nil
		return nil
	}); err != nil {
		t.Fatalf("clear fence: %v", err)
	}
	if _, err := s.SetHumanNote("outer-f1c", "note B"); err != nil {
		t.Fatalf("save B: %v", err)
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
	replayed, err := s.SetHumanNote("outer-f1b", "note A")
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

// TestConcurrentNotesSavesConvergeToOneOrder verifies F2: concurrent saves
// serialize mutation + snapshot + emission under one update lock, so the
// emission order matches the store order (no stale publish after a newer
// store).
func TestConcurrentNotesSavesConvergeToOneOrder(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	const writers = 8
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			note := strings.Repeat("n", i+1)
			if _, err := s.SetHumanNote("outer-conc", note); err != nil &&
				!errors.Is(err, errClientMutationMismatch) {
				t.Errorf("save %q: %v", note, err)
			}
		}(i)
	}
	wg.Wait()
	// Collect every NOTES_UPDATED payload in emission order. Events is
	// best-effort (a full buffer drops), so assert the ordering invariant
	// on what arrived: the last emission must equal the stored note. A
	// stale publish after a newer store would leave them disagreeing.
	var payloads []string
drain:
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind != events.EventNotesUpdated {
				continue
			}
			data, ok := ev.Data.(events.NotesUpdatedData)
			if !ok {
				t.Fatalf("NOTES_UPDATED payload = %T", ev.Data)
			}
			payloads = append(payloads, data.HumanNote)
		default:
			break drain
		}
	}
	s.mu.Lock()
	current := s.humanNote
	s.mu.Unlock()
	if len(payloads) == 0 {
		t.Fatalf("no NOTES_UPDATED events emitted for %d saves", writers)
	}
	last := payloads[len(payloads)-1]
	if last != current {
		t.Fatalf("last emitted note = %q, stored = %q: stale publish won", last, current)
	}
}

// TestNotesPersistenceFailureBlocksSuccessJournal verifies F4: a metadata
// persistence failure surfaces as the mutation error with no success
// journaled, for both the human-note and URL-remove paths.
func TestNotesPersistenceFailureBlocksSuccessJournal(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	injected := errors.New("injected meta save failure")
	s.cfg.testOnly.notesAutoSaveFault = func() error { return injected }
	if _, err := s.SetHumanNote("outer-f4a", "doomed note"); !errors.Is(err, injected) {
		t.Fatalf("human save err = %v, want injected failure", err)
	}
	if _, ok := s.clientMutations.snapshot().Journal["outer-f4a"]; !ok {
		t.Fatalf("outer record missing after failed save (reservation must survive for retry)")
	} else {
		if s.clientMutations.snapshot().Journal["outer-f4a"].OperationState == clientMutationOperationApplied {
			t.Fatalf("failed save journaled applied success")
		}
	}
	entry, err := s.addSessionURL("https://x.test/y", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.RemoveSessionURL("outer-f4b", entry.ID); !errors.Is(err, injected) {
		t.Fatalf("url remove err = %v, want injected failure", err)
	}
	if rec, ok := s.clientMutations.snapshot().Journal["outer-f4b"]; ok &&
		rec.OperationState == clientMutationOperationApplied {
		t.Fatalf("failed url remove journaled applied success")
	}
	// Recovery completes the durable write: with the fault cleared, the
	// still-owned reservation's retry persists and journals success.
	s.cfg.testOnly.notesAutoSaveFault = nil
	if _, err := s.SetHumanNote("outer-f4a", "doomed note"); err != nil {
		t.Fatalf("retry after fault cleared: %v", err)
	}
	if rec := s.clientMutations.snapshot().Journal["outer-f4a"]; rec.OperationState != clientMutationOperationApplied {
		t.Fatalf("retry state = %q, want applied", rec.OperationState)
	}
}

// TestNotesRefreshAfterCompactionReachesRequest verifies F3's first half: a
// notes refresh that ran before a compaction is re-projected into the
// request built after it, so the provider request contains current notes.
func TestNotesRefreshAfterCompactionReachesRequest(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	if _, changed := s.setAgentNote("fresh agent note"); !changed {
		t.Fatal("agent set not reported as change")
	}
	s.maybeAppendNotesContext()
	s.resetNotesProjectionAfterCompaction()
	var timings events.RoundTimings
	_, _, _, req, _, _, err := s.prepareModelRequestWithError(t.Context(), 0, &timings)
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	found := false
	for _, m := range req.Messages {
		if strings.Contains(m.Text(), "fresh agent note") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("provider request omits current notes after compaction reset")
	}
}

// TestNotificationWakeFirstRequestReflectsURLRemoval verifies F3's second
// half: a URL removal that landed while idle is visible on the first
// notification-wake request, even though the notification accept path
// projects no notes context of its own.
func TestNotificationWakeFirstRequestReflectsURLRemoval(t *testing.T) {
	s := newNotesToolSession(t)
	defer s.Close()
	entry, err := s.addSessionURL("https://x.test/keep", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	s.maybeAppendNotesContext()
	if !s.removeSessionURL(entry.ID) {
		t.Fatalf("remove of %q returned false", entry.ID)
	}
	// The notification entry path projects no notes context of its own, so
	// exercise it directly: acceptNotificationInput (empty queue) stands
	// down, while a steering-carrier wake carries the pending steer AND the
	// current notes. Drive the carrier path: queue a steer, accept the
	// carrier, and confirm the wake's history carries the post-removal
	// snapshot (the cleared marker — the removal emptied the store).
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "carrier-1",
		Input:            clientMutationInput("ping", nil),
	}); err != nil {
		t.Fatalf("queue steer: %v", err)
	}
	turnID, ok := s.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued steer")
	}
	if !s.acceptSteeringCarrierInput(t.Context(), turnID) {
		t.Fatalf("acceptSteeringCarrierInput stood down with a steer queued")
	}
	s.mu.Lock()
	var texts []string
	for _, turn := range s.history {
		texts = append(texts, turn.Message.Text())
	}
	s.mu.Unlock()
	joined := strings.Join(texts, "\n")
	// The pre-removal projection stays in history (append-only); what
	// matters is the wake's LATEST projection: the cleared marker with no
	// carried-over URL. maybeAppendNotesContext appends the cleared marker
	// as the last NOTES_CONTEXT turn, which the next model request expands.
	last := texts[len(texts)-2]
	if strings.Contains(last, "https://x.test/keep") {
		t.Fatalf("carrier wake's latest projection still carries the removed URL: %q", last)
	}
	if !strings.Contains(joined, "all shared notes and links cleared") {
		t.Fatalf("carrier wake history omits the post-removal snapshot: %q", joined)
	}
}
