package appprojector

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// A turn the projector opens implicitly — no EventUserInput, no
// EventGoalContinuation, just the first event of a round that finds no turn
// active — must announce itself with turn/started exactly like the two
// explicit openers do. A client keys "this turn is running" on that frame; a
// turn that only appears when its first item or its completion lands is a turn
// the client never saw open (kata e5r2).
func TestAppEventProjectorAnnouncesImplicitlyOpenedTurns(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0).UTC()
	cases := []struct {
		name  string
		event events.SessionEvent
	}{
		{"assistant text start", events.SessionEvent{Kind: events.EventAssistantTextStart, Data: events.AssistantTextStartData{Model: "m"}}},
		{"assistant text delta", events.SessionEvent{Kind: events.EventAssistantTextDelta, Data: events.AssistantTextDeltaData{Delta: "hi"}}},
		{"assistant text end", events.SessionEvent{Kind: events.EventAssistantTextEnd, Data: events.AssistantTextEndData{Text: "done", Usage: llm.Usage{InputTokens: 1}}}},
		{"reasoning summary delta", events.SessionEvent{Kind: events.EventReasoningSummaryDelta, Data: events.ReasoningSummaryDeltaData{Delta: "hmm"}}},
		{"tool call start", events.SessionEvent{Kind: events.EventToolCallStart, Data: events.ToolCallStartData{ToolName: "shell", CallID: "call_1"}}},
		{"communicate", events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "status"}}},
		{"error", events.SessionEvent{Kind: events.EventError, Data: events.ErrorData{Error: "boom"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projector := NewAppEventProjector("th_1", "local:th_1")
			event := tc.event
			event.SessionID = "th_1"
			event.Timestamp = ts
			out := projector.Project(event)

			if len(out) == 0 || out[0].Method != appwire.NotifyTurnStarted {
				t.Fatalf("first notification = %+v, want %s: the implicitly opened turn is never announced", out, appwire.NotifyTurnStarted)
			}
			if started := countMethod(out, appwire.NotifyTurnStarted); started != 1 {
				t.Fatalf("turn/started count = %d, want exactly 1 in %+v", started, out)
			}
			turn := notificationTurn(t, out, appwire.NotifyTurnStarted)
			if turn.ID != "turn_1" {
				t.Fatalf("announced turn id = %q, want turn_1", turn.ID)
			}
			if turn.Status != appwire.TurnStatusInProgress {
				t.Fatalf("announced turn status = %q, want %q", turn.Status, appwire.TurnStatusInProgress)
			}
			if turn.StartedAt == nil || *turn.StartedAt != ts.UnixMilli() {
				t.Fatalf("announced turn StartedAt = %v, want %d (the opening event's own timestamp)", turn.StartedAt, ts.UnixMilli())
			}
			params, ok := out[0].Params.(appwire.TurnStartedParams)
			if !ok {
				t.Fatalf("turn/started params type = %T, want appwire.TurnStartedParams", out[0].Params)
			}
			if params.ThreadID != "th_1" || params.Ref != "local:th_1" {
				t.Fatalf("turn/started routing = (%q, %q), want the projector's own identity", params.ThreadID, params.Ref)
			}
			// Whatever else the event emits belongs to the turn just announced.
			for _, n := range out[1:] {
				if turnID := notificationTurnIDOf(n); turnID != "" && turnID != turn.ID {
					t.Fatalf("%s carries turn %q, want the announced %q", n.Method, turnID, turn.ID)
				}
			}
		})
	}
}

// The announcement is per open turn, not per event: every event after the one
// that opened the turn rides the turn already announced.
func TestAppEventProjectorAnnouncesAnImplicitTurnOnlyOnce(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	ts := time.Unix(1_700_000_000, 0).UTC()

	opening := projector.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Timestamp: ts, Data: events.AssistantTextStartData{Model: "m"}})
	if countMethod(opening, appwire.NotifyTurnStarted) != 1 {
		t.Fatalf("opening event notifications = %+v, want one turn/started", opening)
	}

	rest := [][]AppNotification{
		projector.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Timestamp: ts, Data: events.AssistantTextDeltaData{Delta: "hi"}}),
		projector.Project(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_1", Timestamp: ts, Data: events.ReasoningSummaryDeltaData{Delta: "hmm"}}),
		projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Timestamp: ts, Data: events.ToolCallStartData{ToolName: "shell", CallID: "call_1"}}),
		projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Timestamp: ts, Data: events.AssistantTextEndData{Text: "hi"}}),
	}
	for _, out := range rest {
		if started := countMethod(out, appwire.NotifyTurnStarted); started != 0 {
			t.Fatalf("re-announced an already open turn: %+v", out)
		}
	}
}

// An explicit opener still announces exactly one turn/started: ensureTurn must
// find the turn EventUserInput just started, not open a second one.
func TestAppEventProjectorExplicitTurnIsAnnouncedOnlyOnce(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	if started := countMethod(out, appwire.NotifyTurnStarted); started != 1 {
		t.Fatalf("turn/started count = %d, want exactly 1 in %+v", started, out)
	}

	follow := projector.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "m"}})
	if started := countMethod(follow, appwire.NotifyTurnStarted); started != 0 {
		t.Fatalf("text start re-announced the user's turn: %+v", follow)
	}
}

// A no-active-turn system announcement keeps its own shape: one complete
// turn/completed for the synthetic prelude/gap turn, never a turn/started.
// Those turns are already over when they reach the wire.
func TestAppEventProjectorSystemAnnouncementTurnIsNotAnnouncedAsStarted(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventPluginLoaded, SessionID: "th_1", Data: events.PluginLoadedData{Name: "p"}})
	if started := countMethod(out, appwire.NotifyTurnStarted); started != 0 {
		t.Fatalf("a system announcement opened a turn: %+v", out)
	}
	if completed := countMethod(out, appwire.NotifyTurnCompleted); completed != 1 {
		t.Fatalf("turn/completed count = %d, want exactly 1 in %+v", completed, out)
	}
}

func countMethod(items []AppNotification, method string) int {
	n := 0
	for _, item := range items {
		if item.Method == method {
			n++
		}
	}
	return n
}

// notificationTurnIDOf reads the turn a notification names, across the several
// params shapes the projector sends, or "" when the notification is not
// turn-scoped.
func notificationTurnIDOf(n AppNotification) string {
	switch params := n.Params.(type) {
	case appwire.TurnStartedParams:
		return params.Turn.ID
	case appwire.ItemLifecycleParams:
		return params.TurnID
	case appwire.AgentMessageDeltaParams:
		return params.TurnID
	case appwire.ReasoningSummaryDeltaParams:
		return params.TurnID
	case map[string]any:
		if turn, ok := params["turn"].(appwire.Turn); ok {
			return turn.ID
		}
	}
	return ""
}
