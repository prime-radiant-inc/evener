package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func w3dlg_attachSub(child *Session) *subagent {
	return &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		done:    make(chan struct{}),
	}
}

func w3dlg_attachLink() delegateJobLink {
	return delegateJobLink{
		delegateID: jobstore.NewDelegateID(),
		generation: jobstore.NewDelegateGeneration(),
		create:     true,
	}
}

// TestW3Dlg_AttachTreeAtCapacity covers the resume-path tree-slot reservation:
// when the tree counter is saturated, a resume attach (prepared == nil) fails
// with tree_at_capacity before opening any output.
func TestW3Dlg_AttachTreeAtCapacity(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	if parent.treeCounter == nil {
		t.Fatal("root session has no tree counter; cannot saturate")
	}
	for parent.treeCounter.reserve() {
	}
	t.Cleanup(func() {
		for parent.treeCounter.n.Load() > 0 {
			parent.treeCounter.release()
		}
	})

	run, err := parent.attachDelegateJobWithRestoreAndDelegate(
		parent.jobManager, child.ID(), "task", w3dlg_attachSub(child),
		jobstore.NewJobID(), nil, false, nil, nil, w3dlg_attachLink(), nil)
	if run != nil {
		t.Fatalf("run = %v, want nil at capacity", run)
	}
	if !errors.Is(err, errTreeAtCapacity) {
		t.Fatalf("err = %v, want errTreeAtCapacity", err)
	}
}

// TestW3Dlg_AttachOpenOutputFails covers the output-open error path: when the
// job log path cannot be opened, the reserved tree slot is released and the
// error is surfaced.
func TestW3Dlg_AttachOpenOutputFails(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	jm := parent.jobManager
	jobID := jobstore.NewJobID()
	// Pre-create a directory where the job log file would go so OpenOutput fails.
	logDir := filepath.Join(jm.dir, "jobs", jobID+".log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log collision dir: %v", err)
	}

	before := int64(-1)
	if parent.treeCounter != nil {
		before = parent.treeCounter.n.Load()
	}

	run, err := parent.attachDelegateJobWithRestoreAndDelegate(
		jm, child.ID(), "task", w3dlg_attachSub(child),
		jobID, nil, false, nil, nil, w3dlg_attachLink(), nil)
	if run != nil || err == nil {
		t.Fatalf("run=%v err=%v, want nil run and open error", run, err)
	}
	if parent.treeCounter != nil {
		if after := parent.treeCounter.n.Load(); after != before {
			t.Fatalf("tree slot leaked: before=%d after=%d", before, after)
		}
	}
}

// TestW3Dlg_AttachJobManagerClosing covers the closing-manager guard: an attach
// racing a closing job manager cleans up its output/slot and returns
// errJobManagerClosing.
func TestW3Dlg_AttachJobManagerClosing(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	jm := parent.jobManager

	jm.mu.Lock()
	jm.closing = true
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		jm.closing = false
		jm.mu.Unlock()
	})

	run, err := parent.attachDelegateJobWithRestoreAndDelegate(
		jm, child.ID(), "task", w3dlg_attachSub(child),
		jobstore.NewJobID(), nil, false, nil, nil, w3dlg_attachLink(), nil)
	if run != nil {
		t.Fatalf("run = %v, want nil while closing", run)
	}
	if !errors.Is(err, errJobManagerClosing) {
		t.Fatalf("err = %v, want errJobManagerClosing", err)
	}
}
