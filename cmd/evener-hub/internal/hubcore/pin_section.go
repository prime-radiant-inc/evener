package hubcore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/afero"
	"golang.org/x/text/cases"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"primeradiant.com/evener/hubapi"
)

const PinSectionNameMaxRunes = 80

var (
	ErrPinSectionName     = errors.New("invalid pin section name")
	ErrPinSectionNotFound = errors.New("pin section not found")
	ErrPinSectionConflict = errors.New("pin section name already exists")

	pinSectionRandRead                   = rand.Read
	pinSectionBeforeSectionInsertHook    func()
	pinSectionBeforeAssignmentCommitHook func()
)

// PinSection groups pinned sessions under a durable user-defined name.
type PinSection struct {
	ID          string
	Name        string
	MemberCount int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SessionPin records the durable section assignment for one session. Source is
// empty for the controller's own session and the host name for a remote one,
// so two hosts' sessions that share a bare ID keep separate pins.
type SessionPin struct {
	Source     string
	SessionID  string
	SectionID  string
	AssignedAt time.Time
}

// SessionPinKey returns the source-qualified key one session's pin is stored
// under. Source normalizes like the decision stores' source: an absent or
// "local" spelling is the controller's own session.
func SessionPinKey(source, sessionID string) ArchiveKey {
	return ArchiveKey{Kind: "session", ID: sessionID, Source: NormalizeDecisionSource(source)}
}

// pinKeyLabel renders a pin's (source, sessionID) identity for diagnostics:
// the controller's bare session ID, or the host-qualified ref a remote pin is
// addressed by. ArchiveKey is a data key with no rendering contract (a "%s" of
// it is a struct dump), so the readable spelling lives here, beside the pin
// API that reports it.
func pinKeyLabel(source, sessionID string) string {
	source = NormalizeDecisionSource(source)
	if source == "" {
		return sessionID
	}
	return hubapi.Ref{HostID: source, SessionID: sessionID}.String()
}

// SessionPinIdentity maps a session identity — a bare session ID, a
// "local:<id>" ref, or a "<host>:<id>" ref — to the source-qualified pin key
// it addresses. The wire spelling "local" and an absent source name the
// controller's own session; any other host keeps its name, so one source's
// identity can never address another source's row that shares its bare ID.
func SessionPinIdentity(identity string) ArchiveKey {
	ref, err := hubapi.ParseRef(identity)
	if err != nil {
		return SessionPinKey("", identity)
	}
	return SessionPinKey(ref.HostID, ref.SessionID)
}

// PinSectionStore persists named pin sections and their session assignments in
// the same SQLite index DB as archive/favorite decisions.
type PinSectionStore struct {
	dbPath   string
	fs       afero.Fs
	openDB   func(driverName, dataSourceName string) (*sql.DB, error)
	onChange func()
}

// NewPinSectionStore returns a store backed by the SQLite file at dbPath.
func NewPinSectionStore(dbPath string) *PinSectionStore {
	return &PinSectionStore{dbPath: dbPath, fs: afero.NewOsFs(), openDB: sql.Open}
}

// SetOnChange registers a callback invoked after a committed content change.
func (s *PinSectionStore) SetOnChange(fn func()) { s.onChange = fn }

func (s *PinSectionStore) fireChange() {
	if s.onChange != nil {
		s.onChange()
	}
}

// SetFs overrides the store filesystem seam for directory checks.
func (s *PinSectionStore) SetFs(fs afero.Fs) *PinSectionStore {
	s.fs = fs
	return s
}

// NormalizePinSectionName trims whitespace, case-folds the display name into a
// stable key, and enforces the maximum rune count.
func NormalizePinSectionName(raw string) (display string, key string, err error) {
	display = strings.TrimSpace(raw)
	if display == "" {
		return "", "", ErrPinSectionName
	}
	if utf8.RuneCountInString(display) > PinSectionNameMaxRunes {
		return "", "", ErrPinSectionName
	}
	key = cases.Fold().String(display)
	return display, key, nil
}

const createPinSectionTable = `
CREATE TABLE IF NOT EXISTS pin_section (
  id         TEXT    NOT NULL PRIMARY KEY,
  name       TEXT    NOT NULL,
  name_key   TEXT    NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
)`

const createSessionPinTable = `
CREATE TABLE IF NOT EXISTS session_pin (
  source      TEXT    NOT NULL DEFAULT '',
  session_id  TEXT    NOT NULL,
  section_id  TEXT    NOT NULL REFERENCES pin_section(id) ON DELETE CASCADE,
  assigned_at INTEGER NOT NULL,
  PRIMARY KEY (source, session_id)
)`

func (s *PinSectionStore) open() (*sql.DB, error) {
	return s.openWithImmediateTransaction(false)
}

// openWriteAttempt keeps the optimistic first transaction deferred. A retry
// begins IMMEDIATE so SQLite waits for the current writer before taking the
// next read snapshot.
func (s *PinSectionStore) openWriteAttempt(attempt int) (*sql.DB, error) {
	return s.openWithImmediateTransaction(attempt > 0)
}

func (s *PinSectionStore) openWithImmediateTransaction(immediate bool) (*sql.DB, error) {
	if s == nil || s.dbPath == "" {
		return nil, nil
	}
	if err := s.fs.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	dataSourceName := sqliteDSN(s.dbPath)
	if immediate {
		dataSourceName += "&_txlock=immediate"
	}
	db, err := s.openDB("sqlite", dataSourceName)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	var enabled int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		_ = db.Close()
		return nil, err
	}
	if enabled != 1 {
		_ = db.Close()
		return nil, errors.New("foreign_keys pragma not enabled")
	}
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, stmt := range []string{createPinSectionTable, createSessionPinTable, createFavoriteTable} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
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

// Sections returns every stored section ordered alphabetically with durable
// member counts.
func (s *PinSectionStore) Sections() ([]PinSection, error) {
	if s == nil || s.dbPath == "" {
		return []PinSection{}, nil
	}
	db, err := s.open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), `
SELECT s.id, s.name, s.created_at, s.updated_at, COUNT(p.session_id)
FROM pin_section AS s
LEFT JOIN session_pin AS p ON p.section_id = s.id
GROUP BY s.id, s.name, s.name_key, s.created_at, s.updated_at
	ORDER BY s.name_key, s.name, s.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PinSection
	for rows.Next() {
		var section PinSection
		var createdAt, updatedAt, memberCount int64
		if err := rows.Scan(&section.ID, &section.Name, &createdAt, &updatedAt, &memberCount); err != nil {
			return nil, err
		}
		section.CreatedAt = time.Unix(createdAt, 0).UTC()
		section.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		section.MemberCount = int(memberCount)
		out = append(out, section)
	}
	return out, rows.Err()
}

// errRetryWrite is returned by a writeTx body to ask for another attempt
// without it being a SQLite lock error.
var errRetryWrite = errors.New("retry pin write")

// pinWrite is the outcome of a section or assignment write.
type pinWrite struct {
	section PinSection
	changed bool
}

// writeTx runs fn in one transaction and commits it. A SQLite lock error, or
// fn returning errRetryWrite, rolls back and retries on a fresh connection (the
// retries begin IMMEDIATE, see openWriteAttempt); any other error is returned
// as is. label names the write in the retry-limit error.
func writeTx[T any](s *PinSectionStore, label string, fn func(tx *sql.Tx) (T, error)) (T, error) {
	var zero T
	for attempt := range 8 {
		db, err := s.openWriteAttempt(attempt)
		if err != nil {
			return zero, err
		}
		out, err := runTx(db, fn)
		_ = db.Close()
		if err == nil {
			return out, nil
		}
		if errors.Is(err, errRetryWrite) || isSQLiteRetryable(err) {
			continue
		}
		return zero, err
	}
	return zero, fmt.Errorf("%s: retry limit reached", label)
}

func runTx[T any](db *sql.DB, fn func(tx *sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return zero, err
	}
	out, err := fn(tx)
	if err != nil {
		_ = tx.Rollback()
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return zero, err
	}
	return out, nil
}

// Assign moves the (source, sessionID) pin to sectionID, updating assigned_at
// only when the section actually changes. Source normalizes the same way the
// decision stores' does: absent/"local" is the controller's own session.
func (s *PinSectionStore) Assign(sectionID, source, sessionID string, now time.Time) (PinSection, bool, error) {
	if s == nil || s.dbPath == "" {
		return PinSection{}, false, nil
	}
	source = NormalizeDecisionSource(source)
	out, err := writeTx(s, "assign "+pinKeyLabel(source, sessionID), func(tx *sql.Tx) (pinWrite, error) {
		section, ok, err := sectionByIDTx(tx, sectionID)
		if err != nil {
			return pinWrite{}, err
		}
		if !ok {
			return pinWrite{}, ErrPinSectionNotFound
		}
		changed, err := upsertSessionPinTx(tx, source, sessionID, sectionID, now)
		if err != nil {
			return pinWrite{}, err
		}
		section.MemberCount, err = sessionPinCountTx(tx, section.ID)
		if err != nil {
			return pinWrite{}, err
		}
		if pinSectionBeforeAssignmentCommitHook != nil {
			pinSectionBeforeAssignmentCommitHook()
		}
		return pinWrite{section, changed}, nil
	})
	if err != nil {
		return PinSection{}, false, err
	}
	if out.changed {
		s.fireChange()
	}
	return out.section, out.changed, nil
}

// CreateOrReuseAndAssign creates a section when needed, reuses an existing
// case-folded match, and assigns the (source, sessionID) pin in the same
// transaction.
func (s *PinSectionStore) CreateOrReuseAndAssign(name, source, sessionID string, now time.Time) (PinSection, bool, error) {
	if s == nil || s.dbPath == "" {
		return PinSection{}, false, nil
	}
	display, key, err := NormalizePinSectionName(name)
	if err != nil {
		return PinSection{}, false, err
	}
	source = NormalizeDecisionSource(source)
	out, err := writeTx(s, fmt.Sprintf("create or reuse pin section %q", display), func(tx *sql.Tx) (pinWrite, error) {
		section, created, err := ensureSectionTx(tx, display, key, now)
		if err != nil {
			if isSQLiteUniqueNameConflict(err) {
				return pinWrite{}, errRetryWrite
			}
			return pinWrite{}, err
		}
		changed, err := upsertSessionPinTx(tx, source, sessionID, section.ID, now)
		if err != nil {
			return pinWrite{}, err
		}
		section.MemberCount, err = sessionPinCountTx(tx, section.ID)
		if err != nil {
			return pinWrite{}, err
		}
		if pinSectionBeforeAssignmentCommitHook != nil {
			pinSectionBeforeAssignmentCommitHook()
		}
		return pinWrite{section, created || changed}, nil
	})
	if err != nil {
		return PinSection{}, false, err
	}
	if out.changed {
		s.fireChange()
	}
	return out.section, out.changed, nil
}

// Unpin removes one session's assignment while leaving its section intact.
func (s *PinSectionStore) Unpin(source, sessionID string) (bool, error) {
	return s.DeleteSession(source, sessionID)
}

// Rename updates a section's display name, allowing case-only changes.
func (s *PinSectionStore) Rename(sectionID, name string, now time.Time) (PinSection, bool, error) {
	if s == nil || s.dbPath == "" {
		return PinSection{}, false, nil
	}
	display, key, err := NormalizePinSectionName(name)
	if err != nil {
		return PinSection{}, false, err
	}
	out, err := writeTx(s, "rename pin section "+sectionID, func(tx *sql.Tx) (pinWrite, error) {
		section, ok, err := sectionByIDTx(tx, sectionID)
		if err != nil {
			return pinWrite{}, err
		}
		if !ok {
			return pinWrite{}, ErrPinSectionNotFound
		}
		section.MemberCount, err = sessionPinCountTx(tx, section.ID)
		if err != nil {
			return pinWrite{}, err
		}
		if section.Name == display {
			return pinWrite{section, false}, nil
		}
		otherID, found, err := sectionIDByKeyTx(tx, key)
		if err != nil {
			return pinWrite{}, err
		}
		if found && otherID != sectionID {
			return pinWrite{}, ErrPinSectionConflict
		}
		if _, err := tx.ExecContext(context.Background(), `UPDATE pin_section SET name = ?, name_key = ?, updated_at = ? WHERE id = ?`, display, key, now.Unix(), sectionID); err != nil {
			if !isSQLiteRetryable(err) && isSQLiteUniqueNameConflict(err) {
				return pinWrite{}, ErrPinSectionConflict
			}
			return pinWrite{}, err
		}
		updated, ok, err := sectionByIDTx(tx, sectionID)
		if err != nil {
			return pinWrite{}, err
		}
		if !ok {
			return pinWrite{}, ErrPinSectionNotFound
		}
		updated.MemberCount = section.MemberCount
		return pinWrite{updated, true}, nil
	})
	if err != nil {
		return PinSection{}, false, err
	}
	if out.changed {
		s.fireChange()
	}
	return out.section, out.changed, nil
}

// DeleteSection removes a section and cascades all of its assignments.
func (s *PinSectionStore) DeleteSection(sectionID string) (memberCount int, changed bool, err error) {
	if s == nil || s.dbPath == "" {
		return 0, false, nil
	}
	memberCount, err = writeTx(s, "delete pin section "+sectionID, func(tx *sql.Tx) (int, error) {
		section, ok, err := sectionByIDTx(tx, sectionID)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, ErrPinSectionNotFound
		}
		count, err := sessionPinCountTx(tx, sectionID)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(context.Background(), `DELETE FROM pin_section WHERE id = ?`, section.ID); err != nil {
			return 0, err
		}
		return count, nil
	})
	if err != nil {
		return 0, false, err
	}
	s.fireChange()
	return memberCount, true, nil
}

// DeleteSession removes a single (source, session ID) assignment.
func (s *PinSectionStore) DeleteSession(source, sessionID string) (bool, error) {
	if s == nil || s.dbPath == "" {
		return false, nil
	}
	source = NormalizeDecisionSource(source)
	removed, err := writeTx(s, "delete session pin "+pinKeyLabel(source, sessionID), func(tx *sql.Tx) (bool, error) {
		res, err := tx.ExecContext(context.Background(), `DELETE FROM session_pin WHERE source = ? AND session_id = ?`, source, sessionID)
		if err != nil {
			return false, err
		}
		rows, err := res.RowsAffected()
		return rows > 0, err
	})
	if err != nil {
		return false, err
	}
	if removed {
		s.fireChange()
	}
	return removed, nil
}

// Assignments returns every durable session-to-section mapping, keyed by the
// source-qualified identity it is stored under.
func (s *PinSectionStore) Assignments() (map[ArchiveKey]SessionPin, error) {
	out := make(map[ArchiveKey]SessionPin)
	if s == nil || s.dbPath == "" {
		return out, nil
	}
	db, err := s.open()
	if err != nil {
		return out, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), `SELECT source, session_id, section_id, assigned_at FROM session_pin ORDER BY source, session_id`)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var pin SessionPin
		var assignedAt int64
		if err := rows.Scan(&pin.Source, &pin.SessionID, &pin.SectionID, &assignedAt); err != nil {
			return out, err
		}
		pin.AssignedAt = time.Unix(assignedAt, 0).UTC()
		out[SessionPinKey(pin.Source, pin.SessionID)] = pin
	}
	return out, rows.Err()
}

func ensureSectionTx(tx *sql.Tx, display, key string, now time.Time) (PinSection, bool, error) {
	section, ok, err := sectionByKeyTx(tx, key)
	if err != nil {
		return PinSection{}, false, err
	}
	if ok {
		return section, false, nil
	}
	if pinSectionBeforeSectionInsertHook != nil {
		pinSectionBeforeSectionInsertHook()
	}
	id, err := newPinSectionID()
	if err != nil {
		return PinSection{}, false, err
	}
	section = PinSection{ID: id, Name: display, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO pin_section(id, name, name_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, section.ID, display, key, now.Unix(), now.Unix()); err != nil {
		return PinSection{}, false, err
	}
	return section, true, nil
}

func sectionByIDTx(tx *sql.Tx, id string) (PinSection, bool, error) {
	var section PinSection
	var createdAt, updatedAt int64
	if err := tx.QueryRowContext(context.Background(), `SELECT id, name, created_at, updated_at FROM pin_section WHERE id = ?`, id).Scan(&section.ID, &section.Name, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PinSection{}, false, nil
		}
		return PinSection{}, false, err
	}
	section.CreatedAt = time.Unix(createdAt, 0).UTC()
	section.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return section, true, nil
}

func sectionByKeyTx(tx *sql.Tx, key string) (PinSection, bool, error) {
	var section PinSection
	var createdAt, updatedAt int64
	if err := tx.QueryRowContext(context.Background(), `SELECT id, name, created_at, updated_at FROM pin_section WHERE name_key = ?`, key).Scan(&section.ID, &section.Name, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PinSection{}, false, nil
		}
		return PinSection{}, false, err
	}
	section.CreatedAt = time.Unix(createdAt, 0).UTC()
	section.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return section, true, nil
}

func sectionIDByKeyTx(tx *sql.Tx, key string) (string, bool, error) {
	var id string
	if err := tx.QueryRowContext(context.Background(), `SELECT id FROM pin_section WHERE name_key = ?`, key).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

func sessionPinCountTx(tx *sql.Tx, sectionID string) (int, error) {
	var count int
	if err := tx.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM session_pin WHERE section_id = ?`, sectionID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func upsertSessionPinTx(tx *sql.Tx, source, sessionID, sectionID string, now time.Time) (bool, error) {
	res, err := tx.ExecContext(context.Background(), `
INSERT INTO session_pin(source, session_id, section_id, assigned_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(source, session_id) DO UPDATE
SET section_id = excluded.section_id, assigned_at = excluded.assigned_at
	WHERE session_pin.section_id <> excluded.section_id`, source, sessionID, sectionID, now.Unix())
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func newPinSectionID() (string, error) {
	var raw [16]byte
	if _, err := pinSectionRandRead(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func isSQLiteRetryable(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "database is locked") || strings.Contains(s, "sqlite_busy") || strings.Contains(s, "sqlite_locked") || strings.Contains(s, "busy")
}

func isSQLiteUniqueNameConflict(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique constraint failed") && strings.Contains(s, "pin_section.name_key")
}
