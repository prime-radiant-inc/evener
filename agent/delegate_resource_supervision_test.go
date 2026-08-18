package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestDelegateResourceSupervision_AutoNudgeOccursOnceForEligibleBuiltin(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return finalResponse("recovered after the one nudge") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
	if outcome.result.Err != nil || outcome.result.Status != jobstore.StatusCompleted || !strings.Contains(outcome.result.Output, "recovered after the one nudge") {
		t.Fatalf("nudged stable delegate = %#v", outcome.result)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 5 {
		t.Fatalf("provider requests = %d, want four empty attempts plus one nudge", got)
	}
}

func TestDelegateResourceSupervision_AutoNudgeSuppressedBySteerCancellationAndExhaustion(t *testing.T) {
	t.Run("steer", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				close(entered)
				<-release
				return finalResponse("first result")
			},
			func(llm.Request) llm.Response { return finalResponse("continued after steer") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
		if started.result.Err != nil {
			t.Fatalf("start stable delegate: %v", started.result.Err)
		}
		<-entered
		steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "new steering", 0)
		if steered.result.Err != nil || steered.result.Action != "steered" {
			t.Fatalf("steer stable delegate = %#v", steered.result)
		}
		close(release)
		waitForStableSupervisionRun(t, root, fixture.childID)
		if got := supervisionRequestCount(fixture.adapter); got != 2 {
			t.Fatalf("steered provider requests = %d, want initial plus steering continuation without nudge", got)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
			close(entered)
			<-release
			return finalResponse("cancelled result")
		}}
		root := restoreSupervisionRoot(t, fixture, nil)
		started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
		if started.result.Err != nil {
			t.Fatalf("start stable delegate: %v", started.result.Err)
		}
		<-entered
		sub := root.subagents.get(fixture.childID)
		if sub == nil {
			t.Fatal("stable child was not tracked")
		}
		sub.mu.Lock()
		sub.cancelRequested = true
		cancel := sub.cancel
		sub.mu.Unlock()
		if cancel == nil {
			t.Fatal("stable child has no generation cancel")
		}
		cancel()
		close(release)
		waitForStableSupervisionRun(t, root, fixture.childID)
		if got := supervisionRequestCount(fixture.adapter); got != 1 {
			t.Fatalf("cancelled provider requests = %d, want no nudge", got)
		}
	})

	t.Run("exhaustion", func(t *testing.T) {
		fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
			descriptor.Config.MaxToolRoundsPerInput = 1
		})
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return communicateResponse(false, "continue") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "exhaust", 60_000)
		abortUnpersistedStableDelegateOutcome(t, outcome)
		if outcome.result.Err != nil || outcome.result.Status != jobstore.StatusExhausted {
			t.Fatalf("exhausted stable delegate = %#v", outcome.result)
		}
		if got := supervisionRequestCount(fixture.adapter); got != 1 {
			t.Fatalf("exhausted provider requests = %d, want no nudge", got)
		}
	})
}

func TestDelegateResourceSupervision_FatalFailureBeatsPendingSteer(t *testing.T) {
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal turn", nil, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				close(entered)
				<-release
				return llm.Response{}, fatalErr
			},
			func(llm.Request) (llm.Response, error) {
				return finalResponse("unexpected continuation after fatal failure"), nil
			},
		},
	}
	fixture := newColdStableDelegateFixture(t, "")
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	child := root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	phaseBeforeFinish := make(chan delegatestore.Phase, 1)
	child.sess.cfg.testOnly.subagentAfterFinalStatePublish = func(got *subagent) {
		got.sess.delegateController.mu.Lock()
		aggregate := got.sess.delegateController.durable[fixture.delegateID]
		phase := aggregate.Phase
		got.sess.delegateController.mu.Unlock()
		phaseBeforeFinish <- phase
	}
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "accepted before fatal failure", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider requests after fatal failure = %d, want no steering continuation", got)
	}
	if got := <-phaseBeforeFinish; got != delegatestore.PhaseRunning {
		t.Fatalf("fatal generation phase before atomic finish = %s, want running without a prepared-only state", got)
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeFailed)
}

func TestDelegateResourceSupervision_ExhaustionBeatsPendingSteer(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.MaxToolRoundsPerInput = 1
	})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-release
			return communicateResponse(false, "exhaust this activation")
		},
		func(llm.Request) llm.Response {
			return finalResponse("unexpected continuation after exhaustion")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "accepted before exhaustion", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 1 {
		t.Fatalf("provider requests after exhaustion = %d, want no steering continuation", got)
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeExhausted)
}

func TestDelegateResourceSupervision_CancellationBeatsPendingSteer(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	continued := make(chan struct{})
	var continuedOnce sync.Once
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return finalResponse("cancelled result")
	}}
	root := restoreSupervisionRoot(t, fixture, nil)
	root.cfg.testOnly.subagentRunIteration = func(_ *subagent, iteration int) {
		if iteration > 1 {
			continuedOnce.Do(func() { close(continued) })
		}
	}
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "accepted before cancellation", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil {
		t.Fatal("stable child was not tracked")
	}
	sub.mu.Lock()
	sub.cancelRequested = true
	cancel := sub.cancel
	done := sub.done
	sub.mu.Unlock()
	if cancel == nil || done == nil {
		t.Fatal("stable child has no cancellable generation")
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-continued:
		root.delegateController.mu.Lock()
		root.delegateController.live[fixture.delegateID].pendingSteers = nil
		root.delegateController.mu.Unlock()
		<-done
		t.Fatal("cancelled generation entered a steering continuation")
	}
	if got := supervisionRequestCount(fixture.adapter); got != 1 {
		t.Fatalf("provider requests after cancellation = %d, want no steering continuation", got)
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeCancelled)
}

func assertStableSupervisionOutcome(t *testing.T, root *Session, delegateID string, want delegatestore.OutcomeStatus) {
	t.Helper()
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[delegateID]
	root.delegateController.mu.Unlock()
	if aggregate == nil || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != want {
		t.Fatalf("stable delegate outcome = %#v, want %s", aggregate, want)
	}
}

func TestDelegateResourceSupervision_PendingSteerPrecedesAutoNudge(t *testing.T) {
	enteredFinalEmpty := make(chan struct{})
	releaseFinalEmpty := make(chan struct{})
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response { return agenttest.EmptyResponse() },
		func(llm.Request) llm.Response {
			close(enteredFinalEmpty)
			<-releaseFinalEmpty
			return agenttest.EmptyResponse()
		},
		func(llm.Request) llm.Response { return finalResponse("continued after steering") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-enteredFinalEmpty
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "priority steering", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	close(releaseFinalEmpty)
	waitForStableSupervisionRun(t, root, fixture.childID)
	requests := fixture.adapter.Requests()
	if len(requests) != 5 {
		t.Fatalf("provider requests = %d, want four empty attempts plus steering continuation", len(requests))
	}
	if requestMessagesContainText(requests[4].Messages, communicateNudge("communicate")) {
		t.Fatalf("pending steer was delayed behind auto-nudge: %#v", requests[4].Messages)
	}
	if !requestMessagesContainText(requests[4].Messages, "priority steering") {
		t.Fatalf("steering continuation omitted accepted steer: %#v", requests[4].Messages)
	}
}

func TestDelegateResourceSupervision_LateOrdinarySteerPreservesOwnedWorkForContinuation(t *testing.T) {
	enteredInitialRequest := make(chan struct{})
	releaseInitialRequest := make(chan struct{})
	ownedWorkStopped := make(chan struct{}, 1)
	stoppedBeforeContinuation := make(chan bool, 1)
	releaseContinuation := make(chan struct{})
	lateSteer := make(chan sendMessageResult, 1)
	var child *subagent
	var ownedShell *runningJob
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare text without communicate")}
	}
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(enteredInitialRequest)
			<-releaseInitialRequest
			return bare(llm.Request{})
		},
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
		func(llm.Request) llm.Response {
			select {
			case <-ownedWorkStopped:
				stoppedBeforeContinuation <- true
			default:
				stoppedBeforeContinuation <- false
			}
			<-releaseContinuation
			return finalResponse("continued after late nudge steering")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-enteredInitialRequest
	child = root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil || child.sess.jobManager == nil {
		t.Fatalf("stable child %q has no managed-work runtime", fixture.childID)
	}
	ownedShell = &runningJob{
		rec: &jobstore.JobRecord{
			JobID:          "job_late_nudge_owned_shell",
			Type:           jobstore.JobShell,
			Status:         jobstore.StatusRunning,
			OwnerSessionID: child.sess.ID(),
		},
		signal: func() {
			ownedWorkStopped <- struct{}{}
			ownedShell.closeDone()
		},
		done:           make(chan struct{}),
		durableStarted: true,
	}
	child.sess.jobManager.mu.Lock()
	child.sess.jobManager.running[ownedShell.rec.JobID] = ownedShell
	child.sess.jobManager.mu.Unlock()
	t.Cleanup(func() {
		child.sess.jobManager.mu.Lock()
		delete(child.sess.jobManager.running, ownedShell.rec.JobID)
		child.sess.jobManager.mu.Unlock()
		ownedShell.closeDone()
	})
	var steerOnce sync.Once
	child.sess.cfg.testOnly.subagentBeforeSettlement = func(*subagent) {
		steerOnce.Do(func() {
			lateSteer <- (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "late steering at ordinary settlement", 0).result
		})
	}
	close(releaseInitialRequest)
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	if steered := <-lateSteer; steered.Err != nil || steered.Action != "steered" {
		t.Fatalf("late ordinary steer = %#v", steered)
	}
	var stopped bool
	select {
	case stopped = <-stoppedBeforeContinuation:
		child.sess.jobManager.mu.Lock()
		delete(child.sess.jobManager.running, ownedShell.rec.JobID)
		child.sess.jobManager.mu.Unlock()
		ownedShell.closeDone()
		close(releaseContinuation)
		<-done
	case <-done:
		close(releaseContinuation)
		t.Fatal("ordinary missing-terminal path settled without honoring late steering")
	}
	if stopped {
		t.Fatal("ordinary missing-terminal path stopped owned work before honoring late steering")
	}
	requests := fixture.adapter.Requests()
	if len(requests) != 9 || !requestMessagesContainText(requests[8].Messages, "late steering at ordinary settlement") {
		t.Fatalf("late nudge continuation requests = %d, final history %#v", len(requests), requests[len(requests)-1].Messages)
	}
}

func TestDelegateResourceSupervision_LateCancellationBeatsSettlementSteer(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	continued := make(chan struct{})
	lateCancel := make(chan sendMessageResult, 1)
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-release
			return finalResponse("result before late cancellation")
		},
		func(llm.Request) llm.Response {
			return finalResponse("unexpected continuation after late cancellation")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	child := root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	var cancelOnce sync.Once
	child.sess.cfg.testOnly.subagentRunIteration = func(_ *subagent, iteration int) {
		if iteration > 1 {
			select {
			case <-continued:
			default:
				close(continued)
			}
		}
	}
	child.sess.cfg.testOnly.subagentBeforeSettlement = func(got *subagent) {
		cancelOnce.Do(func() {
			lateCancel <- (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "steer admitted at cancellation boundary", 0).result
			got.mu.Lock()
			got.cancelRequested = true
			cancel := got.cancel
			got.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		})
	}
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if steered := <-lateCancel; steered.Err != nil || steered.Action != "steered" {
		t.Fatalf("late cancellation steer = %#v", steered)
	}
	select {
	case <-continued:
		t.Fatal("cancellation admitted at the settlement boundary entered a steering continuation")
	default:
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeCancelled)
}

type asynchronousCleanupStreamingExecutor struct {
	marker         string
	signalReturned chan struct{}
	releaseWait    chan struct{}
	waitReturned   chan struct{}
	signalOnce     sync.Once
	releaseOnce    sync.Once
}

func newAsynchronousCleanupStreamingExecutor(marker string) *asynchronousCleanupStreamingExecutor {
	return &asynchronousCleanupStreamingExecutor{
		marker:         marker,
		signalReturned: make(chan struct{}),
		releaseWait:    make(chan struct{}),
		waitReturned:   make(chan struct{}),
	}
}

func (e *asynchronousCleanupStreamingExecutor) StreamCommand(_ context.Context, _ string, _ string, _ map[string]string, _ io.Writer) (*execenv.StreamHandle, error) {
	return &execenv.StreamHandle{
		Signal: func() {
			e.signalOnce.Do(func() { close(e.signalReturned) })
		},
		Wait: func() (int, error) {
			<-e.releaseWait
			err := os.WriteFile(e.marker, []byte("cleaned\n"), 0o644)
			close(e.waitReturned)
			return 143, err
		},
	}, nil
}

func (e *asynchronousCleanupStreamingExecutor) release() {
	e.releaseOnce.Do(func() { close(e.releaseWait) })
}

func TestDelegateResourceSupervision_FatalNudgeRunStopsOwnedShell(t *testing.T) {
	worktreeRepo := newWorktreeRepo(t)
	lane, _, _, _, _, err := worktreeRepo.s.createDelegateWorktree(context.Background(), "dlg_01TASK7FATALPACKET000001")
	if err != nil {
		t.Fatalf("create fatal-evidence worktree: %v", err)
	}
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal nudge turn", nil, nil)
	enteredFatalNudge := make(chan struct{})
	releaseFatalNudge := make(chan struct{})
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return agenttest.EmptyResponse(), nil
			},
			func(llm.Request) (llm.Response, error) {
				return agenttest.EmptyResponse(), nil
			},
			func(llm.Request) (llm.Response, error) {
				return agenttest.EmptyResponse(), nil
			},
			func(llm.Request) (llm.Response, error) {
				return agenttest.EmptyResponse(), nil
			},
			func(llm.Request) (llm.Response, error) {
				close(enteredFatalNudge)
				<-releaseFatalNudge
				return llm.Response{}, fatalErr
			},
		},
	}
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Isolation = "worktree"
		descriptor.WorkingDir = lane
	})
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	finalStatePublished := make(chan struct{})
	restore := RestoreSessionConfig{
		StateDir:    fixture.stateDir,
		ForceRealIO: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			subagentAfterFinalStatePublish: func(*subagent) {
				close(finalStatePublished)
			},
		},
	}
	root, err := RestoreSessionFromMetaWithConfig(client, fixture.profile, execenv.NewLocalExecutionEnvironment(fixture.workspace), fixture.meta, restore)
	if err != nil {
		t.Fatalf("restore fatal-nudge root: %v", err)
	}
	t.Cleanup(root.Close)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start fatal nudge run", 0)
	if started.result.Err != nil {
		t.Fatalf("start fatal-nudge delegate: %v", started.result.Err)
	}
	<-enteredFatalNudge
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	root.delegateController.mu.Lock()
	descriptor := cloneDelegateStartDescriptor(root.delegateController.durable[fixture.delegateID].Descriptor)
	root.delegateController.mu.Unlock()
	if report := root.stableDelegateWorktreeReport(descriptor); report == nil || report.Dirty {
		t.Fatalf("initial fatal-evidence worktree report = %#v, want clean", report)
	}
	cleanupMarker := filepath.Join(lane, "fatal-shell-cleanup.txt")
	executor := newAsynchronousCleanupStreamingExecutor(cleanupMarker)
	t.Cleanup(executor.release)
	ownedShell := runShell(context.Background(), sub.sess.jobManager, executor, shellArgs{
		Command:    "asynchronous fatal cleanup",
		Background: true,
		WorkingDir: lane,
	})
	if ownedShell.JobID == "" || !ownedShell.RunningInBackground {
		t.Fatalf("start fatal owned shell = %#v", ownedShell)
	}
	joinStarted := make(chan struct{})
	var joinOnce sync.Once
	sub.sess.jobManager.stopReceiptBeforeWait = func(jobID string) {
		if jobID == ownedShell.JobID {
			joinOnce.Do(func() { close(joinStarted) })
		}
	}
	close(releaseFatalNudge)
	select {
	case <-joinStarted:
		executor.release()
	case <-finalStatePublished:
		executor.release()
		<-executor.waitReturned
		t.Fatal("fatal stable nudge run sampled terminal evidence before joining its owned shell")
	}
	<-finalStatePublished
	<-executor.waitReturned
	if report := root.stableDelegateWorktreeReport(descriptor); report == nil || !report.Dirty {
		t.Fatalf("post-cleanup worktree report = %#v, want dirty", report)
	}
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load fatal terminal packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil {
		t.Fatal("fatal stable generation has no terminal packet")
	}
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
		t.Fatalf("decode fatal terminal metadata: %v", err)
	}
	if metadata.Worktree == nil || !metadata.Worktree.Dirty {
		t.Fatalf("fatal packet sampled worktree before shell cleanup: %#v", metadata.Worktree)
	}
}

func TestDelegateResourceSupervision_FatalCleanupFailureIsObservable(t *testing.T) {
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal cleanup failure turn", nil, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){func(llm.Request) (llm.Response, error) {
			close(entered)
			<-release
			return llm.Response{}, fatalErr
		}},
	}
	fixture := newColdStableDelegateFixture(t, "")
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start cleanup-failure run", 0)
	if started.result.Err != nil {
		t.Fatalf("start cleanup-failure delegate: %v", started.result.Err)
	}
	<-entered
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil || sub.sess.jobManager == nil {
		t.Fatalf("stable child %q has no managed-work runtime", fixture.childID)
	}
	ownedShell := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:          "job_owned_cleanup_failure",
			Type:           jobstore.JobShell,
			Status:         jobstore.StatusRunning,
			OwnerSessionID: sub.sess.ID(),
		},
		done:           make(chan struct{}),
		durableStarted: true,
	}
	ownedShell.signal = func() { ownedShell.closeDoneAbandoned() }
	cleanupErr := "owned job " + ownedShell.rec.JobID + " ended without durable completion"
	jm := sub.sess.jobManager
	jm.mu.Lock()
	jm.running[ownedShell.rec.JobID] = ownedShell
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, ownedShell.rec.JobID)
		jm.mu.Unlock()
	})
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	sub.mu.Lock()
	runErr := sub.err
	sub.mu.Unlock()
	if !strings.Contains(runErr.Error(), cleanupErr) {
		t.Fatalf("retained fatal error = %v, want owned cleanup failure", runErr)
	}
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load cleanup-failure terminal packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil || !strings.Contains(string(packet.Message), cleanupErr) {
		t.Fatalf("cleanup-failure terminal packet = %#v, want observable cleanup error", packet)
	}
}

func TestDelegateResourceSupervision_RootCloseBeforeReceiptCaptureIsCleanupFailure(t *testing.T) {
	clock := agenttest.NewFakeClock()
	fatalErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal close-abandon turn", nil, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){func(llm.Request) (llm.Response, error) {
			close(entered)
			<-release
			return llm.Response{}, fatalErr
		}},
	}
	fixture := newColdStableDelegateFixture(t, "")
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	finalStatePublished := make(chan struct{})
	root := restoreSupervisionRoot(t, fixture, clock)
	root.cfg.testOnly.subagentAfterFinalStatePublish = func(*subagent) {
		close(finalStatePublished)
	}
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start close-abandon run", 0)
	if started.result.Err != nil {
		t.Fatalf("start close-abandon delegate: %v", started.result.Err)
	}
	<-entered
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil || sub.sess.jobManager == nil {
		t.Fatalf("stable child %q has no managed-work runtime", fixture.childID)
	}
	cleanupMarker := filepath.Join(fixture.workspace, "close-abandon-cleanup.txt")
	executor := newAsynchronousCleanupStreamingExecutor(cleanupMarker)
	t.Cleanup(executor.release)
	ownedShell := runShell(context.Background(), sub.sess.jobManager, executor, shellArgs{
		Command:    "asynchronous close-abandon cleanup",
		Background: true,
		WorkingDir: fixture.workspace,
	})
	if ownedShell.JobID == "" || !ownedShell.RunningInBackground {
		t.Fatalf("start close-abandon owned shell = %#v", ownedShell)
	}
	jm := sub.sess.jobManager
	jm.closeGrace = time.Second
	var closeErr error
	var closeOnce sync.Once
	jm.stopReceiptsAfterCapture = func() {
		closeOnce.Do(func() {
			blockedBeforeClose := clock.BlockedCount()
			closeResult := make(chan error, 1)
			go func() {
				closeResult <- jm.closeRuntimeState()
			}()
			clock.BlockUntil(blockedBeforeClose + 1)
			clock.Advance(time.Second)
			closeErr = <-closeResult
		})
	}
	close(release)
	<-finalStatePublished
	if closeErr == nil || !strings.Contains(closeErr.Error(), "timed out waiting for running jobs") {
		t.Fatalf("close runtime result = %v, want bounded running-job timeout", closeErr)
	}
	select {
	case <-executor.waitReturned:
		t.Fatal("blocked executor Wait returned before the test released it")
	default:
	}
	sub.mu.Lock()
	runErr := sub.err
	sub.mu.Unlock()
	if runErr == nil || !strings.Contains(runErr.Error(), ownedShell.JobID) || !strings.Contains(runErr.Error(), "durable completion") {
		t.Fatalf("retained close-abandon error = %v, want exact job and durable-completion failure", runErr)
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load close-abandon terminal packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil || !strings.Contains(string(packet.Message), ownedShell.JobID) || !strings.Contains(string(packet.Message), "durable completion") {
		t.Fatalf("close-abandon terminal packet = %#v, want exact cleanup failure", packet)
	}
	executor.release()
	<-executor.waitReturned
}

func TestDelegateResourceSupervision_OrdinaryCleanupFailureIsObservable(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("partial ordinary result")}
	}
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(entered)
			<-release
			return bare(llm.Request{})
		},
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start ordinary cleanup-failure run", 0)
	if started.result.Err != nil {
		t.Fatalf("start ordinary cleanup-failure delegate: %v", started.result.Err)
	}
	<-entered
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil || sub.sess.jobManager == nil {
		t.Fatalf("stable child %q has no managed-work runtime", fixture.childID)
	}
	ownedShell := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:          "job_ordinary_cleanup_failure",
			Type:           jobstore.JobShell,
			Status:         jobstore.StatusRunning,
			OwnerSessionID: sub.sess.ID(),
		},
		done:           make(chan struct{}),
		durableStarted: true,
	}
	ownedShell.signal = func() { ownedShell.closeDoneAbandoned() }
	cleanupErr := "owned job " + ownedShell.rec.JobID + " ended without durable completion"
	jm := sub.sess.jobManager
	jm.mu.Lock()
	jm.running[ownedShell.rec.JobID] = ownedShell
	jm.mu.Unlock()
	t.Cleanup(func() {
		jm.mu.Lock()
		delete(jm.running, ownedShell.rec.JobID)
		jm.mu.Unlock()
	})
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load ordinary cleanup-failure packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil || !strings.Contains(string(packet.Message), cleanupErr) {
		t.Fatalf("ordinary cleanup-failure packet = %#v, want observable cleanup failure", packet)
	}
}

func TestDelegateResourceSupervision_OrdinaryMissingTerminalCleanupPrecedesPacketEvidence(t *testing.T) {
	worktreeRepo := newWorktreeRepo(t)
	lane, _, _, _, _, err := worktreeRepo.s.createDelegateWorktree(context.Background(), "dlg_01TASK7ORDINARYPACKET001")
	if err != nil {
		t.Fatalf("create ordinary-evidence worktree: %v", err)
	}
	enteredInitialRequest := make(chan struct{})
	releaseInitialRequest := make(chan struct{})
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare text without communicate")}
	}
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Isolation = "worktree"
		descriptor.WorkingDir = lane
	})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			close(enteredInitialRequest)
			<-releaseInitialRequest
			return bare(llm.Request{})
		},
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
		bare,
	}
	finalStatePublished := make(chan struct{})
	restore := RestoreSessionConfig{
		StateDir:    fixture.stateDir,
		ForceRealIO: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			subagentAfterFinalStatePublish: func(*subagent) {
				close(finalStatePublished)
			},
		},
	}
	root, err := RestoreSessionFromMetaWithConfig(fixture.client, fixture.profile, execenv.NewLocalExecutionEnvironment(fixture.workspace), fixture.meta, restore)
	if err != nil {
		t.Fatalf("restore ordinary-evidence root: %v", err)
	}
	t.Cleanup(root.Close)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start ordinary missing-terminal run", 0)
	if started.result.Err != nil {
		t.Fatalf("start ordinary missing-terminal delegate: %v", started.result.Err)
	}
	<-enteredInitialRequest
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	root.delegateController.mu.Lock()
	descriptor := cloneDelegateStartDescriptor(root.delegateController.durable[fixture.delegateID].Descriptor)
	root.delegateController.mu.Unlock()
	if report := root.stableDelegateWorktreeReport(descriptor); report == nil || report.Dirty {
		t.Fatalf("initial ordinary-evidence worktree report = %#v, want clean", report)
	}
	cleanupMarker := filepath.Join(lane, "ordinary-shell-cleanup.txt")
	executor := newAsynchronousCleanupStreamingExecutor(cleanupMarker)
	t.Cleanup(executor.release)
	ownedShell := runShell(context.Background(), sub.sess.jobManager, executor, shellArgs{
		Command:    "asynchronous ordinary cleanup",
		Background: true,
		WorkingDir: lane,
	})
	if ownedShell.JobID == "" || !ownedShell.RunningInBackground {
		t.Fatalf("start ordinary owned shell = %#v", ownedShell)
	}
	joinStarted := make(chan struct{})
	var joinOnce sync.Once
	sub.sess.jobManager.stopReceiptBeforeWait = func(jobID string) {
		if jobID == ownedShell.JobID {
			joinOnce.Do(func() { close(joinStarted) })
		}
	}
	close(releaseInitialRequest)
	<-executor.signalReturned
	lateSteer := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "steer after ordinary settlement claim", 0)
	if !errors.Is(lateSteer.result.Err, errDelegateTargetBusy) {
		executor.release()
		t.Fatalf("steer after ordinary settlement claim error = %v, want target busy", lateSteer.result.Err)
	}
	select {
	case <-joinStarted:
		executor.release()
	case <-finalStatePublished:
		executor.release()
		<-executor.waitReturned
		t.Fatal("ordinary missing-terminal run sampled terminal evidence before joining its owned shell")
	}
	<-finalStatePublished
	<-executor.waitReturned
	if report := root.stableDelegateWorktreeReport(descriptor); report == nil || !report.Dirty {
		t.Fatalf("post-cleanup ordinary worktree report = %#v, want dirty", report)
	}
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load ordinary terminal packet: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil {
		t.Fatal("ordinary missing-terminal generation has no terminal packet")
	}
	var metadata delegateTerminalPacketMetadata
	if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
		t.Fatalf("decode ordinary terminal metadata: %v", err)
	}
	if metadata.Worktree == nil || !metadata.Worktree.Dirty {
		t.Fatalf("ordinary packet sampled worktree before shell cleanup: %#v", metadata.Worktree)
	}
}

func TestDelegateResourceSupervision_SubagentStopRunsAfterFinishAndBeforeContinuation(t *testing.T) {
	observation := runStableSubagentStopHook(t, true)
	if !observation.continuationSawHook || observation.providerRequests != 2 || observation.hookRuns != 1 {
		t.Fatalf("blocking SubagentStop ordering = %#v", observation)
	}
}

func TestDelegateResourceSupervision_SubagentStopBlockingStartsOneContinuation(t *testing.T) {
	observation := runStableSubagentStopHook(t, true)
	if observation.providerRequests != 2 || observation.hookRuns != 1 || !strings.Contains(observation.output, "continued after hook") {
		t.Fatalf("blocking SubagentStop continuation = %#v", observation)
	}
}

func TestDelegateResourceSupervision_SubagentStopNonblockingStartsNoContinuation(t *testing.T) {
	observation := runStableSubagentStopHook(t, false)
	if observation.providerRequests != 1 || observation.hookRuns != 1 || !strings.Contains(observation.output, "initial result") {
		t.Fatalf("nonblocking SubagentStop = %#v", observation)
	}
}

func TestDelegateResourceSupervision_SubtreeStopSuppressesSubagentStop(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "subagent-stop-runs")
	pluginDir := writeStableSubagentStopPlugin(t, marker, `{}`)
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return finalResponse("would normally run the hook")
	}}
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	if _, _, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.delegateRootSessionID), fixture.delegateID); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	close(release)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if raw, err := os.ReadFile(marker); err == nil {
		t.Fatalf("stopped generation ran SubagentStop: %q", raw)
	} else if !os.IsNotExist(err) {
		t.Fatalf("read hook marker: %v", err)
	}
}

func TestDelegateResourceSupervision_BlockingSubagentStopContinuesOnlyOnceWithPendingSteer(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "subagent-stop-runs")
	pluginDir := writeStableSubagentStopPlugin(t, marker, `{"decision":"block","reason":"address hook feedback"}`)
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
	})
	enteredHookContinuation := make(chan struct{})
	releaseHookContinuation := make(chan struct{})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("initial result") },
		func(llm.Request) llm.Response {
			close(enteredHookContinuation)
			<-releaseHookContinuation
			return finalResponse("hook continuation result")
		},
		func(llm.Request) llm.Response { return finalResponse("steering continuation result") },
		func(llm.Request) llm.Response { return finalResponse("unexpected second hook continuation") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-enteredHookContinuation
	steered := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "steer during hook continuation", 0)
	if steered.result.Err != nil || steered.result.Action != "steered" {
		t.Fatalf("steer stable delegate = %#v", steered.result)
	}
	close(releaseHookContinuation)
	waitForStableSupervisionRun(t, root, fixture.childID)
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read hook marker: %v", err)
	}
	if got := len(strings.Fields(string(raw))); got != 1 {
		t.Fatalf("SubagentStop hook runs = %d, want one per generation", got)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 3 {
		t.Fatalf("provider requests = %d, want initial + one hook continuation + steering continuation", got)
	}
}

func TestDelegateResourceSupervision_FinalRoundFailedSalvageAddsResumeHint(t *testing.T) {
	child := newTestSession(t)
	child.totalRounds = 1
	if err := child.persistSalvagedTurn("partial final draft", "gpt-5.2", "openai"); err != nil {
		t.Fatalf("persist final salvage: %v", err)
	}
	sub := &subagent{sess: child}
	packet := sub.stableDelegateFinish("", errors.New("provider failed after partial stream")).packet
	if packet == nil || !containsDelegateSalvageWarning(packet.Warnings) {
		t.Fatalf("failed final-round warnings = %#v", packetWarnings(packet))
	}
}

func TestDelegateResourceSupervision_SuccessExhaustionCancellationStopAndStaleSalvageAddNoHint(t *testing.T) {
	final := newTestSession(t)
	final.totalRounds = 1
	if err := final.persistSalvagedTurn("partial final draft", "gpt-5.2", "openai"); err != nil {
		t.Fatalf("persist final salvage: %v", err)
	}
	final.comm.called = true
	stale := newTestSession(t)
	stale.totalRounds = 1
	if err := stale.persistSalvagedTurn("stale draft", "gpt-5.2", "openai"); err != nil {
		t.Fatalf("persist stale salvage: %v", err)
	}
	stale.totalRounds = 2

	cases := []struct {
		name string
		sub  *subagent
		err  error
	}{
		{name: "success", sub: &subagent{sess: final}},
		{name: "tool exhaustion", sub: &subagent{sess: final}, err: &budgetExhaustionError{Budget: exhaustedBudgetToolRounds, Limit: 1, Resumable: true}},
		{name: "turn exhaustion", sub: &subagent{sess: final}, err: &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 1, Resumable: false}},
		{name: "cancellation", sub: &subagent{sess: final}, err: context.Canceled},
		{name: "stop", sub: &subagent{sess: final, cancelRequested: true}, err: context.Canceled},
		{name: "stale", sub: &subagent{sess: stale}, err: errors.New("later failure")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			packet := tc.sub.stableDelegateFinish("done", tc.err).packet
			if packet != nil && containsDelegateSalvageWarning(packet.Warnings) {
				t.Fatalf("unexpected salvage hint for %s: %#v", tc.name, packet.Warnings)
			}
		})
	}
}

func TestDelegateResourceSupervision_QuietWatchdogUsesTenMinuteThresholdAndThirtySecondChecks(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	cancelWatchdog := root.startDelegateQuietWatchdog(context.Background(), lease)
	if clock.BlockedCount() != 1 {
		t.Errorf("quiet watchdog waiters = %d, want one 30-second ticker", clock.BlockedCount())
	}
	cancelWatchdog()

	clock.Advance(10*time.Minute - time.Second)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("pre-threshold tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 0 {
		t.Fatalf("pre-threshold quiet attention = %#v", got)
	}
	clock.Advance(time.Second)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("threshold tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != delegateQuietAttentionID(lease) {
		t.Fatalf("threshold quiet attention = %#v", got)
	}
	controller.mu.Lock()
	activity := controller.live[lease.delegateID].activityAt
	controller.mu.Unlock()
	if activity.IsZero() {
		t.Fatal("quiet harness lost exact activity")
	}
}

func TestDelegateResourceSupervision_QuietWatchdogFiresOncePerQuietStretch(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(10 * time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Second)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 {
		t.Fatalf("same-stretch quiet attention = %#v", got)
	}
	if err := controller.ReportActivity(lease, clock.Now()); err != nil {
		t.Fatalf("ReportActivity: %v", err)
	}
	clock.Advance(10 * time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 2 {
		t.Fatalf("second-stretch quiet attention = %#v", got)
	}
}

func TestDelegateResourceSupervision_QuietAttentionAppendFailureRetriesSameIdentity(t *testing.T) {
	root, _, lease, clock := newStableQuietSupervisionHarness(t)
	root.mu.Lock()
	writer := root.transcript
	root.mu.Unlock()
	if err := writer.Close(); err != nil {
		t.Fatalf("close receiver writer: %v", err)
	}
	clock.Advance(10 * time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err == nil {
		t.Fatal("quiet tick succeeded without a writable receiver transcript")
	}
	path := transcriptPath(root.stateDir, root.id)
	reopened, _, err := transcript.OpenWriterForSession(path, root.id)
	if err != nil {
		t.Fatalf("reopen receiver writer: %v", err)
	}
	root.mu.Lock()
	root.transcript = reopened
	root.transcriptReady = true
	root.mu.Unlock()
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("retry quiet tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != delegateQuietAttentionID(lease) {
		t.Fatalf("retried quiet attention identities = %#v", got)
	}
}

func TestDelegateResourceSupervision_QuietAttentionWaitsForOwnerTurnBoundary(t *testing.T) {
	root, _, lease, clock := newStableQuietSupervisionHarness(t)
	root.mu.Lock()
	root.state = SessionProcessing
	root.mu.Unlock()
	clock.Advance(10 * time.Minute)
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("processing-owner quiet tick: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 0 {
		t.Fatalf("quiet attention split an active owner turn: %#v", got)
	}

	root.mu.Lock()
	root.state = SessionIdle
	root.mu.Unlock()
	if err := root.runDelegateQuietWatchdogTick(lease, clock.Now()); err != nil {
		t.Fatalf("idle-owner quiet retry: %v", err)
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != delegateQuietAttentionID(lease) {
		t.Fatalf("boundary-retried quiet attention identities = %#v", got)
	}
}

func TestDelegateControllerFinalizationDrainsAdmittedQuietAttention(t *testing.T) {
	tests := []struct {
		name string
		mode delegateSettlementMode
	}{
		{name: "ordinary", mode: delegateSettlementOrdinary},
		{name: "terminal", mode: delegateSettlementTerminal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, controller, lease, clock := newStableQuietSupervisionHarness(t)
			clock.Advance(delegateQuietWindow)
			quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
			if err != nil || quiet == nil {
				t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
			}
			finalization, continueRun, err := controller.BeginFinalization(lease, test.mode)
			if err != nil || continueRun || finalization == nil {
				t.Fatalf("BeginFinalization = claim:%#v continue:%t err:%v", finalization, continueRun, err)
			}
			select {
			case <-finalization.ready:
				t.Fatal("finalization became ready before admitted quiet attention completed")
			default:
			}
			if err := controller.CompleteQuietAttention(quiet, false); err != nil {
				t.Fatalf("CompleteQuietAttention: %v", err)
			}
			select {
			case <-finalization.ready:
			default:
				t.Fatal("finalization remained blocked after quiet attention aborted")
			}
		})
	}
}

func TestDelegateResourceSupervision_QuietAttentionFsyncPrecedesFinalization(t *testing.T) {
	tests := []struct {
		name string
		mode delegateSettlementMode
	}{
		{name: "ordinary", mode: delegateSettlementOrdinary},
		{name: "terminal", mode: delegateSettlementTerminal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, controller, lease, clock := newStableQuietSupervisionHarness(t)
			clock.Advance(delegateQuietWindow)
			quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
			if err != nil || quiet == nil {
				t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
			}
			finalization, continueRun, err := controller.BeginFinalization(lease, test.mode)
			if err != nil || continueRun || finalization == nil {
				t.Fatalf("BeginFinalization = claim:%#v continue:%t err:%v", finalization, continueRun, err)
			}

			var prematureErr error
			if test.mode == delegateSettlementOrdinary {
				_, prematureErr = controller.CompleteSettlement(finalization, nil)
			} else {
				_, prematureErr = controller.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "provider_failed"})
			}
			if !errors.Is(prematureErr, errDelegateTargetBusy) {
				t.Fatalf("finalization before quiet fsync error = %v, want target busy", prematureErr)
			}

			deferred, err := root.appendQuietAttentionAtTurnBoundary(quiet.attentionID, quiet.content)
			if err != nil || deferred {
				t.Fatalf("appendQuietAttentionAtTurnBoundary = deferred:%t err:%v", deferred, err)
			}
			if err := controller.CompleteQuietAttention(quiet, true); err != nil {
				t.Fatalf("CompleteQuietAttention after fsync: %v", err)
			}
			select {
			case <-finalization.ready:
			default:
				t.Fatal("finalization remained blocked after quiet attention fsync")
			}
			if test.mode == delegateSettlementOrdinary {
				if _, err := controller.CompleteSettlement(finalization, nil); err != nil {
					t.Fatalf("CompleteSettlement after quiet fsync: %v", err)
				}
			}
			if _, err := controller.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "provider_failed"}); err != nil {
				t.Fatalf("FinishGeneration after quiet fsync: %v", err)
			}
			if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != quiet.attentionID {
				t.Fatalf("durable quiet attention after finalization = %#v, want %q", got, quiet.attentionID)
			}
		})
	}
}

func TestDelegateResourceSupervision_FinalizationRejectsLaterQuietAttention(t *testing.T) {
	tests := []struct {
		name string
		mode delegateSettlementMode
	}{
		{name: "ordinary", mode: delegateSettlementOrdinary},
		{name: "terminal", mode: delegateSettlementTerminal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, controller, lease, clock := newStableQuietSupervisionHarness(t)
			clock.Advance(delegateQuietWindow)
			finalization, continueRun, err := controller.BeginFinalization(lease, test.mode)
			if err != nil || continueRun || finalization == nil {
				t.Fatalf("BeginFinalization = claim:%#v continue:%t err:%v", finalization, continueRun, err)
			}
			quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
			if quiet != nil || !errors.Is(err, errDelegateTargetBusy) {
				t.Fatalf("BeginQuietAttention after finalization = claim:%#v err:%v, want target busy", quiet, err)
			}
		})
	}
}

func TestDelegateResourceSupervision_QuietAttentionClaimDrainsBeforeStopCompletion(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(delegateQuietWindow)
	quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
	if err != nil || quiet == nil {
		t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
	}
	finalization, continueRun, err := controller.BeginFinalization(lease, delegateSettlementTerminal)
	if err != nil || continueRun || finalization == nil {
		t.Fatalf("BeginFinalization = claim:%#v continue:%t err:%v", finalization, continueRun, err)
	}
	select {
	case <-finalization.ready:
		t.Fatal("finalization became ready before admitted quiet attention completed")
	default:
	}
	result, _, _, err := controller.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile with quiet claim: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed before its pre-admitted quiet claim drained")
	default:
	}
	deferred, err := root.appendQuietAttentionAtTurnBoundary(quiet.attentionID, quiet.content)
	if err != nil || deferred {
		t.Fatalf("appendQuietAttentionAtTurnBoundary = deferred:%t err:%v", deferred, err)
	}
	if err := controller.CompleteQuietAttention(quiet, true); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("CompleteQuietAttention after stop = %v, want stale lease after durable drain", err)
	}
	select {
	case <-finalization.ready:
	default:
		t.Fatal("finalization remained blocked after stopped quiet attention drained")
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile while finalization owns generation: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed before finalization recorded stopped outcome")
	default:
	}
	if _, err := controller.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration after quiet drain: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile after finalization: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after quiet drain and stopped finalization")
	}
	if got := pendingQuietAttention(t, root); len(got) != 1 || got[0] != quiet.attentionID {
		t.Fatalf("durable quiet attention after stop = %#v, want %q", got, quiet.attentionID)
	}
}

func TestDelegateControllerOrdinaryFinalizationAdoptsExactCoveringStop(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(delegateQuietWindow)
	quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
	if err != nil || quiet == nil {
		t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
	}
	result, _, _, err := controller.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}

	finalization, continueRun, err := controller.BeginFinalization(lease, delegateSettlementOrdinary)
	if err != nil || continueRun || finalization == nil {
		t.Fatalf("BeginFinalization after covered stop = claim:%#v continue:%t err:%v", finalization, continueRun, err)
	}
	if finalization.mode != delegateSettlementTerminal {
		t.Fatalf("effective finalization mode = %d, want terminal stop", finalization.mode)
	}
	select {
	case <-finalization.ready:
		t.Fatal("stopped finalization became ready before admitted quiet attention completed")
	default:
	}
	if err := controller.CompleteQuietAttention(quiet, false); err != nil {
		t.Fatalf("CompleteQuietAttention: %v", err)
	}
	select {
	case <-finalization.ready:
	default:
		t.Fatal("stopped finalization remained blocked after quiet attention aborted")
	}
	if _, err := controller.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration after quiet drain: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile after stopped finalization: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after its exact generation finalized")
	}
	controller.mu.Lock()
	aggregate := controller.durable[lease.delegateID]
	live := controller.live[lease.delegateID]
	stop := controller.stop
	controller.mu.Unlock()
	if aggregate == nil || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("durable outcome = %#v, want stopped", aggregate)
	}
	if live == nil || live.binding != nil || stop != nil {
		t.Fatalf("released state = live:%#v stop:%#v, want no binding or active stop", live, stop)
	}
}

func TestDelegateControllerOrdinaryFinalizationRetainsModeWhenItPrecedesStop(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(delegateQuietWindow)
	quiet, err := controller.BeginQuietAttention(root, lease, clock.Now())
	if err != nil || quiet == nil {
		t.Fatalf("BeginQuietAttention = %#v, %v", quiet, err)
	}
	finalization, continueRun, err := controller.BeginFinalization(lease, delegateSettlementOrdinary)
	if err != nil || continueRun || finalization == nil {
		t.Fatalf("BeginFinalization before stop = claim:%#v continue:%t err:%v", finalization, continueRun, err)
	}
	result, _, _, err := controller.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if finalization.mode != delegateSettlementOrdinary {
		t.Fatalf("effective finalization mode = %d, want ordinary winner", finalization.mode)
	}
	if err := controller.CompleteQuietAttention(quiet, false); err != nil {
		t.Fatalf("CompleteQuietAttention: %v", err)
	}
	if _, err := controller.CompleteSettlement(finalization, nil); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("CompleteSettlement after stop = %v, want target busy", err)
	}
	if _, err := controller.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration after quiet drain: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile after stopped finalization: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after ordinary finalization winner drained")
	}
}

func TestDelegateResourceSupervision_StopBeforeOrdinaryFinalizationDrainsQuietAttention(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return finalResponse("ordinary result before covered stop")
	}}
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	child := root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	lease := delegateLease{delegateID: fixture.delegateID, generation: 1}
	type boundaryResult struct {
		quiet *delegateQuietAttentionClaim
		stop  delegateStopResult
		err   error
	}
	boundary := make(chan boundaryResult, 1)
	var boundaryOnce sync.Once
	child.sess.cfg.testOnly.subagentBeforeSettlement = func(*subagent) {
		boundaryOnce.Do(func() {
			root.delegateController.mu.Lock()
			live := root.delegateController.live[lease.delegateID]
			activityAt := live.activityAt
			root.delegateController.mu.Unlock()
			quiet, err := root.delegateController.BeginQuietAttention(root, lease, activityAt.Add(delegateQuietWindow))
			if err != nil || quiet == nil {
				boundary <- boundaryResult{err: fmt.Errorf("BeginQuietAttention = %#v, %w", quiet, err)}
				return
			}
			stop, _, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.delegateRootSessionID), lease.delegateID)
			boundary <- boundaryResult{quiet: quiet, stop: stop, err: err}
		})
	}
	close(release)
	observed := <-boundary
	if observed.err != nil {
		t.Fatalf("stop-before-finalization boundary: %v", observed.err)
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	root.delegateController.mu.Lock()
	stop := root.delegateController.stop
	root.delegateController.mu.Unlock()
	if stop == nil || stop.requestSeq != observed.stop.requestSeq {
		t.Fatalf("active stop = %#v, want request %d", stop, observed.stop.requestSeq)
	}

	select {
	case <-done:
		root.delegateController.mu.Lock()
		_, active := stop.active[lease]
		live := root.delegateController.live[lease.delegateID]
		bindingRetained := live != nil && live.binding != nil && live.binding.lease == lease
		root.delegateController.mu.Unlock()
		if err := root.delegateController.CompleteQuietAttention(observed.quiet, false); err != nil {
			t.Fatalf("cleanup CompleteQuietAttention: %v", err)
		}
		if _, err := root.delegateController.FinishGeneration(lease, delegateFinish{}); err != nil {
			t.Fatalf("cleanup FinishGeneration: %v", err)
		}
		if _, err := root.delegateController.Reconcile(emptyDelegateReconcileEvidence(root.delegateController)); err != nil {
			t.Fatalf("cleanup Reconcile: %v", err)
		}
		t.Fatalf("ordinary runner exited before quiet completion; stop active = %t, binding retained = %t", active, bindingRetained)
	case <-stop.progress:
	}
	select {
	case <-done:
		t.Fatal("ordinary runner exited after claiming the stop but before quiet completion")
	default:
	}
	if err := root.delegateController.CompleteQuietAttention(observed.quiet, false); err != nil {
		t.Fatalf("CompleteQuietAttention: %v", err)
	}
	<-done
	if _, err := root.delegateController.Reconcile(emptyDelegateReconcileEvidence(root.delegateController)); err != nil {
		t.Fatalf("Reconcile after stopped finalization: %v", err)
	}
	select {
	case <-observed.stop.done:
	default:
		t.Fatal("stop remained pending after runner drained quiet attention")
	}
	assertStableSupervisionOutcome(t, root, fixture.delegateID, delegatestore.OutcomeStopped)
}

func TestDelegateResourceSupervision_RestartStartsNoWatchdogOrProvider(t *testing.T) {
	clock := agenttest.NewFakeClock()
	controller, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, controller, "dlg_target", "")
	if err := controller.store.Close(); err != nil {
		t.Fatalf("close controller store: %v", err)
	}
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen delegate store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root-session",
		now:           clock.Now,
	}); err != nil {
		t.Fatalf("reopen controller: %v", err)
	}
	clock.Advance(24 * time.Hour)
	if clock.BlockedCount() != 0 {
		t.Fatalf("restart armed %d watchdog/provider waiters", clock.BlockedCount())
	}
}

type stableSubagentStopObservation struct {
	providerRequests    int
	hookRuns            int
	continuationSawHook bool
	output              string
}

func runStableSubagentStopHook(t *testing.T, blocking bool) stableSubagentStopObservation {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "subagent-stop-runs")
	decision := `{}`
	if blocking {
		decision = `{"decision":"block","reason":"address hook feedback"}`
	}
	pluginDir := writeStableSubagentStopPlugin(t, marker, decision)
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
	})
	continuationSawHook := false
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("initial result") },
	}
	if blocking {
		fixture.adapter.steps = append(fixture.adapter.steps, func(llm.Request) llm.Response {
			_, err := os.Stat(marker)
			continuationSawHook = err == nil
			return finalResponse("continued after hook")
		})
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
	if outcome.result.Err != nil {
		t.Fatalf("stable hook run: %v", outcome.result.Err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read hook marker: %v", err)
	}
	return stableSubagentStopObservation{
		providerRequests:    supervisionRequestCount(fixture.adapter),
		hookRuns:            len(strings.Fields(string(raw))),
		continuationSawHook: continuationSawHook,
		output:              outcome.result.Output,
	}
}

func writeStableSubagentStopPlugin(t *testing.T, marker, decision string) string {
	t.Helper()
	pluginDir := makePluginDir(t, "task7-supervision")
	hooksDir := filepath.Join(pluginDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	command := "printf 'run\\n' >> " + shellQuote(marker) + "; printf '%s' " + shellQuote(decision)
	payload := map[string]any{"hooks": map[string]any{"SubagentStop": []any{map[string]any{
		"matcher": "*",
		"hooks":   []any{map[string]any{"type": "command", "command": command}},
	}}}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return pluginDir
}

func restoreSupervisionRoot(t *testing.T, fixture coldStableDelegateFixture, clock *agenttest.FakeClock) *Session {
	t.Helper()
	restore := RestoreSessionConfig{
		StateDir:    fixture.stateDir,
		ForceRealIO: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
		},
	}
	if clock != nil {
		restore.clock = clock
	}
	root, err := RestoreSessionFromMetaWithConfig(
		fixture.client,
		fixture.profile,
		execenv.NewLocalExecutionEnvironment(fixture.workspace),
		fixture.meta,
		restore,
	)
	if err != nil {
		t.Fatalf("restore supervision root: %v", err)
	}
	t.Cleanup(root.Close)
	return root
}

func waitForStableSupervisionRun(t *testing.T, root *Session, childID string) {
	t.Helper()
	sub := root.subagents.get(childID)
	if sub == nil {
		t.Fatalf("stable child %q was not tracked", childID)
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	if done == nil {
		t.Fatalf("stable child %q has no completion channel", childID)
	}
	<-done
}

func supervisionRequestCount(adapter *fakeAdapter) int {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return len(adapter.requests)
}

func containsDelegateSalvageWarning(warnings []string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, "partial draft salvaged") && strings.Contains(warning, "delegate_send") {
			return true
		}
	}
	return false
}

func packetWarnings(packet *delegatestore.TerminalPacket) []string {
	if packet == nil {
		return nil
	}
	return packet.Warnings
}

func newStableQuietSupervisionHarness(t *testing.T) (*Session, *delegateTreeController, delegateLease, *agenttest.FakeClock) {
	t.Helper()
	clock := agenttest.NewFakeClockAt(time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC))
	root := newDelegateAttentionTestSession(t)
	root.clock = clock
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	controller.rootRuntime = root
	root.delegateController = controller
	seedDelegateControllerRunning(t, controller, "dlg_target", "")
	child := &Session{clock: clock, delegateController: controller}
	controller.live["dlg_target"].runtime = child
	controller.live["dlg_target"].binding.runtime = child
	controller.live["dlg_target"].activityAt = clock.Now()
	return root, controller, delegateLease{delegateID: "dlg_target", generation: 1}, clock
}

func pendingQuietAttention(t *testing.T, root *Session) []string {
	t.Helper()
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.id), root.id)
	if err != nil {
		t.Fatalf("read quiet attention: %v", err)
	}
	return append([]string(nil), fold.order...)
}
