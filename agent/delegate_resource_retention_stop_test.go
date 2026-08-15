package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

func TestDelegateResourceStop_StableStopIsAlwaysRecursive(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerRunning(t, c, "dlg_grandchild", "dlg_child")
	var order []string
	for _, id := range []string{"dlg_parent", "dlg_child", "dlg_grandchild"} {
		id := id
		c.live[id].binding.cancel = func() { order = append(order, id) }
	}

	result, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	if got, want := order, []string{"dlg_grandchild", "dlg_child", "dlg_parent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recursive stop cancellation order = %v, want leaf-first %v", got, want)
	}
	c.mu.Lock()
	members := len(c.stop.members)
	requestSeq := c.stop.requestSeq
	c.mu.Unlock()
	if members != 3 || requestSeq != result.requestSeq {
		t.Fatalf("recursive stop members=%d request=%d, want three members on request %d", members, requestSeq, result.requestSeq)
	}
}

func TestDelegateResourceStop_IncludeChildrenFalseIsIgnoredForDelegate(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	root := &Session{id: "root-session", delegateRootSessionID: "root-session", delegateController: c}
	c.rootRuntime = root
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")

	value, err := jobStopTool(context.Background(), root, map[string]any{
		"target":           "dlg_parent",
		"include_children": false,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("job_stop stable delegate: %v", err)
	}
	assertStableStopPending(t, stableJobStopInvocation{value: value}, "dlg_parent")
	c.mu.Lock()
	stop := c.stop
	_, childCovered := stop.members["dlg_child"]
	c.mu.Unlock()
	if !childCovered {
		t.Fatal("include_children=false excluded a stable delegate descendant")
	}
	if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_child", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish child: %v", err)
	}
	if _, err := c.FinishGeneration(delegateLease{delegateID: "dlg_parent", generation: 1}, delegateFinish{}); err != nil {
		t.Fatalf("finish parent: %v", err)
	}
	<-stop.done
}

func TestDelegateResourceStop_RequestFsyncPrecedesExternalCancellation(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	fsyncedBeforeCancel := false
	c.live["dlg_target"].binding.cancel = func() {
		events, err := c.store.Load()
		if err != nil {
			return
		}
		fsyncedBeforeCancel = len(events) != 0 && events[len(events)-1].SubtreeStopRequested != nil
	}
	_, plan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(plan)
	if !fsyncedBeforeCancel {
		t.Fatal("external cancellation ran before the durable stop request was readable")
	}
}

func TestDelegateResourceStop_DrainsRuntimeShellWatchAttentionAndDeliveryReceipts(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	rootRuntime := &Session{}
	c.rootRuntime = rootRuntime
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	c.live["dlg_target"].runtime = rootRuntime
	c.live["dlg_target"].binding.runtime = rootRuntime
	seedDelegateControllerIdle(t, c, "dlg_delivery", "dlg_target")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}

	work, err := c.BeginShellWork(lease)
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	if cancelNow, err := c.CommitShellWork(work, "job_shell", func() {}); err != nil || cancelNow {
		t.Fatalf("CommitShellWork = cancel:%t err:%v", cancelNow, err)
	}
	seedDelegateControllerDelivery(t, c, "dlg_delivery")
	deliveryPlans := c.ReplayDeliveries()
	if len(deliveryPlans) != 1 {
		t.Fatalf("delivery plans = %d, want one", len(deliveryPlans))
	}
	delivery, admitted, err := c.BeginDelivery(deliveryPlans[0])
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	watch, err := c.BeginWatchEnqueue("dlg_target", 1, "dlg_delivery", "watch-delivery", 1, false)
	if err != nil {
		t.Fatalf("BeginWatchEnqueue: %v", err)
	}
	c.mu.Lock()
	c.nextToken++
	quiet := &delegateQuietAttentionClaim{token: c.nextToken, lease: lease, receiver: rootRuntime, done: make(chan struct{})}
	c.quietClaims[quiet.token] = quiet
	c.live["dlg_target"].quietClaim = quiet
	c.mu.Unlock()

	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile with receipts: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed with runtime or process receipts still admitted")
	default:
	}
	if _, err := c.ReportShellFinished(work, "job_shell"); err != nil {
		t.Fatalf("ReportShellFinished: %v", err)
	}
	if _, err := c.CompleteDelivery(delivery, false); err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}
	if err := c.CompleteQuietAttention(quiet, false); err != nil {
		t.Fatalf("CompleteQuietAttention: %v", err)
	}
	c.AbortWatchEnqueue(watch)
	if _, err := c.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile after receipt drain: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after exact runtime and receipt drain")
	}
}

func TestDelegateResourceStop_SameTargetRetryJoinsDifferentTargetIsBusy(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerRunning(t, c, "dlg_first", "")
	seedDelegateControllerIdle(t, c, "dlg_second", "")
	first, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_first")
	if err != nil {
		t.Fatalf("first StopSubtree: %v", err)
	}
	retry, _, plans, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_first")
	if err != nil || retry.requestSeq != first.requestSeq || retry.done != first.done || len(plans.updates) != 0 {
		t.Fatalf("same-target retry = %#v plans=%#v err=%v, want exact join", retry, plans, err)
	}
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_second"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("different-target stop error = %v, want target busy", err)
	}
}

func TestDelegateResourceStop_PositiveWaitCannotOwnOrCancelDriver(t *testing.T) {
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
	<-waitCtx.entered
	stop := currentDelegateStop(t, harness.root.delegateController)
	harness.root.delegateController.mu.Lock()
	driver := stop.driver
	harness.root.delegateController.mu.Unlock()
	waitCtx.cancel()
	assertStableStopPending(t, <-result, harness.fixture.delegateID)
	select {
	case <-driver.done:
		t.Fatal("canceling one positive wait canceled or completed the reconcile driver")
	default:
	}
	harness.release()
	waitForStableSupervisionRun(t, harness.root, harness.fixture.childID)
	<-stop.done
	harness.root.delegateController.mu.Lock()
	gotDriver := stop.driver
	harness.root.delegateController.mu.Unlock()
	if gotDriver != driver {
		t.Fatal("positive wait replaced the controller-owned reconcile driver")
	}
}

func TestDelegateResourceStop_RestartCompletesPendingStopProviderFree(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("close original store: %v", err)
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("reopen delegate store: %v", err)
	}
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{store: reopened, rootSessionID: "root-session", stateDir: c.stateDir, now: c.now})
	if err != nil {
		t.Fatalf("open restarted controller: %v", err)
	}
	if _, err := reconcileDelegateResourcesForBootstrap(restarted); err != nil {
		t.Fatalf("provider-free bootstrap reconciliation: %v", err)
	}
	restarted.mu.Lock()
	stop := restarted.stop
	aggregate := restarted.durable["dlg_target"]
	restarted.mu.Unlock()
	if stop != nil || aggregate.PendingStopSeq != 0 {
		t.Fatalf("restart stop state = stop:%#v aggregate:%#v", stop, aggregate)
	}
	events, err := restarted.store.Load()
	if err != nil {
		t.Fatalf("load restarted stop events: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].SubtreeStopCompleted == nil {
		t.Fatalf("restart did not durably complete pending stop: %#v", events)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close restarted store: %v", err)
	}
}

func TestDelegateResourceStop_CompletionAppendFailureKeepsAdmissionClosed(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err == nil {
		t.Fatal("stop completion append unexpectedly succeeded on a closed store")
	}
	select {
	case <-result.done:
		t.Fatal("failed completion released the stop fence")
	default:
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart after failed stop completion = %v, want busy", err)
	}
}

func TestDelegateResourceStop_RootCloseJoinsStopAndTeardownPostorder(t *testing.T) {
	harness := newStableStopRuntimeHarness(t)
	root := harness.root
	fixture := harness.fixture
	child := root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil {
		t.Fatal("blocking stable delegate has no resident child Session")
	}
	type observation struct {
		source  string
		fsynced bool
	}
	observed := make(chan observation, 2)
	observe := func(source string) {
		root.delegateController.mu.Lock()
		aggregate := root.delegateController.durable[fixture.delegateID]
		fsynced := aggregate != nil && aggregate.PendingStopSeq != 0
		root.delegateController.mu.Unlock()
		select {
		case observed <- observation{source: source, fsynced: fsynced}:
		default:
		}
		harness.release()
	}

	root.delegateController.mu.Lock()
	originalBindingCancel := root.delegateController.live[fixture.delegateID].binding.cancel
	root.delegateController.live[fixture.delegateID].binding.cancel = func() {
		observe("controller_stop")
		originalBindingCancel()
	}
	root.delegateController.mu.Unlock()
	originalSessionCancel := child.sess.cancelFunc
	child.sess.cancelFunc = func() {
		observe("session_teardown")
		originalSessionCancel()
	}

	closed := make(chan struct{})
	go func() {
		root.Close()
		close(closed)
	}()
	first := <-observed
	<-closed
	if first.source != "controller_stop" || !first.fsynced {
		t.Fatalf("first child shutdown boundary = %+v, want durable controller stop before Session teardown", first)
	}
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[fixture.delegateID]
	stop := root.delegateController.stop
	root.delegateController.mu.Unlock()
	if stop != nil || aggregate == nil || aggregate.PendingStopSeq != 0 || aggregate.CurrentRunOpen {
		t.Fatalf("root close returned before stable stop join: stop=%#v aggregate=%#v", stop, aggregate)
	}
}

func TestDelegateResourceStop_RootCloseAbortsUnpersistedInlineDeliveryCommit(t *testing.T) {
	fixture := newColdStableDelegateFixtureConfigured(t, "", func(descriptor *delegatestore.Descriptor) {
		descriptor.Config.MaxToolRoundsPerInput = 1
	})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(false, "continue") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "exhaust", 60_000)
	if outcome.result.Err != nil || outcome.result.Status != jobstore.StatusExhausted || outcome.commit == nil {
		t.Fatalf("exhausted stable delegate = %#v", outcome.result)
	}
	root.queueDelegateDeliveryCommit("delegate-send", outcome.commit)

	c := root.delegateController
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root.close(ctx, true)
	c.mu.Lock()
	stop := c.stop
	aggregate := c.durable[fixture.delegateID]
	deliveries := len(c.deliveries)
	c.mu.Unlock()
	if stop != nil || aggregate == nil || aggregate.PendingStopSeq != 0 || deliveries != 0 {
		t.Fatalf("close with unpersisted inline result = stop:%#v aggregate:%#v deliveries:%d", stop, aggregate, deliveries)
	}
}

func TestDelegateResourceStop_ForegroundShellTimeoutAbortsUncommittedReceipt(t *testing.T) {
	c, jm, lease, clock := newStableShellReceiptHarness(t)
	failAppendN(jm, jobstore.EventJobStarted, 1)
	executor := newDelayedSuccessStreamingExecutor()
	t.Cleanup(func() {
		select {
		case <-executor.release:
		default:
			close(executor.release)
		}
	})
	beforeToken := c.nextToken
	result := make(chan shellResult, 1)
	go func() {
		result <- runShell(context.Background(), jm, executor, shellArgs{Command: "timeout before commit", BlockTimeoutMS: 1})
	}()
	clock.BlockUntil(1)
	clock.Advance(minShellBlockTimeoutMS * 1_000_000)
	got := <-result
	if got.Reason != "start_failed" {
		t.Fatalf("foreground timeout commit failure = %+v, want start_failed", got)
	}
	c.mu.Lock()
	workCount := len(c.work)
	afterToken := c.nextToken
	c.mu.Unlock()
	if afterToken != beforeToken+1 || workCount != 0 {
		t.Fatalf("uncommitted timeout receipt token %d→%d work=%d, want one Begin followed by exact Abort", beforeToken, afterToken, workCount)
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "test complete"}); err != nil {
		t.Fatalf("finish stable owner: %v", err)
	}
}

func TestDelegateResourceStop_ForegroundShellTimeoutReportsCommittedShellOnce(t *testing.T) {
	c, jm, _, clock := newStableShellReceiptHarness(t)
	executor := newDelayedSuccessStreamingExecutor()
	result := make(chan shellResult, 1)
	go func() {
		result <- runShell(context.Background(), jm, executor, shellArgs{Command: "promote then finish", BlockTimeoutMS: 1})
	}()
	clock.BlockUntil(1)
	clock.Advance(minShellBlockTimeoutMS * 1_000_000)
	got := <-result
	if got.JobID == "" || got.Reason != "foreground_timeout" {
		t.Fatalf("foreground timeout = %+v, want promoted shell", got)
	}
	c.mu.Lock()
	var token delegateWorkToken
	for _, work := range c.work {
		if work.committed && work.jobID == got.JobID {
			token = work.token
		}
	}
	c.mu.Unlock()
	if token == (delegateWorkToken{}) {
		t.Fatalf("promoted shell %q has no committed controller receipt", got.JobID)
	}
	close(executor.release)
	waitForShellDone(t, jm, got.JobID)
	c.mu.Lock()
	workCount := len(c.work)
	c.mu.Unlock()
	if workCount != 0 {
		t.Fatalf("finished promoted shell retained %d controller receipts", workCount)
	}
	if _, err := c.ReportShellFinished(token, got.JobID); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("second ReportShellFinished = %v, want exact-once stale receipt", err)
	}
}

func TestDelegateResourceStop_TimeoutRaceCannotLeakStopMembership(t *testing.T) {
	c, jm, lease, clock := newStableShellReceiptHarness(t)
	executor := newSignalCompletesStreamingExecutor()
	result := make(chan shellResult, 1)
	go func() {
		result <- runShell(context.Background(), jm, executor, shellArgs{Command: "timeout races stop", BlockTimeoutMS: 1})
	}()
	clock.BlockUntil(1)
	stopResult, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	c.mu.Lock()
	workMembership := len(c.stop.work)
	c.mu.Unlock()
	if workMembership != 1 {
		t.Fatalf("stop captured %d foreground shell receipts, want one uncommitted receipt", workMembership)
	}
	executeDelegateCancelPlan(cancelPlan)
	clock.Advance(minShellBlockTimeoutMS * 1_000_000)
	got := <-result
	if got.settle != nil {
		got.JobID = got.settle(true)
	}
	if got.JobID != "" {
		waitForShellDone(t, jm, got.JobID)
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration after shell race: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile after timeout race: %v", err)
	}
	select {
	case <-stopResult.done:
	default:
		t.Fatal("timeout race leaked stop membership")
	}
	c.mu.Lock()
	workCount := len(c.work)
	c.mu.Unlock()
	if workCount != 0 {
		t.Fatalf("timeout race retained %d controller shell receipts", workCount)
	}
}

func newStableShellReceiptHarness(t *testing.T) (*delegateTreeController, *jobManager, delegateLease, *agenttest.FakeClock) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	jm := newTestJM(t)
	clock := agenttest.NewFakeClock()
	jm.clock = clock
	jm.mu.Lock()
	jm.parentJobID = ""
	jm.parentDelegateID = lease.delegateID
	jm.delegateController = c
	jm.delegateLease = lease
	jm.mu.Unlock()
	return c, jm, lease, clock
}
