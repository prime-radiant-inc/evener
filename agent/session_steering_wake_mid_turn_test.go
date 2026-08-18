package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Kata dz5j: steering queued against a turn that ends without communicate
// waits for the next user input.
//
// Neither of steering's two in-turn delivery paths covers a turn whose LAST
// round produces no tool calls at all: the communicate inbox
// (drainSteeringForCommunicate) requires the model to call the result tool,
// and injectDrainedSteering's post-tool-round site (injectPostToolSteering,
// session_tool_round.go) is only reached from the branch that handles a round
// WITH tool calls -- a bare-text round takes the len(calls)==0 branch
// (session_lifecycle.go) and, once the bare-text retry budget
// (maxBareTextRetries) is spent, the turn ends there without ever reaching
// either drain site.
//
// What this test settles is whether that gap actually strands the steering
// today, or whether it is already closed by wakeForPendingSteering's
// unconditional kick (agent/session_client_mutation_queue.go) -- added, per
// its own doc comment, for exactly this race ("a turn can pass its final
// steering drain and still own the turn id, so a steer arriving in that
// window would be skipped ... and then never looked at again"). The turn
// under test runs as a durable queued client mutation (turn/queue), so
// ActiveTurnID is genuinely held while the steer lands mid-turn -- the same
// shape the kata's own confirm recipe describes (a client mutation already
// told Applied, sitting behind a turn that is really running).
//
// If the wake fires and the redelivery lands, the kata's defect is already
// fixed at this commit and this test pins that; if not, it fails and names
// the gap precisely.
func TestSteeringArrivingMidTurnIsDeliveredByTheWakeAfterABareTextEnd(t *testing.T) {
	var sess *Session
	calls := 0
	var steerOnce sync.Once
	var steerErr error

	adapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(req llm.Request) llm.Response {
			calls++
			if calls == 1 {
				// Injected from inside the model call, mid-turn: the durable
				// queue claim already set ActiveTurnID (popQueueHead, before
				// ProcessInputKind ever ran), and this round has no tool call
				// of its own, so it is also strictly after this turn's only
				// possible in-turn drain point for THIS round.
				steerOnce.Do(func() {
					_, steerErr = sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
						ClientMutationID: "cm-steer-mid-turn",
						Input:            []appwire.InputItem{{Type: "text", Text: "steer mid turn"}},
					})
				})
			}
			if calls <= maxBareTextRetries+1 {
				// Bare text, no tool calls, for the whole of the first turn:
				// it never calls the result tool and never has a tool round
				// of its own, so neither in-turn steering drain site is ever
				// reached, and the bare-text retry budget exhausts.
				return llm.Response{Message: llm.Assistant("thinking, no tool yet")}
			}
			// The redelivery turn (ProcessPendingUserInput's carrier): end
			// cleanly so the test can inspect what it delivered.
			return communicateResponse(true, "done")
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess = newSession(t, withClient(client))
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	queueOneMutation(t, sess, "cm-first-turn", "go")

	var wakeMu sync.Mutex
	wakes := 0
	sess.SetPendingUserInputWakeFunc(func() {
		wakeMu.Lock()
		wakes++
		wakeMu.Unlock()
	})
	// SetPendingUserInputWakeFunc fires an immediate catch-up wake at
	// registration when work is already pending (agent/session_client_mutation.go)
	// -- exactly right for its real purpose (a message that survived a crash
	// runs without waiting on something unrelated), but it means registering
	// AFTER queueOneMutation counts one wake that has nothing to do with the
	// steer this test is about. Reset here so the count below isolates the
	// steer's own contribution.
	wakeMu.Lock()
	wakes = 0
	wakeMu.Unlock()

	// The first turn: the durably queued message, claimed and run.
	if _, _, err := sess.ProcessPendingUserInput(context.Background(), nil); steerErr != nil {
		t.Fatalf("AcceptClientMutationSteer (from inside the model call): %v", steerErr)
	} else if !errors.Is(err, errBareTextWithoutResultTool) {
		t.Fatalf("first turn error = %v, want errBareTextWithoutResultTool -- this test only exercises the gap when the turn ends without a result-tool call", err)
	}

	// The premise: the turn's own delivery paths did not drain it, and it was
	// genuinely a running, durably-claimed turn while the steer landed. If
	// either fails, the test is not exercising the shape the kata describes.
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the first turn ended, want it released (this assertion runs after the turn returns, so it only confirms cleanup -- see the mid-turn check inside the adapter for the live claim)", got)
	}
	if !sess.hasPendingSteering() {
		t.Fatal("steering was already delivered by the turn itself; this test's premise (a turn that ends bare-text never drains it) does not hold")
	}

	wakeMu.Lock()
	gotWakes := wakes
	wakeMu.Unlock()
	if gotWakes == 0 {
		t.Fatal("accepting a steer mid-turn did not wake the pending-user-input path; nothing will ever redeliver it once the turn ends")
	}

	// This is the redelivery the wake is FOR: the daemon's serve loop reacts
	// to the wake by calling ProcessPendingUserInput (cmd/evener/serve.go). The
	// wake's own delivery through the guaranteed 1-slot channel is proven
	// independently by server/pending_user_input_wake_test.go; this call
	// exercises the agent-side half of the same contract.
	if _, ran, err := sess.ProcessPendingUserInput(context.Background(), nil); err != nil {
		t.Fatalf("ProcessPendingUserInput: %v", err)
	} else if !ran {
		t.Fatal("the wake's redelivery named no runnable work; the steering that arrived mid-turn is stranded until the user speaks again")
	}

	if sess.hasPendingSteering() {
		t.Fatal("steering is still pending after the redelivery ran")
	}
	if got := countBudgetSteering(budgetHistory(sess), "steer mid turn"); got != 1 {
		t.Fatalf("delivered steering count = %d, want 1", got)
	}
}
