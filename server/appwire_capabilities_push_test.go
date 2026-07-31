package server

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/events"
)

// A client holds ThreadCapabilities as a snapshot from its last thread/read,
// and three of them — Send, Steer, Queue — are defined by whether a turn is in
// flight. So a client that read the thread while it was idle holds
// steer=false/queue=false, and nothing on the wire ever corrects that: the
// composer then renders a session it KNOWS is active (it has the status change
// and the turn) with no Steer, no Stop, and a dead Send, until a reload
// (kata 06t8). A status transition is exactly the moment those flip, so the
// set rides along there — the same fix shape thread/status/changed already
// carries for the running failure count (kata 12rq).
func TestStatusChangeCarriesTheCapabilitiesForThatStatus(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetSteerFunc(func(string) {})
	srv.SetQueueFunc(func(string) error { return nil })
	srv.SetCancelFunc(context.CancelFunc(func() {}))

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})

	statuses := statusNotifications(t, srv, "th_1")
	if len(statuses) != 1 {
		t.Fatalf("thread/status/changed count = %d, want 1 (the turn going active)", len(statuses))
	}
	active := statuses[0]
	if active.Status.Type != "active" {
		t.Fatalf("status = %q, want active", active.Status.Type)
	}
	if active.Capabilities == nil {
		t.Fatal("active thread/status/changed carried no capabilities, want the set that goes with an active turn")
	}
	if !active.Capabilities.Steer || !active.Capabilities.Queue || !active.Capabilities.Interrupt {
		t.Fatalf("active capabilities = %+v, want steer/queue/interrupt all true", *active.Capabilities)
	}
	if active.Capabilities.Send {
		t.Fatalf("active capabilities Send = true, want false while a turn is in flight")
	}

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})

	statuses = statusNotifications(t, srv, "th_1")
	idle := statuses[len(statuses)-1]
	if idle.Status.Type != "idle" {
		t.Fatalf("last status = %q, want idle", idle.Status.Type)
	}
	if idle.Capabilities == nil {
		t.Fatal("idle thread/status/changed carried no capabilities, want the set that goes with no turn")
	}
	if idle.Capabilities.Steer || idle.Capabilities.Queue {
		t.Fatalf("idle capabilities = %+v, want steer/queue false with no turn in flight", *idle.Capabilities)
	}
	if !idle.Capabilities.Send {
		t.Fatal("idle capabilities Send = false, want true with no turn in flight")
	}
}

// A daemon announcing its own close is describing a thread it is about to
// stop running, and the actions still on offer for that thread are the HUB's
// to state: it answers an exited session's read from the past index and
// resumes it on the next send (cmd/serf-hub/app_threadread.go's
// pastThreadCapabilities advertises Send: true for exactly that). A daemon's
// own "send: false" would therefore take the follow-up composer away from a
// session the hub would happily wake — the same wedge in the other direction.
// So the close frame leaves the field empty; the hub stamps its own answer
// onto it as it relays the frame (app_relay.go's
// stampClosedThreadCapabilities), which is what keeps a client from reading
// back a set the departing daemon cut for a turn that is over (kata pk2d).
func TestStatusChangeOmitsCapabilitiesWhenTheDaemonCloses(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	srv.SetSteerFunc(func(string) {})
	srv.SetQueueFunc(func(string) error { return nil })

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{Reason: "shutdown", State: "closed"}})

	statuses := statusNotifications(t, srv, "th_1")
	closed := statuses[len(statuses)-1]
	if closed.Status.Type != "closed" {
		t.Fatalf("last status = %q, want closed", closed.Status.Type)
	}
	if closed.Capabilities != nil {
		t.Fatalf("closed thread/status/changed carried %+v, want no capability update", *closed.Capabilities)
	}
}

// The set describes the status being announced, not whatever the harness
// happens to have wired: a daemon with no steer callback stays honest about
// it in the active frame too.
func TestStatusChangeCapabilitiesRespectAnUnwiredHarness(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "go"}})

	statuses := statusNotifications(t, srv, "th_1")
	if len(statuses) == 0 {
		t.Fatal("no thread/status/changed notifications recorded")
	}
	active := statuses[len(statuses)-1]
	if active.Capabilities == nil {
		t.Fatal("thread/status/changed carried no capabilities")
	}
	if active.Capabilities.Steer || active.Capabilities.Queue || active.Capabilities.Interrupt {
		t.Fatalf("capabilities = %+v, want steer/queue/interrupt false with nothing wired", *active.Capabilities)
	}
}
