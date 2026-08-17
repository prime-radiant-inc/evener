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

// TestStopWakesTheSessionForWorkItLeftQueued closes the strand the fence guard
// would otherwise create. popQueueHead declines while an interrupt is pending,
// and finalize then clears the fence without scheduling anything -- so a Stop
// with a message queued behind it would leave the session reporting itself as
// working with a queue nobody drains.
func TestStopWakesTheSessionForWorkItLeftQueued(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	queueOneMutation(t, sess, "queued-through-stop", "run me after the stop")

	var mu sync.Mutex
	notifies := 0
	sess.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})
	mu.Lock()
	before := notifies
	mu.Unlock()

	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-queued-work",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifies == before {
		t.Fatal("the stop left work queued and woke nothing; the session reports itself active with a queue nobody drains")
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
	case <-time.After(10 * time.Second):
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
