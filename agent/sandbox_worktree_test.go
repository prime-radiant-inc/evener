package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

// sbxWorktreeSession builds a minimal session suitable for exercising the worktree
// env-swap paths (no git snapshot / project prompts, minimal prompt).
func sbxWorktreeSession(t *testing.T) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	return newSession(t, withClient(c), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
}

// sbxMainAndLanes materializes a main repo with two linked worktrees.
func sbxMainAndLanes(t *testing.T) (main, laneA, laneB, home string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	sbxGit(t, main, "init", "-q")
	sbxGit(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	laneA = filepath.Join(base, "a")
	laneB = filepath.Join(base, "b")
	sbxGit(t, main, "worktree", "add", "-q", laneA, "-b", "fa")
	sbxGit(t, main, "worktree", "add", "-q", laneB, "-b", "fb")
	return evalSyms(t, main), evalSyms(t, laneA), evalSyms(t, laneB), t.TempDir()
}

func evalSyms(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(r)
}

// TestWorktreeControlEnvUsesControlPolicy: the manage_worktree control env carries
// the CONTROL policy (main repo + registry writable, config/hooks denied), NOT the
// current worktree's tool policy.
func TestWorktreeControlEnvUsesControlPolicy(t *testing.T) {
	main, laneA, _, home := sbxMainAndLanes(t)
	facts := sbxBwrapFacts(home)

	s := sbxWorktreeSession(t)
	laneEnv := execenv.NewLocalExecutionEnvironment(laneA)
	laneEnv.Sandbox = sbxResolve(t, facts, laneA, sandbox.ModeWorkspaceWrite)
	s.mu.Lock()
	s.env = laneEnv
	s.mu.Unlock()

	ctrl, err := s.worktreeControlEnv(main)
	if err != nil {
		t.Fatalf("worktreeControlEnv: %v", err)
	}
	le := ctrl.(*execenv.LocalExecutionEnvironment)
	if le.Sandbox == nil || !le.Sandbox.Enforced() {
		t.Fatal("control env must be sandboxed")
	}
	// Anchored at the MAIN repo, not the current lane's tool root.
	if le.Sandbox.Git.WorktreeRoot != main {
		t.Errorf("control env worktree = %q, want main %q (not lane %q)", le.Sandbox.Git.WorktreeRoot, main, laneA)
	}
	registry := filepath.Join(main, ".git", "worktrees")
	if !rootGrantsAny(le.Sandbox.Spawned.WriteRoots, registry) {
		t.Errorf("control env must grant the registry %q writable: %v", registry, le.Sandbox.Spawned.WriteRoots)
	}
	for _, denied := range []string{filepath.Join(main, ".git", "config"), filepath.Join(main, ".git", "hooks")} {
		if !slices.Contains(le.Sandbox.Git.ProtectedPaths, denied) {
			t.Errorf("control env must keep %q write-protected: %v", denied, le.Sandbox.Git.ProtectedPaths)
		}
	}
}

// TestEnterExitWorktreeReRootsAndRestores: entering a managed worktree re-roots the
// session sandbox to that worktree; exiting restores the pre-worktree env with its
// original roots.
func TestEnterExitWorktreeReRootsAndRestores(t *testing.T) {
	_, laneA, laneB, home := sbxMainAndLanes(t)
	facts := sbxBwrapFacts(home)

	s := sbxWorktreeSession(t)
	laneAEnv := execenv.NewLocalExecutionEnvironment(laneA)
	laneAEnv.Sandbox = sbxResolve(t, facts, laneA, sandbox.ModeWorkspaceWrite)
	s.swapEnvAndRefresh(laneAEnv)

	s.enterWorktree(laneB, true)
	entered := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if entered.Sandbox == nil || entered.Sandbox.Git.WorktreeRoot != laneB {
		t.Errorf("enterWorktree must re-root the sandbox to lane B %q, got %+v", laneB, entered.Sandbox)
	}
	if !rootGrantsAny(entered.Sandbox.FileTool.WriteRoots, laneB) || rootGrantsAny(entered.Sandbox.FileTool.WriteRoots, laneA) {
		t.Errorf("entered sandbox write roots must be lane B only: %v", entered.Sandbox.FileTool.WriteRoots)
	}

	root, ok := s.exitWorktree()
	if !ok || root != laneA {
		t.Fatalf("exitWorktree must restore lane A, got (%q, %v)", root, ok)
	}
	restored := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if restored.Sandbox == nil || restored.Sandbox.Git.WorktreeRoot != laneA {
		t.Errorf("exitWorktree must restore the lane A sandbox, got %+v", restored.Sandbox)
	}
	if restored.SandboxReRootError() != nil {
		t.Errorf("restoring a saved env must not re-root or error, got %v", restored.SandboxReRootError())
	}
}

// rootGrantsAny reports whether any root equals or is an ancestor of target.
func rootGrantsAny(roots []string, target string) bool {
	for _, r := range roots {
		if r == target {
			return true
		}
		if rel, err := filepath.Rel(r, target); err == nil && rel != ".." && !hasDotDotPrefix(rel) {
			return true
		}
	}
	return false
}

func hasDotDotPrefix(rel string) bool {
	return rel == ".." || (len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && rel[2] == filepath.Separator)
}
