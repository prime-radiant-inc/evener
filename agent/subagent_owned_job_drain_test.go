package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

type ownedJobDrainEnvironment struct {
	execenv.ExecutionEnvironment
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	signaled    chan struct{}
	signalOnce  sync.Once
}

func newOwnedJobDrainEnvironment(dir string) *ownedJobDrainEnvironment {
	return &ownedJobDrainEnvironment{
		ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir),
		started:              make(chan struct{}),
		release:              make(chan struct{}),
		signaled:             make(chan struct{}),
	}
}

func (e *ownedJobDrainEnvironment) releaseJob() {
	e.releaseOnce.Do(func() { close(e.release) })
}

func (e *ownedJobDrainEnvironment) StreamCommand(_ context.Context, _, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	close(e.started)
	return &execenv.StreamHandle{
		Wait: func() (int, error) {
			<-e.release
			_, _ = out.Write([]byte("child shell complete"))
			return 0, nil
		},
		Signal: func() {
			e.signalOnce.Do(func() { close(e.signaled) })
			e.releaseJob()
		},
	}, nil
}

type ownedJobDrainClock struct {
	clock.Clock
	drainEntered chan struct{}
	drainOnce    sync.Once
	onDrainStop  func()
}

type ownedJobDrainTicker struct {
	clock.Ticker
	onStop func()
}

func (t *ownedJobDrainTicker) Stop() {
	if t.onStop != nil {
		t.onStop()
	}
	t.Ticker.Stop()
}

func (t *ownedJobDrainTicker) Reset(d time.Duration) { t.Ticker.Reset(d) }

func newOwnedJobDrainClock() *ownedJobDrainClock {
	return &ownedJobDrainClock{
		Clock:        clock.Real(),
		drainEntered: make(chan struct{}),
	}
}

func (c *ownedJobDrainClock) NewTicker(d time.Duration) clock.Ticker {
	ticker := c.Clock.NewTicker(d)
	if d == drainRecheckInterval {
		c.drainOnce.Do(func() { close(c.drainEntered) })
		return &ownedJobDrainTicker{
			Ticker: ticker,
			onStop: func() {
				if c.onDrainStop != nil {
					c.onDrainStop()
				}
			},
		}
	}
	return ticker
}

type ownedJobDrainFixture struct {
	parent       *Session
	child        *subagent
	env          *ownedJobDrainEnvironment
	adapter      *fakeAdapter
	freshHandled chan struct{}
	drainClock   *ownedJobDrainClock
	result       delegateResult
	runDone      <-chan struct{}
}

func newOwnedJobDrainFixture(t *testing.T) *ownedJobDrainFixture {
	t.Helper()

	idleReported := make(chan struct{})
	freshHandled := make(chan struct{})
	drainClock := newOwnedJobDrainClock()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:        "owned-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"owned shell","mode":"background"}`),
					Type:      "function",
				})
			},
			func(llm.Request) llm.Response {
				close(idleReported)
				return finalResponse("waiting for owned shell")
			},
			func(llm.Request) llm.Response {
				return finalResponse("owned shell handled")
			},
			func(llm.Request) llm.Response {
				close(freshHandled)
				return finalResponse("fresh notification handled")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	env := newOwnedJobDrainEnvironment(t.TempDir())
	parent, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		clock:            drainClock,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { parent.Close() })
	t.Cleanup(env.releaseJob)

	result := parent.createDelegate(context.Background(), delegateArgs{
		Task:       "run an owned background shell",
		Background: true,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	_, childID, err := decodeRef(result.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", result.TranscriptRef, err)
	}
	child := parent.subagents.get(childID)
	if child == nil {
		t.Fatalf("subagent %s not found", childID)
	}

	select {
	case <-env.started:
	case <-time.After(30 * time.Second):
		t.Fatal("child shell did not start")
	}
	select {
	case <-idleReported:
	case <-time.After(30 * time.Second):
		t.Fatal("child did not report its interim result")
	}
	select {
	case <-drainClock.drainEntered:
	case <-time.After(30 * time.Second):
		t.Fatal("subagent did not enter its owned job-tree drain")
	}

	parent.jobManager.mu.Lock()
	run := parent.jobManager.running[result.JobID]
	parent.jobManager.mu.Unlock()
	if run == nil {
		t.Fatalf("delegate job %s finalized before its owned shell drained", result.JobID)
	}

	return &ownedJobDrainFixture{
		parent:       parent,
		child:        child,
		env:          env,
		adapter:      adapter,
		freshHandled: freshHandled,
		drainClock:   drainClock,
		result:       result,
		runDone:      run.done,
	}
}

func TestSubagentDrainRestoresParentDriveCallbackForFreshChildNotification(t *testing.T) {
	fixture := newOwnedJobDrainFixture(t)
	fixture.env.releaseJob()
	select {
	case <-fixture.runDone:
	case <-time.After(30 * time.Second):
		t.Fatalf("delegate job %s did not finish after its owned shell exited", fixture.result.JobID)
	}
	fixture.requireHandledResult(t)

	enqueueCompletedDelegateNotification(t, fixture.child.sess, "fresh-child-job")
	fixture.child.sess.notify()
	select {
	case <-fixture.freshHandled:
	case <-time.After(30 * time.Second):
		t.Fatal("fresh child notification did not reach the restored parent drive callback")
	}
	requests := fixture.adapter.Requests()
	if len(requests) != 4 {
		t.Fatalf("provider requests = %d, want exactly one fresh notification turn after the drained run", len(requests))
	}
	if !requestsContain(requests[3:], "fresh-child-job") {
		t.Fatalf("fresh notification request did not contain the child job: %+v", requests[3:])
	}
}

func TestSubagentDrainRestoresParentDriveAfterTerminalStatePublication(t *testing.T) {
	fixture := newOwnedJobDrainFixture(t)
	fixture.drainClock.onDrainStop = func() {
		enqueueCompletedDelegateNotification(t, fixture.child.sess, "restore-order-job")
	}
	fixture.env.releaseJob()
	select {
	case <-fixture.freshHandled:
	case <-time.After(30 * time.Second):
		t.Fatal("notification queued at drain return did not drive after terminal state publication")
	}
	requests := fixture.adapter.Requests()
	if len(requests) != 4 {
		t.Fatalf("provider requests = %d, want exactly one post-drain drive turn", len(requests))
	}
	if !requestsContain(requests[3:], "restore-order-job") {
		t.Fatalf("post-drain drive request did not contain the queued notification: %+v", requests[3:])
	}
}

func TestSubagentFatalRunStopsOwnedShellAndGatesNotificationDrive(t *testing.T) {
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "provider failed after shell launch", nil, nil)
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return toolCallResponse(llm.ToolCallData{
					ID:        "fatal-owned-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"fatal owned shell","mode":"background"}`),
					Type:      "function",
				}), nil
			},
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, fatalErr
			},
			func(llm.Request) (llm.Response, error) {
				return finalResponse("explicit resume succeeded"), nil
			},
			func(llm.Request) (llm.Response, error) {
				return finalResponse("stopped shell notification handled"), nil
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	env := newOwnedJobDrainEnvironment(t.TempDir())
	parent, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { parent.Close() })
	t.Cleanup(env.releaseJob)

	result := parent.createDelegate(context.Background(), delegateArgs{
		Task:       "fail after launching an owned shell",
		Background: true,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	_, childID, err := decodeRef(result.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", result.TranscriptRef, err)
	}
	child := parent.subagents.get(childID)
	if child == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	select {
	case <-env.started:
	case <-time.After(30 * time.Second):
		t.Fatal("child shell did not start")
	}

	parent.jobManager.mu.Lock()
	run := parent.jobManager.running[result.JobID]
	parent.jobManager.mu.Unlock()
	if run == nil {
		t.Fatalf("delegate job %s finalized before fatal child run completed", result.JobID)
	}
	select {
	case <-run.done:
	case <-time.After(30 * time.Second):
		t.Fatal("parent delegate did not finalize after fatal child run")
	}
	select {
	case <-env.signaled:
	case <-time.After(30 * time.Second):
		t.Fatal("fatal child run did not signal its owned shell")
	}

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider requests after fatal run = %d, want exactly shell launch and fatal model turn", len(requests))
	}
	child.mu.Lock()
	status := child.status
	childErr := child.err
	fatalRunGated := child.fatalRunGated
	childDone := child.done
	child.mu.Unlock()
	if status != SubagentFailed || !errors.Is(childErr, fatalErr) {
		t.Fatalf("child terminal state = %s, err %v; want failed with original provider error", status, childErr)
	}
	if !fatalRunGated {
		t.Fatal("fatal child run did not retain its automatic-drive gate")
	}
	select {
	case <-childDone:
	default:
		t.Fatal("child done channel remained open after parent delegate finalized")
	}
	rec := loadShellRecord(t, parent.jobManager, result.JobID)
	if rec.Status != jobstore.StatusFailed || rec.Reason == "stopped_by_parent" {
		t.Fatalf("delegate record = status %s reason %q, want failed without stopped_by_parent", rec.Status, rec.Reason)
	}
	stored, _, _, err := parent.jobManager.readOutput(result.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read delegate output: %v", err)
	}
	if !strings.Contains(stored, fatalErr.Error()) {
		t.Fatalf("delegate output = %q, want original fatal error", stored)
	}

	if _, err := parent.sendInput(context.Background(), child.id, "resume after fatal run"); err != nil {
		t.Fatalf("explicit child resume: %v", err)
	}
	child.mu.Lock()
	resumedDone := child.done
	child.mu.Unlock()
	select {
	case <-resumedDone:
	case <-time.After(30 * time.Second):
		t.Fatal("explicit child resume did not finish")
	}
	child.mu.Lock()
	resumedStatus := child.status
	resumedGate := child.fatalRunGated
	child.mu.Unlock()
	if resumedStatus != SubagentCompleted || resumedGate {
		t.Fatalf("child after explicit resume = status %s fatal gate %v; want completed/false", resumedStatus, resumedGate)
	}
}

func TestSubagentFatalDriveTurnStopsOwnedShellAndSuppressesRedrive(t *testing.T) {
	driveErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal notification turn", nil, nil)
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return finalResponse("idle before notification"), nil
			},
			func(llm.Request) (llm.Response, error) {
				return toolCallResponse(llm.ToolCallData{
					ID:        "fatal-drive-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"fatal drive shell","mode":"background"}`),
					Type:      "function",
				}), nil
			},
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, driveErr
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	env := newOwnedJobDrainEnvironment(t.TempDir())
	parent, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { parent.Close() })
	t.Cleanup(env.releaseJob)

	result := parent.createDelegate(context.Background(), delegateArgs{
		Task:       "idle before a fatal notification turn",
		Background: true,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	waitForShellDone(t, parent.jobManager, result.JobID)
	_, childID, err := decodeRef(result.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", result.TranscriptRef, err)
	}
	child := parent.subagents.get(childID)
	if child == nil || child.sess == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	child.mu.Lock()
	statusBeforeDrive := child.status
	resultBeforeDrive := child.result
	errBeforeDrive := child.err
	doneBeforeDrive := child.done
	child.mu.Unlock()
	enqueueCompletedDelegateNotification(t, child.sess, "fatal-drive-notification")
	child.sess.notify()
	select {
	case <-env.started:
	case <-time.After(30 * time.Second):
		t.Fatal("automatic notification drive did not launch its owned shell")
	}
	select {
	case <-env.signaled:
	case <-time.After(30 * time.Second):
		t.Fatal("fatal automatic notification drive did not stop its owned shell")
	}
	waitForCondition(t, 5*time.Second, "stopped shell notification to remain gated", func() bool {
		child.mu.Lock()
		driving := child.driving
		child.mu.Unlock()
		return !driving && child.sess.peekNotifications() > 0
	})
	child.sess.notify()
	requests := adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("provider requests after fatal drive = %d, want initial, shell launch, and fatal turns only", len(requests))
	}
	if !child.fatalRunGatedSnapshot() {
		t.Fatal("fatal notification drive did not retain the automatic-drive gate")
	}
	child.mu.Lock()
	statusAfterDrive := child.status
	resultAfterDrive := child.result
	errAfterDrive := child.err
	doneAfterDrive := child.done
	child.mu.Unlock()
	if statusAfterDrive != statusBeforeDrive || resultAfterDrive != resultBeforeDrive || !errors.Is(errAfterDrive, errBeforeDrive) || doneAfterDrive != doneBeforeDrive {
		t.Fatalf("fatal drive changed retained state from (%s, %q, %v, %p) to (%s, %q, %v, %p)",
			statusBeforeDrive, resultBeforeDrive, errBeforeDrive, doneBeforeDrive,
			statusAfterDrive, resultAfterDrive, errAfterDrive, doneAfterDrive)
	}
}

func (f *ownedJobDrainFixture) releaseAndWait(t *testing.T) {
	t.Helper()
	f.env.releaseJob()
	select {
	case <-f.runDone:
	case <-time.After(30 * time.Second):
		t.Fatalf("delegate job %s did not finish after its owned shell exited", f.result.JobID)
	}
}

func (f *ownedJobDrainFixture) requireHandledResult(t *testing.T) {
	t.Helper()

	requests := f.adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("provider requests = %d, want initial, interim, and shell-notification turns", len(requests))
	}
	if !requestsContain(requests[2:], "child shell complete") {
		t.Fatalf("final provider request did not contain the shell notification: %+v", requests[2:])
	}

	f.child.mu.Lock()
	result := f.child.result
	status := f.child.status
	f.child.mu.Unlock()
	if status != SubagentCompleted || result != "owned shell handled" {
		t.Fatalf("child terminal state = %s, result %q; want completed with drained result", status, result)
	}

	stored, _, _, err := f.parent.jobManager.readOutput(f.result.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read delegate output: %v", err)
	}
	if strings.TrimSpace(stored) != "owned shell handled" {
		t.Fatalf("stored delegate output = %q, want drained result", stored)
	}
	rec := loadShellRecord(t, f.parent.jobManager, f.result.JobID)
	if rec.Status != jobstore.StatusCompleted || rec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("delegate record = status %s notification %s, want completed/pending", rec.Status, rec.NotifyState)
	}
	if got := f.parent.peekNotifications(); got != 1 {
		t.Fatalf("parent pending notifications = %d, want one delegate terminal notification", got)
	}
}

func TestSubagentRunDrainsOwnedShellBeforeFinalizingDelegate(t *testing.T) {
	fixture := newOwnedJobDrainFixture(t)

	fixture.child.mu.Lock()
	running := fixture.child.running
	status := fixture.child.status
	done := fixture.child.done
	fixture.child.mu.Unlock()
	if !running || status != SubagentRunning {
		t.Fatalf("child state while owned shell runs = running %v, status %s; want true/running", running, status)
	}
	fixture.parent.jobManager.mu.Lock()
	parentRun := fixture.parent.jobManager.running[fixture.result.JobID]
	fixture.parent.jobManager.mu.Unlock()
	if parentRun == nil {
		t.Fatalf("parent delegate job %s was finalized while its child shell was running", fixture.result.JobID)
	}
	select {
	case <-done:
		t.Fatal("subagent done channel closed while its owned shell was running")
	default:
	}

	fixture.releaseAndWait(t)
	fixture.requireHandledResult(t)
}

func TestRetentionDoesNotReclaimSubagentDrainingOwnedWork(t *testing.T) {
	fixture := newOwnedJobDrainFixture(t)

	terminalSession := newSession(t, withConfig(SessionConfig{NoProjectPrompts: true}))
	endedAt := time.Unix(1, 0).UTC()
	terminalDone := make(chan struct{})
	close(terminalDone)
	terminal := &subagent{
		id:             terminalSession.ID(),
		sess:           terminalSession,
		status:         SubagentCompleted,
		done:           terminalDone,
		resultConsumed: true,
		endedAt:        &endedAt,
	}
	fixture.parent.subagents.track(terminal)
	fixture.parent.subagents.mu.Lock()
	fixture.parent.subagents.maxRetainedTerminal = 1
	fixture.parent.subagents.mu.Unlock()

	evicted, err := fixture.parent.subagents.reserveSlot()
	if err != nil {
		t.Fatalf("reserveSlot: %v", err)
	}
	if len(evicted) != 1 || evicted[0] != terminal {
		t.Fatalf("evicted = %+v, want only consumed terminal child %s", evicted, terminal.id)
	}
	if got := fixture.parent.subagents.get(fixture.child.id); got != fixture.child {
		t.Fatalf("draining child %s was reclaimed under retention pressure", fixture.child.id)
	}
	fixture.child.mu.Lock()
	childRunning := fixture.child.running
	childStatus := fixture.child.status
	childClosed := fixture.child.closed
	fixture.child.mu.Unlock()
	if !childRunning || childStatus != SubagentRunning || childClosed {
		t.Fatalf("draining child = running %v, status %s, closed %v; want true/running/false", childRunning, childStatus, childClosed)
	}

	for _, sub := range evicted {
		sub.sess.Close()
	}
	fixture.releaseAndWait(t)
	fixture.requireHandledResult(t)
}
