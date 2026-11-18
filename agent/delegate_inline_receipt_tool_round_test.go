package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// errInjectedAckAppend stands in for a delegate store that fails one delivery
// acknowledgment append while accepting everything else. It is a sentinel so a
// test can prove the append it targeted failed, rather than matching
// production copy.
var errInjectedAckAppend = errors.New("injected delivery acknowledgment append failure")

// The retryable inline-receipt state is reached when a delivery acknowledgment
// append fails after the caller's tool-result turn is already durable. The
// existing pins build that state by hand at the drain seam, so a regression
// inside the real tool round -- which receipt the round commits, what a failed
// ack leaves behind, which branch the stop then takes -- would never be caught.
//
// This drives the transition through a scripted provider with real Evener below
// it: a delegate_send whose inline reply queues the caller's commit, a store
// that fails that receipt's acknowledgment append, and then a stop that must
// retry the acknowledgment to completion rather than abandon the result.
func TestDelegateInlineAckAppendFailureSettlesThroughRealToolRound(t *testing.T) {
	adapter := newMessagingAdapter("WORKER-BRIEF")
	var (
		mu         sync.Mutex
		delegateID string
		root       *Session
		controller *delegateTreeController
		taken      []string // inline delivery IDs the round took, captured from the hook
		faults     int
		sendResult string
	)
	// The child's first generation is held until the root's opening turn has
	// ended, so the send in the second turn reaches an idle child. A send into a
	// running generation is refused as concurrent_send and never waits inline.
	// The release is idempotent and deferred: a Fatalf in the opening turn skips
	// everything after it, and the scripted adapter does not observe
	// cancellation, so an unreleased gate would leave its worker blocked through
	// cleanup.
	firstGeneration := make(chan struct{})
	releaseFirstGeneration := sync.OnceFunc(func() { close(firstGeneration) })
	defer releaseFirstGeneration()
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "WORKER-BRIEF: answer when asked.")
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("root waiting for a report") },
		func(req llm.Request) llm.Response {
			id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			if id == "" {
				return agenttest.FinalResponse("no delegate id")
			}
			mu.Lock()
			delegateID = id
			mu.Unlock()
			return delegateSendResponse("send", id, "What is the answer?", 60_000)
		},
		func(req llm.Request) llm.Response {
			mu.Lock()
			sendResult = lastToolResultText(req.Messages)
			mu.Unlock()
			return agenttest.FinalResponse("root done")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			<-firstGeneration
			return agenttest.FinalResponse("GEN1_READY")
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("GEN2_ANSWER") },
	)
	root = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.testOnly.delegateDeliveryCommitsTaken = func() {
			// The round has just taken its inline commits: the durable tool-result
			// write and the acknowledgments are still ahead. Target the fault at
			// exactly those receipts, so the round's earlier non-inline reply
			// acknowledgment cannot absorb it.
			controller.mu.Lock()
			defer controller.mu.Unlock()
			var ids []string
			for _, receipt := range controller.deliveries {
				if receipt.inline && !receipt.commitQueued {
					ids = append(ids, receipt.token.deliveryID)
				}
			}
			slices.Sort(ids)
			mu.Lock()
			taken = ids
			mu.Unlock()
			controller.appendFault = func(batch []delegatestore.Event) error {
				for _, event := range batch {
					if event.Kind != delegatestore.EventDelegateDeliveryAcknowledged || event.DeliveryAcknowledged == nil {
						continue
					}
					if slices.Contains(ids, event.DeliveryAcknowledged.DeliveryID) {
						faults++
						return errInjectedAckAppend
					}
				}
				return nil
			}
		}
	})
	controller = root.delegateController
	if controller == nil {
		t.Fatal("root has no delegate controller")
	}

	// Turn one starts the delegate only.
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx := tripwireCtx(t)
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("start turn: %v", err)
	}

	// Let the first generation report, and wait for it to reach the root, so the
	// child is idle and its result is queued ahead of the send's own reply.
	releaseFirstGeneration()
	waitForRootAttention(t, root, 1)

	// Turn two carries the send. Its acknowledgment append fails, which is the
	// transition the drain-level pins never drive.
	_, processErr := root.ProcessInputKind(ctx, "", nil, EntryNotification)
	if !errors.Is(processErr, errInjectedAckAppend) {
		controller.mu.Lock()
		admissions := make([]string, 0, len(controller.deliveries))
		for _, receipt := range controller.deliveries {
			admissions = append(admissions, fmt.Sprintf("%s inline=%t retryable=%t queued=%t callerCommitted=%t",
				receipt.token.deliveryID, receipt.inline, receipt.retryable, receipt.commitQueued, receipt.callerCommitted))
		}
		controller.mu.Unlock()
		mu.Lock()
		captured := slices.Clone(taken)
		send := sendResult
		mu.Unlock()
		t.Fatalf("tool round error = %v, want the injected acknowledgment append failure\ntaken=%v\nadmissions=%v\nsend result=%s",
			processErr, captured, admissions, send)
	}

	mu.Lock()
	id, inlineIDs := delegateID, slices.Clone(taken)
	mu.Unlock()
	if id == "" {
		t.Fatal("the root never named the delegate it sent to")
	}
	if len(inlineIDs) == 0 {
		t.Fatal("the real tool round took no inline delivery commit")
	}

	// The transition under test: the round took the commit, so no queued commit
	// remains, and the failed ack left the inline receipt retryable and tracked.
	controller.mu.Lock()
	retryable := 0
	for _, receipt := range controller.deliveries {
		if receipt.inline && receipt.retryable && !receipt.commitQueued && slices.Contains(inlineIDs, receipt.token.deliveryID) {
			retryable++
		}
	}
	faultCount := faults
	controller.mu.Unlock()
	if retryable != len(inlineIDs) {
		t.Fatalf("retryable inline receipts after the failed ack = %d, want %d", retryable, len(inlineIDs))
	}
	if faultCount == 0 {
		t.Fatal("the injected acknowledgment append failure never fired")
	}

	// Before the fault is cleared, the round must have persisted the send result
	// and its delivery binding. Stop reconciliation acknowledges the in-memory
	// receipt, so without this a round that left a retryable receipt but never
	// wrote the answer or the commit would settle and still pass.
	rootPath := transcriptPath(root.stateDir, root.ID())
	persisted, err := readTranscriptFull(ctx, rootPath, "")
	if err != nil {
		t.Fatalf("read root transcript: %v", err)
	}
	sentAnswer := false
	for _, entry := range persisted.Entries {
		if entry.Turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, part := range entry.Turn.Message.Content {
			if part.ToolResult == nil || part.ToolResult.ToolCallID != "send" {
				continue
			}
			if part.ToolResult.IsError {
				t.Fatalf("the persisted send result is an error: %v", part.ToolResult.Content)
			}
			if text := fmt.Sprint(part.ToolResult.Content); !strings.Contains(text, "GEN2_ANSWER") {
				t.Fatalf("persisted send result = %q, want it to carry the delegate's answer GEN2_ANSWER", text)
			}
			sentAnswer = true
		}
	}
	if !sentAnswer {
		t.Fatal("the tool round persisted no send result for the delegate_send call")
	}
	rootFold, err := readDelegateAttentionFold(rootPath, root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	for _, deliveryID := range inlineIDs {
		if got := rootFold.deliveryCommits[deliveryID]; got != "send" {
			t.Fatalf("persisted delivery %q is committed by tool call %q, want the send call", deliveryID, got)
		}
	}

	// From here the store accepts the acknowledgment, so a stop retrying it
	// settles instead of reporting a lost result.
	controller.mu.Lock()
	controller.appendFault = nil
	controller.mu.Unlock()

	result, cancelPlan, _, err := controller.StopSubtree(rootDelegateActor(root.ID()), id)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	stop := controller.stopForResult(result)
	if stop == nil {
		t.Fatal("no stop state after StopSubtree")
	}
	// The tripwire context, not Background: a receipt left tracked by
	// reconciliation has no owner left to signal progress, so a drain that
	// waits on one must fail here rather than hang until the package timeout.
	if err := controller.drainStopForClose(ctx, stop); err != nil {
		t.Fatalf("close drain after the real ack-append failure = %v, want the retried acknowledgment to settle the stop", err)
	}
	if !channelClosed(stop.done) {
		t.Fatal("stop did not complete after the retried acknowledgment")
	}

	controller.mu.Lock()
	tracked := len(controller.deliveries)
	controller.mu.Unlock()
	if tracked != 0 {
		t.Fatalf("delivery admissions after the retried acknowledgment = %d, want 0", tracked)
	}

	stored, err := delegatestore.ReadEvents(delegateResourceStorePath(root.stateDir, root.ID()))
	if err != nil {
		t.Fatalf("read delegate events: %v", err)
	}
	for _, deliveryID := range inlineIDs {
		if !delegateEventsAcknowledgeDelivery(stored, deliveryID) {
			t.Fatalf("inline delivery %q has no durable acknowledgment after the stop retried it", deliveryID)
		}
	}

	// The close's drains leave their warnings in the pass sink and the close's
	// single publisher emits them later, so a check before that publication
	// cannot see a warning the drain queued: it would pass whether or not
	// reconciliation acknowledged the receipt durably and then also recorded it
	// lost. Publish first, then look.
	controller.publishCloseEvidence()
	for _, warning := range drainSessionWarnings(root) {
		if warning.Code == events.WarningCodeDelegateDeliveryResultLost {
			t.Fatalf("a retried inline acknowledgment produced a lost-result warning: %#v", warning)
		}
	}
}
