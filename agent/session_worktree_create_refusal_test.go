package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
)

// armCloseDuringSwap begins the session's close between the next environment
// swap's scratch move and its install, so the swap is refused and its caller
// has to undo the git changes it already made. before, when non-nil, runs in
// that window first (a test cancels the request context there, as the close's
// own cancel does). The returned channel closes when that close has finished,
// which every caller waits for before asserting.
//
// The window ends once the close has set `closing` and passed its dispose and
// sweep joins — not once the close has finished. Close waits for an admitted
// swap before it cleans the environment (swapEnvAndRefresh), so a hook holding
// the swap until the close returned would hold it for the whole close budget
// and then trip that join's give-up warning.
func armCloseDuringSwap(r *wtRepo, before func()) <-chan struct{} {
	closeBegun := make(chan struct{})
	closeDone := make(chan struct{})
	r.s.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(closeBegun) }
	r.s.cfg.testOnly.swapEnvAfterAdopt = func(context.Context) {
		if before != nil {
			before()
		}
		go func() {
			defer close(closeDone)
			r.s.Close()
		}()
		<-closeBegun
	}
	return closeDone
}

// contextBoundScriptedGit makes r's scripted git honor the context each runner
// is built with, the way the real runner does: a runner bound to a cancelled
// request context fails every command.
func contextBoundScriptedGit(sr *scriptedLaneRepo) {
	sr.s.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, _ execenv.ExecutionEnvironment) worktree.GitRunner {
		return func(args ...string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return sr.git.run(args...)
		}
	}
}

// createLaneExpectations resolves where a create of name puts its lane and its
// sidecar, so a refusal test can assert both are gone.
func createLaneExpectations(t *testing.T, r *wtRepo, name string) (path, sidecar string) {
	t.Helper()
	st := r.s.worktreeStateSnapshot()
	if err := st.resolutionError("create"); err != nil {
		t.Fatalf("worktree state: %v", err)
	}
	projectDir := filepath.Join(st.worktreeRoot, st.project.ID)
	return filepath.Join(projectDir, filepath.FromSlash(name)), filepath.Join(metaDirForProject(projectDir), worktree.EncodeSidecarName(name)+".json")
}

// assertLaneRolledBack checks that nothing of a create of name remains: no
// registry entry, no branch, no sidecar, no recorded occupancy.
func assertLaneRolledBack(t *testing.T, sr *scriptedLaneRepo, r *wtRepo, name, path, sidecar string) {
	t.Helper()
	if sr.lanePresent(path) {
		_, locked, reason := sr.laneLocked(t, path)
		t.Errorf("lane %s is still registered (locked=%v %q), want it removed", path, locked, reason)
	}
	if sr.branchExists(t, name) {
		t.Errorf("branch %q was left behind", name)
	}
	if _, statErr := os.Stat(sidecar); statErr == nil {
		t.Errorf("sidecar %s was left behind", sidecar)
	}
	if got := r.s.Meta().WorktreePath; got != "" {
		t.Errorf("occupancy %q was recorded, want none", got)
	}
}

// A create refused mid-swap has already added and locked the new lane and
// written its sidecar; it has to take all of that back, or the session leaves
// an orphaned, permanently locked worktree behind. A nested name puts the lane
// under a subdirectory while its sidecar stays in the project's own metadata
// dir, so the rollback must use the dir the create wrote to, not one derived
// from the lane path.
func TestWorktreeCreate_RefusedMidSwapLeavesNoLaneBehind(t *testing.T) {
	for _, name := range []string{"lane", "sub/lane"} {
		t.Run(name, func(t *testing.T) {
			sr := newScriptedLaneRepo(t)
			r := sr.wt()
			path, sidecar := createLaneExpectations(t, r, name)
			closeDone := armCloseDuringSwap(r, nil)

			_, err := r.create(t, map[string]any{"name": name})
			<-closeDone

			if err == nil {
				t.Fatal("create succeeded while the session closed under its swap, want a refusal")
			}
			assertLaneRolledBack(t, sr, r, name, path, sidecar)
		})
	}
}

// The close that refuses the swap also cancels the request context the op's
// git runner is bound to, so a rollback through that runner fails silently.
// The rollback has to run on an independent, bounded control context.
func TestWorktreeCreate_RefusedMidSwapRollsBackOnACancelledRequestContext(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	contextBoundScriptedGit(sr)
	path, sidecar := createLaneExpectations(t, r, "lane")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeDone := armCloseDuringSwap(r, cancel)

	_, err := r.s.worktreeCreate(ctx, "lane", "")
	<-closeDone

	if err == nil {
		t.Fatal("create succeeded while the session closed under its swap, want a refusal")
	}
	assertLaneRolledBack(t, sr, r, "lane", path, sidecar)
}

// Any failure after the create core succeeded leaves the same lane, branch,
// and sidecar behind as the refusal does, so every one of them rolls back.
func TestWorktreeCreate_FailureAfterTheCoreRollsBack(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	path, sidecar := createLaneExpectations(t, r, "lane")
	boom := errors.New("injected enter failure")
	installWorktreeSeams(t, r.s, worktreeTestSeams{enterWorktree: func(string, bool) error { return boom }})

	_, err := r.s.worktreeCreate(context.Background(), "lane", "")

	if !errors.Is(err, boom) {
		t.Fatalf("create error = %v, want the injected %v", err, boom)
	}
	assertLaneRolledBack(t, sr, r, "lane", path, sidecar)
}

// The refused create's rollback runs AFTER the swap has left the close fence,
// on a control runner of its own. Its git forks on the same process table the
// session's close reaps, so the close has to wait for the rollback too, not
// just for the swap: walk past it and the environment is torn down mid-rollback,
// leaving behind the very lane the rollback was removing.
func TestWorktreeCreate_CloseWaitsForTheRefusedCreateRollback(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	path, sidecar := createLaneExpectations(t, r, "lane")

	cleanupObserved := make(chan struct{})
	var cleanupDuringRollback, holding atomic.Bool
	r.s.cfg.testOnly.envCleanupObserved = func(execenv.ExecutionEnvironment) { close(cleanupObserved) }

	// Hold the rollback at its first command — the lane unlock — and watch for
	// the close reaching environment cleanup while it is held. The wait happens
	// BEFORE the scripted double is entered, so it never blocks the close's own
	// git behind it.
	base := r.s.cfg.testOnly.worktreeGitRunner
	r.s.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		inner := base(ctx, env)
		return func(args ...string) (string, error) {
			if len(args) == 3 && args[0] == "worktree" && args[1] == "unlock" && args[2] == path &&
				holding.CompareAndSwap(false, true) {
				select {
				case <-cleanupObserved:
					cleanupDuringRollback.Store(true)
				case <-time.After(closeFenceProbe):
				}
			}
			return inner(args...)
		}
	}

	closeDone := armCloseDuringSwap(r, nil)

	_, err := r.create(t, map[string]any{"name": "lane"})
	<-closeDone

	if err == nil {
		t.Fatal("create succeeded while the session closed under its swap, want a refusal")
	}
	if !holding.Load() {
		t.Fatal("the rollback never ran its lane unlock; the test observed nothing")
	}
	if cleanupDuringRollback.Load() {
		t.Error("the close cleaned the session's environment while the refused create's rollback was still running")
	}
	assertLaneRolledBack(t, sr, r, "lane", path, sidecar)
}
