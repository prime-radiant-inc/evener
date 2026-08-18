package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// A Stop and a message queued behind it collide in an ordinary way, and the
// message has to survive it.
//
// WireState reports processing for an idle session whenever work is pending --
// a non-empty input queue counts -- so a Stop is accepted with no turn running,
// writes a fence naming the empty turn, and cancels nothing. Three things have
// to hold through that: the queue head is not claimed into a turn that is
// already dying, a claim that never incorporated its input is returned rather
// than settled, and ActiveTurnID is left empty. A turn id stranded there makes
// every later turn/start fail with "turn is already active" for the life of the
// process.

// queueOneMutation durably queues a client mutation, the way a client's
// turn/queue does, and returns its id.
func queueOneMutation(t *testing.T, sess *Session, clientMutationID, text string) {
	t.Helper()
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: clientMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: text}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue(%q): %v", clientMutationID, err)
	}
}

// TestClaimedQueuedTurnIsReturnedWhenItNeverRan covers the seam where a queued
// message can be lost. The drain loop claims the queue head, then
// processOneInput returns before incorporating anything -- a cancelled context
// is the ordinary way, and a Stop is what cancels it. The completion call that
// follows must return the claim, because the entry is already out of the input
// queue and the only other recovery for this state runs at startup.
func TestClaimedQueuedTurnIsReturnedWhenItNeverRan(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queue-behind-stop", "please still run me")

	claimed := sess.popQueueHead()
	if claimed.ClientMutationID != "queue-behind-stop" {
		t.Fatalf("popQueueHead claimed %#v, want the queued message", claimed)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got == "" {
		t.Fatal("the claim did not take an ActiveTurnID; this test is not in the state it means to be")
	}

	// The turn never incorporated its input. This is the call the drain loop
	// makes next, unconditionally, at session_lifecycle.go:653. Returning the
	// claim finalizes no fence, so it must not report a Stop settling the turn.
	stopFinalized, err := sess.completeClientMutationTurnWithState("queue-behind-stop", "terminal")
	if err != nil {
		t.Fatalf("completeClientMutationTurnWithState: %v", err)
	}
	if stopFinalized {
		t.Fatal("returning a claimed queued turn reported a Stop-finalized fence; there was no fence to finalize")
	}

	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || snapshot.InputQueue[0].ClientMutationID != "queue-behind-stop" {
		t.Fatalf("the message did not return to the queue: %#v", snapshot.InputQueue)
	}
	if record := snapshot.Journal["queue-behind-stop"]; record.OperationState == clientMutationOperationTerminal {
		t.Fatalf("a message that never ran was settled as terminal: %#v", record)
	}

	// And the stop must not strand a turn id. A non-empty ActiveTurnID naming a
	// turn that will never run makes AcceptClientMutationStart refuse every
	// later turn with "turn is already active", for the life of the process.
	if got := snapshot.ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID after the unrun turn = %q, want empty; a stranded id wedges every later turn/start", got)
	}
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "next turn"}},
	}); err != nil {
		t.Fatalf("turn/start after the unrun turn: %v", err)
	}

	// Durably back is not enough: QueueDepth, QueuePreview and WireState all
	// read the process-local queue, and wakeForPendingQueuedInput gates on that
	// same depth. A message restored only in the snapshot is invisible in the
	// queue strip and cannot wake the session that owes it a turn.
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth after the return = %d, want 1; the runtime queue is stale", got)
	}
	if preview := sess.QueuePreview(); len(preview) != 1 || preview[0] != "please still run me" {
		t.Fatalf("QueuePreview after the return = %#v, want the returned message", preview)
	}
}

// TestStopIsHonestAboutAQueuedMessage covers the state the user is actually
// looking at: no turn is running, but a queued message keeps WireState
// reporting processing, so Stop is on screen. Accepting the Stop is right --
// refusing while the UI says "working" is the failure the session-scoped rule
// exists to prevent -- but it must not cost the queued message.
func TestStopIsHonestAboutAQueuedMessage(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queue-behind-stop", "please still run me")

	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID before the stop = %q, want empty (no turn running)", got)
	}
	if got := sess.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState with a queued message = %q, want %q", got, SessionProcessing)
	}

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-with-queued-work",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 {
		t.Fatalf("the stop consumed the queued message: queue=%#v", snapshot.InputQueue)
	}
	if snapshot.InterruptFence != nil {
		t.Fatalf("the stop left a fence behind: %#v", snapshot.InterruptFence)
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("ActiveTurnID after the stop = %q, want empty", snapshot.ActiveTurnID)
	}
}

// stopHoldAdapter is the model seam for the mid-turn Stop tests. Its first
// call blocks until its context is cancelled, which is how a turn is held
// mid-model-round for a Stop to land on. Any later call is a follow-on turn
// the Stop should have prevented; it parks on releaseFollowOn so the test can
// observe the Stop RPC blocked behind it rather than racing its completion.
type stopHoldAdapter struct {
	mu               sync.Mutex
	calls            int
	firstCallRunning chan struct{}
	releaseFollowOn  chan struct{}
}

func newStopHoldAdapter() *stopHoldAdapter {
	return &stopHoldAdapter{
		firstCallRunning: make(chan struct{}),
		releaseFollowOn:  make(chan struct{}),
	}
}

func (a *stopHoldAdapter) Name() string { return "openai" }

func (a *stopHoldAdapter) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *stopHoldAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.calls++
	n := a.calls
	a.mu.Unlock()
	if n == 1 {
		close(a.firstCallRunning)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	select {
	case <-a.releaseFollowOn:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Provider: "openai", Model: req.Model, Message: llm.Assistant("done")}, nil
}

func (a *stopHoldAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

// TestStopOnMidTurnMutationParksTheQueuedMessage is the wms7 ruling for a Stop
// that cancels a running client-mutation turn with another message queued
// behind it: the interrupted turn settles, the queued message stays on the
// queue, nothing auto-starts, and the Stop RPC returns as soon as the
// interrupted turn is settled.
//
// The wiring mirrors cmd/serf/serve.go's processMessage: a per-turn context
// marked WithQueuedInputDrainOnInterruptHandler, a mutation runner whose
// cancel+done pair backs the Stop's cancelAndWait, and a nextTurnCtx factory
// that re-arms the runner for a drained turn. Before the fix, the drain loop
// completed the cancelled turn (finalizing the interrupt fence) and then
// drained the queue head anyway: the queued message ran as a follow-on turn
// under the Stop's own chain, and the Stop RPC blocked until that whole turn
// finished.
func TestStopOnMidTurnMutationParksTheQueuedMessage(t *testing.T) {
	dir := t.TempDir()
	adapter := newStopHoldAdapter()
	sess := newSession(t,
		withAdapter(adapter),
		withDir(dir),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			StateDir:         dir,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}),
	)

	// The daemon's queued-input wake, installed before anything is queued so the
	// installation itself does not fire it. The accept-time wake below counts
	// once; the Stop must not add to it ("nothing auto-starts").
	var wakeMu sync.Mutex
	wakes := 0
	sess.SetPendingUserInputWakeFunc(func() {
		wakeMu.Lock()
		wakes++
		wakeMu.Unlock()
	})
	countWakes := func() int {
		wakeMu.Lock()
		defer wakeMu.Unlock()
		return wakes
	}

	// serve.go:662-673's mutation-runner wiring, verbatim in shape. The root
	// context stands in for the daemon's serve context: alive for the whole
	// test, which is what makes the interrupt drainable at all.
	root := t.Context()
	runnerDone := make(chan struct{})
	var runnerMu sync.Mutex
	var runnerCancel context.CancelFunc
	setRunner := func(cancel context.CancelFunc) {
		runnerMu.Lock()
		runnerCancel = cancel
		runnerMu.Unlock()
	}
	cancelAndWaitMutationRunner := func() {
		runnerMu.Lock()
		cancel := runnerCancel
		runnerMu.Unlock()
		if cancel != nil {
			cancel()
		}
		<-runnerDone
	}
	var nextTurnCtx func(context.Context) (context.Context, context.CancelFunc)
	nextTurnCtx = func(rootCtx context.Context) (context.Context, context.CancelFunc) {
		drainCtx, cancelDrain := context.WithCancel(rootCtx)
		drainCtx = WithQueuedInputDrainOnInterruptHandler(drainCtx, rootCtx, nextTurnCtx)
		setRunner(cancelDrain)
		return drainCtx, cancelDrain
	}
	turnCtx, cancelTurn := context.WithCancel(root)
	defer cancelTurn()
	turnCtx = WithQueuedInputDrainOnInterruptHandler(turnCtx, root, nextTurnCtx)
	setRunner(cancelTurn)

	started, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "turn-one",
		Input:            []appwire.InputItem{{Type: "text", Text: "first message"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	runnerErr := make(chan error, 1)
	go func() {
		defer close(runnerDone)
		_, _, err := sess.ProcessClientMutationStart(turnCtx, nil)
		runnerErr <- err
	}()
	<-adapter.firstCallRunning

	// The collision: a second message queued while turn one is mid-model-call.
	queueOneMutation(t, sess, "queued-behind-stop", "run me later")
	wakesBeforeStop := countWakes()

	stopDone := make(chan struct{})
	var stopResponse appwire.TurnInterruptResponse
	var stopErr error
	go func() {
		defer close(stopDone)
		stopResponse, stopErr = sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
			ClientMutationID: "stop-mid-turn",
		}, cancelAndWaitMutationRunner)
	}()
	select {
	case <-stopDone:
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a
	// genuine hang (Stop stuck behind a follow-on turn).
	case <-time.After(30 * time.Second):
		// The Stop is stuck behind a follow-on turn. Unstick it so the runner
		// and the RPC can finish; the failure is already proven.
		close(adapter.releaseFollowOn)
		<-stopDone
		t.Fatal("Stop did not return once the interrupted turn settled; it waited out a follow-on turn run from the drain loop")
	}
	if stopErr != nil {
		t.Fatalf("InterruptClientMutation: %v", stopErr)
	}
	if stopResponse.Receipt.Disposition != appwire.MutationDispositionApplied ||
		stopResponse.Receipt.TurnID != started.Turn.ID {
		t.Fatalf("stop receipt = %#v, want applied against turn %q", stopResponse.Receipt, started.Turn.ID)
	}
	<-runnerDone
	if err := <-runnerErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted turn returned %v, want its own cancellation", err)
	}

	// No second model call: the queued message did not run under the Stop's
	// chain, and nothing auto-started it afterwards.
	if got := adapter.Calls(); got != 1 {
		t.Fatalf("model calls after the stop = %d, want 1; the drain loop ran the queued message the user just stopped", got)
	}
	if got := countWakes(); got != wakesBeforeStop {
		t.Fatalf("queued-input wakes across the stop went %d -> %d; the stop armed an auto-start for the parked message", wakesBeforeStop, got)
	}

	// The queued message is still on the queue, durably and process-locally,
	// with its accepted (not settled, not claimed) state intact.
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || snapshot.InputQueue[0].ClientMutationID != "queued-behind-stop" {
		t.Fatalf("durable queue after the stop = %#v, want the queued message parked", snapshot.InputQueue)
	}
	queuedRecord := snapshot.Journal["queued-behind-stop"]
	if queuedRecord.OperationState == clientMutationOperationTerminal || queuedRecord.ExecutionState != "accepted" {
		t.Fatalf("queued message record after the stop = %#v, want accepted and not terminal", queuedRecord)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth after the stop = %d, want 1", got)
	}

	// The interrupted turn settled, and settled clean: fence finalized, no
	// stranded turn id.
	turnOne := snapshot.Journal["turn-one"]
	if turnOne.OperationState != clientMutationOperationTerminal || turnOne.ExecutionState != "interrupted" {
		t.Fatalf("interrupted turn record = %#v, want terminal interrupted", turnOne)
	}
	if snapshot.InterruptFence != nil {
		t.Fatalf("the stop left a fence behind: %#v", snapshot.InterruptFence)
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("ActiveTurnID after the stop = %q, want empty", snapshot.ActiveTurnID)
	}
}

// TestQueueClaimRespectsAPendingInterruptFence is the narrower invariant the
// loss above depends on. AcceptClientMutationStart and claimClientMutationStart
// both refuse while an interrupt is pending; popQueueHead did not, so a Stop
// that had been accepted but not yet finalized could still have the queue head
// claimed out from under it.
func TestQueueClaimRespectsAPendingInterruptFence(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-under-fence", "do not claim me yet")
	armTestInterruptFence(t, sess, "interrupt-in-flight")

	if claimed := sess.popQueueHead(); claimed.ClientMutationID != "" {
		t.Fatalf("popQueueHead claimed %q while an interrupt was pending", claimed.ClientMutationID)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 {
		t.Fatalf("input queue depth = %d, want the message still queued", len(snapshot.InputQueue))
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("a refused claim still set ActiveTurnID = %q", snapshot.ActiveTurnID)
	}
}

// TestStopCancelsTheTurnAndKeepsUserWorkDurable pins the two halves of a Stop
// that must not regress: it actually cancels the running turn, and work the
// user already gave the session survives it durably.
//
// The steering case is the one no other test covers. A steer not yet injected
// stays durable and is still DELIVERED -- a Stop with pending steering
// restarts, and that stays true until kata 1k3m gives a parked steer somewhere
// to surface. A restart the user can stop again beats text they cannot get
// back: the rejected designs moved the user's words out of durable server
// storage and then failed to keep them safe. Queued messages, the other
// user-authored rail, park instead (kata wms7,
// TestStopParksTheQueueAgainstBothRestartRails).
func TestStopCancelsTheTurnAndKeepsUserWorkDurable(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	// A turn that is genuinely running: claimed AND incorporated, so it is past
	// the pre-turn window and into the shape a user watches a model work in.
	turnID := runningStartTurn(t, sess, "running-turn", "do the thing")

	// The user steers it, and the steer has not been injected yet -- injection
	// happens at a round boundary, so this is the state during any long tool
	// call.
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-mid-round",
		Input:            []appwire.InputItem{{Type: "text", Text: "actually do it this way"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("the steer did not land as pending user steering; this test is not in the state it means to be")
	}

	cancels := 0
	response, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-pending-steering",
	}, func() { cancels++ })
	if err != nil {
		t.Fatalf("Stop with a steer in flight was refused: %v", err)
	}

	// Half one: Stop cancelled. Reaching the cancel callback is the whole of
	// "Stop works" at this layer -- a Stop that returns Applied without getting
	// here is the lie kata 2f41 and vewa were both about.
	if cancels != 1 {
		t.Fatalf("Stop cancelled %d times, want exactly 1: it reported success without stopping the turn", cancels)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("Stop disposition = %q, want %q", response.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	if response.Receipt.TurnID != turnID {
		t.Fatalf("Stop receipt turn = %q, want the running turn %q", response.Receipt.TurnID, turnID)
	}

	// Half two: the user's words survived it. This is the property the rejected
	// designs broke -- both took the text out of durable storage and lost it, in
	// the composer's unpersisted draft, on a TUI with nowhere to put it, or by
	// nulling the journal payload.
	if !sess.hasPendingUserSteering() {
		t.Fatal("the Stop consumed the user's steering: they typed it, it never reached the model, and it is gone")
	}
}

// TestStopKeepsAQueuedMessageDurable covers the queued-message rail of the
// same promise: Stop cancels, and the message stays durably on the queue --
// parked, visible in the queue strip, payload intact -- until the user asks
// for it to run (kata wms7).
func TestStopKeepsAQueuedMessageDurable(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	runningStartTurn(t, sess, "running-turn", "do the thing")
	queueOneMutation(t, sess, "queued-behind", "and then this")

	cancels := 0
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-queued",
	}, func() { cancels++ }); err != nil {
		t.Fatalf("Stop with a queued message was refused: %v", err)
	}
	if cancels != 1 {
		t.Fatalf("Stop cancelled %d times, want exactly 1", cancels)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth after the Stop = %d, want 1: the queued message was consumed rather than kept for delivery", got)
	}
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 {
		t.Fatalf("durable input queue after the Stop = %#v, want the message still there", snapshot.InputQueue)
	}
	// And its payload is intact, which is what makes "durable" true rather than
	// merely "present": the rejected design nulled this while claiming the
	// journal was a backstop.
	if record := snapshot.Journal["queued-behind"]; len(record.Payload) == 0 {
		t.Fatalf("the queued message's payload was cleared: %#v", record)
	}
}

// TestCompletingAClaimedTurnReportsTheStopThatEndedIt closes the hole 91810a3be
// left: it taught the drain loop to park the queue head when a Stop ended the
// turn, and completeClientMutationTurnWithState reports that fact -- but only
// from its INCORPORATED branch.
//
// A turn claimed out of the queue and stopped during its PRE-TURN WORK never
// reaches that branch. It takes the earlier claimed-and-unrun path, which
// returns the entry to the queue and returns immediately, so the completion
// reported false, the drain loop heard "a bare host cancellation", and it ran
// the very message the user had just stopped. That is the turn-boundary half of
// wms7, and it is the same ruling as the mid-turn half: nothing auto-starts.
//
// The fence is still visible from the claimed branch -- the interrupt finalizes
// it AFTER the completion returns -- so the branch can report it without
// finalizing anything. That asymmetry is the whole fix.
func TestCompletingAClaimedTurnReportsTheStopThatEndedIt(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-then-stopped", "run me only when I say so")
	claimed := sess.popQueueHead()
	if claimed.ClientMutationID != "queued-then-stopped" {
		t.Fatalf("popQueueHead claimed %#v, want the queued message", claimed)
	}

	// The state a Stop leaves between acceptance and the runner's completion: a
	// fence naming the turn the drain loop just claimed.
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{
			ClientMutationID: "stop-at-the-boundary",
			ExpectedTurnID:   claimed.StableTurnID,
		}
		return nil
	}); err != nil {
		t.Fatalf("arm the fence: %v", err)
	}

	stopFinalized, err := sess.completeClientMutationTurnWithState("queued-then-stopped", "terminal")
	if err != nil {
		t.Fatalf("completeClientMutationTurnWithState: %v", err)
	}
	if !stopFinalized {
		t.Fatal("the completion did not report the Stop that ended this turn, so the drain loop will run the message the user stopped (wms7's turn-boundary half)")
	}

	// And the message is still the user's: back on the queue, payload intact,
	// exactly as the mid-turn ruling leaves it.
	snapshot := sess.clientMutations.snapshot()
	if len(snapshot.InputQueue) != 1 || snapshot.InputQueue[0].ClientMutationID != "queued-then-stopped" {
		t.Fatalf("the stopped message is not back on the queue: %#v", snapshot.InputQueue)
	}
	if record := snapshot.Journal["queued-then-stopped"]; len(record.Payload) == 0 {
		t.Fatalf("the returned message lost its payload: %#v", record)
	}
}

// A host cancellation is NOT a Stop, and must still drain. This is the other
// direction of the same predicate: without it the fix above would park the queue
// on every cancelled claim, which is the behaviour PR #74 deliberately built
// (TestDrainableInterruptClaimsQueueHeadInOneDurableTransition).
func TestCompletingAClaimedTurnReportsNoStopWhenNothingStoppedIt(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-then-cancelled", "host cancelled me")
	if claimed := sess.popQueueHead(); claimed.ClientMutationID != "queued-then-cancelled" {
		t.Fatalf("popQueueHead claimed %#v", claimed)
	}

	stopFinalized, err := sess.completeClientMutationTurnWithState("queued-then-cancelled", "terminal")
	if err != nil {
		t.Fatalf("completeClientMutationTurnWithState: %v", err)
	}
	if stopFinalized {
		t.Fatal("a completion with no interrupt fence reported a Stop, so a bare host cancellation would park the queue instead of draining it")
	}
}

func TestStopParksTheQueueAgainstBothRestartRails(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	runningStartTurn(t, sess, "running-turn", "do the thing")
	queueOneMutation(t, sess, "queued-behind", "and then this")

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-queued",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// popQueueHead is the single claim gate for BOTH rails that restart a
	// session: the drain loop in processOneInput, and ProcessPendingUserInput
	// behind the queued-input wake. Refusing here closes both.
	if claimed := sess.popQueueHead(); claimed.ClientMutationID != "" {
		t.Fatalf("the queue head was claimed after a Stop (%q): the message the user stopped will run", claimed.ClientMutationID)
	}
	// The SECOND return is "did a turn run"; the first is that turn's output
	// text, which an adapter may legitimately leave empty.
	if _, ranTurn, err := sess.ProcessPendingUserInput(context.Background(), nil); err != nil || ranTurn {
		t.Fatalf("ProcessPendingUserInput ran work after a Stop: ranTurn=%v err=%v", ranTurn, err)
	}
	// And the message is still the user's.
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth after the Stop = %d, want 1: parking must not cost the message", got)
	}
}

func TestAParkedQueueDoesNotMakeTheSessionLookBusy(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-behind", "and then this")
	// A queue with no Stop IS pending work: something will drain it.
	if got := sess.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState with a live queue = %q, want %q", got, SessionProcessing)
	}

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-queued",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Parked, so nothing will drain it, so claiming to be working is a lie --
	// and it is the lie that leaves a Stop button on screen doing nothing.
	if got := sess.WireState(); got == string(SessionProcessing) {
		t.Fatalf("WireState over a parked queue = %q: the composer keeps showing a busy session with a Stop that cannot help", got)
	}
	// The strip must still show the message.
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("QueueDepth = %d, want 1: the queue strip has nothing to show", got)
	}
}

func TestSendingAgainReleasesTheParkedQueue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release func(t *testing.T, sess *Session)
	}{
		{"turn/start", func(t *testing.T, sess *Session) {
			if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
				ClientMutationID: "next-turn",
				Input:            []appwire.InputItem{{Type: "text", Text: "carry on"}},
			}); err != nil {
				t.Fatalf("turn/start: %v", err)
			}
		}},
		// A user who queues another message has re-engaged. Without this the
		// hold is sticky: a message queued after the Stop inherits it and is
		// parked forever, with the session reporting idle and nothing saying why.
		{"turn/queue", func(t *testing.T, sess *Session) {
			queueOneMutation(t, sess, "one-more", "and this too")
		}},
		// Draining and promoting are the queue-strip's own "run this now"
		// gestures. Each gets its own table entry because each clear is an
		// independent edit an implementer can forget: without these two cases,
		// reverting either clear leaves the whole suite green while a promoted
		// message strands its siblings parked forever.
		{"turn/drainAsSteer", func(t *testing.T, sess *Session) {
			revision := sess.clientMutations.snapshot().QueueRevision
			if _, err := sess.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{
				ClientMutationID:      "drain-after-stop",
				ExpectedQueueRevision: revision,
			}); err != nil {
				t.Fatalf("turn/drainAsSteer: %v", err)
			}
		}},
		{"turn/promoteQueuedAsSteer", func(t *testing.T, sess *Session) {
			if _, err := sess.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{
				ClientMutationID: "promote-after-stop",
				Index:            0,
			}); err != nil {
				t.Fatalf("turn/promoteQueuedAsSteer: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := newQueuePersistTestSession(t, t.TempDir())
			defer sess.Close()

			queueOneMutation(t, sess, "queued-behind", "and then this")
			if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
				ClientMutationID: "stop-over-queued",
			}, func() {}); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if !sess.clientMutations.queueHeld() {
				t.Fatal("the Stop did not park the queue; this test is not in the state it means to be")
			}

			tc.release(t, sess)

			if sess.clientMutations.queueHeld() {
				t.Fatalf("%s left the queue parked: the user asked for work to run and it will not", tc.name)
			}
		})
	}
}

// The clear must sit BELOW every rejection check. executeAtomic commits a
// rejected record, so a clear above them unparks the queue on a request the
// daemon refused -- and the next wake then runs the message the user stopped.
func TestARejectedPromoteLeavesTheQueueParked(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-behind", "and then this")
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-queued",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// A promote naming an entry id the queue no longer has: the ordinary
	// "the queue changed under you" CAS refusal the token exists for.
	if _, err := sess.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{
		ClientMutationID: "promote-stale",
		Index:            0,
		ExpectedEntryID:  "queue_does_not_exist",
	}); err == nil {
		t.Fatal("the stale promote was accepted; this test needs a refusal to be meaningful")
	}
	if !sess.clientMutations.queueHeld() {
		t.Fatal("a REJECTED promote unparked the queue, so the next wake runs the message the user stopped")
	}
}

func TestQueueHeldIsReadableWithoutCloningTheSnapshot(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	if sess.clientMutations.queueHeld() {
		t.Fatal("a fresh store reports the queue held")
	}
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.QueueHeld = true
		return nil
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if !sess.clientMutations.queueHeld() {
		t.Fatal("queueHeld did not see the durable flag")
	}
}
