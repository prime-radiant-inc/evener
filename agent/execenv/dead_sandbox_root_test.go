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
// sandboxFS.rootDead): the discriminator is "the directory is unlinked", not
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

// T2b: a root spelled through a symlinked ancestor (the policy root is
// canonicalized) is still found and refused with the named recovery when it is
// gone — otherwise the mutation would fall through to the sandbox's out-of-root
// denial instead of the recovery (no silent success either way, but the wrong
// error).
func TestSandboxMutations_RefuseUnderDeadRootSpelledThroughSymlink(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realParent := filepath.Join(base, "realdir")
	home := filepath.Join(base, "home")
	for _, d := range []string{realParent, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(realParent, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(base, "link", "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, sbTestHost(home), worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(realParent, "wt")); err != nil {
		t.Fatalf("remove canonical worktree: %v", err)
	}
	if err := env.RemovePath("nested/file.txt"); !errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("remove under a dead symlink-spelled root = %v, want ErrWorkingDirectoryUnavailable", err)
	}
}

// T2c: a masked (secret) target is denied by the sandbox, and that typed denial
// must win over the dead-root recovery even under a dead root — the recovery
// would echo the requested path where the masked denial redacts it.
func TestSandboxMutations_MaskedPathUnderDeadRootKeepsMaskedDenial(t *testing.T) {
	t.Parallel()
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	secretDir := filepath.Join(worktree, "secret")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, DenylistAdd: []string{secretDir}},
		sbTestHost(home), worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove sandbox root: %v", err)
	}
	err = env.RemovePath("secret/file.txt")
	if errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("masked target under a dead root got the recovery error, want the masked denial: %v", err)
	}
	denied, ok := errors.AsType[*sandbox.DeniedError](err)
	if !ok {
		t.Fatalf("masked remove under a dead root = %v, want *sandbox.DeniedError", err)
	}
	if denied.ReasonKind != sandbox.DenialMasked || !denied.Sensitive {
		t.Fatalf("masked remove denial = %+v, want DenialMasked and Sensitive", denied)
	}
}

// T2l: a broader grant reached through a LONGER symlink alias must not outrank
// the shorter canonical workspace root beneath it, or deleting the workspace
// lets a relative write recreate it beneath the broader grant.
func TestSandboxMutations_LongerAliasGrantDoesNotHideDeadRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	realParent := filepath.Join(base, "r")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(realParent, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "long-parent-alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(alias, "wt")
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, sbTestHost(home), worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Grant the broader parent through its longer alias spelling, ahead of the
	// canonical workspace root.
	rp.FileTool.WriteRoots = append([]string{alias}, rp.FileTool.WriteRoots...)
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(realParent, "wt")); err != nil {
		t.Fatalf("remove workspace root: %v", err)
	}
	if err := env.WriteFileRaw("nested/file.txt", []byte("x"), 0o644); !errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("write under a dead workspace with a longer alias grant = %v, want ErrWorkingDirectoryUnavailable", err)
	}
	if _, statErr := os.Stat(filepath.Join(realParent, "wt")); !os.IsNotExist(statErr) {
		t.Fatalf("write recreated the deleted workspace beneath the alias grant: %v", statErr)
	}
}

// T2m: a symlink whose target is a MULTI-COMPONENT path (start → middle/sub,
// middle → root) must be walked component by component, so the anchor is
// recovered after the root is deleted rather than falling through to the
// outside-root denial.
func TestSandboxMutations_RefuseUnderDeadRootThroughMultiComponentLinkTarget(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(base, "middle")); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(base, "start")
	if err := os.Symlink("middle/sub", cwd); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, sbTestHost(home), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove canonical root: %v", err)
	}
	err = env.RemovePath("nested/file.txt")
	if !errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("remove under a dead root through a multi-component link target = %v, want ErrWorkingDirectoryUnavailable", err)
	}
}

// T2e: after the captured root is recreated and a masked entry inside it is made
// a symlink pointing away from the mask, full symlink resolution follows the
// link off the mask while the ancestor-spelled target keeps the alias. The
// canonical root joined with the literal in-root tail must still match the mask,
// so the masked denial — not the recovery — is returned.
func TestSandboxMutations_MaskedTargetUnderRecreatedSymlinkRootKeepsMaskedDenial(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realParent := filepath.Join(base, "realdir")
	home := filepath.Join(base, "home")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{realParent, home, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(realParent, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(base, "link", "wt")
	canonicalSecret := filepath.Join(realParent, "wt", "secret")
	if err := os.MkdirAll(canonicalSecret, 0o755); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, DenylistAdd: []string{canonicalSecret}},
		sbTestHost(home), worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	// Delete the captured root, recreate its path, and point the masked entry
	// outside the mask.
	if err := os.RemoveAll(filepath.Join(realParent, "wt")); err != nil {
		t.Fatalf("remove canonical root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(realParent, "wt"), 0o755); err != nil {
		t.Fatalf("recreate root path: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(realParent, "wt", "secret")); err != nil {
		t.Fatalf("plant masked-entry symlink: %v", err)
	}
	err = env.RemovePath("secret/file.txt")
	if errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("masked target under a recreated symlink root got the recovery error: %v", err)
	}
	denied, ok := errors.AsType[*sandbox.DeniedError](err)
	if !ok {
		t.Fatalf("masked remove under a recreated symlink root = %v, want *sandbox.DeniedError", err)
	}
	if denied.ReasonKind != sandbox.DenialMasked || !denied.Sensitive {
		t.Fatalf("masked remove denial = %+v, want DenialMasked and Sensitive", denied)
	}
}

// T2f: the working directory may itself be a symlink to the root. Deleting the
// root leaves that symlink dangling, so no path-based match finds the anchor
// even though its captured descriptor is unlinked. The relative mutation must
// still refuse with the named recovery, never the outside-write denial (which
// would invite an access-approval retry).
func TestSandboxMutations_RefuseUnderDeadRootWhenCwdIsSymlinkToRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	realRoot := filepath.Join(base, "real", "wt")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, cwd); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, sbTestHost(home), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(realRoot); err != nil {
		t.Fatalf("remove canonical root: %v", err)
	}
	err = env.RemovePath("nested/file.txt")
	if !errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("remove under a dead root reached through a dangling cwd symlink = %v, want ErrWorkingDirectoryUnavailable", err)
	}
}

// T2g: the working directory can be a subdirectory of the writable root, so the
// requested path's literal in-root tail is longer than the request. A masked
// entry under that subdirectory, made an outward symlink after the root is
// recreated, must still yield the masked denial.
func TestSandboxMutations_MaskedTargetUnderDeadSubdirCwdKeepsMaskedDenial(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realParent := filepath.Join(base, "realdir")
	home := filepath.Join(base, "home")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{realParent, home, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(realParent, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(base, "link", "wt", "sub")
	canonicalSecret := filepath.Join(realParent, "wt", "sub", "secret")
	if err := os.MkdirAll(canonicalSecret, 0o755); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, DenylistAdd: []string{canonicalSecret}},
		sbTestHost(home), sub)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(sub)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(realParent, "wt")); err != nil {
		t.Fatalf("remove canonical root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(realParent, "wt", "sub"), 0o755); err != nil {
		t.Fatalf("recreate root path: %v", err)
	}
	if err := os.Symlink(outside, canonicalSecret); err != nil {
		t.Fatalf("plant masked-entry symlink: %v", err)
	}
	err = env.RemovePath("secret/file.txt")
	if errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("masked target under a dead subdir cwd got the recovery error: %v", err)
	}
	denied, ok := errors.AsType[*sandbox.DeniedError](err)
	if !ok {
		t.Fatalf("masked remove under a dead subdir cwd = %v, want *sandbox.DeniedError", err)
	}
	if denied.ReasonKind != sandbox.DenialMasked || !denied.Sensitive {
		t.Fatalf("masked remove denial = %+v, want DenialMasked and Sensitive", denied)
	}
}

// T2h: the working directory can be reached through a chain of symlinks. When
// the root at the end is deleted the whole chain dangles, and the anchor must
// still be recovered so the mutation refuses with the named recovery rather than
// the outside-write denial.
func TestSandboxMutations_RefuseUnderDeadRootThroughCwdSymlinkChain(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	realRoot := filepath.Join(base, "real", "wt")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	intermediate := filepath.Join(base, "intermediate")
	if err := os.Symlink(realRoot, intermediate); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(base, "link")
	if err := os.Symlink(intermediate, cwd); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, sbTestHost(home), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(realRoot); err != nil {
		t.Fatalf("remove canonical root: %v", err)
	}
	err = env.RemovePath("nested/file.txt")
	if !errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("remove under a dead root reached through a symlink chain = %v, want ErrWorkingDirectoryUnavailable", err)
	}
}

// T2i: an overlapping broader writable grant must not hide the deleted specific
// workspace root. With both /real and /real/wt writable and the cwd spelled
// /link/wt, deleting /real/wt must still refuse rather than let a relative write
// recreate wt beneath the live /real grant.
func TestSandboxMutations_OverlappingGrantDoesNotHideDeadRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realParent := filepath.Join(base, "realdir")
	home := filepath.Join(base, "home")
	for _, d := range []string{realParent, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(realParent, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(base, "link", "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, sbTestHost(home), worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Add the live parent as a broader writable grant, ahead of the workspace so
	// a first-match lookup would choose it.
	rp.FileTool.WriteRoots = append([]string{realParent}, rp.FileTool.WriteRoots...)
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(realParent, "wt")); err != nil {
		t.Fatalf("remove workspace root: %v", err)
	}
	if err := env.WriteFileRaw("nested/file.txt", []byte("x"), 0o644); !errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("write under a dead workspace with a live broader grant = %v, want ErrWorkingDirectoryUnavailable", err)
	}
	if _, statErr := os.Stat(filepath.Join(realParent, "wt")); !os.IsNotExist(statErr) {
		t.Fatalf("write recreated the deleted workspace beneath the broader grant: %v", statErr)
	}
}

// T2j: the cwd can sit below a dangling ANCESTOR symlink. With /alias → the
// workspace root and cwd /alias/sub, deleting the root dangles /alias; only the
// COMPLETE RootDir is a symlink in the simple case, so the anchor must be
// recovered by resolving the ancestor link while keeping "sub" literal.
func TestSandboxMutations_RefuseUnderDeadRootBelowDanglingAncestorSymlink(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	realRoot := filepath.Join(base, "real", "wt")
	if err := os.MkdirAll(filepath.Join(realRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(base, "alias", "sub")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, sbTestHost(home), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(realRoot); err != nil {
		t.Fatalf("remove canonical root: %v", err)
	}
	err = env.RemovePath("nested/file.txt")
	if !errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("remove under a dead root below a dangling ancestor symlink = %v, want ErrWorkingDirectoryUnavailable", err)
	}
}

// T2k: a checkout rooted at the writable root, cwd reached through an ancestor
// symlink, and a component of the cwd itself (sub) replaced by an in-root
// symlink pointing outside. Resolving the cwd whole follows that link and loses
// the literal path, so the literal tail beneath the captured root must still
// match the mask.
func TestSandboxMutations_MaskedTargetUnderRecreatedRootInRootSymlinkKeepsMaskedDenial(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	outside := filepath.Join(base, "outside")
	realRoot := filepath.Join(base, "real", "wt")
	if err := os.MkdirAll(filepath.Join(realRoot, "sub", "secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(realRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{home, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	canonicalSecret := filepath.Join(realRoot, "sub", "secret")
	cwd := filepath.Join(base, "alias", "wt", "sub")
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, DenylistAdd: []string{canonicalSecret}},
		sbTestHost(home), cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(cwd)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(realRoot); err != nil {
		t.Fatalf("remove canonical root: %v", err)
	}
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatalf("recreate root path: %v", err)
	}
	// `sub` is a cwd component inside the writable root; replace it with a link
	// pointing away from the mask.
	if err := os.Symlink(outside, filepath.Join(realRoot, "sub")); err != nil {
		t.Fatalf("plant in-root cwd-component symlink: %v", err)
	}
	err = env.RemovePath("secret/file.txt")
	if errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("masked target under a recreated root with an in-root symlink got the recovery error: %v", err)
	}
	denied, ok := errors.AsType[*sandbox.DeniedError](err)
	if !ok {
		t.Fatalf("masked remove = %v, want *sandbox.DeniedError", err)
	}
	if denied.ReasonKind != sandbox.DenialMasked || !denied.Sensitive {
		t.Fatalf("masked remove denial = %+v, want DenialMasked and Sensitive", denied)
	}
}

// T2d: the mask may be spelled through the canonical root while the target is
// spelled through a symlinked ancestor. Under a dead root the masked denial —
// and its Sensitive redaction — must still win over the recovery, or the
// recovery would echo the masked target.
func TestSandboxMutations_MaskedTargetUnderDeadSymlinkRootKeepsMaskedDenial(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realParent := filepath.Join(base, "realdir")
	home := filepath.Join(base, "home")
	for _, d := range []string{realParent, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(realParent, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(base, "link", "wt")
	canonicalSecret := filepath.Join(realParent, "wt", "secret")
	if err := os.MkdirAll(canonicalSecret, 0o755); err != nil {
		t.Fatal(err)
	}
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, DenylistAdd: []string{canonicalSecret}},
		sbTestHost(home), worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if err := env.WriteFileRaw("seed.txt", []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(realParent, "wt")); err != nil {
		t.Fatalf("remove canonical root: %v", err)
	}
	err = env.RemovePath("secret/file.txt")
	if errors.Is(err, ErrWorkingDirectoryUnavailable) {
		t.Fatalf("masked target under a dead symlink-spelled root got the recovery error: %v", err)
	}
	denied, ok := errors.AsType[*sandbox.DeniedError](err)
	if !ok {
		t.Fatalf("masked remove under a dead symlink-spelled root = %v, want *sandbox.DeniedError", err)
	}
	if denied.ReasonKind != sandbox.DenialMasked || !denied.Sensitive {
		t.Fatalf("masked remove denial = %+v, want DenialMasked and Sensitive", denied)
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
