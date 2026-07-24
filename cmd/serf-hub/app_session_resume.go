package main

import (
	"context"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

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
	once func() (R, error),
) (R, error) {
	resp, err := once()
	if err == nil {
		return resp, nil
	}
	if ref != "" && !hubKnowsRef(cfg, ref) {
		return resp, err
	}
	if !shouldResumeAfterSessionUnavailable(err) {
		return resp, err
	}
	if _, resumeErr := hubThreadResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: ref}); resumeErr != nil {
		var zero R
		return zero, resumeErr
	}
	return once()
}

func clearThreadWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return withSessionResume(ctx, cfg, sources, params.Ref, func() (appwire.ThreadClearResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.ThreadClearResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "clear"); err != nil {
			return appwire.ThreadClearResponse{}, err
		}
		return source.ClearThread(ctx, params)
	})
}

func setThreadNameWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadNameSetParams) (appwire.EmptyResponse, error) {
	return withSessionResume(ctx, cfg, sources, params.Ref, func() (appwire.EmptyResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "rename"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.SetThreadName(ctx, params)
	})
}

func setGoalWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	return withSessionResume(ctx, cfg, sources, params.Ref, func() (appwire.GoalSetResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.GoalSetResponse{}, err
		}
		// Gate like every sibling thread action so goal/set is rejected uniformly
		// on sources without the engine (e.g. codex) rather than only self-guarding
		// inside the source after a managed launch (/par A6).
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "goal"); err != nil {
			return appwire.GoalSetResponse{}, err
		}
		return source.GoalSet(ctx, params)
	})
}
