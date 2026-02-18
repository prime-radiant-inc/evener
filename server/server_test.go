package server

import (
	"context"
	"encoding/json"
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
