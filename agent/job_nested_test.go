package agent

import (
	"context"
	"encoding/json"
	"errors"
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

	childDefaultJobs := childJM.list(listFilter{})
	if !containsJobID(childDefaultJobs, childRec.JobID) {
		t.Fatalf("default child list = %+v, want owned nested job %q", childDefaultJobs, childRec.JobID)
	}

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
	if !strings.Contains(err.Error(), "not_controllable:") {
		t.Fatalf("job_stop error = %q, want not_controllable", err.Error())
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

func TestStopOwnerGoneNestedTerminalRecordReturnsStatus(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
	})
	parent := &Session{id: "PARENT", jobManager: parentJM}
	startedAt := time.Unix(1, 0).UTC()
	endedAt := time.Unix(2, 0).UTC()
	if err := parentJM.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            "job_terminal_nested",
		Type:             jobstore.JobShell,
		Command:          "true",
		Description:      "owner gone terminal nested",
		OwnerSessionID:   "CHILDGONE",
		VisibleToSession: "PARENT",
		ParentJobID:      "job_DELEGATE",
		StartedAt:        &startedAt,
	}); err != nil {
		t.Fatalf("append started: %v", err)
	}
	if err := parentJM.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       "job_terminal_nested",
		Status:      jobstore.StatusCompleted,
		Reason:      "exit_zero",
		EndedAt:     &endedAt,
		TerminalGen: "terminal-generation",
	}); err != nil {
		t.Fatalf("append finished: %v", err)
	}

	out, err := jobStopTool(context.Background(), parent, map[string]any{
		"job_id": "job_terminal_nested",
	}, 20000)
	if err != nil {
		t.Fatalf("job_stop terminal owner-gone nested job: %v", err)
	}
	var stop jobStopResult
	if err := json.Unmarshal([]byte(out), &stop); err != nil {
		t.Fatalf("unmarshal job_stop: %v (output: %s)", err, out)
	}
	if stop.JobID != "job_terminal_nested" || stop.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("job_stop result = %+v, want completed terminal nested record", stop)
	}
}

func TestStopOwnerGoneNestedRunningRecordIsNotControllable(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
	})
	parent := &Session{id: "PARENT", jobManager: parentJM}
	startedAt := time.Unix(1, 0).UTC()
	if err := parentJM.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            "job_running_nested",
		Type:             jobstore.JobShell,
		Command:          "sleep 30",
		Description:      "owner gone running nested",
		OwnerSessionID:   "CHILDGONE",
		VisibleToSession: "PARENT",
		ParentJobID:      "job_DELEGATE",
		StartedAt:        &startedAt,
	}); err != nil {
		t.Fatalf("append started: %v", err)
	}

	_, err = jobStopTool(context.Background(), parent, map[string]any{
		"job_id": "job_running_nested",
	}, 20000)
	if err == nil {
		t.Fatal("job_stop owner-gone running nested job succeeded, want not_controllable error")
	}
	if !strings.Contains(err.Error(), "not_controllable:") || strings.Contains(err.Error(), "not controllable") {
		t.Fatalf("job_stop error = %q, want canonical not_controllable token", err.Error())
	}
}

func TestForwardedNestedReconcilesRuntimeLostOnRestart(t *testing.T) {
	parentDir := t.TempDir()

	seed, err := newJobManager(parentDir, "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Unix(1, 0).UTC()
	if err := seed.store.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            "job_NESTED",
		Type:             jobstore.JobShell,
		Command:          "sleep 999",
		Description:      "forwarded nested job",
		ParentJobID:      "job_DELEGATE",
		OwnerSessionID:   "CHILDGONE",
		VisibleToSession: "PARENT",
		StartedAt:        &startedAt,
	}); err != nil {
		t.Fatalf("append forwarded job_started: %v", err)
	}
	if err := seed.store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	var queued []jobNotification
	jm, err := newJobManager(parentDir, "PARENT", func(n jobNotification) { queued = append(queued, n) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = jm.store.Close()
	})
	jm.now = func() time.Time { return time.Unix(100, 0).UTC() }

	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile lost jobs: %v", err)
	}

	recs, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	rec := recs["job_NESTED"]
	if rec == nil {
		t.Fatalf("records = %v, want job_NESTED", keysOf(recs))
	}
	if rec.JobID != "job_NESTED" || rec.Status != jobstore.StatusStopped || rec.Reason != "runtime_lost" {
		t.Fatalf("forwarded nested record = %+v, want job_NESTED stopped/runtime_lost", rec)
	}
	if rec.Type != jobstore.JobShell || rec.Command != "sleep 999" || rec.Description != "forwarded nested job" {
		t.Fatalf("forwarded nested metadata = %+v, want original shell command metadata", rec)
	}
	if rec.ParentJobID != "job_DELEGATE" || rec.OwnerSessionID != "CHILDGONE" || rec.VisibleToSession != "PARENT" {
		t.Fatalf("forwarded nested ownership metadata = %+v, want parent/owner/visible preserved", rec)
	}
	if rec.TerminalGen == "" {
		t.Fatalf("forwarded nested terminal generation is empty: %+v", rec)
	}
	if len(queued) != 1 || queued[0].JobID != "job_NESTED" || queued[0].Status != string(jobstore.StatusStopped) || queued[0].Reason != "runtime_lost" {
		t.Fatalf("expected one runtime_lost notification for the nested job, got %+v", queued)
	}

	terminalGen := rec.TerminalGen
	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatalf("second reconcile lost jobs: %v", err)
	}
	recs, err = jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	rec = recs["job_NESTED"]
	if rec.TerminalGen != terminalGen {
		t.Fatalf("reconcile re-minted terminal_generation: %q -> %q", terminalGen, rec.TerminalGen)
	}
	if len(queued) != 1 {
		t.Fatalf("second reconcile queued another notification: %+v", queued)
	}
}

func TestNestedReconcileLostJobsForwardsRecoveredTerminalEvents(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	childDir := t.TempDir()
	childSeed, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
	})
	startedAt := time.Unix(1, 0).UTC()
	childStarted := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            "job_CHILD_LOST",
		Type:             jobstore.JobShell,
		Command:          "sleep 999",
		Description:      "lost nested job",
		ParentJobID:      "job_DELEGATE",
		OwnerSessionID:   "CHILD",
		VisibleToSession: "CHILD",
		StartedAt:        &startedAt,
	}
	if err := childSeed.store.Append(childStarted); err != nil {
		t.Fatalf("append child started: %v", err)
	}
	parentStarted := childStarted
	parentStarted.VisibleToSession = "PARENT"
	if err := parentJM.store.Append(parentStarted); err != nil {
		t.Fatalf("append parent started: %v", err)
	}
	if err := childSeed.store.Close(); err != nil {
		t.Fatalf("close child seed store: %v", err)
	}

	var childQueued []jobNotification
	childJM, err := newJobManager(childDir, "CHILD", func(n jobNotification) {
		childQueued = append(childQueued, n)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = childJM.store.Close()
	})
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_DELEGATE"
	childJM.now = func() time.Time { return time.Unix(100, 0).UTC() }

	if err := childJM.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile child lost jobs: %v", err)
	}
	parentRecs, err := parentJM.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	parentRec := parentRecs["job_CHILD_LOST"]
	if parentRec == nil {
		t.Fatalf("parent records = %v, want job_CHILD_LOST", keysOf(parentRecs))
	}
	if parentRec.Status != jobstore.StatusStopped || parentRec.Reason != "runtime_lost" || parentRec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("parent forwarded recovered record = %+v, want stopped/runtime_lost pending", parentRec)
	}
	if len(childQueued) != 1 || childQueued[0].JobID != "job_CHILD_LOST" {
		t.Fatalf("child queued = %+v, want recovered notification", childQueued)
	}
}

func TestNestedArmPendingTerminalNotificationsForwardsRecoveredPending(t *testing.T) {
	var parentQueued []jobNotification
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(n jobNotification) {
		parentQueued = append(parentQueued, n)
	})
	if err != nil {
		t.Fatal(err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_DELEGATE"
	rec, err := childJM.createShell(createShellOpts{Command: "true", Description: "nested terminal"})
	if err != nil {
		t.Fatalf("create child shell: %v", err)
	}

	appendErr := errors.New("pending append failed")
	origAppend := childJM.appendEvent
	childJM.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobNotificationPending {
			return appendErr
		}
		return origAppend(e)
	}
	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); !errors.Is(err, appendErr) {
		t.Fatalf("finalize error = %v, want %v", err, appendErr)
	}
	childJM.appendEvent = origAppend

	parentRecs, err := parentJM.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := parentRecs[rec.JobID]; got == nil || got.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("parent record before recovery = %+v, want terminal not_armed", got)
	}
	if err := childJM.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("arm child pending notifications: %v", err)
	}
	parentRecs, err = parentJM.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := parentRecs[rec.JobID]
	if got == nil || got.Status != jobstore.StatusCompleted || got.NotifyState != jobstore.NotifyPending {
		t.Fatalf("parent record after recovery = %+v, want completed pending", got)
	}
	if len(parentQueued) != 1 || parentQueued[0].JobID != rec.JobID || parentQueued[0].Status != string(jobstore.StatusCompleted) {
		t.Fatalf("parent queued = %+v, want forwarded recovered notification", parentQueued)
	}
}

func TestNestedArmPendingTerminalNotificationsForwardsAlreadyPending(t *testing.T) {
	var parentQueued []jobNotification
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(n jobNotification) {
		parentQueued = append(parentQueued, n)
	})
	if err != nil {
		t.Fatal(err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childJM.store.Close()
	})
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_DELEGATE"
	startedAt := time.Unix(1, 0).UTC()
	endedAt := time.Unix(2, 0).UTC()
	started := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            "job_ALREADY_PENDING",
		Type:             jobstore.JobShell,
		Command:          "true",
		Description:      "already pending nested",
		ParentJobID:      "job_DELEGATE",
		OwnerSessionID:   "CHILD",
		VisibleToSession: "CHILD",
		StartedAt:        &startedAt,
	}
	childStarted := started
	if err := childJM.store.Append(childStarted); err != nil {
		t.Fatalf("append child started: %v", err)
	}
	parentStarted := started
	parentStarted.VisibleToSession = "PARENT"
	if err := parentJM.store.Append(parentStarted); err != nil {
		t.Fatalf("append parent started: %v", err)
	}
	finished := jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       "job_ALREADY_PENDING",
		Status:      jobstore.StatusCompleted,
		Reason:      "exit_zero",
		EndedAt:     &endedAt,
		TerminalGen: "terminal-generation",
	}
	if err := childJM.store.Append(finished); err != nil {
		t.Fatalf("append child finished: %v", err)
	}
	if err := parentJM.store.Append(finished); err != nil {
		t.Fatalf("append parent finished: %v", err)
	}
	if err := childJM.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          endedAt,
		JobID:       "job_ALREADY_PENDING",
		TerminalGen: "terminal-generation",
	}); err != nil {
		t.Fatalf("append child pending: %v", err)
	}

	if err := childJM.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("arm child pending notifications: %v", err)
	}
	parentRecs, err := parentJM.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := parentRecs["job_ALREADY_PENDING"]
	if got == nil || got.NotifyState != jobstore.NotifyPending {
		t.Fatalf("parent record after pending recovery = %+v, want pending", got)
	}
	if len(parentQueued) != 1 || parentQueued[0].JobID != "job_ALREADY_PENDING" {
		t.Fatalf("parent queued = %+v, want forwarded already-pending notification", parentQueued)
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

func TestNestedShellEndToEndThroughTools(t *testing.T) {
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

	delegateCall := parent.reg.ExecuteCall(context.Background(), parent.env, llm.ToolCallData{
		ID:        "delegate",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"host nested shell","background":true,"block_timeout_ms":120000}`),
	})
	if delegateCall.IsError {
		t.Fatalf("delegate tool returned error: %s", delegateCall.Output)
	}
	var delegate delegateToolResult
	if err := json.Unmarshal([]byte(delegateCall.Output), &delegate); err != nil {
		t.Fatalf("unmarshal delegate result: %v (output: %s)", err, delegateCall.Output)
	}
	if delegate.JobID == "" || !delegate.RunningInBackground || delegate.TranscriptRef == "" {
		t.Fatalf("delegate result = %+v, want background job with transcript ref", delegate)
	}
	t.Cleanup(func() {
		_, _ = parent.jobManager.stop(delegate.JobID)
		releaseOnce.Do(func() { close(release) })
		waitForShellDone(t, parent.jobManager, delegate.JobID)
	})

	_, childID, err := decodeRef(delegate.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := parent.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("tracked subagent %q not found", childID)
	}
	child := sub.sess

	shellCall := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'nested-e2e-ready\n'; sleep 30","background":true}`),
	})
	if shellCall.IsError {
		t.Fatalf("child shell tool returned error: %s", shellCall.Output)
	}
	var shell shellToolResult
	if err := json.Unmarshal([]byte(shellCall.Output), &shell); err != nil {
		t.Fatalf("unmarshal shell result: %v (output: %s)", err, shellCall.Output)
	}
	if shell.JobID == "" || !shell.RunningInBackground || shell.Status != string(jobstore.StatusRunning) {
		t.Fatalf("shell result = %+v, want running background nested job", shell)
	}
	nestedID := shell.JobID
	t.Cleanup(func() {
		if rec, err := findJobRecord(child.jobManager, nestedID); err == nil && rec.Status == jobstore.StatusRunning {
			_, _ = parent.stopNestedOrLocal(nestedID)
		}
		waitForShellDone(t, child.jobManager, nestedID)
	})

	defaultListCall := parent.reg.ExecuteCall(context.Background(), parent.env, llm.ToolCallData{
		ID:        "list-default",
		Name:      "job_list",
		Arguments: json.RawMessage(`{}`),
	})
	if defaultListCall.IsError {
		t.Fatalf("default job_list returned error: %s", defaultListCall.Output)
	}
	var defaultList jobListToolOutput
	if err := json.Unmarshal([]byte(defaultListCall.Output), &defaultList); err != nil {
		t.Fatalf("unmarshal default job_list: %v (output: %s)", err, defaultListCall.Output)
	}
	if jobListToolOutputContains(defaultList.Jobs, nestedID) {
		t.Fatalf("default job_list exposed nested job %q: %+v", nestedID, defaultList.Jobs)
	}

	nestedListCall := parent.reg.ExecuteCall(context.Background(), parent.env, llm.ToolCallData{
		ID:        "list-nested",
		Name:      "job_list",
		Arguments: json.RawMessage(`{"include_nested":true}`),
	})
	if nestedListCall.IsError {
		t.Fatalf("include_nested job_list returned error: %s", nestedListCall.Output)
	}
	var nestedList jobListToolOutput
	if err := json.Unmarshal([]byte(nestedListCall.Output), &nestedList); err != nil {
		t.Fatalf("unmarshal include_nested job_list: %v (output: %s)", err, nestedListCall.Output)
	}
	nestedEntry := findJobListToolOutput(nestedList.Jobs, nestedID)
	if nestedEntry == nil {
		t.Fatalf("include_nested job_list missing nested job %q: %+v", nestedID, nestedList.Jobs)
	}
	if nestedEntry.ParentJobID == nil || *nestedEntry.ParentJobID != delegate.JobID {
		t.Fatalf("nested job parent_job_id = %v, want delegate job %q", nestedEntry.ParentJobID, delegate.JobID)
	}

	read := waitForJobOutput(t, parent, nestedID, "nested-e2e-ready")
	if read.JobID != nestedID || read.Status != string(jobstore.StatusRunning) || !strings.Contains(read.Content, "nested-e2e-ready") {
		t.Fatalf("job_read_output result = %+v, want routed running nested output", read)
	}

	stopCall := parent.reg.ExecuteCall(context.Background(), parent.env, llm.ToolCallData{
		ID:        "stop-nested",
		Name:      "job_stop",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, nestedID)),
	})
	if stopCall.IsError {
		t.Fatalf("job_stop returned error: %s", stopCall.Output)
	}
	var stop jobStopResult
	if err := json.Unmarshal([]byte(stopCall.Output), &stop); err != nil {
		t.Fatalf("unmarshal job_stop: %v (output: %s)", err, stopCall.Output)
	}
	if stop.JobID != nestedID || (stop.Status != string(jobstore.StatusCancelled) && stop.Status != string(jobstore.StatusStopped)) {
		t.Fatalf("job_stop result = %+v, want cancelled/stopped nested job %q", stop, nestedID)
	}
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
