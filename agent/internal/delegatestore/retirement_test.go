package delegatestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRetirementReadyReadsOriginal proves retirement readiness validates the
// primary log bytes rather than the in-memory fold: a valid store is ready,
// replacing the journal with malformed JSON makes readiness fail, and restoring
// the original bytes makes it ready again without reopening the store.
func TestRetirementReadyReadsOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, _, err := store.Append(make(State), createdEvent("dlg_alpha", "")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.CheckRetirementReady(); err != nil {
		t.Fatalf("ready store reported not ready: %v", err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Error(err)
		}
	})
	if err := store.CheckRetirementReady(); err == nil {
		t.Fatal("readiness accepted a corrupt original journal")
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckRetirementReady(); err != nil {
		t.Fatalf("readiness did not recover after the original bytes were restored: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRetirementReadyRefusesUnusableStore pins the sticky append/rollback
// failure path: once the store latches unusable, readiness must refuse it
// instead of silently folding stale memory.
func TestRetirementReadyRefusesUnusableStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	injected := errors.New("injected operation failure")
	store.ops.write = func(*os.File, []byte) (int, error) { return 0, injected }
	store.ops.truncate = func(*os.File, int64) error { return injected }
	if _, _, err := store.Append(make(State), createdEvent("dlg_alpha", "")); err == nil {
		t.Fatal("append unexpectedly succeeded under an injected failure")
	}
	if err := store.CheckRetirementReady(); err == nil {
		t.Fatal("readiness accepted a store latched unusable by a failed rollback")
	}
}
