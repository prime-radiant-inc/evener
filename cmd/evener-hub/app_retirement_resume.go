package hub

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// isLifecycleRetiringError reports whether err is the typed lifecycle
// CodeUnavailable a daemon returns when its admission fence refuses a mutation
// because the daemon is retiring. The wire client's Data decodes as
// map[string]any, so the shape is re-marshaled into the typed form rather than
// asserted.
func isLifecycleRetiringError(err error) bool {
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeUnavailable || wire.Data == nil {
		return false
	}
	data, ok := wire.Data.(appwire.LifecycleErrorData)
	if !ok {
		raw, merr := json.Marshal(wire.Data)
		if merr != nil {
			return false
		}
		if json.Unmarshal(raw, &data) != nil {
			return false
		}
	}
	return data.EvenerErrorInfo == appwire.ErrorActionUnavailable && data.LifecycleReason == "retiring"
}

// retirementAdmissionRecoveryError re-checks the per-action recovery fences
// for a request that was already admitted and is now in flight: the
// connection-sequence fence guards fresh actions on a fenced connection,
// while an admitted action's own authority is its admission epoch.
func retirementAdmissionRecoveryError(cfg hubcore.WebConfig, id string, epoch uint64) error {
	state := sessionRecoveryState(cfg, "", id)
	if state.Stopping > 0 || state.ResumeRequired {
		return sessionRecoveryAdmissionError{appwire.Unavailable("session recovery requires an explicit thread/resume before submitting another action")}
	}
	if state.Epoch != epoch {
		return sessionRecoveryAdmissionError{appwire.Unavailable("session recovery canceled this pending action; submit it again")}
	}
	return nil
}

// sameDaemonIdentity compares the process identity of two rendezvous entries
// through the single exact-ownership fingerprint (rendezvous.OwnershipFingerprint):
// the same daemon re-published or re-probed keeps every owned field, while a
// replacement differs in at least one. Using the fingerprint rather than a
// hand-picked subset keeps this decision identical to the ownership authority
// the daemon and hub already enforce for exact ownership.
func sameDaemonIdentity(a, b rendezvous.Entry) bool {
	return rendezvous.OwnershipFingerprint(a) == rendezvous.OwnershipFingerprint(b)
}

// awaitRetiredOwner waits for the exact retired owner to exit and verifies the
// exit under existing discovery authority. Confirmed absence — never mere
// unreachability — is the only success: an open failure, a wait failure, an
// incomplete roster scan, or an owner the roster still confirms all yield the
// retryable lifecycle error (or the caller's context error), and no spawn is
// attempted on this path. The owner is opened as a Retiring target: it
// releases its session API log before it exits, and that window is exactly
// when a refused turn/start lands here.
func awaitRetiredOwner(ctx context.Context, cfg hubcore.WebConfig, entry rendezvous.Entry) error {
	controller := cfg.DaemonProcesses
	if controller == nil {
		controller = daemonprocess.NewController()
	}
	// The rendezvous entry may identify its session only through ThreadID; the
	// controller rejects an empty session identity, which would report this
	// owner as retiring instead of awaiting its exit. Fall back the same way the
	// force-stop path does (app_force_stop.go).
	sessionID := entry.SessionID
	if sessionID == "" {
		sessionID = entry.ThreadID
	}
	proc, err := controller.Open(daemonprocess.Target{
		PID:       entry.PID,
		SessionID: sessionID,
		StateDir:  entry.StateDir,
		StartedAt: entry.StartedAt,
		Retiring:  true,
	})
	if err != nil && !errors.Is(err, daemonprocess.ErrExited) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return appwire.LifecycleUnavailable("retiring")
	}
	if proc != nil {
		defer func() { _ = proc.Close() }()
		if err := proc.Wait(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return appwire.LifecycleUnavailable("retiring")
		}
	}
	if cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
			return appwire.LifecycleUnavailable("retiring")
		}
		if cfg.Roster.HasConfirmedEntry(entry) {
			return appwire.LifecycleUnavailable("retiring")
		}
	}
	return nil
}

// resumeAfterConfirmedRetirement resolves a turn/start that the owning daemon
// refused with the typed retiring lifecycle error. It re-checks every existing
// recovery fence with the request's admission epoch (deletion, force-stop,
// stale admission), serializes with concurrent resumes and force stops on the
// session's ownership alias locks, waits for the exact retiring owner's
// confirmed exit, then either reuses a current replacement or runs the shared
// resume discovery-and-spawn half under the same locks. It never signals the
// daemon: retirement remains the daemon's own decision, and force-stop
// authority is unchanged.
func resumeAfterConfirmedRetirement(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.TurnStartParams) (resumeErr error) {
	// A retirement-triggered resume shares resumeThread's correlated lifecycle
	// trace: it opens the trace with the requested identity, brackets the whole
	// attempt in the request pair so every outcome is recorded, and stamps the
	// ownership-resolved target below so its records carry the same
	// session_id/resolved_session_id pair an explicit resume records.
	sessionID := deletionThreadID(params.Ref, params.ThreadID)
	ctx, trace := withThreadLifecycleLog(ctx, "resume", sessionID, nil)
	requestStarted := time.Now()
	trace.record(ctx, "request", "begin", requestStarted, nil, 0, 0)
	defer func() { trace.record(ctx, "request", "complete", requestStarted, resumeErr, 0, 0) }()

	if err := deletionFenceError(cfg, params.Ref, params.ThreadID, params.ClientMutationID); err != nil {
		return err
	}
	if sessionID == "" || cfg.ResumeLocks == nil || cfg.Roster == nil {
		return appwire.LifecycleUnavailable("retiring")
	}
	epoch := sessionRequestRecoveryEpoch(ctx, cfg, params.Ref, params.ThreadID)
	if err := retirementAdmissionRecoveryError(cfg, sessionID, epoch); err != nil {
		return err
	}
	if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
		return appwire.LifecycleUnavailable("retiring")
	}
	ownerBefore, hadOwnerBefore := liveDaemonForThread(cfg.Roster, sessionID)

	ownershipDone := trace.stage(ctx, "ownership")
	target, aliases, err := resumeOwnership(cfg, sessionID, sessionID)
	ownershipDone(err)
	if err != nil {
		return appwire.Unavailable(err.Error())
	}
	// Every outcome after ownership resolution records the resolved identity,
	// including the live-replacement early returns below: the deferred request
	// completion captures this trace by reference.
	ctx, trace = trace.resolved(ctx, target)
	// A successful retirement recovery is a completed resume, so record where
	// the alias resolved exactly as resumeThread's defer does after
	// ExplicitResumeCompleted. This defer covers the normal exit and the three
	// early returns where a replacement is already live. The target resolved
	// from live rendezvous evidence (resumeClaimTarget's entry.SessionID) is in
	// no recorded chain, so without this a later fork/resume of the alias walks
	// the stale hop and branches the older session. RecordResolvedSession is a
	// no-op unless the alias is at the current epoch, is not stopping, and has
	// no pending recovery obligation. Retirement recovery is not an explicit
	// resume, so ExplicitResumeCompleted is deliberately not called here (the
	// automatic branch of the normal path skips it too).
	defer func() {
		if resumeErr != nil {
			return
		}
		cfg.ResumeLocks.RecordResolvedSession(sessionID, target, epoch)
	}()
	epochs := make(map[string]uint64, len(aliases))
	for _, id := range aliases {
		epochs[id] = sessionRequestRecoveryEpoch(ctx, cfg, "", id)
	}
	epochs[sessionID] = epoch
	// Force stop's sorted ownership order, retaining the original mutexes.
	// This path is context-aware, so it acquires each alias through the context
	// and releases the prefix it holds if a later alias blocks past
	// cancellation; an ordinary Lock here would hang behind a long-running
	// explicit Resume and retain every earlier alias.
	acquired := 0
	var heldStarted time.Time
	lockDone := trace.stage(ctx, "lock_wait")
	defer func() {
		for _, id := range slices.Backward(aliases[:acquired]) {
			cfg.ResumeLocks.For(id).Unlock()
		}
		if !heldStarted.IsZero() {
			trace.record(ctx, "lock_held", "complete", heldStarted, nil, 0, 0)
		}
	}()
	for _, id := range aliases {
		if err := cfg.ResumeLocks.For(id).LockContext(ctx); err != nil {
			lockDone(err)
			return err
		}
		acquired++
	}
	lockDone(nil)
	heldStarted = time.Now()
	trace.record(ctx, "lock_held", "begin", heldStarted, nil, 0, 0)
	// A deletion record may name any alias in the resolved ownership
	// group, so the whole group is fenced under the locks that make the
	// check final, before live-owner reuse or replacement below.
	if err := deletionFenceErrorForGroup(cfg, aliases); err != nil {
		return err
	}
	for _, id := range aliases {
		if err := retirementAdmissionRecoveryError(cfg, id, epochs[id]); err != nil {
			return err
		}
	}

	if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
		return appwire.LifecycleUnavailable("retiring")
	}
	owner, ok := liveDaemonForThread(cfg.Roster, sessionID)
	if ok && owner.Protocol == appwire.ProtocolVersion {
		if owner.SessionID != "" && owner.SessionID != target && !slices.Contains(aliases, owner.SessionID) {
			// The daemon claiming this rendezvous now serves a session outside
			// the recovered ownership group; it is not the owner that refused
			// the mutation. Never wait on it or spawn over it — the moved
			// daemon's ownership authority must settle it first.
			return appwire.Unavailable("session is retained by " + localAppRef(owner.SessionID) + "; open the owning session or refresh after it stops")
		}
		retiring := owner.LifecycleFresh && owner.Lifecycle != nil && owner.Lifecycle.Phase == "retiring"
		// A fresh lifecycle probe reporting a NON-retiring phase proves the
		// current owner is an active replacement already serving the session:
		// the entry-time owner was replaced before this retry (a concurrent
		// resume holds the alias locks only until it finishes, and the entry-time
		// sample at the top of this function is taken before this function takes
		// them). sameAsRefused infers "the refuser still owns the session" from
		// identity alone, so it cannot distinguish that replacement from the
		// refuser; the fresh probe can. Treat the replacement as ready instead of
		// waiting on a live PID for the caller's whole context deadline while
		// holding the session's alias locks. An unfresh lifecycle reports the
		// capability as unknown — never as a replacement — and a fresh
		// "retiring" owner is still the refuser and is still awaited below.
		if owner.LifecycleFresh && owner.Lifecycle != nil && !retiring {
			return nil
		}
		sameAsRefused := hadOwnerBefore && sameDaemonIdentity(ownerBefore.Entry, owner.Entry)
		if !retiring && !sameAsRefused {
			// A current replacement already owns the session; the retry resolves it.
			return nil
		}
		// The daemon that refused the mutation still owns the session. Wait for
		// its confirmed exit; re-check ownership identity through the roster
		// rather than ever signaling a process.
		if err := awaitRetiredOwner(ctx, cfg, owner.Entry); err != nil {
			return err
		}
		if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
			return appwire.LifecycleUnavailable("retiring")
		}
		if le, ok := liveDaemonForThread(cfg.Roster, sessionID); ok && le.Protocol == appwire.ProtocolVersion {
			// A concurrent resume already replaced the owner.
			return nil
		}
	}
	// The owner is confirmed absent under discovery authority (an incompatible
	// owner falls through to resume's own restart-required authority). Spawn
	// under the held alias locks so a concurrent resumer double-checks instead
	// of launching twice. resumeThreadLocked does not re-walk ownership aliases,
	// so it must be handed the RESOLVED target: passing the pre-resolution
	// sessionID would drive discovery and spawn for a stale alias whenever
	// resumeOwnership resolved a different current owner (thread/clear), the
	// same convention resumeThread follows by assigning sessionID = target.
	_, resumeErr = resumeThreadLocked(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: target})
	return resumeErr
}
