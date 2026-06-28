package hostlock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireLock_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.lock")
	rel, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	rel()
}

func TestAcquireLock_FailsIfHeld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.lock")
	rel1, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	defer rel1()

	if _, err := AcquireLock(path); err == nil {
		t.Fatal("second AcquireLock should fail while first is held")
	}
}

func TestAcquireLock_ReleaseUnblocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.lock")
	rel1, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	rel1()

	rel2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("second AcquireLock after release: %v", err)
	}
	rel2()
}

func TestAcquireLock_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	// Create a file where a directory is expected.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "hub.lock")
	_, err := AcquireLock(path)
	if err == nil {
		t.Fatal("expected error when parent is a file")
	}
}

func TestAcquireLock_OpenFileError(t *testing.T) {
	dir := t.TempDir()
	// Make the directory read-only so OpenFile fails.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755) // restore for cleanup
	path := filepath.Join(dir, "hub.lock")
	_, err := AcquireLock(path)
	if err == nil {
		t.Fatal("expected error when directory is read-only")
	}
}
