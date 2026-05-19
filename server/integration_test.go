package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
)

func TestIntegration_InputToAppwire(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})

	// Simulate session events
	events := make(chan agent.SessionEvent, 10)
	go Bridge(srv, events)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Verify status returns valid JSON
	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var status StatusInfo
	json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()

	// Send input
	inputBody := strings.NewReader(`{"text":"hello"}`)
	resp, err = http.Post(ts.URL+"/input", "application/json", inputBody)
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("input status: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Read the input from the server's channel
	select {
	case msg := <-srv.InputCh():
		if msg.Text != "hello" {
			t.Errorf("input text: got %q, want hello", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for input")
	}

	// Verify interrupt returns 204
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.SetCancelFunc(cancel)

	resp, err = http.Post(ts.URL+"/interrupt", "", nil)
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("interrupt status: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify cancel was actually called
	select {
	case <-ctx.Done():
		// expected
	default:
		t.Error("context should be cancelled after interrupt")
	}

	close(events)
}

func TestIntegration_StatusUpdates(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})

	events := make(chan agent.SessionEvent, 10)
	go Bridge(srv, events)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Send session start event
	events <- agent.SessionEvent{
		Kind:      agent.EventSessionStart,
		SessionID: "test-session",
		Data: agent.SessionStartData{
			Profile: "openai",
			Model:   "gpt-5",
		},
	}

	// Give bridge time to process
	time.Sleep(50 * time.Millisecond)

	// Check status via HTTP
	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	defer resp.Body.Close()

	var status StatusInfo
	json.NewDecoder(resp.Body).Decode(&status)

	if status.SessionID != "test-session" {
		t.Errorf("session_id: got %q, want test-session", status.SessionID)
	}
	if status.Model != "gpt-5" {
		t.Errorf("model: got %q, want gpt-5", status.Model)
	}
	if status.State != "IDLE" {
		t.Errorf("state: got %q, want IDLE", status.State)
	}

	close(events)
}
