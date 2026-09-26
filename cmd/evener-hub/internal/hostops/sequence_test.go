package hostops

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

// TestTerminalTransitionsAdvanceAndStampTheDurableSequence pins spec §4's
// state-transition sequence: every atomic store write that moves a record into
// a terminal state advances the durable monotonic per-store sequence and stamps
// the transitioned record with the value it advanced to. Non-terminal
// transitions and record creation advance nothing.
func TestTerminalTransitionsAdvanceAndStampTheDurableSequence(t *testing.T) {
	store, _ := openTestStore(t)
	first := createTestRecord(t, store, "h1")
	second := createTestRecord(t, store, "h2")
	if got := store.Sequence(); got != 0 {
		t.Fatalf("sequence = %d after creating records, want 0: creation is not a terminal transition", got)
	}

	running, err := store.Transition(first.ID, StateRunning, nil)
	if err != nil {
		t.Fatalf("Transition(running): %v", err)
	}
	if got := store.Sequence(); got != 0 {
		t.Fatalf("sequence = %d after a non-terminal transition, want 0", got)
	}
	if running.Sequence != 0 {
		t.Fatalf("the running record was stamped with sequence %d, want 0 (non-terminal)", running.Sequence)
	}

	done, err := store.Transition(first.ID, StateComplete, nil)
	if err != nil {
		t.Fatalf("Transition(complete): %v", err)
	}
	if got := store.Sequence(); got != 1 {
		t.Fatalf("sequence = %d after the first terminal transition, want 1", got)
	}
	if done.Sequence != 1 {
		t.Fatalf("the complete record was stamped with sequence %d, want 1", done.Sequence)
	}
	if !done.State.Terminal() {
		t.Fatalf("State(%q).Terminal() = false, want true", done.State)
	}

	failed, err := store.Transition(second.ID, StateFailed, nil)
	if err != nil {
		t.Fatalf("Transition(failed): %v", err)
	}
	if got := store.Sequence(); got != 2 {
		t.Fatalf("sequence = %d after the second terminal transition, want 2", got)
	}
	if failed.Sequence != 2 {
		t.Fatalf("the failed record was stamped with sequence %d, want 2", failed.Sequence)
	}
	if failed.Sequence <= done.Sequence {
		t.Fatalf("sequence stamps are not monotonic: complete=%d, failed=%d", done.Sequence, failed.Sequence)
	}
}

// TestSequenceAndStampsSurviveReload pins the sequence as durable rather than
// in-process: a reload reads the persisted counter and the stamps verbatim, and
// later terminal transitions continue above them.
func TestSequenceAndStampsSurviveReload(t *testing.T) {
	store, path := openTestStore(t)
	first := createTestRecord(t, store, "h1")
	second := createTestRecord(t, store, "h2")
	if _, err := store.Transition(first.ID, StateComplete, nil); err != nil {
		t.Fatalf("Transition(complete): %v", err)
	}
	if _, err := store.Transition(second.ID, StateFailed, nil); err != nil {
		t.Fatalf("Transition(failed): %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.Sequence(); got != 2 {
		t.Fatalf("reloaded sequence = %d, want the persisted 2", got)
	}
	stamp, ok := reopened.Record(second.ID)
	if !ok {
		t.Fatalf("record %q did not survive the reload", second.ID)
	}
	if stamp.Sequence != 2 {
		t.Fatalf("reloaded stamp = %d, want the persisted 2", stamp.Sequence)
	}

	third := createTestRecord(t, reopened, "h3")
	next, err := reopened.Transition(third.ID, StateInterrupted, nil)
	if err != nil {
		t.Fatalf("Transition(interrupted): %v", err)
	}
	if next.Sequence != 3 {
		t.Fatalf("stamp after the reload = %d, want 3: the sequence must continue above the persisted value", next.Sequence)
	}
	if got := reopened.Sequence(); got != 3 {
		t.Fatalf("sequence after the reload = %d, want 3", got)
	}
	if reopened.Sequence() <= stamp.Sequence {
		t.Fatalf("the sequence went backward across the reload: %d then %d", stamp.Sequence, reopened.Sequence())
	}
}

// TestControllerAssignedIdsAreUniqueAndMonotonic pins spec §8's "The id is
// unique and monotonic per store": ids ascend, their string order is their
// allocation order, and a reload hands out the next id above every allocated
// one instead of restarting.
func TestControllerAssignedIdsAreUniqueAndMonotonic(t *testing.T) {
	store, path := openTestStore(t)
	first := createTestRecord(t, store, "h1")
	second := createTestRecord(t, store, "h2")
	if first.ID >= second.ID {
		t.Fatalf("ids do not ascend in string order: %q then %q", first.ID, second.ID)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	third := createTestRecord(t, reopened, "h3")
	if second.ID >= third.ID {
		t.Fatalf("a reload reallocated an id: %q then %q", second.ID, third.ID)
	}
}

// TestRefusedTransitionsLeaveTheStoreUntouched pins that a refusal is not a
// write: no sequence movement, no state change, no temp file.
func TestRefusedTransitionsLeaveTheStoreUntouched(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	before := mustReadFile(t, path)

	if _, err := store.Transition("no-such-id", StateComplete, nil); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("Transition on an unknown record: err = %v, want ErrRecordNotFound", err)
	}
	if _, err := store.Transition(record.ID, State("queued"), nil); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Transition to an unknown state: err = %v, want ErrInvalidState", err)
	}
	if _, err := store.Transition(record.ID, StateComplete, func(r *Record) { r.Host = "" }); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Transition that invalidates the record: err = %v, want ErrInvalidRecord", err)
	}
	if _, err := store.Transition(record.ID, StateOrphanUnverified, func(r *Record) {
		r.OrphanBoundary = json.RawMessage("{")
	}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Transition carrying an unparseable boundary: err = %v, want ErrInvalidRecord", err)
	}

	if got := store.Sequence(); got != 0 {
		t.Fatalf("sequence = %d after refused transitions, want 0", got)
	}
	stored, ok := store.Record(record.ID)
	if !ok {
		t.Fatalf("record %q disappeared on a refused transition", record.ID)
	}
	if stored.State != StatePending || stored.Host != "h1" {
		t.Fatalf("a refused transition mutated the record: %+v", stored)
	}
	if got := string(mustReadFile(t, path)); got != string(before) {
		t.Fatalf("a refused transition rewrote the store file:\nbefore: %s\nafter:  %s", before, got)
	}
	if temps := leftoverTemps(t, filepath.Dir(path)); len(temps) > 0 {
		t.Fatalf("a refused transition left temp files behind: %v", temps)
	}
}
