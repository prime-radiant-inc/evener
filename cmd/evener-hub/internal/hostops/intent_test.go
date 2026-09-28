package hostops

// The cross-file commit intent (spec 08b §9): the pendingStoreSync intent's
// store half and the pendingCompensation armed record's phase machine. These
// tests pin the store's own behaviour; the hub composes them at boot and on the
// remove path.

import (
	"errors"
	"os"
	"testing"
)

// mintTokenAt mints one valid token row for host at the given generation,
// through the store's own mint path, and returns it.
func mintTokenAt(t *testing.T, store *Store, host string, generation uint64) Token {
	t.Helper()
	req := mintDefaults(host, tokenEpoch)
	req.Generation = generation
	req.IncarnationID = testTokenHash("inc-" + host)[:36]
	return mustMint(t, store, req)
}

// TestApplyStoreSyncPurgesExactlyTheNamedRows pins the purge half: only the
// intent's named rows go, a second application is a no-op (the store-already-
// applied case), and an intent naming no present row writes nothing.
func TestApplyStoreSyncPurgesExactlyTheNamedRows(t *testing.T) {
	store, path, _ := openClockStore(t)
	first := mintTokenAt(t, store, "m4", 7)
	other := mintTokenAt(t, store, "m7", 7)
	intent := StoreSyncIntent{Host: "m4", Generation: 7, Values: []string{first.Value}}
	purged, err := store.ApplyStoreSync(intent)
	if err != nil {
		t.Fatalf("ApplyStoreSync: %v", err)
	}
	if purged != 1 {
		t.Fatalf("ApplyStoreSync purged %d rows, want 1", purged)
	}
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("the intent's named row survived the purge")
	}
	if _, ok := store.OutstandingToken("m7"); !ok {
		t.Fatal("the purge dropped a row no intent named")
	}
	_ = other
	// The store-already-applied no-op: a re-applied intent finds nothing to
	// delete and writes nothing.
	before := storeSnapshotForTest(t, path)
	if purged, err := store.ApplyStoreSync(intent); err != nil || purged != 0 {
		t.Fatalf("second ApplyStoreSync = %d/%v, want 0/nil", purged, err)
	}
	if after := storeSnapshotForTest(t, path); after != before {
		t.Fatal("a re-applied intent that found nothing to delete rewrote the store")
	}
}

// TestApplyStoreSyncRefusesAnInvalidIntent pins the schema: an empty host, a
// zero generation, an empty value and an empty value list are refused with
// nothing written.
func TestApplyStoreSyncRefusesAnInvalidIntent(t *testing.T) {
	store, _, _ := openClockStore(t)
	row := mintTokenAt(t, store, "m4", 7)
	for _, intent := range []StoreSyncIntent{
		{Host: "", Generation: 7, Values: []string{row.Value}},
		{Host: "m4", Generation: 0, Values: []string{row.Value}},
		{Host: "m4", Generation: 7, Values: []string{""}},
		{Host: "m4", Generation: 7, Values: nil},
	} {
		if _, err := store.ApplyStoreSync(intent); err == nil {
			t.Fatalf("ApplyStoreSync(%+v) succeeded, want a refusal", intent)
		}
	}
	if _, ok := store.OutstandingToken("m4"); !ok {
		t.Fatal("a refused intent dropped a row")
	}
}

// TestApplyStoreSyncIsOneAtomicWrite pins the fault seam: a failure before the
// rename leaves every row, and the retry lands the purge.
func TestApplyStoreSyncIsOneAtomicWrite(t *testing.T) {
	store, _, _ := openClockStore(t)
	row := mintTokenAt(t, store, "m4", 7)
	intent := StoreSyncIntent{Host: "m4", Generation: 7, Values: []string{row.Value}}
	store.faults.beforeRename = func() error { return errors.New("injected crash before the rename") }
	if purged, err := store.ApplyStoreSync(intent); err == nil || purged != 0 {
		t.Fatalf("ApplyStoreSync with a failing rename = %d/%v, want 0/error", purged, err)
	}
	if _, ok := store.OutstandingToken("m4"); !ok {
		t.Fatal("a pre-rename failure dropped the row")
	}
	store.faults.beforeRename = nil
	if purged, err := store.ApplyStoreSync(intent); err != nil || purged != 1 {
		t.Fatalf("retried ApplyStoreSync = %d/%v, want 1/nil", purged, err)
	}
}

// TestArmAndPurgeCompensationWalksThePhaseMachine pins §9's record: the
// preimage is persisted before the purge, the purge write advances the phase
// past `armed`, and the record's rows are a durable copy of the purged rows.
func TestArmAndPurgeCompensationWalksThePhaseMachine(t *testing.T) {
	store, path, _ := openClockStore(t)
	row := mintTokenAt(t, store, "m4", 7)
	armed := Compensation{
		Host:       "m4",
		Phase:      CompensationArmed,
		Rows:       []Token{row},
		Stash:      "/state/hub.toml.stash",
		Generation: 7,
	}
	if err := store.ArmCompensation(armed); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	read, ok := store.Compensation("m4")
	if !ok {
		t.Fatal("the armed record is not readable")
	}
	if read.Phase != CompensationArmed || len(read.Rows) != 1 || read.Rows[0].Value != row.Value || read.Stash != "/state/hub.toml.stash" || read.Generation != 7 {
		t.Fatalf("armed record = %+v", read)
	}
	purged, err := store.PurgeCompensated("m4", []string{row.Value})
	if err != nil {
		t.Fatalf("PurgeCompensated: %v", err)
	}
	if purged != 1 {
		t.Fatalf("PurgeCompensated purged %d, want 1", purged)
	}
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("the purge left the row behind")
	}
	read, ok = store.Compensation("m4")
	if !ok || read.Phase != CompensationHubTOML {
		t.Fatalf("phase after the purge = %+v, want %s", read, CompensationHubTOML)
	}
	// A crash after the purge re-reads the same phase and preimage from the file.
	reopened := reopenFresh(t, path)
	read, ok = reopened.Compensation("m4")
	if !ok || read.Phase != CompensationHubTOML || len(read.Rows) != 1 {
		t.Fatalf("reopened record = %+v, want the %s phase with its preimage", read, CompensationHubTOML)
	}
}

// TestArmCompensationRefusesAMalformedRecord pins the schema: a record without
// a host, a phase outside the set, a row of another host, a zero generation or
// no stash reference is refused with nothing written.
func TestArmCompensationRefusesAMalformedRecord(t *testing.T) {
	store, _, _ := openClockStore(t)
	row := mintTokenAt(t, store, "m4", 7)
	other := mintTokenAt(t, store, "m7", 7)
	for name, record := range map[string]Compensation{
		"no host":         {Phase: CompensationArmed, Rows: []Token{row}, Stash: "stash", Generation: 7},
		"unknown phase":   {Host: "m4", Phase: "compensating-elsewhere", Rows: []Token{row}, Stash: "stash", Generation: 7},
		"foreign row":     {Host: "m4", Phase: CompensationArmed, Rows: []Token{other}, Stash: "stash", Generation: 7},
		"zero generation": {Host: "m4", Phase: CompensationArmed, Rows: []Token{row}, Stash: "stash"},
		"no stash":        {Host: "m4", Phase: CompensationArmed, Rows: []Token{row}, Generation: 7},
	} {
		if err := store.ArmCompensation(record); err == nil {
			t.Fatalf("ArmCompensation(%s) succeeded, want a refusal", name)
		}
	}
	if _, ok := store.Compensation("m4"); ok {
		t.Fatal("a refused arm wrote a record")
	}
}

// TestCompensationSidecarSpellingIsAnAlias pins §9's legacy literal: a
// persisted `compensating-sidecar` record reads as `compensating-hubtoml`, so
// the arm is the same restore, never an unknown phase.
func TestCompensationSidecarSpellingIsAnAlias(t *testing.T) {
	if got := NormalizeCompensationPhase("compensating-sidecar"); got != CompensationHubTOML {
		t.Fatalf("NormalizeCompensationPhase(compensating-sidecar) = %q, want %q", got, CompensationHubTOML)
	}
	if got := NormalizeCompensationPhase(CompensationArmed); got != CompensationArmed {
		t.Fatalf("NormalizeCompensationPhase(armed) = %q, want armed", got)
	}
	// A record that carries the legacy spelling arms as the aliased phase.
	store, _, _ := openClockStore(t)
	row := mintTokenAt(t, store, "m4", 7)
	if err := store.ArmCompensation(Compensation{Host: "m4", Phase: "compensating-sidecar", Rows: []Token{row}, Stash: "stash", Generation: 7}); err != nil {
		t.Fatalf("ArmCompensation(sidecar alias): %v", err)
	}
	read, ok := store.Compensation("m4")
	if !ok || read.Phase != CompensationHubTOML {
		t.Fatalf("aliased record = %+v, want %s", read, CompensationHubTOML)
	}
}

// TestReinsertCompensationRowsFiltersAndAdvances pins §9's rows arm: exactly
// the rows `keep` revalidates come back, the phase advances to
// `compensating-runtime` in the same write, and a row already present is not
// duplicated.
func TestReinsertCompensationRowsFiltersAndAdvances(t *testing.T) {
	store, path, _ := openClockStore(t)
	kept := mintTokenAt(t, store, "m4", 7)
	dropped := mintTokenAt(t, store, "m7", 6)
	if _, err := store.ApplyStoreSync(StoreSyncIntent{Host: "m4", Generation: 7, Values: []string{kept.Value}}); err != nil {
		t.Fatalf("purge m4: %v", err)
	}
	if _, err := store.ApplyStoreSync(StoreSyncIntent{Host: "m7", Generation: 6, Values: []string{dropped.Value}}); err != nil {
		t.Fatalf("purge m7: %v", err)
	}
	armed := Compensation{Host: "m4", Phase: CompensationHubTOML, Rows: []Token{kept}, Stash: "stash", Generation: 7}
	if err := store.ArmCompensation(armed); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	stale := Compensation{Host: "m7", Phase: CompensationHubTOML, Rows: []Token{dropped}, Stash: "stash", Generation: 6}
	if err := store.ArmCompensation(stale); err != nil {
		t.Fatalf("ArmCompensation(m7): %v", err)
	}
	// The rows arm runs after the hub.toml restore advanced each record to
	// `compensating-rows`.
	for _, host := range []string{"m4", "m7"} {
		if err := store.AdvanceCompensation(host, CompensationRows); err != nil {
			t.Fatalf("AdvanceCompensation(%s, rows): %v", host, err)
		}
	}
	inserted, err := store.ReinsertCompensationRows("m4", func(row Token) bool { return row.Generation == 7 })
	if err != nil {
		t.Fatalf("ReinsertCompensationRows: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("ReinsertCompensationRows inserted %d, want 1", inserted)
	}
	if _, ok := store.OutstandingToken("m4"); !ok {
		t.Fatal("the revalidated row was not re-inserted")
	}
	read, ok := store.Compensation("m4")
	if !ok || read.Phase != CompensationRuntime {
		t.Fatalf("phase after the re-insert = %+v, want %s", read, CompensationRuntime)
	}
	// A row the restored view does not revalidate stays out, and its phase
	// still advances: the rows arm ran, it just re-inserted nothing.
	if inserted, err := store.ReinsertCompensationRows("m7", func(row Token) bool { return row.Generation == 7 }); err != nil || inserted != 0 {
		t.Fatalf("filtered ReinsertCompensationRows = %d/%v, want 0/nil", inserted, err)
	}
	if _, ok := store.OutstandingToken("m7"); ok {
		t.Fatal("a row the restored view does not revalidate came back")
	}
	// A reopen proves the re-inserted row and the phase are durable together.
	reopened := reopenFresh(t, path)
	read, ok = reopened.Compensation("m4")
	if !ok || read.Phase != CompensationRuntime {
		t.Fatalf("reopened phase = %+v, want %s", read, CompensationRuntime)
	}
	if _, ok := reopened.OutstandingToken("m4"); !ok {
		t.Fatal("the re-inserted row did not survive the reopen")
	}
}

// TestClearCompensationRemovesTheRecord pins the final step: the record goes in
// its own write and a second clear is a no-op.
func TestClearCompensationRemovesTheRecord(t *testing.T) {
	store, _, _ := openClockStore(t)
	row := mintTokenAt(t, store, "m4", 7)
	if err := store.ArmCompensation(Compensation{Host: "m4", Phase: CompensationRuntime, Rows: []Token{row}, Stash: "stash", Generation: 7}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	if err := store.ClearCompensation("m4"); err != nil {
		t.Fatalf("ClearCompensation: %v", err)
	}
	if _, ok := store.Compensation("m4"); ok {
		t.Fatal("the cleared record survived")
	}
	if err := store.ClearCompensation("m4"); err != nil {
		t.Fatalf("second ClearCompensation: %v", err)
	}
}

// TestAdvanceCompensationRefusesRegressions pins the phase machine: only the
// forward steps are legal, and a record that is not there cannot advance.
func TestAdvanceCompensationRefusesRegressions(t *testing.T) {
	store, _, _ := openClockStore(t)
	row := mintTokenAt(t, store, "m4", 7)
	if err := store.ArmCompensation(Compensation{Host: "m4", Phase: CompensationArmed, Rows: []Token{row}, Stash: "stash", Generation: 7}); err != nil {
		t.Fatalf("ArmCompensation: %v", err)
	}
	if err := store.AdvanceCompensation("m4", CompensationArmed); err == nil {
		t.Fatal("AdvanceCompensation(armed) succeeded, want a refusal")
	}
	if err := store.AdvanceCompensation("m7", CompensationRows); err == nil {
		t.Fatal("AdvanceCompensation on a missing record succeeded, want a refusal")
	}
	for _, to := range []CompensationPhase{CompensationHubTOML, CompensationRows, CompensationRuntime, CompensationClear} {
		if err := store.AdvanceCompensation("m4", to); err != nil {
			t.Fatalf("AdvanceCompensation(%s): %v", to, err)
		}
	}
	if err := store.AdvanceCompensation("m4", CompensationRows); err == nil {
		t.Fatal("a backward advance succeeded, want a refusal")
	}
}

// TestReconcileTokenRowsDropsStaleAndRemovedRows pins the reverse direction:
// rows for a tombstoned host drop, rows the hub.toml generation advanced past
// drop, live-current rows and intent-covered rows stay, and a host the file
// carries no entry for keeps its rows (the mirror is preserved, never rolled
// back).
func TestReconcileTokenRowsDropsStaleAndRemovedRows(t *testing.T) {
	store, _, _ := openClockStore(t)
	liveCurrent := mintTokenAt(t, store, "live", 7)
	stale := mintTokenAt(t, store, "advanced", 6)
	removed := mintTokenAt(t, store, "gone", 7)
	covered := mintTokenAt(t, store, "covered", 6)
	absent := mintTokenAt(t, store, "absent", 3)
	view := TokenRowReconcile{
		Live:    map[string]uint64{"live": 7, "advanced": 8},
		Removed: map[string]struct{}{"gone": {}},
		Covered: map[string]map[string]struct{}{"covered": {covered.Value: {}}},
	}
	dropped, err := store.ReconcileTokenRows(view)
	if err != nil {
		t.Fatalf("ReconcileTokenRows: %v", err)
	}
	if dropped != 2 {
		t.Fatalf("ReconcileTokenRows dropped %d, want 2", dropped)
	}
	for host, want := range map[string]bool{"live": true, "advanced": false, "gone": false, "covered": true, "absent": true} {
		if _, ok := store.OutstandingToken(host); ok != want {
			t.Fatalf("token row for %q present=%v, want %v", host, ok, want)
		}
	}
	_ = []Token{liveCurrent, stale, removed, absent}
}

// TestApplyHostRemovedPassMarksOnlyTheMatchingPair pins §4's tombstone pass:
// only records whose pinned (generation, incarnation id) pair matches the
// tombstone's pair are marked, a re-add's records are not, and the removed
// host's token rows drop with it.
func TestApplyHostRemovedPassMarksOnlyTheMatchingPair(t *testing.T) {
	store, path, _ := openClockStore(t)
	record, err := store.Create(NewRecord{ClientOperationID: "op-1", Host: "m4", Kind: KindDeploy, Generation: 7, IncarnationID: "inc-7"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A re-add's record carries a different incarnation id and must not match.
	newer, err := store.Create(NewRecord{ClientOperationID: "op-2", Host: "m4", Kind: KindRestart, Generation: 9, IncarnationID: "inc-9"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mintTokenAt(t, store, "m4", 7)
	marked, dropped, err := store.ApplyHostRemovedPass(map[string]HostRemovedMark{"m4": {Generation: 7, IncarnationID: "inc-7"}})
	if err != nil {
		t.Fatalf("ApplyHostRemovedPass: %v", err)
	}
	if marked != 1 || dropped != 1 {
		t.Fatalf("ApplyHostRemovedPass = %d marked/%d dropped, want 1/1", marked, dropped)
	}
	reopened := reopenFresh(t, path)
	stored, ok := reopened.Record(record.ID)
	if !ok || !stored.HostRemoved {
		t.Fatalf("record %s hostRemoved=%v, want true", record.ID, stored.HostRemoved)
	}
	storedNewer, ok := reopened.Record(newer.ID)
	if !ok || storedNewer.HostRemoved {
		t.Fatalf("a re-add's record was marked host-removed")
	}
	if _, ok := reopened.OutstandingToken("m4"); ok {
		t.Fatal("the removed host's token row survived the pass")
	}
	// Idempotent: a second pass marks nothing and writes nothing.
	before := storeSnapshotForTest(t, path)
	if marked, dropped, err := reopened.ApplyHostRemovedPass(map[string]HostRemovedMark{"m4": {Generation: 7, IncarnationID: "inc-7"}}); err != nil || marked != 0 || dropped != 0 {
		t.Fatalf("second ApplyHostRemovedPass = %d/%d/%v, want 0/0/nil", marked, dropped, err)
	}
	if after := storeSnapshotForTest(t, path); after != before {
		t.Fatal("a no-op host-removed pass rewrote the store")
	}
}

// storeSnapshotForTest returns the raw store file bytes, so a test can prove a
// no-op pass wrote nothing.
func storeSnapshotForTest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	return string(raw)
}
