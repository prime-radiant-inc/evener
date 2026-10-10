//go:build linux || darwin

package execenv

import (
	"errors"
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

// Not parallel: swaps the package-wide syscall seam after regular-file admission.
func TestRemoveRegularFileDirectorySwap(t *testing.T) {
	env, err := NewConfinedFileEnvironment(t.TempDir(), "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	path := filepath.Join(env.WorkingDirectory(), "page")
	if err := os.WriteFile(path, []byte("opaque-admitted-375"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := secureUnlinkat
	defer func() { secureUnlinkat = orig }()
	var flagsSeen []int
	secureUnlinkat = func(fd int, leaf string, flags int) error {
		flagsSeen = append(flagsSeen, flags)
		if len(flagsSeen) == 1 {
			if err := unix.Unlinkat(fd, leaf, 0); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkdirat(fd, leaf, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return orig(fd, leaf, flags)
	}
	if err := env.RemoveConfinedFile(path); err == nil {
		t.Errorf("replacement directory deletion reported success, flags=%v", flagsSeen)
	}
	if info, err := os.Lstat(path); err != nil || !info.IsDir() {
		t.Errorf("replacement directory did not survive: %v", err)
	}
	if len(flagsSeen) != 1 || flagsSeen[0] != 0 {
		t.Errorf("file-only deletion attempted directory fallback: %v", flagsSeen)
	}
}

// Not parallel: swaps the package-wide syscall seam to inject unlink failures.
func TestRemoveRegularFileUnlinkErrors(t *testing.T) {
	env, err := NewConfinedFileEnvironment(t.TempDir(), "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	path := filepath.Join(env.WorkingDirectory(), "page")
	if err := os.WriteFile(path, []byte("opaque-error-382"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := secureUnlinkat
	defer func() { secureUnlinkat = orig }()
	for _, failure := range []error{unix.EPERM, unix.EROFS} {
		calls := 0
		secureUnlinkat = func(fd int, leaf string, flags int) error {
			calls++
			if flags != 0 {
				t.Fatal("file-only removal attempted directory fallback")
			}
			return failure
		}
		if err := env.RemoveConfinedFile(path); !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("failure=%v error=%v calls=%d", failure, err, calls)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "opaque-error-382" {
			t.Fatalf("failed unlink changed body: %q %v", got, err)
		}
	}
}
