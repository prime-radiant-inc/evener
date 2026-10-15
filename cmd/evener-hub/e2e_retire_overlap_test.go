package hub

import (
	"context"
	"errors"
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
	// The archive overlap is not here: its retire outcome turns on whether the
	// retire's hub-side check (archivedRetireRefusal) or the archive's decision
	// wins the session's alias lock, and two connections cannot order that.
	// TestDaemonRetireCheckedBeforeAnOverlappingArchive pins it deterministically
	// by gating the daemon's answer, so the retire is provably past its check.
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
			// The client's request-id observer fires when the retire request is
			// minted, before its frame is sent, so the retire is issued before
			// the probe below.
			issued := make(chan struct{})
			retired := make(chan overlappedRetire, 1)
			go func() {
				retireCtx := appwire.WithRequestIDObserver(ctx, func(appwire.ID) { close(issued) })
				resp, err := clientRequest[appwire.DaemonRetireResponse](retireCtx, client, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{Identity: identity})
				retired <- overlappedRetire{resp, err}
			}()
			awaitRetireInProgress(ctx, t, client, ref, issued, retired)
			// The overlapping action comes from its own connection, as a
			// second client's would, so this pins an overlap between two
			// connections rather than a queue on one.
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

// awaitRetireInProgress returns once the retire issued on client is being
// served and a later thread/read on that same connection is answered while the
// retire is still unanswered. The retire now runs off the connection's serial
// worker (#4080), so it no longer parks the connection: the old probe — a
// thread/read that times out behind it — relied on the stall #4080 removes.
// issued is closed by the client's request-id observer when the retire request
// is minted, before its frame is sent, so the retire was issued before the
// read; the read's own answer is what this checks, never the write order. That
// the retire also reached the daemon is pinned deterministically, not here:
// TestDaemonRetireInFlightHoldsNoSessionLock and
// TestDaemonRetireDoesNotHoldALaterRequestOnItsConnection gate the daemon's own
// answer and read the state while it is held. The namer stays held here, so
// the retire cannot be accepted before this test releases it. It fails if the
// retire answers first.
func awaitRetireInProgress(ctx context.Context, t *testing.T, client *appwire.Client, ref string, issued <-chan struct{}, retired <-chan overlappedRetire) {
	t.Helper()
	select {
	case <-issued:
	case got := <-retired:
		t.Fatalf("the retire answered (accepted=%t, %v) before it was issued", got.resp.Accepted, got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("the retire was never issued on the connection")
	}
	// A read on the retire's connection must be answered while the retire
	// waits; only a request parked behind the retire times out.
	deadline := time.Now().Add(30 * time.Second)
	for {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		_, err := clientRequest[appwire.ThreadReadResponse](probe, client, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: ref})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			break // answered, by the daemon or by a refusal
		}
		if time.Now().After(deadline) {
			t.Fatal("a thread/read on the retire's connection was never served while the retire waited (#4080)")
		}
	}
	select {
	case got := <-retired:
		t.Fatalf("the retire answered (accepted=%t, %v) before anything could overlap it", got.resp.Accepted, got.err)
	default:
	}
}
