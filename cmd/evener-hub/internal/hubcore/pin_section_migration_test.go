package hubcore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// seedLegacyIndexStore writes the pre-source-qualification index.db schema:
// archive and favorite keyed by (kind, id), session_pin keyed by session_id
// alone. Real legacy rows are inserted so the migration must preserve them.
//
// The pins mirror what the legacy (bare-keyed) store actually held: a local
// pin is the controller session's bare ID, while a pin on a remote host's
// session was written under that session's host-qualified ref string.
func seedLegacyIndexStore(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE pin_section (
		id         TEXT    NOT NULL PRIMARY KEY,
		name       TEXT    NOT NULL,
		name_key   TEXT    NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pin_section (id, name, name_key, created_at, updated_at) VALUES ('sec-legacy', 'Legacy', 'legacy', 100, 100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session_pin (
		session_id  TEXT    NOT NULL PRIMARY KEY,
		section_id  TEXT    NOT NULL REFERENCES pin_section(id) ON DELETE CASCADE,
		assigned_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		sessionID string
		assigned  int64
	}{
		{"th_1", 111},
		{"host-a:th_1", 222},
		{"local:th_2", 333},
	} {
		if _, err := db.Exec(`INSERT INTO session_pin (session_id, section_id, assigned_at) VALUES (?, 'sec-legacy', ?)`, row.sessionID, row.assigned); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE archive (
		kind       TEXT    NOT NULL,
		id         TEXT    NOT NULL,
		archived   INTEGER NOT NULL,
		decided_at INTEGER NOT NULL,
		PRIMARY KEY (kind, id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO archive (kind, id, archived, decided_at) VALUES ('project', 'legacy-proj', 1, 100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE favorite (
		kind       TEXT    NOT NULL,
		id         TEXT    NOT NULL,
		favorited  INTEGER NOT NULL,
		decided_at INTEGER NOT NULL,
		PRIMARY KEY (kind, id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO favorite (kind, id, favorited, decided_at) VALUES ('project', 'legacy-proj', 1, 100)`); err != nil {
		t.Fatal(err)
	}
}

type sessionPinColumn struct {
	name string
	pk   int
}

func readTableColumns(t *testing.T, dbPath, table string) map[string]sessionPinColumn {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT name, pk FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]sessionPinColumn)
	for rows.Next() {
		var column sessionPinColumn
		if err := rows.Scan(&column.name, &column.pk); err != nil {
			t.Fatal(err)
		}
		out[column.name] = column
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPinSectionStoreMigratesLegacyTableToSourceQualifiedKey pins the shared
// schema migration: a store that predates the source dimension is rebuilt with
// PRIMARY KEY (source, session_id) in one transaction, and every pin it held
// keeps addressing the session it addressed before — the controller's own
// bare-ID row as the local source, a host-qualified remote row under its host.
func TestPinSectionStoreMigratesLegacyTableToSourceQualifiedKey(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seedLegacyIndexStore(t, dbPath)

	store := NewPinSectionStore(dbPath)
	sections, err := store.Sections()
	if err != nil {
		t.Fatalf("Sections after migration: %v", err)
	}
	if len(sections) != 1 || sections[0].MemberCount != 3 {
		t.Fatalf("sections after migration = %+v, want the legacy section with every pin", sections)
	}

	columns := readTableColumns(t, dbPath, "session_pin")
	source, ok := columns["source"]
	if !ok {
		t.Fatalf("session_pin columns after migration = %+v, want a source column", columns)
	}
	sessionID, ok := columns["session_id"]
	if !ok {
		t.Fatalf("session_pin columns after migration = %+v, want session_id", columns)
	}
	if source.pk == 0 || sessionID.pk == 0 {
		t.Fatalf("session_pin primary key = source pk %d, session_id pk %d, want a composite key", source.pk, sessionID.pk)
	}

	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT source, session_id, section_id, assigned_at FROM session_pin ORDER BY source, session_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	type pinRow struct {
		source, sessionID, sectionID string
		assignedAt                   int64
	}
	var got []pinRow
	for rows.Next() {
		var row pinRow
		if err := rows.Scan(&row.source, &row.sessionID, &row.sectionID, &row.assignedAt); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []pinRow{
		{source: "", sessionID: "th_1", sectionID: "sec-legacy", assignedAt: 111},
		// A "local:" spelling predates the source dimension as surely as a
		// bare ID does, and collapses onto the same controller key.
		{source: "", sessionID: "th_2", sectionID: "sec-legacy", assignedAt: 333},
		{source: "host-a", sessionID: "th_1", sectionID: "sec-legacy", assignedAt: 222},
	}
	if len(got) != len(want) {
		t.Fatalf("migrated pins = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("migrated pins[%d] = %+v, want %+v (all: %+v)", index, got[index], want[index], got)
		}
	}

	var userVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if userVersion < 1 {
		t.Fatalf("user_version after migration = %d, want the shared schema version", userVersion)
	}
}

// TestPinSectionStoreLegacyTableMigrationSharesOneTransaction pins the
// centralized step: a single store open upgrades every legacy table in
// index.db, not just the table that store owns.
func TestPinSectionStoreLegacyTableMigrationSharesOneTransaction(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seedLegacyIndexStore(t, dbPath)

	store := NewPinSectionStore(dbPath)
	if _, err := store.Sections(); err != nil {
		t.Fatalf("Sections after migration: %v", err)
	}
	// One pin-store open migrated every legacy table in the shared file,
	// including the two this store does not own.
	for _, table := range []string{"archive", "favorite"} {
		if _, ok := readTableColumns(t, dbPath, table)["source"]; !ok {
			t.Fatalf("%s was not upgraded by the pin store's shared migration", table)
		}
	}

	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	archiveDecisions := NewArchiveStore(dbPath)
	decisions, err := archiveDecisions.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !decisions[ArchiveKey{Kind: "project", ID: "legacy-proj"}] {
		t.Fatalf("legacy archive decision lost by the shared migration: %+v", decisions)
	}
	favorites, err := NewFavoriteStore(dbPath).Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if !favorites[ArchiveKey{Kind: "project", ID: "legacy-proj"}] {
		t.Fatalf("legacy favorite decision lost by the shared migration: %+v", favorites)
	}
	// The rebuilt table keeps its foreign key: deleting the section still
	// cascades every surviving assignment.
	if _, err := db.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM pin_section WHERE id = 'sec-legacy'`); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM session_pin`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("session_pin rows after section delete = %d, want cascade to remove both", remaining)
	}
}

// TestIndexSchemaMigrationRunsFromAnyStoreOpen pins that the shared upgrade
// is not the pin store's private step: whichever store reaches index.db first
// upgrades the whole file, so a store that later reads the pin table (or the
// pin store itself) never meets a pre-source schema.
func TestIndexSchemaMigrationRunsFromAnyStoreOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seedLegacyIndexStore(t, dbPath)

	// The archive store owns neither pin table; opening it must still migrate
	// the legacy session_pin rows.
	if _, err := NewArchiveStore(dbPath).Decisions(); err != nil {
		t.Fatalf("archive store open: %v", err)
	}
	columns := readTableColumns(t, dbPath, "session_pin")
	if _, ok := columns["source"]; !ok {
		t.Fatalf("session_pin columns after an archive-store open = %+v, want the source column", columns)
	}
	store := NewPinSectionStore(dbPath)
	assignments, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 3 {
		t.Fatalf("assignments after an archive-store open = %+v, want every legacy pin", assignments)
	}
	for _, key := range []ArchiveKey{
		{Kind: "session", ID: "th_1"},
		{Kind: "session", ID: "th_2"},
		{Kind: "session", ID: "th_1", Source: "host-a"},
	} {
		if _, ok := assignments[key]; !ok {
			t.Fatalf("assignments after an archive-store open = %+v, want %+v", assignments, key)
		}
	}
}

// TestPinSectionStoreMigrationKeepsNewestCollidingPin pins the survivor rule
// when two legacy spellings normalize to one controller key: the most recently
// assigned row keeps the pin, not whichever spelling sorts last.
func TestPinSectionStoreMigrationKeepsNewestCollidingPin(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seedLegacyIndexStore(t, dbPath)
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// "local:th_1" is the seeded bare "th_1" pin's controller session, assigned
	// later, so the migration must keep this row's assignment.
	if _, err := db.Exec(`INSERT INTO session_pin (session_id, section_id, assigned_at) VALUES ('local:th_1', 'sec-legacy', 999)`); err != nil {
		t.Fatal(err)
	}

	store := NewPinSectionStore(dbPath)
	assignments, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	kept, ok := assignments[SessionPinKey("", "th_1")]
	if !ok {
		t.Fatalf("assignments after migration = %+v, want the controller th_1 pin", assignments)
	}
	if kept.AssignedAt != time.Unix(999, 0).UTC() {
		t.Fatalf("colliding pin survivor = %+v, want the newest assignment (assigned_at 999)", kept)
	}
	if _, ok := assignments[ArchiveKey{Kind: "session", ID: "th_1", Source: "local"}]; ok {
		t.Fatalf("assignments after migration = %+v, want the local spelling folded onto the controller key", assignments)
	}
}

// TestPinSectionStoreMigrationTieBreaksCollidingPinsDeterministically pins the
// determinism half of that rule: two legacy spellings of one controller
// session assigned at the same instant keep the lexically smaller stored
// identity, so the survivor never depends on the order rows are scanned.
func TestPinSectionStoreMigrationTieBreaksCollidingPinsDeterministically(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	seedLegacyIndexStore(t, dbPath)
	db, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO pin_section (id, name, name_key, created_at, updated_at) VALUES ('sec-tie', 'Tie', 'tie', 100, 100)`); err != nil {
		t.Fatal(err)
	}
	// The seeded bare "th_1" pin is assigned at 111; this "local:th_1" row is
	// the same controller session at the same instant. "local:th_1" sorts
	// before "th_1", so the tie must keep this row.
	if _, err := db.Exec(`INSERT INTO session_pin (session_id, section_id, assigned_at) VALUES ('local:th_1', 'sec-tie', 111)`); err != nil {
		t.Fatal(err)
	}

	store := NewPinSectionStore(dbPath)
	assignments, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	kept, ok := assignments[SessionPinKey("", "th_1")]
	if !ok {
		t.Fatalf("assignments after migration = %+v, want the controller th_1 pin", assignments)
	}
	if kept.SectionID != "sec-tie" || kept.AssignedAt != time.Unix(111, 0).UTC() {
		t.Fatalf("colliding pin survivor = %+v, want the lexically smaller stored identity's row (section sec-tie)", kept)
	}
}
