package server

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
)

func TestBridge_ForwardsEvents(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)

	go Bridge(srv, evs)

	evs <- events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "s1",
		Data:      events.AssistantTextDeltaData{Delta: "hello"},
	}
	close(evs)

	// Give bridge time to process
	time.Sleep(50 * time.Millisecond)

	items := srv.AppNotificationsAfter(0, "s1")
	if len(items) == 0 {
		t.Fatal("expected at least one appwire notification")
	}
}

func TestBridge_UpdatesStatusOnSessionStart(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)

	go Bridge(srv, evs)

	evs <- events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "s1",
		Data: events.SessionStartData{
			Profile: "openai",
			Model:   "gpt-5",
		},
	}
	close(evs)
	time.Sleep(50 * time.Millisecond)

	status := srv.GetStatus()
	if status.SessionID != "s1" {
		t.Errorf("session_id: got %q, want s1", status.SessionID)
	}
	if status.Model != "gpt-5" {
		t.Errorf("model: got %q, want gpt-5", status.Model)
	}
	if status.State != "idle" {
		t.Errorf("state: got %q, want idle", status.State)
	}
}

func TestBridge_IncrementsturnsOnAssistantTextEnd(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)

	go Bridge(srv, evs)

	evs <- events.SessionEvent{
		Kind:      events.EventAssistantTextEnd,
		SessionID: "s1",
		Data:      events.AssistantTextEndData{Text: "hi"},
	}
	evs <- events.SessionEvent{
		Kind:      events.EventAssistantTextEnd,
		SessionID: "s1",
		Data:      events.AssistantTextEndData{Text: "bye"},
	}
	close(evs)
	time.Sleep(50 * time.Millisecond)

	status := srv.GetStatus()
	if status.Turns != 2 {
		t.Errorf("turns: got %d, want 2", status.Turns)
	}
}

func TestBridge_ClosesOnSessionEnd(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)

	go Bridge(srv, evs)

	evs <- events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "s1",
		Data:      events.SessionEndData{Reason: "done"},
	}
	close(evs)
	time.Sleep(50 * time.Millisecond)

	status := srv.GetStatus()
	if status.State != "closed" {
		t.Errorf("state: got %q, want closed", status.State)
	}

	srv.mu.RLock()
	processing := srv.processing
	srv.mu.RUnlock()
	if processing {
		t.Error("processing: got true, want false")
	}
}

func TestBridge_UsesSessionEndStateWhenProvided(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)

	go Bridge(srv, evs)

	evs <- events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "s1",
		Data:      events.SessionEndData{Reason: "input_complete", State: "idle"},
	}
	close(evs)
	time.Sleep(50 * time.Millisecond)

	status := srv.GetStatus()
	if status.State != "idle" {
		t.Errorf("state: got %q, want idle", status.State)
	}
}

func TestBridge_InterruptedSessionEndDoesNotClearProcessing(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	srv.SetProcessing(true)
	srv.SetState("active")
	evs := make(chan events.SessionEvent, 10)

	go Bridge(srv, evs)

	evs <- events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "s1",
		Data:      events.SessionEndData{Reason: "interrupted", State: "idle", Interrupted: true},
	}
	close(evs)
	time.Sleep(50 * time.Millisecond)

	status := srv.GetStatus()
	if status.State != "active" {
		t.Errorf("state: got %q, want active", status.State)
	}
	srv.mu.RLock()
	processing := srv.processing
	srv.mu.RUnlock()
	if !processing {
		t.Error("processing: got false, want true")
	}
}

func TestBridge_IgnoresStaleEventsAfterSessionIdentityChanges(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	srv.SetAppIdentity("local", "new-session")
	srv.UpdateSessionInfo("new-session", "gpt-5", "openai")
	srv.SetState("idle")
	evs := make(chan events.SessionEvent, 10)
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

	evs <- events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "old-session",
		Data:      events.SessionEndData{Reason: "clear", State: "closed"},
	}
	close(evs)
	<-done

	status := srv.GetStatus()
	if status.SessionID != "new-session" || status.State != "idle" {
		t.Fatalf("status after stale event=%+v, want new-session idle", status)
	}
	if items := srv.AppNotificationsAfter(0, "new-session"); len(items) != 0 {
		t.Fatalf("stale event was projected under new session: %+v", items)
	}
}

func TestBridgeWithObserver_InvokesObserverAndForwardsEvents(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)
	observed := make(chan events.SessionEvent, 1)
	done := make(chan struct{})

	go func() {
		defer close(done)
		BridgeWithObserver(srv, evs, func(ev events.SessionEvent) {
			observed <- ev
		})
	}()

	want := events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "s1",
		Data:      events.AssistantTextDeltaData{Delta: "hello"},
	}
	evs <- want
	close(evs)
	<-done

	select {
	case got := <-observed:
		if got.Kind != want.Kind || got.SessionID != want.SessionID {
			t.Fatalf("observer saw %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("observer was not invoked")
	}
}

func TestBridge_RecordsAppWireNotifications(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	srv.SetAppIdentity("local", "th_1")
	evs := make(chan events.SessionEvent, 10)

	go Bridge(srv, evs)

	evs <- events.SessionEvent{
		Kind:      events.EventUserInput,
		SessionID: "th_1",
		Data:      events.UserInputData{Text: "hello"},
	}
	evs <- events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "th_1",
		Data:      events.AssistantTextDeltaData{Delta: "hi"},
	}
	close(evs)
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
