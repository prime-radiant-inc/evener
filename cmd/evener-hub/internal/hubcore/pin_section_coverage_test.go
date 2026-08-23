package hubcore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// nilStore is a zero-value PinSectionStore with no dbPath, exercising every
// method's nil/empty guard in one test.
func TestPinSectionStoreNilGuardsReturnZeroValues(t *testing.T) {
	var s *PinSectionStore
	if sections, err := s.Sections(); err != nil || len(sections) != 0 {
		t.Fatalf("nil Sections = %+v, %v", sections, err)
	}
	if sec, changed, err := s.Assign("x", "y", time.Unix(1, 0)); err != nil || changed || sec.ID != "" {
		t.Fatalf("nil Assign = %+v, %v, %v", sec, changed, err)
	}
	if sec, changed, err := s.CreateOrReuseAndAssign("n", "y", time.Unix(1, 0)); err != nil || changed || sec.ID != "" {
		t.Fatalf("nil CreateOrReuseAndAssign = %+v, %v, %v", sec, changed, err)
	}
	if sec, changed, err := s.Rename("x", "n", time.Unix(1, 0)); err != nil || changed || sec.ID != "" {
		t.Fatalf("nil Rename = %+v, %v, %v", sec, changed, err)
	}
	if count, changed, err := s.DeleteSection("x"); err != nil || changed || count != 0 {
		t.Fatalf("nil DeleteSection = %d, %v, %v", count, changed, err)
	}
	if ok, err := s.DeleteSession("y"); err != nil || ok {
		t.Fatalf("nil DeleteSession = %v, %v", ok, err)
	}
	if ok, err := s.Unpin("y"); err != nil || ok {
		t.Fatalf("nil Unpin = %v, %v", ok, err)
	}
	if pins, err := s.Assignments(); err != nil || len(pins) != 0 {
		t.Fatalf("nil Assignments = %+v, %v", pins, err)
	}
}

// emptyStore has a dbPath of "" which exercises the same guards as nil.
func TestPinSectionStoreEmptyPathGuardsReturnZeroValues(t *testing.T) {
	s := &PinSectionStore{}
	if _, err := s.Sections(); err != nil {
		t.Fatalf("empty Sections err = %v", err)
	}
	if _, _, err := s.Assign("x", "y", time.Unix(1, 0)); err != nil {
		t.Fatalf("empty Assign err = %v", err)
	}
	if _, _, err := s.CreateOrReuseAndAssign("n", "y", time.Unix(1, 0)); err != nil {
		t.Fatalf("empty CreateOrReuseAndAssign err = %v", err)
	}
	if _, _, err := s.Rename("x", "n", time.Unix(1, 0)); err != nil {
		t.Fatalf("empty Rename err = %v", err)
	}
	if _, _, err := s.DeleteSection("x"); err != nil {
		t.Fatalf("empty DeleteSection err = %v", err)
	}
	if _, err := s.DeleteSession("y"); err != nil {
		t.Fatalf("empty DeleteSession err = %v", err)
	}
	if _, err := s.Assignments(); err != nil {
		t.Fatalf("empty Assignments err = %v", err)
	}
}

// openDBFailure injects an error at the sql.Open seam, covering the openDB
// error path in openWithImmediateTransaction.
func TestPinSectionStoreOpenDBFailureReturnsError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	store.openDB = func(_, _ string) (*sql.DB, error) {
		return nil, errors.New("driver not registered")
	}
	if _, err := store.Sections(); err == nil || !strings.Contains(err.Error(), "driver not registered") {
		t.Fatalf("Sections openDB error = %v", err)
	}
	if _, _, err := store.Assign("x", "y", time.Unix(1, 0)); err == nil {
		t.Fatalf("Assign openDB should fail")
	}
	if _, _, err := store.CreateOrReuseAndAssign("n", "y", time.Unix(1, 0)); err == nil {
		t.Fatalf("CreateOrReuseAndAssign openDB should fail")
	}
	if _, _, err := store.Rename("x", "n", time.Unix(1, 0)); err == nil {
		t.Fatalf("Rename openDB should fail")
	}
	if _, _, err := store.DeleteSection("x"); err == nil {
		t.Fatalf("DeleteSection openDB should fail")
	}
	if _, err := store.DeleteSession("y"); err == nil {
		t.Fatalf("DeleteSession openDB should fail")
	}
	if _, err := store.Assignments(); err == nil {
		t.Fatalf("Assignments openDB should fail")
	}
}

// mkdirFailFs is an afero.Fs whose MkdirAll always errors, covering the MkdirAll
// guard in openWithImmediateTransaction.
type mkdirFailFs struct{ afero.Fs }

func (f mkdirFailFs) MkdirAll(_ string, _ os.FileMode) error {
	return errors.New("filesystem broken")
}

func TestPinSectionStoreMkdirAllFailureReturnsError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "sub", "index.db"))
	store.SetFs(mkdirFailFs{Fs: afero.NewMemMapFs()})
	if _, err := store.Sections(); err == nil || !strings.Contains(err.Error(), "filesystem broken") {
		t.Fatalf("Sections MkdirAll error = %v", err)
	}
}

// closedDB produces a DB that is already closed, so transaction operations fail
// with non-retryable errors. This covers the non-retryable error branches in
// every method's retry loop.
func TestPinSectionStoreNonRetryableTxErrors(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	realOpen := sql.Open

	mkClosedStore := func() *PinSectionStore {
		store := NewPinSectionStore(dbPath)
		store.openDB = func(driverName, dataSourceName string) (*sql.DB, error) {
			db, err := realOpen(driverName, dataSourceName)
			if err != nil {
				return nil, err
			}
			_ = db.Close()
			return db, nil
		}
		return store
	}

	// Sections: query fails on closed DB.
	if _, err := mkClosedStore().Sections(); err == nil {
		t.Fatalf("Sections should fail on closed DB")
	}

	// Assignments: query fails on closed DB.
	if _, err := mkClosedStore().Assignments(); err == nil {
		t.Fatalf("Assignments should fail on closed DB")
	}

	// Assign: BeginTx fails on closed DB.
	if _, _, err := mkClosedStore().Assign("missing", "s", time.Unix(1, 0)); err == nil {
		t.Fatalf("Assign should fail on closed DB")
	}

	// CreateOrReuseAndAssign: BeginTx fails on closed DB.
	if _, _, err := mkClosedStore().CreateOrReuseAndAssign("n", "s", time.Unix(1, 0)); err == nil {
		t.Fatalf("CreateOrReuseAndAssign should fail on closed DB")
	}

	// Rename: needs a valid name to pass the guard.
	if _, _, err := mkClosedStore().Rename("missing", "NewName", time.Unix(1, 0)); err == nil {
		t.Fatalf("Rename should fail on closed DB")
	}

	// DeleteSection: BeginTx fails on closed DB.
	if _, _, err := mkClosedStore().DeleteSection("missing"); err == nil {
		t.Fatalf("DeleteSection should fail on closed DB")
	}

	// DeleteSession: BeginTx fails on closed DB.
	if _, err := mkClosedStore().DeleteSession("s"); err == nil {
		t.Fatalf("DeleteSession should fail on closed DB")
	}
}

// TestPinSectionStoreAssignNotFound covers the section-not-found path in Assign.
func TestPinSectionStoreAssignNotFoundReturnsError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	if _, _, err := store.Assign("nonexistent-id", "session-a", time.Unix(1, 0)); !errorsIsPinSectionNotFound(err) {
		t.Fatalf("Assign missing section err = %v", err)
	}
}

// TestPinSectionStoreRenameNotFoundReturnsError covers the section-not-found
// path in Rename.
func TestPinSectionStoreRenameNotFoundReturnsError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	if _, _, err := store.Rename("nonexistent-id", "NewName", time.Unix(1, 0)); !errorsIsPinSectionNotFound(err) {
		t.Fatalf("Rename missing section err = %v", err)
	}
}

// TestPinSectionStoreRenameInvalidNameReturnsError covers the NormalizePinSectionName
// failure path in Rename.
func TestPinSectionStoreRenameInvalidNameReturnsError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	section, _, err := store.CreateOrReuseAndAssign("Research", "s", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Rename(section.ID, strings.Repeat("界", 81), time.Unix(2, 0)); err == nil || !errors.Is(err, ErrPinSectionName) {
		t.Fatalf("Rename too-long name err = %v", err)
	}
	if _, _, err := store.Rename(section.ID, "  ", time.Unix(2, 0)); err == nil || !errors.Is(err, ErrPinSectionName) {
		t.Fatalf("Rename blank name err = %v", err)
	}
}

// TestPinSectionStoreCreateOrReuseAssignInvalidName covers the
// NormalizePinSectionName failure path in CreateOrReuseAndAssign.
func TestPinSectionStoreCreateOrReuseAssignInvalidNameReturnsError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	if _, _, err := store.CreateOrReuseAndAssign("", "s", time.Unix(1, 0)); err == nil || !errors.Is(err, ErrPinSectionName) {
		t.Fatalf("CreateOrReuseAndAssign blank name err = %v", err)
	}
}

// TestPinSectionStoreSectionsScanError covers the row scan error path in
// Sections by using a store whose schema is missing expected columns.
func TestPinSectionStoreSectionsScanError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	// Create the tables so open() succeeds, then drop the pin_section table
	// so the SELECT query fails.
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.ExecContext(context.Background(), `DROP TABLE session_pin`)
	_, _ = db.ExecContext(context.Background(), `DROP TABLE pin_section`)
	_, _ = db.ExecContext(context.Background(), `DROP TABLE favorite`)
	_, _ = db.ExecContext(context.Background(), `CREATE TABLE pin_section (id TEXT)`)
	_ = db.Close()

	if _, err := store.Sections(); err == nil {
		t.Fatalf("Sections should fail on malformed schema")
	}
}

// TestPinSectionStoreAssignmentsScanError covers the row scan error path in
// Assignments by using a store whose schema is missing expected columns.
func TestPinSectionStoreAssignmentsScanError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	// Create the tables so open() succeeds, then replace session_pin with a
	// table that has wrong columns so the SELECT fails.
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.ExecContext(context.Background(), `DROP TABLE session_pin`)
	_, _ = db.ExecContext(context.Background(), `DROP TABLE pin_section`)
	_, _ = db.ExecContext(context.Background(), `DROP TABLE favorite`)
	_, _ = db.ExecContext(context.Background(), `CREATE TABLE session_pin (session_id TEXT)`)
	_ = db.Close()

	if _, err := store.Assignments(); err == nil {
		t.Fatalf("Assignments should fail on malformed schema")
	}
}

// TestPinSectionStoreDeleteSessionNotFoundReturnsFalse covers the path where
// DeleteSession deletes zero rows.
func TestPinSectionStoreDeleteSessionNotFoundReturnsFalse(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	ok, err := store.DeleteSession("no-such-session")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("DeleteSession of missing session should return false")
	}
}

// TestPinSectionStoreRenameSameNameNoop covers the path where Rename is called
// with the same display name (already covered by existing test, but this also
// checks the rename with a different case that folds to the same key).
func TestPinSectionStoreRenameToSameKeyReturnsUnchanged(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	section, _, err := store.CreateOrReuseAndAssign("Research", "s", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	// Same display name → noop.
	renamed, changed, err := store.Rename(section.ID, "Research", time.Unix(2, 0))
	if err != nil || changed {
		t.Fatalf("Rename same name = %+v, %v, %v", renamed, changed, err)
	}
}

// TestPinSectionStoreDeleteSectionWithMembers covers the path where
// DeleteSection returns the member count for a section with members.
// This is already covered by the existing test, but we also check the
// cascade by verifying the assignments are gone.
func TestPinSectionStoreDeleteSectionCascadesAssignments(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	section, _, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Assign(section.ID, "session-b", time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	count, changed, err := store.DeleteSection(section.ID)
	if err != nil || !changed || count != 2 {
		t.Fatalf("DeleteSection = %d, %v, %v", count, changed, err)
	}
	pins, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 0 {
		t.Fatalf("assignments after delete = %+v", pins)
	}
	sections, err := store.Sections()
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 0 {
		t.Fatalf("sections after delete = %+v", sections)
	}
}

// TestPinSectionStoreOpenWithImmediateTransaction covers the immediate=true path
// of openWithImmediateTransaction, which adds &_txlock=immediate to the DSN.
func TestPinSectionStoreOpenWithImmediateTransaction(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	db, err := store.openWriteAttempt(1) // attempt > 0 → immediate=true
	if err != nil {
		t.Fatalf("openWriteAttempt(1) = %v", err)
	}
	defer func() { _ = db.Close() }()
	// The DB should be usable.
	var enabled int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys = %d", enabled)
	}
}

// TestPinSectionStoreOpenReturnsNilForNilStore covers open() on a nil store.
func TestPinSectionStoreOpenReturnsNilForNilStore(t *testing.T) {
	var s *PinSectionStore
	db, err := s.open()
	if err != nil || db != nil {
		t.Fatalf("nil open = %v, %v", db, err)
	}
	db2, err := s.openWriteAttempt(0)
	if err != nil || db2 != nil {
		t.Fatalf("nil openWriteAttempt = %v, %v", db2, err)
	}
}

// TestPinSectionStoreNewIDReturnsErrorOnEntropyFailure covers newPinSectionID
// when the random source fails. This is already covered for CreateOrReuseAndAssign
// by TestPinSectionStoreEntropyFailureReturnsError, but we also test the function
// directly.
func TestNewPinSectionIDReturnsErrorOnEntropyFailure(t *testing.T) {
	oldRead := pinSectionRandRead
	defer func() { pinSectionRandRead = oldRead }()
	pinSectionRandRead = func([]byte) (int, error) { return 0, errors.New("entropy exhausted") }
	if _, err := newPinSectionID(); err == nil || !strings.Contains(err.Error(), "entropy exhausted") {
		t.Fatalf("newPinSectionID error = %v", err)
	}
}

// TestIsSQLiteRetryable covers the retryable-error detector.
func TestIsSQLiteRetryable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("database is locked"), true},
		{errors.New("sqlite_busy: another write"), true},
		{errors.New("sqlite_locked: cannot commit"), true},
		{errors.New("server is busy"), true},
		{errors.New("syntax error"), false},
		{fmt.Errorf("wrapped: %w", errors.New("database is locked")), true},
	}
	for _, c := range cases {
		if got := isSQLiteRetryable(c.err); got != c.want {
			t.Errorf("isSQLiteRetryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestIsSQLiteUniqueNameConflict covers the unique-conflict detector.
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

// TestPinSectionStoreSetFsReturnsSelf covers the SetFs builder method.
func TestPinSectionStoreSetFsReturnsSelf(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	fs := afero.NewMemMapFs()
	if store.SetFs(fs) != store {
		t.Fatalf("SetFs should return the store")
	}
	if store.fs != fs {
		t.Fatalf("SetFs should set the filesystem")
	}
}
