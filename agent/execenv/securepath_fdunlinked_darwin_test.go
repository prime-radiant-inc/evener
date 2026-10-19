//go:build darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestFdUnlinkedInconclusiveOnUnsearchableAncestor covers the discrimination the
// macOS dead-root check must make: only ABSENCE means the descriptor's directory
// is gone. When an ancestor loses search permission, stat on the descriptor's
// reported path fails with EACCES — not ENOENT — and that must be read as
// INCONCLUSIVE, not as deletion, because the captured directory is still
// perfectly usable. Reporting it dead would refuse every relative mutation on a
// live root (issue #4680's recovery must not fire on a permission blip).
func TestFdUnlinkedInconclusiveOnUnsearchableAncestor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory search permission, so EACCES cannot be produced")
	}
	base := t.TempDir()
	ancestor := filepath.Join(base, "ancestor")
	captured := filepath.Join(ancestor, "captured")
	if err := os.MkdirAll(captured, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(captured)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Sanity: while reachable, the live descriptor is not dead.
	if fdUnlinked(int(f.Fd())) {
		t.Fatal("fdUnlinked reported a live directory as deleted")
	}

	// Make the captured directory unreachable by removing search permission on
	// its ancestor. stat on the reported path now fails with EACCES.
	if err := os.Chmod(ancestor, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ancestor, 0o755) })

	if fdUnlinked(int(f.Fd())) {
		t.Error("fdUnlinked reported a live directory as deleted when its ancestor was merely unsearchable; a permission error is inconclusive, not absence")
	}

	// And the true positive still holds: remove the directory for real and the
	// check reports it dead.
	if err := os.Chmod(ancestor, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ancestor); err != nil {
		t.Fatal(err)
	}
	if !fdUnlinked(int(f.Fd())) {
		t.Error("fdUnlinked did not report a genuinely deleted directory as dead")
	}
}

// TestFdUnlinkedInconclusiveOnRenameInWindow forces the concurrent-rename window
// the reviewers flagged: fdUnlinked asks for the descriptor's path, then stats
// that path, and a rename BETWEEN the two makes the previously reported path no
// longer resolve — a negative observation for a descriptor that still names a
// perfectly usable directory. That must read as inconclusive, not as deletion,
// or a renamed-but-live root refuses every relative mutation and asks for a
// re-anchor it does not need.
//
// Deterministic via the fdUnlinkedPath seam: the first call reports the captured
// path, the test renames the directory on disk, and the second call (the
// re-validation) reports the NEW path, so the check sees the path moved. Without
// the re-validation the check would return the first call's absence.
func TestFdUnlinkedInconclusiveOnRenameInWindow(t *testing.T) {
	base := t.TempDir()
	original := filepath.Join(base, "original")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	renamed := filepath.Join(base, "renamed")
	realPath := sandbox.FdPath
	calls := 0
	fdUnlinkedPath = func(fd int) (string, error) {
		calls++
		if calls == 1 {
			// Report the pre-rename spelling, as the kernel would mid-window.
			return original, nil
		}
		// The re-validation observes the directory where it now lives.
		return realPath(fd)
	}
	t.Cleanup(func() { fdUnlinkedPath = sandbox.FdPath })

	if err := os.Rename(original, renamed); err != nil {
		t.Fatal(err)
	}

	if fdUnlinked(int(f.Fd())) {
		t.Error("fdUnlinked reported a renamed-but-live directory as deleted; the path moved inside the window, so the negative observation is inconclusive, not death")
	}
	if calls < 2 {
		t.Fatalf("fdUnlinked did not re-validate the descriptor's path (seam called %d times)", calls)
	}

	// True positive: once the renamed directory is genuinely removed, the check
	// reports it dead even with the same seam installed (both calls see a
	// stale spelling that resolves to nothing, or the new path that is gone).
	if err := os.RemoveAll(renamed); err != nil {
		t.Fatal(err)
	}
	fdUnlinkedPath = func(fd int) (string, error) { return renamed, nil }
	if !fdUnlinked(int(f.Fd())) {
		t.Error("fdUnlinked did not report a directory as dead after it was truly removed")
	}
}
