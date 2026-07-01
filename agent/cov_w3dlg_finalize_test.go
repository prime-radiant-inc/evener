package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// TestW3Dlg_FinalizeOnceChildMissing covers the nil-sub prepare arm: finalizing
// a delegate turn whose child subagent is gone marks the job failed/child_missing.
func TestW3Dlg_FinalizeOnceChildMissing(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:     child.ID(),
		sess:   child,
		status: SubagentCompleted,
		done:   make(chan struct{}),
	}
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "task", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	if err := parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, nil, false); err != nil {
		t.Fatalf("finalizeDelegateOnce: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)

	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusFailed || rec.Reason != "child_missing" {
		t.Fatalf("rec = %+v, want failed/child_missing", rec)
	}
}

// TestW3Dlg_FinalizeOnceStructuredCaptureFailed covers the structured-capture
// failure arm: a delegate with a result schema whose child session is not
// retained records the capture as failed.
func TestW3Dlg_FinalizeOnceStructuredCaptureFailed(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	childID := "w3dlg-detached-child"
	schema := map[string]any{"type": "object"}
	sub := &subagent{
		id:     childID,
		sess:   nil, // detached: structured capture cannot run
		status: SubagentCompleted,
		result: "final prose",
		done:   make(chan struct{}),
	}
	run, err := parent.attachDelegateJobWithID(parent.jobManager, childID, "task", sub, jobstore.NewJobID(), schema, false)
	if err != nil {
		t.Fatalf("attachDelegateJobWithID: %v", err)
	}
	if delegateResultSchema(run.rec) == nil {
		t.Fatal("expected result schema on delegate rec")
	}

	if err := parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, sub, false); err != nil {
		t.Fatalf("finalizeDelegateOnce: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)

	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("rec = %+v, want completed", rec)
	}
}

// TestW3Dlg_FinalizeOncePersistResumabilityFails covers the persist-error arm:
// when the resumability assignment event cannot be durably appended,
// finalizeDelegateOnce surfaces the append error.
func TestW3Dlg_FinalizeOncePersistResumabilityFails(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:     child.ID(),
		sess:   child,
		status: SubagentCompleted,
		done:   make(chan struct{}),
	}
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "task", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		parent.jobManager.abandonRunningJob(run.rec.JobID)
	})

	failAppendN(parent.jobManager, jobstore.EventJobSessionAssigned, 1)

	err = parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, sub, false)
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("err = %v, want injected append failure", err)
	}
}

// TestW3Dlg_FinalizeOnceReflushOutputFails covers the defensive re-flush arm:
// when a prior partial finalize already wrote all output bytes but did not mark
// the append complete, the zero-length flush surfaces the output store error.
func TestW3Dlg_FinalizeOnceReflushOutputFails(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:     child.ID(),
		sess:   child,
		status: SubagentCompleted,
		result: "partial output payload",
		done:   make(chan struct{}),
	}
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "task", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		parent.jobManager.abandonRunningJob(run.rec.JobID)
	})

	output := delegateOutputBytes(sub.result)
	if len(output) == 0 {
		t.Fatal("expected non-empty delegate output")
	}
	parent.jobManager.mu.Lock()
	run.delegateOutputWritten = len(output)
	parent.jobManager.mu.Unlock()
	if err := run.output.Close(); err != nil {
		t.Fatalf("close output store: %v", err)
	}

	err = parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, sub, false)
	if err == nil {
		t.Fatalf("err = nil, want output re-flush failure")
	}
}
