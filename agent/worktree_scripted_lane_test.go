package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// scriptedLaneRepo is the delegate-lane counterpart to wtRepo: the same session
// orchestration and the same on-disk sidecars and .git pointer files, with
// scriptedWorktreeGit standing in for the git binary.
//
// WHEN TO USE THIS instead of newWorktreeRepo: only for a test whose subject is
// serf's own decision-making — which refusal rung fires, what event is emitted,
// what serf wrote to its own metadata, control flow. A test whose subject is
// git's observable behavior (real registry effects, real ancestry or dirty
// detection, real porcelain output, real ref rules) MUST use the real-git
// wtRepo harness, or it would be asserting against the model in
// session_tools_worktree_scripted_test.go rather than against git. See
// docs/testing.md.
//
// The saving is large enough to matter: a real-git lane test averages ~1.2s and
// spawns ~14 git subprocesses; a scripted one runs in ~0.04s.
type scriptedLaneRepo struct {
	t        *testing.T
	s        *Session
	git      *scriptedWorktreeGit
	mainRoot string
	stateDir string
	clock    *agenttest.FakeClock
}

// newScriptedLaneRepo builds a session whose worktree git boundary is scripted,
// rooted at a real temp directory so sidecar and .git-pointer persistence stays
// real. cfgFns may adjust the SessionConfig before the session is built.
func newScriptedLaneRepo(t *testing.T) *scriptedLaneRepo {
	t.Helper()
	return newScriptedLaneRepoWithClock(t, agenttest.NewFakeClock())
}

// newScriptedLaneRepoWithClock is newScriptedLaneRepo with a caller-owned fake
// clock, for tests that advance time to fire a lane timer.
func newScriptedLaneRepoWithClock(t *testing.T, clk *agenttest.FakeClock) *scriptedLaneRepo {
	t.Helper()
	cfg := worktreeTestSessionConfig()
	cfg.clock = clk
	return newScriptedLaneRepoWithConfig(t, cfg)
}

// newScriptedLaneRepoWithConfig builds the session from a caller-supplied config,
// overlaying only what the scripted boundary requires. A caller that needs a
// specific spawn parentage or clock sets it on cfg.
func newScriptedLaneRepoWithConfig(t *testing.T, cfg SessionConfig) *scriptedLaneRepo {
	t.Helper()
	root := scriptedCanonicalDir(t, t.TempDir())
	stateDir := scriptedCanonicalDir(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".git", "worktrees"), 0o755); err != nil {
		t.Fatalf("create scripted main git dir: %v", err)
	}

	git := newScriptedWorktreeGit(root)
	cfg.StateDir = stateDir
	cfg.NoProjectPrompts = true
	if cfg.MaxSubagentDepth == 0 {
		cfg.MaxSubagentDepth = 1
	}
	cfg.testOnly.skipGitSnapshot = true
	cfg.testOnly.minimalSystemPrompt = true
	cfg.testOnly.noSyncJobStore = true
	cfg.testOnly.environmentInfo = scriptedEnvironmentInfo
	cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
		return git.run
	}

	s := newSession(t, withDir(root), withConfig(cfg))
	s.stateDir = stateDir
	s.mu.Lock()
	s.worktreeGitVersionOK = true
	s.mu.Unlock()

	clock, _ := cfg.clock.(*agenttest.FakeClock)
	return &scriptedLaneRepo{t: t, s: s, git: git, mainRoot: root, stateDir: stateDir, clock: clock}
}

// laneLocked reports the scripted registry's lock state for path, matching
// wtRepo.laneLocked's signature so a converted test body needs no edit.
func (r *scriptedLaneRepo) laneLocked(t *testing.T, path string) (present, locked bool, reason string) {
	t.Helper()
	entry := r.git.entry(path)
	if entry == nil {
		return false, false, ""
	}
	return true, entry.lockReason != "", entry.lockReason
}

// lanePresent reports whether the scripted registry still has an entry.
func (r *scriptedLaneRepo) lanePresent(path string) bool {
	return r.git.entry(path) != nil
}

// branchExists reports whether the scripted branch set still carries name.
func (r *scriptedLaneRepo) branchExists(t *testing.T, name string) bool {
	t.Helper()
	_, ok := r.git.branches[name]
	return ok
}

// unlockLane releases a lane's lock directly, standing in for the foreign
// session's close-time unlock that leaves the lane as residue.
func (r *scriptedLaneRepo) unlockLane(t *testing.T, path string) {
	t.Helper()
	entry := r.git.entry(path)
	if entry == nil {
		t.Fatalf("no scripted entry for %q", path)
	}
	entry.lockReason = ""
}

// setLaneLock forces a lane's lock reason, for seeding a foreign or bare lock.
func (r *scriptedLaneRepo) setLaneLock(t *testing.T, path, reason string) {
	t.Helper()
	entry := r.git.entry(path)
	if entry == nil {
		t.Fatalf("no scripted entry for %q", path)
	}
	entry.lockReason = reason
}

// removeLane deregisters a lane and deletes its directory, standing in for
// `git worktree remove --force`.
func (r *scriptedLaneRepo) removeLane(t *testing.T, path string) {
	t.Helper()
	if _, err := r.git.run("worktree", "remove", "--force", "--", path); err != nil {
		t.Fatalf("scripted worktree remove %q: %v", path, err)
	}
}

// failLockRunner makes `worktree lock` fail while the returned flag is set,
// wrapping the scripted runner rather than real git.
func (r *scriptedLaneRepo) failLockRunner() *atomic.Bool {
	var fail atomic.Bool
	r.s.cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
		return func(args ...string) (string, error) {
			if fail.Load() && len(args) >= 2 && args[0] == "worktree" && args[1] == "lock" {
				return "", errors.New("injected worktree lock failure")
			}
			return r.git.run(args...)
		}
	}
	return &fail
}

// ageBeyondGrace backdates a lane's sidecar mtime past laneGrace, so a residue
// sweep considers it collectible. The sidecar is serf's own metadata and stays a
// real file under the scripted boundary; lanePath locates its meta dir the same
// way production does.
func (r *scriptedLaneRepo) ageBeyondGrace(t *testing.T, delegateID, lanePath string) {
	t.Helper()
	ageSidecar(t, metaDirForLane(lanePath), delegateID, laneGrace+time.Minute)
}

// appendDisposed records a Disposed event for delegateID in the session's own
// jobstore.
func (r *scriptedLaneRepo) appendDisposed(t *testing.T, delegateID string) {
	t.Helper()
	if err := r.s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateDisposed,
		TS:         time.Now().UTC(),
		DelegateID: delegateID,
	}); err != nil {
		t.Fatalf("append disposed: %v", err)
	}
}

// seedIsolationLane creates a delegate lane through the real production path
// (createDelegateWorktree drives the scripted git boundary) and records the
// job/delegate events a resumed session reads back.
func (r *scriptedLaneRepo) seedIsolationLane(t *testing.T) (delegateID, lanePath string) {
	t.Helper()
	return seedIsolationLaneOn(t, r.s)
}
