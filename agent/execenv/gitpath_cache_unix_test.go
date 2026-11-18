//go:build unix

package execenv

import (
	"context"
	"errors"
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
// regular file — here a FIFO with no writer — is a pointer this walk read and
// refused. Refusing it is a terminal structural answer: handled must stay true
// and the refusal must be the typed ErrNotRegularFile, because handled=false
// hands the question to the Git binary, which resolves a program from a PATH
// that workspace directories can reach. The guard is the property "open only
// regular files, nonblocking", which covers a socket or a device without naming
// them.
func TestLocalStructuralMainRoot_GitFIFOIsANotRegularRefusal(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ".git"), 0o600); err != nil {
		t.Fatalf("mkfifo .git: %v", err)
	}

	root, isGit, handled, err := localStructuralMainRoot(dir)
	if !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("localStructuralMainRoot(.git FIFO) error = %v, want ErrNotRegularFile so no caller falls through to running Git", err)
	}
	if !handled {
		t.Error("handled = false after the entry was read and refused, which would run the Git binary instead")
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
// the nonblocking guard into a blocking read, and the refusal is terminal for
// the same reason as the plain FIFO case.
func TestLocalStructuralMainRoot_GitSymlinkToFIFOIsANotRegularRefusal(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(t.TempDir(), "pointer-fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.Symlink(fifo, filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("symlink .git to the FIFO: %v", err)
	}

	root, _, handled, err := localStructuralMainRoot(dir)
	if !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("localStructuralMainRoot(.git symlink to a FIFO) error = %v, want ErrNotRegularFile", err)
	}
	if !handled {
		t.Error("handled = false after the entry was read and refused, which would run the Git binary instead")
	}
	if root != "" {
		t.Errorf("root = %q, want empty", root)
	}
}

// A .git pointer is one path line. A regular file far larger than any pointer is
// refused unread rather than loaded whole into the daemon's memory, and that
// refusal is terminal and typed: ErrGitPointerTooLarge, handled=true, no
// fall-through to a Git binary that would search a workspace-reachable PATH.
func TestLocalStructuralMainRoot_GitPointerTooLargeIsATerminalRefusal(t *testing.T) {
	dir := t.TempDir()
	huge := make([]byte, 1<<20)
	for i := range huge {
		huge[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), huge, 0o644); err != nil {
		t.Fatalf("write oversized .git: %v", err)
	}

	root, _, handled, err := localStructuralMainRoot(dir)
	if !errors.Is(err, ErrGitPointerTooLarge) {
		t.Fatalf("localStructuralMainRoot(oversized .git) error = %v, want ErrGitPointerTooLarge so no caller falls through to running Git", err)
	}
	if !handled {
		t.Error("handled = false after the entry was read and refused, which would run the Git binary instead")
	}
	if root != "" {
		t.Errorf("root = %q, want empty", root)
	}
}

// A ".git" pointer this walk reads and refuses must never become a Git
// invocation. Resume reaches the same resolver (`ResolveProjectWith` with a
// project resolver over the target), and the Git binary it would fall back to is
// resolved from PATH — a PATH workspace directories can reach. So the refusal is
// observable as a process: a workspace directory holds an executable "git" that
// records its own execution, that directory is first on PATH, and a refused
// pointer must leave no record.
//
// The fixture is an oversized regular pointer rather than a FIFO on purpose.
// Both shapes are refused by the same guard, but a real git run against a FIFO
// blocks, and a test that hangs proves nothing. The regular file exercises the
// same fall-through — the case that actually escalated — and cannot block.
func TestResolveMainRepoRoot_RefusedPointerNeverExecutesGit(t *testing.T) {
	workspace := t.TempDir()
	canary := filepath.Join(workspace, "git-ran")
	fakeGit := filepath.Join(workspace, "git")
	script := "#!/bin/sh\n: > " + canary + "\nexit 1\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatalf("write workspace git: %v", err)
	}
	t.Setenv("PATH", workspace+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	huge := make([]byte, 1<<20)
	for i := range huge {
		huge[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), huge, 0o644); err != nil {
		t.Fatalf("write oversized .git: %v", err)
	}

	_, isGit, err := resolveMainRepoRoot(NewLocalExecutionEnvironment(dir), dir)
	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatalf("resolveMainRepoRoot(oversized .git) executed the workspace-supplied git at %q; a pointer this walk read and refused is a terminal structural answer and must not fall through to a PATH-resolved program", fakeGit)
	}
	if !isGit {
		t.Errorf("isGit = false for a .git entry that exists, want true")
	}
	if err == nil {
		t.Error("resolveMainRepoRoot of a refused .git pointer returned no error, want a terminal structural error")
	}
}

// A ".git" symlink whose target has disappeared is a REFUSED entry, not an
// absent one. os.Stat reports ErrNotExist for it, and treating that as "no
// repository here" makes the walk continue into the parent, so a nested
// checkout inherits its enclosing repository's identity. Absence is established
// on the entry itself; a link that exists and does not resolve is terminal.
func TestLocalStructuralMainRoot_DanglingGitSymlinkIsRefusedNotAbsent(t *testing.T) {
	outer, _ := gittest.NewLinkedWorktree(t)
	nested := filepath.Join(outer, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested checkout: %v", err)
	}
	gone := filepath.Join(t.TempDir(), "gone-pointer")
	if err := os.Symlink(gone, filepath.Join(nested, ".git")); err != nil {
		t.Fatalf("symlink .git to a nonexistent target: %v", err)
	}

	root, isGit, handled, err := localStructuralMainRoot(nested)
	if err == nil {
		t.Fatalf("localStructuralMainRoot(dangling .git symlink) = (%q, %v, %v), want a terminal error: resolving to the enclosing repo's root would mislabel a nested checkout", root, isGit, handled)
	}
	if !handled {
		t.Error("handled = false for a .git entry that exists and does not resolve, want true")
	}
	if root != "" {
		t.Errorf("root = %q, want empty", root)
	}

	// And the enclosing repository is still found from outside the nested
	// directory, so the refusal is about this entry, not about the walk.
	outerRoot, _, _, outerErr := localStructuralMainRoot(outer)
	if outerErr != nil {
		t.Fatalf("localStructuralMainRoot(enclosing checkout): %v", outerErr)
	}
	if want := filepath.Base(outer); filepath.Base(outerRoot) != want {
		t.Errorf("enclosing checkout root = %q, want a checkout named %q", outerRoot, want)
	}
}

// A .git symlink whose target is a readable pseudo-filesystem stream stats
// regular and can block in read(2) forever, so the byte cap does not bound it.
// The pseudo-mount is refused on the OPENED DESCRIPTOR, which is the check that
// covers /proc/kmsg and every alias to it.
//
// The fixture is /proc/version, not /proc/kmsg, on purpose: both are regular
// files on procfs and take the same descriptor check, but /proc/kmsg is
// root-only here, so a test built on it would skip and prove nothing. What is
// proven below is the REJECTION. The stall itself is unproven on this host for
// want of a readable stream, and is not claimed.
func TestLocalStructuralMainRoot_GitSymlinkToPseudoStreamIsRefused(t *testing.T) {
	stream := "/proc/version"
	if f, err := os.Open(stream); err != nil {
		t.Skipf("%s is not readable on this host: %v", stream, err)
	} else {
		_ = f.Close()
	}

	dir := t.TempDir()
	if err := os.Symlink(stream, filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("symlink .git to %s: %v", stream, err)
	}

	root, _, handled, err := localStructuralMainRoot(dir)
	if !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("localStructuralMainRoot(.git symlink to %s) error = %v, want ErrNotRegularFile so the read cannot park on the stream", stream, err)
	}
	if !handled {
		t.Error("handled = false after the entry was refused, want true")
	}
	if root != "" {
		t.Errorf("root = %q, want empty", root)
	}
}
