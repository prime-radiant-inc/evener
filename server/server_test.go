package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
)

func TestStatusEndpoint_Idle(t *testing.T) {
	srv := NewServer(ServerConfig{})

	srv.SetStatus(StatusInfo{
		SessionID: "test-123",
		State:     "idle",
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
	if status.State != "idle" {
		t.Errorf("state: got %q, want idle", status.State)
	}
}

func TestStatusEndpoint_ContextPressure(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{
		SessionID: "test-456",
		State:     "idle",
		Model:     "gpt-4o",
	})
	srv.SetContextPressureFunc(func() float64 { return 0.42 })
	srv.SetContextMetricsFunc(func() ContextMetrics {
		return ContextMetrics{Used: 42000, Window: 100000, Remaining: 58000}
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
	if status.ContextPressure != 0.42 {
		t.Errorf("context_pressure: got %f, want 0.42", status.ContextPressure)
	}
	if status.ContextUsed != 42000 || status.ContextWindow != 100000 || status.ContextRemaining != 58000 {
		t.Fatalf("context metrics: got used=%d window=%d remaining=%d", status.ContextUsed, status.ContextWindow, status.ContextRemaining)
	}
}

func TestStatus_IncludesWorkingDir(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.UpdateSessionInfo("01SESS001", "gpt-5", "openai-gpt-5")
	srv.SetWorkingDir("/tmp/test-wd")

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status code: got %d, want 200", rec.Code)
	}
	var got StatusInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.WorkingDir != "/tmp/test-wd" {
		t.Fatalf("WorkingDir: got %q, want %q", got.WorkingDir, "/tmp/test-wd")
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

// TestInterruptEndpoint_NoCancelFunc verifies the daemon's REST
// /interrupt handler returns 503 (mirroring the appwire path's
// Unavailable error) instead of silently 204'ing when no cancel
// function is registered. Without this, callers can't tell whether
// the turn was actually cancelled. Regression test for kata k7t8.
func TestInterruptEndpoint_NoCancelFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/interrupt", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d, want 503", w.Code)
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
	case msg := <-srv.InputCh():
		if msg.Text != "hello world" {
			t.Errorf("input: got %q, want %q", msg.Text, "hello world")
		}
		if len(msg.Images) != 0 {
			t.Errorf("images: got %d, want 0", len(msg.Images))
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

func TestInputEndpoint_ClosedSessionConflict(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetState("closed")

	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/input", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status code: got %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "session is closed") {
		t.Fatalf("body=%q", w.Body.String())
	}
}

func TestInputEndpoint_EmptyTextAndNoImages(t *testing.T) {
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

func TestInputEndpoint_TextAndImage(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)

	// 1x1 transparent PNG
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	reqBody := InputRequest{
		Text: "caption",
		Images: []ImageAttachment{
			{MediaType: "image/png", Data: pngBytes, Name: "x.png"},
		},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/input", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status code: got %d, want 202", w.Code)
	}

	select {
	case msg := <-srv.InputCh():
		if msg.Text != "caption" {
			t.Errorf("text: got %q, want %q", msg.Text, "caption")
		}
		if len(msg.Images) != 1 {
			t.Fatalf("images: got %d, want 1", len(msg.Images))
		}
		if msg.Images[0].MediaType != "image/png" {
			t.Errorf("media_type: got %q, want image/png", msg.Images[0].MediaType)
		}
		if msg.Images[0].Name != "x.png" {
			t.Errorf("name: got %q, want x.png", msg.Images[0].Name)
		}
		if !bytes.Equal(msg.Images[0].Data, pngBytes) {
			t.Errorf("data mismatch")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout reading input")
	}
}

func TestInputEndpoint_ImageOnly(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)

	reqBody := InputRequest{
		Text: "",
		Images: []ImageAttachment{
			{MediaType: "image/png", Data: []byte{0x89, 0x50}, Name: "x.png"},
		},
	}
	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/input", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status code: got %d, want 202", w.Code)
	}

	select {
	case msg := <-srv.InputCh():
		if msg.Text != "" {
			t.Errorf("text: got %q, want empty", msg.Text)
		}
		if len(msg.Images) != 1 {
			t.Fatalf("images: got %d, want 1", len(msg.Images))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout reading input")
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
		return errors.New("compaction failed")
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

func TestClear_409WhileProcessing(t *testing.T) {
	srv := NewServer(ServerConfig{})
	called := false
	srv.SetClearFunc(func(ctx context.Context) error {
		called = true
		return nil
	})
	srv.SetProcessing(true)

	req := httptest.NewRequest(http.MethodPost, "/clear", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want %d (409)", rec.Code, http.StatusConflict)
	}
	if called {
		t.Fatal("clearFunc should not have been called while processing")
	}
}

func TestClear_OKWhenIdle(t *testing.T) {
	srv := NewServer(ServerConfig{})
	called := false
	srv.SetClearFunc(func(ctx context.Context) error {
		called = true
		return nil
	})
	srv.SetProcessing(false)

	req := httptest.NewRequest(http.MethodPost, "/clear", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusNoContent)
	}
	if !called {
		t.Fatal("clearFunc should have been called when idle")
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
		State:     "idle",
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
		State:     "idle",
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
		return nil, errors.New("upstream error")
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

func TestShutdown_InvokesCallback(t *testing.T) {
	srv := NewServer(ServerConfig{})
	called := make(chan struct{}, 1)
	srv.SetShutdownFunc(func() {
		called <- struct{}{}
	})

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d (202)", rec.Code, http.StatusAccepted)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not invoked within 1s")
	}
}

func TestShutdown_WritesResponseBeforeCallback(t *testing.T) {
	srv := NewServer(ServerConfig{})
	called := make(chan struct{}, 1)
	srv.SetShutdownFunc(func() {
		called <- struct{}{}
	})

	rec := &blockingHeaderRecorder{
		header:        http.Header{},
		headerStarted: make(chan struct{}, 1),
		releaseHeader: make(chan struct{}),
		flushStarted:  make(chan struct{}, 1),
		releaseFlush:  make(chan struct{}),
	}
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-rec.headerStarted:
	case <-time.After(time.Second):
		t.Fatal("shutdown response was not started")
	}
	select {
	case <-called:
		t.Fatal("shutdown callback ran before the response write completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(rec.releaseHeader)
	select {
	case <-rec.flushStarted:
	case <-time.After(time.Second):
		t.Fatal("shutdown response was not flushed")
	}
	select {
	case <-called:
		t.Fatal("shutdown callback ran before the response flush completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(rec.releaseFlush)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown handler did not complete")
	}
	if rec.status != http.StatusAccepted {
		t.Fatalf("status: got %d, want %d (202)", rec.status, http.StatusAccepted)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not invoked after response completed")
	}
}

type blockingHeaderRecorder struct {
	header        http.Header
	headerStarted chan struct{}
	releaseHeader chan struct{}
	flushStarted  chan struct{}
	releaseFlush  chan struct{}
	status        int
}

func (r *blockingHeaderRecorder) Header() http.Header {
	return r.header
}

func (r *blockingHeaderRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return len(p), nil
}

func (r *blockingHeaderRecorder) WriteHeader(status int) {
	r.status = status
	select {
	case r.headerStarted <- struct{}{}:
	default:
	}
	<-r.releaseHeader
}

func (r *blockingHeaderRecorder) Flush() {
	select {
	case r.flushStarted <- struct{}{}:
	default:
	}
	<-r.releaseFlush
}

func TestShutdown_503WhenUnregistered(t *testing.T) {
	srv := NewServer(ServerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want %d (503)", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestShutdown_RejectsGET(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetShutdownFunc(func() {})

	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestQueueEndpoint_Accepted(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	var received []string
	srv.SetQueueFunc(func(text string) error {
		received = append(received, text)
		return nil
	})

	body := strings.NewReader(`{"text":"queued msg"}`)
	req := httptest.NewRequest(http.MethodPost, "/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want 202; body=%q", rec.Code, rec.Body.String())
	}
	if len(received) != 1 || received[0] != "queued msg" {
		t.Fatalf("received=%v", received)
	}
}

func TestQueueEndpoint_RejectsWhenIdle(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)
	srv.SetQueueFunc(func(string) error { return nil })

	body := strings.NewReader(`{"text":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%q", rec.Code, rec.Body.String())
	}
}

func TestQueueEndpoint_RejectsEmptyText(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	srv.SetQueueFunc(func(string) error { return nil })
	body := strings.NewReader(`{"text":""}`)
	req := httptest.NewRequest(http.MethodPost, "/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%q", rec.Code, rec.Body.String())
	}
}

func TestQueueEndpoint_NoFunc(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	body := strings.NewReader(`{"text":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/queue", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503; body=%q", rec.Code, rec.Body.String())
	}
}

func TestDrainAsSteerEndpoint_NoContent(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	called := 0
	srv.SetDrainAsSteerFunc(func() error { called++; return nil })
	srv.SetQueueDepthFunc(func() int { return 2 })

	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
	if called != 1 {
		t.Fatalf("called=%d, want 1", called)
	}
}

func TestDrainAsSteerEndpoint_WithInputBypassesEmptyQueue(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
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

	body := strings.NewReader(`{"text":"composer payload","images":[{"media_type":"image/png","data":"cG5n","name":"shot.png"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
	if gotText != "composer payload" {
		t.Fatalf("text=%q, want composer payload", gotText)
	}
	if len(gotImages) != 1 || gotImages[0].Name != "shot.png" || string(gotImages[0].Data) != "png" {
		t.Fatalf("images=%+v", gotImages)
	}
}

func TestDrainAsSteerEndpoint_RejectsEmpty(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(true)
	srv.SetDrainAsSteerFunc(func() error { return nil })
	srv.SetQueueDepthFunc(func() int { return 0 })

	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409 (empty queue); body=%q", rec.Code, rec.Body.String())
	}
}

func TestDrainAsSteerEndpoint_RejectsWhenIdle(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetProcessing(false)
	srv.SetDrainAsSteerFunc(func() error { return nil })

	req := httptest.NewRequest(http.MethodPost, "/drain-as-steer", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409 (idle); body=%q", rec.Code, rec.Body.String())
	}
}

func TestStatusCapabilities_QueueGatedByProcessing(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetQueueFunc(func(string) error { return nil })

	// Idle: Queue must be false even when QueueFunc is set.
	srv.SetProcessing(false)
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "idle"})
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code=%d", rec.Code)
	}
	var idle StatusInfo
	if err := json.NewDecoder(rec.Body).Decode(&idle); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if idle.Capabilities.Queue {
		t.Fatalf("Queue should be false when idle")
	}

	// Processing: Queue flips to true.
	srv.SetProcessing(true)
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "active"})
	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var processing StatusInfo
	if err := json.NewDecoder(rec.Body).Decode(&processing); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !processing.Capabilities.Queue {
		t.Fatalf("Queue should be true while processing")
	}
}

func TestSubmitNotification_PushesEntryNotification(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SubmitNotification()
	select {
	case msg := <-srv.InputCh():
		if msg.Kind != agent.EntryNotification {
			t.Errorf("Kind: got %v, want EntryNotification", msg.Kind)
		}
		if msg.Text != "" {
			t.Errorf("Text: got %q, want empty", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: SubmitNotification did not deliver a message")
	}
}

func TestSubmitNotification_DropIfFull(t *testing.T) {
	srv := NewServer(ServerConfig{})
	// Fill the 1-slot buffer.
	srv.SubmitNotification()
	// Second call must not block even though the channel is full.
	done := make(chan struct{})
	go func() {
		srv.SubmitNotification()
		close(done)
	}()
	select {
	case <-done:
		// expected: returned without blocking
	case <-time.After(time.Second):
		t.Fatal("SubmitNotification blocked on full channel")
	}
}
