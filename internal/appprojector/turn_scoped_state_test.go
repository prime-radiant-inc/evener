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

func TestReasoningItemCompletesWhenAssistantRoundEnds(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	at := time.Unix(20, 0)
	p.Project(events.SessionEvent{Kind: events.EventUserInput, Timestamp: at, Data: events.UserInputData{Text: "one", StableTurnID: "turn_m1"}})
	started := reasoningItemStarted(p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, Timestamp: at, Data: events.ReasoningSummaryDeltaData{Delta: "thinking"}}))
	if len(started) != 1 {
		t.Fatalf("reasoning started=%v, want one item", started)
	}
	out := p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, Timestamp: at, Data: events.AssistantTextEndData{Text: "answer", FinishReason: "stop"}})
	var completed []appwire.ThreadItem
	for _, n := range out {
		if n.Method != appwire.NotifyItemCompleted {
			continue
		}
		params, ok := n.Params.(appwire.ItemLifecycleParams)
		if ok {
			completed = append(completed, params.Item)
		}
	}
	if len(completed) < 2 {
		t.Fatalf("completed items=%+v, want reasoning and assistant", completed)
	}
	if completed[0].ID != started[0] || completed[0].Type != "reasoning" || completed[0].Status != appwire.TurnStatusCompleted {
		t.Fatalf("reasoning completion=%+v, want completed item %q", completed[0], started[0])
	}
}

func TestReasoningItemCompletesWhenToolOnlyAssistantRoundEnds(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	at := time.Unix(25, 0)
	p.Project(events.SessionEvent{Kind: events.EventUserInput, Timestamp: at, Data: events.UserInputData{Text: "one", StableTurnID: "turn_m1"}})
	started := reasoningItemStarted(p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, Timestamp: at, Data: events.ReasoningSummaryDeltaData{Delta: "thinking"}}))
	out := p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, Timestamp: at, Data: events.AssistantTextEndData{Text: "", FinishReason: "tool_calls"}})
	if len(out) != 1 {
		t.Fatalf("tool-only notifications=%+v, want one reasoning completion", out)
	}
	params, ok := out[0].Params.(appwire.ItemLifecycleParams)
	if !ok || params.Item.ID != started[0] || params.Item.Type != "reasoning" || params.Item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("tool-only completion=%+v, want completed reasoning %q", out[0].Params, started[0])
	}
}

func TestReasoningItemCompletesBeforeNextAssistantRound(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	at := time.Unix(30, 0)
	p.Project(events.SessionEvent{Kind: events.EventUserInput, Timestamp: at, Data: events.UserInputData{Text: "one", StableTurnID: "turn_m1"}})
	started := reasoningItemStarted(p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, Timestamp: at, Data: events.ReasoningSummaryDeltaData{Delta: "thinking"}}))
	out := p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, Timestamp: at, Data: events.AssistantTextStartData{Model: "fixture-model"}})
	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("next-round notifications=%+v, want one reasoning completion", out)
	}
	params, ok := out[0].Params.(appwire.ItemLifecycleParams)
	if !ok || params.Item.ID != started[0] || params.Item.Type != "reasoning" || params.Item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("next-round completion=%+v, want completed reasoning %q", out[0].Params, started[0])
	}
}

func TestReasoningItemCompletesWhenTurnFails(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	at := time.Unix(40, 0)
	p.Project(events.SessionEvent{Kind: events.EventUserInput, Timestamp: at, Data: events.UserInputData{Text: "one", StableTurnID: "turn_m1"}})
	started := reasoningItemStarted(p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, Timestamp: at, Data: events.ReasoningSummaryDeltaData{Delta: "thinking"}}))
	out := p.Project(events.SessionEvent{Kind: events.EventError, Timestamp: at, Data: events.ErrorData{Error: "provider exploded"}})
	var found bool
	for _, n := range out {
		if n.Method != appwire.NotifyItemCompleted {
			continue
		}
		params, ok := n.Params.(appwire.ItemLifecycleParams)
		if ok && params.Item.ID == started[0] {
			found = true
			if params.Item.Status != appwire.TurnStatusFailed {
				t.Fatalf("reasoning status=%q, want failed", params.Item.Status)
			}
		}
	}
	if !found {
		t.Fatalf("failed turn notifications=%+v, want reasoning completion", out)
	}
}

func TestReasoningItemUsesTerminalSessionStatus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		want  string
	}{
		{name: "awaiting", state: appwire.ThreadStatusAwaiting, want: appwire.TurnStatusCompleted},
		{name: "interrupted", state: appwire.ThreadStatusClosed, want: appwire.TurnStatusInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewAppEventProjector("th_1", "local:th_1")
			at := time.Unix(50, 0)
			p.Project(events.SessionEvent{Kind: events.EventUserInput, Timestamp: at, Data: events.UserInputData{Text: "one", StableTurnID: "turn_m1"}})
			started := reasoningItemStarted(p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, Timestamp: at, Data: events.ReasoningSummaryDeltaData{Delta: "thinking"}}))
			out := p.Project(events.SessionEvent{Kind: events.EventSessionEnd, Timestamp: at, Data: events.SessionEndData{State: tc.state, Interrupted: tc.state == appwire.ThreadStatusClosed}})
			var completions []appwire.ItemLifecycleParams
			for _, n := range out {
				if n.Method != appwire.NotifyItemCompleted {
					continue
				}
				if params, ok := n.Params.(appwire.ItemLifecycleParams); ok {
					completions = append(completions, params)
				}
			}
			if len(completions) != 1 || completions[0].Item.ID != started[0] || completions[0].Item.Status != tc.want {
				t.Fatalf("session end completions=%+v, want reasoning %q status %q", completions, started[0], tc.want)
			}
		})
	}
}
