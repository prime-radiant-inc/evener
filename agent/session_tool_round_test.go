package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
)

// These two tests cover part of review round 2's finding:
// session_tool_round.go's direct-append steering sites (no-tool-calls retry,
// task reminder) labeled the live SteeringInjectedData event correctly but
// left schema.Turn.SteeringKind empty, so a reload showed no kind at all.
// Each asserts the kind lands on the appended turn, read off s.history the
// way round 1's tests read the flushed compaction records. The third
// direct-append site in this file, loop detection, is covered by extending
// the existing TestSession_LoopDetection_EmitsEventAndInjectsSteering
// (session_lifecycle_test.go) instead of a new test here, since that test
// already drives the real multi-round trigger path end to end.

// TestApplyNoToolCallsDecision_PersistsNoToolCallsKind drives the dec.Retry
// branch directly (the same seam session_tool_round_tail_coverage_fuzz_test.go
// uses under the serffuzz tag) rather than through a full model round.
func TestApplyNoToolCallsDecision_PersistsNoToolCallsKind(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	retry, err := s.applyNoToolCallsDecision(noToolCallsDecision{Retry: true, SteeringText: "go on"})
	if !retry || err != nil {
		t.Fatalf("applyNoToolCallsDecision = retry %v err %v, want retry=true err=nil", retry, err)
	}
	s.mu.Lock()
	last := s.history[len(s.history)-1]
	s.mu.Unlock()
	if last.Kind != schema.TurnSteering {
		t.Fatalf("last turn kind = %v, want TurnSteering", last.Kind)
	}
	if last.SteeringKind != events.SteeringKindNoToolCalls {
		t.Errorf("SteeringKind = %q, want %q", last.SteeringKind, events.SteeringKindNoToolCalls)
	}
}

// TestInjectPostToolSteering_PersistsTaskReminderKind drives the task-reminder
// tail of injectPostToolSteering (trigger 3: task_list never used, 10+
// rounds in) directly. Only one trigger is exercised here — the site's fix
// (appendSteeringTurn) is generic over whichever kind maybeInjectTaskReminder
// returns, and round 1's TestMaybeInjectTaskReminder_* tests already cover
// that each trigger returns the right kind value.
func TestInjectPostToolSteering_PersistsTaskReminderKind(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.mu.Lock()
	s.totalRounds = 10 // trigger 3: never used task_list, 10+ rounds in.
	s.mu.Unlock()

	var toolSigs []string
	if _, err := s.injectPostToolSteering(context.Background(), nil, &toolSigs); err != nil {
		t.Fatalf("injectPostToolSteering: %v", err)
	}

	s.mu.Lock()
	last := s.history[len(s.history)-1]
	s.mu.Unlock()
	if last.Kind != schema.TurnSteering {
		t.Fatalf("last turn kind = %v, want TurnSteering", last.Kind)
	}
	if last.SteeringKind != events.SteeringKindTaskNudge {
		t.Errorf("SteeringKind = %q, want %q", last.SteeringKind, events.SteeringKindTaskNudge)
	}
}
