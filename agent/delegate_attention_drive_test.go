package agent

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

// Two owner drives may select the same transcript attention before either has
// claimed the child. The delayed drive must revalidate after taking the claim:
// by then the other drive may have consumed and completed that attention run.
// Reserving the consumed ID again conflicts with its generation-bound marker
// and leaves an uncommittable reservation blocking all future owner work.
func TestDelegateAttentionDriveStaleSelectionPreservesNextOwnerRun(t *testing.T) {
	t.Parallel()
	fixture := newColdDelegateFixture(t, "")
	var root *Session
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			if err := root.subagents.get(fixture.childID).sess.Enqueue(context.Background(), "queued follow-up work"); err != nil {
				t.Errorf("queue attention follow-up: %v", err)
			}
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		bare, bare, bare, bare,
		func(llm.Request) llm.Response { return agenttest.FinalResponse("follow-up report") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("next owner report") },
	})
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)

	selected, resume := make(chan struct{}), make(chan struct{})
	var held atomic.Bool
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	t.Cleanup(release)
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionBeforeDriveClaim = func(*subagent) {
			if held.CompareAndSwap(false, true) {
				close(selected)
				<-resume
			}
		}
	})
	const attentionID = "attention:stale-selection"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "inspect before follow-up"); err != nil || !appended {
		t.Fatalf("append attention = appended:%t err:%v", appended, err)
	}
	if _, err := root.delegateController.openDelegateAttention(fixture.delegateID, attentionID); err != nil {
		t.Fatalf("open attention: %v", err)
	}
	// Pin the first drive after its real transcript read. The second drive
	// then owns the attention's real generation and scripted provider turns.
	delayedDone := make(chan struct{})
	go func() {
		defer close(delayedDone)
		root.driveDelegateAttention(sub)
	}()
	select {
	case <-selected:
	case <-time.After(30 * time.Second): // TRIPWIRE: await the selection boundary.
		t.Fatal("attention drive did not select the pending ID")
	}
	if !root.driveDelegateAttention(sub) {
		t.Fatal("second drive did not accept the pending attention")
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	if finished.Generation != 2 || finished.Disposition != delegatestore.DispositionReported || finished.DeliveryID == "" {
		t.Fatalf("attention follow-up finish = %#v, want generation 2 reported delivery", finished)
	}

	release()
	select {
	case <-delayedDone:
	case <-time.After(30 * time.Second): // TRIPWIRE: await the delayed drive's return.
		t.Fatal("delayed attention drive did not return")
	}
	if got := delegateReservationCount(root.delegateController, fixture.delegateID); got != 0 {
		t.Fatalf("completed attention left %d reservations blocking future work", got)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 7 {
		t.Fatalf("provider requests = %d, want warm, attention, four follow-up attempts, and recovery report", got)
	}
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "next owner work", 60_000)
	abortUnpersistedDelegateOutcome(t, outcome)
	if outcome.result.Err != nil {
		t.Fatalf("next owner run after consumed attention: %v", outcome.result.Err)
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	finished = latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	if finished.Generation != 3 || finished.Disposition != delegatestore.DispositionReported {
		t.Fatalf("next owner finish = %#v, want generation 3 reported", finished)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 8 {
		t.Fatalf("provider requests = %d, want one additional owner report", got)
	}
}

// delegateReservationCount reports the attention/turn start reservations the
// controller holds for delegateID. Nothing may outlive a failed attempt: a
// reservation left behind blocks every later drive and owner send.
func delegateReservationCount(c *delegateTreeController, delegateID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, record := range c.reservations {
		if record != nil && record.delegateID == delegateID {
			count++
		}
	}
	return count
}

// A commit the journal refuses must not consume the attention or leave a
// reservation behind (#5681). The row stays pending in the transcript, and the
// next drive over a healthy journal starts a clean generation. That generation
// consumes the report once, when its turn presents it, and no later drive
// fires it again.
func TestDelegateAttentionDriveFailedCommitLeavesRowPendingAndRetries(t *testing.T) {
	t.Parallel()
	fixture := newColdDelegateFixture(t, "")
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("nothing to do")} },
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	const attentionID = "attention:accepted-retry"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "inspect completed work"); err != nil || !appended {
		t.Fatalf("append attention = appended:%t err:%v", appended, err)
	}
	controller := root.delegateController
	if _, err := controller.openDelegateAttention(fixture.delegateID, attentionID); err != nil {
		t.Fatalf("open attention: %v", err)
	}
	// Close the real journal to refuse the commit. No provider turn has started.
	if err := controller.store.Close(); err != nil {
		t.Fatalf("close delegate journal: %v", err)
	}
	if !root.driveDelegateAttention(sub) {
		t.Fatal("attention drive did not attempt the closed journal")
	}
	fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), fixture.childID)
	if err != nil {
		t.Fatal(err)
	}
	if pending := fold.pendingIDs(); !slices.Equal(pending, []string{attentionID}) {
		t.Fatalf("failed commit left pending attention = %v, want the row still pending", pending)
	}
	if len(fold.resolutions) != 0 || len(fold.resumeGenerations) != 0 {
		t.Fatalf("failed commit wrote attention markers: resolutions=%v generations=%v", fold.resolutions, fold.resumeGenerations)
	}
	if got := delegateReservationCount(controller, fixture.delegateID); got != 0 {
		t.Fatalf("failed commit left %d reservations, want none", got)
	}
	if aggregate := controller.durable[fixture.delegateID]; aggregate.Generation != 1 || aggregate.CurrentRunOpen || !aggregate.NeedsAttention {
		t.Fatalf("failed commit published aggregate: %#v, want the warm generation still settled and attention owed", aggregate)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 1 {
		t.Fatalf("provider requests before commit retry = %d, want warm only", got)
	}
	reopened, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("reopen delegate journal: %v", err)
	}
	controller.mu.Lock()
	controller.store = reopened
	controller.mu.Unlock()
	if !root.driveDelegateAttention(sub) {
		t.Fatal("attention drive did not retry the pending row")
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	finished := latestDelegateControllerRunFinished(t, controller, fixture.delegateID)
	if finished.Generation != 2 || finished.Disposition != delegatestore.DispositionCompletedNoAction || finished.DeliveryID != "" {
		t.Fatalf("retried attention finish = %#v, want generation 2 no-action without delivery", finished)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests = %d, want warm plus one retried attention turn", got)
	}
	resolutions := 0
	for _, entry := range readAttentionTranscriptEntries(t, sub.sess.TranscriptPath()) {
		if resolution := entry.Turn.AttentionResolution; resolution != nil && resolution.AttentionID == attentionID {
			resolutions++
		}
	}
	if resolutions != 1 {
		t.Fatalf("attention resolution markers = %d, want one from the presenting turn", resolutions)
	}
	// The presented row is consumed, so a later pass has nothing to launch.
	if root.driveDelegateAttention(sub) {
		t.Fatal("a consumed attention row was driven again")
	}
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("provider requests after consumed attention = %d, want no extra turn", got)
	}
}
