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

// SessionPin records the durable section assignment for one session.
type SessionPin struct {
	SessionID  string
	SectionID  string
	AssignedAt time.Time
}

// LegacyPinDecision carries a legacy favorite row classification into the
// pin-section migration.
type LegacyPinDecision struct {
	StoredID       string
	Classification FavoriteDecisionClassification
}

// PinSectionStore persists named pin sections and their session assignments in
// the same SQLite index DB as archive/favorite decisions.
type PinSectionStore struct {
	dbPath string
	fs     afero.Fs
	openDB func(driverName, dataSourceName string) (*sql.DB, error)
}

// NewPinSectionStore returns a store backed by the SQLite file at dbPath.
func NewPinSectionStore(dbPath string) *PinSectionStore {
	return &PinSectionStore{dbPath: dbPath, fs: afero.NewOsFs(), openDB: sql.Open}
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
  session_id  TEXT    NOT NULL PRIMARY KEY,
  section_id  TEXT    NOT NULL REFERENCES pin_section(id) ON DELETE CASCADE,
  assigned_at INTEGER NOT NULL
)`

const createHubSchemaMigrationTable = `
CREATE TABLE IF NOT EXISTS hub_schema_migration (
  name       TEXT    NOT NULL PRIMARY KEY,
  applied_at INTEGER NOT NULL
)`

func (s *PinSectionStore) open() (*sql.DB, error) {
	if s == nil || s.dbPath == "" {
		return nil, nil
	}
	if err := s.fs.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := s.openDB("sqlite", s.dbPath)
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
	for _, stmt := range []string{createPinSectionTable, createSessionPinTable, createHubSchemaMigrationTable, createFavoriteTable} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			_ = db.Close()
			return nil, err
		}
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

// Assign moves sessionID to sectionID, updating assigned_at only when the
// section actually changes.
func (s *PinSectionStore) Assign(sectionID, sessionID string, now time.Time) (PinSection, bool, error) {
	if s == nil || s.dbPath == "" {
		return PinSection{}, false, nil
	}
	for range 8 {
		db, err := s.open()
		if err != nil {
			return PinSection{}, false, err
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		section, ok, err := sectionByIDTx(tx, sectionID)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		if !ok {
			_ = tx.Rollback()
			_ = db.Close()
			return PinSection{}, false, ErrPinSectionNotFound
		}
		changed, err := upsertSessionPinTx(tx, sessionID, sectionID, now)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		section.MemberCount, err = sessionPinCountTx(tx, section.ID)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		if pinSectionBeforeAssignmentCommitHook != nil {
			pinSectionBeforeAssignmentCommitHook()
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		_ = db.Close()
		return section, changed, nil
	}
	return PinSection{}, false, fmt.Errorf("assign %s: retry limit reached", sessionID)
}

// CreateOrReuseAndAssign creates a section when needed, reuses an existing
// case-folded match, and assigns the session in the same transaction.
func (s *PinSectionStore) CreateOrReuseAndAssign(name, sessionID string, now time.Time) (PinSection, bool, error) {
	if s == nil || s.dbPath == "" {
		return PinSection{}, false, nil
	}
	display, key, err := NormalizePinSectionName(name)
	if err != nil {
		return PinSection{}, false, err
	}
	for range 8 {
		db, err := s.open()
		if err != nil {
			return PinSection{}, false, err
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		section, created, err := ensureSectionTx(tx, display, key, now)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteUniqueNameConflict(err) || isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		changed, err := upsertSessionPinTx(tx, sessionID, section.ID, now)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		section.MemberCount, err = sessionPinCountTx(tx, section.ID)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		if pinSectionBeforeAssignmentCommitHook != nil {
			pinSectionBeforeAssignmentCommitHook()
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		_ = db.Close()
		return section, created || changed, nil
	}
	return PinSection{}, false, fmt.Errorf("create or reuse pin section %q: retry limit reached", display)
}

// Unpin removes one session assignment while leaving its section intact.
func (s *PinSectionStore) Unpin(sessionID string) (bool, error) {
	return s.DeleteSession(sessionID)
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
	for range 8 {
		db, err := s.open()
		if err != nil {
			return PinSection{}, false, err
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		section, ok, err := sectionByIDTx(tx, sectionID)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		if !ok {
			_ = tx.Rollback()
			_ = db.Close()
			return PinSection{}, false, ErrPinSectionNotFound
		}
		section.MemberCount, err = sessionPinCountTx(tx, section.ID)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		if section.Name == display {
			_ = tx.Rollback()
			_ = db.Close()
			return section, false, nil
		}
		otherID, found, err := sectionIDByKeyTx(tx, key)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		if found && otherID != sectionID {
			_ = tx.Rollback()
			_ = db.Close()
			return PinSection{}, false, ErrPinSectionConflict
		}
		if _, err := tx.ExecContext(context.Background(), `UPDATE pin_section SET name = ?, name_key = ?, updated_at = ? WHERE id = ?`, display, key, now.Unix(), sectionID); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			if isSQLiteUniqueNameConflict(err) {
				return PinSection{}, false, ErrPinSectionConflict
			}
			return PinSection{}, false, err
		}
		updated, ok, err := sectionByIDTx(tx, sectionID)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		if !ok {
			_ = tx.Rollback()
			_ = db.Close()
			return PinSection{}, false, ErrPinSectionNotFound
		}
		updated.MemberCount = section.MemberCount
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return PinSection{}, false, err
		}
		_ = db.Close()
		return updated, true, nil
	}
	return PinSection{}, false, fmt.Errorf("rename pin section %s: retry limit reached", sectionID)
}

// DeleteSection removes a section and cascades all of its assignments.
func (s *PinSectionStore) DeleteSection(sectionID string) (memberCount int, changed bool, err error) {
	if s == nil || s.dbPath == "" {
		return 0, false, nil
	}
	for range 8 {
		db, err := s.open()
		if err != nil {
			return 0, false, err
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return 0, false, err
		}
		section, ok, err := sectionByIDTx(tx, sectionID)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return 0, false, err
		}
		if !ok {
			_ = tx.Rollback()
			_ = db.Close()
			return 0, false, ErrPinSectionNotFound
		}
		memberCount, err = sessionPinCountTx(tx, sectionID)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return 0, false, err
		}
		if _, err := tx.ExecContext(context.Background(), `DELETE FROM pin_section WHERE id = ?`, section.ID); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return 0, false, err
		}
		_ = db.Close()
		return memberCount, true, nil
	}
	return 0, false, fmt.Errorf("delete pin section %s: retry limit reached", sectionID)
}

// DeleteSession removes a single assignment by session ID.
func (s *PinSectionStore) DeleteSession(sessionID string) (bool, error) {
	if s == nil || s.dbPath == "" {
		return false, nil
	}
	for range 8 {
		db, err := s.open()
		if err != nil {
			return false, err
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return false, err
		}
		res, err := tx.ExecContext(context.Background(), `DELETE FROM session_pin WHERE session_id = ?`, sessionID)
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return false, err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return false, err
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			if isSQLiteRetryable(err) {
				continue
			}
			return false, err
		}
		_ = db.Close()
		return rows > 0, nil
	}
	return false, fmt.Errorf("delete session pin %s: retry limit reached", sessionID)
}

// Assignments returns every durable session-to-section mapping.
func (s *PinSectionStore) Assignments() (map[string]SessionPin, error) {
	out := make(map[string]SessionPin)
	if s == nil || s.dbPath == "" {
		return out, nil
	}
	db, err := s.open()
	if err != nil {
		return out, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), `SELECT session_id, section_id, assigned_at FROM session_pin ORDER BY session_id`)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var pin SessionPin
		var assignedAt int64
		if err := rows.Scan(&pin.SessionID, &pin.SectionID, &assignedAt); err != nil {
			return out, err
		}
		pin.AssignedAt = time.Unix(assignedAt, 0).UTC()
		out[pin.SessionID] = pin
	}
	return out, rows.Err()
}

// MigrateLegacy promotes legacy session favorite rows into the Pinned section
// and removes all legacy session favorites in one transaction.
func (s *PinSectionStore) MigrateLegacy(decisions []LegacyPinDecision, now time.Time) (bool, error) {
	if s == nil || s.dbPath == "" {
		return false, nil
	}
	db, err := s.open()
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return false, err
	}
	var applied int
	if err := tx.QueryRowContext(context.Background(), `SELECT 1 FROM hub_schema_migration WHERE name = ?`, "named-pin-sections-v1").Scan(&applied); err == nil {
		_ = tx.Rollback()
		return false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return false, err
	}
	classificationByID := make(map[string]LegacyPinDecision, len(decisions))
	for _, decision := range decisions {
		classificationByID[decision.StoredID] = decision
	}
	rows, err := tx.QueryContext(context.Background(), `SELECT id, favorited FROM favorite WHERE kind = 'session'`)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	defer func() { _ = rows.Close() }()
	var assignments []struct {
		sessionID string
		sectionID string
	}
	for rows.Next() {
		var sessionID string
		var favorited int
		if err := rows.Scan(&sessionID, &favorited); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		if favorited != 1 {
			continue
		}
		decision, ok := classificationByID[sessionID]
		if !ok {
			continue
		}
		switch decision.Classification.State {
		case FavoriteDecisionValid:
			if decision.Classification.CanonicalKey.ID != "" {
				assignments = append(assignments, struct {
					sessionID string
					sectionID string
				}{sessionID: decision.Classification.CanonicalKey.ID, sectionID: "Pinned"})
			}
		case FavoriteDecisionDormant:
			assignments = append(assignments, struct {
				sessionID string
				sectionID string
			}{sessionID: decision.StoredID, sectionID: "Pinned"})
		case FavoriteDecisionConfirmedInvalid:
			// discarded
		}
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if len(assignments) > 0 {
		section, _, err := ensureSectionTx(tx, "Pinned", cases.Fold().String("Pinned"), now)
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
		for _, assignment := range assignments {
			if _, err := upsertSessionPinTx(tx, assignment.sessionID, section.ID, now); err != nil {
				_ = tx.Rollback()
				return false, err
			}
		}
	}
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM favorite WHERE kind = 'session'`); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO hub_schema_migration(name, applied_at) VALUES (?, ?)`, "named-pin-sections-v1", now.Unix()); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	return true, nil
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

func upsertSessionPinTx(tx *sql.Tx, sessionID, sectionID string, now time.Time) (bool, error) {
	res, err := tx.ExecContext(context.Background(), `
INSERT INTO session_pin(session_id, section_id, assigned_at)
VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE
SET section_id = excluded.section_id, assigned_at = excluded.assigned_at
	WHERE session_pin.section_id <> excluded.section_id`, sessionID, sectionID, now.Unix())
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
