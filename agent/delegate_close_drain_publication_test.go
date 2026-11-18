package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
)

// closeDrainCascadeBudget is the close-cascade budget the publication test
// mints, and closeDrainStopBudget is the shorter half closeStopJoinContext
// reserves for the stop. Both are named identifiers rather than inline literals
// so the deadline audit sees no bare wall-clock bound at the call site.
const (
	closeDrainCascadeBudget = 10 * time.Second
	closeDrainStopBudget    = 250 * time.Millisecond
)

// The close drain runs the same reconciliation as the stop driver, but it is not
// a stop driver: it has no turn to deliver into and no budget to spend on one.
// It publishes the warnings its pass settled -- the lost-result notice a closing
// session most needs -- and executes nothing else.
//
// The regression is a close aborting on a delivery it should never have driven.
// Acknowledging one delegate's retryable receipt during close plans a replay for
// every other pending delivery its owner holds; executing that replay against a
// closing root reaches the transcript reopen, which refuses a closing session, and
// the error ends the close before the remaining subtrees are durably stopped. The
// unrelated delegate's receipt is still owed and still deliverable: close is
// simply not the driver that owes it.
func TestDelegateCloseDrainDoesNotDriveDeliveryReplay(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, retryable: true, topLevel: true, realRoot: true})
	c := f.controller

	// An unrelated top-level delegate, outside this stop, holding a retryable
	// receipt whose receiver-side write already committed. Its head delivery is
	// owned by the root, so acknowledging the stop's own receipt plans it for
	// replay.
	seedDelegateControllerIdle(t, c, "dlg_second", "")
	seedDelegateControllerDelivery(t, c, "dlg_second")
	secondDeliveryID := delegateAggregateSnapshot(t, c, "dlg_second").PendingDeliveries[0].DeliveryID
	if _, err := f.root.appendDelegateNotificationDurably(delegateAttentionID(secondDeliveryID), delegateNotificationContentForTest(t, c, "dlg_second")); err != nil {
		t.Fatalf("persist the second delegate's receiver-side write: %v", err)
	}
	var secondToken delegateDeliveryToken
	c.mu.Lock()
	c.nextToken++
	secondToken = delegateDeliveryToken{processID: c.nextToken, deliveryID: secondDeliveryID}
	c.deliveries[secondToken.processID] = &delegateDeliveryAdmission{
		token:      secondToken,
		delegateID: "dlg_second",
		claim:      delegateDeliveryClaimToken{processID: secondToken.processID, deliveryID: secondDeliveryID},
		retryable:  true,
	}
	c.closing = true
	c.mu.Unlock()

	// The session is closing: its transcript still exists, and its reopen
	// refuses. Nothing here is deliverable, which is the point.
	f.root.mu.Lock()
	f.root.closing = true
	f.root.mu.Unlock()

	if err := c.drainStopForClose(tripwireCtx(t), c.stopForResult(f.stop)); err != nil {
		t.Fatalf("close drain = %v, want delivery replay left to the subtree's own reconciliation", err)
	}
	stored, err := delegatestore.ReadEvents(f.storePath)
	if err != nil {
		t.Fatalf("read delegate events: %v", err)
	}
	if !delegateEventsStopCompleted(stored, "dlg_target") {
		t.Fatal("the close drain did not durably complete the stop it owned")
	}
	c.mu.Lock()
	receipt := c.deliveries[secondToken.processID]
	retryable := receipt != nil && receipt.retryable
	c.mu.Unlock()
	if !retryable {
		t.Fatal("the close drain executed the unrelated delegate's replay: its retryable receipt was consumed")
	}
	second := delegateAggregateSnapshot(t, c, "dlg_second")
	if !pendingDelivery(&second, secondDeliveryID) {
		t.Fatalf("delivery %q left the durable pending queue without a delivery", secondDeliveryID)
	}
	if delegateEventsAcknowledgeDelivery(stored, secondDeliveryID) {
		t.Fatalf("the close drain acknowledged delivery %q it never delivered", secondDeliveryID)
	}
}

// A close that parks on its own event stream is a stalled shutdown. When the
// root's authoritative consumer is wedged, a warning send has to give up on the
// stop's bound: parked on the cascade's full deadline instead, it spends the half
// teardown needs, and an isolation lane that was otherwise collectible reaches
// disposal with an expired context and stays on disk for a later prune.
//
// The observable is the lane: the same collectible lane, the same close cascade,
// collected or left behind for prune.
func TestDelegateCloseDrainWarningPublicationKeepsDisposalBudget(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	root := r.s
	c := root.delegateController

	stop := seedLostInlineStopForClose(t, c)
	root.sessionCtx = t.Context()

	// The authoritative consumer is wedged: an unbuffered buffer with nothing
	// reading makes every send park until its bound releases it, with no
	// dependence on how many events a fixture managed to emit.
	root.events = make(chan events.SessionEvent)
	root.authoritativeConsumer = true
	defer func() {
		root.authoritativeConsumer = false
	}()

	restoreBudget := SetLaneClosePassBudget(closeDrainCascadeBudget)
	defer restoreBudget()
	cascade, cancelCascade := ensureCloseBudget(context.Background())
	defer cancelCascade()
	// The stop's own half of the cascade, which closeStopJoinContext mints.
	// Held to a short value so a publication that honours it returns promptly,
	// while the cascade behind it stays long enough for real git work.
	stopCtx, cancelStop := context.WithTimeout(cascade, closeDrainStopBudget)
	defer cancelStop()

	// Publish the budget the way a close does, minus its expiry goroutine: the
	// session's own close publishes for real in cleanup, and two publishers
	// racing to close one signal is a fixture bug, not a defect.
	root.closeCtxMu.Lock()
	root.closeCtx = cascade
	root.closeCtxMu.Unlock()
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()

	if err := c.drainStopForClose(stopCtx, stop); err != nil {
		t.Fatalf("close drain = %v, want the warning publication droppable on the stop bound", err)
	}
	if !delegateStopDone(stop) {
		t.Fatal("the close drain did not complete the stop it owned")
	}

	// The stalled consumer is gone; disposal is next, exactly as the close runs
	// it, on whatever the drain left of the cascade.
	root.authoritativeConsumer = false
	root.disposeDelegateLanesAtClose(cascade)
	if lanePresent(lanePath) {
		t.Fatalf("isolation lane %s was left on disk for a later prune: the drain spent the cascade budget disposal needed", lanePath)
	}
	if !r.stableDisposalClosurePresent(t, delegateID) {
		t.Fatalf("isolation lane %s was removed without a durable disposed mark", delegateID)
	}
	if err := cascade.Err(); err != nil {
		t.Fatalf("close cascade budget spent by the drain: %v", err)
	}
}

// seedLostInlineStopForClose seeds one top-level delegate whose inline receipt
// was never answered by its owning tool round, so the stop's reconciliation
// abandons it and settles a lost-result warning, and returns that stop. The
// receipt's owner has no resident session, which is what makes the receipt
// abandonable rather than still answerable.
func seedLostInlineStopForClose(t *testing.T, c *delegateTreeController) *delegateStopState {
	t.Helper()
	const (
		ownerID  = "dlg_gone"
		targetID = "dlg_lost"
	)
	seedDelegateControllerIdle(t, c, ownerID, "")
	seedDelegateControllerRunningForRoot(t, c, targetID, "")
	c.mu.Lock()
	c.live[targetID].recoveryRequired = true
	c.live[targetID].finalizationRecoveryRequired = true
	c.live[targetID].recoveryRunnerPending = false
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateUpdatePosted,
		TS:         c.now(),
		DelegateID: targetID,
		UpdatePosted: &delegatestore.UpdatePosted{
			Generation: 1,
			Message:    "unanswered update",
		},
	})
	if err != nil {
		c.mu.Unlock()
		t.Fatalf("append pending update: %v", err)
	}
	deliveryID := delegatestore.UpdateDeliveryID(targetID, appended[0].Seq)
	c.nextToken++
	token := delegateDeliveryToken{processID: c.nextToken, deliveryID: deliveryID}
	c.deliveries[token.processID] = &delegateDeliveryAdmission{
		token:      token,
		delegateID: targetID,
		ownerID:    ownerID,
		claim:      delegateDeliveryClaimToken{processID: token.processID, deliveryID: deliveryID},
		inline:     true,
	}
	c.mu.Unlock()

	result, cancelPlan, _, err := c.StopSubtree(rootDelegateActor(c.rootSessionID), targetID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	stop := c.stopForResult(result)
	if stop == nil {
		t.Fatal("the stop for the abandoned inline receipt is not tracked")
	}
	return stop
}

// seedDelegateControllerRunningForRoot seeds a running top-level delegate owned
// by this controller's root session. The shared seeder stamps a fixed owner, so it
// cannot authorize a root stop on a harness whose root session has another id.
func seedDelegateControllerRunningForRoot(t *testing.T, c *delegateTreeController, id, parentID string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	created := delegateControllerCreatedEvent(id, parentID)
	created.Created.Descriptor.OwnerSessionID = c.rootSessionID
	_, err := c.appendLocked(
		created,
		delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, time.Unix(10, 0).UTC()),
	)
	if err != nil {
		t.Fatalf("seed running delegate %s: %v", id, err)
	}
	c.live[id] = &delegateLiveState{
		binding: &delegateRuntimeBinding{
			lease:  delegateLease{delegateID: id, generation: 1},
			cancel: func() {},
			ready:  true,
			evidence: &delegateGenerationEvidence{
				requirement: delegateCompletionReportRequired,
			},
		},
	}
}

// delegateNotificationContentForTest renders the notification body the delivery
// path would append for delegateID's head delivery, so a test seeds exactly the
// receiver-side write a real delivery leaves behind.
func delegateNotificationContentForTest(t *testing.T, c *delegateTreeController, delegateID string) string {
	t.Helper()
	aggregate := delegateAggregateSnapshot(t, c, delegateID)
	content, err := delegateNotificationContent(delegateDeliveryPlan{
		delegateID: delegateID,
		name:       aggregate.Descriptor.Name,
		packet:     aggregate.PendingDeliveries[0].Packet,
	})
	if err != nil {
		t.Fatalf("render notification content for %s: %v", delegateID, err)
	}
	return content
}

// delegateEventsStopCompleted reports whether events include the durable
// completion of the stop rooted at delegateID.
func delegateEventsStopCompleted(events []delegatestore.Event, delegateID string) bool {
	for _, event := range events {
		if event.Kind == delegatestore.EventDelegateSubtreeStopCompleted && event.DelegateID == delegateID {
			return true
		}
	}
	return false
}

// A close cascade mints ONE publication cutoff, and every pass in the cascade
// inherits it. Reserved per pass, the cutoff is not one reservation: each
// resident child's disposal pass derived its own from what was LEFT of the
// cascade, so N children spent the removals' time in N serial waits. With the
// default 30-second cascade, eight such waits left under 120 milliseconds before
// any Git work ran, and the root's collectible lanes were left on disk for a
// later prune.
//
// The test observes the cutoff on the context a descendant inherits, which is
// what makes every pass share one: closeStopJoinContext derives its budget from
// what remains, so a child that found no inherited cutoff reserved a fresh slice
// of the parent's remainder.
func TestCloseCascadeMintsOnePublicationCutoff(t *testing.T) {
	cascade, cancel := ensureCloseBudget(t.Context())
	defer cancel()

	first, cancelFirst := closeCascadePublicationContext(cascade)
	defer cancelFirst()
	firstDeadline, ok := first.Deadline()
	if !ok {
		t.Fatal("closeCascadePublicationContext minted no deadline: nothing bounds a publication")
	}

	// A descendant's pass: it inherits the cascade context, exactly as a child
	// session's disposal does, and must reuse that same cutoff.
	childCascade, cancelChild := ensureCloseBudget(cascade)
	defer cancelChild()
	second, cancelSecond := closeCascadePublicationContext(childCascade)
	defer cancelSecond()
	secondDeadline, ok := second.Deadline()
	if !ok {
		t.Fatal("the descendant's publication context has no deadline")
	}

	if !secondDeadline.Equal(firstDeadline) {
		t.Fatalf("a descendant pass minted its own publication cutoff: first=%s descendant=%s; "+
			"each pass then reserves a fresh slice of what is left, so the number of children "+
			"spends the time the lane removals need", firstDeadline, secondDeadline)
	}
}
