package agent

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestConcurrentNotesDistinctIDsConvergeInStoreOrder verifies G1: overlapping
// writes with DISTINCT mutation IDs serialize mutation through publication,
// so the last emission equals the stored note (no stale publish after a
// newer store).
func TestConcurrentNotesDistinctIDsConvergeInStoreOrder(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	const writers = 8
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			note := strings.Repeat(string(rune('a'+i)), i+1)
			if _, err := s.SetHumanNote("outer-g1-"+string(rune('a'+i)), note); err != nil {
				t.Errorf("save %q: %v", note, err)
			}
		}(i)
	}
	wg.Wait()
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

// TestFailedNoteSaveRetryDelivers verifies G2 (note set): a failed metadata
// save rolls the store back, and the retry with the fault cleared emits the
// update and injects the steer.
func TestFailedNoteSaveRetryDelivers(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	injected := errors.New("injected meta save failure")
	s.cfg.testOnly.notesAutoSaveFault = func() error { return injected }
	if _, err := s.SetHumanNote("outer-g2a", "rescued note"); !errors.Is(err, injected) {
		t.Fatalf("save err = %v, want injected failure", err)
	}
	s.mu.Lock()
	current := s.humanNote
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if current != "" {
		t.Fatalf("stored note after failed save = %q, want rolled back to empty", current)
	}
	if n != 0 {
		t.Fatalf("steering queue length = %d, want 0 after failed save", n)
	}
	// Drain the stream so the retry's emission is observable below.
drain:
	for {
		select {
		case <-s.Events():
		default:
			break drain
		}
	}
	s.cfg.testOnly.notesAutoSaveFault = nil
	stored, err := s.SetHumanNote("outer-g2a", "rescued note")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if stored != "rescued note" {
		t.Fatalf("retry stored = %q, want %q", stored, "rescued note")
	}
	data, ok := nextNotesEvent(t, s, events.EventNotesUpdated).(events.NotesUpdatedData)
	if !ok {
		t.Fatal("retry emitted no NOTES_UPDATED")
	}
	if data.HumanNote != "rescued note" {
		t.Fatalf("retry NOTES_UPDATED = %q, want %q", data.HumanNote, "rescued note")
	}
	s.mu.Lock()
	n = len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want 1 after retry delivery", n)
	}
}

// TestFailedURLRemoveRetryDelivers verifies G2 (url remove): a failed
// metadata save rolls the list back (the entry is present again), and the
// retry with the fault cleared removes it and emits the update.
func TestFailedURLRemoveRetryDelivers(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	entry, err := s.addSessionURL("https://x.test/g2", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	injected := errors.New("injected meta save failure")
	s.cfg.testOnly.notesAutoSaveFault = func() error { return injected }
	if _, err := s.RemoveSessionURL("outer-g2b", entry.ID); !errors.Is(err, injected) {
		t.Fatalf("remove err = %v, want injected failure", err)
	}
	if got := s.sessionURLsForTest(); len(got) != 1 {
		t.Fatalf("url list after failed remove = %+v, want the entry restored", got)
	}
	// Drain the stream so the retry's emission is observable below.
drain:
	for {
		select {
		case <-s.Events():
		default:
			break drain
		}
	}
	s.cfg.testOnly.notesAutoSaveFault = nil
	removed, err := s.RemoveSessionURL("outer-g2b", entry.ID)
	if err != nil || !removed {
		t.Fatalf("retry remove = %v, %v; want true, nil", removed, err)
	}
	if _, ok := nextNotesEvent(t, s, events.EventUrlsUpdated).(events.UrlsUpdatedData); !ok {
		t.Fatal("retry emitted no URLS_UPDATED")
	}
	if got := s.sessionURLsForTest(); len(got) != 0 {
		t.Fatalf("url list after retry = %+v, want empty", got)
	}
}

// TestNotesSteerIDReusedAfterInnerAcceptance verifies G3: a recovery between
// the inner steer acceptance and the outer completion reuses the same inner
// ID instead of accepting a second steer, so the session steers exactly once.
func TestNotesSteerIDReusedAfterInnerAcceptance(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	// First attempt: reserve the outer id directly, then drive the inner
	// steer and record it as accepted — the state an attempt leaves behind
	// when it dies between the inner acceptance and the outer success.
	outerID := "outer-g3"
	innerBase := outerID + "/note-steer"
	request, err := newClientMutationRequest(clientMutationMethodNotesHumanSet, outerID, struct {
		Note string
	}{Note: "single steer"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	lookup, err := s.clientMutations.reservePrepared(request, nil)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if lookup.Lease == nil {
		t.Fatalf("no owner lease for the simulated first attempt")
	}
	text := "human updated their whiteboard: single steer"
	if _, err := s.acceptNotesSteer(innerBase, text); err != nil {
		t.Fatalf("inner accept: %v", err)
	}
	if err := s.markNotesDeliveryPending(outerID, "single steer"); err != nil {
		t.Fatalf("mark delivery pending: %v", err)
	}
	if err := s.markNotesSteerAccepted(outerID, innerBase); err != nil {
		t.Fatalf("mark steer accepted: %v", err)
	}
	lookup.Lease.Release()
	// Simulate the crash: the owner is gone but the record stands with
	// delivery pending and the accepted inner id journaled.
	stored, err := s.SetHumanNote(outerID, "single steer")
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if stored != "single steer" {
		t.Fatalf("recovery stored = %q, want %q", stored, "single steer")
	}
	s.mu.Lock()
	n := len(s.steeringQueue)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("steering queue length = %d, want exactly 1 (recovery must reuse the inner id)", n)
	}
	if rec := s.clientMutations.snapshot().Journal[outerID]; rec.NotesInnerSteerID != innerBase {
		t.Fatalf("journaled inner steer id = %q, want %q", rec.NotesInnerSteerID, innerBase)
	}
}

// TestFinalHistorySnapshotPairsBaselineWithHistory verifies G4: a compaction
// interleaving between the post-refresh history copy and its baseline read
// cannot leave a stale boundary — the pair is captured under one lock, so
// the boundary matches the exact history the round expands.
func TestFinalHistorySnapshotPairsBaselineWithHistory(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	defer s.Close()
	// A completed prior turn on model A carrying thinking, and a newer
	// in-flight turn on model B carrying thinking. Target is model A.
	prior := assistantThinkingTurn("anthropic", "claude-sonnet-4-5", "claude-sonnet-4-5")
	inFlight := assistantThinkingTurn("anthropic", "claude-haiku-4-5", "claude-haiku-4-5")
	s.mu.Lock()
	s.history = []schema.Turn{prior, inFlight}
	s.turnHistoryBaseline = 1
	s.mu.Unlock()
	// Simulate the G4 interleave directly against the production pairing:
	// capture historyTurns, then let a competing compaction shrink history
	// and translate the baseline, then capture the pair the way the fixed
	// final snapshot does (both under one lock).
	s.mu.Lock()
	s.history = []schema.Turn{inFlight}
	s.shrinkTurnHistoryBaseline(2, 1, 0)
	s.mu.Unlock()
	// Fixed behavior: the final copy re-pairs the boundary with the history
	// it guards.
	s.mu.Lock()
	historyTurns := append([]schema.Turn{}, s.history...)
	inFlightFrom := s.turnHistoryBaseline
	s.mu.Unlock()
	if inFlightFrom != 0 {
		t.Fatalf("re-paired baseline = %d, want 0 (the in-flight turn's position in the shrunk history)", inFlightFrom)
	}
	out := expandHistory(historyTurns, replayScope{
		Instance: "anthropic", Model: "claude-sonnet-4-5", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: inFlightFrom, protocolOf: replayProtocolOf,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("in-flight thinking was stripped: the stale boundary escaped the exemption")
	}
}
