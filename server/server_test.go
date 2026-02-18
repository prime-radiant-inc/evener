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

func TestEventsEndpoint_MethodNotAllowed(t *testing.T) {
	srv := NewServer(ServerConfig{})
	req := httptest.NewRequest(http.MethodPost, "/events", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code: got %d, want 405", w.Code)
	}
}
