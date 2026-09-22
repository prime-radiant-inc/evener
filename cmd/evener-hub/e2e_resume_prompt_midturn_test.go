package hub

import (
	"context"
	"fmt"
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

	// Whichever turn the resumed session runs first, the NEW prompt must be
	// what reaches the model next (after at most the re-run of the dead turn).
	waitCtx, cancelWait := context.WithTimeout(ctx, 60*time.Second)
	defer cancelWait()
	for attempt := 0; attempt < 2; attempt++ {
		next, nextErr := provider.Next(waitCtx.Done())
		if nextErr != nil {
			t.Fatalf("the new prompt never reached the model (turn/start err=%v): %v", startErr, nextErr)
		}
		t.Logf("model request %d carries dead=%v new=%v", attempt, next.Contains(deadPrompt), next.Contains(newPrompt))
		if next.Contains(newPrompt) {
			return
		}
		next.RespondToolCall("communicate", communicateArgs(fmt.Sprintf("run %d done", attempt)))
	}
	t.Fatalf("the new prompt never reached the model after the resume; turn/start err=%v", startErr)
}
