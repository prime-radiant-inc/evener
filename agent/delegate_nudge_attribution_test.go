package agent

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestDelegateNudgeTextFollowsTheNonTerminalUpdate pins the reminder branch: a
// delegate that already sent an end_turn=false update gets a different reminder
// from one that never called the result tool (#3734).
func TestDelegateNudgeTextFollowsTheNonTerminalUpdate(t *testing.T) {
	t.Parallel()
	if got := delegateNudgeText("communicate", false); got != communicateNudge("communicate") {
		t.Errorf("no-update reminder = %q, want communicateNudge", got)
	}
	if got := delegateNudgeText("communicate", true); got != communicateUpdateNudge("communicate") {
		t.Errorf("update reminder = %q, want communicateUpdateNudge", got)
	}
	if delegateNudgeText("communicate", true) == delegateNudgeText("communicate", false) {
		t.Error("a non-terminal update must select a different reminder, not the never-communicated one")
	}
}

// TestDelegateMissingReportNudgeIsRuntimeSteeringAfterUpdate exercises the real
// delegate lifecycle with a scripted provider: a non-terminal status update,
// the automatic reminder, then a terminal report. The reminder must be recorded
// as a runtime steering turn (not fresh user input) and must be sent once; the
// settled run must report rather than produce a series of paused reports
// (#3734).
func TestDelegateMissingReportNudgeIsRuntimeSteeringAfterUpdate(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("still working")}
	}
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("warm result") },
		func(llm.Request) llm.Response {
			return updateResponse("status_call", "the authorized diagnosis is starting")
		},
		bare, bare, bare, bare,
		func(llm.Request) llm.Response {
			return communicateWithStructured("BOUNDED_DIAG_BLOCKED: the authorized diagnosis could not run", map[string]any{})
		},
	})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	// The reminder's selection must see the non-terminal update the input sent
	// before it ended.
	updatedAtRecovery := false
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentNudgeSelected = func(updated bool) { updatedAtRecovery = updated }
	})
	sendAndAcknowledge(t, root, fixture.delegateID, "Run the one authorized diagnosis.")
	waitForStableSupervisionRun(t, root, fixture.childID)

	runtime := sub.sess
	runtime.mu.Lock()
	nudges, userInputs := 0, 0
	for _, turn := range runtime.history {
		if turn.Kind == schema.TurnSteering && turn.SteeringKind == events.SteeringKindDelegateNudge {
			nudges++
		}
		if turn.Kind == schema.TurnUserInput {
			userInputs++
		}
	}
	runtime.mu.Unlock()

	if nudges != 1 {
		t.Fatalf("runtime steering turns of kind %q = %d, want exactly one automatic reminder", events.SteeringKindDelegateNudge, nudges)
	}
	// Warm input plus the authorized diagnosis; the automatic recovery must add
	// no third USER_INPUT record (and so no user-input event or prompt hook).
	if userInputs != 2 {
		t.Fatalf("USER_INPUT records = %d, want warm plus diagnosis only; the runtime recovery added caller-shaped input", userInputs)
	}
	if !updatedAtRecovery {
		t.Fatal("the non-terminal update was not visible when the reminder was chosen")
	}
	finished := latestDelegateControllerRunFinished(t, root.delegateController, fixture.delegateID)
	aggregate := delegateAggregateSnapshot(t, root.delegateController, fixture.delegateID)
	if finished.Disposition != delegatestore.DispositionReported || aggregate.LatestPacket == nil || aggregate.LatestPacket.Kind != delegatestore.PacketReported {
		t.Fatalf("settled run = %#v aggregate=%#v, want one reported delivery", finished, aggregate)
	}
	// The input's bare-text retries, one reminder, and the report are bounded;
	// the run must not keep re-driving paused reports.
	if got := supervisionRequestCount(fixture.adapter); got > 10 {
		t.Fatalf("provider requests = %d, want a bounded run (no unbounded paused reports)", got)
	}
}
