package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
)

// These are REAL-git integration tests for the manage_worktree create arm
// (spec §3). They init a throwaway repo under t.TempDir(), drive create through
// the session tool surface and the worktreeCreate handler directly, and assert
// against actual git state (porcelain, refs, checked-out files).

// wtRepo is a real git repo plus a session rooted at it, with the managed
// worktree root pointed at an isolated temp state dir so tests never touch the
// user's real state home.
type wtRepo struct {
	s        *Session
	mainRoot string // canonical (symlink-resolved) repo root
	stateDir string // s.stateDir; worktrees land under <stateDir>/worktrees
	head     string // SHA of the initial commit
}

// wtGit runs a git command in dir through a throwaway local env, failing the
// test on any non-zero exit.
func wtGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	env := execenv.NewLocalExecutionEnvironment(dir)
	cmd := "git " + execenv.ShellEscapeArgs(args...)
	res, err := env.ExecCommand(context.Background(), cmd, 30_000, dir, nil)
	if err != nil && res.ExitCode == 0 {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("git %s: exit %d: %s%s", strings.Join(args, " "), res.ExitCode, res.Stdout, res.Stderr)
	}
	return res.Stdout
}

// newWorktreeRepo builds a real one-commit git repo and a session rooted at it.
func newWorktreeRepo(t *testing.T) *wtRepo {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	wtGit(t, root, "init", "-b", "main")
	wtGit(t, root, "config", "user.email", "test@example.com")
	wtGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("main-checkout\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	wtGit(t, root, "add", "README")
	wtGit(t, root, "commit", "-m", "initial")
	head := strings.TrimSpace(wtGit(t, root, "rev-parse", "HEAD"))

	s := newSession(t, withDir(root))
	stateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks state: %v", err)
	}
	s.stateDir = stateDir

	return &wtRepo{s: s, mainRoot: root, stateDir: stateDir, head: head}
}

// create drives the create operation through the registered tool surface,
// returning the structured result map.
func (r *wtRepo) create(t *testing.T, args map[string]any) (map[string]any, error) {
	t.Helper()
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	full := map[string]any{"operation": "create"}
	for k, v := range args {
		full[k] = v
	}
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), full)
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("create result is %T, want map[string]any", out)
	}
	return m, nil
}

// porcelainEntry finds the porcelain record for the worktree at path (cleaned
// comparison), failing if absent.
func (r *wtRepo) porcelainEntry(t *testing.T, path string) worktree.PorcelainEntry {
	t.Helper()
	out := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	want := filepath.Clean(path)
	for _, e := range worktree.ParsePorcelain(out) {
		if filepath.Clean(e.Path) == want {
			return e
		}
	}
	t.Fatalf("no porcelain entry for %s in:\n%s", path, out)
	return worktree.PorcelainEntry{}
}

func (r *wtRepo) metaDir(canonicalMain string) string {
	return filepath.Join(r.stateDir, "worktrees", worktree.ProjectID(canonicalMain), ".meta")
}

// --- Tests ---

func TestWorktreeCreate_CreatesWorktreeWithGitPointer(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "feature-a"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path, _ := res["path"].(string)
	if path == "" {
		t.Fatal("create result has no path")
	}
	// Worktree dir exists with a .git pointer FILE (linked worktrees use a file).
	info, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil {
		t.Fatalf("stat .git pointer: %v", err)
	}
	if info.IsDir() {
		t.Errorf(".git in a linked worktree must be a pointer file, got a directory")
	}
	if got := res["branch"]; got != "feature-a" {
		t.Errorf("branch = %v, want feature-a", got)
	}
	if got := res["base_sha"]; got != r.head {
		t.Errorf("base_sha = %v, want %s", got, r.head)
	}
}

func TestWorktreeCreate_SwapsEnvIntoWorktree(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := res["path"].(string)

	if got := r.s.currentEnv().WorkingDirectory(); got != path {
		t.Fatalf("after create, currentEnv WorkingDirectory = %q, want %q", got, path)
	}
	// A file placed only in the worktree resolves through the swapped env, and
	// the read is confined to the worktree root.
	if err := os.WriteFile(filepath.Join(path, "only-in-worktree.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write worktree file: %v", err)
	}
	got, err := r.s.currentEnv().ReadFile("only-in-worktree.txt", nil, nil)
	if err != nil {
		t.Fatalf("read via swapped env: %v", err)
	}
	if !strings.Contains(got, "hi") {
		t.Errorf("read content = %q, want it to contain %q", got, "hi")
	}
	// The checked-out README is the worktree's copy.
	if _, statErr := os.Stat(filepath.Join(path, "README")); statErr != nil {
		t.Errorf("worktree README not checked out: %v", statErr)
	}
	// The main checkout never saw the worktree-only file.
	if _, statErr := os.Stat(filepath.Join(r.mainRoot, "only-in-worktree.txt")); !os.IsNotExist(statErr) {
		t.Errorf("worktree-only file leaked into the main checkout")
	}
}

func TestWorktreeCreate_AtomicLockWithOwnMarker(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "locked-lane"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	e := r.porcelainEntry(t, res["path"].(string))
	if !e.Locked {
		t.Fatal("fresh worktree is not locked; create must add --lock atomically")
	}
	want := worktree.FormatSessionMarker(r.s.id)
	if e.LockReason != want {
		t.Errorf("lock reason = %q, want %q", e.LockReason, want)
	}
}

func TestWorktreeCreate_WritesSidecarProvenance(t *testing.T) {
	r := newWorktreeRepo(t)
	res, err := r.create(t, map[string]any{"name": "prov"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	canonicalMain := res["main_repo_root"].(string)
	sc, err := worktree.ReadSidecar(r.metaDir(canonicalMain), "prov")
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if sc.Name != "prov" || sc.Branch != "prov" {
		t.Errorf("sidecar name/branch = %q/%q, want prov/prov", sc.Name, sc.Branch)
	}
	if sc.BaseSHA != r.head {
		t.Errorf("sidecar base_sha = %q, want %q", sc.BaseSHA, r.head)
	}
	if sc.CreatorSession != r.s.id {
		t.Errorf("sidecar creator = %q, want %q", sc.CreatorSession, r.s.id)
	}
	if sc.MergeTarget != "main" {
		t.Errorf("sidecar merge_target = %q, want main", sc.MergeTarget)
	}
	if sc.OriginalRoot != canonicalMain {
		t.Errorf("sidecar original_root = %q, want %q", sc.OriginalRoot, canonicalMain)
	}
}

func TestWorktreeCreate_AddFailureCleansSidecarSameCall(t *testing.T) {
	r := newWorktreeRepo(t)
	// A branch "feature" makes "feature/foo" a directory/file ref conflict: the
	// name passes every earlier check but `git worktree add -b feature/foo`
	// dies at git's ref lock. Sidecar-first ordering means the sidecar must be
	// cleaned in the same call.
	wtGit(t, r.mainRoot, "branch", "feature", r.head)

	_, err := r.create(t, map[string]any{"name": "feature/foo"})
	if err == nil {
		t.Fatal("expected create to fail on the D/F ref conflict")
	}
	canonicalMain, _ := filepath.EvalSymlinks(r.mainRoot)
	// The sidecar for feature/foo must be gone (same-call cleanup).
	if _, statErr := worktree.ReadSidecar(r.metaDir(canonicalMain), "feature/foo"); !os.IsNotExist(statErr) {
		t.Errorf("sidecar for feature/foo survived a failed add: err=%v", statErr)
	}
	// The branch must not have been created.
	if branchExistsInRepo(t, r.mainRoot, "feature/foo") {
		t.Errorf("branch feature/foo was created despite the failed add")
	}
	// The session env was not swapped into a nonexistent worktree.
	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Errorf("env swapped to %q on a failed create; want it to stay at %q", got, r.mainRoot)
	}
}

func TestWorktreeCreate_BaseIsActiveWorktreeHead(t *testing.T) {
	r := newWorktreeRepo(t)
	// Create A off main's HEAD, then advance A with a new commit.
	resA, err := r.create(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	pathA := resA["path"].(string)
	wtGit(t, pathA, "config", "user.email", "test@example.com")
	wtGit(t, pathA, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(pathA, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	wtGit(t, pathA, "add", "a.txt")
	wtGit(t, pathA, "commit", "-m", "advance A")
	headA := strings.TrimSpace(wtGit(t, pathA, "rev-parse", "HEAD"))
	if headA == r.head {
		t.Fatal("A's HEAD did not advance")
	}

	// The session is now inside A. Creating B must default its base to A's tip
	// (the ACTIVE root), not main's.
	resB, err := r.create(t, map[string]any{"name": "B"})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if got := resB["base_sha"]; got != headA {
		t.Errorf("B base_sha = %v, want A's active HEAD %s (not main %s)", got, headA, r.head)
	}
}

func TestWorktreeCreate_CreateAwayUnlocksOldWorktree(t *testing.T) {
	r := newWorktreeRepo(t)
	resA, err := r.create(t, map[string]any{"name": "A"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	pathA := resA["path"].(string)
	if !r.porcelainEntry(t, pathA).Locked {
		t.Fatal("A should be locked right after create")
	}

	resB, err := r.create(t, map[string]any{"name": "B"})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	pathB := resB["path"].(string)

	// Creating B from inside A is a leave of A: A is now unlocked, B is locked.
	if r.porcelainEntry(t, pathA).Locked {
		t.Errorf("A stayed locked after create-away; the old worktree must be unlocked")
	}
	eB := r.porcelainEntry(t, pathB)
	if !eB.Locked || eB.LockReason != worktree.FormatSessionMarker(r.s.id) {
		t.Errorf("B lock = (%v,%q), want locked with %q", eB.Locked, eB.LockReason, worktree.FormatSessionMarker(r.s.id))
	}
}

func TestWorktreeCreate_ExplicitBaseRefs(t *testing.T) {
	r := newWorktreeRepo(t)
	// A tag, a branch, and a remote-tracking ref all pointing at HEAD.
	wtGit(t, r.mainRoot, "tag", "v1", r.head)
	wtGit(t, r.mainRoot, "branch", "side", r.head)
	wtGit(t, r.mainRoot, "update-ref", "refs/remotes/origin/main", r.head)

	cases := []struct{ name, ref string }{
		{"from-sha", r.head},
		{"from-tag", "v1"},
		{"from-branch", "side"},
		{"from-remote", "origin/main"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := r.create(t, map[string]any{"name": c.name, "base_ref": c.ref})
			if err != nil {
				t.Fatalf("create base_ref=%q: %v", c.ref, err)
			}
			if got := res["base_sha"]; got != r.head {
				t.Errorf("base_sha = %v, want %s (base_ref %q)", got, r.head, c.ref)
			}
		})
	}
}

func TestWorktreeCreate_RejectsBadBaseRefs(t *testing.T) {
	cases := []struct{ name, ref, why string }{
		{"leading-dash", "-x", "option-like"},
		{"internal-space", "a b", "whitespace"},
		{"nonexistent", "no-such-ref", "unresolvable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newWorktreeRepo(t)
			_, err := r.create(t, map[string]any{"name": c.name, "base_ref": c.ref})
			if err == nil {
				t.Fatalf("expected create to reject %s base_ref %q", c.why, c.ref)
			}
			// No worktree registered for a rejected base.
			if branchExistsInRepo(t, r.mainRoot, c.name) {
				t.Errorf("branch %q created despite a rejected base_ref", c.name)
			}
		})
	}
}

func TestWorktreeCreate_BranchExistsSuggestsSwitchOnlyWhenManaged(t *testing.T) {
	// (a) A plain branch with no managed worktree: error, NO switch suggestion.
	t.Run("unmanaged branch", func(t *testing.T) {
		r := newWorktreeRepo(t)
		wtGit(t, r.mainRoot, "branch", "plain", r.head)
		_, err := r.create(t, map[string]any{"name": "plain"})
		if err == nil {
			t.Fatal("expected an error for an existing branch")
		}
		if strings.Contains(err.Error(), "switch") {
			t.Errorf("must not suggest switch when no managed worktree exists: %v", err)
		}
	})

	// (b) A managed worktree already exists: error DOES suggest switch.
	t.Run("managed worktree", func(t *testing.T) {
		r := newWorktreeRepo(t)
		if _, err := r.create(t, map[string]any{"name": "dup"}); err != nil {
			t.Fatalf("first create: %v", err)
		}
		_, err := r.create(t, map[string]any{"name": "dup"})
		if err == nil {
			t.Fatal("expected an error re-creating an existing name")
		}
		if !strings.Contains(err.Error(), "switch") {
			t.Errorf("expected a switch suggestion for an existing managed worktree: %v", err)
		}
	})
}

func TestWorktreeCreate_RejectsInvalidName(t *testing.T) {
	r := newWorktreeRepo(t)
	for _, bad := range []string{"", "-lead", "has space", "a..b", "trailing/"} {
		if _, err := r.create(t, map[string]any{"name": bad}); err == nil {
			t.Errorf("expected create to reject name %q", bad)
		}
	}
}

func TestWorktreeCreate_NonLocalEnvErrors(t *testing.T) {
	r := newWorktreeRepo(t)
	// Swap the session env to a non-local one; create must refuse clearly.
	r.s.mu.Lock()
	r.s.env = &timeoutEnv{wd: r.mainRoot}
	r.s.mu.Unlock()

	_, err := r.s.worktreeCreate(context.Background(), "x", "")
	if err == nil || !strings.Contains(err.Error(), "local execution environment") {
		t.Fatalf("want a clear non-local-env error, got %v", err)
	}
}

func TestWorktreeCreate_TooOldGitPreflightErrors(t *testing.T) {
	r := newWorktreeRepo(t)

	// PATH-shim a `git` that reports an ancient version. The main repo root
	// resolves structurally (no git needed), so the shim is only hit by the
	// version preflight, which must refuse before any lifecycle git call.
	shimDir := t.TempDir()
	shim := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ]; then echo \"git version 2.20.0\"; exit 0; fi\n" +
		"echo \"shim: unexpected git $*\" >&2; exit 1\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := r.create(t, map[string]any{"name": "x"})
	if err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("want a too-old-git preflight error, got %v", err)
	}
}

// branchExistsInRepo reports whether refs/heads/<name> exists in the repo at
// root, via a throwaway git invocation (separate from the tool's own check).
func branchExistsInRepo(t *testing.T, root, name string) bool {
	t.Helper()
	env := execenv.NewLocalExecutionEnvironment(root)
	cmd := "git " + execenv.ShellEscapeArgs("show-ref", "--verify", "--quiet", "refs/heads/"+name)
	res, _ := env.ExecCommand(context.Background(), cmd, 10_000, root, nil)
	return res.ExitCode == 0
}
