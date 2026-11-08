//go:build unix

package schema

import (
	"errors"
	"syscall"
	"testing"
)

// TestSessionLivelockAcquireHeldReleased: acquire takes the flock, a second
// acquire reports contention rather than pretending to hold it, and the probe
// distinguishes held from free with the file in place.
func TestSessionLivelockAcquireHeldReleased(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const id = "01testsessionsid01"

	release, applied, err := AcquireSessionLivelock(dir, id)
	if err != nil || !applied || release == nil {
		t.Fatalf("acquire = (applied=%v, err=%v), want the lock taken", applied, err)
	}
	defer release()

	held, exists, err := SessionLivelockHeld(dir, id)
	if err != nil || !exists || !held {
		t.Fatalf("probe = (held=%v, exists=%v, err=%v), want held", held, exists, err)
	}

	if _, _, err := AcquireSessionLivelock(dir, id); err == nil {
		t.Fatal("second acquire succeeded, want contention reported")
	} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("second acquire err = %v, want flock contention", err)
	}

	release()
	held, exists, err = SessionLivelockHeld(dir, id)
	if err != nil || !exists || held {
		t.Fatalf("probe after release = (held=%v, exists=%v, err=%v), want free with the file in place", held, exists, err)
	}
}

// TestSessionLivelockHeldAbsent: no file answers nothing — exists=false is the
// unknown callers must never read as death.
func TestSessionLivelockHeldAbsent(t *testing.T) {
	t.Parallel()
	held, exists, err := SessionLivelockHeld(t.TempDir(), "01neverexisted00000")
	if err != nil || exists || held {
		t.Fatalf("probe = (held=%v, exists=%v, err=%v), want absent", held, exists, err)
	}
}
