package hostlock

import (
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
	defer rel()
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
