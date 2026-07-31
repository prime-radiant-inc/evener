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
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

	evs <- events.SessionEvent{
		Kind:      events.EventAssistantTextDelta,
		SessionID: "s1",
		Data:      events.AssistantTextDeltaData{Delta: "hello"},
	}
	close(evs)
	<-done

	items := srv.AppNotificationsAfter(0, "s1")
	if len(items) == 0 {
		t.Fatal("expected at least one appwire notification")
	}
	// The delta arrives last: it opens the agent-message item it appends to.
	last := items[len(items)-1]
	if last.Notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("notification method: got %q, want %q", last.Notification.Method, appwire.NotifyAgentMessageDelta)
	}
}

func TestBridge_UpdatesStatusOnSessionStart(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

	evs <- events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "s1",
		Data: events.SessionStartData{
			Profile: "openai",
			Model:   "gpt-5",
		},
	}
	close(evs)
	<-done

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

func TestBridge_UsesSessionStartStateWhenProvided(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

	evs <- events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "s1",
		Data: events.SessionStartData{
			Profile:  "openai",
			Model:    "gpt-5",
			Restored: true,
			State:    "awaiting",
		},
	}
	close(evs)
	<-done

	status := srv.GetStatus()
	if status.State != "awaiting" {
		t.Errorf("state: got %q, want awaiting", status.State)
	}
}

// TestBridgeUpdatesSessionMetadataBeforeProjectionCommit pins the order the
// bridge announces an identity in: the status a SessionStart carries -- session
// id, model, profile and state -- must already be true by the time the
// projection commit publishes the notification that announces it. Otherwise a
// client that reduces thread/started and then reads thread/read can observe the
// two disagreeing, and on /clear the disagreement spans a whole session swap.
//
// beforeAppProjectionCommit runs on the bridge's own goroutine immediately
// before CommitProjection, so it samples exactly the window the notification is
// published in.
func TestBridgeUpdatesSessionMetadataBeforeProjectionCommit(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	var atCommit StatusInfo
	sampled := false
	srv.mu.Lock()
	srv.beforeAppProjectionCommit = func() {
		if sampled {
			return
		}
		sampled = true
		atCommit = srv.GetStatus()
	}
	srv.mu.Unlock()

	evs := make(chan events.SessionEvent, 1)
	evs <- events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "s1",
		Data: events.SessionStartData{
			Profile: "openai",
			Model:   "gpt-5",
			State:   "awaiting",
		},
	}
	close(evs)
	Bridge(srv, evs)

	if !sampled {
		t.Fatal("projection commit never ran for the session start")
	}
	if atCommit.SessionID != "s1" || atCommit.Model != "gpt-5" || atCommit.Profile != "openai" || atCommit.State != "awaiting" {
		t.Fatalf(
			"status at projection commit = (session %q, model %q, profile %q, state %q); want (s1, gpt-5, openai, awaiting)",
			atCommit.SessionID, atCommit.Model, atCommit.Profile, atCommit.State,
		)
	}
}

// TestBridgeDropsStatusOfAnEventAdmittedBeforeIdentityReplacement covers the
// interleaving /clear opens and nothing else closes.
//
// The bridge tests acceptance without a lock and then applies the event's
// status. An identity replacement landing between the two would let a
// straggling SESSION_START from the session /clear just retired write that
// retired session's id, model, profile and state over the replacement's --
// permanently, since no later event re-derives them.
//
// The two calls here are literally the bridge's two steps, in order, with the
// replacement placed between them. No goroutines, so the interleaving under
// test is the one that runs, every time.
func TestBridgeDropsStatusOfAnEventAdmittedBeforeIdentityReplacement(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	srv.SetAppIdentity("local", "retired")
	retiredStart := events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "retired",
		Data: events.SessionStartData{
			Profile: "retired-profile",
			Model:   "retired-model",
			State:   "awaiting",
		},
	}
	if !srv.acceptsSessionEvent(retiredStart.SessionID) {
		t.Fatal("the bridge would not have admitted the event this test is about")
	}

	srv.SetAppIdentity("local", "replacement")
	srv.UpdateSessionInfo("replacement", "replacement-model", "replacement-profile")
	srv.SetState("idle")

	srv.applySessionEventStatus(retiredStart)

	status := srv.GetStatus()
	if status.SessionID != "replacement" || status.Model != "replacement-model" ||
		status.Profile != "replacement-profile" || status.State != "idle" {
		t.Fatalf(
			"a retired session's admitted event rewrote the replacement's status: (session %q, model %q, profile %q, state %q)",
			status.SessionID, status.Model, status.Profile, status.State,
		)
	}
}

func TestBridge_IncrementsturnsOnAssistantTextEnd(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

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
	<-done

	status := srv.GetStatus()
	if status.Turns != 2 {
		t.Errorf("turns: got %d, want 2", status.Turns)
	}
}

func TestBridge_ClosesOnSessionEnd(t *testing.T) {
	srv := NewServer(ServerConfig{AppReplaySize: 100})
	evs := make(chan events.SessionEvent, 10)
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

	evs <- events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "s1",
		Data:      events.SessionEndData{Reason: "done"},
	}
	close(evs)
	<-done

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
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

	evs <- events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "s1",
		Data:      events.SessionEndData{Reason: "input_complete", State: "idle"},
	}
	close(evs)
	<-done

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
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

	evs <- events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "s1",
		Data:      events.SessionEndData{Reason: "interrupted", State: "idle", Interrupted: true},
	}
	close(evs)
	<-done

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
	done := make(chan struct{})

	go func() {
		defer close(done)
		Bridge(srv, evs)
	}()

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
	<-done

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
