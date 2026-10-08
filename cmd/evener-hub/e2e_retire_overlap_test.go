package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestE2E_RetireOverlappedByAnotherOwnershipAction pins the contract #4052
// gives the hub: it no longer holds a session's alias locks while a retire it
// forwarded waits in the daemon (on a held session namer, #3921), so a
// resume, force stop or deletion can overlap that retire. Each ends in a
// consistent state, a stopped or re-resumed daemon and never one left
// half-claimed: the session resumes afterwards, resident. It pins those end
// states; that the overlapping action no longer waits on the retire is pinned
// deterministically by TestDaemonRetireInFlightHoldsNoSessionLock.
func TestE2E_RetireOverlappedByAnotherOwnershipAction(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a hub + daemon")
	}
	for _, overlap := range []string{"force stop", "delete", "resume"} {
		t.Run(overlap, func(t *testing.T) {
			provider, err := fakellm.New()
			if err != nil {
				t.Fatalf("start fake provider: %v", err)
			}
			t.Cleanup(provider.Close)
			stack := startHubStack(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			client := stack.dialRPC(ctx, t)
			releaseNamer := provider.HoldNamer()
			defer releaseNamer()
			ref := startSessionWithOpeningTurn(ctx, t, client, provider, stack)

			identity := residentDaemonIdentity(ctx, t, client, ref)
			retired := make(chan overlappedRetire, 1)
			go func() {
				resp, err := clientRequest[appwire.DaemonRetireResponse](ctx, client, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{Identity: identity})
				retired <- overlappedRetire{resp, err}
			}()

			switch overlap {
			case "force stop":
				// From its own connection, as a second client would stop it.
				stopper := stack.dialRPC(ctx, t)
				if _, err := clientRequest[appwire.EmptyResponse](ctx, stopper, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref}); err != nil {
					t.Fatalf("force stop during the retire: %v", err)
				}
				releaseNamer()
				if got := <-retired; got.err == nil && got.resp.Accepted {
					t.Logf("the retire was accepted before the stop landed")
				}
			case "delete":
				deleted, err := clientRequest[appwire.SessionDeleteResponse](ctx, client, appwire.MethodEvenerSessionDelete, appwire.SessionDeleteParams{Ref: ref})
				if err != nil {
					t.Fatalf("delete during the retire: %v", err)
				}
				if len(deleted.Deleted) != 0 {
					t.Fatalf("delete during the retire deleted %v, want the live session skipped", deleted.Deleted)
				}
				assertRetireStillPending(t, retired)
				releaseNamer()
				if got := <-retired; got.err != nil || !got.resp.Accepted {
					t.Fatalf("retire after a skipped delete = accepted=%t %v, want accepted once the namer settled", got.resp.Accepted, got.err)
				}
			case "resume":
				if _, err := clientRequest[appwire.ThreadResumeResponse](ctx, client, appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: ref}); err != nil {
					t.Fatalf("resume during the retire: %v", err)
				}
				assertRetireStillPending(t, retired)
				releaseNamer()
				if got := <-retired; got.err != nil || !got.resp.Accepted {
					t.Fatalf("retire after an overlapping resume = accepted=%t %v, want accepted once the namer settled", got.resp.Accepted, got.err)
				}
			}
			if err := awaitDaemonGone(ctx, client, ref); err != nil {
				t.Fatalf("the daemon is still resident after the %s and the retire: %v", overlap, err)
			}
			// Nothing was left half-claimed: the session resumes, resident. A
			// force-stopped session takes its explicit Resume on a fresh
			// connection, as that contract requires.
			resumer := client
			if overlap == "force stop" {
				resumer = stack.dialRPC(ctx, t)
			}
			if _, err := clientRequest[appwire.ThreadResumeResponse](ctx, resumer, appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: ref}); err != nil {
				t.Fatalf("resume after the %s and the retire: %v", overlap, err)
			}
			list, err := clientRequest[appwire.DaemonListResponse](ctx, client, appwire.MethodEvenerDaemonList, appwire.DaemonListParams{})
			if err != nil {
				t.Fatalf("evener/daemon/list: %v", err)
			}
			for _, row := range list.Daemons {
				if row.Identity.Ref == ref && row.Lifecycle != nil && row.Lifecycle.Phase != "resident" {
					t.Fatalf("the re-resumed daemon is in phase %q, want resident", row.Lifecycle.Phase)
				}
			}
			if _, found := residentIdentityForRef(list, ref); !found {
				t.Fatalf("no resident daemon for %s after the resume: %+v", ref, list.Daemons)
			}
		})
	}
}

type overlappedRetire struct {
	resp appwire.DaemonRetireResponse
	err  error
}

// assertRetireStillPending requires the retire to be unanswered: the daemon
// holds it while the session namer is held, so an action that returned
// meanwhile overlapped it.
func assertRetireStillPending(t *testing.T, retired <-chan overlappedRetire) {
	t.Helper()
	select {
	case got := <-retired:
		t.Fatalf("the retire answered (accepted=%t, %v) while the namer was held, so nothing overlapped it", got.resp.Accepted, got.err)
	default:
	}
}
