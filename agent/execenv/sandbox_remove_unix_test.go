//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// TestRemoveEmptyDirSurvivesUnlinkPermission pins the directory-removal fallback
// against the Darwin/Linux errno difference: unlink(dir) returns EPERM on macOS
// and EISDIR on Linux, so the fallback cannot key on EISDIR. Injecting EPERM for
// the plain unlink and delegating the directory removal to the real syscall proves
// an empty in-root directory is still removed (not reported as EPERM).
//
// Unix-only because it swaps the secureUnlinkat seam, which exists only where the
// fd-anchored enforcement layer compiles (securepath_fdops_unix.go).
// Not parallel: swaps a package-level seam.
func TestRemoveEmptyDirSurvivesUnlinkPermission(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	dir := filepath.Join(worktree, "emptydir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := secureUnlinkat
	defer func() { secureUnlinkat = orig }()
	secureUnlinkat = func(fd int, path string, flags int) error {
		if flags == 0 {
			return unix.EPERM // Darwin's unlink(dir) errno
		}
		return orig(fd, path, flags)
	}
	if err := env.RemovePath(dir); err != nil {
		t.Fatalf("empty-dir remove with unlink EPERM: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("empty directory not removed: %v", err)
	}
}
