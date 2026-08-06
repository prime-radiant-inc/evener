package hubcore

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A write from one store must wait out a concurrent writer's brief lock on the
// shared index.db instead of failing immediately with SQLITE_BUSY — this is
// the "Couldn't update archive state: database is locked (5)" bug: the FTS
// rebuild's transaction and an archive click racing on the same file.
func TestArchiveStoreSetWaitsForConcurrentWriter(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := NewArchiveStore(dbPath)
	if err := store.Set("session", "seed", true, time.Unix(1, 0)); err != nil {
		t.Fatalf("seed Set: %v", err)
	}

	// A separate connection (no busy_timeout, like any external writer) takes
	// the write lock and holds it briefly.
	holder, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer func() { _ = holder.Close() }()
	tx, err := holder.Begin()
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO archive (kind, id, archived, decided_at) VALUES ('session', 'holder', 1, 2)`); err != nil {
		t.Fatalf("holder insert: %v", err)
	}
	released := make(chan error, 1)
	go func() {
		// Hold the lock long enough that an immediate-failure open loses the
		// race, then release. The store's busy_timeout must ride this out.
		time.Sleep(200 * time.Millisecond)
		released <- tx.Rollback()
	}()

	if err := store.Set("session", "contended", true, time.Unix(3, 0)); err != nil {
		t.Fatalf("Set during concurrent write lock: %v", err)
	}
	if err := <-released; err != nil {
		t.Fatalf("release lock: %v", err)
	}
}

// Paths containing SQLite URI delimiters ("#", "?", "%") must open the file
// at that literal path, not a truncated one — Go's t.TempDir() embeds fuzz
// seed names like "seed#7" in paths, which is how this bites in practice.
func TestArchiveStoreRoundTripsPathWithURIDelimiters(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "seed#7?x%20")
	dbPath := filepath.Join(dir, "index.db")
	store := NewArchiveStore(dbPath)
	if err := store.Set("session", "s1", true, time.Unix(1, 0)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Decisions()
	if err != nil {
		t.Fatalf("Decisions: %v", err)
	}
	if v, ok := got[ArchiveKey{"session", "s1"}]; !ok || !v {
		t.Fatalf("decision = %v,%v; want true,true", v, ok)
	}
}

// Every store sharing index.db must open it with WAL journaling so readers
// and the writer coexist. WAL mode is persistent in the database file, so a
// plain follow-up connection observes it.
func TestSharedStoresOpenDatabaseInWALMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		write func(t *testing.T, dbPath string)
	}{
		{"archive", func(t *testing.T, dbPath string) {
			t.Helper()
			if err := NewArchiveStore(dbPath).Set("session", "s1", true, time.Unix(1, 0)); err != nil {
				t.Fatalf("archive Set: %v", err)
			}
		}},
		{"favorite", func(t *testing.T, dbPath string) {
			t.Helper()
			if err := NewFavoriteStore(dbPath).Set("session", "s1", true, time.Unix(1, 0)); err != nil {
				t.Fatalf("favorite Set: %v", err)
			}
		}},
		{"pin_section", func(t *testing.T, dbPath string) {
			t.Helper()
			if _, _, err := NewPinSectionStore(dbPath).CreateOrReuseAndAssign("inbox", "s1", time.Unix(1, 0)); err != nil {
				t.Fatalf("pin section assign: %v", err)
			}
		}},
		{"past_fts", func(t *testing.T, dbPath string) {
			t.Helper()
			if err := NewPastIndexWithDB("", dbPath).rebuildFTS(nil); err != nil {
				t.Fatalf("rebuildFTS: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath := filepath.Join(t.TempDir(), "index.db")
			tc.write(t, dbPath)

			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer func() { _ = db.Close() }()
			var mode string
			if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
				t.Fatalf("query journal_mode: %v", err)
			}
			if mode != "wal" {
				t.Fatalf("journal_mode = %q, want %q", mode, "wal")
			}
		})
	}
}
