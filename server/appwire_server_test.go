package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
)

func TestServerAppWireTurnStartQueuesInput(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:    "local:th_1",
		Prompt: "hello",
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	select {
	case msg := <-srv.InputCh():
		if msg.Text != "hello" {
			t.Fatalf("text=%q", msg.Text)
		}
	default:
		t.Fatal("input was not queued")
	}
}

func TestServerAppWireTurnStartIDMatchesProjectedNotifications(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventUserInput,
		SessionID: "th_1",
		Data:      agent.UserInputData{Text: "earlier"},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		SessionID: "th_1",
		Data:      agent.AssistantTextEndData{Text: "done"},
	})
	history := srv.AppNotificationsAfter(0, "th_1")
	cursor := history[len(history)-1].Seq

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:    "local:th_1",
		Prompt: "hello",
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	startResp, ok := resp.Response.Result.(appwire.TurnStartResponse)
	if !ok {
		t.Fatalf("response result=%T", resp.Response.Result)
	}

	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventUserInput,
		SessionID: "th_1",
		Data:      agent.UserInputData{Text: "hello"},
	})

	notifications := srv.AppNotificationsAfter(cursor, "th_1")
	var startedID string
	var itemTurnID string
	for _, item := range notifications {
		switch item.Notification.Method {
		case appwire.NotifyTurnStarted:
			var params struct {
				Turn appwire.Turn `json:"turn"`
			}
			if err := json.Unmarshal(item.Notification.Params, &params); err != nil {
				t.Fatalf("turn started params: %v", err)
			}
			startedID = params.Turn.ID
		case appwire.NotifyItemCompleted:
			var params struct {
				Item appwire.ThreadItem `json:"item"`
			}
			if err := json.Unmarshal(item.Notification.Params, &params); err != nil {
				t.Fatalf("item completed params: %v", err)
			}
			if params.Item.Type == "user_message" && params.Item.Text == "hello" {
				itemTurnID = params.Item.TurnID
			}
		}
	}
	if startedID != startResp.Turn.ID {
		t.Fatalf("turn/start id=%q, turn/started id=%q", startResp.Turn.ID, startedID)
	}
	if itemTurnID != startResp.Turn.ID {
		t.Fatalf("turn/start id=%q, user item turn id=%q", startResp.Turn.ID, itemTurnID)
	}
}

func TestServerAppWireThreadReadIncludesProjectedTurns(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventUserInput,
		SessionID: "th_1",
		Data:      agent.UserInputData{Text: "hello"},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		SessionID: "th_1",
		Data:      agent.AssistantTextEndData{Text: "hi there"},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventSessionEnd,
		SessionID: "th_1",
		Data:      agent.SessionEndData{Reason: "input_complete", State: "IDLE"},
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, ItemsView: "full"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if len(data.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", data.Thread.Turns)
	}
	turn := data.Thread.Turns[0]
	if turn.Status != appwire.TurnStatusCompleted || turn.ItemsView != "full" {
		t.Fatalf("turn=%+v", turn)
	}
	if len(turn.Items) != 2 {
		t.Fatalf("items=%+v", turn.Items)
	}
	if turn.Items[0].Type != "user_message" || turn.Items[0].Text != "hello" {
		t.Fatalf("user item=%+v", turn.Items[0])
	}
	if turn.Items[1].Type != "agent_message" || turn.Items[1].Text != "hi there" {
		t.Fatalf("agent item=%+v", turn.Items[1])
	}
}

func TestServerAppWireThreadReadIncludesInProgressDeltas(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventUserInput,
		SessionID: "th_1",
		Data:      agent.UserInputData{Text: "run"},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextStart,
		SessionID: "th_1",
		Data:      agent.AssistantTextStartData{},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      agent.AssistantTextDeltaData{Delta: "partial "},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      agent.AssistantTextDeltaData{Delta: "answer"},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventToolCallStart,
		SessionID: "th_1",
		Data:      agent.ToolCallStartData{ToolName: "shell", CallID: "call_1", ArgumentsJSON: `{"cmd":"go test"}`},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventToolCallOutputDelta,
		SessionID: "th_1",
		Data:      agent.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call_1", Delta: "ok "},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventToolCallOutputDelta,
		SessionID: "th_1",
		Data:      agent.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call_1", Delta: "done"},
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, ItemsView: "full"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if len(data.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", data.Thread.Turns)
	}
	var agentItem, toolItem *appwire.ThreadItem
	for i := range data.Thread.Turns[0].Items {
		item := &data.Thread.Turns[0].Items[i]
		switch item.Type {
		case "agent_message":
			agentItem = item
		case "tool_call":
			toolItem = item
		}
	}
	if agentItem == nil || agentItem.Text != "partial answer" || agentItem.Status != appwire.TurnStatusRunning {
		t.Fatalf("agent item=%+v", agentItem)
	}
	if toolItem == nil || toolItem.Output != "ok done" || toolItem.Status != appwire.TurnStatusRunning {
		t.Fatalf("tool item=%+v", toolItem)
	}
}

func TestServerAppWireThreadReadMergesCompletionItemsWithDeltas(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventUserInput,
		SessionID: "th_1",
		Data:      agent.UserInputData{Text: "run"},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextStart,
		SessionID: "th_1",
		Data:      agent.AssistantTextStartData{},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      agent.AssistantTextDeltaData{Delta: "partial "},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      agent.AssistantTextDeltaData{Delta: "answer"},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		SessionID: "th_1",
		Data:      agent.AssistantTextEndData{},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventToolCallStart,
		SessionID: "th_1",
		Data:      agent.ToolCallStartData{ToolName: "shell", CallID: "call_1", ArgumentsJSON: `{"cmd":"go test"}`},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventToolCallOutputDelta,
		SessionID: "th_1",
		Data:      agent.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call_1", Delta: "ok "},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventToolCallOutputDelta,
		SessionID: "th_1",
		Data:      agent.ToolCallOutputDeltaData{ToolName: "shell", CallID: "call_1", Delta: "done"},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventToolCallEnd,
		SessionID: "th_1",
		Data:      agent.ToolCallEndData{ToolName: "shell", CallID: "call_1"},
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, ItemsView: "full"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if len(data.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", data.Thread.Turns)
	}
	var agentItem, toolItem *appwire.ThreadItem
	for i := range data.Thread.Turns[0].Items {
		item := &data.Thread.Turns[0].Items[i]
		switch item.Type {
		case "agent_message":
			agentItem = item
		case "tool_call":
			toolItem = item
		}
	}
	if agentItem == nil || agentItem.Text != "partial answer" || agentItem.Status != appwire.TurnStatusCompleted {
		t.Fatalf("agent item=%+v", agentItem)
	}
	if toolItem == nil || toolItem.Output != "ok done" || toolItem.Status != appwire.TurnStatusCompleted {
		t.Fatalf("tool item=%+v", toolItem)
	}
}

func TestServerAppWireThreadReadUsesCommunicateAsAssistantMessage(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	for _, ev := range []agent.SessionEvent{
		{Kind: agent.EventUserInput, SessionID: "th_1", Data: agent.UserInputData{Text: "hello"}},
		{Kind: agent.EventToolCallStart, SessionID: "th_1", Data: agent.ToolCallStartData{
			ToolName:      "communicate",
			CallID:        "call_1",
			ArgumentsJSON: `{"message":"done","await_reply":false}`,
		}},
		{Kind: agent.EventCommunicate, SessionID: "th_1", Data: agent.CommunicateData{Message: "done"}},
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
		{Kind: agent.EventSessionEnd, SessionID: "th_1", Data: agent.SessionEndData{Reason: "input_complete", State: "IDLE"}},
	} {
		srv.RecordAppEvent(ev)
	}

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", IncludeTurns: true, ItemsView: "full"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if len(data.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", data.Thread.Turns)
	}
	var agentMessages, communicateTools int
	for _, item := range data.Thread.Turns[0].Items {
		if item.Type == "agent_message" && item.Text == "done" {
			agentMessages++
		}
		if item.Type == "tool_call" && item.ToolName == "communicate" {
			communicateTools++
		}
	}
	if agentMessages != 1 || communicateTools != 0 {
		t.Fatalf("items=%+v, want one agent message and no communicate tool", data.Thread.Turns[0].Items)
	}
}

func TestServerAppWireInitializeDoesNotAdvertiseUnsupportedTurnList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	conn := srv.AppServer().NewConnection("test")
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.InitializeResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if data.Features.ThreadTurnsList {
		t.Fatalf("ThreadTurnsList advertised without handlers: %+v", data.Features)
	}
}

func TestServerAppWireTurnSteerRejectsMismatchedTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var steered []string
	srv.SetSteerFunc(func(text string) {
		steered = append(steered, text)
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:    "local:th_1",
		Prompt: "hello",
	}))
	startResp := start.Response.Result.(appwire.TurnStartResponse)

	bad := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:    "local:th_1",
		TurnID: startResp.Turn.ID + "-stale",
		Text:   "wrong turn",
	}))
	if bad.Kind() != appwire.MessageError {
		t.Fatalf("bad steer response=%v", bad.Kind())
	}
	if len(steered) != 0 {
		t.Fatalf("stale steer invoked handler: %v", steered)
	}

	good := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(4), appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:    "local:th_1",
		TurnID: startResp.Turn.ID,
		Text:   "right turn",
	}))
	if good.Kind() != appwire.MessageResponse {
		t.Fatalf("good steer response=%v error=%+v", good.Kind(), good.Error)
	}
	if len(steered) != 1 || steered[0] != "right turn" {
		t.Fatalf("steered=%v", steered)
	}
}

func TestServerAppWireTurnSteerRequiresTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetSteerFunc(func(string) {})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:  "local:th_1",
		Text: "missing turn",
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("steer without turn id response=%v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
}

func TestServerAppWireTurnInterruptRequiresActiveTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	cancelled := 0
	srv.SetCancelFunc(func() { cancelled++ })

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	start := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:    "local:th_1",
		Prompt: "hello",
	}))
	startResp := start.Response.Result.(appwire.TurnStartResponse)

	missing := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref: "local:th_1",
	}))
	if missing.Kind() != appwire.MessageError || missing.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("interrupt without turn id=%+v", missing)
	}
	stale := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(4), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:    "local:th_1",
		TurnID: startResp.Turn.ID + "-stale",
	}))
	if stale.Kind() != appwire.MessageError || stale.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("stale interrupt=%+v", stale)
	}
	good := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(5), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:    "local:th_1",
		TurnID: startResp.Turn.ID,
	}))
	if good.Kind() != appwire.MessageResponse {
		t.Fatalf("good interrupt=%+v", good)
	}
	if cancelled != 1 {
		t.Fatalf("cancelled=%d, want 1", cancelled)
	}
}

func TestServerAppWireThreadModelSetQualifiesProvider(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	var got string
	srv.SetModelFunc(func(model string) {
		got = model
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{
		Ref:           "local:th_1",
		ModelProvider: "openai",
		Model:         "gpt-5",
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if got != "openai/gpt-5" {
		t.Fatalf("model=%q, want openai/gpt-5", got)
	}
}

func TestAppStatusPreservesAttentionStates(t *testing.T) {
	tests := map[string]string{
		"AWAITING_INPUT": "awaiting",
		"AWAITING_REPLY": "awaiting",
		"WARNING":        "warning",
		"ERRORED":        appwire.ThreadStatusError,
	}
	for state, want := range tests {
		if got := appStatus(state, false); got != want {
			t.Fatalf("appStatus(%q)=%q, want %q", state, got, want)
		}
	}
}

func TestServerAppWireTurnStartRejectsClosedSession(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetState("CLOSED")

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:    "local:th_1",
		Prompt: "hello",
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("resp=%v", resp.Kind())
	}
	if resp.Error == nil || resp.Error.Error.Message != "session is closed" {
		t.Fatalf("error=%+v", resp.Error)
	}
	select {
	case msg := <-srv.InputCh():
		t.Fatalf("input should not be queued: %+v", msg)
	default:
	}
}

func TestServerAppWireErrorEventNotifiesSubscribers(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	defer httpServer.Close()
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(context.Background())

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventUserInput,
		SessionID: "th_1",
		Data:      agent.UserInputData{Text: "hello"},
	})
	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventError,
		SessionID: "th_1",
		Data:      agent.ErrorData{Error: "provider unavailable"},
	})

	var sawWarning bool
	var sawFailedTurn bool
	deadline := time.After(time.Second)
	for !sawWarning || !sawFailedTurn {
		select {
		case got := <-client.Notifications():
			switch got.Method {
			case appwire.NotifyWarning:
				var params struct {
					Message string `json:"message"`
					Source  string `json:"source"`
				}
				if err := json.Unmarshal(got.Params, &params); err != nil {
					t.Fatalf("warning params: %v", err)
				}
				if params.Message != "provider unavailable" {
					t.Fatalf("warning message=%q", params.Message)
				}
				if params.Source != string(diagnostic.SourceProvider) {
					t.Fatalf("warning source=%q", params.Source)
				}
				sawWarning = true
			case appwire.NotifyTurnCompleted:
				var params struct {
					Turn appwire.Turn `json:"turn"`
				}
				if err := json.Unmarshal(got.Params, &params); err != nil {
					t.Fatalf("turn params: %v", err)
				}
				if params.Turn.Status == appwire.TurnStatusFailed && params.Turn.Error != nil && params.Turn.Error.Message == "provider unavailable" && params.Turn.Error.Source == string(diagnostic.SourceProvider) {
					sawFailedTurn = true
				}
			}
		case <-deadline:
			t.Fatalf("missing notifications: warning=%v failedTurn=%v", sawWarning, sawFailedTurn)
		}
	}
}

func TestServerAppWireThreadReadReturnsStatus(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetStatus(StatusInfo{
		SessionID:  "sess_1",
		State:      "IDLE",
		Model:      "gpt-5",
		Profile:    "openai",
		WorkingDir: "/tmp/project",
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	data, ok := resp.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("result=%T", resp.Response.Result)
	}
	if data.Thread.ID != "th_1" || data.Thread.SessionID != "sess_1" || data.Thread.Serf.Ref != "local:th_1" {
		t.Fatalf("thread=%+v", data.Thread)
	}
	if data.Thread.ModelProvider != "gpt-5" || data.Thread.Serf.Profile != "openai" {
		t.Fatalf("thread model/profile=%+v", data.Thread)
	}
}

func TestServerAppWireThreadShutdownInvokesCallback(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	done := make(chan struct{}, 1)
	srv.SetShutdownFunc(func() { done <- struct{}{} })

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not invoked")
	}
}

func TestServerAppWireThreadReadSubscribesForNotifications(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	httpServer := httptest.NewServer(http.HandlerFunc(srv.AppServer().ServeWebSocket))
	defer httpServer.Close()
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(context.Background())

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}

	srv.RecordAppEvent(agent.SessionEvent{
		Kind:      agent.EventAssistantTextDelta,
		SessionID: "sess_1",
		Data:      agent.AssistantTextDeltaData{Delta: "hi"},
	})

	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification")
	}
}
