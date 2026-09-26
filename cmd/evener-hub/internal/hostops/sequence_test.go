package hostops

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
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

// TestTransitionResolvesAnOrphanOnlyToInterrupted pins spec §4's transition
// graph where it is pinned: "the `orphan-unverified`→`interrupted` resolution"
// is the one exit §4 and §7 name from the fencing state, so an orphan can never
// become a success. Every other edge (pending→running, in-flight→terminal) is
// the deploy slice's to drive; this substrate refuses only what the spec forbids.
func TestTransitionResolvesAnOrphanOnlyToInterrupted(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	orphan, err := store.Transition(record.ID, StateOrphanUnverified, func(r *Record) {
		r.OrphanBoundary = json.RawMessage(`[{"host":"h1","kind":"local-linux"}]`)
	})
	if err != nil {
		t.Fatalf("Transition(orphan-unverified): %v", err)
	}
	before := mustReadFile(t, path)

	for _, to := range []State{StateComplete, StateFailed, StateRunning, StatePending} {
		// The change clears the boundary the way a real resolution would, so the
		// only thing that can refuse this transition is the edge rule itself.
		if _, err := store.Transition(record.ID, to, func(r *Record) { r.OrphanBoundary = nil }); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("Transition(%q) from orphan-unverified: err = %v, want ErrInvalidTransition", to, err)
		}
	}
	stored, ok := store.Record(record.ID)
	if !ok {
		t.Fatalf("record %q disappeared", record.ID)
	}
	if stored.State != StateOrphanUnverified || stored.Sequence != orphan.Sequence {
		t.Fatalf("a refused resolution rewrote the orphan: %+v", stored)
	}
	if got := store.Sequence(); got != 0 {
		t.Fatalf("sequence = %d after refused resolutions, want 0", got)
	}
	if got := string(mustReadFile(t, path)); got != string(before) {
		t.Fatalf("a refused resolution rewrote the store file")
	}
}

// TestTransitionRefusesAChangeThatRewritesTheRecordsIdentity pins the record's
// durable identity against the transition callback. A change may carry progress,
// the terminal result, the fencing epoch, the orphan boundary and the
// host-removed mark; the id, the client operation ID, the host, the kind, the
// pinned (generation, incarnation id) pair, createdAt and the store's sequence
// stamp are not a caller's to rewrite — §4 keys dedup on that identity and race
// scans compare the stamp.
func TestTransitionRefusesAChangeThatRewritesTheRecordsIdentity(t *testing.T) {
	cases := map[string]func(*Record){
		"id":                  func(r *Record) { r.ID = formatAllocatorID(99) },
		"client operation id": func(r *Record) { r.ClientOperationID = "another" },
		"host":                func(r *Record) { r.Host = "h2" },
		"kind":                func(r *Record) { r.Kind = KindRestart },
		"generation":          func(r *Record) { r.Generation = 8 },
		"incarnation id":      func(r *Record) { r.IncarnationID = "inc-2" },
		"created at":          func(r *Record) { r.CreatedAt = r.CreatedAt.Add(time.Hour) },
		"sequence stamp":      func(r *Record) { r.Sequence = 42 },
	}
	for name, rewrite := range cases {
		t.Run(name, func(t *testing.T) {
			store, path := openTestStore(t)
			record := createTestRecord(t, store, "h1")
			before := mustReadFile(t, path)

			if _, err := store.Transition(record.ID, StateRunning, rewrite); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Transition rewriting the %s: err = %v, want ErrInvalidRecord", name, err)
			}
			stored, ok := store.Record(record.ID)
			if !ok {
				t.Fatalf("record %q disappeared", record.ID)
			}
			if stored.State != StatePending || stored.Sequence != 0 {
				t.Fatalf("a refused transition mutated the record: %+v", stored)
			}
			if got := string(mustReadFile(t, path)); got != string(before) {
				t.Fatalf("a refused transition rewrote the store file")
			}
		})
	}
}

// TestTransitionCarriesWhatAChangeMayWrite is the positive control for the
// identity guard: everything the transition callback exists for still lands.
func TestTransitionCarriesWhatAChangeMayWrite(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	done, err := store.Transition(record.ID, StateComplete, func(r *Record) {
		r.Progress = append(r.Progress, ProgressEntry{TS: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), Message: "pushed"})
		r.Result = &Result{OK: true, Message: "deployed"}
		r.FencingEpoch = json.RawMessage(`{"bootId":"boot-1","opSeq":3}`)
		r.HostRemoved = true
	})
	if err != nil {
		t.Fatalf("Transition with a legitimate change: %v", err)
	}
	if done.State != StateComplete || done.Sequence != 1 || done.Result == nil || !done.Result.OK {
		t.Fatalf("transitioned record = %+v, want a complete stamped record with its result", done)
	}
	reloaded := reopenFresh(t, path)
	again, ok := reloaded.Record(record.ID)
	if !ok {
		t.Fatalf("record %q did not survive the reload", record.ID)
	}
	if len(again.Progress) != 1 || again.Result == nil || !again.HostRemoved || len(again.FencingEpoch) == 0 {
		t.Fatalf("the change did not survive the reload: %+v", again)
	}
}

// TestTransitionDoesNotAliasCallerOwnedValues pins the store side of the
// transition callback: whatever mutable value the callback hands in — a progress
// slice, a result, a raw message — the store adopts its own copy. A caller that
// keeps a reference to it and mutates it afterwards must not be able to rewrite
// store state without a write and without the store mutex, or memory and the file
// silently disagree.
func TestTransitionDoesNotAliasCallerOwnedValues(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")

	progress := []ProgressEntry{{TS: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), Message: "pushed"}}
	result := &Result{OK: true, Message: "deployed"}
	epoch := json.RawMessage(`{"bootId":"boot-1","opSeq":3}`)
	if _, err := store.Transition(record.ID, StateComplete, func(r *Record) {
		r.Progress = progress
		r.Result = result
		r.FencingEpoch = epoch
	}); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	before := mustReadFile(t, path)

	// The caller keeps the values it handed in and mutates them.
	progress[0].Message = "rewritten"
	result.OK = false
	result.Message = "rewritten"
	epoch[0] = 'X'

	stored, ok := store.Record(record.ID)
	if !ok {
		t.Fatalf("record %q disappeared", record.ID)
	}
	if len(stored.Progress) != 1 || stored.Progress[0].Message != "pushed" {
		t.Fatalf("the caller's progress slice aliased into the store: %+v", stored.Progress)
	}
	if stored.Result == nil || !stored.Result.OK || stored.Result.Message != "deployed" {
		t.Fatalf("the caller's result aliased into the store: %+v", stored.Result)
	}
	if got := string(stored.FencingEpoch); got != `{"bootId":"boot-1","opSeq":3}` {
		t.Fatalf("the caller's fencing epoch aliased into the store: %s", got)
	}
	if got := string(mustReadFile(t, path)); got != string(before) {
		t.Fatalf("a caller's mutation rewrote the store file")
	}
	reloaded := reopenFresh(t, path)
	again, ok := reloaded.Record(record.ID)
	if !ok {
		t.Fatalf("record %q did not survive the reload", record.ID)
	}
	if again.Result == nil || again.Result.Message != "deployed" {
		t.Fatalf("the persisted result is not the one the transition wrote: %+v", again.Result)
	}
}

// TestTransitionDoesNotLeakAPointerIntoTheAdoptedState pins the other half of the
// callback boundary: the callback never receives a pointer into the snapshot the
// store adopts, so a callback that keeps the pointer cannot mutate store state
// after the call returned — with no lock held and no write.
func TestTransitionDoesNotLeakAPointerIntoTheAdoptedState(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")

	var retained *Record
	if _, err := store.Transition(record.ID, StateRunning, func(r *Record) { retained = r }); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if retained == nil {
		t.Fatalf("the callback never saw the record")
	}
	before := mustReadFile(t, path)

	retained.State = StateFailed
	retained.Host = "h2"
	retained.Sequence = 7
	retained.UpdatedAt = retained.UpdatedAt.Add(time.Hour)

	stored, ok := store.Record(record.ID)
	if !ok {
		t.Fatalf("record %q disappeared", record.ID)
	}
	if stored.State != StateRunning || stored.Host != "h1" || stored.Sequence != 0 {
		t.Fatalf("a retained pointer reached into the store's state: %+v", stored)
	}
	if got := string(mustReadFile(t, path)); got != string(before) {
		t.Fatalf("a retained pointer rewrote the store file")
	}
}
