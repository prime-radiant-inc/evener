package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
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
		subagents:  newSubagentManager(nil),
	}
	parent.subagents.track(&subagent{
		id:     "CHILD",
		sess:   &Session{id: "CHILD", jobManager: childJM},
		status: SubagentRunning,
	})
	return parent, childJM
}

func TestDelegateRelinkSynchronizesNestedShellParent(t *testing.T) {
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = childJM.store.Close()
	})
	child := &Session{id: "CHILD", jobManager: childJM}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 64; i++ {
			relinkDelegateChildToJob(child, fmt.Sprintf("job_PARENT_%d", i))
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 64; i++ {
			run, err := childJM.newDelayedShell(shellArgs{Command: "true"})
			if err != nil {
				t.Errorf("newDelayedShell: %v", err)
				return
			}
			childJM.discardDelayedShell(run)
		}
	}()
	close(start)
	wg.Wait()
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

func TestDelegateStartForwardsToParent(t *testing.T) {
	root, err := newJobManager(t.TempDir(), "ROOT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new root jobManager: %v", err)
	}
	t.Cleanup(func() { _ = root.store.Close() })

	coordinator := newTestSession(t)
	coordinator.jobManager.forward = root.forwardEvent
	coordinator.jobManager.parentJobID = "job_ROOTDELEGATE"

	worker := newTestSession(t)
	sub := &subagent{
		id:     worker.ID(),
		sess:   worker,
		status: SubagentRunning,
		done:   make(chan struct{}),
	}
	coordinator.subagents.track(sub)

	run, err := coordinator.attachDelegateJob(coordinator.jobManager, worker.ID(), "do work", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		coordinator.jobManager.mu.Lock()
		r := coordinator.jobManager.running[run.rec.JobID]
		coordinator.jobManager.mu.Unlock()
		if r != nil && r.output != nil {
			_ = r.output.Close()
		}
	})

	if run.rec.ParentJobID != "job_ROOTDELEGATE" {
		t.Fatalf("delegate record ParentJobID = %q, want job_ROOTDELEGATE", run.rec.ParentJobID)
	}
	rootRecords, err := root.store.Load()
	if err != nil {
		t.Fatalf("load root store: %v", err)
	}
	rootRec, ok := rootRecords[run.rec.JobID]
	if !ok {
		t.Fatalf("root store keys = %v, want forwarded delegate job %q", keysOf(rootRecords), run.rec.JobID)
	}
	if rootRec.Type != jobstore.JobDelegate {
		t.Fatalf("root record Type = %q, want %q", rootRec.Type, jobstore.JobDelegate)
	}
	if rootRec.OwnerSessionID != coordinator.ID() {
		t.Fatalf("root record OwnerSessionID = %q, want %q", rootRec.OwnerSessionID, coordinator.ID())
	}
	if rootRec.ParentJobID != "job_ROOTDELEGATE" {
		t.Fatalf("root record ParentJobID = %q, want job_ROOTDELEGATE", rootRec.ParentJobID)
	}
	if rootRec.VisibleToSession != "ROOT" {
		t.Fatalf("root record VisibleToSession = %q, want ROOT", rootRec.VisibleToSession)
	}
	if rootRec.Status != jobstore.StatusRunning {
		t.Fatalf("root record Status = %q, want %q", rootRec.Status, jobstore.StatusRunning)
	}
}

func TestDelegateStartForwardFailureWritesDurableTerminal(t *testing.T) {
	coordinator := newTestSession(t)
	t.Cleanup(func() { _ = coordinator.jobManager.store.Close() })
	coordinator.jobManager.parentJobID = "job_ROOTDELEGATE"
	coordinator.jobManager.forward = func(jobstore.Event) error {
		return errors.New("parent append failed")
	}

	worker := newTestSession(t)
	sub := &subagent{
		id:     worker.ID(),
		sess:   worker,
		status: SubagentRunning,
		done:   make(chan struct{}),
	}
	coordinator.subagents.track(sub)

	run, err := coordinator.attachDelegateJob(coordinator.jobManager, worker.ID(), "do work", sub)
	if err == nil {
		t.Fatalf("attachDelegateJob run=%+v, want forward failure", run)
	}
	if !errors.Is(err, errDelegateStartForwardFailed) {
		t.Fatalf("attachDelegateJob error = %v, want errDelegateStartForwardFailed", err)
	}

	records, loadErr := coordinator.jobManager.store.Load()
	if loadErr != nil {
		t.Fatalf("load coordinator store: %v", loadErr)
	}
	if len(records) != 1 {
		t.Fatalf("coordinator store records = %+v, want one failed delegate record", records)
	}
	for _, record := range records {
		if record.Status != jobstore.StatusFailed || record.Reason != "forward_failed" {
			t.Fatalf("delegate record after forward failure = %+v, want failed/forward_failed", record)
		}
		coordinator.jobManager.mu.Lock()
		survived := coordinator.jobManager.running[record.JobID]
		coordinator.jobManager.mu.Unlock()
		if survived != nil {
			t.Fatalf("delegate running entry survived start forward failure: %+v", survived.rec)
		}
	}
}

func TestParentRuntimeCloseKeepsStoreOpenForNestedTerminalForward(t *testing.T) {
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

	if err := parentJM.closeRuntimeState(); err != nil {
		t.Fatalf("close parent runtime state: %v", err)
	}
	exitCode := 0
	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &exitCode); err != nil {
		t.Fatalf("finalize child job: %v", err)
	}

	parentRec, err := findJobRecord(parentJM, rec.JobID)
	if err != nil {
		t.Fatalf("find parent forwarded record: %v", err)
	}
	if parentRec.Status != jobstore.StatusCompleted || parentRec.Reason != "exit_zero" {
		t.Fatalf("parent forwarded record = %+v, want completed/exit_zero", parentRec)
	}
}

func TestNestedShellStartReturnsForwardFailure(t *testing.T) {
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() { _ = childJM.store.Close() })

	childJM.parentJobID = "job_PARENTDELEGATE"
	childJM.forward = func(jobstore.Event) error {
		return errors.New("parent append failed")
	}

	rec, err := childJM.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err == nil {
		t.Fatalf("createShell rec=%+v, want parent append error", rec)
	}
	if !strings.Contains(err.Error(), "parent append failed") {
		t.Fatalf("createShell error = %v, want parent append failure", err)
	}
	records, loadErr := childJM.store.Load()
	if loadErr != nil {
		t.Fatalf("load child store: %v", loadErr)
	}
	if len(records) != 1 {
		t.Fatalf("child store records = %+v, want failed started record", records)
	}
	for _, record := range records {
		if record.Status != jobstore.StatusFailed || record.Reason != "forward_failed" {
			t.Fatalf("child record after forward failure = %+v, want failed/forward_failed", record)
		}
		childJM.mu.Lock()
		run := childJM.running[record.JobID]
		childJM.mu.Unlock()
		if run != nil {
			t.Fatalf("child running entry survived start forward failure: %+v", run.rec)
		}
	}
}

func TestNestedDelayedShellStartForwardFailurePreservesFailedOutput(t *testing.T) {
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() { _ = childJM.store.Close() })

	childJM.parentJobID = "job_PARENTDELEGATE"
	childJM.forward = func(jobstore.Event) error {
		return errors.New("parent append failed")
	}
	res := runShell(context.Background(), childJM, waitErrorStreamingExecutor{}, shellArgs{
		Command:    "sleep 1",
		Background: true,
	})
	if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" || res.JobID != "" {
		t.Fatalf("runShell result = %+v, want failed/start_failed without job id", res)
	}
	records, err := childJM.store.Load()
	if err != nil {
		t.Fatalf("load child store: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("child store records = %+v, want failed delayed shell record", records)
	}
	for _, record := range records {
		if record.Status != jobstore.StatusFailed || record.Reason != "forward_failed" {
			t.Fatalf("child record after delayed forward failure = %+v, want failed/forward_failed", record)
		}
		content, total, _, err := childJM.readOutput(record.JobID, 1024)
		if err != nil {
			t.Fatalf("read failed delayed output: %v", err)
		}
		if content != "partial" || total != int64(len("partial")) {
			t.Fatalf("failed delayed output = %q total=%d, want retained executor output", content, total)
		}
	}
}

func TestNestedDelayedShellStartForwardTerminalAppendFailureFinalizesRun(t *testing.T) {
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

	childJM.parentJobID = "job_PARENTDELEGATE"
	childJM.forward = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobStarted {
			return errors.New("parent append failed")
		}
		return parentJM.forwardEvent(e)
	}
	realAppend := childJM.appendEvent
	failForwardFailedTerminal := true
	childJM.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished && e.Reason == "forward_failed" && failForwardFailedTerminal {
			failForwardFailedTerminal = false
			return errors.New("terminal append failed")
		}
		return realAppend(e)
	}

	res := runShell(context.Background(), childJM, waitErrorStreamingExecutor{}, shellArgs{
		Command:    "sleep 1",
		Background: true,
	})
	if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" || res.JobID != "" {
		t.Fatalf("runShell result = %+v, want failed/start_failed without job id", res)
	}

	var terminal *jobstore.JobRecord
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		records, err := childJM.store.Load()
		if err != nil {
			t.Fatalf("load child store: %v", err)
		}
		for _, record := range records {
			if record.Status.IsTerminal() {
				terminal = record
				break
			}
		}
		if terminal != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if terminal == nil {
		t.Fatalf("timed out waiting for terminal child record; records=%+v", childJM.list(listFilter{IncludeNested: true}))
	}
	if terminal.Status != jobstore.StatusFailed || terminal.Reason != "forward_failed" {
		t.Fatalf("fallback terminal = %+v, want failed/forward_failed", terminal)
	}
	childJM.mu.Lock()
	run := childJM.running[terminal.JobID]
	childJM.mu.Unlock()
	if run != nil {
		t.Fatalf("running entry survived fallback finalization: %+v", run.rec)
	}
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	if len(parentRecords) != 0 {
		t.Fatalf("parent records after failed start forward fallback = %+v, want none", parentRecords)
	}
}

func TestNestedRuntimeTimeoutForwardFailurePreservesFailedOutput(t *testing.T) {
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	t.Cleanup(func() { _ = childJM.store.Close() })

	childJM.parentJobID = "job_PARENTDELEGATE"
	childJM.forward = func(jobstore.Event) error {
		return errors.New("parent append failed")
	}
	exec := newSignalCompletesStreamingExecutor()
	res := runShell(context.Background(), childJM, exec, shellArgs{
		Command:        "sleep 1",
		BlockTimeoutMS: 5000,
		MaxRuntimeMS:   1,
	})
	if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" || res.JobID != "" {
		t.Fatalf("runShell result = %+v, want failed/start_failed without job id", res)
	}
	if exec.signals.Load() == 0 {
		t.Fatal("runtime timeout did not signal process")
	}
	records, err := childJM.store.Load()
	if err != nil {
		t.Fatalf("load child store: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("child store records = %+v, want failed runtime-timeout shell record", records)
	}
	for _, record := range records {
		if record.Status != jobstore.StatusFailed || record.Reason != "forward_failed" {
			t.Fatalf("child record after runtime-timeout forward failure = %+v, want failed/forward_failed", record)
		}
		content, total, _, err := childJM.readOutput(record.JobID, 1024)
		if err != nil {
			t.Fatalf("read failed runtime-timeout output: %v", err)
		}
		if content != "running" || total != int64(len("running")) {
			t.Fatalf("failed runtime-timeout output = %q total=%d, want retained executor output", content, total)
		}
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
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_lines":65536,"grep":"owner"}`, nested.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_read_output returned error: %s", res.Output)
	}
	var out jobReadOutputTestResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
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
		subagents:  newSubagentManager(nil),
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
		subagents:  newSubagentManager(nil),
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
		subagents:  newSubagentManager(nil),
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
		subagents:  newSubagentManager(nil),
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

	snap, err := parent.readJobOutputSnapshot(owner, parent, nested.JobID, 65536, false, nil)
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

// TestNestedReadOutputDepth2FallsBackToOwnerParentForwardedCopy proves the A8
// closed-store fallback for a depth >= 2 owner recovers from the owner's DIRECT
// PARENT store (where the single-hop forwarded terminal copy + durable output
// land), not from the receiver (the owner itself) and not from the root.
//
// Topology: root -> coord -> worker, one-hop forwarding. The worker (depth 2)
// finalizes a job, so the forwarded terminal copy reaches COORD (the worker's
// direct parent), NOT the root. Closing the worker (owner) store then drives the
// closed-store fallback with the worker as receiver/current and COORD as the
// fallback target — exactly what the fixed jobReadOutputTool passes. The
// fallback must resolve `local` from COORD and recover the forwarded copy.
func TestNestedReadOutputDepth2FallsBackToOwnerParentForwardedCopy(t *testing.T) {
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
	})

	// One-hop forwarding: each child forwards into its direct parent's store.
	coordJM.forward = rootJM.forwardEvent
	coordJM.parentJobID = "job_root_delegate_coord"
	workerJM.forward = coordJM.forwardEvent
	workerJM.parentJobID = "job_coord_delegate_worker"

	workerRec, err := workerJM.createShell(createShellOpts{Command: "sleep 1", Description: "worker job"})
	if err != nil {
		t.Fatalf("create worker shell: %v", err)
	}
	workerJM.mu.Lock()
	run := workerJM.running[workerRec.JobID]
	workerJM.mu.Unlock()
	if run == nil || run.output == nil {
		t.Fatalf("worker job %q has no live output store", workerRec.JobID)
	}
	if _, err := workerJM.appendJobOutput(workerRec.JobID, run.output, []byte("worker grandchild line\n")); err != nil {
		t.Fatalf("append worker output: %v", err)
	}
	code := 0
	if err := workerJM.finalize(workerRec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize worker job: %v", err)
	}

	// The forwarded terminal copy reaches COORD (the worker's direct parent), not
	// the root: forwarding is single-hop.
	if _, err := findJobRecord(coordJM, workerRec.JobID); err != nil {
		t.Fatalf("coord store missing forwarded worker copy: %v", err)
	}
	if _, err := findJobRecord(rootJM, workerRec.JobID); err == nil {
		t.Fatalf("root store unexpectedly holds forwarded worker copy; forwarding must be single-hop")
	}

	worker := &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil)}
	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	coordinator.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})

	// Close the OWNER (worker) store: the read can only be served from the
	// forwarded copy in COORD.
	if err := workerJM.store.Close(); err != nil {
		t.Fatalf("close worker store: %v", err)
	}

	// Drive the fallback at the level the fix lives: receiver/current is the owner
	// (worker), jm is the owner's closed store, and the fallback target is the
	// owner's DIRECT PARENT (coord) — exactly what the fixed jobReadOutputTool
	// passes. At HEAD the fallback resolves `local` from the receiver (worker),
	// local == current, so it returns (nil,false,ErrStoreClosed) and recovery
	// fails. After the fix it resolves `local` from coord, local != current, and
	// the forwarded copy is recovered.
	snap, err := worker.readJobOutputSnapshot(workerJM, coordinator, workerRec.JobID, 65536, false, nil)
	if err != nil {
		t.Fatalf("readJobOutputSnapshot returned error: %v", err)
	}
	if snap.Manager != coordJM {
		t.Fatalf("readJobOutputSnapshot manager = %p, want owner-parent fallback %p (coord)", snap.Manager, coordJM)
	}
	if snap.Record == nil || snap.Record.JobID != workerRec.JobID || snap.Record.Status != jobstore.StatusCompleted {
		t.Fatalf("snapshot record = %+v, want completed coord forwarded record", snap.Record)
	}
	if !strings.Contains(snap.Content, "worker grandchild line") {
		t.Fatalf("snapshot content = %q, want worker durable output recovered from coord", snap.Content)
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
		subagents:  newSubagentManager(nil),
	}
	parent.subagents.track(&subagent{
		id:     "CHILD",
		sess:   &Session{id: "CHILD", jobManager: childJM},
		status: SubagentRunning,
	})

	// The output and structured result land BEFORE the blocking read starts:
	// a blocking read legitimately wakes on output growth as well as terminal
	// state, so appending mid-block would race the running-vs-completed
	// projection. With output pre-retained, the only wake is the finalize.
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

	type readResult struct {
		out any
		err error
	}
	readDone := make(chan readResult, 1)
	go func() {
		out, err := jobReadOutputTool(context.Background(), parent, map[string]any{
			"job_id":      nested.JobID,
			"max_wait_ms": 1000,
			"tail_lines":  65536,
		}, 20000)
		readDone <- readResult{out: out, err: err}
	}()

	time.Sleep(50 * time.Millisecond)
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
	if err := json.Unmarshal(handlerJSON(t, got.out), &out); err != nil {
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
	if err := json.Unmarshal(handlerJSON(t, out), &stop); err != nil {
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
	if err := json.Unmarshal(handlerJSON(t, out), &stop); err != nil {
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

func TestForwardedNestedDoesNotReconcileRuntimeLostOnParentRestart(t *testing.T) {
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
	if rec.JobID != "job_NESTED" || rec.Status != jobstore.StatusRunning || rec.Reason != "" {
		t.Fatalf("forwarded nested record = %+v, want job_NESTED still running for child owner recovery", rec)
	}
	if rec.Type != jobstore.JobShell || rec.Command != "sleep 999" || rec.Description != "forwarded nested job" {
		t.Fatalf("forwarded nested metadata = %+v, want original shell command metadata", rec)
	}
	if rec.ParentJobID != "job_DELEGATE" || rec.OwnerSessionID != "CHILDGONE" || rec.VisibleToSession != "PARENT" {
		t.Fatalf("forwarded nested ownership metadata = %+v, want parent/owner/visible preserved", rec)
	}
	if rec.TerminalGen != "" {
		t.Fatalf("forwarded nested terminal generation = %q, want empty before child owner recovery", rec.TerminalGen)
	}
	if len(queued) != 0 {
		t.Fatalf("queued runtime_lost notification for forwarded child-owned job: %+v", queued)
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
	if len(queued) != 0 {
		t.Fatalf("second reconcile queued notification for forwarded child-owned job: %+v", queued)
	}
}

func TestRestoreSessionDoesNotInstallNestedForwardHook(t *testing.T) {
	stateDir := t.TempDir()
	parentJM, err := newJobManager(stateDir, "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	childSeed, err := newJobManager(stateDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
	})
	startedAt := time.Unix(1, 0).UTC()
	endedAt := time.Unix(2, 0).UTC()
	started := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            "job_CHILD_COMPLETED",
		Type:             jobstore.JobShell,
		Command:          "true",
		Description:      "restored child completed nested",
		ParentJobID:      "job_DELEGATE",
		OwnerSessionID:   "CHILD",
		VisibleToSession: "CHILD",
		StartedAt:        &startedAt,
	}
	if err := childSeed.store.Append(started); err != nil {
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
		JobID:       "job_CHILD_COMPLETED",
		Status:      jobstore.StatusCompleted,
		Reason:      "exit_zero",
		EndedAt:     &endedAt,
		TerminalGen: "terminal-generation",
	}
	if err := childSeed.store.Append(finished); err != nil {
		t.Fatalf("append child finished: %v", err)
	}
	if err := childSeed.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobNotificationPending,
		TS:          endedAt,
		JobID:       "job_CHILD_COMPLETED",
		TerminalGen: "terminal-generation",
	}); err != nil {
		t.Fatalf("append child pending: %v", err)
	}
	if err := childSeed.store.Close(); err != nil {
		t.Fatalf("close child seed store: %v", err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:        "CHILD",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
		CreatedAt: startedAt,
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if restored.jobManager.forward != nil || restored.jobManager.parentJobID != "" {
		t.Fatalf("restored child forwardSet=%t parentJobID=%q, want no nested forward hook", restored.jobManager.forward != nil, restored.jobManager.parentJobID)
	}
	parentRecs, err := parentJM.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := parentRecs["job_CHILD_COMPLETED"]
	if got == nil || got.Status != jobstore.StatusRunning || got.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("parent record after child restore = %+v, want still running/not_armed", got)
	}

	parentJM.now = func() time.Time { return time.Unix(100, 0).UTC() }
	if err := parentJM.reconcileLostJobs(); err != nil {
		t.Fatalf("parent reconcile lost jobs: %v", err)
	}
	parentRecs, err = parentJM.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got = parentRecs["job_CHILD_COMPLETED"]
	if got.Status != jobstore.StatusRunning || got.Reason != "" || got.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("parent record after reconcile = %+v, want still running/not_armed for child owner recovery", got)
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
	// Owner-scoped notifications (spec §3/§10): the terminal generation forwards
	// to the parent's store verbatim for visibility, but the child-owned job must
	// NOT land on the parent's notification rail — the subagent renders it, the
	// parent is driven, not interrupted.
	if len(parentNotifications) != 0 {
		t.Fatalf("parent notifications = %+v, want none (child-owned job is owner-scoped)", parentNotifications)
	}
}

func TestNestedTerminalForwardFailureRetriesSameGeneration(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child jobManager: %v", err)
	}
	finishedEvents := 0
	childJM.emit = func(kind events.EventKind, _ events.EventData, _ *provenance.Causal) {
		if kind == events.EventJobFinished {
			finishedEvents++
		}
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
	callbackCalls := 0
	childJM.mu.Lock()
	run := childJM.running[rec.JobID]
	if run == nil {
		childJM.mu.Unlock()
		t.Fatalf("running child job %q not found", rec.JobID)
	}
	run.afterDurableFinish = func() { callbackCalls++ }
	childJM.mu.Unlock()

	forwardErr := errors.New("forward terminal failed")
	childJM.forward = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished {
			return forwardErr
		}
		return parentJM.forwardEvent(e)
	}
	code := 0
	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); !errors.Is(err, forwardErr) {
		t.Fatalf("finalize error = %v, want forward terminal failure", err)
	}
	childRecords, err := childJM.store.Load()
	if err != nil {
		t.Fatalf("load child store: %v", err)
	}
	childRec := childRecords[rec.JobID]
	if childRec == nil || childRec.TerminalGen == "" {
		t.Fatalf("child record after failed forward = %+v, want terminal generation", childRec)
	}
	if callbackCalls != 0 {
		t.Fatalf("afterDurableFinish calls after failed forward = %d, want 0", callbackCalls)
	}
	if finishedEvents != 0 {
		t.Fatalf("finished events after failed forward = %d, want 0", finishedEvents)
	}

	childJM.forward = parentJM.forwardEvent
	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("afterDurableFinish calls after retry = %d, want 1", callbackCalls)
	}
	if finishedEvents != 1 {
		t.Fatalf("finished events after retry = %d, want 1", finishedEvents)
	}
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec := parentRecords[rec.JobID]
	if parentRec == nil || parentRec.Status != jobstore.StatusCompleted {
		t.Fatalf("parent record after retry = %+v, want completed forwarded record", parentRec)
	}
	if parentRec.TerminalGen != childRec.TerminalGen {
		t.Fatalf("parent TerminalGen = %q, want original child generation %q", parentRec.TerminalGen, childRec.TerminalGen)
	}
}

func TestNestedTerminalForwardRecoveryReplaysStoredTerminal(t *testing.T) {
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
	rec, err := childJM.createShell(createShellOpts{Command: "true", Description: "nested"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}

	forwardErr := errors.New("forward terminal failed")
	childJM.forward = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished {
			return forwardErr
		}
		return parentJM.forwardEvent(e)
	}
	code := 0
	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); !errors.Is(err, forwardErr) {
		t.Fatalf("finalize error = %v, want forward terminal failure", err)
	}
	childRecords, err := childJM.store.Load()
	if err != nil {
		t.Fatalf("load child store: %v", err)
	}
	childRec := childRecords[rec.JobID]
	if childRec == nil || childRec.TerminalGen == "" {
		t.Fatalf("child record after failed forward = %+v, want terminal generation", childRec)
	}
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec := parentRecords[rec.JobID]
	if parentRec == nil || parentRec.Status != jobstore.StatusRunning {
		t.Fatalf("parent record before recovery = %+v, want stale running record", parentRec)
	}

	childJM.forward = parentJM.forwardEvent
	if err := childJM.recoverForwardedTerminalEvents(); err != nil {
		t.Fatalf("recoverForwardedTerminalEvents: %v", err)
	}
	parentRecords, err = parentJM.store.Load()
	if err != nil {
		t.Fatalf("reload parent store: %v", err)
	}
	parentRec = parentRecords[rec.JobID]
	if parentRec == nil || parentRec.Status != jobstore.StatusCompleted {
		t.Fatalf("parent record after recovery = %+v, want completed forwarded record", parentRec)
	}
	if parentRec.TerminalGen != childRec.TerminalGen {
		t.Fatalf("parent TerminalGen = %q, want original child generation %q", parentRec.TerminalGen, childRec.TerminalGen)
	}
	if parentRec.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("parent NotifyState = %q, want %q until child arms notification", parentRec.NotifyState, jobstore.NotifyNotArmed)
	}

	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("cleanup finalize: %v", err)
	}
}

func TestDeferredRestoreSideEffectsRecoverNestedTerminalForward(t *testing.T) {
	var parentNotifications []jobNotification
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(n jobNotification) {
		parentNotifications = append(parentNotifications, n)
	})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childDir := t.TempDir()
	childSeed, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new seed child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childSeed.store.Close()
	})

	startedAt := time.Unix(4500, 0).UTC()
	jobID := jobstore.NewJobID()
	started := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		Command:          "true",
		Description:      "restored nested shell",
		OwnerSessionID:   "CHILD",
		VisibleToSession: "CHILD",
		ParentJobID:      "job_PARENTDELEGATE",
		StartedAt:        &startedAt,
	}
	if err := childSeed.store.Append(started); err != nil {
		t.Fatalf("append child started: %v", err)
	}
	if err := parentJM.forwardEvent(started); err != nil {
		t.Fatalf("append parent forwarded start: %v", err)
	}
	endedAt := startedAt.Add(time.Second)
	terminalGen := jobstore.NewTerminalGeneration()
	if err := childSeed.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       jobID,
		Status:      jobstore.StatusCompleted,
		Reason:      "exit_zero",
		EndedAt:     &endedAt,
		TerminalGen: terminalGen,
	}); err != nil {
		t.Fatalf("append child finished: %v", err)
	}
	if err := childSeed.store.Close(); err != nil {
		t.Fatalf("close seed child store: %v", err)
	}

	childJM, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new restored child jobManager: %v", err)
	}
	t.Cleanup(func() { _ = childJM.store.Close() })
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"
	restored := &Session{
		id:         "CHILD",
		stateDir:   childDir,
		jobManager: childJM,
	}

	if err := restored.runDeferredRestoreSideEffects(); err != nil {
		t.Fatalf("runDeferredRestoreSideEffects: %v", err)
	}
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec := parentRecords[jobID]
	if parentRec == nil || parentRec.Status != jobstore.StatusCompleted {
		t.Fatalf("parent record after deferred recovery = %+v, want completed forwarded record", parentRec)
	}
	if parentRec.TerminalGen != terminalGen {
		t.Fatalf("parent TerminalGen = %q, want child TerminalGen %q", parentRec.TerminalGen, terminalGen)
	}
	if parentRec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("parent NotifyState = %q, want %q", parentRec.NotifyState, jobstore.NotifyPending)
	}
	// Owner-scoped notifications (spec §3/§10): the restored terminal forwards to
	// the parent's store for visibility, but the child-owned job must NOT land on
	// the parent's notification rail. The subagent re-arms and renders it on its
	// own rail; the parent is driven, not interrupted.
	if len(parentNotifications) != 0 {
		t.Fatalf("parent notifications = %+v, want none (child-owned job is owner-scoped)", parentNotifications)
	}
}

func TestRecoverForwardedTerminalReconstructsMissingParentStart(t *testing.T) {
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

	startedAt := time.Unix(4700, 0).UTC()
	endedAt := startedAt.Add(time.Second)
	jobID := jobstore.NewJobID()
	started := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		Command:          "printf nested",
		Description:      "nested start lost before parent forward",
		OwnerSessionID:   "CHILD",
		VisibleToSession: "CHILD",
		ParentJobID:      "job_PARENTDELEGATE",
		StartedAt:        &startedAt,
		OutputPath:       "/tmp/nested.log",
	}
	if err := childJM.store.Append(started); err != nil {
		t.Fatalf("append child started: %v", err)
	}
	if err := childJM.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       jobID,
		Status:      jobstore.StatusCompleted,
		Reason:      "exit_zero",
		EndedAt:     &endedAt,
		TerminalGen: jobstore.NewTerminalGeneration(),
	}); err != nil {
		t.Fatalf("append child finished: %v", err)
	}

	if err := childJM.recoverForwardedTerminalEvents(); err != nil {
		t.Fatalf("recoverForwardedTerminalEvents: %v", err)
	}
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec := parentRecords[jobID]
	if parentRec == nil {
		t.Fatalf("parent record missing for recovered nested job %s", jobID)
	}
	if parentRec.Status != jobstore.StatusCompleted || parentRec.Type != jobstore.JobShell ||
		parentRec.OwnerSessionID != "CHILD" || parentRec.VisibleToSession != "PARENT" ||
		parentRec.ParentJobID != "job_PARENTDELEGATE" || parentRec.OutputPath != "/tmp/nested.log" {
		t.Fatalf("parent recovered record = %+v, want started metadata plus completed terminal state", parentRec)
	}
}

func TestDeferredRestoreSideEffectsForwardsReconciledNestedRuntimeLost(t *testing.T) {
	var parentNotifications []jobNotification
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(n jobNotification) {
		parentNotifications = append(parentNotifications, n)
	})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childDir := t.TempDir()
	childSeed, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new seed child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childSeed.store.Close()
	})

	startedAt := time.Unix(4600, 0).UTC()
	jobID := jobstore.NewJobID()
	started := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		Command:          "sleep 60",
		Description:      "lost nested shell",
		OwnerSessionID:   "CHILD",
		VisibleToSession: "CHILD",
		ParentJobID:      "job_PARENTDELEGATE",
		StartedAt:        &startedAt,
	}
	if err := childSeed.store.Append(started); err != nil {
		t.Fatalf("append child started: %v", err)
	}
	if err := parentJM.forwardEvent(started); err != nil {
		t.Fatalf("append parent forwarded start: %v", err)
	}
	if err := childSeed.store.Close(); err != nil {
		t.Fatalf("close seed child store: %v", err)
	}

	childJM, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new restored child jobManager: %v", err)
	}
	t.Cleanup(func() { _ = childJM.store.Close() })
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"
	restored := &Session{
		id:         "CHILD",
		stateDir:   childDir,
		jobManager: childJM,
	}

	if err := restored.runDeferredRestoreSideEffects(); err != nil {
		t.Fatalf("runDeferredRestoreSideEffects: %v", err)
	}
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec := parentRecords[jobID]
	if parentRec == nil || parentRec.Status != jobstore.StatusStopped || parentRec.Reason != "runtime_lost" {
		t.Fatalf("parent record after deferred reconcile = %+v, want stopped/runtime_lost", parentRec)
	}
	if parentRec.TerminalGen == "" {
		t.Fatal("parent TerminalGen is empty after deferred reconcile")
	}
	if parentRec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("parent NotifyState = %q, want %q", parentRec.NotifyState, jobstore.NotifyPending)
	}
	// Owner-scoped notifications (spec §3/§10): the reconciled runtime_lost
	// terminal forwards to the parent's store for visibility, but the child-owned
	// job must NOT land on the parent's notification rail. The subagent renders it
	// on its own rail; the parent is driven, not interrupted.
	if len(parentNotifications) != 0 {
		t.Fatalf("parent notifications = %+v, want none (child-owned job is owner-scoped)", parentNotifications)
	}
}

func TestDeferredRestoreSideEffectsSkipsStartForwardFailedNestedTerminal(t *testing.T) {
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	childDir := t.TempDir()
	childSeed, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new seed child jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = childSeed.store.Close()
	})

	startedAt := time.Unix(4700, 0).UTC()
	jobID := jobstore.NewJobID()
	if err := childSeed.store.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		Command:          "true",
		Description:      "failed start forward",
		OwnerSessionID:   "CHILD",
		VisibleToSession: "CHILD",
		ParentJobID:      "job_PARENTDELEGATE",
		StartedAt:        &startedAt,
	}); err != nil {
		t.Fatalf("append child started: %v", err)
	}
	endedAt := startedAt.Add(time.Second)
	if err := childSeed.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          endedAt,
		JobID:       jobID,
		Status:      jobstore.StatusFailed,
		Reason:      "forward_failed",
		EndedAt:     &endedAt,
		TerminalGen: jobstore.NewTerminalGeneration(),
	}); err != nil {
		t.Fatalf("append child finished: %v", err)
	}
	if err := childSeed.store.Close(); err != nil {
		t.Fatalf("close seed child store: %v", err)
	}

	childJM, err := newJobManager(childDir, "CHILD", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new restored child jobManager: %v", err)
	}
	t.Cleanup(func() { _ = childJM.store.Close() })
	childJM.forward = parentJM.forwardEvent
	childJM.parentJobID = "job_PARENTDELEGATE"
	restored := &Session{
		id:         "CHILD",
		stateDir:   childDir,
		jobManager: childJM,
	}

	if err := restored.runDeferredRestoreSideEffects(); err != nil {
		t.Fatalf("runDeferredRestoreSideEffects: %v", err)
	}
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	if parentRec := parentRecords[jobID]; parentRec != nil {
		t.Fatalf("parent record after skipped recovery = %+v, want no malformed terminal-only record", parentRec)
	}
}

func TestNestedNotificationForwardFailureRetriesPendingNotification(t *testing.T) {
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
	rec, err := childJM.createShell(createShellOpts{Command: "true", Description: "nested"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}

	forwardErr := errors.New("forward pending failed")
	childJM.forward = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobNotificationPending {
			return forwardErr
		}
		return parentJM.forwardEvent(e)
	}
	code := 0
	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); !errors.Is(err, forwardErr) {
		t.Fatalf("finalize error = %v, want forward pending failure", err)
	}

	childJM.forward = parentJM.forwardEvent
	if err := childJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec := parentRecords[rec.JobID]
	if parentRec == nil || parentRec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("parent record after retry = %+v, want forwarded pending notification", parentRec)
	}
}

// TestForwardedChildJobDoesNotNotifyParentRail proves the owner-scoped
// notification rule (spec §3/§10): a subagent's nested job notifies the
// SUBAGENT, never the subagent's parent. When the child forwards its terminal
// notification-pending event, forwardEvent appends the record to the parent's
// store (visibility — the parent can still job_list down the tree) but must NOT
// push a child-owned job onto the parent's notification rail.
func TestForwardedChildJobDoesNotNotifyParentRail(t *testing.T) {
	var parentRailMu sync.Mutex
	var parentRail []jobNotification
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(n jobNotification) {
		parentRailMu.Lock()
		parentRail = append(parentRail, n)
		parentRailMu.Unlock()
	})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	var childRailMu sync.Mutex
	var childRail []jobNotification
	childJM, err := newJobManager(t.TempDir(), "CHILD", func(n jobNotification) {
		childRailMu.Lock()
		childRail = append(childRail, n)
		childRailMu.Unlock()
	})
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
		t.Fatalf("finalize nested child job: %v", err)
	}

	// The owner (the subagent) IS notified about its own nested job.
	childRailMu.Lock()
	childOwned := false
	for _, n := range childRail {
		if n.JobID == rec.JobID {
			childOwned = true
		}
	}
	childRailMu.Unlock()
	if !childOwned {
		t.Fatalf("child rail = %+v, want the subagent notified about its own job %q", childRail, rec.JobID)
	}

	// The parent's rail must contain NO entry for the child-owned job: the leak
	// is closed. The parent is not interrupted about a job it did not create.
	parentRailMu.Lock()
	for _, n := range parentRail {
		if n.JobID == rec.JobID {
			parentRailMu.Unlock()
			t.Fatalf("parent rail leaked child-owned job %q: %+v", rec.JobID, parentRail)
		}
	}
	parentRailMu.Unlock()

	// Visibility is preserved: the forwarded record is still in the parent's
	// store, so the parent can job_list(include_descendants=true) down the tree.
	parentRecords, err := parentJM.store.Load()
	if err != nil {
		t.Fatalf("load parent store: %v", err)
	}
	parentRec := parentRecords[rec.JobID]
	if parentRec == nil {
		t.Fatalf("parent store keys = %v, want forwarded record %q for visibility", keysOf(parentRecords), rec.JobID)
	}
	if parentRec.OwnerSessionID != "CHILD" {
		t.Fatalf("parent record OwnerSessionID = %q, want CHILD", parentRec.OwnerSessionID)
	}
	if parentRec.VisibleToSession != "PARENT" {
		t.Fatalf("parent record VisibleToSession = %q, want PARENT", parentRec.VisibleToSession)
	}
	if parentRec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("parent record NotifyState = %q, want pending (forwarded for visibility)", parentRec.NotifyState)
	}
}

// TestParentNotifiedWhenOwnDelegateFinishes is the keep-green counterpart to the
// owner-scoping rule: the parent IS still notified when its OWN delegate (the
// subagent itself) finishes. That notification flows through the parent's own
// jm.enqueue in armFinalizedJob (the delegate job is owned by the parent), not
// through forwardEvent, so the owner gate must not suppress it.
func TestParentNotifiedWhenOwnDelegateFinishes(t *testing.T) {
	var parentRailMu sync.Mutex
	var parentRail []jobNotification
	parentJM, err := newJobManager(t.TempDir(), "PARENT", func(n jobNotification) {
		parentRailMu.Lock()
		parentRail = append(parentRail, n)
		parentRailMu.Unlock()
	})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
	})

	// A delegate job the PARENT created is owned by the parent (OwnerSessionID ==
	// "PARENT"); it is the parent's own job, not a forwarded child job.
	rec, err := parentJM.createShell(createShellOpts{Command: "true", Description: "own delegate"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	if rec.OwnerSessionID != "PARENT" {
		t.Fatalf("own job OwnerSessionID = %q, want PARENT", rec.OwnerSessionID)
	}
	code := 0
	if err := parentJM.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize own delegate job: %v", err)
	}

	parentRailMu.Lock()
	notified := false
	for _, n := range parentRail {
		if n.JobID == rec.JobID {
			notified = true
		}
	}
	parentRailMu.Unlock()
	if !notified {
		t.Fatalf("parent rail = %+v, want parent notified about its own finished job %q", parentRail, rec.JobID)
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
		Arguments: json.RawMessage(`{"task":"host nested shell","max_wait_ms":0}`),
	})
	if delegateCall.IsError {
		t.Fatalf("delegate tool returned error: %s", delegateCall.Output)
	}
	var delegate delegateToolResult
	if err := json.Unmarshal(toolResultJSON(delegateCall), &delegate); err != nil {
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
	if err := json.Unmarshal(toolResultJSON(shellCall), &shell); err != nil {
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
	if err := json.Unmarshal(toolResultJSON(defaultListCall), &defaultList); err != nil {
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
	if err := json.Unmarshal(toolResultJSON(nestedListCall), &nestedList); err != nil {
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
	if err := json.Unmarshal(toolResultJSON(stopCall), &stop); err != nil {
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

	// Current production behavior rejects subagent management below the root
	// (the allowance gate: delegationAllowance == 0). This fixture opens that
	// guard (allowance 1) so the test can exercise config-copy / forward-seam
	// inheritance through a directly-spawned grandchild.
	sub.sess.mu.Lock()
	sub.sess.depth = 0
	sub.sess.cfg.spawn.depth = 0
	sub.sess.cfg.MaxSubagentDepth = 1
	sub.sess.delegationAllowance = 1
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

// TestJobReadOutputDepth2Resolves drives a depth-3 live tree
// (root -> coordinator -> worker) where the WORKER owns a running shell job,
// forwarded one hop into the coordinator's store and one more hop into the
// root's store. job_read_output(worker_job) issued by the root must resolve
// through the recursive owner path (root -> coordinator -> worker) and serve the
// worker's live bytes from the worker's store. A max_wait_ms>0 read at depth 2
// is rejected like a granted cross-session read. The owner-session projection
// (the T11 advisory) is load-bearing: assessing a worker-owned runtime_lost
// delegate record against the root mis-reads resumability; it must be assessed
// against the worker.
func TestJobReadOutputDepth2Resolves(t *testing.T) {
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
	})

	// One-hop forwarding: each child forwards into its direct parent's store.
	coordJM.forward = rootJM.forwardEvent
	coordJM.parentJobID = "job_root_delegate_coord"
	workerJM.forward = coordJM.forwardEvent
	workerJM.parentJobID = "job_coord_delegate_worker"

	workerRec, err := workerJM.createShell(createShellOpts{Command: "sleep 1", Description: "worker job"})
	if err != nil {
		t.Fatalf("create worker shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, workerJM, workerRec.JobID) })

	workerJM.mu.Lock()
	run := workerJM.running[workerRec.JobID]
	workerJM.mu.Unlock()
	if run == nil || run.output == nil {
		t.Fatalf("worker job %q has no live output store", workerRec.JobID)
	}
	if _, err := workerJM.appendJobOutput(workerRec.JobID, run.output, []byte("worker grandchild line\n")); err != nil {
		t.Fatalf("append worker output: %v", err)
	}

	worker := &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil)}
	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	coordinator.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})

	// Snapshot read at depth 2: the root resolves root -> coordinator -> worker
	// and serves the worker's live bytes.
	out, err := jobReadOutputTool(context.Background(), root, map[string]any{
		"job_id":     workerRec.JobID,
		"tail_lines": 65536,
		"grep":       "grandchild",
	}, 20000)
	if err != nil {
		t.Fatalf("job_read_output(worker job) returned error: %v", err)
	}
	var read jobReadOutputTestResult
	if err := json.Unmarshal(handlerJSON(t, out), &read); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, out)
	}
	if read.JobID != workerRec.JobID || read.Status != string(jobstore.StatusRunning) {
		t.Fatalf("job_read_output = %+v, want running worker job %q", read, workerRec.JobID)
	}
	if !strings.Contains(read.Content, "worker grandchild line") {
		t.Fatalf("job_read_output content = %q, want worker grandchild bytes", read.Content)
	}
	if len(read.Matches) != 1 || !strings.Contains(read.Matches[0].Line, "worker grandchild line") {
		t.Fatalf("job_read_output matches = %+v, want grandchild grep match", read.Matches)
	}

	// The resolved owner is the worker, not the root or coordinator: projection
	// and the snapshot read both key on it.
	owner, ownerSess, ownerParent, rec, found := root.resolveDescendantJobOwner(workerRec.JobID)
	if !found || owner != workerJM || ownerSess != worker {
		t.Fatalf("resolveDescendantJobOwner(worker job) = (jm==worker:%v, sess==worker:%v, found:%v), want worker owner", owner == workerJM, ownerSess == worker, found)
	}
	// The owner's direct parent is the coordinator (single-hop forwarding lands
	// the forwarded copy there); the closed-store read fallback keys on it.
	if ownerParent != coordinator {
		t.Fatalf("resolveDescendantJobOwner owner parent = %p, want coordinator %p", ownerParent, coordinator)
	}
	if rec == nil || rec.OwnerSessionID != "WORK" {
		t.Fatalf("resolved record = %+v, want owner WORK", rec)
	}

	// max_wait_ms>0 at depth 2 is rejected like a granted cross-session read.
	_, err = jobReadOutputTool(context.Background(), root, map[string]any{
		"job_id":      workerRec.JobID,
		"max_wait_ms": 1000,
		"tail_lines":  65536,
	}, 20000)
	if err == nil || err.Error() != grantedReadBlockUnsupportedErr {
		t.Fatalf("depth-2 max_wait_ms>0 error = %v, want %q", err, grantedReadBlockUnsupportedErr)
	}

	// Owner-session projection (T11 advisory): a worker-owned runtime_lost
	// delegate record (descriptor.ParentSessionID == "WORK") must be assessed
	// against the worker. Projecting it against the root mis-reads the
	// parent-linkage gate, so the two projections must disagree on the reason.
	delegRec := workerOwnedRuntimeLostDelegate(t, workerJM, "WORK")
	viaOwner := projectJobRecord(worker, delegRec)
	viaRoot := projectJobRecord(root, delegRec)
	if viaOwner.NotResumableReason == nil || viaRoot.NotResumableReason == nil {
		t.Fatalf("expected non-resumable reasons (owner=%v root=%v)", viaOwner.NotResumableReason, viaRoot.NotResumableReason)
	}
	// Projecting against the root mis-reads the parent-linkage gate (root is not
	// the delegate's parent). Projecting against the true owner clears that gate
	// and reports a different, downstream reason.
	if *viaRoot.NotResumableReason != notResumableParentLinkageUnavailable {
		t.Fatalf("root projection reason = %q, want %q (wrong session rejected at the gate)", *viaRoot.NotResumableReason, notResumableParentLinkageUnavailable)
	}
	if *viaOwner.NotResumableReason == notResumableParentLinkageUnavailable {
		t.Fatalf("owner projection reason = %q, want owner to clear the parent-linkage gate; owner session is not load-bearing", *viaOwner.NotResumableReason)
	}
}

// workerOwnedRuntimeLostDelegate seeds a runtime_lost delegate record owned by
// the given session, with a descriptor whose ParentSessionID == ownerID. The
// parent-linkage gate in assessDelegateResumability keys on ParentSessionID ==
// the assessing session's ID, so the record only clears that gate when assessed
// against its true owner.
func workerOwnedRuntimeLostDelegate(t *testing.T, jm *jobManager, ownerID string) *jobstore.JobRecord {
	t.Helper()
	jobID := jobstore.NewJobID()
	now := time.Now().UTC()
	ref := encodeRef("", "WORKERCHILD")
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:          1,
		ChildSessionID:   "WORKERCHILD",
		TranscriptRef:    ref,
		ParentSessionID:  ownerID,
		ParentJobID:      jobID,
		OwnerSessionID:   ownerID,
		VisibleSessionID: ownerID,
		Task:             "worker delegate",
		WorkingDir:       t.TempDir(),
		LocalEnvPolicy:   "default",
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		Task:             desc.Task,
		OwnerSessionID:   ownerID,
		VisibleToSession: ownerID,
		StartedAt:        &now,
		TranscriptRef:    ref,
		DelegateRestore:  desc,
	}); err != nil {
		t.Fatalf("append worker delegate start: %v", err)
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          now,
		JobID:       jobID,
		Status:      jobstore.StatusStopped,
		Reason:      "runtime_lost",
		EndedAt:     &now,
		TerminalGen: jobstore.NewWatchGeneration(),
	}); err != nil {
		t.Fatalf("append worker delegate stopped: %v", err)
	}
	rec, err := findJobRecord(jm, jobID)
	if err != nil {
		t.Fatalf("load worker delegate record: %v", err)
	}
	return rec
}

// seedRunningDelegate installs a running delegate-typed job in jm carrying a
// transcript_ref so the stop cascade can resolve the job to its child session.
// The signal records how many times it fired (the stop path calls it). The job
// is durable-started and forwarded one hop like a real delegate start.
func seedRunningDelegate(t *testing.T, jm *jobManager, transcriptRef string, signals *atomic.Int32) string {
	t.Helper()
	startedAt := jm.now()
	jobID := jobstore.NewJobID()
	run := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:            jobID,
			Type:             jobstore.JobDelegate,
			Status:           jobstore.StatusRunning,
			Task:             "delegate worker",
			OwnerSessionID:   jm.sessionID,
			VisibleToSession: jm.sessionID,
			ParentJobID:      jm.currentParentJobID(),
			TranscriptRef:    transcriptRef,
			StartedAt:        startedAt,
			LastActivity:     &startedAt,
		},
		signal:         func() { signals.Add(1) },
		done:           make(chan struct{}),
		durableStarted: true,
	}
	started := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            jobID,
		Type:             run.rec.Type,
		Task:             run.rec.Task,
		OwnerSessionID:   run.rec.OwnerSessionID,
		VisibleToSession: run.rec.VisibleToSession,
		ParentJobID:      run.rec.ParentJobID,
		TranscriptRef:    transcriptRef,
		StartedAt:        &startedAt,
	}
	jm.mu.Lock()
	if err := jm.appendEvent(started); err != nil {
		jm.mu.Unlock()
		t.Fatalf("append delegate start: %v", err)
	}
	if err := jm.forwardLocked(started); err != nil {
		jm.mu.Unlock()
		t.Fatalf("forward delegate start: %v", err)
	}
	jm.running[jobID] = run
	jm.mu.Unlock()
	return jobID
}

// TestJobStopCascadesToWorkers drives a depth-3 live tree
// (root -> coordinator -> worker). The root owns the coordinator's delegate job;
// the coordinator owns a worker delegate job plus a shell job; the worker owns a
// shell job. job_stop on the coordinator's delegate must CASCADE: it stops the
// coordinator's own running jobs (the worker delegate + the coordinator shell)
// and recurses into the live subtree to stop the worker's shell job too — every
// job in the coordinator's subtree reaches terminal stopped_by_parent. Today the
// stop cancels only the coordinator's turn and the workers survive orphaned.
func TestJobStopCascadesToWorkers(t *testing.T) {
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
	})

	// One-hop forwarding: each child forwards into its direct parent's store.
	coordJM.forward = rootJM.forwardEvent
	workerJM.forward = coordJM.forwardEvent

	// Coordinator-owned worker delegate (its child session is WORK) and the
	// worker-delegate job becomes the parent job for WORK's forwarded jobs.
	var workerDelegateSignals atomic.Int32
	workerDelegateJobID := seedRunningDelegate(t, coordJM, encodeRef("", "WORK"), &workerDelegateSignals)
	workerJM.parentJobID = workerDelegateJobID

	// Worker-owned shell job that finalizes terminal when signalled.
	workerShellSE := newSignalCompletesStreamingExecutor()
	workerShell := runShell(context.Background(), workerJM, workerShellSE, shellArgs{Command: "sleep 30", Background: true})
	if workerShell.JobID == "" {
		t.Fatalf("worker runShell = %+v, want background job", workerShell)
	}

	// Coordinator-owned shell job, sibling of the worker delegate.
	coordShellSE := newSignalCompletesStreamingExecutor()
	coordShell := runShell(context.Background(), coordJM, coordShellSE, shellArgs{Command: "sleep 30", Background: true})
	if coordShell.JobID == "" {
		t.Fatalf("coordinator runShell = %+v, want background job", coordShell)
	}

	// Root-owned coordinator delegate (its child session is COORD); the
	// coordinator's jobs forward up through it.
	var coordDelegateSignals atomic.Int32
	coordDelegateJobID := seedRunningDelegate(t, rootJM, encodeRef("", "COORD"), &coordDelegateSignals)
	coordJM.parentJobID = coordDelegateJobID

	t.Cleanup(func() {
		workerShellSE.once.Do(func() { close(workerShellSE.done) })
		coordShellSE.once.Do(func() { close(coordShellSE.done) })
		waitForShellDone(t, workerJM, workerShell.JobID)
		waitForShellDone(t, coordJM, coordShell.JobID)
	})

	worker := &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil)}
	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	coordinator.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})

	out, err := jobStopTool(context.Background(), root, map[string]any{
		"job_id": coordDelegateJobID,
	}, 20000)
	if err != nil {
		t.Fatalf("job_stop coordinator delegate: %v", err)
	}
	var stop jobStopResult
	if err := json.Unmarshal(handlerJSON(t, out), &stop); err != nil {
		t.Fatalf("unmarshal job_stop: %v (output: %s)", err, out)
	}
	if stop.JobID != coordDelegateJobID {
		t.Fatalf("job_stop job_id = %q, want coordinator delegate %q", stop.JobID, coordDelegateJobID)
	}

	// The cascade signalled the coordinator's own running jobs: the worker
	// delegate and both shell jobs.
	if coordDelegateSignals.Load() != 1 {
		t.Fatalf("coordinator delegate signals = %d, want 1 (primary stop)", coordDelegateSignals.Load())
	}
	if workerDelegateSignals.Load() != 1 {
		t.Fatalf("worker delegate signals = %d, want 1 (cascade reached coordinator's worker delegate)", workerDelegateSignals.Load())
	}
	if coordShellSE.signals.Load() != 1 {
		t.Fatalf("coordinator shell signals = %d, want 1 (cascade reached coordinator's own shell)", coordShellSE.signals.Load())
	}
	if workerShellSE.signals.Load() != 1 {
		t.Fatalf("worker shell signals = %d, want 1 (cascade recursed into the worker subtree)", workerShellSE.signals.Load())
	}

	// The signalled shell jobs finalize terminal stopped_by_parent.
	workerShellSE.once.Do(func() { close(workerShellSE.done) })
	coordShellSE.once.Do(func() { close(coordShellSE.done) })
	waitForShellDone(t, workerJM, workerShell.JobID)
	waitForShellDone(t, coordJM, coordShell.JobID)

	workerShellRec, err := findJobRecord(workerJM, workerShell.JobID)
	if err != nil {
		t.Fatalf("load worker shell record: %v", err)
	}
	if workerShellRec.Status != jobstore.StatusCancelled || workerShellRec.Reason != "stopped_by_parent" {
		t.Fatalf("worker shell record = %+v, want cancelled/stopped_by_parent", workerShellRec)
	}
	coordShellRec, err := findJobRecord(coordJM, coordShell.JobID)
	if err != nil {
		t.Fatalf("load coordinator shell record: %v", err)
	}
	if coordShellRec.Status != jobstore.StatusCancelled || coordShellRec.Reason != "stopped_by_parent" {
		t.Fatalf("coordinator shell record = %+v, want cancelled/stopped_by_parent", coordShellRec)
	}
}

// TestJobStopTerminalDelegateCascadesToLiveWorkers: a job_stop on an
// ALREADY-TERMINAL coordinator delegate MUST still cascade into the
// coordinator's live subtree. A fire-and-return coordinator's OWN delegate job
// goes terminal (completed) while its workers keep running — the normal
// drive-down pattern — so job_stop must halt the live subtree regardless of the
// coordinator's own terminal status. The terminal record is returned unchanged
// (only the coordinator's own record; its live subtree is what gets stopped) and
// the coordinator's running shell is signalled exactly once by the cascade.
func TestJobStopTerminalDelegateCascadesToLiveWorkers(t *testing.T) {
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
	})

	// One-hop forwarding: the coordinator forwards into the root's store.
	coordJM.forward = rootJM.forwardEvent

	// A live coordinator-owned shell job that would finalize terminal if signalled.
	coordShellSE := newSignalCompletesStreamingExecutor()
	coordShell := runShell(context.Background(), coordJM, coordShellSE, shellArgs{Command: "sleep 30", Background: true})
	if coordShell.JobID == "" {
		t.Fatalf("coordinator runShell = %+v, want background job", coordShell)
	}

	// Root-owned coordinator delegate (its child session is COORD), seeded
	// already TERMINAL (completed). The coordinator's jobs forward up through it.
	coordDelegateJobID := jobstore.NewJobID()
	appendDelegateRecordForChild(t, rootJM, coordDelegateJobID, "COORD", jobstore.StatusCompleted, "exit_zero")
	coordJM.parentJobID = coordDelegateJobID

	t.Cleanup(func() {
		coordShellSE.once.Do(func() { close(coordShellSE.done) })
		waitForShellDone(t, coordJM, coordShell.JobID)
	})

	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})

	out, err := jobStopTool(context.Background(), root, map[string]any{
		"job_id": coordDelegateJobID,
	}, 20000)
	if err != nil {
		t.Fatalf("job_stop terminal coordinator delegate: %v", err)
	}
	var stop jobStopResult
	if err := json.Unmarshal(handlerJSON(t, out), &stop); err != nil {
		t.Fatalf("unmarshal job_stop: %v (output: %s)", err, out)
	}
	if stop.JobID != coordDelegateJobID || stop.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("job_stop = %+v, want terminal completed record returned unchanged", stop)
	}

	// The terminal coordinator delegate MUST cascade into its live subtree: the
	// coordinator's running shell is signalled exactly once by the cascade.
	if coordShellSE.signals.Load() != 1 {
		t.Fatalf("coordinator shell signals = %d, want 1: job_stop on a completed fire-and-return coordinator must still cascade-stop its live workers", coordShellSE.signals.Load())
	}
}

// TestJobStopStaleSupersededDelegateDoesNotCascade: a STALE, superseded
// delegate record (job J1) whose child session was RESUMED to a NEWER delegate
// job (J2, same child id) must NOT cascade-stop the child's CURRENT live work.
// The watch-resume path relinks the child to J2 (child.jobManager's current
// parent becomes J2) while the old terminal J1 record persists in the parent
// store. job_stop(J1) targets the job you named — a job that no longer owns the
// child's live runtime — so it must return the terminal J1 record cleanly
// WITHOUT signalling the child's current (J2-era) running shell.
func TestJobStopStaleSupersededDelegateDoesNotCascade(t *testing.T) {
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
	})

	// One-hop forwarding: the coordinator forwards into the root's store.
	coordJM.forward = rootJM.forwardEvent

	// A live coordinator-owned shell job that would finalize terminal if signalled.
	coordShellSE := newSignalCompletesStreamingExecutor()
	coordShell := runShell(context.Background(), coordJM, coordShellSE, shellArgs{Command: "sleep 30", Background: true})
	if coordShell.JobID == "" {
		t.Fatalf("coordinator runShell = %+v, want background job", coordShell)
	}

	// Root-owned coordinator delegate J1 (its child session is COORD), seeded
	// already TERMINAL (stopped) — the OLD, superseded delegate record.
	staleDelegateJobID := jobstore.NewJobID()
	appendDelegateRecordForChild(t, rootJM, staleDelegateJobID, "COORD", jobstore.StatusStopped, "runtime_lost")

	// The child was RESUMED: relinked to a NEWER delegate job J2. Its current
	// parent is J2, NOT the stale J1 record being stopped.
	currentDelegateJobID := jobstore.NewJobID()
	if currentDelegateJobID == staleDelegateJobID {
		t.Fatalf("J2 == J1 = %q, want distinct delegate job ids", currentDelegateJobID)
	}
	coordJM.setParentJobID(currentDelegateJobID)

	t.Cleanup(func() {
		coordShellSE.once.Do(func() { close(coordShellSE.done) })
		waitForShellDone(t, coordJM, coordShell.JobID)
	})

	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})

	out, err := jobStopTool(context.Background(), root, map[string]any{
		"job_id": staleDelegateJobID,
	}, 20000)
	if err != nil {
		t.Fatalf("job_stop stale superseded delegate: %v", err)
	}
	var stop jobStopResult
	if err := json.Unmarshal(handlerJSON(t, out), &stop); err != nil {
		t.Fatalf("unmarshal job_stop: %v (output: %s)", err, out)
	}
	if stop.JobID != staleDelegateJobID || stop.Status != string(jobstore.StatusStopped) {
		t.Fatalf("job_stop = %+v, want terminal stopped J1 record returned unchanged", stop)
	}

	// The stale J1 stop MUST NOT cascade into the child's CURRENT (J2-era) live
	// work: the coordinator's running shell is left untouched.
	if coordShellSE.signals.Load() != 0 {
		t.Fatalf("coordinator shell signals = %d, want 0: job_stop on a stale superseded delegate must NOT cascade-stop the resumed child's current live work", coordShellSE.signals.Load())
	}
}

// TestJobStopNonDirectDescendant: the root issues job_stop on a grandchild
// worker job (a non-direct descendant, two hops down, surfaced only as a
// forwarded copy in the root's store). The root cannot directly control a
// descendant it does not own; the call must fail not_controllable, naming the
// owning descendant session and the direct coordinator delegate the root CAN
// stop to cascade-stop the subtree.
func TestJobStopNonDirectDescendant(t *testing.T) {
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
	})

	coordJM.forward = rootJM.forwardEvent
	workerJM.forward = coordJM.forwardEvent

	var workerDelegateSignals atomic.Int32
	workerDelegateJobID := seedRunningDelegate(t, coordJM, encodeRef("", "WORK"), &workerDelegateSignals)
	workerJM.parentJobID = workerDelegateJobID

	workerShellSE := newSignalCompletesStreamingExecutor()
	workerShell := runShell(context.Background(), workerJM, workerShellSE, shellArgs{Command: "sleep 30", Background: true})
	if workerShell.JobID == "" {
		t.Fatalf("worker runShell = %+v, want background job", workerShell)
	}

	var coordDelegateSignals atomic.Int32
	coordDelegateJobID := seedRunningDelegate(t, rootJM, encodeRef("", "COORD"), &coordDelegateSignals)
	coordJM.parentJobID = coordDelegateJobID

	t.Cleanup(func() {
		workerShellSE.once.Do(func() { close(workerShellSE.done) })
		waitForShellDone(t, workerJM, workerShell.JobID)
	})

	worker := &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil)}
	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	coordinator.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})

	_, err := jobStopTool(context.Background(), root, map[string]any{
		"job_id": workerShell.JobID,
	}, 20000)
	if err == nil {
		t.Fatal("job_stop on a non-direct descendant succeeded, want not_controllable error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not_controllable:") {
		t.Fatalf("job_stop error = %q, want canonical not_controllable token", msg)
	}
	if !strings.Contains(msg, "COORD") {
		t.Fatalf("job_stop error = %q, want it to name the owning coordinator COORD", msg)
	}
	if !strings.Contains(msg, coordDelegateJobID) {
		t.Fatalf("job_stop error = %q, want it to name the controllable coordinator delegate %q", msg, coordDelegateJobID)
	}
	// The grandchild was NOT stopped: a non-direct descendant control attempt is
	// rejected, not silently routed.
	if workerShellSE.signals.Load() != 0 {
		t.Fatalf("worker shell signals = %d, want 0 (non-direct descendant must not be stopped)", workerShellSE.signals.Load())
	}
}

func assertJobForwardHookType(_ func(jobstore.Event) error) {}

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
