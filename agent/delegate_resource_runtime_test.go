package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func TestDelegateResourceRuntime_RunningSendPersistsBeforeAck(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	fs := &delegateSteerBarrierFS{Fs: afero.NewMemMapFs()}
	attachDelegateSteerRuntime(t, c, "dlg_target", fs)
	fs.controller = c
	fs.syncEntered = make(chan struct{})
	fs.allowSync = make(chan struct{})
	fs.blockSync = true

	result := make(chan error, 1)
	go func() {
		_, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "persist me")
		result <- err
	}()
	<-fs.syncEntered
	select {
	case err := <-result:
		t.Fatalf("Steer returned before transcript fsync: %v", err)
	default:
	}
	close(fs.allowSync)
	if err := <-result; err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if !fs.controllerWasUnlocked {
		t.Fatal("controller mutex was held at the transcript durability boundary")
	}
}

func TestDelegateResourceRuntime_RestoresDescriptorPluginDirs(t *testing.T) {
	pluginDir := makePluginDir(t, "delegate-selected")
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
	})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	aggregate := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID)
	got := subagentConfigFromFrozenDescriptor(aggregate.Descriptor.Config, SessionConfig{PluginDirs: []string{"/parent/plugin"}})
	if !slices.Equal(got.PluginDirs, []string{pluginDir}) {
		t.Fatalf("restored delegate PluginDirs = %v, want [%q]", got.PluginDirs, pluginDir)
	}
}

func TestRestoredDelegatePostStartPopulationEmitsTaskCorrection(t *testing.T) {
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.TaskTemplates = []taskpkg.TaskTemplate{{
			Title:  "Restored committed workflow",
			Prompt: "Resume the committed workflow.",
		}}
	})
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	var recorder currentWorkEventRecorder
	root.SetDescendantEventFunc(recorder.record)

	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored {
		t.Fatal("restoreIdle unexpectedly reused a resident child")
	}
	defer func() {
		sub.sess.discardRestoredCandidate()
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(context.Canceled, "test_cleanup"))
	}()

	relevant := childCurrentWorkEvents(recorder.snapshot(), fixture.childID)
	if len(relevant) < 2 {
		t.Fatalf("restored child current-work events = %#v, want SessionStart then TaskUpdated", relevant)
	}
	if relevant[0].Kind != events.EventSessionStart || relevant[1].Kind != events.EventTaskUpdated {
		t.Fatalf("restored child current-work order = [%s, %s], want start then update", relevant[0].Kind, relevant[1].Kind)
	}
	start := relevant[0].Data.(events.SessionStartData)
	if start.CurrentWork == nil || start.CurrentWork.Tasks == nil || start.CurrentWork.Tasks.Total != 0 {
		t.Fatalf("restored child start = %+v, want empty pre-fallback tasks", start.CurrentWork)
	}
	update := relevant[1].Data.(events.TaskUpdatedData)
	if update.Current == nil || update.Current.Description != "Restored committed workflow" {
		t.Fatalf("restored task correction = %+v", update)
	}
	if update.TaskStoreOwnerSessionID != fixture.childID {
		t.Fatalf("restored task correction owner = %q, want child %q", update.TaskStoreOwnerSessionID, fixture.childID)
	}
}

func TestDelegateResourceRuntime_RunningSendDoesNotStartSuccessor(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	owner := &Session{delegateController: c, delegateRootSessionID: "root-session"}
	outcome := (delegateRuntime{owner: owner}).send(context.Background(), "dlg_target", "steer", 0)
	if outcome.result.Err != nil || outcome.result.Action != "steered" {
		t.Fatalf("running send = %#v", outcome.result)
	}
	if generation := c.durable["dlg_target"].Generation; generation != 1 {
		t.Fatalf("running send generation = %d, want 1", generation)
	}
}

func TestDelegateResourceRuntime_PositiveWaitCannotSteerAfterIdleToRunningTransition(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	delegateID := "dlg_target"
	seedDelegateControllerIdle(t, c, delegateID, "")
	root := &Session{delegateController: c, delegateRootSessionID: "root-session"}
	var liveRuntime *Session
	var interleavingLease delegateLease
	root.cfg.testOnly.delegateSendBeforePositiveWaitAdmission = func() {
		reservation, reserveErr := root.delegateController.ReserveStart(rootDelegateActor("root-session"), delegateID)
		if reserveErr != nil {
			t.Fatalf("interleaving ReserveStart: %v", reserveErr)
		}
		started, commitErr := root.delegateController.CommitStart(reservation)
		if commitErr != nil {
			t.Fatalf("interleaving CommitStart: %v", commitErr)
		}
		interleavingLease = started.lease
		liveRuntime = attachDelegateSteerRuntime(t, root.delegateController, delegateID, afero.NewMemMapFs())
		if started.lease.generation != 1 {
			t.Fatalf("interleaving generation = %d, want 1", started.lease.generation)
		}
	}

	outcome := (delegateRuntime{owner: root}).send(context.Background(), delegateID, "must not steer", 1000)
	if outcome.result.Err == nil || !errors.Is(outcome.result.Err, errDelegateTargetBusy) {
		t.Fatalf("positive-wait interleaving outcome = %#v, want busy refusal", outcome.result)
	}
	if outcome.result.Action == "steered" {
		t.Fatal("positive wait durably steered through the idle-to-running transition")
	}
	c = root.delegateController
	c.mu.Lock()
	aggregate := c.durable[delegateID]
	live := c.live[delegateID]
	pendingSteers := 0
	if live != nil {
		pendingSteers = len(live.pendingSteers)
	}
	steeringClaims := len(c.steeringClaims)
	c.mu.Unlock()
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseRunning || steeringClaims != 0 || pendingSteers != 0 {
		t.Fatalf("interleaving state = aggregate:%#v steeringClaims:%d pendingSteers:%d", aggregate, steeringClaims, pendingSteers)
	}
	if liveRuntime == nil {
		t.Fatal("interleaving did not install live runtime")
	}
	if _, err := c.FinishGeneration(interleavingLease, delegateFinish{}); err != nil {
		t.Fatalf("finish interleaving generation: %v", err)
	}
}

func TestDelegateResourceRuntime_IdleSendReservesOneSuccessor(t *testing.T) {
	root, fixture, entered, release := newBlockingColdDelegateRuntime(t)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "continue once", 0)
	if outcome.result.Err != nil || outcome.result.Action != "started" {
		t.Fatalf("idle send = %#v", outcome.result)
	}
	<-entered
	c := root.delegateController
	c.mu.Lock()
	aggregate := c.durable[fixture.delegateID]
	generation := aggregate.Generation
	phase := aggregate.Phase
	reservations := len(c.reservations)
	c.mu.Unlock()
	if generation != 1 || phase != delegatestore.PhaseRunning || reservations != 0 {
		t.Fatalf("successor state = generation:%d phase:%s reservations:%d", generation, phase, reservations)
	}
	close(release)
}

func TestDelegateResourceRuntime_IdleRestoreFailureCommitsBeforeConstruction(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	wantErr := errors.New("injected cold delegate construction failure")
	committedBeforeConstruction := false
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point != "builtin_agents" {
			return nil
		}
		root.delegateController.mu.Lock()
		aggregate := root.delegateController.durable[fixture.delegateID]
		committedBeforeConstruction = aggregate != nil && aggregate.Generation == 1 && aggregate.CurrentRunOpen && aggregate.Phase == delegatestore.PhaseRunning
		root.delegateController.mu.Unlock()
		return wantErr
	}

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "resume after commit", 0)
	if !committedBeforeConstruction {
		t.Fatal("cold delegate construction began before run_started committed")
	}
	if outcome.result.DelegateID != fixture.delegateID || !errors.Is(outcome.result.Err, wantErr) {
		t.Fatalf("post-commit restore failure = %#v, want stable identity and construction error", outcome.result)
	}
	if outcome.result.Status != jobstore.StatusFailed || outcome.result.Reason != "construction_failed" || outcome.result.Resumable == nil || !*outcome.result.Resumable || outcome.result.RunningInBackground || outcome.result.Action != "completed" {
		t.Fatalf("post-commit restore failure state = %#v, want completed failed/resumable", outcome.result)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID)
	if aggregate.Generation != 1 || aggregate.Phase != delegatestore.PhaseIdle || aggregate.CurrentRunOpen || !aggregate.Resumable || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "construction_failed" {
		t.Fatalf("durable restore failure = %#v, want generation-1 failed/resumable idle", aggregate)
	}
	if got := len(fixture.adapter.Requests()); got != 0 {
		t.Fatalf("provider requests after cold construction failure = %d", got)
	}
}

func TestDelegateResourceRuntime_RegisteredIdleRestoreFailureReturnsStableResult(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return errors.New("injected registered cold restore failure")
		}
		return nil
	}

	call := root.reg.ExecuteCall(context.Background(), root.env, llm.ToolCallData{
		ID:        "registered-cold-restore-failure",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"resume through registered tool"}`, fixture.delegateID)),
	})
	if call.IsError {
		t.Fatalf("registered post-commit restore failure returned a transport error: %s", call.Output)
	}
	var result struct {
		DelegateID          string `json:"delegate_id"`
		Status              string `json:"status"`
		Reason              string `json:"reason"`
		Resumable           *bool  `json:"resumable"`
		RunningInBackground bool   `json:"running_in_background"`
		Action              string `json:"action"`
		TranscriptRef       string `json:"transcript_ref"`
	}
	if err := json.Unmarshal(toolResultJSON(call), &result); err != nil {
		t.Fatalf("decode registered post-commit result: %v", err)
	}
	if result.DelegateID != fixture.delegateID || result.Status != string(jobstore.StatusFailed) || result.Reason != "construction_failed" || result.Resumable == nil || !*result.Resumable || result.RunningInBackground || result.Action != "completed" || result.TranscriptRef == "" {
		t.Fatalf("registered post-commit restore failure = %#v", result)
	}
}

func TestDelegateResourceRuntime_RegisteredIdleSendClampsInlineWaitOnSuccessAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		path := "success"
		if failure {
			path = "postcommit failure"
		}
		for _, tc := range []struct {
			name      string
			requested int
			want      time.Duration
		}{
			{name: "minimum", requested: 1, want: time.Second},
			{name: "maximum", requested: 60_001, want: 60 * time.Second},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				fixture := newColdStableDelegateFixture(t, "")
				root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
				if err != nil {
					t.Fatalf("restore root: %v", err)
				}
				defer root.Close()
				if failure {
					root.cfg.testOnly.sessionInitFault = func(point string) error {
						if point == "builtin_agents" {
							return errors.New("injected clamp-path restore failure")
						}
						return nil
					}
				}

				callCtx, cancelCall := context.WithCancel(context.Background())
				defer cancelCall()
				callCtx = context.WithValue(callCtx, ctxToolCallID, "registered-wait-clamp")
				var observed bool
				var observedDuration time.Duration
				var observedDeadline bool
				root.cfg.testOnly.delegateInlineWaitReady = func(waitCtx context.Context, duration time.Duration) {
					observed = true
					observedDuration = duration
					_, observedDeadline = waitCtx.Deadline()
					cancelCall()
				}
				arguments, err := json.Marshal(map[string]any{
					"to":          fixture.delegateID,
					"message":     "exercise registered wait clamp",
					"max_wait_ms": tc.requested,
				})
				if err != nil {
					t.Fatal(err)
				}
				call := root.reg.ExecuteCall(callCtx, root.env, llm.ToolCallData{
					ID:        "registered-wait-clamp",
					Name:      "delegate_send",
					Arguments: arguments,
				})
				if call.IsError {
					t.Fatalf("registered %s send: %s", path, call.Output)
				}
				if !observed || !observedDeadline {
					t.Fatalf("registered %s wait was not observed with a deadline", path)
				}
				if observedDuration != tc.want {
					t.Fatalf("registered %s max_wait_ms=%d applied %s, want %s", path, tc.requested, observedDuration, tc.want)
				}
			})
		}
	}
}

func TestDelegateResourceRuntime_PostCommitFailureWaitsForPriorDelivery(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "missing-owner")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	c := root.delegateController
	firstReservation, err := c.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart first delivery: %v", err)
	}
	first, err := c.CommitStart(firstReservation)
	if err != nil {
		t.Fatalf("CommitStart first delivery: %v", err)
	}
	firstPlans := finishDelegateDeliveryGeneration(t, c, first.lease, "generation one")
	if len(firstPlans.deliveries) != 1 {
		t.Fatalf("first delivery plans = %#v", firstPlans.deliveries)
	}

	outcomes := make(chan stableDelegateSendOutcome, 1)
	go func() {
		outcomes <- (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "generation two restore failure", 60_000)
	}()
	// TRIPWIRE: the generation-two send has no dedicated completion signal -- it
	// queues behind the prior delivery under c.mu -- so this polls the
	// aggregate directly. The queue is set synchronously under the lock well
	// before this returns; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "post-commit failure queued behind prior delivery", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		aggregate := c.durable[fixture.delegateID]
		return aggregate != nil && aggregate.Generation == 2 && len(aggregate.PendingDeliveries) == 2
	})
	select {
	case outcome := <-outcomes:
		t.Fatalf("generation two returned before generation one receiver commit: %#v", outcome)
	default:
	}
	if err := root.executeDelegateMutationPlans(firstPlans); err != nil {
		t.Fatalf("commit generation one delivery: %v", err)
	}
	outcome := <-outcomes
	if outcome.result.DelegateID != fixture.delegateID || outcome.result.Status != jobstore.StatusFailed || outcome.commit == nil || outcome.commit.deliveryID != delegateDeliveryID(fixture.delegateID, 2) {
		t.Fatalf("generation two failure handoff = %#v", outcome)
	}
	ackPlans, err := outcome.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge generation two: %v", err)
	}
	if err := root.executeDelegateMutationPlans(ackPlans); err != nil {
		t.Fatalf("execute generation two acknowledgement: %v", err)
	}
}

func TestDelegateResourceRuntime_InputCompensationFailureLatchesBeforeRunReset(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	var childID string
	root.cfg.testOnly.delegateInitialInputAppend = func(child *Session) {
		childID = child.id
		child.mu.Lock()
		writer := child.transcript
		child.mu.Unlock()
		if writer == nil {
			t.Fatal("restored child transcript is unavailable")
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close child transcript: %v", err)
		}
		if err := root.delegateController.store.Close(); err != nil {
			t.Fatalf("close delegate store: %v", err)
		}
	}

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "fail input and compensation", 0)
	if outcome.result.DelegateID != fixture.delegateID || outcome.result.Err == nil || !strings.Contains(outcome.result.Err.Error(), "store is closed") {
		t.Fatalf("double-failure send result = %#v, want stable identity and durable append error", outcome.result)
	}
	c := root.delegateController
	c.mu.Lock()
	aggregate := c.durable[fixture.delegateID]
	live := c.live[fixture.delegateID]
	turnsInUse := c.turnsInUse
	c.mu.Unlock()
	if aggregate == nil || aggregate.Generation != 1 || aggregate.Phase != delegatestore.PhaseRunning || !aggregate.CurrentRunOpen || live == nil || live.binding == nil || !live.recoveryRequired || turnsInUse != 1 {
		t.Fatalf("double-failure durable state = aggregate:%#v live:%#v capacity:%d", aggregate, live, turnsInUse)
	}
	sub := root.subagents.get(childID)
	if sub == nil {
		t.Fatal("failed restored runtime was not retained")
	}
	sub.mu.Lock()
	running := sub.running
	sub.mu.Unlock()
	if running {
		t.Fatal("subagent run state was reset before durable input admission")
	}
	if got := len(fixture.adapter.Requests()); got != 0 {
		t.Fatalf("provider requests after input compensation failure = %d", got)
	}

	reopened, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("reopen delegate store for cleanup: %v", err)
	}
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
}

func TestDelegateResourceRuntime_StopOwnsColdRestoreBeforeSideEffects(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	restoreReady := make(chan struct{})
	releaseRestore := make(chan struct{})
	root.delegateRestoreBeforeSideEffects = func(*Session) {
		close(restoreReady)
		<-releaseRestore
	}

	sendDone := make(chan stableDelegateSendOutcome, 1)
	go func() {
		sendDone <- (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "race stop with cold restore", 0)
	}()
	<-restoreReady
	_, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		close(releaseRestore)
		t.Fatalf("StopSubtree: %v", err)
	}
	if len(cancelPlan.children) != 1 || cancelPlan.children[0].id != fixture.childID {
		close(releaseRestore)
		t.Fatalf("stop-owned restored runtimes = %#v, want exact child %q", cancelPlan.children, fixture.childID)
	}
	executeDelegateCancelPlan(cancelPlan)
	close(releaseRestore)
	outcome := <-sendDone
	if outcome.result.DelegateID != fixture.delegateID {
		t.Fatalf("stopped cold restore outcome = %#v, want stable delegate identity", outcome.result)
	}
}

// parentCloseColdRestoreJoinWindow bounds how long a broken close may take to
// walk past an in-flight cold restore before this test declares it did not wait.
// With the tree-stop drain neutered, close arrives at the join in single-digit
// milliseconds (measured), so this leaves roughly two orders of magnitude of
// margin; it is dead time only on the green path.
const parentCloseColdRestoreJoinWindow = time.Second

// parentCloseFailedInlineResultWindow bounds how long a parent Close may take
// to return without waiting for a failed generation's optional inline result.
// This is a real assertion (Close must NOT wait), not a hang tripwire: the
// test fails if Close is still blocked at this window, proving it waited.
const parentCloseFailedInlineResultWindow = time.Second

// TestDelegateResourceRuntime_ParentCloseWaitsForColdRestoreSideEffects proves the
// ordering by a POSITIVE observation taken on the closing goroutine, not by
// watching Close fail to return.
//
// "Close has not returned yet" is unfalsifiable here, and measurably so (kata
// 0t1y): parent close blocks on the in-flight cold restore through a chain of
// redundant joins — the delegate-tree stop drain in closeRuntimeTree, and behind
// it subagentManager.waitForReconstructions — so deleting any single one leaves
// Close still parked for as long as the restore is held: measured at 10s with
// drainStopForClose neutered, and past a 2s window with waitForReconstructions
// deleted. A test that only checks Close has not returned therefore stays green
// against every one of them.
//
// So this test observes close from inside, at the in-flight-restore join it
// reaches only after the tree-stop drain has released it, and asserts a fact
// about the RESTORE at that instant: its side effects had already run. The
// ordering is established by the production code, not by the test's own
// sequencing — the test still holds releaseRestore while it watches for that
// arrival, so an early arrival is recorded with the restore demonstrably parked.
// The window below bounds only how long a broken close is given to arrive; the
// arrival itself is the evidence, and it is read after the fact.
func TestDelegateResourceRuntime_ParentCloseWaitsForColdRestoreSideEffects(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	restoreReady := make(chan struct{})
	releaseRestore := make(chan struct{})
	restoreSideEffectsRan := make(chan struct{})
	closeEntered := make(chan struct{})
	previousCloseBudgetMintHook := closeBudgetMintHook
	var closeEnteredOnce sync.Once
	closeBudgetMintHook = func() {
		if previousCloseBudgetMintHook != nil {
			previousCloseBudgetMintHook()
		}
		closeEnteredOnce.Do(func() { close(closeEntered) })
	}
	defer func() { closeBudgetMintHook = previousCloseBudgetMintHook }()

	// The observation point: close reaching its in-flight-restore join. Record
	// whether the cold restore's side effects had already run when close got
	// here. A close that walked past the restore records false.
	restoreDrainedAtJoin := make(chan bool, 1)
	root.subagents.testBeforeReconstructionWait = func() {
		drained := false
		select {
		case <-restoreSideEffectsRan:
			drained = true
		default:
		}
		select {
		case restoreDrainedAtJoin <- drained:
		default:
		}
	}
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "reconcile_lost_jobs" {
			close(restoreReady)
			<-releaseRestore
			close(restoreSideEffectsRan)
		}
		return nil
	}

	sendDone := make(chan stableDelegateSendOutcome, 1)
	go func() {
		sendDone <- (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "race close with cold restore", 0)
	}()
	<-restoreReady
	closeDone := make(chan struct{})
	go func() {
		root.Close()
		close(closeDone)
	}()
	<-closeEntered
	// While the restore is held, close must not reach the join at all. Any
	// arrival here is recorded with the restore provably parked in its side
	// effects, so the observation is positive rather than an absence.
	select {
	case drained := <-restoreDrainedAtJoin:
		close(releaseRestore)
		<-sendDone
		<-closeDone
		t.Fatalf("parent Close reached its in-flight-restore join while the cold restore was still parked in its side effects (side effects drained: %v)", drained)
	case <-time.After(parentCloseColdRestoreJoinWindow):
	}
	close(releaseRestore)
	select {
	case drained := <-restoreDrainedAtJoin:
		if !drained {
			<-sendDone
			<-closeDone
			t.Fatal("parent Close reached its in-flight-restore join before the cold restore's side effects ran")
		}
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background close; 30s only fires on a genuine hang.
		<-sendDone
		<-closeDone
		t.Fatal("parent Close never reached its in-flight-restore join")
	}
	select {
	case <-closeDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background close; 30s only fires on a genuine hang.
		t.Fatal("parent Close did not return after cold-restore side effects drained")
	}
	<-sendDone
}

func TestDelegateResourceRuntime_ParentCloseDoesNotWaitForFailedInlineResult(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	root.cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "builtin_agents" {
			return errors.New("injected cold restore failure before inline wait")
		}
		return nil
	}
	c := root.delegateController
	firstReservation, err := c.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart first delivery: %v", err)
	}
	first, err := c.CommitStart(firstReservation)
	if err != nil {
		t.Fatalf("CommitStart first delivery: %v", err)
	}
	firstPlans := finishDelegateDeliveryGeneration(t, c, first.lease, "generation one blocks inline result")
	if len(firstPlans.deliveries) != 1 {
		t.Fatalf("first delivery plans = %#v", firstPlans.deliveries)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sendDone := make(chan stableDelegateSendOutcome, 1)
	go func() {
		sendDone <- (delegateRuntime{owner: root}).send(ctx, fixture.delegateID, "failed generation two", 60_000)
	}()
	// TRIPWIRE: the failed-generation send has no dedicated completion signal --
	// it queues behind the prior delivery under c.mu -- so this polls the
	// aggregate directly. The queue is set synchronously under the lock well
	// before this returns; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "failed generation queued behind prior delivery", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		aggregate := c.durable[fixture.delegateID]
		return aggregate != nil && aggregate.Generation == 2 && len(aggregate.PendingDeliveries) == 2
	})
	select {
	case outcome := <-sendDone:
		t.Fatalf("failed generation returned before prior delivery: %#v", outcome)
	default:
	}

	closeReachedReconstructionWait := make(chan struct{})
	root.subagents.testBeforeReconstructionWait = func() {
		close(closeReachedReconstructionWait)
	}
	closeDone := make(chan struct{})
	go func() {
		root.Close()
		close(closeDone)
	}()
	<-closeReachedReconstructionWait
	select {
	case <-closeDone:
	case <-time.After(parentCloseFailedInlineResultWindow):
		cancel()
		<-sendDone
		<-closeDone
		t.Fatal("parent Close waited for a failed generation's optional inline result")
	}
	cancel()
	<-sendDone
}

func TestDelegateResourceRuntime_ConcurrentIdleSendsStartOneGeneration(t *testing.T) {
	root, fixture, entered, release := newBlockingColdDelegateRuntime(t)
	start := make(chan struct{})
	outcomes := make(chan stableDelegateSendOutcome, 2)
	for _, message := range []string{"first", "second"} {
		go func() {
			<-start
			outcomes <- (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, message, 0)
		}()
	}
	close(start)
	first := <-outcomes
	second := <-outcomes
	<-entered
	c := root.delegateController
	c.mu.Lock()
	generation := c.durable[fixture.delegateID].Generation
	c.mu.Unlock()
	started := 0
	for _, outcome := range []stableDelegateSendOutcome{first, second} {
		if outcome.result.Action == "started" && outcome.result.Err == nil {
			started++
		}
	}
	if generation != 1 || started != 1 {
		t.Fatalf("concurrent sends = generation:%d started:%d outcomes:%#v/%#v", generation, started, first.result, second.result)
	}
	close(release)
}

func TestDelegateResourceRuntime_CallerCannotWriteIntoUnfinishedRootToolRound(t *testing.T) {
	receiver := newDelegateAttentionTestSession(t)
	receiver.mu.Lock()
	receiver.state = SessionProcessing
	receiver.mu.Unlock()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	c.rootRuntime = receiver
	receiver.delegateController = c
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	plans := finishDelegateDeliveryGeneration(t, c, lease, "finished during caller tool round")

	if err := receiver.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("execute delivery plan: %v", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(receiver.stateDir, receiver.id), receiver.id)
	if err != nil {
		t.Fatalf("read attention fold: %v", err)
	}
	if len(fold.order) != 0 {
		t.Fatalf("delegate delivery appended into unfinished caller tool round: %#v", fold.order)
	}
	if got := len(c.durable["dlg_target"].PendingDeliveries); got != 1 {
		t.Fatalf("pending deliveries = %d, want 1 until caller boundary fsync", got)
	}
}

func TestDelegateResourceRuntime_CallerNestedPersistsAtNextModelBoundary(t *testing.T) {
	receiver := newDelegateAttentionTestSession(t)
	receiver.mu.Lock()
	receiver.state = SessionProcessing
	receiver.mu.Unlock()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	c.rootRuntime = receiver
	receiver.delegateController = c
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	plans := finishDelegateDeliveryGeneration(t, c, lease, "nested caller delivery")
	if err := receiver.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("queue caller delivery: %v", err)
	}
	if err := receiver.flushPendingDelegateDeliveries(); err != nil {
		t.Fatalf("flush at model boundary: %v", err)
	}
	snapshot := receiver.delegateModelHistorySnapshot()
	if len(snapshot) != 1 || snapshot[0].AttentionID != delegateAttentionID(plans.deliveries[0].deliveryID) {
		t.Fatalf("next model-boundary history = %#v", snapshot)
	}
}

func TestDelegateResourceRuntime_CallerRootWaitsForToolRoundPersistence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "inline caller delivery").deliveries[0]
	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatalf("handoff caller delivery: %v", err)
	}
	resolution := <-waiter.resolution
	fs := newDelegateToolResultBarrierFS()
	sess := newDelegateToolResultPersistenceSession(t, c, fs)
	fs.blockSync = true
	sess.queueDelegateDeliveryCommit("delegate-send", resolution.commit)
	done := make(chan error, 1)
	go func() { done <- appendDelegateToolResultFixture(sess, "delegate-send") }()
	select {
	case <-fs.syncEntered:
	case err := <-done:
		t.Fatalf("caller returned before tool-round persistence: %v", err)
	}
	if got := len(c.durable["dlg_target"].PendingDeliveries); got != 1 {
		t.Fatalf("pending caller delivery during fsync = %d, want 1", got)
	}
	close(fs.allowSync)
	if err := <-done; err != nil {
		t.Fatalf("append tool results: %v", err)
	}
	if got := len(c.durable["dlg_target"].PendingDeliveries); got != 0 {
		t.Fatalf("pending caller delivery after fsync = %d, want 0", got)
	}
}

func TestDelegateResourceRuntime_ModelHistorySnapshotRunsAfterControllerUnlock(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	runtime := attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	claim, err := c.BeginModelRequest(delegateLease{delegateID: "dlg_target", generation: 1})
	if err != nil {
		t.Fatalf("BeginModelRequest: %v", err)
	}
	runtime.mu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := c.CompleteModelRequest(claim, runtime.delegateModelHistorySnapshot(), replayScope{})
		done <- err
	}()
	<-started
	if !c.mu.TryLock() {
		runtime.mu.Unlock()
		t.Fatal("controller mutex remained held while history snapshot waited")
	}
	c.mu.Unlock()
	runtime.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("CompleteModelRequest: %v", err)
	}
}

func TestDelegateResourceRuntime_PendingSteerWinsAtTerminalBoundary(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
	if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "continue first"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	packet := delegateControllerReportedPacket("premature finish")
	continued, plans, err := c.prepareSettlementForTest(delegateLease{delegateID: "dlg_target", generation: 1}, &packet)
	if err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	if !continued || len(plans.updates) != 0 || c.durable["dlg_target"].Phase != delegatestore.PhaseRunning {
		t.Fatalf("pending-steer settlement = continued:%t plans:%#v phase:%s", continued, plans, c.durable["dlg_target"].Phase)
	}
}

func TestDelegateResourceRuntime_CommunicateSettlesExactlyOnce(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	packet := delegateControllerReportedPacket("done")
	continued, _, err := c.prepareSettlementForTest(lease, &packet)
	if err != nil || continued {
		t.Fatalf("BeginSettlement = continued:%t err:%v", continued, err)
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCompleted}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "duplicate"}); err != nil {
		t.Fatalf("stale duplicate FinishGeneration: %v", err)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.Generation != 1 || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCompleted || len(aggregate.PendingDeliveries) != 1 {
		t.Fatalf("settled aggregate = %#v", aggregate)
	}
}

func TestDelegateResourceRuntime_CanonicalPacketReusedAcrossFinishReplayAndDelivery(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	sess := &Session{comm: communicateResult{called: true, structured: map[string]any{"answer": "stable"}}}
	endedAt := time.Date(2026, 8, 14, 12, 15, 0, 0, time.UTC)
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		session:                 sess,
		result:                  "canonical result",
		communicated:            true,
		structuredResult:        map[string]any{"answer": "stable"},
		structuredResultPresent: true,
		endedAt:                 endedAt,
	})
	want := cloneDelegateTerminalPacket(*finish.packet)
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	if continued, _, err := c.prepareSettlementForTest(lease, finish.packet); err != nil || continued {
		t.Fatalf("BeginSettlement = continued:%t err:%v", continued, err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         reopened,
		rootSessionID: "root-session",
		now:           c.now,
	})
	if err != nil {
		t.Fatalf("reopen controller: %v", err)
	}
	if _, err := restarted.Reconcile(emptyDelegateReconcileEvidence(restarted)); err != nil {
		t.Fatalf("reconcile prepared terminal: %v", err)
	}
	aggregate := restarted.durable["dlg_target"]
	if aggregate.PreparedTerminal != nil || len(aggregate.PendingDeliveries) != 1 {
		t.Fatalf("replayed terminal state = %#v", aggregate)
	}
	if aggregate.LatestOutcome == nil || !aggregate.LatestOutcome.EndedAt.Equal(endedAt) {
		t.Fatalf("replayed reported outcome = %#v, want ended_at %s", aggregate.LatestOutcome, endedAt)
	}
	if got := aggregate.PendingDeliveries[0].Packet; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered packet = %#v, want prepared packet %#v", got, want)
	}
	events, err := reopened.Load()
	if err != nil {
		t.Fatalf("load terminal events: %v", err)
	}
	prepared := 0
	for _, event := range events {
		if event.Kind != delegatestore.EventDelegateTerminalPrepared {
			continue
		}
		prepared++
		if !reflect.DeepEqual(event.TerminalPrepared.Packet, want) {
			t.Fatalf("prepared event packet = %#v, want %#v", event.TerminalPrepared.Packet, want)
		}
	}
	if prepared != 1 {
		t.Fatalf("terminal prepared event count = %d, want 1", prepared)
	}

	t.Run("typed exhaustion replay", func(t *testing.T) {
		controller, replayPath := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerRunning(t, controller, "dlg_exhausted", "")
		exhausted := stableDelegateFinishFromRun(delegateTerminalRunInputs{
			runErr: &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 23, Resumable: false},
		})
		lease := delegateLease{delegateID: "dlg_exhausted", generation: 1}
		if _, err := controller.FinishGeneration(lease, exhausted); err != nil {
			t.Fatalf("FinishGeneration exhaustion: %v", err)
		}
		if err := controller.store.Close(); err != nil {
			t.Fatalf("close exhaustion store: %v", err)
		}
		replayedStore, err := delegatestore.Open(replayPath)
		if err != nil {
			t.Fatalf("reopen exhaustion store: %v", err)
		}
		t.Cleanup(func() { _ = replayedStore.Close() })
		replayedController, err := openDelegateTreeController(delegateTreeControllerConfig{
			store:         replayedStore,
			rootSessionID: "root-session",
			now:           controller.now,
		})
		if err != nil {
			t.Fatalf("reopen exhaustion controller: %v", err)
		}
		if _, err := replayedController.Reconcile(emptyDelegateReconcileEvidence(replayedController)); err != nil {
			t.Fatalf("reconcile prepared exhaustion: %v", err)
		}
		aggregate := replayedController.durable["dlg_exhausted"]
		if aggregate.Resumable || aggregate.Phase != delegatestore.PhaseClosed || aggregate.NotResumableReason != "turn_budget_exhausted" {
			t.Fatalf("replayed exhaustion lifecycle = phase:%s resumable:%t reason:%q", aggregate.Phase, aggregate.Resumable, aggregate.NotResumableReason)
		}
		assertDelegateExhaustionJSON(t, aggregate.LatestOutcome, string(exhaustedBudgetTurns), 23, false)
		if len(aggregate.PendingDeliveries) != 1 || !reflect.DeepEqual(aggregate.PendingDeliveries[0].Packet, *exhausted.packet) {
			t.Fatalf("replayed exhaustion delivery = %#v, want canonical packet %#v", aggregate.PendingDeliveries, exhausted.packet)
		}
		assertLastDelegateBatchKinds(t, replayPath, delegatestore.EventDelegateTerminalPrepared, delegatestore.EventDelegateRunFinished, delegatestore.EventDelegateResumabilityClosed)
	})

	t.Run("cancellation replay", func(t *testing.T) {
		controller, replayPath := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerRunning(t, controller, "dlg_cancelled", "")
		endedAt := time.Date(2026, 8, 14, 12, 30, 0, 0, time.UTC)
		cancelled := stableDelegateFinishFromRun(delegateTerminalRunInputs{runErr: context.Canceled, endedAt: endedAt})
		lease := delegateLease{delegateID: "dlg_cancelled", generation: 1}
		if _, err := controller.FinishGeneration(lease, cancelled); err != nil {
			t.Fatalf("FinishGeneration cancellation: %v", err)
		}
		if err := controller.store.Close(); err != nil {
			t.Fatalf("close cancellation store: %v", err)
		}
		replayedStore, err := delegatestore.Open(replayPath)
		if err != nil {
			t.Fatalf("reopen cancellation store: %v", err)
		}
		t.Cleanup(func() { _ = replayedStore.Close() })
		replayedController, err := openDelegateTreeController(delegateTreeControllerConfig{
			store:         replayedStore,
			rootSessionID: "root-session",
			now:           controller.now,
		})
		if err != nil {
			t.Fatalf("reopen cancellation controller: %v", err)
		}
		if _, err := replayedController.Reconcile(emptyDelegateReconcileEvidence(replayedController)); err != nil {
			t.Fatalf("reconcile prepared cancellation: %v", err)
		}
		aggregate := replayedController.durable["dlg_cancelled"]
		if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeCancelled || aggregate.LatestOutcome.Reason != "cancelled" || !aggregate.LatestOutcome.EndedAt.Equal(endedAt) || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable {
			t.Fatalf("replayed cancellation = %#v", aggregate)
		}
		if len(aggregate.PendingDeliveries) != 1 || !reflect.DeepEqual(aggregate.PendingDeliveries[0].Packet, *cancelled.packet) {
			t.Fatalf("replayed cancellation delivery = %#v, want canonical packet %#v", aggregate.PendingDeliveries, cancelled.packet)
		}
	})
}

func TestDelegateResourceRuntime_CanonicalInlineDeliveryPreservesPacketSemantics(t *testing.T) {
	t.Run("typed exhaustion", func(t *testing.T) {
		finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
			runErr: &budgetExhaustionError{Budget: exhaustedBudgetToolRounds, Limit: 17, Resumable: true},
		})
		result := runStableDelegateInlinePacket(t, finish)
		if result.Status != jobstore.StatusExhausted || result.Reason != "tool_round_budget_exhausted" || result.ExhaustionBudget != string(exhaustedBudgetToolRounds) || result.ExhaustionLimit != 17 || result.Resumable == nil || !*result.Resumable {
			t.Fatalf("inline typed exhaustion = %#v", result)
		}
	})

	t.Run("explicit null", func(t *testing.T) {
		finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
			result:                  "explicit null",
			communicated:            true,
			structuredResult:        json.RawMessage(`null`),
			structuredResultPresent: true,
		})
		result := runStableDelegateInlinePacket(t, finish)
		marshaled, err := marshalDelegateSendResult(result, 64*1024)
		if err != nil {
			t.Fatalf("marshal inline result: %v", err)
		}
		state, ok := marshaled.(toolpkg.StateResult)
		if !ok {
			t.Fatalf("marshaled inline result type = %T", marshaled)
		}
		raw, err := json.Marshal(state.State)
		if err != nil {
			t.Fatalf("marshal inline state: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode inline state: %v", err)
		}
		structured, present := decoded["structured_result"]
		if !present || structured != nil || decoded["structured_result_valid"] != true {
			t.Fatalf("inline explicit-null state = %s", raw)
		}
	})

	t.Run("invalid structured result", func(t *testing.T) {
		finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
			result:       "missing structured result",
			communicated: true,
			descriptor: delegatestore.Descriptor{
				ResultSchema: json.RawMessage(`{"type":"object","required":["answer"]}`),
			},
		})
		result := runStableDelegateInlinePacket(t, finish)
		if !result.StructuredResultValidSet || result.StructuredResultValid || result.StructuredResultReason != structuredResultReasonSchemaResultMissing {
			t.Fatalf("inline invalid structured result = %#v", result)
		}
	})

	t.Run("operational metadata", func(t *testing.T) {
		startedAt := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
		endedAt := startedAt.Add(90 * time.Second)
		activityAt := endedAt.Add(-2 * time.Second)
		finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
			session:      &Session{comm: communicateResult{called: true}},
			result:       "complete",
			communicated: true,
			descriptor: delegatestore.Descriptor{
				Task:              "verify the runtime",
				Description:       "runtime verifier",
				AgentType:         "reviewer",
				RequestedModel:    "fast",
				ResolvedProfileID: "openai",
				ResolvedModel:     "gpt-5.6",
				Config:            schema.ConfigSnapshot{ReasoningEffort: "high"},
			},
			startedAt:        startedAt,
			endedAt:          endedAt,
			latestActivityAt: activityAt,
			usage: schema.CumulativeUsage{
				InputTokens:     101,
				OutputTokens:    29,
				CacheReadTokens: 7,
				TotalTokens:     130,
			},
			warnings: []string{"worktree validation retained"},
			worktree: &delegateWorktreeReport{
				Path:    "/repo/.worktrees/dlg_target",
				Branch:  "delegate/dlg_target",
				HeadSHA: "abc123",
				Ahead:   4,
				Dirty:   true,
			},
		})
		marshaled, err := marshalDelegateSendResult(runStableDelegateInlinePacket(t, finish), 64*1024)
		if err != nil {
			t.Fatalf("marshal inline operational metadata: %v", err)
		}
		state := stableToolStateMap(t, marshaled)
		wantScalars := map[string]any{
			"task":                "verify the runtime",
			"description":         "runtime verifier",
			"agent_type":          "reviewer",
			"requested_model":     "fast",
			"resolved_profile_id": "openai",
			"resolved_model":      "gpt-5.6",
			"reasoning_effort":    "high",
			"run_started_at":      startedAt.Format(time.RFC3339Nano),
			"run_ended_at":        endedAt.Format(time.RFC3339Nano),
			"latest_activity_at":  activityAt.Format(time.RFC3339Nano),
		}
		for key, want := range wantScalars {
			if got := state[key]; got != want {
				t.Fatalf("inline metadata[%q] = %#v, want %#v; state=%#v", key, got, want, state)
			}
		}
		if got := state["warnings"]; !reflect.DeepEqual(got, []any{"worktree validation retained"}) {
			t.Fatalf("inline warnings = %#v", got)
		}
		usage, ok := state["cumulative_usage"].(map[string]any)
		if !ok || usage["input_tokens"] != float64(101) || usage["output_tokens"] != float64(29) || usage["cache_read_tokens"] != float64(7) || usage["total_tokens"] != float64(130) {
			t.Fatalf("inline cumulative usage = %#v", state["cumulative_usage"])
		}
		worktree, ok := state["worktree"].(map[string]any)
		if !ok || worktree["path"] != "/repo/.worktrees/dlg_target" || worktree["branch"] != "delegate/dlg_target" || worktree["head_sha"] != "abc123" || worktree["ahead_commits"] != float64(4) || worktree["dirty"] != true {
			t.Fatalf("inline worktree = %#v", state["worktree"])
		}
	})
}

func TestDelegateResourceRuntime_GenericStopUsesCanonicalFinish(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "stop-hook-ran")
	pluginDir := writeStableOnceBlockingStopPlugin(t, marker)
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.PluginDirs = []string{pluginDir}
		// The Stop hook intentionally writes a marker outside private scratch.
		// Declare that mutating fixture scope instead of weakening the read-only
		// role floor for a hook side effect.
		descriptor.ToolNameCeiling = append(descriptor.ToolNameCeiling, "write_file")
	})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("before generic Stop continuation") },
		func(llm.Request) llm.Response { return finalResponse("after generic Stop continuation") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "run Stop hook", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
	if outcome.result.Err != nil || outcome.result.Status != jobstore.StatusCompleted || !strings.Contains(outcome.result.Output, "before generic Stop continuation") {
		t.Fatalf("stable generic Stop outcome = %#v", outcome.result)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("generic Stop provider requests = %d, want one blocked continuation", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Stop hook marker: %v", err)
	}
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatalf("load canonical finish: %v", err)
	}
	var packet *delegatestore.TerminalPacket
	for i := range events {
		if events[i].DelegateID == fixture.delegateID && events[i].TerminalPrepared != nil {
			value := events[i].TerminalPrepared.Packet
			packet = &value
		}
	}
	if packet == nil || !strings.Contains(string(packet.Message), "before generic Stop continuation") {
		t.Fatalf("generic Stop canonical packet = %#v", packet)
	}
}

func TestDelegateResourceRuntime_PositiveStopWaitKeepsReconciliationDriverAlive(t *testing.T) {
	harness := newStableStopRuntimeHarness(t)
	waitCtx := newDelegateStopWaitBarrierContext()
	result := make(chan stableJobStopInvocation, 1)
	go func() {
		value, err := jobStopTool(waitCtx, harness.root, map[string]any{
			"target":      harness.fixture.delegateID,
			"max_wait_ms": 60_000,
		}, jobToolResultDefaultMaxChar)
		result <- stableJobStopInvocation{value: value, err: err}
	}()
	select {
	case invocation := <-result:
		harness.release()
		waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
		t.Fatalf("positive stable stop returned before entering its wait: %#v, %v", invocation.value, invocation.err)
	case <-waitCtx.entered:
	}
	stop := currentDelegateStop(t, harness.root.delegateController)
	harness.root.delegateController.mu.Lock()
	driver := stop.driver
	harness.root.delegateController.mu.Unlock()
	if driver == nil {
		t.Fatal("stable stop has no reconciliation driver")
	}
	waitCtx.cancel()
	invocation := <-result
	assertStableStopPending(t, invocation, harness.fixture.delegateID)
	harness.release()
	waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
	select {
	case <-stop.done:
	case <-driver.done:
		select {
		case <-stop.done:
		default:
			t.Fatal("request wait cancellation ended reconciliation before durable stop completion")
		}
	}
	assertStableStopDurableCompletion(t, harness.root.delegateController, harness.fixture.delegateID)
}

func TestDelegateResourceRuntime_StableStopActiveCompletionReportsCancelledByRequest(t *testing.T) {
	harness := newStableStopRuntimeHarness(t)
	finalStatePublished := make(chan struct{})
	releaseFinalization := make(chan struct{})
	sub := harness.root.subagents.get(harness.fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatal("stable stop harness has no child Session")
	}
	if appended, err := sub.sess.appendDelegateNotificationDurably("attention-before-stop", "stop must discard this pending attention"); err != nil || !appended {
		t.Fatalf("append pending stop attention: appended=%t err=%v", appended, err)
	}
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentAfterFinalStatePublish = func(*subagent) {
			close(finalStatePublished)
			<-releaseFinalization
		}
	})
	waitCtx := newDelegateStopWaitBarrierContext()
	result := make(chan stableJobStopInvocation, 1)
	go func() {
		value, err := jobStopTool(waitCtx, harness.root, map[string]any{
			"target":      harness.fixture.delegateID,
			"max_wait_ms": 60_000,
		}, jobToolResultDefaultMaxChar)
		result <- stableJobStopInvocation{value: value, err: err}
	}()
	select {
	case invocation := <-result:
		harness.release()
		waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
		t.Fatalf("positive stable stop returned before entering its wait: %#v, %v", invocation.value, invocation.err)
	case <-waitCtx.entered:
	}
	harness.release()
	<-finalStatePublished
	select {
	case invocation := <-result:
		t.Fatalf("positive stable stop returned before canonical finish fsync: %#v, %v", invocation.value, invocation.err)
	default:
	}
	close(releaseFinalization)
	invocation := <-result
	assertStableStopCompleted(t, invocation, harness.fixture.delegateID, delegateLifecycleRunning, "cancelled_by_request")
	assertStableStopDurableCompletion(t, harness.root.delegateController, harness.fixture.delegateID)
	pending, err := readPendingDelegateAttention(transcriptPath(harness.fixture.stateDir, harness.fixture.childID), harness.fixture.childID)
	if err != nil {
		t.Fatalf("read attention after stable stop: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending attention after stable stop = %#v", pending)
	}
}

// TestDelegateResourceRuntime_StableStopReportsActorAndResumability proves kata
// tpb0's job_stop enrichment end to end: the completed stop result names the
// cancelling actor and the delegate's resumable classification, both in the
// structured State and in the human-readable Output text a caller actually
// reads.
func TestDelegateResourceRuntime_StableStopReportsActorAndResumability(t *testing.T) {
	harness := newStableStopRuntimeHarness(t)
	waitCtx := newDelegateStopWaitBarrierContext()
	result := make(chan stableJobStopInvocation, 1)
	go func() {
		value, err := jobStopTool(waitCtx, harness.root, map[string]any{
			"target":      harness.fixture.delegateID,
			"max_wait_ms": 60_000,
		}, jobToolResultDefaultMaxChar)
		result <- stableJobStopInvocation{value: value, err: err}
	}()
	select {
	case invocation := <-result:
		harness.release()
		waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
		t.Fatalf("positive stable stop returned before entering its wait: %#v, %v", invocation.value, invocation.err)
	case <-waitCtx.entered:
	}
	harness.release()
	invocation := <-result
	assertStableStopCompleted(t, invocation, harness.fixture.delegateID, delegateLifecycleRunning, "cancelled_by_request")

	state := stableJobStopState(t, invocation)
	wantActor := "root session " + harness.root.id
	if state.RequestedBy != wantActor {
		t.Fatalf("job_stop requested_by = %q, want %q", state.RequestedBy, wantActor)
	}
	if state.Resumable == nil || !*state.Resumable {
		t.Fatalf("job_stop resumable = %#v, want true for a fresh delegate", state.Resumable)
	}

	invoked, ok := invocation.value.(toolpkg.StateResult)
	if !ok {
		t.Fatalf("job_stop value = %T, want tool.StateResult", invocation.value)
	}
	if !strings.Contains(invoked.Output, "requested by: "+wantActor) {
		t.Fatalf("job_stop human-readable output missing the cancelling actor: %q", invoked.Output)
	}
	if !strings.Contains(invoked.Output, "resumable: yes") {
		t.Fatalf("job_stop human-readable output missing the resumable classification: %q", invoked.Output)
	}
}

func TestDelegateResourceRuntime_StableStopIdleCompletionReportsAlreadyIdle(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root := restoreSupervisionRoot(t, fixture, nil)
	value, err := jobStopTool(context.Background(), root, map[string]any{
		"target":      fixture.delegateID,
		"max_wait_ms": 60_000,
	}, jobToolResultDefaultMaxChar)
	invocation := stableJobStopInvocation{value: value, err: err}
	assertStableStopCompleted(t, invocation, fixture.delegateID, delegateLifecycleIdle, "already_idle")
	assertStableIdleStopDurableCompletion(t, root.delegateController, fixture.delegateID)
}

func TestDelegateResourceRuntime_StableStopTimeoutReportsStopRequested(t *testing.T) {
	clock := agenttest.NewFakeClock()
	harness := newStableStopRuntimeHarnessWithClock(t, clock)
	waitCtx := newDelegateStopWaitBarrierContext()
	result := make(chan stableJobStopInvocation, 1)
	go func() {
		value, err := jobStopTool(waitCtx, harness.root, map[string]any{
			"target":      harness.fixture.delegateID,
			"max_wait_ms": minJobBlockTimeoutMS,
		}, jobToolResultDefaultMaxChar)
		result <- stableJobStopInvocation{value: value, err: err}
	}()
	select {
	case invocation := <-result:
		harness.release()
		waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
		t.Fatalf("positive stable stop returned before entering its wait: %#v, %v", invocation.value, invocation.err)
	case <-waitCtx.entered:
	}
	stop := currentDelegateStop(t, harness.root.delegateController)
	clock.Advance(time.Duration(minJobBlockTimeoutMS) * time.Millisecond)
	assertStableStopPending(t, <-result, harness.fixture.delegateID)
	harness.release()
	waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
	<-stop.done
	assertStableStopDurableCompletion(t, harness.root.delegateController, harness.fixture.delegateID)
}

func TestDelegateResourceRuntime_StableStopRetryPreservesAdmissionClassification(t *testing.T) {
	harness := newStableStopRuntimeHarness(t)
	finalStatePublished := make(chan struct{})
	releaseFinalization := make(chan struct{})
	var releaseFinalizationOnce sync.Once
	releaseFinalizationNow := func() { releaseFinalizationOnce.Do(func() { close(releaseFinalization) }) }
	sub := harness.root.subagents.get(harness.fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatal("stable stop harness has no child Session")
	}
	controller := harness.root.delegateController
	lease := delegateLease{delegateID: harness.fixture.delegateID, generation: 1}
	work, err := controller.BeginShellWork(lease)
	if err != nil {
		t.Fatalf("hold admitted descendant work: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.AbortShellWork(work)
		releaseFinalizationNow()
	})
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentAfterFinalStatePublish = func(*subagent) {
			close(finalStatePublished)
			<-releaseFinalization
		}
	})

	first, err := jobStopTool(context.Background(), harness.root, map[string]any{
		"target": harness.fixture.delegateID,
	}, jobToolResultDefaultMaxChar)
	assertStableStopPending(t, stableJobStopInvocation{value: first, err: err}, harness.fixture.delegateID)
	stop := currentDelegateStop(t, controller)
	harness.release()
	<-finalStatePublished
	if _, err := controller.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("finish exact stopped generation before retry: %v", err)
	}

	second, err := jobStopTool(context.Background(), harness.root, map[string]any{
		"target": harness.fixture.delegateID,
	}, jobToolResultDefaultMaxChar)
	assertStableStopPending(t, stableJobStopInvocation{value: second, err: err}, harness.fixture.delegateID)
	if err := controller.AbortShellWork(work); err != nil {
		t.Fatalf("release admitted descendant work: %v", err)
	}
	releaseFinalizationNow()
	waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
	<-stop.done
	assertStableStopDurableCompletion(t, controller, harness.fixture.delegateID)
}

func TestDelegateResourceRuntime_TransientDriverFailureCanBeRetried(t *testing.T) {
	harness := newStableStopRuntimeHarness(t)
	transcriptPath := transcriptPath(harness.fixture.stateDir, harness.fixture.childID)
	backupPath := transcriptPath + ".driver-retry"
	if err := os.Rename(transcriptPath, backupPath); err != nil {
		t.Fatalf("hide transcript from first driver: %v", err)
	}
	if err := os.Mkdir(transcriptPath, 0o700); err != nil {
		t.Fatalf("replace transcript with directory: %v", err)
	}
	var restoreOnce sync.Once
	restoreTranscript := func() {
		restoreOnce.Do(func() {
			_ = os.Remove(transcriptPath)
			_ = os.Rename(backupPath, transcriptPath)
		})
	}
	t.Cleanup(restoreTranscript)

	first, err := jobStopTool(context.Background(), harness.root, map[string]any{
		"target": harness.fixture.delegateID,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		restoreTranscript()
		harness.release()
		waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
		t.Fatalf("first stable stop request: %v", err)
	}
	assertStableStopPending(t, stableJobStopInvocation{value: first}, harness.fixture.delegateID)
	stop := currentDelegateStop(t, harness.root.delegateController)
	<-stop.driver.done
	harness.root.delegateController.mu.Lock()
	firstDriver := stop.driver
	firstErr := firstDriver.err
	harness.root.delegateController.mu.Unlock()
	if firstErr == nil {
		restoreTranscript()
		harness.release()
		waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
		t.Fatal("first reconciliation driver did not report the transcript evidence failure")
	}
	restoreTranscript()

	second, err := jobStopTool(context.Background(), harness.root, map[string]any{
		"target": harness.fixture.delegateID,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		harness.release()
		waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
		t.Fatalf("retry stable stop request: %v", err)
	}
	assertStableStopPending(t, stableJobStopInvocation{value: second}, harness.fixture.delegateID)
	harness.root.delegateController.mu.Lock()
	retriedDriver := stop.driver
	harness.root.delegateController.mu.Unlock()
	if retriedDriver == firstDriver {
		harness.release()
		waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
		t.Fatal("retry kept the completed failed driver instead of starting one new exact driver")
	}
	harness.release()
	waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
	<-stop.done
	assertStableStopDurableCompletion(t, harness.root.delegateController, harness.fixture.delegateID)
}

func TestDelegateResourceRuntime_RootStoreCloseJoinsReconciliationDriver(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root := restoreSupervisionRoot(t, fixture, nil)
	result, _, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.delegateRootSessionID), fixture.delegateID)
	if err != nil {
		t.Fatalf("seed stable stop: %v", err)
	}
	stop := root.delegateController.stopForResult(result)
	driverErr := errors.New("driver did not finish cleanly")
	root.delegateController.mu.Lock()
	stop.driver = &delegateStopDriver{done: make(chan struct{}), err: driverErr}
	root.delegateController.stopDriver = stop.driver
	close(stop.driver.done)
	root.delegateController.mu.Unlock()
	if err := root.closeOwnedDelegateStore(); !errors.Is(err, driverErr) {
		t.Fatalf("root store close ignored its reconciliation driver result: %v", err)
	}
	if _, _, _, err := root.delegateController.StopSubtreeAndDrive(rootDelegateActor(root.delegateRootSessionID), fixture.delegateID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("stable stop retry crossed the root store-close fence: %v", err)
	}
	root.delegateController.mu.Lock()
	stop.driver.err = nil
	root.delegateController.mu.Unlock()
	if err := root.closeOwnedDelegateStore(); err != nil {
		t.Fatalf("close root delegate store after stop: %v", err)
	}
}

func TestDelegateResourceRuntime_ZeroWaitReturnsAfterRequestFsync(t *testing.T) {
	harness := newStableStopRuntimeHarness(t)
	value, err := jobStopTool(context.Background(), harness.root, map[string]any{
		"target":      harness.fixture.delegateID,
		"max_wait_ms": 0,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		harness.release()
		waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
		t.Fatalf("zero-wait stable job_stop: %v", err)
	}
	invocation := stableJobStopInvocation{value: value, err: err}
	assertStableStopPending(t, invocation, harness.fixture.delegateID)
	controller := harness.root.delegateController
	controller.mu.Lock()
	aggregate := controller.durable[harness.fixture.delegateID]
	requestSeq := aggregate.PendingStopSeq
	open := aggregate.CurrentRunOpen
	stop := controller.stop
	controller.mu.Unlock()
	if requestSeq == 0 || !open || stop == nil || stop.requestSeq != requestSeq {
		t.Fatalf("zero-wait stop returned without durable request: seq=%d open=%t stop=%#v", requestSeq, open, stop)
	}
	events, err := controller.store.Load()
	if err != nil {
		t.Fatalf("load stop request: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Seq == requestSeq && event.SubtreeStopRequested != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("zero-wait stop request %d was not fsynced", requestSeq)
	}
	harness.release()
	waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
	<-stop.done
}

type stableJobStopInvocation struct {
	value any
	err   error
}

type stableStopRuntimeHarness struct {
	root    *Session
	fixture coldStableDelegateFixture
	release func()
}

func newStableStopRuntimeHarness(t *testing.T) stableStopRuntimeHarness {
	return newStableStopRuntimeHarnessWithClock(t, nil)
}

func newStableStopRuntimeHarnessWithClock(t *testing.T, clock *agenttest.FakeClock) stableStopRuntimeHarness {
	t.Helper()
	fixture := newColdStableDelegateFixture(t, "")
	entered := make(chan struct{})
	releaseProvider := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProvider) }) }
	t.Cleanup(release)
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-releaseProvider
		return finalResponse("provider released after stop")
	}}
	root := restoreSupervisionRoot(t, fixture, clock)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "block for stop", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-entered
	return stableStopRuntimeHarness{root: root, fixture: fixture, release: release}
}

func currentDelegateStop(t *testing.T, controller *delegateTreeController) *delegateStopState {
	t.Helper()
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.stop == nil {
		t.Fatal("stable stop was not durably admitted")
	}
	return controller.stop
}

// awaitDelegateStopAdmission blocks until the controller durably admits a
// subtree stop and returns that stop. A test that goes on to trigger
// settlement (FinishGeneration, releasing the provider) MUST hold the stop it
// captured here rather than reading controller.stop back afterwards: the
// reconcile driver clears controller.stop the instant the stop settles, so the
// later read races the completion it is waiting for.
func awaitDelegateStopAdmission(t *testing.T, controller *delegateTreeController) *delegateStopState {
	t.Helper()
	var stop *delegateStopState
	// TRIPWIRE: admission is a durable fsync + local drive, expected in low
	// hundreds of ms; 5s only absorbs CI scheduling stalls.
	waitForCondition(t, 5*time.Second, "stable stop admission", func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		stop = controller.stop
		return stop != nil
	})
	return stop
}

func stableJobStopState(t *testing.T, invocation stableJobStopInvocation) jobStopResult {
	t.Helper()
	if invocation.err != nil {
		t.Fatalf("stable job_stop: %v", invocation.err)
	}
	result, ok := invocation.value.(toolpkg.StateResult)
	if !ok {
		t.Fatalf("stable job_stop value = %T, want tool.StateResult", invocation.value)
	}
	state, ok := result.State.(jobStopResult)
	if !ok {
		t.Fatalf("stable job_stop state = %T, want jobStopResult", result.State)
	}
	return state
}

func assertStableStopPending(t *testing.T, invocation stableJobStopInvocation, delegateID string) {
	t.Helper()
	state := stableJobStopState(t, invocation)
	if state.ID != delegateID || state.JobID != delegateID || state.Type != "delegate" || state.Outcome != "stop_requested" || state.Status != string(jobstore.StatusRunning) || state.PreviousStatus != string(delegateLifecycleRunning) {
		t.Fatalf("pending stable stop = %#v", state)
	}
}

func assertStableStopCompleted(t *testing.T, invocation stableJobStopInvocation, delegateID string, previous delegateLifecycle, outcome string) {
	t.Helper()
	state := stableJobStopState(t, invocation)
	if state.ID != delegateID || state.JobID != delegateID || state.Type != "delegate" || state.Outcome != outcome || state.Status != string(delegateLifecycleIdle) || state.PreviousStatus != string(previous) {
		t.Fatalf("completed stable stop = %#v", state)
	}
}

func assertStableStopDurableCompletion(t *testing.T, controller *delegateTreeController, delegateID string) {
	t.Helper()
	assertStableStopDurableCompletionState(t, controller, delegateID, true)
}

func assertStableIdleStopDurableCompletion(t *testing.T, controller *delegateTreeController, delegateID string) {
	t.Helper()
	assertStableStopDurableCompletionState(t, controller, delegateID, false)
}

func assertStableStopDurableCompletionState(t *testing.T, controller *delegateTreeController, delegateID string, wantStoppedOutcome bool) {
	t.Helper()
	controller.mu.Lock()
	aggregate := controller.durable[delegateID]
	stop := controller.stop
	controller.mu.Unlock()
	completed := stop == nil && aggregate != nil && aggregate.PendingStopSeq == 0 && !aggregate.CurrentRunOpen
	if wantStoppedOutcome {
		completed = completed && aggregate.LatestOutcome != nil && aggregate.LatestOutcome.Status == delegatestore.OutcomeStopped
	}
	if !completed {
		t.Fatalf("durable stable stop completion = stop:%#v aggregate:%#v", stop, aggregate)
	}
	events, err := controller.store.Load()
	if err != nil {
		t.Fatalf("load durable stable stop completion: %v", err)
	}
	for _, event := range events {
		if event.DelegateID == delegateID && event.SubtreeStopCompleted != nil {
			return
		}
	}
	t.Fatal("stable stop returned completed without a durable stop_completed event")
}

func writeStableOnceBlockingStopPlugin(t *testing.T, marker string) string {
	t.Helper()
	pluginDir := makePluginDir(t, "task7-generic-stop")
	hooksDir := filepath.Join(pluginDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	command := "if [ -f " + shellQuote(marker) + " ]; then printf '{}'; else : > " + shellQuote(marker) + "; printf '%s' '{\"decision\":\"block\",\"reason\":\"continue after generic Stop\"}'; fi"
	payload := map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{
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

func runStableDelegateInlinePacket(t *testing.T, finish delegateFinish) sendMessageResult {
	t.Helper()
	root, fixture, entered, release := newBlockingColdDelegateRuntime(t)
	outcomes := make(chan stableDelegateSendOutcome, 1)
	go func() {
		outcomes <- (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "continue inline", 60_000)
	}()
	<-entered
	lease := delegateLease{delegateID: fixture.delegateID, generation: 1}
	var plans delegateMutationPlans
	var err error
	if finish.outcome == delegatestore.OutcomeCompleted {
		var continued bool
		continued, plans, err = root.delegateController.prepareSettlementForTest(lease, finish.packet)
		if err != nil || continued {
			t.Fatalf("BeginSettlement inline = continued:%t err:%v", continued, err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatalf("execute settlement plans: %v", err)
		}
	}
	plans, err = root.delegateController.FinishGeneration(lease, finish)
	if err != nil {
		t.Fatalf("FinishGeneration inline: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("execute finish plans: %v", err)
	}
	outcome := <-outcomes
	close(release)
	if outcome.result.Err != nil || outcome.commit == nil {
		t.Fatalf("inline outcome = %#v", outcome)
	}
	abortUnpersistedStableDelegateOutcome(t, outcome)
	return outcome.result
}

func abortUnpersistedStableDelegateOutcome(t *testing.T, outcome stableDelegateSendOutcome) {
	t.Helper()
	if outcome.commit == nil {
		t.Fatal("terminal inline delegate outcome has no delivery commit")
	}
	if _, err := outcome.commit.Complete(false); err != nil {
		t.Fatalf("abort unpersisted inline delegate outcome: %v", err)
	}
}

func TestDelegateResourceRuntime_TerminalOutcomeSnapshotDoesNotAliasResumability(t *testing.T) {
	resumable := true
	aggregate := &delegatestore.Aggregate{
		DelegateID: "dlg_target",
		Phase:      delegatestore.PhaseIdle,
		Resumable:  true,
		LatestOutcome: &delegatestore.Outcome{
			Status:    delegatestore.OutcomeExhausted,
			Resumable: &resumable,
		},
	}
	snapshot := captureDelegateSnapshot(aggregate)
	*aggregate.LatestOutcome.Resumable = false
	if snapshot.lastOutcome == nil || snapshot.lastOutcome.Resumable == nil || !*snapshot.lastOutcome.Resumable {
		t.Fatalf("captured outcome changed through aggregate alias: %#v", snapshot.lastOutcome)
	}
}

func TestDelegateResourceRuntime_TerminalPacketUsesProductionActivityBoundary(t *testing.T) {
	endedAt := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	activityAt := endedAt.Add(-45 * time.Second)
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, controller, "dlg_target", "")
	controller.live["dlg_target"].activityAt = activityAt
	sess := &Session{
		clock:              agenttest.NewFakeClockAt(endedAt),
		delegateController: controller,
	}
	controller.live["dlg_target"].runtime = sess
	descriptor := delegatestore.Descriptor{Task: "capture production activity"}
	sub := &subagent{
		id:               "child-session",
		sess:             sess,
		startedAt:        endedAt.Add(-time.Minute),
		stableDescriptor: &descriptor,
	}
	metadata := decodeDelegatePacketMetadata(t, *sub.stableDelegateFinish("failed", errors.New("failed")).packet)
	if got := metadata["latest_activity_at"]; got != activityAt.Format(time.RFC3339Nano) {
		t.Fatalf("production latest_activity_at = %#v, want %q", got, activityAt.Format(time.RFC3339Nano))
	}
}

func TestDelegateResourceRuntime_StructuredResultExplicitNullIsPresent(t *testing.T) {
	var captured any
	deps := &toolDeps{
		emit:            func(events.EventKind, events.EventData) {},
		abort:           func(context.Context) error { return nil },
		drainSteering:   func() []steeringMessage { return nil },
		prependSteering: func([]steeringMessage) {},
		resultToolName:  func() string { return "communicate" },
		setCommunicateTerminal: func(_ context.Context, _, _, _ string, raw any) bool {
			captured = raw
			return true
		},
	}
	reg := toolpkg.NewRegistry()
	def := toolpkg.DefCommunicateNamed("communicate")
	parameters := toolpkg.CloneSchemaMap(def.Parameters)
	properties := parameters["properties"].(map[string]any)
	properties["output"] = map[string]any{"type": "null"}
	def.Parameters = parameters
	if err := reg.Register(toolpkg.RegisteredTool{
		Definition: def,
		Exec:       func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatalf("register custom communicate schema: %v", err)
	}
	registerCommunicateTool(reg, deps)
	if _, err := reg.Get("communicate").Exec(context.Background(), nil, map[string]any{
		"message": "explicit null", "end_turn": true, "output": nil,
	}); err != nil {
		t.Fatalf("communicate explicit null: %v", err)
	}
	if !bytes.Equal(captured.(json.RawMessage), json.RawMessage(`null`)) {
		t.Fatalf("captured explicit null = %#v", captured)
	}
	sess := &Session{comm: communicateResult{called: true}}
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		session:                 sess,
		result:                  "explicit null",
		communicated:            true,
		structuredResult:        captured,
		structuredResultPresent: true,
		descriptor: delegatestore.Descriptor{
			ResultSchema: json.RawMessage(`{"type":"null"}`),
		},
	})
	packet := finish.packet
	if packet == nil || !bytes.Equal(packet.StructuredResult, json.RawMessage(`null`)) {
		t.Fatalf("structured result = %s, want present explicit null", packetStructuredResult(packet))
	}
	if packet.StructuredResultValid == nil || !*packet.StructuredResultValid || packet.StructuredResultReason != "" {
		t.Fatalf("explicit-null validation = valid:%v reason:%q", packet.StructuredResultValid, packet.StructuredResultReason)
	}
}

func TestDelegateResourceRuntime_InvalidStructuredResultIsBoundedAndExplained(t *testing.T) {
	structured := map[string]any{"wrong": true}
	sess := &Session{comm: communicateResult{called: true, structured: structured}}
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		session:                 sess,
		result:                  "schema-invalid result",
		communicated:            true,
		structuredResult:        structured,
		structuredResultPresent: true,
		descriptor: delegatestore.Descriptor{
			ResultSchema: json.RawMessage(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}`),
		},
	})
	want, err := json.Marshal(structured)
	if err != nil {
		t.Fatal(err)
	}
	packet := finish.packet
	if packet == nil || !bytes.Equal(packet.StructuredResult, want) || len(packet.StructuredResult) > delegatestore.MaxTerminalStructuredResultBytes {
		t.Fatalf("bounded invalid structured result = %s, want retained %s", packetStructuredResult(packet), want)
	}
	if packet.StructuredResultValid == nil || *packet.StructuredResultValid || packet.StructuredResultReason != structuredResultReasonSchemaValidationFailed {
		t.Fatalf("invalid structured validation = valid:%v reason:%q", packet.StructuredResultValid, packet.StructuredResultReason)
	}
	tooLarge := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		result:                  "oversized result",
		communicated:            true,
		structuredResult:        strings.Repeat("x", delegatestore.MaxTerminalStructuredResultBytes),
		structuredResultPresent: true,
	})
	packet = tooLarge.packet
	if len(packet.StructuredResult) != 0 || packet.StructuredResultValid == nil || *packet.StructuredResultValid || packet.StructuredResultReason != structuredResultReasonSchemaResultTooLarge {
		t.Fatalf("oversized structured result = bytes:%d valid:%v reason:%q", len(packet.StructuredResult), packet.StructuredResultValid, packet.StructuredResultReason)
	}
}

func TestDelegateResourceRuntime_ToolRoundExhaustionIsTypedAndResumable(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		runErr: &budgetExhaustionError{Budget: exhaustedBudgetToolRounds, Limit: 17, Resumable: true},
	})
	settleDelegateTerminalRun(t, c, delegateLease{delegateID: "dlg_target", generation: 1}, finish)
	aggregate := c.durable["dlg_target"]
	assertDelegateExhaustionJSON(t, aggregate.LatestOutcome, string(exhaustedBudgetToolRounds), 17, true)
	if !aggregate.Resumable || aggregate.Phase != delegatestore.PhaseIdle || aggregate.NotResumableReason != "" {
		t.Fatalf("tool-round exhaustion lifecycle = phase:%s resumable:%t reason:%q", aggregate.Phase, aggregate.Resumable, aggregate.NotResumableReason)
	}
	assertDelegatePacketExhaustionMetadata(t, aggregate.PendingDeliveries[0].Packet, string(exhaustedBudgetToolRounds), 17, true)
}

func TestDelegateResourceRuntime_TurnExhaustionClosesResumabilityAtomically(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		runErr: &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 23, Resumable: false},
	})
	settleDelegateTerminalRun(t, c, delegateLease{delegateID: "dlg_target", generation: 1}, finish)
	aggregate := c.durable["dlg_target"]
	if aggregate.Resumable || aggregate.Phase != delegatestore.PhaseClosed || aggregate.NotResumableReason != "turn_budget_exhausted" {
		t.Fatalf("turn exhaustion lifecycle = phase:%s resumable:%t reason:%q", aggregate.Phase, aggregate.Resumable, aggregate.NotResumableReason)
	}
	assertDelegateExhaustionJSON(t, aggregate.LatestOutcome, string(exhaustedBudgetTurns), 23, false)
	assertDelegatePacketExhaustionMetadata(t, aggregate.PendingDeliveries[0].Packet, string(exhaustedBudgetTurns), 23, false)
	assertLastDelegateBatchKinds(t, path, delegatestore.EventDelegateTerminalPrepared, delegatestore.EventDelegateRunFinished, delegatestore.EventDelegateResumabilityClosed)
}

func TestDelegateResourceRuntime_TerminalPacketPreservesTaskModelEffortTimingUsageAndWorktree(t *testing.T) {
	startedAt := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(90 * time.Second)
	activityAt := endedAt.Add(-2 * time.Second)
	sess := &Session{comm: communicateResult{called: true}}
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		session:      sess,
		result:       "complete",
		communicated: true,
		descriptor: delegatestore.Descriptor{
			Task:              "verify the runtime",
			Description:       "runtime verifier",
			AgentType:         "reviewer",
			RequestedModel:    "fast",
			ResolvedProfileID: "openai",
			ResolvedModel:     "gpt-5.6",
			Config:            schema.ConfigSnapshot{ReasoningEffort: "high"},
		},
		startedAt:        startedAt,
		endedAt:          endedAt,
		latestActivityAt: activityAt,
		usage: schema.CumulativeUsage{
			InputTokens:     101,
			OutputTokens:    29,
			CacheReadTokens: 7,
			TotalTokens:     130,
		},
		warnings: []string{"worktree validation retained"},
		worktree: &delegateWorktreeReport{
			Path:    "/repo/.worktrees/dlg_target",
			Branch:  "delegate/dlg_target",
			HeadSHA: "abc123",
			Ahead:   4,
			Dirty:   true,
		},
	})
	packet := finish.packet
	if packet == nil {
		t.Fatal("terminal packet is nil")
	}
	if !reflect.DeepEqual(packet.Warnings, []string{"worktree validation retained"}) {
		t.Fatalf("terminal warnings = %#v", packet.Warnings)
	}
	metadata := decodeDelegatePacketMetadata(t, *packet)
	wantScalars := map[string]any{
		"task":                "verify the runtime",
		"description":         "runtime verifier",
		"agent_type":          "reviewer",
		"requested_model":     "fast",
		"resolved_profile_id": "openai",
		"resolved_model":      "gpt-5.6",
		"reasoning_effort":    "high",
		"run_started_at":      startedAt.Format(time.RFC3339Nano),
		"run_ended_at":        endedAt.Format(time.RFC3339Nano),
		"latest_activity_at":  activityAt.Format(time.RFC3339Nano),
	}
	for key, want := range wantScalars {
		if got := metadata[key]; got != want {
			t.Fatalf("metadata[%q] = %#v, want %#v", key, got, want)
		}
	}
	usage, ok := metadata["cumulative_usage"].(map[string]any)
	if !ok || usage["input_tokens"] != float64(101) || usage["output_tokens"] != float64(29) || usage["cache_read_tokens"] != float64(7) || usage["total_tokens"] != float64(130) {
		t.Fatalf("cumulative usage metadata = %#v", metadata["cumulative_usage"])
	}
	worktree, ok := metadata["worktree"].(map[string]any)
	if !ok || worktree["path"] != "/repo/.worktrees/dlg_target" || worktree["branch"] != "delegate/dlg_target" || worktree["head_sha"] != "abc123" || worktree["ahead"] != float64(4) || worktree["dirty"] != true {
		t.Fatalf("worktree metadata = %#v", metadata["worktree"])
	}
}

// TestDelegateResourceRuntime_CancelledRunCapturesScratchPath proves kata tpb0's
// scratch-path evidence requirement: a run that ends via ctx.Canceled (an
// external stop, not a completion) still carries its absolute scratch
// directory into the terminal packet metadata, the same way worktree evidence
// already does regardless of outcome.
func TestDelegateResourceRuntime_CancelledRunCapturesScratchPath(t *testing.T) {
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		runErr:      context.Canceled,
		descriptor:  delegatestore.Descriptor{Task: "rebuild the search index"},
		scratchPath: "/abs/scratch/dlg_target",
	})
	if finish.outcome != delegatestore.OutcomeCancelled || finish.reason != "cancelled" {
		t.Fatalf("cancelled run finish = outcome:%q reason:%q, want cancelled/cancelled", finish.outcome, finish.reason)
	}
	if finish.packet == nil {
		t.Fatal("cancelled run terminal packet is nil")
	}
	metadata := decodeDelegatePacketMetadata(t, *finish.packet)
	if got := metadata["scratch_path"]; got != "/abs/scratch/dlg_target" {
		t.Fatalf("cancelled run metadata[scratch_path] = %#v, want the run's scratch dir", got)
	}
}

// stoppedDelegateStartCommit builds the started-commit shape of issue #184: a
// delegate whose durable lastOutcome records an external stop, about to have a
// self-reported "cancelled" terminal packet applied over it.
func stoppedDelegateStartCommit(delegateID string) delegateStartCommit {
	return delegateStartCommit{
		lease: delegateLease{delegateID: delegateID, generation: 2},
		plan: delegateUpdatePlan{rows: []delegateSnapshot{{
			id:         delegateID,
			generation: 1,
			resumable:  true,
			lastOutcome: &delegatestore.Outcome{
				Status: delegatestore.OutcomeStopped,
				Reason: "stopped_by_parent",
			},
		}}},
	}
}

// selfReportedCancelledPacket is the run loop's own terminal packet from an
// external stop: self-reported as "cancelled" (ctx.Canceled), preserved
// verbatim as evidence per PR #128.
func selfReportedCancelledPacket(t *testing.T) delegatestore.TerminalPacket {
	t.Helper()
	rawMetadata, err := json.Marshal(delegateTerminalPacketMetadata{
		Outcome: delegatestore.OutcomeCancelled,
		Reason:  "cancelled",
	})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	rawMessage, err := json.Marshal("partial output gathered before cancellation")
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	return delegatestore.TerminalPacket{
		Kind:     delegatestore.PacketTerminalError,
		Message:  rawMessage,
		Metadata: rawMetadata,
	}
}

// TestDelegateResourceRuntime_StoppedSendSurvivesPacketClobber reproduces
// issue #184 at the delivery-plan site: stableDelegateFailedSendResult
// correctly seeds Status from the durable lastOutcome ("stopped"), but the
// delivery-matching loop then calls populateStableDelegateSendResult, which
// derives Status from the delegate's self-reported terminal packet
// ("cancelled"). The durable outcome must win for Status the same way it
// already does for Reason.
func TestDelegateResourceRuntime_StoppedSendSurvivesPacketClobber(t *testing.T) {
	const delegateID = "dlg_target"
	started := stoppedDelegateStartCommit(delegateID)
	plans := delegateMutationPlans{
		deliveries: []delegateDeliveryPlan{{
			delegateID: delegateID,
			deliveryID: delegateDeliveryID(delegateID, started.lease.generation),
			packet:     selfReportedCancelledPacket(t),
		}},
	}

	result := stableDelegateFailedSendResult(started, plans, errors.New("construction_failed"))

	if result.Status != jobstore.StatusStopped {
		t.Fatalf("delegate_send status = %q, want %q (durable stop outcome must survive the packet-derived clobber); reason = %q",
			result.Status, jobstore.StatusStopped, result.Reason)
	}
	if result.Reason != "stopped_by_parent" {
		t.Fatalf("delegate_send reason = %q, want %q", result.Reason, "stopped_by_parent")
	}
}

// TestDelegateResourceRuntime_StoppedSendSurvivesInlineResolutionClobber covers
// the second clobber site from issue #184: stableSendFailureOutcomeAfterDispatch
// applies the inline waiter's resolution packet via
// populateStableDelegateSendResult after the durable outcome already set
// Status/Reason, so the durable "stopped" status must survive that overwrite
// too.
func TestDelegateResourceRuntime_StoppedSendSurvivesInlineResolutionClobber(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	owner := &Session{delegateController: c, delegateRootSessionID: "root-session"}
	started := stoppedDelegateStartCommit("dlg_target")

	packet := selfReportedCancelledPacket(t)
	waiter := &delegateInlineWaiter{resolution: make(chan delegateInlineResolution, 1)}
	waiter.resolution <- delegateInlineResolution{packet: &packet, commit: &delegateToolResultCommit{}}

	outcome := (delegateRuntime{owner: owner}).stableSendFailureOutcomeAfterDispatch(
		context.Background(), started, waiter, 0, delegateMutationPlans{}, errors.New("construction_failed"), nil)

	if outcome.result.Status != jobstore.StatusStopped {
		t.Fatalf("delegate_send status = %q, want %q (durable stop outcome must survive the inline-resolution clobber); reason = %q",
			outcome.result.Status, jobstore.StatusStopped, outcome.result.Reason)
	}
	if outcome.result.Reason != "stopped_by_parent" {
		t.Fatalf("delegate_send reason = %q, want %q", outcome.result.Reason, "stopped_by_parent")
	}
}

func TestDelegateResourceRuntime_StaleGenerationCannotPublishPacket(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	packet := delegateControllerReportedPacket("stale result")
	finish := delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionReported,
		packet:      &packet,
	}
	if plans, err := c.FinishGeneration(delegateLease{delegateID: "dlg_target", generation: 0}, finish); err != nil || len(plans.deliveries) != 0 || len(plans.updates) != 0 {
		t.Fatalf("stale finish = plans:%#v err:%v", plans, err)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.Generation != 1 || aggregate.PreparedTerminal != nil || aggregate.LatestOutcome != nil || len(aggregate.PendingDeliveries) != 0 {
		t.Fatalf("stale generation published terminal state: %#v", aggregate)
	}
}

func settleDelegateTerminalRun(t *testing.T, c *delegateTreeController, lease delegateLease, finish delegateFinish) {
	t.Helper()
	if _, err := c.FinishGeneration(lease, finish); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
}

func packetStructuredResult(packet *delegatestore.TerminalPacket) string {
	if packet == nil {
		return "<nil packet>"
	}
	return string(packet.StructuredResult)
}

func decodeDelegatePacketMetadata(t *testing.T, packet delegatestore.TerminalPacket) map[string]any {
	t.Helper()
	if len(packet.Metadata) == 0 {
		t.Fatal("terminal packet metadata is absent")
	}
	var metadata map[string]any
	if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
		t.Fatalf("decode terminal metadata: %v", err)
	}
	return metadata
}

func assertDelegateExhaustionJSON(t *testing.T, outcome *delegatestore.Outcome, budget string, limit int, resumable bool) {
	t.Helper()
	if outcome == nil || outcome.Status != delegatestore.OutcomeExhausted {
		t.Fatalf("latest outcome = %#v, want exhausted", outcome)
	}
	if outcome.Reason != map[bool]string{true: "tool_round_budget_exhausted", false: "turn_budget_exhausted"}[resumable] {
		t.Fatalf("outcome reason = %q", outcome.Reason)
	}
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(raw, &projected); err != nil {
		t.Fatal(err)
	}
	if projected["exhaustion_budget"] != budget || projected["exhaustion_limit"] != float64(limit) || projected["resumable"] != resumable {
		t.Fatalf("typed exhaustion outcome = %s", raw)
	}
}

func assertDelegatePacketExhaustionMetadata(t *testing.T, packet delegatestore.TerminalPacket, budget string, limit int, resumable bool) {
	t.Helper()
	metadata := decodeDelegatePacketMetadata(t, packet)
	if metadata["exhaustion_budget"] != budget || metadata["exhaustion_limit"] != float64(limit) || metadata["resumable"] != resumable {
		t.Fatalf("typed packet exhaustion metadata = %#v", metadata)
	}
}

func assertLastDelegateBatchKinds(t *testing.T, path string, want ...delegatestore.EventKind) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) < 2 {
		t.Fatalf("delegate log lines = %d", len(lines))
	}
	var record struct {
		Events []struct {
			Kind delegatestore.EventKind `json:"kind"`
		} `json:"events"`
	}
	if err := json.Unmarshal(lines[len(lines)-1], &record); err != nil {
		t.Fatalf("decode final delegate batch: %v", err)
	}
	got := make([]delegatestore.EventKind, len(record.Events))
	for i := range record.Events {
		got[i] = record.Events[i].Kind
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("final delegate batch kinds = %v, want %v", got, want)
	}
}

func TestDelegateResourceRuntime_ColdIdleUsesCommittedConfigTemplatesAndToolCeiling(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	defer func() {
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(errors.New("test complete"), "construction_failed"))
	}()
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored {
		t.Fatal("cold idle delegate was reported retained")
	}
	defer sub.sess.discardRestoredCandidate()
	if sub.sess.cfg.MaxToolRoundsPerInput != 17 || sub.sess.cfg.ReasoningEffort != "high" {
		t.Fatalf("restored config = rounds:%d effort:%q", sub.sess.cfg.MaxToolRoundsPerInput, sub.sess.cfg.ReasoningEffort)
	}
	if got := sub.sess.reg.RegisteredNames(); len(got) != 1 || !got["communicate"] {
		t.Fatalf("restored tools = %#v, want committed ceiling", got)
	}
	tasks := sub.sess.getOrCreateTaskStore().View()
	if len(tasks) != 1 || tasks[0].Description != "Frozen workflow" || tasks[0].Prompt != "Use committed workflow" {
		t.Fatalf("restored tasks = %#v", tasks)
	}
	if got := len(fixture.adapter.Requests()); got != 0 {
		t.Fatalf("provider requests during cold construction = %d", got)
	}
}

func TestDelegateResourceRuntime_ColdIdleInheritsLiveLifetimeContext(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	owner, cancelOwner := context.WithCancel(context.Background())
	root.cfg.LifetimeContext = owner
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	defer func() {
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(errors.New("test complete"), "construction_failed"))
	}()
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	if !restored {
		t.Fatal("cold idle delegate was reported retained")
	}
	defer sub.sess.discardRestoredCandidate()

	cancelOwner()
	select {
	case <-sub.sess.sessionCtx.Done():
	default:
		t.Fatal("restored stable delegate outlived the live parent lifetime context")
	}
}

func TestDelegateResourceRuntime_ColdIdleReusesExactSharedRootTaskStore(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "root")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	want := root.getOrCreateTaskStore()
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	defer func() {
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(errors.New("test complete"), "construction_failed"))
	}()
	sub, _, err := (delegateRuntime{owner: root}).restoreIdle(started)
	if err != nil {
		t.Fatalf("restoreIdle: %v", err)
	}
	defer sub.sess.discardRestoredCandidate()
	if got := sub.sess.getOrCreateTaskStore(); got != want {
		t.Fatalf("shared TaskStore pointer = %p, want exact root pointer %p", got, want)
	}
	if !reflect.DeepEqual(sub.sess.getOrCreateTaskStore().View(), want.View()) {
		t.Fatal("shared TaskStore history forked")
	}
}

func TestDelegateResourceRuntime_ColdIdleUnavailableAncestorStoreFailsClosedProviderFree(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "missing-owner")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	defer func() {
		_, _ = root.delegateController.FailCommittedRestart(started.lease, delegatePermanentStartFailure(errors.New("test complete"), "construction_failed"))
	}()
	if _, _, err := (delegateRuntime{owner: root}).restoreIdle(started); err == nil {
		t.Fatal("missing shared task-store owner was accepted")
	}
	if got := len(fixture.adapter.Requests()); got != 0 {
		t.Fatalf("provider requests after failed owner resolution = %d", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.stateDir, "tasks", "missing-owner.json")); !os.IsNotExist(err) {
		t.Fatalf("missing owner task history was forked: %v", err)
	}
}

type coldStableDelegateFixture struct {
	meta       schema.SessionMeta
	client     *llm.Client
	profile    *provider.Profile
	stateDir   string
	workspace  string
	adapter    *fakeAdapter
	delegateID string
	childID    string
}

func newBlockingColdDelegateRuntime(t *testing.T) (*Session, coldStableDelegateFixture, <-chan struct{}, chan struct{}) {
	t.Helper()
	fixture := newColdStableDelegateFixture(t, "")
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		close(entered)
		<-release
		return communicateResponse(true, "done")
	}}
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		root.Close()
	})
	return root, fixture, entered, release
}

func newColdStableDelegateFixture(t *testing.T, sharedOwner string) coldStableDelegateFixture {
	return newColdStableDelegateFixtureConfigured(t, sharedOwner, nil)
}

func newColdStableDelegateFixtureConfigured(t *testing.T, sharedOwner string, configure func(*delegatestore.Descriptor)) coldStableDelegateFixture {
	t.Helper()
	meta, client, profile, stateDir, workspace, adapter := closedDelegateResourceBootstrapFixture(t)
	delegateID := identifier.MustNewDelegateID()
	childID := identifier.MustNewSessionID()
	config := meta.Config.Clone()
	config.MaxToolRoundsPerInput = 17
	config.ReasoningEffort = "high"
	config.AgentName = "subagent"
	config.ShareTasksWithChildren = sharedOwner != ""
	ownerID := ""
	if sharedOwner == "root" {
		ownerID = meta.ID
	} else if sharedOwner != "" {
		ownerID = sharedOwner
	}
	descriptor := delegatestore.Descriptor{
		ChildSessionID:                childID,
		TranscriptRef:                 encodeRef("", childID),
		OwnerSessionID:                meta.ID,
		VisibleSessionID:              meta.ID,
		Task:                          "resume from committed descriptor",
		AgentType:                     "default",
		ResolvedProfileID:             "openai",
		ResolvedModel:                 "gpt-5.2",
		FrozenRolePrompt:              defaultSubagentInstructions,
		TaskTemplates:                 []taskpkg.TaskTemplate{{Title: "Frozen workflow", Prompt: "Use committed workflow"}},
		ToolNameCeiling:               []string{"communicate"},
		WorkingDir:                    workspace,
		LocalEnvPolicy:                "default",
		Config:                        config,
		SharedTaskStoreOwnerSessionID: ownerID,
		Resumable:                     true,
	}
	if configure != nil {
		configure(&descriptor)
	}
	store, err := delegatestore.Open(delegateResourceStorePath(stateDir, meta.ID))
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.AppendBatch(state, []delegatestore.Event{{
		Kind:       delegatestore.EventDelegateCreated,
		TS:         time.Unix(1_700_000_200, 0).UTC(),
		DelegateID: delegateID,
		Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
	}})
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	childMeta := meta
	childMeta.ID = childID
	childMeta.ParentSessionID = meta.ID
	childMeta.IsSubagent = true
	childMeta.Config.MaxToolRoundsPerInput = 999
	childMeta.Config.ReasoningEffort = "low"
	if err := schema.SaveSessionMeta(stateDir, childMeta); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(transcriptPath(stateDir, childID), transcript.Header{
		SessionID:       childID,
		ParentSessionID: meta.ID,
		Task:            descriptor.Task,
		ProfileID:       descriptor.ResolvedProfileID,
		Model:           descriptor.ResolvedModel,
		WorkingDir:      workspace,
		Depth:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return coldStableDelegateFixture{meta: meta, client: client, profile: profile, stateDir: stateDir, workspace: workspace, adapter: adapter, delegateID: delegateID, childID: childID}
}
