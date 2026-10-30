package agent

// The process livelock a root session takes at init (session_worktree_owner_liveness.go,
// issue #5471): the flock whose release-by-OS at process death is what lets a
// later prune prove a dead owner gone. Root sessions hold it from init to
// close; the file is left in place (flock releases at close, the OS releases it
// on a crash).

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

// TestRootSessionMintsAndReleasesProcessLivelock: a root session constructed
// with a state dir holds its livelock for its lifetime, and close releases it —
// so a session that is running reads as live, and one whose process died reads
// as dead.
func TestRootSessionMintsAndReleasesProcessLivelock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newSession(t, withStateDir(dir))

	if _, err := os.Stat(schema.SessionLivelockPath(dir, s.ID())); err != nil {
		t.Fatalf("root session took no livelock: %v", err)
	}
	held, exists, err := schema.SessionLivelockHeld(dir, s.ID())
	if err != nil || !exists || !held {
		t.Fatalf("livelock probe = (held=%v, exists=%v, err=%v), want held by the live session", held, exists, err)
	}

	s.Close()
	held, exists, err = schema.SessionLivelockHeld(dir, s.ID())
	if err != nil || !exists || held {
		t.Fatalf("livelock probe after close = (held=%v, exists=%v, err=%v), want free with the file in place", held, exists, err)
	}
}
