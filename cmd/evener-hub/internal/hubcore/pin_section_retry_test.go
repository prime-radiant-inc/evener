package hubcore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lockedDriver wraps the real "sqlite" driver and injects "database is locked"
// errors into specific operations inside transactions for a configurable
// number of calls, then delegates to the real driver. This exercises the
// retryable-error continue branches in every PinSectionStore method's retry
// loop.
//
// Failures are only injected AFTER a transaction begins, so the PRAGMA setup
// in openWithImmediateTransaction is not affected.

var (
	lockedDriverName    = "sqlite-locked-test"
	lockedDriverOnce    sync.Once
	lockedBeginFailures atomic.Int64 // number of Begin calls to fail
	lockedTxExecFails   atomic.Int64 // number of Exec calls INSIDE a tx to fail
	lockedTxQueryFails  atomic.Int64 // number of Query calls INSIDE a tx to fail
	lockedTxCommitFails atomic.Int64 // number of Commit calls to fail
	lockedTxActive      atomic.Bool  // true while a tx is active on the conn
)

type lockedDriver struct {
	real driver.Driver
}

type lockedConn struct {
	real driver.Conn
}

type lockedTx struct {
	real driver.Tx
}

func initLockedDriver() {
	lockedDriverOnce.Do(func() {
		var realDrv driver.Driver
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			panic(fmt.Sprintf("lockedDriver: cannot open sqlite: %v", err))
		}
		realDrv = db.Driver()
		_ = db.Close()
		sql.Register(lockedDriverName, &lockedDriver{real: realDrv})
	})
}

func (d *lockedDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.real.Open(name)
	if err != nil {
		return nil, err
	}
	return &lockedConn{real: conn}, nil
}

func (c *lockedConn) Prepare(query string) (driver.Stmt, error) {
	return c.real.Prepare(query)
}

func (c *lockedConn) Close() error { return c.real.Close() }

func (c *lockedConn) Begin() (driver.Tx, error) {
	if n := lockedBeginFailures.Add(-1); n >= 0 {
		return nil, errors.New("database is locked")
	}
	lockedBeginFailures.Add(1)
	tx, err := c.real.Begin() //nolint:staticcheck // deprecated driver.Conn.Begin used for test coverage
	if err != nil {
		return nil, err
	}
	lockedTxActive.Store(true)
	return &lockedTx{real: tx}, nil
}

func (c *lockedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginTx, ok := c.real.(driver.ConnBeginTx); ok {
		if n := lockedBeginFailures.Add(-1); n >= 0 {
			return nil, errors.New("database is locked")
		}
		lockedBeginFailures.Add(1)
		tx, err := beginTx.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		lockedTxActive.Store(true)
		return &lockedTx{real: tx}, nil
	}
	return c.Begin()
}

func (c *lockedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if lockedTxActive.Load() {
		if n := lockedTxExecFails.Add(-1); n >= 0 {
			return nil, errors.New("database is locked")
		}
		lockedTxExecFails.Add(1)
	}
	if execerCtx, ok := c.real.(driver.ExecerContext); ok {
		return execerCtx.ExecContext(ctx, query, args)
	}
	if execer, ok := c.real.(driver.Execer); ok { //nolint:staticcheck // deprecated driver.Execer used for test coverage
		dargs := make([]driver.Value, len(args))
		for i, a := range args {
			dargs[i] = a.Value
		}
		return execer.Exec(query, dargs)
	}
	return nil, driver.ErrSkip
}

func (c *lockedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if lockedTxActive.Load() {
		if n := lockedTxQueryFails.Add(-1); n >= 0 {
			return nil, errors.New("database is locked")
		}
		lockedTxQueryFails.Add(1)
	}
	if queryerCtx, ok := c.real.(driver.QueryerContext); ok {
		return queryerCtx.QueryContext(ctx, query, args)
	}
	if queryer, ok := c.real.(driver.Queryer); ok { //nolint:staticcheck // deprecated driver.Queryer used for test coverage
		dargs := make([]driver.Value, len(args))
		for i, a := range args {
			dargs[i] = a.Value
		}
		return queryer.Query(query, dargs)
	}
	return nil, driver.ErrSkip
}

func (t *lockedTx) Commit() error {
	lockedTxActive.Store(false)
	if n := lockedTxCommitFails.Add(-1); n >= 0 {
		return errors.New("database is locked")
	}
	lockedTxCommitFails.Add(1)
	return t.real.Commit()
}

func (t *lockedTx) Rollback() error {
	lockedTxActive.Store(false)
	return t.real.Rollback()
}

// setupLockedStore creates a PinSectionStore whose openDB uses the locked driver.
// The real SQLite DB is created first (with proper schema) so the locked driver
// can delegate to it after injecting failures.
func setupLockedStore(t *testing.T) *PinSectionStore {
	t.Helper()
	initLockedDriver()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seed := NewPinSectionStore(dbPath)
	if _, _, err := seed.CreateOrReuseAndAssign("Seed", "seed-session", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	store := NewPinSectionStore(dbPath)
	store.openDB = func(_, dataSourceName string) (*sql.DB, error) {
		return sql.Open(lockedDriverName, dataSourceName)
	}
	return store
}

func resetLockedCounters() {
	lockedBeginFailures.Store(0)
	lockedTxExecFails.Store(0)
	lockedTxQueryFails.Store(0)
	lockedTxCommitFails.Store(0)
	lockedTxActive.Store(false)
}

// TestPinSectionStoreAssignBeginTxRetryable exercises the BeginTx retryable
// continue branch in Assign.
func TestPinSectionStoreAssignBeginTxRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("Research", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedBeginFailures.Store(1)
	_, _, err = store.Assign(section.ID, "session-x", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("Assign with retryable BeginTx should succeed after retry: %v", err)
	}
}

// TestPinSectionStoreAssignQueryRetryable exercises the sectionByIDTx retryable
// continue branch in Assign.
func TestPinSectionStoreAssignQueryRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("Research", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxQueryFails.Store(1)
	_, _, err = store.Assign(section.ID, "session-x", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("Assign with retryable query should succeed after retry: %v", err)
	}
}

// TestPinSectionStoreAssignExecRetryable exercises the upsertSessionPinTx
// retryable continue branch in Assign.
func TestPinSectionStoreAssignExecRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("Research", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxExecFails.Store(1)
	_, _, err = store.Assign(section.ID, "session-x", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("Assign with retryable exec should succeed after retry: %v", err)
	}
}

// TestPinSectionStoreAssignCommitRetryable exercises the Commit retryable
// continue branch in Assign.
func TestPinSectionStoreAssignCommitRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("Research", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxCommitFails.Store(1)
	_, _, err = store.Assign(section.ID, "session-x", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("Assign with retryable commit should succeed after retry: %v", err)
	}
}

// TestPinSectionStoreCreateOrReuseBeginTxRetryable exercises the BeginTx
// retryable continue branch in CreateOrReuseAndAssign.
func TestPinSectionStoreCreateOrReuseBeginTxRetryable(t *testing.T) {
	store := setupLockedStore(t)
	resetLockedCounters()
	lockedBeginFailures.Store(1)
	_, _, err := store.CreateOrReuseAndAssign("NewSection", "session-x", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("CreateOrReuseAndAssign with retryable BeginTx should succeed: %v", err)
	}
}

// TestPinSectionStoreCreateOrReuseExecRetryable exercises the upsertSessionPinTx
// retryable continue branch in CreateOrReuseAndAssign.
func TestPinSectionStoreCreateOrReuseExecRetryable(t *testing.T) {
	store := setupLockedStore(t)
	resetLockedCounters()
	lockedTxExecFails.Store(1)
	_, _, err := store.CreateOrReuseAndAssign("NewSection2", "session-x", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("CreateOrReuseAndAssign with retryable exec should succeed: %v", err)
	}
}

// TestPinSectionStoreCreateOrReuseQueryRetryable exercises the ensureSectionTx
// retryable continue branch in CreateOrReuseAndAssign.
func TestPinSectionStoreCreateOrReuseQueryRetryable(t *testing.T) {
	store := setupLockedStore(t)
	resetLockedCounters()
	lockedTxQueryFails.Store(1)
	_, _, err := store.CreateOrReuseAndAssign("NewSection3", "session-x", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("CreateOrReuseAndAssign with retryable query should succeed: %v", err)
	}
}

// TestPinSectionStoreCreateOrReuseCommitRetryable exercises the Commit
// retryable continue branch in CreateOrReuseAndAssign.
func TestPinSectionStoreCreateOrReuseCommitRetryable(t *testing.T) {
	store := setupLockedStore(t)
	resetLockedCounters()
	lockedTxCommitFails.Store(1)
	_, _, err := store.CreateOrReuseAndAssign("NewSection4", "session-x", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("CreateOrReuseAndAssign with retryable commit should succeed: %v", err)
	}
}

// TestPinSectionStoreRenameBeginTxRetryable exercises the BeginTx retryable
// continue branch in Rename.
func TestPinSectionStoreRenameBeginTxRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("OldName", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedBeginFailures.Store(1)
	_, _, err = store.Rename(section.ID, "NewName", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("Rename with retryable BeginTx should succeed: %v", err)
	}
}

// TestPinSectionStoreRenameQueryRetryable exercises the sectionByIDTx retryable
// continue branch in Rename.
func TestPinSectionStoreRenameQueryRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("OldName2", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxQueryFails.Store(1)
	_, _, err = store.Rename(section.ID, "NewName2", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("Rename with retryable query should succeed: %v", err)
	}
}

// TestPinSectionStoreRenameExecRetryable exercises the UPDATE exec retryable
// continue branch in Rename.
func TestPinSectionStoreRenameExecRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("RenameMe", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxExecFails.Store(1)
	_, _, err = store.Rename(section.ID, "RenamedMe", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("Rename with retryable exec should succeed: %v", err)
	}
}

// TestPinSectionStoreRenameCommitRetryable exercises the Commit retryable
// continue branch in Rename.
func TestPinSectionStoreRenameCommitRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("CommitMe", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxCommitFails.Store(1)
	_, _, err = store.Rename(section.ID, "CommittedMe", time.Unix(2, 0))
	if err != nil {
		t.Fatalf("Rename with retryable commit should succeed: %v", err)
	}
}

// TestPinSectionStoreDeleteSectionBeginTxRetryable exercises the BeginTx
// retryable continue branch in DeleteSection.
func TestPinSectionStoreDeleteSectionBeginTxRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("ToDelete", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedBeginFailures.Store(1)
	_, _, err = store.DeleteSection(section.ID)
	if err != nil {
		t.Fatalf("DeleteSection with retryable BeginTx should succeed: %v", err)
	}
}

// TestPinSectionStoreDeleteSectionQueryRetryable exercises the sectionByIDTx
// retryable continue branch in DeleteSection.
func TestPinSectionStoreDeleteSectionQueryRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("ToDelete2", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxQueryFails.Store(1)
	_, _, err = store.DeleteSection(section.ID)
	if err != nil {
		t.Fatalf("DeleteSection with retryable query should succeed: %v", err)
	}
}

// TestPinSectionStoreDeleteSectionExecRetryable exercises the DELETE exec
// retryable continue branch in DeleteSection.
func TestPinSectionStoreDeleteSectionExecRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("ToDelete3", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxExecFails.Store(1)
	_, _, err = store.DeleteSection(section.ID)
	if err != nil {
		t.Fatalf("DeleteSection with retryable exec should succeed: %v", err)
	}
}

// TestPinSectionStoreDeleteSectionCommitRetryable exercises the Commit retryable
// continue branch in DeleteSection.
func TestPinSectionStoreDeleteSectionCommitRetryable(t *testing.T) {
	store := setupLockedStore(t)
	section, _, err := store.CreateOrReuseAndAssign("ToDelete4", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxCommitFails.Store(1)
	_, _, err = store.DeleteSection(section.ID)
	if err != nil {
		t.Fatalf("DeleteSection with retryable commit should succeed: %v", err)
	}
}

// TestPinSectionStoreDeleteSessionBeginTxRetryable exercises the BeginTx
// retryable continue branch in DeleteSession.
func TestPinSectionStoreDeleteSessionBeginTxRetryable(t *testing.T) {
	store := setupLockedStore(t)
	_, _, err := store.CreateOrReuseAndAssign("Section", "session-x", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedBeginFailures.Store(1)
	_, err = store.DeleteSession("session-x")
	if err != nil {
		t.Fatalf("DeleteSession with retryable BeginTx should succeed: %v", err)
	}
}

// TestPinSectionStoreDeleteSessionExecRetryable exercises the DELETE exec
// retryable continue branch in DeleteSession.
func TestPinSectionStoreDeleteSessionExecRetryable(t *testing.T) {
	store := setupLockedStore(t)
	_, _, err := store.CreateOrReuseAndAssign("Section", "session-y", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxExecFails.Store(1)
	_, err = store.DeleteSession("session-y")
	if err != nil {
		t.Fatalf("DeleteSession with retryable exec should succeed: %v", err)
	}
}

// TestPinSectionStoreDeleteSessionCommitRetryable exercises the Commit retryable
// continue branch in DeleteSession.
func TestPinSectionStoreDeleteSessionCommitRetryable(t *testing.T) {
	store := setupLockedStore(t)
	_, _, err := store.CreateOrReuseAndAssign("Section", "session-z", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetLockedCounters()
	lockedTxCommitFails.Store(1)
	_, err = store.DeleteSession("session-z")
	if err != nil {
		t.Fatalf("DeleteSession with retryable commit should succeed: %v", err)
	}
}

// TestPinSectionStoreRetryLimitReached exercises the retry-limit-reached error
// path by injecting persistent "database is locked" errors that never succeed.
func TestPinSectionStoreRetryLimitReached(t *testing.T) {
	store := setupLockedStore(t)
	resetLockedCounters()
	lockedBeginFailures.Store(100)
	_, _, err := store.Assign("x", "y", time.Unix(1, 0))
	if err == nil || !strings.Contains(err.Error(), "retry limit reached") {
		t.Fatalf("Assign retry limit err = %v, want retry limit reached", err)
	}

	resetLockedCounters()
	lockedBeginFailures.Store(100)
	_, _, err = store.CreateOrReuseAndAssign("Z", "y", time.Unix(1, 0))
	if err == nil || !strings.Contains(err.Error(), "retry limit reached") {
		t.Fatalf("CreateOrReuseAndAssign retry limit err = %v", err)
	}

	resetLockedCounters()
	lockedBeginFailures.Store(100)
	_, _, err = store.Rename("x", "n", time.Unix(1, 0))
	if err == nil || !strings.Contains(err.Error(), "retry limit reached") {
		t.Fatalf("Rename retry limit err = %v", err)
	}

	resetLockedCounters()
	lockedBeginFailures.Store(100)
	_, _, err = store.DeleteSection("x")
	if err == nil || !strings.Contains(err.Error(), "retry limit reached") {
		t.Fatalf("DeleteSection retry limit err = %v", err)
	}

	resetLockedCounters()
	lockedBeginFailures.Store(100)
	_, err = store.DeleteSession("y")
	if err == nil || !strings.Contains(err.Error(), "retry limit reached") {
		t.Fatalf("DeleteSession retry limit err = %v", err)
	}
}

// TestPinSectionStoreRenameConflictError covers the conflict error path in
// Rename where another section already has the same key.
func TestPinSectionStoreRenameConflictError(t *testing.T) {
	store := NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	section1, _, err := store.CreateOrReuseAndAssign("First", "s1", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.CreateOrReuseAndAssign("Second", "s2", time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Rename(section1.ID, "Second", time.Unix(3, 0))
	if err == nil || !errors.Is(err, ErrPinSectionConflict) {
		t.Fatalf("Rename to conflicting name err = %v, want ErrPinSectionConflict", err)
	}
}
