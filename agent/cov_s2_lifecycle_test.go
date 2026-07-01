package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestS2Cov_HandleCompactionTurn_SteersTranscriptRef covers handleCompactionTurn:
// a SUMMARY turn is written, emits EventCompactionTurn, and steers the
// pre-compaction transcript-reference reminder when a state dir and id exist.
func TestS2Cov_HandleCompactionTurn_SteersTranscriptRef(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))

	var sawCompactionEvent bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			if ev.Kind == events.EventCompactionTurn {
				sawCompactionEvent = true
			}
		}
	}()

	sess.handleCompactionTurn(schema.NewTurn(schema.TurnSummary, llm.Assistant("a compaction summary")))

	steered := sess.drainSteering()
	var sawRef bool
	for _, m := range steered {
		if strings.Contains(m.Text, "read_session_transcript") {
			sawRef = true
		}
	}
	if !sawRef {
		t.Fatalf("no transcript-ref steering; steered = %+v", steered)
	}

	sess.Close()
	<-done
	if !sawCompactionEvent {
		t.Fatal("EventCompactionTurn was not emitted")
	}
}

// TestS2Cov_AcceptContinuationInput_AppendsSteeringAndMarker covers the goal
// continuation acceptance: it emits EventGoalContinuation and records the full
// prompt as a steering turn (not a user bubble).
func TestS2Cov_AcceptContinuationInput_AppendsSteeringAndMarker(t *testing.T) {
	t.Parallel()
	sess := newSession(t)

	var sawGoal bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			if ev.Kind == events.EventGoalContinuation {
				sawGoal = true
			}
		}
	}()

	sess.acceptContinuationInput(context.Background(), "keep going toward the objective")

	if k := s2cov_lastTurnKind(sess); k != schema.TurnSteering {
		t.Fatalf("last turn kind = %v, want TurnSteering", k)
	}
	sess.mu.Lock()
	var sawInput bool
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), "keep going toward the objective") {
			sawInput = true
		}
	}
	sess.mu.Unlock()
	if !sawInput {
		t.Fatal("continuation prompt not recorded as a steering turn")
	}

	sess.Close()
	<-done
	if !sawGoal {
		t.Fatal("EventGoalContinuation was not emitted")
	}
}
