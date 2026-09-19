package interactiveartifacts

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSchemaRefusalDoesNotEnableWAL(t *testing.T) {
	for _, version := range []int{0, 1, 200} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private", "refused.sqlite")
			requireNoError(t, os.Mkdir(filepath.Dir(path), 0700))
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			requireNoError(t, err)
			requireNoError(t, file.Close())
			db, err := sql.Open("sqlite", path)
			requireNoError(t, err)
			_, err = db.Exec("CREATE TABLE marker(value TEXT NOT NULL); INSERT INTO marker VALUES('retained')")
			requireNoError(t, err)
			_, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version))
			requireNoError(t, err)
			requireNoError(t, db.Close())
			before, err := os.ReadFile(path)
			requireNoError(t, err)
			info, err := os.Stat(path)
			requireNoError(t, err)
			noSidecars(t, path)
			s, err := OpenStore(path, StoreOptions{})
			if err == nil {
				requireNoError(t, s.Close())
				t.Fatal("accepted refused schema")
			}
			after, err := os.ReadFile(path)
			requireNoError(t, err)
			if !bytes.Equal(before, after) {
				t.Error("schema refusal changed main database bytes")
			}
			afterInfo, err := os.Stat(path)
			requireNoError(t, err)
			if afterInfo.Mode() != info.Mode() {
				t.Error("schema refusal changed file mode")
			}
			noSidecars(t, path)
			db, err = sql.Open("sqlite", path)
			requireNoError(t, err)
			defer db.Close()
			var storedVersion, tables, indexes int
			var journal, marker string
			requireNoError(t, db.QueryRow("PRAGMA user_version").Scan(&storedVersion))
			requireNoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&journal))
			requireNoError(t, db.QueryRow("SELECT value FROM marker").Scan(&marker))
			requireNoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables))
			requireNoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index'").Scan(&indexes))
			if storedVersion != version || journal != "delete" || marker != "retained" || tables != 1 || indexes != 0 {
				t.Fatalf("refusal changed database: version=%d journal=%s marker=%s tables=%d indexes=%d", storedVersion, journal, marker, tables, indexes)
			}
		})
	}
}
func noSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected sidecar %s: %v", suffix, err)
		}
	}
}

func TestStorePreflightReadsCommittedWAL(t *testing.T) {
	s, hash, path := setupStore(t, StoreOptions{})
	ctx := context.Background()
	// Keep this connection open so committed WAL content cannot be checkpointed
	// by closing the last connection. Put refused version 1 in the main file,
	// then commit current version and content only to WAL.
	conn, err := s.db.Conn(ctx)
	requireNoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "PRAGMA wal_autocheckpoint=0")
	requireNoError(t, err)
	_, err = conn.ExecContext(ctx, "PRAGMA user_version=1")
	requireNoError(t, err)
	_, err = conn.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	requireNoError(t, err)
	mainBefore, err := os.ReadFile(path)
	requireNoError(t, err)
	_, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", StoreSchemaVersion))
	requireNoError(t, err)
	created, err := s.Publish(ctx, hash, createJSON("in WAL"), PublicationOrigin{})
	requireNoError(t, err)
	mainAfter, err := os.ReadFile(path)
	requireNoError(t, err)
	if !bytes.Equal(mainBefore, mainAfter) {
		t.Fatal("fixture checkpointed current version into main database")
	}
	reopened := openTestStore(t, path, StoreOptions{Clock: fixedClock})
	requireNoError(t, reopened.InstallGrant(ctx, hash, testScope()))
	retry, err := reopened.Publish(ctx, hash, createJSON("in WAL"), PublicationOrigin{})
	requireNoError(t, err)
	if retry != created {
		t.Fatal("preflight/open lost committed WAL receipt")
	}
	assertUsage(t, reopened)
}

func TestStoreEmptyAndCurrentConnectionsKeepDurablePragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "store.sqlite")
	for range 2 {
		s := openTestStore(t, path, StoreOptions{ReaderConnections: 2})
		var held []*sql.Conn
		for range 3 {
			conn, err := s.db.Conn(context.Background())
			requireNoError(t, err)
			held = append(held, conn)
			var journal string
			var sync, foreign, version int
			requireNoError(t, conn.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journal))
			requireNoError(t, conn.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&sync))
			requireNoError(t, conn.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&foreign))
			requireNoError(t, conn.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version))
			if journal != "wal" || sync != 2 || foreign != 1 || version != StoreSchemaVersion {
				t.Fatalf("connection durability: %s %d %d schema %d", journal, sync, foreign, version)
			}
		}
		for _, conn := range held {
			requireNoError(t, conn.Close())
		}
		requireNoError(t, s.Close())
	}
}
