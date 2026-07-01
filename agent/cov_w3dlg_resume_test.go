package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// TestW3Dlg_ResumeRunningSubActiveJobUnknown covers the resume fast-path guard:
// a sub already marked running whose transcript_ref has no matching running
// delegate job surfaces the active_delegate_not_found error.
func TestW3Dlg_ResumeRunningSubActiveJobUnknown(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: true,
		status:  SubagentRunning,
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)

	run, finalizeErr, active, err := parent.resumeOrFindRunningDelegate(
		parent.jobManager, child.ID(), "hi", sub,
		encodeRef("", child.ID()), "dlg_x", nil, nil, false, nil)
	if run != nil || finalizeErr != nil || active != nil {
		t.Fatalf("run=%v finalizeErr=%v active=%v, want all nil", run, finalizeErr, active)
	}
	if err == nil || !strings.Contains(err.Error(), "active job is unknown") {
		t.Fatalf("err = %v, want active job is unknown", err)
	}
}

// TestW3Dlg_ResumeRunningSubFindsActiveJob covers the resume fast-path success
// return: a sub already marked running whose transcript_ref matches a live
// running delegate job returns that active job for steering.
func TestW3Dlg_ResumeRunningSubFindsActiveJob(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:     child.ID(),
		sess:   child,
		status: SubagentRunning,
		done:   make(chan struct{}),
	}
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "task", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		parent.jobManager.abandonRunningJob(run.rec.JobID)
	})
	sub.mu.Lock()
	sub.running = true
	sub.mu.Unlock()

	got, finalizeErr, active, err := parent.resumeOrFindRunningDelegate(
		parent.jobManager, child.ID(), "hi", sub,
		run.rec.TranscriptRef, "dlg_x", nil, nil, false, nil)
	if err != nil || got != nil || finalizeErr != nil {
		t.Fatalf("got=%v finalizeErr=%v err=%v, want active job only", got, finalizeErr, err)
	}
	if active == nil || active.JobID != run.rec.JobID {
		t.Fatalf("active = %+v, want running job %s", active, run.rec.JobID)
	}
}

// TestW3Dlg_ResumeSessionClosed covers the closing-session guard: resuming a
// terminal delegate while the parent session is closing is rejected before any
// job is attached.
func TestW3Dlg_ResumeSessionClosed(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)

	parent.mu.Lock()
	parent.closing = true
	parent.mu.Unlock()

	run, finalizeErr, active, err := parent.resumeOrFindRunningDelegate(
		parent.jobManager, child.ID(), "hi", sub,
		encodeRef("", child.ID()), "dlg_x", nil, (*jobstore.DelegateRestoreDescriptor)(nil), false, nil)
	if run != nil || finalizeErr != nil || active != nil {
		t.Fatalf("run=%v finalizeErr=%v active=%v, want all nil", run, finalizeErr, active)
	}
	if err == nil || !strings.Contains(err.Error(), "session is closed") {
		t.Fatalf("err = %v, want session is closed", err)
	}
}
