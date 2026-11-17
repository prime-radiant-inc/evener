package hubcore

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
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
