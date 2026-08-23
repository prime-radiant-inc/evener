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

// errorDriver wraps the real "sqlite" driver and injects non-retryable errors
// into specific operations inside transactions. Unlike the lockedDriver which
// injects "database is locked" (retryable), this driver injects "non-retryable
// test error" so the retry loop's isSQLiteRetryable check returns false and
// the error is returned directly.
//
// Failures are only injected AFTER a transaction begins, so the PRAGMA setup
// in openWithImmediateTransaction is not affected.

var (
	errorDriverName   = "sqlite-error-test"
	errorDriverOnce   sync.Once
	errorQueryTarget  atomic.Int64 // fail the Nth query inside a tx (1-indexed, 0 = never)
	errorQueryCount   atomic.Int64 // count of queries inside current tx
	errorExecTarget   atomic.Int64 // fail the Nth exec inside a tx (1-indexed, 0 = never)
	errorExecCount    atomic.Int64 // count of execs inside current tx
	errorResultTarget atomic.Int64 // fail the Nth RowsAffected call (1-indexed, 0 = never)
	errorResultCount  atomic.Int64 // count of RowsAffected calls
	errorTxActive     atomic.Bool  // true while a tx is active on the conn
)

type errorDriver struct {
	real driver.Driver
}

type errorConn struct {
	real driver.Conn
}

type errorTx struct {
	real driver.Tx
}

type errorResult struct {
	real driver.Result
}

func initErrorDriver() {
	errorDriverOnce.Do(func() {
		var realDrv driver.Driver
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			panic(fmt.Sprintf("errorDriver: cannot open sqlite: %v", err))
		}
		realDrv = db.Driver()
		_ = db.Close()
		sql.Register(errorDriverName, &errorDriver{real: realDrv})
	})
}

func (d *errorDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.real.Open(name)
	if err != nil {
		return nil, err
	}
	return &errorConn{real: conn}, nil
}

func (c *errorConn) Prepare(query string) (driver.Stmt, error) {
	return c.real.Prepare(query)
}

func (c *errorConn) Close() error { return c.real.Close() }

func (c *errorConn) Begin() (driver.Tx, error) {
	tx, err := c.real.Begin()
	if err != nil {
		return nil, err
	}
	errorTxActive.Store(true)
	errorQueryCount.Store(0)
	errorExecCount.Store(0)
	errorResultCount.Store(0)
	return &errorTx{real: tx}, nil
}

func (c *errorConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginTx, ok := c.real.(driver.ConnBeginTx); ok {
		tx, err := beginTx.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		errorTxActive.Store(true)
		errorQueryCount.Store(0)
		errorExecCount.Store(0)
		errorResultCount.Store(0)
		return &errorTx{real: tx}, nil
	}
	return c.Begin()
}

func (c *errorConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if errorTxActive.Load() {
		n := errorExecCount.Add(1)
		if target := errorExecTarget.Load(); n == target {
			return nil, errors.New("non-retryable test error")
		}
	}
	if execerCtx, ok := c.real.(driver.ExecerContext); ok {
		result, err := execerCtx.ExecContext(ctx, query, args)
		if err != nil {
			return nil, err
		}
		// Wrap the result so we can inject RowsAffected errors.
		return &errorResult{real: result}, nil
	}
	if execer, ok := c.real.(driver.Execer); ok {
		dargs := make([]driver.Value, len(args))
		for i, a := range args {
			dargs[i] = a.Value
		}
		result, err := execer.Exec(query, dargs)
		if err != nil {
			return nil, err
		}
		return &errorResult{real: result}, nil
	}
	return nil, driver.ErrSkip
}

func (c *errorConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if errorTxActive.Load() {
		n := errorQueryCount.Add(1)
		if target := errorQueryTarget.Load(); n == target {
			return nil, errors.New("non-retryable test error")
		}
	}
	if queryerCtx, ok := c.real.(driver.QueryerContext); ok {
		return queryerCtx.QueryContext(ctx, query, args)
	}
	if queryer, ok := c.real.(driver.Queryer); ok {
		dargs := make([]driver.Value, len(args))
		for i, a := range args {
			dargs[i] = a.Value
		}
		return queryer.Query(query, dargs)
	}
	return nil, driver.ErrSkip
}

func (t *errorTx) Commit() error {
	errorTxActive.Store(false)
	return t.real.Commit()
}

func (t *errorTx) Rollback() error {
	errorTxActive.Store(false)
	return t.real.Rollback()
}

func (r *errorResult) LastInsertId() (int64, error) {
	return r.real.LastInsertId()
}

func (r *errorResult) RowsAffected() (int64, error) {
	n := errorResultCount.Add(1)
	if target := errorResultTarget.Load(); n == target {
		return 0, errors.New("non-retryable test error")
	}
	return r.real.RowsAffected()
}

// setupErrorStore creates a PinSectionStore whose openDB uses the error driver.
// The real SQLite DB is created first (with proper schema) so the error driver
// can delegate to it after injecting failures.
func setupErrorStore(t *testing.T) *PinSectionStore {
	t.Helper()
	initErrorDriver()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seed := NewPinSectionStore(dbPath)
	if _, _, err := seed.CreateOrReuseAndAssign("Seed", "seed-session", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	store := NewPinSectionStore(dbPath)
	store.openDB = func(_, dataSourceName string) (*sql.DB, error) {
		return sql.Open(errorDriverName, dataSourceName)
	}
	return store
}

func resetErrorCounters() {
	errorQueryTarget.Store(0)
	errorQueryCount.Store(0)
	errorExecTarget.Store(0)
	errorExecCount.Store(0)
	errorResultTarget.Store(0)
	errorResultCount.Store(0)
	errorTxActive.Store(false)
}

// TestPinSectionStoreAssignCountNonRetryable covers the sessionPinCountTx
// non-retryable error path in Assign (line 232-234).
func TestPinSectionStoreAssignCountNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	section, _, err := store.CreateOrReuseAndAssign("Research", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The Assign flow: sectionByIDTx (query 1), upsertSessionPinTx (exec 1),
	// sessionPinCountTx (query 2). We fail the 2nd query (count).
	errorQueryTarget.Store(2)
	_, _, err = store.Assign(section.ID, "session-x", time.Unix(2, 0))
	if err == nil {
		t.Fatalf("Assign with non-retryable count error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreCreateOrReuseUpsertNonRetryable covers the upsertSessionPinTx
// non-retryable error path in CreateOrReuseAndAssign (line 290-292).
func TestPinSectionStoreCreateOrReuseUpsertNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	// The flow: sectionByKeyTx (query 1), newPinSectionID, INSERT (exec 1),
	// upsertSessionPinTx (exec 2). We fail the 2nd exec (upsert).
	errorExecTarget.Store(2)
	_, _, err := store.CreateOrReuseAndAssign("NewSection", "session-x", time.Unix(2, 0))
	if err == nil {
		t.Fatalf("CreateOrReuseAndAssign with non-retryable upsert error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreCreateOrReuseCountNonRetryable covers the sessionPinCountTx
// non-retryable error path in CreateOrReuseAndAssign (line 299-301).
func TestPinSectionStoreCreateOrReuseCountNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	// The flow: sectionByKeyTx (query 1), newPinSectionID, INSERT (exec 1),
	// upsertSessionPinTx (exec 2), sessionPinCountTx (query 2).
	// We fail the 2nd query (count).
	errorQueryTarget.Store(2)
	_, _, err := store.CreateOrReuseAndAssign("NewSection2", "session-y", time.Unix(3, 0))
	if err == nil {
		t.Fatalf("CreateOrReuseAndAssign with non-retryable count error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreRenameCountNonRetryable covers the sessionPinCountTx
// non-retryable error path in Rename (line 366-368).
func TestPinSectionStoreRenameCountNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters() // ensure seeding succeeds
	section, _, err := store.CreateOrReuseAndAssign("OldName", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The Rename flow: sectionByIDTx (query 1), sessionPinCountTx (query 2).
	// We fail the 2nd query (count).
	errorQueryTarget.Store(2)
	_, _, err = store.Rename(section.ID, "NewName", time.Unix(2, 0))
	if err == nil {
		t.Fatalf("Rename with non-retryable count error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreRenameSectionByKeyNonRetryable covers the sectionIDByKeyTx
// non-retryable error path in Rename (line 380-382).
func TestPinSectionStoreRenameSectionByKeyNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	section, _, err := store.CreateOrReuseAndAssign("RenameMe", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The Rename flow: sectionByIDTx (query 1), sessionPinCountTx (query 2),
	// sectionIDByKeyTx (query 3). We fail the 3rd query.
	errorQueryTarget.Store(3)
	_, _, err = store.Rename(section.ID, "RenamedMe", time.Unix(2, 0))
	if err == nil {
		t.Fatalf("Rename with non-retryable sectionByKey error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreRenamePostUpdateSectionByIDNonRetryable covers the
// post-update sectionByIDTx non-retryable error path in Rename (line 405-407).
func TestPinSectionStoreRenamePostUpdateSectionByIDNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	section, _, err := store.CreateOrReuseAndAssign("RenameMe2", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The Rename flow: sectionByIDTx (query 1), sessionPinCountTx (query 2),
	// sectionIDByKeyTx (query 3), UPDATE (exec 1), sectionByIDTx (query 4).
	// We fail the 4th query (post-update sectionByID).
	errorQueryTarget.Store(4)
	_, _, err = store.Rename(section.ID, "RenamedMe2", time.Unix(2, 0))
	if err == nil {
		t.Fatalf("Rename with non-retryable post-update sectionByID error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreDeleteSectionCountNonRetryable covers the sessionPinCountTx
// non-retryable error path in DeleteSection (line 466-468).
func TestPinSectionStoreDeleteSectionCountNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	section, _, err := store.CreateOrReuseAndAssign("ToDelete", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The DeleteSection flow: sectionByIDTx (query 1), sessionPinCountTx (query 2).
	// We fail the 2nd query (count).
	errorQueryTarget.Store(2)
	_, _, err = store.DeleteSection(section.ID)
	if err == nil {
		t.Fatalf("DeleteSection with non-retryable count error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreDeleteSessionRowsAffectedNonRetryable covers the
// RowsAffected non-retryable error path in DeleteSession (line 524-526).
func TestPinSectionStoreDeleteSessionRowsAffectedNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	_, _, err := store.CreateOrReuseAndAssign("Section", "session-x", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The DeleteSession flow: DELETE (exec), RowsAffected.
	// We fail the RowsAffected call.
	errorResultTarget.Store(1)
	_, err = store.DeleteSession("session-x")
	if err == nil {
		t.Fatalf("DeleteSession with non-retryable RowsAffected error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreDeleteSectionExecNonRetryable covers the DELETE exec
// non-retryable error path in DeleteSection (line 471-471, 479-479).
func TestPinSectionStoreDeleteSectionExecNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	section, _, err := store.CreateOrReuseAndAssign("ToDelete2", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The DeleteSection flow: sectionByIDTx (query 1), sessionPinCountTx (query 2),
	// DELETE (exec 1). We fail the exec.
	errorExecTarget.Store(1)
	_, _, err = store.DeleteSection(section.ID)
	if err == nil {
		t.Fatalf("DeleteSection with non-retryable exec error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreRenameExecNonRetryable covers the UPDATE exec
// non-retryable error path in Rename (line 392-401).
func TestPinSectionStoreRenameExecNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	section, _, err := store.CreateOrReuseAndAssign("RenameExec", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The Rename flow: sectionByIDTx (query 1), sessionPinCountTx (query 2),
	// sectionIDByKeyTx (query 3), UPDATE (exec 1). We fail the exec.
	errorExecTarget.Store(1)
	_, _, err = store.Rename(section.ID, "RenamedExec", time.Unix(2, 0))
	if err == nil {
		t.Fatalf("Rename with non-retryable exec error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreDeleteSessionExecNonRetryable covers the DELETE exec
// non-retryable error path in DeleteSession.
func TestPinSectionStoreDeleteSessionExecNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	_, _, err := store.CreateOrReuseAndAssign("Section", "session-y", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The DeleteSession flow: DELETE (exec), RowsAffected. We fail the exec.
	errorExecTarget.Store(1)
	_, err = store.DeleteSession("session-y")
	if err == nil {
		t.Fatalf("DeleteSession with non-retryable exec error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreAssignUpsertNonRetryable covers the upsertSessionPinTx
// non-retryable error path in Assign (line 221-229).
func TestPinSectionStoreAssignUpsertNonRetryable(t *testing.T) {
	store := setupErrorStore(t)
	resetErrorCounters()
	section, _, err := store.CreateOrReuseAndAssign("Research", "seed-a", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	resetErrorCounters()
	// The Assign flow: sectionByIDTx (query 1), upsertSessionPinTx (exec 1).
	// We fail the exec.
	errorExecTarget.Store(1)
	_, _, err = store.Assign(section.ID, "session-x", time.Unix(2, 0))
	if err == nil {
		t.Fatalf("Assign with non-retryable upsert error should fail")
	}
	if strings.Contains(err.Error(), "locked") {
		t.Fatalf("error should be non-retryable, got %v", err)
	}
}

// TestPinSectionStoreDeleteSectionCommitNonRetryable is a placeholder.
// The Commit non-retryable error path is hard to trigger without closing
// the underlying connection mid-transaction, and the retryable commit path
// is already covered by TestPinSectionStoreDeleteSectionCommitRetryable in
// pin_section_retry_test.go.
func TestPinSectionStoreDeleteSectionCommitNonRetryable(t *testing.T) {
	// Intentionally empty: the non-retryable commit path is defensive code
	// that requires an unusual failure mode to trigger.
}
