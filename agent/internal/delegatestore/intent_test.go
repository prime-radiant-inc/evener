package delegatestore

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// The delegate_intent_stated event (docs/specs/2026-10-11-delegate-latest-intent-design.md):
// a running generation's newest stated tool intent, journaled so a retained row
// shows the last intent of a run a daemon restart interrupted. An empty Intent
// folds as a clear. The intent belongs to the open generation and clears when
// that generation settles, so a resumed run never inherits the previous run's
// line.

func intentEvent(id string, generation uint64, intent string) Event {
	return Event{
		Kind:         EventDelegateIntentStated,
		DelegateID:   id,
		IntentStated: &IntentStated{Generation: generation, Intent: intent},
	}
}

// TestJournalHeaderVersionStaysOne pins the compatibility promise the intent
// event rode: the journal header's version is 1, exactly what this build
// reads and writes — a bump would strand every journal a daemon ever wrote,
// so an additive event kind must never move it.
func TestJournalHeaderVersionStaysOne(t *testing.T) {
	if CurrentVersion != 1 {
		t.Fatalf("journal header version = %d, want 1: an additive event kind must not bump it", CurrentVersion)
	}
}

// reportedFinish is a completed run's finish with the reported packet the
// fold requires when no terminal was prepared first.
func reportedFinish(id string, generation uint64) Event {
	packet := reportedPacket("done")
	return finishedEvent(id, generation, OutcomeCompleted, DispositionReported,
		fmt.Sprintf("%s/delivery/%d", id, generation), &packet)
}

// foldedThroughJournal appends events through a real store and returns the
// state folded back from the journal on disk, so the test proves the
// retained-read path (encode, strict decode, fold) rather than only the
// append path's in-memory result.
func foldedThroughJournal(t *testing.T, events ...Event) (State, State) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	live := make(State)
	for i, event := range events {
		event.Seq = uint64(i + 1)
		if event.TS.IsZero() {
			event.TS = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second)
		}
		_, live, err = store.Append(live, event)
		if err != nil {
			t.Fatalf("Append %q: %v", event.Kind, err)
		}
	}
	read, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	folded, err := Fold(read)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	return folded, live
}

func openIntentDelegate(t *testing.T, id string) []Event {
	t.Helper()
	return []Event{
		createdEvent(id, ""),
		startedEvent(id, 1, TriggerInitial),
	}
}

// TestIntentStatedFoldsIntoOpenRun proves the event carries the running
// generation's newest stated intent onto the aggregate, and that the value
// survives the journal round-trip a cold retained read performs.
func TestIntentStatedFoldsIntoOpenRun(t *testing.T) {
	events := openIntentDelegate(t, "dlg_alpha")
	events = append(events, intentEvent("dlg_alpha", 1, "Reading the board's row tests."))
	folded, live := foldedThroughJournal(t, events...)
	for name, state := range map[string]State{"folded": folded, "live": live} {
		aggregate := state["dlg_alpha"]
		if aggregate == nil {
			t.Fatalf("%s: delegate missing from state", name)
		}
		if got := aggregate.LatestIntent; got != "Reading the board's row tests." {
			t.Fatalf("%s: LatestIntent = %q, want %q", name, got, "Reading the board's row tests.")
		}
	}
}

// TestIntentStatedEmptyFoldsAsClear pins the deliberate divergence from
// applyUpdatePosted, which rejects empty payloads: an empty Intent is the
// turn-start clear and must fold, standing down a previously stated line.
func TestIntentStatedEmptyFoldsAsClear(t *testing.T) {
	events := openIntentDelegate(t, "dlg_alpha")
	events = append(events,
		intentEvent("dlg_alpha", 1, "Reading the board's row tests."),
		intentEvent("dlg_alpha", 1, ""),
	)
	folded, _ := foldedThroughJournal(t, events...)
	if got := folded["dlg_alpha"].LatestIntent; got != "" {
		t.Fatalf("empty intent did not clear the standing line: LatestIntent = %q", got)
	}
}

// TestIntentStatedRejectsForeignGeneration proves the open-run guard: an
// event for a generation other than the open one errors, and the folded
// state keeps the standing line.
func TestIntentStatedRejectsForeignGeneration(t *testing.T) {
	events := openIntentDelegate(t, "dlg_alpha")
	events = append(events, intentEvent("dlg_alpha", 1, "Reading the board's row tests."))
	folded, _ := foldedThroughJournal(t, events...)
	state := clonedState(t, folded)
	stale := intentEvent("dlg_alpha", 2, "Words from another run.")
	if err := Apply(state, stale); err == nil {
		t.Fatal("Apply accepted an intent event for a generation that is not open")
	}
	if got := state["dlg_alpha"].LatestIntent; got != "Reading the board's row tests." {
		t.Fatalf("rejected event changed the standing line: LatestIntent = %q", got)
	}
}

// TestIntentStatedRejectsClosedRun proves a settled run no longer takes
// intent: the event errors against a closed generation.
func TestIntentStatedRejectsClosedRun(t *testing.T) {
	events := openIntentDelegate(t, "dlg_alpha")
	events = append(events,
		reportedFinish("dlg_alpha", 1),
	)
	folded, _ := foldedThroughJournal(t, events...)
	state := clonedState(t, folded)
	if err := Apply(state, intentEvent("dlg_alpha", 1, "Late words.")); err == nil {
		t.Fatal("Apply accepted an intent event for a settled run")
	}
}

// TestRunFinishClearsIntent proves applyRunFinished retires the line with the
// outcome, so the frame field and the read field agree on a settled row.
func TestRunFinishClearsIntent(t *testing.T) {
	events := openIntentDelegate(t, "dlg_alpha")
	events = append(events,
		intentEvent("dlg_alpha", 1, "Reading the board's row tests."),
		reportedFinish("dlg_alpha", 1),
	)
	folded, _ := foldedThroughJournal(t, events...)
	if got := folded["dlg_alpha"].LatestIntent; got != "" {
		t.Fatalf("run finish left the intent standing: LatestIntent = %q", got)
	}
}

// TestNewGenerationStartsEmptyIntent proves a resumed run never inherits the
// previous run's line: run start clears whatever the settled generation said.
func TestNewGenerationStartsEmptyIntent(t *testing.T) {
	events := openIntentDelegate(t, "dlg_alpha")
	events = append(events,
		intentEvent("dlg_alpha", 1, "Reading the board's row tests."),
		reportedFinish("dlg_alpha", 1),
		startedEvent("dlg_alpha", 2, TriggerOwnerInput),
	)
	folded, _ := foldedThroughJournal(t, events...)
	if got := folded["dlg_alpha"].LatestIntent; got != "" {
		t.Fatalf("new generation inherited the previous run's intent: LatestIntent = %q", got)
	}
}

// TestSuccessiveIntentEventsAdvanceRevisionTwice proves LatestIntent joined
// publicProjection: without the membership, an intent frame carries the
// already-applied projection revision and the store's strictly-greater join
// silently discards it while reads alone serve the value.
func TestSuccessiveIntentEventsAdvanceRevisionTwice(t *testing.T) {
	events := openIntentDelegate(t, "dlg_alpha")
	folded, _ := foldedThroughJournal(t, events...)
	state := clonedState(t, folded)
	before := state["dlg_alpha"].ProjectionRevision
	if err := Apply(state, intentEvent("dlg_alpha", 1, "First call.")); err != nil {
		t.Fatalf("Apply first intent: %v", err)
	}
	if err := Apply(state, intentEvent("dlg_alpha", 1, "Second call.")); err != nil {
		t.Fatalf("Apply second intent: %v", err)
	}
	if got := state["dlg_alpha"].ProjectionRevision - before; got != 2 {
		t.Fatalf("two successive intent events advanced ProjectionRevision by %d, want 2", got)
	}
	if got := state["dlg_alpha"].LatestIntent; got != "Second call." {
		t.Fatalf("newest intent did not win: LatestIntent = %q", got)
	}
}

// TestIntentEventEnvelopeValidation pins the envelope rules for the new kind:
// a matching payload validates, a missing payload does not, and two payloads
// never ride one event.
func TestIntentEventEnvelopeValidation(t *testing.T) {
	valid := intentEvent("dlg_alpha", 1, "Reading the board's row tests.")
	if err := validateEventEnvelope(valid); err != nil {
		t.Fatalf("validating a matching intent event: %v", err)
	}
	missing := intentEvent("dlg_alpha", 1, "Reading the board's row tests.")
	missing.IntentStated = nil
	if err := validateEventEnvelope(missing); err == nil {
		t.Fatal("envelope accepted an event whose payload was missing")
	}
	overfull := intentEvent("dlg_alpha", 1, "Reading the board's row tests.")
	overfull.AttentionChanged = &DelegateAttentionChanged{NeedsAttention: true}
	if err := validateEventEnvelope(overfull); err == nil {
		t.Fatal("envelope accepted an event carrying two payloads")
	}
}

// clonedState copies a folded state so a rejected Apply on the copy cannot
// disturb the aggregate the caller still reads.
func clonedState(t *testing.T, state State) State {
	t.Helper()
	clone, err := cloneState(state)
	if err != nil {
		t.Fatalf("cloneState: %v", err)
	}
	return clone
}
