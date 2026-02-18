package server

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent"
)

func TestBridge_ForwardsEvents(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	events := make(chan agent.SessionEvent, 10)

	go Bridge(srv, events)

	events <- agent.SessionEvent{
		Kind:      agent.EventAssistantTextDelta,
		SessionID: "s1",
		Data:      agent.AssistantTextDeltaData{Delta: "hello"},
	}
	close(events)

	// Give bridge time to process
	time.Sleep(50 * time.Millisecond)

	items := srv.broadcaster.ring.After(0)
	if len(items) == 0 {
		t.Fatal("expected at least one event in ring buffer")
	}
}

func TestBridge_UpdatesStatusOnSessionStart(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	events := make(chan agent.SessionEvent, 10)

	go Bridge(srv, events)

	events <- agent.SessionEvent{
		Kind:      agent.EventSessionStart,
		SessionID: "s1",
		Data: agent.SessionStartData{
			Profile: "openai",
			Model:   "gpt-5",
		},
	}
	close(events)
	time.Sleep(50 * time.Millisecond)

	srv.mu.RLock()
	status := srv.status
	srv.mu.RUnlock()

	if status.SessionID != "s1" {
		t.Errorf("session_id: got %q, want s1", status.SessionID)
	}
	if status.Model != "gpt-5" {
		t.Errorf("model: got %q, want gpt-5", status.Model)
	}
}

func TestBridge_SetsProcessingOnUserInput(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	events := make(chan agent.SessionEvent, 10)

	go Bridge(srv, events)

	events <- agent.SessionEvent{
		Kind:      agent.EventUserInput,
		SessionID: "s1",
		Data:      agent.UserInputData{Text: "hello"},
	}
	close(events)
	time.Sleep(50 * time.Millisecond)

	srv.mu.RLock()
	state := srv.status.State
	processing := srv.processing
	srv.mu.RUnlock()

	if state != "PROCESSING" {
		t.Errorf("state: got %q, want PROCESSING", state)
	}
	if !processing {
		t.Error("processing: got false, want true")
	}
}

func TestBridge_IncrementsturnsOnAssistantTextEnd(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	events := make(chan agent.SessionEvent, 10)

	go Bridge(srv, events)

	events <- agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		SessionID: "s1",
		Data:      agent.AssistantTextEndData{Text: "hi"},
	}
	events <- agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		SessionID: "s1",
		Data:      agent.AssistantTextEndData{Text: "bye"},
	}
	close(events)
	time.Sleep(50 * time.Millisecond)

	srv.mu.RLock()
	turns := srv.status.Turns
	srv.mu.RUnlock()

	if turns != 2 {
		t.Errorf("turns: got %d, want 2", turns)
	}
}

func TestBridge_ClosesOnSessionEnd(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	events := make(chan agent.SessionEvent, 10)

	go Bridge(srv, events)

	events <- agent.SessionEvent{
		Kind:      agent.EventSessionEnd,
		SessionID: "s1",
		Data:      agent.SessionEndData{Reason: "done"},
	}
	close(events)
	time.Sleep(50 * time.Millisecond)

	srv.mu.RLock()
	state := srv.status.State
	processing := srv.processing
	srv.mu.RUnlock()

	if state != "CLOSED" {
		t.Errorf("state: got %q, want CLOSED", state)
	}
	if processing {
		t.Error("processing: got true, want false")
	}
}
