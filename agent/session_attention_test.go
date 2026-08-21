package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func TestDelegateAttention_ResolutionFsyncPrecedesSourceAck(t *testing.T) {
	const (
		sessionID = "child-dlg_target"
		firstID   = "delegate:delivery-attention-1"
		secondID  = "delegate:delivery-attention-2"
	)
	fs := newAttentionSyncBarrierFS()
	c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, fs, firstID, secondID)
	beforeRevision := c.durable["dlg_target"].ProjectionRevision
	reservation, err := c.ReserveAttention(runtime, firstID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}

	fs.arm()
	type startResult struct {
		started delegateStartCommit
		err     error
	}
	done := make(chan startResult, 1)
	go func() {
		startErr := runtime.acceptDelegateAttention(reservation)
		var started delegateStartCommit
		if startErr == nil {
			started, startErr = c.CommitStart(reservation)
		}
		done <- startResult{started: started, err: startErr}
	}()
	select {
	case <-fs.syncEntered:
	case result := <-done:
		t.Fatalf("attention acceptance returned before fsync: %#v", result)
	}
	c.mu.Lock()
	blockedAggregate := c.durable["dlg_target"]
	blockedGeneration := blockedAggregate.Generation
	blockedNeedsAttention := blockedAggregate.NeedsAttention
	_, firstStillUnresolved := c.attentionWakeIDs["dlg_target"][firstID]
	c.mu.Unlock()
	if blockedGeneration != 0 || !blockedNeedsAttention || !firstStillUnresolved {
		t.Fatalf("acceptance published before resolution fsync: generation=%d needs=%t unresolved=%#v", blockedGeneration, blockedNeedsAttention, c.attentionWakeIDs["dlg_target"])
	}
	fs.release()
	first := <-done
	if first.err != nil {
		t.Fatalf("accept first attention: %v", first.err)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.Generation != 1 || aggregate.Trigger != delegatestore.TriggerAttention || !aggregate.CurrentRunOpen || !aggregate.NeedsAttention || aggregate.ProjectionRevision != beforeRevision+1 {
		t.Fatalf("nonfinal acceptance aggregate = %#v, want generation 1 open with unchanged true attention", aggregate)
	}
	if got := c.attentionWakeIDs["dlg_target"]; !reflect.DeepEqual(got, map[string]struct{}{secondID: {}}) {
		t.Fatalf("unresolved IDs after nonfinal consumption = %#v", got)
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read attention fold: %v", err)
	}
	if got := fold.resolutions[firstID]; got != delegateAttentionConsumed || fold.resumeGenerations[firstID] != 1 {
		t.Fatalf("first resolution = %q generation %d, want consumed/1", got, fold.resumeGenerations[firstID])
	}
	if _, err := c.FinishGeneration(first.started.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "first handled"}); err != nil {
		t.Fatalf("FinishGeneration first: %v", err)
	}
	secondReservation, err := c.ReserveAttention(runtime, secondID)
	if err != nil {
		t.Fatalf("ReserveAttention second: %v", err)
	}
	if err := runtime.acceptDelegateAttention(secondReservation); err != nil {
		t.Fatalf("accept final attention: %v", err)
	}
	second, err := c.CommitStart(secondReservation)
	if err != nil {
		t.Fatalf("commit final attention: %v", err)
	}
	aggregate = c.durable["dlg_target"]
	if aggregate.Generation != 2 || aggregate.NeedsAttention || len(c.attentionWakeIDs["dlg_target"]) != 0 {
		t.Fatalf("final acceptance aggregate=%#v unresolved=%#v", aggregate, c.attentionWakeIDs["dlg_target"])
	}
	fold, err = readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read final attention fold: %v", err)
	}
	if got := fold.resolutions[secondID]; got != delegateAttentionConsumed || fold.resumeGenerations[secondID] != 2 {
		t.Fatalf("second resolution = %q generation %d, want consumed/2", got, fold.resumeGenerations[secondID])
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tail := events[len(events)-2:]
	if tail[0].Kind != delegatestore.EventDelegateAttentionChanged || tail[0].AttentionChanged == nil || tail[0].AttentionChanged.NeedsAttention || tail[1].Kind != delegatestore.EventDelegateRunStarted || tail[1].RunStarted.Generation != second.lease.generation {
		t.Fatalf("final-clear/run-start batch tail = %#v", tail)
	}
}

func TestDelegateAttention_StopLeavesBoundAttentionForDiscard(t *testing.T) {
	t.Run("stop wins before acceptance", func(t *testing.T) {
		const (
			sessionID   = "child-dlg_target"
			attentionID = "delegate:delivery-stopped"
		)
		c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
		if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
			t.Fatalf("StopSubtree: %v", err)
		}
		if _, err := c.ReserveAttention(runtime, attentionID); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("ReserveAttention after stop = %v, want target busy", err)
		}
		fold, err := readDelegateAttentionFold(path, sessionID)
		if err != nil {
			t.Fatalf("read attention fold: %v", err)
		}
		if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
			t.Fatalf("pending attention for stop discard = %#v", pending)
		}
		if c.durable["dlg_target"].NeedsAttention {
			t.Fatal("lifecycle stop failed to fold attention false")
		}
		events, err := c.store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if events[len(events)-1].Kind != delegatestore.EventDelegateSubtreeStopRequested {
			t.Fatalf("lifecycle clear appended a separate attention event: %#v", events[len(events)-2:])
		}
	})

	t.Run("stop waits after acceptance admission", func(t *testing.T) {
		const (
			sessionID   = "child-dlg_target"
			attentionID = "delegate:delivery-admitted"
		)
		fs := newAttentionSyncBarrierFS()
		c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, fs, attentionID)
		reservation, err := c.ReserveAttention(runtime, attentionID)
		if err != nil {
			t.Fatalf("ReserveAttention: %v", err)
		}
		fs.arm()
		accepted := make(chan error, 1)
		go func() {
			acceptErr := runtime.acceptDelegateAttention(reservation)
			if acceptErr == nil {
				_, acceptErr = c.CommitStart(reservation)
			}
			accepted <- acceptErr
		}()
		<-fs.syncEntered
		stopped := make(chan error, 1)
		go func() {
			_, _, _, stopErr := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
			stopped <- stopErr
		}()
		select {
		case err := <-stopped:
			t.Fatalf("stop returned before admitted acceptance committed: %v", err)
		default:
		}
		fs.release()
		if err := <-accepted; err != nil {
			t.Fatalf("accept admitted attention: %v", err)
		}
		if err := <-stopped; err != nil {
			t.Fatalf("StopSubtree after acceptance: %v", err)
		}
		fold, err := readDelegateAttentionFold(path, sessionID)
		if err != nil {
			t.Fatalf("read admitted attention fold: %v", err)
		}
		if fold.resolutions[attentionID] != delegateAttentionConsumed || fold.resumeGenerations[attentionID] != 1 {
			t.Fatalf("admitted resolution = %q generation %d", fold.resolutions[attentionID], fold.resumeGenerations[attentionID])
		}
		events, err := c.store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if events[len(events)-2].Kind != delegatestore.EventDelegateRunStarted || events[len(events)-1].Kind != delegatestore.EventDelegateSubtreeStopRequested {
			t.Fatalf("acceptance/stop ordering = %#v", events[len(events)-2:])
		}
	})
}

func newDelegateAttentionAcceptanceHarness(t *testing.T, sessionID string, fs afero.Fs, attentionIDs ...string) (*delegateTreeController, *Session, string) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := transcriptPath(c.stateDir, sessionID)
	var writer *transcript.Writer
	var err error
	if fs == nil {
		writer, err = transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	} else {
		writer, err = transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	}
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	for _, attentionID := range attentionIDs {
		attention := schema.NewTurn(schema.TurnSteering, llm.User("durable attention "+attentionID))
		attention.AttentionID = attentionID
		if err := writer.AppendDurable(attention); err != nil {
			t.Fatalf("append attention %q: %v", attentionID, err)
		}
	}
	runtime := &Session{id: sessionID, stateDir: c.stateDir, delegateController: c}
	runtime.attachTranscript(writer)
	c.mu.Lock()
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionChanged,
		DelegateID: "dlg_target",
		AttentionChanged: &delegatestore.DelegateAttentionChanged{
			NeedsAttention: true,
		},
	}); err != nil {
		c.mu.Unlock()
		t.Fatalf("append attention projection: %v", err)
	}
	c.live["dlg_target"] = &delegateLiveState{runtime: runtime}
	for _, attentionID := range attentionIDs {
		c.noteDelegateAttentionLocked("dlg_target", attentionID)
	}
	c.mu.Unlock()
	return c, runtime, path
}

func TestDelegateAttention_ResolutionFailureLeavesGenerationStoppable(t *testing.T) {
	const (
		sessionID   = "child-dlg_target"
		attentionID = "attention-resolution-failure"
	)
	fs := newAttentionAmbiguousSyncFS()
	c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, fs, attentionID)
	reservation, err := c.ReserveAttention(runtime, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	fs.failNextResolutionDurability()
	if err := runtime.acceptDelegateAttention(reservation); err == nil {
		t.Fatal("consumed attention resolution unexpectedly survived injected sync failure")
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read ambiguous attention resolution: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed || fold.resumeGenerations[attentionID] != 1 {
		t.Fatalf("page-cache-visible attention resolution = %q generation %d, want consumed/1", got, fold.resumeGenerations[attentionID])
	}
	if aggregate := c.durable["dlg_target"]; aggregate.Generation != 0 || aggregate.CurrentRunOpen || !aggregate.NeedsAttention {
		t.Fatalf("failed resolution admitted generation: %#v", aggregate)
	}
	if len(c.reservations) != 1 || c.live["dlg_target"].binding != nil {
		t.Fatalf("failed resolution lost retry claim or installed binding: reservations=%#v live=%#v", c.reservations, c.live["dlg_target"])
	}
	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("retry ambiguous attention resolution: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("commit retried attention generation: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("attention retry established no successful child-transcript durability barrier")
	}
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree after recovered acceptance: %v", err)
	}
	if aggregate := c.durable["dlg_target"]; aggregate.Phase != delegatestore.PhaseStopping || aggregate.Generation != started.lease.generation {
		t.Fatalf("recovered attention generation is not stoppable: %#v", aggregate)
	}
}

func TestDelegateAttention_RecoveryStopWaitsForOldRunnerBeforeReuse(t *testing.T) {
	providerEntered := make(chan struct{})
	releaseProvider := make(chan struct{})
	successorEntered := make(chan struct{})
	releaseSuccessor := make(chan struct{})
	var releaseProviderOnce sync.Once
	var releaseSuccessorOnce sync.Once
	t.Cleanup(func() {
		releaseProviderOnce.Do(func() { close(releaseProvider) })
		releaseSuccessorOnce.Do(func() { close(releaseSuccessor) })
	})

	fixture := newColdStableDelegateFixture(t, "")
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				close(providerEntered)
				<-releaseProvider
				return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "terminal provider failure", nil, nil)
			},
			func(llm.Request) (llm.Response, error) {
				close(successorEntered)
				<-releaseSuccessor
				return finalResponse("successor completed after recovery"), nil
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start recovery-fenced run", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-providerEntered
	child := root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	child.mu.Lock()
	oldDone := child.done
	child.mu.Unlock()

	finalStatePublished := make(chan struct{})
	releaseFinalizer := make(chan struct{})
	var releaseFinalizerOnce sync.Once
	t.Cleanup(func() { releaseFinalizerOnce.Do(func() { close(releaseFinalizer) }) })
	recoveryErr := make(chan error, 1)
	child.sess.cfg.testOnly.subagentAfterFinalStatePublish = func(*subagent) {
		controller := root.delegateController
		lease := delegateLease{delegateID: fixture.delegateID, generation: 1}
		controller.mu.Lock()
		var claim *delegateSettlementClaim
		for _, candidate := range controller.settlementClaims {
			if candidate != nil && candidate.lease == lease {
				claim = candidate
				break
			}
		}
		controller.mu.Unlock()
		if claim == nil {
			recoveryErr <- errors.New("terminal runner published without its exact settlement claim")
		} else {
			recoveryErr <- controller.RequireFinalizationRecovery(claim)
		}
		close(finalStatePublished)
		<-releaseFinalizer
	}
	releaseProviderOnce.Do(func() { close(releaseProvider) })
	<-finalStatePublished
	if err := <-recoveryErr; err != nil {
		t.Fatalf("latch exact finalization recovery: %v", err)
	}

	controller := root.delegateController
	stopResult, cancelPlan, stopPlans, err := controller.StopSubtree(rootDelegateActor(root.delegateRootSessionID), fixture.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	if err := root.executeDelegateMutationPlans(stopPlans); err != nil {
		t.Fatalf("execute stop plans: %v", err)
	}
	for i := range 2 {
		evidence, err := collectDelegateReconcileEvidence(fixture.stateDir, controller.ReconcileRequirements())
		if err != nil {
			t.Fatalf("collect recovery evidence %d: %v", i, err)
		}
		plans, err := controller.Reconcile(evidence)
		if err != nil {
			t.Fatalf("reconcile paused recovery runner %d: %v", i, err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatalf("execute paused recovery plans %d: %v", i, err)
		}
	}
	controller.mu.Lock()
	aggregate := controller.durable[fixture.delegateID]
	stop := controller.stop
	controller.mu.Unlock()
	if aggregate == nil || !aggregate.CurrentRunOpen {
		t.Fatalf("recovery stop released generation before its old runner quiesced: %#v", aggregate)
	}
	select {
	case <-stopResult.done:
		t.Fatal("recovery stop completed before its old runner quiesced")
	default:
	}
	if stop == nil {
		t.Fatal("recovery stop disappeared while its old runner was paused")
	}

	for {
		select {
		case <-stop.progress:
			continue
		default:
			goto progressDrained
		}
	}

progressDrained:
	releaseFinalizerOnce.Do(func() { close(releaseFinalizer) })
	<-oldDone
	<-stop.progress
	child.sess.cfg.testOnly.subagentAfterFinalStatePublish = nil
	for i := range 2 {
		evidence, err := collectDelegateReconcileEvidence(fixture.stateDir, controller.ReconcileRequirements())
		if err != nil {
			t.Fatalf("collect quiesced recovery evidence %d: %v", i, err)
		}
		plans, err := controller.Reconcile(evidence)
		if err != nil {
			t.Fatalf("reconcile quiesced recovery runner %d: %v", i, err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatalf("execute quiesced recovery plans %d: %v", i, err)
		}
	}
	select {
	case <-stopResult.done:
	default:
		t.Fatal("recovery stop remained pending after its old runner quiesced")
	}

	continued := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start successor after recovery", 0)
	if continued.result.Err != nil || continued.result.Action != "started" {
		t.Fatalf("start successor after recovery = %#v", continued.result)
	}
	<-successorEntered
	successor := root.subagents.get(fixture.childID)
	if successor != child {
		t.Fatalf("successor runtime = %p, want retained runtime %p", successor, child)
	}
	successor.mu.Lock()
	successorDone := successor.done
	successor.mu.Unlock()
	select {
	case <-successorDone:
		t.Fatal("old recovery runner prematurely closed the successor completion channel")
	default:
	}
	releaseSuccessorOnce.Do(func() { close(releaseSuccessor) })
	<-successorDone
}

func TestDelegateAttention_StabilizationRetryPreservesFailedBarrier(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "stabilization-retry"
		attentionID = "attention-stabilization-retry"
	)
	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	runtime := &Session{id: sessionID, stateDir: stateDir}
	runtime.attachTranscript(writer)
	t.Cleanup(func() { _ = runtime.closeAttachedTranscript() })
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	fs.failNextAmbiguousDurability(2)
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err == nil {
		t.Fatal("ambiguous resolution unexpectedly succeeded")
	}
	openCalls := 0
	runtime.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		openCalls++
		if openCalls == 1 {
			return nil, nil, errors.New("injected stabilization reopen failure")
		}
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	if err := runtime.stabilizeAttentionForStop(attentionID); err == nil {
		t.Fatal("first stabilization unexpectedly survived injected reopen failure")
	}
	if err := runtime.stabilizeAttentionForStop(attentionID); err == nil {
		t.Fatal("stabilization retry accepted the readable marker without retrying the failed durability barrier")
	}
	if err := runtime.stabilizeAttentionForStop(attentionID); err != nil {
		t.Fatalf("stabilization after recovered barrier: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("stabilization retry established no successful transcript barrier")
	}
}

func TestDelegateAttention_RecoveryRetainedRuntimePreservesFailedToolCount(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "stabilization-failure-count"
		attentionID = "attention-failure-count"
	)
	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	writer.SyncInterval = time.Hour
	writer.TrackFailures(nil, 0)
	runtime := &Session{id: sessionID, stateDir: stateDir}
	runtime.attachTranscript(writer)
	t.Cleanup(func() { _ = runtime.closeAttachedTranscript() })
	failed := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("failed-call-1", "probe", "failed", true))
	if err := writer.AppendDurable(failed); err != nil {
		t.Fatalf("append first failed result: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	fs.failNextResolutionDurability()
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err == nil {
		t.Fatal("ambiguous resolution unexpectedly succeeded")
	}
	runtime.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	if err := runtime.stabilizeAttentionForStop(attentionID); err != nil {
		t.Fatalf("stabilize attention: %v", err)
	}
	if got, measured := runtime.FailedToolCallsSnapshot(); !measured || got != 1 {
		t.Fatalf("failed-tool count after stabilization = %d measured=%v, want 1 true", got, measured)
	}
	syncsBeforeAppend := fs.successfulSyncsAfterFailure()
	failed = schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("failed-call-2", "probe", "failed again", true))
	if err := runtime.writeTranscript(failed); err != nil {
		t.Fatalf("append retained-runtime failed result: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got != syncsBeforeAppend {
		t.Fatalf("retained writer lost sync interval: successful syncs %d -> %d", syncsBeforeAppend, got)
	}
	if got, measured := runtime.FailedToolCallsSnapshot(); !measured || got != 2 {
		t.Fatalf("failed-tool count after retained-runtime append = %d measured=%v, want 2 true", got, measured)
	}
}

func TestDelegateAttention_ColdResolutionValidatesBatchBeforeAppend(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "cold-resolution"
		attentionID = "attention-present"
	)
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	err = appendColdAttentionResolution(path, sessionID, []string{attentionID, "attention-missing"}, delegateAttentionDiscarded)
	if err == nil {
		t.Fatal("cold resolution accepted a batch containing an unknown attention ID")
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read attention after rejected batch: %v", err)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("rejected batch partially resolved attention: %#v", pending)
	}
}

func TestDelegateAttention_ColdNotificationRetryReestablishesDurabilityBeforeSourceAck(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "cold-notification-durability"
		attentionID = "delegate:delivery-durability"
	)
	path := transcriptPath(stateDir, sessionID)
	fs := newAttentionAmbiguousSyncFS()
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close initial transcript: %v", err)
	}
	open := func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}

	fs.failNextAmbiguousDurability(2)
	if _, err := appendColdDelegateNotificationDurablyWithOpen(path, sessionID, attentionID, "durable packet", time.Unix(100, 0), open); err == nil {
		t.Fatal("ambiguous cold notification append unexpectedly succeeded")
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read ambiguous notification: %v", err)
	}
	if _, visible := fold.content[attentionID]; !visible {
		t.Fatal("fault fixture did not leave the notification readable")
	}
	if _, err := appendColdDelegateNotificationDurablyWithOpen(path, sessionID, attentionID, "durable packet", time.Unix(100, 0), open); err != nil {
		t.Fatalf("retry existing cold notification: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("existing cold notification was accepted without a renewed durability barrier")
	}
}

func TestDelegateAttention_ColdResolutionRetryReestablishesDurabilityBeforeStopAck(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "cold-resolution-durability"
		attentionID = "attention-durability"
	)
	path := transcriptPath(stateDir, sessionID)
	fs := newAttentionAmbiguousSyncFS()
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close initial transcript: %v", err)
	}
	open := func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}

	fs.failNextAmbiguousDurability(2)
	if err := appendColdAttentionResolutionWithOpen(path, sessionID, []string{attentionID}, delegateAttentionDiscarded, open); err == nil {
		t.Fatal("ambiguous cold resolution append unexpectedly succeeded")
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read ambiguous resolution: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("fault fixture resolution = %q, want readable discard", got)
	}
	if err := appendColdAttentionResolutionWithOpen(path, sessionID, []string{attentionID}, delegateAttentionDiscarded, open); err != nil {
		t.Fatalf("retry existing cold resolution: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("existing cold resolution was accepted without a renewed durability barrier")
	}
}

func TestDelegateAttention_ColdCallerCommitRequiresRenewedDurabilityBeforeSourceAck(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}

	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	commit := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-call", "delegate_send", "done", false))
	commit.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "delegate-call", DeliveryID: plans[0].deliveryID}}
	fs.failNextAmbiguousDurability(3)
	if err := writer.AppendDurable(commit); err == nil {
		t.Fatal("ambiguous caller commit append unexpectedly succeeded")
	}
	if err := writer.Close(); err == nil {
		t.Fatal("ambiguous caller commit close unexpectedly succeeded")
	}
	c.attentionOpen = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}

	if _, err := prepareColdDelegateDeliveryReplay(c, plans); err == nil {
		t.Fatal("cold replay acknowledged a readable caller commit without a renewed durability barrier")
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("failed caller-commit barrier acknowledged source head: remaining=%d", remaining)
	}
	if pending, err := prepareColdDelegateDeliveryReplay(c, plans); err != nil || len(pending) != 0 {
		t.Fatalf("retry caller-commit barrier = pending %#v err %v", pending, err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("caller commit source ack had no successful renewed durability barrier")
	}
}

func TestDelegateAttention_LiveNotificationRetryReestablishesDurabilityBeforeSourceAck(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c}
	root.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	root.attachTranscript(writer)
	c.rootRuntime = root
	t.Cleanup(func() { _ = root.attachedTranscript().Close() })

	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}
	fs.failNextAmbiguousDurability(2)
	if _, err := deliverDelegatePacket(plans[0], root); err == nil {
		t.Fatal("ambiguous live notification append unexpectedly succeeded")
	}
	plans = c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries after ambiguous append = %#v", plans)
	}
	if _, err := deliverDelegatePacket(plans[0], root); err == nil {
		t.Fatal("live retry source-acked readable notification without renewing the failed durability barrier")
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("failed live barrier acknowledged source head: remaining=%d", remaining)
	}
	plans = c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries after failed barrier = %#v", plans)
	}
	if _, err := deliverDelegatePacket(plans[0], root); err != nil {
		t.Fatalf("live notification retry after recovered barrier: %v", err)
	}
	c.mu.Lock()
	remaining = len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("live notification remained pending after recovered barrier: %d", remaining)
	}
	if err := root.writeTranscriptDurable(schema.NewTurn(schema.TurnUserInput, llm.User("ordinary turn after recovery"))); err != nil {
		t.Fatalf("append ordinary turn after recovery: %v", err)
	}
	entries := readAttentionTranscriptEntries(t, path)
	for i, entry := range entries {
		if entry.Seq != i {
			t.Fatalf("entry %d sequence after live recovery = %d, want %d", i, entry.Seq, i)
		}
	}
}

func TestDelegateAttention_LiveResolutionRetryReestablishesDurabilityBeforeSettlement(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "live-resolution-durability"
		attentionID = "attention-live-resolution"
	)
	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	runtime := &Session{id: sessionID, stateDir: stateDir}
	runtime.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	runtime.attachTranscript(writer)
	t.Cleanup(func() { _ = runtime.attachedTranscript().Close() })
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	fs.failNextAmbiguousDurability(2)
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err == nil {
		t.Fatal("ambiguous live resolution unexpectedly succeeded")
	}
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err == nil {
		t.Fatal("live resolution retry accepted readable marker without renewing the failed durability barrier")
	}
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("live resolution after recovered barrier: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("live resolution settlement had no successful renewed barrier")
	}
}

func TestDelegateAttention_ResolutionMarkerDoesNotSplitToolCallAndResult(t *testing.T) {
	const callID = "call-with-resolution"
	for _, test := range []struct {
		name           string
		preserveRecent int
	}{
		{name: "cutoff on result", preserveRecent: 2},
		{name: "cutoff on resolution", preserveRecent: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			attention := schema.NewTurn(schema.TurnSteering, llm.User("delegate completed"))
			attention.AttentionID = "delegate:delivery-1"
			history := []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("task")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("earlier")),
				attention,
				schema.NewTurn(schema.TurnAssistant, delegateAttentionToolCall(callID)),
				delegateAttentionResolutionTurn(attention.AttentionID, delegateAttentionConsumed),
				schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "real result", false)),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent")),
			}
			manager := contextmgr.NewManager(NewOpenAIProfile("gpt-5.2"), nil)
			manager.PreserveRecentTurns = test.preserveRecent
			manager.ForceCompact(context.Background(), &history, "", func(events.EventKind, events.EventData) {})

			messages := expandHistory(history, replayScope{})
			callIndex, resultIndex, resultCount := -1, -1, 0
			for i, message := range messages {
				if message.Text() == "Attention resolved." {
					t.Fatalf("resolution marker reached provider history: %#v", messages)
				}
				for _, part := range message.Content {
					if part.ToolCall != nil && part.ToolCall.ID == callID {
						callIndex = i
					}
					if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
						resultIndex = i
						resultCount++
						if part.ToolResult.IsError || part.ToolResult.Content != "real result" {
							t.Fatalf("projected tool result = %#v, want real non-error result", part.ToolResult)
						}
					}
				}
			}
			if callIndex < 0 || resultIndex != callIndex+1 || resultCount != 1 {
				t.Fatalf("projected exchange call=%d result=%d count=%d; messages=%#v", callIndex, resultIndex, resultCount, messages)
			}
		})
	}
}

func TestDelegateAttention_PublicTranscriptReadsExcludePrivateResolutionMetadata(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "public-attention-read"
		attentionID = "private-attention-correlation"
		deliveryID  = "private-delivery-correlation"
		callID      = "public-tool-call"
	)
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	attentionContent, err := delegateNotificationContent(delegateDeliveryPlan{
		delegateID: "dlg_public_source",
		deliveryID: deliveryID,
		packet:     delegateControllerReportedPacket("visible attention content"),
	})
	if err != nil {
		t.Fatalf("render delegate notification: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User(attentionContent))
	attention.AttentionID = attentionID
	turns := []schema.Turn{
		attention,
		schema.NewTurn(schema.TurnAssistant, delegateAttentionToolCall(callID)),
		delegateAttentionResolutionTurn(attentionID, delegateAttentionConsumed),
	}
	results := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "visible result", false))
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: callID, DeliveryID: deliveryID}}
	turns = append(turns, results)
	for _, turn := range turns {
		if err := writer.AppendDurable(turn); err != nil {
			_ = writer.Close()
			t.Fatalf("append transcript turn: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	rawValue, err := readRaw(path, "local:"+sessionID, "")
	if err != nil {
		t.Fatalf("read raw transcript: %v", err)
	}
	raw, ok := rawValue.(readRawEnvelope)
	if !ok {
		t.Fatalf("raw transcript type = %T", rawValue)
	}
	pin := 1
	expandedValue, err := readMarkdownPage(path, "local:"+sessionID, schema.SessionMeta{}, "", &pin, 0, maxExpansionBytes)
	if err != nil {
		t.Fatalf("expand transcript turn: %v", err)
	}
	expanded, ok := expandedValue.(readMarkdownEnvelope)
	if !ok || expanded.Expansion == nil {
		t.Fatalf("expanded transcript = %#v", expandedValue)
	}
	outlineValue, err := readOutline(path, "local:"+sessionID, "")
	if err != nil {
		t.Fatalf("read transcript outline: %v", err)
	}
	outline, ok := outlineValue.(readOutlineEnvelope)
	if !ok {
		t.Fatalf("outline transcript type = %T", outlineValue)
	}
	for surface, content := range map[string]string{
		"jsonl":       raw.Content,
		"markdown":    expanded.Content,
		"expand_turn": expanded.Expansion.Data,
		"outline":     outline.Content,
	} {
		for _, private := range []string{attentionID, deliveryID, "Attention resolved.", "attention_resolution", "ATTENTION_RESOLUTION", "delegate_delivery_commits"} {
			if strings.Contains(content, private) {
				t.Fatalf("%s leaked private resolution metadata %q: %s", surface, private, content)
			}
		}
		if surface == "outline" {
			continue
		}
		visibleContent := []string{"visible result"}
		if surface != "markdown" {
			visibleContent = append(visibleContent, callID)
		}
		for _, visible := range visibleContent {
			if !strings.Contains(content, visible) {
				t.Fatalf("%s dropped public tool-round content %q: %s", surface, visible, content)
			}
		}
	}
	providerHistory := expandHistory(turns, replayScope{})
	providerJSON, err := json.Marshal(providerHistory)
	if err != nil {
		t.Fatalf("marshal provider history: %v", err)
	}
	if strings.Contains(string(providerJSON), deliveryID) {
		t.Fatalf("provider history leaked private delivery ID %q: %s", deliveryID, providerJSON)
	}
	if !strings.Contains(string(providerJSON), "visible attention content") {
		t.Fatalf("provider history dropped delegate packet content: %s", providerJSON)
	}

	privateSnippets, opened := contentSnippets(stateDir, sessionID, "Attention resolved.", "attention resolved.")
	if !opened {
		t.Fatal("find_session_transcripts content scan did not open the real transcript")
	}
	if len(privateSnippets) != 0 {
		t.Fatalf("find_session_transcripts exposed the private resolution marker: %#v", privateSnippets)
	}
	visibleSnippets, opened := contentSnippets(stateDir, sessionID, "visible result", "visible result")
	if !opened || len(visibleSnippets) != 1 || visibleSnippets[0].Seq != 1 {
		t.Fatalf("find_session_transcripts visible tool-result query = opened %v, snippets %#v", opened, visibleSnippets)
	}

	rawWindowValue, err := readRaw(path, "local:"+sessionID, "last:2")
	if err != nil {
		t.Fatalf("read ranged raw transcript: %v", err)
	}
	rawWindow := rawWindowValue.(readRawEnvelope)
	windowLines := strings.Split(strings.TrimSpace(rawWindow.Content), "\n")
	windowKinds := make([]schema.TurnKind, 0, len(windowLines)-1)
	for _, line := range windowLines[1:] {
		entry, err := transcript.DecodeEntry([]byte(line))
		if err != nil {
			t.Fatalf("decode ranged public entry: %v", err)
		}
		windowKinds = append(windowKinds, entry.Turn.Kind)
	}
	if want := []schema.TurnKind{schema.TurnAssistant, schema.TurnToolResults}; !reflect.DeepEqual(windowKinds, want) {
		t.Fatalf("ranged public JSONL kinds = %#v, want %#v", windowKinds, want)
	}

	markdownWindowValue, err := readMarkdownPage(path, "local:"+sessionID, schema.SessionMeta{}, "last:2", nil, 0, 0)
	if err != nil {
		t.Fatalf("read ranged markdown transcript: %v", err)
	}
	markdownWindow := markdownWindowValue.(readMarkdownEnvelope)
	if !strings.Contains(markdownWindow.Content, "## Turn 1 — Assistant") || !strings.Contains(markdownWindow.Content, "visible result") || strings.Contains(markdownWindow.Content, "Tool results without a shown call") {
		t.Fatalf("ranged markdown split the public tool round: %s", markdownWindow.Content)
	}

	outlineWindowValue, err := readOutline(path, "local:"+sessionID, "last:2")
	if err != nil {
		t.Fatalf("read ranged outline transcript: %v", err)
	}
	outlineWindow := outlineWindowValue.(readOutlineEnvelope)
	if !strings.Contains(outlineWindow.Content, "1 · Assistant · probe") {
		t.Fatalf("ranged outline split the public tool round: %s", outlineWindow.Content)
	}

	findSessionID := sessionID + "-find"
	findPath := transcriptPath(stateDir, findSessionID)
	findWriter, err := transcript.NewWriter(findPath, transcript.Header{SessionID: findSessionID})
	if err != nil {
		t.Fatalf("create searchable transcript: %v", err)
	}
	findAttention := schema.NewTurn(schema.TurnSteering, llm.User("search preface"))
	findAttention.AttentionID = "private-find-attention"
	for _, turn := range []schema.Turn{
		findAttention,
		delegateAttentionResolutionTurn(findAttention.AttentionID, delegateAttentionConsumed),
		delegateAttentionResolutionTurn(findAttention.AttentionID, delegateAttentionConsumed),
		schema.NewTurn(schema.TurnUserInput, llm.User("later public match")),
	} {
		if err := findWriter.AppendDurable(turn); err != nil {
			_ = findWriter.Close()
			t.Fatalf("append searchable transcript: %v", err)
		}
	}
	if err := findWriter.Close(); err != nil {
		t.Fatalf("close searchable transcript: %v", err)
	}
	findSnippets, opened := contentSnippets(stateDir, findSessionID, "later public match", "later public match")
	if !opened || len(findSnippets) != 1 || findSnippets[0].Seq != 1 {
		t.Fatalf("public find sequence = opened %v snippets %#v, want public seq 1", opened, findSnippets)
	}
	findPin := findSnippets[0].Seq
	findExpansionValue, err := readMarkdownPage(findPath, "local:"+findSessionID, schema.SessionMeta{}, "", &findPin, 0, maxExpansionBytes)
	if err != nil {
		t.Fatalf("expand public find sequence: %v", err)
	}
	findExpansion := findExpansionValue.(readMarkdownEnvelope)
	if findExpansion.Expansion == nil || !strings.Contains(findExpansion.Expansion.Data, "later public match") {
		t.Fatalf("public find sequence expanded the wrong turn: %#v", findExpansion.Expansion)
	}
}

func TestDelegateAttention_PublicJSONLPreservesExactToolResultNumbers(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID = "public-exact-number"
		callID    = "large-number-call"
		largeID   = uint64(9007199254740993)
	)
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	for _, turn := range []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, delegateAttentionToolCall(callID)),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", map[string]any{"id": largeID}, false)),
	} {
		if err := writer.AppendDurable(turn); err != nil {
			_ = writer.Close()
			t.Fatalf("append transcript turn: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	rawValue, err := readRaw(path, "local:"+sessionID, "")
	if err != nil {
		t.Fatalf("read raw transcript: %v", err)
	}
	raw := rawValue.(readRawEnvelope)
	pin := 0
	expandedValue, err := readMarkdownPage(path, "local:"+sessionID, schema.SessionMeta{}, "", &pin, 0, maxExpansionBytes)
	if err != nil {
		t.Fatalf("expand transcript turn: %v", err)
	}
	expanded := expandedValue.(readMarkdownEnvelope)
	for surface, content := range map[string]string{
		"jsonl":       raw.Content,
		"expand_turn": expanded.Expansion.Data,
	} {
		if !strings.Contains(content, "9007199254740993") {
			t.Fatalf("%s rounded exact tool-result integer: %s", surface, content)
		}
		if strings.Contains(content, "9007199254740992") {
			t.Fatalf("%s emitted rounded tool-result integer: %s", surface, content)
		}
	}
}

func TestDelegateAttention_HistoryRepairCannotCreateOrphanedToolResult(t *testing.T) {
	const callID = "call-with-resolution"
	attention := schema.NewTurn(schema.TurnSteering, llm.User("delegate completed"))
	attention.AttentionID = "delegate:delivery-1"
	history := []schema.Turn{
		attention,
		schema.NewTurn(schema.TurnAssistant, delegateAttentionToolCall(callID)),
		delegateAttentionResolutionTurn(attention.AttentionID, delegateAttentionConsumed),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "real result", false)),
	}
	repaired, repairs := repairOrphanedToolResults(history)
	if repairs != 0 {
		t.Fatalf("repair count = %d, want 0; history=%#v", repairs, repaired)
	}
	if !reflect.DeepEqual(repaired, history) {
		t.Fatalf("repair changed valid history:\n got %#v\nwant %#v", repaired, history)
	}
	results := 0
	for _, turn := range repaired {
		for _, part := range turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				results++
				if part.ToolResult.IsError || part.ToolResult.Content != "real result" {
					t.Fatalf("tool result = %#v, want real non-error result", part.ToolResult)
				}
			}
		}
	}
	if results != 1 {
		t.Fatalf("matching tool results = %d, want 1", results)
	}
}

func TestDelegateAttention_RestartFoldIsProviderFreeAndReadOnly(t *testing.T) {
	stateDir := t.TempDir()
	const sessionID = "cold-attention"
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User("cold attention"))
	attention.AttentionID = "attention-cold"
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := writer.AppendDurable(delegateAttentionResolutionTurn(attention.AttentionID, delegateAttentionConsumed)); err != nil {
		t.Fatalf("append resolution: %v", err)
	}
	toolResults := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call-cold", "delegate_send", "done", false))
	toolResults.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "call-cold", DeliveryID: "dlg/delivery/1"}}
	if err := writer.AppendDurable(toolResults); err != nil {
		t.Fatalf("append delivery commit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}

	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read cold fold: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("pending attention = %#v, want none", pending)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if !bytes.Equal(after, before) || afterInfo.Size() != beforeInfo.Size() || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("cold fold mutated transcript: before=%d/%s after=%d/%s", beforeInfo.Size(), beforeInfo.ModTime(), afterInfo.Size(), afterInfo.ModTime())
	}
	missing := filepath.Join(stateDir, sessionsSubdir, "missing.transcript.jsonl")
	if fold, err := readDelegateAttentionFold(missing, "missing"); err != nil || len(fold.pendingIDs()) != 0 {
		t.Fatalf("missing transcript fold = %#v, %v", fold, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing transcript was created: %v", err)
	}

	invalid := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call-real", "delegate_send", "done", false))
	invalid.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "call-wrong", DeliveryID: "dlg/delivery/1"}}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: invalid}}); err == nil {
		t.Fatal("cold fold accepted delivery metadata whose ToolCallID is absent from its TurnToolResults")
	}
	duplicateCall := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call-real", "delegate_send", "done", false))
	duplicateCall.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{
		{ToolCallID: "call-real", DeliveryID: "dlg/delivery/1"},
		{ToolCallID: "call-real", DeliveryID: "dlg/delivery/2"},
	}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: duplicateCall}}); err == nil {
		t.Fatal("cold fold accepted one ToolCallID committed to multiple deliveries")
	}
}

func TestDelegateAttention_RestartReplaysCallerDeliveryCommitWithoutDuplicateToolResult(t *testing.T) {
	stateDir := t.TempDir()
	const (
		rootSessionID   = "root-session"
		ownerDelegateID = "dlg_parent"
		ownerSessionID  = "child-dlg_parent"
	)
	storePath := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	store, err := delegatestore.Open(storePath)
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: rootSessionID,
		stateDir:      stateDir,
		turnLimit:     2,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("open controller: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ownerCreated := delegateControllerCreatedEvent(ownerDelegateID, "")
	ownerCreated.Created.Descriptor.ResolvedProfileID = "openai"
	ownerCreated.Created.Descriptor.ResolvedModel = "gpt-5.2"
	c.mu.Lock()
	_, err = c.appendLocked(ownerCreated)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed owner delegate: %v", err)
	}
	seedDelegateControllerIdle(t, c, "dlg_target", ownerDelegateID)
	seedDelegateControllerDelivery(t, c, "dlg_target")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("initial nested delivery plans = %#v, want serialized head", plans)
	}
	firstPlan := plans[0]
	token, admitted, err := c.BeginDelivery(firstPlan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	commit := &delegateToolResultCommit{controller: c, token: token, deliveryID: firstPlan.deliveryID}

	fs := newAttentionSyncBarrierFS()
	path := transcriptPath(stateDir, ownerSessionID)
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: ownerSessionID})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	original := &Session{id: ownerSessionID, stateDir: stateDir, delegateController: c}
	original.attachTranscript(writer)
	c.rootRuntime = original
	original.queueDelegateDeliveryCommit("delegate-send-N", commit)
	fs.arm()
	appendDone := make(chan error, 1)
	go func() { appendDone <- appendDelegateToolResultFixture(original, "delegate-send-N") }()
	select {
	case <-fs.syncEntered:
	case err := <-appendDone:
		t.Fatalf("caller tool result returned before fsync barrier: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close crashed controller store: %v", err)
	}
	fs.release()
	if err := <-appendDone; err == nil {
		t.Fatal("crashed process unexpectedly acknowledged through its closed delegate store")
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close crashed transcript writer: %v", err)
	}
	crashedFold, err := readDelegateAttentionFold(path, ownerSessionID)
	if err != nil {
		t.Fatalf("read crashed caller transcript: %v", err)
	}
	if got := crashedFold.deliveryCommits[firstPlan.deliveryID]; got != "delegate-send-N" {
		t.Fatalf("crashed caller commit = %q, want exact tool call", got)
	}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:              ownerSessionID,
		ParentSessionID: rootSessionID,
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		IsSubagent:      true,
	}); err != nil {
		t.Fatalf("save owner metadata: %v", err)
	}

	adapter := &delegateAttentionPanicProvider{}
	client := llm.NewClient()
	client.Register(adapter)
	fresh := &Session{
		id:       rootSessionID,
		stateDir: stateDir,
		client:   client,
		cfg: SessionConfig{
			StateDir:                   stateDir,
			MaxConcurrentDelegateTurns: 2,
			testOnly: testConfig{sessionInitFault: func(point string) error {
				panic("cold delivery replay constructed a Session at " + point)
			}},
		},
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap fresh controller: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	fresh.delegateController.mu.Lock()
	pending := append([]delegatestore.PendingDelivery(nil), fresh.delegateController.durable["dlg_target"].PendingDeliveries...)
	claims := len(fresh.delegateController.deliveryClaims)
	fresh.delegateController.mu.Unlock()
	fresh.delegateDeliveryMu.Lock()
	queued := append([]delegateDeliveryPlan(nil), fresh.pendingDelegateDeliveries...)
	fresh.delegateDeliveryMu.Unlock()
	if len(pending) != 1 || pending[0].DeliveryID != delegateDeliveryID("dlg_target", 2) || claims != 1 || len(queued) != 1 || queued[0].deliveryID != pending[0].DeliveryID {
		t.Fatalf("cold replay pending=%#v claims=%d queued=%#v, want only claimed N+1 queued for attachment", pending, claims, queued)
	}

	rootPath := transcriptPath(stateDir, rootSessionID)
	reopened, err := transcript.NewWriter(rootPath, transcript.Header{SessionID: rootSessionID})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fresh.attachTranscript(reopened)
	if err := fresh.flushPendingDelegateDeliveries(); err != nil {
		t.Fatalf("release N+1 after transcript attachment: %v", err)
	}

	fold, err := readDelegateAttentionFold(path, ownerSessionID)
	if err != nil {
		t.Fatalf("read final caller fold: %v", err)
	}
	entries := readAttentionTranscriptEntries(t, path)
	toolResults, firstAttention, secondAttention := 0, 0, 0
	for _, entry := range entries {
		turn := entry.Turn
		if turn.Kind == schema.TurnToolResults {
			toolResults++
		}
		if turn.AttentionID == delegateAttentionID(firstPlan.deliveryID) {
			firstAttention++
		}
		if turn.AttentionID == delegateAttentionID(delegateDeliveryID("dlg_target", 2)) {
			secondAttention++
		}
	}
	if toolResults != 1 || firstAttention != 0 || secondAttention != 1 {
		t.Fatalf("replayed transcript counts tool_results=%d N_attention=%d N+1_attention=%d", toolResults, firstAttention, secondAttention)
	}
	if len(fold.pendingIDs()) != 1 || fold.pendingIDs()[0] != delegateAttentionID(delegateDeliveryID("dlg_target", 2)) {
		t.Fatalf("final pending attention = %#v, want N+1 only", fold.pendingIDs())
	}
	fresh.delegateController.mu.Lock()
	remaining := len(fresh.delegateController.durable["dlg_target"].PendingDeliveries)
	fresh.delegateController.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("pending deliveries after N+1 replay = %d, want 0", remaining)
	}
}

func TestDelegateAttention_BootstrapDrainsRestoredStopAttention(t *testing.T) {
	stateDir := t.TempDir()
	const (
		rootSessionID  = "root-session"
		delegateID     = "dlg_target"
		childSessionID = "child-dlg_target"
		attentionID    = "attention-before-restart-stop"
	)
	storePath := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	store, err := delegatestore.Open(storePath)
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: rootSessionID,
		stateDir:      stateDir,
		turnLimit:     1,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("open controller: %v", err)
	}
	seedDelegateControllerRunning(t, c, delegateID, "")
	writeDelegateAttentionTranscript(t, transcriptPath(stateDir, childSessionID), childSessionID, attentionID)
	if _, _, _, err := c.StopSubtree(rootDelegateActor(rootSessionID), delegateID); err != nil {
		_ = store.Close()
		t.Fatalf("persist stop request: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close crashed delegate store: %v", err)
	}

	fresh := &Session{
		id:       rootSessionID,
		stateDir: stateDir,
		cfg: SessionConfig{
			StateDir:                   stateDir,
			MaxConcurrentDelegateTurns: 1,
		},
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap restored stop: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })

	fresh.delegateController.mu.Lock()
	aggregate := fresh.delegateController.durable[delegateID]
	stop := fresh.delegateController.stop
	claims := len(fresh.delegateController.deliveryClaims)
	fresh.delegateController.mu.Unlock()
	if stop != nil || aggregate == nil || aggregate.PendingStopSeq != 0 || aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("bootstrap restored stop state = stop:%#v aggregate:%#v", stop, aggregate)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, childSessionID), childSessionID)
	if err != nil {
		t.Fatalf("read stopped child attention: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("restored stop attention disposition = %q, want discarded", got)
	}
	fresh.delegateDeliveryMu.Lock()
	queued := append([]delegateDeliveryPlan(nil), fresh.pendingDelegateDeliveries...)
	fresh.delegateDeliveryMu.Unlock()
	if claims != 1 || len(queued) != 1 || queued[0].deliveryID != delegateDeliveryID(delegateID, 1) {
		t.Fatalf("restored stop delivery claims=%d queued=%#v, want one preserved terminal delivery", claims, queued)
	}
}

func TestDelegateAttention_RestoreReconcilesColdCommitBeforeProviderMetadata(t *testing.T) {
	stateDir := t.TempDir()
	rootSessionID := identifier.MustNewSessionID()
	storePath := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	store, err := delegatestore.Open(storePath)
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: rootSessionID,
		stateDir:      stateDir,
		turnLimit:     2,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("open controller: %v", err)
	}
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	if err := store.Close(); err != nil {
		t.Fatalf("close seeded delegate store: %v", err)
	}

	const callID = "cold-replay-before-provider"
	path := transcriptPath(stateDir, rootSessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: rootSessionID})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	result := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "delegate_send", "done", false))
	result.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{
		ToolCallID: callID,
		DeliveryID: delegateDeliveryID("dlg_target", 1),
	}}
	if err := writer.AppendDurable(result); err != nil {
		_ = writer.Close()
		t.Fatalf("append caller delivery commit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close caller transcript: %v", err)
	}

	var probeErr error
	adapter := &delegateAttentionListModelsAdapter{
		onList: func() {
			events, err := delegatestore.ReadEvents(storePath)
			if err != nil {
				probeErr = err
				return
			}
			state, err := delegatestore.Fold(events)
			if err != nil {
				probeErr = err
				return
			}
			if aggregate := state["dlg_target"]; aggregate == nil || len(aggregate.PendingDeliveries) != 0 {
				probeErr = fmt.Errorf("provider metadata observed unreconciled delivery: %#v", aggregate)
			}
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	restored, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		schema.SessionMeta{ID: rootSessionID, ProfileID: "openai", Model: "gpt-5.2", CreatedAt: time.Unix(100, 0).UTC()},
		RestoreSessionConfig{
			StateDir: stateDir,
			testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		},
	)
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	t.Cleanup(restored.Close)
	if adapter.listCalls != 1 {
		t.Fatalf("provider metadata calls = %d, want 1 after cold replay", adapter.listCalls)
	}
	if probeErr != nil {
		t.Fatal(probeErr)
	}
}

func TestDelegateAttention_RestartReplayRequiresDeliveryIDFromExactPair(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}

	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	turn := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-call", "delegate_send", "done", false))
	turn.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{
		ToolCallID: "delegate-call",
		DeliveryID: "dlg_other/delivery/1",
	}}
	if err := writer.AppendDurable(turn); err != nil {
		_ = writer.Close()
		t.Fatalf("append caller delivery commit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close caller transcript: %v", err)
	}

	pending, err := prepareColdDelegateDeliveryReplay(c, plans)
	if err != nil {
		t.Fatalf("prepare cold replay: %v", err)
	}
	if len(pending) != 1 || pending[0].deliveryID != plans[0].deliveryID {
		t.Fatalf("cold replay accepted mismatched delivery pair: %#v", pending)
	}
	c.mu.Lock()
	durable := append([]delegatestore.PendingDelivery(nil), c.durable["dlg_target"].PendingDeliveries...)
	c.mu.Unlock()
	if len(durable) != 1 || durable[0].DeliveryID != plans[0].deliveryID {
		t.Fatalf("mismatched pair acknowledged source head: %#v", durable)
	}
}

func TestDelegateAttention_ColdReplayRefoldsNextSameOwnerCommit(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child_a", "dlg_parent")
	seedDelegateControllerIdle(t, c, "dlg_child_b", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child_a")
	seedDelegateControllerDelivery(t, c, "dlg_child_b")

	path := transcriptPath(c.stateDir, "child-dlg_parent")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create nested caller transcript: %v", err)
	}
	message := llm.ToolResultNamed("call-a", "delegate_send", "first", false)
	second := llm.ToolResultNamed("call-b", "delegate_send", "second", false)
	message.Content = append(message.Content, second.Content...)
	results := schema.NewTurn(schema.TurnToolResults, message)
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{
		{ToolCallID: "call-a", DeliveryID: delegateDeliveryID("dlg_child_a", 1)},
		{ToolCallID: "call-b", DeliveryID: delegateDeliveryID("dlg_child_b", 1)},
	}
	if err := writer.AppendDurable(results); err != nil {
		_ = writer.Close()
		t.Fatalf("append nested caller commits: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close nested caller transcript: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read nested caller transcript: %v", err)
	}

	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("serialized cold replay plans = %#v", plans)
	}
	pending, err := prepareColdDelegateDeliveryReplay(c, plans)
	if err != nil {
		t.Fatalf("prepare cold replay: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("already-committed successor remained pending: %#v", pending)
	}
	c.mu.Lock()
	remainingA := len(c.durable["dlg_child_a"].PendingDeliveries)
	remainingB := len(c.durable["dlg_child_b"].PendingDeliveries)
	c.mu.Unlock()
	if remainingA != 0 || remainingB != 0 {
		t.Fatalf("cold committed deliveries remain: child_a=%d child_b=%d", remainingA, remainingB)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread nested caller transcript: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("cold commit replay mutated the nested caller transcript")
	}
}

func TestDelegateAttention_ColdReplayRefoldsMixedSameOwnerCommits(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child_a", "dlg_parent")
	seedDelegateControllerIdle(t, c, "dlg_child_b", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child_a")
	seedDelegateControllerDelivery(t, c, "dlg_child_b")

	path := transcriptPath(c.stateDir, "child-dlg_parent")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create nested caller transcript: %v", err)
	}
	results := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call-b", "delegate_send", "second", false))
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{
		ToolCallID: "call-b",
		DeliveryID: delegateDeliveryID("dlg_child_b", 1),
	}}
	if err := writer.AppendDurable(results); err != nil {
		_ = writer.Close()
		t.Fatalf("append second caller commit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close nested caller transcript: %v", err)
	}

	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].delegateID != "dlg_child_a" {
		t.Fatalf("initial serialized cold replay plans = %#v", plans)
	}
	pending, err := prepareColdDelegateDeliveryReplay(c, plans)
	if err != nil {
		t.Fatalf("prepare mixed cold replay: %v", err)
	}
	if len(pending) != 1 || pending[0].delegateID != "dlg_child_a" {
		t.Fatalf("mixed cold replay pending = %#v, want only uncommitted child A", pending)
	}
	pump := &Session{delegateController: c}
	c.rootRuntime = pump
	pump.pendingDelegateDeliveries = append(pump.pendingDelegateDeliveries, pending...)
	if err := pump.flushPendingDelegateDeliveries(); err != nil {
		t.Fatalf("flush mixed cold replay: %v", err)
	}

	fold, err := readDelegateAttentionFold(path, "child-dlg_parent")
	if err != nil {
		t.Fatalf("read mixed cold replay fold: %v", err)
	}
	aID := delegateAttentionID(delegateDeliveryID("dlg_child_a", 1))
	bID := delegateAttentionID(delegateDeliveryID("dlg_child_b", 1))
	if _, exists := fold.content[bID]; exists {
		t.Fatalf("already-committed child B gained duplicate attention %q", bID)
	}
	if pendingIDs := fold.pendingIDs(); !reflect.DeepEqual(pendingIDs, []string{aID}) {
		t.Fatalf("mixed cold replay pending attention = %#v, want only child A", pendingIDs)
	}
	c.mu.Lock()
	remainingA := len(c.durable["dlg_child_a"].PendingDeliveries)
	remainingB := len(c.durable["dlg_child_b"].PendingDeliveries)
	c.mu.Unlock()
	if remainingA != 0 || remainingB != 0 {
		t.Fatalf("mixed cold replay retained source deliveries: child_a=%d child_b=%d", remainingA, remainingB)
	}
}

func TestDelegateAttention_LiveReplayHonorsDurableCallerCommit(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")

	const sessionID = "root-session"
	path := transcriptPath(c.stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	root := &Session{id: sessionID, stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(writer)
	t.Cleanup(func() { _ = root.closeAttachedTranscript() })
	c.rootRuntime = root

	deliveryID := delegateDeliveryID("dlg_target", 1)
	results := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-call", "delegate_send", "done", false))
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{
		ToolCallID: "delegate-call",
		DeliveryID: deliveryID,
	}}
	if err := root.writeTranscriptDurable(results); err != nil {
		t.Fatalf("append durable caller commit: %v", err)
	}

	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("live replay plans = %#v, want one", plans)
	}
	if err := root.executeDelegateMutationPlans(delegateMutationPlans{deliveries: plans}); err != nil {
		t.Fatalf("execute live replay: %v", err)
	}

	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read live caller fold: %v", err)
	}
	if _, exists := fold.content[delegateAttentionID(deliveryID)]; exists {
		t.Fatalf("durably committed live delivery %q gained duplicate attention", deliveryID)
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("durably committed live delivery remained pending: %d", remaining)
	}
	root.attentionMu.Lock()
	wakeIDs := len(root.rootAttentionWakeIDs)
	wakeArmed := root.rootAttentionWake
	root.attentionMu.Unlock()
	if wakeIDs != 0 || wakeArmed {
		t.Fatalf("durably committed live delivery armed phantom root attention: ids=%d armed=%t", wakeIDs, wakeArmed)
	}
}

func TestDelegateAttention_PostAckArmReadFailureRetriesExactID(t *testing.T) {
	stateDir := t.TempDir()
	clock := agenttest.NewFakeClock()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, NoProjectPrompts: true, clock: clock}),
		withSteps(func(llm.Request) llm.Response {
			return communicateResponse(true, "retried exact attention")
		}),
	)
	c := root.delegateController
	c.mu.Lock()
	created := delegateControllerCreatedEvent("dlg_target", "")
	created.Created.Descriptor.OwnerSessionID = root.ID()
	_, err := c.appendLocked(created)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed target delegate: %v", err)
	}
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("delivery plans = %#v, want one", plans)
	}

	readCalls := 0
	injected := errors.New("injected post-ack attention fold failure")
	root.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		readCalls++
		if readCalls == 3 {
			return delegateAttentionFold{}, injected
		}
		return readDelegateAttentionFold(path, sessionID)
	}
	wakes := make(chan struct{}, 2)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })
	retryTimersBefore := clock.BlockedCount()
	err = root.executeDelegateMutationPlans(delegateMutationPlans{deliveries: plans})
	if !errors.Is(err, injected) {
		t.Fatalf("delivery error = %v, want post-ack fold failure", err)
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	receipts := len(c.deliveries)
	c.mu.Unlock()
	if remaining != 0 || receipts != 0 {
		t.Fatalf("source after injected arm failure = pending:%d receipts:%d, want acknowledged and released", remaining, receipts)
	}
	select {
	case <-wakes:
		t.Fatal("post-ack fold failure emitted an unverified wake")
	default:
	}
	if got := clock.BlockedCount(); got != retryTimersBefore+1 {
		t.Fatalf("post-ack fold failure retry timers = %d, want baseline %d plus one", got, retryTimersBefore)
	}
	root.cfg.testOnly.delegateAttentionReadFold = nil
	clock.Advance(jobNotificationRetryInitialDelay)
	<-wakes
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("consume retried exact attention: %v", err)
	}
	attentionID := delegateAttentionID(delegateDeliveryID("dlg_target", 1))
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read retried attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("retried attention resolution = %q, want consumed", got)
	}
}

func TestDelegateAttention_ColdPostAckReadFailureRetriesExactID(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	const attentionID = "watch:cold-post-ack:1"
	path := transcriptPath(c.stateDir, "child-dlg_target")
	writeDelegateAttentionTranscript(t, path, "child-dlg_target", attentionID)

	clock := agenttest.NewFakeClock()
	root := &Session{
		id:                     "root-session",
		stateDir:               c.stateDir,
		state:                  SessionIdle,
		clock:                  clock,
		delegateController:     c,
		delegateRootSessionID:  "root-session",
		ownsDelegateController: true,
	}
	c.rootRuntime = root
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })
	retryTimersBefore := clock.BlockedCount()

	backup := path + ".readable"
	if err := os.Rename(path, backup); err != nil {
		t.Fatalf("hide cold attention transcript: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		_ = os.Rename(backup, path)
		t.Fatalf("install cold attention read fault: %v", err)
	}
	err := c.armColdDelegateAttention("dlg_target", attentionID)
	if err == nil {
		t.Fatal("cold post-ack attention fold unexpectedly succeeded")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove cold attention read fault: %v", err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatalf("restore cold attention transcript: %v", err)
	}
	if got := clock.BlockedCount(); got != retryTimersBefore {
		t.Fatalf("cold fold failure created a second arm mirror timer: got %d, want baseline %d", got, retryTimersBefore)
	}
	if root.sessionWorkPending() || len(c.attentionWakeIDs["dlg_target"]) != 0 {
		t.Fatalf("failed cold fold published unverified attention: work=%t unresolved=%#v", root.sessionWorkPending(), c.attentionWakeIDs["dlg_target"])
	}
	if err := c.armColdDelegateAttention("dlg_target", attentionID); err != nil {
		t.Fatalf("caller retry cold attention: %v", err)
	}
	<-wakes
	delegateID, gotAttentionID, pending := c.nextIdleDelegateAttention()
	if !pending || delegateID != "dlg_target" || gotAttentionID != attentionID {
		t.Fatalf("retried cold attention = delegate:%q attention:%q pending:%t", delegateID, gotAttentionID, pending)
	}
}

func TestDelegateAttention_SettledResidentChildStartsExactAttentionGeneration(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	attentionEntered := make(chan struct{})
	releaseAttention := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseAttention) }) })
	attentionContent := `<job-notification event="watch_send">drive exact attention</job-notification>`
	attentionSeen := false
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(true, "initial run complete") },
		func(request llm.Request) llm.Response {
			attentionSeen = requestContainsText(request, attentionContent)
			close(attentionEntered)
			<-releaseAttention
			return communicateResponse(true, "attention handled")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "warm retained runtime", 60_000)
	if outcome.result.Err != nil || outcome.commit == nil {
		t.Fatalf("initial stable run = %#v", outcome)
	}
	plans, err := outcome.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge initial stable result: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("execute initial delivery acknowledgement: %v", err)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatal("initial stable run retained no resident child")
	}
	const attentionID = "watch:resident-attention:1"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, attentionContent); err != nil || !appended {
		t.Fatalf("append resident attention = appended:%t err:%v", appended, err)
	}
	if err := sub.sess.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm resident attention: %v", err)
	}
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[fixture.delegateID]
	generation := aggregate.Generation
	trigger := aggregate.Trigger
	open := aggregate.CurrentRunOpen
	root.delegateController.mu.Unlock()
	if generation != 2 || trigger != delegatestore.TriggerAttention || !open {
		t.Fatalf("attention wake durable generation = generation:%d trigger:%q open:%t, want 2/attention/open", generation, trigger, open)
	}
	<-attentionEntered
	if !attentionSeen {
		t.Fatal("attention generation provider request omitted the exact durable attention")
	}
	releaseOnce.Do(func() { close(releaseAttention) })
	waitForStableSupervisionRun(t, root, fixture.childID)
	fold, err := readDelegateAttentionFold(transcriptPath(fixture.stateDir, fixture.childID), fixture.childID)
	if err != nil {
		t.Fatalf("read settled child attention: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("settled child attention resolution = %q, want consumed", got)
	}
}

func TestDelegateAttention_RestoreSessionStartCountsColdReplayAppend(t *testing.T) {
	stateDir := t.TempDir()
	const sessionID = "01ATTENTIONREPLAYCOUNT01"
	store, err := delegatestore.Open(filepath.Join(jobsDir(stateDir, sessionID), "delegates.jsonl"))
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: sessionID,
		stateDir:      stateDir,
		turnLimit:     2,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("open controller: %v", err)
	}
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	firstDeliveryID := delegateDeliveryID("dlg_target", 1)
	if err := store.Close(); err != nil {
		t.Fatalf("close delegate store: %v", err)
	}

	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
	})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	results := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-call", "delegate_send", "done", false))
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "delegate-call", DeliveryID: firstDeliveryID}}
	if err := writer.AppendDurable(results); err != nil {
		_ = writer.Close()
		t.Fatalf("append committed caller result: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close caller transcript: %v", err)
	}

	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config: (SessionConfig{
			MaxConcurrentDelegateTurns: 2,
		}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	start, ok := resumeTurnSeedFindSessionStart(t, restored)
	if !ok {
		t.Fatal("restored session emitted no SESSION_START")
	}
	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatalf("read restored transcript: %v", err)
	}
	if len(entries) != 2 || entries[1].Turn.AttentionID != delegateAttentionID(delegateDeliveryID("dlg_target", 2)) {
		t.Fatalf("restored transcript entries = %#v, want committed N plus N+1 attention", entries)
	}
	if start.TranscriptEntries != len(entries) {
		t.Fatalf("SESSION_START transcript entries = %d, durable entries after replay = %d", start.TranscriptEntries, len(entries))
	}
}

func TestDelegateAttention_StopDrainDoesNotResolveAttentionTwice(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	const (
		sessionID   = "child-dlg_target"
		attentionID = "attention-before-stop"
	)
	path := transcriptPath(c.stateDir, sessionID)
	writeDelegateAttentionTranscript(t, path, sessionID, attentionID)

	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	root := &Session{delegateController: c}
	if err := c.drainStop(context.Background(), c.stopForResult(result), root); err != nil {
		t.Fatalf("drain stop with root mutation executor: %v", err)
	}
	pending, err := readPendingDelegateAttention(path, sessionID)
	if err != nil {
		t.Fatalf("read attention after drain: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending attention after drain = %#v", pending)
	}
}

func TestDelegateAttention_BootstrapFlushRoutesNestedDeliveryToOwner(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")

	const (
		rootSessionID  = "root-session"
		ownerSessionID = "child-dlg_parent"
	)
	ownerPath := transcriptPath(c.stateDir, ownerSessionID)
	ownerWriter, err := transcript.NewWriter(ownerPath, transcript.Header{SessionID: ownerSessionID})
	if err != nil {
		t.Fatalf("create owner transcript: %v", err)
	}
	if err := ownerWriter.Close(); err != nil {
		t.Fatalf("close owner transcript: %v", err)
	}
	rootPath := transcriptPath(c.stateDir, rootSessionID)
	rootWriter, err := transcript.NewWriter(rootPath, transcript.Header{SessionID: rootSessionID})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = rootWriter.Close() })
	root := &Session{id: rootSessionID, stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(rootWriter)
	c.rootRuntime = root

	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("nested delivery plans = %#v", plans)
	}
	root.pendingDelegateDeliveries = append(root.pendingDelegateDeliveries, plans[0])
	if err := root.flushPendingDelegateDeliveries(); err != nil {
		t.Fatalf("flush pending nested delivery: %v", err)
	}

	attentionID := delegateAttentionID(plans[0].deliveryID)
	ownerFold, err := readDelegateAttentionFold(ownerPath, ownerSessionID)
	if err != nil {
		t.Fatalf("read owner attention: %v", err)
	}
	if pending := ownerFold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("owner pending attention = %#v, want %q", pending, attentionID)
	}
	rootFold, err := readDelegateAttentionFold(rootPath, rootSessionID)
	if err != nil {
		t.Fatalf("read root attention: %v", err)
	}
	if pending := rootFold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("nested delivery leaked to root attention: %#v", pending)
	}
}

func TestDelegateAttention_ColdDeliveryClaimFencesOwnerRestore(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")

	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("cold owner delivery plans = %#v", plans)
	}
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("ReserveStart owner: %v", err)
	}
	if _, err := c.CommitStart(reservation); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("CommitStart with cold delivery claim = %v, want target busy", err)
	}
	c.mu.Lock()
	aggregate := c.durable["dlg_parent"]
	phase, generation := aggregate.Phase, aggregate.Generation
	c.mu.Unlock()
	if phase != delegatestore.PhaseIdle || generation != 0 {
		t.Fatalf("owner mutated across cold delivery claim: phase=%s generation=%d", phase, generation)
	}
}

func TestDelegateAttention_RestoringOwnerDefersColdDeliveryUntilRuntimeAttach(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")

	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("ReserveStart owner: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart owner: %v", err)
	}
	if plans := c.ReplayDeliveries(); len(plans) != 0 {
		t.Fatalf("delivery entered cold owner transcript during restore: %#v", plans)
	}

	ownerPath := transcriptPath(c.stateDir, "child-dlg_parent")
	ownerWriter, err := transcript.NewWriter(ownerPath, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create owner transcript: %v", err)
	}
	t.Cleanup(func() { _ = ownerWriter.Close() })
	owner := &Session{id: "child-dlg_parent", stateDir: c.stateDir, delegateController: c}
	owner.attachTranscript(ownerWriter)
	if err := c.AttachRuntime(started.lease, owner); err != nil {
		t.Fatalf("AttachRuntime owner: %v", err)
	}
	claim, err := c.BeginStartInput(started.lease)
	if err != nil {
		t.Fatalf("BeginStartInput owner: %v", err)
	}
	plans, err := c.CompleteStartInput(claim, true, delegateFinish{})
	if err != nil {
		t.Fatalf("CompleteStartInput owner: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].receiver != owner || plans.deliveries[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("post-attach owner deliveries = %#v", plans.deliveries)
	}
}

func TestDelegateAttention_ColdOwnerSerializesTranscriptDeliveries(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child_a", "dlg_parent")
	seedDelegateControllerIdle(t, c, "dlg_child_b", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child_a")
	seedDelegateControllerDelivery(t, c, "dlg_child_b")

	ownerPath := transcriptPath(c.stateDir, "child-dlg_parent")
	ownerWriter, err := transcript.NewWriter(ownerPath, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create owner transcript: %v", err)
	}
	if err := ownerWriter.Close(); err != nil {
		t.Fatalf("close owner transcript: %v", err)
	}

	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("simultaneous cold owner plans = %#v, want one receiver receipt", plans)
	}
	next, err := deliverDelegatePacket(plans[0], plans[0].receiver)
	if err != nil {
		t.Fatalf("deliver first cold packet: %v", err)
	}
	if len(next.deliveries) != 1 || next.deliveries[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("next cold owner delivery = %#v", next.deliveries)
	}
	if _, err := deliverDelegatePacket(next.deliveries[0], next.deliveries[0].receiver); err != nil {
		t.Fatalf("deliver second cold packet: %v", err)
	}
	fold, err := readDelegateAttentionFold(ownerPath, "child-dlg_parent")
	if err != nil {
		t.Fatalf("read owner transcript: %v", err)
	}
	if got := len(fold.pendingIDs()); got != 2 {
		t.Fatalf("serialized cold owner attention count = %d, want 2", got)
	}
}

func TestDelegateAttention_FailedFlushRetainsDeliveryForLiveRetry(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")

	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(writer)
	c.rootRuntime = root
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}
	root.pendingDelegateDeliveries = append(root.pendingDelegateDeliveries, plans[0])
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript writer: %v", err)
	}
	if err := root.flushPendingDelegateDeliveries(); err == nil {
		t.Fatal("flush with closed transcript writer succeeded")
	}
	root.delegateDeliveryMu.Lock()
	queued := append([]delegateDeliveryPlan(nil), root.pendingDelegateDeliveries...)
	root.delegateDeliveryMu.Unlock()
	if len(queued) != 1 || queued[0].deliveryID != plans[0].deliveryID || queued[0].claim == plans[0].claim {
		t.Fatalf("retry queue = %#v, want fresh claim for %q", queued, plans[0].deliveryID)
	}
	c.mu.Lock()
	pending := append([]delegatestore.PendingDelivery(nil), c.durable["dlg_target"].PendingDeliveries...)
	c.mu.Unlock()
	if len(pending) != 1 || pending[0].DeliveryID != plans[0].deliveryID {
		t.Fatalf("durable delivery after failed flush = %#v", pending)
	}
}

func TestDelegateAttention_FailedMutationQueueRetainsOtherOwners(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerIdle(t, c, "dlg_a_root", "")
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_z_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_a_root")
	seedDelegateControllerDelivery(t, c, "dlg_z_child")

	rootPath := transcriptPath(c.stateDir, "root-session")
	rootWriter, err := transcript.NewWriter(rootPath, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(rootWriter)
	c.rootRuntime = root
	ownerPath := transcriptPath(c.stateDir, "child-dlg_parent")
	ownerWriter, err := transcript.NewWriter(ownerPath, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create nested owner transcript: %v", err)
	}
	if err := ownerWriter.Close(); err != nil {
		t.Fatalf("close nested owner transcript: %v", err)
	}
	plans := c.ReplayDeliveries()
	if len(plans) != 2 || plans[0].delegateID != "dlg_a_root" || plans[1].delegateID != "dlg_z_child" {
		t.Fatalf("multi-owner replay plans = %#v", plans)
	}
	if err := rootWriter.Close(); err != nil {
		t.Fatalf("close root transcript writer: %v", err)
	}
	if err := root.executeDelegateMutationPlans(delegateMutationPlans{deliveries: plans}); err == nil {
		t.Fatal("multi-owner delivery execution unexpectedly succeeded")
	}
	root.delegateDeliveryMu.Lock()
	queued := append([]delegateDeliveryPlan(nil), root.pendingDelegateDeliveries...)
	root.delegateDeliveryMu.Unlock()
	if len(queued) != 2 || queued[0].delegateID != "dlg_a_root" || queued[1].delegateID != "dlg_z_child" {
		t.Fatalf("multi-owner retry queue = %#v, want failed head plus unprocessed owner", queued)
	}
}

func TestDelegateAttention_FailedTerminalDeliveryRetriesFromRootPump(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")

	fs := newAttentionFailNextSyncFS()
	rootPath := transcriptPath(c.stateDir, "root-session")
	rootWriter, err := transcript.NewWriterWithFS(fs, rootPath, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = rootWriter.Close() })
	clock := agenttest.NewFakeClock()
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c, events: make(chan events.SessionEvent, 16), profile: NewOpenAIProfile("gpt-5.2"), clock: clock}
	root.contextMgr = contextmgr.NewManager(root.profile, nil)
	root.sessionCtx, root.cancelFunc = context.WithCancel(context.Background())
	t.Cleanup(root.cancelFunc)
	root.attachTranscript(rootWriter)
	c.rootRuntime = root
	wakes := make(chan struct{}, 4)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })
	child := &Session{id: "child-dlg_target", stateDir: c.stateDir, delegateController: c}

	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	plans := finishDelegateDeliveryGeneration(t, c, lease, "terminal result")
	if len(plans.deliveries) != 1 {
		t.Fatalf("terminal delivery plans = %#v", plans.deliveries)
	}
	fs.failNextSync()
	if err := child.executeDelegateMutationPlans(plans); err == nil {
		t.Fatal("terminal delivery unexpectedly survived injected owner transcript failure")
	}
	child.delegateDeliveryMu.Lock()
	childQueued := len(child.pendingDelegateDeliveries)
	child.delegateDeliveryMu.Unlock()
	root.delegateDeliveryMu.Lock()
	rootQueued := len(root.pendingDelegateDeliveries)
	root.delegateDeliveryMu.Unlock()
	if childQueued != 0 || rootQueued != 1 {
		t.Fatalf("delivery retry ownership child=%d root=%d, want child=0 root=1", childQueued, rootQueued)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("root retry drive emitted no wake")
	}
	if !root.sessionWorkPending() {
		t.Fatal("root retry drive did not expose autonomous work")
	}

	fs.failNextSync()
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("consume first root delivery wake: %v", err)
	}
	c.mu.Lock()
	remainingAfterRetryFailure := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remainingAfterRetryFailure != 1 {
		t.Fatalf("pending deliveries after second transient failure = %d, want 1", remainingAfterRetryFailure)
	}
	select {
	case <-wakes:
		t.Fatal("second transient failure spun an immediate retry")
	default:
	}
	root.delegateDeliveryMu.Lock()
	retryActive := root.delegateDeliveryRetry.active
	retryDelay := root.delegateDeliveryRetry.delay
	root.delegateDeliveryMu.Unlock()
	if !retryActive || retryDelay != 0 {
		t.Fatalf("paced root retry state active=%v delay=%v, want active with initial delay", retryActive, retryDelay)
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	select {
	case <-wakes:
	// TRIPWIRE: in-process notify channel with a fake clock already advanced
	// past the retry delay; only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("second transient failure did not re-arm a paced wake")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("consume paced root delivery wake: %v", err)
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("pending terminal deliveries after root retry = %d, want 0", remaining)
	}
}

func TestDelegateAttention_DeliveryDeferredDuringProcessingArmsWake(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c, state: SessionProcessing}
	root.attachTranscript(writer)
	c.rootRuntime = root
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}
	_, deferred, err := root.acceptDelegateDeliveryPlan(plans[0])
	if err != nil || !deferred {
		t.Fatalf("accept processing delivery = deferred %v err %v", deferred, err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("processing deferral emitted no autonomous wake")
	}
	if !root.sessionWorkPending() {
		t.Fatal("processing deferral did not expose autonomous work")
	}
}

func TestRootDelegateAttention_DurableAppendWaitsForSourceSettlementBeforeWake(t *testing.T) {
	stateDir := t.TempDir()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, NoProjectPrompts: true}),
	)
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })

	const attentionID = "delegate:dlg_wake/delivery/1"
	if appended, err := root.appendDelegateNotificationDurably(attentionID, `<delegate-notification delegate_id="dlg_wake">done</delegate-notification>`); err != nil || !appended {
		t.Fatalf("append root attention = appended:%t err:%v", appended, err)
	}
	select {
	case <-wakes:
		t.Fatal("durable root attention woke before its source settled")
	default:
	}
	if err := root.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm settled root attention: %v", err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("settled root attention emitted no autonomous wake")
	}
	if !root.sessionWorkPending() {
		t.Fatal("durable root attention is absent from root work-pending state")
	}
}

func TestRootDelegateAttention_SuccessfulNotificationConsumesExactIDs(t *testing.T) {
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_consume/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_consume">complete</delegate-notification>`
	)
	requestSawAttention := false
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, NoProjectPrompts: true}),
		withSteps(func(req llm.Request) llm.Response {
			requestSawAttention = requestContainsText(req, content)
			return toolCallResponse(communicateCall("root-attention-consumed", "completion received"))
		}),
	)
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append root attention: %v", err)
	}
	if err := root.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm root attention: %v", err)
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("process root attention: %v", err)
	}
	if !requestSawAttention {
		t.Fatal("root notification turn did not deliver durable attention to the model")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("root attention disposition = %q, want consumed", got)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("root attention remains pending after successful turn: %#v", pending)
	}
}

func TestRootDelegateAttention_FailedConsumptionRemainsPendingAndRearms(t *testing.T) {
	stateDir := t.TempDir()
	clock := agenttest.NewFakeClock()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, NoProjectPrompts: true, clock: clock}),
		withSteps(
			func(llm.Request) llm.Response {
				return toolCallResponse(communicateCall("root-attention-first", "first attempt"))
			},
			func(llm.Request) llm.Response {
				return toolCallResponse(communicateCall("root-attention-retry", "retry complete"))
			},
		),
	)
	installResolutionSyncFailureWriter(t, root)
	wakes := make(chan struct{}, 2)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })

	const attentionID = "delegate:dlg_retry/delivery/1"
	if _, err := root.appendDelegateNotificationDurably(attentionID, `<delegate-notification delegate_id="dlg_retry">retry me</delegate-notification>`); err != nil {
		t.Fatalf("append root attention: %v", err)
	}
	if err := root.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm root attention: %v", err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("initial root attention wake absent")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil || !strings.Contains(err.Error(), "injected root attention resolution sync failure") {
		t.Fatalf("first root attention turn error = %v, want injected resolution failure", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read failed root attention fold: %v", err)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("pending after failed consumption = %#v, want exact attention", pending)
	}
	if !root.sessionWorkPending() {
		t.Fatal("failed root attention consumption cleared autonomous work")
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	select {
	case <-wakes:
	// TRIPWIRE: in-process notify channel with a fake clock already advanced
	// past the retry delay; only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("failed root attention consumption did not re-arm a paced wake")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("retry root attention turn: %v", err)
	}
	fold, err = readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read retried root attention fold: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("pending after successful retry = %#v", pending)
	}
}

func TestRootDelegateAttention_RestoreRearmsPendingIDsWithoutProviderCall(t *testing.T) {
	stateDir := t.TempDir()
	rootID := identifier.MustNewSessionID()
	if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	const attentionID = "delegate:dlg_restore/delivery/1"
	writer, err := transcript.NewWriter(transcriptPath(stateDir, rootID), transcript.Header{SessionID: rootID, ProfileID: "openai", Model: "gpt-5.2"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User(`<delegate-notification delegate_id="dlg_restore">restore me</delegate-notification>`))
	attention.AttentionID = attentionID
	attention.StableTurnID = newQueueEntryID()
	if err := writer.AppendDurable(attention); err != nil {
		_ = writer.Close()
		t.Fatalf("append pending root attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close root transcript: %v", err)
	}
	meta := schema.SessionMeta{ID: rootID, ProfileID: "openai", Model: "gpt-5.2"}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("save root metadata: %v", err)
	}
	client := llm.NewClient()
	adapter := &delegateAttentionListModelsAdapter{}
	client.Register(adapter)
	restored, err := RestoreSessionFromMeta(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer restored.Close()
	wakes := make(chan struct{}, 1)
	restored.SetNotifyFunc(func() { wakes <- struct{}{} })
	select {
	case <-wakes:
	default:
		t.Fatal("restored pending root attention emitted no provider-free wake")
	}
	if !restored.sessionWorkPending() {
		t.Fatal("restored pending root attention is absent from autonomous work")
	}
}

func TestDelegateAttention_RestartRearmsColdChildAndDrainsExactAttention(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	const (
		owedID            = "watch:restart-attention:owed"
		owedContent       = `<job-notification event="watch_send">recover consumed owed generation</job-notification>`
		remainingID       = "watch:restart-attention:remaining"
		remainingContent  = `<job-notification event="watch_send">remain unresolved while owed generation runs</job-notification>`
		historicalID      = "watch:restart-attention:historical"
		historicalContent = `<job-notification event="watch_send">historical zero marker launches nothing</job-notification>`
	)
	childPath := transcriptPath(fixture.stateDir, fixture.childID)
	for index, item := range []struct {
		id      string
		content string
	}{
		{id: owedID, content: owedContent},
		{id: remainingID, content: remainingContent},
		{id: historicalID, content: historicalContent},
	} {
		if appended, err := appendColdDelegateNotificationDurablyWithOpen(
			childPath,
			fixture.childID,
			item.id,
			item.content,
			time.Unix(1_700_000_300+int64(index), 0).UTC(),
			transcript.OpenWriterForSession,
		); err != nil || !appended {
			t.Fatalf("append cold delegate attention %q = appended:%t err:%v", item.id, appended, err)
		}
	}
	writer, _, err := transcript.OpenWriterForSession(childPath, fixture.childID)
	if err != nil {
		t.Fatalf("open child attention transcript: %v", err)
	}
	owedResolution := delegateAttentionResolutionTurn(owedID, delegateAttentionConsumed)
	owedResolution.AttentionResolution.ResumeGeneration = 1
	if err := writer.AppendDurable(owedResolution); err != nil {
		_ = writer.Close()
		t.Fatalf("append owed consumed marker: %v", err)
	}
	if err := writer.AppendDurable(delegateAttentionResolutionTurn(historicalID, delegateAttentionConsumed)); err != nil {
		_ = writer.Close()
		t.Fatalf("append historical consumed marker: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close child attention transcript: %v", err)
	}
	store, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	events, err := store.Load()
	if err != nil {
		_ = store.Close()
		t.Fatalf("load delegate store: %v", err)
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		_ = store.Close()
		t.Fatalf("fold delegate store: %v", err)
	}
	if _, _, err := store.AppendBatch(state, []delegatestore.Event{{
		Kind:       delegatestore.EventDelegateAttentionChanged,
		DelegateID: fixture.delegateID,
		AttentionChanged: &delegatestore.DelegateAttentionChanged{
			NeedsAttention: true,
		},
	}}); err != nil {
		_ = store.Close()
		t.Fatalf("append initial attention projection: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close delegate store: %v", err)
	}

	attentionEntered := make(chan struct{})
	releaseAttention := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseAttention) }) })
	attentionSeen := false
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(request llm.Request) llm.Response {
			attentionSeen = requestContainsText(request, owedContent)
			close(attentionEntered)
			<-releaseAttention
			return communicateResponse(true, "recovered owed generation handled")
		},
		func(request llm.Request) llm.Response {
			if !requestContainsText(request, remainingContent) {
				t.Errorf("successor attention request omitted remaining unresolved input")
			}
			return communicateResponse(true, "remaining attention handled")
		},
		func(llm.Request) llm.Response {
			return communicateResponse(true, "root drained recovered completions")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	<-attentionEntered
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[fixture.delegateID]
	generation := aggregate.Generation
	trigger := aggregate.Trigger
	open := aggregate.CurrentRunOpen
	needsAttention := aggregate.NeedsAttention
	unresolved := maps.Clone(root.delegateController.attentionWakeIDs[fixture.delegateID])
	root.delegateController.mu.Unlock()
	if generation != 1 || trigger != delegatestore.TriggerAttention || !open || !needsAttention {
		t.Fatalf("owed attention generation = generation:%d trigger:%q open:%t needs:%t, want 1/attention/open/true", generation, trigger, open, needsAttention)
	}
	if !reflect.DeepEqual(unresolved, map[string]struct{}{remainingID: {}}) {
		t.Fatalf("restart unresolved IDs = %#v, want only %q", unresolved, remainingID)
	}
	if !attentionSeen {
		t.Fatal("owed generation provider request omitted the exact consumed attention")
	}
	fold, err := readDelegateAttentionFold(childPath, fixture.childID)
	if err != nil {
		t.Fatalf("read restarted attention fold: %v", err)
	}
	if fold.resumeGenerations[owedID] != 1 || fold.resumeGenerations[historicalID] != 0 || fold.resolutions[historicalID] != delegateAttentionConsumed {
		t.Fatalf("restart resolution generations = %#v resolutions=%#v", fold.resumeGenerations, fold.resolutions)
	}
	releaseOnce.Do(func() { close(releaseAttention) })
	waitForStableSupervisionRun(t, root, fixture.childID)
}

func TestDelegateAttention_PrelaunchRecoveryDoesNotWaitForUnlaunchedRunner(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	c := root.delegateController
	actor := rootDelegateActor(root.id)
	seedDelegateControllerIdle(t, c, "dlg_nested_source", fixture.delegateID)
	seedDelegateControllerDelivery(t, c, "dlg_nested_source")
	fs := newAttentionFailNextSyncFS()
	root.delegateRestoreBeforeSideEffects = func(child *Session) {
		child.mu.Lock()
		oldWriter := child.transcript
		child.mu.Unlock()
		if err := oldWriter.Close(); err != nil {
			t.Fatalf("close restored child transcript: %v", err)
		}
		writer, err := transcript.NewWriterWithFS(fs, transcriptPath(child.stateDir, child.id), transcript.Header{SessionID: child.id})
		if err != nil {
			t.Fatalf("replace child transcript: %v", err)
		}
		child.attachTranscript(writer)
		child.cfg.testOnly.delegateInitialInputAppend = func(*Session) {
			fs.failSyncAfter(1, func() {
				if err := c.store.Close(); err != nil {
					t.Errorf("close delegate store at delivery fault: %v", err)
				}
			})
		}
	}

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start successor", 0)
	if outcome.result.Err == nil || !strings.Contains(outcome.result.Err.Error(), "store is closed") {
		t.Fatalf("prelaunch double failure = %#v, want durable store failure", outcome.result)
	}
	if got := len(fixture.adapter.Requests()); got != 0 {
		t.Fatalf("prelaunch failure reached provider %d times", got)
	}

	reopened, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("reopen delegate store: %v", err)
	}
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	stop, cancelPlan, stopPlans, err := c.StopSubtree(actor, fixture.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if err := root.executeDelegateMutationPlans(stopPlans); err != nil {
		t.Fatalf("execute stop plans: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	for range 3 {
		plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
		if err != nil {
			t.Fatalf("Reconcile prelaunch recovery: %v", err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatalf("execute prelaunch recovery plans: %v", err)
		}
	}
	select {
	case <-stop.done:
	default:
		c.mu.Lock()
		live := c.live[fixture.delegateID]
		aggregate := c.durable[fixture.delegateID]
		c.mu.Unlock()
		t.Fatalf("prelaunch recovery waited for an unlaunched runner: live=%#v aggregate=%#v", live, aggregate)
	}
}

func delegateAttentionToolCall(callID string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        callID,
			Name:      "probe",
			Type:      "function",
			Arguments: json.RawMessage(`{}`),
		},
	}}}
}

func readAttentionTranscriptEntries(t *testing.T, path string) []transcript.Entry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile transcript: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	entries := make([]transcript.Entry, 0, len(lines)-1)
	for _, line := range lines[1:] {
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatalf("DecodeEntry: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

type attentionSyncBarrierFS struct {
	afero.Fs
	mu          sync.Mutex
	armed       bool
	syncEntered chan struct{}
	allowSync   chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

type delegateAttentionListModelsAdapter struct {
	delegateAttentionPanicProvider
	onList    func()
	listCalls int
}

type delegateAttentionPanicProvider struct{}

var _ llm.ProviderAdapter = (*delegateAttentionPanicProvider)(nil)
var _ llm.ModelLister = (*delegateAttentionPanicProvider)(nil)

func (*delegateAttentionPanicProvider) Name() string { return "openai" }

func (*delegateAttentionPanicProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	panic("cold delivery replay called provider Complete")
}

func (*delegateAttentionPanicProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	panic("cold delivery replay called provider Stream")
}

func (*delegateAttentionPanicProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	panic("cold delivery replay called provider ListModels")
}

func (a *delegateAttentionListModelsAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	a.listCalls++
	if a.onList != nil {
		a.onList()
	}
	return nil, nil
}

func newAttentionSyncBarrierFS() *attentionSyncBarrierFS {
	return &attentionSyncBarrierFS{
		Fs:          afero.NewOsFs(),
		syncEntered: make(chan struct{}),
		allowSync:   make(chan struct{}),
	}
}

func (fs *attentionSyncBarrierFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &attentionSyncBarrierFile{File: file, fs: fs}, nil
}

func (fs *attentionSyncBarrierFS) arm() {
	fs.mu.Lock()
	fs.armed = true
	fs.mu.Unlock()
}

func (fs *attentionSyncBarrierFS) rearm() {
	fs.mu.Lock()
	fs.armed = true
	fs.syncEntered = make(chan struct{})
	fs.allowSync = make(chan struct{})
	fs.enterOnce = sync.Once{}
	fs.releaseOnce = sync.Once{}
	fs.mu.Unlock()
}

func (fs *attentionSyncBarrierFS) release() {
	fs.releaseOnce.Do(func() { close(fs.allowSync) })
}

type attentionSyncBarrierFile struct {
	afero.File
	fs *attentionSyncBarrierFS
}

func (file *attentionSyncBarrierFile) Sync() error {
	if err := file.File.Sync(); err != nil {
		return err
	}
	file.fs.mu.Lock()
	armed := file.fs.armed
	file.fs.mu.Unlock()
	if !armed {
		return nil
	}
	file.fs.enterOnce.Do(func() { close(file.fs.syncEntered) })
	<-file.fs.allowSync
	return nil
}

type attentionFailNextSyncFS struct {
	afero.Fs
	mu        sync.Mutex
	failNext  bool
	failAfter int
	onFail    func()
}

func installResolutionSyncFailureWriter(t *testing.T, s *Session) {
	t.Helper()
	fs := &rootAttentionResolutionFailFS{Fs: afero.NewOsFs()}
	path := transcriptPath(s.stateDir, s.id)
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	s.mu.Lock()
	old := s.transcript
	s.mu.Unlock()
	if old == nil {
		t.Fatal("root attention session has no transcript writer")
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close original root attention writer: %v", err)
	}
	replacement, entries, err := transcript.OpenWriterForSessionWithFS(fs, path, s.id)
	if err != nil {
		t.Fatalf("open resolution-failing root attention writer: %v", err)
	}
	replacement.TrackFailures(entries, s.fork.divergence)
	s.mu.Lock()
	s.transcript = replacement
	s.mu.Unlock()
}

type rootAttentionResolutionFailFS struct {
	afero.Fs
	mu     sync.Mutex
	armed  bool
	failed bool
}

func (fs *rootAttentionResolutionFailFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &rootAttentionResolutionFailFile{File: file, fs: fs}, nil
}

func (fs *rootAttentionResolutionFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &rootAttentionResolutionFailFile{File: file, fs: fs}, nil
}

type rootAttentionResolutionFailFile struct {
	afero.File
	fs *rootAttentionResolutionFailFS
}

func (file *rootAttentionResolutionFailFile) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"kind":"ATTENTION_RESOLUTION"`)) {
		file.fs.mu.Lock()
		if !file.fs.failed {
			file.fs.armed = true
		}
		file.fs.mu.Unlock()
	}
	return file.File.Write(p)
}

func (file *rootAttentionResolutionFailFile) Sync() error {
	file.fs.mu.Lock()
	fail := file.fs.armed && !file.fs.failed
	if fail {
		file.fs.armed = false
		file.fs.failed = true
	}
	file.fs.mu.Unlock()
	if fail {
		return errors.New("injected root attention resolution sync failure")
	}
	return file.File.Sync()
}

func newAttentionFailNextSyncFS() *attentionFailNextSyncFS {
	return &attentionFailNextSyncFS{Fs: afero.NewOsFs()}
}

func (fs *attentionFailNextSyncFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &attentionFailNextSyncFile{File: file, fs: fs}, nil
}

func (fs *attentionFailNextSyncFS) failNextSync() {
	fs.failNextSyncWith(nil)
}

func (fs *attentionFailNextSyncFS) failNextSyncWith(onFail func()) {
	fs.failSyncAfter(0, onFail)
}

func (fs *attentionFailNextSyncFS) failSyncAfter(successfulSyncs int, onFail func()) {
	fs.mu.Lock()
	fs.failNext = true
	fs.failAfter = successfulSyncs
	fs.onFail = onFail
	fs.mu.Unlock()
}

type attentionFailNextSyncFile struct {
	afero.File
	fs *attentionFailNextSyncFS
}

type attentionAmbiguousSyncFS struct {
	afero.Fs
	mu                   sync.Mutex
	failSyncs            int
	failNextTruncate     bool
	failureInjected      bool
	successfulAfterFault int
}

func newAttentionAmbiguousSyncFS() *attentionAmbiguousSyncFS {
	return &attentionAmbiguousSyncFS{Fs: afero.NewOsFs()}
}

func (fs *attentionAmbiguousSyncFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &attentionAmbiguousSyncFile{File: file, fs: fs}, nil
}

func (fs *attentionAmbiguousSyncFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &attentionAmbiguousSyncFile{File: file, fs: fs}, nil
}

func (fs *attentionAmbiguousSyncFS) failNextResolutionDurability() {
	fs.failNextAmbiguousDurability(1)
}

func (fs *attentionAmbiguousSyncFS) failNextAmbiguousDurability(syncFailures int) {
	fs.mu.Lock()
	fs.failSyncs = syncFailures
	fs.failNextTruncate = true
	fs.failureInjected = true
	fs.successfulAfterFault = 0
	fs.mu.Unlock()
}

func (fs *attentionAmbiguousSyncFS) successfulSyncsAfterFailure() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.successfulAfterFault
}

type attentionAmbiguousSyncFile struct {
	afero.File
	fs *attentionAmbiguousSyncFS
}

func (file *attentionAmbiguousSyncFile) Sync() error {
	file.fs.mu.Lock()
	if file.fs.failSyncs > 0 {
		file.fs.failSyncs--
		file.fs.mu.Unlock()
		return errors.New("injected attention resolution sync failure")
	}
	failureInjected := file.fs.failureInjected
	file.fs.mu.Unlock()
	if err := file.File.Sync(); err != nil {
		return err
	}
	if failureInjected {
		file.fs.mu.Lock()
		file.fs.successfulAfterFault++
		file.fs.mu.Unlock()
	}
	return nil
}

func (file *attentionAmbiguousSyncFile) Truncate(size int64) error {
	file.fs.mu.Lock()
	if file.fs.failNextTruncate {
		file.fs.failNextTruncate = false
		file.fs.mu.Unlock()
		return errors.New("injected attention resolution rollback failure")
	}
	file.fs.mu.Unlock()
	return file.File.Truncate(size)
}

func (file *attentionFailNextSyncFile) Sync() error {
	file.fs.mu.Lock()
	fail := file.fs.failNext
	if fail && file.fs.failAfter > 0 {
		file.fs.failAfter--
		file.fs.mu.Unlock()
		return file.File.Sync()
	}
	file.fs.failNext = false
	onFail := file.fs.onFail
	file.fs.onFail = nil
	file.fs.mu.Unlock()
	if fail {
		if onFail != nil {
			onFail()
		}
		return errors.New("injected attention resolution sync failure")
	}
	return file.File.Sync()
}

func TestStableDelegateAttention_ReachableColdOwnerRetainsAttentionAfterRestoreFailure(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 2, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_owner", "", true)
	const attentionID = "delegate:reachable-owner"
	ownerPath := transcriptPath(c.stateDir, "child-dlg_owner")
	writeDelegateAttentionTranscript(t, ownerPath, "child-dlg_owner", attentionID)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	metaPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_owner.meta.json")
	fresh := newAttentionRepairRoot(c.stateDir, nil)
	fresh.cfg.testOnly.delegateRestoreReadFile = func(path string) ([]byte, error) {
		if path == metaPath {
			return nil, &os.PathError{Op: "read", Path: path, Err: syscall.EIO}
		}
		return os.ReadFile(path)
	}
	if err := fresh.bootstrapDelegateResources(); err == nil {
		t.Fatal("bootstrap accepted a transient cold-owner restore inspection failure")
	}
	ownerFold, err := readDelegateAttentionFold(ownerPath, "child-dlg_owner")
	if err != nil {
		t.Fatalf("read reachable owner attention: %v", err)
	}
	if pending := ownerFold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("reachable cold owner attention after transient failure = %#v, want retained", pending)
	}
	rootFold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, "root-session"), "root-session")
	if err != nil {
		t.Fatalf("read root attention: %v", err)
	}
	if _, escalated := rootFold.content[attentionID]; escalated {
		t.Fatal("transient cold-owner restore failure escalated attention to root")
	}
}

func TestStableDelegateAttention_UnreachableOwnerTransfersToNearestReachableAncestor(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 3, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_parent", "", true)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "dlg_parent", false)
	const attentionID = "delegate:unreachable-child"
	parentPath := transcriptPath(c.stateDir, "child-dlg_parent")
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	writeEmptyAttentionTranscript(t, parentPath, "child-dlg_parent")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionID)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	fresh := newAttentionRepairRoot(c.stateDir, nil)
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap unreachable attention repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	parentFold, err := readDelegateAttentionFold(parentPath, "child-dlg_parent")
	if err != nil {
		t.Fatalf("read ancestor attention: %v", err)
	}
	if pending := parentFold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) || parentFold.content[attentionID].Text() != "attention" {
		t.Fatalf("nearest ancestor attention = pending:%#v content:%#v", pending, parentFold.content[attentionID])
	}
	childFold, err := readDelegateAttentionFold(childPath, "child-dlg_child")
	if err != nil {
		t.Fatalf("read child attention: %v", err)
	}
	if got := childFold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("unreachable child resolution = %q, want discarded after transfer", got)
	}
	rootFold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, "root-session"), "root-session")
	if err != nil {
		t.Fatalf("read root attention: %v", err)
	}
	if _, present := rootFold.content[attentionID]; present {
		t.Fatal("attention skipped the nearest reachable delegate ancestor")
	}
}

func TestStableDelegateAttention_AncestorFsyncPrecedesChildDiscard(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 3, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_parent", "", true)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "dlg_parent", false)
	const attentionID = "delegate:ordered-transfer"
	parentPath := transcriptPath(c.stateDir, "child-dlg_parent")
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	writeEmptyAttentionTranscript(t, parentPath, "child-dlg_parent")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionID)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	fs := newAttentionPathOrderFS()
	fresh := newAttentionRepairRoot(c.stateDir, nil)
	fresh.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap ordered attention repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	paths := fs.syncedPaths()
	parentIndex, childIndex := pathIndex(paths, parentPath), pathIndex(paths, childPath)
	if parentIndex < 0 || childIndex < 0 || parentIndex >= childIndex {
		t.Fatalf("attention repair fsync order = %#v, want ancestor %q before child %q", paths, parentPath, childPath)
	}
}

func TestStableDelegateAttention_ConsumedEntryIsNeverEscalated(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 3, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_parent", "", true)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "dlg_parent", false)
	const attentionID = "delegate:already-consumed"
	parentPath := transcriptPath(c.stateDir, "child-dlg_parent")
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	writeEmptyAttentionTranscript(t, parentPath, "child-dlg_parent")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionID)
	if err := appendColdAttentionResolution(childPath, "child-dlg_child", []string{attentionID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("append consumed resolution: %v", err)
	}
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	fresh := newAttentionRepairRoot(c.stateDir, nil)
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap consumed attention repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	parentFold, err := readDelegateAttentionFold(parentPath, "child-dlg_parent")
	if err != nil {
		t.Fatalf("read ancestor attention: %v", err)
	}
	if _, present := parentFold.content[attentionID]; present {
		t.Fatal("consumed child attention was escalated")
	}
	childFold, err := readDelegateAttentionFold(childPath, "child-dlg_child")
	if err != nil {
		t.Fatalf("read consumed child attention: %v", err)
	}
	if got := childFold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("consumed child resolution changed to %q", got)
	}
}

func TestStableDelegateAttention_StartupRepairUsesNoProvider(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 2, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "", false)
	const attentionID = "delegate:provider-free-transfer"
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	rootPath := transcriptPath(c.stateDir, "root-session")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionID)
	writeEmptyAttentionTranscript(t, rootPath, "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	client := llm.NewClient()
	client.Register(&delegateAttentionPanicProvider{})
	fresh := newAttentionRepairRoot(c.stateDir, client)
	fresh.cfg.testOnly.sessionInitFault = func(point string) error {
		panic("startup attention repair constructed a Session at " + point)
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("provider-free startup attention repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	rootFold, err := readDelegateAttentionFold(rootPath, "root-session")
	if err != nil {
		t.Fatalf("read root attention: %v", err)
	}
	if pending := rootFold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("provider-free root transfer = %#v, want %q", pending, attentionID)
	}
	childFold, err := readDelegateAttentionFold(childPath, "child-dlg_child")
	if err != nil {
		t.Fatalf("read child attention: %v", err)
	}
	if got := childFold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("provider-free child discard = %q", got)
	}
}

func seedStableAttentionRepairDelegate(t *testing.T, c *delegateTreeController, id, parentID string, reachable bool) {
	t.Helper()
	event := delegateControllerCreatedEvent(id, parentID)
	event.Created.Descriptor.ResolvedProfileID = "openai"
	event.Created.Descriptor.ResolvedModel = "gpt-5.2"
	c.mu.Lock()
	_, err := c.appendLocked(event)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed attention repair delegate %s: %v", id, err)
	}
	if !reachable {
		return
	}
	parentSessionID := "root-session"
	if parentID != "" {
		parentSessionID = "child-" + parentID
	}
	if err := schema.SaveSessionMeta(c.stateDir, schema.SessionMeta{
		ID:              "child-" + id,
		ParentSessionID: parentSessionID,
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		IsSubagent:      true,
	}); err != nil {
		t.Fatalf("save reachable delegate metadata %s: %v", id, err)
	}
}

func writeEmptyAttentionTranscript(t *testing.T, path, sessionID string) {
	t.Helper()
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create empty attention transcript %s: %v", sessionID, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close empty attention transcript %s: %v", sessionID, err)
	}
}

func copyDelegateJournalForBootstrap(t *testing.T, c *delegateTreeController, sourcePath string) {
	t.Helper()
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read delegate journal: %v", err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("close delegate journal: %v", err)
	}
	destination := filepath.Join(jobsDir(c.stateDir, "root-session"), "delegates.jsonl")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatalf("create bootstrap delegate directory: %v", err)
	}
	if err := os.WriteFile(destination, raw, 0o644); err != nil {
		t.Fatalf("write bootstrap delegate journal: %v", err)
	}
}

func newAttentionRepairRoot(stateDir string, client *llm.Client) *Session {
	return &Session{
		id:       "root-session",
		stateDir: stateDir,
		client:   client,
		state:    SessionIdle,
		cfg: SessionConfig{
			StateDir:                   stateDir,
			MaxConcurrentDelegateTurns: 4,
		},
	}
}

type attentionPathOrderFS struct {
	afero.Fs
	mu    sync.Mutex
	paths []string
}

func newAttentionPathOrderFS() *attentionPathOrderFS {
	return &attentionPathOrderFS{Fs: afero.NewOsFs()}
}

func (fs *attentionPathOrderFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &attentionPathOrderFile{File: file, fs: fs, path: filepath.Clean(name)}, nil
}

func (fs *attentionPathOrderFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &attentionPathOrderFile{File: file, fs: fs, path: filepath.Clean(name)}, nil
}

func (fs *attentionPathOrderFS) syncedPaths() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.paths...)
}

type attentionPathOrderFile struct {
	afero.File
	fs   *attentionPathOrderFS
	path string
}

func (file *attentionPathOrderFile) Sync() error {
	if err := file.File.Sync(); err != nil {
		return err
	}
	file.fs.mu.Lock()
	file.fs.paths = append(file.fs.paths, file.path)
	file.fs.mu.Unlock()
	return nil
}

func pathIndex(paths []string, want string) int {
	want = filepath.Clean(want)
	for index, path := range paths {
		if filepath.Clean(path) == want {
			return index
		}
	}
	return -1
}
