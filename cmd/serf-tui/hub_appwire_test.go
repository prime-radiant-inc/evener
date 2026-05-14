package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

func TestFetchHubTreeUsesAppWireThreadList(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{{
			ID:        "th_1",
			SessionID: "sess_1",
			CWD:       "/tmp/project",
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:      appwire.SerfThread{Ref: "local:th_1"},
		}}}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := fetchHubTree(client)()
	treeMsg, ok := msg.(hubTreeMsg)
	if !ok || treeMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, treeMsg.err)
	}
	if len(treeMsg.tree.Live) != 1 || treeMsg.tree.Live[0].Ref != "local:th_1" {
		t.Fatalf("tree=%+v", treeMsg.tree)
	}
}

func TestSendHubInputUsesAppWireTurnStart(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var got appwire.TurnStartParams
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		got = params
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := sendHubInput(client, appwire.Ref{SourceID: "local", ThreadID: "th_1"}, "ship it", "ship it")()
	sendMsg, ok := msg.(hubSendMsg)
	if !ok || sendMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, sendMsg.err)
	}
	if got.Ref != "local:th_1" || got.Prompt != "ship it" {
		t.Fatalf("params=%+v", got)
	}
}

func TestHubModelAppliesAppWireNotifications(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}
	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			ThreadID: "th_1",
			Ref:      "local:th_1",
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    "hello",
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			ThreadID: "th_1",
			Ref:      "local:th_1",
			TurnID:   "turn_1",
			ItemID:   "item_1",
			Delta:    " world",
		}).Notification,
	})
	got := updated.(hubModel)
	if len(got.session.messages) != 1 || got.session.messages[0].Kind != msgAssistant || got.session.messages[0].Text != "hello world" {
		t.Fatalf("messages=%+v", got.session.messages)
	}
	updated, _ = got.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_1",
			"item": appwire.ThreadItem{
				Type:   "agent_message",
				ID:     "item_1",
				TurnID: "turn_1",
				Text:   "hello world final",
			},
		}).Notification,
	})
	got = updated.(hubModel)
	if len(got.session.messages) != 1 || got.session.messages[0].Text != "hello world final" {
		t.Fatalf("final messages=%+v", got.session.messages)
	}
}

func TestHubModelCompletesLiveToolWithoutDuplicateMessage(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}

	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyItemStarted, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_1",
			"item": appwire.ThreadItem{
				Type:          "tool_call",
				ID:            "item_tool_1",
				CallID:        "call_1",
				TurnID:        "turn_1",
				ToolName:      "shell",
				ArgumentsJSON: `{"command":"printf 'one\ntwo\n'"}`,
				Status:        "running",
			},
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyToolOutputDelta, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_1",
			"itemId":   "item_tool_1",
			"delta":    "one\n",
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyToolOutputDelta, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_1",
			"itemId":   "item_tool_1",
			"delta":    "two\n",
		}).Notification,
	})
	updated, _ = updated.(hubModel).Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_1",
			"item": appwire.ThreadItem{
				Type:     "tool_call",
				ID:       "item_tool_1",
				CallID:   "call_1",
				TurnID:   "turn_1",
				ToolName: "shell",
				Output:   "one\ntwo\n",
				Status:   "completed",
			},
		}).Notification,
	})

	got := updated.(hubModel)
	var tools []chatMessage
	for _, msg := range got.session.messages {
		if msg.Kind == msgTool {
			tools = append(tools, msg)
		}
	}
	if len(tools) != 1 {
		t.Fatalf("expected one tool message, got %d: %+v", len(tools), got.session.messages)
	}
	tool := tools[0].Tool
	if tool == nil || tool.Output != "one\ntwo\n" || !tool.Done {
		t.Fatalf("tool=%+v messages=%+v", tool, got.session.messages)
	}
	if _, ok := got.session.activeTools["item_tool_1"]; ok {
		t.Fatalf("completed item id still active: %+v", got.session.activeTools)
	}
	if _, ok := got.session.activeTools["call_1"]; ok {
		t.Fatalf("completed call id still active: %+v", got.session.activeTools)
	}
}

func TestHubModelSurfacesStructuredWarningDiagnostic(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}

	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyWarning, map[string]any{
			"threadId": "th_1",
			"ref":      "local:th_1",
			"message":  "openai error (status=429): rate limited",
			"source":   "provider",
			"title":    "Provider error",
		}).Notification,
	})

	got := updated.(hubModel)
	if len(got.session.messages) != 1 || got.session.messages[0].Kind != msgSystem || got.session.messages[0].Text != "Provider error: openai error (status=429): rate limited" {
		t.Fatalf("messages=%+v", got.session.messages)
	}
}

func TestMessagesFromThreadIncludesFailedTurnDiagnostic(t *testing.T) {
	messages := messagesFromThread(appwire.Thread{
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusFailed,
			Error: &appwire.TurnError{
				Message: "configuration error: unknown provider: openrouter",
				Source:  "serf",
				Title:   "Serf configuration error",
			},
		}},
	})

	if len(messages) != 1 || messages[0].Kind != msgSystem || messages[0].Text != "Serf configuration error: configuration error: unknown provider: openrouter" {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestHubThreadFixtureKeepsSplitToolResultsGrouped(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "appwire", "testdata", "tool-groups-thread.json"))
	if err != nil {
		t.Fatal(err)
	}
	var thread appwire.Thread
	if err := json.Unmarshal(data, &thread); err != nil {
		t.Fatal(err)
	}

	messages := messagesFromThread(thread)
	var tools []toolCallInfo
	for _, msg := range messages {
		if msg.Kind == msgTool && msg.Tool != nil {
			tools = append(tools, *msg.Tool)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("expected shell and failed read_file tools, got %d tools in %+v", len(tools), messages)
	}
	if tools[0].Name != "shell" || tools[0].Output != "alpha\nbeta\n" || !tools[0].Done {
		t.Fatalf("shell tool not grouped with result: %+v", tools[0])
	}
	if tools[1].Name != "read_file" || tools[1].Error == "" || !tools[1].Done {
		t.Fatalf("failed read_file not represented as completed tool: %+v", tools[1])
	}
}

func newTUIAppWireClient(t *testing.T, app *appserver.Server) (*appwire.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+srv.URL[len("http"):], srv.Client())
	if err != nil {
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		srv.Close()
		t.Fatalf("initialize: %v", err)
	}
	return client, func() {
		client.Close()
		srv.Close()
	}
}

var _ tea.Msg = hubNotificationMsg{}
