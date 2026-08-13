package agent

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

func TestDelegateControllerStopPersistsBeforeCancellationPlan(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	cancelled := false
	c.live["dlg_target"].binding.cancel = func() { cancelled = true }
	result, plan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Kind != delegatestore.EventDelegateSubtreeStopRequested || events[len(events)-1].Seq != result.requestSeq {
		t.Fatalf("last event = %#v, want durable stop request %d", events, result.requestSeq)
	}
	if cancelled || len(plan.cancel) != 1 {
		t.Fatalf("cancellation before plan execution = %t, plan=%#v", cancelled, plan)
	}
	plan.cancel[0]()
	if !cancelled {
		t.Fatal("captured cancellation plan did not cancel exact runtime")
	}
}

func TestDelegateControllerStopCancellationPlanIsLeafFirst(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerRunning(t, c, "dlg_grandchild", "dlg_child")
	order := make([]string, 0, 3)
	for _, id := range []string{"dlg_parent", "dlg_child", "dlg_grandchild"} {
		delegateID := id
		c.live[id].binding.cancel = func() { order = append(order, delegateID) }
	}
	_, plan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(plan)
	want := []string{"dlg_grandchild", "dlg_child", "dlg_parent"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("cancellation order = %#v, want leaf-first %#v", order, want)
	}
}

func TestDelegateControllerSameTargetStopRetryJoins(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	first, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("first StopSubtree: %v", err)
	}
	second, plan, plans, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("second StopSubtree: %v", err)
	}
	if second.requestSeq != first.requestSeq || second.done != first.done || len(plan.cancel) != 1 || len(plans.updates) != 0 {
		t.Fatalf("joined stop = %#v plan=%#v plans=%#v, want same durable operation", second, plan, plans)
	}
}

func TestDelegateControllerDifferentTargetStopIsBusy(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_first", "")
	seedDelegateControllerIdle(t, c, "dlg_second", "")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_first"); err != nil {
		t.Fatalf("first StopSubtree: %v", err)
	}
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_second"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("different stop error = %v, want busy", err)
	}
}

func TestDelegateControllerCoveringAndIntersectingStopAreBusy(t *testing.T) {
	for _, targetFirst := range []string{"dlg_parent", "dlg_child"} {
		t.Run(targetFirst, func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 2, 1)
			if targetFirst == "dlg_child" {
				seedDelegateControllerRunning(t, c, "dlg_parent", "")
			} else {
				seedDelegateControllerIdle(t, c, "dlg_parent", "")
			}
			seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
			actor := rootDelegateActor("root-session")
			if targetFirst == "dlg_child" {
				lease := delegateLease{delegateID: "dlg_parent", generation: 1}
				actor = delegateActor{lease: &lease}
			}
			if _, _, _, err := c.StopSubtree(actor, targetFirst); err != nil {
				t.Fatalf("first StopSubtree: %v", err)
			}
			other := "dlg_parent"
			if targetFirst == other {
				other = "dlg_child"
			}
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), other); !errors.Is(err, errDelegateTargetBusy) {
				t.Fatalf("overlapping stop error = %v, want busy", err)
			}
		})
	}
}

func TestDelegateControllerSuccessorWaitsForStopCompletion(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart before completion = %v, want busy", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("ReserveStart after completion: %v", err)
	}
}

func TestDelegateControllerRestartThenStopUsesNewRequestSequence(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	first, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("first StopSubtree: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	restarted := restartDelegateDeliveryController(t, c, path)
	second, _, _, err := restarted.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("second StopSubtree: %v", err)
	}
	if second.requestSeq <= first.requestSeq {
		t.Fatalf("restart request seq = %d, want > %d", second.requestSeq, first.requestSeq)
	}
}

func TestDelegateControllerStopRescansCancellationAttentionBeforeCompletion(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	evidence := emptyDelegateReconcileEvidence(c)
	evidence.attention["dlg_target"] = []string{"attention-1"}
	plans, err := c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile attention: %v", err)
	}
	if len(plans.attention) != 1 || plans.attention[0].attentionID != "attention-1" {
		t.Fatalf("attention plans = %#v", plans.attention)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed before attention resolution")
	default:
	}
	if _, err := c.ReportAttentionResolved(result.requestSeq, "dlg_target", "attention-1", delegateAttentionDiscarded, nil); err != nil {
		t.Fatalf("ReportAttentionResolved: %v", err)
	}
	if _, err := c.Reconcile(evidence); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("stale Reconcile error = %v, want busy", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("fresh Reconcile: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop remained pending after fresh empty attention fold")
	}
}

func TestDelegateControllerStopPreservesOwnerDeliveryOutsideSubtree(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	parentLease := delegateLease{delegateID: "dlg_parent", generation: 1}
	if _, _, _, err := c.StopSubtree(delegateActor{lease: &parentLease}, "dlg_child"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.durable["dlg_child"].PendingDeliveries) != 1 || len(plans.deliveries) != 1 {
		t.Fatalf("outside-owner delivery = pending:%#v plans:%#v", c.durable["dlg_child"].PendingDeliveries, plans.deliveries)
	}
}

func TestDelegateControllerStopSuppressesCoveredOwnerDelivery(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(c.durable["dlg_child"].PendingDeliveries) != 0 || len(plans.deliveries) != 0 {
		t.Fatalf("covered-owner delivery survived = pending:%#v plans:%#v", c.durable["dlg_child"].PendingDeliveries, plans.deliveries)
	}
}

func TestDelegateControllerStopDefersExternalOwnerDeliveryUntilCompletion(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if plans := c.ReplayDeliveries(); len(plans) != 0 {
		t.Fatalf("delivery released during stop: %#v", plans)
	}
	plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].deliveryID != "dlg_target/delivery/1" {
		t.Fatalf("post-completion plans = %#v", plans.deliveries)
	}
}

func TestDelegateControllerIdleStopWithPendingDeliveryPersistsAndSuppresses(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if result.requestSeq == 0 || c.durable["dlg_target"].PendingStopSeq != result.requestSeq || len(c.ReplayDeliveries()) != 0 {
		t.Fatalf("idle stop result=%#v aggregate=%#v", result, c.durable["dlg_target"])
	}
}

func TestDelegateControllerStopRequestAppendFailureDispatchesNothing(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	cancelled := false
	c.live["dlg_target"].binding.cancel = func() { cancelled = true }
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, plan, plans, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err == nil {
		t.Fatal("StopSubtree succeeded after store close")
	}
	if cancelled || len(plan.cancel) != 0 || len(plans.updates) != 0 || c.stop != nil {
		t.Fatalf("failed request dispatched state cancelled=%t plan=%#v plans=%#v stop=%#v", cancelled, plan, plans, c.stop)
	}
}

func TestDelegateControllerStopCompletionAppendFailureKeepsFence(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err == nil {
		t.Fatal("Reconcile completion succeeded after store close")
	}
	select {
	case <-result.done:
		t.Fatal("failed completion closed stop")
	default:
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart after failed completion = %v, want busy", err)
	}
}

func TestDelegateControllerResumabilityAppendFailureMutatesNothing(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	before := cloneDelegateControllerState(t, c.durable)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	plans, err := c.CloseResumability(rootDelegateActor("root-session"), "dlg_target", "retired")
	if err == nil {
		t.Fatal("CloseResumability succeeded after store close")
	}
	if len(plans.updates) != 0 || !reflect.DeepEqual(c.durable, before) {
		t.Fatalf("failed resumability closure mutated state plans=%#v durable=%#v", plans, c.durable)
	}
}

func TestDelegateControllerRootCloseJoinsPendingStopWithoutSecondIdentity(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("Close did not join pending stop")
	}
	events, err := c.store.Load()
	if err == nil {
		t.Fatal("Load after Close succeeded, want closed store")
	}
	_ = events
	raw := readDelegateControllerFile(t, filepath.Join(c.stateDir, "delegate-events.jsonl"))
	if got := bytes.Count(raw, []byte(`"delegate_subtree_stop_requested"`)); got != 1 {
		t.Fatalf("stop request count = %d, want 1\n%s", got, raw)
	}
}

func TestDelegateControllerRootCloseFencesAdmissionWhileReceiptDrains(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plan := c.ReplayDeliveries()[0]
	token, admitted, err := c.BeginDelivery(plan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with outstanding receipt = %v, want canceled", err)
	}
	if _, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_target"); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("ReserveStart after close fence = %v, want busy", err)
	}
	if _, err := c.CompleteDelivery(token, false); err != nil {
		t.Fatalf("CompleteDelivery cleanup: %v", err)
	}
}

func seedDelegateControllerDelivery(t *testing.T, c *delegateTreeController, delegateID string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle {
		t.Fatalf("delegate %s is not idle", delegateID)
	}
	lease := delegateLease{delegateID: delegateID, generation: aggregate.Generation + 1}
	packet := delegateControllerReportedPacket("seed")
	_, err := c.appendLocked(
		delegateControllerRunStartedEvent(delegateID, lease.generation, delegatestore.TriggerOwnerInput, c.now()),
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateTerminalPrepared,
			DelegateID: delegateID,
			TerminalPrepared: &delegatestore.TerminalPrepared{
				Generation: lease.generation,
				Packet:     packet,
			},
		},
		delegateRunFinishedEvent(lease, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", c.now(), delegateDeliveryID(delegateID, lease.generation), nil),
	)
	if err != nil {
		t.Fatalf("seed delivery: %v", err)
	}
}
