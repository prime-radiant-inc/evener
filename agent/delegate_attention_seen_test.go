package agent

import (
	"context"
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
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "FOLLOW-UP: also check the migration", 60_000)
	if outcome.result.Err != nil || outcome.commit == nil {
		t.Fatalf("follow-up send = %#v", outcome)
	}
	plans, err := outcome.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge follow-up result: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("execute follow-up acknowledgement: %v", err)
	}
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
	sess := sub.sess
	sess.mu.Lock()
	path, sessionID := transcriptPath(sess.stateDir, sess.id), sess.id
	sess.mu.Unlock()
	// A fresh read: the session's fold cursor belongs to attentionMu, which
	// the running drive holds while it reads.
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("fold child attention: %v", err)
	}
	return fold.resolutions[attentionID], fold.resumeGenerations[attentionID]
}
