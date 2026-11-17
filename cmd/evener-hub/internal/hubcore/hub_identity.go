package hubcore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// HubIdentityStore persists this hub's nonsecret peer identity in index.db.
// The identity names the hub in session-tools binding and bridge identify
// exchanges; it is not a credential and carries no authority.
type HubIdentityStore struct {
	dbPath string
	openDB func(driverName, dataSourceName string) (*sql.DB, error)
	now    func() time.Time
}

func NewHubIdentityStore(dbPath string) *HubIdentityStore {
	return &HubIdentityStore{dbPath: dbPath, openDB: sql.Open, now: time.Now}
}

// hubIdentityOpenAttempts bounds how many times the store retries a lock
// conflict that fails the connection open. See connect.
const hubIdentityOpenAttempts = 8

// hubIdentityOpenBudget is how much wall time connect may spend on lock
// conflicts in total: one busy_timeout window, which is also the most the
// CREATE TABLE outside the retry can spend waiting once.
const hubIdentityOpenBudget = sqliteBusyTimeoutMS * time.Millisecond

// hubIdentityWaitedOutAttempt is the attempt duration at which a lock conflict
// is a busy_timeout expiry rather than the WAL transition race the retry
// covers. The race returns in milliseconds — 2-23ms measured on the runs
// behind #6636 — while an expiry spends most of the window: 4.2-5.1s measured
// against a held EXCLUSIVE lock, because SQLite stops once the remaining share
// of the timeout cannot pay for another sleep. Half the window sits two orders
// of magnitude above the race and below every expiry measured, so an attempt
// past it is not the case the retry exists for.
const hubIdentityWaitedOutAttempt = hubIdentityOpenBudget / 2

func (s *HubIdentityStore) open() (*sql.DB, error) {
	if s.dbPath == "" {
		return nil, errors.New("hub identity storage is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := s.connect()
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS hub_identity (singleton INTEGER PRIMARY KEY CHECK(singleton = 1), hub_id TEXT NOT NULL)`); err != nil { //nolint:noctx // local file DB
		_ = db.Close()
		return nil, fmt.Errorf("initialize hub identity: %w", err)
	}
	return db, nil
}

// connect opens a connection with the DSN every opener of index.db shares, and
// retries a lock conflict while doing it.
//
// At hub startup this store is normally not the first opener of index.db: the
// past index opens the file first, so its journal mode and its durability
// belong to that opener, and a hand-built DSN would land on SQLite's FULL
// default and pay a per-commit fsync no other opener of this file pays. The
// store can still be the first opener — startup only logs a failed past-index
// rebuild, which may leave the file to this store — so it must also work on a
// brand-new index.db, as it does in this package's tests.
//
// That shared DSN enters WAL on every new connection, and the transition needs
// the database's exclusive lock. Two connections opening a brand-new index.db
// at once contend for it, and SQLite reports the loser as SQLITE_BUSY without
// consulting busy_timeout: the losing attempt returns in milliseconds instead
// of waiting out the 5s timeout. Its next attempt succeeds, because the
// winner's transition is already recorded in the database file — journal_mode
// is persistent — so a tight retry converges.
//
// A conflict that is not that race — an abnormal holder such as a stuck
// sibling hub or a stray sqlite3 shell keeping index.db locked past
// busy_timeout — is reported, not retried. It surfaces only after an attempt
// has waited the timeout out, and retrying it spends the timeout again per
// attempt: 8 attempts x busy_timeout(5000) is the ~40s startup block of
// #6650, for an error the first attempt already had. Two rules bound it: an
// attempt that outlasted hubIdentityWaitedOutAttempt was that expiry, so it
// ends the loop, and the elapsed time is checked against
// hubIdentityOpenBudget in case several slower-than-the-race attempts
// accumulate. The per-attempt rule runs first, so the abnormal-holder case
// stops on the attempt that waited (4.2s measured), not one attempt later.
//
// A deadline on the ping cannot do this job: the wait happens inside SQLite's
// busy handler, which does not observe the context — a ping with a 300ms
// deadline still blocked 4.2s.
//
// Only the open is retried. The CREATE TABLE that follows is an ordinary
// write, which busy_timeout does wait out, so retrying it as well would
// multiply that timeout by the attempt count without covering anything new.
func (s *HubIdentityStore) connect() (*sql.DB, error) {
	var lastErr error
	started := s.now()
	for range hubIdentityOpenAttempts {
		attemptStart := s.now()
		db, err := s.openDB("sqlite", sqliteDSNImmediate(s.dbPath))
		if err == nil {
			// Ping forces the connection, so the DSN's pragmas — including the
			// WAL transition that races — run here, inside the retry.
			err = db.PingContext(context.Background())
		}
		if err == nil {
			return db, nil
		}
		if db != nil {
			_ = db.Close()
		}
		if !isSQLiteRetryable(err) {
			return nil, err
		}
		lastErr = err
		if s.now().Sub(attemptStart) >= hubIdentityWaitedOutAttempt {
			return nil, lastErr
		}
		if s.now().Sub(started) >= hubIdentityOpenBudget {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

// HubID persists the identity once, including concurrent first opens.
func (s *HubIdentityStore) HubID() (string, error) {
	db, err := s.open()
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	var id string
	if err := db.QueryRowContext(context.Background(), `SELECT hub_id FROM hub_identity WHERE singleton = 1`).Scan(&id); err == nil && id != "" {
		return id, nil
	} else if !errors.Is(err, sql.ErrNoRows) && err != nil {
		return "", fmt.Errorf("read hub identity: %w", err)
	}
	id = "hub-" + uuid.NewString()
	if _, err := db.Exec(`INSERT INTO hub_identity (singleton, hub_id) VALUES (1, ?) ON CONFLICT(singleton) DO NOTHING`, id); err != nil { //nolint:noctx // local file DB
		return "", fmt.Errorf("persist hub identity: %w", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT hub_id FROM hub_identity WHERE singleton = 1`).Scan(&id); err != nil {
		return "", fmt.Errorf("reread hub identity: %w", err)
	}
	return id, nil
}
