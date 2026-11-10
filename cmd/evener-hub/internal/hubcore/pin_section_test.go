package hubcore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestNormalizePinSectionName(t *testing.T) {
	tests := []struct {
		raw, display, key string
		wantErr           bool
	}{
		{raw: "  Research  ", display: "Research", key: "research"},
		{raw: "Straße", display: "Straße", key: "strasse"},
		{raw: strings.Repeat("a", PinSectionNameMaxRunes), display: strings.Repeat("a", PinSectionNameMaxRunes), key: strings.Repeat("a", PinSectionNameMaxRunes)},
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
	first, changed, err := store.CreateOrReuseAndAssign("Research", "", "session-a", time.Unix(1, 0))
	if err != nil || !changed || first.MemberCount != 1 {
		t.Fatalf("first = %+v, %v, %v", first, changed, err)
	}
	reused, changed, err := store.CreateOrReuseAndAssign("research", "", "session-b", time.Unix(2, 0))
	if err != nil || !changed || reused.ID != first.ID || reused.Name != "Research" || reused.MemberCount != 2 {
		t.Fatalf("reuse = %+v, %v, %v", reused, changed, err)
	}
	other, _, err := store.CreateOrReuseAndAssign("Client", "", "session-a", time.Unix(3, 0))
	if err != nil || other.MemberCount != 1 {
		t.Fatalf("other = %+v, %v", other, err)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins[SessionPinKey("", "session-a")].SectionID != other.ID {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestPinSectionStoreCreateReusesUnicodeCaseFoldedName(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	first, changed, err := store.CreateOrReuseAndAssign("Straße", "", "session-a", time.Unix(1, 0))
	if err != nil || !changed || first.MemberCount != 1 {
		t.Fatalf("first = %+v, %v, %v", first, changed, err)
	}
	reused, changed, err := store.CreateOrReuseAndAssign("STRASSE", "", "session-b", time.Unix(2, 0))
	if err != nil || !changed || reused.ID != first.ID || reused.Name != "Straße" || reused.MemberCount != 2 {
		t.Fatalf("reuse = %+v, %v, %v", reused, changed, err)
	}
}

func TestPinSectionStoreEquivalentReuseDoesNotRenameOrReassign(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	first, changed, err := store.CreateOrReuseAndAssign("Research", "", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("first = %+v, %v, %v", first, changed, err)
	}
	second, changed, err := store.CreateOrReuseAndAssign("research", "", "session-a", time.Unix(2, 0))
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
	if len(pins) != 1 || pins[SessionPinKey("", "session-a")].AssignedAt != time.Unix(1, 0).UTC() {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestPinSectionStoreSectionsOrderedAndMemberCountDurable(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	alpha, changed, err := store.CreateOrReuseAndAssign("beta", "", "session-a", time.Unix(10, 0))
	if err != nil || !changed {
		t.Fatalf("create beta = %+v, %v, %v", alpha, changed, err)
	}
	_, changed, err = store.CreateOrReuseAndAssign("Alpha", "", "session-b", time.Unix(11, 0))
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
	if _, err := store.Unpin("", "session-a"); err != nil {
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
	section, changed, err := store.CreateOrReuseAndAssign("Research", "", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("create = %+v, %v, %v", section, changed, err)
	}
	renamed, changed, err := store.Rename(section.ID, "research", time.Unix(2, 0))
	if err != nil || !changed || renamed.Name != "research" || renamed.MemberCount != 1 {
		t.Fatalf("rename = %+v, %v, %v", renamed, changed, err)
	}
	other, changed, err := store.CreateOrReuseAndAssign("Client", "", "session-b", time.Unix(3, 0))
	if err != nil || !changed {
		t.Fatalf("create other = %+v, %v, %v", other, changed, err)
	}
	if _, _, err := store.Rename(other.ID, "Research", time.Unix(4, 0)); !errorsIsPinSectionConflict(err) {
		t.Fatalf("rename conflict err = %v", err)
	}
}

func TestPinSectionStoreDeleteSectionReturnsMemberCountAndCascadesAssignments(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	section, changed, err := store.CreateOrReuseAndAssign("Research", "", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("create = %+v, %v, %v", section, changed, err)
	}
	assigned, changed, err := store.Assign(section.ID, "", "session-b", time.Unix(2, 0))
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
	section, changed, err := store.CreateOrReuseAndAssign("Research", "", "session-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("create = %+v, %v, %v", section, changed, err)
	}
	if _, changed, err := store.Assign(section.ID, "", "session-a", time.Unix(2, 0)); err != nil || changed {
		t.Fatalf("assign noop = %v, %v", changed, err)
	}
	if ok, err := store.Unpin("", "session-missing"); err != nil || ok {
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
	var enteredCount atomic.Int32
	pinSectionBeforeSectionInsertHook = func() {
		if enteredCount.Add(1) <= 2 {
			if enteredCount.Load() == 1 {
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
		section, changed, err := storeA.CreateOrReuseAndAssign("Research", "", "session-a", time.Unix(1, 0))
		results <- result{section: section, changed: changed, err: err}
	}()
	go func() {
		section, changed, err := storeB.CreateOrReuseAndAssign("research", "", "session-b", time.Unix(2, 0))
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
	if len(pins) != 2 || pins[SessionPinKey("", "session-a")].SectionID != first.section.ID || pins[SessionPinKey("", "session-b")].SectionID != first.section.ID {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestPinSectionStoreLastCommittedAssignmentWinsWithoutDuplicates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	storeA := NewPinSectionStore(dbPath)
	storeB := NewPinSectionStore(dbPath)
	firstSection, changed, err := storeA.CreateOrReuseAndAssign("One", "", "seed-a", time.Unix(1, 0))
	if err != nil || !changed {
		t.Fatalf("seed first = %+v, %v, %v", firstSection, changed, err)
	}
	secondSection, changed, err := storeA.CreateOrReuseAndAssign("Two", "", "seed-b", time.Unix(2, 0))
	if err != nil || !changed {
		t.Fatalf("seed second = %+v, %v, %v", secondSection, changed, err)
	}
	if ok, err := storeA.Unpin("", "seed-a"); err != nil || !ok {
		t.Fatalf("unpin seed-a = %v, %v", ok, err)
	}
	if ok, err := storeA.Unpin("", "seed-b"); err != nil || !ok {
		t.Fatalf("unpin seed-b = %v, %v", ok, err)
	}
	oldHook := pinSectionBeforeAssignmentCommitHook
	defer func() { pinSectionBeforeAssignmentCommitHook = oldHook }()
	releaseFirst := make(chan struct{})
	firstEntered := make(chan struct{})
	var hookCount atomic.Int32
	pinSectionBeforeAssignmentCommitHook = func() {
		if hookCount.Add(1) == 1 {
			close(firstEntered)
			<-releaseFirst
		}
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, _, err := storeA.Assign(firstSection.ID, "", "session-x", time.Unix(3, 0))
		firstDone <- err
	}()
	<-firstEntered
	go func() {
		_, _, err := storeB.Assign(secondSection.ID, "", "session-x", time.Unix(4, 0))
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
	if len(pins) != 1 || pins[SessionPinKey("", "session-x")].SectionID != secondSection.ID {
		t.Fatalf("pins = %+v", pins)
	}
}

func TestPinSectionStoreEntropyFailureReturnsError(t *testing.T) {
	oldRead := pinSectionRandRead
	defer func() { pinSectionRandRead = oldRead }()
	pinSectionRandRead = func([]byte) (int, error) { return 0, errors.New("entropy exhausted") }
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	if _, _, err := store.CreateOrReuseAndAssign("Research", "", "session-a", time.Unix(1, 0)); err == nil || !strings.Contains(err.Error(), "entropy exhausted") {
		t.Fatalf("CreateOrReuseAndAssign error = %v", err)
	}
}

func errorsIsPinSectionNotFound(err error) bool { return errors.Is(err, ErrPinSectionNotFound) }

func errorsIsPinSectionConflict(err error) bool { return errors.Is(err, ErrPinSectionConflict) }

// A store with no database (nil, or no path) is the unconfigured hub: every
// read is empty and every write is a successful no-op.
func TestPinSectionStoreUnconfiguredIsANoOp(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *PinSectionStore
	}{
		{"nil store", nil},
		{"no database path", &PinSectionStore{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.store
			if db, err := s.open(); err != nil || db != nil {
				t.Fatalf("open = %v, %v", db, err)
			}
			if sections, err := s.Sections(); err != nil || len(sections) != 0 {
				t.Fatalf("Sections = %+v, %v", sections, err)
			}
			if sec, changed, err := s.Assign("x", "", "y", time.Unix(1, 0)); err != nil || changed || sec.ID != "" {
				t.Fatalf("Assign = %+v, %v, %v", sec, changed, err)
			}
			if sec, changed, err := s.CreateOrReuseAndAssign("n", "", "y", time.Unix(1, 0)); err != nil || changed || sec.ID != "" {
				t.Fatalf("CreateOrReuseAndAssign = %+v, %v, %v", sec, changed, err)
			}
			if sec, changed, err := s.Rename("x", "n", time.Unix(1, 0)); err != nil || changed || sec.ID != "" {
				t.Fatalf("Rename = %+v, %v, %v", sec, changed, err)
			}
			if count, changed, err := s.DeleteSection("x"); err != nil || changed || count != 0 {
				t.Fatalf("DeleteSection = %d, %v, %v", count, changed, err)
			}
			if ok, err := s.DeleteSession("", "y"); err != nil || ok {
				t.Fatalf("DeleteSession = %v, %v", ok, err)
			}
			if ok, err := s.Unpin("", "y"); err != nil || ok {
				t.Fatalf("Unpin = %v, %v", ok, err)
			}
			if pins, err := s.Assignments(); err != nil || len(pins) != 0 {
				t.Fatalf("Assignments = %+v, %v", pins, err)
			}
		})
	}
}

// TestPinSectionStoreOpenFailureFailsEveryOperation pins that when the
// database cannot be opened and prepared, every read and write returns that
// failure instead of an empty or partial answer.
func TestPinSectionStoreOpenFailureFailsEveryOperation(t *testing.T) {
	errOpen := errors.New("driver not registered")
	errMkdir := errors.New("filesystem broken")
	for _, tc := range []struct {
		name    string
		prepare func(store *PinSectionStore)
		wantErr error
	}{
		{name: "driver open fails", wantErr: errOpen, prepare: func(store *PinSectionStore) {
			store.openDB = func(_, _ string) (*sql.DB, error) { return nil, errOpen }
		}},
		{name: "database is closed", prepare: func(store *PinSectionStore) {
			store.openDB = func(driverName, dataSourceName string) (*sql.DB, error) {
				db, err := sql.Open(driverName, dataSourceName)
				if err != nil {
					return nil, err
				}
				_ = db.Close()
				return db, nil
			}
		}},
		{name: "database directory cannot be created", wantErr: errMkdir, prepare: func(store *PinSectionStore) {
			store.SetFs(failingFs{Fs: afero.NewMemMapFs(), mkdirErr: errMkdir})
		}},
	} {
		for _, op := range []struct {
			name string
			run  func(store *PinSectionStore) error
		}{
			{"sections", func(s *PinSectionStore) error { _, err := s.Sections(); return err }},
			{"assignments", func(s *PinSectionStore) error { _, err := s.Assignments(); return err }},
			{"assign", func(s *PinSectionStore) error { _, _, err := s.Assign("x", "", "y", time.Unix(1, 0)); return err }},
			{"create or reuse and assign", func(s *PinSectionStore) error {
				_, _, err := s.CreateOrReuseAndAssign("n", "", "y", time.Unix(1, 0))
				return err
			}},
			{"rename", func(s *PinSectionStore) error { _, _, err := s.Rename("x", "NewName", time.Unix(1, 0)); return err }},
			{"delete section", func(s *PinSectionStore) error { _, _, err := s.DeleteSection("x"); return err }},
			{"delete session", func(s *PinSectionStore) error { _, err := s.DeleteSession("", "y"); return err }},
		} {
			t.Run(tc.name+"/"+op.name, func(t *testing.T) {
				store := NewPinSectionStore(filepath.Join(t.TempDir(), "sub", "index.db"))
				tc.prepare(store)
				err := op.run(store)
				if err == nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Fatalf("%s = %v, want the open failure %v", op.name, err, tc.wantErr)
				}
			})
		}
	}
}

func TestPinSectionStoreMissingSectionIsNotFound(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	if _, _, err := store.Assign("nonexistent-id", "", "session-a", time.Unix(1, 0)); !errorsIsPinSectionNotFound(err) {
		t.Fatalf("Assign missing section err = %v", err)
	}
	if _, _, err := store.Rename("nonexistent-id", "NewName", time.Unix(1, 0)); !errorsIsPinSectionNotFound(err) {
		t.Fatalf("Rename missing section err = %v", err)
	}
}

func TestPinSectionStoreRejectsAnInvalidName(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	if _, _, err := store.CreateOrReuseAndAssign("", "", "s", time.Unix(1, 0)); !errors.Is(err, ErrPinSectionName) {
		t.Fatalf("CreateOrReuseAndAssign blank name err = %v", err)
	}
	section, _, err := store.CreateOrReuseAndAssign("Research", "", "s", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Rename(section.ID, strings.Repeat("界", 81), time.Unix(2, 0)); !errors.Is(err, ErrPinSectionName) {
		t.Fatalf("Rename too-long name err = %v", err)
	}
	if _, _, err := store.Rename(section.ID, "  ", time.Unix(2, 0)); !errors.Is(err, ErrPinSectionName) {
		t.Fatalf("Rename blank name err = %v", err)
	}
}

// A database whose pin tables do not have the store's columns fails reads
// rather than returning rows it could not decode.
func TestPinSectionStoreReadsFailOnAMalformedSchema(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table string
		read  func(store *PinSectionStore) error
	}{
		{"sections", `CREATE TABLE pin_section (id TEXT)`, func(s *PinSectionStore) error { _, err := s.Sections(); return err }},
		{"assignments", `CREATE TABLE session_pin (session_id TEXT)`, func(s *PinSectionStore) error { _, err := s.Assignments(); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
			db, err := store.open()
			if err != nil {
				t.Fatal(err)
			}
			for _, stmt := range []string{`DROP TABLE session_pin`, `DROP TABLE pin_section`, `DROP TABLE favorite`, tc.table} {
				if _, err := db.ExecContext(context.Background(), stmt); err != nil {
					t.Fatal(err)
				}
			}
			_ = db.Close()
			if err := tc.read(store); err == nil {
				t.Fatalf("%s read succeeded on a malformed schema", tc.name)
			}
		})
	}
}

func TestIsSQLiteUniqueNameConflict(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("UNIQUE constraint failed: pin_section.name_key"), true},
		{errors.New("UNIQUE constraint failed: other_table.col"), false},
		{errors.New("syntax error"), false},
	}
	for _, c := range cases {
		if got := isSQLiteUniqueNameConflict(c.err); got != c.want {
			t.Errorf("isSQLiteUniqueNameConflict(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// CreateOrReuseAndAssign exposes the same pre-commit seam as Assign, so a race
// test can park either write at its commit boundary.
func TestPinSectionStoreCreateOrReuseHookFires(t *testing.T) {
	oldHook := pinSectionBeforeAssignmentCommitHook
	defer func() { pinSectionBeforeAssignmentCommitHook = oldHook }()
	fired := false
	pinSectionBeforeAssignmentCommitHook = func() { fired = true }
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	if _, _, err := store.CreateOrReuseAndAssign("Research", "", "s1", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatalf("pinSectionBeforeAssignmentCommitHook should have fired during CreateOrReuseAndAssign")
	}
}
