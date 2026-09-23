package hub

import (
	"context"
	"errors"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// preDispatchRefusalError marks a session-mutation failure that happened before
// anything reached a source: the caller's shape could not even resolve the
// owning source for the ref, so nothing was dispatched.
//
// It exists so withSessionResume can tell the two kinds of retry failure apart.
// A retry that reached a source and failed gives the caller no way to know
// whether a mutation it cannot correlate was accepted, so it stays
// blocked-unknown (see correlateRetryFailure). A retry that failed to resolve a
// source proves nothing was dispatched, exactly like turn/start's pre-dispatch
// retry failure, so its outcome is known -- not accepted.
//
// The signal must be present on EVERY attempt for the whole operation to count
// as pre-dispatch: the not-accepted outcome is emitted only when the original
// attempt and the retry both carry it. The original attempt that triggered the
// resume reached the owning source by definition (that is what raised the
// session-unavailable failure), so a retry-only signal never proves the whole
// operation was rejected -- the mutation may already have been applied before
// the response was lost.
//
// No current caller can satisfy that whole-operation proof, and the not-accepted
// outcome it guards is therefore forward-compatibility, not a live production
// result. Reaching the retry at all requires shouldResumeAfterSessionUnavailable
// to accept the original attempt's failure (see withSessionResume), which needs a
// WireError carrying CodeUnavailable and ErrorSessionUnavailable; sourceForThread
// (Registry.SourceForRef) can only return a ref parse error or a plain "source
// not found", never that. So whenever a retry runs, the original attempt
// necessarily resolved a source, firstAttemptPreDispatch is false, and a real
// post-resume resolution failure is reported blocked-unknown -- pinned by
// TestHubRPCResumeRetrySourceResolutionFailureIsNotAccepted. The rule is kept
// deliberately so it states the invariant rather than being dropped: a future
// source resolution that itself yields a session-unavailable error would make the
// whole-operation proof reachable again and settle the mutation as rejected.
//
// Wrapped at the resolution source (the caller shapes'
// sourceForThread calls) so the signal is exact: an arbitrary pre-dispatch
// probe failure is not the same proof as a resolution failure, and this type is
// deliberately not set on those. Unwrap keeps every downstream errors.As/Is
// probe (session-unavailable detection, wire serialization) seeing the original
// error.
type preDispatchRefusalError struct{ err error }

func (e preDispatchRefusalError) Error() string { return e.err.Error() }
func (e preDispatchRefusalError) Unwrap() error { return e.err }

// isPreDispatchRefusal reports whether err is (or wraps) the pre-dispatch
// source-resolution signal.
func isPreDispatchRefusal(err error) bool {
	_, ok := errors.AsType[preDispatchRefusalError](err)
	return ok
}

// unwrapPreDispatchRefusal returns the refusal's original failure so callers
// outside withSessionResume's retry rule see exactly the error they saw before
// the signal existed. A non-signal error is returned unchanged.
func unwrapPreDispatchRefusal(err error) error {
	if refusal, ok := errors.AsType[preDispatchRefusalError](err); ok {
		return refusal.err
	}
	return err
}

// withSessionResume runs a session-level mutation and, if it fails because the
// backing daemon has exited, resumes the session once and retries. This is the
// shared "exited == never-exited" contract (kata qp94): every user-reachable
// session mutation the hub advertises for a past thread must succeed by
// resuming the daemon, exactly as StartTurn/Compact/SetModel already do.
//
// The retry only fires when the ref is one the hub knows (a managed-launch
// source or a thread in the local past index) and the failure is a
// session-unavailable error — a live in-flight error or an unknown ref is
// returned unchanged so we never resurrect a session to mask a real fault.
func withSessionResume[R any](
	ctx context.Context,
	cfg hubcore.WebConfig,
	sources *appsource.Registry,
	ref string,
	clientMutationID string,
	once func() (R, error),
) (R, error) {
	attempt := func() (R, error) {
		if clientMutationID == "" {
			return withSessionActionOwnership(ctx, cfg, ref, "", once)
		}
		return withDeletionTargetOwnership(ctx, cfg, ref, "", clientMutationID, once)
	}
	resp, err := attempt()
	if err == nil {
		return resp, nil
	}
	// firstAttemptPreDispatch records whether the ORIGINAL attempt is proven to
	// have failed before anything was dispatched. Only the source-resolution
	// signal proves it (see preDispatchRefusalError): any other first-attempt
	// failure resolved the owning source first, so it may have reached it and
	// applied the mutation before failing. The retry's own pre-dispatch signal
	// cannot stand in for that, so it is kept here for the not-accepted rule
	// below.
	//
	// In practice this is always false here: the code below only reaches the
	// retry when the first attempt failed with a session-unavailable WireError,
	// and source resolution never produces one (see preDispatchRefusalError). So
	// the not-accepted rule it feeds is forward-compatibility for a resolution
	// that itself reports session-unavailable, not an outcome any caller gets
	// today -- a real post-resume resolution failure stays blocked-unknown.
	firstAttemptPreDispatch := isPreDispatchRefusal(err)
	// Outside the retry rule the pre-dispatch signal carries no meaning: the
	// first attempt's failure is returned exactly as it was raised.
	err = unwrapPreDispatchRefusal(err)
	if !shouldResumeAfterSessionUnavailable(err) {
		return resp, err
	}
	if ref != "" && !hubKnowsRef(cfg, ref) {
		return resp, err
	}
	if _, resumeErr := hubThreadAutoResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: ref}); resumeErr != nil {
		var zero R
		if clientMutationID != "" {
			// A resume that failed because THIS CALLER'S target was deleted keeps
			// that deletion's outcome, named for this caller, rather than being
			// hidden behind blocked-unknown; a sibling-alias deletion in the
			// ownership group does not qualify. See mutationResumeFailureError.
			return zero, mutationResumeFailureError(cfg, ref, "", clientMutationID, resumeErr)
		}
		return zero, resumeErr
	}
	// The retry runs because this request's resume made it possible, so its
	// failure is judged the same way turn/start's retry is: a failure that
	// names no clientMutationId is uncorrelated and must be wrapped so the
	// caller's mutation dispatcher can decide what to do with it (see
	// correlateRetryFailure). Only wrap when this request actually carries an
	// id to correlate against.
	resp, retryErr := attempt()
	// The resumed daemon names the id it trimmed; rewrite it back to the
	// caller's own id before anything compares or returns it, so a canonical
	// match is recognized and the response still carries exactly the id the
	// caller submitted.
	retryErr = adoptCallerMutationID(retryErr, clientMutationID)
	if retryErr == nil || clientMutationID == "" {
		return resp, unwrapPreDispatchRefusal(retryErr)
	}
	if wrapped := correlateRetryFailure(clientMutationID, retryErr); wrapped != nil {
		var zero R
		// not-accepted requires the WHOLE operation to be proven pre-dispatch:
		// both the original attempt and the retry must have failed before
		// reaching a source. A retry can fail source resolution while the
		// original attempt already reached the owning source -- indeed reaching
		// it is what raised the session-unavailable failure that triggered the
		// resume -- and a source call that loses its response does not turn a
		// possibly-applied mutation into a known rejection (thread/clear is the
		// example). Emitting not-accepted from the retry's signal alone would
		// overstate that possibly-applied mutation as rejected, so a
		// not-fully-proven operation stays blocked-unknown and the record is
		// retained. A target deletion keeps its own meaning even here, as it does
		// in turn/start's identical rule.
		//
		// firstAttemptPreDispatch cannot be true for any caller today (see its
		// declaration and preDispatchRefusalError): reaching this retry means the
		// first attempt resolved a source, so this branch is deliberate
		// forward-compatibility for a source resolution that itself reports
		// session-unavailable, not a production outcome. It is retained so the
		// rule stays stated and stays correct if such a resolution ever appears.
		if firstAttemptPreDispatch && isPreDispatchRefusal(retryErr) && !isTargetDeletedError(retryErr) {
			return zero, appwire.MutationNotAccepted(clientMutationID, retryErr.Error())
		}
		return zero, wrapped
	}
	return resp, unwrapPreDispatchRefusal(retryErr)
}

// shutdownThreadTolerateExited runs thread/shutdown and treats an
// already-exited session as a no-op success. Shutdown's goal is a stopped
// daemon; if the session the hub knows is already gone, that goal is met, so
// we must NOT resurrect it just to kill it (kata qp94 carve-out). An unknown
// ref or any non-session-unavailable failure is still returned unchanged.
func shutdownThreadTolerateExited(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadShutdownParams) error {
	if err := shutdownCleanupError(cfg, params.Ref); err != nil {
		return err
	}
	discoveryUncertain := false
	if ref, err := appwire.ParseRef(params.Ref); err == nil && ref.SourceID == "local" {
		decision, err := checkConfirmedStoppedWithoutClaim(ctx, cfg, ref.ThreadID, false, nil)
		if err != nil {
			return err
		}
		if decision.stopped {
			// Match the force-stop shortcut: report the stopped session only
			// after the roster re-lists, so the live/stopped projection does
			// not stay stale until the next watcher pass.
			refreshAfterForceStop(ctx, cfg)
			return nil
		}
		discoveryUncertain = decision.discoveryUncertain
	}
	action := func() (struct{}, error) {
		// A Resume can retain failed child cleanup while shutdown waits for
		// alias ownership, after the pre-check above already passed. Recheck
		// under ownership before the shutdown action.
		if err := shutdownCleanupError(cfg, params.Ref); err != nil {
			return struct{}{}, err
		}
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return struct{}{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "shutdown"); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, source.ShutdownThread(ctx, params)
	}
	var err error
	if discoveryUncertain {
		// Strict discovery failed for a session already confirmed exited, so
		// the confirmed-stopped check could not prove the absence of a claim.
		// The resume-required refusal must not gate this fall-through:
		// shutdown never resurrects the session, and its goal — a stopped
		// daemon — is what durable authority already asserts. The tolerant
		// source attempt resolves the uncertainty from the other side under
		// deletion-fence ownership.
		_, err = withShutdownDiscoveryUncertainOwnership(ctx, cfg, params.Ref, "", action)
	} else {
		_, err = withSessionActionOwnership(ctx, cfg, params.Ref, "", action)
	}
	if err != nil && params.Ref != "" && hubKnowsRef(cfg, params.Ref) && isSessionUnavailableError(err) {
		// The fallback treats an unavailable session as successfully stopped;
		// a cleanup failure retained since the under-ownership recheck must
		// not become that success.
		if err := shutdownCleanupError(cfg, params.Ref); err != nil {
			return err
		}
		if cfg.Roster != nil {
			if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
				return appwire.Unavailable(err.Error())
			}
		}
		if restartErr := daemonRestartRequiredError(ctx, cfg, params.Ref, "", ""); restartErr != nil {
			return restartErr
		}
		return nil
	}
	return err
}

// withShutdownDiscoveryUncertainOwnership runs a shutdown source attempt when
// strict rendezvous discovery failed for a session already confirmed exited.
// It keeps deletion fencing and alias ownership but omits the session-action
// recovery gate: a ResumeRequired refusal is spurious here because shutdown
// manufactures no recovery obligation and an already-exited session reported
// by the source is the desired end state, not an error to mask. It rechecks
// resume admission under the reacquired ownership: the uncertain decision
// released the whole reservation group before this reacquire, so an explicit
// Resume can register in that window and be mid-launch here — it would finish
// launching after shutdown reported the session stopped, or have the daemon
// it just started shut down by the source action.
func withShutdownDiscoveryUncertainOwnership[R any](ctx context.Context, cfg hubcore.WebConfig, ref, threadID string, action func() (R, error)) (R, error) {
	return withDeletionTargetOwnership(ctx, cfg, ref, threadID, "", func() (R, error) {
		if err := shutdownResumeActiveError(cfg, ref); err != nil {
			var zero R
			return zero, err
		}
		return action()
	})
}

// shutdownResumeActiveError refuses shutdown while an explicit Resume is
// registered on the session's ownership group. The uncertain-discovery path
// rechecks it under reacquired ownership, the way shutdownCleanupError already
// rechecks retained child cleanup: the admission decision released the whole
// reservation group before the tolerant attempt reacquired the request's
// alias, so a Resume registered in that window is already mid-launch and must
// not be overlapped by the tolerant action.
func shutdownResumeActiveError(cfg hubcore.WebConfig, ref string) error {
	if parsed, err := appwire.ParseRef(ref); err == nil && parsed.SourceID == "local" && cfg.ResumeLocks != nil {
		if cfg.ResumeLocks.HasActiveResume(cfg.ResumeLocks.RecoveryAliases(parsed.ThreadID)) {
			return sessionResumeRequiredError()
		}
	}
	return nil
}

// shutdownCleanupError refuses shutdown while any Resume in the session's
// ownership group has unconfirmed child cleanup. It runs before ownership and
// again under it, because a Resume can report the failure while shutdown
// waits for the alias.
func shutdownCleanupError(cfg hubcore.WebConfig, ref string) error {
	if parsed, err := appwire.ParseRef(ref); err == nil && parsed.SourceID == "local" && cfg.ResumeLocks != nil {
		if err := cfg.ResumeLocks.ResumeCleanupErrorStrict(cfg.ResumeLocks.RecoveryAliases(parsed.ThreadID)); err != nil {
			return appwire.Unavailable(err.Error())
		}
	}
	return nil
}

func setGoalWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	return withSessionResume(ctx, cfg, sources, params.Ref, "", func() (appwire.GoalSetResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			// Resolution failed before anything reached a source, so a resume
			// retry that fails the same way proves nothing was dispatched.
			return appwire.GoalSetResponse{}, preDispatchRefusalError{err}
		}
		// Gate like every sibling thread action so goal/set is rejected uniformly
		// on sources without the engine rather than only self-guarding inside the
		// source implementation.
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "goal"); err != nil {
			return appwire.GoalSetResponse{}, err
		}
		return source.GoalSet(ctx, params)
	})
}

// setNotesHumanWithResume relays notes/human/set to the owning source,
// resuming an exited session first like goal/set. The hub pre-flight gates on
// the shared-notes capability so an old daemon with a new hub fails closed
// instead of dropping writes.
func setNotesHumanWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	return relayWithResume(ctx, cfg, sources, params.Ref, params.ClientMutationID, "shared-notes",
		func(source appsource.Source) (appwire.NotesHumanSetResponse, error) {
			return source.NotesHumanSet(ctx, params)
		})
}

// removeURLWithResume relays urls/remove to the owning source, resuming an
// exited session first like goal/set, gated on the shared-notes capability.
func removeURLWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	return relayWithResume(ctx, cfg, sources, params.Ref, params.ClientMutationID, "shared-notes",
		func(source appsource.Source) (appwire.UrlsRemoveResponse, error) {
			return source.UrlsRemove(ctx, params)
		})
}

// relayWithResume resolves the owning source for ref, gates on the named
// thread action, and runs call — the shared shape behind the notes/urls
// resume relays above.
func relayWithResume[R any](ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, ref, clientMutationID, action string, call func(appsource.Source) (R, error)) (R, error) {
	return withSessionResume(ctx, cfg, sources, ref, clientMutationID, func() (R, error) {
		source, err := sourceForThread(sources, ref, "")
		if err != nil {
			var zero R
			// Resolution failed before anything reached a source, so a resume
			// retry that fails the same way proves nothing was dispatched.
			return zero, preDispatchRefusalError{err}
		}
		if err := ensureThreadActionAvailable(ctx, source, ref, "", action); err != nil {
			var zero R
			return zero, err
		}
		return call(source)
	})
}
