package server

import (
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
)

func TestAppEventProjectorProjectsAssistantDelta(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	projector.Project(agent.SessionEvent{Kind: agent.EventUserInput, SessionID: "th_1", Data: agent.UserInputData{Text: "hello"}})
	projector.Project(agent.SessionEvent{Kind: agent.EventAssistantTextStart, SessionID: "th_1", Data: agent.AssistantTextStartData{Model: "gpt-5"}})
	out := projector.Project(agent.SessionEvent{Kind: agent.EventAssistantTextDelta, SessionID: "th_1", Data: agent.AssistantTextDeltaData{Delta: "hi"}})

	if len(out) != 1 {
		t.Fatalf("notifications=%+v", out)
	}
	if out[0].Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("method=%q", out[0].Method)
	}
	params, ok := out[0].Params.(appwire.AgentMessageDeltaParams)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params.ThreadID != "th_1" || params.Ref != "local:th_1" || params.TurnID == "" || params.ItemID == "" || params.Delta != "hi" {
		t.Fatalf("params=%+v", params)
	}
}

func TestAppEventProjectorCarriesUserInputTranscriptEntryIndex(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(agent.SessionEvent{Kind: agent.EventUserInput, SessionID: "th_1", Data: agent.UserInputData{Text: "hello", Turn: 3}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.TranscriptEntryIndex != 3 {
		t.Fatalf("transcript entry index=%d, want 3", item.TranscriptEntryIndex)
	}
}

func TestAppEventProjectorCompletesTurnOnSessionEnd(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(agent.SessionEvent{Kind: agent.EventUserInput, SessionID: "th_1", Data: agent.UserInputData{Text: "hello"}})
	assistantEnd := projector.Project(agent.SessionEvent{Kind: agent.EventAssistantTextEnd, SessionID: "th_1", Data: agent.AssistantTextEndData{Text: "hi"}})
	sessionEnd := projector.Project(agent.SessionEvent{Kind: agent.EventSessionEnd, SessionID: "th_1", Data: agent.SessionEndData{Reason: "input_complete", State: "IDLE"}})

	if len(started) == 0 || started[0].Method != appwire.NotifyTurnStarted {
		t.Fatalf("started=%+v", started)
	}
	if hasAppNotification(assistantEnd, appwire.NotifyTurnCompleted) {
		t.Fatalf("assistant end completed turn early: %+v", assistantEnd)
	}
	if !hasAppNotification(sessionEnd, appwire.NotifyTurnCompleted) {
		t.Fatalf("session end did not complete turn: %+v", sessionEnd)
	}
	if !hasAppNotification(sessionEnd, appwire.NotifyThreadStatusChanged) {
		t.Fatalf("session end did not update thread status: %+v", sessionEnd)
	}
}

func TestAppEventProjectorKeepsToolEventsInActiveTurnAfterAssistantText(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(agent.SessionEvent{Kind: agent.EventUserInput, SessionID: "th_1", Data: agent.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	assistantEnd := projector.Project(agent.SessionEvent{Kind: agent.EventAssistantTextEnd, SessionID: "th_1", Data: agent.AssistantTextEndData{Text: "I'll check."}})
	if hasAppNotification(assistantEnd, appwire.NotifyTurnCompleted) {
		t.Fatalf("assistant end completed turn early: %+v", assistantEnd)
	}
	toolStart := projector.Project(agent.SessionEvent{Kind: agent.EventToolCallStart, SessionID: "th_1", Data: agent.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"pwd"}`,
	}})

	if got := notificationItemTurnID(t, toolStart, appwire.NotifyItemStarted); got != turnID {
		t.Fatalf("tool turn_id=%q, want active turn %q (notifications=%+v)", got, turnID, toolStart)
	}
}

func TestAppEventProjectorProjectsCommunicateAsAssistantMessage(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(agent.SessionEvent{Kind: agent.EventUserInput, SessionID: "th_1", Data: agent.UserInputData{Text: "hello"}})

	out := projector.Project(agent.SessionEvent{
		Kind:      agent.EventCommunicate,
		SessionID: "th_1",
		Data:      agent.CommunicateData{Message: "done"},
	})

	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "agent_message" || item.Text != "done" || item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("communicate item=%+v", item)
	}
}

func TestAppEventProjectorSuppressesCommunicateToolEvents(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(agent.SessionEvent{Kind: agent.EventUserInput, SessionID: "th_1", Data: agent.UserInputData{Text: "hello"}})
	projector.Project(agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		SessionID: "th_1",
		Data:      agent.AssistantTextEndData{Text: "done"},
	})

	for _, ev := range []agent.SessionEvent{
		{Kind: agent.EventToolCallStart, SessionID: "th_1", Data: agent.ToolCallStartData{
			ToolName:      "communicate",
			CallID:        "call_1",
			ArgumentsJSON: `{"message":"done"}`,
		}},
		{Kind: agent.EventToolCallOutputDelta, SessionID: "th_1", Data: agent.ToolCallOutputDeltaData{
			ToolName: "communicate",
			CallID:   "call_1",
			Delta:    `{"accepted":true}`,
		}},
		{Kind: agent.EventToolCallEnd, SessionID: "th_1", Data: agent.ToolCallEndData{
			ToolName: "communicate",
			CallID:   "call_1",
			Output:   `{"accepted":true}`,
		}},
	} {
		if out := projector.Project(ev); len(out) != 0 {
			t.Fatalf("%s projected communicate tool notifications: %+v", ev.Kind, out)
		}
	}
}

func TestAppEventProjectorIncludesCallIDOnToolOutputDelta(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(agent.SessionEvent{Kind: agent.EventUserInput, SessionID: "th_1", Data: agent.UserInputData{Text: "hello"}})
	projector.Project(agent.SessionEvent{Kind: agent.EventToolCallStart, SessionID: "th_1", Data: agent.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"pwd"}`,
	}})

	out := projector.Project(agent.SessionEvent{Kind: agent.EventToolCallOutputDelta, SessionID: "th_1", Data: agent.ToolCallOutputDeltaData{
		CallID: "call_1",
		Delta:  "partial\n",
	}})

	if len(out) != 1 || out[0].Method != appwire.NotifyToolOutputDelta {
		t.Fatalf("notifications=%+v", out)
	}
	params, ok := out[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params["callId"] != "call_1" {
		t.Fatalf("callId=%q, want call_1 (params=%+v)", params["callId"], params)
	}
	if params["itemId"] == "" || params["itemId"] == params["callId"] {
		t.Fatalf("itemId should preserve projected item identity separately from callId: %+v", params)
	}
}

func TestAppEventProjectorProjectsSubagentEvents(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(agent.SessionEvent{
		Kind:      agent.EventSubagentStart,
		SessionID: "th_1",
		Data:      agent.SubagentStartData{AgentID: "a1", Task: "inspect"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSubagentStarted {
		t.Fatalf("out=%+v", out)
	}
}

func TestAppEventProjectorProjectsSteeringInjected(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(agent.SessionEvent{
		Kind:      agent.EventSteeringInjected,
		SessionID: "th_1",
		Data:      agent.SteeringInjectedData{Text: "stay focused"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSteeringInjected {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params["threadId"] != "th_1" || params["ref"] != "local:th_1" || params["text"] != "stay focused" {
		t.Fatalf("params=%+v", params)
	}
}

func hasAppNotification(items []AppNotification, method string) bool {
	for _, item := range items {
		if item.Method == method {
			return true
		}
	}
	return false
}

func notificationTurnID(t *testing.T, items []AppNotification, method string) string {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		params, ok := item.Params.(map[string]any)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		turn, ok := params["turn"].(appwire.Turn)
		if !ok {
			t.Fatalf("turn param=%T in %+v", params["turn"], params)
		}
		return turn.ID
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return ""
}

func notificationItemTurnID(t *testing.T, items []AppNotification, method string) string {
	t.Helper()
	return notificationThreadItem(t, items, method).TurnID
}

func notificationThreadItem(t *testing.T, items []AppNotification, method string) appwire.ThreadItem {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		params, ok := item.Params.(map[string]any)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		threadItem, ok := params["item"].(appwire.ThreadItem)
		if !ok {
			t.Fatalf("item param=%T in %+v", params["item"], params)
		}
		return threadItem
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.ThreadItem{}
}
