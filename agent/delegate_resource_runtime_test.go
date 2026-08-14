package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	toolpkg "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
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

func TestDelegateResourceRuntime_ConcurrentIdleSendsStartOneGeneration(t *testing.T) {
	root, fixture, entered, release := newBlockingColdDelegateRuntime(t)
	start := make(chan struct{})
	outcomes := make(chan stableDelegateSendOutcome, 2)
	for _, message := range []string{"first", "second"} {
		message := message
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
		_, err := c.CompleteModelRequest(claim, runtime.delegateModelHistorySnapshot())
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
	continued, plans, err := c.BeginSettlement(delegateLease{delegateID: "dlg_target", generation: 1}, &packet)
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
	continued, _, err := c.BeginSettlement(lease, &packet)
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
	if continued, _, err := c.BeginSettlement(lease, finish.packet); err != nil || continued {
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
		if continued, _, err := controller.BeginSettlement(lease, exhausted.packet); err != nil || continued {
			t.Fatalf("BeginSettlement exhaustion = continued:%t err:%v", continued, err)
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
		assertLastDelegateBatchKinds(t, replayPath, delegatestore.EventDelegateRunFinished, delegatestore.EventDelegateResumabilityClosed)
	})

	t.Run("cancellation replay", func(t *testing.T) {
		controller, replayPath := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerRunning(t, controller, "dlg_cancelled", "")
		endedAt := time.Date(2026, 8, 14, 12, 30, 0, 0, time.UTC)
		cancelled := stableDelegateFinishFromRun(delegateTerminalRunInputs{runErr: context.Canceled, endedAt: endedAt})
		lease := delegateLease{delegateID: "dlg_cancelled", generation: 1}
		if continued, _, err := controller.BeginSettlement(lease, cancelled.packet); err != nil || continued {
			t.Fatalf("BeginSettlement cancellation = continued:%t err:%v", continued, err)
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
	continued, plans, err := root.delegateController.BeginSettlement(lease, finish.packet)
	if err != nil || continued {
		t.Fatalf("BeginSettlement inline = continued:%t err:%v", continued, err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("execute settlement plans: %v", err)
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
	return outcome.result
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
		emit:                     func(events.EventKind, events.EventData) {},
		abort:                    func(context.Context) error { return nil },
		drainSteering:            func() []steeringMessage { return nil },
		prependSteering:          func([]steeringMessage) {},
		resultToolName:           func() string { return "communicate" },
		setCommunicateResult:     func(string, string, string) {},
		setCommunicateStructured: func(raw any) { captured = raw },
	}
	reg := toolpkg.NewRegistry()
	def := toolpkg.DefCommunicateNamed("communicate")
	parameters := toolpkg.CloneSchemaMap(def.Parameters)
	properties := parameters["properties"].(map[string]any)
	properties["output"] = map[string]any{"type": "null"}
	def.Parameters = parameters
	if err := reg.Register(toolpkg.RegisteredTool{
		Tool: llm.Tool{Definition: def},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) { return nil, nil },
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
	assertLastDelegateBatchKinds(t, path, delegatestore.EventDelegateRunFinished, delegatestore.EventDelegateResumabilityClosed)
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
	continued, _, err := c.BeginSettlement(lease, finish.packet)
	if err != nil || continued {
		t.Fatalf("BeginSettlement = continued:%t err:%v", continued, err)
	}
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
	defer func() { _ = root.delegateController.AbortStart(reservation) }()
	sub, restored, err := (delegateRuntime{owner: root}).restoreIdle(reservation)
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
	defer func() { _ = root.delegateController.AbortStart(reservation) }()
	sub, _, err := (delegateRuntime{owner: root}).restoreIdle(reservation)
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
	defer func() { _ = root.delegateController.AbortStart(reservation) }()
	if _, _, err := (delegateRuntime{owner: root}).restoreIdle(reservation); err == nil {
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
