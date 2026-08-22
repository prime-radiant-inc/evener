package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestDelegateControllerTwoGenerationsCanFinishBeforeFirstAck(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	first, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	firstPlans := finishDelegateDeliveryGeneration(t, c, first, "first")
	second, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	secondPlans := finishDelegateDeliveryGeneration(t, c, second, "second")

	if len(firstPlans.deliveries) != 1 || len(secondPlans.deliveries) != 0 {
		t.Fatalf("finish delivery plans = first:%#v second:%#v", firstPlans.deliveries, secondPlans.deliveries)
	}
	got := c.durable["dlg_target"].PendingDeliveries
	if len(got) != 2 || got[0].DeliveryID != "dlg_target/delivery/1" || got[1].DeliveryID != "dlg_target/delivery/2" {
		t.Fatalf("pending deliveries = %#v", got)
	}
}

func TestDelegateControllerSecondDeliveryWaitsForFirstLiveAck(t *testing.T) {
	c, firstPlan, _, secondPlan, _ := controllerWithTwoDelegateDeliveries(t, false, false)
	if secondPlan != nil {
		t.Fatalf("second finish dispatched behind first: %#v", secondPlan)
	}
	token, admitted, err := c.BeginDelivery(firstPlan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery first = token:%#v admitted:%t err:%v", token, admitted, err)
	}
	plans, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("CompleteDelivery first: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].deliveryID != "dlg_target/delivery/2" {
		t.Fatalf("next delivery plans = %#v", plans.deliveries)
	}
}

func TestDelegateControllerBlockedFirstDeliveryPreservesSecondInlineWaiter(t *testing.T) {
	c, firstPlan, _, secondPlan, secondWaiter := controllerWithTwoDelegateDeliveries(t, true, true)
	if firstPlan.waiter == nil || secondPlan != nil {
		t.Fatalf("ordered plans = first:%#v second:%#v", firstPlan, secondPlan)
	}
	c.mu.Lock()
	got := c.live["dlg_target"].waiters[2]
	c.mu.Unlock()
	if got != secondWaiter {
		t.Fatalf("second waiter = %#v, want preserved %#v", got, secondWaiter)
	}
}

func TestDelegateControllerInlineTimeoutWithdrawsBeforeHeadClaim(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolution := c.waitForDelegateInline(ctx, waiter)
	if !resolution.fallback {
		t.Fatalf("timeout resolution = %#v, want fallback", resolution)
	}
	plans := finishDelegateDeliveryGeneration(t, c, lease, "first")
	if len(plans.deliveries) != 1 || plans.deliveries[0].waiter != nil {
		t.Fatalf("post-timeout delivery plan = %#v", plans.deliveries)
	}
}

func TestDelegateControllerHeadClaimWinsInlineTimeout(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plans := finishDelegateDeliveryGeneration(t, c, lease, "first")
	plan := plans.deliveries[0]
	assertDelegateClaimWinsTimeout(t, c, waiter, plan)
}

func TestDelegateControllerNextHeadClaimWinsInlineTimeout(t *testing.T) {
	c, firstPlan, _, _, secondWaiter := controllerWithTwoDelegateDeliveries(t, false, true)
	token, admitted, err := c.BeginDelivery(firstPlan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery first = admitted:%t err:%v", admitted, err)
	}
	plans, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("CompleteDelivery first: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].waiter != secondWaiter {
		t.Fatalf("claimed next plan = %#v, want waiter %#v", plans.deliveries, secondWaiter)
	}
	assertDelegateClaimWinsTimeout(t, c, secondWaiter, plans.deliveries[0])
}

func TestDelegateControllerInlineClaimFailureFallsBackAndResolvesWaiter(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "first").deliveries[0]
	appendDelegateControllerStopRequest(t, c, "dlg_target")

	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	resolution := <-waiter.resolution
	if !resolution.fallback || resolution.packet != nil || resolution.commit != nil {
		t.Fatalf("claim-failure resolution = %#v", resolution)
	}
	if len(c.durable["dlg_target"].PendingDeliveries) != 1 {
		t.Fatalf("claim failure removed durable head: %#v", c.durable["dlg_target"].PendingDeliveries)
	}
}

func TestDelegateControllerInlineHandoffDoesNotAcknowledgeBeforeReceiverCommit(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "first").deliveries[0]
	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	resolution := <-waiter.resolution
	if resolution.packet == nil || resolution.commit == nil || resolution.fallback {
		t.Fatalf("inline resolution = %#v", resolution)
	}
	if len(c.durable["dlg_target"].PendingDeliveries) != 1 {
		t.Fatalf("inline handoff acknowledged before receiver commit: %#v", c.durable["dlg_target"].PendingDeliveries)
	}
}

func TestDelegateControllerInlineCommitFailureLeavesNAndNPlusOneQueued(t *testing.T) {
	c, firstPlan, firstWaiter, _, _ := controllerWithTwoDelegateDeliveries(t, true, true)
	if _, err := deliverDelegatePacket(firstPlan, nil); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	resolution := <-firstWaiter.resolution
	plans, err := resolution.commit.Complete(false)
	if err != nil {
		t.Fatalf("Complete(false): %v", err)
	}
	if len(plans.deliveries) != 0 || len(c.durable["dlg_target"].PendingDeliveries) != 2 {
		t.Fatalf("failed inline commit changed ordering: plans=%#v pending=%#v", plans, c.durable["dlg_target"].PendingDeliveries)
	}
}

func TestDelegateControllerInlineCommitReleasesNPlusOneOnlyAfterN(t *testing.T) {
	c, firstPlan, firstWaiter, _, secondWaiter := controllerWithTwoDelegateDeliveries(t, true, true)
	if _, err := deliverDelegatePacket(firstPlan, nil); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	resolution := <-firstWaiter.resolution
	plans, err := resolution.commit.Complete(true)
	if err != nil {
		t.Fatalf("Complete(true): %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].deliveryID != "dlg_target/delivery/2" || plans.deliveries[0].waiter != secondWaiter {
		t.Fatalf("released plans = %#v", plans.deliveries)
	}
	pending := c.durable["dlg_target"].PendingDeliveries
	if len(pending) != 1 || pending[0].DeliveryID != "dlg_target/delivery/2" {
		t.Fatalf("pending after N commit = %#v", pending)
	}

	nested, nestedPlan, nestedWaiter := nestedInlineDelegateDelivery(t)
	if _, err := deliverDelegatePacket(nestedPlan, nil); err != nil {
		t.Fatalf("deliver nested inline packet: %v", err)
	}
	nestedResolution := <-nestedWaiter.resolution
	if nestedResolution.commit == nil {
		t.Fatal("nested inline delivery returned no commit")
	}
	if _, err := nestedResolution.commit.Complete(true); err != nil {
		t.Fatalf("commit nested inline delivery: %v", err)
	}
	if nested.durable["dlg_owner"].NeedsAttention || len(nested.attentionWakeIDs["dlg_owner"]) != 0 {
		t.Fatalf("inline DelegateDeliveryCommit opened owner attention: owner=%#v unresolved=%#v", nested.durable["dlg_owner"], nested.attentionWakeIDs["dlg_owner"])
	}
	events, err := nested.store.Load()
	if err != nil {
		t.Fatalf("load nested inline journal: %v", err)
	}
	if tail := events[len(events)-1]; tail.Kind != delegatestore.EventDelegateDeliveryAcknowledged || tail.DeliveryAcknowledged == nil || tail.DeliveryAcknowledged.DeliveryID != nestedPlan.deliveryID {
		t.Fatalf("nested inline completion tail = %#v, want sender acknowledgment only", tail)
	}
}

func TestDelegateControllerInlineReplayAfterReceiverCommitIsIdempotent(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "first").deliveries[0]
	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatalf("deliverDelegatePacket inline: %v", err)
	}
	resolution := <-waiter.resolution
	if _, err := resolution.commit.Complete(false); err != nil {
		t.Fatalf("Complete(false): %v", err)
	}
	receiver := newFakeDelegateDeliveryReceiver()
	receiver.present[delegateAttentionID(plan.deliveryID)] = true
	replay := c.ReplayDeliveries()
	if len(replay) != 1 || replay[0].waiter != nil {
		t.Fatalf("replay plans = %#v", replay)
	}
	if _, err := deliverDelegatePacket(replay[0], receiver); err != nil {
		t.Fatalf("deliverDelegatePacket replay: %v", err)
	}
	if receiver.writes != 0 || len(c.durable["dlg_target"].PendingDeliveries) != 0 {
		t.Fatalf("idempotent replay writes=%d pending=%#v", receiver.writes, c.durable["dlg_target"].PendingDeliveries)
	}

	nested, nestedPlan, nestedWaiter := nestedInlineDelegateDelivery(t)
	if _, err := deliverDelegatePacket(nestedPlan, nil); err != nil {
		t.Fatalf("deliver nested inline packet: %v", err)
	}
	nestedResolution := <-nestedWaiter.resolution
	if _, err := nestedResolution.commit.Complete(false); err != nil {
		t.Fatalf("leave nested inline acknowledgement pending: %v", err)
	}
	ownerSessionID := nested.durable["dlg_owner"].Descriptor.ChildSessionID
	writer, err := transcript.NewWriter(transcriptPath(nested.stateDir, ownerSessionID), transcript.Header{SessionID: ownerSessionID})
	if err != nil {
		t.Fatalf("create nested caller transcript: %v", err)
	}
	commit := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-call", "delegate", "nested inline", false))
	commit.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "delegate-call", DeliveryID: nestedPlan.deliveryID}}
	if err := writer.AppendDurable(commit); err != nil {
		_ = writer.Close()
		t.Fatalf("append nested caller commit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close nested caller transcript: %v", err)
	}
	pending, err := prepareColdDelegateDeliveryReplay(nested, nested.ReplayDeliveries())
	if err != nil {
		t.Fatalf("replay caller-committed nested delivery: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("caller-committed nested replay remained pending: %#v", pending)
	}
	if nested.durable["dlg_owner"].NeedsAttention || len(nested.attentionWakeIDs["dlg_owner"]) != 0 {
		t.Fatalf("caller-committed nested replay opened owner attention: owner=%#v unresolved=%#v", nested.durable["dlg_owner"], nested.attentionWakeIDs["dlg_owner"])
	}
	events, err := nested.store.Load()
	if err != nil {
		t.Fatalf("load caller-committed nested replay journal: %v", err)
	}
	if tail := events[len(events)-1]; tail.Kind != delegatestore.EventDelegateDeliveryAcknowledged || tail.DeliveryAcknowledged == nil || tail.DeliveryAcknowledged.DeliveryID != nestedPlan.deliveryID {
		t.Fatalf("caller-committed nested replay tail = %#v, want sender acknowledgment only", tail)
	}
}

func TestDelegateControllerBeginDeliveryCreatesOneExactReceipt(t *testing.T) {
	c, firstPlan, _, _, _ := controllerWithTwoDelegateDeliveries(t, false, false)
	forged := firstPlan
	forged.packet.Message = []byte(`"forged"`)
	if token, admitted, err := c.BeginDelivery(forged); err != nil || admitted || token != (delegateDeliveryToken{}) {
		t.Fatalf("forged BeginDelivery = %#v %t %v", token, admitted, err)
	}
	first, admitted, err := c.BeginDelivery(firstPlan)
	if err != nil || !admitted {
		t.Fatalf("first BeginDelivery = %#v %t %v", first, admitted, err)
	}
	second, admitted, err := c.BeginDelivery(firstPlan)
	if err != nil || admitted || second != (delegateDeliveryToken{}) {
		t.Fatalf("duplicate BeginDelivery = %#v %t %v, want one executor", second, admitted, err)
	}
	c.mu.Lock()
	receipts := len(c.deliveries)
	c.mu.Unlock()
	if receipts != 1 {
		t.Fatalf("delivery receipts = %d, want 1", receipts)
	}
}

func TestDelegateControllerStopReleasesClaimedInlineWaiterAcrossCompletionOrders(t *testing.T) {
	for _, completeBeforeFallback := range []bool{false, true} {
		name := "fallback-before-completion"
		if completeBeforeFallback {
			name = "completion-before-fallback"
		}
		t.Run(name, func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerIdle(t, c, "dlg_target", "")
			lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
			original := finishDelegateDeliveryGeneration(t, c, lease, "terminal").deliveries[0]
			_, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
			if err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}

			var completion delegateMutationPlans
			complete := func() {
				completion, err = c.Reconcile(emptyDelegateReconcileEvidence(c))
				if err != nil {
					t.Fatalf("Reconcile: %v", err)
				}
			}
			runFallback := make(chan struct{})
			fallbackDone := make(chan delegateInlineResolution, 1)
			go func() {
				<-runFallback
				executeDelegateCancelPlan(cancelPlan)
				fallbackDone <- <-waiter.resolution
			}()
			fallback := func() {
				close(runFallback)
				resolution := <-fallbackDone
				if !resolution.fallback || resolution.packet != nil || resolution.commit != nil {
					t.Fatalf("released waiter resolution = %#v", resolution)
				}
			}
			if completeBeforeFallback {
				complete()
				fallback()
			} else {
				fallback()
				complete()
			}

			if len(completion.deliveries) != 1 || completion.deliveries[0].waiter != nil {
				t.Fatalf("post-stop delivery plans = %#v, want one background retry", completion.deliveries)
			}
			if token, admitted, err := c.BeginDelivery(original); err != nil || admitted || token != (delegateDeliveryToken{}) {
				t.Fatalf("stale original BeginDelivery = %#v %t %v", token, admitted, err)
			}
			if _, admitted, err := c.BeginDelivery(completion.deliveries[0]); err != nil || !admitted {
				t.Fatalf("retry BeginDelivery = admitted:%t err:%v", admitted, err)
			}
		})
	}
}

func TestDelegateControllerBackgroundReplayAfterReceiverCommitRetriesAck(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "first").deliveries[0]
	receiver := newFakeDelegateDeliveryReceiver()
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := deliverDelegatePacket(plan, receiver); err == nil {
		t.Fatal("delivery acknowledgement succeeded after store close")
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	replay := c.ReplayDeliveries()
	if len(replay) != 1 {
		t.Fatalf("replay plans = %#v", replay)
	}
	if _, err := deliverDelegatePacket(replay[0], receiver); err != nil {
		t.Fatalf("deliver replay: %v", err)
	}
	if receiver.writes != 1 || len(c.durable["dlg_target"].PendingDeliveries) != 0 {
		t.Fatalf("receiver writes=%d pending=%#v", receiver.writes, c.durable["dlg_target"].PendingDeliveries)
	}
}

func TestDelegateControllerBackgroundAppendFailureLeavesHeadPending(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "first").deliveries[0]
	receiver := newFakeDelegateDeliveryReceiver()
	receiver.fail = errInjectedTranscriptWrite

	plans, err := deliverDelegatePacket(plan, receiver)
	if !errors.Is(err, errInjectedTranscriptWrite) {
		t.Fatalf("deliverDelegatePacket error = %v, want receiver append failure", err)
	}
	if len(plans.deliveries) != 0 || len(c.durable["dlg_target"].PendingDeliveries) != 1 || receiver.writes != 0 {
		t.Fatalf("receiver append failure plans=%#v pending=%#v writes=%d", plans, c.durable["dlg_target"].PendingDeliveries, receiver.writes)
	}
}

func TestDelegateControllerFailedDeliveryCompletionLeavesHeadPending(t *testing.T) {
	c, firstPlan, _, _, _ := controllerWithTwoDelegateDeliveries(t, false, false)
	token, admitted, err := c.BeginDelivery(firstPlan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	plans, err := c.CompleteDelivery(token, false)
	if err != nil {
		t.Fatalf("CompleteDelivery(false): %v", err)
	}
	if len(plans.deliveries) != 0 || c.durable["dlg_target"].PendingDeliveries[0].DeliveryID != firstPlan.deliveryID {
		t.Fatalf("failed completion plans=%#v pending=%#v", plans, c.durable["dlg_target"].PendingDeliveries)
	}
}

func TestDelegateControllerCommittedDeliveryCompletionAcknowledgesExactHead(t *testing.T) {
	c, path, firstPlan := controllerWithNestedDelegateDeliveries(t)
	before := readDelegateControllerFile(t, path)
	beforeRevision := c.durable["dlg_owner"].ProjectionRevision
	token, admitted, err := c.BeginDelivery(firstPlan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	plans, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("CompleteDelivery(true): %v", err)
	}
	pending := c.durable["dlg_target"].PendingDeliveries
	if len(pending) != 1 || pending[0].DeliveryID != "dlg_target/delivery/2" {
		t.Fatalf("pending after exact ack = %#v", pending)
	}
	owner := c.durable["dlg_owner"]
	attentionID := delegateAttentionID(firstPlan.deliveryID)
	if !owner.NeedsAttention || owner.ProjectionRevision != beforeRevision+1 {
		t.Fatalf("owner attention projection = needs:%t revision:%d, want true/%d", owner.NeedsAttention, owner.ProjectionRevision, beforeRevision+1)
	}
	if _, present := c.attentionWakeIDs["dlg_owner"][attentionID]; !present || len(c.attentionWakeIDs["dlg_owner"]) != 1 {
		t.Fatalf("published unresolved attention = %#v, want only %q", c.attentionWakeIDs["dlg_owner"], attentionID)
	}
	if got := readDelegateControllerFile(t, path); bytes.Count(got, []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatalf("attention open and delivery ack did not share one journal batch:\n%s", got)
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tail := events[len(events)-2:]
	if tail[0].Kind != delegatestore.EventDelegateAttentionChanged || tail[0].DelegateID != "dlg_owner" || tail[0].AttentionChanged == nil || !tail[0].AttentionChanged.NeedsAttention || tail[1].Kind != delegatestore.EventDelegateDeliveryAcknowledged || tail[1].DelegateID != "dlg_target" || tail[1].DeliveryAcknowledged == nil || tail[1].DeliveryAcknowledged.DeliveryID != firstPlan.deliveryID {
		t.Fatalf("serialized delivery batch tail = %#v", tail)
	}
	if len(plans.updates) != 2 {
		t.Fatalf("delivery updates = %#v, want owner open and sender ack", plans.updates)
	}
}

func TestDelegateControllerDeliveryAckRemovesOnlyExactID(t *testing.T) {
	c, _, firstPlan := controllerWithNestedDelegateDeliveries(t)
	beforeSecond := cloneDelegateTerminalPacket(c.durable["dlg_target"].PendingDeliveries[1].Packet)
	token, _, _ := c.BeginDelivery(firstPlan)
	plans, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}
	pending := c.durable["dlg_target"].PendingDeliveries
	if len(pending) != 1 || pending[0].DeliveryID != "dlg_target/delivery/2" || !reflect.DeepEqual(pending[0].Packet, beforeSecond) {
		t.Fatalf("ack removed or changed non-head delivery: %#v", pending)
	}
	if len(plans.deliveries) != 1 {
		t.Fatalf("next delivery plans = %#v", plans.deliveries)
	}
	beforeRevision := c.durable["dlg_owner"].ProjectionRevision
	secondPlan := plans.deliveries[0]
	secondToken, admitted, err := c.BeginDelivery(secondPlan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery second = admitted:%t err:%v", admitted, err)
	}
	if _, err := c.CompleteDelivery(secondToken, true); err != nil {
		t.Fatalf("CompleteDelivery second: %v", err)
	}
	owner := c.durable["dlg_owner"]
	if !owner.NeedsAttention || owner.ProjectionRevision != beforeRevision {
		t.Fatalf("additional unresolved ID changed attention projection = needs:%t revision:%d, want true/%d", owner.NeedsAttention, owner.ProjectionRevision, beforeRevision)
	}
	wantIDs := map[string]struct{}{
		delegateAttentionID(firstPlan.deliveryID):  {},
		delegateAttentionID(secondPlan.deliveryID): {},
	}
	if !reflect.DeepEqual(c.attentionWakeIDs["dlg_owner"], wantIDs) {
		t.Fatalf("exact unresolved attention = %#v, want %#v", c.attentionWakeIDs["dlg_owner"], wantIDs)
	}
}

func TestDelegateControllerRestartReplaysTwoDeliveriesInOrder(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	first, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	finishDelegateDeliveryGeneration(t, c, first, "first")
	second, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	finishDelegateDeliveryGeneration(t, c, second, "second")
	restarted := restartDelegateDeliveryController(t, c, path)

	plans := restarted.ReplayDeliveries()
	if len(plans) != 1 || plans[0].deliveryID != "dlg_target/delivery/1" {
		t.Fatalf("initial replay plans = %#v", plans)
	}
	receiver := newFakeDelegateDeliveryReceiver()
	next, err := deliverDelegatePacket(plans[0], receiver)
	if err != nil {
		t.Fatalf("deliver first replay: %v", err)
	}
	if len(next.deliveries) != 1 || next.deliveries[0].deliveryID != "dlg_target/delivery/2" {
		t.Fatalf("next replay plans = %#v", next.deliveries)
	}
	if _, err := deliverDelegatePacket(next.deliveries[0], receiver); err != nil {
		t.Fatalf("deliver second replay: %v", err)
	}
	if len(restarted.durable["dlg_target"].PendingDeliveries) != 0 || receiver.writes != 2 {
		t.Fatalf("restart delivery result pending=%#v writes=%d", restarted.durable["dlg_target"].PendingDeliveries, receiver.writes)
	}
}

func TestDelegateControllerRestartThenFinishUsesNewDeliveryID(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	first, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	finishDelegateDeliveryGeneration(t, c, first, "first")
	restarted := restartDelegateDeliveryController(t, c, path)
	second, _ := startDelegateDeliveryGeneration(t, restarted, "dlg_target", false)
	plans := finishDelegateDeliveryGeneration(t, restarted, second, "second")
	if len(plans.deliveries) != 0 {
		t.Fatalf("second finish dispatched before first ack: %#v", plans.deliveries)
	}
	pending := restarted.durable["dlg_target"].PendingDeliveries
	if len(pending) != 2 || pending[0].DeliveryID != "dlg_target/delivery/1" || pending[1].DeliveryID != "dlg_target/delivery/2" {
		t.Fatalf("restart delivery IDs = %#v", pending)
	}
}

func TestDelegateControllerFatalFinishAppendFailurePublishesNoDelivery(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	before := readDelegateControllerFile(t, path)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	plans, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeFailed, reason: "fatal"})
	if err == nil {
		t.Fatal("FinishGeneration succeeded after store close")
	}
	if len(plans.deliveries) != 0 || len(c.durable["dlg_target"].PendingDeliveries) != 0 || c.live["dlg_target"].waiters[1] != waiter {
		t.Fatalf("failed finish published state plans=%#v aggregate=%#v live=%#v", plans, c.durable["dlg_target"], c.live["dlg_target"])
	}
	if got := readDelegateControllerFile(t, path); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed finish changed bytes")
	}
}

func TestDelegateControllerRunFinishedAppendFailureKeepsPreparedAndWaiter(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	runtime := &Session{}
	if err := c.AttachRuntime(lease, runtime); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	if _, err := c.AdmitStartInput(lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	if _, _, err := c.prepareSettlementForTest(lease, nil); err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	plans, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCompleted})
	if err == nil {
		t.Fatal("FinishGeneration succeeded after store close")
	}
	aggregate := c.durable["dlg_target"]
	if len(plans.deliveries) != 0 || aggregate.Phase != delegatestore.PhaseSettling || aggregate.PreparedTerminal == nil || c.live["dlg_target"].waiters[1] != waiter {
		t.Fatalf("failed run finish state plans=%#v aggregate=%#v live=%#v", plans, aggregate, c.live["dlg_target"])
	}
	if turns, _ := c.capacityInUse(); turns != 1 {
		t.Fatalf("capacity after failed run finish = %d, want 1", turns)
	}
}

func TestDelegateControllerDeliveryAcknowledgedAppendFailureKeepsReceiptAndHead(t *testing.T) {
	c, path, firstPlan := controllerWithNestedDelegateDeliveries(t)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "child-dlg_owner"), "child-dlg_owner")
	before := readDelegateControllerFile(t, path)
	beforeOwner := captureDelegateSnapshot(c.durable["dlg_owner"])
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	plans, err := deliverColdDelegatePacket(firstPlan)
	if err == nil {
		t.Fatal("delivery acknowledgment succeeded after store close")
	}
	c.mu.Lock()
	var receipt *delegateDeliveryAdmission
	for _, candidate := range c.deliveries {
		receipt = candidate
	}
	c.mu.Unlock()
	if len(plans.deliveries) != 0 || receipt == nil || c.durable["dlg_target"].PendingDeliveries[0].DeliveryID != firstPlan.deliveryID {
		t.Fatalf("failed ack state plans=%#v receipt=%#v pending=%#v", plans, receipt, c.durable["dlg_target"].PendingDeliveries)
	}
	if got := captureDelegateSnapshot(c.durable["dlg_owner"]); got.needsAttention != beforeOwner.needsAttention || got.revision != beforeOwner.revision || len(c.attentionWakeIDs["dlg_owner"]) != 0 {
		t.Fatalf("failed batch published owner attention: before=%#v after=%#v unresolved=%#v", beforeOwner, got, c.attentionWakeIDs["dlg_owner"])
	}
	if got := readDelegateControllerFile(t, path); !bytes.Equal(got, before) {
		t.Fatalf("failed delivery batch changed journal bytes")
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	ownerWriter, _, err := transcript.OpenWriterForSession(transcriptPath(c.stateDir, "child-dlg_owner"), "child-dlg_owner")
	if err != nil {
		t.Fatalf("open current owner receiver: %v", err)
	}
	t.Cleanup(func() { _ = ownerWriter.Close() })
	ownerRuntime := &Session{id: "child-dlg_owner", stateDir: c.stateDir, delegateController: c, owningDelegateID: "dlg_owner"}
	ownerRuntime.attachTranscript(ownerWriter)
	c.mu.Lock()
	c.live["dlg_owner"] = &delegateLiveState{runtime: ownerRuntime}
	c.mu.Unlock()
	replay := c.ReplayDeliveries()
	if len(replay) != 1 || replay[0].deliveryID != firstPlan.deliveryID || replay[0].waiter != nil || replay[0].receiver != ownerRuntime {
		t.Fatalf("public replay plans = %#v, want rebuilt exact head without a spent waiter", replay)
	}
	if _, err := deliverColdDelegatePacket(replay[0]); err != nil {
		t.Fatalf("retry through public delivery path: %v", err)
	}
	if len(c.durable["dlg_target"].PendingDeliveries) != 1 || !c.durable["dlg_owner"].NeedsAttention || len(c.attentionWakeIDs["dlg_owner"]) != 1 {
		t.Fatalf("retried delivery did not publish atomically: sender=%#v owner=%#v unresolved=%#v", c.durable["dlg_target"], c.durable["dlg_owner"], c.attentionWakeIDs["dlg_owner"])
	}

	inline, inlineStorePath := newDelegateControllerTestHarness(t, 2, 1)
	inlineIDs := []string{"dlg_inline_a", "dlg_inline_b"}
	inlinePlans := make([]delegateDeliveryPlan, 0, len(inlineIDs))
	inlineCommits := make([]delegateToolCallDeliveryCommit, 0, len(inlineIDs))
	for index, delegateID := range inlineIDs {
		seedDelegateControllerIdle(t, inline, delegateID, "")
		lease, waiter := startDelegateDeliveryGeneration(t, inline, delegateID, true)
		plan := finishDelegateDeliveryGeneration(t, inline, lease, "inline").deliveries[0]
		if _, err := deliverDelegatePacket(plan, nil); err != nil {
			t.Fatalf("handoff inline delivery %s: %v", delegateID, err)
		}
		resolution := <-waiter.resolution
		if resolution.commit == nil {
			t.Fatalf("inline handoff %s returned no delivery commit", delegateID)
		}
		inlinePlans = append(inlinePlans, plan)
		inlineCommits = append(inlineCommits, delegateToolCallDeliveryCommit{toolCallID: fmt.Sprintf("inline-call-%d", index+1), commit: resolution.commit})
	}
	rootWriter, err := transcript.NewWriter(transcriptPath(inline.stateDir, "root-session"), transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create inline owner transcript: %v", err)
	}
	t.Cleanup(func() { _ = rootWriter.Close() })
	root := &Session{id: "root-session", stateDir: inline.stateDir, state: SessionIdle, delegateController: inline}
	root.attachTranscript(rootWriter)
	inline.rootRuntime = root
	if err := inline.store.Close(); err != nil {
		t.Fatalf("close inline store: %v", err)
	}
	result := llm.ToolResultNamed("inline-call-1", "delegate_send", "done one", false)
	secondResult := llm.ToolResultNamed("inline-call-2", "delegate_send", "done two", false)
	result.Content = append(result.Content, secondResult.Content...)
	err = root.appendToolResultsWithDeliveryCommitsDurably(result, result, inlineCommits)
	if err == nil {
		t.Fatal("inline tool-result acknowledgment succeeded after store close")
	}
	root.delegateDeliveryMu.Lock()
	scheduled := append([]delegateDeliveryPlan(nil), root.pendingDelegateDeliveries...)
	root.delegateDeliveryMu.Unlock()
	if len(scheduled) != 2 || scheduled[0].deliveryID != inlinePlans[0].deliveryID || scheduled[1].deliveryID != inlinePlans[1].deliveryID || scheduled[0].waiter != nil || scheduled[1].waiter != nil {
		t.Fatalf("scheduled inline replay = %#v, want both public rebuilt plans", scheduled)
	}
	inlineReopened, err := delegatestore.Open(inlineStorePath)
	if err != nil {
		t.Fatalf("reopen inline store: %v", err)
	}
	t.Cleanup(func() { _ = inlineReopened.Close() })
	inline.mu.Lock()
	inline.store = inlineReopened
	inline.mu.Unlock()
	if err := root.flushPendingDelegateDeliveries(); err != nil {
		t.Fatalf("drain scheduled inline replay: %v", err)
	}
	for _, delegateID := range inlineIDs {
		if len(inline.durable[delegateID].PendingDeliveries) != 0 {
			t.Fatalf("scheduled inline replay left %s head: %#v", delegateID, inline.durable[delegateID].PendingDeliveries)
		}
	}
	rootFold, err := readDelegateAttentionFold(transcriptPath(inline.stateDir, "root-session"), "root-session")
	if err != nil {
		t.Fatalf("read inline owner transcript: %v", err)
	}
	if rootFold.deliveryCommits[inlinePlans[0].deliveryID] != "inline-call-1" || rootFold.deliveryCommits[inlinePlans[1].deliveryID] != "inline-call-2" || len(rootFold.content) != 0 {
		t.Fatalf("inline retry fold = commits:%#v unresolved:%#v", rootFold.deliveryCommits, rootFold.content)
	}
}

func assertDelegateClaimWinsTimeout(t *testing.T, c *delegateTreeController, waiter *delegateInlineWaiter, plan delegateDeliveryPlan) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.mu.Lock()
	started := make(chan struct{})
	result := make(chan delegateInlineResolution, 1)
	go func() {
		close(started)
		result <- c.waitForDelegateInline(ctx, waiter)
	}()
	<-started
	select {
	case got := <-result:
		c.mu.Unlock()
		t.Fatalf("claimed waiter timed out before delivery: %#v", got)
	default:
	}
	c.mu.Unlock()
	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	got := <-result
	if got.fallback || got.packet == nil || got.commit == nil {
		t.Fatalf("claimed waiter resolution = %#v", got)
	}
}

func startDelegateDeliveryGeneration(t *testing.T, c *delegateTreeController, delegateID string, withWaiter bool) (delegateLease, *delegateInlineWaiter) {
	t.Helper()
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), delegateID)
	if err != nil {
		t.Fatalf("ReserveStart: %v", err)
	}
	var waiter *delegateInlineWaiter
	if withWaiter {
		waiter, err = c.RegisterInlineWaiter(reservation)
		if err != nil {
			t.Fatalf("RegisterInlineWaiter: %v", err)
		}
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	return started.lease, waiter
}

func finishDelegateDeliveryGeneration(t *testing.T, c *delegateTreeController, lease delegateLease, message string) delegateMutationPlans {
	t.Helper()
	packet := delegateControllerReportedPacket(message)
	plans, err := c.FinishGeneration(lease, delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionReported,
		packet:      &packet,
	})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	return plans
}

func controllerWithTwoDelegateDeliveries(t *testing.T, firstWaiter, secondWaiter bool) (*delegateTreeController, delegateDeliveryPlan, *delegateInlineWaiter, *delegateDeliveryPlan, *delegateInlineWaiter) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	firstLease, first := startDelegateDeliveryGeneration(t, c, "dlg_target", firstWaiter)
	firstPlans := finishDelegateDeliveryGeneration(t, c, firstLease, "first")
	secondLease, second := startDelegateDeliveryGeneration(t, c, "dlg_target", secondWaiter)
	secondPlans := finishDelegateDeliveryGeneration(t, c, secondLease, "second")
	var secondPlan *delegateDeliveryPlan
	if len(secondPlans.deliveries) != 0 {
		secondPlan = &secondPlans.deliveries[0]
	}
	return c, firstPlans.deliveries[0], first, secondPlan, second
}

func controllerWithNestedDelegateDeliveries(t *testing.T) (*delegateTreeController, string, delegateDeliveryPlan) {
	t.Helper()
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_owner", "")
	seedDelegateControllerIdle(t, c, "dlg_target", "dlg_owner")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("first nested delivery plans = %#v", plans)
	}
	return c, path, plans[0]
}

func nestedInlineDelegateDelivery(t *testing.T) (*delegateTreeController, delegateDeliveryPlan, *delegateInlineWaiter) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_owner", "")
	seedDelegateControllerIdle(t, c, "dlg_target", "dlg_owner")
	ownerReservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_owner")
	if err != nil {
		t.Fatalf("ReserveStart owner: %v", err)
	}
	owner, err := c.CommitStart(ownerReservation)
	if err != nil {
		t.Fatalf("CommitStart owner: %v", err)
	}
	ownerRuntime := &Session{}
	if err := c.AttachRuntime(owner.lease, ownerRuntime); err != nil {
		t.Fatalf("AttachRuntime owner: %v", err)
	}
	if _, err := c.AdmitStartInput(owner.lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput owner: %v", err)
	}
	reservation, err := c.ReserveStart(delegateActor{lease: &owner.lease}, "dlg_target")
	if err != nil {
		t.Fatalf("ReserveStart nested target: %v", err)
	}
	waiter, err := c.RegisterInlineWaiter(reservation)
	if err != nil {
		t.Fatalf("RegisterInlineWaiter nested target: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart nested target: %v", err)
	}
	plans := finishDelegateDeliveryGeneration(t, c, started.lease, "nested inline")
	if len(plans.deliveries) != 1 {
		t.Fatalf("nested inline delivery plans = %#v", plans.deliveries)
	}
	return c, plans.deliveries[0], waiter
}

func restartDelegateDeliveryController(t *testing.T, c *delegateTreeController, path string) *delegateTreeController {
	t.Helper()
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         reopened,
		rootSessionID: "root-session",
		turnLimit:     2,
		driveLimit:    1,
		now:           c.now,
	})
	if err != nil {
		t.Fatalf("openDelegateTreeController: %v", err)
	}
	return restarted
}

type fakeDelegateDeliveryReceiver struct {
	mu      sync.Mutex
	present map[string]bool
	writes  int
	fail    error
}

func newFakeDelegateDeliveryReceiver() *fakeDelegateDeliveryReceiver {
	return &fakeDelegateDeliveryReceiver{present: make(map[string]bool)}
}

func (r *fakeDelegateDeliveryReceiver) appendDelegateNotificationDurably(attentionID, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return false, r.fail
	}
	if r.present[attentionID] {
		return true, nil
	}
	r.present[attentionID] = true
	r.writes++
	return false, nil
}

func TestDelegateControllerDeliveryReceiptBeforeStopDrainsAndCleans(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plan := c.ReplayDeliveries()[0]
	token, admitted, err := c.BeginDelivery(plan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile with receipt: %v", err)
	}
	select {
	case <-result.done:
		t.Fatal("stop completed with pre-stop delivery receipt")
	default:
	}
	if _, err := c.CompleteDelivery(token, false); err != nil {
		t.Fatalf("CompleteDelivery: %v", err)
	}
	if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
		t.Fatalf("Reconcile after receipt: %v", err)
	}
}

func TestDelegateControllerStopBeforeDeliveryReceiptDefersAdmission(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plan := c.ReplayDeliveries()[0]
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if token, admitted, err := c.BeginDelivery(plan); err != nil || admitted || token != (delegateDeliveryToken{}) {
		t.Fatalf("BeginDelivery during stop = token:%#v admitted:%t err:%v", token, admitted, err)
	}
}
