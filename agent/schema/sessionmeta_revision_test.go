package schema

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
