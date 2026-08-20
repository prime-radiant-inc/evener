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

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/llm"
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
	shellHandled chan struct{}
	freshHandled chan struct{}
	drainClock   *ownedJobDrainClock
	result       delegateResult
	shellJobID   string
	runDone      <-chan struct{}
}

func newOwnedJobDrainFixture(t *testing.T) *ownedJobDrainFixture {
	t.Helper()

	idleReported := make(chan struct{})
	shellHandled := make(chan struct{})
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
				close(shellHandled)
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
		Task: "run an owned background shell",
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
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("child shell did not start")
	}
	select {
	case <-idleReported:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("child did not report its interim result")
	}
	select {
	case <-drainClock.drainEntered:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("subagent did not enter its owned job-tree drain")
	}

	shellJobID := liveOwnedShellJobID(t, child.sess.jobManager)
	child.mu.Lock()
	runDone := child.done
	child.mu.Unlock()

	return &ownedJobDrainFixture{
		parent:       parent,
		child:        child,
		env:          env,
		adapter:      adapter,
		shellHandled: shellHandled,
		freshHandled: freshHandled,
		drainClock:   drainClock,
		result:       result,
		shellJobID:   shellJobID,
		runDone:      runDone,
	}
}

func liveOwnedShellJobID(t *testing.T, manager *jobManager) string {
	t.Helper()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	var shellJobID string
	for jobID, run := range manager.running {
		if run == nil || run.rec == nil || run.rec.Type != jobstore.JobShell {
			continue
		}
		if shellJobID != "" {
			t.Fatalf("multiple live child-owned shell jobs: %q and %q", shellJobID, jobID)
		}
		shellJobID = jobID
	}
	if shellJobID == "" {
		t.Fatal("registered child has no live owned shell job")
	}
	return shellJobID
}

func loadStableDelegateTerminalPacket(t *testing.T, owner *Session, delegateID string) delegatestore.TerminalPacket {
	t.Helper()
	events, err := owner.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load stable delegate events: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID != delegateID || events[i].TerminalPrepared == nil {
			continue
		}
		value := cloneDelegateTerminalPacket(events[i].TerminalPrepared.Packet)
		packet = &value
	}
	if packet == nil {
		t.Fatalf("stable delegate %s has no canonical terminal packet", delegateID)
	}
	return *packet
}

func TestSubagentDrainRestoresParentDriveCallbackForFreshChildNotification(t *testing.T) {
	fixture := newOwnedJobDrainFixture(t)
	fixture.releaseAndWait(t)
	fixture.requireHandledResult(t, 3, "owned shell handled")

	if err := armOwnedJobDrainAttention(fixture.child.sess, "delegate:fresh-child-job", "fresh-child-job"); err != nil {
		t.Fatalf("arm fresh child attention: %v", err)
	}
	select {
	case <-fixture.freshHandled:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
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
	var armOnce sync.Once
	fixture.drainClock.onDrainStop = func() {
		armOnce.Do(func() {
			if err := armOwnedJobDrainAttention(fixture.child.sess, "delegate:restore-order-job", "restore-order-job"); err != nil {
				t.Errorf("arm drain-return attention: %v", err)
			}
		})
	}
	fixture.env.releaseJob()
	select {
	case <-fixture.freshHandled:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
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

func TestSubagentOwnedJobDrainAcceptsStableSteeringWithoutBlockingNotifications(t *testing.T) {
	fixture := newOwnedJobDrainFixture(t)
	fixture.child.mu.Lock()
	finalizing := fixture.child.finalizing
	running := fixture.child.running
	fixture.child.mu.Unlock()
	if !running || !finalizing {
		t.Fatalf("active drain state = running %v finalizing %v, want running finalization gate", running, finalizing)
	}

	plain := (delegateRuntime{owner: fixture.parent}).send(context.Background(), fixture.result.DelegateID, "plain send during owned-job drain", 0).result
	if plain.Err != nil || plain.Action != "steered" {
		t.Fatalf("plain delegate_send during owned-job drain = %+v, want durable steer", plain)
	}
	second := (delegateRuntime{owner: fixture.parent}).send(context.Background(), fixture.result.DelegateID, "second send during owned-job drain", 0).result
	if second.Err != nil || second.Action != "steered" {
		t.Fatalf("second delegate_send during owned-job drain = %+v, want durable steer", second)
	}
	if queue := fixture.child.sess.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("legacy steering queue during stable owned-job drain = %+v, want empty", queue)
	}

	fixture.releaseAndWait(t)
	requests := fixture.adapter.Requests()
	shellSeen := len(requests) == 4 && requestsContain(requests[2:3], "child shell complete")
	plainSeen := len(requests) == 4 && requestsContain(requests[2:3], "plain send during owned-job drain")
	secondSeen := len(requests) == 4 && requestsContain(requests[2:3], "second send during owned-job drain")
	if !shellSeen || !plainSeen || !secondSeen {
		t.Fatalf("stable drain/steering requests = count %d shell %t plain %t second %t, want the shell notification and both accepted steers at the next model boundary", len(requests), shellSeen, plainSeen, secondSeen)
	}
	fixture.requireHandledResult(t, 4, "fresh notification handled")
	fixture.child.mu.Lock()
	finalizing = fixture.child.finalizing
	fixture.child.mu.Unlock()
	if finalizing {
		t.Fatal("finalization gate remained set after owned-job drain published terminal state")
	}
}

func TestSubagentDrainReturnHandoffPreservesStableSteeringBeforeTerminalPublication(t *testing.T) {
	requireDrainReturnHandoffMergesSteering(t, newOwnedJobDrainFixture(t))
}

// TestSubagentDrainReturnHandoffNoPhantomTurnInFinalizationWindow is #243's
// CI flake made deterministic: it holds armFinalizedJob's
// NotifyPending→NotifyDelivered window open across multiple drain recheck
// passes, so the drain loop's own kick → rematerializeDurablePendings provably
// runs against the transiently-NotifyPending shell record with an empty
// in-memory queue. The pre-#237 rematerialize re-enqueued that record, and the
// drain ran a phantom notification turn BEFORE the handoff sends landed — the
// phantom stole the continuation's slot (CI observed 12 provider requests with
// none of the accepted sends in it). With the running-map skip the drain
// cannot see the mid-finalization record at all, and the handoff below must
// merge every accepted send exactly as in the unperturbed test.
func TestSubagentDrainReturnHandoffNoPhantomTurnInFinalizationWindow(t *testing.T) {
	fixture := newOwnedJobDrainFixture(t)
	// Hold the window open until the drain loop's own recheck has provably run
	// rematerializeDurablePendings inside it: windowHeld blocks the
	// finalization goroutine between the NotifyPending append and the
	// NotifyDelivered transition, and the rematerialize-load hook — wired to
	// the drain's 250ms recheck kick — proves the pass happened rather than
	// assuming it from wall time.
	windowHeld := make(chan struct{})
	recheckSawWindow := make(chan struct{})
	releaseWindow := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseWindow) }) })
	var pendingOnce, recheckOnce sync.Once
	fixture.child.sess.jobManager.testOnlyAfterNotifyPendingAppend = func(string) {
		pendingOnce.Do(func() {
			close(windowHeld)
			<-releaseWindow
		})
	}
	fixture.child.sess.jobManager.testOnlyAfterRematerializeDurableLoad = func() {
		select {
		case <-windowHeld:
			recheckOnce.Do(func() { close(recheckSawWindow) })
		default:
		}
	}
	go func() {
		select {
		case <-recheckSawWindow:
			releaseOnce.Do(func() { close(releaseWindow) })
		case <-time.After(30 * time.Second): // TRIPWIRE: the drain's 250ms recheck fires rematerialize inside the window within one interval; 30s only fires on a genuine hang.
		}
	}()
	requireDrainReturnHandoffMergesSteering(t, fixture)
}

func requireDrainReturnHandoffMergesSteering(t *testing.T, fixture *ownedJobDrainFixture) {
	t.Helper()
	type handoffResult struct {
		finalizing bool
		running    bool
		resume     sendMessageResult
		plain      sendMessageResult
		second     sendMessageResult
		queue      []SteeringEntry
		pending    int
	}
	handoffDone := make(chan handoffResult, 1)
	var handoffOnce sync.Once
	fixture.drainClock.onDrainStop = func() {
		handoffOnce.Do(func() {
			fixture.child.mu.Lock()
			finalizing := fixture.child.finalizing
			running := fixture.child.running
			fixture.child.mu.Unlock()
			runtime := delegateRuntime{owner: fixture.parent}
			resume := runtime.send(context.Background(), fixture.result.DelegateID, "resume during drain return", 0).result
			plain := runtime.send(context.Background(), fixture.result.DelegateID, "plain send during drain return", 0).result
			second := runtime.send(context.Background(), fixture.result.DelegateID, "second send during drain return", 0).result
			handoffDone <- handoffResult{
				finalizing: finalizing,
				running:    running,
				resume:     resume,
				plain:      plain,
				second:     second,
				queue:      fixture.child.sess.SteeringQueueSnapshot(),
				pending:    fixture.child.sess.peekNotifications(),
			}
		})
	}

	fixture.env.releaseJob()
	var got handoffResult
	select {
	case got = <-handoffDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("subagent did not reach the drain-return handoff")
	}
	if !got.running || !got.finalizing {
		t.Fatalf("drain-return state = running %v finalizing %v, want running finalization gate", got.running, got.finalizing)
	}
	if got.resume.Err != nil || got.resume.Action != "steered" {
		t.Fatalf("first stable send during drain return = %+v, want durable steer", got.resume)
	}
	if got.plain.Err != nil || got.plain.Action != "steered" {
		t.Fatalf("plain delegate_send during drain return = %+v, want durable steer", got.plain)
	}
	if got.second.Err != nil || got.second.Action != "steered" {
		t.Fatalf("second delegate_send during drain return = %+v, want durable steer", got.second)
	}
	if len(got.queue) != 0 {
		t.Fatalf("steering queue after drain-return sends = %+v, want empty", got.queue)
	}
	if got.pending != 0 {
		t.Fatalf("notifications during drain-return sends = %d, want none", got.pending)
	}

	select {
	case <-fixture.runDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("delegate did not publish terminal state after drain return")
	}
	waitForOwnedJobDrainGeneration(t, fixture.child, fixture.shellHandled, "drain-return continuation")
	requests := fixture.adapter.Requests()
	resumeSeen := len(requests) >= 3 && requestsContain(requests[2:3], "resume during drain return")
	plainSeen := len(requests) >= 3 && requestsContain(requests[2:3], "plain send during drain return")
	secondSeen := len(requests) >= 3 && requestsContain(requests[2:3], "second send during drain return")
	if len(requests) != 4 || !resumeSeen || !plainSeen || !secondSeen {
		t.Fatalf("drain-return stable steering requests = count %d resume %t plain %t second %t, want one continuation containing all accepted sends", len(requests), resumeSeen, plainSeen, secondSeen)
	}
	waitForOwnedJobDrainGeneration(t, fixture.child, fixture.freshHandled, "drain-return shell attention generation")
}

func TestSubagentRunPreservesStructuredResultAcrossLateNotification(t *testing.T) {
	releaseInitial := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseInitial) }) })
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				<-releaseInitial
				return communicateWithStructured("original delegate result", map[string]any{
					"message": "original delegate result",
					"summary": "original structured result",
				})
			},
			func(llm.Request) llm.Response {
				return communicateWithStructured("late notification handled", map[string]any{
					"message": "late notification handled",
					"summary": "late structured overwrite",
				})
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	var child *subagent
	parent, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			subagentAfterFinalStatePublish: func(got *subagent) {
				if got != child {
					t.Errorf("finalization hook child = %p, want %p", got, child)
				}
				enqueueCompletedJobNotification(t, got.sess, "late-structured-job")
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { parent.Close() })

	result := parent.createDelegate(context.Background(), delegateArgs{
		Task: "return structured output before a late notification",
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
				"summary": map[string]any{"type": "string"},
			},
			"required": []string{"message", "summary"},
		},
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	_, childID, err := decodeRef(result.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", result.TranscriptRef, err)
	}
	child = parent.subagents.get(childID)
	if child == nil {
		t.Fatalf("subagent %s not found", childID)
	}

	lateDriveDone := make(chan error, 1)
	child.sess.SetNotifyFunc(func() {
		_, driveErr := child.sess.ProcessInputKind(context.Background(), "", nil, EntryNotification)
		lateDriveDone <- driveErr
	})
	child.mu.Lock()
	runDone := child.done
	child.mu.Unlock()
	releaseOnce.Do(func() { close(releaseInitial) })

	select {
	case <-runDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("stable delegate did not publish its original structured result")
	}
	select {
	case driveErr := <-lateDriveDone:
		if driveErr != nil {
			t.Fatalf("late notification drive: %v", driveErr)
		}
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("late notification drive did not finish")
	}
	liveStructured, ok := child.sess.CommunicateStructured().(map[string]any)
	if !ok || liveStructured["summary"] != "late structured overwrite" {
		t.Fatalf("live child structured result = %#v, want the late notification overwrite", child.sess.CommunicateStructured())
	}
	packet := loadStableDelegateTerminalPacket(t, parent, result.DelegateID)
	var structured map[string]any
	err = json.Unmarshal(packet.StructuredResult, &structured)
	if err != nil || structured["summary"] != "original structured result" {
		t.Fatalf("durable stable structured result = %#v (error %v), want original run snapshot", structured, err)
	}
}

func TestSubagentFinalizationRefusesResumeAndDriveUntilCallbackRestored(t *testing.T) {
	// All three resume/deliver entry points converge on a single code path:
	// delegateRuntime.send (explicit resume, delegate_send, and watch-origin
	// stable send all route through Steer/ReserveStart/CommitStart). There is no
	// distinct entry point to exercise, so the test runs a single case against
	// that shared path rather than three identical copy-pasted iterations.
	fixture := newOwnedJobDrainFixture(t)
	finalizationEntered := make(chan struct{})
	finalizationRelease := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(finalizationRelease) }) })
	var finalizationOnce sync.Once
	fixture.child.sess.cfg.testOnly.subagentAfterFinalStatePublish = func(got *subagent) {
		finalizationOnce.Do(func() {
			if got != fixture.child {
				t.Errorf("finalization hook child = %p, want %p", got, fixture.child)
			}
			close(finalizationEntered)
			<-finalizationRelease
		})
	}
	var armOnce sync.Once
	fixture.drainClock.onDrainStop = func() {
		armOnce.Do(func() {
			if err := armOwnedJobDrainAttention(fixture.child.sess, "delegate:finalization-window-job", "finalization-window-job"); err != nil {
				t.Errorf("arm finalization-window attention: %v", err)
			}
		})
	}
	fixture.env.releaseJob()
	select {
	case <-finalizationEntered:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("subagent did not reach the post-publication finalization window")
	}

	fixture.child.mu.Lock()
	running := fixture.child.running
	fixture.child.mu.Unlock()
	if running {
		t.Fatal("finalization hook ran before running=false was published")
	}
	if fixture.parent.driveSubagentNotificationTurn(fixture.child) {
		t.Fatal("automatic notification drive launched during callback restoration")
	}

	// The shared send path (used by explicit resume, delegate_send, and
	// watch-origin stable send) must refuse the message while the finalization
	// callback holds the delegate, returning a retryable target_busy error.
	res := (delegateRuntime{owner: fixture.parent}).send(context.Background(), fixture.result.DelegateID, "resume too early", 0).result
	if !errors.Is(res.Err, errDelegateTargetBusy) {
		t.Fatalf("early send during finalization = %+v, want target_busy", res)
	}
	if queue := fixture.child.sess.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("child steering queue during finalization = %+v, want no delivery", queue)
	}
	if requests := fixture.adapter.Requests(); len(requests) != 2 {
		t.Fatalf("provider requests during finalization = %d, want no resumed or automatic turn", len(requests))
	}

	releaseOnce.Do(func() { close(finalizationRelease) })
	waitForOwnedJobDrainGeneration(t, fixture.child, fixture.freshHandled, "post-finalization attention generation")
	select {
	case <-fixture.runDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("delegate did not finish after callback restoration")
	}
	fixture.child.sess.cfg.testOnly.subagentAfterFinalStatePublish = nil
	resumed := (delegateRuntime{owner: fixture.parent}).send(context.Background(), fixture.result.DelegateID, "resume after finalization", 0).result
	if resumed.Err != nil || resumed.Action != "started" {
		t.Fatalf("explicit resume after finalization = %+v, want started", resumed)
	}
	fixture.child.mu.Lock()
	resumedDone := fixture.child.done
	fixture.child.mu.Unlock()
	select {
	case <-resumedDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("explicit resume after finalization did not finish")
	}
}

func TestSubagentFatalRunStopsOwnedShellAndGatesNotificationDrive(t *testing.T) {
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "provider failed after shell launch", nil, nil)
	enteredFatal := make(chan struct{})
	releaseFatal := make(chan struct{})
	stoppedShellHandled := make(chan struct{})
	var releaseFatalOnce sync.Once
	t.Cleanup(func() { releaseFatalOnce.Do(func() { close(releaseFatal) }) })
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
				close(enteredFatal)
				<-releaseFatal
				return llm.Response{}, fatalErr
			},
			func(llm.Request) (llm.Response, error) {
				return finalResponse("explicit resume succeeded"), nil
			},
			func(llm.Request) (llm.Response, error) {
				close(stoppedShellHandled)
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
		Task: "fail after launching an owned shell",
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
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("child shell did not start")
	}
	<-enteredFatal
	shellJobID := liveOwnedShellJobID(t, child.sess.jobManager)
	child.mu.Lock()
	childDone := child.done
	child.mu.Unlock()
	releaseFatalOnce.Do(func() { close(releaseFatal) })
	select {
	case <-childDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("stable delegate did not finalize after fatal child run")
	}
	select {
	case <-env.signaled:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
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
	rec := loadShellRecord(t, child.sess.jobManager, shellJobID)
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("child-owned shell record = status %s reason %q, want cancelled/stopped_by_parent", rec.Status, rec.Reason)
	}
	aggregate := delegateAggregateSnapshot(t, parent.delegateController, result.DelegateID)
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "failed" {
		t.Fatalf("stable delegate outcome = %#v, want failed", aggregate.LatestOutcome)
	}
	packet := loadStableDelegateTerminalPacket(t, parent, result.DelegateID)
	var message string
	if err := json.Unmarshal(packet.Message, &message); err != nil || !strings.Contains(message, fatalErr.Error()) {
		t.Fatalf("stable terminal message = %q (error %v), want original fatal error", message, err)
	}

	if parent.driveSubagentNotificationTurn(child) {
		t.Fatal("fatal-gated child admitted an automatic notification drive")
	}
	if requests := adapter.Requests(); len(requests) != 2 {
		t.Fatalf("provider requests after refused automatic drive = %d, want 2", len(requests))
	}
	explicitResume := (delegateRuntime{owner: parent}).send(context.Background(), result.DelegateID, "resume after fatal run", 0).result
	if explicitResume.Err != nil || explicitResume.Action != "started" {
		t.Fatalf("explicit child resume = %+v, want started", explicitResume)
	}
	child.mu.Lock()
	resumedDone := child.done
	child.mu.Unlock()
	select {
	case <-resumedDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("explicit child resume did not finish")
	}
	waitForOwnedJobDrainGeneration(t, child, stoppedShellHandled, "stopped-shell attention generation")
	child.mu.Lock()
	resumedStatus := child.status
	resumedGate := child.fatalRunGated
	child.mu.Unlock()
	if resumedStatus != SubagentCompleted || resumedGate {
		t.Fatalf("child after explicit resume = status %s fatal gate %v; want completed/false", resumedStatus, resumedGate)
	}
}

func TestIdleFatalGatedWatchSendDropsAndDoesNotPinDrain(t *testing.T) {
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal before watch delivery", nil, nil)
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, fatalErr
			},
			func(llm.Request) (llm.Response, error) {
				return finalResponse("failed delegate notification handled"), nil
			},
			func(llm.Request) (llm.Response, error) {
				return finalResponse("dropped watch diagnostic handled"), nil
			},
			func(llm.Request) (llm.Response, error) {
				return finalResponse("explicit recovery completed"), nil
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	parent, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
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

	result := parent.createDelegate(context.Background(), delegateArgs{
		Task: "fail before a persisted watch send",
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
	child.mu.Lock()
	runDone := child.done
	child.mu.Unlock()
	select {
	case <-runDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("stable delegate did not settle its fatal initial run")
	}
	if !child.fatalRunGatedSnapshot() {
		t.Fatalf("retained child %q is not fatal-gated", childID)
	}
	if _, err := parent.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("handle failed delegate notification: %v", err)
	}

	now := parent.jobManager.now()
	for _, event := range restoredWatchSendPendingEvents(parent.ID(), "job_observed", result.DelegateID, now) {
		if err := parent.jobManager.appendEvent(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	if err := parent.jobManager.restoreWatchSendPending(); err != nil {
		t.Fatalf("restore persisted watch send: %v", err)
	}
	if pending := loadWatchSendRecord(t, parent.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("persisted watch sends before drain = %+v, want one", pending)
	}

	recheck := make(chan time.Time)
	drainCtx, cancelDrain := context.WithCancel(context.Background())
	t.Cleanup(cancelDrain)
	type drainResult struct {
		output string
		err    error
	}
	drainDone := make(chan drainResult, 1)
	go func() {
		output, err := parent.drainJobTree(drainCtx, recheck)
		drainDone <- drainResult{output: output, err: err}
	}()
	select {
	case got := <-drainDone:
		if got.err != nil {
			t.Fatalf("DrainJobTree: %v", got.err)
		}
		requests := adapter.Requests()
		// One notification per turn, in order: request 2 handles the fatal
		// delegate terminal_error, request 3 handles the dropped-watch
		// diagnostic.
		//
		// Each assertion is scoped to the request that handles its notification
		// because requestsContain scans a request's ENTIRE message list, and
		// request 3 carries request 2 as transcript history. An assertion aimed
		// at request 3 is therefore satisfied by anything either turn saw, and
		// cannot tell one coalesced turn from two sequential ones.
		//
		// Scoping watchSeen to request 3 is what carries the ordering. The
		// pending watch send does not exist until after request 2 is already
		// sent, so no assertion ABOUT request 2 can say anything about it.
		fatalSeen := len(requests) == 3 && requestsContain(requests[1:2], "fatal before watch delivery")
		watchSeen := len(requests) == 3 && requestsContain(requests[2:3], "watch send failed")
		if got.output != "dropped watch diagnostic handled" || !watchSeen || !fatalSeen {
			t.Fatalf("DrainJobTree output = %q with %d provider requests (fatal attention in request 2 %t, watch diagnostic in request 3 %t), want one notification per turn in that order", got.output, len(requests), fatalSeen, watchSeen)
		}
	case recheck <- now.Add(time.Second):
		cancelDrain()
		<-drainDone
		t.Fatal("DrainJobTree waited with an idle fatal-gated watch send still pending")
	}
	if parent.jobManager.hasPendingWatchSends() {
		t.Fatal("fatal-gated watch send remained pending after drain")
	}
	if pending := loadWatchSendRecord(t, parent.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("persisted watch sends after drain = %+v, want dropped", pending)
	}
	var dropped bool
	for _, event := range loadJobStoreEvents(t, parent.jobManager) {
		if event.Kind == jobstore.EventWatchSendDropped && event.WatchSend != nil && event.WatchSend.DeliveryID == "delivery_restore_pending" {
			dropped = true
		}
	}
	if !dropped {
		t.Fatal("watch send drop was not persisted")
	}

	recovered := (delegateRuntime{owner: parent}).send(context.Background(), result.DelegateID, "explicitly recover after dropped watch send", 0).result
	if recovered.Err != nil || recovered.Action != "started" {
		t.Fatalf("explicit recovery = %+v, want started", recovered)
	}
	child.mu.Lock()
	recoveredDone := child.done
	child.mu.Unlock()
	select {
	case <-recoveredDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("explicit recovery did not finish")
	}
	if child.fatalRunGatedSnapshot() {
		t.Fatal("explicit recovery did not clear fatal gate")
	}
}

func TestSubagentFatalDriveTurnStopsOwnedShellAndSuppressesRedrive(t *testing.T) {
	driveErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal notification turn", nil, nil)
	enteredFatalDrive := make(chan struct{})
	releaseFatalDrive := make(chan struct{})
	var releaseFatalDriveOnce sync.Once
	t.Cleanup(func() { releaseFatalDriveOnce.Do(func() { close(releaseFatalDrive) }) })
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
				close(enteredFatalDrive)
				<-releaseFatalDrive
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
		Task: "idle before a fatal notification turn",
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	_, childID, err := decodeRef(result.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", result.TranscriptRef, err)
	}
	child := parent.subagents.get(childID)
	if child == nil || child.sess == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	child.mu.Lock()
	initialDone := child.done
	child.mu.Unlock()
	select {
	case <-initialDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("stable delegate did not finish before notification drive")
	}
	child.mu.Lock()
	statusBeforeDrive := child.status
	doneBeforeDrive := child.done
	child.mu.Unlock()
	if err := armOwnedJobDrainAttention(child.sess, "delegate:fatal-drive-notification", "fatal-drive-notification"); err != nil {
		t.Fatalf("arm fatal-drive attention: %v", err)
	}
	child.mu.Lock()
	fatalDriveDone := child.done
	child.mu.Unlock()
	if fatalDriveDone == doneBeforeDrive {
		t.Fatal("stable attention did not start a new delegate generation")
	}
	select {
	case <-env.started:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("automatic notification drive did not launch its owned shell")
	}
	<-enteredFatalDrive
	shellJobID := liveOwnedShellJobID(t, child.sess.jobManager)
	child.sess.jobManager.mu.Lock()
	shellDone := child.sess.jobManager.running[shellJobID].done
	child.sess.jobManager.mu.Unlock()
	releaseFatalDriveOnce.Do(func() { close(releaseFatalDrive) })
	select {
	case <-env.signaled:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("fatal automatic notification drive did not stop its owned shell")
	}
	select {
	case <-shellDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("fatal-drive shell did not durably finalize")
	}
	pending, err := child.sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read fatal-drive shell attention: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("fatal-drive shell completion did not retain durable attention")
	}
	select {
	case <-fatalDriveDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("fatal attention generation did not finalize")
	}
	child.sess.notify()
	requests := adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("provider requests after fatal drive = %d, want initial, shell launch, and fatal turns only", len(requests))
	}
	if !child.fatalRunGatedSnapshot() {
		t.Fatal("fatal notification drive did not retain the automatic-drive gate")
	}
	rec := loadShellRecord(t, child.sess.jobManager, shellJobID)
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("fatal-drive shell record = status %s reason %q, want cancelled/stopped_by_parent", rec.Status, rec.Reason)
	}
	child.mu.Lock()
	statusAfterDrive := child.status
	resultAfterDrive := child.result
	errAfterDrive := child.err
	doneAfterDrive := child.done
	child.mu.Unlock()
	if statusBeforeDrive != SubagentCompleted || statusAfterDrive != SubagentFailed || !errors.Is(errAfterDrive, driveErr) || doneAfterDrive != fatalDriveDone {
		t.Fatalf("fatal attention generation state = before %s after (%s, %q, %v, %p), want completed -> failed with its exact generation error/done %p",
			statusBeforeDrive, statusAfterDrive, resultAfterDrive, errAfterDrive, doneAfterDrive, fatalDriveDone)
	}
}

func (f *ownedJobDrainFixture) releaseAndWait(t *testing.T) {
	t.Helper()
	f.env.releaseJob()
	select {
	case <-f.runDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatalf("stable delegate %s did not finish after its owned shell exited", f.result.DelegateID)
	}
	waitForOwnedJobDrainGeneration(t, f.child, f.shellHandled, "shell attention generation")
	pending, err := f.child.sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending shell attention after generation: %v", err)
	}
	if len(pending) != 0 {
		waitForOwnedJobDrainGeneration(t, f.child, f.freshHandled, "follow-up attention generation")
	}
}

func armOwnedJobDrainAttention(sess *Session, attentionID, content string) error {
	_, err := sess.appendDelegateNotificationDurably(attentionID, content)
	if err != nil {
		return err
	}
	if err := sess.armDelegateAttention(attentionID); err != nil {
		return err
	}
	return nil
}

func waitForOwnedJobDrainGeneration(t *testing.T, child *subagent, entered <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatalf("%s did not reach the provider", description)
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	select {
	case <-done:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatalf("%s did not finish", description)
	}
}

func (f *ownedJobDrainFixture) requireHandledResult(t *testing.T, wantRequests int, wantResult string) {
	t.Helper()

	requests := f.adapter.Requests()
	if len(requests) != wantRequests {
		t.Fatalf("provider requests = %d, want %d through stable shell attention settlement", len(requests), wantRequests)
	}
	if !requestsContain(requests[2:], "child shell complete") {
		t.Fatalf("final provider request did not contain the shell notification: %+v", requests[2:])
	}

	f.child.mu.Lock()
	result := f.child.result
	status := f.child.status
	f.child.mu.Unlock()
	if status != SubagentCompleted || result != wantResult {
		t.Fatalf("child terminal state = %s, result %q; want completed with result %q", status, result, wantResult)
	}

	stored, _, _, err := f.child.sess.jobManager.readOutput(f.shellJobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read child-owned shell output: %v", err)
	}
	if !strings.Contains(stored, "child shell complete") {
		t.Fatalf("stored child-owned shell output = %q, want completed shell output", stored)
	}
	rec := loadShellRecord(t, f.child.sess.jobManager, f.shellJobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("child-owned shell record = status %s reason %q, want completed", rec.Status, rec.Reason)
	}
	aggregate := delegateAggregateSnapshot(t, f.parent.delegateController, f.result.DelegateID)
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCompleted {
		t.Fatalf("stable delegate aggregate = %#v, want completed outcome", aggregate)
	}
	packet := loadStableDelegateTerminalPacket(t, f.parent, f.result.DelegateID)
	var message string
	if err := json.Unmarshal(packet.Message, &message); err != nil {
		t.Fatalf("decode canonical terminal message: %v", err)
	}
	if packet.Kind != delegatestore.PacketReported || strings.TrimSpace(message) != wantResult {
		t.Fatalf("canonical terminal packet = kind %s message %q, want reported result %q", packet.Kind, message, wantResult)
	}
	pending, err := readPendingDelegateAttention(transcriptPath(f.parent.stateDir, f.parent.id), f.parent.id)
	if err != nil {
		t.Fatalf("read parent stable attention: %v", err)
	}
	if len(pending) != 2 || !strings.HasPrefix(pending[0], "delegate:") || !strings.HasPrefix(pending[1], "delegate:") {
		t.Fatalf("parent pending stable attention = %#v, want the original and attention-generation terminal deliveries", pending)
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
	fixture.child.sess.jobManager.mu.Lock()
	ownedShell := fixture.child.sess.jobManager.running[fixture.shellJobID]
	fixture.child.sess.jobManager.mu.Unlock()
	if ownedShell == nil {
		t.Fatalf("child-owned shell %s was finalized before the stable delegate drain", fixture.shellJobID)
	}
	select {
	case <-done:
		t.Fatal("subagent done channel closed while its owned shell was running")
	default:
	}

	fixture.releaseAndWait(t)
	fixture.requireHandledResult(t, 3, "owned shell handled")
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
	fixture.requireHandledResult(t, 3, "owned shell handled")
}

// TestSubagentFinalizationDrainReenqueueRaceDeterministic reproduces issue #140
// deterministically. The root cause is the non-atomic NotifyPending→NotifyDelivered
// transition in armFinalizedJob: between appending the NotifyPending event and
// persistStableShellAttention marking the record NotifyDelivered, the drain loop's
// rematerializeDurablePendings (run on every 250ms recheck kick) observes the still-
// NotifyPending record and re-enqueues it onto the child's notification queue. The
// drain then sees peekNotifications > 0 and runs a provider turn during finalization
// where none is expected.
//
// The existing TestSubagentFinalizationRefusesResumeAndDriveUntilCallbackRestored
// catches this only intermittently because the race window is scheduler-dependent.
// This test forces the window open with the testOnlyAfterNotifyPendingAppend hook
// (which fires exactly between the NotifyPending append and the NotifyDelivered
// transition) and invokes rematerializeDurablePendings directly, then asserts the
// re-enqueue must NOT happen: the durable pending must not be re-materializable
// while the finalizing job's NotifyDelivered transition is still in flight. Under
// the current (buggy) code rematerializeDurablePendings re-enqueues the record, so
// peekNotifications becomes > 0 and the assertion fails red.
func TestSubagentFinalizationDrainReenqueueRaceDeterministic(t *testing.T) {
	fixture := newOwnedJobDrainFixture(t)

	hookFired := make(chan struct{})
	hookRelease := make(chan struct{})
	var hookReleaseOnce sync.Once
	t.Cleanup(func() { hookReleaseOnce.Do(func() { close(hookRelease) }) })

	// Snapshot captured inside the race window, after rematerializeDurablePendings
	// has been forced to run against the still-NotifyPending record.
	type windowSnapshot struct {
		peekAfterRematerialize int
		requestsAtWindow       int
	}
	windowResult := make(chan windowSnapshot, 1)

	var hookOnce sync.Once
	fixture.child.sess.jobManager.testOnlyAfterNotifyPendingAppend = func(jobID string) {
		hookOnce.Do(func() {
			// We are now inside armFinalizedJob, AFTER the NotifyPending event was
			// appended and the terminal marked notification-pending, but BEFORE
			// persistStableShellAttention transitions the record to NotifyDelivered.
			// This is the exact race window the 250ms drain recheck exploits.
			if jobID != fixture.shellJobID {
				t.Errorf("after-notify-pending hook jobID = %q, want owned shell %q", jobID, fixture.shellJobID)
			}
			// Simulate the drain loop's kick() → rematerializeDurablePendings() that
			// races in during the 250ms recheck window. The queue should be empty here
			// (the stable path does not enqueue on the child's own rail), so the
			// rematerialize path is the one that can strand a re-enqueue.
			childSess := fixture.child.sess
			if err := childSess.rematerializeDurablePendings(); err != nil {
				t.Errorf("rematerializeDurablePendings during finalization window: %v", err)
			}
			peek := childSess.peekNotifications()
			requests := len(fixture.adapter.Requests())
			windowResult <- windowSnapshot{
				peekAfterRematerialize: peek,
				requestsAtWindow:       requests,
			}
			close(hookFired)
			// Hold the window open until the test has inspected the snapshot, so the
			// NotifyDelivered transition cannot race back in and mask the leak.
			<-hookRelease
		})
	}

	// Release the owned shell so it completes and armFinalizedJob enters the window.
	fixture.env.releaseJob()
	select {
	case <-hookFired:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("after-notify-pending hook did not fire during owned shell finalization")
	}

	snap := <-windowResult
	// The invariant the fix must enforce: a job whose NotifyPending→NotifyDelivered
	// transition is in flight must not be re-materializable onto the notification
	// queue. rematerializeDurablePendings must observe the in-flight finalization
	// (or the NotifyDelivered transition must be atomic with the NotifyPending
	// append) and leave the queue empty. Under the buggy code the record is still
	// NotifyPending, so it is re-enqueued and peekNotifications becomes 1.
	if snap.peekAfterRematerialize != 0 {
		t.Fatalf("rematerializeDurablePendings re-enqueued during finalization: peekNotifications = %d, want 0 (NotifyPending→NotifyDelivered transition is non-atomic; issue #140)", snap.peekAfterRematerialize)
	}
	// No provider turn should have fired from the re-enqueue during the window.
	// The two legitimate requests are the shell launch and the idle report; the
	// re-enqueued pending would drive a third (the CI count=3 failure).
	if snap.requestsAtWindow != 2 {
		t.Fatalf("provider requests at finalization window = %d, want 2 (shell launch + idle); a third means the re-enqueue drove a turn", snap.requestsAtWindow)
	}

	// Release armFinalizedJob and let finalization complete normally.
	hookReleaseOnce.Do(func() { close(hookRelease) })
	fixture.releaseAndWait(t)
	fixture.requireHandledResult(t, 3, "owned shell handled")
}

// TestSubagentFinalizationRematerializeLoadSnapshotStraddle pins the discipline
// documented on outstandingDrainJobIDs: the durable snapshot and the running-map
// read that rematerializeDurablePendings bases its re-enqueue decision on must be
// taken under the SAME jm.mu hold. A pass that loads the durable records first
// and snapshots the running map afterwards can straddle a finalization: the load
// observes the still-NotifyPending record, armFinalizedJob then appends
// NotifyDelivered and deletes the run, and the late snapshot no longer shows the
// job as live — so the stale pending is re-enqueued after its delivery, driving
// the same extra finalization provider turn as issue #140 through a narrower
// window. The test parks armFinalizedJob in its notify-pending window, parks a
// rematerialize pass on its already-loaded durable snapshot, lets finalization
// complete, releases the pass, and asserts nothing was re-enqueued.
func TestSubagentFinalizationRematerializeLoadSnapshotStraddle(t *testing.T) {
	fixture := newOwnedJobDrainFixture(t)
	childSess := fixture.child.sess
	jm := childSess.jobManager

	windowOpen := make(chan struct{})
	windowRelease := make(chan struct{})
	var windowReleaseOnce sync.Once
	t.Cleanup(func() { windowReleaseOnce.Do(func() { close(windowRelease) }) })
	var windowOnce sync.Once
	jm.testOnlyAfterNotifyPendingAppend = func(string) {
		windowOnce.Do(func() {
			close(windowOpen)
			<-windowRelease
		})
	}

	loaded := make(chan struct{})
	loadRelease := make(chan struct{})
	var loadReleaseOnce sync.Once
	t.Cleanup(func() { loadReleaseOnce.Do(func() { close(loadRelease) }) })
	var loadOnce sync.Once

	// Complete the owned shell; armFinalizedJob parks with the record durably
	// NotifyPending and the run still in the running map.
	fixture.env.releaseJob()
	select {
	case <-windowOpen:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("after-notify-pending hook did not fire during owned shell finalization")
	}

	jm.mu.Lock()
	jm.testOnlyAfterRematerializeDurableLoad = func() {
		loadOnce.Do(func() {
			close(loaded)
			<-loadRelease
		})
	}
	jm.mu.Unlock()

	// Start a rematerialize pass and park it on its already-loaded durable
	// snapshot, which still shows the shell record NotifyPending.
	remDone := make(chan error, 1)
	go func() { remDone <- childSess.rematerializeDurablePendings() }()
	select {
	case <-loaded:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("rematerialize pass did not reach its durable-load hook")
	}

	// Let finalization run to completion: NotifyDelivered is appended and the run
	// leaves the running map while the rematerialize pass is still parked.
	windowReleaseOnce.Do(func() { close(windowRelease) })
	waitForOwnedShellFinalized(t, jm, fixture.shellJobID)

	// Release the parked pass. Its durable snapshot is stale (NotifyPending), so
	// only a running-map view taken under the same jm.mu hold as the load can
	// tell it armFinalizedJob owned — and has since delivered — that notification.
	loadReleaseOnce.Do(func() { close(loadRelease) })
	select {
	case err := <-remDone:
		if err != nil {
			t.Fatalf("rematerializeDurablePendings: %v", err)
		}
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("rematerialize pass did not finish")
	}
	if n := childSess.peekNotifications(); n != 0 {
		t.Fatalf("straddling rematerialize re-enqueued a delivered notification: peekNotifications = %d, want 0", n)
	}

	// The delivered notification must reach the child exactly once, via the
	// stable-attention drive: three provider turns total, ending on the owned
	// shell result. A duplicate child-rail delivery would add a fourth turn and
	// leave the child on the fresh-notification response instead.
	fixture.releaseAndWait(t)
	fixture.requireHandledResult(t, 3, "owned shell handled")
}

// waitForOwnedShellFinalized waits until armFinalizedJob has finished the owned
// shell's finalization: the run has left the running map (which happens strictly
// after the stable path appends NotifyDelivered) and the durable record reads
// NotifyDelivered.
func waitForOwnedShellFinalized(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second) // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
	for {
		jm.mu.Lock()
		_, live := jm.running[jobID]
		jm.mu.Unlock()
		if !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned shell %s did not leave the running map after its finalization window was released", jobID)
		}
		time.Sleep(2 * time.Millisecond)
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load job store after finalization: %v", err)
	}
	rec := recs[jobID]
	if rec == nil || rec.NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("owned shell %s record = %+v, want NotifyDelivered after finalization", jobID, rec)
	}
}
