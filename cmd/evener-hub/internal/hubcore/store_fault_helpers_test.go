package hubcore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// Fault-injection seams shared by the store tests: a filesystem whose
// directory, stat and chmod calls fail on demand, and two wrappers around the
// real SQLite driver that fail chosen operations inside a transaction.

// failingFs fails MkdirAll and Chmod with the configured errors, and reports
// every Stat as missing unless statErr is set.
type failingFs struct {
	afero.Fs
	mkdirErr error
	statErr  error
	chmodErr error
}

func (f failingFs) MkdirAll(string, os.FileMode) error { return f.mkdirErr }
func (f failingFs) Stat(string) (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return nil, os.ErrNotExist
}
func (f failingFs) Chmod(string, os.FileMode) error { return f.chmodErr }

// errInjectedFault is the non-retryable failure errorDriver injects.
var errInjectedFault = errors.New("injected non-retryable fault")

// lockedDriver wraps the real "sqlite" driver and injects a real SQLITE_BUSY
// error into specific operations inside transactions for a configurable
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
	errInjectedLock     error        // the real driver error injected as the lock failure
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
		return nil, errInjectedLock
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
			return nil, errInjectedLock
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
			return nil, errInjectedLock
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
			return nil, errInjectedLock
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
		return errInjectedLock
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
	resetLockedCounters()
	t.Cleanup(resetLockedCounters)
	initLockedDriver()
	errInjectedLock = realBusyError(t)
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seed := NewPinSectionStore(dbPath)
	if _, _, err := seed.CreateOrReuseAndAssign("Seed", "", "seed-session", time.Unix(1, 0)); err != nil {
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
	tx, err := c.real.Begin() //nolint:staticcheck // deprecated driver.Conn.Begin used for test coverage
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
			return nil, errInjectedFault
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
	if execer, ok := c.real.(driver.Execer); ok { //nolint:staticcheck // deprecated driver.Execer used for test coverage
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
			return nil, errInjectedFault
		}
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
		return 0, errInjectedFault
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
	if _, _, err := seed.CreateOrReuseAndAssign("Seed", "", "seed-session", time.Unix(1, 0)); err != nil {
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
