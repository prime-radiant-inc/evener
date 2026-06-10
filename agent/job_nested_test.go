package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

func newNestedStopTestSession(t *testing.T, parentJobID string) (*Session, *jobManager) {
	t.Helper()
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = parentJobID

	parent := &Session{
		id:         "PARENT",
		jobManager: parentJM,
		subagents:  newSubagentManager(nil, nil),
	}
	parent.subagents.track(&subagent{
		id:     "CHILD",
		sess:   &Session{id: "CHILD", jobManager: childJM},
		status: SubagentRunning,
	})
	return parent, childJM
}

func TestNestedShellForwardsJobStarted(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"

	rec, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	t.Cleanup(func() {
		childJM.mu.Lock()
		run := childJM.running[rec.JobID]
		childJM.mu.Unlock()
		if run != nil && run.output != nil {
			_ = run.output.Close()
		}
	})

	if rec.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("record ParentJobID = %q, want job_PARENTDELEGATE", rec.ParentJobID)
	}
	childRecords, err := childJM.store.Load()
	if err != nil {
		t.Fatalf("load child store: %v", err)
	}
	childRec := childRecords[rec.JobID]
	if childRec == nil {
		t.Fatalf("child store keys = %v, want job %q", keysOf(childRecords), rec.JobID)
	}
	if childRec.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("child record ParentJobID = %q, want job_PARENTDELEGATE", childRec.ParentJobID)
	}
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec, ok := parentRecords[rec.JobID]
	if !ok {
		t.Fatalf("parent store keys = %v, want forwarded job %q", keysOf(parentRecords), rec.JobID)
	}
	if parentRec.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("parent record ParentJobID = %q, want job_PARENTDELEGATE", parentRec.ParentJobID)
	}
	if parentRec.OwnerSessionID != "CHILD" {
		t.Fatalf("parent record OwnerSessionID = %q, want CHILD", parentRec.OwnerSessionID)
	}
	if parentRec.VisibleToSession != "PARENT" {
		t.Fatalf("parent record VisibleToSession = %q, want PARENT", parentRec.VisibleToSession)
	}
	if parentRec.Status != jobstore.StatusRunning {
		t.Fatalf("parent record Status = %q, want %q", parentRec.Status, jobstore.StatusRunning)
	}
}

func TestJobListIncludeNestedFilter(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"

	parentRec, err := parentJM.createShell(createShellOpts{Command: "sleep 1", Description: "parent"})
	if err != nil {
		t.Fatalf("create parent shell: %v", err)
	}
	childRec, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create child shell: %v", err)
	}
	t.Cleanup(func() {
		finishRunningTestJob(t, parentJM, parentRec.JobID)
		finishRunningTestJob(t, childJM, childRec.JobID)
	})

	defaultJobs := parentJM.list(listFilter{})
	if containsJobID(defaultJobs, childRec.JobID) {
		t.Fatalf("default parent list includes nested job %q: %+v", childRec.JobID, defaultJobs)
	}
	if !containsJobID(defaultJobs, parentRec.JobID) {
		t.Fatalf("default parent list = %+v, want parent job %q", defaultJobs, parentRec.JobID)
	}

	nestedJobs := parentJM.list(listFilter{IncludeNested: true})
	nestedRec := findListedJob(nestedJobs, childRec.JobID)
	if nestedRec == nil {
		t.Fatalf("include_nested parent list = %+v, want nested job %q", nestedJobs, childRec.JobID)
	}
	if nestedRec.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("nested ParentJobID = %q, want job_PARENTDELEGATE", nestedRec.ParentJobID)
	}
	if !containsJobID(nestedJobs, parentRec.JobID) {
		t.Fatalf("include_nested parent list = %+v, want parent job %q", nestedJobs, parentRec.JobID)
	}
}

func TestFindJobRecordFindsForwardedNestedJob(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"

	childRec, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create child shell: %v", err)
	}
	t.Cleanup(func() {
		finishRunningTestJob(t, childJM, childRec.JobID)
	})

	nestedJobs := parentJM.list(listFilter{IncludeNested: true})
	if !containsJobID(nestedJobs, childRec.JobID) {
		t.Fatalf("include_nested parent list = %+v, want nested job %q", nestedJobs, childRec.JobID)
	}

	found, err := findJobRecord(parentJM, childRec.JobID)
	if err != nil {
		t.Fatalf("findJobRecord(%q): %v", childRec.JobID, err)
	}
	if found.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("found ParentJobID = %q, want job_PARENTDELEGATE", found.ParentJobID)
	}
}

func TestParentReadsNestedOutputViaOwnerRuntime(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithDefaultOutput("delegate complete")
			},
		},
	})
	parentDir := t.TempDir()
	parent, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(parentDir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() {
		parent.Close()
		releaseOnce.Do(func() { close(release) })
	})

	delegate := parent.createDelegate(context.Background(), delegateArgs{
		Task:       "run nested output owner",
		Background: true,
	})
	if delegate.Err != nil {
		t.Fatalf("createDelegate returned error: %v", delegate.Err)
	}
	_, childID, err := decodeRef(delegate.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := parent.subagents.get(childID)
	if sub == nil || sub.sess == nil || sub.sess.jobManager == nil {
		t.Fatalf("tracked subagent %q not found with live jobManager", childID)
	}
	childJM := sub.sess.jobManager

	nested, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested output"})
	if err != nil {
		t.Fatalf("create nested shell: %v", err)
	}
	t.Cleanup(func() {
		finishRunningTestJob(t, childJM, nested.JobID)
		_, _ = parent.jobManager.stop(delegate.JobID)
		releaseOnce.Do(func() { close(release) })
		waitForShellDone(t, parent.jobManager, delegate.JobID)
	})

	childJM.mu.Lock()
	run := childJM.running[nested.JobID]
	childJM.mu.Unlock()
	if run == nil || run.output == nil {
		t.Fatalf("nested job %q has no live output store", nested.JobID)
	}
	if _, err := childJM.appendJobOutput(nested.JobID, run.output, []byte("nested owner line\n")); err != nil {
		t.Fatalf("append nested output: %v", err)
	}

	res := parent.reg.ExecuteCall(context.Background(), parent.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_bytes":65536,"grep":"owner","max_chars":20000}`, nested.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_read_output returned error: %s", res.Output)
	}
	var out jobReadOutputTestResult
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
	}
	if out.JobID != nested.JobID || out.Status != string(jobstore.StatusRunning) || !strings.Contains(out.Content, "nested owner line") {
		t.Fatalf("job_read_output = %+v, want running nested output from owner", out)
	}
	if len(out.Matches) != 1 || !strings.Contains(out.Matches[0].Line, "nested owner line") {
		t.Fatalf("job_read_output matches = %+v, want owner grep match", out.Matches)
	}
}

func TestClosedNestedOwnerFallsBackToForwardedRecord(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		childJM.mu.Lock()
		run := childJM.running
		childJM.mu.Unlock()
		for _, running := range run {
			if running.output != nil {
				_ = running.output.Close()
			}
		}
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"
	nested, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create nested shell: %v", err)
	}

	parent := &Session{
		id:         "PARENT",
		jobManager: parentJM,
		subagents:  newSubagentManager(nil, nil),
	}
	parent.subagents.track(&subagent{
		id:     "CHILD",
		sess:   &Session{id: "CHILD", jobManager: childJM},
		status: SubagentCompleted,
		closed: true,
	})
	if err := childJM.store.Close(); err != nil {
		t.Fatalf("close child store: %v", err)
	}

	owner, forwarded := parent.ownerJobManagerFor(nested.JobID)
	if owner != nil {
		t.Fatalf("ownerJobManagerFor returned closed child manager, want owner-gone fallback")
	}
	if forwarded == nil || forwarded.JobID != nested.JobID {
		t.Fatalf("ownerJobManagerFor forwarded record = %+v, want nested job %q", forwarded, nested.JobID)
	}
	selected, rec, err := parent.nestedOrLocalJobManager(nested.JobID)
	if err != nil {
		t.Fatalf("nestedOrLocalJobManager returned error: %v", err)
	}
	if selected != parentJM {
		t.Fatalf("nestedOrLocalJobManager selected closed child manager, want parent manager")
	}
	if rec == nil || rec.JobID != nested.JobID || rec.OwnerSessionID != "CHILD" {
		t.Fatalf("nestedOrLocalJobManager record = %+v, want parent forwarded record", rec)
	}
}

func TestJobStopClosedNestedOwnerErrors(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		childJM.mu.Lock()
		run := childJM.running
		childJM.mu.Unlock()
		for _, running := range run {
			if running.output != nil {
				_ = running.output.Close()
			}
		}
		_ = childJM.store.Close()
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"
	nested, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create nested shell: %v", err)
	}
	parent := &Session{
		id:         "PARENT",
		jobManager: parentJM,
		subagents:  newSubagentManager(nil, nil),
	}
	parent.subagents.track(&subagent{
		id:     "CHILD",
		sess:   &Session{id: "CHILD", jobManager: childJM},
		status: SubagentCompleted,
		closed: true,
	})

	out, err := jobStopTool(context.Background(), parent, map[string]any{
		"job_id": nested.JobID,
	}, 20000)
	if err == nil {
		t.Fatalf("job_stop closed nested owner succeeded with output %s, want not-controllable error", out)
	}
	if !strings.Contains(err.Error(), "not controllable") {
		t.Fatalf("job_stop error = %q, want not controllable", err.Error())
	}
}

func TestClosedStoreNestedOwnerFallsBackToForwardedRecord(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		childJM.mu.Lock()
		run := childJM.running
		childJM.mu.Unlock()
		for _, running := range run {
			if running.output != nil {
				_ = running.output.Close()
			}
		}
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"
	nested, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create nested shell: %v", err)
	}

	parent := &Session{
		id:         "PARENT",
		jobManager: parentJM,
		subagents:  newSubagentManager(nil, nil),
	}
	parent.subagents.track(&subagent{
		id:            "CHILD",
		sess:          &Session{id: "CHILD", jobManager: childJM},
		status:        SubagentCompleted,
		closeTimedOut: true,
	})
	if err := childJM.store.Close(); err != nil {
		t.Fatalf("close child store: %v", err)
	}

	owner, forwarded := parent.ownerJobManagerFor(nested.JobID)
	if owner != nil {
		t.Fatalf("ownerJobManagerFor returned closed-store child manager, want owner-gone fallback")
	}
	if forwarded == nil || forwarded.JobID != nested.JobID {
		t.Fatalf("ownerJobManagerFor forwarded record = %+v, want nested job %q", forwarded, nested.JobID)
	}
	selected, rec, err := parent.nestedOrLocalJobManager(nested.JobID)
	if err != nil {
		t.Fatalf("nestedOrLocalJobManager returned error: %v", err)
	}
	if selected != parentJM {
		t.Fatalf("nestedOrLocalJobManager selected closed-store child manager, want parent manager")
	}
	if rec == nil || rec.JobID != nested.JobID || rec.OwnerSessionID != "CHILD" {
		t.Fatalf("nestedOrLocalJobManager record = %+v, want parent forwarded record", rec)
	}
}

func TestNestedReadOutputFallsBackWhenOwnerStoreClosesAfterSelection(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"
	nested, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create nested shell: %v", err)
	}
	childJM.mu.Lock()
	run := childJM.running[nested.JobID]
	childJM.mu.Unlock()
	if run == nil || run.output == nil {
		t.Fatalf("nested job %q has no live output store", nested.JobID)
	}
	if _, err := childJM.appendJobOutput(nested.JobID, run.output, []byte("durable nested output\n")); err != nil {
		t.Fatalf("append nested output: %v", err)
	}
	code := 0
	if err := childJM.finalize(nested.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize nested job: %v", err)
	}

	parent := &Session{
		id:         "PARENT",
		jobManager: parentJM,
		subagents:  newSubagentManager(nil, nil),
	}
	parent.subagents.track(&subagent{
		id:     "CHILD",
		sess:   &Session{id: "CHILD", jobManager: childJM},
		status: SubagentCompleted,
	})

	owner, forwarded := parent.ownerJobManagerFor(nested.JobID)
	if owner != childJM {
		t.Fatalf("ownerJobManagerFor owner = %p, want child manager %p", owner, childJM)
	}
	if forwarded == nil || forwarded.JobID != nested.JobID {
		t.Fatalf("ownerJobManagerFor forwarded record = %+v, want nested job %q", forwarded, nested.JobID)
	}
	if err := childJM.store.Close(); err != nil {
		t.Fatalf("close child store: %v", err)
	}

	snap, err := parent.readJobOutputSnapshot(owner, nested.JobID, 65536, nil, 0)
	if err != nil {
		t.Fatalf("readJobOutputSnapshot returned error: %v", err)
	}
	if snap.Manager != parentJM {
		t.Fatalf("readJobOutputSnapshot manager = %p, want parent fallback %p", snap.Manager, parentJM)
	}
	if snap.Record == nil || snap.Record.JobID != nested.JobID || snap.Record.Status != jobstore.StatusCompleted {
		t.Fatalf("snapshot record = %+v, want completed parent forwarded record", snap.Record)
	}
	if !strings.Contains(snap.Content, "durable nested output") {
		t.Fatalf("snapshot content = %q, want parent durable output", snap.Content)
	}
}

func TestNestedReadOutputBlockRefreshesOwnerRecord(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"
	nested, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create nested shell: %v", err)
	}
	parent := &Session{
		id:         "PARENT",
		jobManager: parentJM,
		subagents:  newSubagentManager(nil, nil),
	}
	parent.subagents.track(&subagent{
		id:     "CHILD",
		sess:   &Session{id: "CHILD", jobManager: childJM},
		status: SubagentRunning,
	})

	type readResult struct {
		out string
		err error
	}
	readDone := make(chan readResult, 1)
	go func() {
		out, err := jobReadOutputTool(context.Background(), parent, map[string]any{
			"job_id":           nested.JobID,
			"block":            true,
			"block_timeout_ms": 1000,
			"tail_bytes":       65536,
			"max_chars":        20000,
		}, 20000)
		readDone <- readResult{out: out, err: err}
	}()

	time.Sleep(50 * time.Millisecond)
	childJM.mu.Lock()
	run := childJM.running[nested.JobID]
	if run != nil {
		run.structured = map[string]any{"summary": "finished during block"}
	}
	childJM.mu.Unlock()
	if run == nil || run.output == nil {
		t.Fatalf("nested job %q has no live output store", nested.JobID)
	}
	if _, err := childJM.appendJobOutput(nested.JobID, run.output, []byte("finished during block\n")); err != nil {
		t.Fatalf("append nested output: %v", err)
	}
	code := 0
	if err := childJM.finalize(nested.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize nested job: %v", err)
	}

	var got readResult
	select {
	case got = <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("job_read_output did not return after nested job finalized")
	}
	if got.err != nil {
		t.Fatalf("job_read_output returned error: %v", got.err)
	}
	var out jobReadOutputTestResult
	if err := json.Unmarshal([]byte(got.out), &out); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, got.out)
	}
	if !strings.Contains(out.Content, "finished during block") {
		t.Fatalf("content = %q, want finalized nested output", out.Content)
	}
	if out.Status != string(jobstore.StatusCompleted) || out.Reason == nil || *out.Reason != "exit_zero" || out.ExitCode == nil || *out.ExitCode != 0 {
		t.Fatalf("job_read_output = %+v, want refreshed completed projection", out)
	}
	if !out.StructuredResultValid || out.StructuredResult["summary"] != "finished during block" {
		t.Fatalf("structured result = %+v valid=%v, want refreshed owner result", out.StructuredResult, out.StructuredResultValid)
	}
}

func TestParentStopsNestedJobViaOwner(t *testing.T) {
	parent, childJM := newNestedStopTestSession(t, "job_PARENTDELEGATE")
	se := newDelayedExitStreamingExecutor()
	var releaseOnce sync.Once
	var jobID string
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(se.release) })
		if jobID != "" {
			waitForShellDone(t, childJM, jobID)
		}
	})

	nested := runShell(context.Background(), childJM, se, shellArgs{
		Command:    "sleep 30",
		Background: true,
	})
	if nested.JobID == "" {
		t.Fatalf("runShell result = %+v, want background nested job", nested)
	}
	jobID = nested.JobID

	rec, err := parent.stopNestedOrLocal(nested.JobID)
	if err != nil {
		t.Fatalf("parent stop of nested job: %v", err)
	}
	if rec.JobID != nested.JobID {
		t.Fatalf("stopped job = %q, want %q", rec.JobID, nested.JobID)
	}
	if rec.Status != jobstore.StatusCancelled {
		t.Fatalf("nested job status after stop = %q, want %q", rec.Status, jobstore.StatusCancelled)
	}
	if rec.Reason != "stopped_by_parent" {
		t.Fatalf("nested job reason after stop = %q, want stopped_by_parent", rec.Reason)
	}
	if se.signals.Load() != 1 {
		t.Fatalf("nested shell signals = %d, want 1", se.signals.Load())
	}
	releaseOnce.Do(func() { close(se.release) })
	waitForShellDone(t, childJM, nested.JobID)
	parentRec, err := findJobRecord(parent.jobManager, nested.JobID)
	if err != nil {
		t.Fatalf("find parent forwarded nested record: %v", err)
	}
	if parentRec.Status != jobstore.StatusCancelled || parentRec.Reason != "stopped_by_parent" {
		t.Fatalf("parent forwarded nested record = %+v, want cancelled/stopped_by_parent", parentRec)
	}
}

func TestStopDelegateIncludeChildrenStopsNested(t *testing.T) {
	parent, childJM := newNestedStopTestSession(t, "")
	delegate, err := parent.jobManager.createShell(createShellOpts{Command: "delegate", Description: "delegate"})
	if err != nil {
		t.Fatalf("create delegate stand-in: %v", err)
	}
	childJM.parentJobID = delegate.JobID
	se := newDelayedExitStreamingExecutor()
	var releaseOnce sync.Once
	var jobID string
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(se.release) })
		if jobID != "" {
			waitForShellDone(t, childJM, jobID)
		}
		finishRunningTestJob(t, parent.jobManager, delegate.JobID)
	})

	nested := runShell(context.Background(), childJM, se, shellArgs{
		Command:    "sleep 30",
		Background: true,
	})
	if nested.JobID == "" {
		t.Fatalf("runShell result = %+v, want background nested job", nested)
	}
	jobID = nested.JobID

	out, err := jobStopTool(context.Background(), parent, map[string]any{
		"job_id":           delegate.JobID,
		"include_children": true,
	}, 20000)
	if err != nil {
		t.Fatalf("job_stop include_children: %v", err)
	}
	var stop jobStopResult
	if err := json.Unmarshal([]byte(out), &stop); err != nil {
		t.Fatalf("unmarshal job_stop: %v (output: %s)", err, out)
	}
	if stop.JobID != delegate.JobID {
		t.Fatalf("job_stop job_id = %q, want primary delegate %q", stop.JobID, delegate.JobID)
	}
	if se.signals.Load() != 1 {
		t.Fatalf("nested shell signals = %d, want include_children to signal it once", se.signals.Load())
	}
	releaseOnce.Do(func() { close(se.release) })
	waitForShellDone(t, childJM, nested.JobID)
	childRec, err := findJobRecord(childJM, nested.JobID)
	if err != nil {
		t.Fatalf("find nested record: %v", err)
	}
	if childRec.Status != jobstore.StatusCancelled || childRec.Reason != "stopped_by_parent" {
		t.Fatalf("nested child record = %+v, want cancelled/stopped_by_parent", childRec)
	}
}

func TestStopDelegateWithoutIncludeChildrenLeavesNestedRunning(t *testing.T) {
	parent, childJM := newNestedStopTestSession(t, "")
	delegate, err := parent.jobManager.createShell(createShellOpts{Command: "delegate", Description: "delegate"})
	if err != nil {
		t.Fatalf("create delegate stand-in: %v", err)
	}
	childJM.parentJobID = delegate.JobID
	se := newDelayedExitStreamingExecutor()
	var releaseOnce sync.Once
	var jobID string
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(se.release) })
		if jobID != "" {
			waitForShellDone(t, childJM, jobID)
		}
		finishRunningTestJob(t, parent.jobManager, delegate.JobID)
	})

	nested := runShell(context.Background(), childJM, se, shellArgs{
		Command:    "sleep 30",
		Background: true,
	})
	if nested.JobID == "" {
		t.Fatalf("runShell result = %+v, want background nested job", nested)
	}
	jobID = nested.JobID

	if _, err := jobStopTool(context.Background(), parent, map[string]any{
		"job_id": delegate.JobID,
	}, 20000); err != nil {
		t.Fatalf("job_stop without include_children: %v", err)
	}
	if se.signals.Load() != 0 {
		t.Fatalf("nested shell signals = %d, want include_children=false to leave it running", se.signals.Load())
	}
	childRec, err := findJobRecord(childJM, nested.JobID)
	if err != nil {
		t.Fatalf("find nested record: %v", err)
	}
	if childRec.Status != jobstore.StatusRunning {
		t.Fatalf("nested child record status = %q, want running", childRec.Status)
	}
}

func TestStopDelegateIncludeChildrenSurfacesChildStopError(t *testing.T) {
	parent, _ := newNestedStopTestSession(t, "")
	se := newDelayedExitStreamingExecutor()
	var releaseOnce sync.Once
	delegate := runShell(context.Background(), parent.jobManager, se, shellArgs{
		Command:    "delegate",
		Background: true,
	})
	if delegate.JobID == "" {
		t.Fatalf("runShell result = %+v, want background delegate job", delegate)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(se.release) })
		waitForShellDone(t, parent.jobManager, delegate.JobID)
	})

	startedAt := parent.jobManager.now()
	if err := parent.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            "job_missing_child",
		Type:             jobstore.JobShell,
		Command:          "sleep 30",
		Description:      "stale nested child",
		OwnerSessionID:   "CHILD",
		VisibleToSession: "PARENT",
		ParentJobID:      delegate.JobID,
		StartedAt:        &startedAt,
	}); err != nil {
		t.Fatalf("append stale forwarded child: %v", err)
	}

	_, err := jobStopTool(context.Background(), parent, map[string]any{
		"job_id":           delegate.JobID,
		"include_children": true,
	}, 20000)
	if err == nil {
		t.Fatal("job_stop include_children succeeded, want child stop error")
	}
	if !strings.Contains(err.Error(), `job "job_missing_child" not found`) {
		t.Fatalf("job_stop error = %q, want missing child job", err.Error())
	}
	if se.signals.Load() != 1 {
		t.Fatalf("delegate shell signals = %d, want primary delegate stopped despite child error", se.signals.Load())
	}
	releaseOnce.Do(func() { close(se.release) })
	waitForShellDone(t, parent.jobManager, delegate.JobID)
	delegateRec, err := findJobRecord(parent.jobManager, delegate.JobID)
	if err != nil {
		t.Fatalf("find delegate record: %v", err)
	}
	if delegateRec.Status != jobstore.StatusCancelled || delegateRec.Reason != "stopped_by_parent" {
		t.Fatalf("delegate record = %+v, want cancelled/stopped_by_parent", delegateRec)
	}
}

func TestNestedRunShellForwardsDelayedJobStarted(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"
	se := newDelayedExitStreamingExecutor()
	var jobID string
	t.Cleanup(func() {
		close(se.release)
		if jobID != "" {
			waitForShellDone(t, childJM, jobID)
		}
	})

	res := runShell(context.Background(), childJM, se, shellArgs{
		Command:    "sleep 1",
		Background: true,
	})
	if res.JobID == "" || !res.RunningInBackground {
		t.Fatalf("runShell result = %+v, want background job", res)
	}
	jobID = res.JobID

	childRecords, err := childJM.store.Load()
	if err != nil {
		t.Fatalf("load child store: %v", err)
	}
	childRec := childRecords[res.JobID]
	if childRec == nil {
		t.Fatalf("child store keys = %v, want job %q", keysOf(childRecords), res.JobID)
	}
	if childRec.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("child record ParentJobID = %q, want job_PARENTDELEGATE", childRec.ParentJobID)
	}

	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec := parentRecords[res.JobID]
	if parentRec == nil {
		t.Fatalf("parent store keys = %v, want forwarded job %q", keysOf(parentRecords), res.JobID)
	}
	if parentRec.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("parent record ParentJobID = %q, want job_PARENTDELEGATE", parentRec.ParentJobID)
	}
	if parentRec.OwnerSessionID != "CHILD" {
		t.Fatalf("parent record OwnerSessionID = %q, want CHILD", parentRec.OwnerSessionID)
	}
	if parentRec.VisibleToSession != "PARENT" {
		t.Fatalf("parent record VisibleToSession = %q, want PARENT", parentRec.VisibleToSession)
	}
	if parentRec.Status != jobstore.StatusRunning {
		t.Fatalf("parent record Status = %q, want %q", parentRec.Status, jobstore.StatusRunning)
	}
}

func TestNestedTerminalForwardsGenerationVerbatim(t *testing.T) {
	var parentNotifications []jobNotification
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(n jobNotification) {
		parentNotifications = append(parentNotifications, n)
	})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})

	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"

	rec, err := childJM.createShell(createShellOpts{Command: "true", Description: "nested"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	code := 0
	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	childRecords, err := childJM.store.Load()
	if err != nil {
		t.Fatalf("load child store: %v", err)
	}
	childRec := childRecords[rec.JobID]
	if childRec == nil {
		t.Fatalf("child store keys = %v, want job %q", keysOf(childRecords), rec.JobID)
	}
	if childRec.Status != jobstore.StatusCompleted {
		t.Fatalf("child record Status = %q, want %q", childRec.Status, jobstore.StatusCompleted)
	}
	if childRec.TerminalGen == "" {
		t.Fatal("child record TerminalGen is empty")
	}

	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec := parentRecords[rec.JobID]
	if parentRec == nil {
		t.Fatalf("parent store keys = %v, want forwarded job %q", keysOf(parentRecords), rec.JobID)
	}
	if parentRec.Status != jobstore.StatusCompleted {
		t.Fatalf("parent record Status = %q, want %q", parentRec.Status, jobstore.StatusCompleted)
	}
	if parentRec.TerminalGen != childRec.TerminalGen {
		t.Fatalf("parent TerminalGen = %q, want child TerminalGen %q", parentRec.TerminalGen, childRec.TerminalGen)
	}
	if parentRec.VisibleToSession != "PARENT" {
		t.Fatalf("parent record VisibleToSession = %q, want PARENT", parentRec.VisibleToSession)
	}
	if parentRec.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("parent record ParentJobID = %q, want job_PARENTDELEGATE", parentRec.ParentJobID)
	}
	if parentRec.OwnerSessionID != "CHILD" {
		t.Fatalf("parent record OwnerSessionID = %q, want CHILD", parentRec.OwnerSessionID)
	}
	if parentRec.DedupeKey() != (jobstore.DedupeKey{
		VisibleSessionID: "PARENT",
		JobID:            rec.JobID,
		TerminalGen:      childRec.TerminalGen,
	}) {
		t.Fatalf("parent dedupe key = %+v, want parent visible session and child terminal generation", parentRec.DedupeKey())
	}
	if parentRec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("parent NotifyState = %q, want %q", parentRec.NotifyState, jobstore.NotifyPending)
	}
	if len(parentNotifications) != 1 {
		t.Fatalf("parent notifications = %+v, want exactly one", parentNotifications)
	}
	if parentNotifications[0].JobID != rec.JobID || parentNotifications[0].Status != string(jobstore.StatusCompleted) {
		t.Fatalf("parent notification = %+v, want terminal notification for %s", parentNotifications[0], rec.JobID)
	}
}

func TestChildJobManagerHasForwardSeam(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithDefaultOutput("nested delegate complete")
			},
		},
	})
	parent := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	res := parent.createDelegate(context.Background(), delegateArgs{
		Task:       "run nested shell work",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" {
		t.Fatal("delegate job_id is empty")
	}

	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := parent.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("tracked subagent %q not found", childID)
	}

	childJM := sub.sess.jobManager
	if childJM == nil {
		t.Fatal("child jobManager is nil")
	}
	assertJobForwardHookType(childJM.forward)
	if childJM.forward == nil {
		t.Fatal("child jobManager forward hook is nil")
	}
	if childJM.parentJobID != res.JobID {
		t.Fatalf("child parentJobID = %q, want delegate job_id %q", childJM.parentJobID, res.JobID)
	}
	if parent.jobManager.forward != nil {
		t.Fatal("root jobManager forward hook is non-nil")
	}
	if parent.jobManager.parentJobID != "" {
		t.Fatalf("root parentJobID = %q, want empty", parent.jobManager.parentJobID)
	}

	c2 := llm.NewClient()
	c2.Register(&fakeAdapter{name: "openai"})
	directParent := newDelegateTestSession(t, c2)
	spawned, err := directParent.spawnAgent(context.Background(), "run direct subagent work", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	directChildID, err := parseSpawnedAgentID(spawned)
	if err != nil {
		t.Fatalf("parse direct child id: %v", err)
	}
	directSub := directParent.subagents.get(directChildID)
	if directSub == nil || directSub.sess == nil {
		t.Fatalf("tracked direct subagent %q not found", directChildID)
	}
	directJM := directSub.sess.jobManager
	if directJM == nil {
		t.Fatal("direct child jobManager is nil")
	}
	if directJM.forward != nil {
		t.Fatal("direct child jobManager forward hook is non-nil")
	}
	if directJM.parentJobID != "" {
		t.Fatalf("direct child parentJobID = %q, want empty", directJM.parentJobID)
	}

	_, _ = parent.jobManager.stop(res.JobID)
	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, parent.jobManager, res.JobID)
}

func TestDelegateChildDirectSpawnDoesNotInheritForwardSeam(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithDefaultOutput("delegate complete")
			},
		},
	})
	parent := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	res := parent.createDelegate(context.Background(), delegateArgs{
		Task:       "run delegate child",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := parent.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("tracked subagent %q not found", childID)
	}
	if sub.sess.jobManager == nil {
		t.Fatal("delegate child jobManager is nil")
	}
	if sub.sess.jobManager.forward == nil || sub.sess.jobManager.parentJobID != res.JobID {
		t.Fatalf("delegate child seam = forward:%v parentJobID:%q, want delegate seam for %q",
			sub.sess.jobManager.forward != nil,
			sub.sess.jobManager.parentJobID,
			res.JobID)
	}

	// Current production behavior rejects subagent management below the root.
	// This fixture opens only that guard so the test can exercise config-copy
	// inheritance through a delegate child.
	sub.sess.mu.Lock()
	sub.sess.depth = 0
	sub.sess.cfg.spawn.depth = 0
	sub.sess.cfg.MaxSubagentDepth = 1
	sub.sess.mu.Unlock()

	spawned, err := sub.sess.spawnAgent(context.Background(), "run direct grandchild work", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent from delegate child: %v", err)
	}
	grandchildID, err := parseSpawnedAgentID(spawned)
	if err != nil {
		t.Fatalf("parse grandchild id: %v", err)
	}
	grandchild := sub.sess.subagents.get(grandchildID)
	if grandchild == nil || grandchild.sess == nil {
		t.Fatalf("tracked grandchild %q not found", grandchildID)
	}
	grandchildJM := grandchild.sess.jobManager
	if grandchildJM == nil {
		t.Fatal("grandchild jobManager is nil")
	}
	if grandchildJM.forward != nil {
		t.Fatal("grandchild jobManager inherited delegate forward hook")
	}
	if grandchildJM.parentJobID != "" {
		t.Fatalf("grandchild parentJobID = %q, want empty", grandchildJM.parentJobID)
	}

	_, _ = parent.jobManager.stop(res.JobID)
	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, parent.jobManager, res.JobID)
}

func assertJobForwardHookType(_ func(jobstore.Event)) {}

func finishRunningTestJob(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	code := 0
	if err := jm.finalize(jobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize job %q: %v", jobID, err)
	}
}

func containsJobID(records []*jobstore.JobRecord, jobID string) bool {
	return findListedJob(records, jobID) != nil
}

func findListedJob(records []*jobstore.JobRecord, jobID string) *jobstore.JobRecord {
	for _, rec := range records {
		if rec.JobID == jobID {
			return rec
		}
	}
	return nil
}

func keysOf(records map[string]*jobstore.JobRecord) []string {
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
