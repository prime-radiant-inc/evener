package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
)

// armCloseDuringSwap makes the next environment swap on r.s run the session's
// close to completion between the scratch move and the install, so the swap
// is refused and its caller has to undo the git changes it already made.
// before, when non-nil, runs in that window first (a test cancels the request
// context there, as the close's own cancel does). The returned channel closes
// when that close has finished.
func armCloseDuringSwap(r *wtRepo, before func()) <-chan struct{} {
	closeBegun := make(chan struct{})
	closeDone := make(chan struct{})
	r.s.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(closeBegun) }
	r.s.cfg.testOnly.swapEnvAfterAdopt = func() {
		if before != nil {
			before()
		}
		go func() {
			defer close(closeDone)
			r.s.Close()
		}()
		<-closeBegun
		<-closeDone
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
	worktreeSeams.Store(r.s, worktreeTestSeams{enterWorktree: func(string, bool) error { return boom }})
	t.Cleanup(func() { worktreeSeams.Delete(r.s) })

	_, err := r.s.worktreeCreate(context.Background(), "lane", "")

	if !errors.Is(err, boom) {
		t.Fatalf("create error = %v, want the injected %v", err, boom)
	}
	assertLaneRolledBack(t, sr, r, "lane", path, sidecar)
}
