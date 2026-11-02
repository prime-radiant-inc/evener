//go:build linux

package schema

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSessionLivelockReleaseDropsMarkerWithInheritedFD(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "inherited-marker"
	path := SessionLivelockPath(stateDir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := markLivelockHeld(f); err != nil {
		t.Fatal(err)
	}
	inheritedFD, err := unix.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(inheritedFD)

	releaseSessionLivelockFile(f)
	held, exists, err := SessionLivelockHeld(stateDir, id)
	if err != nil {
		t.Fatalf("probe after release: %v", err)
	}
	if !exists {
		t.Fatal("livelock file disappeared while inherited descriptor remained open")
	}
	if held {
		t.Fatal("inherited descriptor kept the OFD marker held after release")
	}
}
