package appprojector

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
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
