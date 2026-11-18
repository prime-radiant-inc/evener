//go:build unix

package execenv

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/gittest"
)

// An ancestor this process cannot stat says nothing about whether a .git is
// there. Reading the stat error as "no .git anywhere" and memoizing it makes
// the environment call the directory non-git for the rest of its life — the
// same poisoning as a cancelled fork, reached through the filesystem instead of
// through git. Only ErrNotExist is an absence.
func TestGitRootOrEmptyContext_UnreadableAncestorIsNotMemoized(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the stat cannot be made to fail")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	// With the directory itself unreadable, the stat of repo/.git fails EACCES
	// rather than reporting the entry absent.
	if err := os.Chmod(repo, 0o000); err != nil {
		t.Fatalf("chmod repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(repo, 0o755) })
	env := NewLocalExecutionEnvironment(base)

	if got := GitRootOrEmptyContext(context.Background(), env, repo); got != "" {
		t.Fatalf("resolution under an unreadable ancestor = %q, want empty", got)
	}

	// The permission clears and the repository is plainly there.
	if err := os.Chmod(repo, 0o755); err != nil {
		t.Fatalf("restore repo mode: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	got := GitRootOrEmptyContext(context.Background(), env, repo)

	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Errorf("resolution once the ancestor became readable = %q, want %q: the stat error was memoized as an absence", got, want)
	}
}

// The main-root resolver walks the same ancestors through the same os.Stat and
// shares the same cache, so it has the same hazard: a stat that failed for want
// of permission is not an absence, and memoizing it as one makes the
// environment call the path non-git for the rest of its life.
func TestResolveMainRepoRoot_UnreadableAncestorIsNotMemoized(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the stat cannot be made to fail")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.Chmod(repo, 0o000); err != nil {
		t.Fatalf("chmod repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(repo, 0o755) })
	env := NewLocalExecutionEnvironment(base)

	if got := ResolveMainRepoRoot(env, repo); got != "" {
		t.Fatalf("resolution under an unreadable ancestor = %q, want empty", got)
	}

	if err := os.Chmod(repo, 0o755); err != nil {
		t.Fatalf("restore repo mode: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	got := ResolveMainRepoRoot(env, repo)

	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got != want {
		t.Errorf("resolution once the ancestor became readable = %q, want %q: the stat error was memoized as an absence", got, want)
	}
}

// A ".git" entry that exists but is not a directory and cannot be opened as a
// regular file — here a FIFO with no writer — is not a pointer this walk can
// read. Reading it would block forever, so the entry must be reported as
// unsupported (handled=false, no error) and the caller's own fallback answers.
// The guard is the property "open only regular files, nonblocking", which
// covers a socket or a device without naming them.
func TestLocalStructuralMainRoot_GitFIFOIsUnsupportedNotBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ".git"), 0o600); err != nil {
		t.Fatalf("mkfifo .git: %v", err)
	}

	root, isGit, handled, err := localStructuralMainRoot(dir)
	if err != nil {
		t.Fatalf("localStructuralMainRoot(.git FIFO): %v, want the entry reported as unsupported", err)
	}
	if handled {
		t.Error("handled = true for a .git this walk cannot read, want false so the caller falls back")
	}
	if !isGit {
		t.Error("isGit = false for a .git entry that exists, want true")
	}
	if root != "" {
		t.Errorf("root = %q, want empty", root)
	}
}

// A linked checkout whose ".git" is a symlink to a regular pointer file is a
// legitimate layout: os.ReadFile followed the link and read the pointer. The
// nonblocking guard must keep that behavior — it resolves the link first and
// then requires the target to be a regular file — while still refusing a FIFO
// or any other non-regular target behind the same link.
func TestLocalStructuralMainRoot_GitSymlinkToPointerResolves(t *testing.T) {
	main, wt := gittest.NewLinkedWorktree(t)
	pointer := filepath.Join(wt, ".git")
	content, err := os.ReadFile(pointer)
	if err != nil {
		t.Fatalf("read linked worktree pointer: %v", err)
	}
	shared := filepath.Join(t.TempDir(), "shared-git-pointer")
	if err := os.WriteFile(shared, content, 0o644); err != nil {
		t.Fatalf("write shared pointer: %v", err)
	}
	if err := os.Remove(pointer); err != nil {
		t.Fatalf("remove pointer: %v", err)
	}
	if err := os.Symlink(shared, pointer); err != nil {
		t.Fatalf("symlink .git to the shared pointer: %v", err)
	}

	root, isGit, handled, err := localStructuralMainRoot(wt)
	if err != nil {
		t.Fatalf("localStructuralMainRoot(.git symlink to a regular pointer): %v, want the linked checkout resolved", err)
	}
	if !isGit || !handled {
		t.Fatalf("isGit = %v, handled = %v, want both true so the walk answers instead of running Git", isGit, handled)
	}
	if want := filepath.Base(main); filepath.Base(root) != want {
		t.Errorf("root = %q, want the main checkout named %q", root, want)
	}
}

// The same symlink to a FIFO is still a FIFO: following the link must not turn
// the nonblocking guard into a blocking read, so the entry stays unsupported.
func TestLocalStructuralMainRoot_GitSymlinkToFIFOIsUnsupported(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(t.TempDir(), "pointer-fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.Symlink(fifo, filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("symlink .git to the FIFO: %v", err)
	}

	root, _, handled, err := localStructuralMainRoot(dir)
	if err != nil {
		t.Fatalf("localStructuralMainRoot(.git symlink to a FIFO): %v, want the entry reported as unsupported", err)
	}
	if handled || root != "" {
		t.Errorf("handled = %v, root = %q, want the entry unsupported so the caller falls back", handled, root)
	}
}

// A .git pointer is one path line. A regular file far larger than any pointer is
// refused unread rather than loaded whole into the daemon's memory, and the
// refusal is the same "unreadable pointer" answer the FIFO case gives: no error,
// handled=false, so the caller's own fallback answers.
func TestLocalStructuralMainRoot_GitPointerTooLargeIsUnsupported(t *testing.T) {
	dir := t.TempDir()
	huge := make([]byte, 1<<20)
	for i := range huge {
		huge[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), huge, 0o644); err != nil {
		t.Fatalf("write oversized .git: %v", err)
	}

	root, _, handled, err := localStructuralMainRoot(dir)
	if err != nil {
		t.Fatalf("localStructuralMainRoot(oversized .git): %v, want the entry reported as unsupported", err)
	}
	if handled || root != "" {
		t.Errorf("handled = %v, root = %q, want the entry unsupported so the caller falls back", handled, root)
	}
}
