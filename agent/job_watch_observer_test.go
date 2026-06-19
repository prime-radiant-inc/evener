package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// stateDirForJM derives the project state dir from a jobManager's per-session
// jobs dir (<stateDir>/sessions/<id>), the location of session .meta.json files.
func stateDirForJM(jm *jobManager) string {
	return filepath.Dir(filepath.Dir(jm.dir))
}

// TestMintWatchCreateReadGrantStampsObservedBy proves that installing a sidecar
// watch (concrete worker target, delivered to a concrete observer delegate)
// stamps the observer's session id onto the watched WORKER's SessionMeta, so the
// hub can later auto-open the observer beside the worker.
func TestMintWatchCreateReadGrantStampsObservedBy(t *testing.T) {
	jm := newTestJM(t)
	stateDir := stateDirForJM(jm)

	// The watched worker is a running delegate whose transcript ref resolves to
	// its child session id; the observer is a delegate send target.
	const workerSessionID = "WORKER"
	var signals atomic.Int32
	workerJobID := seedRunningDelegate(t, jm, encodeRef("", workerSessionID), &signals)
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
	observerSessionID := "child_job_obs"

	// The worker's meta must exist for the stamp to land on it (the worker is a
	// real child session with its own meta on disk).
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: workerSessionID, IsSubagent: true}); err != nil {
		t.Fatalf("seed worker meta: %v", err)
	}

	if _, err := jm.configureWatch(watchArgs{
		Target:      workerJobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configureWatch: %v", err)
	}

	got, err := schema.LoadSessionMeta(stateDir, workerSessionID)
	if err != nil {
		t.Fatalf("load worker meta: %v", err)
	}
	if len(got.ObservedBy) != 1 || got.ObservedBy[0] != observerSessionID {
		t.Fatalf("worker meta ObservedBy = %v, want [%s]", got.ObservedBy, observerSessionID)
	}
}

// TestMintWatchCreateReadGrantObservedByDedups proves repeated watch installs of
// the same (worker, observer) pair do not duplicate the observer id on the
// worker's meta — the set is append-only and deduped, mirroring the grant log.
func TestMintWatchCreateReadGrantObservedByDedups(t *testing.T) {
	jm := newTestJM(t)
	stateDir := stateDirForJM(jm)
	var signals atomic.Int32
	workerJobID := seedRunningDelegate(t, jm, encodeRef("", "WORKER"), &signals)
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: "WORKER", IsSubagent: true}); err != nil {
		t.Fatalf("seed worker meta: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := jm.configureWatch(watchArgs{
			Target:      workerJobID,
			OutputMatch: "(?i)ready",
			Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
		}); err != nil {
			t.Fatalf("configureWatch #%d: %v", i, err)
		}
	}

	got, err := schema.LoadSessionMeta(stateDir, "WORKER")
	if err != nil {
		t.Fatalf("load worker meta: %v", err)
	}
	if len(got.ObservedBy) != 1 {
		t.Fatalf("ObservedBy must dedup; got %v", got.ObservedBy)
	}
}

// TestOrdinaryWorkerHasNoObservedBy proves a delegate worker that is not the
// target of any watch never gains an ObservedBy entry — the stamp is confined to
// the watch-install seam.
func TestOrdinaryWorkerHasNoObservedBy(t *testing.T) {
	jm := newTestJM(t)
	stateDir := stateDirForJM(jm)
	var signals atomic.Int32
	_ = seedRunningDelegate(t, jm, encodeRef("", "WORKER"), &signals)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: "WORKER", IsSubagent: true}); err != nil {
		t.Fatalf("seed worker meta: %v", err)
	}

	got, err := schema.LoadSessionMeta(stateDir, "WORKER")
	if err != nil {
		t.Fatalf("load worker meta: %v", err)
	}
	if len(got.ObservedBy) != 0 {
		t.Fatalf("un-watched worker meta ObservedBy = %v, want empty", got.ObservedBy)
	}
}

func TestWatchSendBuildsObserverFrame(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	captured := captureWatchSends(t, s.jobManager)
	watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "watch",
		Name: "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(
			`{"operation":"create","target":%q,"output_match":"(?i)ready","send":{"to":"dlg_obs","message":"observe"}}`,
			shellOut.JobID,
		)),
	})
	if watchRes.IsError {
		t.Fatalf("job_watch returned error: %s", watchRes.Output)
	}

	feedJob(s.jobManager, shellOut.JobID, []byte("server READY\n"))
	sends := captured()
	if len(sends) != 1 {
		t.Fatalf("expected one watch send, got %#v", sends)
	}
	if sends[0].Target != "dlg_obs" {
		t.Fatalf("watch send target = %q, want dlg_obs", sends[0].Target)
	}
	if !sends[0].FromWatch || !sends[0].Background || !sends[0].BackgroundSet || sends[0].OnIdle != "start" {
		t.Fatalf("watch send args = %+v, want background watch delivery", sends[0])
	}
	if !strings.Contains(sends[0].Message, "observe") || !strings.Contains(sends[0].Message, "server READY") {
		t.Fatalf("watch send message = %q, want configured message and trigger context", sends[0].Message)
	}
}

func TestJobWatchSendsToObserverDelegateIDAcrossResume(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateWithDefaultOutput("observer ready") },
		func(llm.Request) llm.Response { return communicateWithDefaultOutput("observer saw ready") },
	}})
	s := newDelegateTestSession(t, c)
	observer := s.createDelegate(context.Background(), delegateArgs{Task: "observe", Background: false, BlockTimeoutMS: 5000})
	if observer.Err != nil {
		t.Fatalf("observer delegate: %v", observer.Err)
	}

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "watch",
		Name: "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(
			`{"operation":"create","target":%q,"output_match":"READY","send":{"to":%q,"message":"observe","include_excerpt":true}}`,
			shellOut.JobID,
			observer.DelegateID,
		)),
	})
	if watchRes.IsError {
		t.Fatalf("job_watch returned error: %s", watchRes.Output)
	}

	feedJob(s.jobManager, shellOut.JobID, []byte("server READY\n"))
	if err := drainWatchSendsVia(t, s.jobManager, s.sendDelegateMessage); err != nil {
		t.Fatalf("drain watch sends: %v", err)
	}

	delegates, err := s.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	got := delegates[observer.DelegateID]
	if got == nil || got.LatestJobID == observer.JobID {
		t.Fatalf("observer delegate = %+v, want resumed under same delegate_id", got)
	}
	grants, err := s.jobManager.store.LoadGrants()
	if err != nil {
		t.Fatalf("LoadGrants: %v", err)
	}
	if !grants[got.ChildSessionID][shellOut.JobID] {
		t.Fatalf("grants = %+v, want observer child session grant to watched job", grants)
	}
}

// captureWatchSends returns a closure that drives the drain's delivery primitive
// for every recorded delegate-targeted pending send and returns the captured
// delivery args. Observation only records pending intent (spec §3); calling the
// returned closure stands in for the loop-owned drain, capturing what it delivers.
func captureWatchSends(t *testing.T, jm *jobManager) func() []sendMessageArgs {
	t.Helper()
	var mu sync.Mutex
	var sent []sendMessageArgs
	send := func(_ context.Context, a sendMessageArgs) sendMessageResult {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, a)
		return sendMessageResult{}
	}

	seedCommonWatchSendTargets(t, jm)

	return func() []sendMessageArgs {
		_ = drainWatchSendsVia(t, jm, send)
		mu.Lock()
		defer mu.Unlock()
		return append([]sendMessageArgs(nil), sent...)
	}
}

// TestWatchSendStateCarriesDeliveryProvenance proves a watch delivery mints its
// own causal provenance (the watch key + a chain entry naming the delivery id)
// onto the persisted WatchSendState, so a downstream event that this send causes
// can be recognized as the watch's own echo.
func TestWatchSendStateCarriesDeliveryProvenance(t *testing.T) {
	s := newTestSession(t)
	cfg := &watchConfig{
		target:           runtimeMessageAliasCaller,
		watchID:          "watch_A",
		generation:       "wg_1",
		send:             &watchSendArgs{To: "dlg_1", Message: "observe"},
		pending:          make(map[jobstore.WatchSendKey]*jobstore.WatchSendState),
		settledUpdateSeq: make(map[jobstore.WatchSendKey]uint64),
	}
	ev := events.New(events.CommunicateData{Message: "actually alpha marker", AwaitReply: false})
	ev.SessionID = s.ID()

	d := s.jobManager.watchSendSnapshot(cfg, runtimeMessageAliasCaller, "event: COMMUNICATE", ev)
	state := s.jobManager.watchSendState(d, "dlg_1")

	if !provenance.ContainsWatch(state.Provenance, "watch_A", "wg_1") {
		t.Fatalf("watch send provenance = %+v, want watch_A/wg_1", state.Provenance)
	}
	if provenance.LatestDeliveryID(state.Provenance) != d.deliveryID {
		t.Fatalf("latest delivery id = %q, want %q", provenance.LatestDeliveryID(state.Provenance), d.deliveryID)
	}
}

// TestObserverInjectionDoesNotRetriggerSameWatch proves that when an observer
// receives a watch delivery and calls delegate_send(to="caller"), the resulting
// steering injected into the parent carries the watch's provenance, so the
// parent's acknowledgement communicate event is suppressed by shouldSuppressWatch
// and the watch does not fire a second time.
//
// The load-bearing assertion is cfg.nextUpdateSeq == 1 after the suppressed ack:
// each real watch firing increments nextUpdateSeq in watchSendSnapshot; suppression
// skips watchSendSnapshot entirely. If shouldSuppressWatch were removed, the ack
// emit would re-fire the watch and nextUpdateSeq would be 2.
func TestObserverInjectionDoesNotRetriggerSameWatch(t *testing.T) {
	parent := newTestSession(t)
	observer := newTestSession(t)
	// Wire the observer's caller route to the parent so delegate_send(to=caller)
	// enqueues a steering message with the watch provenance on the parent.
	observer.cfg.spawn.parentSteer = parent.SteerWithProvenance

	observerID := observer.ID()
	sub := &subagent{id: observerID, sess: observer, running: true, done: make(chan struct{})}
	parent.subagents.track(sub)

	run, err := parent.attachDelegateJob(parent.jobManager, observerID, "actually watcher", sub)
	if err != nil {
		t.Fatalf("attach observer: %v", err)
	}
	installWatchBelowValidation(t, parent.jobManager, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: run.rec.DelegateID, Message: "Filter this caller message."},
	})
	cfg := onlyWatchConfigForTest(t, parent.jobManager)

	// External communicate event — watch fires once (nextUpdateSeq goes to 1, pending populated).
	parent.emit(events.EventCommunicate, events.CommunicateData{Message: "actually alpha marker", AwaitReply: false})
	if cfg.nextUpdateSeq == 0 {
		t.Fatal("external communicate should trigger observer watch (nextUpdateSeq still 0)")
	}
	var state jobstore.WatchSendState
	for _, pending := range cfg.pending {
		state = *pending
	}
	if !provenance.ContainsWatch(state.Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("delivery provenance = %+v, want %s/%s", state.Provenance, cfg.watchID, cfg.generation)
	}

	// Observer calls delegate_send(to=caller) carrying the delivery's provenance.
	observer.replaceActiveProvenance(state.Provenance)
	res := observer.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  runtimeMessageAliasCaller,
		Message: "PYTHON_QUOTE delivery=" + state.DeliveryID + " quote=Ni!",
	})
	if res.Err != nil || !res.Delivered {
		t.Fatalf("observer caller send = %+v, want delivered", res)
	}

	// Parent drains steering (with the watch provenance) and emits communicate to
	// acknowledge the observer's message. The acknowledge emit must be suppressed.
	for _, msg := range parent.drainSteeringForTurn() {
		parent.appendTurn(schema.TurnSteering, steeringMessageToLLM(msg))
		parent.emit(events.EventSteeringInjected, steeringInjectedDataFromMessage(msg))
	}
	parent.emit(events.EventCommunicate, events.CommunicateData{Message: "acknowledged quote", AwaitReply: false})

	// nextUpdateSeq must still be 1: the ack communicate was suppressed and did not
	// call watchSendSnapshot again. A value of 2 would mean suppression regressed.
	if cfg.nextUpdateSeq != 1 {
		t.Fatalf("nextUpdateSeq = %d after suppressed ack, want 1 (only the original external trigger)", cfg.nextUpdateSeq)
	}
}

func TestIdleWatchSendObserverCallerSendCarriesProvenance(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateWithDefaultOutput("observer ready") },
		func(llm.Request) llm.Response {
			args, _ := json.Marshal(map[string]any{
				"to":      runtimeMessageAliasCaller,
				"message": "observer says Ni",
			})
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "send_caller",
				Name:      "delegate_send",
				Arguments: args,
				Type:      "function",
			})
		},
		func(llm.Request) llm.Response { return communicateWithDefaultOutput("observer reported") },
	}})
	parent := newDelegateTestSession(t, c)
	observer := parent.createDelegate(context.Background(), delegateArgs{Task: "observe", Background: false, BlockTimeoutMS: 5000})
	if observer.Err != nil {
		t.Fatalf("observer delegate: %v", observer.Err)
	}
	if _, err := parent.jobManager.configureWatch(watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: observer.DelegateID, Message: "Filter this caller message."},
	}); err != nil {
		t.Fatalf("configure observer watch: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, parent.jobManager)

	parent.emit(events.EventCommunicate, events.CommunicateData{Message: "actually alpha marker", AwaitReply: false})
	if cfg.nextUpdateSeq != 1 {
		t.Fatalf("external communicate fired %d watch sends, want 1", cfg.nextUpdateSeq)
	}
	if err := drainWatchSendsVia(t, parent.jobManager, parent.sendDelegateMessage); err != nil {
		t.Fatalf("drain watch sends: %v", err)
	}

	delegates, err := parent.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	delegateRec := delegates[observer.DelegateID]
	if delegateRec == nil || delegateRec.ChildSessionID == "" {
		t.Fatalf("observer delegate record = %+v, want child session", delegateRec)
	}
	result := waitForRuntimeSubagent(t, parent, delegateRec.ChildSessionID)
	if result.Status != SubagentCompleted {
		t.Fatalf("observer resumed status = %s, want completed: %+v", result.Status, result)
	}

	drained := parent.drainSteeringForTurn()
	if len(drained) != 1 {
		t.Fatalf("parent steering messages = %d, want observer caller send", len(drained))
	}
	if !provenance.ContainsWatch(drained[0].Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("observer caller steering provenance = %+v, want %s/%s", drained[0].Provenance, cfg.watchID, cfg.generation)
	}

	parent.emit(events.EventCommunicate, events.CommunicateData{Message: "acknowledged observer", AwaitReply: false})
	if cfg.nextUpdateSeq != 1 {
		t.Fatalf("nextUpdateSeq = %d after observer ack, want 1 (same-watch echo suppressed)", cfg.nextUpdateSeq)
	}
}

func TestRunningDelegateSendFallsBackToActiveProvenance(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{id: child.ID(), sess: child, running: true, done: make(chan struct{})}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "running observer", sub)
	if err != nil {
		t.Fatalf("attach delegate: %v", err)
	}

	activeProv := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", parent.ID(), "caller")
	parent.replaceActiveProvenance(activeProv)
	res := parent.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  run.rec.DelegateID,
		Message: "steer running observer",
	})
	if res.Err != nil || res.Action != "steered" {
		t.Fatalf("send running delegate = %+v, want steered", res)
	}

	drained := child.drainSteeringForTurn()
	if len(drained) != 1 {
		t.Fatalf("drained steering = %d, want 1", len(drained))
	}
	if !provenance.ContainsWatch(drained[0].Provenance, "watch_A", "wg_1") {
		t.Fatalf("steering provenance = %+v, want watch_A/wg_1", drained[0].Provenance)
	}
}

// TestObserverNotificationAcknowledgementDoesNotRetriggerSameWatch proves that
// when the parent acknowledges an observer's terminal notification (whose pending
// event carried the watch's provenance), the resulting communicate event is
// suppressed by shouldSuppressWatch and the watch does not fire.
//
// The load-bearing assertion is cfg.nextUpdateSeq == 0 (no firing) after the
// communicate emit. If shouldSuppressWatch were removed, the ack communicate
// would fire the watch and nextUpdateSeq would be 1.
func TestObserverNotificationAcknowledgementDoesNotRetriggerSameWatch(t *testing.T) {
	parent := newTestSession(t)
	// Seed the watch send target (dlg_observer / job_observer).
	seedWatchSendDelegateTarget(t, parent.jobManager, "dlg_observer")
	installWatchBelowValidation(t, parent.jobManager, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: "dlg_observer", Message: "observe"},
	})
	cfg := onlyWatchConfigForTest(t, parent.jobManager)

	// Persist a durable terminal record for "job_completed" (distinct from the
	// "job_observer" running job seeded by seedWatchSendDelegateTarget) whose
	// notification-pending event carries the watch's (watchID, generation)
	// provenance. filterDeliverableJobNotifications rebuilds the notification from
	// this record so the delivery inherits the provenance and acceptNotificationInput
	// adopts it into the parent's active provenance.
	watchProv := provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", parent.ID(), "caller")
	appendPendingJobNotificationRecordWithProvenance(t, parent.jobManager, parent.ID(), "job_completed", watchProv)
	// Enqueue a wake token so acceptNotificationInput sees a pending notification.
	parent.enqueueJobNotification(jobNotification{
		JobID:   "job_completed",
		JobType: string(jobstore.JobDelegate),
		Status:  string(jobstore.StatusCompleted),
	})

	if !parent.acceptNotificationInput(context.Background()) {
		t.Fatal("notification input should proceed")
	}
	// After accepting the notification, the parent's active provenance carries the
	// watch provenance. An acknowledgement communicate must be suppressed.
	parent.emit(events.EventCommunicate, events.CommunicateData{Message: "observer done acknowledged", AwaitReply: false})

	// nextUpdateSeq must still be 0: the ack communicate was suppressed and did not
	// call watchSendSnapshot. A value of 1 would mean suppression regressed.
	if cfg.nextUpdateSeq != 0 {
		t.Fatalf("nextUpdateSeq = %d after suppressed ack, want 0 (notification ack must not retrigger watch)", cfg.nextUpdateSeq)
	}
}

func TestWatchDrivenDelegateJobNotificationDoesNotRetriggerSameWatch(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	seedWatchSendDelegateTarget(t, parent.jobManager, "dlg_observer")
	installWatchBelowValidation(t, parent.jobManager, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_observer", Message: "observe"},
	})
	cfg := onlyWatchConfigForTest(t, parent.jobManager)
	watchProv := provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", parent.ID(), "caller")

	sub := completedDelegateSubagent(child, "NOTIFY_DONE")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJobFromWatchWithDelegate(
		parent.jobManager,
		child.ID(),
		"Classify this job notification.",
		sub,
		"dlg_observer",
		nil,
		nil,
		true,
		watchProv,
	)
	if err != nil {
		t.Fatalf("attach watch-driven delegate: %v", err)
	}
	if !provenance.ContainsWatch(run.rec.Provenance, cfg.watchID, cfg.generation) {
		t.Fatalf("run provenance = %+v, want %s/%s", run.rec.Provenance, cfg.watchID, cfg.generation)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalize watch-driven delegate: %v", err)
	}

	if cfg.nextUpdateSeq != 0 {
		t.Fatalf("nextUpdateSeq = %d after watch-driven delegate completion, want 0 (own notification must be suppressed)", cfg.nextUpdateSeq)
	}
	if len(cfg.pending) != 0 {
		t.Fatalf("pending sends = %d after watch-driven delegate completion, want 0", len(cfg.pending))
	}
}

func TestDelegateOutputCausedByWatchDoesNotRetriggerOutputMatchWatch(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := completedDelegateSubagent(child, "server READY")
	parent.subagents.track(sub)

	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "observed delegate", sub)
	if err != nil {
		t.Fatalf("attach delegate: %v", err)
	}

	var watchNotifications []jobNotification
	parent.jobManager.enqueue = func(n jobNotification) {
		if n.Status == jobNotificationEventWatch || strings.Contains(n.Reason, "output_match") {
			watchNotifications = append(watchNotifications, n)
		}
	}
	if _, err := parent.jobManager.configureWatch(watchArgs{Target: run.rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure watch: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, parent.jobManager)
	child.replaceActiveProvenance(provenance.WithWatch(nil, cfg.watchID, cfg.generation, "wd_1", parent.ID(), run.rec.JobID))

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalize delegate: %v", err)
	}

	if len(watchNotifications) != 0 {
		t.Fatalf("same-watch delegate output_match must be suppressed; got %d notifications: %+v", len(watchNotifications), watchNotifications)
	}
	if cfg.deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0 for same-watch delegate output", cfg.deliveries)
	}
}
