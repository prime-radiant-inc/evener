package agent

import (
	"context"
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
			return quietYieldResponse("attention-no-action", `{"message":"waiting on work","next":"log_quietly_and_end_turn"}`)
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
	if got := len(root.delegateController.reservations); got != 0 {
		t.Fatalf("completed attention left %d stale reservations blocking future work", got)
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
