package appprojector

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestReserveStableTurnIDReplacesAStaleActiveProjection(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	at := time.Unix(1_700_000_000, 0)
	p.Project(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Timestamp: at,
		Data: events.UserInputData{
			StableTurnID: "turn_old",
			Text:         "first turn",
		},
	})
	if got := p.ActiveTurnID(); got != "turn_old" {
		t.Fatalf("fixture ActiveTurnID = %q, want turn_old", got)
	}

	p.ReserveStableTurnID("turn_durable")
	if got := p.ReservedTurnID(); got != "turn_durable" {
		t.Fatalf("ReservedTurnID = %q, want turn_durable", got)
	}
	if got := p.ActiveTurnID(); got != "turn_durable" {
		t.Fatalf("ActiveTurnID after reservation = %q, want turn_durable", got)
	}

	out := p.Project(events.SessionEvent{
		Kind:      events.EventAssistantTextStart,
		SessionID: "th_1",
		Timestamp: at.Add(time.Second),
	})
	if got := turnStartedID(t, out); got != "turn_durable" {
		t.Fatalf("projected turn ID = %q, want turn_durable", got)
	}
	if got := p.ReservedTurnID(); got != "" {
		t.Fatalf("ReservedTurnID after turn start = %q, want empty", got)
	}
}

func TestReasoningCompletionKeepsItsOwningTurnAfterReservation(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "first", StableTurnID: "turn_old"}})
	p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_1", Data: events.ReasoningSummaryDeltaData{Delta: "thinking"}})
	p.ReserveStableTurnID("turn_durable")

	out := p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1"})
	for _, n := range out {
		if n.Method != appwire.NotifyItemCompleted {
			continue
		}
		params, ok := n.Params.(appwire.ItemLifecycleParams)
		if ok && params.Item.Type == "reasoning" {
			if params.TurnID != "turn_old" || params.Item.TurnID != "turn_old" {
				t.Fatalf("reasoning completion=(notification turn %q, item turn %q), want turn_old", params.TurnID, params.Item.TurnID)
			}
			return
		}
	}
	t.Fatalf("notifications=%+v, want reasoning completion", out)
}

func TestReservationCompletesThePreviousTurnBeforeTheReservedTurnStarts(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "first", StableTurnID: "turn_old"}})
	p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_1", Data: events.ReasoningSummaryDeltaData{Delta: "thinking"}})
	p.ReserveStableTurnID("turn_durable")

	out := p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1"})
	completed := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if completed.ID != "turn_old" || completed.Status != appwire.TurnStatusCompleted {
		t.Fatalf("completed turn=%+v, want old completed turn", completed)
	}
	started := notificationTurn(t, out, appwire.NotifyTurnStarted)
	if started.ID != "turn_durable" {
		t.Fatalf("started turn=%+v, want reserved turn", started)
	}
	completedIndex, startedIndex := -1, -1
	for i, notification := range out {
		switch notification.Method {
		case appwire.NotifyTurnCompleted:
			completedIndex = i
		case appwire.NotifyTurnStarted:
			startedIndex = i
		}
	}
	if completedIndex >= startedIndex {
		t.Fatalf("completion index=%d, start index=%d, want old completion before new start", completedIndex, startedIndex)
	}
}

func TestReservationPreservesPreviousTurnTimingUsageAndCost(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.SetCostLookup(func(provider, model string) *registry.Cost {
		return &registry.Cost{Input: 5, Output: 25}
	})
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Timestamp: time.Unix(100, 0), Data: events.UserInputData{Text: "first", StableTurnID: "turn_old"}})
	p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_1", Data: events.ReasoningSummaryDeltaData{Delta: "thinking"}})
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "answer", Usage: llm.Usage{InputTokens: 1000, OutputTokens: 500}, Provider: "anthropic", Model: "claude-opus-4-5"}})
	p.Project(events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_1", Data: events.TurnEndedData{TurnDurationMS: 4200}})
	p.ReserveStableTurnID("turn_durable")

	out := p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1"})
	completed := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if completed.ID != "turn_old" || completed.DurationMS == nil || *completed.DurationMS != 4200 || completed.Usage == nil || completed.Usage.InputTokens != 1000 || completed.Cost != "~$0.02" {
		t.Fatalf("completed turn=%+v, want old timing/usage/cost preserved", completed)
	}
}

func TestUnmaterializedReservationDoesNotEmitPhantomCompletion(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.ReserveStableTurnID("turn_first")
	p.ReserveStableTurnID("turn_second")
	out := p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1"})
	for _, notification := range out {
		if notification.Method == appwire.NotifyTurnCompleted {
			t.Fatalf("phantom completion=%+v", notification)
		}
	}
	if got := notificationTurn(t, out, appwire.NotifyTurnStarted).ID; got != "turn_second" {
		t.Fatalf("started turn=%q, want latest reservation", got)
	}
}

func TestReasoningDeltaAfterReservationStartsANewOwnedItem(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "first", StableTurnID: "turn_old"}})
	old := p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_1", Data: events.ReasoningSummaryDeltaData{Delta: "old"}})
	oldID := old[1].Params.(appwire.ReasoningSummaryDeltaParams).ItemID
	p.ReserveStableTurnID("turn_durable")

	out := p.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_1", Data: events.ReasoningSummaryDeltaData{Delta: "new"}})
	var completed, started appwire.ItemLifecycleParams
	var deltaParams appwire.ReasoningSummaryDeltaParams
	for _, n := range out {
		switch n.Method {
		case appwire.NotifyItemCompleted:
			completed = n.Params.(appwire.ItemLifecycleParams)
		case appwire.NotifyItemStarted:
			started = n.Params.(appwire.ItemLifecycleParams)
		case appwire.NotifyReasoningSummaryDelta:
			deltaParams = n.Params.(appwire.ReasoningSummaryDeltaParams)
		}
	}
	if completed.Item.ID != oldID || completed.Item.TurnID != "turn_old" {
		t.Fatalf("stale completion=%+v, want item %q owned by turn_old", completed, oldID)
	}
	if started.Item.ID == oldID || started.Item.TurnID != "turn_durable" {
		t.Fatalf("new reasoning start=%+v, want fresh item in turn_durable", started)
	}
	if deltaParams.ItemID != started.Item.ID || deltaParams.TurnID != "turn_durable" {
		t.Fatalf("new reasoning delta=%+v, want fresh item in turn_durable", deltaParams)
	}
}
