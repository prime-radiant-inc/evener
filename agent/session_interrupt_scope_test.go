package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// runningStartTurn accepts a turn/start, claims it, and incorporates its
// transcript, leaving the session in the state a turn that is actually running
// has: a durable ActiveTurnID and an incorporated pending execution that only
// completeClientMutationTurnWithState can settle.
func runningStartTurn(t *testing.T, sess *Session, clientMutationID, text string) string {
	t.Helper()
	started, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: clientMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: text}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart(%q): %v", clientMutationID, err)
	}
	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if err := sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		nil,
		false,
	); err != nil {
		t.Fatalf("incorporate start %q: %v", clientMutationID, err)
	}
	return started.Turn.ID
}

// completedStartTurn runs a turn to completion and returns its id, leaving the
// session settled after it: the state a stale Stop's SinceTurnID refers to.
func completedStartTurn(t *testing.T, sess *Session, clientMutationID, text string) string {
	t.Helper()
	turnID := runningStartTurn(t, sess, clientMutationID, text)
	if err := sess.completeClientMutationTurn(clientMutationID); err != nil {
		t.Fatalf("completeClientMutationTurn(%q): %v", clientMutationID, err)
	}
	return turnID
}

// TestInterruptStopsATurnItCannotName is Task 1's between-turn gap: the drain
// loop settled turn 1 (clearing the durable name) while a queued message keeps
// the session running, so no id any client holds names the turn that is over.
// Stop has to work anyway -- the user can see the session working.
func TestInterruptStopsATurnItCannotName(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	runningStartTurn(t, sess, "start-gap-turn-one", "first message")
	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "queue-gap-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "second message"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	if err := sess.completeClientMutationTurn("start-gap-turn-one"); err != nil {
		t.Fatalf("completeClientMutationTurn: %v", err)
	}

	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("durable ActiveTurnID after the first turn settled = %q, want empty", got)
	}
	if got := sess.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState in the gap = %q, want %q", got, SessionProcessing)
	}

	scopedCancels := 0
	response, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-gap-session-scoped",
	}, func() { scopedCancels++ })
	if err != nil {
		t.Fatalf("interrupt in the gap: %v", err)
	}
	if scopedCancels != 1 {
		t.Fatalf("interrupt cancelled %d times, want 1", scopedCancels)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("interrupt disposition = %q, want applied", response.Receipt.Disposition)
	}
	snapshot := sess.clientMutations.snapshot()
	if snapshot.InterruptFence != nil {
		t.Fatalf("interrupt left a fence: %#v", snapshot.InterruptFence)
	}
	record := snapshot.Journal["interrupt-gap-session-scoped"]
	if record.OperationState != clientMutationOperationTerminal ||
		record.ExecutionState != "interrupted" {
		t.Fatalf("interrupt record = %#v", record)
	}
}

// TestInterruptFenceRecordsTheTurnItCancelled pins the half of the
// design that keeps finalizeClientMutationInterrupt unchanged: the client does
// not supply a target, so the fence takes the id the durable store is holding
// and terminalizes exactly that turn's pending execution.
func TestInterruptFenceRecordsTheTurnItCancelled(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	turnID := runningStartTurn(t, sess, "start-named-turn", "running message")

	cancels := 0
	response, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-named-session-scoped",
	}, func() { cancels++ })
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if cancels != 1 {
		t.Fatalf("interrupt cancelled %d times, want 1", cancels)
	}
	if response.Receipt.TurnID != turnID {
		t.Fatalf("interrupt receipt turn = %q, want %q", response.Receipt.TurnID, turnID)
	}
	snapshot := sess.clientMutations.snapshot()
	if snapshot.ActiveTurnID != "" || snapshot.InterruptFence != nil {
		t.Fatalf("post-interrupt state: active=%q fence=%#v", snapshot.ActiveTurnID, snapshot.InterruptFence)
	}
	target := snapshot.Journal["start-named-turn"]
	if target.OperationState != clientMutationOperationTerminal || target.ExecutionState != "interrupted" {
		t.Fatalf("interrupted target record = %#v", target)
	}
	if _, pending := snapshot.PendingExecutions["start-named-turn"]; pending {
		t.Fatal("interrupted target remained a pending execution")
	}
}

// TestInterruptStopsAClaimedTurnBeforeTheSessionIsProcessing is kata vewa: the
// pre-turn window, which every turn passes through and in which Stop was
// refused.
//
// A turn/start is accepted and claimed -- the durable snapshot names the turn --
// and the daemon reports the thread active, so the composer draws a working
// session and offers Stop. The session itself has not entered SessionProcessing
// yet: it is still doing pre-turn work (slash-command expansion, which runs an
// inline shell span, is the widest instance). So WireState() reports a settled
// session, and a Stop pressed against a session the user can see working was
// rejected with "session is not processing".
//
// The precondition's own comment claimed it sampled "the fact the wire
// publishes as the thread's status". It did not: the daemon reports active off
// its own `processing` flag, which it holds across the whole of an input rather
// than only the model round, while this sampled the SESSION's state. The two
// disagree for the whole of pre-turn work.
//
// The rule this encodes: Stop is available whenever the session is not
// quiesced. A claimed turn is work in progress even before the model round
// starts, and cancelling it is exactly what the user asked for.
func TestInterruptStopsAClaimedTurnBeforeTheSessionIsProcessing(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-claimed-not-yet-processing",
		Input:            []appwire.InputItem{{Type: "text", Text: "/park"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}

	// The window's two defining facts. Guarded so a change in either one turns
	// this into a visible premise failure rather than a silent pass.
	if got := sess.WireState(); got == string(SessionProcessing) {
		t.Fatalf("session WireState = %q: pre-turn work is over, so this test is no longer in the window it names", got)
	}
	claimedTurn := sess.clientMutations.snapshot().ActiveTurnID
	if claimedTurn == "" {
		t.Fatal("no durable turn was claimed, so the daemon would not be publishing an active status here and there is nothing for Stop to cancel")
	}

	cancels := 0
	response, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-claimed-not-yet-processing",
	}, func() { cancels++ })
	if err != nil {
		t.Fatalf("Stop during pre-turn work on claimed turn %s: %v", claimedTurn, err)
	}
	if cancels != 1 {
		t.Fatalf("Stop during pre-turn work cancelled %d times, want 1", cancels)
	}
	if response.Receipt.TurnID != claimedTurn {
		t.Fatalf("Stop receipt turn = %q, want the claimed turn %q", response.Receipt.TurnID, claimedTurn)
	}
}

// TestInterruptIsRefusedOnASettledSession keeps the escape hatch
// from being a blanket bypass: with nothing running there is nothing to stop,
// and an accepted interrupt would clear a turn identity out from under whatever
// claims it next.
func TestInterruptIsRefusedOnASettledSession(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	if got := sess.WireState(); got == string(SessionProcessing) {
		t.Fatalf("fresh session WireState = %q, want a settled state", got)
	}
	cancels := 0
	_, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-settled-session-scoped",
	}, func() { cancels++ })
	if err == nil || !strings.Contains(err.Error(), "session is not processing") {
		t.Fatalf("interrupt on a settled session = %v, want session is not processing", err)
	}
	if cancels != 0 {
		t.Fatalf("refused interrupt cancelled %d times, want 0", cancels)
	}
	if fence := sess.clientMutations.snapshot().InterruptFence; fence != nil {
		t.Fatalf("refused interrupt left a fence: %#v", fence)
	}
}

// TestInterruptRetryStopsNothingTwice is the property the rejected
// "just make expectedTurnId optional" shape could not hold. A retry of the same
// clientMutationId must replay the first interrupt's receipt, not cancel
// whatever turn happens to be running when the retry lands.
func TestInterruptRetryStopsNothingTwice(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()

	firstTurn := runningStartTurn(t, sess, "start-idempotence-one", "first message")
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-idempotence",
	}
	cancels := 0
	first, err := sess.InterruptClientMutation(context.Background(), interrupt, func() { cancels++ })
	if err != nil {
		t.Fatalf("first interrupt: %v", err)
	}
	if first.Receipt.TurnID != firstTurn {
		t.Fatalf("first interrupt receipt turn = %q, want %q", first.Receipt.TurnID, firstTurn)
	}

	secondTurn := runningStartTurn(t, sess, "start-idempotence-two", "second message")

	retry, err := sess.InterruptClientMutation(context.Background(), interrupt, func() { cancels++ })
	if err != nil {
		t.Fatalf("retried interrupt: %v", err)
	}
	if cancels != 1 {
		t.Fatalf("retried interrupt cancelled %d times, want 1", cancels)
	}
	if retry.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("retry disposition = %q, want replayed", retry.Receipt.Disposition)
	}
	if retry.Receipt.TurnID != firstTurn {
		t.Fatalf("retry receipt turn = %q, want the interrupted turn %q", retry.Receipt.TurnID, firstTurn)
	}
	snapshot := sess.clientMutations.snapshot()
	if snapshot.ActiveTurnID != secondTurn {
		t.Fatalf("ActiveTurnID after the retry = %q, want the untouched second turn %q", snapshot.ActiveTurnID, secondTurn)
	}
	if snapshot.InterruptFence != nil {
		t.Fatalf("retry re-armed an interrupt fence: %#v", snapshot.InterruptFence)
	}
	survivor := snapshot.Journal["start-idempotence-two"]
	if survivor.OperationState == clientMutationOperationTerminal {
		t.Fatalf("retry terminalized the second turn: %#v", survivor)
	}
}

// TestInterruptSinceTurnID rejects a Stop whose delivery crossed a turn
// boundary the user never saw. The click-time binding is SinceTurnID -- the
// active turn id the client held when Stop was pressed -- and issue #178 is
// the delayed dispatch that lands only after a LATER turn is already running.
// The guard must reject that delivery outright (surfaced as a rejection, the
// same shape as "session is not processing"), never cancel the later turn.
//
// The same-generation case is the #176 win preserved: a Stop pressed during
// turn N's pre-turn work, delivered late but still inside turn N, must still
// land. And the absent field is backward compatibility -- an old client (or an
// old durable outbox record) that sends no sinceTurnId gets today's
// session-scoped behavior, unchanged.
func TestInterruptSinceTurnID(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, sess *Session)
	}{
		{
			// The issue #178 repro: turn 1 is clicked-then-missed, turn 2 is
			// running at delivery. Modelled on TestInterruptRetryStopsNothingTwice
			// but WITHOUT reusing the clientMutationId -- this is the FIRST
			// delivery arriving late, not a replayed one.
			name: "delayed stop must not cancel a later turn",
			run: func(t *testing.T, sess *Session) {
				firstTurn := completedStartTurn(t, sess, "start-since-one", "first message")
				secondTurn := runningStartTurn(t, sess, "start-since-two", "second message")

				cancels := 0
				_, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
					ClientMutationID: "interrupt-since-stale",
					SinceTurnID:      firstTurn,
				}, func() { cancels++ })
				if err == nil || !strings.Contains(err.Error(), "stop expired") {
					t.Fatalf("stale-SinceTurnID interrupt = %v, want a stop expired rejection", err)
				}
				if cancels != 0 {
					t.Fatalf("stale-SinceTurnID interrupt cancelled %d times, want 0", cancels)
				}
				snapshot := sess.clientMutations.snapshot()
				if snapshot.ActiveTurnID != secondTurn {
					t.Fatalf("ActiveTurnID after the stale interrupt = %q, want the untouched second turn %q", snapshot.ActiveTurnID, secondTurn)
				}
				if snapshot.InterruptFence != nil {
					t.Fatalf("stale interrupt left a fence: %#v", snapshot.InterruptFence)
				}
				if snapshot.QueueHeld {
					t.Fatal("stale interrupt held the queue")
				}
				survivor := snapshot.Journal["start-since-two"]
				if survivor.OperationState == clientMutationOperationTerminal {
					t.Fatalf("stale interrupt terminalized the second turn: %#v", survivor)
				}
				rejected := snapshot.Journal["interrupt-since-stale"]
				if rejected.OperationState != clientMutationOperationRejected {
					t.Fatalf("stale interrupt record = %#v, want rejected", rejected)
				}
			},
		},
		{
			// A genuine retry of an expired Stop must replay the terminal
			// rejection, not re-evaluate against a now-current ActiveTurnID --
			// the property TestInterruptRetryStopsNothingTwice pins for the
			// applied case, pinned here for the expired one.
			name: "retry of an expired stop replays the rejection",
			run: func(t *testing.T, sess *Session) {
				firstTurn := completedStartTurn(t, sess, "start-retry-expired-one", "first message")
				secondTurn := runningStartTurn(t, sess, "start-retry-expired-two", "second message")

				interrupt := appwire.TurnInterruptParams{
					ClientMutationID: "interrupt-retry-expired",
					SinceTurnID:      firstTurn,
				}
				cancels := 0
				_, firstErr := sess.InterruptClientMutation(context.Background(), interrupt, func() { cancels++ })
				if firstErr == nil || !strings.Contains(firstErr.Error(), "stop expired") {
					t.Fatalf("first expired interrupt = %v, want a stop expired rejection", firstErr)
				}
				_, retryErr := sess.InterruptClientMutation(context.Background(), interrupt, func() { cancels++ })
				if retryErr == nil || !strings.Contains(retryErr.Error(), "stop expired") {
					t.Fatalf("retried expired interrupt = %v, want the same stop expired rejection", retryErr)
				}
				if cancels != 0 {
					t.Fatalf("retried expired interrupt cancelled %d times, want 0", cancels)
				}
				// The retry replays the durable rejection itself (a retry of
				// a rejected mutation returns that rejection), so the journal
				// record must still be the same terminal rejection and the
				// second turn must remain untouched.
				snapshot := sess.clientMutations.snapshot()
				if snapshot.ActiveTurnID != secondTurn {
					t.Fatalf("ActiveTurnID after the retry = %q, want the untouched second turn %q", snapshot.ActiveTurnID, secondTurn)
				}
				if rejected := snapshot.Journal["interrupt-retry-expired"]; rejected.OperationState != clientMutationOperationRejected {
					t.Fatalf("retried expired interrupt record = %#v, want rejected", rejected)
				}
			},
		},
		{
			// The #176 regression guard: SinceTurnID equal to the claimed turn's
			// own id -- a same-generation Stop delivered during pre-turn work --
			// must still apply.
			name: "same-generation stop during pre-turn work still applies",
			run: func(t *testing.T, sess *Session) {
				if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
					ClientMutationID: "start-since-claimed",
					Input:            []appwire.InputItem{{Type: "text", Text: "/park"}},
				}); err != nil {
					t.Fatalf("AcceptClientMutationStart: %v", err)
				}
				claimed, ok, err := sess.claimClientMutationStart()
				if err != nil || !ok {
					t.Fatalf("claimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
				}
				claimedTurn := sess.clientMutations.snapshot().ActiveTurnID
				if claimedTurn == "" {
					t.Fatal("no durable turn was claimed")
				}

				cancels := 0
				response, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
					ClientMutationID: "interrupt-since-claimed",
					SinceTurnID:      claimedTurn,
				}, func() { cancels++ })
				if err != nil {
					t.Fatalf("same-generation Stop on claimed turn %s: %v", claimedTurn, err)
				}
				if cancels != 1 {
					t.Fatalf("same-generation Stop cancelled %d times, want 1", cancels)
				}
				if response.Receipt.TurnID != claimedTurn {
					t.Fatalf("Stop receipt turn = %q, want the claimed turn %q", response.Receipt.TurnID, claimedTurn)
				}
			},
		},
		{
			// Backward compatibility: absent SinceTurnID is today's
			// session-scoped behavior, deliberately -- an old client or an old
			// durable outbox record without the field must never trip the new
			// rejection branch.
			name: "no sinceTurnId keeps session-scoped behavior",
			run: func(t *testing.T, sess *Session) {
				completedStartTurn(t, sess, "start-since-absent-one", "first message")
				secondTurn := runningStartTurn(t, sess, "start-since-absent-two", "second message")

				cancels := 0
				response, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
					ClientMutationID: "interrupt-since-absent",
				}, func() { cancels++ })
				if err != nil {
					t.Fatalf("absent-SinceTurnID interrupt across a turn boundary: %v", err)
				}
				if cancels != 1 {
					t.Fatalf("absent-SinceTurnID interrupt cancelled %d times, want 1", cancels)
				}
				if response.Receipt.TurnID != secondTurn {
					t.Fatalf("interrupt receipt turn = %q, want the second turn %q (session-scoped behavior is unchanged)", response.Receipt.TurnID, secondTurn)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := newQueuePersistTestSession(t, t.TempDir())
			defer sess.Close()
			tc.run(t, sess)
		})
	}
}
