//go:build unix

package schema

import (
	"encoding/json"
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
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SaveSessionMeta after releasing the lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SaveSessionMeta did not complete after the lock was released")
	}
}

// TestAppendSessionObservedByLoadsUnderCrossProcessLock pins HIGH 1: the append
// must read the current meta inside the cross-process lock, not before it. A
// writer in another process commits a newer name while this process is blocked
// on the lock; the append must merge ObservedBy onto the committed meta and must
// not write its pre-lock snapshot of the other fields back over it.
func TestAppendSessionObservedByLoadsUnderCrossProcessLock(t *testing.T) {
	dir := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	const observer = "02wMz5Txv8Vo4rqb3QYZuV"
	if err := SaveSessionMeta(dir, SessionMeta{ID: id, Name: "original"}); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "sessions", id+".meta.json.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- AppendSessionObservedBy(dir, id, observer) }()
	// Let the append reach the lock (it must not complete while it is held).
	select {
	case err := <-done:
		t.Fatalf("AppendSessionObservedBy completed while the lock was held (err=%v)", err)
	case <-time.After(150 * time.Millisecond):
	}

	// Another process commits a newer revision with a different name, bypassing
	// the lock the way a racy writer would.
	newer := SessionMeta{ID: id, Name: "newer", Revision: 2}
	body, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", id+".meta.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("append after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AppendSessionObservedBy did not complete after the lock was released")
	}

	got, err := LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "newer" {
		t.Fatalf("append wrote its pre-lock snapshot over the committed meta: Name=%q, want %q", got.Name, "newer")
	}
	if len(got.ObservedBy) == 0 {
		t.Fatal("append did not persist ObservedBy")
	}
}
