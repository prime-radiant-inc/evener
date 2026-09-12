package hub

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

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

// sameDaemonIdentity compares the process identity of two rendezvous entries:
// the same daemon re-published or re-probed keeps every field, while a
// replacement differs in at least one.
func sameDaemonIdentity(a, b rendezvous.Entry) bool {
	return a.PID == b.PID && a.StartedAt.Equal(b.StartedAt) && a.InstanceID == b.InstanceID && a.Endpoint == b.Endpoint
}

// awaitRetiredOwner waits for the exact retired owner to exit and verifies the
// exit under existing discovery authority. Confirmed absence — never mere
// unreachability — is the only success: an open failure, a wait failure, an
// incomplete roster scan, or an owner the roster still confirms all yield the
// retryable lifecycle error (or the caller's context error), and no spawn is
// attempted on this path.
func awaitRetiredOwner(ctx context.Context, cfg hubcore.WebConfig, entry rendezvous.Entry) error {
	controller := cfg.DaemonProcesses
	if controller == nil {
		controller = daemonprocess.NewController()
	}
	proc, err := controller.Open(daemonprocess.Target{
		PID:       entry.PID,
		SessionID: entry.SessionID,
		StateDir:  entry.StateDir,
		StartedAt: entry.StartedAt,
	})
	if err != nil && !errors.Is(err, daemonprocess.ErrExited) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return appwire.LifecycleUnavailable("retiring")
	}
	if proc != nil {
		defer proc.Close()
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
func resumeAfterConfirmedRetirement(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.TurnStartParams) error {
	if err := deletionFenceError(cfg, params.Ref, params.ThreadID, params.ClientMutationID); err != nil {
		return err
	}
	sessionID := deletionThreadID(params.Ref, params.ThreadID)
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

	target, aliases, err := resumeOwnership(cfg, sessionID, sessionID)
	if err != nil {
		return appwire.Unavailable(err.Error())
	}
	epochs := make(map[string]uint64, len(aliases))
	for _, id := range aliases {
		epochs[id] = sessionRequestRecoveryEpoch(ctx, cfg, "", id)
	}
	epochs[sessionID] = epoch
	// Force stop's sorted ownership order, retaining the original mutexes.
	for _, id := range aliases {
		cfg.ResumeLocks.For(id).Lock()
	}
	defer func() {
		for _, id := range slices.Backward(aliases) {
			cfg.ResumeLocks.For(id).Unlock()
		}
	}()
	for _, id := range aliases {
		if err := retirementAdmissionRecoveryError(cfg, id, epochs[id]); err != nil {
			return err
		}
	}
	if target != sessionID {
		if err := deletionFenceError(cfg, "", target, ""); err != nil {
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
	// of launching twice.
	_, err = resumeThreadLocked(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: sessionID})
	return err
}
