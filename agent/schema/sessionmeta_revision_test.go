package schema

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTombstoneSessionMetaBlocksWriters pins the deletion protocol: after
// TombstoneSessionMeta, a writer (including an out-of-process autosave) must
// refuse to recreate the meta, so a deletion cannot be undone by an in-flight
// save. The meta itself is left for the caller's sweep.
func TestTombstoneSessionMetaBlocksWriters(t *testing.T) {
	dir := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	if err := SaveSessionMeta(dir, SessionMeta{ID: id, Name: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := TombstoneSessionMeta(dir, id); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionMeta(dir, SessionMeta{ID: id, Name: "resurrected"}); !errors.Is(err, ErrSessionDeleted) {
		t.Fatalf("save over a tombstone = %v, want ErrSessionDeleted", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", id+SessionMetaTombstoneSuffix)); err != nil {
		t.Fatalf("tombstone not written: %v", err)
	}
}

// TestUntombstoneSessionMetaRestoresWritability pins the failed-deletion
// rollback: once the tombstone has been rolled back because a deletion failed
// while the meta survived, a writer must be able to save again. Leaving the
// marker would fence every later save for a session that still exists.
func TestUntombstoneSessionMetaRestoresWritability(t *testing.T) {
	dir := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	if err := SaveSessionMeta(dir, SessionMeta{ID: id, Name: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := TombstoneSessionMeta(dir, id); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionMeta(dir, SessionMeta{ID: id, Name: "blocked"}); !errors.Is(err, ErrSessionDeleted) {
		t.Fatalf("save over a tombstone = %v, want ErrSessionDeleted", err)
	}
	if err := UntombstoneSessionMeta(dir, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", id+SessionMetaTombstoneSuffix)); !os.IsNotExist(err) {
		t.Fatalf("tombstone not removed: %v", err)
	}
	if err := SaveSessionMeta(dir, SessionMeta{ID: id, Name: "resumed"}); err != nil {
		t.Fatalf("save after rollback = %v, want nil", err)
	}
	// Removing an absent marker is a no-op.
	if err := UntombstoneSessionMeta(dir, id); err != nil {
		t.Fatalf("second untombstone = %v, want nil", err)
	}
}

// TestTombstoneSessionMetaDoesNotCreateMissingSessionsDir pins the medium: a
// deletion of a session whose state dir is already gone must be a no-op, not
// recreate the deleted project's state directory — which the PastIndex
// projects/* glob would then surface as a live project.
func TestTombstoneSessionMetaDoesNotCreateMissingSessionsDir(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "projects", "project-x-0123456789")
	const id = "02wMz5Txv1C3Hut0M8GCeB"

	if err := TombstoneSessionMeta(stateDir, id); err != nil {
		t.Fatalf("tombstoning an already-removed session = %v, want nil", err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("tombstone recreated the deleted state dir: %v", err)
	}
	if err := UntombstoneSessionMeta(stateDir, id); err != nil {
		t.Fatalf("untombstoning an absent state dir = %v, want nil", err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("untombstone recreated the deleted state dir: %v", err)
	}
}

// TestSessionMetaRevisionInitializesAndIncrements pins the invariant hubcore's
// metaNewer depends on: the first save of a session sets Revision to 1, and every
// subsequent save (including an observer append) increments it by exactly one.
func TestSessionMetaRevisionInitializesAndIncrements(t *testing.T) {
	dir := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	if err := SaveSessionMeta(dir, SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	got, err := LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 {
		t.Fatalf("first save Revision = %d, want 1", got.Revision)
	}

	if err := SaveSessionMeta(dir, got); err != nil {
		t.Fatalf("second save: %v", err)
	}
	if got, err = LoadSessionMeta(dir, id); err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 {
		t.Fatalf("second save Revision = %d, want 2", got.Revision)
	}

	if err := AppendSessionObservedBy(dir, id, "02wMz5Txv8Vo4rqb3QYZuV"); err != nil {
		t.Fatalf("observer append: %v", err)
	}
	if got, err = LoadSessionMeta(dir, id); err != nil {
		t.Fatal(err)
	}
	if got.Revision != 3 {
		t.Fatalf("observer append Revision = %d, want 3", got.Revision)
	}
}

// TestSessionMetaRevisionOverflowErrors pins that a save over a max-revision
// meta is refused rather than wrapping Revision to zero (which would make the
// next write look like legacy metadata to metaNewer).
func TestSessionMetaRevisionOverflowErrors(t *testing.T) {
	dir := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	sessDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(SessionMeta{ID: id, Revision: math.MaxUint64, UpdatedAt: time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, id+".meta.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SaveSessionMeta(dir, SessionMeta{ID: id, UpdatedAt: time.Unix(1_700_000_001, 0)}); err == nil {
		t.Fatal("expected an error saving over a max-revision meta, got nil")
	}
}
