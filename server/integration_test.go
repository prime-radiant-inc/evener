package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
)

func TestIntegration_InputToAppwire(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})

	// Simulate session events
	evs := make(chan events.SessionEvent, 10)
	go Bridge(srv, evs)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Verify /status returns 200 with well-formed JSON before any events.
	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("status code: got %d, want 200", resp.StatusCode)
	}
	var status StatusInfo
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		resp.Body.Close()
		t.Fatalf("status decode: %v", err)
	}
	resp.Body.Close()
	if !status.Capabilities.Send {
		t.Errorf("capabilities.send: got false, want true (session is idle and ready)")
	}

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

	close(evs)
}

func TestIntegration_StatusUpdates(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})

	evs := make(chan events.SessionEvent, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Send session start event, then close to let Bridge drain and exit.
	evs <- events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "test-session",
		Data: events.SessionStartData{
			Profile: "openai",
			Model:   "gpt-5",
		},
	}
	close(evs)
	<-done // Bridge has processed all events; status is now stable.

	// Check status via HTTP
	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	defer resp.Body.Close()

	var status StatusInfo
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("status decode: %v", err)
	}

	if status.SessionID != "test-session" {
		t.Errorf("session_id: got %q, want test-session", status.SessionID)
	}
	if status.Model != "gpt-5" {
		t.Errorf("model: got %q, want gpt-5", status.Model)
	}
	if status.State != "idle" {
		t.Errorf("state: got %q, want idle", status.State)
	}
}
