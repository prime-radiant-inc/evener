package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// heldLegAdapter parks the FIRST model call until the test releases it and
// answers every call with a terminal communicate. It is the skill guard's
// provider "hold" in miniature: a turn whose in-flight leg the test can drain
// a queue into before the leg returns.
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

	var mu sync.Mutex
	var seen []events.SessionEvent
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	}, func() { close(drained) })
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.ProcessInput(context.Background(), "open a long turn for the queue", nil)
		done <- err
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
	close(adapter.release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
	// TRIPWIRE: both legs answer from the scripted adapter; only a genuine hang gets here.
	case <-time.After(10 * time.Second):
		t.Fatal("ProcessInput never returned")
	}

	// Join the event stream before reading it: ProcessInput returns before the
	// consumer necessarily saw its last events.
	s.Close()
	<-drained
	mu.Lock()
	transitions := boundaryTransitions(seen)
	mu.Unlock()

	requests := adapter.Requests()
	if len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("ProcessInput returned after %d model call(s) with the drained steering leg undispatched; the session settled idle mid-turn (transitions: %s)",
			len(requests), strings.Join(transitions, " "))
	}

	carrierOpened := -1
	var sessionEnds []int
	for i, tr := range transitions {
		switch {
		case tr == "turn_started:"+drain.Receipt.TurnID:
			carrierOpened = i
		case strings.HasPrefix(tr, "session_end:"):
			sessionEnds = append(sessionEnds, i)
		}
	}
	if carrierOpened < 0 {
		t.Fatalf("the drain's turn %s never opened (transitions: %s)", drain.Receipt.TurnID, strings.Join(transitions, " "))
	}
	if len(sessionEnds) != 1 || sessionEnds[0] < carrierOpened {
		t.Fatalf("the input ended before its drained steering leg ran; want exactly one session end after %s opened (transitions: %s)",
			drain.Receipt.TurnID, strings.Join(transitions, " "))
	}
	// idle or awaiting is settleTerminalState's call; what matters here is that
	// the one session end is the clean completion of BOTH legs.
	if got := transitions[sessionEnds[0]]; !strings.HasPrefix(got, "session_end:input_complete:") {
		t.Fatalf("session end = %s, want input_complete once both legs ran", got)
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

// TestStopInTheCarrierClaimWindowParksTheSteerForTheNextUserRun is review
// round 1's first finding on #1329. The drain ladder claims the carrier
// (ActiveTurnID = the steer's reserved id) and only then opens the turn that
// takes the steer. A Stop landing between those two names the carrier, and
// the interrupt's finalization then swept every pending execution naming
// that turn -- the untaken steer included -- out of the durable store while
// its order entry, its in-memory copy and the hold the Stop armed for it all
// stayed: a steer nothing could deliver and nothing could clear.
//
// The contract: a steer the cancelled carrier never took is a message the
// user still owes a run, exactly like a queued message a Stop returns to the
// queue (wms7). It stays accepted, parked behind the steering hold, and the
// user's next run carries it.
func TestStopInTheCarrierClaimWindowParksTheSteerForTheNextUserRun(t *testing.T) {
	adapter := newHeldLegAdapter()
	var s *Session
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processDone := make(chan struct{})
	interruptDone := make(chan error, 1)
	cancelled := make(chan struct{})
	s = newTestSessionForEnvctx(t, withAdapter(adapter), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		StateDir:         t.TempDir(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			steeringCarrierClaimed: func(string) {
				// The daemon's Stop: cancel the running input, wait for the
				// runner to unwind, then finalize (cmd/evener/serve.go's
				// cancelAndWait). The ladder resumes once the cancel landed.
				go func() {
					_, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
						ClientMutationID: "stop-in-the-claim-window",
					}, func() {
						cancel()
						close(cancelled)
						<-processDone
					})
					interruptDone <- err
				}()
				<-cancelled
			},
		},
	}))
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	carrier, done := drainMidHeldLeg(ctx, t, s, adapter)
	close(adapter.release)
	err := awaitInput(t, done)
	close(processDone)
	if err == nil {
		t.Fatal("ProcessInput returned nil after the Stop; this test is not in the window it means to be")
	}
	if err := <-interruptDone; err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}

	snapshot := s.clientMutations.snapshot()
	pending, ok := snapshot.PendingExecutions["cm-drain-mid-leg"]
	if !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("the steer the cancelled carrier never took is pending=%v state=%q, want accepted (back in the queue)", ok, pending.ExecutionState)
	}
	if !snapshot.SteeringHeld {
		t.Fatal("the Stop parked nothing: the returned steer would be delivered by the next wake, the very run the user just stopped")
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

	// The user's next run releases the hold and carries the steer.
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "carry on"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if _, _, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil {
		t.Fatalf("ProcessClientMutationStart: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("the user's next run made %d request(s) and did not carry the parked steer (want the second request to carry %q)", len(requests), "second pass")
	}
	if _, still := s.clientMutations.snapshot().PendingExecutions["cm-drain-mid-leg"]; still || s.clientMutations.steeringHeld() {
		t.Fatalf("after the run: steer still pending=%v held=%v, want delivered and released", still, s.clientMutations.steeringHeld())
	}
	_ = carrier
}

// TestSteeringCarrierAppendFailureRunsOnceAndReturnsTheSteer is review round
// 1's second finding on #1329. When the carrier's durable steering append
// fails without poisoning the writer (a zero-byte write the rollback cleans
// up), consumeSteeringMessage returns the claim to accepted and the carrier
// used to carry on as if it had delivered: a model request carrying nothing,
// a clean completion, and a drain ladder that saw an accepted steer and
// claimed it again -- a loop of model turns bounded only by the writer
// eventually poisoning.
//
// The contract: the carrier reports that it delivered nothing (its announced
// turn fails, no model request is made), the input settles idle with the
// steer back in the queue, and the next wake -- not this input -- retries.
func TestSteeringCarrierAppendFailureRunsOnceAndReturnsTheSteer(t *testing.T) {
	adapter := newHeldLegAdapter()
	var s *Session
	var fs *environmentSyncFailureFS
	s = newTestSessionForEnvctx(t, withAdapter(adapter), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		StateDir:         t.TempDir(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			steeringCarrierClaimed: func(string) {
				// The next durable append is the carrier's steering entry:
				// fail it before a byte lands, so the rollback succeeds and
				// the writer stays usable.
				fs.mu.Lock()
				fs.writeFailure = errors.New("injected: no space left on device")
				fs.transferBeforeWriteFailure = 0
				fs.mu.Unlock()
			},
		},
	}))
	fs = attachEnvironmentFailureFS(t, s)

	var mu sync.Mutex
	var seen []events.SessionEvent
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	}, func() { close(drained) })
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	carrier, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider requests = %d, want 1: a carrier whose steer was not recorded must not make a model request, let alone keep making them", got)
	}
	if s.attachedTranscript().Poisoned() {
		t.Fatal("the writer is poisoned; this test is not the non-poisoning failure it means to be")
	}
	snapshot := s.clientMutations.snapshot()
	pending, ok := snapshot.PendingExecutions["cm-drain-mid-leg"]
	if !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("the undelivered steer is pending=%v state=%q, want accepted (back in the queue)", ok, pending.ExecutionState)
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("ActiveTurnID = %q after the input, want released", snapshot.ActiveTurnID)
	}
	if got := s.State(); got != SessionIdle {
		t.Fatalf("session state = %q after the input, want idle: the steer is runnable work the wake will carry, not the user's turn", got)
	}
	// The retry is the next wake's, and it delivers.
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("the wake after the failed carrier: ran=%v err=%v, want it to carry the returned steer", ran, err)
	}
	if requests := adapter.Requests(); len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("the wake made %d request(s) in total and did not carry %q", len(requests), "second pass")
	}
	if _, still := s.clientMutations.snapshot().PendingExecutions["cm-drain-mid-leg"]; still {
		t.Fatal("the steer is still pending after the wake delivered it")
	}

	s.Close()
	<-drained
	mu.Lock()
	transitions := boundaryTransitions(seen)
	// The input's own events end at its session end; what follows is the
	// wake's retry above.
	carrierOpenings, turnFailures := 0, 0
	inputEnd := ""
	for _, ev := range seen {
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
	mu.Unlock()
	if carrierOpenings != 1 || turnFailures != 1 || inputEnd != "input_complete:idle" {
		t.Fatalf("within the input: carrier openings=%d turn failures=%d session end=%q, want one carrier turn, failed once, ending idle (transitions: %s)",
			carrierOpenings, turnFailures, inputEnd, strings.Join(transitions, " "))
	}
}
