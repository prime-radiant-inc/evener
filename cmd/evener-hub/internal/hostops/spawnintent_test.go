package hostops

// Crash-fencing 08c §3's pending-spawn intent: the durable pre-spawn record,
// its post-spawn marker match, its clean-convergence drop, and the fence rules
// that keep an open intent out of the interrupted transition and compaction.

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// linuxIntent is a minimal valid pre-spawn intent for the tests.
func linuxIntent(nonce string) SpawnIntent {
	return SpawnIntent{
		Nonce:    nonce,
		Platform: SpawnPlatformLinux,
		CgroupID: "/sys/fs/cgroup/evener/evener-boundary-" + nonce,
	}
}

// TestSpawnIntentArmsBeforeSpawnAndSurvivesReload pins §3's pre-spawn persist:
// the intent lands in its own store write before the spawn and is durable, so a
// crash between the intent and the spawn leaves a reapable record.
func TestSpawnIntentArmsBeforeSpawnAndSurvivesReload(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")

	armed, err := store.ArmSpawnIntent(record.ID, linuxIntent("nonce-1"))
	if err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if len(armed.PendingSpawns) != 1 || armed.PendingSpawns[0].Nonce != "nonce-1" {
		t.Fatalf("armed record carries %+v, want the intent", armed.PendingSpawns)
	}
	if armed.PendingSpawns[0].ValidMarker() {
		t.Fatal("a pre-spawn intent carries the launcher marker before the spawn")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	for _, want := range []string{`"pendingSpawns"`, `"nonce":"nonce-1"`, `"platform":"linux"`, `"cgroupId"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the store file does not carry %s:\n%s", want, raw)
		}
	}

	reopened := reopenFresh(t, path)
	stored, ok := reopened.Record(record.ID)
	if !ok || len(stored.PendingSpawns) != 1 || stored.PendingSpawns[0].CgroupID != "/sys/fs/cgroup/evener/evener-boundary-nonce-1" {
		t.Fatalf("reloaded record = %+v/%v, want the intent", stored.PendingSpawns, ok)
	}
}

// TestSpawnIntentRefusesADuplicateNonce pins the one-boundary-one-nonce rule: a
// second intent naming an armed nonce would give one identity two boundaries.
func TestSpawnIntentRefusesADuplicateNonce(t *testing.T) {
	store, _ := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("nonce-1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("nonce-1")); !errors.Is(err, ErrInvalidSpawnIntent) {
		t.Fatalf("duplicate arm = %v, want ErrInvalidSpawnIntent", err)
	}
}

// TestSpawnIntentRefusesATerminalRecord pins that a finished operation starts
// no new subprocess: arming on a terminal record is refused, so no intent can
// appear beside an outcome already decided.
func TestSpawnIntentRefusesATerminalRecord(t *testing.T) {
	store, _ := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	finish(t, store, record.ID)
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("nonce-1")); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("arm on a terminal record = %v, want ErrInvalidRecord", err)
	}
}

// TestSpawnIntentMatchPersistsTheLauncherMarker pins the post-spawn half: the
// launcher-observed (pid, start token) marker lands durably, a retried match
// with the same pair rewrites nothing, and a different pair is refused so the
// persisted instance proof can never disagree with the process the launcher saw.
func TestSpawnIntentMatchPersistsTheLauncherMarker(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("nonce-1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	matched, err := store.MatchSpawnIntent(record.ID, "nonce-1", 4242, "998877")
	if err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	if !matched.PendingSpawns[0].ValidMarker() || *matched.PendingSpawns[0].PID != 4242 || matched.PendingSpawns[0].StartTime != "998877" {
		t.Fatalf("matched intent = %+v, want the launcher marker", matched.PendingSpawns[0])
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "nonce-1", 4242, "998877"); err != nil {
		t.Fatalf("idempotent MatchSpawnIntent: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a retried match with the same marker rewrote the store")
	}
	if _, err := store.MatchSpawnIntent(record.ID, "nonce-1", 7, "1"); !errors.Is(err, ErrInvalidSpawnIntent) {
		t.Fatalf("match over a marker = %v, want ErrInvalidSpawnIntent", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "other-nonce", 1, "1"); !errors.Is(err, ErrInvalidSpawnIntent) {
		t.Fatalf("match on an unarmed nonce = %v, want ErrInvalidSpawnIntent", err)
	}
}

// TestSpawnIntentDropConvergesAndClearsTheField pins §3's "drops on clean reap
// or resolve": the drop is idempotent, unknown nonces and missing records are
// no-ops, and the last drop removes the field entirely.
func TestSpawnIntentDropConvergesAndClearsTheField(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("nonce-1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("nonce-2")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if err := store.DropSpawnIntent(record.ID, "nonce-1"); err != nil {
		t.Fatalf("DropSpawnIntent: %v", err)
	}
	stored, _ := store.Record(record.ID)
	if len(stored.PendingSpawns) != 1 || stored.PendingSpawns[0].Nonce != "nonce-2" {
		t.Fatalf("after one drop the intents are %+v, want nonce-2", stored.PendingSpawns)
	}
	if err := store.DropSpawnIntent(record.ID, "nonce-1"); err != nil {
		t.Fatalf("repeated drop must be a no-op: %v", err)
	}
	if err := store.DropSpawnIntent("00000000000000000099", "nonce-2"); err != nil {
		t.Fatalf("drop on a missing record must be a no-op: %v", err)
	}
	if err := store.DropSpawnIntent(record.ID, "nonce-2"); err != nil {
		t.Fatalf("DropSpawnIntent: %v", err)
	}
	stored, _ = store.Record(record.ID)
	if len(stored.PendingSpawns) != 0 {
		t.Fatalf("after the last drop the intents are %+v, want none", stored.PendingSpawns)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(raw), "pendingSpawns") {
		t.Fatalf("the store file kept the empty intent field:\n%s", raw)
	}
}

// TestSpawnIntentValidation pins the schema boundary: every malformed intent a
// hand-edited store (or a buggy writer) could carry is refused rather than
// persisted.
func TestSpawnIntentValidation(t *testing.T) {
	cases := map[string]SpawnIntent{
		"empty nonce":            {Platform: SpawnPlatformLinux, CgroupID: "/cg"},
		"unknown platform":       {Nonce: "n", Platform: "plan9", CgroupID: "/cg"},
		"linux without a cgroup": {Nonce: "n", Platform: SpawnPlatformLinux},
		"linux with a darwin pair": func() SpawnIntent {
			pid := 1
			return SpawnIntent{Nonce: "n", Platform: SpawnPlatformLinux, CgroupID: "/cg", PGID: &pid}
		}(),
		"darwin without the pair": {Nonce: "n", Platform: SpawnPlatformDarwin, CgroupID: ""},
		"darwin with a cgroup": func() SpawnIntent {
			pgid, session := 2, 2
			return SpawnIntent{Nonce: "n", Platform: SpawnPlatformDarwin, PGID: &pgid, SessionID: &session, CgroupID: "/cg"}
		}(),
		"half a marker": func() SpawnIntent {
			pid := 9
			return SpawnIntent{Nonce: "n", Platform: SpawnPlatformLinux, CgroupID: "/cg", PID: &pid}
		}(),
		"non-positive marker pid": func() SpawnIntent {
			pid := 0
			return SpawnIntent{Nonce: "n", Platform: SpawnPlatformLinux, CgroupID: "/cg", PID: &pid, StartTime: "1"}
		}(),
		"oversized nonce":     {Nonce: strings.Repeat("x", MaxSpawnNonceBytes+1), Platform: SpawnPlatformLinux, CgroupID: "/cg"},
		"invalid utf-8 nonce": {Nonce: "n\xff", Platform: SpawnPlatformLinux, CgroupID: "/cg"},
	}
	for name, intent := range cases {
		t.Run(name, func(t *testing.T) {
			store, _ := openTestStore(t)
			record := createTestRecord(t, store, "h1")
			if _, err := store.ArmSpawnIntent(record.ID, intent); !errors.Is(err, ErrInvalidSpawnIntent) {
				t.Fatalf("ArmSpawnIntent(%s) = %v, want ErrInvalidSpawnIntent", name, err)
			}
		})
	}
}

// TestSpawnIntentRecordsAndOrphanUnverifiedReads pins the two reads the boot
// reap and the admission fence stand on: only intent-carrying records, only
// open orphan-unverified records, each in id order.
func TestSpawnIntentRecordsAndOrphanUnverifiedReads(t *testing.T) {
	store, _ := openTestStore(t)
	first := createTestRecord(t, store, "h1")
	second := createTestRecord(t, store, "h2")
	plain := createTestRecord(t, store, "h3")
	if _, err := store.ArmSpawnIntent(first.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.ArmSpawnIntent(second.ID, linuxIntent("n2")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.Transition(second.ID, StateOrphanUnverified, func(r *Record) {
		r.OrphanBoundary = []byte(`[{"kind":"local-markerless","platform":"linux","nonce":"n2"}]`)
	}); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	carrying := store.SpawnIntentRecords()
	if len(carrying) != 2 || carrying[0].ID != first.ID || carrying[1].ID != second.ID {
		t.Fatalf("SpawnIntentRecords = %+v, want the two armed records in id order", carrying)
	}
	orphans := store.OrphanUnverified()
	if len(orphans) != 1 || orphans[0].ID != second.ID || len(orphans[0].OrphanBoundary) == 0 {
		t.Fatalf("OrphanUnverified = %+v, want only the second record with its boundary", orphans)
	}
	_ = plain
}

// TestInterruptedTransitionsSkipOpenSpawnIntents pins §3's "never clean, never
// interrupted": neither the boot pass nor the shutdown pass may move a record
// whose spawn intent is still open, because its boundary may still hold the
// orphan. The fence is lost if such a record becomes a terminal unknown.
func TestInterruptedTransitionsSkipOpenSpawnIntents(t *testing.T) {
	store, _ := openTestStore(t)
	fenced := createTestRecord(t, store, "h1")
	plain := createTestRecord(t, store, "h2")
	if _, err := store.ArmSpawnIntent(fenced.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}

	moved, err := store.RecoverInterrupted()
	if err != nil {
		t.Fatalf("RecoverInterrupted: %v", err)
	}
	if moved != 1 {
		t.Fatalf("boot moved %d record(s), want only the record with no open intent", moved)
	}
	stored, _ := store.Record(fenced.ID)
	if stored.State != StatePending {
		t.Fatalf("the fenced record's state = %q, want it left open", stored.State)
	}
	if other, _ := store.Record(plain.ID); other.State != StateInterrupted {
		t.Fatalf("the plain record's state = %q, want interrupted", other.State)
	}

	moved, err = store.InterruptInFlight("controller shut down")
	if err != nil {
		t.Fatalf("InterruptInFlight: %v", err)
	}
	if moved != 0 {
		t.Fatalf("shutdown moved %d record(s), want the open intent left for the reap", moved)
	}
	stored, _ = store.Record(fenced.ID)
	if stored.State != StatePending {
		t.Fatalf("after shutdown the fenced record's state = %q, want pending", stored.State)
	}
}

// TestCompactionKeepsRecordsWithOpenSpawnIntents pins §3's "never an invisible
// orphan": a terminal record still carrying an open intent is not a compaction
// victim, and once its intent drops it compacts normally. This build's API
// cannot produce the shape (Transition refuses to terminalize an intent-carrying
// record and ResolveReapedSpawn clears every intent), so the test injects it
// the way a hand-edited or pre-guard store file can still carry it.
func TestCompactionKeepsRecordsWithOpenSpawnIntents(t *testing.T) {
	store, _ := openRetentionStore(t, RetentionPolicy{TerminalPerHost: 1})
	first := createOp(t, store, "h1", "client-1")
	second := createOp(t, store, "h1", "client-2")
	finish(t, store, first.ID)
	// Inject the intent the moment the record is terminal, before any further
	// committing write: a hand-edited or pre-guard file carries this shape.
	store.cell.mu.Lock()
	for i := range store.cell.state.Records {
		if store.cell.state.Records[i].ID == first.ID {
			store.cell.state.Records[i].PendingSpawns = []SpawnIntent{linuxIntent("n1")}
		}
	}
	store.cell.mu.Unlock()

	finish(t, store, second.ID)
	if _, ok := store.Record(first.ID); !ok {
		t.Fatal("compaction dropped a terminal record whose spawn intent is still open")
	}
	// Dropping the intent makes the record compactable, and the drop's own
	// committing write runs the pass: the oldest terminal record now compacts.
	if err := store.DropSpawnIntent(first.ID, "n1"); err != nil {
		t.Fatalf("DropSpawnIntent: %v", err)
	}
	// The injected intent is in the live snapshot but the drop's write turned it
	// into the file's own state: reload fresh so the assertion is durable.
	reopened := reopenFresh(t, store.path)
	if _, ok := reopened.Record(first.ID); ok {
		t.Fatal("the record did not compact after its intent dropped")
	}
}

// TestTransitionRefusesTerminalizationWithOpenIntents pins §3's fence over a
// record whose boundary is still unaccounted for: a worker cannot record a
// terminal outcome — which would make the record invisible to OrphanUnverified
// and to orphan-resolve — while a spawn intent is open. Dropping the intent
// first is what lets the operation finish.
func TestTransitionRefusesTerminalizationWithOpenIntents(t *testing.T) {
	store, _ := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.Transition(record.ID, StateComplete, terminalChange(true)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition to complete with an open intent = %v, want ErrInvalidTransition", err)
	}
	if _, err := store.TransitionToState(record.ID, StateComplete, &Result{OK: true, Message: "done"}, ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("TransitionToState to complete with an open intent = %v, want ErrInvalidTransition", err)
	}
	if err := store.DropSpawnIntent(record.ID, "n1"); err != nil {
		t.Fatalf("DropSpawnIntent: %v", err)
	}
	if _, err := store.Transition(record.ID, StateComplete, terminalChange(true)); err != nil {
		t.Fatalf("Transition after the intent dropped: %v", err)
	}
}

// TestSetOrphanBoundaryRefusesAnEmptyBoundary pins the fail-closed rule: a mark
// with no entry would read as "verified empty" to the reap, so no zero-entry
// form — `[]`, `[ ]`, null, or malformed bytes — may be persisted.
func TestSetOrphanBoundaryRefusesAnEmptyBoundary(t *testing.T) {
	cases := map[string][]byte{
		"empty array":      []byte(`[]`),
		"whitespace array": []byte(`[ ]`),
		"null":             []byte(`null`),
		"malformed":        []byte(`{`),
	}
	for name, boundary := range cases {
		t.Run(name, func(t *testing.T) {
			store, _ := openTestStore(t)
			record := createTestRecord(t, store, "h1")
			if _, err := store.SetOrphanBoundary(record.ID, boundary, nil); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("SetOrphanBoundary(%s) = %v, want ErrInvalidRecord", name, err)
			}
			stored, _ := store.Record(record.ID)
			if stored.State == StateOrphanUnverified {
				t.Fatal("a refused empty boundary still marked the record")
			}
		})
	}
}

// TestTransitionRejectsATerminalStateWhenTheChangeAddsAnIntent pins the guard's
// placement: the open-intent rule must be checked on the record the callback
// produced, not only before the callback ran, or a terminal transition could
// commit a record carrying an intent — invisible to the orphan fence.
func TestTransitionRejectsATerminalStateWhenTheChangeAddsAnIntent(t *testing.T) {
	store, _ := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if _, err := store.Transition(record.ID, StateComplete, func(r *Record) {
		r.Result = &Result{OK: true, Message: "done"}
		r.PendingSpawns = []SpawnIntent{linuxIntent("n1")}
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition whose change adds an intent = %v, want ErrInvalidTransition", err)
	}
	stored, _ := store.Record(record.ID)
	if stored.State == StateComplete || len(stored.PendingSpawns) != 0 {
		t.Fatalf("the refused transition changed the record: %q/%+v", stored.State, stored.PendingSpawns)
	}
	if _, err := store.Transition(record.ID, StateComplete, terminalChange(true)); err != nil {
		t.Fatalf("the ordinary terminal transition after the refusal: %v", err)
	}
}

// TestResolveReapedSpawnClearsIntentsInOneWrite pins §5's atomic resolution:
// the `orphan-unverified`→`interrupted` transition, the boundary clear and the
// intent drop land in one write, and a resolve that would leave an intent
// unnamed refuses.
func TestResolveReapedSpawnClearsIntentsInOneWrite(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n1")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.ArmSpawnIntent(record.ID, linuxIntent("n2")); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	boundary := []byte(`[{"kind":"local-markerless","platform":"linux","cgroupId":"/cg/n1","nonce":"n1"},{"kind":"local-markerless","platform":"linux","cgroupId":"/cg/n2","nonce":"n2"}]`)
	if _, err := store.Transition(record.ID, StateOrphanUnverified, func(r *Record) { r.OrphanBoundary = boundary }); err != nil {
		t.Fatalf("Transition to orphan-unverified: %v", err)
	}

	if _, err := store.ResolveReapedSpawn(record.ID, []string{"n1"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("resolve with an unnamed intent = %v, want ErrInvalidTransition", err)
	}
	stored, _ := store.Record(record.ID)
	if stored.State != StateOrphanUnverified || len(stored.PendingSpawns) != 2 {
		t.Fatalf("a refused resolve changed the record: %q/%+v", stored.State, stored.PendingSpawns)
	}
	if _, err := store.ResolveReapedSpawn("00000000000000000099", []string{"n1"}); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("resolve of an unknown id = %v, want ErrRecordNotFound", err)
	}

	resolved, err := store.ResolveReapedSpawn(record.ID, []string{"n1", "n2"})
	if err != nil {
		t.Fatalf("ResolveReapedSpawn: %v", err)
	}
	if resolved.State != StateInterrupted || len(resolved.OrphanBoundary) != 0 || len(resolved.PendingSpawns) != 0 {
		t.Fatalf("resolved record = %q/%s/%+v, want interrupted with no boundary or intents", resolved.State, resolved.OrphanBoundary, resolved.PendingSpawns)
	}
	if resolved.Result == nil || resolved.Result.Message != InterruptedNote {
		t.Fatalf("resolved result = %+v, want the interrupted note", resolved.Result)
	}
	if resolved.Sequence == 0 {
		t.Fatal("the resolution did not advance the sequence")
	}
	reloaded := reopenFresh(t, path)
	durable, _ := reloaded.Record(record.ID)
	if durable.State != StateInterrupted || len(durable.PendingSpawns) != 0 || len(durable.OrphanBoundary) != 0 {
		t.Fatalf("the reloaded record = %q/%+v/%s, want one durable write", durable.State, durable.PendingSpawns, durable.OrphanBoundary)
	}
	if _, err := store.ResolveReapedSpawn(record.ID, []string{"n1"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("resolve of a non-orphan record = %v, want ErrInvalidTransition", err)
	}
}
