package rendezvous

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveUnlessRegularRemovesNonRegularArtifact pins the reconciliation the
// stale-cleanup fallback needs: a pid-named artifact that is not the regular
// file Write publishes (here a directory) is unlinked, so a cleanup that
// failed on it can still finish on a retry.
func TestRemoveUnlessRegularRemovesNonRegularArtifact(t *testing.T) {
	dir := t.TempDir()
	const pid = 7401
	target := filepath.Join(dir, "7401.json")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("seed non-regular artifact: %v", err)
	}
	if err := RemoveUnlessRegular(dir, pid); err != nil {
		t.Fatalf("RemoveUnlessRegular(non-regular): %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("non-regular artifact survived: %v", err)
	}
}

// TestRemoveUnlessRegularLeavesRegularEntry pins the guard: a regular entry --
// what Write publishes for a live daemon -- is never unlinked, and the refusal
// is reported so the caller keeps its registration retryable.
func TestRemoveUnlessRegularLeavesRegularEntry(t *testing.T) {
	dir := t.TempDir()
	entry := ownershipTestEntry(7402)
	if _, err := Write(dir, entry); err != nil {
		t.Fatalf("Write: %v", err)
	}
	target := filepath.Join(dir, "7402.json")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if err := RemoveUnlessRegular(dir, entry.PID); err == nil {
		t.Fatal("RemoveUnlessRegular unlinked a regular rendezvous entry")
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("regular entry was removed: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("regular entry mutated: %s", after)
	}
}

// TestRemoveUnlessRegularMissingIsNil pins the exit-race case: an artifact
// another cleanup already removed is not an error.
func TestRemoveUnlessRegularMissingIsNil(t *testing.T) {
	dir := t.TempDir()
	if err := RemoveUnlessRegular(dir, 7403); err != nil {
		t.Fatalf("RemoveUnlessRegular(missing): %v, want nil", err)
	}
}
