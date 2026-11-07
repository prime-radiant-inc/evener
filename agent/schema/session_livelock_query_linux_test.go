//go:build linux

package schema

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSessionLivelockProbeNeverTakesTheLock pins the non-contending
// inspection (review round 6, finding 3): the probe must observe held-ness
// WITHOUT taking the lock, because the mint's one non-blocking acquisition
// must never be made to fail by a probe. The pin is constructive — a foreign
// shared flock sits on the file, which an exclusive-taking probe cannot take
// and would misreport as held; the F_GETLK inspection is not blocked by it
// and answers that no livelock holder exists. Linux-only: it is Linux's
// F_GETLK that reports OFD locks and not flock locks.
func TestSessionLivelockProbeNeverTakesTheLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := "probe-never-takes"
	path := SessionLivelockPath(dir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed livelock file: %v", err)
	}

	// A shared flock the probe must neither take nor trip on. An
	// exclusive-taking probe (LOCK_EX|LOCK_NB) is blocked by it and would
	// answer held=true — the take-and-release behavior this test forbids.
	shared, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open livelock: %v", err)
	}
	defer func() { _ = shared.Close() }()
	if err := syscall.Flock(int(shared.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatalf("take shared flock: %v", err)
	}

	held, exists, err := SessionLivelockHeld(dir, id)
	if err != nil {
		t.Fatalf("SessionLivelockHeld: %v", err)
	}
	if !exists {
		t.Fatal("exists = false, want true (the seeded file is there)")
	}
	if held {
		t.Fatal("probe answered held=true under a foreign shared flock: the probe took an exclusive lock, which a pure inspection must never do")
	}

	// The positive control in the same terms: a real mint's marker reads held.
	release, applied, err := AcquireSessionLivelock(dir, id+"-held")
	if err != nil || !applied {
		t.Fatalf("acquire: applied=%t err=%v", applied, err)
	}
	held, exists, err = SessionLivelockHeld(dir, id+"-held")
	if err != nil || !exists || !held {
		t.Fatalf("probe of a minted livelock = (held=%v, exists=%v, err=%v), want held and exists", held, exists, err)
	}
	release()
	held, _, err = SessionLivelockHeld(dir, id+"-held")
	if err != nil {
		t.Fatalf("probe after release: %v", err)
	}
	if held {
		t.Fatal("probe answered held=true after release")
	}
}
