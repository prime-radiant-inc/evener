package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// A watch on another session can subscribe to that session's human messages
// and turn ends: the frame carries the bounded message text, and a turn end
// fires a frame of its own.
func TestStableWatchDeliversUserMessageAndTurnEnd(t *testing.T) {
	fixture := newStableWatchRuntimeBase(t, nil)
	if _, err := jobWatchToolWithContext(context.Background(), fixture.root, map[string]any{
		"operation": "create",
		"source":    "dlg_source",
		"events":    []any{"user.message", "turn.end"},
	}, 4096); err != nil {
		t.Fatalf("create user.message watch: %v", err)
	}

	onSessionEventKD(fixture.sourceJM, events.EventUserInput, events.UserInputData{Text: "remember: money is integer cents", Turn: 3})
	frame := fixture.requireOnePending(t).state.Frame
	for _, want := range []string{"kind: user.message", "money is integer cents", "turn: 3"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("user.message frame lacks %q:\n%s", want, frame)
		}
	}

	onSessionEventKD(fixture.sourceJM, events.EventTurnEnded, events.TurnEndedData{TurnDurationMS: 1500})
	// Frames coalesce while the receiver is busy, so the pending frame is now
	// the turn end.
	frame = fixture.requireOnePending(t).state.Frame
	if !strings.Contains(frame, "kind: turn.end") || !strings.Contains(frame, "turn_duration_ms: 1500") {
		t.Fatalf("turn.end frame:\n%s", frame)
	}
}

// A long human message is bounded in the frame and marked truncated.
func TestUserMessageWatchFrameIsBounded(t *testing.T) {
	var b strings.Builder
	writeWatchFrameEvent(&b, events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: strings.Repeat("x", 3*watchExcerptMaxChars)}})
	got := b.String()
	if !strings.Contains(got, "truncated: true") || len(got) > 2*watchExcerptMaxChars {
		t.Fatalf("user.message frame not bounded (%d chars): %.200q", len(got), got)
	}
}

// The new kinds are opt-in: a wildcard watch on another session keeps its
// earlier vocabulary and does not fire on them.
func TestWildcardStableWatchIgnoresOptInKinds(t *testing.T) {
	fixture := newStableWatchRuntimeBase(t, nil)
	if _, err := jobWatchToolWithContext(context.Background(), fixture.root, map[string]any{
		"operation": "create",
		"source":    "dlg_source",
		"events":    []any{"*"},
	}, 4096); err != nil {
		t.Fatalf("create wildcard watch: %v", err)
	}
	onSessionEventKD(fixture.sourceJM, events.EventUserInput, events.UserInputData{Text: "hi"})
	onSessionEventKD(fixture.sourceJM, events.EventTurnEnded, events.TurnEndedData{})
	if pending := fixture.sourceJM.pendingWatchSendDeliveries(nil); len(pending) != 0 {
		t.Fatalf("wildcard watch fired on opt-in kinds: %d frames", len(pending))
	}
}

// A session may not watch its own turn ends or messages: each frame would wake
// it into another turn.
func TestSelfWatchRefusesOptInKinds(t *testing.T) {
	for _, kind := range []string{"user.message", "turn.end"} {
		err := validateWatchEventArgs(watchArgs{Source: "self", Events: []string{kind}})
		if err == nil || !strings.Contains(err.Error(), "only on another session") {
			t.Fatalf("self watch on %s: err = %v", kind, err)
		}
		if err := validateWatchEventArgs(watchArgs{Source: "parent", Events: []string{kind}, StableReceiver: true}); err != nil {
			t.Fatalf("parent watch on %s refused: %v", kind, err)
		}
	}
}
