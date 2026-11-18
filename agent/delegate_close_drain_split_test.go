package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/transcript"
)

// errInjectedAttentionCleanup fails a stop's attention cleanup after the same
// pass has also abandoned an inline receipt. A sentinel, so the test proves the
// pass ended at that cleanup rather than matching production copy.
var errInjectedAttentionCleanup = errors.New("injected attention cleanup failure")

// A single reconciliation pass can both abandon an inline receipt and plan
// attention cleanup. The abandon bumps the evidence version and drops the
// receipt, so its lost-result warning exists only in that pass: the next pass
// has no receipt left to notice. A shell repair or attention cleanup that then
// ends the pass with an error must therefore find the warning already
// published -- otherwise the user never learns the result was lost.
func TestDelegateStopCloseDrainReportsLostResultBeforeCleanupEndsThePass(t *testing.T) {
	const (
		childSessionID = "child-dlg_target"
		attentionID    = "attention-pending-with-lost-receipt"
	)
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true})
	if f.root == nil {
		t.Fatal("fixture did not create the root runtime")
	}
	writeDelegateAttentionTranscript(t, transcriptPath(f.controller.stateDir, childSessionID), childSessionID, attentionID)
	f.root.events = make(chan events.SessionEvent, 8)

	// The pass plans the abandon and the cleanup together; the cleanup then
	// fails, which is a step that ends the pass after the warning was settled.
	f.controller.mu.Lock()
	f.controller.attentionOpen = func(string, string) (*transcript.Writer, []transcript.Entry, error) {
		return nil, nil, errInjectedAttentionCleanup
	}
	f.controller.mu.Unlock()

	err := f.controller.drainStopForClose(context.Background(), f.controller.stopForResult(f.stop))
	if !errors.Is(err, errInjectedAttentionCleanup) {
		t.Fatalf("close drain error = %v, want the injected cleanup failure", err)
	}

	found := false
	for _, warning := range drainSessionWarnings(f.root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_target" {
			found = true
		}
	}
	if !found {
		t.Fatal("the pass abandoned an inline receipt but ended on a cleanup step, and the lost result was never reported")
	}
}

// Two top-level delegates with retryable acknowledgments share the root
// receiver. A close settles one subtree at a time, so a reconciliation that
// replayed the other subtree's receipt would acknowledge -- or fail on -- a
// delivery belonging to a subtree whose durable stop had not been admitted yet,
// and a failed append would end the close before that subtree was stopped at
// all. The close drain must publish warnings and snapshots without running the
// ordinary owner replay, leaving each subtree to retry its own acknowledgment.
//
// The receipts carry real claims, as a delivery awaiting its acknowledgment
// does, so a replay admitted by BeginDelivery would reach the acknowledgment
// append rather than being refused as stale. The injected failure is armed only
// once the second subtree's durable stop request exists, so the assertions
// below distinguish "replayed and failed early" from "settled by its own stop".
func TestDelegateStopCloseDrainLeavesOtherSubtreeReplayToItsOwnStop(t *testing.T) {
	c, storePath := newDelegateControllerTestHarness(t, 2, 2)
	root := newRootRuntimeForClose(t, c)
	root.events = make(chan events.SessionEvent, 8)
	var published []delegateUpdatePlan
	c.mu.Lock()
	c.emitUpdate = func(plan delegateUpdatePlan) { published = append(published, plan) }
	c.mu.Unlock()

	const (
		first  = "dlg_first"
		second = "dlg_second"
	)
	var tokens []delegateDeliveryToken
	for _, id := range []string{first, second} {
		seedDelegateControllerIdle(t, c, id, "")
		seedDelegateControllerDelivery(t, c, id)
	}
	c.mu.Lock()
	for _, id := range []string{first, second} {
		aggregate := c.durable[id]
		if aggregate == nil || len(aggregate.PendingDeliveries) != 1 {
			c.mu.Unlock()
			t.Fatalf("delegate %q has no single pending delivery: %#v", id, aggregate)
		}
		head := aggregate.PendingDeliveries[0]
		c.nextToken++
		token := delegateDeliveryToken{processID: c.nextToken, deliveryID: head.DeliveryID}
		// A real claim under the pending delivery, as claimDelegateDeliveryLocked
		// builds it: without one, BeginDelivery refuses the replay as stale
		// before it can reach an acknowledgment append.
		claim := &delegateDeliveryClaim{
			token:      delegateDeliveryClaimToken{processID: token.processID, deliveryID: head.DeliveryID},
			delegateID: id,
			ownerID:    head.OwnerDelegateID,
		}
		c.deliveryClaims[head.DeliveryID] = claim
		c.deliveries[token.processID] = &delegateDeliveryAdmission{
			token:      token,
			delegateID: id,
			ownerID:    head.OwnerDelegateID,
			claim:      claim.token,
			retryable:  true,
			inline:     true,
		}
		tokens = append(tokens, token)
	}
	// Fail one acknowledgment append, for the second subtree's receipt, and only
	// once that subtree's durable stop request exists. Replayed through the
	// first subtree it fires before that request is appended, which is the bug;
	// reached through the second subtree's own drain it fires after, which
	// leaves the receipt retryable for the drain's retry.
	faults, stopRequested := 0, false
	c.appendFault = func(batch []delegatestore.Event) error {
		for _, event := range batch {
			if event.Kind == delegatestore.EventDelegateSubtreeStopRequested && event.DelegateID == second {
				stopRequested = true
			}
		}
		for _, event := range batch {
			if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged && event.DeliveryAcknowledged != nil &&
				event.DeliveryAcknowledged.DeliveryID == tokens[1].deliveryID && faults == 0 {
				faults++
				if !stopRequested {
					return errors.Join(errInjectedAckAppend, errors.New("acknowledged the second subtree before its stop was requested"))
				}
				return errInjectedAckAppend
			}
		}
		return nil
	}
	c.mu.Unlock()

	if err := c.closeRuntimeTree(context.Background()); err != nil {
		t.Fatalf("close with two retryable top-level subtrees = %v, want both subtrees stopped", err)
	}

	stored, err := delegatestore.ReadEvents(storePath)
	if err != nil {
		t.Fatalf("read delegate events: %v", err)
	}
	stopped := make(map[string]bool)
	stopRequestedIndex := make(map[string]int)
	ackIndex := -1
	for i, event := range stored {
		if event.Kind == delegatestore.EventDelegateSubtreeStopRequested {
			stopRequestedIndex[event.DelegateID] = i
		}
		if event.Kind == delegatestore.EventDelegateSubtreeStopCompleted {
			stopped[event.DelegateID] = true
		}
		if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged && event.DeliveryAcknowledged != nil &&
			event.DeliveryAcknowledged.DeliveryID == tokens[1].deliveryID {
			ackIndex = i
		}
	}
	for _, id := range []string{first, second} {
		if !stopped[id] {
			t.Fatalf("subtree %q has no durable stop completion; the close skipped it", id)
		}
	}
	if faults == 0 {
		t.Fatal("the injected acknowledgment append failure never fired")
	}
	if ackIndex < 0 {
		t.Fatal("the second subtree's receipt was never acknowledged")
	}
	requestIndex, ok := stopRequestedIndex[second]
	if !ok {
		t.Fatalf("subtree %q has no durable stop request", second)
	}
	if ackIndex < requestIndex {
		t.Fatal("the second subtree's delivery was acknowledged before its own stop was requested: another subtree's replay reached it")
	}

	// The per-subtree drains leave snapshot publication to the close, so the
	// close itself must publish exactly one snapshot for every subtree it
	// stopped: none means deferring quietly became dropping, and more than one
	// means a drain published inside the loop, where event backpressure can
	// spend a later subtree's stop budget.
	publishedRows := make(map[string]int)
	for _, plan := range published {
		for _, row := range plan.rows {
			publishedRows[row.id]++
		}
	}
	for _, id := range []string{first, second} {
		if got := publishedRows[id]; got != 1 {
			t.Fatalf("subtree %q snapshot publications = %d, want exactly one from the close: %#v", id, got, publishedRows)
		}
	}
}

// errInjectedStopCompletionAppend fails the durable stop-completion append of a
// pass that has already abandoned an inline receipt. A sentinel, so the test
// proves the pass ended at that append rather than matching production copy.
var errInjectedStopCompletionAppend = errors.New("injected stop completion append failure")

// The lost-result warning for an abandoned receipt exists only in the pass that
// abandons it: the abandon takes the receipt out of the admission map, so no
// later pass can notice it again. Reconciliation can then fail on the stop's
// own completion append, and it reports that failure to the drain. The warning
// must survive that error rather than being thrown away with the plan, or the
// user never learns the result was lost.
func TestDelegateStopCloseDrainReportsLostResultWhenStopCompletionAppendFails(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, inline: true, realRoot: true})
	if f.root == nil {
		t.Fatal("fixture did not create the root runtime")
	}
	f.root.events = make(chan events.SessionEvent, 8)

	// The pass abandons the unowned inline receipt and then fails its own stop
	// completion append, which is the step that returns an error to the drain.
	f.controller.appendFault = func(batch []delegatestore.Event) error {
		for _, event := range batch {
			if event.Kind == delegatestore.EventDelegateSubtreeStopCompleted {
				return errInjectedStopCompletionAppend
			}
		}
		return nil
	}

	err := f.controller.drainStopForClose(context.Background(), f.controller.stopForResult(f.stop))
	if !errors.Is(err, errInjectedStopCompletionAppend) {
		t.Fatalf("close drain error = %v, want the injected stop-completion append failure", err)
	}

	found := false
	for _, warning := range drainSessionWarnings(f.root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == "dlg_target" {
			found = true
		}
	}
	if !found {
		t.Fatal("the pass abandoned an inline receipt but failed its stop-completion append, and the lost result was never reported")
	}
}

// newRootRuntimeForClose attaches a durable transcript to a root session the
// controller owns, so the close drain's plan hand-off has a real receiver.
func newRootRuntimeForClose(t *testing.T, c *delegateTreeController) *Session {
	t.Helper()
	writer, err := transcript.NewWriter(transcriptPath(c.stateDir, c.rootSessionID), transcript.Header{SessionID: c.rootSessionID})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	root := &Session{id: c.rootSessionID, stateDir: c.stateDir, state: SessionIdle, delegateController: c}
	root.attachTranscript(writer, nil)
	c.mu.Lock()
	c.rootRuntime = root
	c.mu.Unlock()
	return root
}
