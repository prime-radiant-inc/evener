package hubcore

import (
	"path/filepath"
	"strings"
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
	if err != nil || !changed {
		t.Fatalf("first = %+v, %v, %v", first, changed, err)
	}
	reused, changed, err := store.CreateOrReuseAndAssign("research", "session-b", time.Unix(2, 0))
	if err != nil || !changed || reused.ID != first.ID {
		t.Fatalf("reuse = %+v, %v, %v", reused, changed, err)
	}
	other, _, err := store.CreateOrReuseAndAssign("Client", "session-a", time.Unix(3, 0))
	if err != nil {
		t.Fatal(err)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins["session-a"].SectionID != other.ID {
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
	if err != nil || !changed || renamed.Name != "research" {
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
	_, changed, err = store.Assign(section.ID, "session-b", time.Unix(2, 0))
	if err != nil || !changed {
		t.Fatalf("assign = %v, %v", changed, err)
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

func TestPinSectionStoreConcurrentEquivalentCreateOrReuseConverges(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	section, changed, err := store.CreateOrReuseAndAssign("Pinned", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("seed = %+v, %v, %v", section, changed, err)
	}
	results := make(chan PinSection, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			got, _, err := store.CreateOrReuseAndAssign("pinned", "session-x", time.Unix(int64(2+i), 0))
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}(i)
	}
	for i := 0; i < 8; i++ {
		select {
		case err := <-errs:
			t.Fatal(err)
		case got := <-results:
			if got.ID != section.ID {
				t.Fatalf("concurrent section = %+v, want %s", got, section.ID)
			}
		}
	}
}

func TestPinSectionStoreLastCommittedAssignmentWinsWithoutDuplicates(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	first, changed, err := store.CreateOrReuseAndAssign("One", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("first = %+v, %v, %v", first, changed, err)
	}
	second, changed, err := store.CreateOrReuseAndAssign("Two", "session-a", time.Unix(2, 0))
	if err != nil || !changed {
		t.Fatalf("second = %+v, %v, %v", second, changed, err)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 || pins["session-a"].SectionID != second.ID {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestPinSectionStoreMigrateLegacy(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	now := time.Unix(10, 0)
	seedLegacyFavoriteRows(t, store)
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
	favs, err := favoriteRowsForTest(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(favs) != 1 || favs[ArchiveKey{Kind: "project", ID: "project-a"}] != true {
		t.Fatalf("favorite rows = %+v", favs)
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

func seedLegacyFavoriteRows(t *testing.T, store *PinSectionStore) {
	t.Helper()
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	stmts := []string{
		`INSERT INTO favorite(kind, id, favorited, decided_at) VALUES('session', 'valid', 1, 1)`,
		`INSERT INTO favorite(kind, id, favorited, decided_at) VALUES('session', 'remote-missing', 1, 1)`,
		`INSERT INTO favorite(kind, id, favorited, decided_at) VALUES('session', 'subagent', 1, 1)`,
		`INSERT INTO favorite(kind, id, favorited, decided_at) VALUES('session', 'false-row', 0, 1)`,
		`INSERT INTO favorite(kind, id, favorited, decided_at) VALUES('project', 'project-a', 1, 1)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func favoriteRowsForTest(store *PinSectionStore) (map[ArchiveKey]bool, error) {
	db, err := store.open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT kind, id, favorited FROM favorite`) //nolint:noctx // local file DB
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[ArchiveKey]bool)
	for rows.Next() {
		var kind, id string
		var favorited int
		if err := rows.Scan(&kind, &id, &favorited); err != nil {
			return nil, err
		}
		out[ArchiveKey{Kind: kind, ID: id}] = favorited == 1
	}
	return out, rows.Err()
}

func errorsIsPinSectionNotFound(err error) bool { return err == ErrPinSectionNotFound }

func errorsIsPinSectionConflict(err error) bool { return err == ErrPinSectionConflict }
