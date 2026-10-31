package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func testProvenance(watchID, generation string) *provenance.Causal {
	return provenance.WithWatch(nil, watchID, generation, "wd_"+watchID, "session_parent", "caller")
}

func TestEmitAttachesActiveProvenance(t *testing.T) {
	t.Parallel()
	s := &Session{id: "session_1", events: make(chan events.SessionEvent, 1)}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))

	s.emit(events.EventCommunicate, events.CommunicateData{Message: "ack", EndTurn: false})

	ev := <-s.events
	if !provenance.ContainsWatch(ev.Provenance, "watch_A", "wg_1") {
		t.Fatalf("event provenance = %+v, want watch_A/wg_1", ev.Provenance)
	}
}

func TestActiveProvenanceResetsForExternalInput(t *testing.T) {
	t.Parallel()
	s := &Session{}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))
	s.replaceActiveProvenance(nil)

	if provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatal("external top-level input must replace active provenance with empty")
	}
}

func TestFinishProcessingAtBoundaryClearsActiveProvenance(t *testing.T) {
	t.Parallel()
	s := &Session{state: SessionProcessing}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))

	s.finishProcessingAtBoundary(context.Background(), SessionIdle)

	if provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatal("processing boundary must clear active provenance")
	}
}

func TestFinishProcessingAtBoundaryCapturesCompletedProvenance(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	s := newProvenanceErrorSession(t, errors.New("llm failed"))

	_, err := s.processInputWithProvenance(context.Background(), "trigger failure", nil, testProvenance("watch_A", "wg_1"))
	if err == nil {
		t.Fatal("processInputWithProvenance succeeded, want LLM error")
	}
	if provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("active provenance after failed turn = %+v, want cleared", s.activeCausalProvenance())
	}
}

func TestNonRetryableModelErrorClearsActiveProvenanceAtIdleBoundary(t *testing.T) {
	t.Parallel()
	s := newProvenanceErrorSession(t, llm.ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil))

	_, err := s.processInputWithProvenance(context.Background(), "trigger auth failure", nil, testProvenance("watch_A", "wg_1"))
	if err == nil {
		t.Fatal("processInputWithProvenance succeeded, want provider error")
	}
	if provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("active provenance after failed turn = %+v, want cleared", s.activeCausalProvenance())
	}
	if !provenance.ContainsWatch(s.completedCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("completed provenance after failed turn = %+v, want watch_A/wg_1", s.completedCausalProvenance())
	}
	if got := s.State(); got != SessionIdle {
		t.Fatalf("state after non-retryable provider error = %q, want %q", got, SessionIdle)
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
	t.Parallel()
	s := &Session{state: SessionProcessing}
	s.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))
	s.finishProcessingAtBoundary(context.Background(), SessionIdle)

	s.replaceActiveProvenance(testProvenance("watch_B", "wg_1"))

	if provenance.ContainsWatch(s.completedCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("completed provenance survived new input: %+v", s.completedCausalProvenance())
	}
}

// TestAcceptSteeringCarrierInputClearsCompletedProvenance pins issue #185 item
// 2: the steering carrier is the only accept*Input that did not reset
// provenance at its start. Without the reset the previous turn's completed
// provenance survives into the carrier turn, where subagent.followUpProvenance
// (which unions completedCausalProvenance) can read the stale lineage.
func TestAcceptSteeringCarrierInputClearsCompletedProvenance(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	defer sess.Close()

	// A previous turn finished with completed provenance watch_A/wg_1.
	sess.replaceActiveProvenance(testProvenance("watch_A", "wg_1"))
	sess.finishActiveProvenance()
	if !provenance.ContainsWatch(sess.completedCausalProvenance(), "watch_A", "wg_1") {
		t.Fatal("test setup: completed provenance not seeded")
	}

	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "carrier-prov-1",
		Input:            clientMutationInput("focus on the tests", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	turnID, ok := sess.claimSteeringCarrierTurn()
	if !ok {
		t.Fatalf("claimSteeringCarrierTurn refused a queued steer")
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sess.acceptSteeringCarrierInput(ctx, queuedClientMutationIdentity{ClientMutationID: "carrier-prov-1", StableTurnID: turnID, SteeringCarrier: true}); err != nil {
		t.Fatalf("acceptSteeringCarrierInput: %v", err)
	}
	if provenance.ContainsWatch(sess.completedCausalProvenance(), "watch_A", "wg_1") {
		t.Fatalf("completed provenance survived carrier start: %+v", sess.completedCausalProvenance())
	}
}

func TestDrainSteeringForTurnUnionsMessageProvenance(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	a := &skillContinuationAdapter{}
	s := observationSession(t, a, false)
	evs, mu, done := collectEvents(s)
	// Terminal source migration preserves lineage in canonical custody. Request
	// selection, not the earlier turn boundary, adopts that lineage for output.
	appendPendingJobNotificationRecordWithProvenance(t, s.jobManager, s.ID(), "job_A", testProvenance("watch_A", "wg_1"))
	s.enqueueJobNotification(jobNotification{
		JobID:      "job_A",
		JobType:    string(jobstore.JobShell),
		Status:     string(jobstore.StatusCompleted),
		Provenance: testProvenance("watch_A", "wg_1"),
	})
	s.replaceActiveProvenance(testProvenance("watch_stale", "wg_old"))
	var id string
	a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
		observationRequest(t, req, []string{"job_A"}, nil)
		if !provenance.ContainsWatch(s.activeCausalProvenance(), "watch_A", "wg_1") || provenance.ContainsWatch(s.activeCausalProvenance(), "watch_stale", "wg_old") {
			t.Errorf("request-selected provenance=%+v", s.activeCausalProvenance())
		}
		ids, err := s.pendingNotificationIDs()
		if err != nil || len(ids) != 1 {
			t.Fatalf("accepted pending=%v error=%v", ids, err)
		}
		id = ids[0]
		for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
			if entry.Turn.AttentionID == id && !provenance.ContainsWatch(entry.Turn.NotificationProvenance, "watch_A", "wg_1") {
				t.Error("canonical custody lost source lineage")
			}
			if len(entry.Turn.ObservedNotificationIDs) != 0 {
				t.Error("acceptance granted observation before a response")
			}
		}
		return toolCallResponse(communicateCallArgs("causal-output", map[string]any{"message": "provenance-output-5", "end_turn": true})), nil
	}}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	assertNotificationObservedOnce(t, s.TranscriptPath(), id)
	assertNotificationPending(t, s, []string{})
	if len(a.Requests()) != 1 || producerRecordCount(t, s) != 1 {
		t.Fatal("provenance delivery duplicated its record or provider request")
	}
	s.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	counts := map[events.EventKind]int{}
	for _, ev := range *evs {
		if ev.Kind == events.EventToolCallStart || ev.Kind == events.EventCommunicate {
			counts[ev.Kind]++
			if !provenance.ContainsWatch(ev.Provenance, "watch_A", "wg_1") || provenance.ContainsWatch(ev.Provenance, "watch_stale", "wg_old") {
				t.Errorf("model output %s provenance=%+v", ev.Kind, ev.Provenance)
			}
		}
	}
	if counts[events.EventToolCallStart] != 1 || counts[events.EventCommunicate] != 1 {
		t.Fatalf("real output event counts=%v, want one tool call and one communicate", counts)
	}
}

func TestAcceptNotificationInputAdoptsCallerWatchSendStateProvenance(t *testing.T) {
	t.Parallel()
	a := &skillContinuationAdapter{}
	s := observationSession(t, a, false)
	s.jobManager.retirementOwner, s.jobManager.delegateController = nil, nil
	cfg, key, _ := installCallerSendWatchWithCurrentFrame(t, s.jobManager, "frame-v2")
	s.jobManager.retirementOwner, s.jobManager.delegateController = s, s.delegateController
	cfg.pending[key].ReceiverSessionID = s.ID()
	beforeSeq := cfg.nextUpdateSeq
	s.jobManager.dispatchProducedWatchSends()
	a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
		observationRequest(t, req, []string{"frame-v2"}, nil)
		if !provenance.ContainsWatch(s.activeCausalProvenance(), cfg.watchID, cfg.generation) {
			t.Fatal("provider call lost caller watch provenance")
		}
		return llm.Response{Message: llm.Assistant("observed")}, nil
	}}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	s.emit(events.EventCommunicate, events.CommunicateData{Message: "watch-send acknowledgement", EndTurn: false})
	if cfg.nextUpdateSeq != beforeSeq+1 {
		t.Fatalf("echo sequence = %d, want %d", cfg.nextUpdateSeq, beforeSeq+1)
	}
}

func TestAcceptNotificationInputAdoptsRestoredCallerWatchSendProvenance(t *testing.T) {
	t.Parallel()
	a := &skillContinuationAdapter{}
	s := observationSession(t, a, false)
	state := jobstore.WatchSendState{
		Key:        jobstore.WatchSendKey{VisibleSessionID: s.ID(), WatchID: "watch_restore", WatchTarget: "job_restore", ResolvedWatchedIdentity: "job_restore", ResolvedSendTo: runtimeMessageAliasCaller, WatchGeneration: "wg_restore"},
		DeliveryID: "delivery_restore", UpdateSeq: 1, Frame: "restored frame", ReceiverSessionID: s.ID(), Provenance: testProvenance("watch_restore", "wg_restore"),
	}
	if err := s.jobManager.appendWatchSendEvents([]jobstore.Event{{Kind: jobstore.EventWatchSendPending, TS: time.Now(), WatchSend: &state}}); err != nil {
		t.Fatal(err)
	}
	if err := s.jobManager.store.Close(); err != nil {
		t.Fatal(err)
	}
	jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
	if err != nil {
		t.Fatal(err)
	}
	s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, s.delegateController
	if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
		observationRequest(t, req, []string{"restored frame"}, nil)
		if !provenance.ContainsWatch(s.activeCausalProvenance(), "watch_restore", "wg_restore") {
			t.Fatal("provider call lost restored caller provenance")
		}
		return llm.Response{Message: llm.Assistant("observed")}, nil
	}}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
}

func TestDrainAsSteerCreatesExternalSteering(t *testing.T) {
	t.Parallel()
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
