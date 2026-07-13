package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// injectRunningShell inserts a synthetic running background-shell job into a
// session's own job manager rooted at dir, mirroring the shape a real
// runShell(Background:true) leaves in jm.running (jobs.go liveWorkHandles). It
// returns the job id and registers cleanup.
func injectRunningShell(t *testing.T, s *Session, jobID, dir string) string {
	t.Helper()
	jm := s.jobManager
	jm.mu.Lock()
	jm.running[jobID] = &runningJob{rec: &jobstore.JobRecord{
		JobID:      jobID,
		Type:       jobstore.JobShell,
		Status:     jobstore.StatusRunning,
		WorkingDir: dir,
	}}
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, jobID)
		jm.mu.Unlock()
	})
	return jobID
}

// TestLiveShellsUnderTree_FindsGrandchildShell_ParentScanMisses is the brief's
// red-first proof (spec §P1 step 3): a background shell running in a
// GRANDCHILD's own job manager, rooted under a path, is invisible to a
// parent-only scan (jm.liveShellHandles) because each manager only sees its own
// running map — but the tree-wide walk liveShellsUnderTree finds it.
func TestLiveShellsUnderTree_FindsGrandchildShell_ParentScanMisses(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	lane := filepath.Join(base, "lane")
	gcDir := filepath.Join(lane, "gc")
	if err := os.MkdirAll(gcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	parent := newSession(t, withDir(base))
	child := newSession(t, withDir(lane))
	parent.subagents.track(&subagent{id: child.id, sess: child})
	grandchild := newSession(t, withDir(gcDir))
	child.subagents.track(&subagent{id: grandchild.id, sess: grandchild})

	shellID := injectRunningShell(t, grandchild, "job_gc_shell", gcDir)

	// Parent-only scan cannot see the grandchild's shell.
	for _, h := range parent.jobManager.liveShellHandles() {
		if strings.Contains(h.handle, shellID) {
			t.Fatalf("parent-only scan unexpectedly saw the grandchild shell: %q", h.handle)
		}
	}

	// The tree-wide walk finds it.
	live := parent.liveShellsUnderTree(lane)
	found := false
	for _, l := range live {
		if strings.Contains(l, shellID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("liveShellsUnderTree(%q) = %v, want it to include the grandchild shell %s", lane, live, shellID)
	}
}

// TestLiveShellsUnderTree_FiltersByPathAndType confirms the walk excludes a
// grandchild shell rooted OUTSIDE the target path and ignores delegate job
// records (shell-only), so it neither over- nor under-reports.
func TestLiveShellsUnderTree_FiltersByPathAndType(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	lane := filepath.Join(base, "lane")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(lane, 0o755); err != nil {
		t.Fatalf("mkdir lane: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}

	parent := newSession(t, withDir(base))
	child := newSession(t, withDir(lane))
	parent.subagents.track(&subagent{id: child.id, sess: child})

	inLane := injectRunningShell(t, child, "job_in_lane", lane)
	injectRunningShell(t, child, "job_outside", outside)

	// A delegate job record under the lane must NOT be picked up (shell-only).
	jm := child.jobManager
	jm.mu.Lock()
	jm.running["job_dlg"] = &runningJob{rec: &jobstore.JobRecord{
		JobID:           "job_dlg",
		Type:            jobstore.JobDelegate,
		Status:          jobstore.StatusRunning,
		DelegateRestore: &jobstore.DelegateRestoreDescriptor{WorkingDir: lane},
	}}
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, "job_dlg")
		jm.mu.Unlock()
	})

	live := parent.liveShellsUnderTree(lane)
	joined := strings.Join(live, ", ")
	if !strings.Contains(joined, inLane) {
		t.Errorf("want in-lane shell %s reported, got %v", inLane, live)
	}
	if strings.Contains(joined, "job_outside") {
		t.Errorf("outside-lane shell should be filtered, got %v", live)
	}
	if strings.Contains(joined, "job_dlg") {
		t.Errorf("delegate job record must not appear in a shell-only walk, got %v", live)
	}
}
