package server

import (
	"testing"

	"primeradiant.com/evener/agent/events"
)

func TestIntegration_StatusUpdates(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})

	evs := make(chan events.SessionEvent, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

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

	thread := srv.appThread()
	if thread.SessionID != "test-session" {
		t.Errorf("session_id: got %q, want test-session", thread.SessionID)
	}
	if thread.ModelProvider != "gpt-5" {
		t.Errorf("model: got %q, want gpt-5", thread.ModelProvider)
	}
	if thread.Status.Type != "idle" {
		t.Errorf("state: got %q, want idle", thread.Status.Type)
	}
}
