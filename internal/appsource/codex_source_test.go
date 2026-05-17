package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
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
	if !thread.Serf.Capabilities.Send || !thread.Serf.Capabilities.Compact || thread.Serf.Capabilities.ForkFromTurn || thread.Serf.Capabilities.Steer || thread.Serf.Capabilities.Interrupt || thread.Serf.Capabilities.Shutdown {
		t.Fatalf("capabilities=%+v", thread.Serf.Capabilities)
	}
}

func TestCodexSourceListThreadsTranslatesSerfStatusFilters(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, params map[string]any) (map[string]any, error) {
		statuses, ok := params["statuses"].([]any)
		if !ok || len(statuses) != 2 || statuses[0] != "active" || statuses[1] != "notLoaded" {
			t.Fatalf("statuses=%#v, want active/notLoaded", params["statuses"])
		}
		return map[string]any{"data": []map[string]any{{
			"id":            "th_codex",
			"sessionId":     "th_codex",
			"preview":       "Codex task",
			"modelProvider": "openai",
			"createdAt":     100,
			"updatedAt":     200,
			"status":        map[string]any{"type": "active"},
			"cwd":           "/work/project",
			"cliVersion":    "codex-test",
			"source":        "appServer",
		}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{
		Statuses: []string{appwire.ThreadStatusProcessing, appwire.ThreadStatusEnded},
	})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Status.Type != appwire.ThreadStatusProcessing {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestCodexSourceLoadedThreadAdvertisesTurnActions(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{
			{
				"id":        "th_active",
				"sessionId": "th_active",
				"preview":   "active codex",
				"status":    map[string]any{"type": "active"},
				"source":    "appServer",
			},
			{
				"id":        "th_idle",
				"sessionId": "th_idle",
				"preview":   "idle codex",
				"status":    map[string]any{"type": "idle"},
				"source":    "appServer",
			},
			{
				"id":        "th_unloaded",
				"sessionId": "th_unloaded",
				"preview":   "unloaded codex",
				"status":    map[string]any{"type": "notLoaded"},
				"source":    "appServer",
			},
		}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	active := resp.Data[0].Serf.Capabilities
	if !active.Send || !active.Compact || !active.Steer || !active.Interrupt {
		t.Fatalf("active capabilities=%+v", active)
	}
	idle := resp.Data[1].Serf.Capabilities
	if !idle.Send || !idle.Compact || !idle.Steer || !idle.Interrupt {
		t.Fatalf("idle capabilities=%+v", idle)
	}
	unloaded := resp.Data[2].Serf.Capabilities
	if unloaded.Steer || unloaded.Interrupt {
		t.Fatalf("unloaded capabilities=%+v", unloaded)
	}
}

func TestCodexSourceStartTurnMapsPromptToInput(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	handleCodexResume(server)
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
	handleCodexResume(server)
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

func TestCodexSourceStartTurnResumesThreadBeforeStreaming(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var resumed bool
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		if params.ThreadID != "th_codex" {
			t.Fatalf("threadId=%q", params.ThreadID)
		}
		resumed = true
		appserver.Subscribe(ctx, params.ThreadID)
		return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		if !resumed {
			t.Fatal("turn/start called before thread/resume")
		}
		if params["threadId"] != "th_codex" {
			t.Fatalf("threadId=%v", params["threadId"])
		}
		server.Broadcast("th_codex", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
			ThreadID: "th_codex",
			Delta:    "direct streamed",
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
	if _, err := source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "codex:th_codex", Prompt: "follow up"}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	assertDelta(t, notifications, "direct streamed")
}

func handleCodexResume(server *appserver.Server) {
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		appserver.Subscribe(ctx, params.ThreadID)
		return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
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

func TestCodexSourceUsesLifecycleModelMetadata(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	response := func(id string) map[string]any {
		return map[string]any{
			"thread":        codexThreadMap(id),
			"model":         "gpt-5.3-codex",
			"modelProvider": "openai",
		}
	}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return response("th_started"), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		appserver.Subscribe(ctx, params.ThreadID)
		return response(params.ThreadID), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadFork, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return response("th_child"), nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	start, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/work/project"})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	resume, err := source.ResumeThread(context.Background(), appwire.ThreadResumeParams{Ref: "codex:th_started"})
	if err != nil {
		t.Fatalf("ResumeThread: %v", err)
	}
	fork, err := source.ForkThread(context.Background(), appwire.ThreadForkParams{Ref: "codex:th_started"})
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	for _, thread := range []appwire.Thread{start.Thread, resume.Thread, fork.Thread} {
		if thread.ModelProvider != "gpt-5.3-codex" || thread.Serf.Profile != "openai" {
			t.Fatalf("thread model metadata=%+v", thread)
		}
	}
}

func TestCodexSourceForkThreadRejectsEditAtTurnMetadata(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	var forkCalled bool
	appserver.HandleTyped(server.Router(), appwire.MethodThreadFork, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		forkCalled = true
		return map[string]any{"thread": codexThreadMap("th_child")}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	_, err := source.ForkThread(context.Background(), appwire.ThreadForkParams{
		Ref:          "codex:th_codex",
		SourceTurnID: "turn_1",
		EditedInput:  "edited input",
	})
	if err == nil {
		t.Fatal("ForkThread succeeded with edit-at-turn metadata")
	}
	if forkCalled {
		t.Fatal("Codex thread/fork was called despite unsupported edit-at-turn metadata")
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

func TestCodexSourceSubscribeTreatsNoRolloutAsIdle(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return nil, appwire.InvalidParams("no rollout found for thread id th_codex")
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	if _, ok := <-notifications; ok {
		t.Fatal("no-rollout subscription should return a closed idle channel")
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

func TestCodexSourceStartThreadSpoolsInitialTurnNotificationsForEarlySubscribers(t *testing.T) {
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
	notifications1, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: resp.Thread.Serf.Ref})
	if err != nil {
		t.Fatalf("SubscribeThread 1: %v", err)
	}
	notifications2, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: resp.Thread.Serf.Ref})
	if err != nil {
		t.Fatalf("SubscribeThread 2: %v", err)
	}
	assertDelta(t, notifications1, "initial backlog")
	assertDelta(t, notifications2, "initial backlog")
}

func TestCodexLiveThreadDoesNotReplayDeliveredLiveNotifications(t *testing.T) {
	live := &codexLiveThread{
		close:       func() error { return nil },
		subscribers: map[chan appwire.Notification]struct{}{},
	}
	live.publish(deltaNotification("initial backlog"))

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	notifications1 := live.subscribe(ctx1)
	assertDelta(t, notifications1, "initial backlog")

	live.publish(deltaNotification("live after subscriber"))
	assertDelta(t, notifications1, "live after subscriber")

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	notifications2 := live.subscribe(ctx2)
	assertDelta(t, notifications2, "initial backlog")
	assertNoNotification(t, notifications2, "live after subscriber")
}

func TestCodexLiveThreadIsClosedAfterLastSubscriberRetires(t *testing.T) {
	retired := make(chan struct{})
	live := &codexLiveThread{
		close: func() error {
			close(retired)
			return nil
		},
		subscribers: map[chan appwire.Notification]struct{}{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	notifications := live.subscribe(ctx)
	cancel()

	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("live thread did not retire after last subscriber canceled")
	}
	if _, ok := <-notifications; ok {
		t.Fatal("subscriber channel stayed open after cancel")
	}
	if !live.isClosed() {
		t.Fatal("retiring live thread remained visible as open")
	}
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
	handleCodexResume(server)
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

func assertSessionUnavailable(t *testing.T, err error, label string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil", label)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("%s: error %T=%v, want appwire.WireError", label, err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || wire.Code != appwire.CodeUnavailable || data.SerfErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("%s: wire=%+v, want SessionUnavailable", label, wire)
	}
}

func assertNoNotification(t *testing.T, notifications <-chan appwire.Notification, label string) {
	t.Helper()
	select {
	case got, ok := <-notifications:
		if !ok {
			return
		}
		t.Fatalf("unexpected notification for %s: %+v", label, got)
	case <-time.After(50 * time.Millisecond):
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

func TestCodexSourceDialErrorMapsTransportFailures(t *testing.T) {
	// Direct unit-level coverage of the error classifier. Goes alongside the
	// integration tests below that exercise the StartTurn / ListThreads code
	// paths end-to-end.
	cases := []struct {
		name string
		err  error
	}{
		{"ECONNREFUSED", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}},
		{"ECONNRESET", &net.OpError{Op: "read", Err: syscall.ECONNRESET}},
		{"EPIPE", &net.OpError{Op: "write", Err: syscall.EPIPE}},
		{"io.EOF", io.EOF},
		{"io.ErrUnexpectedEOF wrapped", fmt.Errorf("recv: %w", io.ErrUnexpectedEOF)},
		{"context.DeadlineExceeded (transport-level)", context.DeadlineExceeded},
		{"websocket close error", websocket.CloseError{Code: websocket.StatusAbnormalClosure, Reason: "dropped"}},
		{"connection reset string match", errors.New("read tcp 127.0.0.1:1->127.0.0.1:2: connection reset by peer")},
		{"broken pipe string match", errors.New("write tcp: broken pipe")},
		{"use of closed network connection", errors.New("use of closed network connection")},
		{"i/o timeout string match", errors.New("dial tcp: i/o timeout")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := codexSourceDialError(tc.err)
			assertSessionUnavailable(t, got, tc.name)
		})
	}
}

func TestCodexSourceDialErrorPassesThroughApplicationErrors(t *testing.T) {
	// JSON-RPC application-level error: must not be remapped, since the
	// daemon is alive and the error has semantic meaning (e.g. a malformed
	// codex parameter).
	app := appwire.InvalidParams("missing ref")
	got := codexSourceDialError(app)
	var wire appwire.WireError
	if !errors.As(got, &wire) {
		t.Fatalf("got %T=%v, want WireError", got, got)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v, want InvalidParams code preserved", wire)
	}

	plain := errors.New("some codex-side problem")
	if got := codexSourceDialError(plain); !errors.Is(got, plain) {
		t.Fatalf("plain error rewritten: %v", got)
	}
}

func TestCodexSourceDialErrorIgnoresNil(t *testing.T) {
	if got := codexSourceDialError(nil); got != nil {
		t.Fatalf("nil mapped to %v, want nil", got)
	}
}

func TestCodexSourceCallErrorMapsTransportFailures(t *testing.T) {
	// The appwire client surfaces transport-level mid-RPC failures as
	// CodeInternalError wrapping the underlying message; codexSourceCallError
	// promotes those to SessionUnavailable while leaving genuine internal
	// errors (and other codes) untouched.
	cases := []struct {
		name string
		msg  string
	}{
		{"websocket failed to get reader", "appwire turn/start: failed to get reader: websocket"},
		{"eof", "appwire turn/start: EOF"},
		{"connection reset", "appwire turn/start: read tcp: connection reset by peer"},
		{"broken pipe", "appwire turn/start: write: broken pipe"},
		{"use of closed network connection", "appwire turn/start: use of closed network connection"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := appwire.InternalError(tc.msg)
			got := codexSourceCallError(in)
			assertSessionUnavailable(t, got, tc.name)
		})
	}
}

func TestCodexSourceCallErrorPassesThroughApplicationErrors(t *testing.T) {
	// A 400-style application error from codex must survive intact so the
	// caller can surface the real failure to the user.
	app := appwire.InvalidParams("400 bad request: invalid model id")
	got := codexSourceCallError(app)
	var wire appwire.WireError
	if !errors.As(got, &wire) {
		t.Fatalf("got %T=%v, want WireError", got, got)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v, want InvalidParams code preserved", wire)
	}

	// An InternalError without a transport-shaped message also passes
	// through — only daemon-dead patterns get promoted.
	semantic := appwire.InternalError("appwire turn/start: model token quota exceeded")
	if got := codexSourceCallError(semantic); !errors.Is(got, semantic) {
		var w appwire.WireError
		if errors.As(got, &w) && w.Code == appwire.CodeUnavailable {
			t.Fatalf("non-transport internal error remapped: %+v", w)
		}
	}
}

func TestCodexSourceStartTurnMapsDialRefusedToSessionUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	endpoint := "ws://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: endpoint}, nil)
	_, err = source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "codex:th_codex", Prompt: "hi"})
	assertSessionUnavailable(t, err, t.Name())
}

func TestCodexSourceStartTurnMapsDroppedTransportToSessionUnavailable(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		transport := appwire.NewWSTransport(conn)
		// Reply to initialize so connect() succeeds, then drop the connection
		// mid-call to simulate the codex daemon dying.
		for {
			msg, err := transport.Recv(r.Context())
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			req := *msg.Request
			switch req.Method {
			case appwire.MethodInitialize:
				_ = transport.Send(r.Context(), appwire.ResponseMessage(req.ID, appwire.InitializeResponse{
					ServerInfo:      appwire.ServerInfo{Name: "codex-test"},
					ProtocolVersion: appwire.ProtocolVersion,
					SourceID:        "codex",
				}))
			default:
				_ = conn.Close(websocket.StatusAbnormalClosure, "dropped")
				return
			}
		}
	}))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	_, err := source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "codex:th_codex", Prompt: "hi"})
	assertSessionUnavailable(t, err, t.Name())
}

func TestCodexSourceStartTurnPassesThroughApplicationErrors(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex"})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(_ context.Context, _ struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		return nil, appwire.InvalidParams("400 bad request: malformed threadId")
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	_, err := source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "codex:th_codex", Prompt: "hi"})
	if err == nil {
		t.Fatal("StartTurn unexpectedly succeeded")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("StartTurn error %T=%v, want WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire code=%d, want CodeInvalidParams (application error must not be remapped)", wire.Code)
	}
	if data, ok := wire.Data.(appwire.ErrorData); ok && data.SerfErrorInfo == appwire.ErrorSessionUnavailable {
		t.Fatalf("application error was remapped to SessionUnavailable: %+v", wire)
	}
}

func TestCodexSourceListThreadsMapsDialRefusedToSessionUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	endpoint := "ws://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: endpoint}, nil)
	_, err = source.ListThreads(context.Background(), appwire.ThreadListParams{})
	assertSessionUnavailable(t, err, t.Name())
}
