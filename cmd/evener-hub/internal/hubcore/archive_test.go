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

// A source is an exact host name, so surrounding whitespace is stripped before
// the empty/"local" check. Otherwise " local " would bypass controller-source
// normalization and "host-a " would key a row no real source addresses.
func TestNormalizeDecisionSourceTrimsWhitespace(t *testing.T) {
	cases := map[string]string{
		" host-a ":   "host-a",
		"\thost-b\n": "host-b",
		"  local  ":  "",
		"   ":        "",
		"\t":         "",
	}
	for source, want := range cases {
		if got := NormalizeDecisionSource(source); got != want {
			t.Fatalf("NormalizeDecisionSource(%q) = %q, want %q", source, got, want)
		}
	}
}

// A session decision is keyed by the identity its row is read under: the
// canonical ref for a remote row, the bare session ID for the controller's
// own. A "local:thread" spelling therefore collapses onto the bare ID instead
// of minting a key no read path consults, and a remote ref survives trimmed
// and canonical.
func TestNormalizeDecisionSessionID(t *testing.T) {
	cases := map[string]string{
		"session-1":        "session-1",
		"  session-1  ":    "session-1",
		"\tsession-1\n":    "session-1",
		"local:session-1":  "session-1",
		" local:session-1": "session-1",
		"host-a:t1":        "host-a:t1",
		" host-a:t1 ":      "host-a:t1",
		"local":            "local",
		"host-a:":          "host-a:",
		"":                 "",
	}
	for id, want := range cases {
		if got := NormalizeDecisionSessionID(id); got != want {
			t.Fatalf("NormalizeDecisionSessionID(%q) = %q, want %q", id, got, want)
		}
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

// Two migrators can both observe the legacy schema outside a transaction. The
// loser of the write-lock race must recheck the schema once it holds the lock
// and leave the already-upgraded table alone; rebuilding it would migrate the
// winner's source-qualified rows under the controller source, collapsing two
// hosts' same-ID decisions into one. The interleave hook pins the race
// deterministically instead of sleeping.
func TestArchiveStoreMigrationRechecksSchemaUnderWriteLock(t *testing.T) {
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
	if _, err := legacy.Exec(`INSERT INTO archive (kind, id, archived, decided_at) VALUES ('project', 'legacy-proj', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	// Interleave a competing migrator between the caller's schema pre-check and
	// its write lock: it upgrades the table and writes a source-qualified row.
	previous := indexMigrationInterleave
	indexMigrationInterleave = func() {
		indexMigrationInterleave = nil // the competing migration is one-shot
		competing := NewArchiveStore(dbPath)
		if _, err := competing.Decisions(); err != nil {
			t.Errorf("competing migration: %v", err)
		}
		if err := competing.Set("host-a", "project", "proj-a", true, time.Unix(2, 0)); err != nil {
			t.Errorf("competing set: %v", err)
		}
	}
	t.Cleanup(func() { indexMigrationInterleave = previous })

	store := NewArchiveStore(dbPath)
	got, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if !got[ArchiveKey{Kind: "project", ID: "proj-a", Source: "host-a"}] {
		t.Fatalf("host-a decision lost after the concurrent migration: %v", got)
	}
	if got[ArchiveKey{Kind: "project", ID: "proj-a"}] {
		t.Fatalf("host-a row collapsed onto the controller source: %v", got)
	}
	if !got[ArchiveKey{Kind: "project", ID: "legacy-proj"}] {
		t.Fatalf("legacy row did not migrate to the controller source: %v", got)
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
