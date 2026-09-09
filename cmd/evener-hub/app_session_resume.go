package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
	if !shouldResumeAfterSessionUnavailable(err) {
		return resp, err
	}
	if ref != "" && !hubKnowsRef(cfg, ref) {
		return resp, err
	}
	if _, resumeErr := hubThreadAutoResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: ref}); resumeErr != nil {
		var zero R
		if clientMutationID != "" {
			return zero, blockedUnknownMutationError(clientMutationID, resumeErr)
		}
		return zero, resumeErr
	}
	return attempt()
}

// shutdownThreadTolerateExited runs thread/shutdown and treats an
// already-exited session as a no-op success. Shutdown's goal is a stopped
// daemon; if the session the hub knows is already gone, that goal is met, so
// we must NOT resurrect it just to kill it (kata qp94 carve-out). An unknown
// ref or any non-session-unavailable failure is still returned unchanged.
func shutdownThreadTolerateExited(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadShutdownParams) error {
	_, err := withSessionActionOwnership(ctx, cfg, params.Ref, "", func() (struct{}, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return struct{}{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "shutdown"); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, source.ShutdownThread(ctx, params)
	})
	if err != nil && params.Ref != "" && hubKnowsRef(cfg, params.Ref) && isSessionUnavailableError(err) {
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

func setGoalWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	return withSessionResume(ctx, cfg, sources, params.Ref, "", func() (appwire.GoalSetResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.GoalSetResponse{}, err
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
	return withSessionResume(ctx, cfg, sources, params.Ref, params.ClientMutationID, func() (appwire.NotesHumanSetResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.NotesHumanSetResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "shared-notes"); err != nil {
			return appwire.NotesHumanSetResponse{}, err
		}
		return source.NotesHumanSet(ctx, params)
	})
}

// removeURLWithResume relays urls/remove to the owning source, resuming an
// exited session first like goal/set, gated on the shared-notes capability.
func removeURLWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	return withSessionResume(ctx, cfg, sources, params.Ref, params.ClientMutationID, func() (appwire.UrlsRemoveResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.UrlsRemoveResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "shared-notes"); err != nil {
			return appwire.UrlsRemoveResponse{}, err
		}
		return source.UrlsRemove(ctx, params)
	})
}
