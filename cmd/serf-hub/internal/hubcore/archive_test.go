package hubcore

import (
	"os"
	"path/filepath"
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
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	// Attempting to create a DB file in a read-only directory should fail.
	db := filepath.Join(dir, "index.db")
	s := NewArchiveStore(db)
	now := time.Now()
	if err := s.Set("session", "sess-1", true, now); err == nil {
		t.Fatal("expected error when DB cannot be created")
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
	if err := s.Set("session", "sess-1", true, now); err == nil {
		t.Fatal("expected error when MkdirAll parent is a file")
	}
}
