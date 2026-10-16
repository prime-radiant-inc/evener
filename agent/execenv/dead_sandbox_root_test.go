//go:build linux || darwin

package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// This file is the behavior surface for issue #4680: a file-tool-confined
// session whose anchored directory is deleted out from under it must refuse a
// relative mutation with the named recovery, instead of resolving beneath the
// stale captured root descriptor. The descriptor route is used (see
// capturedRootUnlinked): the discriminator is "the directory is unlinked", not
// "the path is missing" — the confined-file root legitimately has a live
// descriptor with an absent path (TestConfinedFileRecoveryCapturedHost).

// T1: every relative mutation entry refuses under a dead sandbox root, naming
// the recovery, and neither recreates the deleted root nor lands anything.
func TestMutations_RefuseRelativePathUnderDeadSandboxRoot(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)

	// Capture the sandbox root fd the way a real session does: one successful
	// relative write opens and caches the root descriptor.
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write (root fd capture): %v", err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove sandbox root: %v", err)
	}

	cases := []struct {
		name string
		call func() error
	}{
		{"WriteFileRaw", func() error { return env.WriteFileRaw("nested/file.txt", []byte("x"), 0o644) }},
		{"WriteFile", func() error { _, err := env.WriteFile("nested/file.txt", "x"); return err }},
		{"EditFile", func() error { _, err := env.EditFile("nested/file.txt", "a", "b", false); return err }},
		{"RemovePath", func() error { return env.RemovePath("nested/file.txt") }},
		{"RenamePath", func() error { return env.RenamePath("a.txt", "nested/b.txt") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, ErrWorkingDirectoryUnavailable) {
				t.Fatalf("%s error = %v, want ErrWorkingDirectoryUnavailable", tc.name, err)
			}
			if _, statErr := os.Stat(worktree); !os.IsNotExist(statErr) {
				t.Fatalf("%s recreated the deleted sandbox root: %v", tc.name, statErr)
			}
		})
	}
}

// T2: the discriminator. A root renamed aside leaves the logical path missing
// while its captured descriptor is live. That must NOT be refused — the write
// stays anchored to the captured directory, exactly as the confined-file
// recovery root requires. This pins "unlinked", not "path missing".
func TestSandboxMutations_RenamedRootIsNotRefused(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	moved := worktree + "-moved"
	if err := os.Rename(worktree, moved); err != nil {
		t.Fatalf("rename root aside: %v", err)
	}
	if _, statErr := os.Stat(worktree); !os.IsNotExist(statErr) {
		t.Fatalf("logical root path still present after rename: %v", statErr)
	}
	if err := env.WriteFileRaw("nested/file.txt", []byte("payload"), 0o644); err != nil {
		t.Fatalf("relative write under a renamed (live-descriptor) root = %v, want success", err)
	}
	if _, statErr := os.Stat(worktree); !os.IsNotExist(statErr) {
		t.Fatalf("write recreated the renamed-away logical path: %v", statErr)
	}
	got, err := os.ReadFile(filepath.Join(moved, "nested", "file.txt"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("write did not stay anchored to the captured directory: got=%q err=%v", got, err)
	}
}

// T1b: a root never opened (its descriptor was never captured) must refuse the
// same way. Left unfixed, a cold relative RemovePath returns nil — the
// best-effort remove treats the missing root as "already gone" and reports
// success without doing anything.
func TestSandboxMutations_RefuseRelativeMutationsUnderDeadRootCold(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	// No seed write: the sandbox root descriptor is never captured.
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove sandbox root: %v", err)
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"WriteFileRaw", func() error { return env.WriteFileRaw("nested/file.txt", []byte("x"), 0o644) }},
		{"RemovePath", func() error { return env.RemovePath("nested/file.txt") }},
		{"RenamePath", func() error { return env.RenamePath("a.txt", "b.txt") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, ErrWorkingDirectoryUnavailable) {
				t.Fatalf("%s (cold root) error = %v, want ErrWorkingDirectoryUnavailable", tc.name, err)
			}
			if _, statErr := os.Stat(worktree); !os.IsNotExist(statErr) {
				t.Fatalf("%s (cold root) recreated the deleted sandbox root: %v", tc.name, statErr)
			}
		})
	}
}

// T1c: the root path recreated after the captured directory was unlinked still
// refuses, because the captured descriptor is the stale, unlinked one — the
// session must re-anchor rather than write beneath a directory that is gone.
func TestSandboxMutations_RefuseAfterDeadRootRecreated(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove sandbox root: %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("recreate root path: %v", err)
	}
	if err := env.RemovePath("nested/file.txt"); !errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("remove under a stale root = %v, want ErrWorkingDirectoryUnavailable", err)
	}
	if got, rerr := os.ReadDir(worktree); rerr != nil || len(got) != 0 {
		t.Fatalf("refused remove touched the recreated path: entries=%v err=%v", got, rerr)
	}
}

// T3: absolute-path mutations are unaffected — the guard is relative-only, as
// in issue #4671. An absolute target under the dead root is not refused with
// the named recovery (its handling is otherwise unchanged).
func TestSandboxMutations_AbsolutePathUnderDeadRootNotNamedRefused(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove sandbox root: %v", err)
	}
	abs := filepath.Join(worktree, "nested", "file.txt")
	err := env.WriteFileRaw(abs, []byte("x"), 0o644)
	if errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("absolute write under a dead root returned the relative-only named error: %v", err)
	}
	if _, statErr := os.Stat(worktree); !os.IsNotExist(statErr) {
		t.Fatalf("absolute write recreated the deleted root: %v", statErr)
	}
}

// Preservation: with a live root, relative mutations behave exactly as before.
func TestSandboxMutations_LiveRootRelativeStillWorks(t *testing.T) {
	t.Parallel()
	env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
	if err := env.WriteFileRaw("nested/file.txt", []byte("payload"), 0o644); err != nil {
		t.Fatalf("relative write with a live root = %v, want success", err)
	}
	got, err := os.ReadFile(filepath.Join(worktree, "nested", "file.txt"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("live-root write = %q err=%v, want payload", got, err)
	}
	if err := env.RemovePath("nested/file.txt"); err != nil {
		t.Fatalf("relative remove with a live root = %v, want success", err)
	}
	if _, statErr := os.Stat(filepath.Join(worktree, "nested", "file.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("live-root remove left the file: %v", statErr)
	}
}
