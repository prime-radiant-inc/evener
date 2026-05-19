package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
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
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
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

func TestServerAppWireTurnStartAcceptsCodexInput(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	init := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	if init.Kind() != appwire.MessageResponse {
		t.Fatalf("init=%v", init.Kind())
	}
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{
		ThreadID: "th_1",
		Input: []appwire.InputItem{
			{Type: "text", Text: "hello"},
			{Type: "image", MediaType: "image/png", Data: []byte("png"), Name: "shot.png"},
		},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	select {
	case msg := <-srv.InputCh():
		if msg.Text != "hello" {
			t.Fatalf("text=%q", msg.Text)
		}
		if len(msg.Images) != 1 || msg.Images[0].MediaType != "image/png" || string(msg.Images[0].Data) != "png" || msg.Images[0].Name != "shot.png" {
			t.Fatalf("images=%+v", msg.Images)
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
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
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
			if params.Item.Type == "userMessage" && params.Item.Text == "hello" {
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
	if turn.Items[0].Type != "userMessage" || turn.Items[0].Text != "hello" {
		t.Fatalf("user item=%+v", turn.Items[0])
	}
	if turn.Items[1].Type != "agentMessage" || turn.Items[1].Text != "hi there" {
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
		case "agentMessage":
			agentItem = item
		case "commandExecution":
			toolItem = item
		}
	}
	if agentItem == nil || agentItem.Text != "partial answer" || agentItem.Status != appwire.TurnStatusInProgress {
		t.Fatalf("agent item=%+v", agentItem)
	}
	if toolItem == nil || toolItem.Output != "ok done" || toolItem.Status != appwire.TurnStatusInProgress {
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
		case "agentMessage":
			agentItem = item
		case "commandExecution":
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
		if item.Type == "agentMessage" && item.Text == "done" {
			agentMessages++
		}
		if item.Type == "commandExecution" && item.ToolName == "communicate" {
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
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	startResp := start.Response.Result.(appwire.TurnStartResponse)

	bad := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID + "-stale",
		Input:          []appwire.InputItem{{Type: "text", Text: "wrong turn"}},
	}))
	if bad.Kind() != appwire.MessageError {
		t.Fatalf("bad steer response=%v", bad.Kind())
	}
	if len(steered) != 0 {
		t.Fatalf("stale steer invoked handler: %v", steered)
	}

	good := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(4), appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID,
		Input:          []appwire.InputItem{{Type: "text", Text: "right turn"}},
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
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "missing turn"}},
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
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}))
	startResp := start.Response.Result.(appwire.TurnStartResponse)

	missing := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref: "local:th_1",
	}))
	if missing.Kind() != appwire.MessageError || missing.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("interrupt without turn id=%+v", missing)
	}
	stale := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(4), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID + "-stale",
	}))
	if stale.Kind() != appwire.MessageError || stale.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("stale interrupt=%+v", stale)
	}
	good := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(5), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:            "local:th_1",
		ExpectedTurnID: startResp.Turn.ID,
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
		"ERRORED":        appwire.ThreadStatusSystemError,
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
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
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
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1", Subscribe: true}); err != nil {
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
	srv.SetContextPressureFunc(func() float64 { return 0.42 })
	srv.SetDetailedStatusFunc(func() DetailedStatus {
		return DetailedStatus{
			Tools: []ToolInfo{{Name: "shell", Source: "core"}},
			MCP:   []MCPServerInfo{{Name: "linear", Tools: []string{"search"}}},
			Skills: []SkillInfo{
				{Name: "superpowers:systematic-debugging", Description: "debug"},
			},
			Plugins:   []PluginStatusInfo{{Name: "superpowers", Version: "4.3.0", SkillCount: 12, AgentCount: 2, HookCount: 4}},
			Hooks:     map[string]int{"PreToolUse": 3},
			Subagents: []SubagentStatusInfo{{ID: "sub-1", Status: "completed", TurnsUsed: 2}},
			Agents:    []string{"explorer"},
		}
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
	if data.Thread.Serf.ContextPressure != 0.42 {
		t.Fatalf("context pressure=%v", data.Thread.Serf.ContextPressure)
	}
	diag := data.Thread.Serf.Diagnostics
	if diag == nil || len(diag.Tools) != 1 || len(diag.MCP) != 1 || len(diag.Skills) != 1 || len(diag.Plugins) != 1 || len(diag.Subagents) != 1 || len(diag.Agents) != 1 {
		t.Fatalf("diagnostics=%+v", diag)
	}
	if diag.Hooks["PreToolUse"] != 3 {
		t.Fatalf("hooks=%+v", diag.Hooks)
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

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1", Subscribe: true}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	if got := srv.AppServer().SubscriberCount("th_1"); got != 1 {
		t.Fatalf("subscriber count=%d, want 1", got)
	}
}

func TestServerAppWireThreadReadDoesNotSubscribeByDefault(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:th_1"}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v", resp.Kind())
	}
	if got := srv.AppServer().SubscriberCount("th_1"); got != 0 {
		t.Fatalf("subscriber count=%d, want 0", got)
	}
}

// TestServerAppWireQueueCapabilityFlipsWithProcessing verifies that the
// appwire Capabilities.queue bit is gated on the session being mid-turn
// (kata 111a).
func TestServerAppWireQueueCapabilityFlipsWithProcessing(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetQueueFunc(func(string) error { return nil })

	// Idle: capabilities.queue must be false even with QueueFunc set.
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "IDLE"})
	caps := srv.appCapabilities("IDLE", false)
	if caps.Queue {
		t.Fatalf("Queue should be false when idle")
	}

	// Processing: capabilities.queue flips to true.
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "PROCESSING"})
	caps = srv.appCapabilities("PROCESSING", true)
	if !caps.Queue {
		t.Fatalf("Queue should be true mid-turn")
	}

	// Reserved active turn: turn/start has returned an active turn ID, but
	// the input loop has not necessarily flipped processing yet.
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "IDLE"})
	srv.appActiveTurnID = "turn_reserved"
	srv.appReservedTurnID = "turn_reserved"
	caps = srv.appCapabilities("IDLE", false)
	if !caps.Queue {
		t.Fatalf("Queue should be true while an active turn is reserved")
	}
	if caps.Send {
		t.Fatalf("Send should be false while an active turn is reserved")
	}

	// No QueueFunc registered: Queue stays false.
	srv2 := NewServer(ServerConfig{})
	srv2.SetAppIdentity("local", "th_2")
	srv2.SetProcessing(true)
	if caps2 := srv2.appCapabilities("PROCESSING", true); caps2.Queue {
		t.Fatalf("Queue should be false without QueueFunc registered")
	}
}

// TestServerAppWireTurnQueueAcceptsMidTurnMessage verifies the
// turn/queue handler dispatches text to the registered QueueFunc when a
// turn is in flight.
func TestServerAppWireTurnQueueAcceptsMidTurnMessage(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "PROCESSING"})
	var got []string
	srv.SetQueueFunc(func(text string) error {
		got = append(got, text)
		return nil
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "queued"}},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if len(got) != 1 || got[0] != "queued" {
		t.Fatalf("got=%v", got)
	}
}

func TestServerAppWireTurnQueueAcceptsReservedActiveTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "IDLE"})
	srv.appActiveTurnID = "turn_reserved"
	srv.appReservedTurnID = "turn_reserved"
	var got []string
	srv.SetQueueFunc(func(text string) error {
		got = append(got, text)
		return nil
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "queued"}},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if len(got) != 1 || got[0] != "queued" {
		t.Fatalf("got=%v", got)
	}
}

func TestServerAppWireTurnStartRejectsReservedActiveTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "IDLE"})
	srv.appActiveTurnID = "turn_reserved"
	srv.appReservedTurnID = "turn_reserved"

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "second"}},
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error response, got %v", resp.Kind())
	}
	if srv.appReservedTurnID != "turn_reserved" || srv.appActiveTurnID != "turn_reserved" {
		t.Fatalf("reservation mutated after rejected start: active=%q reserved=%q", srv.appActiveTurnID, srv.appReservedTurnID)
	}
}

func TestReserveAppTurnIDForStartIsAtomic(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	ids := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, err := srv.reserveAppTurnIDForStart()
			errs <- err
			ids <- id
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(ids)

	var successes, conflicts int
	for err := range errs {
		if err == nil {
			successes++
		} else {
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want one success and one conflict", successes, conflicts)
	}
}

func TestServerAppWireTurnQueueRejectsStaleProjectedActiveTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "IDLE"})
	srv.appActiveTurnID = "turn_stale"
	srv.SetQueueFunc(func(string) error { return nil })

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "queued"}},
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error response, got %v", resp.Kind())
	}
}

// TestServerAppWireTurnQueueRejectsWhenIdle verifies the handler returns
// Conflict when the session is not processing.
func TestServerAppWireTurnQueueRejectsWhenIdle(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "IDLE"})
	srv.SetQueueFunc(func(string) error { return nil })

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:   "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "queued"}},
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error response, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
}

// TestServerAppWireTurnDrainAsSteerRequiresQueuedMessages verifies the
// handler returns Conflict when no messages are queued.
func TestServerAppWireTurnDrainAsSteerRequiresQueuedMessages(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "PROCESSING"})
	srv.SetDrainAsSteerFunc(func() error { return nil })
	srv.SetQueueDepthFunc(func() int { return 0 })

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{
		Ref: "local:th_1",
	}))
	if resp.Kind() != appwire.MessageError {
		t.Fatalf("expected error, got %v", resp.Kind())
	}
	if resp.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("error=%+v", resp.Error.Error)
	}
}

// TestServerAppWireTurnDrainAsSteerDispatchesWhenQueued verifies the
// handler invokes the registered drain callback.
func TestServerAppWireTurnDrainAsSteerDispatchesWhenQueued(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "PROCESSING"})
	called := 0
	srv.SetDrainAsSteerFunc(func() error { called++; return nil })
	srv.SetQueueDepthFunc(func() int { return 2 })

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{
		Ref: "local:th_1",
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if called != 1 {
		t.Fatalf("drain called=%d, want 1", called)
	}
}

func TestServerAppWireTurnDrainAsSteerDispatchesInputAtomically(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "th_1", State: "PROCESSING"})
	srv.SetDrainAsSteerFunc(func() error {
		t.Fatal("classic drain callback should not be used for input-bearing drain")
		return nil
	})
	var gotText string
	var gotImages []ImageAttachment
	srv.SetDrainAsSteerWithInputFunc(func(text string, images []ImageAttachment) error {
		gotText = text
		gotImages = append([]ImageAttachment(nil), images...)
		return nil
	})
	srv.SetQueueDepthFunc(func() int { return 0 })

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{}))
	resp := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{
		Ref: "local:th_1",
		Input: []appwire.InputItem{{Type: "text", Text: "composer payload"}, {
			Type:      "image",
			MediaType: "image/png",
			Data:      []byte("png"),
			Name:      "shot.png",
		}},
	}))
	if resp.Kind() != appwire.MessageResponse {
		t.Fatalf("resp=%v error=%+v", resp.Kind(), resp.Error)
	}
	if gotText != "composer payload" {
		t.Fatalf("text=%q, want composer payload", gotText)
	}
	if len(gotImages) != 1 || gotImages[0].Name != "shot.png" || !bytes.Equal(gotImages[0].Data, []byte("png")) {
		t.Fatalf("images=%+v", gotImages)
	}
}
