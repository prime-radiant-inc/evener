package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
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
// A turn/start is accepted and claimed -- the durable snapshot names the turn,
// and the daemon publishes status=active off that reservation, so the composer
// draws a working session and offers Stop. The session itself has not entered
// SessionProcessing yet: it is still doing pre-turn work (slash-command
// expansion, which runs an inline shell span, is the widest instance). So
// WireState() reports a settled session, and a Stop pressed against a session
// the user can see working was rejected with "session is not processing".
//
// The precondition's own comment claimed it sampled "the fact the wire
// publishes as the thread's status". It did not: the wire's active comes from
// the daemon's reservation, this sampled the session's own state, and the two
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
