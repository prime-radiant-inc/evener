package hub

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// forceStopThread bypasses daemon RPC only after verifying the local process.
// The session remains reserved until the exact process has exited.
func forceStopThread(ctx context.Context, cfg hubcore.WebConfig, params appwire.ThreadForceStopParams, sources *appsource.Registry) (stopErr error) {
	ref, err := appwire.ParseRef(params.Ref)
	if err != nil || ref.SourceID != "local" {
		return appwire.InvalidParams("force stop requires a local session ref")
	}
	if cfg.RunDir == "" || cfg.ResumeLocks == nil {
		return appwire.Unavailable("local session ownership is not configured")
	}
	// A deleted target and a caller-rendered daemon identity are validated
	// before the confirmed-stopped shortcut or any cancellation fence: a request
	// that is going to be refused must not abort the in-flight Resume it can no
	// longer address. confirmedStoppedWithoutClaim itself cancels active resumes
	// on its stopResumes branch, so its validation and the caller's must both
	// precede it. A nil identity preserves the ref-only Stop intent, which may
	// legitimately abort a launch before an addressable claim exists.
	if err := deletionFenceError(cfg, params.Ref, ref.ThreadID, ""); err != nil {
		return err
	}
	if params.ExpectedDaemon != nil {
		recoveryTarget := cfg.ResumeLocks.RecoveryState(ref.ThreadID).ResumeSessionID
		if err := expectedDaemonRevalidationError(cfg, ref.ThreadID, recoveryTarget, params.ExpectedDaemon); err != nil {
			return err
		}
	}
	if stopped, err := confirmedStoppedWithoutClaim(ctx, cfg, ref.ThreadID, true, params.ExpectedDaemon); err != nil {
		return forceStopResumeStopError(err)
	} else if stopped {
		refreshAfterForceStop(ctx, cfg)
		return nil
	}
	// A deletion record may name any alias in the ownership group of the
	// Resume this drain would cancel, not only the requested alias. Resolve
	// the group first and validate every alias's fence before the abort: a
	// request a sibling-alias fence refuses must not cancel the in-flight
	// Resume first — the ordering invariant the verified-process path keeps
	// under its own reservations. The validation is final only while every
	// alias's reservation stays held across the cancellation: by this
	// request, or by an active Resume the drain is about to cancel, whose
	// launch holds its aliases until the abort settles. A Resume between its
	// registration and reacquiring its aliases holds nothing, so when
	// another action holds one of the group's reservations it can release
	// inside the check-to-cancel window and a deletion may publish after the
	// check and before the abort — such a cancellation is deferred to the
	// main path, which revalidates the deletion under its own reservations
	// and complete ownership before its drain. New Resume admissions stay
	// fenced across the drain by the stop fence itself.
	// canceledResumes tracks whether this request actually stopped an active
	// Resume: a refusal that follows keeps the fence's advance only when it
	// does, because the stop attempt then already mutated the world those
	// epochs guard.
	var canceledResumes bool
	// entryResumeAliases is the ownership group of the Resume that was active
	// when the request arrived, resolved before any cancellation. The deletion
	// validations below keep covering it whether the entry drain ran above
	// or was deferred to the drain below.
	var entryResumeAliases []string
	if stopAliases := cfg.ResumeLocks.ActiveResumeStopAliases(ref.ThreadID); stopAliases != nil {
		entryResumeAliases = stopAliases
		reservationsHeld := tryLockForceStopReservations(cfg.ResumeLocks, stopAliases)
		releaseReservations := func() {
			if reservationsHeld {
				unlockForceStopReservations(cfg.ResumeLocks, stopAliases)
			}
		}
		if err := deletionFenceErrorForGroup(cfg, stopAliases); err != nil {
			releaseReservations()
			return err
		}
		if reservationsHeld || cfg.ResumeLocks.HeldByActiveResumes(stopAliases) {
			if stop := cfg.ResumeLocks.BeginActiveResumeStop(ref.ThreadID); stop != nil {
				canceledResumes = true
				defer stop.Release()
				if err := stop.Wait(ctx); err != nil {
					releaseReservations()
					return forceStopResumeStopError(err)
				}
			}
		}
		releaseReservations()
	}
	recoveryTarget := cfg.ResumeLocks.RecoveryState(ref.ThreadID).ResumeSessionID
	entry, err := forceStopEntry(cfg.RunDir, ref.ThreadID, cfg.DaemonProcesses, nil, recoveryTarget, params.ExpectedDaemon)
	if err != nil {
		return appwire.Unavailable(err.Error())
	}
	// A caller-rendered daemon identity is compared against verified
	// discovery before any admission or recovery fence, so a stale row can
	// never signal a replacement process.
	if err := expectedDaemonConflict(entry, params.ExpectedDaemon); err != nil {
		return err
	}
	// Verify the process before interrupting RPCs. Kill verifies this retained
	// process handle again after ownership and deletion reservations are held.
	controller := cfg.DaemonProcesses
	if controller == nil {
		controller = daemonprocess.NewController()
	}
	target := hubcore.DaemonTarget(entry)
	process, err := controller.Open(target)
	exited := errors.Is(err, daemonprocess.ErrExited)
	if err != nil && !exited {
		return appwire.Unavailable(fmt.Sprintf("cannot verify daemon for force stop: %v", err))
	}
	defer func() {
		if process == nil {
			return
		}
		if err := process.Close(); err != nil {
			log.Printf("force stop process handle cleanup: %v", err)
		}
	}()
	if exited && recoveryTarget != "" && target.SessionID != recoveryTarget {
		return appwire.Unavailable("exited daemon claim does not match session recovery authority")
	}
	// Descendants share the stopped process, but keep independent transcript
	// identities. Their old requests must expire even after root ownership clears.
	if err := ctx.Err(); err != nil {
		return err
	}
	fenced := make(map[string]bool)
	var descendantFences []*hubcore.ForceStopFence
	// A refusal that has canceled nothing must not leave the descendant
	// fences' connection sequences behind either — they advanced no
	// admission epoch at begin, so the sequences are all a refusal gives
	// back: existing descendant clients stay valid despite the refusal.
	// Fences installed after the cancellation — the post-termination scan —
	// are never rejected: termination was attempted and the stop's
	// follow-through must stale connections admitted before it.
	rejectDescendants := func() {
		for _, fence := range descendantFences {
			fence.Reject()
		}
	}
	defer func() {
		for _, fence := range descendantFences {
			fence.Finish(false)
		}
	}()
	fenceDescendants := func(scanCtx context.Context) error {
		if entry.StateDir == "" {
			return nil
		}
		ids, err := agent.SessionOwnedDelegateIDs(scanCtx, entry.StateDir, target.SessionID)
		if err != nil {
			return appwire.Unavailable(fmt.Sprintf("read daemon delegates: %v", err))
		}
		var added []string
		for _, id := range ids {
			if !fenced[id] {
				fenced[id] = true
				added = append(added, id)
			}
		}
		if len(added) != 0 {
			descendantFences = append(descendantFences, cfg.ResumeLocks.BeginForceStop(added))
		}
		return nil
	}
	// These are admission fences, not aliases or durable resume targets. Hub
	// restart drops the old connections and queued requests altogether.
	if err := fenceDescendants(ctx); err != nil {
		return err
	}
	// preCancellationDescendants are the descendant fences installed before
	// the drain below: a post-drain refusal that canceled nothing rejects
	// exactly these alongside its own recovery fence. The post-termination
	// scan below appends to descendantFences only after termination was
	// attempted; its fences are never rejected.
	preCancellationDescendants := slices.Clone(descendantFences)
	aliases := forceStopAliases(entry)
	// The deletion fence covers the entry-active Resume's ownership group as
	// well: a record naming one of its aliases must refuse this request the
	// same way one naming the daemon's own aliases does, whether the entry
	// drain above ran or was deferred to the drain below. The extra aliases
	// are validated refusal-only before the drain, then reserved alongside
	// the daemon's own aliases through the authoritative re-check and the
	// process-control operation, so no record can publish on them after the
	// final fence check and race the force-stop signal.
	deletionFenceAliases := aliases
	if len(entryResumeAliases) != 0 {
		deletionFenceAliases = append(slices.Clone(aliases), entryResumeAliases...)
		slices.Sort(deletionFenceAliases)
		deletionFenceAliases = slices.Compact(deletionFenceAliases)
	}
	fenceRecovery := cfg.ResumeLocks.BeginForceStop(aliases)
	defer func() { fenceRecovery.Finish(stopErr == nil) }()
	// Clear gives one daemon stable and current session aliases. Lock both so
	// resume or deletion through either alias cannot race exit confirmation.
	// reserved records every per-alias reservation this request holds, in
	// acquisition order; the deferred release walks it backward.
	var reserved []string
	defer func() { unlockForceStopReservations(cfg.ResumeLocks, reserved) }()
	// Deletion publication takes the same per-alias reservations an in-flight
	// Resume holds across its launch. Take them before cancelling whenever they
	// are free, so the deletion re-check under them is the final validation and
	// runs before cancelActiveResumes aborts a Resume the request may still have
	// to refuse. A reservation an in-flight launch already holds blocks deletion
	// publication itself, so that case keeps the cancel-then-acquire order, and
	// the re-check after ownership below stays authoritative.
	reservationsHeld := tryLockForceStopReservations(cfg.ResumeLocks, aliases)
	if reservationsHeld {
		reserved = append(reserved, aliases...)
		if err := deletionFenceErrorForGroup(cfg, deletionFenceAliases); err != nil {
			// The refusal canceled nothing: the fence advanced no admission
			// epoch at begin, so only the connection sequences — this
			// fence's and the descendant fences' — roll back.
			rejectDescendants()
			fenceRecovery.Reject()
			return err
		}
	}
	// A caller-rendered identity is revalidated here — under the admission
	// fence, after the reservation attempt, immediately before
	// cancelActiveResumes — because a replacement claim can appear after the
	// pre-fence validation, and only a check synchronized with the fence keeps
	// the cancellation below from aborting the replacement Resume a stale
	// request can no longer address. With every alias reservation held no
	// Resume is mid-launch (a launch holds its aliases across the claim), so no
	// Resume-originated claim can appear between this check and the
	// cancellation; a launch already holding a reservation keeps the
	// cancel-then-acquire order, where a claim landing during the drain stays
	// subject to the authoritative post-ownership reread below.
	if params.ExpectedDaemon != nil {
		if err := expectedDaemonRevalidationError(cfg, ref.ThreadID, recoveryTarget, params.ExpectedDaemon); err != nil {
			// Refused before any cancellation: the fence published no
			// admission epoch at begin, so the in-flight Resume the refusal
			// preserves still completes its recovery clear against the epoch
			// it was admitted under; Reject rolls back only the connection
			// sequences this fence — and the descendant fences — wrote.
			rejectDescendants()
			fenceRecovery.Reject()
			return err
		}
	}
	// A Resume can register after the initial snapshot while process discovery
	// is running. The fence now prevents new registrations; cancel and drain
	// any operation that entered that window before waiting for ownership.
	releaseResumes, drainCanceled, err := cancelActiveResumes(ctx, cfg.ResumeLocks, aliases)
	if err != nil {
		return forceStopResumeStopError(err)
	}
	canceledResumes = canceledResumes || drainCanceled
	defer releaseResumes()
	// A refusal after the drain publishes the fence's epoch advance only when
	// the drain actually stopped a Resume (its Finish lands unrejected). A
	// drain that canceled nothing leaves the refusal admission-neutral, the
	// way the pre-cancellation refusals above are: the fence advanced no
	// epoch at begin, and the connection-level sequences this fence — and the
	// pre-cancellation descendant fences — wrote roll back before the release
	// below. The post-termination scan's fences are
	// excluded: termination was attempted and the stop's follow-through must
	// stale connections admitted before it.
	refuseStop := func(err error) error {
		if !canceledResumes {
			// The refusal canceled nothing, so it must not leave the descendant
			// fences' advance behind either.
			for _, fence := range preCancellationDescendants {
				fence.Reject()
			}
			fenceRecovery.Reject()
		}
		return err
	}
	if sources != nil {
		if source, ok := sources.Source("local"); ok {
			if local, ok := source.(*appsource.LocalDaemonSource); ok {
				release := local.BeginRecovery(entry)
				defer release()
			}
		}
	}
	// The authoritative re-check below, and the process-control operation it
	// guards, must be final for every alias a deletion record may name — not
	// only the daemon's own aliases but also the entry-active Resume's group
	// aliases the deletion-fence set resolved. Reserve the complete
	// deduplicated set across both: take the extra aliases whenever they are
	// free, and when one is held — by an in-flight launch or an unrelated
	// action — hand the daemon aliases back and take the complete set in one
	// sorted pass. Waiting for a held alias while holding the others could
	// deadlock a registration already parked on them; the sorted pass uses the
	// acquisition order RegisterResume and a launch use, so it cannot.
	deletionFenceExtras := slices.DeleteFunc(slices.Clone(deletionFenceAliases), func(id string) bool {
		return slices.Contains(aliases, id)
	})
	if reservationsHeld && !tryLockForceStopReservations(cfg.ResumeLocks, deletionFenceExtras) {
		unlockForceStopReservations(cfg.ResumeLocks, reserved)
		reserved, reservationsHeld = nil, false
	} else if reservationsHeld {
		reserved = append(reserved, deletionFenceExtras...)
	}
	if !reservationsHeld {
		for _, id := range deletionFenceAliases {
			if err := cfg.ResumeLocks.For(id).LockContext(ctx); err != nil {
				return refuseStop(err)
			}
			reserved = append(reserved, id)
		}
	}
	if err := ctx.Err(); err != nil {
		return refuseStop(err)
	}
	currentTarget := cfg.ResumeLocks.RecoveryState(ref.ThreadID).ResumeSessionID
	if currentTarget != recoveryTarget {
		return refuseStop(appwire.Unavailable("session recovery authority changed; retry force stop"))
	}
	current, err := forceStopRereadEntry(cfg.RunDir, ref.ThreadID, entry, cfg.DaemonProcesses, currentTarget, params.ExpectedDaemon)
	if err != nil {
		return refuseStop(appwire.Unavailable(err.Error()))
	}
	if err := expectedDaemonConflict(current, params.ExpectedDaemon); err != nil {
		return refuseStop(err)
	}
	// A deletion record may name any alias in the ownership group, not only
	// the one the request addressed — including the entry-active Resume's
	// group. Loop over every alias here, the way the reservationsHeld branch
	// and confirmedStoppedWithoutClaim already do, so a sibling-alias fence
	// cannot slip past the cancel-then-acquire fallback before the
	// exited/kill branch.
	if err := deletionFenceErrorForGroup(cfg, deletionFenceAliases); err != nil {
		return refuseStop(err)
	}
	if exited {
		// There is no retained process handle to reverify. An unchanged marker
		// alone cannot confirm absence after waiting for ownership.
		process, err = controller.Open(target)
		if !errors.Is(err, daemonprocess.ErrExited) {
			if err == nil {
				err = errors.New("daemon is live after initial exit observation")
			}
			return refuseStop(appwire.Unavailable(fmt.Sprintf("daemon exit is not confirmed: %v", err)))
		}
		// An exited claim cannot replace newer authority held by any alias
		// it would overwrite, even when the requested alias still names it.
		for _, alias := range aliases {
			authority := cfg.ResumeLocks.RecoveryState(alias).ResumeSessionID
			if authority != "" && authority != target.SessionID {
				return refuseStop(appwire.Unavailable("exited daemon claim conflicts with alias recovery authority"))
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return refuseStop(err)
	}
	// Commit recovery authority before the signal can take effect. Interrupted
	// signaling conservatively retains the explicit-Resume requirement.
	if committed, err := cfg.ResumeLocks.PersistForceStopWithOwner(aliases, target.SessionID, target); err != nil {
		if !committed {
			// Nothing committed and nothing signaled, so this refusal canceled
			// nothing: like every other such refusal it stays admission-neutral
			// — an unrejected Finish would mint an epoch and sequence advance
			// for a record that never landed.
			return refuseStop(appwire.Unavailable(fmt.Sprintf("persist session recovery: %v", err)))
		}
		// The record landed (the rename committed; a later sync step failed):
		// the recovery obligation is installed, so this refusal DID mutate the
		// world and the root fence must publish — pre-stop admissions are stale
		// and the obligation stands for the explicit Resume to answer. No
		// signal ever reached the shared process, though, so the
		// pre-cancellation descendant fences guard processes nothing happened
		// to: they reject like every refusal that canceled nothing.
		if !canceledResumes {
			for _, fence := range preCancellationDescendants {
				fence.Reject()
			}
		}
		return appwire.Unavailable(fmt.Sprintf("persist session recovery: %v", err))
	}
	if exited {
		if err := cfg.ResumeLocks.ConfirmForceStop(target.SessionID); err != nil {
			return appwire.Unavailable(fmt.Sprintf("persist confirmed daemon exit: %v", err))
		}
		refreshAfterForceStop(ctx, cfg)
		return nil
	}
	if err := process.Kill(); err != nil && !errors.Is(err, daemonprocess.ErrExited) {
		return appwire.Unavailable(fmt.Sprintf("cannot force stop daemon: %v", err))
	}
	exitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := process.Wait(exitCtx); err != nil {
		return appwire.Unavailable(fmt.Sprintf("daemon exit is not confirmed: %v", err))
	}
	// Exit makes the ownership journal stable. Capture delegates created while
	// termination was in progress before releasing any recovery admission fence.
	scanCtx, cancelScan := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelScan()
	if err := fenceDescendants(scanCtx); err != nil {
		return err
	}
	if err := cfg.ResumeLocks.ConfirmForceStop(target.SessionID); err != nil {
		return appwire.Unavailable(fmt.Sprintf("persist confirmed daemon exit: %v", err))
	}
	refreshAfterForceStop(ctx, cfg)
	return nil
}

// forceStopResumeStopError classifies a Stop wait failure at the force-stop
// boundary. A retained child-cleanup failure is a retryable "cleanup remains
// unconfirmed" state and must surface as appwire.Unavailable; a request-context
// cancellation or deadline keeps propagating unchanged so the ordinary request
// lifecycle still governs the response.
func forceStopResumeStopError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if _, ok := errors.AsType[*resumeCleanupError](err); ok {
		return appwire.Unavailable(err.Error())
	}
	return err
}

// cancelActiveResumes is called after Stop installed its admission fences, but
// before it waits for ownership. It closes the discovery/registration race
// without waiting for child reaping while holding any session mutex. It also
// reports whether any alias had an active Resume to cancel, so a refusal that
// follows can tell a drain that mutated the world from one that canceled
// nothing.
func cancelActiveResumes(ctx context.Context, locks *hubcore.ResumeLocks, aliases []string) (func(), bool, error) {
	var stops []*hubcore.ResumeStop
	canceled := false
	release := func() {
		for _, stop := range stops {
			stop.Release()
		}
	}
	for _, alias := range aliases {
		if stop := locks.BeginActiveResumeStop(alias); stop != nil {
			stops = append(stops, stop)
			canceled = true
			if err := stop.Wait(ctx); err != nil {
				release()
				return nil, canceled, err
			}
		}
	}
	return release, canceled, nil
}

// tryLockForceStopReservations takes every per-alias reservation without
// blocking. It returns true only when all of them are held, leaving them held
// for the caller's release; on any failure it releases the prefix it took.
// Holding them excludes deletion publication, which takes the same
// reservations, so a deletion check made while they are held is final.
func tryLockForceStopReservations(locks *hubcore.ResumeLocks, aliases []string) bool {
	acquired := 0
	for _, alias := range aliases {
		if !locks.For(alias).TryLock() {
			unlockForceStopReservations(locks, aliases[:acquired])
			return false
		}
		acquired++
	}
	return true
}

// unlockForceStopReservations releases reservations tryLockForceStopReservations
// left held, in reverse acquisition order.
func unlockForceStopReservations(locks *hubcore.ResumeLocks, aliases []string) {
	for _, alias := range slices.Backward(aliases) {
		locks.For(alias).Unlock()
	}
}

// confirmedStoppedDecision is checkConfirmedStoppedWithoutClaim's outcome.
// stopped reports the proven no-op. discoveryUncertain reports that strict
// rendezvous discovery failed for a session already confirmed exited, so the
// absence of a claim is unproven; ordinary shutdown resolves that uncertainty
// through its dedicated tolerant source path rather than the session-action
// gate, which would refuse the attempt as resume-required.
type confirmedStoppedDecision struct {
	stopped            bool
	discoveryUncertain bool
}

// A missing marker is not proof of exit. Only already-confirmed durable
// recovery authority can authorize this no-op, and only if strict discovery
// finds no claim against ANY alias while all those aliases are reserved. A
// non-nil expectedDaemon identity is reverified against current discovery
// immediately before the stopResumes cancellation, so a replacement claim
// appearing after the caller's own validation is refused before the in-flight
// Resume is aborted. confirmedStoppedWithoutClaim reports only the proven
// no-op; callers that must distinguish the tolerated discovery uncertainty
// (ordinary shutdown) use checkConfirmedStoppedWithoutClaim.
func confirmedStoppedWithoutClaim(ctx context.Context, cfg hubcore.WebConfig, sessionID string, stopResumes bool, expectedDaemon *appwire.DaemonIdentity) (bool, error) {
	decision, err := checkConfirmedStoppedWithoutClaim(ctx, cfg, sessionID, stopResumes, expectedDaemon)
	return decision.stopped, err
}

func checkConfirmedStoppedWithoutClaim(ctx context.Context, cfg hubcore.WebConfig, sessionID string, stopResumes bool, expectedDaemon *appwire.DaemonIdentity) (confirmedStoppedDecision, error) {
	if cfg.ResumeLocks == nil || cfg.RunDir == "" {
		return confirmedStoppedDecision{}, nil
	}
	state := cfg.ResumeLocks.RecoveryState(sessionID)
	if !state.ResumeRequired || !state.ExitConfirmed || state.ResumeSessionID == "" {
		return confirmedStoppedDecision{}, nil
	}
	if !stopResumes && state.Stopping != 0 {
		return confirmedStoppedDecision{}, nil
	}
	aliases := cfg.ResumeLocks.RecoveryAliases(sessionID)
	slices.Sort(aliases)
	var fence *hubcore.ForceStopFence
	// canceled reports whether the drain below stopped an active Resume: a
	// post-drain outcome keeps this fence's advance only when it did, because
	// the stop attempt then already mutated the world those epochs guard.
	var canceled bool
	if stopResumes {
		fence = cfg.ResumeLocks.BeginForceStop(aliases)
		defer fence.Finish(false)
		// A deletion record may name any alias in the ownership group, not only
		// the one the request addressed. Refuse a deleted group here, before any
		// cancellation: the authoritative per-alias re-check runs under the alias
		// locks, which is only reachable after the in-flight Resume has already
		// been aborted. That re-check still runs, so a deletion that starts in
		// this window is still caught.
		if err := deletionFenceErrorForGroup(cfg, aliases); err != nil {
			// The refusal canceled nothing; the fence advanced no
			// admission epoch at begin, so only its connection sequence
			// rolls back.
			fence.Reject()
			return confirmedStoppedDecision{}, err
		}
	} else if cfg.ResumeLocks.HasActiveResume(aliases) {
		// Ordinary shutdown is not force stop: do not cancel a pending restore,
		// change its admission epochs, or wait behind it to manufacture a no-op.
		return confirmedStoppedDecision{}, nil
	}
	acquired := 0
	defer func() {
		for _, alias := range slices.Backward(aliases[:acquired]) {
			cfg.ResumeLocks.For(alias).Unlock()
		}
	}()
	// Deletion publication takes the same per-alias reservations an in-flight
	// Resume does. Take them before cancelling whenever they are free, so the
	// deletion re-check below is the final validation and runs before
	// cancelActiveResumes aborts a Resume the request may still have to refuse.
	// A reservation an in-flight launch already holds blocks publication itself,
	// so that case falls through to the cancel-then-acquire order.
	reservationsHeld := stopResumes && tryLockForceStopReservations(cfg.ResumeLocks, aliases)
	if reservationsHeld {
		acquired = len(aliases)
		if err := deletionFenceErrorForGroup(cfg, aliases); err != nil {
			// The refusal canceled nothing; the fence advanced no
			// admission epoch at begin, so only its connection sequence
			// rolls back.
			fence.Reject()
			return confirmedStoppedDecision{}, err
		}
	}
	if stopResumes {
		if expectedDaemon != nil {
			// A replacement claim can appear after the caller's
			// pre-cancellation identity validation. Recheck the current
			// rendezvous identity before cancelActiveResumes aborts an
			// in-flight Resume the request may still have to refuse; the
			// discovery recheck under alias ownership below stays
			// authoritative.
			if err := expectedDaemonRevalidationError(cfg, sessionID, state.ResumeSessionID, expectedDaemon); err != nil {
				// Refused before any cancellation: the fence published no
				// admission epoch at begin, so the in-flight Resume the
				// refusal preserves still completes its recovery clear against
				// the epoch it was admitted under; Reject rolls back only the
				// connection sequence the fence wrote.
				fence.Reject()
				return confirmedStoppedDecision{}, err
			}
		}
		releaseResumes, drainCanceled, err := cancelActiveResumes(ctx, cfg.ResumeLocks, aliases)
		if err != nil {
			return confirmedStoppedDecision{}, err
		}
		canceled = drainCanceled
		defer releaseResumes()
	}
	// refuseAfterDrain wraps every post-drain return other than the proven
	// no-op. A refusal — or the {stopped:false} claim handoff, whose caller
	// installs its own fence next — must be admission-neutral when the drain
	// canceled nothing, exactly like the pre-cancellation refusals above:
	// otherwise a refused shortcut permanently advanced every alias's Epoch
	// and LastRecoverySequence, because forceStopThread's refusal paths reject
	// only the fences they hold themselves and cannot reach this one. A drain
	// that canceled a Resume keeps the advance — the stop attempt already
	// mutated the world those epochs guard. The proven no-op at the end never
	// passes through here: its advance is the admission invalidation that
	// re-admits waiting registrations on a post-decision snapshot.
	refuseAfterDrain := func(decision confirmedStoppedDecision, err error) (confirmedStoppedDecision, error) {
		if fence != nil && !canceled {
			fence.Reject()
		}
		return decision, err
	}
	// Capture after our own fences and cancellations, then compare the entire
	// authority (including epochs/group identity) after acquiring ownership.
	expected := make(map[string]hubcore.SessionRecoveryState, len(aliases))
	for _, alias := range aliases {
		current := cfg.ResumeLocks.RecoveryState(alias)
		if !current.ResumeRequired || !current.ExitConfirmed || current.ResumeSessionID != state.ResumeSessionID {
			if !stopResumes {
				return confirmedStoppedDecision{}, nil
			}
			return refuseAfterDrain(confirmedStoppedDecision{}, appwire.Unavailable("session recovery authority changed; retry force stop"))
		}
		expected[alias] = current
	}
	if !reservationsHeld {
		for _, alias := range aliases {
			if err := cfg.ResumeLocks.For(alias).LockContext(ctx); err != nil {
				return refuseAfterDrain(confirmedStoppedDecision{}, err)
			}
			acquired++
		}
	}
	// Every alias in the resolved group is an identity a deletion record may
	// name. The shortcut is only a no-op while none of them is deleted, or a
	// deletion of another alias in the same group is bypassed and reported as
	// success.
	if err := deletionFenceErrorForGroup(cfg, aliases); err != nil {
		return refuseAfterDrain(confirmedStoppedDecision{}, err)
	}
	currentAliases := cfg.ResumeLocks.RecoveryAliases(sessionID)
	slices.Sort(currentAliases)
	if !slices.Equal(aliases, currentAliases) {
		if !stopResumes {
			return confirmedStoppedDecision{}, nil
		}
		return refuseAfterDrain(confirmedStoppedDecision{}, appwire.Unavailable("session recovery aliases changed; retry force stop"))
	}
	for _, alias := range aliases {
		if cfg.ResumeLocks.RecoveryState(alias) != expected[alias] {
			if !stopResumes {
				return confirmedStoppedDecision{}, nil
			}
			return refuseAfterDrain(confirmedStoppedDecision{}, appwire.Unavailable("session recovery authority changed; retry force stop"))
		}
	}
	if !stopResumes && cfg.ResumeLocks.HasActiveResume(aliases) {
		return confirmedStoppedDecision{}, nil
	}
	if err := ctx.Err(); err != nil {
		return refuseAfterDrain(confirmedStoppedDecision{}, err)
	}
	entries, err := rendezvous.ListStrict(cfg.RunDir)
	if err != nil {
		if !stopResumes {
			// Ordinary shutdown tolerates a transient discovery failure on a
			// session already confirmed exited: report the uncertainty so the
			// caller's dedicated shutdown path runs the tolerant source
			// attempt, which treats an already-exited session as a no-op.
			// The session-action gate must not see this fall-through: it
			// refuses every action while the session stays ResumeRequired.
			// The tolerant attempt reacquires only the request's alias after
			// this reservation is released, so publish the decision the way
			// the confirmed-stopped no-op does: invalidate admission while
			// the aliases are still reserved, so a Resume registration
			// waiting for them re-admits on a snapshot taken after the
			// decision instead of launching on one taken before it.
			cfg.ResumeLocks.InvalidateResumeAdmission(aliases)
			return confirmedStoppedDecision{discoveryUncertain: true}, nil
		}
		return refuseAfterDrain(confirmedStoppedDecision{}, appwire.Unavailable(err.Error()))
	}
	for _, entry := range entries {
		for _, alias := range forceStopAliases(entry) {
			if slices.Contains(aliases, alias) {
				// An existing claim, even foreign or stale, must take the ordinary
				// verified process path. Never turn its eventual error into success.
				// The caller installs its own fence next, so this fence's
				// release stays admission-neutral — no epoch publication, the
				// connection sequence rolled back — unless the drain canceled a
				// Resume.
				return refuseAfterDrain(confirmedStoppedDecision{}, nil)
			}
		}
	}
	if !stopResumes {
		// Publish the no-op decision under the held alias tokens: a Resume
		// registration already waiting for them must re-admit on a snapshot
		// taken after shutdown reported the session stopped, not launch on
		// one taken before. The stopResumes path already invalidates these
		// waiters through its BeginForceStop fence; a fence here would also
		// refuse fresh admissions during the check, which ordinary shutdown
		// must not do — it manufactures no recovery obligation.
		cfg.ResumeLocks.InvalidateResumeAdmission(aliases)
	}
	return confirmedStoppedDecision{stopped: true}, nil
}

// forceStopOwnershipUnchanged revalidates discovery after acquiring every
// alias lock, before signaling the verified process.
func forceStopOwnershipUnchanged(runDir, sessionID string, previous rendezvous.Entry, controller daemonprocess.Controller, recoveryTarget string) error {
	_, err := forceStopRereadEntry(runDir, sessionID, previous, controller, recoveryTarget, nil)
	return err
}

// forceStopRereadEntry revalidates discovery after acquiring every alias
// lock and returns the current verified entry for the caller's own fences.
func forceStopRereadEntry(runDir, sessionID string, previous rendezvous.Entry, controller daemonprocess.Controller, recoveryTarget string, expected *appwire.DaemonIdentity) (rendezvous.Entry, error) {
	current, err := forceStopEntry(runDir, sessionID, controller, &previous, recoveryTarget, expected)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	if !sameForceStopEntry(current, previous) {
		return rendezvous.Entry{}, errors.New("daemon ownership changed; refresh the session before force stopping")
	}
	return current, nil
}

// expectedDaemonRevalidationError re-checks a caller-rendered daemon identity
// against current verified discovery. The force-stop paths run it before
// canceling an in-flight Resume the request may still have to refuse: a
// replacement claim can appear after any earlier validation. A discovery
// failure is a retryable transient state and surfaces as Unavailable; an
// identity mismatch keeps expectedDaemonConflict's refusal unchanged.
func expectedDaemonRevalidationError(cfg hubcore.WebConfig, sessionID, recoveryTarget string, expected *appwire.DaemonIdentity) error {
	addressed, err := forceStopEntry(cfg.RunDir, sessionID, cfg.DaemonProcesses, nil, recoveryTarget, expected)
	if err != nil {
		return appwire.Unavailable(err.Error())
	}
	return expectedDaemonConflict(addressed, expected)
}

// expectedDaemonConflict compares a caller-rendered daemon identity (the
// resident row the action was aimed at) against the verified current entry.
// A nil expectation preserves existing ref-only callers. The identity is
// recomputed from the current rendezvous entry on every call, so a cached
// Generation across a daemon replacement yields a conflict — the intended
// signal.
func expectedDaemonConflict(entry rendezvous.Entry, expected *appwire.DaemonIdentity) error {
	if expected == nil {
		return nil
	}
	if daemonIdentity(entry) != *expected {
		return appwire.Conflict("daemon identity changed since the resident row was rendered; refresh before acting")
	}
	return nil
}

// forceStopIdentityMatches reports whether entry is the exact resident the
// caller rendered into expected. A nil expectation preserves ref-only
// resolution. It reuses expectedDaemonConflict so selection and the caller's
// final conflict check compare through one identity implementation.
func forceStopIdentityMatches(entry rendezvous.Entry, expected *appwire.DaemonIdentity) bool {
	return expectedDaemonConflict(entry, expected) == nil
}

// sameForceStopEntry compares persisted ownership values; timestamp location
// pointers may differ across reads even when they represent the same instant.
func sameForceStopEntry(a, b rendezvous.Entry) bool {
	if !a.StartedAt.Equal(b.StartedAt) {
		return false
	}
	b.StartedAt = a.StartedAt
	return a == b
}

func forceStopEntry(runDir, sessionID string, controller daemonprocess.Controller, previous *rendezvous.Entry, recoveryTarget string, expected *appwire.DaemonIdentity) (rendezvous.Entry, error) {
	entries, err := rendezvous.ListStrict(runDir)
	if err != nil {
		return rendezvous.Entry{}, err
	}
	// A caller that names one exact daemon resolves it before the ref-ambiguity
	// checks: two live residents can share a ref, and the rendered identity says
	// which one the request means. Candidates must also claim the requested
	// session, so an identity can never retarget an unrelated resident. An
	// identity that matches no claim leaves the list untouched, so the caller's
	// own identity conflict check still refuses a stale or foreign row rather
	// than a same-ref replacement being selected.
	if expected != nil {
		var addressed []rendezvous.Entry
		for _, entry := range entries {
			if !forceStopIdentityMatches(entry, expected) {
				continue
			}
			if entry.SessionID != sessionID && entry.ThreadID != sessionID && entry.WorkspaceRef != "local:"+sessionID {
				continue
			}
			addressed = append(addressed, entry)
		}
		if len(addressed) != 0 {
			entries = addressed
		}
	}
	// Retained crash markers are evidence, not competing live ownership. Only
	// inspect overlapping claims, and retain every unresolved process identity.
	var targetAliases []string
	for _, entry := range entries {
		if slices.Contains(forceStopAliases(entry), sessionID) {
			targetAliases = append(targetAliases, forceStopAliases(entry)...)
		}
	}
	overlaps := func(entry rendezvous.Entry) bool {
		for _, alias := range forceStopAliases(entry) {
			if slices.Contains(targetAliases, alias) {
				return true
			}
		}
		return false
	}
	claims := 0
	for _, entry := range entries {
		if overlaps(entry) {
			claims++
		}
	}
	if claims > 1 {
		if controller == nil {
			controller = daemonprocess.NewController()
		}
		var exitedMatches []rendezvous.Entry
		entries = slices.DeleteFunc(entries, func(entry rendezvous.Entry) bool {
			if !overlaps(entry) {
				return false
			}
			process, err := controller.Open(hubcore.DaemonTarget(entry))
			if err == nil {
				_ = process.Close()
			}
			exited := errors.Is(err, daemonprocess.ErrExited)
			if exited && slices.Contains(forceStopAliases(entry), sessionID) {
				exitedMatches = append(exitedMatches, entry)
			}
			return exited
		})
		// Exited markers cannot establish which transcript is current. Durable
		// authority resolves differing targets; without it every direct claim
		// must agree. A reserved process is preferred only within that target.
		if len(exitedMatches) > 0 && !slices.ContainsFunc(entries, overlaps) {
			var selected rendezvous.Entry
			var selectedID string
			found := false
			for _, entry := range exitedMatches {
				id := entry.SessionID
				if id == "" {
					id = entry.ThreadID
				}
				if recoveryTarget != "" && id != recoveryTarget {
					continue
				}
				if found && id != selectedID {
					return rendezvous.Entry{}, errors.New("exited daemons claim different transcripts; cannot choose a force-stop target")
				}
				if !found || (previous != nil && sameForceStopEntry(entry, *previous)) {
					selected, selectedID = entry, id
				}
				found = true
			}
			if !found {
				return rendezvous.Entry{}, errors.New("no exited daemon claim matches session recovery authority")
			}
			entries = append(entries, selected)
		}
	}
	var match rendezvous.Entry
	found := false
	for _, entry := range entries {
		if entry.SessionID != sessionID && entry.ThreadID != sessionID && entry.WorkspaceRef != "local:"+sessionID {
			continue
		}
		if entry.SourceID != "" && entry.SourceID != "local" {
			return rendezvous.Entry{}, errors.New("daemon claims a foreign session source")
		}
		if found {
			return rendezvous.Entry{}, errors.New("multiple daemons claim this session; cannot choose a force-stop target")
		}
		match, found = entry, true
	}
	if !found {
		return rendezvous.Entry{}, errors.New("no direct daemon ownership claim for this session")
	}
	aliases := forceStopAliases(match)
	claims = 0
	for _, entry := range entries {
		for _, alias := range forceStopAliases(entry) {
			if slices.Contains(aliases, alias) {
				claims++
				break
			}
		}
	}
	if claims != 1 {
		return rendezvous.Entry{}, errors.New("multiple daemons claim this session; cannot choose a force-stop target")
	}
	return match, nil
}

func forceStopAliases(entry rendezvous.Entry) []string {
	aliases := []string{entry.SessionID, entry.ThreadID}
	if workspace, err := appwire.ParseRef(entry.WorkspaceRef); err == nil && workspace.SourceID == "local" {
		aliases = append(aliases, workspace.ThreadID)
	}
	slices.Sort(aliases)
	return slices.DeleteFunc(slices.Compact(aliases), func(id string) bool { return id == "" })
}

func refreshAfterForceStop(ctx context.Context, cfg hubcore.WebConfig) {
	if cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
			log.Printf("daemon stopped; roster refresh remains incomplete: %v", err)
		}
	}
	if cfg.Inputs != nil {
		cfg.Inputs.Bump()
	}
	if cfg.PokeAttention != nil {
		cfg.PokeAttention()
	}
}
