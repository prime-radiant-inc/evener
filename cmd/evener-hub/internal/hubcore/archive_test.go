package hubcore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestArchiveStoreKeysDecisionsBySource(t *testing.T) {
	s := NewArchiveStore(filepath.Join(t.TempDir(), "index.db"))
	now := time.Unix(1_700_000_000, 0)
	for _, source := range []string{"", "host-a", "host-b"} {
		if err := s.Set(source, "project", "proj-a", true, now); err != nil {
			t.Fatalf("set %q: %v", source, err)
		}
	}
	got, err := s.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"", "host-a", "host-b"} {
		if !got[ArchiveKey{Kind: "project", ID: "proj-a", Source: source}] {
			t.Fatalf("missing decision for source %q: %v", source, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("decisions = %v, want three source-scoped entries", got)
	}
}

func TestArchiveStoreNormalizesLocalSource(t *testing.T) {
	for _, source := range []string{"", "local"} {
		if got := NormalizeDecisionSource(source); got != "" {
			t.Fatalf("NormalizeDecisionSource(%q) = %q, want the controller key", source, got)
		}
	}
	if got := NormalizeDecisionSource("host-a"); got != "host-a" {
		t.Fatalf("NormalizeDecisionSource(host-a) = %q, want host-a", got)
	}
}

// A pre-federation index.db keys decisions on (kind, id) alone; opening it must
// migrate the table to the (source, kind, id) key with legacy rows landing on
// the controller source, after which a remote sibling of the same ID fits.
func TestArchiveStoreMigratesLegacyKeyToSourceColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	legacy, err := sql.Open("sqlite", sqliteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE archive (
		kind       TEXT    NOT NULL,
		id         TEXT    NOT NULL,
		archived   INTEGER NOT NULL,
		decided_at INTEGER NOT NULL,
		PRIMARY KEY (kind, id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO archive (kind, id, archived, decided_at) VALUES ('project', 'proj-a', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	s := NewArchiveStore(dbPath)
	got, err := s.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !got[ArchiveKey{Kind: "project", ID: "proj-a"}] {
		t.Fatalf("legacy decision was not migrated to the controller key: %v", got)
	}
	if err := s.Set("host-a", "project", "proj-a", true, time.Now()); err != nil {
		t.Fatalf("set remote sibling after migration: %v", err)
	}
}

func fuzzScenarioArchiveStoreSetAndRead(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	s := NewArchiveStore(db)
	now := time.Unix(1_700_000_000, 0)

	if err := s.Set("", "session", "sess-1", true, now); err != nil {
		t.Fatalf("set archive: %v", err)
	}
	if err := s.Set("", "project", "proj-a", true, now); err != nil {
		t.Fatalf("set project: %v", err)
	}
	// unarchive flips it back
	if err := s.Set("", "session", "sess-1", false, now); err != nil {
		t.Fatalf("unset: %v", err)
	}

	got, err := s.Decisions()
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	if v, ok := got[ArchiveKey{Kind: "session", ID: "sess-1"}]; !ok || v != false {
		t.Fatalf("session decision = %v,%v; want false,true", v, ok)
	}
	if v, ok := got[ArchiveKey{Kind: "project", ID: "proj-a"}]; !ok || v != true {
		t.Fatalf("project decision = %v,%v; want true,true", v, ok)
	}
}

func fuzzScenarioArchiveStoreEmptyWhenNoDB(t *testing.T) {
	s := NewArchiveStore("")
	got, err := s.Decisions()
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %d", len(got))
	}
}

func fuzzScenarioArchiveStoreOpenError(t *testing.T) {
	// Root-proof injection: make the dbPath itself a directory. MkdirAll of the
	// parent succeeds, but sqlite cannot open a directory as a database file, so
	// open() fails at db.Exec regardless of uid (root cannot open a dir as a DB).
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	if err := os.Mkdir(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewArchiveStore(dbPath)
	now := time.Now()
	err := s.Set("", "session", "sess-1", true, now)
	if err == nil {
		t.Fatal("expected error when DB path is a directory")
	}
	// The failure must come from sqlite opening the DB file (the db.Exec step in
	// open()), not from some unrelated branch. Pin the sqlite open-failure message.
	if !strings.Contains(err.Error(), "unable to open database file") {
		t.Fatalf("error = %q; want it to reference the sqlite open failure", err)
	}
}

func fuzzScenarioArchiveStoreMkdirAllError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(blocker, "sub", "index.db")
	s := NewArchiveStore(db)
	now := time.Now()
	err := s.Set("", "session", "sess-1", true, now)
	if err == nil {
		t.Fatal("expected error when MkdirAll parent is a file")
	}
	// The failure must come from MkdirAll refusing to descend through a file, not
	// from a later branch. Pin the ENOTDIR cause and the offending parent path.
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("error = %v; want it to wrap ENOTDIR from MkdirAll", err)
	}
	if !strings.Contains(err.Error(), blocker) {
		t.Fatalf("error = %q; want it to reference the blocking parent path %q", err, blocker)
	}
}

func fuzzScenarioArchiveStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store := NewArchiveStore(filepath.Join(dir, "index.db"))
	now := time.Unix(1_700_000_000, 0)
	_ = store.Set("", "project", "/a/foo", true, now)
	if err := store.Delete("", "project", "/a/foo"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Decisions()
	if _, present := got[ArchiveKey{Kind: "project", ID: "/a/foo"}]; present {
		t.Fatalf("archive row should be gone: %v", got)
	}
}
