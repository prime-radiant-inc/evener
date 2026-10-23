package hostlock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEndpointIsHeldReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.lock")
	held, err := IsHeld(path)
	if err != nil || held {
		t.Fatalf("missing: %v %v", held, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("lock created: %v", err)
	}
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	held, err = IsHeld(path)
	if err != nil || !held {
		t.Fatalf("held: %v %v", held, err)
	}
	release()
	held, err = IsHeld(path)
	if err != nil || held {
		t.Fatalf("released: %v %v", held, err)
	}
}
func TestEndpointIsHeldRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.lock")
	if err := os.WriteFile(filepath.Join(dir, "target"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "target"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := IsHeld(path); err == nil {
		t.Fatal("accepted symlink lock")
	}
}
