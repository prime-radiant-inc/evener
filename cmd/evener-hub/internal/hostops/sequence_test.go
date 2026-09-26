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

	reopened := reopenFresh(t, path)
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

	reopened := reopenFresh(t, path)
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

// TestTransitionRefusesAFurtherMoveFromATerminalState pins terminal states as
// final. Spec §4 advances the state-transition sequence for "every atomic store
// write that moves a record into a terminal state" and stamps the moved record;
// a second move would advance it again and replace the stamp, letting a finished
// operation's outcome be rewritten under the race scans that compare sequence
// values only.
func TestTransitionRefusesAFurtherMoveFromATerminalState(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	done, err := store.Transition(record.ID, StateComplete, nil)
	if err != nil {
		t.Fatalf("Transition(complete): %v", err)
	}
	before := mustReadFile(t, path)

	for _, to := range []State{StateFailed, StateInterrupted, StateRunning, StatePending, StateComplete} {
		if _, err := store.Transition(record.ID, to, nil); !errors.Is(err, ErrRecordTerminal) {
			t.Fatalf("Transition(%q) out of a terminal state: err = %v, want ErrRecordTerminal", to, err)
		}
	}
	if got := store.Sequence(); got != done.Sequence {
		t.Fatalf("sequence = %d after refused transitions, want the terminal state's %d", got, done.Sequence)
	}
	stored, ok := store.Record(record.ID)
	if !ok {
		t.Fatalf("record %q disappeared", record.ID)
	}
	if stored.State != StateComplete || stored.Sequence != done.Sequence {
		t.Fatalf("a refused transition rewrote the finished record: %+v", stored)
	}
	if got := string(mustReadFile(t, path)); got != string(before) {
		t.Fatalf("a refused transition rewrote the store file:\nbefore: %s\nafter:  %s", before, got)
	}
}

// TestTransitionResolvesOrphanUnverifiedToInterrupted pins the one resolution
// spec §4 names into a terminal state from a non-terminal one: "the
// `orphan-unverified`→`interrupted` resolution", which advances the sequence
// like every other terminal transition.
func TestTransitionResolvesOrphanUnverifiedToInterrupted(t *testing.T) {
	store, _ := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if _, err := store.Transition(record.ID, StateOrphanUnverified, func(r *Record) {
		r.OrphanBoundary = json.RawMessage(`[{"host":"h1","kind":"local-linux"}]`)
	}); err != nil {
		t.Fatalf("Transition(orphan-unverified): %v", err)
	}
	resolved, err := store.Transition(record.ID, StateInterrupted, func(r *Record) {
		// The boundary belongs to the orphan state; the fencing slice clears it
		// with the resolution, and the schema requires exactly that pairing.
		r.OrphanBoundary = nil
	})
	if err != nil {
		t.Fatalf("resolving orphan-unverified: %v", err)
	}
	if resolved.Sequence != 1 {
		t.Fatalf("the resolution was stamped %d, want 1", resolved.Sequence)
	}
	if got := store.Sequence(); got != 1 {
		t.Fatalf("sequence = %d after the resolution, want 1", got)
	}
	if resolved.State != StateInterrupted {
		t.Fatalf("resolved state = %q, want %q", resolved.State, StateInterrupted)
	}
}
