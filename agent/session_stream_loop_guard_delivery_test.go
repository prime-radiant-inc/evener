package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

// This file pins delivery of a stream-loop nudge queued by consumeModelStream
// (session_stream.go) onto sess.pendingStreamLoopNudge: TestConsumeModelStream_*
// already proves the field gets SET correctly; these tests prove it actually
// reaches the transcript as a steering turn rather than sitting unread.
//
// In Phase 1 there is exactly one reachable delivery site. Both detectors
// (cycle and ceiling) key off StreamEventToolCallEnd, so a trip is only ever
// possible once at least one tool call has completed -- which means the round
// always has tool calls and always reaches injectPostToolSteering. Delivering
// there puts the nudge after tool results, so it never lands between a
// tool_use turn and its tool_result (providers that require them adjacent
// would reject the next request).

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
