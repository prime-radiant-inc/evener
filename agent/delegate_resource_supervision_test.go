package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
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
	if requestMessagesContain(requests[4], communicateNudge("communicate")) {
		t.Fatalf("pending steer was delayed behind auto-nudge: %#v", requests[4].Messages)
	}
	if !requestMessagesContain(requests[4], "priority steering") {
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
	if len(requests) != 9 || !requestMessagesContain(requests[8], "late steering at ordinary settlement") {
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

func TestDelegateResourceSupervision_FatalNudgeRunStopsOwnedShell(t *testing.T) {
	worktreeClient := llm.NewClient()
	worktreeClient.Register(&fakeAdapter{name: "openai"})
	worktreeRepo := newWtDlgRepo(t, worktreeClient)
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
	signaled := make(chan struct{}, 1)
	ownedShell := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:          "job_fatal_nudge_owned_shell",
			Type:           jobstore.JobShell,
			Status:         jobstore.StatusRunning,
			OwnerSessionID: sub.sess.ID(),
		},
		signal: func() {
			if err := os.WriteFile(cleanupMarker, []byte("cleaned\n"), 0o644); err != nil {
				panic(err)
			}
			signaled <- struct{}{}
		},
		done:           make(chan struct{}),
		durableStarted: true,
	}
	sub.sess.jobManager.mu.Lock()
	sub.sess.jobManager.running[ownedShell.rec.JobID] = ownedShell
	sub.sess.jobManager.mu.Unlock()
	t.Cleanup(func() {
		sub.sess.jobManager.mu.Lock()
		delete(sub.sess.jobManager.running, ownedShell.rec.JobID)
		sub.sess.jobManager.mu.Unlock()
		ownedShell.closeDone()
	})
	close(releaseFatalNudge)
	<-finalStatePublished
	select {
	case <-signaled:
	default:
		t.Fatal("fatal stable nudge run published final state before stopping its owned shell")
	}
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

func TestDelegateResourceSupervision_OrdinaryMissingTerminalCleanupPrecedesPacketEvidence(t *testing.T) {
	worktreeClient := llm.NewClient()
	worktreeClient.Register(&fakeAdapter{name: "openai"})
	worktreeRepo := newWtDlgRepo(t, worktreeClient)
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
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	signaled := make(chan struct{}, 1)
	ownedShell := &runningJob{
		rec: &jobstore.JobRecord{
			JobID:          "job_ordinary_owned_shell",
			Type:           jobstore.JobShell,
			Status:         jobstore.StatusRunning,
			OwnerSessionID: sub.sess.ID(),
		},
		signal: func() {
			close(cleanupStarted)
			<-releaseCleanup
			if err := os.WriteFile(cleanupMarker, []byte("cleaned\n"), 0o644); err != nil {
				panic(err)
			}
			signaled <- struct{}{}
		},
		done:           make(chan struct{}),
		durableStarted: true,
	}
	sub.sess.jobManager.mu.Lock()
	sub.sess.jobManager.running[ownedShell.rec.JobID] = ownedShell
	sub.sess.jobManager.mu.Unlock()
	t.Cleanup(func() {
		sub.sess.jobManager.mu.Lock()
		delete(sub.sess.jobManager.running, ownedShell.rec.JobID)
		sub.sess.jobManager.mu.Unlock()
		ownedShell.closeDone()
	})
	close(releaseInitialRequest)
	<-cleanupStarted
	lateSteer := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "steer after ordinary settlement claim", 0)
	if !errors.Is(lateSteer.result.Err, errDelegateTargetBusy) {
		t.Fatalf("steer after ordinary settlement claim error = %v, want target busy", lateSteer.result.Err)
	}
	close(releaseCleanup)
	<-finalStatePublished
	select {
	case <-signaled:
	default:
		t.Fatal("ordinary missing-terminal run published final state before stopping its owned shell")
	}
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

func TestDelegateResourceSupervision_QuietAttentionClaimDrainsBeforeStopCompletion(t *testing.T) {
	root, controller, lease, clock := newStableQuietSupervisionHarness(t)
	clock.Advance(10 * time.Minute)
	claim, err := controller.BeginQuietAttention(root, lease, clock.Now())
	if err != nil || claim == nil {
		t.Fatalf("BeginQuietAttention = %#v, %v", claim, err)
	}
	result, _, _, err := controller.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := controller.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile with quiet claim: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed before its pre-admitted quiet claim drained")
	default:
	}
	if err := controller.CompleteQuietAttention(claim, false); err != nil {
		t.Fatalf("CompleteQuietAttention after generation finish: %v", err)
	}
	if _, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller)); err != nil {
		t.Fatalf("Reconcile after quiet claim: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after its quiet claim drained")
	}
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
