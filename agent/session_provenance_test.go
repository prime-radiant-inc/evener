package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/llm"
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

func TestFinishProcessingAtBoundaryClearsActiveProvenance(t *testing.T) {
	s := &Session{state: SessionProcessing}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))

	s.finishProcessingAtBoundary(context.Background(), SessionIdle)

	if provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatal("processing boundary must clear active provenance")
	}
}

func TestFinishProcessingAtBoundaryCapturesCompletedProvenance(t *testing.T) {
	s := &Session{state: SessionProcessing}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))
	s.unionActiveProvenance(testProvenance("watch_B", "wg_1"))

	s.finishProcessingAtBoundary(context.Background(), SessionIdle)

	got := s.completedCausalProvenance()
	if !provenance.ContainsWatch(got, "watch_A", "wg_1") || !provenance.ContainsWatch(got, "watch_B", "wg_1") {
		t.Fatalf("completed provenance = %+v, want watch_A and watch_B", got)
	}
}

func TestProcessInputErrorClearsActiveProvenance(t *testing.T) {
	s := newProvenanceErrorSession(t, errors.New("llm failed"))

	_, err := s.processInputWithProvenance(context.Background(), "trigger failure", nil, testProvenance("watch_A", "wg_1"))
	if err == nil {
		t.Fatal("processInputWithProvenance succeeded, want LLM error")
	}
	if provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("active provenance after failed turn = %+v, want cleared", s.activeCausalProvenance())
	}
}

func TestNonRetryableModelErrorClearsActiveProvenanceBeforeClose(t *testing.T) {
	s := newProvenanceErrorSession(t, llm.ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil))

	_, err := s.processInputWithProvenance(context.Background(), "trigger auth failure", nil, testProvenance("watch_A", "wg_1"))
	if err == nil {
		t.Fatal("processInputWithProvenance succeeded, want provider error")
	}
	if provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("active provenance after closed failed turn = %+v, want cleared", s.activeCausalProvenance())
	}
	if !provenance.ContainsWatch(s.completedCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("completed provenance after closed failed turn = %+v, want watch_A/wg_1", s.completedCausalProvenance())
	}
}

func newProvenanceErrorSession(t *testing.T, modelErr error) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, modelErr
			},
		},
	})
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestReplaceActiveProvenanceClearsCompletedProvenance(t *testing.T) {
	s := &Session{state: SessionProcessing}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))
	s.finishProcessingAtBoundary(context.Background(), SessionIdle)

	s.replaceActiveProvenance(testProvenance("watch_B", "wg_1"))

	if provenance.ContainsWatch(s.completedCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("completed provenance survived new input: %+v", s.completedCausalProvenance())
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

func TestAcceptNotificationInputAdoptsCallerWatchSendStateProvenance(t *testing.T) {
	s := newTestSession(t)
	s.events = make(chan events.SessionEvent, 8)
	cfg, _, _ := installCallerSendWatchWithCurrentFrame(t, s.jobManager, "frame-v2")
	beforeSeq := cfg.nextUpdateSeq

	if !s.acceptNotificationInput(context.Background()) {
		t.Fatal("notification input should proceed")
	}
	if !provenance.ContainsWatch(s.activeCausalProvenance(), cfg.watchID, cfg.generation) {
		t.Fatalf("active provenance = %+v, want caller watch-send state provenance", s.activeCausalProvenance())
	}
	ev := <-s.events
	if ev.Kind != events.EventSteeringInjected {
		t.Fatalf("first event = %s, want STEERING_INJECTED", ev.Kind)
	}
	if !provenance.ContainsWatch(ev.Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("steering event provenance = %+v, want %s/%s", ev.Provenance, cfg.watchID, cfg.generation)
	}

	s.emit(events.EventAssistantTextEnd, events.AssistantTextEndData{Text: "watch-send acknowledgement"})
	if cfg.nextUpdateSeq != beforeSeq {
		t.Fatalf("nextUpdateSeq = %d after acknowledgement, want %d (same-watch echo suppressed)", cfg.nextUpdateSeq, beforeSeq)
	}
}

func TestAcceptNotificationInputAdoptsRestoredCallerWatchSendProvenance(t *testing.T) {
	s := newTestSession(t)
	s.events = make(chan events.SessionEvent, 8)
	p := testProvenance("watch_restore", "wg_restore")
	key := jobstore.WatchSendKey{
		VisibleSessionID:        s.ID(),
		WatchID:                 "watch_restore",
		WatchTarget:             "job_restore",
		ResolvedWatchedIdentity: "job_restore",
		ResolvedSendTo:          runtimeMessageAliasCaller,
		WatchGeneration:         "wg_restore",
	}
	cfg := &watchConfig{
		id:         "watch_restore",
		watchID:    "watch_restore",
		target:     "job_restore",
		send:       &watchSendArgs{To: runtimeMessageAliasCaller},
		generation: "wg_restore",
		pending: map[jobstore.WatchSendKey]*jobstore.WatchSendState{
			key: {
				Key:           key,
				DeliveryID:    "delivery_restore",
				UpdateSeq:     1,
				Frame:         "restored frame",
				TriggerReason: "output_match: ready",
				Provenance:    p,
			},
		},
		pendingOrder: []jobstore.WatchSendKey{key},
	}
	s.jobManager.terminalFlush = map[*watchConfig]bool{cfg: true}

	if !s.acceptNotificationInput(context.Background()) {
		t.Fatal("notification input should proceed for restored caller watch send")
	}
	if !provenance.ContainsWatch(s.activeCausalProvenance(), "watch_restore", "wg_restore") {
		t.Fatalf("active provenance = %+v, want restored watch-send state provenance", s.activeCausalProvenance())
	}
	ev := <-s.events
	if ev.Kind != events.EventSteeringInjected {
		t.Fatalf("first event = %s, want STEERING_INJECTED", ev.Kind)
	}
	if !provenance.ContainsWatch(ev.Provenance, "watch_restore", "wg_restore") {
		t.Fatalf("steering event provenance = %+v, want restored watch provenance", ev.Provenance)
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
