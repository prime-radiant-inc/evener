package hubcore

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizePinSectionName(t *testing.T) {
	tests := []struct {
		raw, display, key string
		wantErr           bool
	}{
		{raw: "  Research  ", display: "Research", key: "research"},
		{raw: "Straße", display: "Straße", key: "strasse"},
		{raw: "\t\n", wantErr: true},
		{raw: strings.Repeat("界", 81), wantErr: true},
	}
	for _, tt := range tests {
		display, key, err := NormalizePinSectionName(tt.raw)
		if (err != nil) != tt.wantErr || display != tt.display || key != tt.key {
			t.Fatalf("NormalizePinSectionName(%q) = %q, %q, %v", tt.raw, display, key, err)
		}
	}
}

func TestPinSectionStoreCreateReusesCaseFoldedNameAndMovesAtomically(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	first, changed, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1, 0))
	if err != nil || !changed || first.MemberCount != 1 {
		t.Fatalf("first = %+v, %v, %v", first, changed, err)
	}
	reused, changed, err := store.CreateOrReuseAndAssign("research", "session-b", time.Unix(2, 0))
	if err != nil || !changed || reused.ID != first.ID || reused.Name != "Research" || reused.MemberCount != 2 {
		t.Fatalf("reuse = %+v, %v, %v", reused, changed, err)
	}
	other, _, err := store.CreateOrReuseAndAssign("Client", "session-a", time.Unix(3, 0))
	if err != nil || other.MemberCount != 1 {
		t.Fatalf("other = %+v, %v", other, err)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins["session-a"].SectionID != other.ID {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestPinSectionStoreCreateReusesUnicodeCaseFoldedName(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	first, changed, err := store.CreateOrReuseAndAssign("Straße", "session-a", time.Unix(1, 0))
	if err != nil || !changed || first.MemberCount != 1 {
		t.Fatalf("first = %+v, %v, %v", first, changed, err)
	}
	reused, changed, err := store.CreateOrReuseAndAssign("STRASSE", "session-b", time.Unix(2, 0))
	if err != nil || !changed || reused.ID != first.ID || reused.Name != "Straße" || reused.MemberCount != 2 {
		t.Fatalf("reuse = %+v, %v, %v", reused, changed, err)
	}
}

func TestPinSectionStoreEquivalentReuseDoesNotRenameOrReassign(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	first, changed, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("first = %+v, %v, %v", first, changed, err)
	}
	second, changed, err := store.CreateOrReuseAndAssign("research", "session-a", time.Unix(2, 0))
	if err != nil || changed || second.ID != first.ID || second.Name != "Research" {
		t.Fatalf("reuse noop = %+v, %v, %v", second, changed, err)
	}
	sections, err := store.Sections()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || sections[0].Name != "Research" {
		t.Fatalf("sections = %+v", sections)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 || pins["session-a"].AssignedAt != time.Unix(1, 0).UTC() {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestPinSectionStoreSectionsOrderedAndMemberCountDurable(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	alpha, changed, err := store.CreateOrReuseAndAssign("beta", "session-a", time.Unix(10, 0))
	if err != nil || !changed {
		t.Fatalf("create beta = %+v, %v, %v", alpha, changed, err)
	}
	_, changed, err = store.CreateOrReuseAndAssign("Alpha", "session-b", time.Unix(11, 0))
	if err != nil || !changed {
		t.Fatalf("create alpha = %v, %v", changed, err)
	}
	sections, err := store.Sections()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 || sections[0].Name != "Alpha" || sections[0].MemberCount != 1 || sections[1].Name != "beta" || sections[1].MemberCount != 1 {
		t.Fatalf("sections = %+v", sections)
	}
	if _, err := store.Unpin("session-a"); err != nil {
		t.Fatal(err)
	}
	sections, err = store.Sections()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 || sections[1].MemberCount != 0 {
		t.Fatalf("sections after unpin = %+v", sections)
	}
}

func TestPinSectionStoreRenameAllowsCaseOnlyChangeAndRejectsConflicts(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	section, changed, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("create = %+v, %v, %v", section, changed, err)
	}
	renamed, changed, err := store.Rename(section.ID, "research", time.Unix(2, 0))
	if err != nil || !changed || renamed.Name != "research" || renamed.MemberCount != 1 {
		t.Fatalf("rename = %+v, %v, %v", renamed, changed, err)
	}
	other, changed, err := store.CreateOrReuseAndAssign("Client", "session-b", time.Unix(3, 0))
	if err != nil || !changed {
		t.Fatalf("create other = %+v, %v, %v", other, changed, err)
	}
	if _, _, err := store.Rename(other.ID, "Research", time.Unix(4, 0)); !errorsIsPinSectionConflict(err) {
		t.Fatalf("rename conflict err = %v", err)
	}
}

func TestPinSectionStoreDeleteSectionReturnsMemberCountAndCascadesAssignments(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	section, changed, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("create = %+v, %v, %v", section, changed, err)
	}
	assigned, changed, err := store.Assign(section.ID, "session-b", time.Unix(2, 0))
	if err != nil || !changed || assigned.MemberCount != 2 {
		t.Fatalf("assign = %+v, %v, %v", assigned, changed, err)
	}
	count, changed, err := store.DeleteSection(section.ID)
	if err != nil || !changed || count != 2 {
		t.Fatalf("delete = %d, %v, %v", count, changed, err)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 0 {
		t.Fatalf("assignments after delete = %+v", pins)
	}
}

func TestPinSectionStoreNoOpsReturnChangedFalse(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	section, changed, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("create = %+v, %v, %v", section, changed, err)
	}
	if _, changed, err := store.Assign(section.ID, "session-a", time.Unix(2, 0)); err != nil || changed {
		t.Fatalf("assign noop = %v, %v", changed, err)
	}
	if ok, err := store.Unpin("session-missing"); err != nil || ok {
		t.Fatalf("unpin noop = %v, %v", ok, err)
	}
	if _, changed, err := store.Rename(section.ID, "Research", time.Unix(3, 0)); err != nil || changed {
		t.Fatalf("rename noop = %v, %v", changed, err)
	}
	if count, changed, err := store.DeleteSection("missing"); !errorsIsPinSectionNotFound(err) || changed || count != 0 {
		t.Fatalf("delete noop = %d, %v, %v", count, changed, err)
	}
}

func TestPinSectionStoreForeignKeysEnabled(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var enabled int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys = %d", enabled)
	}
}

func TestPinSectionStoreConcurrentEquivalentCreateOrReuseConvergesAcrossConnections(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	storeA := NewPinSectionStore(dbPath)
	storeB := NewPinSectionStore(dbPath)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	firstInserted := make(chan struct{})
	oldHook := pinSectionBeforeSectionInsertHook
	defer func() { pinSectionBeforeSectionInsertHook = oldHook }()
	var enteredCount int32
	pinSectionBeforeSectionInsertHook = func() {
		if atomic.AddInt32(&enteredCount, 1) <= 2 {
			if atomic.LoadInt32(&enteredCount) == 1 {
				close(firstInserted)
			}
			entered <- struct{}{}
			<-release
		}
	}
	oldOpen := storeB.openDB
	storeB.openDB = func(driverName, dataSourceName string) (*sql.DB, error) {
		<-firstInserted
		return oldOpen(driverName, dataSourceName)
	}
	type result struct {
		section PinSection
		changed bool
		err     error
	}
	results := make(chan result, 2)
	go func() {
		section, changed, err := storeA.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1, 0))
		results <- result{section: section, changed: changed, err: err}
	}()
	go func() {
		section, changed, err := storeB.CreateOrReuseAndAssign("research", "session-b", time.Unix(2, 0))
		results <- result{section: section, changed: changed, err: err}
	}()
	<-entered
	<-entered
	close(release)
	first := <-results
	second := <-results
	if first.err != nil {
		t.Fatal(first.err)
	}
	if second.err != nil {
		t.Fatal(second.err)
	}
	if first.section.ID != second.section.ID || !first.changed || !second.changed {
		t.Fatalf("results = %+v %+v", first, second)
	}
	sections, err := storeA.Sections()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || !strings.EqualFold(sections[0].Name, "research") || sections[0].MemberCount != 2 {
		t.Fatalf("sections = %+v", sections)
	}
	pins, err := storeA.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins["session-a"].SectionID != first.section.ID || pins["session-b"].SectionID != first.section.ID {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestPinSectionStoreLastCommittedAssignmentWinsWithoutDuplicates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	storeA := NewPinSectionStore(dbPath)
	storeB := NewPinSectionStore(dbPath)
	firstSection, changed, err := storeA.CreateOrReuseAndAssign("One", "seed-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("seed first = %+v, %v, %v", firstSection, changed, err)
	}
	secondSection, changed, err := storeA.CreateOrReuseAndAssign("Two", "seed-b", time.Unix(2, 0))
	if err != nil || !changed {
		t.Fatalf("seed second = %+v, %v, %v", secondSection, changed, err)
	}
	if ok, err := storeA.Unpin("seed-a"); err != nil || !ok {
		t.Fatalf("unpin seed-a = %v, %v", ok, err)
	}
	if ok, err := storeA.Unpin("seed-b"); err != nil || !ok {
		t.Fatalf("unpin seed-b = %v, %v", ok, err)
	}
	oldHook := pinSectionBeforeAssignmentCommitHook
	defer func() { pinSectionBeforeAssignmentCommitHook = oldHook }()
	releaseFirst := make(chan struct{})
	firstEntered := make(chan struct{})
	var hookCount int32
	pinSectionBeforeAssignmentCommitHook = func() {
		if atomic.AddInt32(&hookCount, 1) == 1 {
			close(firstEntered)
			<-releaseFirst
		}
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, _, err := storeA.Assign(firstSection.ID, "session-x", time.Unix(3, 0))
		firstDone <- err
	}()
	<-firstEntered
	go func() {
		_, _, err := storeB.Assign(secondSection.ID, "session-x", time.Unix(4, 0))
		secondDone <- err
	}()
	close(releaseFirst)
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	pins, err := storeA.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 || pins["session-x"].SectionID != secondSection.ID {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestPinSectionStoreEntropyFailureReturnsError(t *testing.T) {
	oldRead := pinSectionRandRead
	defer func() { pinSectionRandRead = oldRead }()
	pinSectionRandRead = func([]byte) (int, error) { return 0, errors.New("entropy exhausted") }
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	if _, _, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1, 0)); err == nil || !strings.Contains(err.Error(), "entropy exhausted") {
		t.Fatalf("CreateOrReuseAndAssign error = %v", err)
	}
}

func TestPinSectionStoreMigrateLegacy(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	now := time.Unix(10, 0)
	beforeRows := seedLegacyFavoriteRows(t, store)
	changed, err := store.MigrateLegacy([]LegacyPinDecision{
		{StoredID: "valid", Classification: FavoriteDecisionClassification{State: FavoriteDecisionValid, CanonicalKey: ArchiveKey{Kind: "session", ID: "canonical-valid"}}},
		{StoredID: "remote-missing", Classification: FavoriteDecisionClassification{State: FavoriteDecisionDormant}},
		{StoredID: "subagent", Classification: FavoriteDecisionClassification{State: FavoriteDecisionConfirmedInvalid}},
	}, now)
	if err != nil || !changed {
		t.Fatalf("migrate = %v, %v", changed, err)
	}
	sections, err := store.Sections()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || sections[0].Name != "Pinned" {
		t.Fatalf("sections = %+v", sections)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins["canonical-valid"]; !ok {
		t.Fatalf("canonical-valid missing: %+v", pins)
	}
	if _, ok := pins["remote-missing"]; !ok {
		t.Fatalf("remote-missing missing: %+v", pins)
	}
	if _, ok := pins["subagent"]; ok {
		t.Fatalf("subagent should be absent: %+v", pins)
	}
	afterRows, err := favoriteRowsSnapshotForTest(store)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRows, []favoriteRow{{Kind: "project", ID: "project-a", Favorited: 1, DecidedAt: 5}}) {
		t.Fatalf("favorite rows = %+v", afterRows)
	}
	if !reflect.DeepEqual(beforeRows, []favoriteRow{{Kind: "project", ID: "project-a", Favorited: 1, DecidedAt: 5}}) {
		t.Fatalf("seed project rows = %+v", beforeRows)
	}
	changed, err = store.MigrateLegacy([]LegacyPinDecision{{StoredID: "valid", Classification: FavoriteDecisionClassification{State: FavoriteDecisionValid, CanonicalKey: ArchiveKey{Kind: "session", ID: "canonical-valid"}}}}, now.Add(time.Minute))
	if err != nil || changed {
		t.Fatalf("second migrate = %v, %v", changed, err)
	}
	if ok, err := store.Unpin("canonical-valid"); err != nil || !ok {
		t.Fatalf("unpin after migrate = %v, %v", ok, err)
	}
	reopened := NewPinSectionStore(store.dbPath)
	pins, err = reopened.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins["canonical-valid"]; ok {
		t.Fatalf("canonical-valid restored after reopen path: %+v", pins)
	}
}

func TestPinSectionStoreMigrateLegacyNeverPersistsHostQualifiedSessionAlias(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO favorite(kind, id, favorited, decided_at) VALUES('session', 'local:canonical-valid', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	changed, err := store.MigrateLegacy([]LegacyPinDecision{{
		StoredID: "local:canonical-valid",
		Classification: FavoriteDecisionClassification{
			State:        FavoriteDecisionDormant,
			CanonicalKey: ArchiveKey{Kind: "session", ID: "canonical-valid"},
		},
	}}, time.Unix(10, 0))
	if err != nil || !changed {
		t.Fatalf("migrate = %v, %v", changed, err)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins["local:canonical-valid"]; ok {
		t.Fatalf("host-qualified alias persisted as session ID: %+v", pins)
	}
	if _, ok := pins["canonical-valid"]; !ok {
		t.Fatalf("canonical session ID missing: %+v", pins)
	}
}

func TestPinSectionStoreMigrateLegacyPreservesDormantRemoteSessionIdentity(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO favorite(kind, id, favorited, decided_at) VALUES('session', 'codex:remote-thread', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	changed, err := store.MigrateLegacy([]LegacyPinDecision{{
		StoredID:       "codex:remote-thread",
		Classification: FavoriteDecisionClassification{State: FavoriteDecisionDormant},
	}}, time.Unix(10, 0))
	if err != nil || !changed {
		t.Fatalf("migrate = %v, %v", changed, err)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins["codex:remote-thread"]; !ok {
		t.Fatalf("remote session identity was discarded: %+v", pins)
	}
}

func TestPinSectionStoreMigrateLegacyDoesNotExhaustDuringConcurrentFirstUse(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seed := NewPinSectionStore(dbPath)
	seedLegacyFavoriteRows(t, seed)
	store := NewPinSectionStore(dbPath)
	realOpen := store.openDB
	var attempts int32
	store.openDB = func(driverName, dataSourceName string) (*sql.DB, error) {
		if atomic.AddInt32(&attempts, 1) <= 8 {
			return nil, errors.New("SQLITE_BUSY: concurrent first-use migration is committing")
		}
		return realOpen(driverName, dataSourceName)
	}
	oldWait := pinSectionMigrationRetryWait
	defer func() { pinSectionMigrationRetryWait = oldWait }()
	pinSectionMigrationRetryWait = func(time.Duration) {}

	changed, err := store.MigrateLegacy([]LegacyPinDecision{
		{StoredID: "valid", Classification: FavoriteDecisionClassification{State: FavoriteDecisionValid, CanonicalKey: ArchiveKey{Kind: "session", ID: "canonical-valid"}}},
	}, time.Unix(10, 0))
	if err != nil || !changed {
		t.Fatalf("migrate after %d opens = %v, %v", attempts, changed, err)
	}
	if attempts != 9 {
		t.Fatalf("open attempts = %d, want 9", attempts)
	}
}

func TestPinSectionStoreMigrateLegacyConcurrentFirstUseConvergesAcrossConnections(t *testing.T) {
	const contenders = 16
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seed := NewPinSectionStore(dbPath)
	now := time.Unix(10, 0)
	beforeRows := seedLegacyFavoriteRows(t, seed)
	decisions := []LegacyPinDecision{
		{StoredID: "valid", Classification: FavoriteDecisionClassification{State: FavoriteDecisionValid, CanonicalKey: ArchiveKey{Kind: "session", ID: "canonical-valid"}}},
		{StoredID: "remote-missing", Classification: FavoriteDecisionClassification{State: FavoriteDecisionDormant}},
		{StoredID: "subagent", Classification: FavoriteDecisionClassification{State: FavoriteDecisionConfirmedInvalid}},
	}
	type result struct {
		changed bool
		err     error
	}
	results := make(chan result, contenders)
	start := make(chan struct{})
	for range contenders {
		store := NewPinSectionStore(dbPath)
		go func() {
			<-start
			changed, err := store.MigrateLegacy(decisions, now)
			results <- result{changed: changed, err: err}
		}()
	}
	close(start)
	changedCount := 0
	for range contenders {
		got := <-results
		if got.err != nil {
			t.Fatalf("concurrent migrate error = %v", got.err)
		}
		if got.changed {
			changedCount++
		}
	}
	if changedCount != 1 {
		t.Fatalf("concurrent migrate changed count = %d", changedCount)
	}
	if count, err := migrationMarkerCountForTest(seed); err != nil || count != 1 {
		t.Fatalf("migration marker count = %d, %v", count, err)
	}
	sections, err := seed.Sections()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || sections[0].Name != "Pinned" || sections[0].MemberCount != 2 {
		t.Fatalf("sections = %+v", sections)
	}
	pins, err := seed.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins["canonical-valid"]; !ok {
		t.Fatalf("canonical-valid missing: %+v", pins)
	}
	if _, ok := pins["remote-missing"]; !ok {
		t.Fatalf("remote-missing missing: %+v", pins)
	}
	if _, ok := pins["subagent"]; ok {
		t.Fatalf("subagent should be absent: %+v", pins)
	}
	afterRows, err := favoriteRowsSnapshotForTest(seed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRows, []favoriteRow{{Kind: "project", ID: "project-a", Favorited: 1, DecidedAt: 5}}) {
		t.Fatalf("favorite rows = %+v", afterRows)
	}
	if !reflect.DeepEqual(beforeRows, []favoriteRow{{Kind: "project", ID: "project-a", Favorited: 1, DecidedAt: 5}}) {
		t.Fatalf("seed project rows = %+v", beforeRows)
	}
	if ok, err := seed.Unpin("canonical-valid"); err != nil || !ok {
		t.Fatalf("unpin after migrate = %v, %v", ok, err)
	}
	reopened := NewPinSectionStore(dbPath)
	changed, err := reopened.MigrateLegacy(decisions, now.Add(time.Minute))
	if err != nil || changed {
		t.Fatalf("reopened migrate = %v, %v", changed, err)
	}
	pins, err = reopened.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins["canonical-valid"]; ok {
		t.Fatalf("canonical-valid restored after reopen path: %+v", pins)
	}
}

type favoriteRow struct {
	Kind      string
	ID        string
	Favorited int
	DecidedAt int64
}

func seedLegacyFavoriteRows(t *testing.T, store *PinSectionStore) []favoriteRow {
	t.Helper()
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows := []favoriteRow{
		{Kind: "session", ID: "valid", Favorited: 1, DecidedAt: 1},
		{Kind: "session", ID: "remote-missing", Favorited: 1, DecidedAt: 2},
		{Kind: "session", ID: "subagent", Favorited: 1, DecidedAt: 3},
		{Kind: "session", ID: "false-row", Favorited: 0, DecidedAt: 4},
		{Kind: "project", ID: "project-a", Favorited: 1, DecidedAt: 5},
	}
	for _, row := range rows {
		if _, err := db.Exec(`INSERT INTO favorite(kind, id, favorited, decided_at) VALUES(?, ?, ?, ?)`, row.Kind, row.ID, row.Favorited, row.DecidedAt); err != nil { //nolint:noctx // local file DB
			t.Fatal(err)
		}
	}
	return []favoriteRow{{Kind: "project", ID: "project-a", Favorited: 1, DecidedAt: 5}}
}

func favoriteRowsSnapshotForTest(store *PinSectionStore) ([]favoriteRow, error) {
	db, err := store.open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT kind, id, favorited, decided_at FROM favorite ORDER BY kind, id`) //nolint:noctx // local file DB
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]favoriteRow, 0)
	for rows.Next() {
		var row favoriteRow
		if err := rows.Scan(&row.Kind, &row.ID, &row.Favorited, &row.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func migrationMarkerCountForTest(store *PinSectionStore) (int, error) {
	db, err := store.open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hub_schema_migration WHERE name = ?`, "named-pin-sections-v1").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func errorsIsPinSectionNotFound(err error) bool { return errors.Is(err, ErrPinSectionNotFound) }

func errorsIsPinSectionConflict(err error) bool { return errors.Is(err, ErrPinSectionConflict) }
