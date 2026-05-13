package appsource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
