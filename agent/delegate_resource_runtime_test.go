package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/internal/delegatestore"
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
	return coldStableDelegateFixture{meta: meta, client: client, profile: profile, stateDir: stateDir, workspace: workspace, adapter: adapter, delegateID: delegateID}
}
