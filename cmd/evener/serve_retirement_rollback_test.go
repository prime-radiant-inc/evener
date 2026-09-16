package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener/internal/rvreg"
	"primeradiant.com/evener/rendezvous"
)

// TestServeRetirementEligibilityWaitsForInitialRegistration pins M1 of roborev
// round 10: the daemon-owned idle clock must not start before the initial
// rendezvous entry is published. The real ordering is driven by holding
// deps.register open; the observable is the controller's own timer arm, so a
// daemon that becomes eligible while registration is still pending fails.
func TestServeRetirementEligibilityWaitsForInitialRegistration(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	clk := newServeRetireClock()
	deps.retirementClock = clk
	rec := newRetireEventRecorder()
	deps.retirementObserve = rec.observe
	args = append(args, "--daemon-idle-timeout", "1h")
	runDir := serveArgValue(args, "--run-dir")

	registered := make(chan struct{})
	releaseRegister := make(chan struct{})
	var releaseOnce sync.Once
	// Even a t.Fatal must not leave the serve goroutine parked in register.
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseRegister) }) })
	realRegister := deps.register
	deps.register = func(reg *rvreg.Registration, dir string, entry rendezvous.Entry) error {
		close(registered)
		<-releaseRegister
		return realRegister(reg, dir, entry)
	}

	done := runRetireServe(t, deps, state, args)
	rec.await(t, "root_published")

	select {
	case <-registered:
	case <-time.After(15 * time.Second):
		t.Fatal("daemon never reached its initial rendezvous registration")
	}
	// Registration is held: the idle clock must not have armed. Before the fix
	// the controller loop started ~650 lines earlier and armed here, so a short
	// timeout could claim and cancel the context before the entry existed.
	clk.assertNoArmWithin(t, 500*time.Millisecond)

	releaseOnce.Do(func() { close(releaseRegister) })
	if d := clk.awaitArm(t); d != time.Hour {
		t.Fatalf("retirement arm after registration = %v, want the configured 1h", d)
	}
	if _, err := rendezvous.List(runDir); err != nil {
		t.Fatalf("rendezvous list after registration: %v", err)
	}

	state.srv.shutdown()
	if err := <-done; err != nil {
		t.Fatalf("serve exit: %v", err)
	}
}

// TestServeRetirementCommitFailureRollsBack pins M2 of roborev round 10: a
// failed retirement.Commit must abort the claim (so the controller returns to
// "resident" and future retirement is not blocked) and must release the exit
// reservation reserveRetirementExit took (so a later shutdown still closes the
// live session instead of leaking it).
func TestServeRetirementCommitFailureRollsBack(t *testing.T) {
	newCommitFailureDeps := func(t *testing.T) (serveDeps, *clearTestState, []string, *retireEventRecorder, *atomic.Bool) {
		t.Helper()
		deps, state, args := newClearServeDeps(t)
		clk := newServeRetireClock()
		deps.retirementClock = clk
		rec := newRetireEventRecorder()
		deps.retirementObserve = rec.observe
		var failCommit atomic.Bool
		failCommit.Store(true)
		deps.retirementCommit = func(c *agent.RetirementController, claim *agent.RetirementClaim) error {
			if failCommit.Load() {
				return errors.New("injected retirement commit failure")
			}
			return c.Commit(claim)
		}
		return deps, state, args, rec, &failCommit
	}

	t.Run("failed commit leaves the controller resumable", func(t *testing.T) {
		deps, state, args, rec, failCommit := newCommitFailureDeps(t)
		done := runRetireServe(t, deps, state, args)
		rec.await(t, "root_published")
		entry := awaitRendezvousEntry(t, serveArgValue(args, "--run-dir"))
		awaitRetirementSettled(t, state.srv)

		if _, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
			appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)}); err == nil {
			t.Fatal("retire with an injected commit failure returned no error")
		}

		out, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
		if err != nil {
			t.Fatalf("status after a failed commit: %v", err)
		}
		status, ok := out.(appwire.DaemonStatusResponse)
		if !ok {
			t.Fatalf("status result = %T, want DaemonStatusResponse", out)
		}
		if status.Lifecycle.Phase != "resident" {
			t.Fatalf("phase after a failed commit = %q, want %q: the claim must abort, or every future retirement is refused while phase != resident",
				status.Lifecycle.Phase, "resident")
		}
		if n := rec.count("released"); n != 0 {
			t.Fatalf("released events after a failed commit = %d, want 0", n)
		}

		// A later retirement is not blocked: the injection is lifted and the
		// daemon must accept and complete a real retirement.
		failCommit.Store(false)
		out, err = dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
			appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)})
		if err != nil {
			t.Fatalf("retire after a rolled-back commit failure: %v", err)
		}
		resp, ok := out.(appwire.DaemonRetireResponse)
		if !ok || !resp.Accepted {
			t.Fatalf("retire after a rolled-back commit failure = %+v, want accepted", out)
		}
		rec.await(t, "released")
		if err := <-done; err != nil {
			t.Fatalf("serve exit: %v", err)
		}
	})

	t.Run("failed commit leaves shutdown able to close the live session", func(t *testing.T) {
		deps, state, args, rec, _ := newCommitFailureDeps(t)
		serveCtx, cancelServe := context.WithCancel(context.Background())
		t.Cleanup(cancelServe)
		deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			return serveCtx, cancelServe
		}
		done := runRetireServe(t, deps, state, args)
		rec.await(t, "root_published")
		entry := awaitRendezvousEntry(t, serveArgValue(args, "--run-dir"))
		awaitRetirementSettled(t, state.srv)

		if _, err := dispatchDaemonRPC(state.srv, appwire.MethodEvenerDaemonRetire,
			appwire.DaemonRetireParams{Identity: daemonIdentityFor(entry)}); err == nil {
			t.Fatal("retire with an injected commit failure returned no error")
		}

		// The process did not commit, so the exit is still undecided and
		// shutdown's single pass must close the live session.
		cancelServe()
		if err := <-done; err != nil {
			t.Fatalf("serve exit: %v", err)
		}
		if got := state.session(0).State(); got != agent.SessionClosed {
			t.Fatalf("live session state after a failed commit + shutdown = %q, want %q: a leaked retirement exit reservation makes closeLiveSession return early and the session (its delegates, jobs, worktree locks and transcript flushes) never closes",
				got, agent.SessionClosed)
		}
	})
}
