package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// heldDeliveryFixture finishes a delegate's first generation, has its
// receiver hold that result for its next round, then finishes a second
// generation that has an inline waiter: the second's plan carries the held
// first (#3906).
func heldDeliveryFixture(t *testing.T) (*delegateTreeController, string, delegateDeliveryPlan, delegateDeliveryPlan, *delegateInlineWaiter) {
	t.Helper()
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	firstLease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	held := finishDelegateDeliveryGeneration(t, c, firstLease, "first").deliveries[0]
	c.holdDeliveryClaim(held)
	secondLease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plans := finishDelegateDeliveryGeneration(t, c, secondLease, "second")
	if len(plans.deliveries) != 1 || len(plans.deliveries[0].held) != 1 || plans.deliveries[0].held[0].DeliveryID != held.deliveryID {
		t.Fatalf("second generation's plans = %#v, want one carrying the held first", plans.deliveries)
	}
	return c, path, held, plans.deliveries[0], waiter
}

func waitedResolution(t *testing.T, waiter *delegateInlineWaiter) delegateInlineResolution {
	t.Helper()
	select {
	case resolution := <-waiter.resolution:
		return resolution
	default:
		t.Fatal("the inline waiter was not resolved")
		return delegateInlineResolution{}
	}
}

func pendingDeliveryIDs(c *delegateTreeController, delegateID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, pending := range c.durable[delegateID].PendingDeliveries {
		ids = append(ids, pending.DeliveryID)
	}
	return ids
}

// A carrying plan that isn't admitted gives the held result back to the plan
// its receiver holds, which then delivers it as before.
func TestHeldDeliveryReturnsToItsPlanWhenTheCarrierIsRefused(t *testing.T) {
	c, _, held, carrying, waiter := heldDeliveryFixture(t)
	forged := carrying
	forged.packet = cloneDelegateTerminalPacket(carrying.packet)
	forged.packet.Message = []byte(`"forged"`)
	if _, err := deliverDelegatePacket(forged, nil); err != nil {
		t.Fatalf("refused carrier: %v", err)
	}
	if resolution := waitedResolution(t, waiter); !resolution.fallback {
		t.Fatalf("refused carrier resolved its waiter with %#v, want the fallback", resolution)
	}
	token, admitted, err := c.BeginDelivery(held)
	if err != nil || !admitted {
		t.Fatalf("the held plan after a refused carrier: admitted=%t err=%v", admitted, err)
	}
	// Once the held result is acknowledged, the second is planned: the refused
	// carrier left no claim on it behind.
	plans, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("acknowledge the held result: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].deliveryID != carrying.deliveryID {
		t.Fatalf("plans after the held result's ack = %#v, want the second delivery planned", plans.deliveries)
	}
}

// A carrying plan refused because the delegate's results are being stopped
// drops the held result's claim instead of handing it back, as a stop does.
func TestHeldDeliveryClaimDropsWhenTheCarrierIsRefusedByAStop(t *testing.T) {
	c, _, _, carrying, _ := heldDeliveryFixture(t)
	c.mu.Lock()
	c.durable["dlg_target"].PendingStopSeq = 1
	c.mu.Unlock()
	if _, err := deliverDelegatePacket(carrying, nil); err != nil {
		t.Fatalf("refused carrier: %v", err)
	}
	c.mu.Lock()
	claims := len(c.deliveryClaims)
	c.mu.Unlock()
	if claims != 0 {
		t.Fatalf("%d claims left after a refusal by a pending stop, want none", claims)
	}
}

// A reply that carried a held result but whose tool round was abandoned gives
// the held result back to the plan its receiver holds.
func TestHeldDeliveryReturnsToItsPlanWhenTheReplyIsAbandoned(t *testing.T) {
	c, _, held, carrying, waiter := heldDeliveryFixture(t)
	if _, err := deliverDelegatePacket(carrying, nil); err != nil {
		t.Fatalf("carrier: %v", err)
	}
	resolution := waitedResolution(t, waiter)
	if len(resolution.earlier) != 1 || resolution.commit == nil {
		t.Fatalf("resolution = %#v, want the held result and the reply's own", resolution)
	}
	if _, admitted, _ := c.BeginDelivery(held); admitted {
		t.Fatal("the held plan was admitted while the reply carried its result")
	}
	for _, commit := range []*delegateToolResultCommit{resolution.earlier[0].commit, resolution.commit} {
		if _, err := commit.Complete(false); err != nil {
			t.Fatalf("abandon: %v", err)
		}
	}
	if _, admitted, err := c.BeginDelivery(held); err != nil || !admitted {
		t.Fatalf("the held plan after the reply was abandoned: admitted=%t err=%v", admitted, err)
	}
}

// When acknowledging the held result fails, the reply's own result is retried
// inline behind it, never re-delivered as a notification: both are already in
// the reply.
func TestHeldDeliveryAckFailureRetriesBothInline(t *testing.T) {
	c, path, _, carrying, waiter := heldDeliveryFixture(t)
	if _, err := deliverDelegatePacket(carrying, nil); err != nil {
		t.Fatalf("carrier: %v", err)
	}
	resolution := waitedResolution(t, waiter)
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	plans, err := completeDelegateDeliveryCommits([]*delegateToolResultCommit{resolution.earlier[0].commit, resolution.commit})
	if err == nil || len(plans) != 0 {
		t.Fatalf("acknowledging after store close: plans=%#v err=%v, want a failure and no plans", plans, err)
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	retryEveryDeliveryInline(t, c)
}

// retryEveryDeliveryInline replays c's deliveries after a failed
// acknowledgement and requires every one to be an inline retry that is
// acknowledged, leaving nothing to deliver as a notification.
func retryEveryDeliveryInline(t *testing.T, c *delegateTreeController) {
	t.Helper()
	queue := c.ReplayDeliveries()
	for len(queue) != 0 {
		plan := queue[0]
		queue = queue[1:]
		c.mu.Lock()
		receipt := c.deliveryReceiptLocked(plan.deliveryID)
		c.mu.Unlock()
		if plan.waiter != nil || receipt == nil || !receipt.inline {
			t.Fatalf("replayed plan %q is not an inline retry: %#v", plan.deliveryID, plan)
		}
		next, err := deliverDelegatePacket(plan, nil)
		if err != nil {
			t.Fatalf("retry %q: %v", plan.deliveryID, err)
		}
		queue = append(queue, next.deliveries...)
	}
	if ids := pendingDeliveryIDs(c, "dlg_target"); len(ids) != 0 {
		t.Fatalf("pending deliveries after the retries = %v, want all acknowledged", ids)
	}
	c.mu.Lock()
	claims := len(c.deliveryClaims)
	c.mu.Unlock()
	if claims != 0 {
		t.Fatalf("%d claims left after the retries, want none: nothing is re-delivered as a notification", claims)
	}
}

// A stop covering the owner takes the held result with the rest of the
// owner's deliveries. When the owner's tool round is then cancelled and its
// reply abandoned, the held result's claim must not come back: nothing could
// ever admit it, and it would block the owner's next start, its reclamation
// and retirement for good.
func TestHeldDeliveryStaysWithAStopThatCoversItsOwner(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 2)
	seedDelegateControllerRunning(t, c, "dlg_owner", "")
	seedDelegateControllerIdle(t, c, "dlg_target", "dlg_owner")
	c.mu.Lock()
	c.live["dlg_owner"].runtime = &Session{}
	c.mu.Unlock()
	ownerLease := delegateLease{delegateID: "dlg_owner", generation: 1}
	actor := delegateActor{rootSessionID: "root-session", lease: &ownerLease}
	start := func(withWaiter bool) (delegateLease, *delegateInlineWaiter) {
		t.Helper()
		reservation, err := c.ReserveStart(actor, "dlg_target")
		if err != nil {
			t.Fatalf("ReserveStart: %v", err)
		}
		var waiter *delegateInlineWaiter
		if withWaiter {
			if waiter, err = c.RegisterInlineWaiter(reservation); err != nil {
				t.Fatalf("RegisterInlineWaiter: %v", err)
			}
		}
		started, err := c.CommitStart(reservation)
		if err != nil {
			t.Fatalf("CommitStart: %v", err)
		}
		return started.lease, waiter
	}
	firstLease, _ := start(false)
	held := finishDelegateDeliveryGeneration(t, c, firstLease, "first").deliveries[0]
	c.holdDeliveryClaim(held)
	secondLease, waiter := start(true)
	plans := finishDelegateDeliveryGeneration(t, c, secondLease, "second")
	if len(plans.deliveries) != 1 || len(plans.deliveries[0].held) != 1 {
		t.Fatalf("second generation's plans = %#v, want one carrying the held first", plans.deliveries)
	}
	if _, err := deliverDelegatePacket(plans.deliveries[0], nil); err != nil {
		t.Fatalf("carrier: %v", err)
	}
	resolution := waitedResolution(t, waiter)
	stop, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_owner")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	for _, commit := range []*delegateToolResultCommit{resolution.earlier[0].commit, resolution.commit} {
		if _, err := commit.Complete(false); err != nil {
			t.Fatalf("abandon: %v", err)
		}
	}
	if _, err := c.FinishGeneration(ownerLease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatalf("FinishGeneration owner: %v", err)
	}
	for range 4 {
		if _, err := c.Reconcile(emptyDelegateReconcileEvidence(c)); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	select {
	case <-stop.done:
	default:
		t.Fatal("the stop did not complete")
	}
	if _, admitted, _ := c.BeginDelivery(held); admitted {
		t.Fatal("the held plan was admitted after the stop took its delivery")
	}
	// The owner's own result to the root is planned as usual; only the
	// target's held delivery must leave no claim behind.
	c.mu.Lock()
	targetClaims := 0
	for _, claim := range c.deliveryClaims {
		if claim.delegateID == "dlg_target" {
			targetClaims++
		}
	}
	work := c.hasDeliveryWorkForOwnerLocked("dlg_owner")
	c.mu.Unlock()
	if targetClaims != 0 || work {
		t.Fatalf("after the stop: %d target claims left, owner delivery work=%t, want none", targetClaims, work)
	}
}

// twoHeldDeliveriesFixture finishes a delegate's first generation and has its
// receiver hold that result, then finishes a second generation, which queues
// unplanned behind the held head, then a third with an inline waiter: the
// third's plan carries both earlier results, oldest first (#3951).
func twoHeldDeliveriesFixture(t *testing.T) (*delegateTreeController, string, delegateDeliveryPlan, delegateDeliveryPlan, *delegateInlineWaiter) {
	t.Helper()
	c, path := newDelegateControllerTestHarness(t, 4, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	firstLease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	held := finishDelegateDeliveryGeneration(t, c, firstLease, "first").deliveries[0]
	c.holdDeliveryClaim(held)
	secondLease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	if plans := finishDelegateDeliveryGeneration(t, c, secondLease, "second"); len(plans.deliveries) != 0 {
		t.Fatalf("second generation's plans = %#v, want none behind the held head", plans.deliveries)
	}
	thirdLease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plans := finishDelegateDeliveryGeneration(t, c, thirdLease, "third")
	if len(plans.deliveries) != 1 {
		t.Fatalf("third generation's plans = %#v, want one carrying both earlier results", plans.deliveries)
	}
	carrying := plans.deliveries[0]
	pending := pendingDeliveryIDs(c, "dlg_target")
	if len(carrying.held) != 2 || carrying.held[0].DeliveryID != pending[0] || carrying.held[1].DeliveryID != pending[1] || carrying.deliveryID != pending[2] {
		t.Fatalf("carrying plan holds %#v for %q, want %v in order", carrying.held, carrying.deliveryID, pending)
	}
	return c, path, held, carrying, waiter
}

// A wait whose result queues behind a held result and another earlier one
// answers with all three, oldest first, and acknowledging them in order
// leaves nothing to deliver as a notification.
func TestDelegateSendWaitCarriesEveryEarlierResultQueuedAheadOfIt(t *testing.T) {
	c, _, _, carrying, waiter := twoHeldDeliveriesFixture(t)
	if _, err := deliverDelegatePacket(carrying, nil); err != nil {
		t.Fatalf("carrier: %v", err)
	}
	resolution := waitedResolution(t, waiter)
	if resolution.fallback || resolution.commit == nil || len(resolution.earlier) != 2 {
		t.Fatalf("resolution = %#v, want two earlier results and the reply's own", resolution)
	}
	for i, want := range []string{"first", "second"} {
		if got := string(resolution.earlier[i].packet.Message); !strings.Contains(got, want) {
			t.Fatalf("earlier result %d = %s, want %q", i, got, want)
		}
	}
	commits := []*delegateToolResultCommit{resolution.earlier[0].commit, resolution.earlier[1].commit, resolution.commit}
	if _, err := completeDelegateDeliveryCommits(commits); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if ids := pendingDeliveryIDs(c, "dlg_target"); len(ids) != 0 {
		t.Fatalf("pending deliveries = %v, want all three acknowledged", ids)
	}
	c.mu.Lock()
	claims, receipts := len(c.deliveryClaims), len(c.deliveries)
	c.mu.Unlock()
	if claims != 0 || receipts != 0 {
		t.Fatalf("%d claims and %d receipts left, want none", claims, receipts)
	}
}

// An abandoned reply that carried two earlier results gives the held head
// back to its plan; once that is acknowledged, the others are planned in
// order as notifications.
func TestAbandonedReplyWithTwoEarlierResultsDeliversThemInOrder(t *testing.T) {
	c, _, held, carrying, waiter := twoHeldDeliveriesFixture(t)
	if _, err := deliverDelegatePacket(carrying, nil); err != nil {
		t.Fatalf("carrier: %v", err)
	}
	resolution := waitedResolution(t, waiter)
	for _, commit := range []*delegateToolResultCommit{resolution.earlier[0].commit, resolution.earlier[1].commit, resolution.commit} {
		if _, err := commit.Complete(false); err != nil {
			t.Fatalf("abandon: %v", err)
		}
	}
	token, admitted, err := c.BeginDelivery(held)
	if err != nil || !admitted {
		t.Fatalf("the held plan after the reply was abandoned: admitted=%t err=%v", admitted, err)
	}
	plans, err := c.CompleteDelivery(token, true)
	if err != nil {
		t.Fatalf("acknowledge the held result: %v", err)
	}
	for _, want := range []string{carrying.held[1].DeliveryID, carrying.deliveryID} {
		if len(plans.deliveries) != 1 || plans.deliveries[0].deliveryID != want {
			t.Fatalf("plans = %#v, want %q planned next", plans.deliveries, want)
		}
		if plans, err = deliverDelegatePacket(plans.deliveries[0], committedCallerDeliveryReceiver{}); err != nil {
			t.Fatalf("deliver %q: %v", want, err)
		}
	}
	if ids := pendingDeliveryIDs(c, "dlg_target"); len(ids) != 0 {
		t.Fatalf("pending deliveries = %v, want all delivered", ids)
	}
}

// A result behind the head that a reply has already taken is never carried
// again: while an abandoned reply has handed the head back but still holds
// the others, a new waiter's result is not planned over them.
func TestNewWaiterDoesNotCarryResultsAnotherReplyStillHolds(t *testing.T) {
	c, _, _, carrying, waiter := twoHeldDeliveriesFixture(t)
	if _, err := deliverDelegatePacket(carrying, nil); err != nil {
		t.Fatalf("carrier: %v", err)
	}
	resolution := waitedResolution(t, waiter)
	if _, err := resolution.earlier[0].commit.Complete(false); err != nil {
		t.Fatalf("abandon the head: %v", err)
	}
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	if plans := finishDelegateDeliveryGeneration(t, c, lease, "fourth"); len(plans.deliveries) != 0 {
		t.Fatalf("fourth generation's plans = %#v, want none while the second is still in a reply", plans.deliveries)
	}
}

// When acknowledging a reply's second earlier result fails after the first
// succeeded, it and the reply's own result are retried inline in order.
func TestSecondEarlierResultAckFailureRetriesTheRestInline(t *testing.T) {
	c, path, _, carrying, waiter := twoHeldDeliveriesFixture(t)
	if _, err := deliverDelegatePacket(carrying, nil); err != nil {
		t.Fatalf("carrier: %v", err)
	}
	resolution := waitedResolution(t, waiter)
	plans, err := completeDelegateDeliveryCommits([]*delegateToolResultCommit{resolution.earlier[0].commit})
	if err != nil || len(plans) != 1 || len(plans[0].deliveries) != 0 {
		t.Fatalf("acknowledging the first: plans=%#v err=%v, want nothing planned while the rest are in the reply", plans, err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := completeDelegateDeliveryCommits([]*delegateToolResultCommit{resolution.earlier[1].commit, resolution.commit}); err == nil {
		t.Fatal("acknowledging after store close succeeded, want a failure")
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	retryEveryDeliveryInline(t, c)
}
