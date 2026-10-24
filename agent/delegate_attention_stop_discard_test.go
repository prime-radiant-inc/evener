package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestSubtreeStopPreservesNotificationCustody(t *testing.T) {
	t.Parallel()
	t.Run("registered tools observe after release", testSubtreeStopCustodyObservedAfterRelease)
	for _, historical := range []bool{false, true} {
		name := "new request"
		if historical {
			name = "historical request"
		}
		t.Run(name, func(t *testing.T) {
			c, path := newDelegateControllerTestHarness(t, 2, 1)
			seedDelegateControllerIdle(t, c, "dlg_parent", "")
			seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
			seedDelegateControllerDelivery(t, c, "dlg_child")
			childPath := transcriptPath(c.stateDir, "child-dlg_parent")
			writeDelegateAttentionTranscript(t, childPath, "child-dlg_parent", "accepted-stop-731a")
			shellPath := filepath.Join(jobsDir(c.stateDir, "child-dlg_parent"), "jobs.jsonl")
			seedDelegateShellStoreAt(t, shellPath)
			if historical {
				appendDelegateControllerStopRequest(t, c, "dlg_parent")
			} else {
				_, cancel, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_parent")
				if err != nil {
					t.Fatal(err)
				}
				executeDelegateCancelPlan(cancel)
			}
			// Reopen while the Stop fence is active. Neither source delivery nor
			// accepted input grants permission to execute through this fence.
			c = reopenDelegateController(t, c, path)
			if got := c.ReplayDeliveries(); len(got) != 0 {
				t.Fatalf("delivery crossed pending Stop: %+v", got)
			}
			if err := c.drainStop(context.Background(), c.stop, nil); err != nil {
				t.Fatal(err)
			}
			c = reopenDelegateController(t, c, path)
			fold, err := readDelegateAttentionFold(childPath, "child-dlg_parent")
			if err != nil {
				t.Fatal(err)
			}
			shellEvents, err := jobstore.ReadEvents(shellPath)
			if err != nil {
				t.Fatal(err)
			}
			consumed := 0
			for _, event := range shellEvents {
				if event.Kind == jobstore.EventJobNotificationConsumed {
					consumed++
				}
			}
			if historical {
				if len(fold.pendingIDs()) != 0 || fold.resolutions["accepted-stop-731a"] != delegateAttentionDiscarded || len(c.durable["dlg_child"].PendingDeliveries) != 0 || consumed != 1 {
					t.Fatalf("historical policy changed: fold=%+v child=%+v consumed=%d", fold, c.durable["dlg_child"], consumed)
				}
				return
			}
			if len(fold.pendingIDs()) != 1 || len(fold.resolutions) != 0 {
				t.Fatalf("Stop discarded accepted input: pending=%v resolutions=%v", fold.pendingIDs(), fold.resolutions)
			}
			if consumed != 0 {
				t.Fatalf("Stop wrote %d unearned shell observation receipts", consumed)
			}
			if len(c.durable["dlg_child"].PendingDeliveries) != 1 {
				t.Fatal("Stop erased produced-but-undelivered child source")
			}
			// Stop completion is this controller fence's existing release path.
			if got := c.ReplayDeliveries(); len(got) != 1 {
				t.Fatalf("retained delivery not eligible after Stop completion: %+v", got)
			}
		})
	}
}

func testSubtreeStopCustodyObservedAfterRelease(t *testing.T) {
	adapter := newMessagingAdapter("STOP-CUSTODY-731a")
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	entered, modelGate, observed := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	releaseModel := sync.OnceFunc(func() { close(modelGate) })
	defer releaseModel()
	adapter.script("root", func(llm.Request) llm.Response {
		return delegateToolResponse("stop-owner", "STOP-CUSTODY-731a")
	}, func(llm.Request) llm.Response { return finalResponse("root-before-stop-731a") })
	adapter.script("STOP-CUSTODY-731a", func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "stop-owned-shell", Name: "shell", Arguments: args, Type: "function"})
	}, func(llm.Request) llm.Response {
		return toolCallResponse(llm.ToolCallData{ID: "stop-watch", Name: "job_watch", Arguments: json.RawMessage(`{"operation":"create","source":"self","events":["assistant.tool"],"event_filter":{"tool_name":"read_file"},"note":"stop-produced-frame-731a"}`), Type: "function"})
	}, func(llm.Request) llm.Response {
		close(entered)
		<-modelGate
		return finalResponse("canceled-before-observation-731a")
	}, func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{"stop-produced-frame-731a"}, nil)
		observed <- struct{}{}
		return finalResponse("observed-after-stop-release-731a")
	})
	root := newMessagingRoot(t, adapter, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start Stop custody fixture", nil); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, entered, "initial child provider request")
	child := root.subagents.sessions()[0]
	child.execTool(ctx, llm.ToolCallData{ID: "stop-frame-source", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"missing-stop-source-731a","intent":"Reading source to produce a real frame before subtree Stop."}`), Type: "function"}, llm.FinishReasonToolCalls)
	ids, err := child.pendingNotificationIDs()
	if err != nil || len(ids) != 1 {
		t.Fatalf("accepted frame before Stop: ids=%v err=%v", ids, err)
	}
	result, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), child.owningDelegateID)
	if err != nil {
		t.Fatal(err)
	}
	executeDelegateCancelPlan(cancelPlan)
	stop := root.delegateController.stopForResult(result)
	if stop == nil {
		t.Fatal("real Stop did not retain its cancellation fence")
	}
	if got := len(adapter.requestsFor("STOP-CUSTODY-731a")); got != 3 {
		t.Fatalf("Stop fence admitted %d requests, want the original three", got)
	}
	assertNotificationPending(t, child, ids)
	fold, err := readDelegateAttentionFold(child.TranscriptPath(), child.ID())
	if err != nil || len(fold.resolutions) != 0 {
		t.Fatalf("Stop earned a false receiver receipt: resolutions=%v err=%v", fold.resolutions, err)
	}
	// The registered tool joins the same admitted Stop, not another policy or
	// synthetic completion. Its fence cannot settle until the real run unwinds.
	toolDone := make(chan error, 1)
	go func() {
		args, _ := json.Marshal(map[string]any{"target": child.owningDelegateID, "max_wait_ms": 5000})
		out := root.execTool(ctx, llm.ToolCallData{ID: "real-subtree-stop", Name: "job_stop", Arguments: args, Type: "function"}, llm.FinishReasonToolCalls)
		if out.IsError {
			toolDone <- fmt.Errorf("registered job_stop: %s", out.Output)
			return
		}
		toolDone <- nil
	}()
	releaseModel()
	select {
	case <-stop.done:
	case <-ctx.Done():
		t.Fatal("real subtree Stop did not settle")
	}
	if err := <-toolDone; err != nil {
		t.Fatal(err)
	}
	if got := len(producerSourceEvents(t, child.jobManager, jobstore.EventJobNotificationConsumed)); got != 0 {
		t.Fatalf("Stop wrote %d shell observation receipts before successful coverage", got)
	}
	// Stop completion releases this subtree fence. No new resume API or inbox
	// append overrides it, and the ordinary common executor owns the successor.
	if err := root.kickDriveTree(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("retained accepted frame did not reach the eligible provider")
	}
	waitForStableSupervisionRun(t, root, child.ID())
	requireOwnedDrainAttentionObservedOnce(t, child, ids[0])
	if got := len(adapter.requestsFor("STOP-CUSTODY-731a")); got != 4 {
		t.Fatalf("post-release requests=%d, want exactly one successor", got)
	}
}

// Regression test for the live attention-wake leak observed in production: a
// live controller (not a restore/bootstrap) with attention armed for a
// delegate that is then deliberately stopped (subtree stop settling
// stopped_by_parent) must discard the bound attention at settlement, exactly
// the way bootstrap discards restored stop-bound attention. Before the fix,
// an attention wake noted after the stop drain's final evidence collection
// but before the stop-completion append survived the completed stop: the
// in-memory wake ID leaked, hasPendingDelegateAttention stayed true forever,
// and the attention drive retried the stop-gated delegate every cycle.
//
// The race is modelled deterministically: the reconcile evidence is the
// pre-arm snapshot (empty attention) while the controller already holds the
// wake ID, which is exactly the window between collectDelegateReconcileEvidence
// and Reconcile's completion append. The transcript open is durable before
// the wake is noted, mirroring the arm path's append-then-note order.
func TestDelegateAttention_LiveStopCompletionPreservesLateBoundWake(t *testing.T) {
	const (
		delegateID     = "dlg_target"
		childSessionID = "child-dlg_target"
		attentionID    = "attention-armed-during-stop-drain"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, delegateID, "")
	writeDelegateAttentionTranscript(t, transcriptPath(c.stateDir, childSessionID), childSessionID, attentionID)

	result, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	// The cancelled run loop settles its generation through the controller's
	// own finish path, committing OutcomeStopped/stopped_by_parent.
	lease := delegateLease{delegateID: delegateID, generation: 1}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}

	// A shell/watch notification arms attention in the gap between the drain's
	// last evidence collection and its stop-completion append.
	c.noteDelegateAttention(delegateID, attentionID)
	evidence := emptyDelegateReconcileEvidence(c)

	// Drive the stop reconcile to completion the way drainStop does, executing
	// any attention cleanup the controller asks for.
	for range 4 {
		plans, err := c.Reconcile(evidence)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		for _, plan := range plans.attention {
			if plan.disposition != delegateAttentionDiscarded {
				t.Fatalf("stop-bound attention disposition = %q, want %q", plan.disposition, delegateAttentionDiscarded)
			}
			if err := c.executeDelegateAttentionCleanup(plan); err != nil {
				t.Fatalf("executeDelegateAttentionCleanup: %v", err)
			}
		}
		if delegateStopDone(c.stop) {
			break
		}
		evidence = emptyDelegateReconcileEvidence(c)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop did not complete")
	}

	aggregate := c.durable[delegateID]
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || aggregate.PendingStopSeq != 0 ||
		aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped ||
		aggregate.LatestOutcome.Reason != "stopped_by_parent" {
		t.Fatalf("settled stop aggregate = %#v", aggregate)
	}
	if got := len(attentionWakeIDsOf(c, delegateID)); got != 1 {
		t.Fatalf("stop-gated delegate retained %d attention wake IDs", got)
	}
	if !c.hasPendingDelegateAttention() {
		t.Fatal("hasPendingDelegateAttention is true for a deliberately stopped delegate")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, childSessionID), childSessionID)
	if err != nil {
		t.Fatalf("read stopped child attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != "" {
		t.Fatalf("stop-bound attention disposition = %q, want %q", got, delegateAttentionDiscarded)
	}
}

// Companion end-to-end check: when the bound attention is already visible to
// the drain's evidence collection, the live stop drain discards it before
// completing the stop.
func TestDelegateAttention_LiveStopSettlementPreservesBoundAttention(t *testing.T) {
	const (
		delegateID     = "dlg_target"
		childSessionID = "child-dlg_target"
		attentionID    = "attention-armed-before-live-stop"
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, delegateID, "")
	writeDelegateAttentionTranscript(t, transcriptPath(c.stateDir, childSessionID), childSessionID, attentionID)
	c.noteDelegateAttention(delegateID, attentionID)
	if !c.hasPendingDelegateAttention() {
		t.Fatal("armed attention is not pending before the stop")
	}

	result, cancelPlan, _, err := c.StopSubtree(rootDelegateActor("root-session"), delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	lease := delegateLease{delegateID: delegateID, generation: 1}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}

	c.mu.Lock()
	stop := c.stop
	c.mu.Unlock()
	if stop == nil {
		t.Fatal("stop state missing after admission")
	}
	if err := c.drainStop(context.Background(), stop, nil); err != nil {
		t.Fatalf("drainStop: %v", err)
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop did not complete after drain")
	}

	aggregate := c.durable[delegateID]
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || aggregate.PendingStopSeq != 0 ||
		aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped ||
		aggregate.LatestOutcome.Reason != "stopped_by_parent" {
		t.Fatalf("settled stop aggregate = %#v", aggregate)
	}

	if got := len(attentionWakeIDsOf(c, delegateID)); got != 1 {
		t.Fatalf("stop-gated delegate retained %d attention wake IDs", got)
	}
	if !c.hasPendingDelegateAttention() {
		t.Fatal("hasPendingDelegateAttention is true for a deliberately stopped delegate")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, childSessionID), childSessionID)
	if err != nil {
		t.Fatalf("read stopped child attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != "" {
		t.Fatalf("live stop attention disposition = %q, want %q", got, delegateAttentionDiscarded)
	}
}

// A stop's discard whose line landed but whose fsync failed leaves a readable
// marker that is not yet durable. The cleanup's retry finds the item already
// resolved; it must renew the durability barrier for that marker before it
// forgets the wake and lets the stop complete, or a crash could lose the
// discard and let the attention restart the delegate after recovery.
func TestDelegateAttention_StopRetryRenewsDurabilityOfAnUnsyncedDiscard(t *testing.T) {
	const (
		delegateID     = "dlg_target"
		childSessionID = "child-dlg_target"
		attentionID    = "attention-discard-unsynced"
		// ambiguousSyncFailures is how many fsyncs the first discard fails.
		ambiguousSyncFailures = 3
		// maxRetryPasses bounds the reconcile passes the stop needs to finish.
		maxRetryPasses = 4
	)
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, delegateID, "")
	writeDelegateAttentionTranscript(t, transcriptPath(c.stateDir, childSessionID), childSessionID, attentionID)
	fs := newAttentionAmbiguousSyncFS()
	c.attentionOpen = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}

	appendDelegateControllerStopRequest(t, c, delegateID)
	events, err := c.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.restorePendingStop(events); err != nil {
		t.Fatal(err)
	}
	result := c.stopResultLocked(c.stop)
	executeDelegateCancelPlan(c.cancelPlanForStopLocked(c.stop))
	lease := delegateLease{delegateID: delegateID, generation: 1}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	c.noteDelegateAttention(delegateID, attentionID)

	// First pass: the discard's line lands, its fsync fails.
	plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.attention) != 1 {
		t.Fatalf("stop attention cleanup plans = %#v, want one discard", plans.attention)
	}
	fs.failNextAmbiguousDurability(ambiguousSyncFailures)
	if err := c.executeDelegateAttentionCleanup(plans.attention[0]); err == nil {
		t.Fatal("discard with a failed fsync unexpectedly succeeded")
	}

	// Retry passes run until the stop completes.
	for range maxRetryPasses {
		plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		for _, plan := range plans.attention {
			if err := c.executeDelegateAttentionCleanup(plan); err != nil {
				t.Fatalf("retry discard %q: %v", plan.attentionID, err)
			}
		}
		if delegateStopDone(c.stop) {
			break
		}
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop did not complete")
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("the stop completed without renewing durability for the unsynced discard")
	}
}
