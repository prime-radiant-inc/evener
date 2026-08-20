package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/internal/e2ecap"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestE2E_QueuedInputRunsWhileAQuestionIsPending is the whole delivery path for
// a message queued against a session that is holding an unanswered question.
//
// The entry gate refuses every non-user kind while a question is pending, so
// that a delegate or a job finishing cannot silently resolve a question the
// user is still reading. Queued input has to get past it anyway: it is the user
// speaking, and someone who types instead of answering has moved past the
// question. Only the daemon can show that the wake, the gate and the drain loop
// agree -- the agent-level test proves the claim path, and the serve loop is
// what routes the wake to it.
//
// The assertion is at the model boundary. A receipt says the daemon accepted
// the message; the next model request is what proves it ran.
func TestE2E_QueuedInputRunsWhileAQuestionIsPending(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
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
	ref := startLiveThread(ctx, t, client, stack, "EVENER-E2E-ASK-OPENING")

	// Round 1 ends by asking the user something, which leaves the question
	// pending and the session awaiting a reply.
	call, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the session's first model request: %v", err)
	}
	call.RespondToolCall("ask_user", map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which option?",
				"options": []any{
					map[string]any{"label": "Option A", "detail": "First choice"},
					map[string]any{"label": "Option B", "detail": "Second choice"},
				},
			},
		},
	})

	awaitThread(ctx, t, client, ref, "the session to hold the question", func(thread appwire.Thread) bool {
		return thread.Status.Type == appwire.ThreadStatusAwaiting
	})

	const queuedText = "EVENER-E2E-QUEUED-UNDER-ASK"
	receipt, err := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:              ref,
		ClientMutationID: newMutationID(t),
		Input:            []appwire.InputItem{{Type: "text", Text: queuedText}},
	})
	if err != nil {
		t.Fatalf("turn/queue against a session holding a question: %v", err)
	}
	if receipt.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("turn/queue disposition = %q, want %q", receipt.Receipt.Disposition, appwire.MutationDispositionApplied)
	}

	// The message has to reach the model. Accepting it and never running it is
	// the silent loss this exists to prevent: the user watched it leave the
	// composer, and it wears an Applied receipt either way.
	next, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("the queued message never reached the model: %v", err)
	}
	body, marshalErr := json.Marshal(next.Body)
	if marshalErr != nil {
		t.Fatalf("marshal the model request: %v", marshalErr)
	}
	if !strings.Contains(string(body), queuedText) {
		t.Fatalf("the model request that followed the queued message does not carry it: %s", body)
	}
	next.RespondText("acknowledged")
}
