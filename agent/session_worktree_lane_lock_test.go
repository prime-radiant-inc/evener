package agent

import (
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
