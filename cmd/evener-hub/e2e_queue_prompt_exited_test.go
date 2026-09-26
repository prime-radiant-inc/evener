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

// TestE2E_QueuePromptToARetiredSession drives the route the web composer
// chooses for a message sent while it already has a send in flight against a
// finished session: deriveSendQueueAvailability answers queue-mode for a
// terminal status whenever this client has its own send pending
// (appwire-client/typescript/sendQueueAvailability.ts), so the composer emits
// turn/queue.
//
// This asserts what that queued prompt actually does. The hub's turn/queue
// carried no auto-resume while turn/start did, so a queued prompt to an
// exited session used to be refused outright ("thread not found") — the
// session was never resumed for it and the message never ran.
func TestE2E_QueuePromptToARetiredSession(t *testing.T) {
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

	ref := startSessionWithOpeningTurn(ctx, t, client, provider, stack)

	// Exit the daemon.
	list, err := clientRequest[appwire.DaemonListResponse](ctx, client, appwire.MethodEvenerDaemonList, appwire.DaemonListParams{})
	if err != nil {
		t.Fatalf("evener/daemon/list: %v", err)
	}
	identity, found := daemonIdentityForRef(list, ref)
	if !found {
		t.Fatalf("no resident daemon for %s in %+v", ref, list.Daemons)
	}
	if _, err := clientRequest[appwire.DaemonRetireResponse](ctx, client, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{Identity: identity}); err != nil {
		t.Fatalf("evener/daemon/retire: %v", err)
	}
	if err := awaitDaemonGone(ctx, client, ref); err != nil {
		t.Fatalf("the daemon never exited: %v", err)
	}

	const prompt = "EVENER-E2E-QUEUED-TO-EXITED"
	resp, queueErr := clientRequest[appwire.TurnQueueResponse](ctx, client, appwire.MethodTurnQueue, appwire.TurnQueueParams{
		Ref:                ref,
		ClientMutationID:   newMutationID(t),
		ExpectedInstanceID: localInstanceIDForTestRef(ref),
		Input:              []appwire.InputItem{{Type: "text", Text: prompt}},
	})
	t.Logf("turn/queue against the exited session: err=%v disposition=%q", queueErr, resp.Receipt.Disposition)
	if queueErr != nil {
		t.Fatalf("turn/queue against the exited session: %v", queueErr)
	}
	if disp := resp.Receipt.Disposition; disp != appwire.MutationDispositionApplied && disp != appwire.MutationDispositionReplayed {
		t.Fatalf("turn/queue disposition = %q, want %q or %q", disp, appwire.MutationDispositionApplied, appwire.MutationDispositionReplayed)
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, 45*time.Second)
	defer cancelWait()
	next, nextErr := provider.Next(waitCtx.Done())
	if nextErr != nil {
		t.Fatalf("the queued prompt never reached the model (turn/queue err=%v): %v", queueErr, nextErr)
	}
	if !next.Contains(prompt) {
		t.Fatalf("the model request does not carry the queued prompt; messages:\n%s", strings.Join(next.Texts(), "\n"))
	}
}

// startSessionWithOpeningTurn opens a session through startLiveThread and
// drives one complete turn through the fake provider, returning the session's
// ref. The start and its shutdown cleanup belong to that helper, so the leak
// guard and the thread/start checks keep one definition.
func startSessionWithOpeningTurn(ctx context.Context, t *testing.T, client *appwire.Client, provider *fakellm.Server, stack hubStack) string {
	t.Helper()
	opening := fmt.Sprintf("EVENER-E2E-OPENING-%d", time.Now().UnixNano())
	ref := startLiveThread(ctx, t, client, stack, opening)

	round, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the opening model request: %v", err)
	}
	if !round.Contains(opening) {
		t.Fatalf("the opening prompt never reached the model; messages:\n%s", strings.Join(round.Texts(), "\n"))
	}
	round.RespondToolCall("communicate", communicateArgs("opening turn done"))
	awaitThread(ctx, t, client, ref, "the opening turn to finish", func(thread appwire.Thread) bool {
		return thread.Status.Type != "active"
	})
	return ref
}

// awaitDaemonGone waits until the roster no longer reports the daemon serving
// ref.
func awaitDaemonGone(ctx context.Context, client *appwire.Client, ref string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		list, err := clientRequest[appwire.DaemonListResponse](ctx, client, appwire.MethodEvenerDaemonList, appwire.DaemonListParams{})
		if err == nil {
			if _, found := daemonIdentityForRef(list, ref); !found {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return context.DeadlineExceeded
}

// daemonIdentityForRef resolves the resident daemon serving ref, which is the
// exact ownership identity evener/daemon/retire demands. It lives here rather
// than beside the other e2e helpers so this test carries its own scaffolding:
// the branch that adds this file has to compile against main on its own.
func daemonIdentityForRef(list appwire.DaemonListResponse, ref string) (appwire.DaemonIdentity, bool) {
	for _, resident := range list.Daemons {
		if resident.Identity.Ref == ref {
			return resident.Identity, true
		}
	}
	return appwire.DaemonIdentity{}, false
}
