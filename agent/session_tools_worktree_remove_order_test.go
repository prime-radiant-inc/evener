package agent

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
)

// When more than one remove guard would refuse, the earlier one wins and the
// later ones are never consulted: the lock check precedes the live-work scan,
// which precedes provenance, which precedes the dirtiness probe. A refusal
// leaves the lane in place.

func TestWorktreeRemove_ForeignLockIsRefusedBeforeLiveWorkIsScanned(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	path := sr.addManagedLane(t, "lane")
	sr.setLaneLock(t, path, worktree.FormatSessionMarker("01FOREIGNSESSIONID0000004"))

	var scans atomic.Int32
	r.s.worktreeLiveWorkStub = func(string) []string {
		scans.Add(1)
		return []string{"job_abc123 (shell, running)"}
	}

	if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err == nil {
		t.Fatal("remove of a foreign-locked, busy lane was not refused")
	}
	if got := scans.Load(); got != 0 {
		t.Errorf("live-work scan ran %d times after the lock already refused", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lane removed despite the refusal: %v", err)
	}
}

func TestWorktreeRemove_LiveWorkIsRefusedBeforeTheDirtinessProbe(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	path := sr.addManagedLane(t, "lane")
	r.s.worktreeLiveWorkStub = func(p string) []string {
		if p == filepath.Clean(path) {
			return []string{"job_abc123 (shell, running)"}
		}
		return nil
	}

	if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err == nil {
		t.Fatal("remove of a lane with live work was not refused")
	}
	if sr.sawGitCommand("-C", path, "status") {
		t.Error("dirtiness probe ran after the live-work guard already refused")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lane removed despite the refusal: %v", err)
	}
}

func TestWorktreeRemove_MissingProvenanceIsRefusedBeforeTheDirtinessProbe(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	path := sr.addManagedLane(t, "lane")
	metaDir := r.metaDir(t, r.canonicalMain(t))
	if err := r.s.deleteWorktreeSidecar(metaDir, "lane"); err != nil {
		t.Fatalf("delete sidecar: %v", err)
	}

	if _, err := r.removeOp(t, map[string]any{"name": "lane"}); err == nil {
		t.Fatal("remove of a lane with no sidecar was not refused without force")
	}
	if sr.sawGitCommand("-C", path, "status") {
		t.Error("dirtiness probe ran after the provenance guard already refused")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lane removed despite the refusal: %v", err)
	}
}

// The negative assertions above are only meaningful if a remove that passes
// every guard does reach the dirtiness probe.
func TestWorktreeRemove_ACleanRemoveReachesTheDirtinessProbe(t *testing.T) {
	t.Parallel()
	sr := newScriptedLaneRepo(t)
	path := sr.addManagedLane(t, "lane")

	if _, err := sr.wt().removeOp(t, map[string]any{"name": "lane"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !sr.sawGitCommand("-C", path, "status") {
		t.Error("dirtiness probe was not recorded for a remove that passed every guard")
	}
}
