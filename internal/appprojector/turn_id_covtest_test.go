package appprojector

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
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
