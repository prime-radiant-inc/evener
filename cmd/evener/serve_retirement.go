package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener/internal/rvreg"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// serveRetirement serves both retirement triggers (the idle timer and
// evener/daemon/retire) and the idle-timeout RPC against the daemon's
// retirement controller, resolving the live session on every call.
type serveRetirement struct {
	deps serveDeps
	live *liveSession
	// retirement is the controller; it is also re-rooted by thread/clear.
	retirement     *agent.RetirementController
	rvRegistration *rvreg.Registration
	// commit is the controller's Commit step. serveDeps may inject a failure
	// so a test can drive the pre-commit rollback; nil means the controller's
	// own Commit.
	commit func(*agent.RetirementController, *agent.RetirementClaim) error
	// ctx is the daemon's lifetime context and cancel is what starts its exit.
	ctx    context.Context
	cancel context.CancelFunc
}

func (r *serveRetirement) observe(event, rootID string) {
	if r.deps.retirementObserve != nil {
		r.deps.retirementObserve(event, rootID)
	}
}

// consumeRetirementClaim is the single pipeline both triggers share. Any
// failure before Commit aborts the claim and leaves the daemon resident.
// Past Commit admission is closed permanently, so teardown runs under the
// daemon's own lifetime context rather than the caller's request context: a
// client that disconnects mid-teardown must not be able to wedge the process
// in "retiring". Exit is unconditional once Commit returns — a teardown
// failure is reported, never fatal to terminating the process.
func (r *serveRetirement) consumeClaim(reqCtx context.Context, claim *agent.RetirementClaim) error {
	root := r.live.session()
	r.observe("claim_consumed", root.ID())
	// Every failure before Commit must abort the claim, or it wedges in
	// preparing and the daemon never retires and never recovers. This is
	// deliberately NOT a defer: a failure at or after Commit must leave the
	// process retiring, not abort it.
	failBeforeCommit := func(err error) error {
		_ = r.retirement.Abort(claim, "prepare_failed")
		return err
	}
	if !rendezvous.StrongOwnershipAvailable() {
		return failBeforeCommit(errors.New("retirement requires strong rendezvous ownership, unavailable on this platform"))
	}
	prepared, err := r.retirement.Prepare(reqCtx, claim)
	if err != nil {
		return failBeforeCommit(fmt.Errorf("retirement preparation: %w", err))
	}
	r.observe("prepared", root.ID())
	if !r.live.reserveRetirementExit(root) {
		return failBeforeCommit(errors.New("retirement exit is no longer owned by the prepared root"))
	}
	if err := r.commit(r.retirement, claim); err != nil {
		// The process did not commit, so the exit is genuinely still
		// undecided: drop the reservation reserveRetirementExit took before
		// the claim aborts.
		r.live.releaseRetirementExit()
		return failBeforeCommit(fmt.Errorf("retirement commit: %w", err))
	}
	r.observe("committed", root.ID())
	// Post-commit teardown is daemon-owned, not request-owned. The ctx used
	// here is the daemon's lifetime context (the incoming request context is
	// reqCtx): a committed retirement already closed admission forever, so
	// request cancellation must never strand the process. The drain stays
	// bounded by retirementReaderDrainBudget.
	drainCtx, drainCancel := context.WithTimeout(r.ctx, retirementReaderDrainBudget)
	drainErr := r.retirement.DrainReaders(drainCtx)
	drainCancel()
	var teardownErr error
	if drainErr != nil {
		serveLogf(os.Stderr, root.ID(), "retirement reader drain failed: %v", drainErr)
		teardownErr = fmt.Errorf("retirement reader drain: %w", drainErr)
	} else if err := agent.ReleaseForRetirement(r.ctx, prepared); err != nil {
		serveLogf(os.Stderr, root.ID(), "retirement release failed: %v", err)
		teardownErr = fmt.Errorf("retirement release: %w", err)
	} else {
		r.observe("released", root.ID())
	}
	// Exit is unconditional after Commit: teardown success is not the
	// precondition for terminating the process. The cancel is what starts
	// that exit -- the shutdown goroutine below waits on ctx.Done() -- so on
	// the RPC trigger it must not run until the accepted response has reached
	// the transport. Cancelling from inside the handler starts the shutdown
	// pass before the response frame is written, which leaves the caller with
	// a transport error and the Hub with a failure banner despite a
	// successful retirement. AfterResponseWritten runs the cancel once that
	// frame has been written, or at connection teardown if it never could be,
	// so exit is preserved. The idle-timer trigger runs on the daemon's own
	// lifetime context, which carries no connection, so it reports false and
	// cancels immediately.
	if !appserver.AfterResponseWritten(reqCtx, r.cancel) {
		r.cancel()
	}
	if teardownErr != nil {
		return &retirementTeardownError{err: teardownErr}
	}
	return nil
}

// requireExactOwnership is the one exact-ownership fence behind both
// identity-fenced daemon control RPCs: the current rendezvous entry must
// exist and the caller's generation fingerprint must match it, where an
// unrendered (empty) generation never compares equal. The retire path runs
// it again between TryManualClaim and the claim's prepare so a thread/clear
// that swapped ownership under an uncommitted claim cannot be retired by its
// predecessor's stale row.
func (r *serveRetirement) requireExactOwnership(generation string) error {
	entry, ok := r.rvRegistration.Entry()
	if !ok {
		return appwire.Unavailable("daemon rendezvous not registered")
	}
	if generation == "" || generation != rendezvous.OwnershipFingerprint(entry) {
		return appwire.Conflict("daemon identity does not match current ownership")
	}
	return nil
}

// requestRetirement serves evener/daemon/retire. Exact-ownership
// revalidation runs BEFORE the admission fence is touched: a caller
// holding a stale generation (same PID, drifted identity) gets a conflict
// and no claim is consumed.
func (r *serveRetirement) requestRetirement(ctx context.Context, params appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
	if err := r.requireExactOwnership(params.Identity.Generation); err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	// claim_attempted marks the instant between the pre-claim identity check
	// and the admission fence: the caller's generation has been accepted but
	// TryManualClaim has not run. It is an observability beat outside every
	// lock (retirementObserve is nil in production) so a test can interleave
	// a thread/clear into exactly the window this closure must still defend.
	// TryManualClaim widens that window: it first waits, up to the namer's
	// timeout, for a session namer to settle (#3921). The ownership re-check
	// after the claim covers the wider window the same way.
	r.observe("claim_attempted", r.live.session().ID())
	claim, snap, err := r.retirement.TryManualClaim(ctx)
	if err != nil {
		return appwire.DaemonRetireResponse{}, err
	}
	if claim == nil {
		return appwire.DaemonRetireResponse{Accepted: false, Lifecycle: server.DaemonLifecycleFromSnapshot(snap)}, nil
	}
	// The pre-claim check above and TryManualClaim are not atomic: a thread/clear
	// can run to completion between them. It holds its own admission lease
	// (so TryClaim declines while it is in flight), rewrites rendezvous
	// ownership to the replacement, swaps the session and re-roots the
	// controller, then releases. The claim TryClaim just took is therefore
	// against whatever root is current now, not necessarily the generation
	// the caller proved. Re-read ownership and abort the uncommitted claim if
	// it moved, so a stale request can never retire its replacement. The
	// abort is essential: a claim left in "preparing" wedges the daemon.
	if err := r.requireExactOwnership(params.Identity.Generation); err != nil {
		_ = r.retirement.Abort(claim, "prepare_failed")
		return appwire.DaemonRetireResponse{}, err
	}
	if err := r.consumeClaim(ctx, claim); err != nil {
		// A post-commit teardown failure means the retirement already
		// succeeded — admission is closed for good and the process exits —
		// so it is reported as accepted. The failure is logged (serveLogf)
		// and stays in the lifecycle's Failure field; it is observability,
		// not an RPC error the caller could misread as a refused retire.
		if _, ok := errors.AsType[*retirementTeardownError](err); !ok {
			return appwire.DaemonRetireResponse{Accepted: false, Lifecycle: server.DaemonLifecycleFromSnapshot(r.retirement.Snapshot())}, err
		}
	}
	return appwire.DaemonRetireResponse{Accepted: true, Lifecycle: server.DaemonLifecycleFromSnapshot(r.retirement.Snapshot())}, nil
}

// requestIdleTimeoutSet serves evener/daemon/idle-timeout/set. Like retire,
// the exact-ownership fence runs BEFORE the controller is touched, so a
// caller holding a stale generation (same PID, drifted identity) gets a
// conflict and no deadline moves.
func (r *serveRetirement) requestIdleTimeoutSet(_ context.Context, params appwire.DaemonIdleTimeoutSetParams) (appwire.DaemonIdleTimeoutSetResponse, error) {
	if err := r.requireExactOwnership(params.Identity.Generation); err != nil {
		return appwire.DaemonIdleTimeoutSetResponse{}, err
	}
	if params.TimeoutMillis < 0 {
		return appwire.DaemonIdleTimeoutSetResponse{}, appwire.InvalidParams("timeoutMillis must not be negative")
	}
	// maxIdleTimeoutMillis is the largest value whose nanosecond conversion
	// cannot overflow time.Duration: larger values would wrap — 1<<58 millis
	// lands on exactly zero, a success response that silently disabled
	// automatic retirement.
	const maxIdleTimeoutMillis = math.MaxInt64 / int64(time.Millisecond)
	if params.TimeoutMillis > maxIdleTimeoutMillis {
		return appwire.DaemonIdleTimeoutSetResponse{}, appwire.InvalidParams("timeoutMillis exceeds the maximum representable duration")
	}
	// idle_timeout_attempted marks the instant between the pre-retarget
	// identity check and the controller write: the caller's generation has
	// been accepted but Retarget has not run. Like claim_attempted above, it
	// is an observability beat outside every lock (retirementObserve is nil
	// in production) so a test can interleave a thread/clear into exactly
	// the window the revalidation below must still defend.
	r.observe("idle_timeout_attempted", r.live.session().ID())
	prev, token, err := r.retirement.RetargetStamped(time.Duration(params.TimeoutMillis) * time.Millisecond)
	if err != nil {
		return appwire.DaemonIdleTimeoutSetResponse{}, err
	}
	// idle_timeout_written parks the handler immediately after the
	// controller write and before the revalidation fence — the exact
	// window in which a later legitimate writer's deadline must survive
	// the stale request's revert. Same contract as idle_timeout_attempted:
	// observability only, nil in production.
	r.observe("idle_timeout_written", r.live.session().ID())
	// The pre-retarget check and Retarget are not atomic: a thread/clear can
	// run to completion between them (mutations do not serialize with each
	// other), re-rooting the controller to the replacement — and the write
	// above then moved the REPLACEMENT's deadline, so a bare conflict would
	// leave the harm in place. Re-read ownership and, if it moved, undo the
	// write by its token: UndoRetarget restores the deadline the write
	// replaced, but only while this request's write is still the newest — a
	// later legitimate writer (the replacement's own archive decision) owns
	// the deadline now and must survive the refusal, even when it chose the
	// same value, which a value comparison could not tell apart. A clear
	// that completed before this write reset the fresh root to the
	// configured baseline, so this write landed after that reset and the
	// undo below is the only thing that can take it back; a clear that
	// completes after the write has already superseded this write's token
	// with its own reset, and the undo correctly yields. Refuse like the
	// retire path does after its claim. The undo is best-effort: a
	// controller that is no longer resident never restores (the process is
	// preparing or retiring and its deadline no longer matters).
	if err := r.requireExactOwnership(params.Identity.Generation); err != nil {
		r.retirement.UndoRetarget(token, prev)
		return appwire.DaemonIdleTimeoutSetResponse{}, err
	}
	return appwire.DaemonIdleTimeoutSetResponse{Lifecycle: server.DaemonLifecycleFromSnapshot(r.retirement.Snapshot())}, nil
}
