package hubcore

import (
	"database/sql"
	"errors"
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
	openDB func(string, string) (*sql.DB, error)

	// onChange, when set via SetOnChange, is fired after a successful Set or
	// Delete. Nil is a safe no-op (existing tests construct stores without it).
	onChange func()
}

func NewFavoriteStore(dbPath string) *FavoriteStore {
	return &FavoriteStore{dbPath: dbPath, fs: afero.NewOsFs(), openDB: sql.Open}
}

func (s *FavoriteStore) SetFs(fs afero.Fs) *FavoriteStore { s.fs = fs; return s }

// SetOnChange registers a callback fired after a successful Set or Delete.
// Nil disables the hook.
func (s *FavoriteStore) SetOnChange(fn func()) { s.onChange = fn }

func (s *FavoriteStore) fireChange() {
	if s.onChange != nil {
		s.onChange()
	}
}

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
	db, err := s.openDB("sqlite", sqliteDSN(s.dbPath))
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
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var previous int
	err = tx.QueryRow(`SELECT favorited FROM favorite WHERE kind = ? AND id = ?`, kind, id).Scan(&previous)
	changed := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.Exec(`INSERT INTO favorite (kind, id, favorited, decided_at) VALUES (?, ?, ?, ?)`, kind, id, flag, now.Unix())
		changed = err == nil
	case err == nil && previous != flag:
		_, err = tx.Exec(`UPDATE favorite SET favorited = ?, decided_at = ? WHERE kind = ? AND id = ?`, flag, now.Unix(), kind, id)
		changed = err == nil
	case err == nil:
		// An equivalent decision is a content no-op; notably, do not churn decided_at.
	default:
		return err
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if changed {
		s.fireChange()
	}
	return nil
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
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`DELETE FROM favorite WHERE kind = ? AND id = ?`, kind, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if changed != 0 {
		s.fireChange()
	}
	return nil
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
