package hubcore

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// openLockDriverName registers a driver that fails its first N opens with the
// injected error, then delegates to the real sqlite driver. With a real
// SQLITE_BUSY it reproduces the concurrent-first-open failure
// deterministically: the WAL transition the shared DSN performs on every new
// connection needs index.db's exclusive lock, and a racing opener gets
// SQLITE_BUSY back in milliseconds rather than waiting out busy_timeout.
const openLockDriverName = "sqlite-hub-identity-open-lock-test"

var (
	openLockDriverOnce  sync.Once
	openLockFailures    atomic.Int64
	errOpenLockInjected error
	// openLockMu serializes the tests that configure the shared driver. Its
	// failure count and injected error are process-global, so two of them
	// running at once would spend each other's injected failures.
	openLockMu sync.Mutex
)

type openLockDriver struct{ real driver.Driver }

func (d *openLockDriver) Open(name string) (driver.Conn, error) {
	if n := openLockFailures.Add(-1); n >= 0 {
		return nil, errOpenLockInjected
	}
	openLockFailures.Add(1)
	return d.real.Open(name)
}

// hubIdentityStoreWithFailingOpen returns a store whose connections are opened
// through a driver that fails the next n opens with injectErr.
func hubIdentityStoreWithFailingOpen(t *testing.T, dbPath string, n int, injectErr error) *HubIdentityStore {
	t.Helper()
	openLockMu.Lock()
	t.Cleanup(openLockMu.Unlock)
	openLockDriverOnce.Do(func() {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			panic("openLockDriver: cannot open sqlite: " + err.Error())
		}
		realDriver := db.Driver()
		_ = db.Close()
		sql.Register(openLockDriverName, &openLockDriver{real: realDriver})
	})
	errOpenLockInjected = injectErr
	openLockFailures.Store(int64(n))
	store := NewHubIdentityStore(dbPath)
	store.openDB = func(_, dataSourceName string) (*sql.DB, error) {
		return sql.Open(openLockDriverName, dataSourceName)
	}
	return store
}

// openWaitClock stands in for the wall clock in the identity open retry so a
// test can charge each open the wait it spent — the abnormal-holder case, where
// SQLite consults busy_timeout and the open blocks for seconds — without
// spending those seconds. waited() is the total virtual wait the store spent,
// which is what a bug in the retry arithmetic multiplies.
type openWaitClock struct {
	mu    sync.Mutex
	now   time.Time
	total time.Duration
}

func newOpenWaitClock() *openWaitClock {
	return &openWaitClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *openWaitClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *openWaitClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.total += d
}

func (c *openWaitClock) waited() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// chargeOpens makes every connection open cost d on the store's clock, which
// models an open that spent d inside SQLite's busy handler before failing.
func chargeOpens(t *testing.T, store *HubIdentityStore, clock *openWaitClock, d time.Duration) {
	t.Helper()
	inner := store.openDB
	store.now = clock.Now
	store.openDB = func(driverName, dataSourceName string) (*sql.DB, error) {
		clock.advance(d)
		return inner(driverName, dataSourceName)
	}
}

// A lock conflict on open must be retried, not surfaced: the concurrent first
// open the store documents hits it whenever two connections race the WAL
// transition on a brand-new index.db, and the losing attempt comes back in
// milliseconds without waiting out busy_timeout.
func TestHubIdentityRetriesLockConflictOnOpen(t *testing.T) {
	store := hubIdentityStoreWithFailingOpen(t, filepath.Join(t.TempDir(), "index.db"), 2, realBusyError(t))
	id, err := store.HubID()
	if err != nil {
		t.Fatalf("HubID with two busy opens = %q, %v", id, err)
	}
	if id == "" {
		t.Fatal("HubID returned an empty identity")
	}
	if left := openLockFailures.Load(); left != 0 {
		t.Fatalf("injected busy opens remaining = %d, want 0 (the retry did not reach a real open)", left)
	}
}

// A lock conflict that clears in milliseconds keeps every attempt: the WAL
// transition race the retry covers fails faster than busy_timeout, so it must
// not be mistaken for a waited-out one. Cutting the retry short at the first
// conflict would bring back the concurrent-first-open flake.
func TestHubIdentityFastLockConflictKeepsFullRetryBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	store := hubIdentityStoreWithFailingOpen(t, path, hubIdentityOpenAttempts-1, realBusyError(t))
	clock := newOpenWaitClock()
	chargeOpens(t, store, clock, 0)
	id, err := store.HubID()
	if err != nil || id == "" {
		t.Fatalf("HubID after %d fast busy opens = %q, %v", hubIdentityOpenAttempts-1, id, err)
	}
	if waited := clock.waited(); waited != 0 {
		t.Fatalf("virtual wait on fast lock conflicts = %v, want 0", waited)
	}
	if left := openLockFailures.Load(); left != 0 {
		t.Fatalf("injected busy opens remaining = %d, want 0: attempt %d was not reached", left, hubIdentityOpenAttempts)
	}
}

// A lock conflict that already waited busy_timeout out is reported, not
// retried. Retrying it spends the whole timeout again on every attempt, which
// is what turned a ~5s startup failure under an abnormal exclusive holder into
// a ~40s one: 8 attempts x busy_timeout(5000). A deadline on the ping cannot
// catch this, because the wait happens inside SQLite's busy handler.
func TestHubIdentityWaitedOutLockConflictIsNotRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	store := hubIdentityStoreWithFailingOpen(t, path, hubIdentityOpenAttempts, realBusyError(t))
	clock := newOpenWaitClock()
	chargeOpens(t, store, clock, hubIdentityOpenBudget)
	if _, err := store.HubID(); err == nil {
		t.Fatal("HubID succeeded while every open returned SQLITE_BUSY")
	}
	if waited := clock.waited(); waited > hubIdentityOpenBudget {
		t.Fatalf("total wait on lock conflicts = %v, want at most one busy_timeout window (%v)", waited, hubIdentityOpenBudget)
	}
	if left := openLockFailures.Load(); left != hubIdentityOpenAttempts-1 {
		t.Fatalf("injected busy opens remaining = %d, want %d: a waited-out conflict must end the retry on its own attempt", left, hubIdentityOpenAttempts-1)
	}
}

// A conflict that waited more than half the window out is a busy_timeout
// expiry even when it returns a little early: SQLite stops the wait once the
// remaining share of the timeout cannot pay for another sleep, so a real
// expiry measures 4.2-5.1s against a 5s setting. Charging 4.2s — what an
// actual held-EXCLUSIVE lock produced — must stop the retry, because the
// WAL-transition race returns in milliseconds and retrying an expiry is the
// whole of #6650's 40s.
func TestHubIdentityExpiryJustUnderTheWindowIsNotRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	store := hubIdentityStoreWithFailingOpen(t, path, hubIdentityOpenAttempts, realBusyError(t))
	clock := newOpenWaitClock()
	chargeOpens(t, store, clock, 4200*time.Millisecond)
	if _, err := store.HubID(); err == nil {
		t.Fatal("HubID succeeded while every open returned SQLITE_BUSY")
	}
	if left := openLockFailures.Load(); left != hubIdentityOpenAttempts-1 {
		t.Fatalf("injected busy opens remaining = %d, want %d: 4.2s is an expiry, not the race", left, hubIdentityOpenAttempts-1)
	}
}

// A lock conflict is retried, but not forever.
func TestHubIdentityOpenLockConflictIsBounded(t *testing.T) {
	store := hubIdentityStoreWithFailingOpen(t, filepath.Join(t.TempDir(), "index.db"), hubIdentityOpenAttempts+5, realBusyError(t))
	if _, err := store.HubID(); err == nil {
		t.Fatal("HubID succeeded while every open returned SQLITE_BUSY")
	}
	if left := openLockFailures.Load(); left != 5 {
		t.Fatalf("busy opens remaining = %d, want 5: want exactly %d attempts", left, hubIdentityOpenAttempts)
	}
}

// An open error that is not a lock conflict is not retried: another attempt
// cannot clear it, so it is returned after exactly one open. The injected
// error is a real driver error that retrying cannot fix and whose text
// contains "busy", so text matching on the message would misclassify it.
func TestHubIdentityDoesNotRetryNonLockOpenError(t *testing.T) {
	injectErr := realNonLockError(t)
	store := hubIdentityStoreWithFailingOpen(t, filepath.Join(t.TempDir(), "index.db"), 1, injectErr)
	_, err := store.HubID()
	if !errors.Is(err, injectErr) {
		t.Fatalf("HubID error = %v, want %v", err, injectErr)
	}
	if left := openLockFailures.Load(); left != 0 {
		t.Fatalf("failing opens remaining = %d, want 0: want exactly one open attempt", left)
	}
}

func TestHubIdentityPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	id, err := NewHubIdentityStore(path).HubID()
	if err != nil || id == "" {
		t.Fatalf("HubID = %q, %v", id, err)
	}
	got, err := NewHubIdentityStore(path).HubID()
	if err != nil || got != id {
		t.Fatalf("reopened ID = %q, %v, want %q", got, err, id)
	}
}

func TestHubIdentityConcurrentFirstOpen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			<-start
			id, err := NewHubIdentityStore(path).HubID()
			results <- result{id, err}
		}()
	}
	close(start)
	var want string
	for range 8 {
		got := <-results
		if got.err != nil || got.id == "" {
			t.Fatalf("HubID = %q, %v", got.id, got.err)
		}
		if want == "" {
			want = got.id
		}
		if got.id != want {
			t.Fatalf("concurrent HubID = %q, want %q", got.id, want)
		}
	}
}
