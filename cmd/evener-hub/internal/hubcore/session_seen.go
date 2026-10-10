package hubcore

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"path/filepath"
	"time"

	"github.com/spf13/afero"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql
)

// SessionSeenStore persists the hub's per-session "seen through" marker (spec
// 18, S4) in index.db beside the archive, favorite and pin stores, so the phone
// and the web agree on which finished turns are still unseen (spec 7.1: "A
// blue dot marks the ones you haven't opened since"). The hub has no user
// identity, so the marker is per hub, which on a personal hub is per person.
// Rows are source-qualified like session pins (SessionPinKey): two hosts'
// sessions that share a bare ID keep separate markers.
type SessionSeenStore struct {
	dbPath   string
	fs       afero.Fs
	openDB   func(driverName, dataSourceName string) (*sql.DB, error)
	now      func() time.Time
	onChange func()
}

// NewSessionSeenStore returns a store backed by the SQLite file at dbPath. An
// empty path is a store that reads as no store and writes nothing.
func NewSessionSeenStore(dbPath string) *SessionSeenStore {
	return &SessionSeenStore{dbPath: dbPath, fs: afero.NewOsFs(), openDB: sql.Open, now: time.Now}
}

// SetOnChange registers a callback fired after a write that changed a marker.
func (s *SessionSeenStore) SetOnChange(fn func()) { s.onChange = fn }

// SessionSeenRecord is one session's stored marker. SeenThrough is the newest
// turn-ended time a client showed when it marked the session seen; Unread is
// an explicit "Mark as unread", which outranks it until the next mark.
type SessionSeenRecord struct {
	SeenThrough time.Time
	Unread      bool
}

// SessionSeenSnapshot is the store's contents for one navigation build.
type SessionSeenSnapshot struct {
	// Epoch is when the store first opened. A turn that ended before it counts
	// as seen, so the first build after an upgrade does not report every live
	// session unseen. Zero means no store, and then nothing is unseen.
	Epoch time.Time
	// Records are keyed by SessionPinKey(source, session ID).
	Records map[ArchiveKey]SessionSeenRecord
}

// Unseen reports whether a live session finished a turn nobody has seen since:
// it was marked unread, or its last turn ended after both its seen-through
// mark and the store's epoch. A session with no turn-ended time is never
// unseen here: its row carries no turn_ended_at, and a client keeps its own
// fallback for such a row.
func (s SessionSeenSnapshot) Unseen(key ArchiveKey, turnEndedAt time.Time) bool {
	if s.Epoch.IsZero() || turnEndedAt.IsZero() {
		return false
	}
	record := s.Records[key]
	if record.Unread {
		return true
	}
	return turnEndedAt.After(s.Epoch) && turnEndedAt.After(record.SeenThrough)
}

// SeenThrough is the session's seen-through mark floored at the store's
// epoch: anything that happened after it is new to the person. Zero when there
// is no store.
func (s SessionSeenSnapshot) SeenThrough(key ArchiveKey) time.Time {
	if s.Epoch.IsZero() {
		return time.Time{}
	}
	if mark := s.Records[key].SeenThrough; mark.After(s.Epoch) {
		return mark
	}
	return s.Epoch
}

// Clone returns a snapshot whose records the caller owns.
func (s SessionSeenSnapshot) Clone() SessionSeenSnapshot {
	s.Records = maps.Clone(s.Records)
	return s
}

const createSessionSeenTable = `
CREATE TABLE IF NOT EXISTS session_seen (
  source       TEXT    NOT NULL DEFAULT '',
  session_id   TEXT    NOT NULL,
  seen_through INTEGER NOT NULL DEFAULT 0,
  unread       INTEGER NOT NULL DEFAULT 0,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (source, session_id)
)`

const createSessionSeenEpochTable = `
CREATE TABLE IF NOT EXISTS session_seen_epoch (
  id    INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
  epoch INTEGER NOT NULL
)`

func (s *SessionSeenStore) open() (*sql.DB, error) {
	if err := s.fs.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := s.openDB("sqlite", sqliteDSN(s.dbPath))
	if err != nil {
		return nil, err
	}
	for _, stmt := range []string{createSessionSeenTable, createSessionSeenEpochTable} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if err := ensureIndexSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Snapshot reads the epoch, recording it on the store's first read, and every
// record.
func (s *SessionSeenStore) Snapshot() (SessionSeenSnapshot, error) {
	if s == nil || s.dbPath == "" {
		return SessionSeenSnapshot{}, nil
	}
	db, err := s.open()
	if err != nil {
		return SessionSeenSnapshot{}, err
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	epoch, err := s.epoch(ctx, db)
	if err != nil {
		return SessionSeenSnapshot{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT source, session_id, seen_through, unread FROM session_seen`)
	if err != nil {
		return SessionSeenSnapshot{}, err
	}
	defer func() { _ = rows.Close() }()
	records := make(map[ArchiveKey]SessionSeenRecord)
	for rows.Next() {
		var source, sessionID string
		var through int64
		var unread int
		if err := rows.Scan(&source, &sessionID, &through, &unread); err != nil {
			return SessionSeenSnapshot{}, err
		}
		records[SessionPinKey(source, sessionID)] = SessionSeenRecord{SeenThrough: UnixMilliTime(through), Unread: unread != 0}
	}
	return SessionSeenSnapshot{Epoch: epoch, Records: records}, rows.Err()
}

// epoch reads the store's epoch, recording now as the epoch on the first read.
// INSERT OR IGNORE settles two first reads racing: the loser's value is
// ignored, and both read back the winner's.
func (s *SessionSeenStore) epoch(ctx context.Context, db *sql.DB) (time.Time, error) {
	const read = `SELECT epoch FROM session_seen_epoch WHERE id = 1`
	var epoch int64
	err := db.QueryRowContext(ctx, read).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO session_seen_epoch (id, epoch) VALUES (1, ?)`, s.now().UnixMilli()); err != nil {
			return time.Time{}, err
		}
		err = db.QueryRowContext(ctx, read).Scan(&epoch)
	}
	if err != nil {
		return time.Time{}, err
	}
	return UnixMilliTime(epoch), nil
}

// markSeenQuery and markUnreadQuery back both a single MarkSeen/MarkUnread
// call (autocommitted through write) and MarkBatch (run against a shared
// *sql.Tx instead) - one copy of each statement either path executes.
const markSeenQuery = `
INSERT INTO session_seen (source, session_id, seen_through, unread, updated_at) VALUES (?, ?, ?, 0, ?)
ON CONFLICT(source, session_id) DO UPDATE SET
  seen_through = max(session_seen.seen_through, excluded.seen_through),
  unread = 0,
  updated_at = excluded.updated_at
WHERE session_seen.seen_through < excluded.seen_through OR session_seen.unread != 0`

const markUnreadQuery = `
INSERT INTO session_seen (source, session_id, seen_through, unread, updated_at) VALUES (?, ?, 0, 1, ?)
ON CONFLICT(source, session_id) DO UPDATE SET unread = 1, updated_at = excluded.updated_at
WHERE session_seen.unread = 0`

// MarkSeen records that a client showed the session's last turn ending at
// through: the row's turn_ended_at, a hub timestamp, never the client's clock.
// The mark only moves forward, so a device showing an older Board cannot
// un-see a turn another device already marked, and it clears an explicit
// unread. It reports whether anything changed, and fires onChange only then.
func (s *SessionSeenStore) MarkSeen(source, sessionID string, through time.Time) (bool, error) {
	return s.write(markSeenQuery, NormalizeDecisionSource(source), sessionID, through.UnixMilli(), s.now().Unix())
}

// MarkUnread records an explicit "Mark as unread" (spec 7.3): the session reads
// unseen until it is next marked seen.
func (s *SessionSeenStore) MarkUnread(source, sessionID string) (bool, error) {
	return s.write(markUnreadQuery, NormalizeDecisionSource(source), sessionID, s.now().Unix())
}

// SessionSeenMark is one mark for MarkBatch: the same fields MarkSeen and
// MarkUnread take separately, so a caller building a batch has one type to
// fill in instead of calling one method or the other in a loop.
type SessionSeenMark struct {
	Source, SessionID string
	SeenThrough       time.Time
	Unread            bool
}

// MarkBatch applies every mark in marks inside one index.db transaction:
// either all of them land, or an error rolls back every one of them,
// including marks that individually would have succeeded. sessionSeenSet
// (evener/session/seen/set, S4) validates a whole call before writing any of
// it; writing each mark in its own autocommitted statement, as MarkSeen and
// MarkUnread do on their own, would let a failure partway through leave some
// sessions marked and others not, defeating that all-or-nothing validation.
// It reports whether any mark changed a row, and fires onChange once if so.
func (s *SessionSeenStore) MarkBatch(marks []SessionSeenMark) (bool, error) {
	if s == nil || s.dbPath == "" || len(marks) == 0 {
		return false, nil
	}
	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	changed := false
	for _, mark := range marks {
		var result sql.Result
		if mark.Unread {
			result, err = tx.ExecContext(ctx, markUnreadQuery, NormalizeDecisionSource(mark.Source), mark.SessionID, s.now().Unix())
		} else {
			result, err = tx.ExecContext(ctx, markSeenQuery, NormalizeDecisionSource(mark.Source), mark.SessionID, mark.SeenThrough.UnixMilli(), s.now().Unix())
		}
		if err == nil {
			var rows int64
			rows, err = result.RowsAffected()
			changed = changed || rows > 0
		}
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if changed && s.onChange != nil {
		s.onChange()
	}
	return changed, nil
}

// Delete forgets one session's marker, for session deletion.
func (s *SessionSeenStore) Delete(source, sessionID string) (bool, error) {
	return s.write(`DELETE FROM session_seen WHERE source = ? AND session_id = ?`, NormalizeDecisionSource(source), sessionID)
}

// write runs one statement and reports whether it changed a row. A single
// statement is atomic, and sqliteDSN's busy_timeout waits out a concurrent
// writer, so no retry loop is needed.
func (s *SessionSeenStore) write(query string, args ...any) (bool, error) {
	if s == nil || s.dbPath == "" {
		return false, nil
	}
	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	result, err := db.ExecContext(context.Background(), query, args...)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	if s.onChange != nil {
		s.onChange()
	}
	return true, nil
}
