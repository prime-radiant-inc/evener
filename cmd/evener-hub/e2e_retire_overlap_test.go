package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestE2E_RetireOverlappedByAnotherOwnershipAction pins the contract #4052
// gives the hub: it holds none of a session's alias locks while a retire it
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
	for _, overlap := range []string{"force stop", "delete", "resume", "archive"} {
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
			awaitRetireInProgress(ctx, t, client, ref, retired)
			// The overlapping action comes from its own connection, as a
			// second client's would: a connection serves its requests in
			// order, so one sharing the retire's would only queue behind it.
			other := stack.dialRPC(ctx, t)
			// Well inside the namer's 15s hold, so an action that waited
			// behind the retire fails rather than racing its answer.
			actionCtx, cancelAction := context.WithTimeout(ctx, 8*time.Second)
			defer cancelAction()

			switch overlap {
			case "force stop":
				_, err = clientRequest[appwire.EmptyResponse](actionCtx, other, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref})
			case "delete":
				var deleted appwire.SessionDeleteResponse
				deleted, err = clientRequest[appwire.SessionDeleteResponse](actionCtx, other, appwire.MethodEvenerSessionDelete, appwire.SessionDeleteParams{Ref: ref})
				if err == nil && len(deleted.Deleted) != 0 {
					t.Fatalf("delete during the retire deleted %v, want the live session skipped", deleted.Deleted)
				}
			case "resume":
				_, err = clientRequest[appwire.ThreadResumeResponse](actionCtx, other, appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: ref})
			case "archive":
				// The retire was checked before the archive, so it stands: an
				// archived session's daemon retires anyway.
				_, err = clientRequest[appwire.ArchiveResponse](actionCtx, other, appwire.MethodEvenerArchiveSet, appwire.ArchiveParams{
					Kind: appwire.ArchiveTargetSession, ID: strings.TrimPrefix(ref, "local:"), WorkingDir: stack.workDir, Archived: true,
				})
			}
			if err != nil {
				t.Fatalf("%s during the retire: %v", overlap, err)
			}
			if overlap == "force stop" {
				releaseNamer()
				// The stop cancels the forwarded retire before ending the daemon.
				if got := <-retired; got.err == nil {
					t.Fatalf("retire overlapped by a force stop = accepted=%t, want its forward cancelled", got.resp.Accepted)
				}
			} else {
				assertRetireStillPending(t, retired)
				releaseNamer()
				if got := <-retired; got.err != nil || !got.resp.Accepted {
					t.Fatalf("retire overlapped by a %s = accepted=%t %v, want accepted once the namer settled", overlap, got.resp.Accepted, got.err)
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
				if row.Identity.Ref != ref {
					continue
				}
				if row.Lifecycle != nil && row.Lifecycle.Phase != "resident" {
					t.Fatalf("the re-resumed daemon is in phase %q, want resident", row.Lifecycle.Phase)
				}
				// The archive overlapping the retire persisted its decision.
				if row.Archived != (overlap == "archive") {
					t.Fatalf("the re-resumed daemon's row archived=%t after the %s", row.Archived, overlap)
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

// assertRetireStillPending requires the retire to be unanswered after the
// overlapping action returned: the daemon holds it while the session namer is
// held, and it was already in progress (awaitRetireInProgress).
func assertRetireStillPending(t *testing.T, retired <-chan overlappedRetire) {
	t.Helper()
	select {
	case got := <-retired:
		t.Fatalf("the retire answered (accepted=%t, %v) before the overlapping action returned, so the action waited on it", got.resp.Accepted, got.err)
	default:
	}
}

// awaitRetireInProgress returns once the retire sent on client is being
// served: a connection serves a thread/read after an earlier request, so a
// short thread/read on the same connection times out behind the retire. The
// probe repeats until one does, since the first can reach the hub before the
// retire; it fails if the retire answers first. It relies on retire being
// served on the connection's serial lane: when #4080 moves it off, this needs
// another signal that the retire is in flight.
func awaitRetireInProgress(ctx context.Context, t *testing.T, client *appwire.Client, ref string, retired <-chan overlappedRetire) {
	t.Helper()
	for range 30 {
		behind, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, err := clientRequest[appwire.ThreadReadResponse](behind, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
		cancel()
		if err != nil {
			return
		}
		select {
		case got := <-retired:
			t.Fatalf("the retire answered (accepted=%t, %v) before anything could overlap it", got.resp.Accepted, got.err)
		default:
		}
	}
	t.Fatal("no thread/read on the retire's connection ever waited behind it")
}
