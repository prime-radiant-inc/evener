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
type HubIdentityStore struct{ dbPath string }

func NewHubIdentityStore(dbPath string) *HubIdentityStore {
	return &HubIdentityStore{dbPath: dbPath}
}

func (s *HubIdentityStore) open() (*sql.DB, error) {
	if s.dbPath == "" {
		return nil, errors.New("hub identity storage is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + sqliteURIPathEscaper.Replace(s.dbPath) + "?_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS hub_identity (singleton INTEGER PRIMARY KEY CHECK(singleton = 1), hub_id TEXT NOT NULL)`); err != nil { //nolint:noctx // local file DB
		_ = db.Close()
		return nil, fmt.Errorf("initialize hub identity: %w", err)
	}
	return db, nil
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
