package hub

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestE2E_SendPromptAfterDaemonDiedMidTurn is the reported flow, in the state a
// daemon that dies while a turn is in flight leaves behind: a durable pending
// turn/start whose turn never finished.
//
// The claim under test is that the caller's NEW prompt runs once the session is
// resumed. A resumed session that keeps the dead turn's claim (the durable
// snapshot names an ActiveTurnID, and that turn's own pending start owns it, so
// load deliberately preserves it) can instead reject the new turn/start with
// Conflict("turn is already active") — the session is live, the new prompt is
// refused, and the client returns the text to the composer.
//
// The assertion is made in two steps at the model boundary, the only place that
// cannot lie, and it pins ORDER as well as delivery: the recovered prompt must
// reach the model first and the user's new prompt after it. The recovered
// turn's id was reserved before the crash, so it is the older of the two, and
// the new prompt was admitted behind it.
func TestE2E_SendPromptAfterDaemonDiedMidTurn(t *testing.T) {
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

	const (
		deadPrompt = "EVENER-E2E-MIDTURN-DEAD"
		newPrompt  = "EVENER-E2E-MIDTURN-NEW"
	)

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "evener",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: deadPrompt}},
		Model:           stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	ref := started.Thread.Evener.Ref
	t.Cleanup(func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelShutdown()
		_, _ = clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref})
	})

	// Round 1 arrives and is deliberately left unanswered: the turn is in
	// flight when the daemon dies.
	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the first model request: %v", err)
	}
	if !round1.Contains(deadPrompt) {
		t.Fatalf("the first model request does not carry the opening prompt; messages:\n%s", strings.Join(round1.Texts(), "\n"))
	}

	list, err := clientRequest[appwire.DaemonListResponse](ctx, client, appwire.MethodEvenerDaemonList, appwire.DaemonListParams{})
	if err != nil {
		t.Fatalf("evener/daemon/list: %v", err)
	}
	identity, ok := residentIdentityForRef(list, ref)
	if !ok {
		t.Fatalf("no resident daemon for %s in %+v", ref, list.Daemons)
	}
	// SIGKILL, not a force stop: this is the ungraceful exit whose durable
	// state (an active-turn claim, a pending start) the restore has to
	// reconcile, and it leaves no recovery obligation behind.
	t.Logf("killing daemon pid=%d mid-turn", identity.PID)
	if err := daemonRetirementKillPID(identity.PID); err != nil {
		t.Fatalf("kill daemon pid %d: %v", identity.PID, err)
	}
	// Wait for the roster to drop the killed daemon: the send below is only the
	// reported flow once the hub reads this session as exited.
	killedAt := time.Now()
	for {
		list, err := clientRequest[appwire.DaemonListResponse](ctx, client, appwire.MethodEvenerDaemonList, appwire.DaemonListParams{})
		if err == nil {
			if _, still := residentIdentityForRef(list, ref); !still {
				break
			}
		}
		if time.Since(killedAt) > 30*time.Second {
			t.Fatal("the killed daemon never left the roster")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The user's next prompt. This is the send the report is about.
	resp, startErr := clientRequest[appwire.TurnStartResponse](ctx, client, appwire.MethodTurnStart, appwire.TurnStartParams{
		Ref:                ref,
		ClientMutationID:   newMutationID(t),
		ExpectedInstanceID: localInstanceIDForTestRef(ref),
		Input:              []appwire.InputItem{{Type: "text", Text: newPrompt}},
	})
	t.Logf("turn/start after the mid-turn kill: err=%v disposition=%q", startErr, resp.Receipt.Disposition)

	// Ordering is the claim under test, not merely delivery. The resumed
	// session's first model request must be the recovered (dead) prompt: that
	// turn was reserved first -- its id has the lower sequence, minted before
	// the crash -- and the user's new prompt was admitted behind it. A resume
	// that runs them in the other order executes the user's latest words against
	// the dead turn's state and leaves the stale prompt trailing after them,
	// which is not what was promised.
	//
	// The assertion is on the NEWEST user message of each request, not
	// Call.Contains. Contains searches the whole conversation, so the dead
	// prompt replayed from round one satisfies it even when this round ran the
	// new prompt. The newest user message is the one that names what the round
	// actually ran, so the recovered prompt must be it and the new prompt, which
	// has not run yet, must not be.
	recoveredWaitCtx, cancelRecoveredWait := context.WithTimeout(ctx, 60*time.Second)
	defer cancelRecoveredWait()
	recovered, recoveredErr := provider.Next(recoveredWaitCtx.Done())
	if recoveredErr != nil {
		t.Fatalf("the recovered turn never reached the model (turn/start err=%v): %v", startErr, recoveredErr)
	}
	recoveredNewestUser := newestUserText(recovered)
	t.Logf("model request 1 newest user message carries dead=%v new=%v",
		strings.Contains(recoveredNewestUser, deadPrompt), strings.Contains(recoveredNewestUser, newPrompt))
	if !strings.Contains(recoveredNewestUser, deadPrompt) || strings.Contains(recoveredNewestUser, newPrompt) {
		t.Fatalf("the resumed session ran the new prompt BEFORE the recovered one; newest user message=%q turn/start err=%v messages:\n%s",
			recoveredNewestUser, startErr, strings.Join(recovered.Texts(), "\n"))
	}
	recovered.RespondToolCall("communicate", communicateArgs("recovered turn done"))

	// Only then does the prompt the user sent run. There is deliberately no wait
	// for the thread to fall idle here: the follow-up it was admitted behind is
	// already the next active turn and is sitting at its own model request, so
	// waiting for an idle thread would wait for a request this test is the one
	// that must answer. Its newest user message must be the new prompt, with no
	// dead prompt trailing it. This wait gets a FRESH budget: sharing one
	// deadline with the recovered turn's wait would let time spent there eat
	// this one's and flake the test on a slow host.
	nextWaitCtx, cancelNextWait := context.WithTimeout(ctx, 60*time.Second)
	defer cancelNextWait()
	next, nextErr := provider.Next(nextWaitCtx.Done())
	if nextErr != nil {
		t.Fatalf("the new prompt never reached the model after the recovered turn (turn/start err=%v): %v", startErr, nextErr)
	}
	nextNewestUser := newestUserText(next)
	t.Logf("model request 2 newest user message carries dead=%v new=%v",
		strings.Contains(nextNewestUser, deadPrompt), strings.Contains(nextNewestUser, newPrompt))
	if !strings.Contains(nextNewestUser, newPrompt) || strings.Contains(nextNewestUser, deadPrompt) {
		t.Fatalf("the model request after the recovered turn is not the new prompt; newest user message=%q turn/start err=%v messages:\n%s",
			nextNewestUser, startErr, strings.Join(next.Texts(), "\n"))
	}
}

// newestUserText returns the text of the last user-role message in a model
// request -- the prompt the round is answering. Call.Contains searches the whole
// conversation, so a prompt replayed from an earlier round satisfies it even
// when the round under test ran a different prompt; the newest user message is
// the one that names what this round actually ran.
func newestUserText(call *fakellm.Call) string {
	lines := call.Texts()
	for _, line := range slices.Backward(lines) {
		if text, ok := strings.CutPrefix(line, "user: "); ok {
			return text
		}
	}
	return ""
}
