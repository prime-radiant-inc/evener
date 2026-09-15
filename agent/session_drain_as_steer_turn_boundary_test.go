package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
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

// steerRefusingFS refuses, forever and before a byte lands, every transcript
// write that carries the marker -- a disk that has room for everything but
// the steer. The rollback of such a write succeeds, so the writer stays
// usable and every other entry keeps landing.
type steerRefusingFS struct {
	afero.Fs
	marker []byte
	// onRefuse, when set, runs on the refusing write before it fails -- the
	// moment the steer is claimed and its append is about to fail.
	onRefuse func()
}

type steerRefusingFile struct {
	afero.File
	fs *steerRefusingFS
}

func (fs *steerRefusingFS) OpenFile(name string, flag int, mode os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	return &steerRefusingFile{File: file, fs: fs}, nil
}

func (file *steerRefusingFile) Write(p []byte) (int, error) {
	if bytes.Contains(p, file.fs.marker) {
		if file.fs.onRefuse != nil {
			file.fs.onRefuse()
		}
		return 0, errors.New("injected: no space left on device")
	}
	return file.File.Write(p)
}

func attachSteerRefusingFS(t *testing.T, s *Session, marker string) *steerRefusingFS {
	t.Helper()
	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &steerRefusingFS{Fs: afero.NewOsFs(), marker: []byte(marker)}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.attachTranscript(writer)
	return fs
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

func drainWakes(wakes <-chan struct{}) int {
	n := 0
	for {
		select {
		case <-wakes:
			n++
		default:
			return n
		}
	}
}

// inputBoundary summarizes the events of one ProcessInput call: the carrier
// openings and turn failures before its session end, and that end.
func inputBoundary(seen []events.SessionEvent, carrier string) (carrierOpenings, turnFailures int, end string) {
	for _, ev := range seen {
		switch ev.Kind {
		case events.EventSessionEnd:
			data := ev.Data.(events.SessionEndData)
			return carrierOpenings, turnFailures, data.Reason + ":" + data.State
		case events.EventTurnStarted:
			if ev.Data.(events.TurnStartedData).TurnID == carrier {
				carrierOpenings++
			}
		case events.EventError:
			turnFailures++
		}
	}
	return carrierOpenings, turnFailures, ""
}

func carrierTestConfig(dir string, claimed func(string)) SessionConfig {
	return SessionConfig{
		MaxSubagentDepth: 1,
		StateDir:         dir,
		testOnly: testConfig{
			skipGitSnapshot:        true,
			minimalSystemPrompt:    true,
			noSyncJobStore:         true,
			steeringCarrierClaimed: claimed,
		},
	}
}

// TestSteeringCarrierFailureIsNotRetriedByALaterTurnOfTheSameInput is review
// round 2's first finding on #1329: the no-reclaim guard was recomputed per
// iteration, so a queued message (or a notification, or a goal turn) running
// after the failed carrier reset it, and the iteration after that claimed the
// same steer again -- one more failed carrier per intervening turn. The
// failure is latched for the whole input; the retry stays the next wake's.
//
// The disk here refuses the steer's bytes for good, so the queued turn that
// runs after the failed carrier cannot carry it either (a queued turn drains
// steering at its start): the steer is still pending when that turn ends,
// which is exactly the state that provoked the second claim.
func TestSteeringCarrierFailureIsNotRetriedByALaterTurnOfTheSameInput(t *testing.T) {
	adapter := newHeldLegAdapter()
	var s *Session
	s = newTestSessionForEnvctx(t, withAdapter(adapter), withConfig(carrierTestConfig(t.TempDir(), func(string) {
		// A message queued while the carrier is claimed runs as the next
		// turn of this same input.
		queueOneMutation(t, s, "cm-queued-third", "third message")
	})))
	attachSteerRefusingFS(t, s, "second pass")

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

	requests := adapter.Requests()
	if len(requests) != 2 || !requestContainsText(requests[1], "third message") {
		t.Fatalf("provider requests = %d, want 2: the held leg and the queued message's turn, and no carrier's", len(requests))
	}
	if pending, ok := s.clientMutations.snapshot().PendingExecutions["cm-drain-mid-leg"]; !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("the refused steer is pending=%v state=%q, want accepted (still queued for the next wake)", ok, pending.ExecutionState)
	}
	s.Close()
	<-drained
	mu.Lock()
	openings, failures, end := inputBoundary(seen, carrier)
	transitions := boundaryTransitions(seen)
	mu.Unlock()
	if openings != 1 || failures != 1 || end != "input_complete:idle" {
		t.Fatalf("within the input: carrier openings=%d turn failures=%d session end=%q, want the carrier claimed once for the whole input (transitions: %s)",
			openings, failures, end, strings.Join(transitions, " "))
	}
}

// TestSteeringCarrierRecordedSteerSurvivesAFailedIncorporationWrite is review
// round 2's second finding on #1329. consumeSteeringMessage appends the steer
// to the transcript and then writes its incorporation to the mutation store;
// when that second write fails the entry stays claimed, and a carrier that
// read "still pending" as "undelivered" failed its turn over a steer the
// model was about to read. Only a steer back to accepted is undelivered.
func TestSteeringCarrierRecordedSteerSurvivesAFailedIncorporationWrite(t *testing.T) {
	adapter := newHeldLegAdapter()
	var s *Session
	s = newTestSessionForEnvctx(t, withAdapter(adapter), withConfig(carrierTestConfig(t.TempDir(), func(string) {
		// After the claim, the carrier's store writes are popSteeringHead's
		// claimed-mark and then finalizeIncorporatedSteering's; fail the
		// second, after the transcript append that precedes it succeeded.
		writes := 0
		s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
			writes++
			if writes == 2 {
				return errors.New("injected: incorporation write failed")
			}
			return nil
		}
	})))

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

	if pending, ok := s.clientMutations.snapshot().PendingExecutions["cm-drain-mid-leg"]; !ok || pending.ExecutionState != "claimed" {
		t.Fatalf("the steer reads pending=%v state=%q, want claimed: the fault did not land on the incorporation write, so this test is not in the state it means to be", ok, pending.ExecutionState)
	}
	requests := adapter.Requests()
	if len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("provider requests = %d carrying the steer=%v, want the carrier to run its model call over the recorded steer", len(requests), len(requests) == 2 && requestContainsText(requests[1], "second pass"))
	}
	s.Close()
	<-drained
	mu.Lock()
	openings, failures, end := inputBoundary(seen, carrier)
	transitions := boundaryTransitions(seen)
	mu.Unlock()
	if openings != 1 || failures != 0 || !strings.HasPrefix(end, "input_complete:") {
		t.Fatalf("within the input: carrier openings=%d turn failures=%d session end=%q, want one carrier, no failure, a clean completion (transitions: %s)",
			openings, failures, end, strings.Join(transitions, " "))
	}
}

// TestSteeringCarrierAppendFailureReArmsTheWake is review round 3's first
// finding on #1329. A carrier whose steer could not be recorded returns it to
// the queue and ends the input idle; the retry was "the next wake's", but
// nothing armed one. The daemon's acceptance-time wake is consumed by the
// input that just failed (or by the one retry it parks behind it), so a
// steer refused twice sat queued until an unrelated client action. The
// failure now arms its own wake, with the backoff the wake path already
// uses, so the session retries on its own.
func TestSteeringCarrierAppendFailureReArmsTheWake(t *testing.T) {
	adapter := newHeldLegAdapter()
	var s *Session
	var fs *environmentSyncFailureFS
	s = newTestSessionForEnvctx(t, withAdapter(adapter), withConfig(carrierTestConfig(t.TempDir(), func(string) {
		fs.mu.Lock()
		fs.writeFailure = errors.New("injected: no space left on device")
		fs.transferBeforeWriteFailure = 0
		fs.mu.Unlock()
	})))
	fs = attachEnvironmentFailureFS(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	wakes := countingUserInputWake(s)

	_, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	drainWakes(wakes) // the acceptance-time wake, consumed by the input that failed

	select {
	case <-wakes:
	// TRIPWIRE: the first backoff is jobNotificationRetryInitialDelay (250ms); only a wake never armed gets here.
	case <-time.After(5 * time.Second):
		t.Fatal("no wake was armed for the returned steer: an idle session leaves it queued until an unrelated client action")
	}
	// The wake's retry delivers (the fault was one-shot).
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("the re-armed wake's retry: ran=%v err=%v", ran, err)
	}
	if requests := adapter.Requests(); len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("the retry made %d request(s) in total and did not carry the steer", len(requests))
	}
}

// TestStopWhileTheCarrierIsAppendingParksTheReturnedSteer is review round 3's
// second finding on #1329. popSteeringHead marks the steer claimed before
// its append; a Stop landing there names the carrier and, seeing a claimed
// steer whose id is the turn it is cancelling, arms no hold (that steer is
// the turn, and would be gone once its append finalizes). When the append
// then fails, the steer comes back to accepted and round 1's finalization
// preserved it -- unparked, so the wake at the end of the Stop delivered
// the very steer the user had just stopped. Preserving it now parks it.
func TestStopWhileTheCarrierIsAppendingParksTheReturnedSteer(t *testing.T) {
	adapter := newHeldLegAdapter()
	var s *Session
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processDone := make(chan struct{})
	interruptDone := make(chan error, 1)
	s = newTestSessionForEnvctx(t, withAdapter(adapter), withConfig(carrierTestConfig(t.TempDir(), nil)))
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	fs := attachSteerRefusingFS(t, s, "second pass")
	var stopOnce sync.Once
	fs.onRefuse = func() {
		// The Stop lands while the steer is claimed and its append is in
		// flight: serve.go's cancel, wait for the runner, then finalize.
		stopOnce.Do(func() {
			cancelled := make(chan struct{})
			go func() {
				_, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
					ClientMutationID: "stop-while-appending",
				}, func() {
					cancel()
					close(cancelled)
					<-processDone
				})
				interruptDone <- err
			}()
			<-cancelled
		})
	}
	wakes := countingUserInputWake(s)

	_, done := drainMidHeldLeg(ctx, t, s, adapter)
	close(adapter.release)
	awaitInput(t, done)
	close(processDone)
	if err := <-interruptDone; err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}
	drainWakes(wakes)

	snapshot := s.clientMutations.snapshot()
	if pending, ok := snapshot.PendingExecutions["cm-drain-mid-leg"]; !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("the returned steer is pending=%v state=%q, want accepted", ok, pending.ExecutionState)
	}
	if !snapshot.SteeringHeld {
		t.Fatal("the Stop's finalization preserved the returned steer unparked: the wake will deliver what the user just stopped")
	}
	select {
	case <-wakes:
		t.Fatal("a wake fired for the steer the Stop parked")
	// TRIPWIRE: the backoff re-arm is 250ms; a second is far past it and only bounds the wait.
	case <-time.After(time.Second):
	}
	if id, ok := s.claimSteeringCarrierTurn(); ok {
		t.Fatalf("claimSteeringCarrierTurn claimed %q while the steer is parked", id)
	}

	// The user's next run releases the hold and carries it (the disk has
	// room again).
	fs.marker = []byte("<<nothing carries this marker>>")
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
		t.Fatalf("the user's next run made %d request(s) and did not carry the parked steer", len(requests))
	}
}

// TestRestoreReleasesACarrierClaimThatNeverRan is #1342, folded in by review
// round 4 of #1329. claimSteeringCarrierTurn publishes ActiveTurnID = the
// steer's reserved id durably, before the carrier turn opens and takes the
// steer. A process death in that window leaves the slot named by a steer
// that is still accepted; forgetRunningTurnNoOneOwns kept it because a
// pending execution names it, and every later carrier claim and turn/start
// was refused for the life of the session. A steering mutation's id in the
// slot at load is a carrier that never ran to incorporation: the slot is
// released and the steer, still pending, is carried by the next run.
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
	carrier, ok := crashed.claimSteeringCarrierTurn()
	if !ok {
		t.Fatal("the steer could not claim a carrier turn; this test is not in the state it means to be")
	}
	// The process dies here: the claim is durable, the carrier never opened.
	crashed.Close()

	// The resume path `evener serve --resume` takes, with a scripted model so
	// the run below can complete.
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
	defer restored.Close()
	serveSession(t, restored)
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
		t.Fatalf("the steer that owned carrier %s is still pending after the user's run; want it carried", carrier)
	}
}

// TestSteeringCarrierKeepsTheSteerReachableWhenTheClaimReturnFailsToo is
// review round 4's Medium on #1329. The append fails, and returning the
// claim fails as well: the steer sits claimed in the store, which no wake
// materializes, and a carrier that read "claimed" as recorded went on to
// its model request without it. The carrier now confirms the steer is in
// the transcript before treating a claimed entry as delivered, and the
// retry recovers a claimed steer the transcript does not hold.
func TestSteeringCarrierKeepsTheSteerReachableWhenTheClaimReturnFailsToo(t *testing.T) {
	adapter := newHeldLegAdapter()
	var s *Session
	s = newTestSessionForEnvctx(t, withAdapter(adapter), withConfig(carrierTestConfig(t.TempDir(), func(string) {
		// After the claim: popSteeringHead's claimed-mark lands, the append
		// is refused below, and the claim's return is the second store
		// write -- refused too.
		writes := 0
		s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
			writes++
			if writes == 2 {
				return errors.New("injected: claim return refused")
			}
			return nil
		}
	})))
	fs := attachSteerRefusingFS(t, s, "second pass")
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	wakes := countingUserInputWake(s)

	_, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider requests = %d, want 1: the carrier made a model request without the steer it exists to carry", got)
	}
	if state := s.clientMutations.snapshot().PendingExecutions["cm-drain-mid-leg"].ExecutionState; state != "claimed" {
		t.Fatalf("the steer reads %q, want claimed: the claim's return was not refused, so this test is not in the state it means to be", state)
	}
	drainWakes(wakes)

	// Storage recovers (the store fault above fires on its second write
	// only); the retry must still be able to reach the steer.
	fs.marker = []byte("<<nothing carries this marker>>")
	select {
	case <-wakes:
	// TRIPWIRE: the first backoff is 250ms; only a retry never armed, or one that cannot see the steer, gets here.
	case <-time.After(5 * time.Second):
		t.Fatal("no wake reached the claimed steer: it is invisible to the retry")
	}
	if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("the retry: ran=%v err=%v, want it to carry the recovered steer", ran, err)
	}
	if requests := adapter.Requests(); len(requests) != 2 || !requestContainsText(requests[1], "second pass") {
		t.Fatalf("the retry made %d request(s) in total and did not carry the steer", len(requests))
	}
}

// TestAnyRecordedSteerResetsTheCarrierRetryBudget is review round 4's Low on
// #1329: the backoff was cleared only when a carrier turn recorded its
// steer. A queued turn draining the same steer left the spent attempt on the
// books for the next, unrelated carrier episode.
func TestAnyRecordedSteerResetsTheCarrierRetryBudget(t *testing.T) {
	adapter := newHeldLegAdapter()
	var s *Session
	var fs *environmentSyncFailureFS
	s = newTestSessionForEnvctx(t, withAdapter(adapter), withConfig(carrierTestConfig(t.TempDir(), func(string) {
		// One refused append for the carrier, and a message queued behind
		// it whose turn drains the steer successfully.
		fs.mu.Lock()
		fs.writeFailure = errors.New("injected: no space left on device")
		fs.transferBeforeWriteFailure = 0
		fs.mu.Unlock()
		queueOneMutation(t, s, "cm-queued-third", "third message")
	})))
	fs = attachEnvironmentFailureFS(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	_, done := drainMidHeldLeg(context.Background(), t, s, adapter)
	close(adapter.release)
	if err := awaitInput(t, done); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 2 || !requestContainsText(requests[1], "second pass") || !requestContainsText(requests[1], "third message") {
		t.Fatalf("provider requests = %d; want the queued turn to have carried the steer (this test is not in the state it means to be otherwise)", len(requests))
	}
	s.steeringRetryMu.Lock()
	attempts := s.steeringCarrierRetry.attempts
	s.steeringRetryMu.Unlock()
	if attempts != 0 {
		t.Fatalf("carrier retry attempts = %d after the steer was recorded by the queued turn, want 0: the next carrier episode would inherit a spent budget", attempts)
	}
}

// ---- review round 5: the undelivered-steer table ----

// TestStopWithFailedAppendAndFailedReturnParksTheSteer is round 5's High: a
// Stop crossing a carrier whose append fails AND whose claim return fails
// found the steer claimed under the cancelled turn's id, and the retirement
// path discarded it -- gone from the store, the queue and the transcript.
func TestStopWithFailedAppendAndFailedReturnParksTheSteer(t *testing.T) {
	adapter := newHeldLegAdapter()
	var s *Session
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	processDone := make(chan struct{})
	interruptDone := make(chan error, 1)
	s = newTestSessionForEnvctx(t, withAdapter(adapter), withConfig(carrierTestConfig(t.TempDir(), func(string) {
		// popSteeringHead's claimed-mark is the first store write after the
		// claim; the claim's return after the refused append is the second.
		writes := 0
		s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
			writes++
			if writes == 2 {
				return errors.New("injected: claim return refused")
			}
			return nil
		}
	})))
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	fs := attachSteerRefusingFS(t, s, "second pass")
	var stopOnce sync.Once
	fs.onRefuse = func() {
		stopOnce.Do(func() {
			cancelled := make(chan struct{})
			go func() {
				_, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
					ClientMutationID: "stop-over-double-failure",
				}, func() {
					cancel()
					close(cancelled)
					<-processDone
				})
				interruptDone <- err
			}()
			<-cancelled
		})
	}

	_, done := drainMidHeldLeg(ctx, t, s, adapter)
	close(adapter.release)
	awaitInput(t, done)
	close(processDone)
	if err := <-interruptDone; err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}

	snapshot := s.clientMutations.snapshot()
	pending, ok := snapshot.PendingExecutions["cm-drain-mid-leg"]
	if !ok {
		t.Fatalf("the steer was discarded by the Stop's finalization (journal=%+v); the user's Applied message is gone from store, queue and transcript", snapshot.Journal["cm-drain-mid-leg"].ExecutionState)
	}
	if pending.ExecutionState != "accepted" || !snapshot.SteeringHeld {
		t.Fatalf("the steer reads %q held=%v, want accepted and parked", pending.ExecutionState, snapshot.SteeringHeld)
	}
	if !s.hasPendingUserSteering() {
		t.Fatal("the parked steer has no in-memory copy")
	}
}

// TestRetryLeavesAnInFlightAppendAlone is round 5's first Medium: the retry
// timer firing while a turn has claimed a steer and is appending it returned
// that steer to accepted and re-materialized it, so the turn's own finalize
// then left a stale head in the in-memory queue that popSteeringHead could
// never pop ("not pending"), blocking every steer behind it.
func TestRetryLeavesAnInFlightAppendAlone(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	runningStartTurn(t, s, "running-turn", "do the thing")
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-in-flight",
		Input:            []appwire.InputItem{{Type: "text", Text: "mid-append"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	// The turn is running and has popped the steer: claimed, append in flight.
	s.mu.Lock()
	s.state = SessionProcessing
	s.mu.Unlock()
	if _, ok := s.popSteeringHead(); !ok {
		t.Fatal("popSteeringHead claimed nothing; this test is not in the window it means to be")
	}

	clk := agenttest.NewFakeClockAt(time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC))
	s.clock = clk
	s.scheduleSteeringCarrierRetry()
	// The retry fires on virtual time; Drain returns once its callback --
	// the reconciliation -- has run to completion.
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()

	if state := s.clientMutations.snapshot().PendingExecutions["steer-in-flight"].ExecutionState; state != "claimed" {
		t.Fatalf("the retry returned an in-flight steer to %q; the turn appending it will finalize a record the queue no longer agrees with", state)
	}
	if s.hasPendingUserSteering() {
		t.Fatal("the retry re-materialized an in-flight steer into the queue: a stale head the turn's finalize leaves behind")
	}
}

// TestFailedRecoveryReArmsTheRetry is round 5's second Medium: recovering a
// claimed steer whose store write fails was warned about and forgotten; the
// steer stayed claimed and invisible until a restart.
func TestFailedRecoveryReArmsTheRetry(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-unrecoverable",
		Input:            []appwire.InputItem{{Type: "text", Text: "recover me"}},
	}); err != nil {
		t.Fatalf("steer: %v", err)
	}
	// Claimed, never appended, no turn running: the recovery case.
	if _, ok := s.popSteeringHead(); !ok {
		t.Fatal("popSteeringHead claimed nothing; this test is not in the state it means to be")
	}
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		return errors.New("injected: store refuses every write")
	}

	clk := agenttest.NewFakeClockAt(time.Date(2026, 9, 15, 9, 0, 0, 0, time.UTC))
	s.clock = clk
	s.scheduleSteeringCarrierRetry()
	// The retry fires on virtual time; Drain returns once its callback -- the
	// reconciliation whose write fails -- has run to completion.
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()

	s.steeringRetryMu.Lock()
	attempts := s.steeringCarrierRetry.attempts
	s.steeringRetryMu.Unlock()
	if attempts < 2 {
		t.Fatalf("carrier retry attempts = %d after a recovery whose write failed, want it re-armed (>= 2): the steer stays claimed and invisible otherwise", attempts)
	}
}

// TestFailedCarrierClaimReArmsTheRetryButAHeldRailDoesNot is round 5's third
// Medium: a claim refused by the store on a retry wake stood down like a
// benign race, and the already-consumed wake left the steer stranded. A held
// rail is the benign case and must stay a no-op.
func TestFailedCarrierClaimReArmsTheRetryButAHeldRailDoesNot(t *testing.T) {
	attemptsOf := func(s *Session) int {
		s.steeringRetryMu.Lock()
		defer s.steeringRetryMu.Unlock()
		return s.steeringCarrierRetry.attempts
	}
	t.Run("store refuses the claim", func(t *testing.T) {
		s := newQueuePersistTestSession(t, t.TempDir())
		defer s.Close()
		serveSession(t, s)
		if err := s.ensureClientMutationStore(); err != nil {
			t.Fatalf("ensureClientMutationStore: %v", err)
		}
		if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
			ClientMutationID: "steer-claim-refused",
			Input:            []appwire.InputItem{{Type: "text", Text: "claim me"}},
		}); err != nil {
			t.Fatalf("steer: %v", err)
		}
		s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
			return errors.New("injected: store refuses the claim")
		}
		if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || ran {
			t.Fatalf("wake with a refused claim: ran=%v err=%v, want a stand-down", ran, err)
		}
		if got := attemptsOf(s); got != 1 {
			t.Fatalf("carrier retry attempts = %d after the store refused the claim, want 1: the consumed wake leaves the steer stranded otherwise", got)
		}
	})
	t.Run("held rail", func(t *testing.T) {
		s := newQueuePersistTestSession(t, t.TempDir())
		defer s.Close()
		serveSession(t, s)
		if err := s.ensureClientMutationStore(); err != nil {
			t.Fatalf("ensureClientMutationStore: %v", err)
		}
		if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
			ClientMutationID: "steer-parked",
			Input:            []appwire.InputItem{{Type: "text", Text: "parked"}},
		}); err != nil {
			t.Fatalf("steer: %v", err)
		}
		if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
			snapshot.SteeringHeld = true
			return nil
		}); err != nil {
			t.Fatalf("park: %v", err)
		}
		if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || ran {
			t.Fatalf("wake against a held rail: ran=%v err=%v, want a stand-down", ran, err)
		}
		if got := attemptsOf(s); got != 0 {
			t.Fatalf("carrier retry attempts = %d for a held rail, want 0: a parked steer waits for the user, not a timer", got)
		}
	})
}

// ---- review round 6: rows the table was missing ----

// recordSteerWithFailedIncorporation takes the steer at the head of the queue
// through popSteeringHead and consumeSteeringMessage with the store refusing
// the incorporation write: the transcript holds the steer, the store still
// reads claimed. The fault is one-shot.
func recordSteerWithFailedIncorporation(t *testing.T, s *Session, clientMutationID string) {
	t.Helper()
	msg, ok := s.popSteeringHead()
	if !ok || msg.ClientMutationID != clientMutationID {
		t.Fatalf("popSteeringHead claimed %q (ok=%v), want %q", msg.ClientMutationID, ok, clientMutationID)
	}
	s.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		s.clientMutations.faults.BeforeEffectSnapshotRename = nil
		return errors.New("injected: incorporation write refused")
	}
	if !s.consumeSteeringMessage(msg) {
		t.Fatal("consumeSteeringMessage reported the append failed; this test wants it recorded")
	}
	if state := s.clientMutations.snapshot().PendingExecutions[clientMutationID].ExecutionState; state != "claimed" {
		t.Fatalf("the recorded steer reads %q, want claimed (the fault did not land on the incorporation write)", state)
	}
	if !s.steeringRecorded(clientMutationID) {
		t.Fatal("the transcript does not hold the steer; this test is not in the state it means to be")
	}
}

// TestRestoreReleasesTheSlotOfACarrierWhoseSteerItFinalizes is round 6's
// High: the carrier recorded its steer, the process died before the
// incorporation write, and restore's row 6 finalized the steer -- removing
// it from the order -- before the slot rule asked whether the order named
// the active turn. It no longer did, so the carrier's claim outlived the
// steer and every later turn/start was refused.
func TestRestoreReleasesTheSlotOfACarrierWhoseSteerItFinalizes(t *testing.T) {
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

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	serveSession(t, restored)
	if _, still := restored.clientMutations.snapshot().PendingExecutions["cm-recorded-carrier"]; still {
		t.Fatal("restore did not finalize the recorded steer; this test is not in the state it means to be")
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after restore finalized the carrier's steer, want released: every later turn/start is refused", got)
	}
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-start-after-restart",
		Input:            []appwire.InputItem{{Type: "text", Text: "hello again"}},
	}); err != nil {
		t.Fatalf("turn/start after the restart: %v", err)
	}
}

// TestStopReleasesAHoldTheLastSteerLeavesBehind is round 6's second Medium:
// the Stop armed a hold for a claimed passenger steer, finalization's row 6
// then incorporated that steer (recorded, incorporation write failed), and
// the hold stayed armed naming nothing -- the #710 shape: the next steer is
// accepted and silently parked.
func TestStopReleasesAHoldTheLastSteerLeavesBehind(t *testing.T) {
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
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	snapshot := s.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["steer-recorded-inline"]; still {
		t.Fatal("finalization did not incorporate the recorded steer; this test is not in the state it means to be")
	}
	if snapshot.SteeringHeld {
		t.Fatal("the hold outlived the last steer: the next steer is accepted and silently parked (#710)")
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

// TestReleaseRetryReRunsTheTableAndWakes is round 6's third Medium: a slot
// left behind by a release write the store refused reads as a turn in
// flight to the carrier retry, which leaves the steer alone and arms
// nothing; the release retry then lands and told nobody, so the steer sat
// until an unrelated client action. The release retry now re-runs the table
// and wakes.
func TestReleaseRetryReRunsTheTableAndWakes(t *testing.T) {
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

	// The release retry lands.
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

// TestInjectDrainedSteeringStopsAtTheFirstFailedAppend is round 6's fourth
// Medium: the drain loop ignored consumeSteeringMessage's false, and since
// the table had just put the failed steer back at the head of the queue,
// the next iteration popped the same steer again -- one retry attempt per
// peeked message, inside one turn, without the backoff.
func TestInjectDrainedSteeringStopsAtTheFirstFailedAppend(t *testing.T) {
	s := newQueuePersistTestSession(t, t.TempDir())
	defer s.Close()
	serveSession(t, s)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	attachSteerRefusingFS(t, s, "first steer")
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

	s.steeringRetryMu.Lock()
	attempts := s.steeringCarrierRetry.attempts
	s.steeringRetryMu.Unlock()
	if attempts != 1 {
		t.Fatalf("carrier retry attempts = %d after one drain, want 1: the loop re-popped the failed steer", attempts)
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
