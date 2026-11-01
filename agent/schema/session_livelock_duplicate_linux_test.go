//go:build linux

package schema

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// An inherited or duplicated descriptor shares the mint's open file
// description. Release must clear held-ness even while that descriptor lives.
// This isolates the mechanism, not the cause of a historical startup failure.
func TestSessionLivelockReleaseClearsMarkerWithDuplicatedDescriptor(t *testing.T) {
	dir, id := t.TempDir(), "duplicate-held-descriptor"
	release, applied, err := AcquireSessionLivelock(dir, id)
	if err != nil || !applied {
		t.Fatalf("acquire: applied=%t err=%v", applied, err)
	}
	t.Cleanup(release)
	held, exists, err := SessionLivelockHeld(dir, id)
	if err != nil || !exists || !held {
		t.Fatalf("minted probe: held=%t exists=%t err=%v", held, exists, err)
	}
	marker, err := os.Stat(SessionLivelockPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	const fdDir = "/proc/self/fd"
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		t.Fatal(err)
	}
	var minted []int
	for _, fd := range fds {
		info, err := os.Stat(filepath.Join(fdDir, fd.Name()))
		if err != nil || !os.SameFile(marker, info) {
			continue
		}
		number, err := strconv.Atoi(fd.Name())
		if err != nil {
			t.Fatal(err)
		}
		minted = append(minted, number)
	}
	if len(minted) != 1 {
		t.Fatalf("mint descriptors=%v, want exactly one before duplication", minted)
	}
	duplicate, err := syscall.Dup(minted[0])
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(duplicate)
	t.Cleanup(func() { _ = syscall.Close(duplicate) })
	release()
	// No sleep, retry or duplicate close precedes the immediate-free verdict.
	held, exists, err = SessionLivelockHeld(dir, id)
	if err != nil || !exists || held {
		t.Fatalf("probe immediately after release with duplicate still open: held=%t exists=%t err=%v, want false,true,nil", held, exists, err)
	}
}
