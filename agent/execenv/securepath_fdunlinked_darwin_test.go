//go:build darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"
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
