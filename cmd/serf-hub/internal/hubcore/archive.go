package hubcore

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
)

// ArchiveKey identifies an archivable entity: a session by ID or a project by name.
type ArchiveKey struct {
	Kind string // "session" | "project"
	ID   string
}

// ArchiveStore persists explicit user archive/unarchive decisions in index.db.
// Auto-archive (by inactivity age) is computed at tree-build time and is NOT
// stored here — only deliberate user decisions are recorded.
type ArchiveStore struct {
	dbPath string

	// fs mediates the directory-scaffolding and existence-check filesystem ops
	// (MkdirAll, Stat) around the SQLite database. It defaults to
	// afero.NewOsFs() (identical to direct os calls). NOTE: the SQLite driver
	// (sql.Open) opens dbPath on the real OS filesystem directly and does NOT
	// route through fs — so injecting a non-OS filesystem here only redirects
	// the surrounding dir/stat ops, not the database read/write itself.
	fs afero.Fs
}

// NewArchiveStore returns a store backed by the SQLite file at dbPath. An empty
// dbPath yields a store whose Decisions() is always empty (graceful no-op).
func NewArchiveStore(dbPath string) *ArchiveStore {
	return &ArchiveStore{dbPath: dbPath, fs: afero.NewOsFs()}
}

// SetFs overrides the store's filesystem for the dir-scaffolding and
// existence-check ops (see the fs field note about the SQLite boundary).
// Returns the store for call chaining.
func (s *ArchiveStore) SetFs(fs afero.Fs) *ArchiveStore {
	s.fs = fs
	return s
}

const createArchiveTable = `
CREATE TABLE IF NOT EXISTS archive (
  kind       TEXT    NOT NULL,
  id         TEXT    NOT NULL,
  archived   INTEGER NOT NULL,
  decided_at INTEGER NOT NULL,
  PRIMARY KEY (kind, id)
)`

func (s *ArchiveStore) open() (*sql.DB, error) {
	if err := s.fs.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(createArchiveTable); err != nil { //nolint:noctx // local file DB
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Set upserts a user decision: archived=true to archive, false to unarchive.
func (s *ArchiveStore) Set(kind, id string, archived bool, now time.Time) error {
	if s.dbPath == "" {
		return nil
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	flag := 0
	if archived {
		flag = 1
	}
	_, err = db.Exec( //nolint:noctx // local file DB
		`INSERT INTO archive (kind, id, archived, decided_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(kind, id) DO UPDATE SET archived=excluded.archived, decided_at=excluded.decided_at`,
		kind, id, flag, now.Unix())
	return err
}

// Decisions returns every explicit decision. Empty when no DB / no table.
func (s *ArchiveStore) Decisions() (map[ArchiveKey]bool, error) {
	out := make(map[ArchiveKey]bool)
	if s.dbPath == "" {
		return out, nil
	}
	if _, err := s.fs.Stat(s.dbPath); os.IsNotExist(err) {
		return out, nil
	}
	db, err := s.open()
	if err != nil {
		return out, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT kind, id, archived FROM archive`) //nolint:noctx // local file DB
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, id string
		var flag int
		if err := rows.Scan(&k, &id, &flag); err != nil {
			return out, err
		}
		out[ArchiveKey{Kind: k, ID: id}] = flag == 1
	}
	return out, rows.Err()
}
