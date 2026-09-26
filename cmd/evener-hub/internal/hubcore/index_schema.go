package hubcore

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"primeradiant.com/evener/hubapi"
)

// indexSchemaVersion versions the shared index.db schema. index.db is written
// by the archive, favorite, and pin stores (and the past index's FTS mirror),
// so its header reserves PRAGMA user_version for schema versioning — see
// sqlite_dsn.go.
const indexSchemaVersion = 1

var (
	// indexMigrationMu serializes the legacy-table rebuild within this process
	// so two opens cannot both decide to migrate before either has taken
	// SQLite's write lock.
	indexMigrationMu sync.Mutex
	// indexMigrationInterleave runs between the schema pre-check and the
	// serialized rebuild. It is a deterministic test seam for a competing
	// migrator; nil in production.
	indexMigrationInterleave func()
)

// ensureIndexSchema upgrades the shared index.db to indexSchemaVersion in one
// BEGIN IMMEDIATE transaction, before the store that opened it serves.
//
// The rebuild is serialized two ways. indexMigrationMu keeps two in-process
// opens from both deciding to migrate before either takes the write lock, and
// the immediate transaction that follows takes SQLite's write lock before the
// schema is rechecked, so a migrator in another connection or process that won
// the race is observed and its already-upgraded tables are left alone.
// Rebuilding an upgraded table would insert its source-qualified rows under
// the controller source, collapsing two hosts' same-ID rows into one.
//
// Every legacy row is a controller-local decision, so it migrates under the
// empty controller source, except a pin row that already carried a remote
// session's host-qualified ref: those keep addressing the host they named.
// A freshly created table already carries the source column, so the rebuilds
// are a one-time no-op for new databases, and a table this binary never
// creates is skipped (it cannot exist yet).
func ensureIndexSchema(db *sql.DB) error {
	if db == nil {
		return nil
	}
	current, err := readIndexSchemaVersion(db)
	if err != nil || current >= indexSchemaVersion {
		return err
	}
	if indexMigrationInterleave != nil {
		indexMigrationInterleave()
	}
	indexMigrationMu.Lock()
	defer indexMigrationMu.Unlock()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	// BEGIN IMMEDIATE, not database/sql's deferred Begin: the recheck below
	// must run while this connection already holds the write lock.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	version, err := readIndexSchemaVersionContext(ctx, conn)
	if err != nil {
		return err
	}
	if version < indexSchemaVersion {
		if err := rebuildLegacyIndexTables(ctx, conn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", indexSchemaVersion)); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// readIndexSchemaVersion reads the shared schema version from the database
// header.
func readIndexSchemaVersion(db *sql.DB) (int, error) {
	return readIndexSchemaVersionContext(context.Background(), db)
}

func readIndexSchemaVersionContext(ctx context.Context, q rowQuerier) (int, error) {
	var version int
	if err := q.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

// rebuildLegacyIndexTables rebuilds every present table whose primary key
// predates the source dimension, inside the caller's transaction.
func rebuildLegacyIndexTables(ctx context.Context, conn *sql.Conn) error {
	for _, table := range []struct {
		name        string
		createTable string
		valueColumn string
	}{
		{name: "favorite", createTable: createFavoriteTable, valueColumn: "favorited"},
		{name: "archive", createTable: createArchiveTable, valueColumn: "archived"},
	} {
		exists, err := tableExistsContext(ctx, conn, table.name)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		has, err := tableHasColumnContext(ctx, conn, table.name, decisionSourceColumn)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if err := rebuildDecisionTable(ctx, conn, table.name, table.createTable, table.valueColumn); err != nil {
			return err
		}
	}
	exists, err := tableExistsContext(ctx, conn, "session_pin")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	has, err := tableHasColumnContext(ctx, conn, "session_pin", decisionSourceColumn)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	return rebuildSessionPinTable(ctx, conn)
}

// rebuildDecisionTable replaces a legacy (kind, id)-keyed decision table with
// the source-qualified shape, copying every row under the controller source.
func rebuildDecisionTable(ctx context.Context, conn *sql.Conn, table, createTable, valueColumn string) error {
	legacy := table + "_legacy"
	statements := []string{
		"DROP TABLE IF EXISTS " + legacy,
		"ALTER TABLE " + table + " RENAME TO " + legacy,
		createTable,
		"INSERT INTO " + table + " (source, kind, id, " + valueColumn + ", decided_at) SELECT '', kind, id, " + valueColumn + ", decided_at FROM " + legacy,
		"DROP TABLE " + legacy,
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// rebuildSessionPinTable replaces the legacy session_id-keyed pin table with
// the (source, session_id) shape. Unlike a decision's ID, a legacy pin's
// session_id could already be a remote session's host-qualified ref string —
// the identity the pre-source store wrote for a remote pin — so a row that
// parses as a non-controller ref migrates under the host it names and keeps
// pinning that host's session. A bare ID (or a "local:" spelling) is the
// controller's own and migrates under the empty controller source.
func rebuildSessionPinTable(ctx context.Context, conn *sql.Conn) error {
	const legacy = "session_pin_legacy"
	for _, statement := range []string{
		"DROP TABLE IF EXISTS " + legacy,
		"ALTER TABLE session_pin RENAME TO " + legacy,
		createSessionPinTable,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	rows, err := conn.QueryContext(ctx, `SELECT session_id, section_id, assigned_at FROM `+legacy+` ORDER BY session_id`)
	if err != nil {
		return err
	}
	type legacyPin struct {
		sessionID  string
		sectionID  string
		assignedAt int64
	}
	var pins []legacyPin
	for rows.Next() {
		var pin legacyPin
		if err := rows.Scan(&pin.sessionID, &pin.sectionID, &pin.assignedAt); err != nil {
			_ = rows.Close()
			return err
		}
		pins = append(pins, pin)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, pin := range pins {
		source, sessionID := splitLegacyPinSessionID(pin.sessionID)
		if _, err := conn.ExecContext(ctx, `
INSERT OR REPLACE INTO session_pin(source, session_id, section_id, assigned_at)
VALUES (?, ?, ?, ?)`, source, sessionID, pin.sectionID, pin.assignedAt); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE "+legacy); err != nil {
		return err
	}
	return nil
}

// splitLegacyPinSessionID maps a pre-source pin's stored session identity to
// the source-qualified key. A host-qualified ref keeps its host; every other
// spelling is the controller's own session.
func splitLegacyPinSessionID(stored string) (source, sessionID string) {
	ref, err := hubapi.ParseRef(stored)
	if err != nil {
		return "", stored
	}
	source = NormalizeDecisionSource(ref.HostID)
	return source, ref.SessionID
}

// tableExistsContext reports whether the named table exists in the database.
func tableExistsContext(ctx context.Context, q rowQuerier, table string) (bool, error) {
	var name string
	rows, err := q.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return name != "", nil
}
