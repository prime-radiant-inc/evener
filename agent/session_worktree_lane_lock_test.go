package agent

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/identifier"
)

// TestTransferWorktreeLaneTo_movesTheMarker pins the half of thread/clear's
// lane preservation the daemon cannot show through its sandboxed git view: the
// predecessor's occupancy marker is rewritten to the replacement while the
// predecessor is still live, and the undo puts it back (#2957).
func TestTransferWorktreeLaneTo_movesTheMarker(t *testing.T) {
	r := newWorktreeRepo(t)
	lane := filepath.Join(t.TempDir(), "adopted-lane")
	wtGit(t, r.mainRoot, "worktree", "add", "-q", lane, "-b", "adopted-lane")
	if err := r.s.enterWorktree(lane, true); err != nil {
		t.Fatalf("enter the lane: %v", err)
	}
	oldMarker := worktree.FormatSessionMarker(r.s.ID())
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", oldMarker, lane)

	newID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatalf("new session id: %v", err)
	}
	undo, err := r.s.TransferWorktreeLaneTo(newID)
	if err != nil {
		t.Fatalf("TransferWorktreeLaneTo: %v", err)
	}
	if undo == nil {
		t.Fatal("TransferWorktreeLaneTo returned no undo for an owned managed lane")
	}
	out := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	if !strings.Contains(out, "locked evener:"+newID) {
		t.Fatalf("lane is not locked for the replacement:\n%s", out)
	}

	undo()
	out = wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	if !strings.Contains(out, "locked "+oldMarker) {
		t.Fatalf("undo did not restore the predecessor's marker:\n%s", out)
	}
}

// TestTransferWorktreeLaneTo_keepsTheLockDuringRewrite pins the no-unlock half
// of the transfer: a competing git worktree lock must keep failing while the
// predecessor's marker is rewritten for the replacement. The old unlock/relock
// implementation opened a window where that acquisition could succeed.
func TestTransferWorktreeLaneTo_keepsTheLockDuringRewrite(t *testing.T) {
	r := newWorktreeRepo(t)
	lane := filepath.Join(t.TempDir(), "adopted-lane")
	wtGit(t, r.mainRoot, "worktree", "add", "-q", lane, "-b", "adopted-lane")
	if err := r.s.enterWorktree(lane, true); err != nil {
		t.Fatalf("enter the lane: %v", err)
	}
	oldMarker := worktree.FormatSessionMarker(r.s.ID())
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", oldMarker, lane)

	installWorktreeSeams(t, r.s, worktreeTestSeams{
		beforeLaneLockRewrite: func(lanePath string) {
			cmd := exec.Command("git", "-C", r.mainRoot, "worktree", "lock", "--reason", "competitor", lanePath)
			if _, err := cmd.CombinedOutput(); err == nil {
				t.Error("a competing acquisition succeeded while the lane lock was being rewritten")
			}
			// A failed competitor must leave the lane's occupancy marker
			// unchanged; the diagnostic text is git's, not this test's contract.
			if listing := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain"); !strings.Contains(listing, "locked "+oldMarker) {
				t.Errorf("competing acquisition changed the lane's occupancy marker:\n%s", listing)
			}
		},
	})

	newID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatalf("new session id: %v", err)
	}
	if _, err := r.s.TransferWorktreeLaneTo(newID); err != nil {
		t.Fatalf("TransferWorktreeLaneTo: %v", err)
	}
	out := wtGit(t, r.mainRoot, "worktree", "list", "--porcelain")
	if !strings.Contains(out, "locked evener:"+newID) {
		t.Fatalf("lane is not locked for the replacement:\n%s", out)
	}
}
