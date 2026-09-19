//go:build unix

package schema

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestSaveSessionMetaWaitsForCrossProcessLock pins that the Revision
// load/increment/rename serializes against a writer in another process, not just
// the in-process striped mutex: while the session's lock file is held elsewhere,
// SaveSessionMeta must block, and must complete once it is released.
func TestSaveSessionMetaWaitsForCrossProcessLock(t *testing.T) {
	dir := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	sessDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(sessDir, id+".meta.json.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- SaveSessionMeta(dir, SessionMeta{ID: id}) }()

	select {
	case err := <-done:
		t.Fatalf("SaveSessionMeta completed while another process held the lock (err=%v)", err)
	case <-time.After(150 * time.Millisecond):
		// Blocked on the cross-process lock, as required.
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("SaveSessionMeta after releasing the lock: %v", err)
	}
}
