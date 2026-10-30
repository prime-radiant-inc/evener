package hub

import (
	"context"
	"errors"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
)

func (o *sessionOperations) Start(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	cfg, sources, relays := o.cfg, o.sources, o.relays
	if err := validateAppWireInputItems(params.Input); err != nil {
		return appwire.TurnStartResponse{}, appwire.InvalidParams(err.Error())
	}
	if err := requireClientMutationID(params.ClientMutationID); err != nil {
		return appwire.TurnStartResponse{}, err
	}
	resolved := false
	// initialPreDispatch records whether the ORIGINAL attempt is proven to
	// have failed before anything was dispatched: it stayed true only when the
	// first attempt never resolved a source. Any other first-attempt failure
	// resolved the source first, so it may have dispatched; see
	// retryAfterResume's not-accepted rule.
	initialPreDispatch := false
	attemptStart := func() (appwire.TurnStartResponse, error) {
		source, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appsource.Source, error) {
			return resolveTurnStartSource(sources, params.Ref, params.ThreadID)
		})
		if err != nil {
			return appwire.TurnStartResponse{}, err
		}
		resolved = true
		resp, err := relays.startTurn(ctx, source, params)
		err = adoptCallerMutationResult(&resp.Receipt, err, params.ClientMutationID)
		return resp, err
	}
	// retryAfterResume runs the attempt a resume this request performed made
	// possible. A failure there that gives the caller no way to judge its own
	// mutation is wrapped in the resume path's own blocked-unknown envelope
	// (see blockedUnknownMutationError), so the prompt is retained for a
	// retry rather than left submitting forever.
	//
	// correlateRetryFailure's exemptions are consulted first, so a refusal
	// that already carries its own meaning is never rewritten: a refusal
	// naming this caller's mutation (recovery admission, daemon-restart-
	// required, a shape refusal) and a target deletion keep their own
	// outcome. Only a genuinely uncorrelated failure is left. It is reported
	// not-accepted only when the WHOLE operation is proven pre-dispatch --
	// both the retry and the original attempt failed before reaching a
	// source -- because a retry that fails source resolution proves nothing
	// about an earlier attempt that already reached a source and may have
	// applied the mutation before its response was lost. The caller's
	// cancellation is handled before either, since it is not a mutation
	// outcome at all.
	retryAfterResume := func() (appwire.TurnStartResponse, error) {
		resolved = false
		resp, err := attemptStart()
		if err == nil {
			return resp, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return resp, err
		}
		// The resumed daemon names the id it trimmed; rewrite it back to the
		// caller's own id before anything compares or returns it, so a
		// canonical match is recognized and the response still carries exactly
		// the id the caller submitted.
		err = adoptCallerMutationID(err, params.ClientMutationID)
		if wrapped := correlateRetryFailure(params.ClientMutationID, err); wrapped != nil {
			// A target deletion keeps its own outcome even pre-dispatch: a
			// deleted target never accepts the mutation, but the caller must
			// still be told the target is gone rather than re-offered it as
			// not-accepted. correlateRetryFailure hands it back enriched.
			if !resolved && initialPreDispatch && !isTargetDeletedError(err) {
				// Source resolution failed before the retry reached a source,
				// and the original attempt never reached one either, so nothing
				// was dispatched and the mutation's outcome is known -- not
				// accepted -- rather than unknown.
				return appwire.TurnStartResponse{}, appwire.MutationNotAccepted(params.ClientMutationID, err.Error())
			}
			return appwire.TurnStartResponse{}, wrapped
		}
		return resp, err
	}
	resp, err := attemptStart()
	if err == nil {
		return resp, nil
	}
	initialPreDispatch = !resolved
	if !resolved {
		if wire, ok := errors.AsType[appwire.WireError](err); ok && wire.Code == appwire.CodeInvalidParams {
			return appwire.TurnStartResponse{}, err
		}
		if isTargetDeletedError(err) || isDaemonRestartRequiredError(err) || isSessionRecoveryAdmissionError(err) {
			return appwire.TurnStartResponse{}, err
		}
		if _, resumeErr := resumeTurnStartThread(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
			return appwire.TurnStartResponse{}, mutationResumeFailureError(cfg, params.Ref, params.ThreadID, params.ClientMutationID, resumeErr)
		}
		return retryAfterResume()
	}
	if isLifecycleRetiringError(err) {
		// The owning daemon refused the mutation because it is retiring and
		// still owns the session. Resolve the race under existing recovery
		// authority — admission fences, ownership alias locks, confirmed exit,
		// one resume — then retry the original request verbatim.
		if resumeErr := resumeAfterConfirmedRetirement(ctx, cfg, sources, params); resumeErr != nil {
			// This path fails with refusals that name no mutation at all -- the
			// retirement/lifecycle "retiring" refusal, an ownership or roster
			// error, an admission fence -- and its ownership-group fence can
			// name a sibling alias, so hand it the same treatment as every
			// other resume failure (see mutationResumeFailureError): the
			// deletion outcome only when it is this caller's own target's, and
			// otherwise the blocked-unknown envelope carrying the caller's id,
			// rather than an unnamed error the client can never correlate.
			return appwire.TurnStartResponse{}, mutationResumeFailureError(cfg, params.Ref, params.ThreadID, params.ClientMutationID, resumeErr)
		}
		return retryAfterResume()
	}
	if params.Ref != "" && !hubKnowsRef(cfg, params.Ref) {
		return appwire.TurnStartResponse{}, err
	}
	if !isSessionUnavailableError(err) {
		return appwire.TurnStartResponse{}, err
	}
	if _, resumeErr := resumeTurnStartThread(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
		return appwire.TurnStartResponse{}, mutationResumeFailureError(cfg, params.Ref, params.ThreadID, params.ClientMutationID, resumeErr)
	}
	return retryAfterResume()
}

func (o *sessionOperations) Steer(ctx context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	cfg, sources := o.cfg, o.sources
	if err := validateAppWireInputItems(params.Input); err != nil {
		return appwire.TurnSteerResponse{}, appwire.InvalidParams(err.Error())
	}
	if err := requireClientMutationID(params.ClientMutationID); err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	resp, err := relayOwnedMutation(ctx, cfg, sources, params.Ref, params.ThreadID, params.ClientMutationID, func(source appsource.Source) (appwire.TurnSteerResponse, error) {
		if err := ensureSkillInputSupported(ctx, source, params.Ref, params.ThreadID, params.Input); err != nil {
			return appwire.TurnSteerResponse{}, err
		}
		return source.SteerTurn(ctx, params)
	})
	err = adoptCallerMutationResult(&resp.Receipt, err, params.ClientMutationID)
	return resp, err
}

func (o *sessionOperations) Queue(ctx context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	cfg, sources := o.cfg, o.sources
	if err := validateAppWireInputItems(params.Input); err != nil {
		return appwire.TurnQueueResponse{}, appwire.InvalidParams(err.Error())
	}
	if err := requireClientMutationID(params.ClientMutationID); err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	// A queued message is a session mutation a past thread advertises, so it
	// carries the same exited == never-exited contract as turn/start: the hub
	// resumes the session and retries the write (withSessionResume). Without
	// it a queue write against a thread whose daemon has exited was refused
	// outright, which is exactly the route the web composer chooses for a
	// message sent while it already has a send in flight against a finished
	// session (appwire-client/typescript/sendQueueAvailability.ts) — so the
	// message was dropped instead of being queued behind the resume that send
	// had started.
	resp, err := withSessionResume(ctx, cfg, sources, params.Ref, params.ClientMutationID, func() (appwire.TurnQueueResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.TurnQueueResponse{}, err
		}
		if err := ensureSkillInputSupported(ctx, source, params.Ref, "", params.Input); err != nil {
			return appwire.TurnQueueResponse{}, err
		}
		return source.QueueTurn(ctx, params)
	})
	err = adoptCallerMutationResult(&resp.Receipt, err, params.ClientMutationID)
	return resp, err
}
