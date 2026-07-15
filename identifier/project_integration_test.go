package identifier

import (
	"os"
	"path/filepath"
	"testing"
)

func newLinkedWorktree(t *testing.T) (main, worktree string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	runGit(t, base, "init", "-q", "main")
	runGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	worktree = filepath.Join(base, "worktree")
	runGit(t, main, "worktree", "add", "-q", worktree, "-b", "feature")
	return main, worktree
}

func TestResolveProjectLocalGitIntegration(t *testing.T) {
	main, worktree := newLinkedWorktree(t)
	nested := filepath.Join(worktree, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	mainCanonical := evalSym(t, main)
	for name, path := range map[string]string{
		"main": main, "nested": nested, "linked worktree": worktree,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveProject(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.CanonicalPath != mainCanonical {
				t.Fatalf("CanonicalPath = %q, want %q", got.CanonicalPath, mainCanonical)
			}
		})
	}

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(worktree, alias); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveProject(alias)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalPath != mainCanonical {
		t.Fatalf("symlink CanonicalPath = %q, want %q", got.CanonicalPath, mainCanonical)
	}
}

func TestResolveProjectLocalSubmoduleIsOwnRepository(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	super := filepath.Join(base, "super")
	for _, dir := range []string{sub, super} {
		runGit(t, base, "init", "-q", filepath.Base(dir))
		runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "seed")
	}
	runGit(t, super, "submodule", "add", "-q", "../sub", "sub")
	subRoot := evalSym(t, filepath.Join(super, "sub"))
	got, err := ResolveProject(filepath.Join(super, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalPath != subRoot {
		t.Fatalf("submodule CanonicalPath = %q, want %q", got.CanonicalPath, subRoot)
	}
	if got.CanonicalPath == evalSym(t, super) {
		t.Fatalf("submodule resolved to superproject root %q", got.CanonicalPath)
	}
}

func TestResolveProjectLocalErrors(t *testing.T) {
	if _, err := ResolveProject(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("nonexistent path accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProject(file); err == nil {
		t.Fatal("regular file accepted")
	}
}

func TestResolveProjectLocalMalformedDetectedWorktreePointerNoFallback(t *testing.T) {
	main, worktree := newLinkedWorktree(t)
	pointerPath := filepath.Join(worktree, ".git")
	missing := filepath.Join(main, ".git", "worktrees", "missing")
	if err := os.WriteFile(pointerPath, []byte("gitdir: "+missing+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProject(worktree); err == nil {
		t.Fatal("missing linked-worktree target silently fell back")
	}
	if _, err := os.Stat(filepath.Join(main, ".git")); err != nil {
		t.Fatal(err)
	}
}

func TestResolveProjectLocalIgnoresHostileGitEnvironment(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	super := filepath.Join(base, "super")
	for _, dir := range []string{sub, super} {
		runGit(t, base, "init", "-q", filepath.Base(dir))
		runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "seed")
	}
	runGit(t, super, "submodule", "add", "-q", "../sub", "sub")
	subRoot := evalSym(t, filepath.Join(super, "sub"))
	for _, name := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
	} {
		t.Setenv(name, filepath.Join(t.TempDir(), "hostile"))
	}
	got, err := ResolveProject(filepath.Join(super, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalPath != subRoot {
		t.Fatalf("CanonicalPath = %q, want submodule root %q", got.CanonicalPath, subRoot)
	}
	if got.CanonicalPath == evalSym(t, super) {
		t.Fatalf("CanonicalPath = %q, unexpectedly resolved to superproject", got.CanonicalPath)
	}
}
