package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
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

	status := srv.GetStatus()
	if status.SessionID != "s1" {
		t.Errorf("session_id: got %q, want s1", status.SessionID)
	}
	if status.Model != "gpt-5" {
		t.Errorf("model: got %q, want gpt-5", status.Model)
	}
	if status.State != "IDLE" {
		t.Errorf("state: got %q, want IDLE", status.State)
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

	status := srv.GetStatus()
	if status.Turns != 2 {
		t.Errorf("turns: got %d, want 2", status.Turns)
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

	status := srv.GetStatus()
	if status.State != "CLOSED" {
		t.Errorf("state: got %q, want CLOSED", status.State)
	}

	srv.mu.RLock()
	processing := srv.processing
	srv.mu.RUnlock()
	if processing {
		t.Error("processing: got true, want false")
	}
}

func TestBridge_UsesSessionEndStateWhenProvided(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	events := make(chan agent.SessionEvent, 10)

	go Bridge(srv, events)

	events <- agent.SessionEvent{
		Kind:      agent.EventSessionEnd,
		SessionID: "s1",
		Data:      agent.SessionEndData{Reason: "input_complete", State: "IDLE"},
	}
	close(events)
	time.Sleep(50 * time.Millisecond)

	status := srv.GetStatus()
	if status.State != "IDLE" {
		t.Errorf("state: got %q, want IDLE", status.State)
	}
}

func TestBridge_IgnoresStaleEventsAfterSessionIdentityChanges(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	srv.SetAppIdentity("local", "new-session")
	srv.UpdateSessionInfo("new-session", "gpt-5", "openai")
	srv.SetState("IDLE")
	events := make(chan agent.SessionEvent, 10)
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, events)
	}()

	events <- agent.SessionEvent{
		Kind:      agent.EventSessionEnd,
		SessionID: "old-session",
		Data:      agent.SessionEndData{Reason: "clear", State: "CLOSED"},
	}
	close(events)
	<-done

	status := srv.GetStatus()
	if status.SessionID != "new-session" || status.State != "IDLE" {
		t.Fatalf("status after stale event=%+v, want new-session IDLE", status)
	}
	if items := srv.broadcaster.ring.After(0); len(items) != 0 {
		t.Fatalf("stale event was broadcast: %+v", items)
	}
	if items := srv.AppNotificationsAfter(0, "new-session"); len(items) != 0 {
		t.Fatalf("stale event was projected under new session: %+v", items)
	}
}

func TestBridgeWithObserver_InvokesObserverAndForwardsEvents(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	events := make(chan agent.SessionEvent, 10)
	observed := make(chan agent.SessionEvent, 1)
	done := make(chan struct{})

	go func() {
		defer close(done)
		BridgeWithObserver(srv, events, func(ev agent.SessionEvent) {
			observed <- ev
		})
	}()

	want := agent.SessionEvent{
		Kind:      agent.EventAssistantTextDelta,
		SessionID: "s1",
		Data:      agent.AssistantTextDeltaData{Delta: "hello"},
	}
	events <- want
	close(events)
	<-done

	select {
	case got := <-observed:
		if got.Kind != want.Kind || got.SessionID != want.SessionID {
			t.Fatalf("observer saw %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("observer was not invoked")
	}

	items := srv.broadcaster.ring.After(0)
	if len(items) == 0 {
		t.Fatal("expected forwarded event in ring buffer")
	}
}

func TestBridge_SessionStartEnrichesSSEData(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	events := make(chan agent.SessionEvent, 10)

	go Bridge(srv, events)

	events <- agent.SessionEvent{
		Kind:      agent.EventSessionStart,
		SessionID: "sess-abc",
		Data: agent.SessionStartData{
			Profile: "anthropic",
			Model:   "claude-4",
		},
	}
	close(events)
	time.Sleep(50 * time.Millisecond)

	items := srv.broadcaster.ring.After(0)
	if len(items) == 0 {
		t.Fatal("expected at least one event in ring buffer")
	}
	ev, ok := items[0].Value.(sseEvent)
	if !ok {
		t.Fatal("expected sseEvent in ring buffer")
	}
	if ev.Type != "SESSION_START" {
		t.Errorf("event type: got %q, want SESSION_START", ev.Type)
	}
	// The data should be json.RawMessage containing session_id
	raw, ok := ev.Data.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage data, got %T", ev.Data)
	}
	dataStr := string(raw)
	if !strings.Contains(dataStr, `"session_id":"sess-abc"`) {
		t.Errorf("SSE data should contain session_id, got: %s", dataStr)
	}
}

func TestBridge_RecordsAppWireNotifications(t *testing.T) {
	srv := NewServer(ServerConfig{RingBufferSize: 100})
	srv.SetAppIdentity("local", "th_1")
	events := make(chan agent.SessionEvent, 10)

	go Bridge(srv, events)

	events <- agent.SessionEvent{
		Kind:      agent.EventUserInput,
		SessionID: "th_1",
		Data:      agent.UserInputData{Text: "hello"},
	}
	events <- agent.SessionEvent{
		Kind:      agent.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      agent.AssistantTextDeltaData{Delta: "hi"},
	}
	close(events)
	time.Sleep(50 * time.Millisecond)

	items := srv.AppNotificationsAfter(0, "th_1")
	if len(items) == 0 {
		t.Fatal("expected app-wire notifications")
	}
	found := false
	for _, item := range items {
		if item.Notification.Method == appwire.NotifyAgentMessageDelta {
			found = true
		}
	}
	if !found {
		t.Fatalf("notifications=%+v", items)
	}
}
