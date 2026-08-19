package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// The rule under test: control mutations do not name turns. Steer, queue, stop,
// drain and promote carry no expectedTurnId at any layer, because what such a
// field asserts -- "the session is still in the state I saw" -- is not what any
// of those buttons means, and asserting it can only ever turn a success into a
// refusal. The preconditions that remain are the ones that name a real object:
// clientMutationId everywhere, expectedQueueRevision on drain, expectedEntryId
// on promote and cancel. Stop's precondition is the session's wire state.
//
// Everything here runs against a real evener-hub process, the real evener daemon it
// spawns, and the real AppWire socket, because that is the only place the rule
// exists as one piece. The rule spans a Go type, a hand-written validator list,
// three wire layers and a TypeScript client, and neither compiler can see
// across that seam: a client omitting a field a daemon still requires type-checks
// on both sides and fails only on the wire.
//
// The provider is fakellm -- a scripted HTTP provider, not a stub of anything
// evener owns -- so a turn stays in flight for exactly as long as a test declines
// to answer the model call. No credential, no network, no pacing prompt.

// TestE2E_StopCancelsWhateverIsRunningAndNamesIt is the first half of the rule:
// a client that cannot name the running turn -- every client, now -- can still
// stop it, and learns from the receipt what it stopped.
//
// The three claims are separable, and each has its own way of failing:
// the mutation is accepted (a restored expectedTurnId precondition refuses it),
// the receipt names the turn actually cancelled (a fence that recorded no
// target returns an empty id), and the session really stops (a receipt is not
// evidence -- the turn is held at the model call, so only cancellation can end
// it, and the durable transcript is where that shows up).
func TestE2E_StopCancelsWhateverIsRunningAndNamesIt(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)
	ref := startLiveThread(ctx, t, client, stack, "EVENER-E2E-STOP-UNNAMED-OPENING")

	// Hold round 1 open. The turn can now end only by cancellation.
	if _, err := provider.Next(ctx.Done()); err != nil {
		t.Fatalf("waiting for the session's first model request: %v", err)
	}
	running := awaitActiveTurn(ctx, t, client, ref, "")

	receipt, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	if err != nil {
		t.Fatalf("turn/interrupt carrying no turn id was refused against running turn %q: %v", running, err)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/interrupt disposition = %q, want %q", receipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	if receipt.Receipt.TurnID != running {
		t.Fatalf("the receipt names turn %q, but the session was running %q: a client that did not name the turn learns nothing about what it stopped",
			receipt.Receipt.TurnID, running)
	}

	awaitThread(ctx, t, client, ref, "the session to stop", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Status.Type != appwire.ThreadStatusActive
	})
	awaitTurnStatus(ctx, t, client, ref, running, "interrupted")
}

// TestE2E_ASendThatRacedAStopStillRuns is the second half of the rule, in the
// only form the live stack can actually hold still.
//
// Dropping expectedTurnId made turn/queue acceptable against a session with no
// running turn, which is what a Send that raced a Stop becomes: the Stop settles
// the session IDLE (an interrupted turn clears the pending-ask set, unlike a
// turn that ends by communicating) and the follow-up the client had already
// dispatched arrives with nothing running to consume it. Accepting it is right.
// Accepting it and then never running it is a message the user watched leave
// the composer and never saw again -- the same silent loss as a refused Stop,
// wearing an Applied receipt.
//
// So the assertion is at the model boundary, not the receipt: the queued text
// has to show up in a real model request. And the Stop aimed at the turn that
// wakes has to land, because a session woken by a message the user is already
// regretting is exactly when Stop gets pressed again.
//
// What this test deliberately does NOT claim: that the Stop lands while the
// durable store still names no turn. That window is real -- Session.WireState
// reports an idle session with queued work as processing, and Stop's
// precondition reads that rather than State() -- but it is at most a few
// milliseconds wide from a client, because accepting the queue now wakes the
// session immediately. Measured across all three wake paths (queued input, goal
// continuation, job notification) the daemon publishes a durable turn id within
// ~10ms, so no client-driven fixture can hold it open. That precondition is
// pinned where it can be: agent's TestStopIsHonestAboutAQueuedMessage.
func TestE2E_ASendThatRacedAStopStillRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)
	ref := startLiveThread(ctx, t, client, stack, "EVENER-E2E-RACED-SEND-OPENING")

	const followUpText = "EVENER-E2E-RACED-SEND-FOLLOWUP"

	if _, err := provider.Next(ctx.Done()); err != nil {
		t.Fatalf("waiting for the session's first model request: %v", err)
	}
	stopped := awaitActiveTurn(ctx, t, client, ref, "")

	if _, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	}); err != nil {
		t.Fatalf("turn/interrupt against running turn %q: %v", stopped, err)
	}
	awaitThread(ctx, t, client, ref, "the interrupted session to settle", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Status.Type != appwire.ThreadStatusActive
	})

	// The Send that was already in flight when the user pressed Stop. The
	// composer routes it to turn/queue because it believed a turn was running,
	// and the daemon no longer refuses that just because the turn has ended.
	receipt, err := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: followUpText}},
	})
	if err != nil {
		t.Fatalf("turn/queue against a session whose turn had just been stopped: %v", err)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/queue disposition = %q, want %q", receipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}

	// Bounded well inside the test's deadline: a message nothing wakes for is
	// never going to arrive, and that must read as this assertion failing rather
	// than as the package timing out.
	runCtx, cancelRun := context.WithTimeout(ctx, 45*time.Second)
	defer cancelRun()
	woken, err := provider.Next(runCtx.Done())
	if err != nil {
		t.Fatalf("THE QUEUED MESSAGE WAS ACCEPTED AND NEVER RAN: nothing woke the session after turn/queue: %v", err)
	}
	if !woken.Contains(followUpText) {
		t.Fatalf("a turn ran but does not carry the queued message %q; messages:\n%s", followUpText, strings.Join(woken.Texts(), "\n"))
	}

	// And Stop still reaches the turn the queued message woke.
	wokenTurn := awaitActiveTurn(ctx, t, client, ref, stopped)
	stopReceipt, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	if err != nil {
		t.Fatalf("turn/interrupt against the woken turn %q: %v", wokenTurn, err)
	}
	if stopReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/interrupt disposition = %q, want %q", stopReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	awaitTurnStatus(ctx, t, client, ref, wokenTurn, "interrupted")
}

// TestE2E_SteerWithNoTurnIDReachesTheModelAndTheTranscript pins the delivery
// half of a steer that names no turn. A receipt says the daemon accepted the
// mutation; the next model request is what proves the running loop consumed it,
// and the durable transcript is what proves a user reading the session back
// sees it. A test that stops at the receipt asserts only that the daemon said
// yes.
func TestE2E_SteerWithNoTurnIDReachesTheModelAndTheTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)
	ref := startLiveThread(ctx, t, client, stack, "EVENER-E2E-STEER-DELIVERY-OPENING")

	const steerText = "EVENER-E2E-STEER-DELIVERY-TEXT"

	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the session's first model request: %v", err)
	}
	running := awaitActiveTurn(ctx, t, client, ref, "")

	receipt, err := clientRequest[appwire.TurnSteerResponse](ctx, client, appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: steerText}},
	})
	if err != nil {
		t.Fatalf("turn/steer carrying no turn id was refused against running turn %q: %v", running, err)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/steer disposition = %q, want %q", receipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}

	// Steering is injected between the tool round and the next model call
	// (agent/session_tool_round.go), so round 2 is where delivery becomes visible.
	round1.RespondToolCall("read_file", map[string]any{"file_path": stack.readableFile})
	round2, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the model request after the tool round: %v", err)
	}
	if !round2.Contains(steerText) {
		t.Fatalf("the steer never reached the model: the request after the tool round does not carry %q; messages:\n%s",
			steerText, strings.Join(round2.Texts(), "\n"))
	}

	turnID := awaitSteeringItem(ctx, t, client, ref, steerText)
	if turnID != running {
		t.Fatalf("the steer landed in the transcript under turn %q, but the running turn was %q", turnID, running)
	}

	round2.RespondToolCall("communicate", communicateArgs("steered turn done"))
}

// TestE2E_SteerLandsInTheNextTurnWhenItsTurnEnded is the case the deleted
// precondition made impossible: the turn a steer was aimed at is over by the
// time the steer arrives. Under expectedTurnId that was a Conflict and the
// user's words were dropped on the floor; the rule now is that the intent
// applies as soon as possible instead of bouncing, so the steer opens the next
// turn rather than failing.
//
// The end of the opening turn is what makes the window deterministic: the
// session has demonstrably settled before the steer is sent, so there is no
// turn for it to name and no race to lose.
func TestE2E_SteerLandsInTheNextTurnWhenItsTurnEnded(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)
	ref := startLiveThread(ctx, t, client, stack, "EVENER-E2E-LATE-STEER-OPENING")

	const steerText = "EVENER-E2E-LATE-STEER-TEXT"

	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the session's first model request: %v", err)
	}
	endedTurn := awaitActiveTurn(ctx, t, client, ref, "")
	round1.RespondToolCall("communicate", communicateArgs("opening turn done"))
	awaitThread(ctx, t, client, ref, "the opening turn to end", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Status.Type != appwire.ThreadStatusActive
	})

	receipt, err := clientRequest[appwire.TurnSteerResponse](ctx, client, appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: steerText}},
	})
	if err != nil {
		t.Fatalf("A STEER BOUNCED BECAUSE ITS TURN ENDED: turn/steer after turn %q settled: %v", endedTurn, err)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/steer disposition = %q, want %q", receipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}

	// Accepting a steer nobody drains loses it more quietly than the rejection
	// it replaced (Session.wakeForPendingSteering), so the next model request is
	// the assertion that matters. Bounded well inside the test's own deadline:
	// an undelivered steer means no request is ever coming, and that must read
	// as this assertion failing rather than as the package timing out.
	deliveryCtx, cancelDelivery := context.WithTimeout(ctx, 45*time.Second)
	defer cancelDelivery()
	next, err := provider.Next(deliveryCtx.Done())
	if err != nil {
		t.Fatalf("the steer was accepted but no turn ever ran to deliver it: %v", err)
	}
	if !next.Contains(steerText) {
		t.Fatalf("the turn the steer woke does not carry %q; messages:\n%s", steerText, strings.Join(next.Texts(), "\n"))
	}

	landedIn := awaitSteeringItem(ctx, t, client, ref, steerText)
	if landedIn == endedTurn {
		t.Fatalf("the steer was folded back into the turn that had already ended (%q); it must open a later turn", endedTurn)
	}

	next.RespondToolCall("communicate", communicateArgs("late steer done"))
}

// TestE2E_QueuePreconditionsStillRefuseAStaleClient is the other side of
// deleting a precondition: the two that name a real object have to keep biting.
// expectedQueueRevision on drainAsSteer and expectedEntryId on
// promoteQueuedAsSteer are what stop a client working from a stale queue
// snapshot from steering the wrong message into the model, and a refactor that
// removed a precondition is exactly when they would be lost by accident.
//
// Each refusal is paired with the same call made against the CURRENT snapshot,
// so a daemon that refuses everything fails here too.
func TestE2E_QueuePreconditionsStillRefuseAStaleClient(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)

	stack := startHubStack(t, provider)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)
	ref := startLiveThread(ctx, t, client, stack, "EVENER-E2E-PRECONDITION-OPENING")

	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the session's first model request: %v", err)
	}
	awaitActiveTurn(ctx, t, client, ref, "")

	first, err := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: "EVENER-E2E-QUEUED-FIRST"}},
	})
	if err != nil {
		t.Fatalf("turn/queue (first): %v", err)
	}
	staleSnapshot := awaitQueueDepth(ctx, t, client, ref, 1)
	if len(first.Receipt.QueueEntryIDs) != 1 {
		t.Fatalf("turn/queue receipt named %d queue entries, want 1", len(first.Receipt.QueueEntryIDs))
	}
	staleEntryID := first.Receipt.QueueEntryIDs[0]

	if _, err := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: "EVENER-E2E-QUEUED-SECOND"}},
	}); err != nil {
		t.Fatalf("turn/queue (second): %v", err)
	}
	current := awaitQueueDepth(ctx, t, client, ref, 2)
	if current.Revision == staleSnapshot.Revision {
		t.Fatalf("the queue revision did not move across two queued messages (%d); the drain precondition cannot be tested",
			current.Revision)
	}

	// --- drainAsSteer against a revision that has moved on --------------------
	_, err = clientRequest[appwire.TurnDrainAsSteerResponse](ctx, client, appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{
		Ref:                   ref,
		ClientMutationID:      newMutationID(t),
		ExpectedQueueRevision: staleSnapshot.Revision,
	})
	requireConflict(t, err, "revision",
		"turn/drainAsSteer accepted expectedQueueRevision %d while the queue is at %d: a stale client just steered messages it never saw",
		staleSnapshot.Revision, current.Revision)

	// --- promoteQueuedAsSteer against an entry that has been consumed ---------
	// Cancelling the head is how a real client's snapshot goes stale without the
	// depth changing under it: index 0 now names a different message.
	if _, err := clientRequest[appwire.TurnCancelQueuedResponse](ctx, client, appwire.MethodTurnCancelQueued, appwire.TurnCancelQueuedParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Index:            0,
		ExpectedEntryID:  staleEntryID,
	}); err != nil {
		t.Fatalf("turn/cancelQueued of the head: %v", err)
	}
	afterCancel := awaitQueueDepth(ctx, t, client, ref, 1)
	if len(afterCancel.IDs) != 1 {
		t.Fatalf("queue reports depth 1 with %d ids", len(afterCancel.IDs))
	}
	liveEntryID := afterCancel.IDs[0]
	if liveEntryID == staleEntryID {
		t.Fatalf("the cancelled entry %q is still at index 0; the stale-snapshot fixture did not form", staleEntryID)
	}

	_, err = clientRequest[appwire.TurnPromoteQueuedAsSteerResponse](ctx, client, appwire.MethodTurnPromoteQueuedAsSteer, appwire.TurnPromoteQueuedAsSteerParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Index:            0,
		ExpectedEntryID:  staleEntryID,
	})
	requireConflict(t, err, "no longer matches",
		"turn/promoteQueuedAsSteer accepted expectedEntryId %q while index 0 holds %q: the wrong message just went to the model",
		staleEntryID, liveEntryID)

	// --- the same two calls, made honestly, must still work -------------------
	if _, err := clientRequest[appwire.TurnPromoteQueuedAsSteerResponse](ctx, client, appwire.MethodTurnPromoteQueuedAsSteer, appwire.TurnPromoteQueuedAsSteerParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Index:            0,
		ExpectedEntryID:  liveEntryID,
	}); err != nil {
		t.Fatalf("turn/promoteQueuedAsSteer with the live entry id %q was refused, so the refusals above prove nothing: %v", liveEntryID, err)
	}
	drained := awaitQueueDepth(ctx, t, client, ref, 0)
	if _, err := clientRequest[appwire.TurnDrainAsSteerResponse](ctx, client, appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{
		Ref:                   ref,
		ClientMutationID:      newMutationID(t),
		ExpectedQueueRevision: drained.Revision,
		Input:                 []appwire.InputItem{{Type: "text", Text: "EVENER-E2E-DRAIN-WITH-CURRENT-REVISION"}},
	}); err != nil {
		t.Fatalf("turn/drainAsSteer with the current revision %d was refused, so the refusal above proves nothing: %v", drained.Revision, err)
	}

	round1.RespondToolCall("communicate", communicateArgs("precondition turn done"))
}

// TestE2E_LiveModelStopAndSteerNeedNoTurnID runs the two claims that matter
// most against a real model instead of a scripted one. The scripted provider
// proves the wire and the daemon agree; only a real turn proves the rule holds
// when the thing being stopped is a model streaming tokens back, which is the
// shape of every Stop a user has ever pressed.
//
// Skips by default, and on any machine without credentials: it is extra
// evidence for a rule the fakellm tests already gate, never the gate itself.
func TestE2E_LiveModelStopAndSteerNeedNoTurnID(t *testing.T) {
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skip("set EVENER_LIVE_TESTS=1 to run the live-model turn-control e2e test")
	}
	instance := os.Getenv("EVENER_TEST_PROVIDER")
	model := os.Getenv("EVENER_TEST_MODEL")
	if instance == "" || model == "" {
		t.Skip("EVENER_TEST_PROVIDER and EVENER_TEST_MODEL required")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("no LLM API key in env")
	}

	stack := startHubStackOnProvider(t, fmt.Sprintf(`schema = 1
default = %q

[instances.%s]
type = %q
`, instance, instance, instance), instance+"/"+model)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)

	// A prompt whose answer is long enough that the Stop lands mid-stream. A
	// one-word answer would let the turn end on its own and the test would pass
	// without ever cancelling anything.
	ref := startLiveThread(ctx, t, client, stack,
		"Count from 1 to 400. Print one number per line and nothing else. Do not stop early.")

	running := awaitActiveTurn(ctx, t, client, ref, "")
	// An active turn is published before the model is dispatched, so
	// interrupting here could cancel a turn that never reached the provider --
	// and the test would pass without a live model having done anything. Wait
	// for output the model actually produced.
	awaitModelOutput(ctx, t, client, ref, running)
	t.Logf("live turn in flight with model output: %s", running)

	receipt, err := clientRequest[appwire.TurnInterruptResponse](ctx, client, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
	})
	if err != nil {
		t.Fatalf("turn/interrupt carrying no turn id was refused against live turn %q: %v", running, err)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/interrupt disposition = %q, want %q", receipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	if receipt.Receipt.TurnID != running {
		t.Fatalf("the receipt names turn %q, but the session was running %q", receipt.Receipt.TurnID, running)
	}
	awaitThread(ctx, t, client, ref, "the live session to stop", func(thread appwire.Thread) bool {
		return thread.Evener.ActiveTurnID == "" && thread.Status.Type != appwire.ThreadStatusActive
	})
	awaitTurnStatus(ctx, t, client, ref, running, "interrupted")

	// And a steer aimed at a turn that is already over lands in the next one.
	const steerText = "EVENER-E2E-LIVE-STEER-TEXT"
	steerReceipt, err := clientRequest[appwire.TurnSteerResponse](ctx, client, appwire.MethodTurnSteer, appwire.TurnSteerParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: "Reply with exactly: " + steerText}},
	})
	if err != nil {
		t.Fatalf("turn/steer after the live turn was stopped: %v", err)
	}
	if steerReceipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/steer disposition = %q, want %q", steerReceipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}
	landedIn := awaitSteeringItem(ctx, t, client, ref, steerText)
	if landedIn == running {
		t.Fatalf("the steer was folded into the stopped turn %q instead of a later one", running)
	}
	// The steering item is written when a turn INCORPORATES the steer, not when
	// the daemon accepts it, so its presence proves the running loop drained the
	// message - and still not that the model read it. The model repeating the
	// marker back is what proves delivery.
	awaitModelEcho(ctx, t, client, ref, steerText)
	t.Logf("live steer landed in turn %s and came back from the model", landedIn)
}

// startLiveThread opens a evener thread on the stack and registers the shutdown
// that keeps the daemon -- a grandchild the hub deliberately outlives -- from
// leaking. t.Cleanup is LIFO, so this runs before startHubStack's hub kill.
func startLiveThread(ctx context.Context, t *testing.T, client *appwire.Client, stack hubStack, opening string) string {
	t.Helper()
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "evener",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: opening}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	ref := started.Thread.Evener.Ref
	if ref == "" {
		t.Fatalf("thread/start returned no evener ref: %+v", started.Thread)
	}
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref}); err != nil {
			t.Errorf("thread/shutdown left the daemon running: %v", err)
		}
	})
	return ref
}

// awaitQueueDepth waits for the published queue to settle at depth and returns
// the state it settled in, so a test reads the revision and entry ids a real
// client would have read rather than guessing them.
func awaitQueueDepth(ctx context.Context, t *testing.T, client *appwire.Client, ref string, depth int) appwire.QueueState {
	t.Helper()
	var queue appwire.QueueState
	awaitThread(ctx, t, client, ref, "queue depth "+strconv.Itoa(depth), func(thread appwire.Thread) bool {
		if thread.Evener.Queue.Depth != depth {
			return false
		}
		queue = thread.Evener.Queue
		return true
	})
	return queue
}

// awaitTurnStatus waits for the durable transcript to report turnID in the
// given status, and is how "the session actually stopped" is asserted: the
// receipt and the live status projection are both upstream of the record a user
// reads back.
func awaitTurnStatus(ctx context.Context, t *testing.T, client *appwire.Client, ref, turnID, status string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var seen string
	for time.Now().Before(deadline) {
		turns, err := clientRequest[appwire.ThreadTurnsListResponse](ctx, client, appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: ref})
		if err == nil {
			for _, turn := range turns.Data {
				if turn.ID != turnID {
					continue
				}
				seen = turn.Status
				if turn.Status == status {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for turn %s to read %q: %v", turnID, status, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("turn %s never reached status %q in the transcript; last status %q", turnID, status, seen)
}

// awaitModelOutput waits for the named turn to carry model-produced output, so
// a caller can act on a turn the provider has demonstrably started rather than
// one that has only been announced.
//
// The budget is generous because the wait is on a real provider: under the load
// of the whole e2e set a live model can take well over a minute to produce, and
// a deadline tuned to an idle machine turns that into a spurious failure. The
// caller's context still bounds the test as a whole.
func awaitModelOutput(ctx context.Context, t *testing.T, client *appwire.Client, ref, turnID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		turns, err := clientRequest[appwire.ThreadTurnsListResponse](ctx, client, appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: ref, ItemsView: "full"})
		if err == nil {
			for _, turn := range turns.Data {
				if turn.ID != turnID {
					continue
				}
				for _, item := range turn.Items {
					if item.Type == "agentMessage" && strings.TrimSpace(item.Text) != "" {
						return
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for model output in turn %s: %v", turnID, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("turn %s produced no model output; anything asserted about it would hold for a turn the provider never ran", turnID)
}

// awaitModelEcho waits for text to come back as model output. Unlike a
// steering item -- written when a turn incorporates the steer, via
// consumeSteeringMessage on the drain path -- this can only appear if the
// steer reached the model and the model answered it.
func awaitModelEcho(ctx context.Context, t *testing.T, client *appwire.Client, ref, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		turns, err := clientRequest[appwire.ThreadTurnsListResponse](ctx, client, appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: ref, ItemsView: "full"})
		if err == nil {
			for _, turn := range turns.Data {
				for _, item := range turn.Items {
					if item.Type == "agentMessage" && strings.Contains(item.Text, text) {
						return
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for the model to echo %q: %v", text, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("%q never came back as model output: the steer was accepted and recorded, but nothing proves the model received it", text)
}

// awaitSteeringItem waits for text to appear as a steering item in the durable
// transcript and returns the id of the turn it landed in.
func awaitSteeringItem(ctx context.Context, t *testing.T, client *appwire.Client, ref, text string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		turns, err := clientRequest[appwire.ThreadTurnsListResponse](ctx, client, appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: ref, ItemsView: "full"})
		if err == nil {
			for _, turn := range turns.Data {
				for _, item := range turn.Items {
					if item.Type == "steering" && strings.Contains(item.Text, text) {
						return turn.ID
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %q in the transcript: %v", text, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("%q never appeared as a steering item in the transcript: the steer was accepted but a user reading the session back never sees it", text)
	return ""
}

// requireConflict fails unless err is the daemon's Conflict carrying want,
// which is how a test tells "the precondition refused this" apart from "the
// call failed for some other reason and the assertion passed anyway".
func requireConflict(t *testing.T, err error, want, format string, args ...any) {
	t.Helper()
	if err == nil {
		t.Fatalf(format, args...)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("want an appwire Conflict mentioning %q, got a non-wire error: %v", want, err)
	}
	if wire.Code != appwire.CodeConflict {
		t.Fatalf("want an appwire Conflict (%d) mentioning %q, got code %d: %v", appwire.CodeConflict, want, wire.Code, err)
	}
	if !strings.Contains(wire.Message, want) {
		t.Fatalf("the refusal does not name the precondition that fired: want a message containing %q, got %q", want, wire.Message)
	}
}
