package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStatusEndpoint_Idle(t *testing.T) {
	srv := NewServer(ServerConfig{})

	srv.SetStatus(StatusInfo{
		SessionID: "test-123",
		State:     "IDLE",
		Turns:     5,
		Model:     "gpt-5",
		Profile:   "openai",
	})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}

	var status StatusInfo
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.SessionID != "test-123" {
		t.Errorf("session_id: got %q, want test-123", status.SessionID)
	}
	if status.State != "IDLE" {
		t.Errorf("state: got %q, want IDLE", status.State)
	}
}

func TestStatusEndpoint_ContextPressure(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{
		SessionID: "test-456",
		State:     "IDLE",
		Model:     "gpt-4o",
	})
	srv.SetContextPressureFunc(func() float64 { return 0.42 })

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}

	var status StatusInfo
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.ContextPressure != 0.42 {
		t.Errorf("context_pressure: got %f, want 0.42", status.ContextPressure)
	}
}

func TestInterruptEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer(ServerConfig{})
	srv.SetCancelFunc(cancel)

	req := httptest.NewRequest(http.MethodPost, "/interrupt", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}

	select {
	case <-ctx.Done():
		// expected
	default:
		t.Error("context should be cancelled after interrupt")
	}
}

func TestStatusEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}

func TestInputEndpoint_Accepted(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)

	body := strings.NewReader(`{"text":"hello world"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status code: got %d, want 202", w.Code)
	}

	select {
	case text := <-srv.InputCh():
		if text != "hello world" {
			t.Errorf("input: got %q, want %q", text, "hello world")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout reading input")
	}
}

func TestInputEndpoint_Conflict(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)

	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status code: got %d, want 409", w.Code)
	}
}

func TestInputEndpoint_EmptyText(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)

	body := strings.NewReader(`{"text":""}`)
	req := httptest.NewRequest(http.MethodPost, "/input", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status code: got %d, want 400", w.Code)
	}
}

func TestEventsEndpoint_SSE(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})

	// Start a test HTTP server
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Connect to SSE endpoint
	resp, err := http.Get(ts.URL + "/events")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type: got %q, want text/event-stream", ct)
	}

	// Send an event through the broadcaster
	srv.Broadcast("TEST_EVENT", map[string]string{"msg": "hello"})

	// Read the SSE event
	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	output := string(buf[:n])
	if !strings.Contains(output, "event: TEST_EVENT") {
		t.Errorf("expected event type in output, got: %s", output)
	}
	if !strings.Contains(output, `"msg":"hello"`) {
		t.Errorf("expected data in output, got: %s", output)
	}
}

func TestCompactEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})

	called := false
	srv.SetCompactFunc(func(ctx context.Context) error {
		called = true
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/compact", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}
	if !called {
		t.Error("compact function should have been called")
	}
}

func TestCompactEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/compact", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
	}
}

func TestCompactEndpoint_Error(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetCompactFunc(func(ctx context.Context) error {
		return fmt.Errorf("compaction failed")
	})

	req := httptest.NewRequest(http.MethodPost, "/compact", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status code: got %d, want 500", w.Code)
	}
}

func TestCompactEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/compact", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}

func TestClearEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})

	called := false
	srv.SetClearFunc(func(ctx context.Context) error {
		called = true
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/clear", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}
	if !called {
		t.Error("clear function not called")
	}
}

func TestClearEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/clear", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
	}
}

func TestModelEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})

	var gotModel string
	srv.SetModelFunc(func(model string) {
		gotModel = model
	})

	body := strings.NewReader(`{"model":"gpt-4o-mini"}`)
	req := httptest.NewRequest(http.MethodPost, "/model", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status code: got %d, want 204", w.Code)
	}
	if gotModel != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", gotModel)
	}
}

func TestModelEndpoint_EmptyModel(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetModelFunc(func(model string) {})

	body := strings.NewReader(`{"model":""}`)
	req := httptest.NewRequest(http.MethodPost, "/model", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status code: got %d, want 400", w.Code)
	}
}

func TestModelEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	body := strings.NewReader(`{"model":"gpt-4o"}`)
	req := httptest.NewRequest(http.MethodPost, "/model", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
	}
}

func TestStatusEndpoint_DetailedStatus(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{
		SessionID: "test-789",
		State:     "IDLE",
		Model:     "gpt-5",
		Profile:   "openai",
	})

	srv.SetDetailedStatusFunc(func() DetailedStatus {
		return DetailedStatus{
			Tools: []ToolInfo{
				{Name: "shell", Source: "core"},
				{Name: "linear__search", Source: "mcp:streamlinear"},
			},
			MCP: []MCPServerInfo{
				{Name: "streamlinear", Tools: []string{"linear__search"}},
			},
			Skills: []SkillInfo{
				{Name: "brainstorming", Description: "brainstorm stuff"},
			},
			Plugins: []PluginStatusInfo{
				{Name: "superpowers", Version: "4.3.0", SkillCount: 8, HookCount: 12},
			},
			Hooks: map[string]int{
				"PreToolUse":   3,
				"SessionStart": 1,
			},
			Agents: []string{"superpowers:code-reviewer"},
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}

	var status StatusInfo
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.SessionID != "test-789" {
		t.Errorf("session_id: got %q, want test-789", status.SessionID)
	}
	if status.Detailed == nil {
		t.Fatal("expected detailed status to be present")
	}
	if len(status.Detailed.Tools) != 2 {
		t.Errorf("tools: got %d, want 2", len(status.Detailed.Tools))
	}
	if len(status.Detailed.MCP) != 1 {
		t.Errorf("mcp: got %d, want 1", len(status.Detailed.MCP))
	}
	if status.Detailed.MCP[0].Name != "streamlinear" {
		t.Errorf("mcp name: got %q, want streamlinear", status.Detailed.MCP[0].Name)
	}
	if len(status.Detailed.Skills) != 1 {
		t.Errorf("skills: got %d, want 1", len(status.Detailed.Skills))
	}
	if len(status.Detailed.Plugins) != 1 {
		t.Errorf("plugins: got %d, want 1", len(status.Detailed.Plugins))
	}
	if status.Detailed.Hooks["PreToolUse"] != 3 {
		t.Errorf("hooks PreToolUse: got %d, want 3", status.Detailed.Hooks["PreToolUse"])
	}
	if len(status.Detailed.Agents) != 1 {
		t.Errorf("agents: got %d, want 1", len(status.Detailed.Agents))
	}
}

func TestStatusEndpoint_NoDetailedStatusFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{
		SessionID: "test-no-detail",
		State:     "IDLE",
	})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}

	var status StatusInfo
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Detailed != nil {
		t.Error("expected nil detailed status when no func set")
	}
}

func TestEventsEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})
	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}

func TestModelsEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{})

	srv.SetListModelsFunc(func(ctx context.Context) ([]ModelsResponseItem, error) {
		return []ModelsResponseItem{
			{ID: "gpt-4o", DisplayName: "gpt-4o"},
			{ID: "gpt-4o-mini", DisplayName: "gpt-4o-mini"},
		}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", w.Code)
	}

	var resp ModelsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(resp.Models))
	}
	if resp.Models[0].ID != "gpt-4o" {
		t.Errorf("models[0].id = %q", resp.Models[0].ID)
	}
}

func TestModelsEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
	}
}

func TestModelsEndpoint_Error(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetListModelsFunc(func(ctx context.Context) ([]ModelsResponseItem, error) {
		return nil, fmt.Errorf("upstream error")
	})

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status code: got %d, want 502", w.Code)
	}
}

func TestModelsEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/models", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}
