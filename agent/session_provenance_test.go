package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
)

func testProvenance(watchID, generation string) *provenance.Causal {
	return provenance.WithWatch(nil, watchID, generation, "wd_"+watchID, "session_parent", "caller")
}

func TestEmitAttachesActiveProvenance(t *testing.T) {
	s := &Session{id: "session_1", events: make(chan events.SessionEvent, 1)}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))

	s.emit(events.EventCommunicate, events.CommunicateData{Message: "ack", AwaitReply: false})

	ev := <-s.events
	if !provenance.ContainsWatch(ev.Provenance, "watch_A", "wg_1") {
		t.Fatalf("event provenance = %+v, want watch_A/wg_1", ev.Provenance)
	}
}

func TestActiveProvenanceResetsForExternalInput(t *testing.T) {
	s := &Session{}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))
	s.replaceActiveProvenance(nil)

	if provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatal("external top-level input must replace active provenance with empty")
	}
}

func TestDrainSteeringForTurnUnionsMessageProvenance(t *testing.T) {
	s := &Session{}
	s.steeringQueue = []steeringMessage{
		{Text: "from A", Provenance: testProvenance("watch_A", "wg_1")},
		{Text: "from B", Provenance: testProvenance("watch_B", "wg_1")},
	}

	got := s.drainSteeringForTurn()
	if len(got) != 2 {
		t.Fatalf("drained = %d, want 2", len(got))
	}
	active := s.activeCausalProvenance()
	if !provenance.ContainsWatch(active, "watch_A", "wg_1") || !provenance.ContainsWatch(active, "watch_B", "wg_1") {
		t.Fatalf("active provenance = %+v, want union of A and B", active)
	}
}

func TestPrependSteeringPreservesProvenanceForDeferredImages(t *testing.T) {
	s := &Session{}
	p := testProvenance("watch_A", "wg_1")
	s.prependSteering([]steeringMessage{{Text: "image reminder", Provenance: p}})

	got := s.drainSteeringForTurn()
	if len(got) != 1 {
		t.Fatalf("drained = %d, want 1", len(got))
	}
	if !provenance.ContainsWatch(got[0].Provenance, "watch_A", "wg_1") {
		t.Fatalf("deferred provenance = %+v, want watch_A/wg_1", got[0].Provenance)
	}
	if !provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("active provenance = %+v, want watch_A/wg_1", s.activeCausalProvenance())
	}
}

func TestCommunicateInboxDrainUnionsProvenance(t *testing.T) {
	s := &Session{id: "session_1", profile: NewOpenAIProfile("gpt-5.2"), events: make(chan events.SessionEvent, 4)}
	deps := newToolDeps(s)
	s.steeringQueue = []steeringMessage{{Text: "observer steering", Provenance: testProvenance("watch_A", "wg_1")}}

	drained := deps.drainSteering()
	if len(drained) != 1 || !strings.Contains(drained[0].Text, "observer steering") {
		t.Fatalf("drained = %+v, want observer steering", drained)
	}
	if !provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("communicate inbox drain did not union provenance: %+v", s.activeCausalProvenance())
	}
}

func TestAcceptNotificationInputAdoptsNotificationProvenance(t *testing.T) {
	s := newTestSession(t)
	s.events = make(chan events.SessionEvent, 4)
	// The notification turn rebuilds the delivered notification from the durable
	// record (filterDeliverableJobNotifications), so the adopted provenance is the
	// record's notification provenance. Persist a terminal record whose pending
	// event carries watch_A/wg_1 and enqueue the wake token for it.
	appendPendingJobNotificationRecordWithProvenance(t, s.jobManager, s.ID(), "job_A", testProvenance("watch_A", "wg_1"))
	s.enqueueJobNotification(jobNotification{
		JobID:      "job_A",
		JobType:    string(jobstore.JobDelegate),
		Status:     string(jobstore.StatusCompleted),
		Provenance: testProvenance("watch_A", "wg_1"),
	})

	if !s.acceptNotificationInput(context.Background()) {
		t.Fatal("notification input should proceed")
	}
	if !provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("active provenance = %+v, want notification provenance", s.activeCausalProvenance())
	}
	ev := <-s.events
	if ev.Kind != events.EventSteeringInjected {
		t.Fatalf("first event = %s, want STEERING_INJECTED", ev.Kind)
	}
	if !provenance.ContainsWatch(ev.Provenance, "watch_A", "wg_1") {
		t.Fatalf("steering event provenance = %+v, want watch_A/wg_1", ev.Provenance)
	}
}

func TestDrainAsSteerCreatesExternalSteering(t *testing.T) {
	s := &Session{state: SessionProcessing}
	if err := s.DrainAsSteerWithInput(context.Background(), "human queued text", nil); err != nil {
		t.Fatalf("DrainAsSteerWithInput: %v", err)
	}
	got := s.drainSteeringForTurn()
	if len(got) != 1 {
		t.Fatalf("drained = %d, want 1", len(got))
	}
	if got[0].Provenance != nil {
		t.Fatalf("human queue steering provenance = %+v, want nil", got[0].Provenance)
	}
}
