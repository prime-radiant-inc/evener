package hubcore

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// The store DSN must relax WAL's per-commit write-ahead-log fsync. SQLite's
// default synchronous level is FULL (modernc compiles
// SQLITE_DEFAULT_SYNCHRONOUS = 2), and switching the journal to WAL does not
// change it, so every store write fsyncs until the DSN asks for NORMAL.
//
// The pragma has no string spelling: PRAGMA synchronous reports an integer,
// 0=OFF, 1=NORMAL, 2=FULL, 3=EXTRA. It is also per connection, unlike the
// persistent journal_mode, so this reads it through each store's own open
// path rather than through a fresh connection that would report SQLite's
// default instead of the store's setting.
func TestSharedStoresOpenWithSynchronousNormal(t *testing.T) {
	t.Parallel()
	const (
		synchronousOff    = 0
		synchronousNormal = 1
		synchronousFull   = 2
		synchronousExtra  = 3
	)
	readSynchronous := func(t *testing.T, db *sql.DB) int {
		t.Helper()
		var level int
		if err := db.QueryRow(`PRAGMA synchronous`).Scan(&level); err != nil {
			t.Fatalf("query synchronous: %v", err)
		}
		switch level {
		case synchronousOff, synchronousNormal, synchronousFull, synchronousExtra:
		default:
			t.Fatalf("synchronous = %d, not a SQLite synchronous level", level)
		}
		return level
	}
	cases := []struct {
		name    string
		want    int
		observe func(t *testing.T, dbPath string) int
	}{
		{"archive", synchronousNormal, func(t *testing.T, dbPath string) int {
			t.Helper()
			db, err := NewArchiveStore(dbPath).open()
			if err != nil {
				t.Fatalf("archive open: %v", err)
			}
			defer func() { _ = db.Close() }()
			return readSynchronous(t, db)
		}},
		{"favorite", synchronousNormal, func(t *testing.T, dbPath string) int {
			t.Helper()
			db, err := NewFavoriteStore(dbPath).open()
			if err != nil {
				t.Fatalf("favorite open: %v", err)
			}
			defer func() { _ = db.Close() }()
			return readSynchronous(t, db)
		}},
		{"pin_section", synchronousNormal, func(t *testing.T, dbPath string) int {
			t.Helper()
			db, err := NewPinSectionStore(dbPath).open()
			if err != nil {
				t.Fatalf("pin section open: %v", err)
			}
			defer func() { _ = db.Close() }()
			return readSynchronous(t, db)
		}},
		{"session_seen", synchronousNormal, func(t *testing.T, dbPath string) int {
			t.Helper()
			db, err := NewSessionSeenStore(dbPath).open()
			if err != nil {
				t.Fatalf("session seen open: %v", err)
			}
			defer func() { _ = db.Close() }()
			return readSynchronous(t, db)
		}},
		{"hub_identity", synchronousNormal, func(t *testing.T, dbPath string) int {
			t.Helper()
			db, err := NewHubIdentityStore(dbPath).open()
			if err != nil {
				t.Fatalf("hub identity open: %v", err)
			}
			defer func() { _ = db.Close() }()
			return readSynchronous(t, db)
		}},
		{"past_fts", synchronousNormal, func(t *testing.T, dbPath string) int {
			t.Helper()
			index := closeOnCleanup(t, NewPastIndexWithDB("", dbPath))
			level := -1
			// The FTS writer opens, uses, and closes its own connection, so
			// observe the handle it was handed and read the pragma there.
			index.openDB = func(driver, dsn string) (*sql.DB, error) {
				db, err := sql.Open(driver, dsn)
				if err != nil {
					return nil, err
				}
				level = readSynchronous(t, db)
				return db, nil
			}
			if err := index.rebuildFTS(nil); err != nil {
				t.Fatalf("rebuildFTS: %v", err)
			}
			return level
		}},
		// The voice journal is the one connection that deliberately keeps FULL:
		// it carries durable input custody, which this relaxation must not
		// silently weaken. A single synchronous pragma in its DSN is what makes
		// this hold, because the driver orders pragmas itself (busy_timeout
		// first, then lexicographically) rather than applying them in the order
		// they appear in the DSN.
		{"voice_journal", synchronousFull, func(t *testing.T, _ string) int {
			t.Helper()
			journal, err := NewVoiceJournal(t.TempDir())
			if err != nil {
				t.Fatalf("voice journal open: %v", err)
			}
			defer func() { _ = journal.Close() }()
			return readSynchronous(t, journal.db)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath := filepath.Join(t.TempDir(), "index.db")
			if got := tc.observe(t, dbPath); got != tc.want {
				t.Fatalf("synchronous = %d, want %d", got, tc.want)
			}
		})
	}
}

// A write from one store must wait out a concurrent writer's brief lock on the
// shared index.db instead of failing immediately with SQLITE_BUSY — this is
// the "Couldn't update archive state: database is locked (5)" bug: the FTS
// rebuild's transaction and an archive click racing on the same file.
func TestArchiveStoreSetWaitsForConcurrentWriter(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	store := closeOnCleanup(t, NewArchiveStore(dbPath))
	if err := store.Set("", "session", "seed", true, time.Unix(1, 0)); err != nil {
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

	if err := store.Set("", "session", "contended", true, time.Unix(3, 0)); err != nil {
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
	store := closeOnCleanup(t, NewArchiveStore(dbPath))
	if err := store.Set("", "session", "s1", true, time.Unix(1, 0)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Decisions()
	if err != nil {
		t.Fatalf("Decisions: %v", err)
	}
	if v, ok := got[ArchiveKey{Kind: "session", ID: "s1"}]; !ok || !v {
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
			if err := closeOnCleanup(t, NewArchiveStore(dbPath)).Set("", "session", "s1", true, time.Unix(1, 0)); err != nil {
				t.Fatalf("archive Set: %v", err)
			}
		}},
		{"favorite", func(t *testing.T, dbPath string) {
			t.Helper()
			if err := closeOnCleanup(t, NewFavoriteStore(dbPath)).Set("", "session", "s1", true, time.Unix(1, 0)); err != nil {
				t.Fatalf("favorite Set: %v", err)
			}
		}},
		{"pin_section", func(t *testing.T, dbPath string) {
			t.Helper()
			if _, _, err := closeOnCleanup(t, NewPinSectionStore(dbPath)).CreateOrReuseAndAssign("inbox", "", "s1", time.Unix(1, 0)); err != nil {
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
