package appsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

func fuzzScenarioCodexSourceUsesAdapterNativeInitialize(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodInitialize, func(_ context.Context, params map[string]any) (map[string]any, error) {
		if _, ok := params["protocolVersion"]; ok {
			t.Fatalf("Codex initialize included Serf protocolVersion: %#v", params)
		}
		return map[string]any{"userAgent": "codex-test"}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (map[string]any, error) {
		return map[string]any{"data": []any{}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	if _, err := source.ListThreads(context.Background(), appwire.ThreadListParams{}); err != nil {
		t.Fatalf("ListThreads after native initialize: %v", err)
	}
}

func fuzzScenarioCodexSourceListsThreads(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(_ context.Context, params map[string]any) (map[string]any, error) {
		if params["searchTerm"] != "task" {
			t.Fatalf("searchTerm=%v", params["searchTerm"])
		}
		statuses, ok := params["statuses"].([]any)
		if !ok || len(statuses) != 2 || statuses[0] != "active" || statuses[1] != "notLoaded" {
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
		Statuses:         []string{"active", "notLoaded"},
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
	if thread.Status.Type != appwire.ThreadStatusNotLoaded {
		t.Fatalf("status=%+v", thread.Status)
	}
	if thread.Serf.Capabilities.Send || !thread.Serf.Capabilities.Compact || thread.Serf.Capabilities.ForkFromTurn || thread.Serf.Capabilities.Steer || thread.Serf.Capabilities.Interrupt || thread.Serf.Capabilities.Shutdown {
		t.Fatalf("capabilities=%+v", thread.Serf.Capabilities)
	}
}

func fuzzScenarioCodexSourceListThreadsTranslatesSerfStatusFilters(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
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
		Statuses: []string{appwire.ThreadStatusActive, appwire.ThreadStatusNotLoaded},
	})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func fuzzScenarioMapCodexTurnPreservesErrorDetails(t *testing.T) {
	turn := mapCodexTurn(codexTurn{
		ID:     "turn_failed",
		Status: "failed",
		Error: &codexTurnError{
			Message:           "request failed",
			AdditionalDetails: "upstream refused",
			CodexErrorInfo:    json.RawMessage(`{"type":"Unauthorized","httpStatusCode":401}`),
		},
	})
	if turn.Error == nil {
		t.Fatal("missing turn error")
	}
	if turn.Error.Message != "request failed" || turn.Error.AdditionalDetails != "upstream refused" || turn.Error.Source != "codex" {
		t.Fatalf("turn error=%+v", turn.Error)
	}
	raw, ok := turn.Error.CodexErrorInfo.(json.RawMessage)
	if !ok {
		t.Fatalf("codexErrorInfo=%T, want json.RawMessage", turn.Error.CodexErrorInfo)
	}
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("codexErrorInfo json: %v", err)
	}
	if info["type"] != "Unauthorized" || info["httpStatusCode"] != float64(401) {
		t.Fatalf("codexErrorInfo=%+v", info)
	}
}

func fuzzScenarioCodexSourceLoadedThreadAdvertisesTurnActions(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
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
	if active.Send || !active.Compact || active.Steer || active.Interrupt {
		t.Fatalf("active capabilities=%+v", active)
	}
	idle := resp.Data[1].Serf.Capabilities
	if idle.Send || !idle.Compact || idle.Steer || idle.Interrupt {
		t.Fatalf("idle capabilities=%+v", idle)
	}
	unloaded := resp.Data[2].Serf.Capabilities
	if unloaded.Steer || unloaded.Interrupt {
		t.Fatalf("unloaded capabilities=%+v", unloaded)
	}
}

// The outbound codex input wire shape is asserted here and in
// fuzzScenarioCodexSourceStartThreadAcceptsCodexNativeInputItems below, and
// nowhere else. Both used to drive CodexSource.StartTurn, which serf-appwire-v2
// reduced to an Unavailable stub; they reach the same startTurnWithClient path
// through StartThread's initial prompt turn instead, which is how a codex turn
// now starts at all.
func fuzzScenarioCodexSourceStartThreadMapsPromptToInput(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"thread": codexThreadMap("th_codex")}, nil
	})
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
	resp, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/work/project", Input: []appwire.InputItem{{Type: "text", Text: "hello codex"}}})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	if resp.Turn.ID != "turn_codex" || resp.Turn.Status != appwire.TurnStatusInProgress {
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

func fuzzScenarioCodexSourceStartThreadAcceptsCodexNativeInputItems(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"thread": codexThreadMap("th_codex")}, nil
	})
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
	if _, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/work/project", Input: items}); err != nil {
		t.Fatalf("StartThread: %v", err)
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

func handleCodexRead(server *appserver.Server) {
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
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

func fuzzScenarioMapCodexItemUserMessagePreservesImageContent(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"userMessage",
		"id":"item_img",
		"content":[
			{"type":"image","url":"data:image/png;base64,aW1n","mediaType":"image/png"},
			{"type":"text","text":"caption this"},
			{"type":"localImage","path":"/tmp/local.png","name":"local.png"}
		]
	}`)

	item := mapCodexItem("turn_1", raw)
	if item.Type != "userMessage" || item.ID != "item_img" || item.TurnID != "turn_1" {
		t.Fatalf("item identity=%+v", item)
	}
	if item.Text != "caption this" {
		t.Fatalf("text=%q", item.Text)
	}
	assertCodexImageItems(t, item.Images, []appwire.InputItem{
		{Type: "input_image", Data: []byte("img"), MediaType: "image/png"},
		{Type: "local_image", Path: "/tmp/local.png", Name: "local.png"},
	})
}

func fuzzScenarioMapCodexItemUserMessagePreservesImageOnlyContent(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"userMessage",
		"id":"item_img_only",
		"content":[
			{"type":"image","url":"https://example.com/screenshot.png","mimeType":"image/png"}
		]
	}`)

	item := mapCodexItem("turn_1", raw)
	if item.Type != "userMessage" || item.Text != "" {
		t.Fatalf("item=%+v", item)
	}
	assertCodexImageItems(t, item.Images, []appwire.InputItem{
		{Type: "input_image", URL: "https://example.com/screenshot.png", MediaType: "image/png"},
	})
}

func fuzzScenarioMapCodexNotificationPreservesUserMessageImages(t *testing.T) {
	source := NewCodexSource(CodexSourceConfig{ID: "codex"}, nil)
	notification := notificationMessage(appwire.NotifyItemCompleted, map[string]any{
		"turnId": "turn_1",
		"item": map[string]any{
			"type": "userMessage",
			"id":   "item_img",
			"content": []map[string]any{
				{"type": "image", "url": "https://example.com/screenshot.png"},
			},
		},
	})

	mapped := source.mapNotification("th_codex", notification)
	if mapped.Method != appwire.NotifyItemCompleted {
		t.Fatalf("method=%q", mapped.Method)
	}
	var params struct {
		ThreadID string             `json:"threadId"`
		Ref      string             `json:"ref"`
		TurnID   string             `json:"turnId"`
		Item     appwire.ThreadItem `json:"item"`
	}
	if err := json.Unmarshal(mapped.Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.ThreadID != "th_codex" || params.Ref != "codex:th_codex" || params.TurnID != "turn_1" {
		t.Fatalf("params identity=%+v", params)
	}
	assertCodexImageItems(t, params.Item.Images, []appwire.InputItem{
		{Type: "input_image", URL: "https://example.com/screenshot.png"},
	})
}

func fuzzScenarioMapCodexItemReasoningPreservesType(t *testing.T) {
	raw := json.RawMessage(`{"type":"reasoning","id":"item_r1","text":"weighing the cache eviction logic"}`)
	item := mapCodexItem("turn_1", raw)
	if item.Type != "reasoning" {
		t.Fatalf("a reasoning item must keep type reasoning (not be mapped to a tool call), got %q: %+v", item.Type, item)
	}
	if item.ID != "item_r1" || item.TurnID != "turn_1" {
		t.Fatalf("item identity=%+v", item)
	}
}

func fuzzScenarioMapCodexNotificationNormalizesReasoningDelta(t *testing.T) {
	source := NewCodexSource(CodexSourceConfig{ID: "codex"}, nil)
	notification := notificationMessage(appwire.NotifyReasoningSummaryDelta, map[string]any{
		"turnId": "turn_1",
		"itemId": "item_r1",
		"delta":  "let me check the cache",
	})

	mapped := source.mapNotification("th_codex", notification)
	if mapped.Method != appwire.NotifyReasoningSummaryDelta {
		t.Fatalf("method=%q", mapped.Method)
	}
	var params appwire.ReasoningSummaryDeltaParams
	if err := json.Unmarshal(mapped.Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.ThreadID != "th_codex" || params.Ref != "codex:th_codex" {
		t.Fatalf("reasoning delta must be source-qualified, got threadId=%q ref=%q", params.ThreadID, params.Ref)
	}
	if params.TurnID != "turn_1" || params.ItemID != "item_r1" || params.Delta != "let me check the cache" {
		t.Fatalf("reasoning delta payload=%+v", params)
	}
}

func assertCodexImageItems(t *testing.T, got, want []appwire.InputItem) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("images=%+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Type != want[i].Type ||
			got[i].URL != want[i].URL ||
			got[i].MediaType != want[i].MediaType ||
			got[i].Path != want[i].Path ||
			got[i].Name != want[i].Name ||
			!bytes.Equal(got[i].Data, want[i].Data) {
			t.Fatalf("images[%d]=%+v, want %+v", i, got[i], want[i])
		}
	}
}

func fuzzScenarioCodexSourceStartThreadUsesCodexUserThreadSource(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
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

func fuzzScenarioCodexSourceUsesLifecycleModelMetadata(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	response := func(id string) map[string]any {
		return map[string]any{
			"thread":        codexThreadMap(id),
			"model":         "gpt-5.3-codex",
			"modelProvider": "azure",
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
		if thread.ModelProvider != "gpt-5.3-codex" || thread.Serf.Profile != "azure" {
			t.Fatalf("thread model metadata=%+v", thread)
		}
	}
}

func fuzzScenarioCodexSourceForkThreadRejectsEditAtTurnMetadata(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
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

func fuzzScenarioCodexSourceSubscribeReusesStartedThreadConnection(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	handleCodexRead(server)
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

	assertResync(t, notifications)
}

func fuzzScenarioCodexSourceSubscribeTreatsNoRolloutAsIdle(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
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

func fuzzScenarioCodexSourceStartedThreadLiveConnectionSurvivesCallerCancel(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	handleCodexRead(server)
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

	assertResync(t, notifications)
}

func fuzzScenarioCodexSourceStartThreadRetiresLiveConnectionWithoutSubscriber(t *testing.T) {
	withCodexLiveNoSubscriberTimeout(t, 20*time.Millisecond)
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
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

func fuzzScenarioCodexSourceLiveThreadFansOutToMultipleSubscribers(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	handleCodexRead(server)
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
	assertResync(t, sub1)
	assertResync(t, sub2)

	cancel1()
	// Wait for the unsubscribe goroutine to close sub1's channel.
	waitChannelClosed := func(ch <-chan appwire.Notification) bool {
		deadline := time.After(2 * time.Second)
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					return true
				}
			case <-deadline:
				return false
			}
		}
	}
	if !waitChannelClosed(sub1) {
		t.Fatal("sub1 channel was not closed after context cancellation")
	}
	server.Broadcast("th_codex", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_codex",
		Delta:    "fanout two",
	})
	assertResync(t, sub2)
	assertNoNotification(t, sub1, "fanout two")
}

func fuzzScenarioCodexSourceStartThreadSpoolsInitialTurnNotificationsForEarlySubscribers(t *testing.T) {
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
					ServerInfo: appwire.ServerInfo{Name: "codex-test"},
					SourceID:   "codex",
				}))
			case appwire.MethodThreadStart:
				_ = transport.Send(context.Background(), appwire.ResponseMessage(req.ID, map[string]any{"thread": codexThreadMap("th_codex")}))
			case appwire.MethodTurnStart:
				for range 160 {
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
			case appwire.MethodThreadRead:
				_ = transport.Send(context.Background(), appwire.ResponseMessage(req.ID, map[string]any{"thread": codexThreadMap("th_codex")}))
			default:
				_ = transport.Send(context.Background(), appwire.ErrorMessage(req.ID, appwire.MethodNotFound(req.Method)))
			}
		}
	}))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := source.StartThread(ctx, appwire.ThreadStartParams{CWD: "/work/project", Input: []appwire.InputItem{{Type: "text", Text: "hello codex"}}})
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
	assertResync(t, notifications1)
	assertResync(t, notifications2)
}

func fuzzScenarioCodexLiveThreadDoesNotReplayDeliveredLiveNotifications(t *testing.T) {
	live := &codexLiveThread{
		close:       func() error { return nil },
		done:        make(chan struct{}),
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

func fuzzScenarioCodexLiveThreadClosesSlowSubscriberInsteadOfDropping(t *testing.T) {
	live := &codexLiveThread{
		close:       func() error { return nil },
		subscribers: map[chan appwire.Notification]struct{}{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifications := live.subscribe(ctx)

	for range codexLiveSubscriberBuffer {
		live.publish(deltaNotification("buffered"))
	}
	live.publish(deltaNotification("would have dropped"))

	for i := range codexLiveSubscriberBuffer {
		if _, ok := <-notifications; !ok {
			t.Fatalf("subscriber closed before draining buffered notification %d", i)
		}
	}
	if _, ok := <-notifications; ok {
		t.Fatal("slow subscriber stayed open after its channel filled")
	}
}

func fuzzScenarioCodexLiveThreadIsClosedAfterLastSubscriberRetires(t *testing.T) {
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

func fuzzScenarioCodexSourceStartThreadWithPromptKeepsTurnNotificationsOnLiveConnection(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	handleCodexRead(server)
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
	resp, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/work/project", Input: []appwire.InputItem{{Type: "text", Text: "hello codex"}}})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: resp.Thread.Serf.Ref})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	assertResync(t, notifications)
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

func assertResync(t *testing.T, notifications <-chan appwire.Notification) {
	t.Helper()
	select {
	case got, ok := <-notifications:
		if !ok {
			t.Fatal("notification channel closed before full-state replacement")
		}
		if got.Method != appwire.NotifySerfThreadResync {
			t.Fatalf("method=%q, want %q", got.Method, appwire.NotifySerfThreadResync)
		}
		var params appwire.ThreadResyncParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatalf("params: %v", err)
		}
		if params.ThreadID != "th_codex" || params.Ref != "codex:th_codex" {
			t.Fatalf("params=%+v", params)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for full-state replacement")
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

func fuzzScenarioCodexSourceReadThreadRetriesWithoutTurnsBeforeFirstMessage(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
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

func fuzzScenarioCodexSourceSubscribeTranslatesNotifications(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	handleCodexRead(server)
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

	assertResync(t, notifications)
}

func TestCodexPartialReadDoesNotPopulateAuthoritativeCache(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	var readCount atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params struct {
		ThreadID     string `json:"threadId"`
		IncludeTurns bool   `json:"includeTurns"`
		ItemsView    string `json:"itemsView"`
	}) (map[string]any, error) {
		call := readCount.Add(1)
		thread := codexThreadMap(params.ThreadID)
		switch call {
		case 1:
			if params.IncludeTurns {
				t.Fatal("partial read unexpectedly requested turns")
			}
			thread["preview"] = "partial"
		case 2:
			if !params.IncludeTurns || params.ItemsView != "full" {
				t.Fatalf("full read shape: includeTurns=%v itemsView=%q", params.IncludeTurns, params.ItemsView)
			}
			thread["preview"] = "full"
		default:
			t.Fatalf("unexpected read call %d", call)
		}
		return map[string]any{"thread": thread}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	partial, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("partial ReadThread: %v", err)
	}
	if partial.Thread.Preview != "partial" {
		t.Fatalf("partial preview=%q", partial.Thread.Preview)
	}

	full, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
		Ref:          "codex:th_codex",
		IncludeTurns: true,
		ItemsView:    "full",
	})
	if err != nil {
		t.Fatalf("full ReadThread: %v", err)
	}
	if full.Thread.Preview != "full" {
		t.Fatalf("full preview=%q, want second upstream response", full.Thread.Preview)
	}
	if got := readCount.Load(); got != 2 {
		t.Fatalf("upstream reads=%d, want partial and full reads", got)
	}
}

func TestCodexCachedThreadReadOwnsNestedState(t *testing.T) {
	source := NewCodexSource(CodexSourceConfig{ID: "codex"}, nil)
	source.live["th_codex"] = &codexLiveThread{dirty: 1, committed: 1}
	source.cache["th_codex"] = appwire.Thread{
		ID:     "th_codex",
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive, ActiveFlags: []string{"streaming"}},
		Turns: []appwire.Turn{{
			ID:    "turn_codex",
			Error: &appwire.TurnError{CodexErrorInfo: json.RawMessage(`{"kind":"provider"}`)},
			Items: []appwire.ThreadItem{{
				ID:   "message_1",
				Type: "userMessage",
				Text: "original",
				Raw:  json.RawMessage(`{"type":"userMessage"}`),
				Images: []appwire.InputItem{{
					Type:     "input_image",
					Data:     []byte("image"),
					Metadata: map[string]string{"source": "codex"},
				}},
			}},
		}},
	}

	first, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
		Ref:          "codex:th_codex",
		IncludeTurns: true,
	})
	if err != nil {
		t.Fatalf("first ReadThread: %v", err)
	}
	first.Thread.Status.ActiveFlags[0] = "mutated"
	first.Thread.Turns[0].Error.CodexErrorInfo.(json.RawMessage)[0] = '['
	first.Thread.Turns[0].Items[0].Text = "mutated"
	first.Thread.Turns[0].Items[0].Raw[0] = '['
	first.Thread.Turns[0].Items[0].Images[0].Data[0] = 'X'
	first.Thread.Turns[0].Items[0].Images[0].Metadata["source"] = "mutated"
	first.Thread.Turns[0].Items[0].OutputImages = append(
		first.Thread.Turns[0].Items[0].OutputImages,
		appwire.OutputImage{Source: "file", URL: "/api/output-image"},
	)

	second, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
		Ref:          "codex:th_codex",
		IncludeTurns: true,
	})
	if err != nil {
		t.Fatalf("second ReadThread: %v", err)
	}
	if got := second.Thread.Status.ActiveFlags[0]; got != "streaming" {
		t.Errorf("cached active flag=%q, want streaming", got)
	}
	if got := string(second.Thread.Turns[0].Error.CodexErrorInfo.(json.RawMessage)); got != `{"kind":"provider"}` {
		t.Errorf("cached Codex error info=%q", got)
	}
	item := second.Thread.Turns[0].Items[0]
	if item.Text != "original" {
		t.Errorf("cached item text=%q, want original", item.Text)
	}
	if got := string(item.Raw); got != `{"type":"userMessage"}` {
		t.Errorf("cached item raw=%q", got)
	}
	if got := string(item.Images[0].Data); got != "image" {
		t.Errorf("cached image data=%q, want image", got)
	}
	if got := item.Images[0].Metadata["source"]; got != "codex" {
		t.Errorf("cached image metadata source=%q, want codex", got)
	}
	if len(item.OutputImages) != 0 {
		t.Errorf("cached output images=%+v, want none", item.OutputImages)
	}

	withoutTurns, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("ReadThread without turns: %v", err)
	}
	if withoutTurns.Thread.Turns != nil {
		t.Fatalf("turns=%+v, want nil when IncludeTurns is false", withoutTurns.Thread.Turns)
	}
	withoutTurns.Thread.Status.ActiveFlags[0] = "mutated again"
	final, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("final ReadThread: %v", err)
	}
	if got := final.Thread.Status.ActiveFlags[0]; got != "streaming" {
		t.Errorf("cached active flag after turnless read=%q, want streaming", got)
	}
}

func TestCodexCachedThreadConcurrentReadersDoNotShareWritableItems(t *testing.T) {
	source := NewCodexSource(CodexSourceConfig{ID: "codex"}, nil)
	source.live["th_codex"] = &codexLiveThread{dirty: 1, committed: 1}
	source.cache["th_codex"] = appwire.Thread{
		ID: "th_codex",
		Turns: []appwire.Turn{{
			ID: "turn_codex",
			Items: []appwire.ThreadItem{{
				ID:   "message_1",
				Type: "agentMessage",
				Text: "original",
			}},
		}},
	}

	read := func() appwire.Thread {
		t.Helper()
		resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{
			Ref:          "codex:th_codex",
			IncludeTurns: true,
		})
		if err != nil {
			t.Fatalf("ReadThread: %v", err)
		}
		return resp.Thread
	}
	first := read()
	second := read()

	start := make(chan struct{})
	var writers sync.WaitGroup
	for _, mutation := range []struct {
		thread *appwire.Thread
		text   string
	}{
		{thread: &first, text: "first"},
		{thread: &second, text: "second"},
	} {
		writers.Go(func() {
			<-start
			for range 1_000 {
				mutation.thread.Turns[0].Items[0].Text = mutation.text
			}
		})
	}
	close(start)
	writers.Wait()

	if got := read().Turns[0].Items[0].Text; got != "original" {
		t.Fatalf("cached item text=%q after concurrent readers, want original", got)
	}
}

func TestCodexNewLiveConnectionDoesNotQualifyPreviousCache(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	var readCount atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		readCount.Add(1)
		thread := codexThreadMap(params.ThreadID)
		thread["preview"] = "fresh"
		return map[string]any{"thread": thread}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	source.cache["th_codex"] = appwire.Thread{ID: "th_codex", Preview: "previous"}
	source.live["th_codex"] = &codexLiveThread{}

	resp, err := source.ReadThread(t.Context(), appwire.ThreadReadParams{
		Ref:          "codex:th_codex",
		IncludeTurns: true,
		ItemsView:    "full",
	})
	if err != nil {
		t.Fatalf("ReadThread before new live snapshot commit: %v", err)
	}
	if resp.Thread.Preview != "fresh" {
		t.Fatalf("preview before new live snapshot commit=%q, want fresh upstream state", resp.Thread.Preview)
	}
	if got := readCount.Load(); got != 1 {
		t.Fatalf("upstream reads=%d, want one authoritative read", got)
	}
}

func TestCodexDirtyFullStateSuppressesRawDelta(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		appserver.Subscribe(ctx, params.ThreadID)
		return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
	})
	readStarted := make(chan struct{}, 1)
	releaseRead := make(chan struct{})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		readStarted <- struct{}{}
		<-releaseRead
		thread := codexThreadMap(params.ThreadID)
		thread["preview"] = "authoritative replacement"
		return map[string]any{"thread": thread}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}

	server.Broadcast("th_codex", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_codex",
		TurnID:   "turn_codex",
		ItemID:   "item_codex",
		Delta:    "must not be forwarded",
	})

	select {
	case got := <-notifications:
		if got.Method != appwire.NotifySerfThreadResync {
			t.Fatalf("live event method=%q, want authoritative full-state resync", got.Method)
		}
	case <-readStarted:
		close(releaseRead)
		got := <-notifications
		if got.Method != appwire.NotifySerfThreadResync {
			t.Fatalf("post-read method=%q, want authoritative full-state resync", got.Method)
		}
	}
}

func TestCodexDirtyFullStateReadsAgainWhenEventRacesRead(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		appserver.Subscribe(ctx, params.ThreadID)
		return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
	})
	readStarted := make(chan int, 2)
	releaseRead := make(chan struct{}, 2)
	var readCount atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		call := int(readCount.Add(1))
		readStarted <- call
		<-releaseRead
		thread := codexThreadMap(params.ThreadID)
		thread["preview"] = fmt.Sprintf("snapshot-%d", call)
		return map[string]any{"thread": thread}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	if call := <-readStarted; call != 1 {
		t.Fatalf("first read call=%d, want 1", call)
	}

	live := source.liveThread("th_codex")
	if live == nil {
		t.Fatal("missing live thread")
	}
	live.markDirty()
	releaseRead <- struct{}{}
	if got := <-notifications; got.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("first replacement method=%q", got.Method)
	}
	if call := <-readStarted; call != 2 {
		t.Fatalf("second read call=%d, want 2", call)
	}
	releaseRead <- struct{}{}
	if got := <-notifications; got.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("second replacement method=%q", got.Method)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := source.ReadThread(canceled, appwire.ThreadReadParams{Ref: "codex:th_codex", IncludeTurns: true})
	if err != nil {
		t.Fatalf("cached ReadThread: %v", err)
	}
	if resp.Thread.Preview != "snapshot-2" {
		t.Fatalf("cached preview=%q, want latest committed snapshot", resp.Thread.Preview)
	}
	if got := readCount.Load(); got != 2 {
		t.Fatalf("upstream reads after cached ReadThread=%d, want 2", got)
	}
}

func TestCodexDirtyFullStateRetriesFinalFailedReadWithoutAnotherEvent(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		appserver.Subscribe(ctx, params.ThreadID)
		return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
	})
	var readCount atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		if readCount.Add(1) == 1 {
			return nil, appwire.Unavailable("transient full read failure")
		}
		thread := codexThreadMap(params.ThreadID)
		thread["preview"] = "recovered without another event"
		return map[string]any{"thread": thread}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	retryWaiting := make(chan time.Duration, 1)
	releaseRetry := make(chan struct{})
	source.waitReadRetry = func(ctx context.Context, delay time.Duration) error {
		retryWaiting <- delay
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseRetry:
			return nil
		}
	}
	notifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	if delay := <-retryWaiting; delay != 100*time.Millisecond {
		t.Fatalf("first retry delay=%v, want 100ms", delay)
	}
	close(releaseRetry)

	if got := <-notifications; got.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("replacement method=%q", got.Method)
	}
	if got := readCount.Load(); got != 2 {
		t.Fatalf("upstream reads=%d, want failed read plus automatic retry", got)
	}
	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex", IncludeTurns: true})
	if err != nil {
		t.Fatalf("cached ReadThread: %v", err)
	}
	if resp.Thread.Preview != "recovered without another event" {
		t.Fatalf("cached preview=%q", resp.Thread.Preview)
	}
}

func TestCodexDirtyCacheIsNotAuthoritativeWhileRefreshRetries(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		appserver.Subscribe(ctx, params.ThreadID)
		return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
	})
	var readCount atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		switch call := readCount.Add(1); call {
		case 1:
			thread := codexThreadMap(params.ThreadID)
			thread["preview"] = "committed generation 1"
			return map[string]any{"thread": thread}, nil
		case 2:
			return nil, appwire.Unavailable("generation 2 refresh failed")
		case 3:
			return nil, appwire.Unavailable("authoritative caller read failed")
		default:
			t.Fatalf("unexpected thread/read call %d", call)
			return nil, nil
		}
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	retryWaiting := make(chan struct{}, 1)
	source.waitReadRetry = func(ctx context.Context, _ time.Duration) error {
		retryWaiting <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	subscriptionCtx, cancelSubscription := context.WithCancel(t.Context())
	defer cancelSubscription()
	notifications, err := source.SubscribeThread(subscriptionCtx, appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	if got := <-notifications; got.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("generation 1 replacement method=%q", got.Method)
	}

	server.Broadcast("th_codex", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{
		ThreadID: "th_codex",
		TurnID:   "turn_codex",
		ItemID:   "item_codex",
		Delta:    "generation 2",
	})
	<-retryWaiting

	resp, err := source.ReadThread(t.Context(), appwire.ThreadReadParams{
		Ref:          "codex:th_codex",
		IncludeTurns: true,
		ItemsView:    "full",
	})
	if err == nil {
		t.Fatalf("ReadThread returned cached preview %q while generation 2 refresh was retrying", resp.Thread.Preview)
	}
	if got := readCount.Load(); got != 3 {
		t.Fatalf("thread/read calls=%d, want committed read, failed refresh, and authoritative caller read", got)
	}
}

func TestCodexDirtyFullStateReconnectForcesReadWithWarmCache(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		appserver.Subscribe(ctx, params.ThreadID)
		return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
	})
	readStarted := make(chan int, 2)
	var readCount atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		call := int(readCount.Add(1))
		readStarted <- call
		thread := codexThreadMap(params.ThreadID)
		thread["preview"] = fmt.Sprintf("connection-%d", call)
		return map[string]any{"thread": thread}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	firstNotifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("first SubscribeThread: %v", err)
	}
	if call := <-readStarted; call != 1 {
		t.Fatalf("first connection read=%d, want 1", call)
	}
	if got := <-firstNotifications; got.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("first replacement method=%q", got.Method)
	}
	firstLive := source.liveThread("th_codex")
	if firstLive == nil {
		t.Fatal("missing first live thread")
	}
	firstLive.retire()
	<-firstLive.done

	secondNotifications, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("second SubscribeThread: %v", err)
	}
	if call := <-readStarted; call != 2 {
		t.Fatalf("reconnect read=%d, want unconditional second read", call)
	}
	if got := <-secondNotifications; got.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("reconnect replacement method=%q", got.Method)
	}
	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "codex:th_codex", IncludeTurns: true})
	if err != nil {
		t.Fatalf("cached ReadThread: %v", err)
	}
	if resp.Thread.Preview != "connection-2" {
		t.Fatalf("cached preview=%q, want reconnect snapshot", resp.Thread.Preview)
	}
}

func TestCodexNoRolloutReconnectInvalidatesWarmCache(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	var resumeCount atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		if resumeCount.Add(1) == 1 {
			appserver.Subscribe(ctx, params.ThreadID)
			return map[string]any{"thread": codexThreadMap(params.ThreadID)}, nil
		}
		return nil, appwire.InvalidParams("no rollout found for thread id " + params.ThreadID)
	})
	var readCount atomic.Int32
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(_ context.Context, params struct {
		ThreadID string `json:"threadId"`
	}) (map[string]any, error) {
		call := readCount.Add(1)
		thread := codexThreadMap(params.ThreadID)
		thread["preview"] = fmt.Sprintf("snapshot-%d", call)
		return map[string]any{"thread": thread}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	defer httpServer.Close()

	source := NewCodexSource(CodexSourceConfig{ID: "codex", Endpoint: wsURL(httpServer)}, httpServer.Client())
	firstNotifications, err := source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("first SubscribeThread: %v", err)
	}
	if got := <-firstNotifications; got.Method != appwire.NotifySerfThreadResync {
		t.Fatalf("first replacement method=%q", got.Method)
	}
	firstLive := source.liveThread("th_codex")
	if firstLive == nil {
		t.Fatal("missing first live thread")
	}
	firstLive.retire()
	<-firstLive.done

	resp, err := source.ReadThread(t.Context(), appwire.ThreadReadParams{
		Ref:          "codex:th_codex",
		IncludeTurns: true,
		ItemsView:    "full",
	})
	if err != nil {
		t.Fatalf("ReadThread after live source retired: %v", err)
	}
	if resp.Thread.Preview != "snapshot-2" {
		t.Fatalf("preview after live source retired=%q, want authoritative snapshot-2", resp.Thread.Preview)
	}
	if got := readCount.Load(); got != 2 {
		t.Fatalf("upstream reads=%d, want warm-cache read plus authoritative post-retirement read", got)
	}

	secondNotifications, err := source.SubscribeThread(t.Context(), appwire.ThreadReadParams{Ref: "codex:th_codex"})
	if err != nil {
		t.Fatalf("second SubscribeThread: %v", err)
	}
	if _, ok := <-secondNotifications; ok {
		t.Fatal("no-rollout reconnect returned a live notification channel")
	}
}

func fuzzScenarioCodexSourceTurnCompletedNotificationIncludesCanonicalRef(t *testing.T) {
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "codex-test", SourceID: "codex", AdapterNativeInitialize: true})
	handleCodexRead(server)
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

	assertResync(t, notifications)
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

func fuzzScenarioCodexSourceDialErrorMapsTransportFailures(t *testing.T) {
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

func fuzzScenarioCodexSourceDialErrorPassesThroughApplicationErrors(t *testing.T) {
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

func fuzzScenarioCodexSourceDialErrorIgnoresNil(t *testing.T) {
	if got := codexSourceDialError(nil); got != nil {
		t.Fatalf("nil mapped to %v, want nil", got)
	}
}

func fuzzScenarioCodexSourceCallErrorMapsTransportFailures(t *testing.T) {
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
		{"i/o timeout", "appwire turn/start: write tcp: i/o timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := appwire.InternalError(tc.msg)
			got := codexSourceCallError(in)
			assertSessionUnavailable(t, got, tc.name)
		})
	}
}

func fuzzScenarioCodexSourceCallErrorMapsRawTransportFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"ECONNRESET", &net.OpError{Op: "write", Err: syscall.ECONNRESET}},
		{"broken pipe string", errors.New("write tcp: broken pipe")},
		{"closed connection string", errors.New("use of closed network connection")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := codexSourceCallError(tc.err)
			assertSessionUnavailable(t, got, tc.name)
		})
	}
}

func fuzzScenarioCodexSourceCallErrorPassesThroughApplicationErrors(t *testing.T) {
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
		t.Fatalf("semantic internal error was rewritten: got %T=%v", got, got)
	}

	if got := codexSourceCallError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("context.Canceled remapped: %v", got)
	}
	if got := codexSourceCallError(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("context.DeadlineExceeded remapped: %v", got)
	}
}

func fuzzScenarioCodexSourceListThreadsMapsDialRefusedToSessionUnavailable(t *testing.T) {
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
