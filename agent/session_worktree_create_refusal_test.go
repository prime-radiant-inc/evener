package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
)

// armCloseDuringSwap makes the next environment swap on r.s run the session's
// close to completion between the scratch move and the install, so the swap
// is refused and its caller has to undo the git changes it already made. The
// returned channel closes when that close has finished.
func armCloseDuringSwap(r *wtRepo) <-chan struct{} {
	closeBegun := make(chan struct{})
	closeDone := make(chan struct{})
	r.s.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(closeBegun) }
	r.s.cfg.testOnly.swapEnvAfterAdopt = func() {
		go func() {
			defer close(closeDone)
			r.s.Close()
		}()
		<-closeBegun
		<-closeDone
	}
	return closeDone
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
			st := r.s.worktreeStateSnapshot()
			if err := st.resolutionError("create"); err != nil {
				t.Fatalf("worktree state: %v", err)
			}
			projectDir := filepath.Join(st.worktreeRoot, st.project.ID)
			path := filepath.Join(projectDir, filepath.FromSlash(name))
			sidecar := filepath.Join(metaDirForProject(projectDir), worktree.EncodeSidecarName(name)+".json")
			closeDone := armCloseDuringSwap(r)

			_, err := r.create(t, map[string]any{"name": name})
			<-closeDone

			if err == nil {
				t.Fatal("create succeeded while the session closed under its swap, want a refusal")
			}
			if sr.lanePresent(path) {
				_, locked, reason := sr.laneLocked(t, path)
				t.Errorf("the refused create left lane %s registered (locked=%v %q), want it removed", path, locked, reason)
			}
			if sr.branchExists(t, name) {
				t.Errorf("the refused create left branch %q behind", name)
			}
			if _, statErr := os.Stat(sidecar); statErr == nil {
				t.Errorf("the refused create left sidecar %s behind", sidecar)
			}
			if got := r.s.Meta().WorktreePath; got != "" {
				t.Errorf("the refused create recorded occupancy %q, want none", got)
			}
		})
	}
}
