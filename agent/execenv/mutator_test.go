package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// TestRemovePathOffModeSurfacesRemoveFailure pins issue #2376: off-mode
// RemovePath must report a genuine filesystem failure instead of an unconditional
// success. A read-only wrapping is the injected boundary (the same seam
// local_edge_program_fuzz_test.go uses): Remove returns EPERM, so the call must
// return an error and leave the target in place. A missing target stays a no-op.
func TestRemovePathOffModeSurfacesRemoveFailure(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	target := filepath.Join(worktree, "keep.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(worktree).SetFs(afero.NewReadOnlyFs(afero.NewOsFs()))

	if err := env.RemovePath("keep.txt"); err == nil {
		t.Fatal("RemovePath reported success on a real EPERM remove failure")
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("a failed remove must leave the file in place: %v", err)
	}
	// Absence stays a no-op success: an absent target is already gone. The
	// read-only wrapping rejects every Remove with EPERM regardless of existence,
	// so restore the real filesystem to exercise the absence contract.
	env.SetFs(afero.NewOsFs())
	if err := env.RemovePath("missing.txt"); err != nil {
		t.Errorf("deleting an absent target should be a no-op: %v", err)
	}
}
