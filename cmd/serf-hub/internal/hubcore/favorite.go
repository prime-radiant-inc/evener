package hubcore

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/afero"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
)

// FavoriteStore persists explicit user favorite decisions in index.db, cloned
// from ArchiveStore's shape (kind="session"). It shares the same DB file.
type FavoriteStore struct {
	dbPath string
	fs     afero.Fs
}

func NewFavoriteStore(dbPath string) *FavoriteStore {
	return &FavoriteStore{dbPath: dbPath, fs: afero.NewOsFs()}
}

func (s *FavoriteStore) SetFs(fs afero.Fs) *FavoriteStore { s.fs = fs; return s }

const createFavoriteTable = `
CREATE TABLE IF NOT EXISTS favorite (
  kind       TEXT    NOT NULL,
  id         TEXT    NOT NULL,
  favorited  INTEGER NOT NULL,
  decided_at INTEGER NOT NULL,
  PRIMARY KEY (kind, id)
)`

func (s *FavoriteStore) open() (*sql.DB, error) {
	if err := s.fs.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(createFavoriteTable); err != nil { //nolint:noctx // local file DB
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (s *FavoriteStore) Set(kind, id string, favorited bool, now time.Time) error {
	if s.dbPath == "" {
		return nil
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	flag := 0
	if favorited {
		flag = 1
	}
	_, err = db.Exec( //nolint:noctx // local file DB
		`INSERT INTO favorite (kind, id, favorited, decided_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(kind, id) DO UPDATE SET favorited=excluded.favorited, decided_at=excluded.decided_at`,
		kind, id, flag, now.Unix())
	return err
}

func (s *FavoriteStore) Delete(kind, id string) error {
	if s.dbPath == "" {
		return nil
	}
	db, err := s.open()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`DELETE FROM favorite WHERE kind = ? AND id = ?`, kind, id) //nolint:noctx // local file DB
	return err
}

// Favorites returns every favorited=true decision. Empty when no DB / no table.
func (s *FavoriteStore) Favorites() (map[ArchiveKey]bool, error) {
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
	rows, err := db.Query(`SELECT kind, id, favorited FROM favorite`) //nolint:noctx // local file DB
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
		if flag == 1 {
			out[ArchiveKey{Kind: k, ID: id}] = true
		}
	}
	return out, rows.Err()
}
