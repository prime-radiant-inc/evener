package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	fixture := newColdStableDelegateFixture(t, "")
	var root *Session
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare without communicate")}
	}
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response {
			if err := root.subagents.get(fixture.childID).sess.FollowUp("queued follow-up work"); err != nil {
				t.Errorf("queue attention follow-up: %v", err)
			}
			return llm.Response{Message: llm.Assistant("attention requires no action")}
		},
		bare, bare, bare, bare,
		func(llm.Request) llm.Response { return finalResponse("follow-up report") },
		func(llm.Request) llm.Response { return finalResponse("next owner report") },
	}
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
		root.driveStableDelegateAttention(sub)
	}()
	select {
	case <-selected:
	case <-time.After(30 * time.Second): // TRIPWIRE: await the selection boundary.
		t.Fatal("attention drive did not select the pending ID")
	}
	if !root.driveStableDelegateAttention(sub) {
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
	if got := root.delegateController.reservedAttentionID(sub.sess); got != "" {
		t.Fatalf("completed attention left stale reservation %q blocking future work", got)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 7 {
		t.Fatalf("provider requests = %d, want warm, attention, four follow-up attempts, and recovery report", got)
	}
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "next owner work", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
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

// An accepted marker belongs to an exact reserved generation even when its
// journal commit failed. Revalidating fresh selections must not discard that
// existing reservation just because the transcript ID is already consumed.
func TestDelegateAttentionDriveRetriesAcceptedMarkerGeneration(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("nothing to do")} },
	}
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
	// Close the real journal to fail commit after the real transcript has
	// durably accepted the generation. No provider turn has started yet.
	if err := controller.store.Close(); err != nil {
		t.Fatalf("close delegate journal: %v", err)
	}
	if !root.driveStableDelegateAttention(sub) {
		t.Fatal("attention drive did not attempt the closed journal")
	}
	if got := controller.reservedAttentionID(sub.sess); got != attentionID {
		t.Fatalf("failed commit reservation = %q, want %q", got, attentionID)
	}
	fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), fixture.childID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fold.pendingIDs()) != 0 || fold.resumeGenerations[attentionID] != 2 {
		t.Fatalf("accepted attention = pending:%v generations:%v, want consumed generation 2", fold.pendingIDs(), fold.resumeGenerations)
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
	if !root.driveStableDelegateAttention(sub) {
		t.Fatal("attention drive did not retry its accepted generation")
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
		t.Fatalf("accepted attention resolution markers = %d, want one", resolutions)
	}
}
