//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"os"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
	"primeradiant.com/serf/llm"
)

// FuzzJobDelegateAttachFinalizeSeed100Edges drives rare delegate attach and
// terminal-result branches with deterministic in-process faults.
func FuzzJobDelegateAttachFinalizeSeed100Edges(f *testing.F) {
	for i := byte(0); i < 4; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, op byte) {
		switch op % 4 {
		case 0:
			jdaf100AttachForwardDoubleFault(t)
		case 1:
			jdaf100FinalizeStoreClosed(t)
		case 2:
			jdaf100TerminalStructuredFallback(t)
		case 3:
			jdaf100CreateAttachRollback(t)
		}
	})
}

func jdaf100CreateAttachRollback(t *testing.T) {
	t.Helper()
	client := delegateTestClient(func(llm.Request) llm.Response {
		return communicateWithDefaultOutput("unused")
	})
	rig := newWtDlgRepo(t, client)
	jm := rig.s.jobManager
	openErr := errors.New("seed output open failure")
	jm.openOutput = func(string, int64) (*jobstore.OutputStore, error) {
		return nil, openErr
	}

	result := rig.s.createDelegate(context.Background(), delegateArgs{
		Task:       "rollback isolated attach",
		Isolation:  "worktree",
		Background: true,
	})
	if !errors.Is(result.Err, openErr) {
		t.Fatalf("create error = %v, want output open failure", result.Err)
	}
	if result.DelegateID == "" {
		t.Fatal("failed create did not report delegate id")
	}
	if _, err := os.Stat(rig.lanePath(result.DelegateID)); !os.IsNotExist(err) {
		t.Fatalf("lane stat after rollback = %v, want not exist", err)
	}
	if _, err := worktree.ReadSidecar(rig.metaDir(), result.DelegateID); err == nil {
		t.Fatal("delegate sidecar survived attach rollback")
	}
}

func jdaf100AttachForwardDoubleFault(t *testing.T) {
	t.Helper()
	parent := newTestSession(t)
	child := newTestSession(t)
	jm := parent.jobManager
	jm.setParentJobID("job_parent")
	forwardErr := errors.New("seed forward failure")
	jm.forward = func(jobstore.Event) error { return forwardErr }
	origAppend := jm.appendEvent
	terminalErr := errors.New("seed terminal append failure")
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished {
			return terminalErr
		}
		return origAppend(e)
	}

	sub := w3dlg_attachSub(child)
	run, err := parent.attachDelegateJob(jm, child.ID(), "double fault", sub)
	if run != nil {
		t.Fatalf("run = %+v, want nil", run)
	}
	if !errors.Is(err, errDelegateStartForwardTerminalFailed) ||
		!errors.Is(err, forwardErr) || !errors.Is(err, terminalErr) {
		t.Fatalf("attach error = %v, want joined forward and terminal failures", err)
	}
	jm.mu.Lock()
	running := len(jm.running)
	jm.mu.Unlock()
	if running != 0 {
		t.Fatalf("running jobs = %d, want 0", running)
	}
}

func jdaf100FinalizeStoreClosed(t *testing.T) {
	t.Helper()
	parent := newTestSession(t)
	child := newTestSession(t)
	jm := parent.jobManager
	clk := agenttest.NewFakeClock()
	jm.clock = clk
	jm.now = clk.Now
	sub := w3dlg_attachSub(child)
	run, err := parent.attachDelegateJob(jm, child.ID(), "closed finalize", sub)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	parent.subagents.track(sub)
	jm.appendEvent = func(jobstore.Event) error { return jobstore.ErrStoreClosed }

	err = parent.finalizeDelegateWithNotification(run.rec.JobID, child.ID(), nil, true)
	if !errors.Is(err, jobstore.ErrStoreClosed) {
		t.Fatalf("finalize error = %v, want store closed", err)
	}
	jm.mu.Lock()
	_, running := jm.running[run.rec.JobID]
	jm.mu.Unlock()
	if running {
		t.Fatal("terminal finalize failure left job running")
	}
}

func jdaf100TerminalStructuredFallback(t *testing.T) {
	t.Helper()
	parent := newTestSession(t)
	child := newTestSession(t)
	jm := parent.jobManager
	run, err := parent.attachDelegateJob(jm, child.ID(), "structured fallback", w3dlg_attachSub(child))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	structured := map[string]any{"answer": "seed100"}
	jm.mu.Lock()
	run.structured = structured
	jm.mu.Unlock()
	endedAt := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       run.rec.JobID,
		Status:      jobstore.StatusCompleted,
		EndedAt:     &endedAt,
		TerminalGen: jobstore.NewTerminalGeneration(),
	}); err != nil {
		t.Fatalf("append terminal: %v", err)
	}

	result := delegateTerminalResult(parent, jm, run)
	if !result.StructuredResultValid {
		t.Fatalf("structured valid = false, result = %+v", result)
	}
	got, ok := result.StructuredResult.(map[string]any)
	if !ok || got["answer"] != "seed100" {
		t.Fatalf("structured result = %#v", result.StructuredResult)
	}
	jm.abandonRunningJob(run.rec.JobID)
}
