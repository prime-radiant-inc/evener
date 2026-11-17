package hubcore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// HubIdentityStore persists this hub's nonsecret peer identity in index.db.
// The identity names the hub in session-tools binding and bridge identify
// exchanges; it is not a credential and carries no authority.
type HubIdentityStore struct {
	dbPath string
	openDB func(driverName, dataSourceName string) (*sql.DB, error)
}

func NewHubIdentityStore(dbPath string) *HubIdentityStore {
	return &HubIdentityStore{dbPath: dbPath, openDB: sql.Open}
}

// hubIdentityOpenAttempts bounds how many times the store retries a lock
// conflict that fails the connection open. See connect.
const hubIdentityOpenAttempts = 8

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
// This store is not the first opener of index.db, so the file's journal mode
// and its durability belong to whichever store opened it first; a hand-built
// DSN would land on SQLite's FULL default and pay a per-commit fsync no other
// opener of this file pays.
//
// That shared DSN enters WAL on every new connection, and the transition needs
// the database's exclusive lock. Two connections opening a brand-new index.db
// at once contend for it, and SQLite reports the loser as SQLITE_BUSY without
// consulting busy_timeout: the losing attempt returns in milliseconds instead
// of waiting out the 5s timeout. Its next attempt succeeds, because the
// winner's transition is already recorded in the database file — journal_mode
// is persistent — so a tight retry converges.
//
// Only the open is retried. The CREATE TABLE that follows is an ordinary
// write, which busy_timeout does wait out, so retrying it as well would
// multiply that timeout by the attempt count without covering anything new.
func (s *HubIdentityStore) connect() (*sql.DB, error) {
	var lastErr error
	for range hubIdentityOpenAttempts {
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
