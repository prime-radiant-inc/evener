package appprojector

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
)

func TestAppEventProjectorProjectsAssistantDelta(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "gpt-5"}})
	out := projector.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "hi"}})

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
	out := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello", Turn: 3}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.TranscriptEntryIndex != 3 {
		t.Fatalf("transcript entry index=%d, want 3", item.TranscriptEntryIndex)
	}
}

func TestAppEventProjectorJSONUsesCodexLifecycleShape(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	started := notificationParamsJSON(t, out, appwire.NotifyTurnStarted)
	var turnStarted struct {
		ThreadID string       `json:"threadId"`
		Ref      string       `json:"ref"`
		Turn     appwire.Turn `json:"turn"`
	}
	if err := json.Unmarshal(started, &turnStarted); err != nil {
		t.Fatalf("turn/started json: %v", err)
	}
	if turnStarted.ThreadID != "th_1" || turnStarted.Ref != "local:th_1" || turnStarted.Turn.Status != appwire.TurnStatusInProgress {
		t.Fatalf("turn/started=%s", started)
	}

	completed := notificationParamsJSON(t, out, appwire.NotifyItemCompleted)
	var itemCompleted struct {
		ThreadID string `json:"threadId"`
		Ref      string `json:"ref"`
		TurnID   string `json:"turnId"`
		Item     struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"item"`
	}
	if err := json.Unmarshal(completed, &itemCompleted); err != nil {
		t.Fatalf("item/completed json: %v", err)
	}
	if itemCompleted.ThreadID != "th_1" || itemCompleted.Ref != "local:th_1" || itemCompleted.Item.Type != "userMessage" || itemCompleted.Item.Text != "hello" || itemCompleted.Item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("item/completed=%s", completed)
	}
}

func TestAppEventProjectorCarriesUserInputImages(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data: events.UserInputData{
			Text: "",
			Images: []events.UserInputImage{{
				MediaType: "image/png",
				Data:      []byte("png"),
				Name:      "shot.png",
			}},
		},
	})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if len(item.Images) != 1 {
		t.Fatalf("images=%+v, want one image", item.Images)
	}
	if item.Images[0].Type != "image" || item.Images[0].MediaType != "image/png" || string(item.Images[0].Data) != "png" || item.Images[0].Name != "shot.png" {
		t.Fatalf("image item=%+v", item.Images[0])
	}
}

func TestAppEventProjectorCompletesActiveTurnBeforeQueuedUserInput(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	first := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "first"}})
	firstTurnID := notificationTurnID(t, first, appwire.NotifyTurnStarted)

	second := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "second"}})
	if len(second) < 2 {
		t.Fatalf("second notifications=%+v", second)
	}
	if second[0].Method != appwire.NotifyTurnCompleted {
		t.Fatalf("first notification=%q, want turn/completed (notifications=%+v)", second[0].Method, second)
	}
	completed := notificationTurn(t, second, appwire.NotifyTurnCompleted)
	if completed.ID != firstTurnID || completed.Status != appwire.TurnStatusCompleted {
		t.Fatalf("completed turn=%+v, want id=%q completed", completed, firstTurnID)
	}
	started := notificationTurn(t, second, appwire.NotifyTurnStarted)
	if started.ID == "" || started.ID == firstTurnID {
		t.Fatalf("queued user input did not start a fresh turn: %+v", started)
	}
	item := notificationThreadItem(t, second, appwire.NotifyItemCompleted)
	if item.TurnID != started.ID || item.Text != "second" {
		t.Fatalf("queued user item=%+v, want turn=%q text=second", item, started.ID)
	}
}

func TestAppEventProjectorGoalContinuationOpensNonUserTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventGoalContinuation, SessionID: "th_1", Data: events.GoalContinuationData{Text: "continue toward the goal"}})

	started := notificationTurn(t, out, appwire.NotifyTurnStarted)
	if started.ID == "" || started.Status != appwire.TurnStatusInProgress {
		t.Fatalf("turn/started=%+v, want a fresh in-progress turn", started)
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type == "userMessage" {
		t.Fatalf("goal continuation rendered a userMessage; continuations must not look like the user spoke (item=%+v)", item)
	}
	if item.Type != "systemMessage" {
		t.Fatalf("goal continuation item type=%q, want systemMessage", item.Type)
	}
	if item.TurnID != started.ID || item.Text != "continue toward the goal" {
		t.Fatalf("goal continuation item=%+v, want turn=%q text=%q", item, started.ID, "continue toward the goal")
	}
}

func TestAppEventProjectorGoalContinuationCompletesActivePriorTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	first := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "do the thing"}})
	firstTurnID := notificationTurnID(t, first, appwire.NotifyTurnStarted)

	out := projector.Project(events.SessionEvent{Kind: events.EventGoalContinuation, SessionID: "th_1", Data: events.GoalContinuationData{Text: "keep going"}})
	if len(out) < 2 || out[0].Method != appwire.NotifyTurnCompleted {
		t.Fatalf("first notification=%+v, want turn/completed to close the prior turn", out)
	}
	completed := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if completed.ID != firstTurnID || completed.Status != appwire.TurnStatusCompleted {
		t.Fatalf("completed turn=%+v, want id=%q completed", completed, firstTurnID)
	}
	started := notificationTurn(t, out, appwire.NotifyTurnStarted)
	if started.ID == "" || started.ID == firstTurnID {
		t.Fatalf("goal continuation did not start a fresh turn: %+v", started)
	}
}

func TestAppEventProjectorGoalEndedRendersSystemAnnouncement(t *testing.T) {
	tests := []struct {
		name     string
		data     events.GoalEndedData
		contains string
	}{
		{
			name:     "achieved",
			data:     events.GoalEndedData{Status: "complete", Iterations: 4},
			contains: "✓ Goal achieved",
		},
		{
			name:     "blocked with reason",
			data:     events.GoalEndedData{Status: "blocked", Reason: "no progress", Iterations: 3},
			contains: "⊘ Goal blocked: no progress",
		},
		{
			name:     "blocked without reason",
			data:     events.GoalEndedData{Status: "blocked", Iterations: 2},
			contains: "⊘ Goal blocked",
		},
		{
			name:     "other terminal status stopped",
			data:     events.GoalEndedData{Status: "weird", Iterations: 1},
			contains: "⊘ Goal stopped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projector := NewAppEventProjector("th_1", "local:th_1")
			out := projector.Project(events.SessionEvent{Kind: events.EventGoalEnded, SessionID: "th_1", Data: tt.data})

			if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
				t.Fatalf("notifications=%+v, want a single systemAnnouncement turn", out)
			}
			turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
			if turn.Status != appwire.TurnStatusCompleted || turn.ItemsView != "full" || len(turn.Items) != 1 {
				t.Fatalf("turn=%+v", turn)
			}
			item := turn.Items[0]
			if item.Type != "systemMessage" || item.Description != "Goal" || item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("item=%+v", item)
			}
			if !strings.Contains(item.Text, tt.contains) {
				t.Fatalf("item text %q does not contain %q", item.Text, tt.contains)
			}
		})
	}
}

func TestAppEventProjectorProjectsThreadLifecycle(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Profile: "openai", Model: "gpt-5"},
	})

	thread := notificationThread(t, started, appwire.NotifyThreadStarted)
	if thread.ID != "th_1" || thread.SessionID != "th_1" || thread.Serf.Ref != "local:th_1" {
		t.Fatalf("started thread identity=%+v", thread)
	}
	if thread.Serf.Profile != "openai" || thread.ModelProvider != "gpt-5" {
		t.Fatalf("started thread model/profile=%+v", thread)
	}
	if status := notificationThreadStatus(t, started, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("started status=%+v, want idle", status)
	}

	closed := projector.Project(events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "th_1",
		Data:      events.SessionEndData{Reason: "done", State: "closed"},
	})
	if !hasAppNotification(closed, appwire.NotifyThreadClosed) {
		t.Fatalf("closed lifecycle missing thread/closed: %+v", closed)
	}
	if status := notificationThreadStatus(t, closed, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusClosed {
		t.Fatalf("closed status=%+v, want closed", status)
	}
}

func TestAppEventProjectorCompletesTurnOnSessionEnd(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	assistantEnd := projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "hi"}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})

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

func TestAppEventProjectorMapsAwaitingSessionEnd(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{
		Reason: "input_complete",
		State:  "awaiting",
	}})

	if hasAppNotification(sessionEnd, appwire.NotifyThreadClosed) {
		t.Fatalf("awaiting SessionEnd emitted thread/closed: %+v", sessionEnd)
	}
	if status := notificationThreadStatus(t, sessionEnd, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("awaiting status=%+v, want awaiting", status)
	}
	for _, n := range sessionEnd {
		if n.Method != appwire.NotifyTurnCompleted {
			continue
		}
		params, ok := n.Params.(map[string]any)
		if !ok {
			t.Fatalf("turnCompleted params=%T", n.Params)
		}
		turn, ok := params["turn"].(appwire.Turn)
		if !ok {
			t.Fatalf("turn=%T", params["turn"])
		}
		if turn.Status != appwire.TurnStatusCompleted {
			t.Fatalf("awaiting turn status=%s, want completed", turn.Status)
		}
		return
	}
	t.Fatalf("awaiting SessionEnd missing turn/completed: %+v", sessionEnd)
}

// TestAppEventProjectorMarksInterruptedTurnCanceled covers kata 0ax1:
// an interrupted turn keeps the thread alive (status=idle) but the
// active turn must be reported as canceled, not completed.
func TestAppEventProjectorMarksInterruptedTurnCanceled(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{
		Reason:      "interrupted",
		State:       "idle",
		Interrupted: true,
	}})

	var sawCanceled bool
	var sawIdle bool
	for _, n := range sessionEnd {
		switch n.Method {
		case appwire.NotifyTurnCompleted:
			params, ok := n.Params.(map[string]any)
			if !ok {
				t.Fatalf("turnCompleted params=%T", n.Params)
			}
			turn, ok := params["turn"].(appwire.Turn)
			if !ok {
				t.Fatalf("turn=%T", params["turn"])
			}
			if turn.Status == appwire.TurnStatusInterrupted {
				sawCanceled = true
			}
		case appwire.NotifyThreadStatusChanged:
			params, ok := n.Params.(appwire.ThreadStatusChangedParams)
			if !ok {
				t.Fatalf("threadStatus params=%T", n.Params)
			}
			if params.Status.Type == appwire.ThreadStatusIdle {
				sawIdle = true
			}
		}
	}
	if !sawCanceled {
		t.Fatalf("interrupted SessionEnd did not mark turn canceled: %+v", sessionEnd)
	}
	if !sawIdle {
		t.Fatalf("interrupted SessionEnd did not flip thread status to idle: %+v", sessionEnd)
	}
}

func TestAppEventProjectorLetsInterruptedSessionEndCancelAfterContextCanceledError(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	errOut := projector.Project(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data:      events.ErrorData{Error: "context canceled"},
	})
	if !hasAppNotification(errOut, appwire.NotifyWarning) {
		t.Fatalf("context canceled EventError missing warning: %+v", errOut)
	}
	if hasAppNotification(errOut, appwire.NotifyTurnCompleted) {
		t.Fatalf("context canceled EventError completed turn before interrupted SessionEnd: %+v", errOut)
	}

	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{
		Reason:      "interrupted",
		State:       "idle",
		Interrupted: true,
	}})
	for _, n := range sessionEnd {
		if n.Method != appwire.NotifyTurnCompleted {
			continue
		}
		params, ok := n.Params.(map[string]any)
		if !ok {
			t.Fatalf("turnCompleted params=%T", n.Params)
		}
		turn, ok := params["turn"].(appwire.Turn)
		if !ok {
			t.Fatalf("turn=%T", params["turn"])
		}
		if turn.Status != appwire.TurnStatusInterrupted {
			t.Fatalf("turn status=%s, want canceled", turn.Status)
		}
		return
	}
	t.Fatalf("interrupted SessionEnd did not complete the active turn: %+v", sessionEnd)
}

func TestAppEventProjectorKeepsToolEventsInActiveTurnAfterAssistantText(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	assistantEnd := projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "I'll check."}})
	if hasAppNotification(assistantEnd, appwire.NotifyTurnCompleted) {
		t.Fatalf("assistant end completed turn early: %+v", assistantEnd)
	}
	toolStart := projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"pwd"}`,
	}})

	if got := notificationItemTurnID(t, toolStart, appwire.NotifyItemStarted); got != turnID {
		t.Fatalf("tool turn_id=%q, want active turn %q (notifications=%+v)", got, turnID, toolStart)
	}
}

func TestAppEventProjectorCarriesToolDescription(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"pwd"}`,
		Description:   "Check the working directory.",
	}})

	item := notificationThreadItem(t, out, appwire.NotifyItemStarted)
	if item.Description != "Check the working directory." {
		t.Fatalf("tool description=%q", item.Description)
	}
}

func TestAppEventProjectorProjectsCommunicateAsAssistantMessage(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventCommunicate,
		SessionID: "th_1",
		Data:      events.CommunicateData{Message: "done"},
	})

	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "agentMessage" || item.Text != "done" || item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("communicate item=%+v", item)
	}
}

func TestAppEventProjectorSuppressesCommunicateToolEvents(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{
		Kind:      events.EventAssistantTextEnd,
		SessionID: "th_1",
		Data:      events.AssistantTextEndData{Text: "done"},
	})

	for _, ev := range []events.SessionEvent{
		{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
			ToolName:      "communicate",
			CallID:        "call_1",
			ArgumentsJSON: `{"message":"done"}`,
		}},
		{Kind: events.EventToolCallOutputDelta, SessionID: "th_1", Data: events.ToolCallOutputDeltaData{
			ToolName: "communicate",
			CallID:   "call_1",
			Delta:    `{"accepted":true}`,
		}},
		{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
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
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "shell",
		CallID:        "call_1",
		ArgumentsJSON: `{"command":"pwd"}`,
	}})

	out := projector.Project(events.SessionEvent{Kind: events.EventToolCallOutputDelta, SessionID: "th_1", Data: events.ToolCallOutputDeltaData{
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
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSubagentStart,
		SessionID: "th_1",
		Data:      events.SubagentStartData{AgentID: "a1", Task: "inspect"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSubagentStarted {
		t.Fatalf("out=%+v", out)
	}
}

// TestAppEventProjectorProjectsQueueChanged (kata r80p) verifies the
// projector wraps QUEUE_CHANGED into a thread/queueChanged appwire
// notification carrying the authoritative depth + first-line-truncated
// preview.
func TestAppEventProjectorProjectsQueueChanged(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventQueueChanged,
		SessionID: "th_1",
		Data: events.QueueChangedData{
			Depth:   2,
			Preview: []string{"first line", "second"},
		},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyThreadQueueChanged {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(appwire.ThreadQueueChangedParams)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params.ThreadID != "th_1" || params.Ref != "local:th_1" {
		t.Fatalf("params identity=%+v", params)
	}
	if params.Queue.Depth != 2 {
		t.Fatalf("depth=%d, want 2", params.Queue.Depth)
	}
	if len(params.Queue.Preview) != 2 || params.Queue.Preview[0] != "first line" || params.Queue.Preview[1] != "second" {
		t.Fatalf("preview=%+v", params.Queue.Preview)
	}
}

func TestAppEventProjectorProjectsSteeringInjected(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data:      events.SteeringInjectedData{Text: "stay focused"},
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

func TestAppEventProjectorProjectsCompactionTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventCompactionTurn,
		SessionID: "th_1",
		Data: events.CompactionTurnData{
			Kind: string(schema.TurnSummary),
			Text: "[CONTEXT SUMMARY]\nkept the useful state",
		},
	})

	if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
	if turn.ID == "" || turn.Status != appwire.TurnStatusCompleted || turn.ItemsView != "full" || len(turn.Items) != 1 {
		t.Fatalf("turn=%+v", turn)
	}
	item := turn.Items[0]
	if item.TurnID != turn.ID || item.Type != "systemMessage" || item.Description != "Context summary" || item.Text != "[CONTEXT SUMMARY]\nkept the useful state" || item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("item=%+v", item)
	}
}

func TestAppEventProjectorProjectsCompactionTurnInActiveTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventCompactionTurn,
		SessionID: "th_1",
		Data: events.CompactionTurnData{
			Kind: string(schema.TurnCheckpoint),
			Text: "[CONTEXT CHECKPOINT]\nkept raw context",
		},
	})

	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.TurnID != turnID || item.Type != "systemMessage" || item.Description != "Context checkpoint" || item.Text != "[CONTEXT CHECKPOINT]\nkept raw context" || item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("item=%+v", item)
	}
}

func TestAppEventProjectorProjectsAgentOnlyEventsAsSystemAnnouncements(t *testing.T) {
	tests := []struct {
		name        string
		event       events.SessionEvent
		description string
		contains    []string
		notContains []string
		singleLine  bool
	}{
		{
			name:        "turn limit max turns",
			event:       events.SessionEvent{Kind: events.EventTurnLimit, SessionID: "th_1", Data: events.TurnLimitData{MaxTurns: 3}},
			description: "Turn limit",
			contains:    []string{"Maximum turns reached: 3"},
		},
		{
			name:        "turn limit tool rounds",
			event:       events.SessionEvent{Kind: events.EventTurnLimit, SessionID: "th_1", Data: events.TurnLimitData{MaxToolRoundsPerInput: 7}},
			description: "Turn limit",
			contains:    []string{"Maximum tool rounds per input reached: 7"},
		},
		{
			name:        "loop detection",
			event:       events.SessionEvent{Kind: events.EventLoopDetection, SessionID: "th_1", Data: events.LoopDetectionData{Message: "Repeated tool pattern detected"}},
			description: "Loop detection",
			contains:    []string{"Repeated tool pattern detected"},
		},
		{
			name:        "skill activated",
			event:       events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "using-superpowers"}},
			description: "Skill activated",
			contains:    []string{"using-superpowers"},
		},
		{
			name: "context compaction",
			event: events.SessionEvent{Kind: events.EventContextCompaction, SessionID: "th_1", Data: events.ContextCompactionData{
				Layer:           "L4",
				TurnsBefore:     42,
				TurnsAfter:      8,
				EstTokensBefore: 120000,
				EstTokensAfter:  23000,
			}},
			description: "Context compaction",
			contains:    []string{"Layer: L4", "Turns: 42 -> 8", "Estimated tokens: 120000 -> 23000"},
		},
		{
			name: "plugin loaded",
			event: events.SessionEvent{Kind: events.EventPluginLoaded, SessionID: "th_1", Data: events.PluginLoadedData{
				Name: "superpowers", SkillCount: 5, AgentCount: 2, MCPCount: 1,
			}},
			description: "Plugin loaded",
			contains:    []string{"superpowers", "5 skills", "2 agents", "1 MCP"},
		},
		{
			name: "hook end",
			event: events.SessionEvent{Kind: events.EventHookEnd, SessionID: "th_1", Data: events.HookEndData{
				Event: "SessionStart", HookType: "command", Matcher: "using-superpowers", PluginName: "superpowers", ExitCode: 0, DurationMS: 37,
			}},
			description: "Hook",
			contains:    []string{"SessionStart hook", "using-superpowers", "superpowers", "command", "exit 0"},
			notContains: []string{"37ms"},
			singleLine:  true,
		},
		{
			name:        "fork summary",
			event:       events.SessionEvent{Kind: events.EventForkSummary, SessionID: "th_1", Data: events.ForkSummaryData{Turn: 12}},
			description: "Fork summary",
			contains:    []string{"turn 12"},
		},
		{
			name:        "prompt loaded",
			event:       events.SessionEvent{Kind: events.EventPromptLoaded, SessionID: "th_1", Data: events.PromptLoadedData{Label: "system.md", Size: 2048}},
			description: "Prompt loaded",
			contains:    []string{"system.md", "2048 B"},
		},
		{
			name: "round timings",
			event: events.SessionEvent{Kind: events.EventRoundTimings, SessionID: "th_1", Data: events.RoundTimings{
				Round:        2,
				TotalRound:   1500 * time.Millisecond,
				LLMCall:      1200 * time.Millisecond,
				ContextMgmt:  25 * time.Millisecond,
				ToolExec:     40 * time.Millisecond,
				LoopOverhead: 5 * time.Millisecond,
			}},
			description: "Round timings",
			contains:    []string{"Round 2", "total=1.5s", "llm=1.2s", "context=25ms", "tools=40ms"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projector := NewAppEventProjector("th_1", "local:th_1")
			out := projector.Project(tt.event)

			if len(out) != 1 || out[0].Method != appwire.NotifyTurnCompleted {
				t.Fatalf("notifications=%+v", out)
			}
			turn := notificationTurn(t, out, appwire.NotifyTurnCompleted)
			if turn.Status != appwire.TurnStatusCompleted || turn.ItemsView != "full" || len(turn.Items) != 1 {
				t.Fatalf("turn=%+v", turn)
			}
			item := turn.Items[0]
			if item.Type != "systemMessage" || item.Description != tt.description || item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("item=%+v", item)
			}
			for _, want := range tt.contains {
				if !strings.Contains(item.Text, want) {
					t.Fatalf("item text %q does not contain %q", item.Text, want)
				}
			}
			for _, unwanted := range tt.notContains {
				if strings.Contains(item.Text, unwanted) {
					t.Fatalf("item text %q contains unwanted %q", item.Text, unwanted)
				}
			}
			if tt.singleLine && strings.Contains(item.Text, "\n") {
				t.Fatalf("item text %q should be one line", item.Text)
			}
		})
	}
}

func TestAppEventProjectorDoesNotDisplayHookStart(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{Kind: events.EventHookStart, SessionID: "th_1", Data: events.HookStartData{
		Event: "SessionStart", HookType: "command", Matcher: "using-superpowers", PluginName: "superpowers",
	}})

	if len(out) != 0 {
		t.Fatalf("hook start should not project appwire notifications: %+v", out)
	}
}

func TestAppEventProjectorProjectsAgentOnlyAnnouncementInActiveTurn(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSkillActivated,
		SessionID: "th_1",
		Data:      events.SkillActivatedData{Name: "using-superpowers"},
	})

	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.TurnID != turnID || item.Type != "systemMessage" || item.Description != "Skill activated" || !strings.Contains(item.Text, "using-superpowers") {
		t.Fatalf("item=%+v", item)
	}
}

func TestAppEventProjectorProjectsImageOnlySteeringInjected(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventSteeringInjected,
		SessionID: "th_1",
		Data: events.SteeringInjectedData{Images: []events.UserInputImage{{
			MediaType: "image/png",
			Data:      []byte("png"),
			Name:      "shot.png",
		}}},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfSteeringInjected {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(map[string]any)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params["text"] != "[image]" {
		t.Fatalf("text=%q, want [image]", params["text"])
	}
	images, ok := params["images"].([]appwire.InputItem)
	if !ok || len(images) != 1 || images[0].MediaType != "image/png" {
		t.Fatalf("images=%+v", params["images"])
	}
}

// TestProjector_ForwardsProviderCause (kata cmfz) verifies that when an
// EventError carries a structured ErrorCause (populated by agent.Session
// when the underlying error is a typed llm.Error), the projector forwards
// the cause into the NotifyWarning envelope so consumers can typed-branch
// on Cause.Kind instead of substring-matching the message.
func TestProjector_ForwardsProviderCause(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data: events.ErrorData{
			Error: "anthropic: 503 service unavailable",
			Cause: &events.ErrorCause{
				Kind:     "provider",
				Provider: "anthropic",
				Model:    "claude-opus-4-7",
				Status:   503,
			},
		},
	})

	var warning *AppNotification
	for i := range out {
		if out[i].Method == appwire.NotifyWarning {
			warning = &out[i]
			break
		}
	}
	if warning == nil {
		t.Fatalf("no warning notification: %+v", out)
	}
	params, ok := warning.Params.(map[string]any)
	if !ok {
		t.Fatalf("warning params=%T", warning.Params)
	}
	cause, ok := params["cause"].(*appwire.DiagnosticCause)
	if !ok || cause == nil {
		t.Fatalf("cause field missing/typed-wrong: got %T (params=%+v)", params["cause"], params)
	}
	if cause.Kind != "provider" {
		t.Fatalf("cause.Kind=%q, want provider", cause.Kind)
	}
	if cause.Provider != "anthropic" {
		t.Fatalf("cause.Provider=%q, want anthropic", cause.Provider)
	}
	if cause.Model != "claude-opus-4-7" {
		t.Fatalf("cause.Model=%q, want claude-opus-4-7", cause.Model)
	}
	if cause.Status != 503 {
		t.Fatalf("cause.Status=%d, want 503", cause.Status)
	}
	var completed *AppNotification
	for i := range out {
		if out[i].Method == appwire.NotifyTurnCompleted {
			completed = &out[i]
			break
		}
	}
	if completed == nil {
		t.Fatalf("no turn/completed notification: %+v", out)
	}
	completedParams, ok := completed.Params.(map[string]any)
	if !ok {
		t.Fatalf("completed params=%T", completed.Params)
	}
	turn, ok := completedParams["turn"].(appwire.Turn)
	if !ok {
		t.Fatalf("completed turn=%T", completedParams["turn"])
	}
	if turn.Error == nil || turn.Error.Cause == nil {
		t.Fatalf("turn error cause missing: %+v", turn.Error)
	}
	if turn.Error.Cause.Provider != "anthropic" || turn.Error.Cause.Model != "claude-opus-4-7" || turn.Error.Cause.Status != 503 {
		t.Fatalf("turn error cause=%+v", turn.Error.Cause)
	}
}

// TestProjector_OmitsCauseWhenAbsent (kata cmfz) verifies that when an
// EventError has no structured cause, the projector does not invent one
// and the warning envelope's cause field stays absent (or nil).
func TestProjector_OmitsCauseWhenAbsent(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data:      events.ErrorData{Error: "something else broke"},
	})

	var warning *AppNotification
	for i := range out {
		if out[i].Method == appwire.NotifyWarning {
			warning = &out[i]
			break
		}
	}
	if warning == nil {
		t.Fatalf("no warning notification: %+v", out)
	}
	params, ok := warning.Params.(map[string]any)
	if !ok {
		t.Fatalf("warning params=%T", warning.Params)
	}
	raw, present := params["cause"]
	if present && raw != nil {
		// A *appwire.DiagnosticCause that is non-nil would be a defect.
		if cause, ok := raw.(*appwire.DiagnosticCause); !ok || cause != nil {
			t.Fatalf("expected cause absent or nil, got %T %+v", raw, raw)
		}
	}
}

// TestProjector_BackcompatNonProviderError (kata cmfz) regression-locks
// the pre-existing warning envelope projection — message, an explicit
// hub source, title, and hint must still pass through unchanged after
// the cause field is added.
func TestProjector_BackcompatNonProviderError(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})

	out := projector.Project(events.SessionEvent{
		Kind:      events.EventError,
		SessionID: "th_1",
		Data: events.ErrorData{
			Error:  "subscribe failed",
			Source: "hub",
			Title:  "Live updates unavailable",
			Hint:   "Retry the action.",
		},
	})

	var warning *AppNotification
	for i := range out {
		if out[i].Method == appwire.NotifyWarning {
			warning = &out[i]
			break
		}
	}
	if warning == nil {
		t.Fatalf("no warning notification: %+v", out)
	}
	params, ok := warning.Params.(map[string]any)
	if !ok {
		t.Fatalf("warning params=%T", warning.Params)
	}
	if params["message"] != "subscribe failed" {
		t.Fatalf("message=%v", params["message"])
	}
	if params["source"] != "hub" {
		t.Fatalf("source=%v", params["source"])
	}
	if params["title"] != "Live updates unavailable" {
		t.Fatalf("title=%v", params["title"])
	}
	if params["hint"] != "Retry the action." {
		t.Fatalf("hint=%v", params["hint"])
	}
	// Cause should be absent since this EventError did not carry one.
	if raw, present := params["cause"]; present && raw != nil {
		if cause, ok := raw.(*appwire.DiagnosticCause); !ok || cause != nil {
			t.Fatalf("expected cause absent or nil, got %T %+v", raw, raw)
		}
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

func notificationParamsJSON(t *testing.T, items []AppNotification, method string) []byte {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		data, err := json.Marshal(item.Params)
		if err != nil {
			t.Fatalf("marshal params for %s: %v", method, err)
		}
		return data
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return nil
}

func notificationTurnID(t *testing.T, items []AppNotification, method string) string {
	t.Helper()
	return notificationTurn(t, items, method).ID
}

func notificationTurn(t *testing.T, items []AppNotification, method string) appwire.Turn {
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
		return turn
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.Turn{}
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

func notificationThread(t *testing.T, items []AppNotification, method string) appwire.Thread {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		params, ok := item.Params.(map[string]any)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		thread, ok := params["thread"].(appwire.Thread)
		if !ok {
			t.Fatalf("thread param=%T in %+v", params["thread"], params)
		}
		return thread
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.Thread{}
}

func notificationThreadStatus(t *testing.T, items []AppNotification, method string) appwire.ThreadStatus {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		params, ok := item.Params.(appwire.ThreadStatusChangedParams)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		return params.Status
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.ThreadStatus{}
}
