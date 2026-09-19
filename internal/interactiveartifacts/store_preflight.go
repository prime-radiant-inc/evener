package interactiveartifacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
)

// Inspect the actual SQLite snapshot, including committed WAL, before allowing
// writable connection pragmas or initialization. Read-only WAL may still use
// sidecars; preflight never requests a journal-mode transition or schema DDL.
func preflightStore(ctx context.Context, uri url.URL) (resultErr error) {
	uri.RawQuery = url.Values{"mode": {"ro"}}.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, db.Close()) }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, tx.Rollback()) }()
	_, err = acceptedStoreSchema(ctx, tx)
	return err
}

// Both preflight and writable initialization enforce this acceptance rule.
// Schema zero means a new empty database, never an unknown existing schema.
func acceptedStoreSchema(ctx context.Context, tx *sql.Tx) (int, error) {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	if version != 0 && version != StoreSchemaVersion {
		return 0, fmt.Errorf("unsupported artifact schema version %d", version)
	}
	if version == 0 {
		var tables int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return 0, err
		}
		if tables != 0 {
			return 0, errors.New("unversioned artifact database has existing tables")
		}
	}
	return version, nil
}
