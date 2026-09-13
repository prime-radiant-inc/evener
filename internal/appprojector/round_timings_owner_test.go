package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// A round's timing record is published as that round ends, so the turn it
// belongs to can already be over by the time the event arrives. The durable
// entry names the owner; the live item has to land on the same one rather than
// on whatever turn the projector has open.
func TestRoundTimingsItemKeepsItsDurableOwner(t *testing.T) {
	p := NewAppEventProjector("timings-owner", "local:timings-owner")
	user := p.Project(events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: "input"}})
	running := notificationThreadItem(t, user, appwire.NotifyItemCompleted).TurnID
	if running == "" {
		t.Fatal("test setup: no running turn to be wrong about")
	}

	const owner = "turn_m7"
	timings := p.Project(events.SessionEvent{Kind: events.EventRoundTimings, Data: events.RoundTimings{OwningTurnID: owner, Round: 2}})
	item := notificationThreadItem(t, timings, appwire.NotifyItemCompleted)
	if item.TurnID != owner {
		t.Fatalf("round-timing item turn=%q, want the owner its entry carries %q (the projector had %q open)", item.TurnID, owner, running)
	}
	if item.EventKind != appwire.ThreadItemEventKindRoundTimings {
		t.Fatalf("round-timing item kind=%q", item.EventKind)
	}
	if item.Raw == nil {
		t.Fatal("round-timing item lost the measurements it carries in Raw")
	}
}
