package server

import (
	"reflect"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// assertEnvelopeCarrierQuad pins every field the notes/urls carriers share the
// goal facet with, so a fix that opens one direction by dropping another field
// cannot pass: Goal, the notes pair, and the URL list are asserted together.
func assertEnvelopeCarrierQuad(t *testing.T, thread appwire.Thread, stage, wantGoal, wantHuman, wantAgent string, wantURLs []appwire.SessionURL) {
	t.Helper()
	if thread.Evener.Goal == nil {
		t.Fatalf("%s goal = nil, want objective %q", stage, wantGoal)
	}
	if got := thread.Evener.Goal.Objective; got != wantGoal {
		t.Fatalf("%s goal objective = %q, want %q", stage, got, wantGoal)
	}
	if thread.Evener.HumanNote != wantHuman || thread.Evener.AgentNote != wantAgent {
		t.Fatalf("%s notes = %q/%q, want %q/%q", stage, thread.Evener.HumanNote, thread.Evener.AgentNote, wantHuman, wantAgent)
	}
	if !reflect.DeepEqual(thread.Evener.SessionURLs, wantURLs) {
		t.Fatalf("%s urls = %+v, want %+v", stage, thread.Evener.SessionURLs, wantURLs)
	}
}

// TestServerAppWireNotesCarrierKeepsNewerSampledURLList pins the one-directional
// gap the formerly shared notesCarrierGeneration had: a notes carrier landing
// after a facetGoal sample was taken fences HumanNote/AgentNote only, never
// SessionURLs. Here the session's URL list is already newer than the envelope's
// when the sample is captured; pre-fix the notes commit bumped the one shared
// generation, so assign dropped the sampled newer URL list and thread/read kept
// reporting the older list until the next push or full sample. Post-fix the
// notes carrier keeps its own fields and the sampled URL list installs.
func TestServerAppWireNotesCarrierKeepsNewerSampledURLList(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{
		HumanNote:   "seed human",
		AgentNote:   "seed agent",
		Goal:        &schema.GoalSnapshot{Objective: "seed goal", Status: "active", Iterations: 1},
		SessionURLs: []schema.SessionURL{{ID: "u_old", URL: "https://old.test/", AddedBy: "agent", AddedAt: 1}},
	}})

	// The session moves its URL list (and goal) ahead of the envelope with no
	// URL carrier in flight: the parked sample below captures the newer values
	// while the envelope still holds the older URL list.
	src.meta.Goal = &schema.GoalSnapshot{Objective: "sampled goal", Status: "active", Iterations: 2}
	src.meta.SessionURLs = []schema.SessionURL{{ID: "u_new", URL: "https://new.test/", AddedBy: "agent", AddedAt: 2}}

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

	// A notes carrier lands after the sample was taken. It owns the note fields
	// and nothing else.
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: "root", Data: events.NotesUpdatedData{
		HumanNote: "carrier human", AgentNote: "carrier agent",
	}}, nil)
	notifications := srv.AppNotificationsAfter(0, "root")
	if len(notifications) == 0 || notifications[len(notifications)-1].Notification.Method != appwire.NotifyEvenerNotesUpdated {
		t.Fatalf("root notifications = %+v, want notes update", notifications)
	}

	// Before the parked sample installs: the carrier's notes are live, and the
	// sample's newer goal and URL list are still in flight.
	assertEnvelopeCarrierQuad(t, readThreadOverWire(t, srv, "local:root"), "notification cut",
		"seed goal", "carrier human", "carrier agent",
		[]appwire.SessionURL{{ID: "u_old", URL: "https://old.test/", AddedBy: "agent", AddedAt: 1}})

	close(release)
	<-checkpointDone
	// The notes carrier must not have fenced the URL list: the sampled newer
	// list installs, the carrier keeps the notes, and the sampled goal applies.
	assertEnvelopeCarrierQuad(t, readThreadOverWire(t, srv, "local:root"), "after checkpoint",
		"sampled goal", "carrier human", "carrier agent",
		[]appwire.SessionURL{{ID: "u_new", URL: "https://new.test/", AddedBy: "agent", AddedAt: 2}})
}

// TestServerAppWireURLsCarrierKeepsNewerSampledNotes is the mirror direction:
// a URLs carrier landing after a facetGoal sample was taken fences SessionURLs
// only, never HumanNote/AgentNote. Here the session's notes are newer than the
// envelope's when the sample is captured and the URL list stays behind for the
// carrier; pre-fix the URLs carrier bumped the one shared generation, so assign
// dropped the sampled newer notes and thread/read kept reporting the older
// ones. Post-fix the sampled notes install while the URLs carrier keeps its
// list.
func TestServerAppWireURLsCarrierKeepsNewerSampledNotes(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{
		HumanNote:   "seed human",
		AgentNote:   "seed agent",
		Goal:        &schema.GoalSnapshot{Objective: "seed goal", Status: "active", Iterations: 1},
		SessionURLs: []schema.SessionURL{{ID: "u_old", URL: "https://old.test/", AddedBy: "agent", AddedAt: 1}},
	}})

	// Mirror setup: the session's notes (and goal) move ahead of the envelope
	// with no notes carrier in flight; the URL list stays behind for the carrier.
	src.meta.Goal = &schema.GoalSnapshot{Objective: "sampled goal", Status: "active", Iterations: 2}
	src.meta.HumanNote = "sampled new human"
	src.meta.AgentNote = "sampled new agent"

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

	// A URLs carrier lands after the sample was taken. It owns the URL list and
	// nothing else.
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventUrlsUpdated, SessionID: "root", Data: events.UrlsUpdatedData{
		URLs: []events.SessionURLData{{ID: "u_carrier", URL: "https://carrier.test/", AddedBy: "agent", AddedAt: 3}},
	}}, nil)
	notifications := srv.AppNotificationsAfter(0, "root")
	if len(notifications) == 0 || notifications[len(notifications)-1].Notification.Method != appwire.NotifyEvenerUrlsUpdated {
		t.Fatalf("root notifications = %+v, want urls update", notifications)
	}

	// Before the parked sample installs: the carrier's URL list is live, and the
	// sample's newer goal and notes are still in flight.
	assertEnvelopeCarrierQuad(t, readThreadOverWire(t, srv, "local:root"), "notification cut",
		"seed goal", "seed human", "seed agent",
		[]appwire.SessionURL{{ID: "u_carrier", URL: "https://carrier.test/", AddedBy: "agent", AddedAt: 3}})

	close(release)
	<-checkpointDone
	// The URLs carrier must not have fenced the notes pair: the sampled newer
	// notes install, the carrier keeps the URL list, and the sampled goal applies.
	assertEnvelopeCarrierQuad(t, readThreadOverWire(t, srv, "local:root"), "after checkpoint",
		"sampled goal", "sampled new human", "sampled new agent",
		[]appwire.SessionURL{{ID: "u_carrier", URL: "https://carrier.test/", AddedBy: "agent", AddedAt: 3}})
}

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
