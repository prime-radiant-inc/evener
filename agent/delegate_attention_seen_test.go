package agent

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

// #3725: attention that reaches a running delegate and is shown to its model
// in a settled request of that same generation is consumed with it. The
// delegate does not start another generation for it, so it never repeats
// its handoff, and its parent's next message still resumes it.
func TestDelegatePresentedAttentionDoesNotEarnSuccessorGeneration(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			sub := root.subagents.get(fixture.childID)
			armStableSupervisionAttention(t, sub, "attention:mid-run", "gate job finished: exit 0")
			return communicateResponse(false, "running gates")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "gate job finished") {
				t.Errorf("second request did not present the mid-run attention")
			}
			return finalResponse("handoff: committed, waiting for review")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "FOLLOW-UP: also check the migration") {
				t.Errorf("third request is not the parent's follow-up")
			}
			return finalResponse("migration checked")
		},
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (a successor generation re-ran the delegate)", got)
	}
	if got := attentionRunStarts(t, fixture); got != 0 {
		t.Fatalf("attention generations = %d, want 0", got)
	}
	if resolution, generation := childAttentionResolution(t, sub, "attention:mid-run"); resolution != delegateAttentionConsumed || generation != 0 {
		t.Fatalf("mid-run attention resolution = %q for generation %d, want consumed by the generation that presented it (0)", resolution, generation)
	}
	// The delegate does not stay marked as needing attention it consumed.
	controller := root.delegateController
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the delegate's needs-attention mark to clear", func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		aggregate := controller.durable[fixture.delegateID]
		return aggregate != nil && !aggregate.NeedsAttention
	})

	// The parent's next message still resumes the same delegate.
	sendAndAcknowledge(t, root, fixture.delegateID, "FOLLOW-UP: also check the migration")
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 3 {
		t.Fatalf("provider requests after the follow-up = %d, want 3", got)
	}
	if got := attentionRunStarts(t, fixture); got != 0 {
		t.Fatalf("attention generations after the follow-up = %d, want 0", got)
	}
}

// Attention appended after the generation's last request was built has not
// been shown to the model, so it still wakes the delegate: one attention
// generation, whose request presents it.
func TestDelegateAttentionAfterLastRequestStillWakesTheDelegate(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			// This request is already built: the attention lands after the
			// generation's last model boundary.
			sub := root.subagents.get(fixture.childID)
			armStableSupervisionAttention(t, sub, "attention:late", "late gate job finished: exit 1")
			return finalResponse("handoff: committed, waiting for review")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "late gate job finished") {
				t.Errorf("the attention generation's request did not present the late attention")
			}
			return finalResponse("the late gate run failed; looking into it")
		},
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (the late attention must start one generation)", got)
	}
	if got := attentionRunStarts(t, fixture); got != 1 {
		t.Fatalf("attention generations = %d, want 1", got)
	}
	if resolution, generation := childAttentionResolution(t, sub, "attention:late"); resolution != delegateAttentionConsumed || generation == 0 {
		t.Fatalf("late attention resolution = %q for generation %d, want consumed by its own attention generation", resolution, generation)
	}
}

// Attention that lands after the round snapshots its history but before the
// delegate's leased request takes its own snapshot is in the request the
// model sees, so the generation consumes it like any other presented item and
// starts no successor for it.
func TestDelegateAttentionLandingBetweenRequestSnapshotsIsConsumed(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm") },
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "review finished: approved") {
				t.Errorf("the send's request did not present the attention that landed between snapshots")
			}
			return finalResponse("merged")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	var landOnce sync.Once
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.afterModelHistorySnapshot = func() {
			landOnce.Do(func() {
				armStableSupervisionAttention(t, sub, "attention:between-snapshots", "review finished: approved")
			})
		}
	})
	sendAndAcknowledge(t, root, fixture.delegateID, "FOLLOW-UP: merge it")
	waitForStableSupervisionRun(t, root, fixture.childID)

	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (an attention generation re-ran the delegate)", got)
	}
	if got := attentionRunStarts(t, fixture); got != 0 {
		t.Fatalf("attention generations = %d, want 0", got)
	}
	if resolution, generation := childAttentionResolution(t, sub, "attention:between-snapshots"); resolution != delegateAttentionConsumed || generation != 0 {
		t.Fatalf("attention resolution = %q for generation %d, want consumed by the send generation that presented it (0)", resolution, generation)
	}
}

// A generation that consumed the attention it presented lowers the
// delegate's needs-attention mark itself when it lets go: no attention
// reservation ran to report the consumption, and the parent may never drive
// the child again to reconcile it. Nothing on this root drives the child.
func TestDelegateConsumingPresentedAttentionClearsNeedsAttentionWithoutAParentDrive(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm") },
		func(llm.Request) llm.Response {
			armStableSupervisionAttention(t, root.subagents.get(fixture.childID), "attention:unserved", "gate job finished: exit 0")
			return communicateResponse(false, "running gates")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "gate job finished") {
				t.Errorf("second request of the turn did not present the attention")
			}
			return finalResponse("gates passed")
		},
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	sendAndAcknowledge(t, root, fixture.delegateID, "run the gates")
	waitForStableSupervisionRun(t, root, fixture.childID)

	if got := supervisionRequestCount(fixture.adapter); got != 3 {
		t.Fatalf("provider requests = %d, want 3", got)
	}
	if resolution, _ := childAttentionResolution(t, sub, "attention:unserved"); resolution != delegateAttentionConsumed {
		t.Fatalf("attention resolution = %q, want consumed", resolution)
	}
	// The finalize tail clears the mark after the run reads as quiesced.
	controller := root.delegateController
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the delegate's needs-attention mark to clear", func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		aggregate := controller.durable[fixture.delegateID]
		return aggregate != nil && !aggregate.NeedsAttention
	})
}

// A generation that consumed attention it presented while later attention is
// still pending must not leave the consumed item among the delegate's wakes:
// a Stop admitted before the successor generation discards every noted wake,
// and discarding an already-consumed item is a conflicting resolution that
// would leave the stop outstanding.
func TestDelegateStopAfterConsumingPresentedAttentionCompletesWithLaterAttentionPending(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm") },
		func(llm.Request) llm.Response {
			armStableSupervisionAttention(t, root.subagents.get(fixture.childID), "attention:presented", "gate job finished: exit 0")
			return communicateResponse(false, "running gates")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "gate job finished") {
				t.Errorf("second request of the turn did not present the attention")
			}
			// Built after this request: still pending when the generation ends.
			armStableSupervisionAttention(t, root.subagents.get(fixture.childID), "attention:late", "review finished: approved")
			return finalResponse("gates passed")
		},
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// Hold the successor attention drive so the Stop lands before it.
	driveReached := make(chan struct{})
	releaseDrive := make(chan struct{})
	var reachOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseDrive) }) })
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionBeforeDriveClaim = func(child *subagent) {
			// Earlier drives (the mid-run one for the presented item) pass.
			if pending, err := child.sess.pendingDelegateAttentionIDs(); err != nil || !slices.Equal(pending, []string{"attention:late"}) {
				return
			}
			reachOnce.Do(func() { close(driveReached) })
			<-releaseDrive
		}
	})
	sendAndAcknowledge(t, root, fixture.delegateID, "run the gates")
	select {
	case <-driveReached:
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	case <-time.After(30 * time.Second):
		t.Fatal("the successor attention drive was not reached")
	}
	if resolution, _ := childAttentionResolution(t, sub, "attention:presented"); resolution != delegateAttentionConsumed {
		t.Fatalf("presented attention resolution = %q, want consumed", resolution)
	}

	controller := root.delegateController
	result, cancelPlan, _, err := controller.StopSubtree(rootDelegateActor(root.delegateRootSessionID), fixture.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	for range 4 {
		plans, err := controller.Reconcile(emptyDelegateReconcileEvidence(controller))
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		for _, plan := range plans.attention {
			if err := controller.executeDelegateAttentionCleanup(plan); err != nil {
				t.Fatalf("discard %q for the stop: %v", plan.attentionID, err)
			}
		}
		if delegateStopDone(controller.stop) {
			break
		}
	}
	select {
	case <-result.done:
	default:
		t.Fatal("stop did not complete")
	}
	if resolution, _ := childAttentionResolution(t, sub, "attention:late"); resolution != delegateAttentionDiscarded {
		t.Fatalf("late attention resolution = %q, want discarded by the stop", resolution)
	}
	releaseOnce.Do(func() { close(releaseDrive) })
}

// A needs-attention clear that fails once after a generation consumed the
// attention it presented is retried without parent activity: the delegate's
// noted wakes stay the retry obligation until a clear lands, and the root's
// attention drive reconciles them against the transcript.
func TestDelegateNeedsAttentionClearRecoversFromATransientFailure(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm") },
		func(llm.Request) llm.Response {
			armStableSupervisionAttention(t, root.subagents.get(fixture.childID), "attention:clear-retry", "gate job finished: exit 0")
			return communicateResponse(false, "running gates")
		},
		func(llm.Request) llm.Response { return finalResponse("gates passed") },
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	// Fail the first clear's fold read, whichever path makes it.
	compute := readExistingDelegateAttentionFoldCompute
	var failedOnce atomic.Bool
	readExistingDelegateAttentionFoldCompute = func(path, sessionID string) (delegateAttentionFold, int64, error) {
		if failedOnce.CompareAndSwap(false, true) {
			return delegateAttentionFold{}, 0, errors.New("transient fold read failure")
		}
		return compute(path, sessionID)
	}
	t.Cleanup(func() { readExistingDelegateAttentionFoldCompute = compute })

	sendAndAcknowledge(t, root, fixture.delegateID, "run the gates")
	waitForStableSupervisionRun(t, root, fixture.childID)
	controller := root.delegateController
	// TRIPWIRE: the root's paced attention retry is the only actor; only a
	// stranded flag reaches this bound.
	waitForCondition(t, 30*time.Second, "the delegate's needs-attention mark to clear after a failed clear", func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		aggregate := controller.durable[fixture.delegateID]
		return aggregate != nil && !aggregate.NeedsAttention
	})
	if got := supervisionRequestCount(fixture.adapter); got != 3 {
		t.Fatalf("provider requests = %d, want 3: the retry reconciles without another generation", got)
	}
}

// attentionRunStarts counts the fixture delegate's generations its journal
// records as started by attention.
func attentionRunStarts(t *testing.T, fixture coldStableDelegateFixture) int {
	t.Helper()
	events, err := delegatestore.ReadEvents(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("read delegate journal: %v", err)
	}
	starts := 0
	for _, event := range events {
		if event.Kind == delegatestore.EventDelegateRunStarted && event.DelegateID == fixture.delegateID &&
			event.RunStarted.Trigger == delegatestore.TriggerAttention {
			starts++
		}
	}
	return starts
}

// childAttentionResolution reads how the child transcript resolved
// attentionID: its disposition, and the generation it was consumed for, which
// is zero when the generation that presented it consumed it.
func childAttentionResolution(t *testing.T, sub *subagent, attentionID string) (delegateAttentionResolution, uint64) {
	t.Helper()
	// A fresh read: the session's fold cursor belongs to attentionMu, which
	// the running drive holds while it reads.
	fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), sub.sess.id)
	if err != nil {
		t.Fatalf("fold child attention: %v", err)
	}
	return fold.resolutions[attentionID], fold.resumeGenerations[attentionID]
}

// Attention already pending when a leased generation starts is consumed by
// that generation too, once its request shows the model the item. Here the
// item lands on the idle delegate and a delegate_send's reservation wins the
// race against the attention drive: the item is appended without arming the
// drive, so the send's generation is the first to run, and its first request
// presents it.
func TestDelegateAttentionPendingBeforeASendIsConsumedByThatSend(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm") },
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "review finished: approved") {
				t.Errorf("the send's request did not present the pending attention")
			}
			if !requestContainsText(req, "FOLLOW-UP: merge it") {
				t.Errorf("the send's request is not the parent's follow-up")
			}
			return finalResponse("merged")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	if appended, err := sub.sess.appendDelegateNotificationDurably("attention:before-send", "review finished: approved"); err != nil || !appended {
		t.Fatalf("append pending attention = appended:%t err:%v", appended, err)
	}
	sendAndAcknowledge(t, root, fixture.delegateID, "FOLLOW-UP: merge it")
	waitForStableSupervisionRun(t, root, fixture.childID)

	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (an attention generation re-ran the delegate)", got)
	}
	if got := attentionRunStarts(t, fixture); got != 0 {
		t.Fatalf("attention generations = %d, want 0", got)
	}
	if resolution, generation := childAttentionResolution(t, sub, "attention:before-send"); resolution != delegateAttentionConsumed || generation != 0 {
		t.Fatalf("pending attention resolution = %q for generation %d, want consumed by the send generation that presented it (0)", resolution, generation)
	}
}

// A delegate turn that presented attention in a settled request and then
// failed consumes nothing: the item stays pending, and the delegate does not
// take on the root's paused-updates rail (no "Background updates paused"
// warning, which belongs to the root). A permanent provider failure gates the
// delegate's attention drive until its parent re-engages, so recovery comes
// from the parent's next message: that generation presents the item and
// consumes it, and no attention generation runs.
func TestDelegateFailedTurnLeavesPresentedAttentionPending(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm") },
		func(llm.Request) llm.Response {
			sub := root.subagents.get(fixture.childID)
			armStableSupervisionAttention(t, sub, "attention:failed-turn", "gate job finished: exit 0")
			return communicateResponse(false, "running gates")
		},
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "gate job finished") {
				t.Errorf("the settled request did not present the attention")
			}
			return communicateResponse(false, "gates look good, committing")
		},
		// failingRequestAdapter fails the next request with a 401 here.
		func(req llm.Request) llm.Response {
			if !requestContainsText(req, "gate job finished") || !requestContainsText(req, "FOLLOW-UP: try again") {
				t.Errorf("the parent's follow-up request did not present the pending attention")
			}
			return finalResponse("gates passed; committed")
		},
	}
	// Requests, zero-based: 0 warms the delegate; 1 and 2 are the gate turn's
	// settled rounds, 2 presenting the attention; 3 is that turn's next
	// round, which fails.
	fixture.client.Register(&failingRequestAdapter{
		fakeAdapter: fixture.adapter,
		failAt:      3,
		err:         llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil),
	})
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	childPausedWarnings := pausedWarnings(t, sub.sess)

	sendAndAcknowledge(t, root, fixture.delegateID, "run the gates and commit")
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the failed generation to settle with its drive gated", func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return sub.fatalRunGated && !sub.running && !sub.finalizing && !sub.driving
	})
	if resolution, _ := childAttentionResolution(t, sub, "attention:failed-turn"); resolution != "" {
		t.Fatalf("attention resolution after the failed turn = %q, want still pending", resolution)
	}

	sendAndAcknowledge(t, root, fixture.delegateID, "FOLLOW-UP: try again")
	waitForStableSupervisionRun(t, root, fixture.childID)
	if got := supervisionRequestCount(fixture.adapter); got != 4 {
		t.Fatalf("scripted provider requests = %d, want 4", got)
	}
	if got := attentionRunStarts(t, fixture); got != 0 {
		t.Fatalf("attention generations = %d, want 0", got)
	}
	if resolution, generation := childAttentionResolution(t, sub, "attention:failed-turn"); resolution != delegateAttentionConsumed || generation != 0 {
		t.Fatalf("attention resolution = %q for generation %d, want consumed by the follow-up generation that presented it (0)", resolution, generation)
	}
	if warnings := childPausedWarnings(); len(warnings) != 0 {
		t.Fatalf("child emitted paused-updates warnings %+v, want none: that rail is the root's", warnings)
	}
}

// failingRequestAdapter serves its fakeAdapter's script but fails the request
// at index failAt (zero-based, counting every request) with err.
type failingRequestAdapter struct {
	*fakeAdapter
	failAt int
	err    error

	callsMu sync.Mutex
	calls   int
}

func (a *failingRequestAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.callsMu.Lock()
	call := a.calls
	a.calls++
	a.callsMu.Unlock()
	if call == a.failAt {
		return llm.Response{}, a.err
	}
	return a.fakeAdapter.Complete(ctx, req)
}
