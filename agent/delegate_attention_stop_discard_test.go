package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestSubtreeStopPreservesNotificationCustody(t *testing.T) {
	t.Parallel()
	t.Run("registered tools observe after release", testSubtreeStopCustodyObservedAfterRelease)
	t.Run("permanent ancestor reaches root", testStopPermanentAncestorReachesRoot)
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
			producedDelivery := c.durable["dlg_child"].PendingDeliveries[0]
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
			if !reflect.DeepEqual(fold.pendingIDs(), []string{"accepted-stop-731a"}) || len(fold.resolutions) != 0 || len(fold.observed) != 0 {
				t.Fatalf("Stop discarded accepted input: pending=%v resolutions=%v", fold.pendingIDs(), fold.resolutions)
			}
			if consumed != 0 {
				t.Fatalf("Stop wrote %d unearned shell observation receipts", consumed)
			}
			if got := c.durable["dlg_child"].PendingDeliveries; !reflect.DeepEqual(got, []delegatestore.PendingDelivery{producedDelivery}) {
				t.Fatal("Stop erased produced-but-undelivered child source")
			}
			// Completion releases only this controller's cancellation fence, not
			// a human execution hold. Transport may acquire custody while held.
			if got := c.ReplayDeliveries(); len(got) != 1 || got[0].deliveryID != producedDelivery.DeliveryID {
				t.Fatalf("retained delivery not eligible after Stop completion: %+v", got)
			}
		})
	}
}

func testStopPermanentAncestorReachesRoot(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root, fixture := fenced.root, fenced.fixture
	sourcePath := transcriptPath(fixture.stateDir, fenced.grandchildSessionID)
	before, err := readDelegateAttentionFold(sourcePath, fenced.grandchildSessionID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.adapter.mu.Lock()
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		found := 0
		for _, msg := range req.Messages {
			if reflect.DeepEqual(msg, before.content[fenced.attentionID]) {
				found++
			}
		}
		if found != 1 {
			t.Errorf("root request carried original message %d times, want once", found)
		}
		return finalResponse("permanent-handoff-result-42")
	}}
	fixture.adapter.mu.Unlock()
	result, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), fixture.delegateID)
	if err != nil {
		t.Fatal(err)
	}
	executeDelegateCancelPlan(cancelPlan)
	if err := root.delegateController.drainStop(context.Background(), root.delegateController.stopForResult(result), nil); err != nil {
		t.Fatal(err)
	}
	fenced.closeParent(t)
	// The ordinary wake owner must hand accepted content to the reachable root,
	// not revive the permanently closed parent or run its fenced descendant.
	root.delegateController.recheckNotificationReceivers()
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireOwnedDrainAttentionObservedOnce(t, root, fenced.attentionID)
	source, err := readDelegateAttentionFold(sourcePath, fenced.grandchildSessionID)
	if err != nil || source.resolutions[fenced.attentionID] != delegateAttentionDiscarded || len(source.observed) != 0 {
		t.Fatalf("unreachable receiver has no durable transfer disposition: %+v err=%v", source, err)
	}
	if got := len(fixture.adapter.Requests()); got != 1 {
		t.Fatalf("permanent handoff requests=%d, want one reachable root request", got)
	}
	for _, id := range []string{fixture.delegateID, fenced.grandchildDelegateID} {
		if a := delegateAggregateSnapshot(t, root.delegateController, id); a.CurrentRunOpen || (id == fixture.delegateID && a.Resumable) {
			t.Fatalf("permanent handoff revived %s: %+v", id, a)
		}
	}
}

func testSubtreeStopCustodyObservedAfterRelease(t *testing.T) {
	adapter := newMessagingAdapter("STOP-CUSTODY-731a")
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	entered, modelGate, observed := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	var contentMu sync.Mutex
	var releasedContent map[string]llm.Message
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
		contentMu.Lock()
		defer contentMu.Unlock()
		if len(releasedContent) != 2 {
			t.Errorf("released request did not wait for the two real producer records: %v", releasedContent)
		}
		for id, message := range releasedContent {
			found := 0
			for _, msg := range req.Messages {
				if reflect.DeepEqual(msg, message) {
					found++
				}
			}
			if found != 1 {
				t.Errorf("released request carried %s content %d times, want once", id, found)
			}
		}
		observed <- struct{}{}
		return finalResponse("observed-after-stop-release-731a")
	})
	root := newMessagingRoot(t, adapter, nil)
	ctx, cancel := context.WithCancel(t.Context())
	var interruptExited, toolExited chan struct{}
	defer func() {
		releaseModel()
		cancel()
		if toolExited != nil {
			waitForTestSignal(t, toolExited, "registered Stop tool worker cleanup")
		}
		if interruptExited != nil {
			waitForTestSignal(t, interruptExited, "human Stop worker cleanup")
		}
	}()
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
	produced := producerSourceEvents(t, child.jobManager, jobstore.EventWatchSendPending)
	if len(produced) != 1 || ids[0] != "watch:"+produced[0].WatchSend.DeliveryID+":1" {
		t.Fatalf("accepted frame lost exact produced identity: ids=%v source=%+v", ids, produced)
	}
	admitted := make(chan delegateStopResult, 1)
	interruptDone := make(chan error, 1)
	interruptExited = make(chan struct{})
	admissionOutcome := make(chan struct{})
	signalAdmissionOutcome := sync.OnceFunc(func() { close(admissionOutcome) })
	go func() {
		defer close(interruptExited)
		defer signalAdmissionOutcome()
		var stopErr error
		_, interruptErr := child.InterruptClientMutation(ctx, appwire.TurnInterruptParams{ClientMutationID: "stop-custody-hold-731a"}, func() {
			result, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), child.owningDelegateID)
			if err != nil {
				stopErr = err
				return
			}
			executeDelegateCancelPlan(cancelPlan)
			admitted <- result
			signalAdmissionOutcome()
			select {
			case <-result.done:
			case <-ctx.Done():
				stopErr = ctx.Err()
			}
		})
		if interruptErr != nil {
			stopErr = interruptErr
		}
		interruptDone <- stopErr
	}()
	waitForTestSignal(t, admissionOutcome, "human Stop admission or worker result")
	var result delegateStopResult
	select {
	case result = <-admitted:
	case err := <-interruptDone:
		t.Fatalf("human Stop returned before subtree admission: %v", err)
	default:
		t.Fatal("human Stop signaled without admission or a worker result")
	}
	if !child.notificationStopHeld() {
		t.Fatal("human Stop did not establish its execution hold")
	}
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
	originalContent := fold.content[ids[0]]
	// The registered tool joins the same admitted Stop, not another policy or
	// synthetic completion. Its fence cannot settle until the real run unwinds.
	toolDone := make(chan error, 1)
	toolExited = make(chan struct{})
	go func() {
		defer close(toolExited)
		args, _ := json.Marshal(map[string]any{"target": child.owningDelegateID, "max_wait_ms": 5000})
		out := root.execTool(ctx, llm.ToolCallData{ID: "real-subtree-stop", Name: "job_stop", Arguments: args, Type: "function"}, llm.FinishReasonToolCalls)
		if out.IsError {
			toolDone <- fmt.Errorf("registered job_stop: %s", out.Output)
			return
		}
		toolDone <- nil
	}()
	releaseModel()
	waitForTestSignal(t, stop.done, "real subtree Stop settlement")
	waitForTestSignal(t, toolExited, "registered Stop tool worker return")
	if err := <-toolDone; err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, interruptExited, "human Stop worker return")
	if err := <-interruptDone; err != nil {
		t.Fatalf("human Stop cancellation and wait: %v", err)
	}
	if got := len(producerSourceEvents(t, child.jobManager, jobstore.EventJobNotificationConsumed)); got != 0 {
		t.Fatalf("Stop wrote %d shell observation receipts before successful coverage", got)
	}
	// Cancelling the real owned FIFO shell produces one more source obligation.
	// It must keep the journal's exact job/terminal generation at the receiver.
	shellProduced := producerSourceEvents(t, child.jobManager, jobstore.EventJobNotificationPending)
	if len(shellProduced) != 1 {
		t.Fatalf("cancellation produced shell obligations=%+v, want one", shellProduced)
	}
	shellID := "shell:" + shellProduced[0].JobID + ":" + shellProduced[0].TerminalGen
	ids = append(ids, shellID)
	// Finishing cancellation releases the controller fence, not the separately
	// persisted human hold. Even an explicit common wake cannot run through it.
	if err := root.kickDriveTree(ctx); err != nil {
		t.Fatal(err)
	}
	if !child.notificationStopHeld() || len(adapter.requestsFor("STOP-CUSTODY-731a")) != 3 {
		t.Fatal("Stop completion released the human hold or made a model request")
	}
	assertNotificationPending(t, child, ids)
	fold, err = readDelegateAttentionFold(child.TranscriptPath(), child.ID())
	if err != nil || len(fold.resolutions) != 0 || len(fold.observed) != 0 {
		t.Fatalf("held Stop fabricated a receiver receipt: %+v err=%v", fold, err)
	}
	if !reflect.DeepEqual(fold.content[ids[0]], originalContent) {
		t.Fatal("Stop replaced the originally accepted content")
	}
	contentMu.Lock()
	releasedContent = map[string]llm.Message{ids[0]: originalContent, shellID: fold.content[shellID]}
	contentMu.Unlock()
	// This user mutation is the explicit release itself, not an unrelated event
	// needed to make retained input discoverable. It releases both durable rails.
	if _, err := child.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "stop-custody-release-731a", Input: []appwire.InputItem{{Type: "text", Text: "continue"}}}); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, observed, "retained accepted frame at eligible provider")
	waitForStableSupervisionRun(t, root, child.ID())
	for _, id := range ids {
		requireOwnedDrainAttentionObservedOnce(t, child, id)
	}
	if got := len(adapter.requestsFor("STOP-CUSTODY-731a")); got != 4 {
		t.Fatalf("post-release requests=%d, want exactly one successor", got)
	}
}

// A newly admitted subtree Stop preserves bound attention even when its wake
// arrives after the live drain's final evidence collection. Stop settlement
// does not observe or discard the notification. Historical requests without
// the preservation field retain their separate discard semantics.
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
		t.Fatalf("stop-gated delegate retained %d attention wake IDs, want 1", got)
	}
	if !c.hasPendingDelegateAttention() {
		t.Fatal("preserved attention is no longer pending after Stop settlement")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, childSessionID), childSessionID)
	if err != nil {
		t.Fatalf("read stopped child attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != "" {
		t.Fatalf("stop-bound attention disposition = %q, want unresolved", got)
	}
}

// Companion end-to-end check: when the bound attention is already visible to
// the drain's evidence collection, the newly admitted live Stop still preserves
// it unresolved through settlement, just like attention armed in the late gap.
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
		t.Fatalf("stop-gated delegate retained %d attention wake IDs, want 1", got)
	}
	if !c.hasPendingDelegateAttention() {
		t.Fatal("preserved attention is no longer pending after Stop settlement")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, childSessionID), childSessionID)
	if err != nil {
		t.Fatalf("read stopped child attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != "" {
		t.Fatalf("live stop attention disposition = %q, want unresolved", got)
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
