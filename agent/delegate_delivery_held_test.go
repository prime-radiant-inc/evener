package agent

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/transcript"
)

func waitedResolution(t *testing.T, waiter *delegateInlineWaiter) delegateInlineResolution {
	t.Helper()
	select {
	case resolution := <-waiter.resolution:
		return resolution
	default:
		t.Fatal("inline subscriber was not resolved")
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

// A subscriber uses the same real receiver writer as ordinary delivery. The
// source journal, not a receiver queue, owns any retry before custody.
func attachDeliveryTestReceiver(t *testing.T, c *delegateTreeController) *Session {
	t.Helper()
	writer, err := transcript.NewWriter(transcriptPath(c.stateDir, c.rootSessionID), transcript.Header{SessionID: c.rootSessionID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	root := &Session{id: c.rootSessionID, stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(writer)
	c.mu.Lock()
	c.rootRuntime = root
	c.mu.Unlock()
	return root
}

func canonicalGenerationsFixture(t *testing.T, count int) (*delegateTreeController, *Session, delegateDeliveryPlan, *delegateInlineWaiter) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, count+1, 1)
	root := attachDeliveryTestReceiver(t, c)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	for i := 1; i < count; i++ {
		lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
		if err := root.executeDelegateMutationPlans(finishDelegateDeliveryGeneration(t, c, lease, []string{"first", "second"}[i-1])); err != nil {
			t.Fatal(err)
		}
	}
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "latest").deliveries[0]
	return c, root, plan, waiter
}

func TestCanonicalDeliveryRefusedPlanLeavesEarlierCustody(t *testing.T) {
	c, root, plan, waiter := canonicalGenerationsFixture(t, 2)
	forged := plan
	forged.packet = cloneDelegateTerminalPacket(plan.packet)
	forged.packet.Message = []byte(`"forged"`)
	if _, err := deliverDelegatePacket(forged, root); err != nil {
		t.Fatal(err)
	}
	if !waitedResolution(t, waiter).fallback {
		t.Fatal("forged projection did not fall back")
	}
	ids, err := root.pendingNotificationIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{delegateAttentionID(delegateDeliveryID("dlg_target", 1))}) {
		t.Fatalf("earlier canonical custody = %v", ids)
	}
	if _, err := deliverDelegatePacket(plan, root); err != nil {
		t.Fatal(err)
	}
	if ids := pendingDeliveryIDs(c, "dlg_target"); len(ids) != 0 {
		t.Fatalf("pending source = %v", ids)
	}
}

func TestCanonicalDeliveryStoppedPlanLeavesNoStaleClaim(t *testing.T) {
	c, root, plan, waiter := canonicalGenerationsFixture(t, 2)
	appendDelegateControllerStopRequest(t, c, "dlg_target")
	if _, err := deliverDelegatePacket(plan, root); err != nil {
		t.Fatal(err)
	}
	if !waitedResolution(t, waiter).fallback {
		t.Fatal("stopped subscriber did not fall back")
	}
	c.mu.Lock()
	claims := len(c.deliveryClaims)
	c.mu.Unlock()
	if claims != 0 {
		t.Fatalf("claims after stop refusal = %d", claims)
	}
}

func TestCanonicalDeliveryAbandonedProjectionKeepsOrdinaryCustody(t *testing.T) {
	c, root, plan, waiter := canonicalGenerationsFixture(t, 3)
	if _, err := deliverDelegatePacket(plan, root); err != nil {
		t.Fatal(err)
	}
	resolution := waitedResolution(t, waiter)
	if len(resolution.earlier) != 2 || resolution.commit == nil {
		t.Fatalf("resolution = %#v", resolution)
	}
	for _, commit := range []*delegateToolResultCommit{resolution.earlier[0].commit, resolution.earlier[1].commit, resolution.commit} {
		if _, err := commit.Complete(false); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := root.pendingNotificationIDs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{delegateAttentionID(delegateDeliveryID("dlg_target", 1)), delegateAttentionID(delegateDeliveryID("dlg_target", 2)), delegateAttentionID(plan.deliveryID)}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ordinary canonical IDs = %v, want %v", ids, want)
	}
	if len(pendingDeliveryIDs(c, "dlg_target")) != 0 {
		t.Fatal("projection abandonment reopened source custody")
	}
}

func TestCanonicalDeliveryOwnerStopDoesNotRestoreProjectionClaims(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 2)
	root := attachDeliveryTestReceiver(t, c)
	seedDelegateControllerRunning(t, c, "dlg_owner", "")
	seedDelegateControllerIdle(t, c, "dlg_target", "dlg_owner")
	ownerID := c.durable["dlg_owner"].Descriptor.ChildSessionID
	owner := &Session{id: ownerID, stateDir: c.stateDir, delegateController: c, owningDelegateID: "dlg_owner"}
	writer, err := transcript.NewWriter(transcriptPath(c.stateDir, ownerID), transcript.Header{SessionID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	owner.attachTranscript(writer)
	t.Cleanup(func() { _ = writer.Close() })
	c.live["dlg_owner"].runtime = owner
	ownerLease := delegateLease{delegateID: "dlg_owner", generation: 1}
	actor := delegateActor{rootSessionID: "root-session", lease: &ownerLease}
	var commits []*delegateToolResultCommit
	var first delegateDeliveryPlan
	for i := 0; i < 2; i++ {
		reservation, err := c.ReserveStart(actor, "dlg_target")
		if err != nil {
			t.Fatal(err)
		}
		var waiter *delegateInlineWaiter
		if i == 1 {
			waiter, err = c.RegisterInlineWaiter(reservation)
			if err != nil {
				t.Fatal(err)
			}
		}
		started, err := c.CommitStart(reservation)
		if err != nil {
			t.Fatal(err)
		}
		plan := finishDelegateDeliveryGeneration(t, c, started.lease, "result").deliveries[0]
		if i == 0 {
			first = plan
		}
		if _, err := deliverDelegatePacket(plan, owner); err != nil {
			t.Fatal(err)
		}
		if waiter != nil {
			resolution := waitedResolution(t, waiter)
			if len(resolution.earlier) != 1 || resolution.commit == nil {
				t.Fatalf("resolution=%#v", resolution)
			}
			commits = []*delegateToolResultCommit{resolution.earlier[0].commit, resolution.commit}
		}
	}
	stop, _, stopPlans, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.executeDelegateMutationPlans(stopPlans); err != nil {
		t.Fatal(err)
	}
	for _, commit := range commits {
		if _, err := commit.Complete(false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.FinishGeneration(ownerLease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
		if err != nil {
			t.Fatal(err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-stop.done:
	default:
		t.Fatal("owner stop did not complete")
	}
	if _, admitted, _ := c.BeginDelivery(first); admitted {
		t.Fatal("stale projection restored a stopped delivery claim")
	}
	c.mu.Lock()
	work := c.hasDeliveryWorkForOwnerLocked("dlg_owner")
	targetClaims := 0
	for _, claim := range c.deliveryClaims {
		if claim.delegateID == "dlg_target" {
			targetClaims++
		}
	}
	c.mu.Unlock()
	if work || targetClaims != 0 {
		t.Fatalf("owner work=%t target claims=%d", work, targetClaims)
	}
}

func TestCanonicalDeliveryEarlierResultsRemainOrdered(t *testing.T) {
	c, root, plan, waiter := canonicalGenerationsFixture(t, 3)
	if _, err := deliverDelegatePacket(plan, root); err != nil {
		t.Fatal(err)
	}
	resolution := waitedResolution(t, waiter)
	if len(resolution.earlier) != 2 {
		t.Fatalf("earlier results = %#v", resolution.earlier)
	}
	for i, want := range []string{"first", "second"} {
		if !strings.Contains(string(resolution.earlier[i].packet.Message), want) || resolution.earlier[i].commit.deliveryID != delegateDeliveryID("dlg_target", uint64(i+1)) {
			t.Fatalf("earlier %d = %#v", i, resolution.earlier[i])
		}
	}
	if len(pendingDeliveryIDs(c, "dlg_target")) != 0 {
		t.Fatal("inline subscriber retained source custody")
	}
}

func TestCanonicalDeliverySourceAckFailureRetriesWithoutDuplicate(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 1)
	root := attachDeliveryTestReceiver(t, c)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "first").deliveries[0]
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := deliverDelegatePacket(plan, root); err == nil {
		t.Fatal("source acknowledgment unexpectedly succeeded")
	}
	// A failed immutable-packet lookup cannot steal ordinary canonical delivery.
	if !waitedResolution(t, waiter).fallback {
		t.Fatal("failed packet lookup did not fall back")
	}
	ids, err := root.pendingNotificationIDs()
	if err != nil || len(ids) != 1 {
		t.Fatalf("canonical fallback = %v, %v", ids, err)
	}
	reopened, err := delegatestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	if err := root.replayDelegateDeliveries(); err != nil {
		t.Fatal(err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, c.rootSessionID), c.rootSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fold.order) != 1 || len(pendingDeliveryIDs(c, "dlg_target")) != 0 {
		t.Fatalf("fold order=%v pending=%v", fold.order, pendingDeliveryIDs(c, "dlg_target"))
	}
}

func TestCanonicalDeliveryPacketReadFailureKeepsUnrelatedProgress(t *testing.T) {
	c, path := newDelegateControllerTestHarness(t, 2, 2)
	root := attachDeliveryTestReceiver(t, c)
	seedDelegateControllerIdle(t, c, "dlg_waited", "")
	seedDelegateControllerIdle(t, c, "dlg_other", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_waited", true)
	plans := finishDelegateDeliveryGeneration(t, c, lease, "waited")
	other, _ := startDelegateDeliveryGeneration(t, c, "dlg_other", false)
	plans.deliveries = append(plans.deliveries, finishDelegateDeliveryGeneration(t, c, other, "other").deliveries...)
	// The source writer remains open and can acknowledge custody. Only the
	// immutable packet lookup through Store.Load loses its external read path.
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(path+".saved", path) })
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatal(err)
	}
	if !waitedResolution(t, waiter).fallback {
		t.Fatal("packet lookup failure did not fall back")
	}
	assertNotificationPending(t, root, []string{delegateAttentionID(delegateDeliveryID("dlg_waited", 1)), delegateAttentionID(delegateDeliveryID("dlg_other", 1))})
	if len(pendingDeliveryIDs(c, "dlg_waited")) != 0 || len(pendingDeliveryIDs(c, "dlg_other")) != 0 {
		t.Fatal("packet lookup failure blocked source custody or unrelated progress")
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if plans := c.ReplayDeliveries(); len(plans) != 0 {
		t.Fatalf("already accepted sources replayed: %#v", plans)
	}
}

func TestCanonicalDeliveryFailedReceiverPreservesSourceFIFO(t *testing.T) {
	c, first, _, _, _ := controllerWithTwoDelegateDeliveries(t, false, true)
	receiver := newFakeDelegateDeliveryReceiver()
	receiver.fail = errInjectedTranscriptWrite
	if _, err := deliverDelegatePacket(first, receiver); !errors.Is(err, errInjectedTranscriptWrite) {
		t.Fatalf("receiver failure=%v", err)
	}
	if got := pendingDeliveryIDs(c, "dlg_target"); !reflect.DeepEqual(got, []string{delegateDeliveryID("dlg_target", 1), delegateDeliveryID("dlg_target", 2)}) {
		t.Fatalf("source FIFO=%v", got)
	}
	receiver.fail = nil
	queue := c.ReplayDeliveries()
	for len(queue) > 0 {
		plan := queue[0]
		queue = queue[1:]
		next, err := deliverDelegatePacket(plan, receiver)
		if err != nil {
			t.Fatal(err)
		}
		queue = append(queue, next.deliveries...)
	}
	if receiver.writes != 2 || len(pendingDeliveryIDs(c, "dlg_target")) != 0 {
		t.Fatalf("writes=%d pending=%v", receiver.writes, pendingDeliveryIDs(c, "dlg_target"))
	}
}

// seedPendingDeliverySource replaces receiver-queue-only fixtures with an actual
// journal-backed producer obligation. It deliberately does not run transport.
func seedPendingDeliverySource(t *testing.T, s *Session) string {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_pending", "")
	seedDelegateControllerDelivery(t, c, "dlg_pending")
	old := s.delegateController
	s.delegateController = c
	c.rootSessionID = s.ID()
	c.rootRuntime = s
	t.Cleanup(func() { s.delegateController = old })
	if s.transcript == nil {
		s.stateDir = c.stateDir
		writer, err := transcript.NewWriter(transcriptPath(s.stateDir, s.ID()), transcript.Header{SessionID: s.ID()})
		if err != nil {
			t.Fatal(err)
		}
		s.attachTranscript(writer)
		t.Cleanup(func() { _ = writer.Close() })
	}
	return delegateDeliveryID("dlg_pending", 1)
}
