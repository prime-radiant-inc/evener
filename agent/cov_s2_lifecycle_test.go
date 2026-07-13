package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestS2Cov_HandleCompactionTurn_WritesTranscriptAndEmitsEvent covers the
// handleCompactionTurn artifact contract: a SUMMARY turn is appended to the
// transcript and emits EventCompactionTurn.
func TestS2Cov_HandleCompactionTurn_WritesTranscriptAndEmitsEvent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 1, StateDir: dir}))
	path := transcriptPath(dir, sess.ID())
	before, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("read transcript before compaction: %v", err)
	}
	if got := len(before.Entries); got != 0 {
		t.Fatalf("pre-compaction transcript entries = %d, want 0", got)
	}

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

	after, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("read transcript after compaction: %v", err)
	}
	if got := len(after.Entries); got != len(before.Entries)+1 {
		t.Fatalf("post-compaction transcript entries = %d, want %d", got, len(before.Entries)+1)
	}
	entry := after.Entries[len(after.Entries)-1]
	if entry.Turn.Kind != schema.TurnSummary {
		t.Fatalf("appended turn kind = %v, want %v", entry.Turn.Kind, schema.TurnSummary)
	}
	if got, want := entry.Turn.Message.Text(), "a compaction summary"; got != want {
		t.Fatalf("appended turn text = %q, want %q", got, want)
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
		if turn.Kind == schema.TurnSteering && turn.Message.Text() == "keep going toward the objective" {
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
