package hubcore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"primeradiant.com/evener/appwire"
)

// ArchiveKey identifies an archivable entity: a session by ID or a project by
// name, qualified by the source (host) that owns it. Source is empty for the
// controller's own entries and the configured host name for a remote one, so
// two hosts' projects that share an ID keep separate keys.
type ArchiveKey struct {
	Kind   string // "session" | "project"
	ID     string
	Source string
}

// NormalizeDecisionSource maps a wire-provided source to its decision store
// key. The controller's own entries key on the empty string; a remote host keys
// on its host name. The wire default, "local" (and an absent field), normalizes
// to the controller key so both spellings address the same entries. Surrounding
// whitespace is trimmed first: a source is an exact host name, so " local " and
// "host-a " must not create a key no real source can ever address.
func NormalizeDecisionSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" || source == "local" {
		return ""
	}
	return source
}

// NormalizeDecisionSessionID maps a wire-provided session identity to the
// decision store key that row is read under. A remote session's canonical ref
// ("host-a:thread") is exactly the identity the tree gives that host's row, so
// it is kept, trimmed and re-spelled canonically. The controller's own rows are
// keyed by their bare session ID — every stored local decision predates the ref
// spelling — so a "local:thread" ref normalizes to that bare ID instead of
// creating a second key no read path consults, and a bare ID is already it.
func NormalizeDecisionSessionID(id string) string {
	id = strings.TrimSpace(id)
	ref, err := appwire.ParseRef(id)
	if err != nil {
		return id
	}
	if NormalizeDecisionSource(ref.SourceID) == "" {
		return ref.ThreadID
	}
	return ref.String()
}

// ArchiveStore persists explicit user archive/unarchive decisions in index.db.
// Auto-archive (by inactivity age) is computed at tree-build time and is NOT
// stored here — only deliberate user decisions are recorded.
type ArchiveStore struct {
	dbPath string
	openDB func(string, string) (*sql.DB, error)

	// fs mediates the directory-scaffolding and existence-check filesystem ops
	// (MkdirAll, Stat) around the SQLite database. It defaults to
	// afero.NewOsFs() (identical to direct os calls). NOTE: the SQLite driver
	// (sql.Open) opens dbPath on the real OS filesystem directly and does NOT
	// route through fs — so injecting a non-OS filesystem here only redirects
	// the surrounding dir/stat ops, not the database read/write itself.
	fs afero.Fs

	// onChange, when set via SetOnChange, is fired after a successful Set or
	// Delete. Nil is a safe no-op (existing tests construct stores without it).
	onChange func()
}

// NewArchiveStore returns a store backed by the SQLite file at dbPath. An empty
// dbPath yields a store whose Decisions() is always empty (graceful no-op).
func NewArchiveStore(dbPath string) *ArchiveStore {
	return &ArchiveStore{dbPath: dbPath, fs: afero.NewOsFs(), openDB: sql.Open}
}

// SetFs overrides the store's filesystem for the dir-scaffolding and
// existence-check ops (see the fs field note about the SQLite boundary).
// Returns the store for call chaining.
func (s *ArchiveStore) SetFs(fs afero.Fs) *ArchiveStore {
	s.fs = fs
	return s
}

// SetOnChange registers a callback fired after a successful Set or Delete.
// Nil disables the hook.
func (s *ArchiveStore) SetOnChange(fn func()) { s.onChange = fn }

func (s *ArchiveStore) fireChange() {
	if s.onChange != nil {
		s.onChange()
	}
}

const createArchiveTable = `
CREATE TABLE IF NOT EXISTS archive (
  source     TEXT    NOT NULL DEFAULT '',
  kind       TEXT    NOT NULL,
  id         TEXT    NOT NULL,
  archived   INTEGER NOT NULL,
  decided_at INTEGER NOT NULL,
  PRIMARY KEY (source, kind, id)
)`

func (s *ArchiveStore) open() (*sql.DB, error) {
	if err := s.fs.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := s.openDB("sqlite", sqliteDSN(s.dbPath))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(createArchiveTable); err != nil { //nolint:noctx // local file DB
		_ = db.Close()
		return nil, err
	}
	if err := ensureIndexSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Set upserts a user decision: archived=true to archive, false to unarchive.
func (s *ArchiveStore) Set(source, kind, id string, archived bool, now time.Time) error {
	if s.dbPath == "" {
		return nil
	}
	source = NormalizeDecisionSource(source)
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
		`INSERT INTO archive (source, kind, id, archived, decided_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(source, kind, id) DO UPDATE SET archived=excluded.archived, decided_at=excluded.decided_at`,
		source, kind, id, flag, now.Unix())
	if err != nil {
		return err
	}
	s.fireChange()
	return nil
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
	rows, err := db.Query(`SELECT source, kind, id, archived FROM archive`) //nolint:noctx // local file DB
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var source, k, id string
		var flag int
		if err := rows.Scan(&source, &k, &id, &flag); err != nil {
			return out, err
		}
		out[ArchiveKey{Kind: k, ID: id, Source: source}] = flag == 1
	}
	return out, rows.Err()
}

// Delete removes a decision row. A no-op when the DB path is empty or the row
// is absent (idempotent — the delete/scrub paths call it unconditionally).
func (s *ArchiveStore) Delete(source, kind, id string) error {
	if s.dbPath == "" {
		return nil
	}
	source = NormalizeDecisionSource(source)
	db, err := s.open()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`DELETE FROM archive WHERE source = ? AND kind = ? AND id = ?`, source, kind, id) //nolint:noctx // local file DB
	if err != nil {
		return err
	}
	s.fireChange()
	return nil
}

// decisionSourceColumn is the SQLite column carrying the host/source dimension
// of a decision key. Legacy index.db files predate multi-host federation and
// lack it; ensureIndexSchema rebuilds those tables.
const decisionSourceColumn = "source"

// rowQuerier is the query surface shared by *sql.DB and the dedicated *sql.Conn
// the migration recheck runs on.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func tableHasColumnContext(ctx context.Context, q rowQuerier, table, column string) (bool, error) {
	rows, err := q.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
