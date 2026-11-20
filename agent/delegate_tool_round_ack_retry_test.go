package agent

import (
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// ackRetryDeferredWait bounds the wait for the child's first result to be
// deferred behind the root's running turn. The released child answers in
// process, so only a hang reaches the bound.
const ackRetryDeferredWait = 5 * time.Second

// TestDelegateToolRoundAckAppendFailureLeavesReceiptRetryable drives the
// acknowledgment-append failure through a real tool round instead of starting
// from a pre-made retryable receipt. A root session starts a delegate, sends it
// a follow-up with a positive wait, and that send's reply carries the
// delegate's results inline. The tool round persists its tool-result turn (with
// the delivery commits) before acknowledging the deliveries, so the injected
// append failure lands after the transcript turn is durable and leaves the
// inline receipts retryable. A subsequent stop must settle those receipts from
// that state: acknowledge them, complete, and report no lost result.
func TestDelegateToolRoundAckAppendFailureLeavesReceiptRetryable(t *testing.T) {
	t.Parallel()
	releaseChild := make(chan struct{})
	var releaseOnce sync.Once
	deferred := make(chan struct{})
	var deferredOnce sync.Once
	var delegateID string

	adapter := newMessagingAdapter("ACK-FAILURE-CHILD")
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolCall("create", map[string]any{
				"prompt": "ACK-FAILURE-CHILD: report the repository name.",
			})
		},
		func(req llm.Request) llm.Response {
			id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			if id == "" {
				t.Error("the send request carried no created delegate id")
				return agenttest.FinalResponse("root done")
			}
			delegateID = id
			return delegateSendResponse("send", id, "Report again.", 60_000)
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("root done") },
	)
	adapter.script("ACK-FAILURE-CHILD",
		func(llm.Request) llm.Response {
			<-releaseChild
			return agenttest.FinalResponse("CHILD-FIRST")
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("CHILD-SECOND") },
	)

	var root *Session
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.delegateDeliveryClassified = func(s *Session, isDeferred bool) {
			if s == root && isDeferred {
				deferredOnce.Do(func() { close(deferred) })
			}
		}
		// The child's first generation finishes only once the send is about to
		// reserve its wait, and the send proceeds only after the result was
		// deferred behind the root's running turn. That pins the interleaving
		// without a wall-clock sleep.
		cfg.testOnly.delegateSendBeforePositiveWaitAdmission = func() {
			releaseOnce.Do(func() { close(releaseChild) })
			select {
			case <-deferred:
			// TRIPWIRE: the released child answers in process; only a hang
			// reaches it.
			case <-time.After(ackRetryDeferredWait):
				t.Error("the delegate's first result was never deferred behind the root's turn")
			}
		}
	})

	ackFailure := errors.New("test: acknowledgment append failed")
	var faultMu sync.Mutex
	var faultedIDs []string
	faultFired := func() int {
		faultMu.Lock()
		defer faultMu.Unlock()
		return len(faultedIDs)
	}
	controller := root.delegateController
	controller.mu.Lock()
	controller.appendFault = func(appended []delegatestore.Event) error {
		for _, event := range appended {
			if event.Kind != delegatestore.EventDelegateDeliveryAcknowledged || event.DeliveryAcknowledged == nil {
				continue
			}
			faultMu.Lock()
			faultedIDs = append(faultedIDs, event.DeliveryAcknowledged.DeliveryID)
			faultMu.Unlock()
			return ackFailure
		}
		return nil
	}
	controller.mu.Unlock()

	// The round fails because its acknowledgment append fails, which is the
	// transition under test.
	_, processErr := root.ProcessInput(tripwireCtx(t), "start", nil)
	if processErr == nil {
		t.Error("the tool round reported success even though its acknowledgment append failed")
	} else if !errors.Is(processErr, ackFailure) {
		t.Errorf("tool round error = %v, want the injected acknowledgment append failure", processErr)
	}

	// (a) Premise: the fault really did intercept a delivery acknowledgment, so
	// the state asserted below came from the injected transition.
	if faultFired() == 0 {
		t.Fatal("the acknowledgment append fault never fired: the tool round did not reach the " +
			"delivery acknowledgment this test exists to break")
	}
	createdID := delegateID
	if createdID == "" {
		t.Fatal("the root's send never recovered the created delegate id from the tool result")
	}

	// (b) The inline receipts are left retryable and their transcript turn is
	// already durable.
	type receiptState struct {
		deliveryID string
		inline     bool
		retryable  bool
	}
	var receipts []receiptState
	controller.mu.Lock()
	for _, receipt := range controller.deliveries {
		if receipt.delegateID != createdID {
			continue
		}
		receipts = append(receipts, receiptState{
			deliveryID: receipt.token.deliveryID,
			inline:     receipt.inline,
			retryable:  receipt.retryable,
		})
	}
	controller.mu.Unlock()
	if len(receipts) == 0 {
		t.Fatal("the tool round left no delivery receipt to retry")
	}
	if len(receipts) != 2 {
		t.Fatalf("the send's reply left %d receipts, want the deferred first generation and the send's own: %#v",
			len(receipts), receipts)
	}
	fold := rootAttentionFold(t, root)
	for _, receipt := range receipts {
		if !receipt.inline {
			t.Errorf("delivery %q was not admitted inline", receipt.deliveryID)
		}
		if !receipt.retryable {
			t.Errorf("delivery %q is not retryable after its acknowledgment append failed; "+
				"the append-failure transition did not leave it for a retry", receipt.deliveryID)
		}
		if callID := fold.deliveryCommits[receipt.deliveryID]; callID != "send" {
			t.Errorf("delivery %q has no durable delivery commit in the root transcript: "+
				"the turn was not durable before the acknowledgment failed (commit %q)", receipt.deliveryID, callID)
		}
	}
	faultMu.Lock()
	fired := append([]string(nil), faultedIDs...)
	faultMu.Unlock()
	receiptIDs := make(map[string]bool, len(receipts))
	for _, receipt := range receipts {
		receiptIDs[receipt.deliveryID] = true
	}
	for _, deliveryID := range fired {
		if !receiptIDs[deliveryID] {
			t.Errorf("the injected fault fired on delivery %q, which is not one of the tool round's receipts %v",
				deliveryID, receiptIDs)
		}
	}

	// (c) The stop retries the receipts from the state the tool round left and
	// settles without a lost result.
	beforeStop, err := delegatestore.ReadEvents(schema.SessionDelegateJournalPath(root.stateDir, root.ID()))
	if err != nil {
		t.Fatalf("read delegate events before the stop: %v", err)
	}
	for _, receipt := range receipts {
		if delegateEventsAcknowledgeDelivery(beforeStop, receipt.deliveryID) {
			t.Fatalf("delivery %q was acknowledged before the stop: the retry did not come from the stop",
				receipt.deliveryID)
		}
	}
	controller.mu.Lock()
	controller.appendFault = nil
	controller.mu.Unlock()
	out, err := stopDelegate(tripwireCtx(t), root, createdID, 5_000)
	if err != nil {
		t.Fatalf("stop the delegate after the failed acknowledgment: %v", err)
	}
	stop, ok := out.(tool.StateResult).State.(jobStopResult)
	if !ok {
		t.Fatalf("stop result state = %#v, want a jobStopResult", out)
	}
	if stop.Reason == nil || *stop.Reason != "stopped_by_parent" {
		t.Fatalf("stop reason = %v, want stopped_by_parent (the stop completed)", stop.Reason)
	}
	stored, err := delegatestore.ReadEvents(schema.SessionDelegateJournalPath(root.stateDir, root.ID()))
	if err != nil {
		t.Fatalf("read delegate events: %v", err)
	}
	for _, receipt := range receipts {
		if !delegateEventsAcknowledgeDelivery(stored, receipt.deliveryID) {
			t.Errorf("delivery %q has no durable acknowledgment after the stop retried it", receipt.deliveryID)
		}
	}
	for _, warning := range drainSessionWarnings(root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost && warning.DelegateID == createdID {
			t.Fatalf("the stop reported the retried receipt as a lost result: %#v", warning)
		}
	}
}
