package agent

// Tests for steering Kind (system steering voice): the daemon names what it
// injected (events.SteeringKind*) at the queue-enqueue call site, and that
// name must travel through the steering queue, the emitted
// SteeringInjectedData event, and the persisted transcript turn so a reader
// gets a labeled steer instead of one guessed from the message's prose.

import (
	"testing"

	"primeradiant.com/serf/agent/events"
)

func TestSteerKindReachesTheInjectedEvent(t *testing.T) {
	s := newTestSession(t)
	s.SteerKind("nudge", events.SteeringKindCompactNudge)
	msgs := s.drainSteeringForTurn()
	if len(msgs) != 1 {
		t.Fatalf("queued %d messages, want 1", len(msgs))
	}
	if msgs[0].Kind != events.SteeringKindCompactNudge {
		t.Errorf("queued Kind = %q, want %q", msgs[0].Kind, events.SteeringKindCompactNudge)
	}
	got := steeringInjectedDataFromMessage(msgs[0])
	if got.Kind != events.SteeringKindCompactNudge {
		t.Errorf("event Kind = %q, want %q", got.Kind, events.SteeringKindCompactNudge)
	}
}

func TestSteerLeavesKindEmpty(t *testing.T) {
	s := newTestSession(t)
	s.Steer("no kind here")
	msgs := s.drainSteeringForTurn()
	if len(msgs) != 1 {
		t.Fatalf("queued %d messages, want 1", len(msgs))
	}
	if msgs[0].Kind != "" {
		t.Errorf("queued Kind = %q, want empty", msgs[0].Kind)
	}
}

func TestConsumeSteeringMessagePersistsKindOnTheTurn(t *testing.T) {
	s := newTestSession(t)
	s.consumeSteeringMessage(steeringMessage{Text: "x", Kind: events.SteeringKindLoopDetected})
	last := s.history[len(s.history)-1]
	if last.SteeringKind != events.SteeringKindLoopDetected {
		t.Errorf("turn SteeringKind = %q, want %q", last.SteeringKind, events.SteeringKindLoopDetected)
	}
}
