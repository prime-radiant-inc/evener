package hubcore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"primeradiant.com/evener/appwire"
)

// ErrManagerAuthority refuses a toggle by a hub other than the selected authority.
var ErrManagerAuthority = errors.New("manager mode belongs to another hub")

// SessionPreferencesStore keeps the hosting-source workspace's human preference
// in index.db. It does not replicate settings onto an enabling hub.
type SessionPreferencesStore struct{ dbPath string }

func NewSessionPreferencesStore(dbPath string) *SessionPreferencesStore {
	return &SessionPreferencesStore{dbPath: dbPath}
}

func (s *SessionPreferencesStore) open() (*sql.DB, error) {
	if s.dbPath == "" {
		return nil, errors.New("session preferences storage is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	// Keep the index owner's persistent journal mode. Requesting WAL on every
	// cold connection can race the first writer's journal-mode transition,
	// which SQLite refuses immediately despite busy_timeout. Authority writes
	// need only an IMMEDIATE transaction, in either journal mode.
	dsn := "file:" + sqliteURIPathEscaper.Replace(s.dbPath) + "?_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS hub_identity (singleton INTEGER PRIMARY KEY CHECK(singleton = 1), hub_id TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS session_manager (ref TEXT PRIMARY KEY, hub_id TEXT NOT NULL, enabled INTEGER NOT NULL, revision INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil { //nolint:noctx // local file DB
			_ = db.Close()
			return nil, fmt.Errorf("initialize session preferences: %w", err)
		}
	}
	return db, nil
}

// HubID persists a nonsecret identity once, including concurrent first opens.
func (s *SessionPreferencesStore) HubID() (string, error) {
	db, err := s.open()
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(`INSERT INTO hub_identity(singleton, hub_id) VALUES(1, ?) ON CONFLICT(singleton) DO NOTHING`, id.String()); err != nil { //nolint:noctx // local file DB
		return "", err
	}
	var stored string
	err = db.QueryRow(`SELECT hub_id FROM hub_identity WHERE singleton = 1`).Scan(&stored) //nolint:noctx // local file DB
	return stored, err
}

func readManager(row *sql.Row) (appwire.ManagerSetting, error) {
	var setting appwire.ManagerSetting
	err := row.Scan(&setting.HubID, &setting.Enabled, &setting.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return appwire.ManagerSetting{}, nil
	}
	return setting, err
}

func (s *SessionPreferencesStore) Manager(ref string) (appwire.ManagerSetting, error) {
	if _, err := appwire.ParseRef(ref); err != nil {
		return appwire.ManagerSetting{}, err
	}
	db, err := s.open()
	if err != nil {
		return appwire.ManagerSetting{}, err
	}
	defer func() { _ = db.Close() }()
	return readManager(db.QueryRow(`SELECT hub_id, enabled, revision FROM session_manager WHERE ref = ?`, ref)) //nolint:noctx // local file DB
}

// SetManager selects authority and changes the setting in one IMMEDIATE
// transaction. A failed write returns the unchanged setting read under its lock.
// Disabling retains the last selected hub for display, but releases authority.
func (s *SessionPreferencesStore) SetManager(ref, enablingHubID string, enabled bool) (appwire.ManagerSetting, error) {
	if _, err := appwire.ParseRef(ref); err != nil {
		return appwire.ManagerSetting{}, err
	}
	if strings.TrimSpace(enablingHubID) == "" {
		return appwire.ManagerSetting{}, errors.New("enabling hub identity is required")
	}
	db, err := s.open()
	if err != nil {
		return appwire.ManagerSetting{}, err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.Begin() //nolint:noctx // local file DB
	if err != nil {
		return appwire.ManagerSetting{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := readManager(tx.QueryRow(`SELECT hub_id, enabled, revision FROM session_manager WHERE ref = ?`, ref)) //nolint:noctx // local file DB
	if err != nil {
		return current, err
	}
	if current.Enabled && current.HubID != enablingHubID {
		return current, ErrManagerAuthority
	}
	if current.Enabled == enabled {
		return current, nil
	}
	next := current
	next.Enabled = enabled
	if enabled {
		next.HubID = enablingHubID
	}
	next.Revision++
	_, err = tx.Exec(`INSERT INTO session_manager(ref, hub_id, enabled, revision) VALUES(?, ?, ?, ?) ON CONFLICT(ref) DO UPDATE SET hub_id=excluded.hub_id, enabled=excluded.enabled, revision=excluded.revision`, ref, next.HubID, next.Enabled, next.Revision) //nolint:noctx // local file DB
	if err != nil {
		return current, err
	}
	if err := tx.Commit(); err != nil {
		return current, err
	}
	return next, nil
}
