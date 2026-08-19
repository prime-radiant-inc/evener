package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// Kata t9kt: the steering-only carrier turn ran as an empty user turn and was
// never made runnable. Two defects, both properties of the four-line branch
// that used to close ProcessPendingUserInput: (1) routing the steering-only
// wake through acceptUserInput's full user-turn accounting meant a MaxTurns
// ceiling failed the carrier turn before injectDrainedSteering ever ran,
// stranding the Applied steering with nothing left to wake it; (2) onRunnable
// was only ever called on the queued-message branch, so the carrier turn
// never reserved or published a durable turn id, and the daemon never wired
// cancellation to it.
//
// These two tests pin the acceptance criteria named in the kata.

// TestSteeringOnlyCarrierDeliversAtMaxTurnsCeilingWithoutAnEmptyUserTurn is
// acceptance criterion 1: a steering-only wake at the MaxTurns ceiling still
// delivers its steering, and appends no empty user turn.
//
// Before the fix, ProcessPendingUserInput ran the steering-only branch as
// ProcessInputKind(ctx, "", nil, EntryUserInput), which went through
// acceptUserInput -- and acceptUserInput's MaxTurns check runs before its
// injectDrainedSteering call. At the ceiling, claimDirectClientMutationTurn
// returned budgetExhaustionError before the turn ever appended anything or
// drained the steer, leaving the Applied mutation pending forever.
func TestSteeringOnlyCarrierDeliversAtMaxTurnsCeilingWithoutAnEmptyUserTurn(t *testing.T) {
	sess, adapter, _ := newBudgetSession(t, SessionConfig{MaxTurns: 1}, nil)
	sess.mu.Lock()
	sess.turns = 1 // already at the ceiling
	sess.mu.Unlock()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-at-ceiling",
		Input:            []appwire.InputItem{{Type: "text", Text: "steer at the ceiling"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	historyBefore := len(budgetHistory(sess))

	var reportedTurnID string
	_, ran, err := sess.ProcessPendingUserInput(context.Background(), func(turnID string) {
		reportedTurnID = turnID
	})
	if err != nil {
		t.Fatalf("ProcessPendingUserInput at the MaxTurns ceiling: %v", err)
	}
	if !ran {
		t.Fatal("ProcessPendingUserInput did not run; the steering was lost at the ceiling")
	}
	if reportedTurnID == "" {
		t.Fatal("onRunnable was never called; the daemon cannot publish or cancel this turn")
	}
	if sess.hasPendingSteering() {
		t.Fatal("steering is still pending after the carrier turn ran; it was accepted and never delivered")
	}

	history := budgetHistory(sess)
	for _, turn := range history[historyBefore:] {
		if turn.Kind == schema.TurnUserInput {
			t.Fatalf("the carrier turn appended an empty user turn: %+v", turn)
		}
	}
	if got := countBudgetSteering(history, "steer at the ceiling"); got != 1 {
		t.Fatalf("delivered steering count = %d, want 1", got)
	}

	sess.mu.Lock()
	turnsAfter := sess.turns
	sess.mu.Unlock()
	if turnsAfter != 1 {
		t.Fatalf("s.turns = %d after the carrier turn, want unchanged at 1 (notification-style accounting, not a user turn)", turnsAfter)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1; the steering must actually reach the model", len(requests))
	}
	// The steering must be drained into history BEFORE the first LLM call of
	// the carrier turn (spec 2.5), not merely by the time the turn ends -- the
	// round loop's own post-tool-round drain (session_tool_round.go) would
	// eventually pick it up either way, which would pass this test for the
	// wrong reason if it only checked the transcript after the turn returned.
	if !requestHasExactUserMessage(requests[0], "steer at the ceiling") {
		t.Fatalf("first model request missing the steering message: %+v", requests[0].Messages)
	}
}

// TestSteeringOnlyCarrierInterruptCancelsTheModelRequestAndMatchesTurnAuthority
// is acceptance criterion 2: an interrupt during a steering-only carrier turn
// actually cancels the model request, and the projected turn id matches the
// mutation's authority.
//
// Before the fix, onRunnable was never called on the steering-only branch, so
// cmd/evener/serve.go never installed the daemon's cancellation callback for it
// -- an interrupt would return Applied without cancelling anything. This test
// drives a real in-flight model call (blockingAdapter) and a real
// InterruptClientMutation, and checks the id ProcessPendingUserInput reports
// is the same id the steer's own Applied receipt already promised the client.
func TestSteeringOnlyCarrierInterruptCancelsTheModelRequestAndMatchesTurnAuthority(t *testing.T) {
	blocked := make(chan struct{})
	sess := newSession(t, withAdapter(&blockingAdapter{name: "openai", blocked: blocked}))
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	steerResp, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-interrupt",
		Input:            []appwire.InputItem{{Type: "text", Text: "steer me"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	wantTurnID := steerResp.Receipt.TurnID
	if wantTurnID == "" {
		t.Fatal("the steer's own Applied receipt carried no turn id; this test cannot check authority")
	}

	turnCtx, cancelTurn := context.WithCancel(context.Background())
	defer cancelTurn()

	var reportedTurnID string
	done := make(chan error, 1)
	go func() {
		_, _, procErr := sess.ProcessPendingUserInput(turnCtx, func(turnID string) {
			reportedTurnID = turnID
		})
		done <- procErr
	}()

	<-blocked // the model call is genuinely in flight

	if reportedTurnID != wantTurnID {
		t.Fatalf("onRunnable reported turn %q, want the steer's own reserved turn %q -- the projected turn id does not match mutation authority", reportedTurnID, wantTurnID)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != wantTurnID {
		t.Fatalf("ActiveTurnID = %q while the carrier is running, want %q", got, wantTurnID)
	}

	cancels := 0
	var procErr error
	if _, err := sess.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-steering-carrier",
	}, func() {
		cancels++
		cancelTurn()
		procErr = <-done
	}); err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}
	if cancels != 1 {
		t.Fatalf("interrupt's cancelAndWait ran %d times, want 1 -- the model request was not actually cancelled", cancels)
	}
	if !errors.Is(procErr, context.Canceled) {
		t.Fatalf("ProcessPendingUserInput error after interrupt = %v, want context.Canceled", procErr)
	}
	if got := sess.clientMutations.snapshot().InterruptFence; got != nil {
		t.Fatalf("interrupt fence left set: %#v", got)
	}
}

// TestSteeringOnlyCarrierHandsBackAClaimTheTurnNeverUsed is the obligation the
// claim takes on by publishing ActiveTurnID from OUTSIDE processOneInput:
// whatever happens next, the id comes back.
//
// processOneInput's own deferred releaseRunningTurnID covers the turn once it
// starts, but it is registered several early returns into the call --
// processInputKindWithProvenance refuses a closed session before it, and
// processOneInput checks its context for cancellation before it. A claim
// stranded on either of those is permanent rather than merely untidy: the
// pending steer that reserved the id still owns it, so forgetRunningTurnNoOneOwns
// (the load-time sweep for ids nobody will settle) deliberately leaves it
// alone, and AcceptClientMutationStart's precondition then refuses every later
// turn/start with "turn is already active" -- across restarts, for the life of
// the session.
func TestSteeringOnlyCarrierHandsBackAClaimTheTurnNeverUsed(t *testing.T) {
	cases := []struct {
		name string
		// arrange makes the wake fail before processOneInput registers its
		// release, and reports the context to wake with.
		arrange func(sess *Session) context.Context
	}{
		{
			name: "turn context already cancelled",
			arrange: func(*Session) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "session closed",
			arrange: func(sess *Session) context.Context {
				sess.Close()
				return context.Background()
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := newSession(t)
			if err := sess.ensureClientMutationStore(); err != nil {
				t.Fatalf("ensureClientMutationStore: %v", err)
			}
			if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
				ClientMutationID: "cm-steer-unused-claim",
				Input:            []appwire.InputItem{{Type: "text", Text: "steer"}},
			}); err != nil {
				t.Fatalf("AcceptClientMutationSteer: %v", err)
			}

			ctx := tc.arrange(sess)
			if _, _, err := sess.ProcessPendingUserInput(ctx, nil); err == nil {
				t.Fatal("the wake reported success; this case is meant to fail before the turn runs")
			}

			if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
				t.Fatalf("ActiveTurnID = %q after a wake whose turn never ran: the claim was never handed back, "+
					"and a pending steer owns it so nothing at load will clear it -- every later turn/start is refused", got)
			}
		})
	}
}

// TestSteeringOnlyCarrierStrandedClaimWouldRefuseTheNextTurnStart names the
// consequence the test above exists to prevent, on the one case where the
// session is still open enough to prove it: a stranded claim is not a bookkeeping
// wart, it is a session that can never start another turn.
func TestSteeringOnlyCarrierStrandedClaimWouldRefuseTheNextTurnStart(t *testing.T) {
	sess := newSession(t)
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-then-start",
		Input:            []appwire.InputItem{{Type: "text", Text: "steer"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := sess.ProcessPendingUserInput(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessPendingUserInput on a cancelled context = %v, want context.Canceled", err)
	}

	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-start-after-the-wake",
		Input:            []appwire.InputItem{{Type: "text", Text: "hello"}},
	}); err != nil {
		t.Fatalf("turn/start after a wake that never ran: %v -- the carrier's claim is still holding the session's turn identity", err)
	}
}
