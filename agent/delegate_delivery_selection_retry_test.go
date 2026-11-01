package agent

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestColdDelegateSourceSelectionReadFailureRetries(t *testing.T) {
	t.Parallel()
	t.Run("new_plan_EIO", func(t *testing.T) { testColdDelegateSourceSelection(t, false) })
	t.Run("retry_plan_busy_control", func(t *testing.T) { testColdDelegateSourceSelection(t, true) })
}

func testColdDelegateSourceSelection(t *testing.T, retryBusyControl bool) {
	adapter := newMessagingAdapter("SELECTION-PARENT", "SELECTION-CHILD")
	returns := newActivationReturns()
	initialBoundary, terminal := newWaitBoundary(), newWaitBoundary()
	defer initialBoundary.release()
	defer terminal.release()
	childEntered, childGate := make(chan struct{}), make(chan struct{})
	releaseChild := sync.OnceFunc(func() { close(childGate) })
	defer releaseChild()
	var childID string
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("selection-parent", "SELECTION-PARENT") }, func(llm.Request) llm.Response { return agenttest.FinalResponse("root") })
	adapter.script("SELECTION-PARENT", func(llm.Request) llm.Response { return delegateToolResponse("selection-child", "SELECTION-CHILD") }, func(req llm.Request) llm.Response {
		childID = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
		initialBoundary.hold(t)
		return waitResponseFor("selection-wait", childID)
	})
	adapter.script("SELECTION-CHILD", func(llm.Request) llm.Response {
		close(childEntered)
		<-childGate
		return agenttest.FinalResponse("original child")
	})
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "start", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	previous := initialBoundary.take(t, returns, owner.ID())
	initialBoundary.release()
	waitYielded(t, root, previous, "selection-wait", childID)
	waitForTestSignal(t, childEntered, "original child request")
	copyDir := copyWaitingState(t, root.stateDir)
	presented := make(chan llm.Request, 1)
	response := agenttest.FinalResponse("selection-parent-response")
	adapter.script("SELECTION-PARENT", func(req llm.Request) llm.Response { presented <- req; terminal.hold(t); return response })
	restoredReturns := newActivationReturns()
	clock := agenttest.NewFakeClock()
	meta, err := schema.LoadSessionMeta(copyDir, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	var healthyReads, faults atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	opener := func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
		if id == owner.ID() {
			// Fault only the real selector. Bootstrap reconciliation and ancestor
			// validation use this same opener, so ordinal read counts are unsafe.
			// Stack inspection is an injection boundary, not a result oracle.
			var pcs [64]uintptr
			frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
			selection := false
			for {
				frame, more := frames.Next()
				if frame.Function == "primeradiant.com/evener/agent.(*delegateTreeController).deliveryReceiverWithEvidenceLocked" {
					selection = true
					break
				}
				if !more {
					break
				}
			}
			if selection && fail.Load() {
				faults.Add(1)
				return nil, nil, &os.PathError{Op: "open", Path: path, Err: syscall.EIO}
			}
			if !selection {
				healthyReads.Add(1)
			}
		}
		return transcript.OpenWriterForSession(path, id)
	}
	restored, err := RestoreSessionFromMetaWithConfig(root.client, root.profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{StateDir: copyDir, ForceRealIO: true, clock: clock, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true, subagentActivationReturn: restoredReturns.capture, delegateAttentionOpenWriter: opener}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restoredReturns.close(t, restored) })
	restored.SetNotifyFunc(func() {})
	c := restored.delegateController
	assertCold := func() {
		t.Helper()
		c.mu.Lock()
		live := c.live[owner.owningDelegateID]
		cold := live == nil || (live.runtime == nil && live.binding == nil)
		c.mu.Unlock()
		if !cold || restored.subagentForChild(owner.ID()) != nil {
			t.Fatal("parent unexpectedly warm before selection")
		}
	}
	assertCold()
	run := delegateAggregateSnapshot(t, c, owner.owningDelegateID)
	if run.Phase != delegatestore.PhaseRunning || !run.CurrentRunOpen || run.Generation != previous.lease.generation {
		t.Fatalf("not a cold open wait: %+v", run)
	}
	source := delegateAggregateSnapshot(t, c, childID)
	if len(source.PendingDeliveries) != 1 || source.PendingDeliveries[0].OwnerDelegateID != owner.owningDelegateID {
		t.Fatalf("no reconciled child source addressed to waiting parent: %+v", source)
	}
	deliveryID := source.PendingDeliveries[0].DeliveryID
	attentionID := delegateAttentionID(deliveryID)
	fail.Store(false)
	c.mu.Lock()
	safe, err := c.readSafeDelegateWaitLocked(c.durable[owner.owningDelegateID])
	c.mu.Unlock()
	if err != nil || !safe || healthyReads.Load() == 0 {
		t.Fatalf("healthy evidence safe=%t reads=%d err=%v", safe, healthyReads.Load(), err)
	}
	if retryBusyControl {
		// Exercise the controller admission/custody/ack API, not the cold
		// executor's receiverCommitted optimization. A real accepted canonical
		// record must precede CompleteDelivery(true), even in this control.
		c.mu.Lock()
		plan := c.headDeliveryPlanLocked(childID, deliveryID)
		c.mu.Unlock()
		if plan == nil {
			t.Fatal("healthy control setup omitted plan")
		}
		token, admitted, err := c.BeginDelivery(*plan)
		if err != nil || !admitted {
			t.Fatalf("control admission=%t err=%v", admitted, err)
		}
		defer func() {
			if _, err := c.CompleteDelivery(token, false); err != nil {
				t.Errorf("release control admission: %v", err)
			}
		}()
		content, err := delegateNotificationContent(*plan)
		if err != nil {
			t.Fatal(err)
		}
		if accepted, err := plan.receiver.appendDelegateNotificationDurably(attentionID, content); err != nil || !accepted {
			t.Fatalf("real canonical custody accepted=%t err=%v", accepted, err)
		}
		if err := c.store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.CompleteDelivery(token, true); err == nil {
			t.Fatal("closed source journal unexpectedly acknowledged delivery")
		}
		reopened, err := delegatestore.Open(delegateResourceStorePath(copyDir, restored.ID()))
		if err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		c.store = reopened
		receipt := c.deliveryReceiptLocked(deliveryID)
		retained := receipt != nil && receipt.retryable && !receipt.receiverCommitted
		_, evidenceErr := c.deliveryReceiverWithEvidenceLocked(owner.owningDelegateID)
		retryPlan := c.retryDeliveryPlanLocked(receipt)
		c.mu.Unlock()
		if !retained || !errors.Is(evidenceErr, errDelegateTargetBusy) || retryPlan != nil {
			t.Fatalf("retry custody fence retained=%t evidence=%v plan=%+v", retained, evidenceErr, retryPlan)
		}
		faults.Store(0)
		fail.Store(true)
		if err := restored.replayDelegateDeliveries(); err != nil {
			t.Fatalf("legitimate custody exclusion became an execution error: %v", err)
		}
		restored.delegateDeliveryMu.Lock()
		armed := restored.delegateDeliveryRetry.active
		restored.delegateDeliveryMu.Unlock()
		assertCold()
		if faults.Load() != 0 || armed || len(adapter.requestsFor("SELECTION-PARENT")) != 2 || !c.deliveryPending(childID, deliveryID) {
			t.Fatalf("busy retry polled or lost debt: EIO faults=%d armed=%t", faults.Load(), armed)
		}
		t.Log("retry-plan custody fence returned busy before opener, source debt retained without polling")
		return
	}
	const healthyID = "dlg_selection_healthy"
	healthy := delegateControllerCreatedEvent(healthyID, "")
	healthy.Created.Descriptor.OwnerSessionID = restored.ID()
	c.mu.Lock()
	_, err = c.appendLocked(healthy)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	seedDelegateControllerDelivery(t, c, healthyID)
	healthyDeliveryID := delegateDeliveryID(healthyID, 1)
	faults.Store(0)
	fail.Store(true)
	replayErr := restored.replayDelegateDeliveries()
	if faults.Load() != 1 {
		t.Fatalf("selection faults=%d want1", faults.Load())
	}
	assertCold()
	if len(adapter.requestsFor("SELECTION-PARENT")) != 2 {
		t.Fatal("fault admitted a parent model request")
	}
	source = delegateAggregateSnapshot(t, c, childID)
	if len(source.PendingDeliveries) != 1 || source.PendingDeliveries[0].DeliveryID != deliveryID {
		t.Fatalf("source prematurely acknowledged: %+v", source)
	}
	if len(delegateAggregateSnapshot(t, c, healthyID).PendingDeliveries) != 0 {
		t.Fatal("uncertain destination prevented healthy custody")
	}
	if _, ok := rootAttentionFold(t, restored).content[delegateAttentionID(healthyDeliveryID)]; !ok {
		t.Fatal("healthy destination has no canonical custody")
	}
	restored.delegateDeliveryMu.Lock()
	armed := restored.delegateDeliveryRetry.active
	restored.delegateDeliveryMu.Unlock()
	t.Logf("selection EIO fired once, parent cold with source %s pending, healthy destination committed, root retry armed=%t", deliveryID, armed)
	if !armed {
		t.Fatal("cold selection fault stranded source without root retry")
	}
	if !errors.Is(replayErr, syscall.EIO) {
		t.Fatalf("selection error was lost: %v", replayErr)
	}
	fail.Store(false)
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	resumed := terminal.take(t, restoredReturns, owner.ID())
	req := <-presented
	if resumed.sessionID != previous.sessionID || resumed.lease != previous.lease {
		t.Fatalf("cold retry changed generation: %+v -> %+v", previous, resumed)
	}
	if ids := incidentPresentedIDs(t, copyDir, owner.ID(), req, attentionID); !slices.Equal(ids, []string{attentionID}) {
		t.Fatalf("retry did not present same source ID: %v", ids)
	}
	terminal.release()
	incidentSettled(t, restored, owner.ID(), resumed)
	// A yielded wait resumes the original generation. No new TriggerAttention
	// RunStarted is expected, unlike the ordinary idle-delegate receipt helper.
	starts := 0
	for _, ev := range publicationEvents(t, restored) {
		if ev.DelegateID == owner.owningDelegateID && ev.RunStarted != nil {
			starts++
			if ev.RunStarted.Generation != previous.lease.generation || ev.RunStarted.Trigger != delegatestore.TriggerOwnerInput {
				t.Fatalf("wait resume invented another generation start: %+v", ev.RunStarted)
			}
		}
	}
	if starts != 1 {
		t.Fatalf("wait resume starts=%d want1 original start", starts)
	}
	receipts, resolutions := 0, 0
	path := transcriptPath(copyDir, owner.ID())
	for _, entry := range readAttentionTranscriptEntries(t, path) {
		if slices.Contains(entry.Turn.ObservedNotificationIDs, attentionID) {
			if entry.Turn.Kind != schema.TurnAssistant || entry.Turn.RoundID == "" || !reflect.DeepEqual(entry.Turn.Message, response.Message) || !slices.Equal(entry.Turn.ObservedNotificationIDs, []string{attentionID}) {
				t.Fatalf("wrong successful response for %s: %#v", attentionID, entry.Turn)
			}
			receipts++
		}
		if resolution := entry.Turn.AttentionResolution; resolution != nil && resolution.AttentionID == attentionID {
			resolutions++
			if resolution.Disposition != string(delegateAttentionConsumed) {
				t.Fatalf("wrong derived resolution: %+v", resolution)
			}
		}
	}
	fold, err := readDelegateAttentionFold(path, owner.ID())
	if err != nil || receipts != 1 || resolutions != 1 || slices.Contains(fold.pendingIDs(), attentionID) || fold.resolutions[attentionID] != delegateAttentionConsumed {
		t.Fatalf("same-generation receipt unresolved: receipts=%d resolutions=%d pending=%v err=%v", receipts, resolutions, fold.pendingIDs(), err)
	}
	if len(delegateAggregateSnapshot(t, c, childID).PendingDeliveries) != 0 || faults.Load() != 1 {
		t.Fatal("source retry did not settle exactly the faulted obligation")
	}
}
