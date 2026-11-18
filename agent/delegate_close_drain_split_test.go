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
// has no receipt to abandon and therefore nothing to report. When a cleanup
// step then ends the pass with an error, the warning must already have been
// published -- otherwise the user never learns the result was lost, and no
// later pass can tell them.
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
func TestDelegateStopCloseDrainLeavesOtherSubtreeReplayToItsOwnStop(t *testing.T) {
	c, storePath := newDelegateControllerTestHarness(t, 2, 2)
	root := newRootRuntimeForClose(t, c)
	root.events = make(chan events.SessionEvent, 8)

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
		c.nextToken++
		token := delegateDeliveryToken{processID: c.nextToken, deliveryID: aggregate.PendingDeliveries[0].DeliveryID}
		c.deliveries[token.processID] = &delegateDeliveryAdmission{
			token:      token,
			delegateID: id,
			// Both are owned by the root, so both resolve to the same receiver.
			ownerID:   "",
			retryable: true,
			inline:    true,
		}
		tokens = append(tokens, token)
	}
	// Fail exactly one acknowledgment append, for the second subtree's receipt.
	// Reached through the first subtree's replay, it aborts the close before the
	// second subtree is stopped; reached through the second subtree's own drain,
	// it leaves that receipt retryable for the drain's own retry.
	faults := 0
	c.appendFault = func(batch []delegatestore.Event) error {
		for _, event := range batch {
			if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged && event.DeliveryAcknowledged != nil &&
				event.DeliveryAcknowledged.DeliveryID == tokens[1].deliveryID && faults == 0 {
				faults++
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
	for _, event := range stored {
		if event.Kind == delegatestore.EventDelegateSubtreeStopCompleted {
			stopped[event.DelegateID] = true
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
