package server

import (
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// TestServerAppWireCheckpointCannotOverwriteConcurrentNotesCarrier mirrors
// TestServerAppWireCheckpointCannotOverwriteConcurrentGoalCarrier for the
// shared-notes carriers: a sampled facetGoal checkpoint taken before a
// notes/urls carrier commits must not overwrite the carrier values on assign.
// The sampled Goal still applies (independent carriers); only the stale notes
// fields are dropped.
func TestServerAppWireCheckpointCannotOverwriteConcurrentNotesCarrier(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{
		HumanNote: "old sampled human",
		AgentNote: "old sampled agent",
		Goal:      &schema.GoalSnapshot{Objective: "old sampled goal", Status: "active", Iterations: 1},
	}})

	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	src.parkAfterMeta = func() {
		once.Do(func() {
			close(parked)
			<-release
		})
	}
	checkpointDone := make(chan struct{})
	go func() {
		defer close(checkpointDone)
		BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root"}, nil)
	}()
	<-parked

	BridgeEvent(srv, events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: "root", Data: events.NotesUpdatedData{
		HumanNote: "new carrier human", AgentNote: "new carrier agent",
	}}, nil)
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventUrlsUpdated, SessionID: "root", Data: events.UrlsUpdatedData{
		URLs: []events.SessionURLData{{ID: "u1", URL: "https://x.test/y", Label: "x", AddedBy: "agent", AddedAt: 7}},
	}}, nil)
	notifications := srv.AppNotificationsAfter(0, "root")
	if len(notifications) == 0 || notifications[len(notifications)-1].Notification.Method != appwire.NotifyEvenerUrlsUpdated {
		t.Fatalf("root notifications = %+v, want urls update", notifications)
	}
	assertNotes := func(stage string) {
		t.Helper()
		thread := readThreadOverWire(t, srv, "local:root")
		if thread.Evener.HumanNote != "new carrier human" || thread.Evener.AgentNote != "new carrier agent" {
			t.Fatalf("%s notes = %q/%q, want direct carrier", stage, thread.Evener.HumanNote, thread.Evener.AgentNote)
		}
		if len(thread.Evener.SessionURLs) != 1 || thread.Evener.SessionURLs[0].ID != "u1" {
			t.Fatalf("%s urls = %+v, want direct carrier", stage, thread.Evener.SessionURLs)
		}
		if thread.Evener.Goal == nil || thread.Evener.Goal.Objective != "old sampled goal" {
			t.Fatalf("%s goal = %+v, want the checkpoint sample (independent carrier)", stage, thread.Evener.Goal)
		}
	}
	assertNotes("notification cut")
	close(release)
	<-checkpointDone
	assertNotes("after checkpoint")
}

// TestServerAppWireCheckpointStillRepairsNotesWithoutConcurrentCarrier is the
// notes half of TestServerAppWireCheckpointStillRepairsTaskAndGoalWithoutConcurrentCarrier:
// with no overlapping carrier, a checkpoint sample still repairs stale notes.
func TestServerAppWireCheckpointStillRepairsNotesWithoutConcurrentCarrier(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: "root", Data: events.NotesUpdatedData{
		HumanNote: "stale carrier human", AgentNote: "stale carrier agent",
	}})

	src.meta.HumanNote = "checkpoint repaired human"
	src.meta.AgentNote = "checkpoint repaired agent"
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root"}, nil)

	thread := readThreadOverWire(t, srv, "local:root")
	if thread.Evener.HumanNote != "checkpoint repaired human" || thread.Evener.AgentNote != "checkpoint repaired agent" {
		t.Fatalf("repaired notes = %q/%q", thread.Evener.HumanNote, thread.Evener.AgentNote)
	}
}
