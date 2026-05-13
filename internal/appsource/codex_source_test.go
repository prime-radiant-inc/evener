package appsource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

func TestCodexSourceListsThreads(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, params map[string]any) (map[string]any, error) {
		if params["searchTerm"] != "task" {
			t.Fatalf("searchTerm=%v", params["searchTerm"])
		}
		statuses, ok := params["statuses"].([]any)
		if !ok || len(statuses) != 2 || statuses[0] != "running" || statuses[1] != "completed" {
			t.Fatalf("statuses=%#v", params["statuses"])
		}
		if params["includeSubagents"] != true {
			t.Fatalf("includeSubagents=%v", params["includeSubagents"])
		}
		return map[string]any{"data": []map[string]any{{
			"id":            "th_codex",
			"sessionId":     "sess_codex",
			"preview":       "Codex task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     200,
			"status":        map[string]any{"type": "notLoaded"},
			"path":          "/tmp/codex/rollout.jsonl",
			"cwd":           "/work/project",
			"cliVersion":    "codex-test",
			"source":        "appServer",
			"name":          "Codex task name",
		}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{
		SearchTerm:       "task",
		Statuses:         []string{"running", "completed"},
		IncludeSubagents: true,
	})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	thread := resp.Data[0]
	if thread.ID != "th_codex" || thread.SessionID != "sess_codex" || thread.Source != "codex" {
		t.Fatalf("thread identity=%+v", thread)
	}
	if thread.Serf.Ref != "codex:th_codex" {
		t.Fatalf("ref=%q", thread.Serf.Ref)
	}
	if thread.Status.Type != appwire.ThreadStatusEnded {
		t.Fatalf("status=%+v", thread.Status)
	}
	if !thread.Serf.Capabilities.Send || thread.Serf.Capabilities.Compact || thread.Serf.Capabilities.ForkFromTurn || thread.Serf.Capabilities.Steer || thread.Serf.Capabilities.Interrupt || thread.Serf.Capabilities.Shutdown {
		t.Fatalf("capabilities=%+v", thread.Serf.Capabilities)
	}
}

func TestCodexSourceStartTurnMapsPromptToInput(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var captured map[string]any
	appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		captured = params
		return map[string]any{"turn": map[string]any{
			"id":        "turn_codex",
			"items":     []any{},
			"itemsView": "full",
			"status":    "inProgress",
		}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	resp, err := source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "codex:th_codex", Prompt: "hello codex"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	if resp.Turn.ID != "turn_codex" || resp.Turn.Status != appwire.TurnStatusRunning {
		t.Fatalf("turn=%+v", resp.Turn)
	}
	if captured["threadId"] != "th_codex" {
		t.Fatalf("threadId=%v", captured["threadId"])
	}
	input, ok := captured["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input=%#v", captured["input"])
	}
	text, ok := input[0].(map[string]any)
	if !ok || text["type"] != "text" || text["text"] != "hello codex" {
		t.Fatalf("text input=%#v", input[0])
	}
}

func TestCodexSourceStartTurnAcceptsCodexNativeInputItems(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var captured map[string]any
	appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		captured = params
		return map[string]any{"turn": map[string]any{
			"id":        "turn_codex",
			"items":     []any{},
			"itemsView": "full",
			"status":    "inProgress",
		}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	var items []appwire.InputItem
	if err := json.Unmarshal([]byte(`[
		{"type":"text","text":"Use $skill-creator with $demo-app"},
		{"type":"image","url":"https://example.com/screenshot.png"},
		{"type":"localImage","path":"/tmp/screenshot.png"},
		{"type":"skill","name":"skill-creator","path":"/Users/me/.codex/skills/skill-creator/SKILL.md"},
		{"type":"mention","name":"Demo App","path":"app://demo-app"}
	]`), &items); err != nil {
		t.Fatalf("unmarshal items: %v", err)
	}

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	if _, err := source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "codex:th_codex", Items: items}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	input, ok := captured["input"].([]any)
	if !ok || len(input) != 5 {
		t.Fatalf("input=%#v", captured["input"])
	}
	assertCodexInputItem(t, input[0], map[string]any{
		"type": "text",
		"text": "Use $skill-creator with $demo-app",
	})
	assertCodexInputItem(t, input[1], map[string]any{
		"type": "image",
		"url":  "https://example.com/screenshot.png",
	})
	assertCodexInputItem(t, input[2], map[string]any{
		"type": "localImage",
		"path": "/tmp/screenshot.png",
	})
	assertCodexInputItem(t, input[3], map[string]any{
		"type": "skill",
		"name": "skill-creator",
		"path": "/Users/me/.codex/skills/skill-creator/SKILL.md",
	})
	assertCodexInputItem(t, input[4], map[string]any{
		"type": "mention",
		"name": "Demo App",
		"path": "app://demo-app",
	})
}

func assertCodexInputItem(t *testing.T, raw any, want map[string]any) {
	t.Helper()
	got, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("input item %T=%#v, want map", raw, raw)
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Fatalf("input item %v = %v, want %v (item=%#v)", key, got[key], wantValue, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("input item has extra fields: got=%#v want=%#v", got, want)
	}
}
func TestCodexSourceStartThreadUsesCodexUserThreadSource(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var captured map[string]any
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		captured = params
		return map[string]any{"thread": codexThreadMap("th_codex")}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	resp, err := source.StartThread(context.Background(), appwire.ThreadStartParams{Model: "gpt-5.1-codex", CWD: "/work/project"})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex:th_codex" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	if captured["threadSource"] != "user" {
		t.Fatalf("threadSource=%v, want user", captured["threadSource"])
	}
	if captured["modelProvider"] != nil || captured["model"] != "gpt-5.1-codex" {
		t.Fatalf("model params=%+v", captured)
	}
}

func TestCodexSourceSubscribeReusesStartedThreadConnection(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		appserver.Subscribe(ctx, "th_codex")
		return map[string]any{"thread": codexThreadMap("th_codex")}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return nil, appwire.InvalidParams("no rollout found for thread id th_codex")
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	resp, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/work/project"})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: resp.Thread.Serf.Ref})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	server.Broadcast("th_codex", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_codex",
		Delta:    "hello",
	})

	select {
	case got := <-notifications:
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
		var params appwire.AgentMessageDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("params: %v", err)
		}
		if params.Ref != "codex:th_codex" || params.Delta != "hello" {
			t.Fatalf("params=%+v", params)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for notification")
	}
}

func TestCodexSourceStartedThreadLiveConnectionSurvivesCallerCancel(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		appserver.Subscribe(ctx, "th_codex")
		return map[string]any{"thread": codexThreadMap("th_codex")}, nil
	})
	var resumed bool
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		resumed = true
		return nil, appwire.InvalidParams("no rollout found for thread id th_codex")
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := source.StartThread(ctx, appwire.ThreadStartParams{CWD: "/work/project"})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	cancel()
	time.Sleep(50 * time.Millisecond)

	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: resp.Thread.Serf.Ref})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	if resumed {
		t.Fatal("SubscribeThread fell back to thread/resume")
	}
	server.Broadcast("th_codex", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_codex",
		Delta:    "survived",
	})

	select {
	case got, ok := <-notifications:
		if !ok {
			t.Fatal("live notifications closed after caller context cancellation")
		}
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
		var params appwire.AgentMessageDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("params: %v", err)
		}
		if params.Ref != "codex:th_codex" || params.Delta != "survived" {
			t.Fatalf("params=%+v", params)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live notification after caller context cancellation")
	}
}

func TestCodexSourceStartThreadRetiresLiveConnectionWithoutSubscriber(t *testing.T) {
	withCodexLiveNoSubscriberTimeout(t, 20*time.Millisecond)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		appserver.Subscribe(ctx, "th_codex")
		return map[string]any{"thread": codexThreadMap("th_codex")}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	if _, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/work/project"}); err != nil {
		t.Fatalf("StartThread: %v", err)
	}

	waitForNoLiveThread(t, source, "th_codex")
}

func TestCodexSourceLiveThreadFansOutToMultipleSubscribers(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		appserver.Subscribe(ctx, "th_codex")
		return map[string]any{"thread": codexThreadMap("th_codex")}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return nil, appwire.InvalidParams("thread/resume should not be used while the live client exists")
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	resp, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/work/project"})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	sub1, err := source.SubscribeThread(ctx1, appwire.ThreadReadParams{Ref: resp.Thread.Serf.Ref})
	if err != nil {
		t.Fatalf("SubscribeThread 1: %v", err)
	}
	sub2, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: resp.Thread.Serf.Ref})
	if err != nil {
		t.Fatalf("SubscribeThread 2: %v", err)
	}

	server.Broadcast("th_codex", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_codex",
		Delta:    "fanout one",
	})
	assertDelta(t, sub1, "fanout one")
	assertDelta(t, sub2, "fanout one")

	cancel1()
	time.Sleep(50 * time.Millisecond)
	server.Broadcast("th_codex", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_codex",
		Delta:    "fanout two",
	})
	assertDelta(t, sub2, "fanout two")
}

func TestCodexSourceStartThreadSpoolsInitialTurnNotificationsBeforeSubscribe(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		transport := appwire.NewWSTransport(ws)
		for {
			msg, err := transport.Recv(context.Background())
			if err != nil {
				return
			}
			if msg.Notification != nil {
				continue
			}
			if msg.Request == nil {
				continue
			}
			req := *msg.Request
			switch req.Method {
			case appwire.MethodInitialize:
				_ = transport.Send(context.Background(), appwire.ResponseMessage(req.ID, appwire.InitializeResponse{
					ServerInfo:      appwire.ServerInfo{Name: "codex-test"},
					ProtocolVersion: appwire.ProtocolVersion,
					SourceID:        "codex",
				}))
			case appwire.MethodThreadStart:
				_ = transport.Send(context.Background(), appwire.ResponseMessage(req.ID, map[string]any{"thread": codexThreadMap("th_codex")}))
			case appwire.MethodTurnStart:
				for i := 0; i < 160; i++ {
					_ = transport.Send(context.Background(), appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
						ThreadID: "th_codex",
						Delta:    "initial backlog",
					}))
				}
				_ = transport.Send(context.Background(), appwire.ResponseMessage(req.ID, map[string]any{"turn": map[string]any{
					"id":        "turn_codex",
					"items":     []any{},
					"itemsView": "full",
					"status":    "inProgress",
				}}))
			default:
				_ = transport.Send(context.Background(), appwire.ErrorMessage(req.ID, appwire.MethodNotFound(req.Method)))
			}
		}
	}))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := source.StartThread(ctx, appwire.ThreadStartParams{CWD: "/work/project", Prompt: "hello codex"})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: resp.Thread.Serf.Ref})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	assertDelta(t, notifications, "initial backlog")
}

func TestCodexSourceStartThreadWithPromptKeepsTurnNotificationsOnLiveConnection(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"thread": codexThreadMap("th_codex")}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(ctx context.Context, params map[string]any) (map[string]any, error) {
		if params["threadId"] != "th_codex" {
			t.Fatalf("threadId=%v", params["threadId"])
		}
		appserver.Subscribe(ctx, "th_codex")
		server.Broadcast("th_codex", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			ThreadID: "th_codex",
			Delta:    "hello from initial turn",
		})
		return map[string]any{"turn": map[string]any{
			"id":        "turn_codex",
			"items":     []any{},
			"itemsView": "full",
			"status":    "inProgress",
		}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	resp, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/work/project", Prompt: "hello codex"})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: resp.Thread.Serf.Ref})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	select {
	case got := <-notifications:
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
		var params appwire.AgentMessageDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("params: %v", err)
		}
		if params.Ref != "codex:th_codex" || params.Delta != "hello from initial turn" {
			t.Fatalf("params=%+v", params)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial-turn notification")
	}
}

func TestCodexSourceStartTurnUsesLiveConnectionForNotifications(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		transport := appwire.NewWSTransport(ws)
		for {
			msg, err := transport.Recv(context.Background())
			if err != nil {
				return
			}
			if msg.Notification != nil {
				continue
			}
			if msg.Request == nil {
				continue
			}
			req := *msg.Request
			switch req.Method {
			case appwire.MethodInitialize:
				_ = transport.Send(context.Background(), appwire.ResponseMessage(req.ID, appwire.InitializeResponse{
					ServerInfo:      appwire.ServerInfo{Name: "codex-test"},
					ProtocolVersion: appwire.ProtocolVersion,
					SourceID:        "codex",
				}))
			case appwire.MethodThreadResume:
				_ = transport.Send(context.Background(), appwire.ResponseMessage(req.ID, map[string]any{"thread": codexThreadMap("th_codex")}))
			case appwire.MethodTurnStart:
				_ = transport.Send(context.Background(), appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
					ThreadID: "th_codex",
					Delta:    "follow-up live",
				}))
				_ = transport.Send(context.Background(), appwire.ResponseMessage(req.ID, map[string]any{"turn": map[string]any{
					"id":        "turn_codex",
					"items":     []any{},
					"itemsView": "full",
					"status":    "inProgress",
				}}))
			default:
				_ = transport.Send(context.Background(), appwire.ErrorMessage(req.ID, appwire.MethodNotFound(req.Method)))
			}
		}
	}))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	if _, err := source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "codex:th_codex", Prompt: "follow up"}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	assertDelta(t, notifications, "follow-up live")
}

func TestCodexSourceStartTurnRetiresLiveConnectionWithoutSubscriber(t *testing.T) {
	withCodexLiveNoSubscriberTimeout(t, 20*time.Millisecond)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(ctx context.Context, params map[string]any) (map[string]any, error) {
		if params["threadId"] != "th_codex" {
			t.Fatalf("threadId=%v", params["threadId"])
		}
		appserver.Subscribe(ctx, "th_codex")
		return map[string]any{"turn": map[string]any{
			"id":        "turn_codex",
			"items":     []any{},
			"itemsView": "full",
			"status":    "inProgress",
		}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	if _, err := source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "codex:th_codex", Prompt: "follow up"}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}

	waitForNoLiveThread(t, source, "th_codex")
}

func withCodexLiveNoSubscriberTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	previous := codexLiveNoSubscriberTimeout
	codexLiveNoSubscriberTimeout = timeout
	t.Cleanup(func() {
		codexLiveNoSubscriberTimeout = previous
	})
}

func waitForNoLiveThread(t *testing.T, source *CodexSource, threadID string) {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if source.liveThread(threadID) == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("live thread %q remained retained without a subscriber", threadID)
		case <-ticker.C:
		}
	}
}

func deltaNotification(delta string) appwire.Notification {
	msg := appwire.NotificationMessage(appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_codex",
		TurnID:   "turn_codex",
		ItemID:   "item_codex",
		Delta:    delta,
	})
	return *msg.Notification
}

func assertDelta(t *testing.T, notifications <-chan appwire.Notification, want string) {
	t.Helper()
	select {
	case got, ok := <-notifications:
		if !ok {
			t.Fatalf("notification channel closed before delta %q", want)
		}
		if got.Method != appwire.NotifyAgentMessageDelta {
			t.Fatalf("method=%q", got.Method)
		}
		var params appwire.AgentMessageDeltaParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("params: %v", err)
		}
		if params.Delta != want {
			t.Fatalf("delta=%q, want %q", params.Delta, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for delta %q", want)
	}
}

func TestCodexSourceReadThreadRetriesWithoutTurnsBeforeFirstMessage(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var includeTurnsValues []any
	var itemsViewValues []any
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		includeTurnsValues = append(includeTurnsValues, params["includeTurns"])
		itemsViewValues = append(itemsViewValues, params["itemsView"])
		if params["includeTurns"] == true {
			return nil, appwire.InvalidParams("thread th_codex is not materialized yet; includeTurns is unavailable before first user message")
		}
		return map[string]any{"thread": codexThreadMap("th_codex")}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex", IncludeTurns: true, ItemsView: "full"})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex:th_codex" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	if len(includeTurnsValues) != 2 || includeTurnsValues[0] != true || includeTurnsValues[1] != false {
		t.Fatalf("includeTurns calls=%+v", includeTurnsValues)
	}
	if len(itemsViewValues) != 2 || itemsViewValues[0] != "full" || itemsViewValues[1] != "full" {
		t.Fatalf("itemsView calls=%+v", itemsViewValues)
	}
}

func TestCodexSourceSubscribeTranslatesNotifications(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		appserver.Subscribe(ctx, params.ThreadID)
		return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	server.Broadcast("th_codex", "item/commandExecution/outputDelta", map[string]any{
		"threadId": "th_codex",
		"turnId":   "turn_codex",
		"itemId":   "cmd_1",
		"delta":    "stdout\n",
	})

	select {
	case got := <-notifications:
		if got.Method != appwire.NotifyToolOutputDelta {
			t.Fatalf("method=%q", got.Method)
		}
		var params struct {
			ThreadID string `json:"threadId"`
			Ref      string `json:"ref,omitempty"`
			TurnID   string `json:"turnId"`
			ItemID   string `json:"itemId"`
			Delta    string `json:"delta"`
		}
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("params: %v", err)
		}
		if params.ThreadID != "th_codex" || params.Ref != "codex:th_codex" || params.ItemID != "cmd_1" || params.Delta != "stdout\n" {
			t.Fatalf("params=%+v", params)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for translated notification")
	}
}

func TestCodexSourceTurnCompletedNotificationIncludesCanonicalRef(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		appserver.Subscribe(ctx, params.ThreadID)
		return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	server.Broadcast("th_codex", appwire.NotifyTurnCompleted, map[string]any{
		"threadId": "th_codex",
		"turn": map[string]any{
			"id":        "turn_codex",
			"items":     []any{},
			"itemsView": "full",
			"status":    "completed",
		},
	})

	select {
	case got := <-notifications:
		if got.Method != appwire.NotifyTurnCompleted {
			t.Fatalf("method=%q", got.Method)
		}
		var params struct {
			ThreadID string `json:"threadId"`
			Ref      string `json:"ref"`
		}
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("params: %v", err)
		}
		if params.ThreadID != "th_codex" || params.Ref != "codex:th_codex" {
			t.Fatalf("params=%+v", params)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for turn/completed notification")
	}
}

func codexThreadMap(id string) map[string]any {
	return map[string]any{
		"id":            id,
		"sessionId":     id,
		"preview":       id,
		"modelProvider": "openai",
		"createdAt":     100,
		"updatedAt":     200,
		"status":        map[string]any{"type": "idle"},
		"cwd":           "/work/project",
		"cliVersion":    "codex-test",
		"source":        "appServer",
		"turns":         []any{},
	}
}

func wsURL(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http")
}
