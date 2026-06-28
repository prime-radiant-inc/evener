package hubcore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestArchiveStoreSetAndRead(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	s := NewArchiveStore(db)
	now := time.Unix(1_700_000_000, 0)

	if err := s.Set("session", "sess-1", true, now); err != nil {
		t.Fatalf("set archive: %v", err)
	}
	if err := s.Set("project", "proj-a", true, now); err != nil {
		t.Fatalf("set project: %v", err)
	}
	// unarchive flips it back
	if err := s.Set("session", "sess-1", false, now); err != nil {
		t.Fatalf("unset: %v", err)
	}

	got, err := s.Decisions()
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	if v, ok := got[ArchiveKey{"session", "sess-1"}]; !ok || v != false {
		t.Fatalf("session decision = %v,%v; want false,true", v, ok)
	}
	if v, ok := got[ArchiveKey{"project", "proj-a"}]; !ok || v != true {
		t.Fatalf("project decision = %v,%v; want true,true", v, ok)
	}
}

func TestArchiveStoreEmptyWhenNoDB(t *testing.T) {
	s := NewArchiveStore("")
	got, err := s.Decisions()
	if err != nil {
		t.Fatalf("decisions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %d", len(got))
	}
}

func TestArchiveStoreOpenError(t *testing.T) {
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
	err := s.Set("session", "sess-1", true, now)
	if err == nil {
		t.Fatal("expected error when DB path is a directory")
	}
	// The failure must come from sqlite opening the DB file (the db.Exec step in
	// open()), not from some unrelated branch. Pin the sqlite open-failure message.
	if !strings.Contains(err.Error(), "unable to open database file") {
		t.Fatalf("error = %q; want it to reference the sqlite open failure", err)
	}
}

func TestArchiveStoreMkdirAllError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(blocker, "sub", "index.db")
	s := NewArchiveStore(db)
	now := time.Now()
	err := s.Set("session", "sess-1", true, now)
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
