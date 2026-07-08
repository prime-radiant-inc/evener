package tool

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
)

// sandboxedApplyEnv builds a LocalExecutionEnvironment rooted at a fresh worktree
// beneath a fake home, carrying the resolved policy for mode. apply_patch drives
// real files through the full chain (ApplyPatch -> FileMutator -> sandboxFS); the
// enforcement here is reached by internal construction, not the feature-gated
// --sandbox flag.
func sandboxedApplyEnv(t *testing.T, mode sandbox.Mode) (env *execenv.LocalExecutionEnvironment, home, worktree string) {
	t.Helper()
	home = t.TempDir()
	worktree = filepath.Join(home, "project")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	host := sandbox.HostFacts{
		OS: "linux", Home: home,
		BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true,
	}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: mode}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve(%v): %v", mode, err)
	}
	env = execenv.NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	return env, home, worktree
}

func applyDenied(t *testing.T, env execenv.FileMutator, patch, whatf string) {
	t.Helper()
	_, err := ApplyPatch(env, patch)
	if err == nil {
		t.Fatalf("%s: expected a denial, got nil", whatf)
	}
	var denied *sandbox.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("%s: expected *sandbox.DeniedError, got %T: %v", whatf, err, err)
	}
}

// TestApplyPatchSandbox_InWorktreeWorks: Add/Update/Move/Delete all succeed within
// the worktree under workspace-write, going through the race-safe layer.
func TestApplyPatchSandbox_InWorktreeWorks(t *testing.T) {
	env, _, worktree := sandboxedApplyEnv(t, sandbox.ModeWorkspaceWrite)
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Add File: sub/b.txt\n+hello\n+world\n" +
		"*** Update File: a.txt\n@@\n one\n-two\n+TWO\n three\n" +
		"*** Update File: sub/b.txt\n*** Move to: sub/c.txt\n@@\n hello\n world\n" +
		"*** End Patch\n"
	if _, err := ApplyPatch(env, patch); err != nil {
		t.Fatalf("in-worktree apply_patch: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(worktree, "a.txt")); string(got) != "one\nTWO\nthree\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(worktree, "sub", "c.txt")); string(got) != "hello\nworld\n" {
		t.Errorf("moved c.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(worktree, "sub", "b.txt")); err == nil {
		t.Error("b.txt should have been moved away")
	}
}

// TestApplyPatchSandbox_EscapesDenied: every op targeting outside the worktree —
// via ../, an absolute out-of-root path, or a symlinked component — is denied and
// creates/leaks nothing outside.
func TestApplyPatchSandbox_EscapesDenied(t *testing.T) {
	env, home, worktree := sandboxedApplyEnv(t, sandbox.ModeWorkspaceWrite)

	// A symlinked component escaping the root.
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(worktree, "link")); err != nil {
		t.Fatal(err)
	}
	// A file to move, and one to attempt to read/patch.
	if err := os.WriteFile(filepath.Join(worktree, "src.txt"), []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	applyDenied(t, env, "*** Begin Patch\n*** Add File: ../evil.txt\n+x\n*** End Patch\n", "Add via ../")
	applyDenied(t, env, "*** Begin Patch\n*** Add File: "+filepath.Join(home, "abs-evil.txt")+"\n+x\n*** End Patch\n", "Add via absolute out-of-root")
	applyDenied(t, env, "*** Begin Patch\n*** Add File: link/planted.txt\n+x\n*** End Patch\n", "Add via symlinked component")
	applyDenied(t, env, "*** Begin Patch\n*** Update File: src.txt\n*** Move to: ../moved.txt\n@@\n-data\n+data2\n*** End Patch\n", "Move to ../")

	// Nothing landed outside the worktree.
	for _, p := range []string{
		filepath.Join(home, "evil.txt"),
		filepath.Join(home, "abs-evil.txt"),
		filepath.Join(outside, "planted.txt"),
		filepath.Join(home, "moved.txt"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("a denied apply_patch op leaked a file outside the worktree: %s", p)
		}
	}
	// src.txt was not moved away (the failed Move is a denial before rename).
	if _, err := os.Stat(filepath.Join(worktree, "src.txt")); err != nil {
		t.Errorf("src.txt should be untouched after a denied move: %v", err)
	}
}

// TestApplyPatchSandbox_DenylistReadDirect: an Update naming a DIRECT (non-symlink)
// absolute path inside a denylisted directory is refused by the masked-path check
// — the genuine "denylist read via apply_patch" case (a resolver that unmasked
// ~/.ssh would fail this, unlike the symlink-refusal case below).
func TestApplyPatchSandbox_DenylistReadDirect(t *testing.T) {
	env, home, _ := sandboxedApplyEnv(t, sandbox.ModeWorkspaceWrite)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(ssh, "id_rsa")
	if err := os.WriteFile(key, []byte("KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: " + key + "\n@@\n-KEY\n+PWNED\n*** End Patch\n"
	_, err := ApplyPatch(env, patch)
	var denied *sandbox.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("Update of a denylisted path: want *sandbox.DeniedError, got %T: %v", err, err)
	}
	if !strings.Contains(denied.Reason, "masked") {
		t.Errorf("want a MASKED denial (not e.g. symlink), got reason %q", denied.Reason)
	}
	if got, _ := os.ReadFile(key); string(got) != "KEY\n" {
		t.Errorf("denylisted key was modified: %q", got)
	}
}

// TestApplyPatchSandbox_DenylistViaSymlink: an Update that reads through a symlink
// into a denylisted directory is refused (the symlink-refusal path, distinct from
// the direct masked-path check above).
func TestApplyPatchSandbox_DenylistViaSymlink(t *testing.T) {
	env, home, worktree := sandboxedApplyEnv(t, sandbox.ModeWorkspaceWrite)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ssh, "id_rsa"), []byte("KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ssh, filepath.Join(worktree, "keys")); err != nil {
		t.Fatal(err)
	}
	applyDenied(t, env, "*** Begin Patch\n*** Update File: keys/id_rsa\n@@\n-KEY\n+PWNED\n*** End Patch\n", "Update through a symlink into ~/.ssh")
	// The key is unchanged.
	if got, _ := os.ReadFile(filepath.Join(ssh, "id_rsa")); string(got) != "KEY\n" {
		t.Errorf("denylisted key was modified: %q", got)
	}
}

// TestApplyPatchSandbox_GitHookProtected: apply_patch cannot plant a git hook or
// rewrite git config — those surfaces are write-protected by the resolved policy
// (from M1's gitdir classification) even inside the writable worktree.
func TestApplyPatchSandbox_GitHookProtected(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("needs a unix host")
	}
	home := t.TempDir()
	worktree := t.TempDir()
	// A real git repo so the resolved policy carries Git.ProtectedPaths.
	initGitRepo(t, worktree)
	host := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true, OverlaySupported: true}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)

	applyDenied(t, env, "*** Begin Patch\n*** Add File: .git/hooks/post-checkout\n+#!/bin/sh\n+echo pwned\n*** End Patch\n", "apply_patch plant a git hook")
	if _, err := os.Stat(filepath.Join(worktree, ".git", "hooks", "post-checkout")); err == nil {
		t.Error("a hook was planted despite the protected denial")
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

// TestApplyPatchSandbox_ReadOnlyDeniesMutations: read-only mode denies every
// mutating op (Add/Update/Delete), leaving files untouched.
func TestApplyPatchSandbox_ReadOnlyDeniesMutations(t *testing.T) {
	env, _, worktree := sandboxedApplyEnv(t, sandbox.ModeReadOnly)
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "d.txt"), []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	applyDenied(t, env, "*** Begin Patch\n*** Add File: new.txt\n+x\n*** End Patch\n", "read-only Add")
	applyDenied(t, env, "*** Begin Patch\n*** Update File: a.txt\n@@\n-one\n+ONE\n*** End Patch\n", "read-only Update")
	applyDenied(t, env, "*** Begin Patch\n*** Delete File: d.txt\n*** End Patch\n", "read-only Delete")

	if got, _ := os.ReadFile(filepath.Join(worktree, "a.txt")); string(got) != "one\n" {
		t.Errorf("read-only mode mutated a.txt: %q", got)
	}
	if _, err := os.Stat(filepath.Join(worktree, "d.txt")); err != nil {
		t.Error("read-only Delete removed the file")
	}
	if _, err := os.Stat(filepath.Join(worktree, "new.txt")); err == nil {
		t.Error("read-only Add created a file")
	}
}
