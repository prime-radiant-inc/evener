package appprojector

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// reasoningItemStarted reports the item id of every item/started frame for a
// reasoning item in out.
func reasoningItemStarted(out []AppNotification) []string {
	var ids []string
	for _, n := range out {
		if n.Method != appwire.NotifyItemStarted {
			continue
		}
		params, ok := n.Params.(appwire.ItemLifecycleParams)
		if !ok || params.Item.Type != "reasoning" {
			continue
		}
		ids = append(ids, params.Item.ID)
	}
	return ids
}

// TestFailedTurnDoesNotLeakItsReasoningItem pins the turn-scoped state reset
// across a failed turn.
//
// EventError used to end a turn by clearing a smaller field set than the normal
// close path: it left reasoningItem, toolArgsByKey and toolStartByKey set, and
// startTurn does not clear reasoningItem either. The first reasoning delta of
// the NEXT turn then found a non-empty reasoningItem, took ensureReasoningItem's
// "already exists" branch, and emitted its deltas against the failed turn's item
// id with no item/started ever announced for it. Both endings now share
// resetTurnScopedState; EventError stays a separate case only for its error
// payload and its ensureTurn call.
//
// A client that materializes items from item/started then has deltas addressed
// to an item it never saw open, in a turn that item does not belong to.
func TestFailedTurnDoesNotLeakItsReasoningItem(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	at := time.Unix(10, 0)

	p.Project(events.SessionEvent{
		Kind:      events.EventUserInput,
		Timestamp: at,
		Data:      events.UserInputData{Text: "one", StableTurnID: "turn_m1"},
	})
	first := reasoningItemStarted(p.Project(events.SessionEvent{
		Kind:      events.EventReasoningSummaryDelta,
		Timestamp: at,
		Data:      events.ReasoningSummaryDeltaData{Delta: "thinking"},
	}))
	if len(first) != 1 {
		t.Fatalf("the first turn announced %d reasoning items, want 1", len(first))
	}

	p.Project(events.SessionEvent{
		Kind:      events.EventError,
		Timestamp: at,
		Data:      events.ErrorData{Error: "provider exploded"},
	})

	p.Project(events.SessionEvent{
		Kind:      events.EventUserInput,
		Timestamp: at,
		Data:      events.UserInputData{Text: "two", StableTurnID: "turn_m2"},
	})
	second := reasoningItemStarted(p.Project(events.SessionEvent{
		Kind:      events.EventReasoningSummaryDelta,
		Timestamp: at,
		Data:      events.ReasoningSummaryDeltaData{Delta: "thinking again"},
	}))

	if len(second) != 1 {
		t.Fatalf("the turn after a failed one announced %d reasoning items, want 1: its deltas address an item the client never saw open", len(second))
	}
	if second[0] == first[0] {
		t.Fatalf("the turn after a failed one reused the failed turn's reasoning item %q", first[0])
	}
}
