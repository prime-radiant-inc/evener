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
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
	"primeradiant.com/serf/internal/appserver"
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

	msg := sendHubInput(client, appwire.Ref{SourceID: "local", ThreadID: "th_1"}, "ship it", "ship it", nil)()
	sendMsg, ok := msg.(hubSendMsg)
	if !ok || sendMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, sendMsg.err)
	}
	if got.Ref != "local:th_1" || testInputText(got.Input) != "ship it" {
		t.Fatalf("params=%+v", got)
	}
}

func testInputText(input []appwire.InputItem) string {
	for _, item := range input {
		if item.Text != "" {
			return item.Text
		}
	}
	return ""
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
	if len(got.session.messages) != 1 || got.session.messages[0].Kind != transcript.MsgAssistant || got.session.messages[0].Text != "hello world" {
		t.Fatalf("messages=%+v", got.session.messages)
	}
	updated, _ = got.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyItemCompleted, map[string]any{
			"threadId": "th_1",
			"turnId":   "turn_1",
			"item": appwire.ThreadItem{
				Type:   "agentMessage",
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
				Type:          "commandExecution",
				ID:            "item_tool_1",
				CallID:        "call_1",
				TurnID:        "turn_1",
				ToolName:      "shell",
				ArgumentsJSON: `{"command":"printf 'one\ntwo\n'"}`,
				Status:        appwire.TurnStatusInProgress,
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
				Type:     "commandExecution",
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
	var tools []transcript.ChatMessage
	for _, msg := range got.session.messages {
		if msg.Kind == transcript.MsgTool {
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

func TestHubModelAppliesSteeringInjectedNotification(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}

	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifySerfSteeringInjected, map[string]any{
			"threadId": "th_1",
			"ref":      "local:th_1",
			"text":     "check the logs",
		}).Notification,
	})

	got := updated.(hubModel)
	if len(got.session.messages) != 1 {
		t.Fatalf("messages=%+v", got.session.messages)
	}
	msg := got.session.messages[0]
	if msg.Kind != transcript.MsgSteering || msg.Text != "check the logs" || msg.Pending || msg.Failed {
		t.Fatalf("message=%+v, want authoritative steering", msg)
	}
}

// TestHubModelAppliesQueueChangedNotification (kata r80p) verifies the
// TUI consumes thread/queueChanged as the authoritative source for the
// composer queue preview. The local sessionQueue field reflects the
// wire snapshot exactly; consecutive notifications replace state in
// full rather than appending.
func TestHubModelAppliesQueueChangedNotification(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1"}

	// First mutation: depth=1, one entry.
	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			ThreadID: "sess_1",
			Ref:      "local:th_1",
			Queue:    appwire.QueueState{Depth: 1, Preview: []string{"first queued"}},
		}).Notification,
	})
	got := updated.(hubModel)
	if len(got.sessionQueue) != 1 || got.sessionQueue[0] != "first queued" {
		t.Fatalf("sessionQueue after first notification=%v", got.sessionQueue)
	}
	if got.sessionQueueRef != "local:th_1" {
		t.Fatalf("sessionQueueRef=%q, want local:th_1", got.sessionQueueRef)
	}

	// Second mutation: depth=2, replacing state fully (head + new tail).
	updated, _ = got.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			Ref:   "local:th_1",
			Queue: appwire.QueueState{Depth: 2, Preview: []string{"first queued", "second queued"}},
		}).Notification,
	})
	got = updated.(hubModel)
	if len(got.sessionQueue) != 2 || got.sessionQueue[0] != "first queued" || got.sessionQueue[1] != "second queued" {
		t.Fatalf("sessionQueue after second notification=%v", got.sessionQueue)
	}

	// Drain to depth=0: state must be wiped, not retained.
	updated, _ = got.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyThreadQueueChanged, appwire.ThreadQueueChangedParams{
			Ref:   "local:th_1",
			Queue: appwire.QueueState{},
		}).Notification,
	})
	got = updated.(hubModel)
	if len(got.sessionQueue) != 0 {
		t.Fatalf("sessionQueue after drain=%v, want empty", got.sessionQueue)
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
	if len(got.session.messages) != 1 || got.session.messages[0].Kind != transcript.MsgSystem || got.session.messages[0].Text != "Provider error: openai error (status=429): rate limited" {
		t.Fatalf("messages=%+v", got.session.messages)
	}
}

func TestHubModelTurnCompletedAppliesSnapshotItems(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess_1", ActiveTurnID: "turn_1"}

	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyTurnCompleted, map[string]any{
			"threadId": "th_1",
			"ref":      "local:th_1",
			"turnId":   "turn_1",
			"turn": appwire.Turn{
				Status: appwire.TurnStatusCompleted,
				Items: []appwire.ThreadItem{{
					Type: "userMessage",
					ID:   "item_user",
					Text: "hello",
				}, {
					Type: "agentMessage",
					ID:   "item_agent",
					Text: "done",
				}},
			},
		}).Notification,
	})

	got := updated.(hubModel)
	if got.detail.ActiveTurnID != "" {
		t.Fatalf("active turn=%q, want cleared", got.detail.ActiveTurnID)
	}
	if len(got.session.messages) != 2 {
		t.Fatalf("messages=%+v, want user and assistant snapshot items", got.session.messages)
	}
	if got.session.messages[0].Kind != transcript.MsgUser || got.session.messages[0].Text != "hello" {
		t.Fatalf("user message=%+v", got.session.messages[0])
	}
	if got.session.messages[1].Kind != transcript.MsgAssistant || got.session.messages[1].Text != "done" {
		t.Fatalf("assistant message=%+v", got.session.messages[1])
	}
}

func TestMessagesFromThreadIncludesFailedTurnDiagnostic(t *testing.T) {
	messages := transcript.MessagesFromThread(appwire.Thread{
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

	if len(messages) != 1 || messages[0].Kind != transcript.MsgSystem || messages[0].Text != "Serf configuration error: configuration error: unknown provider: openrouter" {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestHubThreadFixtureKeepsSplitToolResultsGrouped(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "appwire", "testdata", "tool-groups-thread.json"))
	if err != nil {
		t.Fatal(err)
	}
	var thread appwire.Thread
	if err := json.Unmarshal(data, &thread); err != nil {
		t.Fatal(err)
	}

	messages := transcript.MessagesFromThread(thread)
	var tools []transcript.ToolCallInfo
	for _, msg := range messages {
		if msg.Kind == transcript.MsgTool && msg.Tool != nil {
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
