package hubcore

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
)

// TestStoreCloseReleasesTheSharedHandle pins the shutdown contract the keeper
// handles add: Close releases the handle, is safe on a store that never opened
// one, and is safe to call twice, so the hub's cleanup can close every store
// unconditionally.
func TestStoreCloseReleasesTheSharedHandle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	now := time.Unix(1_700_000_000, 0)

	never := closeOnCleanup(t, NewArchiveStore(dbPath))
	if err := never.Close(); err != nil {
		t.Fatalf("Close on a store that never opened = %v, want nil", err)
	}

	archive := closeOnCleanup(t, NewArchiveStore(dbPath))
	if err := archive.Set("", "session", "before-close", true, now); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("archive Close = %v", err)
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("second archive Close = %v, want nil: Close stays idempotent", err)
	}

	favorite := closeOnCleanup(t, NewFavoriteStore(dbPath))
	if err := favorite.Set("", "session", "closed", true, now); err != nil {
		t.Fatal(err)
	}
	if err := favorite.Close(); err != nil {
		t.Fatalf("favorite Close = %v", err)
	}

	pins := closeOnCleanup(t, NewPinSectionStore(dbPath))
	if _, err := pins.Sections(); err != nil {
		t.Fatal(err)
	}
	if err := pins.Close(); err != nil {
		t.Fatalf("pin Close = %v", err)
	}

	seen := closeOnCleanup(t, NewSessionSeenStore(dbPath))
	if _, err := seen.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := seen.Close(); err != nil {
		t.Fatalf("seen Close = %v", err)
	}
}

// TestStoreUseAfterCloseReopens pins that a close is not a poison pill: the
// handle is a cache, so the next call opens a fresh one. Callers that outlive
// the hub's cleanup — a test harness that reads a store after runMain returns,
// or a shutdown that races an in-flight caller — keep working, the way they
// did when every call opened its own handle.
func TestStoreUseAfterCloseReopens(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	now := time.Unix(1_700_000_000, 0)

	archive := closeOnCleanup(t, NewArchiveStore(dbPath))
	if err := archive.Set("", "session", "a", true, now); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Set("", "session", "b", true, now); err != nil {
		t.Fatalf("Set after Close = %v, want a fresh handle", err)
	}
	if got, err := archive.Decisions(); err != nil || !got[ArchiveKey{Kind: "session", ID: "b"}] {
		t.Fatalf("Decisions after Close = %v, %v, want the new row", got, err)
	}

	favorite := closeOnCleanup(t, NewFavoriteStore(dbPath))
	if err := favorite.Set("", "session", "a", true, now); err != nil {
		t.Fatal(err)
	}
	if err := favorite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := favorite.Set("", "session", "b", true, now); err != nil {
		t.Fatalf("favorite Set after Close = %v, want a fresh handle", err)
	}

	pins := closeOnCleanup(t, NewPinSectionStore(dbPath))
	if _, err := pins.Sections(); err != nil {
		t.Fatal(err)
	}
	if err := pins.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := pins.Sections(); err != nil {
		t.Fatalf("Sections after Close = %v, want a fresh handle", err)
	}

	seen := closeOnCleanup(t, NewSessionSeenStore(dbPath))
	if _, err := seen.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := seen.Close(); err != nil {
		t.Fatal(err)
	}
	seen.now = func() time.Time { return now.Add(time.Hour) }
	if _, err := seen.MarkBatch([]SessionSeenMark{{SessionID: "s", SeenThrough: now}}); err != nil {
		t.Fatalf("MarkBatch after Close = %v, want a fresh handle", err)
	}
}

// TestKeeperHandleSwapDoesNotCloseAHandleInUse pins that a replacement does not
// close the pool another caller is still querying. get hands the *sql.DB to the
// caller, so closing it on the swap path makes an in-flight read fail with
// "database is closed" — and a caller that swallows the error (archiveDecisions
// falls back to an empty map) silently drops its data instead.
func TestKeeperHandleSwapDoesNotCloseAHandleInUse(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "index.db")
	store := closeOnCleanup(t, NewArchiveStore(dbPath))
	now := time.Unix(1_700_000_000, 0)
	if err := store.Set("", "session", "before", true, now); err != nil {
		t.Fatal(err)
	}

	// A caller takes the handle the way every read path does.
	inUse, err := store.db()
	if err != nil {
		t.Fatal(err)
	}

	// Another caller sees a replaced file and forces the swap.
	for _, p := range []string{dbPath, dbPath + "-journal", dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if err := store.Set("", "session", "after", true, now); err != nil {
		t.Fatal(err)
	}

	// The first caller's handle must still work: a swap may retire a pool, it
	// must not close one out from under an active query.
	if _, err := inUse.Exec(`SELECT 1`); err != nil {
		t.Fatalf("the handle a caller already held was closed by the swap: %v", err)
	}
}

// TestKeeperHandleDoesNotCacheAFileReplacedDuringOpen pins the ordering hazard:
// if the identity is taken only after the connect, a replacement landing in
// between is recorded as the identity of a pool still connected to the
// predecessor. Every later check then passes, and the handle keeps reading a
// file nothing writes any more until something closes the store.
func TestKeeperHandleDoesNotCacheAFileReplacedDuringOpen(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "index.db")
	seed := closeOnCleanup(t, NewArchiveStore(dbPath))
	now := time.Unix(1_700_000_000, 0)
	if err := seed.Set("", "session", "in-predecessor", true, now); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	var handle keeperHandle
	t.Cleanup(func() { _ = handle.close() })
	fsys := afero.NewOsFs()
	opens := 0
	open := func() (*sql.DB, error) {
		opens++
		db, err := sql.Open("sqlite", sqliteDSN(dbPath))
		if err != nil {
			return nil, err
		}
		// Force the connect now, then replace the file, so the pool below is
		// on the predecessor while the path names the successor.
		if err := db.Ping(); err != nil {
			_ = db.Close()
			return nil, err
		}
		if opens == 1 {
			// Replace the file with a different inode while the pool above is
			// connected to the predecessor. Deleting alone would leave the path
			// unstatable, which the handle already declines to cache.
			if err := os.Remove(dbPath); err != nil {
				_ = db.Close()
				return nil, err
			}
			replacement, err := sql.Open("sqlite", sqliteDSN(dbPath))
			if err != nil {
				_ = db.Close()
				return nil, err
			}
			if _, err := replacement.Exec(createArchiveTable); err != nil {
				_ = replacement.Close()
				_ = db.Close()
				return nil, err
			}
			if err := replacement.Close(); err != nil {
				_ = db.Close()
				return nil, err
			}
		}
		return db, nil
	}

	first, err := handle.get(dbPath, fsys, open)
	if err != nil {
		t.Fatalf("first get = %v", err)
	}
	second, err := handle.get(dbPath, fsys, open)
	if err != nil {
		t.Fatalf("second get = %v", err)
	}
	if first == second {
		t.Fatal("the second get reused the pool opened across the replacement: its identity was cached against a file it is not connected to")
	}
	if _, err := first.Exec(`SELECT 1`); err != nil {
		t.Fatalf("the retired pool was closed while a caller may hold it: %v", err)
	}
}

// TestKeeperHandleDoesNotCacheAFileCreatedThenReplacedInOpen pins the same
// hazard reached through the absent-file branch: when the path names no file
// before the open, the open creates the one its pool connects to, so there is
// no before-identity to hold that pool to the path. A replacement landing in
// that window must not be recorded as its identity either — caching it would
// make every later check pass over a pool on a file the path no longer names.
func TestKeeperHandleDoesNotCacheAFileCreatedThenReplacedInOpen(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "index.db")
	var handle keeperHandle
	t.Cleanup(func() { _ = handle.close() })
	fsys := afero.NewOsFs()
	opens := 0
	open := func() (*sql.DB, error) {
		opens++
		db, err := sql.Open("sqlite", sqliteDSN(dbPath))
		if err != nil {
			return nil, err
		}
		if err := db.Ping(); err != nil {
			_ = db.Close()
			return nil, err
		}
		if opens == 1 {
			if _, err := db.Exec(createArchiveTable); err != nil {
				_ = db.Close()
				return nil, err
			}
			// The path was absent before this open, so this pool created the
			// file. Replace it so the path names a different inode before the
			// trailing Stat runs.
			if err := os.Remove(dbPath); err != nil {
				_ = db.Close()
				return nil, err
			}
			replacement, err := sql.Open("sqlite", sqliteDSN(dbPath))
			if err != nil {
				_ = db.Close()
				return nil, err
			}
			if _, err := replacement.Exec(createArchiveTable); err != nil {
				_ = replacement.Close()
				_ = db.Close()
				return nil, err
			}
			if err := replacement.Close(); err != nil {
				_ = db.Close()
				return nil, err
			}
		}
		return db, nil
	}

	first, err := handle.get(dbPath, fsys, open)
	if err != nil {
		t.Fatalf("first get = %v", err)
	}
	second, err := handle.get(dbPath, fsys, open)
	if err != nil {
		t.Fatalf("second get = %v", err)
	}
	if first == second {
		t.Fatal("the second get reused the pool created across the replacement: its identity was cached against a file it is not connected to")
	}
	if _, err := first.Exec(`SELECT 1`); err != nil {
		t.Fatalf("the retired pool was closed while a caller may hold it: %v", err)
	}
}

// TestKeeperHandleCloseReleasesRetiredHandles pins that the pools a swap
// retires are released on Close. database/sql keeps no finalizer, so a retired
// pool that is never closed holds its opener goroutine and its connections for
// the life of the process: an index.db replaced repeatedly would accumulate
// them. Close must reach the retired pools, not only the current one.
func TestKeeperHandleCloseReleasesRetiredHandles(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "index.db")
	store := closeOnCleanup(t, NewArchiveStore(dbPath))
	now := time.Unix(1_700_000_000, 0)

	for i, id := range []string{"first", "second"} {
		if err := store.Set("", "session", id, true, now); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			for _, p := range []string{dbPath, dbPath + "-journal", dbPath + "-wal", dbPath + "-shm"} {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
		}
	}

	store.handle.mu.Lock()
	retired := append([]*sql.DB(nil), store.handle.retired...)
	store.handle.mu.Unlock()
	if len(retired) == 0 {
		t.Fatal("the replacement retired no handle: the rest of this test would prove nothing")
	}
	if store.handle.db != nil && store.handle.db == retired[0] {
		t.Fatal("the replaced handle is still the current one")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	for i, db := range retired {
		if err := db.Ping(); err == nil {
			t.Fatalf("retired handle %d is still open after Close: retired pools accumulate for the process lifetime", i)
		}
	}
	if store.handle.db != nil {
		t.Fatal("Close left a handle in the field")
	}
	if len(store.handle.retired) != 0 {
		t.Fatalf("Close left %d retired handles in the field", len(store.handle.retired))
	}
}

// TestKeeperHandleReopensWhenTheFileIsReplaced pins the one way a cached
// handle could serve data no per-call open would have: an index.db deleted and
// recreated underneath the hub is a new inode, and a handle still bound to the
// old one would answer every later read from a file nothing writes any more.
// A search must see the recreated file, which is what Search's FTS path reads.
func TestKeeperHandleReopensWhenTheFileIsReplaced(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	base := time.Unix(1_700_000_000, 0)
	const alpha = "02wMz5Txv1C3Hut0M8GCeB"
	const bravo = "02wMz5Txv2enqVTitaig6F"
	writeMeta(t, proj, schema.SessionMeta{
		ID: alpha, Name: "alpha", UpdatedAt: base,
		OriginalPrompt: "alpha needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"},
	})
	dbPath := filepath.Join(root, "index.db")
	index := closeOnCleanup(t, NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath))
	if _, err := index.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, ok := index.searchFTS("needle"); !ok {
		t.Fatal("the first search did not use the FTS table")
	}

	// Lose the file and its sidecars outside the index's lock, the way an
	// operator restoring a backup or a crashed checkpoint can.
	for _, p := range []string{dbPath, dbPath + "-journal", dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	// A session published after the loss lands in the recreated file.
	writeMeta(t, proj, schema.SessionMeta{
		ID: bravo, Name: "bravo", UpdatedAt: base.Add(time.Minute),
		OriginalPrompt: "bravo needle", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/w"},
	})
	if _, err := index.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// ok=false would be the pre-change behavior (the query fails on a file the
	// handle no longer names, and Search falls back to the memory scan). A
	// stale handle is the dangerous one: ok=true with the new row missing.
	if got, ok := index.searchFTS("bravo"); ok && len(got) == 0 {
		t.Fatal("the FTS read served the replaced file's predecessor: a post-loss publish is invisible behind the cached handle")
	}
	found := index.Search("bravo needle", 10, 0)
	if len(found) == 0 || found[0].ID != bravo {
		t.Fatalf("Search after index.db was replaced = %+v, want the session published after the loss", found)
	}
}

// TestPastIndexCloseReleasesTheReadHandle pins the past index's half: a close
// after a search releases the handle it opened, and a close before any search
// is a no-op.
func TestPastIndexCloseReleasesTheReadHandle(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "index.db")
	proj := filepath.Join(root, "projects", "project-x-0123456789")
	writeMeta(t, proj, schema.SessionMeta{
		ID:             "02wMz5Txv1C3Hut0M8GCeB",
		Name:           "closeable",
		UpdatedAt:      time.Unix(1_700_000_000, 0),
		OriginalPrompt: "closable needle",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/w"},
	})

	never := closeOnCleanup(t, NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath))
	if err := never.Close(); err != nil {
		t.Fatalf("Close before any search = %v, want nil", err)
	}

	index := closeOnCleanup(t, NewPastIndexWithDB(filepath.Join(root, "projects", "*"), dbPath))
	if _, err := index.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, ok := index.searchFTS("closable"); !ok {
		t.Fatal("searchFTS did not serve the indexed session")
	}
	if err := index.Close(); err != nil {
		t.Fatalf("past index Close = %v", err)
	}
	if _, ok := index.searchFTS("closable"); !ok {
		t.Fatal("searchFTS after Close did not serve the indexed session: a close must not poison the read path")
	}
}
