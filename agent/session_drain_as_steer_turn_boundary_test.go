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
	// later, when set, answers the calls after the first in order (the first
	// always answers with a terminal response once released); calls past its
	// end answer terminally too.
	later []func(llm.Request) llm.Response
	// holdCall is the call that parks until release: the first unless set.
	holdCall int
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
	if n == max(a.holdCall, 1) {
		close(a.entered)
		<-a.release
	}
	var resp llm.Response
	if n >= 2 && n-2 < len(a.later) {
		resp = a.later[n-2](req)
	} else {
		resp = finalResponse(fmt.Sprintf("leg %d done", n))
	}
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
func countingUserInputWake(s *Session) chan struct{} {
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
// queue, parked runnable, and the next external wake -- not this input, and
// not a wake of the failure's own -- carries it. Nothing is lost and nothing
// is retried in a loop.
func TestSteeringCarrierAppendFailureFailsTheTurnAndTheNextRunCarriesTheSteer(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	refusal := refuseSteerAppends(s, "cm-drain-mid-leg")
	wakes := countingUserInputWake(s)
	// The disk fills the moment the carrier is claimed. The wakes before this
	// point are the drain's own acceptance-time wake.
	s.cfg.testOnly.steeringCarrierClaimed = func(string) {
		drainWakes(wakes)
		refusal.refuse.Store(true)
	}
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
	select {
	case <-wakes:
		t.Fatal("the failed append armed a wake of its own: with a disk that stays full that is a loop of failed carrier turns")
	default:
	}

	// The disk has room again, and the next external wake -- the attach-time
	// wake here -- carries the steer.
	refusal.refuse.Store(false)
	externalWake(s, wakes)
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 {
		t.Fatalf("external wake runs = %d, want 1", runs)
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
	if recorded != steeringDelivered {
		t.Fatalf("consumeSteeringMessage = %v; this test wants the steer delivered", recorded)
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

// TestCarrierClaimSkipsASteerInFlight: a steer whose append landed but whose
// incorporation write the store refused is accepted in the store and absent
// from the queue (in flight, recorded). A carrier claim that read the store
// alone took it as the head steer, drained a different steer under its id,
// and failed again on the next wake. Eligibility excludes in-flight steers.
func TestCarrierClaimSkipsASteerInFlight(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-in-flight",
		Input:            []appwire.InputItem{{Type: "text", Text: "recorded, unmarked"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	recordSteerWithFailedIncorporation(t, s, "steer-in-flight")

	if carrier, _ := s.claimSteeringCarrierInput(); carrier.SteeringCarrier {
		t.Fatalf("claimSteeringCarrierInput claimed %q with only an in-flight steer in the store; the carrier would drain nothing and fail", carrier.ClientMutationID)
	}
	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after a refused claim, want empty", got)
	}
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-queued",
		Input:            []appwire.InputItem{{Type: "text", Text: "queued behind it"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	carrier, _ := s.claimSteeringCarrierInput()
	if !carrier.SteeringCarrier || carrier.ClientMutationID != "steer-queued" {
		t.Fatalf("claimSteeringCarrierInput = %q ok=%v, want the queued steer behind the in-flight one", carrier.ClientMutationID, carrier.SteeringCarrier)
	}
}

// TestFailedSteeringSelectionLandsTheSteer: a steer whose skill selection
// cannot be prepared is recorded as a failure and retired from the store, and
// its in-flight window ends with it. Left in flight, every later carrier claim
// would skip a steer that no longer exists -- and skip whatever is queued
// behind it, since eligibility walks the order.
func TestFailedSteeringSelectionLandsTheSteer(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-bad-skill",
		Input:            []appwire.InputItem{{Type: "skill", Name: "no-such-skill"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	msg, ok := s.popSteeringHead()
	if !ok || msg.ClientMutationID != "steer-bad-skill" {
		t.Fatalf("popSteeringHead took %q (ok=%v), want the steer", msg.ClientMutationID, ok)
	}
	if got := s.consumeSteeringMessage(msg); got != steeringRetired {
		t.Fatalf("consumeSteeringMessage = %v, want the steer retired: the failure record landed", got)
	}
	snapshot := s.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["steer-bad-skill"]; still || snapshot.Journal["steer-bad-skill"].ExecutionState != "failed" {
		t.Fatalf("the steer reads pending=%v journal=%q, want retired as failed", still, snapshot.Journal["steer-bad-skill"].ExecutionState)
	}
	if _, inFlight := s.steeringInFlightSample()["steer-bad-skill"]; inFlight {
		t.Fatal("the retired steer is still in flight: every later carrier claim skips it, and everything behind it")
	}
	if s.hasPendingUserSteering() {
		t.Fatal("the retired steer is back in the queue")
	}
}

// TestFailedSteeringSelectionWhoseRecordFailsStopsTheDrain: when the failure
// record itself cannot be appended, the steer goes back to the head of the
// queue and the drain stops there -- carrying on would pop the same steer
// again inside the turn. The steer lands like any failed append: out of the
// in-flight set, queued once, for the next drain.
func TestFailedSteeringSelectionWhoseRecordFailsStopsTheDrain(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	var refusals atomic.Int32
	s.clientMutationTranscriptAppend = func(turn schema.Turn) error {
		if turn.Kind == schema.TurnFailure && turn.ClientMutationID == "steer-bad-skill" {
			refusals.Add(1)
			return errors.New("injected: no space left on device")
		}
		return s.writeTranscriptDurableLocked(turn)
	}
	for _, steer := range []appwire.TurnSteerParams{
		{ClientMutationID: "steer-bad-skill", Input: []appwire.InputItem{{Type: "skill", Name: "no-such-skill"}}},
		{ClientMutationID: "steer-behind", Input: []appwire.InputItem{{Type: "text", Text: "behind it"}}},
	} {
		if _, err := s.AcceptClientMutationSteer(steer); err != nil {
			t.Fatalf("steer %s: %v", steer.ClientMutationID, err)
		}
	}
	// The turn's acceptance drains the steering queue once.
	runningStartTurn(t, s, "running-turn", "do the thing")

	if got := refusals.Load(); got != 1 {
		t.Fatalf("failure records attempted = %d in one drain, want 1: the drain re-popped the steer whose record failed", got)
	}
	if _, inFlight := s.steeringInFlightSample()["steer-bad-skill"]; inFlight {
		t.Fatal("the steer whose failure record did not land is still in flight")
	}
	s.mu.Lock()
	var queued []string
	for _, entry := range s.steeringQueue {
		queued = append(queued, entry.ClientMutationID)
	}
	s.mu.Unlock()
	if !slices.Equal(queued, []string{"steer-bad-skill", "steer-behind"}) {
		t.Fatalf("steering queue = %v after the drain, want both steers queued once, in order", queued)
	}
}

// TestCarrierProceedsWhenOnlyTheIncorporationWriteFails (#1389): the
// carrier's steer is appended to the transcript and only the store's
// incorporation write is refused. The steer IS delivered -- the model reads
// it -- so the carrier makes its model request rather than failing a turn
// whose steer is in the transcript, and the store's record catches up at the
// input's settle through the steering table.
func TestCarrierProceedsWhenOnlyTheIncorporationWriteFails(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	s.cfg.testOnly.steeringCarrierClaimed = func(string) {
		// The first store write after the claim is the incorporation mark.
		writes := 0
		s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
			writes++
			if writes == 1 {
				return errors.New("injected: incorporation write refused")
			}
			return nil
		}
	}

	_, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err != nil {
		t.Fatalf("ProcessInput: %v (the carrier failed a turn whose steer is in the transcript)", err)
	}
	if requests := adapter.Requests(); len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("provider requests = %d, want 2 with the second carrying the steer: it is in the transcript and the model must read it", len(requests))
	}
	snapshot := s.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["cm-drain-mid-leg"]; still {
		t.Fatal("the recorded steer is still pending after the input settled: nothing reconciled the failed incorporation write")
	}
	if record := snapshot.Journal["cm-drain-mid-leg"]; record.ExecutionState != "incorporated" {
		t.Fatalf("the recorded steer's journal reads %q, want incorporated", record.ExecutionState)
	}
	if _, inFlight := s.steeringInFlightSample()["cm-drain-mid-leg"]; inFlight {
		t.Fatal("the reconciled steer is still in flight")
	}
}

// runPendingInputOnEachWake is the daemon's serial input loop in miniature:
// each wake already delivered runs the pending input once, up to limit runs,
// and reports how many ran. It never waits: a wake the run under test did not
// arm is not answered.
func runPendingInputOnEachWake(t *testing.T, s *Session, wakes <-chan struct{}, limit int) (runs int) {
	t.Helper()
	for runs < limit {
		select {
		case <-wakes:
		default:
			return runs
		}
		runs++
		if _, _, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil && !strings.Contains(err.Error(), "stays queued") {
			t.Fatalf("ProcessPendingUserInput on wake %d: %v", runs, err)
		}
	}
	return runs
}

// externalWake is the attach-time wake, the one external trigger a parked
// steer waits for: re-registering the pending-input wake fires it when
// runnable user steering is pending.
func externalWake(s *Session, wakes chan struct{}) {
	s.SetPendingUserInputWakeFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
}

// TestPersistentSteeringAppendFailureRunsOneCarrierPerExternalWake (round 11,
// M1): a steering append that fails without poisoning the writer -- ENOSPC
// before a byte lands, so the rollback succeeds -- and keeps failing. If the
// failure re-arms the wake itself, the daemon answers every wake with a
// carrier that fails again: an unbounded loop of failed turns with no pause.
// The contract: one carrier attempt per external wake; the failed drain parks
// the steer, runnable, and the next external wake -- attach, or any accepted
// client mutation -- carries it once the disk has room.
func TestPersistentSteeringAppendFailureRunsOneCarrierPerExternalWake(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	refusal := refuseSteerAppends(s, "cm-drain-mid-leg")
	refusal.refuse.Store(true)
	wakes := make(chan struct{}, 64)
	externalWake(s, wakes)
	// The wakes before the claim are the drain's own acceptance-time wake.
	s.cfg.testOnly.steeringCarrierClaimed = func(string) { drainWakes(wakes) }
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	_, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err == nil || !strings.Contains(err.Error(), "stays queued") {
		t.Fatalf("ProcessInput returned %v, want the carrier's failure", err)
	}
	// The daemon answers whatever the failure armed, bounded so a loop shows
	// as a count rather than a hang.
	runs := runPendingInputOnEachWake(t, s, wakes, 5)
	if runs != 0 || refusal.refusals.Load() != 1 {
		t.Fatalf("after the failed carrier: wake-driven runs=%d append attempts=%d, want 0 and 1: a failure that re-arms its own wake loops failed turns for as long as the disk is full", runs, refusal.refusals.Load())
	}
	if !s.hasPendingUserSteering() || s.State() != SessionIdle {
		t.Fatalf("queued=%v state=%q, want the steer parked runnable on an idle session", s.hasPendingUserSteering(), s.State())
	}

	// The disk has room again, and the next external wake carries the steer.
	refusal.refuse.Store(false)
	externalWake(s, wakes)
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 {
		t.Fatalf("external wake runs = %d, want 1", runs)
	}
	if requests := adapter.Requests(); len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("provider requests = %d after the external wake, want 2 with the steer carried", len(requests))
	}
	if _, still := s.clientMutations.snapshot().PendingExecutions["cm-drain-mid-leg"]; still {
		t.Fatal("the steer is still pending after it was carried")
	}
}

// TestFailedSelectionRecordParksTheSteerUntilTheNextExternalWake (round 11,
// M2): a steer whose skill selection fails and whose failure record cannot be
// appended goes back to the queue when the carrier exits; nothing is armed by
// the failure (the same contract as M1), the steer is runnable, and the next
// external wake records the failure and retires it.
func TestFailedSelectionRecordParksTheSteerUntilTheNextExternalWake(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	seen := recordEvents(s)
	defer func() {
		if t.Failed() {
			for _, ev := range seen() {
				switch ev.Kind {
				case events.EventWarning, events.EventError, events.EventTurnStarted, events.EventSteeringInjected, events.EventSessionEnd:
					t.Logf("event %v: %+v", ev.Kind, ev.Data)
				}
			}
		}
	}()
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	var refuse atomic.Bool
	refuse.Store(true)
	var refusals atomic.Int32
	s.clientMutationTranscriptAppend = func(turn schema.Turn) error {
		if refuse.Load() && turn.Kind == schema.TurnFailure && turn.ClientMutationID == "steer-bad-skill" {
			refusals.Add(1)
			return errors.New("injected: no space left on device")
		}
		return s.writeTranscriptDurableLocked(turn)
	}
	wakes := make(chan struct{}, 64)
	externalWake(s, wakes)
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-bad-skill",
		Input:            []appwire.InputItem{{Type: "skill", Name: "no-such-skill"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	// The acceptance woke; the daemon runs the carrier, which fails.
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 || refusals.Load() != 1 {
		t.Fatalf("runs=%d failure records attempted=%d after the acceptance wake, want 1 and 1", runs, refusals.Load())
	}
	if state := s.clientMutations.snapshot().PendingExecutions["steer-bad-skill"].ExecutionState; state != "accepted" || !s.hasPendingUserSteering() {
		t.Fatalf("state=%q queued=%v after the carrier exited, want accepted and queued", state, s.hasPendingUserSteering())
	}
	if _, inFlight := s.steeringInFlightSample()["steer-bad-skill"]; inFlight {
		t.Fatal("the steer is still in flight after the carrier exited")
	}
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 0 {
		t.Fatalf("the failure armed %d wake(s); want none (one attempt per external wake)", runs)
	}

	// The disk has room again; the next external wake records the failure.
	refuse.Store(false)
	externalWake(s, wakes)
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 {
		t.Fatalf("external wake runs = %d, want 1", runs)
	}
	snapshot := s.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["steer-bad-skill"]; still || snapshot.Journal["steer-bad-skill"].ExecutionState != "failed" {
		t.Fatalf("pending=%v journal=%q after the external wake, want retired as failed", still, snapshot.Journal["steer-bad-skill"].ExecutionState)
	}
}

// TestFailedRecordedSteerMarkRetriesAtTheNextWake (round 11, M3): a recorded
// steer whose incorporation mark the store refuses stays in flight, out of
// the queue; when the mark's retry fails too, the next wake retries it again
// -- and never re-queues a steer the transcript already holds.
func TestFailedRecordedSteerMarkRetriesAtTheNextWake(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	runningStartTurn(t, s, "running-turn", "do the thing")
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-recorded",
		Input:            []appwire.InputItem{{Type: "text", Text: "recorded"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	recordSteerWithFailedIncorporation(t, s, "steer-recorded")

	// The mark's first retry, at a wake, is refused too.
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		return errors.New("injected: store still refusing")
	}
	s.wakeForPendingSteering()
	s.clientMutations.faults.BeforeEffectSnapshotRename = nil
	if state := s.clientMutations.snapshot().PendingExecutions["steer-recorded"].ExecutionState; state != "accepted" {
		t.Fatalf("state=%q after the refused retry, want still accepted (unmarked)", state)
	}
	if recorded, inFlight := s.steeringInFlightSample()["steer-recorded"]; !inFlight || recorded != "incorporated" {
		t.Fatalf("inFlight=%v recorded=%q after the refused retry, want in flight and recorded as incorporated", inFlight, recorded)
	}
	if s.hasPendingUserSteering() {
		t.Fatal("the recorded steer was re-queued: it would be delivered a second time")
	}

	// The next wake -- here the acceptance of another steer -- retries the mark.
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-next",
		Input:            []appwire.InputItem{{Type: "text", Text: "next"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	snapshot := s.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["steer-recorded"]; still || snapshot.Journal["steer-recorded"].ExecutionState != "incorporated" {
		t.Fatalf("pending=%v journal=%q after the next wake, want marked incorporated", still, snapshot.Journal["steer-recorded"].ExecutionState)
	}
	if _, inFlight := s.steeringInFlightSample()["steer-recorded"]; inFlight {
		t.Fatal("the marked steer is still in flight")
	}
}

// recordFailedSelectionWithFailedRetirement takes the head steer, whose skill
// selection cannot be prepared, and consumes it with a store that refuses the
// retirement write: the failure turn is in the transcript, the store keeps the
// steer accepted, and it is in flight marked recorded.
func recordFailedSelectionWithFailedRetirement(t *testing.T, s *Session, clientMutationID string) {
	t.Helper()
	msg, ok := s.popSteeringHead()
	if !ok || msg.ClientMutationID != clientMutationID {
		t.Fatalf("popSteeringHead took %q (ok=%v), want %q", msg.ClientMutationID, ok, clientMutationID)
	}
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		return errors.New("injected: retirement write refused")
	}
	recorded := s.consumeSteeringMessage(msg)
	s.clientMutations.faults.BeforeEffectSnapshotRename = nil
	if recorded != steeringRetired {
		t.Fatalf("consumeSteeringMessage = %v; this test wants the failure recorded and the steer retired", recorded)
	}
	if state := s.clientMutations.snapshot().PendingExecutions[clientMutationID].ExecutionState; state != "accepted" {
		t.Fatalf("the steer reads %q, want accepted: the fault did not land on the retirement write", state)
	}
}

// TestRecordedSelectionFailureIsMarkedFailedNotIncorporated (round 11, M4):
// a skill-selection failure whose retirement write the store refused is
// recorded as a failure turn; the mark the store catches up with must be the
// failure's terminal state, not "incorporated".
func TestRecordedSelectionFailureIsMarkedFailedNotIncorporated(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-bad-skill",
		Input:            []appwire.InputItem{{Type: "skill", Name: "no-such-skill"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	recordFailedSelectionWithFailedRetirement(t, s, "steer-bad-skill")

	// The next wake -- the acceptance of another steer -- writes the mark.
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-next",
		Input:            []appwire.InputItem{{Type: "text", Text: "next"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	snapshot := s.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["steer-bad-skill"]; still {
		t.Fatal("the recorded failure is still pending after the next wake")
	}
	if got := snapshot.Journal["steer-bad-skill"].ExecutionState; got != "failed" {
		t.Fatalf("the recorded selection failure's journal reads %q after the mark, want failed", got)
	}
}

// TestRestoreMarksARecordedSelectionFailureFailed (round 11, M4 at restore):
// the same failure, with the process dying before the retirement write.
func TestRestoreMarksARecordedSelectionFailureFailed(t *testing.T) {
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	serveSession(t, crashed)
	if err := crashed.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := crashed.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-bad-skill",
		Input:            []appwire.InputItem{{Type: "skill", Name: "no-such-skill"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	recordFailedSelectionWithFailedRetirement(t, crashed, "steer-bad-skill")
	crashed.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	snapshot := restored.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["steer-bad-skill"]; still || restored.hasPendingUserSteering() {
		t.Fatalf("pending=%v queued=%v after restore, want the recorded failure retired", still, restored.hasPendingUserSteering())
	}
	if got := snapshot.Journal["steer-bad-skill"].ExecutionState; got != "failed" {
		t.Fatalf("the recorded selection failure's journal reads %q after restore, want failed", got)
	}
}

// TestCarrierProceedsWhenALaterSteerWasDeliveredBehindAFailedSelection (round
// 12): the head steer's skill selection fails and is retired; the drain goes
// on and delivers the steer behind it. The carrier must make its model
// request -- a steering turn is in the transcript that no request has read --
// and stand down only when the drain delivered nothing.
func TestCarrierProceedsWhenALaterSteerWasDeliveredBehindAFailedSelection(t *testing.T) {
	adapter := newHeldLegAdapter()
	close(adapter.release) // every call answers at once
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	wakes := make(chan struct{}, 64)
	externalWake(s, wakes)
	for _, steer := range []appwire.TurnSteerParams{
		{ClientMutationID: "steer-bad-skill", Input: []appwire.InputItem{{Type: "skill", Name: "no-such-skill"}}},
		{ClientMutationID: "steer-behind", Input: []appwire.InputItem{{Type: "text", Text: "carry me"}}},
	} {
		if _, err := s.AcceptClientMutationSteer(steer); err != nil {
			t.Fatalf("steer %s: %v", steer.ClientMutationID, err)
		}
	}
	drainWakes(wakes)
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("the carrier: ran=%v err=%v", ran, err)
	}

	requests := adapter.Requests()
	if len(requests) != 1 || !requestContainsText(requests[0], "carry me") {
		t.Fatalf("provider requests = %d, want 1 carrying the delivered steer: it is in the transcript and no request has read it", len(requests))
	}
	snapshot := s.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["steer-behind"]; still || snapshot.Journal["steer-behind"].ExecutionState != "incorporated" {
		t.Fatalf("the delivered steer reads pending=%v journal=%q, want incorporated", still, snapshot.Journal["steer-behind"].ExecutionState)
	}
	if _, still := snapshot.PendingExecutions["steer-bad-skill"]; still || snapshot.Journal["steer-bad-skill"].ExecutionState != "failed" {
		t.Fatalf("the failed selection reads pending=%v journal=%q, want retired as failed", still, snapshot.Journal["steer-bad-skill"].ExecutionState)
	}
	if s.hasPendingUserSteering() {
		t.Fatal("steering is still queued after the carrier")
	}
}

// TestRefusedCarrierClaimParksTheSteerForTheNextExternalWake (round 12): the
// claim's write of ActiveTurnID is refused by the store. The steer stays
// accepted and queued, nothing arms a wake of its own (the same parking
// contract as a failed append), and the next external wake -- here the
// acceptance of an unrelated steer -- carries both.
func TestRefusedCarrierClaimParksTheSteerForTheNextExternalWake(t *testing.T) {
	adapter := newHeldLegAdapter()
	close(adapter.release)
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	wakes := make(chan struct{}, 64)
	externalWake(s, wakes)
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-first",
		Input:            []appwire.InputItem{{Type: "text", Text: "first"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	drainWakes(wakes)
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		return errors.New("injected: claim write refused")
	}
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || ran {
		t.Fatalf("the wake with the claim refused: ran=%v err=%v, want a stand-down", ran, err)
	}
	s.clientMutations.faults.BeforeEffectSnapshotRename = nil
	snapshot := s.clientMutations.snapshot()
	if state := snapshot.PendingExecutions["steer-first"].ExecutionState; state != "accepted" || snapshot.ActiveTurnID != "" || !s.hasPendingUserSteering() {
		t.Fatalf("state=%q active=%q queued=%v after the refused claim, want accepted, no slot, queued", state, snapshot.ActiveTurnID, s.hasPendingUserSteering())
	}
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 0 {
		t.Fatalf("the refused claim armed %d wake(s); want none", runs)
	}

	// An unrelated accepted mutation is the next external wake.
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-second",
		Input:            []appwire.InputItem{{Type: "text", Text: "second"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 {
		t.Fatalf("external wake runs = %d, want 1", runs)
	}
	if requests := adapter.Requests(); len(requests) != 1 || !requestContainsText(requests[0], "first") || !requestContainsText(requests[0], "second") {
		t.Fatalf("provider requests = %d, want 1 carrying both steers", len(requests))
	}
	if s.hasPendingUserSteering() {
		t.Fatal("steering is still queued after the external wake carried it")
	}
}

// TestRestoreFinalizesARecordedSteerCompactedOutOfHistory (round 13, M1): the
// carrier recorded its steer, the store's incorporation write failed, and a
// compaction then summarized the transcript past the steering turn before the
// process died. Restore's history starts at the summary (retainedFrom), but
// the full transcript index still holds the steer, and the transcript decides:
// the steer is finalized, not queued and delivered a second time. The
// compaction marker is written directly -- Compact folds through the context
// manager and keeps a history this short verbatim -- as the TurnSummary entry
// resume anchors on.
func TestRestoreFinalizesARecordedSteerCompactedOutOfHistory(t *testing.T) {
	crashed := newTestSessionForEnvctx(t)
	dir, id := crashed.cfg.StateDir, crashed.ID()
	if err := crashed.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := crashed.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-compacted",
		Input:            []appwire.InputItem{{Type: "text", Text: "recorded, then compacted past"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	recordSteerWithFailedIncorporation(t, crashed, "steer-compacted")
	summary := schema.NewTurn(schema.TurnSummary, llm.Assistant("compacted context"))
	crashed.recordTurn(summary, summary)
	_, entries, _, err := readTranscript(crashed.TranscriptPath(), "")
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	if n := len(entries); n < 2 || entries[n-1].Turn.Kind != schema.TurnSummary || entries[n-2].Turn.ClientMutationID != "steer-compacted" {
		t.Fatalf("transcript tail = %d entries, want the steering turn then the summary; this test is not in the state it means to be", n)
	}
	crashed.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	restored.mu.Lock()
	inHistory := slices.ContainsFunc(restored.history, func(turn schema.Turn) bool { return turn.ClientMutationID == "steer-compacted" })
	restored.mu.Unlock()
	if inHistory {
		t.Fatal("the resumed history still holds the steering turn; the compaction did not cut it, so this test measures nothing")
	}
	snapshot := restored.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["steer-compacted"]; still || restored.hasPendingUserSteering() {
		t.Fatalf("pending=%v queued=%v after restore, want the recorded steer finalized: queued, it is delivered a second time", still, restored.hasPendingUserSteering())
	}
	if got := snapshot.Journal["steer-compacted"].ExecutionState; got != "incorporated" {
		t.Fatalf("the recorded steer's journal reads %q after restore, want incorporated", got)
	}
}

// TestRefusedLadderClaimRunsNoAutonomousTurnBehindIt (round 13, M2): the drain
// ladder's carrier claim is refused by the store while a job notification is
// pending and the notification turn's model makes a tool call. Measured at
// `b946b4ee8`: the notification turn ran and its tool round drained the steer
// under a turn id that was not the receipt's. Parked steering runs no
// autonomous turn behind it: the input settles with the steer queued, the
// notification stays pending for the daemon's own wake, and the next external
// wake carries the steer under its reserved id.
func TestRefusedLadderClaimRunsNoAutonomousTurnBehindIt(t *testing.T) {
	adapter := newHeldLegAdapter()
	adapter.later = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(false, "looking at the job") },
		func(llm.Request) llm.Response { return finalResponse("done") },
	}
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	claimed := false
	s.cfg.testOnly.steeringCarrierClaimed = func(string) { claimed = true }

	carrier, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	s.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "shell", Status: "completed", OutputBytes: 42})
	// After the leg completes: popQueueHead writes (1), the carrier claim
	// writes (2). Refuse the claim alone.
	var writes atomic.Int32
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		if writes.Add(1) == 2 {
			return errors.New("injected: claim write refused")
		}
		return nil
	}
	close(adapter.release)
	err := awaitInput(t, done)
	s.clientMutations.faults.BeforeEffectSnapshotRename = nil
	t.Logf("trace: ProcessInput err=%v store writes=%d carrier claimed=%v model calls=%d", err, writes.Load(), claimed, len(adapter.Requests()))
	if claimed {
		t.Fatal("the carrier claim landed; this measurement is not in the state it means to be")
	}
	s.mu.Lock()
	var owner string
	delivered := false
	for _, turn := range s.history {
		if turn.Kind == schema.TurnSteering && turn.ClientMutationID == "cm-drain-mid-leg" {
			delivered, owner = true, turn.OwningTurnID
		}
	}
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if delivered {
		t.Fatalf("an autonomous turn delivered the steer under %q; its receipt names %q", owner, carrier)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model calls = %d, want 1: an autonomous turn ran behind the refused claim", got)
	}
	if state := s.clientMutations.snapshot().PendingExecutions["cm-drain-mid-leg"].ExecutionState; state != "accepted" || !s.hasPendingUserSteering() {
		t.Fatalf("state=%q queued=%v, want the steer parked accepted and queued", state, s.hasPendingUserSteering())
	}
	if s.peekNotifications() != 1 {
		t.Fatalf("pending notifications = %d, want the notification left for the daemon's own wake", s.peekNotifications())
	}

	// The next external wake carries the steer under its reserved id.
	wakes := make(chan struct{}, 64)
	externalWake(s, wakes)
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 {
		t.Fatalf("external wake runs = %d, want 1", runs)
	}
	s.mu.Lock()
	for _, turn := range s.history {
		if turn.Kind == schema.TurnSteering && turn.ClientMutationID == "cm-drain-mid-leg" {
			owner = turn.OwningTurnID
		}
	}
	s.mu.Unlock()
	if owner != carrier {
		t.Fatalf("the steer was delivered under %q, want its receipt's %q", owner, carrier)
	}
}

// TestBufferedAcceptanceWakeDoesNotRetryAFailedInlineCarrier (round 13, M3):
// the steer's acceptance mid-turn arms the pending-input wake, which the daemon
// holds until the input ends. The inline carrier then fails. The buffered wake
// must not spend a second attempt on the failed steer: one attempt per external
// wake, and that wake was consumed by the attempt the ladder already made.
func TestBufferedAcceptanceWakeDoesNotRetryAFailedInlineCarrier(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	refusal := refuseSteerAppends(s, "cm-drain-mid-leg")
	wakes := make(chan struct{}, 64)
	externalWake(s, wakes)
	s.cfg.testOnly.steeringCarrierClaimed = func(string) { refusal.refuse.Store(true) }
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	_, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err == nil || !strings.Contains(err.Error(), "stays queued") {
		t.Fatalf("ProcessInput returned %v, want the carrier's failure", err)
	}
	// The daemon now answers the wake the acceptance buffered mid-turn.
	runs := runPendingInputOnEachWake(t, s, wakes, 5)
	t.Logf("trace: buffered wakes answered=%d append attempts=%d", runs, refusal.refusals.Load())
	if refusal.refusals.Load() != 1 {
		t.Fatalf("append attempts = %d after the buffered acceptance wake, want 1: that wake was spent by the inline attempt", refusal.refusals.Load())
	}

	// The disk has room again; a genuinely external wake carries the steer.
	refusal.refuse.Store(false)
	externalWake(s, wakes)
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 {
		t.Fatalf("external wake runs = %d, want 1", runs)
	}
	if requests := adapter.Requests(); len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("provider requests = %d, want 2 with the steer carried", len(requests))
	}
}

// refuseTheNextCarrierClaimWrite arms the store to refuse the write of the next
// carrier claim, and that write alone.
func refuseTheNextCarrierClaimWrite(s *Session) {
	s.cfg.testOnly.steeringCarrierClaiming = func() {
		s.cfg.testOnly.steeringCarrierClaiming = nil
		s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
			s.clientMutations.faults.BeforeEffectSnapshotRename = nil
			return errors.New("injected: claim write refused")
		}
	}
}

// steeringTurnOwner returns the OwningTurnID of the steering turn history
// holds for the client mutation, and whether one is there.
func steeringTurnOwner(s *Session, clientMutationID string) (owner string, delivered bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.history {
		if turn.Kind == schema.TurnSteering && turn.ClientMutationID == clientMutationID {
			owner, delivered = turn.OwningTurnID, true
		}
	}
	return owner, delivered
}

// TestRefusedLadderClaimKicksNoGoalContinuation (round 14): the ladder's
// carrier claim is refused with a goal active. The settle must not kick the
// goal's continuation -- its acceptance drains the parked steer under the
// continuation's fresh turn id -- and the goal is deferred, not dropped: the
// next external wake carries the steer under its reserved id, and the goal
// resumes at that input's settle.
func TestRefusedLadderClaimKicksNoGoalContinuation(t *testing.T) {
	adapter := newHeldLegAdapter()
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.SetGoal(context.Background(), "finish the feature"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	kicks := make(chan string, 8)
	s.SetKickFunc(func(prompt string) { kicks <- prompt })
	refuseTheNextCarrierClaimWrite(s)

	carrier, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(kicks); got != 0 {
		t.Fatalf("goal kicks after the refused claim = %d, want 0: the continuation's acceptance drains the parked steer under its own turn id", got)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model calls = %d, want 1: an autonomous turn ran behind the refused claim", got)
	}
	if _, delivered := steeringTurnOwner(s, "cm-drain-mid-leg"); delivered || !s.hasPendingUserSteering() {
		t.Fatalf("delivered=%v queued=%v, want the steer parked and queued", delivered, s.hasPendingUserSteering())
	}

	// The next external wake carries the steer under its reserved id, and the
	// goal resumes behind it.
	wakes := make(chan struct{}, 64)
	externalWake(s, wakes)
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 {
		t.Fatalf("external wake runs = %d, want 1", runs)
	}
	if owner, delivered := steeringTurnOwner(s, "cm-drain-mid-leg"); !delivered || owner != carrier {
		t.Fatalf("delivered=%v owner=%q, want the steer delivered under its receipt's %q", delivered, owner, carrier)
	}
	if got := len(kicks) + len(adapter.Requests()) - 2; got < 1 {
		t.Fatalf("no goal continuation after the external wake: kicks=%d requests=%d; the goal was dropped, not deferred", len(kicks), len(adapter.Requests()))
	}
}

// TestRefusedLadderClaimHoldsADeferredContinuation (round 14): the goal's
// continuation was armed at an earlier tail of the same input, a notification
// turn interleaved, the steer arrived during it, and the tail after it cannot
// persist the carrier claim. The wrapper must hold the deferred continuation
// rather than run it into the parked steer.
func TestRefusedLadderClaimHoldsADeferredContinuation(t *testing.T) {
	adapter := newHeldLegAdapter()
	adapter.holdCall = 2 // the notification turn's model call is the held one
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.SetGoal(context.Background(), "finish the feature"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	kicks := make(chan string, 8)
	s.SetKickFunc(func(prompt string) { kicks <- prompt })
	s.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "shell", Status: "completed", OutputBytes: 42})
	refuseTheNextCarrierClaimWrite(s)

	// Leg 1 answers at once; tail 1 folds the goal gate and runs the
	// notification, whose call is held while the queue is drained as steering.
	_, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if owner, delivered := steeringTurnOwner(s, "cm-drain-mid-leg"); delivered {
		t.Fatalf("the deferred continuation ran into the parked steer and delivered it under %q", owner)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("model calls = %d, want 2 (the leg and the notification turn): an autonomous turn ran behind the refused claim", got)
	}
	if got := len(kicks); got != 0 {
		t.Fatalf("goal kicks = %d, want 0 while steering is parked", got)
	}
	if !s.hasPendingUserSteering() {
		t.Fatal("the steer is not queued after the refused claim")
	}
}

// TestParkedSteeringRefusesADaemonNotificationTurn (round 14, #1453): a
// notification turn the daemon starts on its own wake, outside any drain
// ladder, would drain a parked steer as a passenger under the notification's
// turn id. The entry gate refuses autonomous turns while steering is parked,
// the way it refuses them while a question is pending; the notification stays
// queued and is delivered by the ladder behind the external wake's carrier.
func TestParkedSteeringRefusesADaemonNotificationTurn(t *testing.T) {
	adapter := newHeldLegAdapter()
	close(adapter.release)
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	wakes := make(chan struct{}, 64)
	externalWake(s, wakes)
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-parked",
		Input:            []appwire.InputItem{{Type: "text", Text: "parked"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	drainWakes(wakes)
	refuseTheNextCarrierClaimWrite(s)
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || ran {
		t.Fatalf("the wake with the claim refused: ran=%v err=%v, want a stand-down", ran, err)
	}
	s.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "shell", Status: "completed", OutputBytes: 42})

	// The daemon's own notification wake.
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}
	if owner, delivered := steeringTurnOwner(s, "steer-parked"); delivered {
		t.Fatalf("the daemon's notification turn delivered the parked steer under %q", owner)
	}
	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("model calls = %d, want 0: the notification turn ran while steering was parked", got)
	}
	if s.peekNotifications() != 1 {
		t.Fatalf("pending notifications = %d, want the notification kept for later", s.peekNotifications())
	}

	// The external wake carries the steer under its reserved id, and its
	// ladder delivers the notification behind it.
	externalWake(s, wakes)
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 {
		t.Fatalf("external wake runs = %d, want 1", runs)
	}
	snapshot := s.clientMutations.snapshot()
	owner, delivered := steeringTurnOwner(s, "steer-parked")
	if !delivered || owner != snapshot.Journal["steer-parked"].StableTurnID {
		t.Fatalf("delivered=%v owner=%q, want the steer delivered under its receipt's %q", delivered, owner, snapshot.Journal["steer-parked"].StableTurnID)
	}
	if s.peekNotifications() != 0 {
		t.Fatalf("pending notifications = %d after the external wake, want the ladder to have delivered it", s.peekNotifications())
	}
}

// TestUserTurnWhoseDrainFailedDoesNotReclaimTheSteerInline (round 15): a
// user turn's own boundary drain fails the steer's append; the turn goes on
// and completes normally, so the drain ladder reaches its carrier rung with
// the steer parked, queued and the rail open. The ladder must not claim the
// parked steer inline -- that is a second attempt within the input, the one
// the park exists to prevent -- and the next external wake carries it under
// its receipt's id.
func TestUserTurnWhoseDrainFailedDoesNotReclaimTheSteerInline(t *testing.T) {
	adapter := newHeldLegAdapter()
	close(adapter.release)
	s := newTestSessionForEnvctx(t, withAdapter(adapter))
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	refusal := refuseSteerAppends(s, "steer-passenger")
	refusal.refuse.Store(true)
	wakes := make(chan struct{}, 64)
	externalWake(s, wakes)
	claimed := false
	s.cfg.testOnly.steeringCarrierClaimed = func(string) { claimed = true }
	accepted, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-passenger",
		Input:            []appwire.InputItem{{Type: "text", Text: "passenger"}},
	})
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	drainWakes(wakes)

	// The user's turn drains the steer at its acceptance; the append fails.
	if _, err := s.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatalf("ProcessInput: %v (the ladder claimed the parked steer inline and the carrier failed)", err)
	}
	if claimed {
		t.Fatal("the drain ladder claimed the parked steer inline: a second attempt within the input")
	}
	if got := refusal.refusals.Load(); got != 1 {
		t.Fatalf("append attempts = %d within the input, want 1", got)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("model calls = %d, want 1 (the user's turn alone)", got)
	}
	if state := s.clientMutations.snapshot().PendingExecutions["steer-passenger"].ExecutionState; state != "accepted" || !s.hasPendingUserSteering() {
		t.Fatalf("state=%q queued=%v, want the steer parked accepted and queued", state, s.hasPendingUserSteering())
	}

	// The disk has room again; the next external wake carries the steer.
	refusal.refuse.Store(false)
	externalWake(s, wakes)
	if runs := runPendingInputOnEachWake(t, s, wakes, 5); runs != 1 {
		t.Fatalf("external wake runs = %d, want 1", runs)
	}
	if owner, delivered := steeringTurnOwner(s, "steer-passenger"); !delivered || owner != accepted.Receipt.TurnID {
		t.Fatalf("delivered=%v owner=%q, want the steer delivered under its receipt's %q", delivered, owner, accepted.Receipt.TurnID)
	}
}
