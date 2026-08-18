package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// This file pins delivery of a stream-loop nudge queued by consumeModelStream
// (session_stream.go) onto sess.pendingStreamLoopNudge: TestConsumeModelStream_*
// already proves the field gets SET correctly; these tests prove it actually
// reaches the transcript as a steering turn rather than sitting unread.
//
// Two call sites need to read it, not one: a response with tool calls reaches
// injectPostToolSteering (after tool results, matching the pre-existing
// cross-round detector's steering point), but a chant trip (kata fixture (c))
// has ZERO tool calls by construction, so it takes processOneInput's separate
// len(calls)==0 branch instead -- which never calls injectPostToolSteering at
// all. Without its own delivery point there, exactly the shape the kata's
// spec highlights as needing this guard (a reasoning-only runaway) would have
// its nudge silently dropped, violating "re-prompt on trip, do not silently
// suppress" (kata research citation: hermes-agent #41490).

// findSteeringTurnByKind returns the first history turn tagged with the given
// SteeringKind. It searches by kind rather than "the last steering turn"
// because a round that also exercises the pre-existing no-tool-calls retry
// path appends its OWN steering turns around the one under test here.
func findSteeringTurnByKind(sess *Session, kind string) (schema.Turn, bool) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && turn.SteeringKind == kind {
			return turn, true
		}
	}
	return schema.Turn{}, false
}

// TestDeliverPendingStreamLoopNudge_AppendsAndClears drives the helper
// directly: a pending nudge becomes a steering turn tagged
// SteeringKindStreamLoop, and the field is cleared so it cannot be delivered
// twice.
func TestDeliverPendingStreamLoopNudge_AppendsAndClears(t *testing.T) {
	sess := newSession(t)
	sess.mu.Lock()
	sess.pendingStreamLoopNudge = "you are repeating yourself"
	sess.mu.Unlock()

	if err := sess.deliverPendingStreamLoopNudge(context.Background()); err != nil {
		t.Fatalf("deliverPendingStreamLoopNudge: %v", err)
	}

	turn, ok := findSteeringTurnByKind(sess, events.SteeringKindStreamLoop)
	if !ok {
		t.Fatal("no SteeringKindStreamLoop turn appended")
	}
	if !strings.Contains(turn.Message.Text(), "repeating yourself") {
		t.Errorf("turn text = %q, want it to carry the nudge", turn.Message.Text())
	}

	sess.mu.Lock()
	remaining := sess.pendingStreamLoopNudge
	historyLen := len(sess.history)
	sess.mu.Unlock()
	if remaining != "" {
		t.Errorf("pendingStreamLoopNudge = %q, want cleared after delivery", remaining)
	}

	// A second call with nothing pending must not append another turn.
	if err := sess.deliverPendingStreamLoopNudge(context.Background()); err != nil {
		t.Fatalf("second deliverPendingStreamLoopNudge: %v", err)
	}
	sess.mu.Lock()
	got := len(sess.history)
	sess.mu.Unlock()
	if got != historyLen {
		t.Errorf("history grew from %d to %d on an empty pending nudge, want no-op", historyLen, got)
	}
}

// TestInjectPostToolSteering_DeliversStreamLoopNudge covers the has-tool-calls
// path: injectPostToolSteering must deliver a pending stream-loop nudge the
// same way it delivers the cross-round detector's own warning.
func TestInjectPostToolSteering_DeliversStreamLoopNudge(t *testing.T) {
	sess := newSession(t)
	sess.mu.Lock()
	sess.pendingStreamLoopNudge = "cut off after a repeating cycle"
	sess.mu.Unlock()

	var toolSigs []string
	var toolSigFailed []bool
	if _, err := sess.injectPostToolSteering(context.Background(), nil, nil, &toolSigs, &toolSigFailed); err != nil {
		t.Fatalf("injectPostToolSteering: %v", err)
	}

	if _, ok := findSteeringTurnByKind(sess, events.SteeringKindStreamLoop); !ok {
		t.Fatal("no SteeringKindStreamLoop turn appended")
	}
}

// TestProcessOneInput_ZeroToolCalls_DeliversStreamLoopNudge covers the
// chant-shaped path directly: a round that produces NO tool calls never
// reaches injectPostToolSteering, so processOneInput's own len(calls)==0
// branch must deliver the nudge itself, or exactly the shape the kata's spec
// highlights as needing this guard (a reasoning-only runaway, zero tool
// calls) would have its nudge silently dropped. Simulates "consumeModelStream
// just tripped" by presetting the field directly
// (TestConsumeModelStream_LoopGuard_ChantShape already proves
// consumeModelStream itself sets it correctly for this exact shape); this
// test is only about whether the round loop reads it back out. The scripted
// model reply is bare text with no tool call, which the round loop retries
// (and eventually fails) via its own unrelated no-tool-calls budget -- ignored
// here; only the presence of the stream-loop turn is under test.
func TestProcessOneInput_ZeroToolCalls_DeliversStreamLoopNudge(t *testing.T) {
	sess := newSession(t, withSteps(
		func(req llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("I looked into it but did not call a tool.")}
		},
	))
	sess.mu.Lock()
	sess.pendingStreamLoopNudge = "chant trip: repeated the same passage"
	sess.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "go", nil)

	if _, ok := findSteeringTurnByKind(sess, events.SteeringKindStreamLoop); !ok {
		t.Fatal("no SteeringKindStreamLoop turn appended")
	}
}
