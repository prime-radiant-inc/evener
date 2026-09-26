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

// TestE2E_SendPromptToARetiredSession reproduces the reported defect: with the
// session's daemon exited, sending a prompt resumes the session but the prompt
// is never delivered to it.
//
// The assertion is made at the model boundary, the only place that cannot lie:
// if turn/start reached the session, the fake provider receives a request
// carrying the prompt text. A receipt alone proves nothing.
//
// The daemon is retired through the daemon's own retirement authority
// (evener/daemon/retire), which is the same exit an idle retirement produces
// and leaves no recovery fence behind. The prompt is then sent at the two ends
// of that exit: the moment retirement is accepted (the daemon is still alive
// and retiring), and once the roster reports the daemon gone. Fixed sleeps only
// sampled arbitrary offsets into the window -- and sampled them by wall-clock
// timing, so on a slow machine they could miss the state they named -- while
// these two are the deterministic ends of it.
func TestE2E_SendPromptToARetiredSession(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	client := stack.dialRPC(ctx, t)

	const openingPrompt = "EVENER-E2E-RETIRED-OPENING"

	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness:         "evener",
		CWD:             stack.workDir,
		Input:           []appwire.InputItem{{Type: "text", Text: openingPrompt}},
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
		_, _ = clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: ref})
	})

	round1, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatalf("waiting for the opening model request: %v", err)
	}
	if !round1.Contains(openingPrompt) {
		t.Fatalf("the opening prompt never reached the model; messages:\n%s", strings.Join(round1.Texts(), "\n"))
	}
	round1.RespondToolCall("communicate", communicateArgs("opening turn done"))
	awaitThread(ctx, t, client, ref, "the opening turn to finish", func(thread appwire.Thread) bool {
		return thread.Status.Type != "active"
	})

	// Each round retires the daemon and then sends a prompt at one of the two
	// deterministic points in the resulting exit: waitGone false is "retirement
	// was just accepted", and true is "the roster reports the daemon gone".
	for round, waitGone := range []bool{false, true} {
		prompt := fmt.Sprintf("EVENER-E2E-RETIRED-PROMPT-%d", round)

		list, err := clientRequest[appwire.DaemonListResponse](ctx, client, appwire.MethodEvenerDaemonList, appwire.DaemonListParams{})
		if err != nil {
			t.Fatalf("round %d: evener/daemon/list: %v", round, err)
		}
		identity, ok := residentIdentityForRef(list, ref)
		if !ok {
			t.Fatalf("round %d: no resident daemon for %s in %+v", round, ref, list.Daemons)
		}
		retired, err := clientRequest[appwire.DaemonRetireResponse](ctx, client, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{Identity: identity})
		if err != nil {
			t.Fatalf("round %d: evener/daemon/retire: %v", round, err)
		}
		t.Logf("round %d (waitGone=%v): retire accepted=%v phase=%q", round, waitGone, retired.Accepted, retired.Lifecycle.Phase)

		if waitGone {
			// Poll the roster rather than sleep: the send below must land after
			// the exit is complete, and the roster is what says so.
			goneAt := time.Now()
			for {
				list, err := clientRequest[appwire.DaemonListResponse](ctx, client, appwire.MethodEvenerDaemonList, appwire.DaemonListParams{})
				if err == nil {
					if _, still := residentIdentityForRef(list, ref); !still {
						break
					}
				}
				if time.Since(goneAt) > 30*time.Second {
					t.Fatalf("round %d: the retired daemon never left the roster", round)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}

		resp, startErr := clientRequest[appwire.TurnStartResponse](ctx, client, appwire.MethodTurnStart, appwire.TurnStartParams{
			Ref:                ref,
			ClientMutationID:   newMutationID(t),
			ExpectedInstanceID: localInstanceIDForTestRef(ref),
			Input:              []appwire.InputItem{{Type: "text", Text: prompt}},
		})
		t.Logf("round %d (waitGone=%v): turn/start err=%v disposition=%q threadID=%q", round, waitGone, startErr, resp.Receipt.Disposition, resp.Receipt.ThreadID)

		// Decisive: the prompt must reach the model, whether or not the call
		// above reported an error.
		waitCtx, cancelWait := context.WithTimeout(ctx, 60*time.Second)
		next, nextErr := provider.Next(waitCtx.Done())
		cancelWait()
		if nextErr != nil {
			t.Fatalf("round %d (waitGone=%v): the prompt never reached the model (turn/start err=%v): %v", round, waitGone, startErr, nextErr)
		}
		if !next.Contains(prompt) {
			t.Fatalf("round %d (waitGone=%v): the model request does not carry the prompt; messages:\n%s",
				round, waitGone, strings.Join(next.Texts(), "\n"))
		}
		next.RespondToolCall("communicate", communicateArgs("done"))
		awaitThread(ctx, t, client, ref, "the turn to finish", func(thread appwire.Thread) bool {
			return thread.Status.Type != "active"
		})
	}
}

// residentIdentityForRef finds the resident daemon serving ref.
func residentIdentityForRef(list appwire.DaemonListResponse, ref string) (appwire.DaemonIdentity, bool) {
	want := localInstanceIDForTestRef(ref)
	for _, resident := range list.Daemons {
		if resident.Identity.Ref == ref || resident.Identity.Ref == "local:"+want {
			return resident.Identity, true
		}
	}
	return appwire.DaemonIdentity{}, false
}
