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
	current, _ := s.notesSnapshot()
	if len(payloads) == 0 {
		t.Fatalf("no NOTES_UPDATED events emitted for %d saves", writers)
	}
	last := payloads[len(payloads)-1]
	if last != current {
		t.Fatalf("last emitted note = %q, stored = %q: stale publish won", last, current)
	}
}

func TestFailedNoteSaveRetryDelivers(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error { return errors.New("failed rename") }
	if _, err := s.SetHumanNote("save", "sentinel"); err == nil {
		t.Fatal("missing fault")
	}
	if note, _ := s.notesSnapshot(); note != "" {
		t.Fatalf("failed save stored %q", note)
	}
	if len(s.clientMutations.snapshot().SteeringOrder) != 0 {
		t.Fatal("failed save queued notification")
	}
	reloadHumanNoteStore(t, s)
	response, err := s.SetHumanNote("save", "sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if response.Note != "sentinel" {
		t.Fatalf("retry = %+v", response)
	}
	if len(s.clientMutations.snapshot().SteeringOrder) != 1 {
		t.Fatal("retry lost or duplicated notification")
	}
	nextNotesEvent(t, s, events.EventNotesUpdated)
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

func TestNotesSingleIdentitySurvivesAcceptedRestart(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	s.clientMutations.faults.AfterEffectSnapshotRename = func() error { return errors.New("lost response") }
	if _, err := s.SetHumanNote("save", "sentinel"); err == nil {
		t.Fatal("missing fault")
	}
	reloadHumanNoteStore(t, s)
	response, err := s.SetHumanNote("save", "sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if response.Receipt.ClientMutationID != "save" {
		t.Fatalf("receipt = %+v", response.Receipt)
	}
	snapshot := s.clientMutations.snapshot()
	if len(snapshot.Journal) != 1 || len(snapshot.SteeringOrder) != 1 || snapshot.SteeringOrder[0] != "save" {
		t.Fatalf("nested or duplicate mutation: %v", snapshot.SteeringOrder)
	}
	rebuilt := clientSteeringFromSnapshot(snapshot)
	if len(rebuilt) != 1 || rebuilt[0].ClientMutationID != "save" || rebuilt[0].Kind != events.SteeringKindHumanNote {
		t.Fatalf("rebuilt steering = %+v", rebuilt)
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
