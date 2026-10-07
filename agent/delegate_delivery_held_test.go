package agent

import (
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
	if len(plans.deliveries) != 1 || plans.deliveries[0].held == nil || plans.deliveries[0].held.DeliveryID != held.deliveryID {
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
	c.mu.Lock()
	c.durable["dlg_target"].PendingStopSeq = 1
	c.mu.Unlock()
	if _, err := deliverDelegatePacket(carrying, nil); err != nil {
		t.Fatalf("refused carrier: %v", err)
	}
	if resolution := waitedResolution(t, waiter); !resolution.fallback {
		t.Fatalf("refused carrier resolved its waiter with %#v, want the fallback", resolution)
	}
	c.mu.Lock()
	c.durable["dlg_target"].PendingStopSeq = 0
	c.mu.Unlock()
	if _, admitted, err := c.BeginDelivery(held); err != nil || !admitted {
		t.Fatalf("the held plan after a refused carrier: admitted=%t err=%v", admitted, err)
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
	if resolution.earlier == nil || resolution.commit == nil {
		t.Fatalf("resolution = %#v, want the held result and the reply's own", resolution)
	}
	if _, admitted, _ := c.BeginDelivery(held); admitted {
		t.Fatal("the held plan was admitted while the reply carried its result")
	}
	for _, commit := range []*delegateToolResultCommit{resolution.earlier.commit, resolution.commit} {
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
	plans, err := completeDelegateDeliveryCommits([]*delegateToolResultCommit{resolution.earlier.commit, resolution.commit})
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
		t.Fatalf("pending deliveries after the retries = %v, want both acknowledged", ids)
	}
	c.mu.Lock()
	claims := len(c.deliveryClaims)
	c.mu.Unlock()
	if claims != 0 {
		t.Fatalf("%d claims left after the retries, want none: nothing is re-delivered as a notification", claims)
	}
}
