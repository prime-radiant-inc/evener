//go:build linux

package execenv

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
	"primeradiant.com/serf/agent/sandbox"
)

// swapper atomically exchanges the two entries name<->spare beneath dirFd in a
// tight loop until stop is set, using RENAME_EXCHANGE (an atomic swap with no
// window where either name is missing). It models the model-spawned background
// job that swaps a path component for an out-of-root symlink between serf's check
// and its I/O — the TOCTOU adversary the fd-anchored resolver must defeat.
func swapper(t *testing.T, dirFd int, name, spare string, stop *atomic.Bool, wg *sync.WaitGroup) {
	t.Helper()
	defer wg.Done()
	for !stop.Load() {
		if err := unix.Renameat2(dirFd, name, dirFd, spare, unix.RENAME_EXCHANGE); err != nil {
			// EINTR/EBUSY can occur under load; keep swapping.
			continue
		}
	}
}

// TestResolveVsSymlinkSwap drives the read path against a concurrent component
// swap. "data" alternates atomically between a real in-root directory (holding an
// in-root file) and a symlink to an out-of-root secret. Every read must be either
// a success reading the IN-ROOT bytes or a typed *DeniedError (the symlink was
// refused, not followed). The out-of-root secret must NEVER be returned.
func TestResolveVsSymlinkSwap(t *testing.T) {
	s, home, worktree := newSB(t, sandbox.ModeRestricted)

	const inRoot = "IN_ROOT_CONTENT"
	const secret = "OUT_OF_ROOT_SECRET"

	// Out-of-root secret directory (under home, outside the worktree).
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "file.txt"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	// In-root "data" directory with the benign file.
	realDir := filepath.Join(worktree, "data")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "file.txt"), []byte(inRoot), 0o644); err != nil {
		t.Fatal(err)
	}
	// The spare entry is a symlink to the out-of-root dir; swapping it onto "data"
	// makes "data/file.txt" resolve through a symlink that escapes the root.
	if err := os.Symlink(outside, filepath.Join(worktree, "spare")); err != nil {
		t.Fatal(err)
	}

	wfd, err := unix.Open(worktree, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(wfd)

	target := filepath.Join(worktree, "data", "file.txt")

	// Deterministic pre-check (no race): with "data" as the real dir the read
	// succeeds in-root; after one atomic swap it is a symlink and the read is a
	// typed denial. This guarantees both outcomes are genuinely reachable before
	// the racing loop, so the loop's safety assertions are not vacuously true.
	if got, rerr := s.readFile("read_file", target); rerr != nil || string(got) != inRoot {
		t.Fatalf("pre-check: expected in-root read, got %q err %v", got, rerr)
	}
	if err := unix.Renameat2(wfd, "data", wfd, "spare", unix.RENAME_EXCHANGE); err != nil {
		t.Fatalf("pre-check swap: %v", err)
	}
	if _, rerr := s.readFile("read_file", target); rerr == nil {
		t.Fatal("pre-check: read through a swapped-in symlink must be denied")
	} else {
		var denied *sandbox.DeniedError
		if !asDenied(rerr, &denied) {
			t.Fatalf("pre-check: symlink refusal must be *sandbox.DeniedError, got %T: %v", rerr, rerr)
		}
	}
	// Restore "data" to the real dir before the racing loop.
	if err := unix.Renameat2(wfd, "data", wfd, "spare", unix.RENAME_EXCHANGE); err != nil {
		t.Fatalf("pre-check restore: %v", err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go swapper(t, wfd, "data", "spare", &stop, &wg)

	sawSuccess, sawDenied := false, false
	for i := 0; i < 4000; i++ {
		got, rerr := s.readFile("read_file", target)
		if rerr == nil {
			if string(got) != inRoot {
				stop.Store(true)
				wg.Wait()
				t.Fatalf("read #%d returned out-of-root content %q (want in-root %q) — TOCTOU escape", i, got, inRoot)
			}
			sawSuccess = true
			continue
		}
		var denied *sandbox.DeniedError
		if !asDenied(rerr, &denied) {
			// ENOENT is possible only if a swap left a dangling state; the atomic
			// exchange never removes a name, so any non-denial error is a defect.
			stop.Store(true)
			wg.Wait()
			t.Fatalf("read #%d failed with a non-denial error %T: %v", i, rerr, rerr)
		}
		sawDenied = true
	}
	stop.Store(true)
	wg.Wait()

	// Both outcomes should have been observed given 4000 iterations against a
	// tight swapper; if not, the test still passed its safety assertions but log
	// it so a degenerate schedule is visible rather than silently trivial.
	if !sawSuccess {
		t.Log("note: never observed a successful in-root read (swapper may have starved the reader)")
	}
	if !sawDenied {
		t.Log("note: never observed a symlink-refusal denial (swapper may have starved)")
	}
}
