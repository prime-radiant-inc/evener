package main

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	msg := sendHubInput(client, appwire.Ref{SourceID: "local", ThreadID: "th_1"}, "ship it")()
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
	got := updated.(hubModel)
	if len(got.session.messages) != 1 || got.session.messages[0].Kind != msgAssistant || got.session.messages[0].Text != "hello" {
		t.Fatalf("messages=%+v", got.session.messages)
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
