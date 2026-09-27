package hostops

// Dedup and the atomic consume-and-create write (spec 08b §4, §6 step 1/step 4,
// §12). The rows this file pins: the (host, kind, generation, incarnation id)
// dedup scope — the replay path, the superseded-generation path (newest
// retained, or `stale-entry` on a stale intended pair), the clean-slate re-add
// path and the conflicting-reuse refusals (different host, deploy-versus-restart,
// a current-generation host-removed record never matching); the atomic
// consume-and-create write, crash window included; the step-(4) token refusals
// with the token unconsumed and no record; the fencing epoch the created record
// carries; and the epoch-only row boot deletes silently.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// opQuery builds a dedup query for one host/kind/client-operation-id at the
// current pair, with no intended pair named.
func opQuery(host string, kind Kind, clientID string, generation uint64, incarnationID string) OperationDedupQuery {
	return OperationDedupQuery{
		ClientOperationID: clientID,
		Host:              host,
		Kind:              kind,
		Current:           OperationPair{Generation: generation, IncarnationID: incarnationID},
	}
}

// createPairRecord persists one pending record pinned to (generation,
// incarnationID) and completes it, so the dedup path sees a terminal retained
// record.
func createPairRecord(t *testing.T, store *Store, host string, kind Kind, clientID string, generation uint64, incarnationID string) Record {
	t.Helper()
	record, err := store.Create(NewRecord{
		ClientOperationID: clientID,
		Host:              host,
		Kind:              kind,
		Generation:        generation,
		IncarnationID:     incarnationID,
	})
	if err != nil {
		t.Fatalf("Create(%s/%s): %v", host, clientID, err)
	}
	done, err := store.Transition(record.ID, StateComplete, func(r *Record) {
		r.Result = &Result{OK: true, Message: "done"}
	})
	if err != nil {
		t.Fatalf("Transition(%s): %v", record.ID, err)
	}
	return done
}

// TestLookupOperationReplaysTheCurrentPairRecord is §4's replay: a same-key
// record whose pinned pair equals the registry's current pair is returned.
func TestLookupOperationReplaysTheCurrentPairRecord(t *testing.T) {
	store, _ := openTestStore(t)
	record := createPairRecord(t, store, "h1", KindDeploy, "op-1", 7, "inc-h1")

	got, hit, err := store.LookupOperation(opQuery("h1", KindDeploy, "op-1", 7, "inc-h1"))
	if err != nil || !hit {
		t.Fatalf("LookupOperation = (%+v, %v, %v), want a hit", got, hit, err)
	}
	if got.ID != record.ID || got.State != StateComplete {
		t.Fatalf("replay = %+v, want record %s in state complete", got, record.ID)
	}
}

// TestLookupOperationRefusesCrossHostReuse is §12's conflicting-reuse row: a
// current-generation record of a different host refuses the reuse.
func TestLookupOperationRefusesCrossHostReuse(t *testing.T) {
	store, _ := openTestStore(t)
	createPairRecord(t, store, "h2", KindDeploy, "op-1", 7, "inc-h2")
	if err := store.MirrorBoundaries(map[string]Boundary{
		"h2": {Generation: 7, IncarnationID: "inc-h2", PresenceEpoch: 1},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}

	_, _, err := store.LookupOperation(opQuery("h1", KindDeploy, "op-1", 7, "inc-h1"))
	if _, ok := errors.AsType[*ConflictingOperationIDError](err); !ok {
		t.Fatalf("LookupOperation error = %v, want ConflictingOperationIDError", err)
	}
}

// TestLookupOperationRefusesDeployVersusRestartReuse is the same row for a
// current-generation record of a different kind.
func TestLookupOperationRefusesDeployVersusRestartReuse(t *testing.T) {
	store, _ := openTestStore(t)
	createPairRecord(t, store, "h1", KindDeploy, "op-1", 7, "inc-h1")

	_, _, err := store.LookupOperation(opQuery("h1", KindRestart, "op-1", 7, "inc-h1"))
	if _, ok := errors.AsType[*ConflictingOperationIDError](err); !ok {
		t.Fatalf("LookupOperation error = %v, want ConflictingOperationIDError", err)
	}
}

// TestLookupOperationNeverMatchesAHostRemovedRecord pins §4's "a host-removed
// record never matches a dedup lookup": the current-pair host-removed record is
// a conflicting reuse, never a replay.
func TestLookupOperationNeverMatchesAHostRemovedRecord(t *testing.T) {
	store, _ := openTestStore(t)
	record, err := store.Create(NewRecord{
		ClientOperationID: "op-1", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-h1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(record.ID, StateComplete, func(r *Record) {
		r.Result = &Result{OK: true, Message: "done"}
		r.HostRemoved = true
	}); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	_, hit, err := store.LookupOperation(opQuery("h1", KindDeploy, "op-1", 7, "inc-h1"))
	if _, ok := errors.AsType[*ConflictingOperationIDError](err); !ok {
		t.Fatalf("LookupOperation error = %v, want ConflictingOperationIDError", err)
	}
	if hit {
		t.Fatal("a host-removed record matched a dedup lookup")
	}
}

// TestLookupOperationReturnsTheNewestSupersededRecord is §4's superseded arm
// for a request that names no intended pair: the newest retained
// superseded-generation record comes back, never a fresh operation.
func TestLookupOperationReturnsTheNewestSupersededRecord(t *testing.T) {
	store, _ := openTestStore(t)
	first := createPairRecord(t, store, "h1", KindDeploy, "op-1", 7, "inc-h1")
	second := createPairRecord(t, store, "h1", KindDeploy, "op-1", 8, "inc-h1b")

	got, hit, err := store.LookupOperation(opQuery("h1", KindDeploy, "op-1", 9, "inc-h1c"))
	if err != nil || !hit {
		t.Fatalf("LookupOperation = (%+v, %v, %v), want the newest retained record", got, hit, err)
	}
	if got.ID != second.ID {
		t.Fatalf("replay id = %s, want the newest retained %s (first was %s)", got.ID, second.ID, first.ID)
	}
}

// TestLookupOperationRefusesAStaleIntendedPair is the same arm for a request
// that names an intended pair older than current and matching no retained
// record: §11's pruned-generation stale-entry.
func TestLookupOperationRefusesAStaleIntendedPair(t *testing.T) {
	store, _ := openTestStore(t)
	createPairRecord(t, store, "h1", KindDeploy, "op-1", 8, "inc-h1b")

	intended := OperationPair{Generation: 6, IncarnationID: "inc-h1a"}
	query := opQuery("h1", KindRestart, "op-1", 9, "inc-h1c")
	query.Intended = &intended
	_, hit, err := store.LookupOperation(query)
	var stale *StaleEntryError
	if !errors.As(err, &stale) || stale.Binding != StaleBindingPrunedGeneration {
		t.Fatalf("LookupOperation error = %v, want a pruned-generation StaleEntryError", err)
	}
	if hit {
		t.Fatal("a stale intended pair reported a dedup hit")
	}
}

// TestLookupOperationReplaysTheIntendedSupersededPair pins the lost-response
// retry: a request repeating the old pair replays the retained record.
func TestLookupOperationReplaysTheIntendedSupersededPair(t *testing.T) {
	store, _ := openTestStore(t)
	record := createPairRecord(t, store, "h1", KindRestart, "op-1", 7, "inc-h1")

	intended := OperationPair{Generation: 7, IncarnationID: "inc-h1"}
	query := opQuery("h1", KindRestart, "op-1", 8, "inc-h1b")
	query.Intended = &intended
	got, hit, err := store.LookupOperation(query)
	if err != nil || !hit {
		t.Fatalf("LookupOperation = (%+v, %v, %v), want the retained record", got, hit, err)
	}
	if got.ID != record.ID {
		t.Fatalf("replay id = %s, want %s", got.ID, record.ID)
	}
}

// TestLookupOperationOpensFreshOnTheCurrentPairAfterReAdd pins the clean-slate
// re-add path: the removed incarnation's records are history, and a request
// naming the re-added pair opens fresh rather than replaying them.
func TestLookupOperationOpensFreshOnTheCurrentPairAfterReAdd(t *testing.T) {
	store, _ := openTestStore(t)
	record, err := store.Create(NewRecord{
		ClientOperationID: "op-1", Host: "h1", Kind: KindRestart, Generation: 7, IncarnationID: "inc-old",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(record.ID, StateFailed, func(r *Record) {
		r.Result = &Result{OK: false, Message: "lost"}
		r.HostRemoved = true
	}); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	intended := OperationPair{Generation: 8, IncarnationID: "inc-new"}
	query := opQuery("h1", KindRestart, "op-1", 8, "inc-new")
	query.Intended = &intended
	got, hit, err := store.LookupOperation(query)
	if err != nil {
		t.Fatalf("LookupOperation: %v", err)
	}
	if hit {
		t.Fatalf("a reuse after re-add replayed %+v", got)
	}
}

// TestConsumeTokenAndCreatePromotesTheEpochInOneWrite is §6 step 4: the token
// row is deleted, the probe epoch is promoted into the pending record carrying
// the worker's fencing epoch, and both land in one write.
func TestConsumeTokenAndCreatePromotesTheEpochInOneWrite(t *testing.T) {
	store, path, clock := openClockStore(t)
	token := mustMint(t, store, mintDefaults("h1", clock.now()))
	epoch, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	})
	if err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}

	record, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        token.Value,
		SequenceBefore:    store.Sequence(),
	})
	if err != nil {
		t.Fatalf("ConsumeTokenAndCreateOperation: %v", err)
	}
	if record.State != StatePending || record.Kind != KindDeploy {
		t.Fatalf("record = %+v, want a pending deploy", record)
	}
	var carried GuardEpoch
	if err := json.Unmarshal(record.FencingEpoch, &carried); err != nil {
		t.Fatalf("record fencing epoch %s: %v", record.FencingEpoch, err)
	}
	if carried.BootID != epoch.BootID || carried.OpSeq != epoch.OpSeq {
		t.Fatalf("record epoch = %+v, want the promoted %+v", carried, epoch)
	}

	// One write: the file the atomic write landed carries the record, no token
	// row and no probe epoch row.
	reloaded := reopenFresh(t, path)
	if _, ok := reloaded.Record(record.ID); !ok {
		t.Fatalf("record %s did not survive the reload", record.ID)
	}
	if row, ok := reloaded.OutstandingToken("h1"); ok {
		t.Fatalf("the consumed token row survived: %+v", row)
	}
	if row, ok := reloaded.ProbeEpoch("h1"); ok {
		t.Fatalf("the promoted probe epoch row survived: %+v", row)
	}
}

// TestConsumeTokenAndCreateRefusesExpiredTokenWithoutConsuming pins the
// at-or-before-now arm: the refusal leaves the token row and creates no record.
func TestConsumeTokenAndCreateRefusesExpiredTokenWithoutConsuming(t *testing.T) {
	store, _, clock := openClockStore(t)
	token := mustMint(t, store, mintDefaults("h1", clock.now()))
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}
	clock.advance(DefaultTokenTTL)

	_, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        token.Value,
	})
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("ConsumeTokenAndCreateOperation error = %v, want ErrTokenExpired", err)
	}
	if row, ok := store.OutstandingToken("h1"); !ok || row.Value != token.Value {
		t.Fatalf("the expired refusal consumed the token: %+v", row)
	}
	if got := store.Records(); len(got) != 0 {
		t.Fatalf("the expired refusal created records: %+v", got)
	}
}

// TestConsumeTokenAndCreateRefusesSupersededToken pins the changed-nonce arm.
func TestConsumeTokenAndCreateRefusesSupersededToken(t *testing.T) {
	store, _, clock := openClockStore(t)
	first := mustMint(t, store, mintDefaults("h1", clock.now()))
	second := mustMint(t, store, mintDefaults("h1", clock.now()))
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}

	_, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        first.Value,
	})
	if !errors.Is(err, ErrTokenSuperseded) {
		t.Fatalf("ConsumeTokenAndCreateOperation error = %v, want ErrTokenSuperseded", err)
	}
	if row, ok := store.OutstandingToken("h1"); !ok || row.Value != second.Value {
		t.Fatalf("the superseded refusal disturbed the current row: %+v", row)
	}
	if got := store.Records(); len(got) != 0 {
		t.Fatalf("the superseded refusal created records: %+v", got)
	}
}

// TestConsumeTokenAndCreateRefusesAMismatchedToken pins the value-outside-the-
// wire-shape arm: nothing is consumed and nothing is created.
func TestConsumeTokenAndCreateRefusesAMismatchedToken(t *testing.T) {
	store, _, clock := openClockStore(t)
	token := mustMint(t, store, mintDefaults("h1", clock.now()))
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}

	_, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        "not-a-token",
	})
	if !errors.Is(err, ErrTokenMismatched) {
		t.Fatalf("ConsumeTokenAndCreateOperation error = %v, want ErrTokenMismatched", err)
	}
	if row, ok := store.OutstandingToken("h1"); !ok || row.Value != token.Value {
		t.Fatalf("the mismatched refusal consumed the token: %+v", row)
	}
}

// TestConsumeTokenAndCreateRefusesAConcurrentTerminalOp pins the step-(3) scan
// folded into the consume's locked read: a terminal operation above the caller's
// pre-probe sequence refuses, token unconsumed and no record.
func TestConsumeTokenAndCreateRefusesAConcurrentTerminalOp(t *testing.T) {
	store, _, clock := openClockStore(t)
	token := mustMint(t, store, mintDefaults("h1", clock.now()))
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}
	sequenceBefore := store.Sequence()
	other := createPairRecord(t, store, "h1", KindRestart, "op-other", 7, "inc-h1")

	_, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        token.Value,
		SequenceBefore:    sequenceBefore,
	})
	var concurrent *ConcurrentTerminalOpError
	if !errors.As(err, &concurrent) || concurrent.ID != other.ID {
		t.Fatalf("ConsumeTokenAndCreateOperation error = %v, want a concurrent-terminal-op naming %s", err, other.ID)
	}
	if row, ok := store.OutstandingToken("h1"); !ok || row.Value != token.Value {
		t.Fatalf("the concurrent-terminal refusal consumed the token: %+v", row)
	}
}

// TestConsumeAndCreateCrashWindowLeavesNoConsumedToken is §12's
// atomic-consume-and-create row: a failure before the rename consumes nothing
// and creates nothing; a failure after the rename (the landed, un-synced write)
// is reported as landed and the record is what a replayed deploy finds.
func TestConsumeAndCreateCrashWindowLeavesNoConsumedToken(t *testing.T) {
	store, path, clock := openClockStore(t)
	token := mustMint(t, store, mintDefaults("h1", clock.now()))
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}
	request := OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        token.Value,
	}

	// The crash window: the write fails before its rename lands.
	store.faults.beforeRename = func() error { return errors.New("injected crash before the rename") }
	_, _, err := store.ConsumeTokenAndCreateOperation(request)
	if err == nil || RenameLanded(err) {
		t.Fatalf("pre-rename failure = %v, want a plain refusal", err)
	}
	if row, ok := store.OutstandingToken("h1"); !ok || row.Value != token.Value {
		t.Fatalf("the pre-rename failure consumed the token: %+v", row)
	}
	if got := store.Records(); len(got) != 0 {
		t.Fatalf("the pre-rename failure created records: %+v", got)
	}

	// The landed write behind a failed directory sync: the caller must reconcile
	// with the record, never retry as though nothing was written. The fault is
	// keyed to the store's own directory, which only the post-rename sync names
	// (ensureStoreDir syncs the parents above it).
	store.faults.beforeRename = nil
	var syncErr error
	store.faults.syncDir = func(_ afero.Fs, dir string) error {
		if dir != filepath.Dir(path) {
			return nil
		}
		return syncErr
	}
	syncErr = errors.New("injected directory-sync failure")
	record, _, err := store.ConsumeTokenAndCreateOperation(request)
	if !RenameLanded(err) {
		t.Fatalf("post-rename failure = %v, want RenameLanded", err)
	}
	if record.ID == "" || record.State != StatePending {
		t.Fatalf("landed record = %+v, want a pending record", record)
	}
	reloaded := reopenFresh(t, path)
	if _, ok := reloaded.Record(record.ID); !ok {
		t.Fatalf("record %s did not survive the reload", record.ID)
	}
	if row, ok := reloaded.OutstandingToken("h1"); ok {
		t.Fatalf("the landed write left the consumed token behind: %+v", row)
	}
}

// TestCreateOperationMintsItsFencingEpoch is restart's atomic record creation:
// no pre-probe epoch row is promoted, but the record still carries a durable
// (boot id, op sequence) epoch, minted above the host's counter, and a stale
// probe-epoch row for the host is superseded.
func TestCreateOperationMintsItsFencingEpoch(t *testing.T) {
	store, path, _ := openClockStore(t)
	stale, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-old", Generation: 7, IncarnationID: "inc-h1",
	})
	if err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}

	record, _, err := store.CreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindRestart,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		BootID:            "boot-1",
	})
	if err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}
	if record.State != StatePending || record.Kind != KindRestart {
		t.Fatalf("record = %+v, want a pending restart", record)
	}
	var carried GuardEpoch
	if err := json.Unmarshal(record.FencingEpoch, &carried); err != nil {
		t.Fatalf("record fencing epoch %s: %v", record.FencingEpoch, err)
	}
	if carried.BootID != "boot-1" || carried.OpSeq <= stale.OpSeq {
		t.Fatalf("record epoch = %+v, want boot-1 above the stale %+v", carried, stale)
	}
	reloaded := reopenFresh(t, path)
	if row, ok := reloaded.ProbeEpoch("h1"); ok {
		t.Fatalf("the superseded probe epoch row survived: %+v", row)
	}
	if _, ok := reloaded.Record(record.ID); !ok {
		t.Fatalf("record %s did not survive the reload", record.ID)
	}
}

// TestInterruptInFlightNamesTheShutdown pins the worker-lifetime transition:
// every pending/running record moves to interrupted with the caller's note, and
// the move advances the state-transition sequence.
func TestInterruptInFlightNamesTheShutdown(t *testing.T) {
	store, path := openTestStore(t)
	pending := createPairRecord(t, store, "h1", KindDeploy, "op-1", 7, "inc-h1")
	running, err := store.Create(NewRecord{
		ClientOperationID: "op-2", Host: "h2", Kind: KindRestart, Generation: 7, IncarnationID: "inc-h2",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(running.ID, StateRunning, nil); err != nil {
		t.Fatalf("Transition to running: %v", err)
	}

	moved, err := store.InterruptInFlight("interrupted: the controller is shutting down")
	if err != nil || moved != 1 {
		t.Fatalf("InterruptInFlight = (%d, %v), want (1, nil)", moved, err)
	}
	reloaded := reopenFresh(t, path)
	got, ok := reloaded.Record(running.ID)
	if !ok || got.State != StateInterrupted || got.Result == nil {
		t.Fatalf("interrupted record = %+v, ok=%v", got, ok)
	}
	if got.Result.Message != "interrupted: the controller is shutting down" {
		t.Fatalf("interrupted note = %q", got.Result.Message)
	}
	if got.Sequence != reloaded.Sequence() {
		t.Fatalf("interrupted record sequence %d, store sequence %d", got.Sequence, reloaded.Sequence())
	}
	if terminal, _ := reloaded.Record(pending.ID); terminal.State != StateComplete {
		t.Fatalf("a terminal record moved: %+v", terminal)
	}
}

// TestEpochOnlyRowIsReapedSilentlyAtBoot pins the durable-probe-epoch row's
// crash half: a crash between the epoch's persist and the consume leaves an
// epoch-only row that boot deletes without a record and without a token.
func TestEpochOnlyRowIsReapedSilentlyAtBoot(t *testing.T) {
	store, path, _ := openClockStore(t)
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}

	reloaded := reopenFresh(t, path)
	reaped, err := reloaded.ReapProbeEpochs()
	if err != nil || reaped != 1 {
		t.Fatalf("ReapProbeEpochs = (%d, %v), want (1, nil)", reaped, err)
	}
	if _, ok := reloaded.ProbeEpoch("h1"); ok {
		t.Fatal("the epoch-only row survived the boot reap")
	}
	if got := reloaded.Records(); len(got) != 0 {
		t.Fatalf("the boot reap left records behind: %+v", got)
	}
}

// TestInterruptInFlightLeavesTerminalRecordsAlone pins the shutdown pass's
// scope: only pending/running records move.
func TestInterruptInFlightLeavesTerminalRecordsAlone(t *testing.T) {
	store, _ := openTestStore(t)
	done := createPairRecord(t, store, "h1", KindDeploy, "op-1", 7, "inc-h1")
	moved, err := store.InterruptInFlight("interrupted: the controller is shutting down")
	if err != nil || moved != 0 {
		t.Fatalf("InterruptInFlight = (%d, %v), want (0, nil)", moved, err)
	}
	got, _ := store.Record(done.ID)
	if got.State != StateComplete {
		t.Fatalf("terminal record moved to %q", got.State)
	}
}

// TestEffectiveNowReadsMaxOfClockAndMark pins the one clock the deploy paths
// compare against: the durable wall-clock high-water mark wins while a rollback
// is active.
func TestEffectiveNowReadsMaxOfClockAndMark(t *testing.T) {
	store, _, clock := openClockStore(t)
	if got := store.EffectiveNow(); !got.Equal(clock.now()) {
		t.Fatalf("EffectiveNow = %s, want %s", got, clock.now())
	}
	if _, err := store.MintToken(mintDefaults("h1", clock.now())); err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	clock.set(clock.now().Add(-time.Hour))
	mark := tokenEpoch
	if got := store.EffectiveNow(); !got.Equal(mark) {
		t.Fatalf("EffectiveNow during a rollback = %s, want the mark %s", got, mark)
	}
}

// TestAppendProgressIsBounded keeps the record's progress list within §10's
// bounded shape: oldest entries drop first and the list never exceeds the cap.
func TestAppendProgressIsBounded(t *testing.T) {
	store, _ := openTestStore(t)
	record, err := store.Create(NewRecord{
		ClientOperationID: "op-1", Host: "h1", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-h1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := range MaxProgressEntries + 5 {
		if _, err := store.AppendProgress(record.ID, "step"); err != nil {
			t.Fatalf("AppendProgress(%d): %v", i, err)
		}
	}
	got, _ := store.Record(record.ID)
	if len(got.Progress) != MaxProgressEntries {
		t.Fatalf("progress entries = %d, want the %d cap", len(got.Progress), MaxProgressEntries)
	}
}

// TestConsumeAndCreateRefusesAnUnpromotableEpoch pins the promotion's own
// precondition: a deploy whose probe epoch row is missing never consumes the
// token and never creates a record.
func TestConsumeAndCreateRefusesAnUnpromotableEpoch(t *testing.T) {
	store, _, clock := openClockStore(t)
	token := mustMint(t, store, mintDefaults("h1", clock.now()))
	_, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        token.Value,
	})
	if !errors.Is(err, ErrProbeEpochUnavailable) {
		t.Fatalf("ConsumeTokenAndCreateOperation error = %v, want ErrProbeEpochUnavailable", err)
	}
	if row, ok := store.OutstandingToken("h1"); !ok || row.Value != token.Value {
		t.Fatalf("the refused consume disturbed the token: %+v", row)
	}
	if got := store.Records(); len(got) != 0 {
		t.Fatalf("the refused consume created records: %+v", got)
	}
}

// TestConsumeAndCreateRefusesAPairMismatchedEpoch pins the promotion's binding
// check: an epoch row bound to another pair is never promoted.
func TestConsumeAndCreateRefusesAPairMismatchedEpoch(t *testing.T) {
	store, _, clock := openClockStore(t)
	token := mustMint(t, store, mintDefaults("h1", clock.now()))
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 8, IncarnationID: "inc-other",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}
	_, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        token.Value,
	})
	if !errors.Is(err, ErrProbeEpochUnavailable) {
		t.Fatalf("ConsumeTokenAndCreateOperation error = %v, want ErrProbeEpochUnavailable", err)
	}
}

// TestConsumedTokenReplayedWithANewOperationIDReadsTokenMissing pins §12's
// consumed-then-replayed row through the consume-and-create path: the row is
// gone, so a replay with a new operation ID is a token-missing refusal.
func TestConsumedTokenReplayedWithANewOperationIDReadsTokenMissing(t *testing.T) {
	store, _, clock := openClockStore(t)
	token := mustMint(t, store, mintDefaults("h1", clock.now()))
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}
	if _, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        token.Value,
	}); err != nil {
		t.Fatalf("ConsumeTokenAndCreateOperation: %v", err)
	}
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}
	_, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-2",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        token.Value,
	})
	if !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("replayed consume error = %v, want ErrTokenMissing", err)
	}
	if got := store.Records(); len(got) != 1 {
		t.Fatalf("records = %d, want the one consumed record", len(got))
	}
}

// TestConsumeAndCreateKeepsTheStoreMode pins §4's mode rule on this write's own
// paths: the store file it lands is owner-only.
func TestConsumeAndCreateKeepsTheStoreMode(t *testing.T) {
	store, path, clock := openClockStore(t)
	token := mustMint(t, store, mintDefaults("h1", clock.now()))
	if _, err := store.PersistProbeEpoch(ProbeEpochRequest{
		Host: "h1", BootID: "boot-1", Generation: 7, IncarnationID: "inc-h1",
	}); err != nil {
		t.Fatalf("PersistProbeEpoch: %v", err)
	}
	if _, _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{
		ClientOperationID: "op-1",
		Host:              "h1",
		Kind:              KindDeploy,
		Pair:              OperationPair{Generation: 7, IncarnationID: "inc-h1"},
		TokenValue:        token.Value,
	}); err != nil {
		t.Fatalf("ConsumeTokenAndCreateOperation: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store mode = %04o, want 0600", perm)
	}
}
