package hostops

// The cross-file commit intent (spec 08b §9): the pendingStoreSync intent's
// store half and the pre-migration pendingCompensation record's phase machine.
// These tests pin the store's own behaviour; the hub composes them at boot.

import (
	"errors"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
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

func compensationTokenValue(seed string) string {
	return testTokenHash(seed)[:minTokenValueChars]
}

func compensationTokenRowJSON(host, value string, generation uint64, at time.Time) string {
	row := tokenRowJSON(host, value, at, at.Add(DefaultTokenTTL), at)
	return strings.Replace(row, `"generation":7`, `"generation":`+strconv.FormatUint(generation, 10), 1)
}

func compensationRecordJSON(host string, phase CompensationPhase, rows []string, stash string, generation uint64) string {
	return `{"host":` + strconv.Quote(host) +
		`,"phase":` + strconv.Quote(string(phase)) +
		`,"rows":[` + strings.Join(rows, ",") + `]` +
		`,"stashReference":` + strconv.Quote(stash) +
		`,"generation":` + strconv.FormatUint(generation, 10) + `}`
}

func rawCompensationStoreJSON(mark time.Time, tokens []string, compensations map[string]string) string {
	entries := make([]string, 0, len(compensations))
	for _, host := range slices.Sorted(maps.Keys(compensations)) {
		entries = append(entries, strconv.Quote(host)+`:`+compensations[host])
	}
	return `{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[],"boundaries":{},` +
		`"wallClockHighWaterMark":` + strconv.Quote(mark.UTC().Format(time.RFC3339)) + `,` +
		`"tokens":[` + strings.Join(tokens, ",") + `],` +
		`"pendingCompensation":{` + strings.Join(entries, ",") + `}}`
}

func seedRawCompensationStore(t *testing.T, tokens []string, compensations map[string]string) (*Store, string, *testClock) {
	t.Helper()
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, rawCompensationStoreJSON(tokenEpoch, tokens, compensations))
	store, err := Open(path, RetentionPolicy{})
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	clock := &testClock{at: tokenEpoch}
	store.clock = clock.now
	return store, path, clock
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
	before := storeFileBytesForTest(t, path)
	if purged, err := store.ApplyStoreSync(intent); err != nil || purged != 0 {
		t.Fatalf("second ApplyStoreSync = %d/%v, want 0/nil", purged, err)
	}
	if after := storeFileBytesForTest(t, path); after != before {
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

// TestOpenReadsPreMigrationPendingCompensation pins reading pre-migration
// pendingCompensation records from disk: fields decode intact, empty row lists
// normalize to non-nil slices, and callers receive isolated copies.
func TestOpenReadsPreMigrationPendingCompensation(t *testing.T) {
	value := compensationTokenValue("m4-open")
	row := compensationTokenRowJSON("m4", value, 7, tokenEpoch)
	store, _, _ := seedRawCompensationStore(t, []string{row}, map[string]string{
		"m4": compensationRecordJSON("m4", CompensationArmed, []string{row}, "/state/hub.toml.stash", 7),
		"m7": compensationRecordJSON("m7", CompensationClear, nil, "/state/m7.stash", 3),
	})
	read, ok := store.Compensation("m4")
	if !ok {
		t.Fatal("the seeded record is not readable")
	}
	if read.Host != "m4" || read.Phase != CompensationArmed || len(read.Rows) != 1 || read.Rows[0].Value != value || read.Stash != "/state/hub.toml.stash" || read.Generation != 7 {
		t.Fatalf("read record = %+v", read)
	}
	read.Rows[0].Host = "mutated"
	again, _ := store.Compensation("m4")
	if again.Rows[0].Host != "m4" {
		t.Fatal("mutating Compensation()'s returned slice mutated store state")
	}
	emptyRows, ok := store.Compensation("m7")
	if !ok || emptyRows.Rows == nil || len(emptyRows.Rows) != 0 {
		t.Fatalf("empty-rows record = %+v/%v, want non-nil empty Rows", emptyRows, ok)
	}
	if _, ok := store.Compensation("missing"); ok {
		t.Fatal("Compensation returned true for an absent host")
	}
	all := store.Compensations()
	if len(all) != 2 || all["m4"].Stash != "/state/hub.toml.stash" || all["m7"].Phase != CompensationClear {
		t.Fatalf("Compensations() = %+v, want both seeded records", all)
	}
	all["m4"].Rows[0].Value = "mutated"
	delete(all, "m7")
	if check := store.Compensations(); len(check) != 2 || check["m4"].Rows[0].Value != value {
		t.Fatal("mutating Compensations()'s returned map mutated store state")
	}
}

// TestLoadRefusesMalformedPendingCompensation pins the decode and schema rules
// for on-disk pendingCompensation records: a missing or empty host, mismatched
// key, omitted field, null rows, unknown phase, foreign preimage row, zero
// generation, or empty stash reference is refused on load.
func TestLoadRefusesMalformedPendingCompensation(t *testing.T) {
	row := compensationTokenRowJSON("m4", compensationTokenValue("m4-bad"), 7, tokenEpoch)
	other := compensationTokenRowJSON("m7", compensationTokenValue("m7-bad"), 7, tokenEpoch)
	for name, comps := range map[string]map[string]string{
		"empty host":        {"": compensationRecordJSON("", CompensationArmed, []string{row}, "stash", 7)},
		"key host mismatch": {"m4": compensationRecordJSON("", CompensationArmed, []string{row}, "stash", 7)},
		"unknown phase":     {"m4": compensationRecordJSON("m4", "compensating-elsewhere", []string{row}, "stash", 7)},
		"foreign row":       {"m4": compensationRecordJSON("m4", CompensationArmed, []string{other}, "stash", 7)},
		"zero generation":   {"m4": compensationRecordJSON("m4", CompensationArmed, []string{row}, "stash", 0)},
		"no stash":          {"m4": compensationRecordJSON("m4", CompensationArmed, []string{row}, "", 7)},
		"omitted phase":     {"m4": `{"host":"m4","rows":[` + row + `],"stashReference":"stash","generation":7}`},
		"null rows":         {"m4": `{"host":"m4","phase":"compensating-armed","rows":null,"stashReference":"stash","generation":7}`},
	} {
		t.Run(name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, rawCompensationStoreJSON(tokenEpoch, nil, comps))
			if _, err := loadFS(afero.NewOsFs(), path); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("loadFS(%s): err = %v, want ErrStoreCorrupt", name, err)
			}
		})
	}
	// Positive control: the same fixture shape with valid values loads.
	validPath := StorePath(t.TempDir())
	writeRawStore(t, validPath, 0o600, rawCompensationStoreJSON(tokenEpoch, nil, map[string]string{
		"m4": compensationRecordJSON("m4", CompensationArmed, []string{row}, "stash", 7),
	}))
	if _, err := loadFS(afero.NewOsFs(), validPath); err != nil {
		t.Fatalf("loadFS(valid): %v", err)
	}
	if _, err := Open(validPath, RetentionPolicy{}); err != nil {
		t.Fatalf("Open(valid): %v", err)
	}
}

// TestReinsertCompensationRowsFiltersAndAdvances pins §9's rows arm: exactly
// the rows `keep` revalidates come back, the phase advances to
// `compensating-runtime` in the same write, and a row already present is not
// duplicated.
func TestReinsertCompensationRowsFiltersAndAdvances(t *testing.T) {
	kept := compensationTokenRowJSON("m4", compensationTokenValue("m4-reinsert"), 7, tokenEpoch)
	dropped := compensationTokenRowJSON("m7", compensationTokenValue("m7-reinsert"), 6, tokenEpoch)
	store, path, _ := seedRawCompensationStore(t, nil, map[string]string{
		"m4": compensationRecordJSON("m4", CompensationHubTOML, []string{kept}, "stash", 7),
		"m7": compensationRecordJSON("m7", CompensationHubTOML, []string{dropped}, "stash", 6),
	})
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
	row := compensationTokenRowJSON("m4", compensationTokenValue("m4-clear"), 7, tokenEpoch)
	store, path, _ := seedRawCompensationStore(t, []string{row}, map[string]string{
		"m4": compensationRecordJSON("m4", CompensationRuntime, []string{row}, "stash", 7),
	})
	if err := store.ClearCompensation("m4"); err != nil {
		t.Fatalf("ClearCompensation: %v", err)
	}
	if _, ok := store.Compensation("m4"); ok {
		t.Fatal("the cleared record survived")
	}
	if err := store.ClearCompensation("m4"); err != nil {
		t.Fatalf("second ClearCompensation: %v", err)
	}
	if _, ok := reopenFresh(t, path).Compensation("m4"); ok {
		t.Fatal("the cleared record survived a reopen")
	}
}

// TestAdvanceCompensationRefusesRegressions pins the phase machine: only the
// forward steps are legal, and a record that is not there cannot advance.
func TestAdvanceCompensationRefusesRegressions(t *testing.T) {
	row := compensationTokenRowJSON("m4", compensationTokenValue("m4-advance"), 7, tokenEpoch)
	store, _, _ := seedRawCompensationStore(t, []string{row}, map[string]string{
		"m4": compensationRecordJSON("m4", CompensationArmed, []string{row}, "stash", 7),
	})
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
		Removed: map[string]HostRemovedMark{"gone": {Generation: 7, IncarnationID: testTokenHash("inc-gone")[:36]}},
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

// TestReconcileTokenRowsMatchesTheTombstonedIncarnation pins the pair rule: a
// token row drops only when the tombstone names its exact (generation,
// incarnation) pair — an old-incarnation tombstone never deletes a live
// re-add's row, and a tombstone a live re-add superseded matches nothing.
func TestReconcileTokenRowsMatchesTheTombstonedIncarnation(t *testing.T) {
	store, _, _ := openClockStore(t)
	readd := mintTokenAt(t, store, "m4", 9)
	removedRow := mintTokenAt(t, store, "m9", 2)
	view := TokenRowReconcile{
		Live: map[string]uint64{"m4": 9},
		Removed: map[string]HostRemovedMark{
			// The name was re-added: this tombstone's incarnation is not the
			// live row's, and the live name supersedes it.
			"m4": {Generation: 2, IncarnationID: "incarnation-m4-old"},
			// This one names the removed incarnation exactly.
			"m9": {Generation: 2, IncarnationID: testTokenHash("inc-m9")[:36]},
		},
	}
	dropped, err := store.ReconcileTokenRows(view)
	if err != nil {
		t.Fatalf("ReconcileTokenRows: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("ReconcileTokenRows dropped %d, want 1 (the tombstoned incarnation's row)", dropped)
	}
	if _, ok := store.OutstandingToken("m4"); !ok {
		t.Fatal("an old-incarnation tombstone deleted a live re-add's token row")
	}
	if _, ok := store.OutstandingToken("m9"); ok {
		t.Fatal("the tombstoned incarnation's token row survived")
	}
	_ = readd
	_ = removedRow
}

// TestApplyHostRemovedPassMarksOnlyTheMatchingPair pins §4's tombstone pass:
// only records whose pinned (generation, incarnation id) pair matches the
// tombstone's pair are marked, a re-add's records are not, and the removed
// host's token rows drop with it.
func TestApplyHostRemovedPassMarksOnlyTheMatchingPair(t *testing.T) {
	store, path, _ := openClockStore(t)
	outcome, err := store.Create(OperationCreateRequest{ClientOperationID: "op-1", Host: "m4", Kind: KindDeploy, Pair: OperationPair{Generation: 7, IncarnationID: "inc-7"}, BootID: "test-boot"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	record := outcome.Record
	// A re-add's record carries a different incarnation id and must not match.
	newerOutcome, err := store.Create(OperationCreateRequest{ClientOperationID: "op-2", Host: "m4", Kind: KindRestart, Pair: OperationPair{Generation: 9, IncarnationID: "inc-9"}, BootID: "test-boot"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	newer := newerOutcome.Record
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
	before := storeFileBytesForTest(t, path)
	if marked, dropped, err := reopened.ApplyHostRemovedPass(map[string]HostRemovedMark{"m4": {Generation: 7, IncarnationID: "inc-7"}}); err != nil || marked != 0 || dropped != 0 {
		t.Fatalf("second ApplyHostRemovedPass = %d/%d/%v, want 0/0/nil", marked, dropped, err)
	}
	if after := storeFileBytesForTest(t, path); after != before {
		t.Fatal("a no-op host-removed pass rewrote the store")
	}
}

// storeFileBytesForTest returns the raw store file bytes, so a test can prove a
// no-op pass wrote nothing.
func storeFileBytesForTest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	return string(raw)
}
