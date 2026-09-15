package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// heldLegAdapter parks the FIRST model call until the test releases it and
// answers every call with a terminal response, recording each request. It is
// the skill guard's provider "hold" in miniature: a turn whose in-flight leg
// the test can drain a queue into before the leg returns. (stopHoldAdapter's
// first call returns only on cancellation and closeRaceAdapter holds every
// call, so neither can release the first leg and answer the second.)
type heldLegAdapter struct {
	entered chan struct{} // closed when the first call arrives
	release chan struct{} // closed by the test to let the first call return

	mu       sync.Mutex
	requests []llm.Request
}

func newHeldLegAdapter() *heldLegAdapter {
	return &heldLegAdapter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (a *heldLegAdapter) Name() string { return "openai" }

func (a *heldLegAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	n := len(a.requests)
	a.mu.Unlock()
	if n == 1 {
		close(a.entered)
		<-a.release
	}
	resp := finalResponse(fmt.Sprintf("leg %d done", n))
	resp.Provider = a.Name()
	resp.Model = req.Model
	return resp, nil
}

func (a *heldLegAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *heldLegAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

// boundaryTransitions renders the events that tell a client where the session
// stands -- turn openings and session ends with the state they carry -- in
// stream order, so a failure names what a client would have seen.
func boundaryTransitions(seen []events.SessionEvent) []string {
	var out []string
	for _, ev := range seen {
		switch ev.Kind {
		case events.EventTurnStarted:
			out = append(out, "turn_started:"+ev.Data.(events.TurnStartedData).TurnID)
		case events.EventUserInput:
			out = append(out, "user_input:"+ev.Data.(events.UserInputData).StableTurnID)
		case events.EventSessionEnd:
			data := ev.Data.(events.SessionEndData)
			out = append(out, "session_end:"+data.Reason+":"+data.State)
		}
	}
	return out
}

// recordEvents joins the session's lossless event stream; the returned func
// closes the session, waits for the stream to drain, and hands back everything
// it saw.
func recordEvents(s *Session) func() []events.SessionEvent {
	var mu sync.Mutex
	var seen []events.SessionEvent
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	}, func() { close(drained) })
	return func() []events.SessionEvent {
		s.Close()
		<-drained
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

// drainMidHeldLeg runs one input whose first model call is held, drains a
// queued entry as steering while that call is in flight, and returns the
// drain's reserved turn id (the carrier's) plus the channel ProcessInput's
// result lands on. release is left to the caller.
func drainMidHeldLeg(ctx context.Context, t *testing.T, s *Session, adapter *heldLegAdapter) (carrierTurnID string, done <-chan error) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := s.ProcessInput(ctx, "open a long turn for the queue", nil)
		result <- err
	}()
	select {
	case <-adapter.entered:
	// TRIPWIRE: scripted in-process adapter, no real I/O; only a genuine hang gets here.
	case <-time.After(10 * time.Second):
		t.Fatal("the first model call never arrived")
	}
	queueOneMutation(t, s, "cm-queued-second-pass", "second pass")
	revision := s.clientMutations.snapshot().QueueRevision
	drain, err := s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{
		ClientMutationID:      "cm-drain-mid-leg",
		ExpectedQueueRevision: revision,
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationDrainAsSteer: %v", err)
	}
	if drain.Receipt.TurnID == "" {
		t.Fatalf("drain receipt carries no turn id: %+v", drain.Receipt)
	}
	return drain.Receipt.TurnID, result
}

func awaitInput(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	// TRIPWIRE: scripted adapter, no real I/O; only a genuine hang gets here.
	case <-time.After(10 * time.Second):
		t.Fatal("ProcessInput never returned")
		return nil
	}
}

// steerAppendRefusal drives the client-mutation transcript seam: while refuse
// is set, the transcript refuses the steering turn of one client mutation --
// a disk with room for everything but the steer -- and every other write
// lands for real. The writer is untouched, so nothing is poisoned.
type steerAppendRefusal struct {
	refuse   atomic.Bool
	refusals atomic.Int32
	// onRefuse, when set, runs on the refusing write before it fails -- the
	// moment the steer is popped and its append is about to fail.
	onRefuse func()
}

func refuseSteerAppends(s *Session, clientMutationID string) *steerAppendRefusal {
	r := &steerAppendRefusal{}
	s.clientMutationTranscriptAppend = func(turn schema.Turn) error {
		if r.refuse.Load() && turn.Kind == schema.TurnSteering && turn.ClientMutationID == clientMutationID {
			r.refusals.Add(1)
			if r.onRefuse != nil {
				r.onRefuse()
			}
			return errors.New("injected: no space left on device")
		}
		return s.writeTranscriptDurableLocked(turn)
	}
	return r
}

// countingUserInputWake installs the daemon's pending-input wake seam and
// returns a channel that receives one value per wake.
func countingUserInputWake(s *Session) <-chan struct{} {
	wakes := make(chan struct{}, 64)
	s.SetPendingUserInputWakeFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	return wakes
}

func drainWakes(wakes <-chan struct{}) {
	for {
		select {
		case <-wakes:
		default:
			return
		}
	}
}

// stopFromTheClaimWindow is the daemon's Stop (cmd/evener/serve.go's
// cancelAndWait) armed to land in a window a test opens: cancel the running
// input, wait for the runner to unwind, then finalize. The returned func runs
// it and waits only until the cancel landed, so the code under test resumes
// with the Stop in flight; processDone is what the Stop then waits on.
func stopFromTheClaimWindow(s *Session, cancel context.CancelFunc, processDone <-chan struct{}) (stop func(), result <-chan error) {
	interruptDone := make(chan error, 1)
	var once sync.Once
	return func() {
		once.Do(func() {
			cancelled := make(chan struct{})
			go func() {
				_, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
					ClientMutationID: "stop-in-the-window",
				}, func() {
					cancel()
					close(cancelled)
					<-processDone
				})
				interruptDone <- err
			}()
			<-cancelled
		})
	}, interruptDone
}

// TestDrainAsSteerKeepsTheTurnOpenUntilItsSteeringLegRuns is the measured
// failure from issue #1308 (CI run 34900212155): the queue was drained as
// steering while the turn's model call was in flight, that call came back a
// terminal communicate, and the turn settled idle -- EventSessionEnd with
// state idle, the wire's "offer a fresh turn" -- while the drained text was
// still undispatched. A wake opened the carrier turn ~0.9s later. Any client
// reading the status offered a fresh turn on a session that still owed the
// user a leg.
//
// The contract this pins: the input that drained the queue is not over until
// the steering it produced has run. The steering leg runs inside the same
// ProcessInput call, and the only EventSessionEnd of that call comes after the
// carrier turn opened.
func TestDrainAsSteerKeepsTheTurnOpenUntilItsSteeringLegRuns(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	seen := recordEvents(s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	carrier, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	transitions := boundaryTransitions(seen())

	requests := adapter.Requests()
	if len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("ProcessInput returned after %d model call(s) with the drained steering leg undispatched; the session settled idle mid-turn (transitions: %s)",
			len(requests), strings.Join(transitions, " "))
	}
	carrierOpened := -1
	var sessionEnds []int
	for i, tr := range transitions {
		switch {
		case tr == "turn_started:"+carrier:
			carrierOpened = i
		case strings.HasPrefix(tr, "session_end:"):
			sessionEnds = append(sessionEnds, i)
		}
	}
	if carrierOpened < 0 {
		t.Fatalf("the drain's turn %s never opened (transitions: %s)", carrier, strings.Join(transitions, " "))
	}
	if len(sessionEnds) != 1 || sessionEnds[0] < carrierOpened {
		t.Fatalf("the input ended before its drained steering leg ran; want exactly one session end after %s opened (transitions: %s)",
			carrier, strings.Join(transitions, " "))
	}
	// idle or awaiting is settleTerminalState's call; what matters here is that
	// the one session end is the clean completion of BOTH legs.
	if got := transitions[sessionEnds[0]]; !strings.HasPrefix(got, "session_end:input_complete:") {
		t.Fatalf("session end = %s, want input_complete once both legs ran", got)
	}
}

// TestSteeringCarrierAppendFailureFailsTheTurnAndTheNextRunCarriesTheSteer
// is the failure policy, the same as a queued message whose append fails:
// the carrier's announced turn fails without a model request, the input ends
// with that error and the session idle, the steer stays accepted in the
// queue (runnable work, so the session does not rest), and the next wake --
// not this input -- carries it. Nothing is lost and nothing is retried in a
// loop.
func TestSteeringCarrierAppendFailureFailsTheTurnAndTheNextRunCarriesTheSteer(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	refusal := refuseSteerAppends(s, "cm-drain-mid-leg")
	// The disk fills the moment the carrier is claimed.
	s.cfg.testOnly.steeringCarrierClaimed = func(string) { refusal.refuse.Store(true) }
	seen := recordEvents(s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	carrier, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err == nil || !strings.Contains(err.Error(), "stays queued") {
		t.Fatalf("ProcessInput returned %v, want the carrier's failure: its steering was not recorded", err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider requests = %d, want 1: a carrier whose steer was not recorded must not make a model request", got)
	}
	if got := refusal.refusals.Load(); got != 1 {
		t.Fatalf("steer appends attempted = %d within the input, want 1: the failed steer was tried again before the next run", got)
	}
	snapshot := s.clientMutations.snapshot()
	if pending, ok := snapshot.PendingExecutions["cm-drain-mid-leg"]; !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("the undelivered steer is pending=%v state=%q, want accepted", ok, pending.ExecutionState)
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("ActiveTurnID = %q after the input, want the carrier's claim released", snapshot.ActiveTurnID)
	}
	if !s.hasPendingUserSteering() {
		t.Fatal("the steer is not back in the queue: nothing will carry it")
	}
	if got := s.State(); got != SessionIdle {
		t.Fatalf("session state = %q after the input, want idle", got)
	}

	// The disk has room again; the next wake carries the steer.
	refusal.refuse.Store(false)
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("the wake after the failed carrier: ran=%v err=%v, want it to carry the steer", ran, err)
	}
	if requests := adapter.Requests(); len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("the wake made %d request(s) in total and did not carry %q", len(requests), "second pass")
	}
	if _, still := s.clientMutations.snapshot().PendingExecutions["cm-drain-mid-leg"]; still {
		t.Fatal("the steer is still pending after the wake delivered it")
	}

	// Within the input: one carrier turn, failed once, and the input ended as
	// a failed turn. What follows the first session end is the wake's run.
	carrierOpenings, turnFailures := 0, 0
	inputEnd := ""
	for _, ev := range seen() {
		if ev.Kind == events.EventSessionEnd {
			data := ev.Data.(events.SessionEndData)
			inputEnd = data.Reason + ":" + data.State
			break
		}
		switch ev.Kind {
		case events.EventTurnStarted:
			if ev.Data.(events.TurnStartedData).TurnID == carrier {
				carrierOpenings++
			}
		case events.EventError:
			turnFailures++
		}
	}
	if carrierOpenings != 1 || turnFailures != 1 || !strings.HasPrefix(inputEnd, "turn_failed:") {
		t.Fatalf("within the input: carrier openings=%d turn failures=%d session end=%q, want one carrier turn, failed once, ending the input as a failed turn",
			carrierOpenings, turnFailures, inputEnd)
	}
}

// TestStopInTheCarrierClaimWindowParksTheSteerForTheNextUserRun: the drain
// ladder claims the carrier (ActiveTurnID = the steer's reserved id) and only
// then opens the turn that takes the steer. A Stop landing between the two
// names the carrier. The steer it never took is a message the user still owes
// a run, exactly like a queued message a Stop returns to the queue (wms7): it
// stays accepted, parked behind the steering hold, and the user's next run
// carries it.
func TestStopInTheCarrierClaimWindowParksTheSteerForTheNextUserRun(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	serveSession(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processDone := make(chan struct{})
	stop, stopped := stopFromTheClaimWindow(s, cancel, processDone)
	s.cfg.testOnly.steeringCarrierClaimed = func(string) { stop() }
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	_, done := drainMidHeldLeg(ctx, t, s, adapter)
	close(adapter.release)
	err := awaitInput(t, done)
	close(processDone)
	if err == nil {
		t.Fatal("ProcessInput returned nil after the Stop; this test is not in the window it means to be")
	}
	if err := <-stopped; err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}

	snapshot := s.clientMutations.snapshot()
	if pending, ok := snapshot.PendingExecutions["cm-drain-mid-leg"]; !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("the steer the cancelled carrier never took is pending=%v state=%q, want accepted", ok, pending.ExecutionState)
	}
	if !snapshot.SteeringHeld {
		t.Fatal("the Stop parked nothing: the steer would be delivered by the next wake, the very run the user just stopped")
	}
	if snapshot.ActiveTurnID != "" || snapshot.InterruptFence != nil {
		t.Fatalf("ActiveTurnID=%q fence=%v after the Stop settled, want both clear", snapshot.ActiveTurnID, snapshot.InterruptFence)
	}
	if !s.hasPendingUserSteering() {
		t.Fatal("the durable steer has no in-memory copy: nothing will ever pop it")
	}
	if id, ok := s.claimSteeringCarrierTurn(); ok {
		t.Fatalf("claimSteeringCarrierTurn claimed %q while the steer is parked", id)
	}
	runUserTurnAndExpectTheSteerCarried(t, s, adapter)
}

// TestStopWhileTheCarrierIsAppendingParksTheSteer: a Stop lands after the
// carrier popped its steer and while the append is in flight; the append then
// fails. The steer never left accepted, the Stop's finalization parks it, no
// wake fires for it, and the user's next run carries it.
func TestStopWhileTheCarrierIsAppendingParksTheSteer(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	serveSession(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processDone := make(chan struct{})
	stop, stopped := stopFromTheClaimWindow(s, cancel, processDone)
	refusal := refuseSteerAppends(s, "cm-drain-mid-leg")
	refusal.refuse.Store(true)
	wakes := countingUserInputWake(s)
	refusal.onRefuse = func() {
		// The wakes before this point are the drain's own acceptance-time
		// wake; only what the Stop arms from here on is under test.
		drainWakes(wakes)
		stop()
	}
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	_, done := drainMidHeldLeg(ctx, t, s, adapter)
	close(adapter.release)
	awaitInput(t, done)
	close(processDone)
	if err := <-stopped; err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}
	if got := refusal.refusals.Load(); got != 1 {
		t.Fatalf("steer appends refused = %d, want 1; this test is not in the window it means to be", got)
	}

	snapshot := s.clientMutations.snapshot()
	if pending, ok := snapshot.PendingExecutions["cm-drain-mid-leg"]; !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("the steer is pending=%v state=%q, want accepted", ok, pending.ExecutionState)
	}
	if !snapshot.SteeringHeld {
		t.Fatal("the Stop's finalization left the steer unparked: the wake will deliver what the user just stopped")
	}
	// The Stop's own closing wake runs inside InterruptClientMutation and
	// stands down for a parked steer; nothing else arms one, and the retry
	// that used to is gone.
	select {
	case <-wakes:
		t.Fatal("a wake fired for the steer the Stop parked")
	default:
	}
	if id, ok := s.claimSteeringCarrierTurn(); ok {
		t.Fatalf("claimSteeringCarrierTurn claimed %q while the steer is parked", id)
	}
	refusal.refuse.Store(false)
	runUserTurnAndExpectTheSteerCarried(t, s, adapter)
}

// runUserTurnAndExpectTheSteerCarried is the user's next run after a Stop
// parked the drained steer: turn/start releases the hold, and the run's
// request carries the steer.
func runUserTurnAndExpectTheSteerCarried(t *testing.T, s *Session, adapter *heldLegAdapter) {
	t.Helper()
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "carry on"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if _, _, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil {
		t.Fatalf("ProcessClientMutationStart: %v", err)
	}
	if requests := adapter.Requests(); len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("the user's next run made %d request(s) and did not carry the parked steer (want the second request to carry %q)", len(requests), "second pass")
	}
	if _, still := s.clientMutations.snapshot().PendingExecutions["cm-drain-mid-leg"]; still || s.clientMutations.steeringHeld() {
		t.Fatalf("after the run: steer still pending=%v held=%v, want delivered and released", still, s.clientMutations.steeringHeld())
	}
}

// restoreWithScriptedModel is the resume path `evener serve --resume` takes,
// with a scripted model so a run after the restore can complete.
func restoreWithScriptedModel(t *testing.T, dir, id string) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	client := llm.NewClient()
	// Enough scripted rounds for the session namer and the turn: this restore
	// has no dedicated namer adapter.
	client.Register(&fakeAdapter{name: "openai", steps: repeatFinalResponse(4, "carried")})
	restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(restored.Close)
	serveSession(t, restored)
	return restored
}

// TestRestoreReleasesACarrierClaimThatNeverRan is #1342. The carrier's claim
// publishes ActiveTurnID = the steer's reserved id durably, before the turn
// opens and takes the steer. A process death in that window leaves the slot
// named by a steer that is still accepted; the load-time sweep releases it
// (nothing re-runs a carrier by its id) and the steer, still pending, is
// carried by the next run.
func TestRestoreReleasesACarrierClaimThatNeverRan(t *testing.T) {
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	serveSession(t, crashed)
	if err := crashed.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := crashed.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-carrier-crash",
		Input:            []appwire.InputItem{{Type: "text", Text: "carried after the restart"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	if _, ok := crashed.claimSteeringCarrierTurn(); !ok {
		t.Fatal("the steer could not claim a carrier turn; this test is not in the state it means to be")
	}
	// The process dies here: the claim is durable, the carrier never opened.
	crashed.Close()

	restored := restoreWithScriptedModel(t, dir, id)
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after restore, want released: a carrier claim that never ran holds the slot against every later turn/start (#1342)", got)
	}
	if !restored.hasPendingUserSteering() {
		t.Fatal("the steer is no longer pending after restore; the release must not lose it")
	}
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-start-after-restart",
		Input:            []appwire.InputItem{{Type: "text", Text: "hello again"}},
	}); err != nil {
		t.Fatalf("turn/start after the restart: %v", err)
	}
	if _, _, err := restored.ProcessClientMutationStart(context.Background(), nil); err != nil {
		t.Fatalf("ProcessClientMutationStart: %v", err)
	}
	if _, still := restored.clientMutations.snapshot().PendingExecutions["cm-carrier-crash"]; still {
		t.Fatal("the steer is still pending after the user's run; want it carried")
	}
}

// recordSteerWithFailedIncorporation takes the steer at the head of the queue
// and consumes it with a store that refuses the incorporation write: the
// transcript append lands, the store keeps the steer accepted, and the steer
// stays in flight in memory, marked recorded. This is the state a process dies
// in between the append and the mark.
func recordSteerWithFailedIncorporation(t *testing.T, s *Session, clientMutationID string) {
	t.Helper()
	msg, ok := s.popSteeringHead()
	if !ok || msg.ClientMutationID != clientMutationID {
		t.Fatalf("popSteeringHead took %q (ok=%v), want %q", msg.ClientMutationID, ok, clientMutationID)
	}
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		return errors.New("injected: incorporation write refused")
	}
	recorded := s.consumeSteeringMessage(msg)
	s.clientMutations.faults.BeforeEffectSnapshotRename = nil
	if !recorded {
		t.Fatal("consumeSteeringMessage reported the append failed; this test wants it recorded")
	}
	if state := s.clientMutations.snapshot().PendingExecutions[clientMutationID].ExecutionState; state != "accepted" {
		t.Fatalf("the steer reads %q, want accepted: the fault did not land on the incorporation write", state)
	}
	s.mu.Lock()
	inTranscript := slices.ContainsFunc(s.history, func(turn schema.Turn) bool {
		return turn.Kind == schema.TurnSteering && turn.ClientMutationID == clientMutationID
	})
	s.mu.Unlock()
	if !inTranscript {
		t.Fatal("the transcript does not hold the steer; this test wants it recorded")
	}
	if s.hasPendingUserSteering() {
		t.Fatal("a recorded steer was re-queued: the in-flight window did not cover the failed incorporation write")
	}
}

// TestRestoreFinalizesARecordedSteerAndReleasesItsCarrierSlot: the transcript
// decides. A steer the transcript holds -- the carrier recorded it and the
// process died before the store's incorporation write -- is finalized at
// restore rather than queued and delivered a second time, and the slot its
// carrier claimed is released so the next turn/start is accepted.
func TestRestoreFinalizesARecordedSteerAndReleasesItsCarrierSlot(t *testing.T) {
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	serveSession(t, crashed)
	if err := crashed.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := crashed.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-recorded-carrier",
		Input:            []appwire.InputItem{{Type: "text", Text: "recorded before the crash"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	carrier, ok := crashed.claimSteeringCarrierTurn()
	if !ok {
		t.Fatal("the steer could not claim a carrier turn; this test is not in the state it means to be")
	}
	recordSteerWithFailedIncorporation(t, crashed, "cm-recorded-carrier")
	if got := crashed.clientMutations.snapshot().ActiveTurnID; got != carrier {
		t.Fatalf("ActiveTurnID = %q before the crash, want the carrier %q", got, carrier)
	}
	crashed.Close()

	restored := restoreWithScriptedModel(t, dir, id)
	snapshot := restored.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["cm-recorded-carrier"]; still {
		t.Fatal("restore left a steer the transcript holds pending")
	}
	if record := snapshot.Journal["cm-recorded-carrier"]; record.ExecutionState != "incorporated" || record.OperationState != clientMutationOperationTerminal {
		t.Fatalf("the recorded steer's journal = %q/%q after restore, want terminal/incorporated", record.OperationState, record.ExecutionState)
	}
	if restored.hasPendingUserSteering() {
		t.Fatal("restore queued a steer the transcript already holds: it would be delivered a second time")
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("ActiveTurnID = %q after restore finalized the carrier's steer, want released: every later turn/start is refused", snapshot.ActiveTurnID)
	}
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-start-after-restart",
		Input:            []appwire.InputItem{{Type: "text", Text: "hello again"}},
	}); err != nil {
		t.Fatalf("turn/start after the restart: %v", err)
	}
}

// TestStopFinalizationMarksARecordedSteerAndReleasesTheHold: a passenger
// steer whose append landed but whose incorporation write the store refused
// is delivered, and a Stop must not park it. The daemon's order: the Stop is
// accepted (the hold is armed for the accepted steer), cancelAndWait ends the
// running turn, and that turn's completion finalizes the fence -- from the
// in-flight set, never a history scan -- marking the steer incorporated and
// releasing a hold that would otherwise name nothing (rule H, #710).
func TestStopFinalizationMarksARecordedSteerAndReleasesTheHold(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	runningStartTurn(t, s, "running-turn", "do the thing")
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-recorded-inline",
		Input:            []appwire.InputItem{{Type: "text", Text: "recorded inline"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	recordSteerWithFailedIncorporation(t, s, "steer-recorded-inline")

	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-recorded-steer",
	}, func() {
		// The cancelled turn unwinds and completes; its completion finalizes
		// the fence the Stop left.
		if err := s.completeClientMutationInterruptedTurn("running-turn"); err != nil {
			t.Errorf("complete the interrupted turn: %v", err)
		}
	}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	snapshot := s.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["steer-recorded-inline"]; still {
		t.Fatal("the completion's finalization did not mark the recorded steer incorporated")
	}
	if snapshot.SteeringHeld {
		t.Fatal("the hold outlived the last steer: the next steer is accepted and silently parked (#710)")
	}
	if s.hasPendingUserSteering() {
		t.Fatal("a recorded steer is back in the queue after the Stop: it would be delivered a second time")
	}
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "runs"}},
	}); err != nil {
		t.Fatalf("steer after the Stop: %v", err)
	}
	if _, ok := s.claimSteeringCarrierTurn(); !ok {
		t.Fatal("the next steer cannot claim a carrier: it is parked behind a hold that names nothing")
	}
}

// TestReleaseRetryWakesTheSteerBehindAStaleSlot: a slot left behind by a
// release write the store refused closes the steering rail (a claim refuses an
// occupied slot). When the release retry lands it wakes, so the steer behind
// the stale slot runs rather than waiting for an unrelated client action.
func TestReleaseRetryWakesTheSteerBehindAStaleSlot(t *testing.T) {
	// A scripted model, so the carrier the wake runs can complete.
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: repeatFinalResponse(4, "carried")})
	s, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	clk := agenttest.NewFakeClockAt(time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC))
	s.clock = clk
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-behind-stale-slot",
		Input:            []appwire.InputItem{{Type: "text", Text: "deliver me"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	carrier, ok := s.claimSteeringCarrierTurn()
	if !ok {
		t.Fatal("the steer could not claim a carrier turn; this test is not in the state it means to be")
	}
	// The carrier's release write is refused: the slot is stale, the release
	// retry is armed.
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		return errors.New("injected: release write refused")
	}
	if got := s.releaseRunningTurnID(carrier); got != turnNameReleaseStoreFailed {
		t.Fatalf("releaseRunningTurnID = %v, want the store failure that arms the release retry", got)
	}
	s.clientMutations.faults.BeforeEffectSnapshotRename = nil
	wakes := countingUserInputWake(s)
	drainWakes(wakes)

	// The release retry lands on virtual time; Drain returns once its
	// callback has run.
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the release retry, want released; this test is not in the state it means to be", got)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("the release retry landed and woke nobody: the steer behind the stale slot waits for an unrelated client action")
	}
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("the wake's run: ran=%v err=%v, want the steer carried", ran, err)
	}
}

// TestInjectDrainedSteeringStopsAtTheFirstFailedAppend: the drain loop stops
// at a failed append. The failed steer goes back to the head of the queue, so
// a loop that carried on would pop the same steer again -- one attempt per
// peeked message, inside one turn -- and the steer behind it is left for the
// next drain.
func TestInjectDrainedSteeringStopsAtTheFirstFailedAppend(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	refusal := refuseSteerAppends(s, "steer-first")
	refusal.refuse.Store(true)
	for _, steer := range []struct{ id, text string }{{"steer-first", "first steer"}, {"steer-second", "second steer"}} {
		if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
			ClientMutationID: steer.id,
			Input:            []appwire.InputItem{{Type: "text", Text: steer.text}},
		}); err != nil {
			t.Fatalf("steer %s: %v", steer.id, err)
		}
	}
	// The turn's acceptance drains the steering queue once: injectDrainedSteering
	// runs inside acceptUserInput.
	runningStartTurn(t, s, "running-turn", "do the thing")

	if got := refusal.refusals.Load(); got != 1 {
		t.Fatalf("steer appends attempted = %d after one drain, want 1: the loop re-popped the failed steer", got)
	}
	snapshot := s.clientMutations.snapshot()
	for _, id := range []string{"steer-first", "steer-second"} {
		if state := snapshot.PendingExecutions[id].ExecutionState; state != "accepted" {
			t.Fatalf("%s reads %q after the drain, want accepted (first returned, second untouched)", id, state)
		}
	}
	s.mu.Lock()
	queued := len(s.steeringQueue)
	s.mu.Unlock()
	if queued != 2 {
		t.Fatalf("steering queue holds %d after the drain, want both steers still queued", queued)
	}
}
