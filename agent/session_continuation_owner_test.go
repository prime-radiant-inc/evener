package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestUnnamedContinuationOwnsItsSteering pins #4599: a client turn/start can
// reserve the durable active-turn name in the window before a goal continuation
// is admitted, which makes the continuation run unnamed (the refusal itself is
// pinned by TestMintRunningTurnIDRefusesWhenATurnIsAlreadyNamed). Steering
// recorded during that run must name the execution that is actually running,
// not the reserved turn that is not. Both recording sites are covered: a queued
// steer drained by the continuation's acceptance and a steer appended directly
// mid-turn.
func TestUnnamedContinuationOwnsItsSteering(t *testing.T) {
	t.Parallel()
	const drainedText = "daemon steer drained by the continuation"
	const appendedText = "daemon steer appended during the continuation"

	var sess *Session
	var runningID string
	step := func(llm.Request) llm.Response {
		sess.mu.Lock()
		runningID = sess.execution.turnID
		sess.mu.Unlock()
		sess.appendSteeringTurn(appendedText, events.SteeringKindTaskInactive)
		return agenttest.FinalResponse("continued")
	}
	sess = newTestSessionForEnvctx(t, withSteps(step))
	serveSessionWithMutationStore(t, sess)
	if err := sess.steerKind(drainedText, events.SteeringKindTaskInactive); err != nil {
		t.Fatalf("queue the steer the continuation drains: %v", err)
	}

	// The real admission path reserves the slot the way the daemon does: the
	// start is accepted but never claimed, so ActiveTurnID names a turn that is
	// not running when the continuation is admitted.
	accepted, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-continuation-owner",
		Input:            []appwire.InputItem{{Type: "text", Text: "reserve the active turn"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	held := accepted.Turn.ID
	if held == "" {
		t.Fatal("turn/start reserved no active turn; the test is not in the state it means to be")
	}

	if _, err := sess.ProcessInputKind(context.Background(), "keep going", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind(EntryContinuation): %v", err)
	}

	owners := make(map[string]string)
	sess.mu.Lock()
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnSteering && (turn.Message.Text() == drainedText || turn.Message.Text() == appendedText) {
			owners[turn.Message.Text()] = turn.OwningTurnID
		}
	}
	sess.mu.Unlock()

	if runningID == "" || runningID == held {
		t.Fatalf("continuation ran under %q, want its own execution id distinct from the held %q", runningID, held)
	}
	for _, text := range []string{drainedText, appendedText} {
		if owners[text] != runningID {
			t.Fatalf("steering %q OwningTurnID = %q, want the running execution %q (held turn %q)",
				text, owners[text], runningID, held)
		}
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != held {
		t.Fatalf("ActiveTurnID = %q after the continuation, want the held %q untouched", got, held)
	}
}
