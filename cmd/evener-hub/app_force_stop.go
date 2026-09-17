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
		addressed, err := forceStopEntry(cfg.RunDir, ref.ThreadID, cfg.DaemonProcesses, nil, recoveryTarget, params.ExpectedDaemon)
		if err != nil {
			return appwire.Unavailable(err.Error())
		}
		if err := expectedDaemonConflict(addressed, params.ExpectedDaemon); err != nil {
			return err
		}
	}
	if stopped, err := confirmedStoppedWithoutClaim(ctx, cfg, ref.ThreadID, true); err != nil {
		return forceStopResumeStopError(err)
	} else if stopped {
		refreshAfterForceStop(ctx, cfg)
		return nil
	}
	if stop := cfg.ResumeLocks.BeginActiveResumeStop(ref.ThreadID); stop != nil {
		defer stop.Release()
		if err := stop.Wait(ctx); err != nil {
			return forceStopResumeStopError(err)
		}
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
	var descendantFinishes []func(bool)
	defer func() {
		for _, finish := range descendantFinishes {
			finish(false)
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
			descendantFinishes = append(descendantFinishes, cfg.ResumeLocks.BeginForceStop(added))
		}
		return nil
	}
	// These are admission fences, not aliases or durable resume targets. Hub
	// restart drops the old connections and queued requests altogether.
	if err := fenceDescendants(ctx); err != nil {
		return err
	}
	aliases := forceStopAliases(entry)
	finishRecovery := cfg.ResumeLocks.BeginForceStop(aliases)
	defer func() { finishRecovery(stopErr == nil) }()
	// Clear gives one daemon stable and current session aliases. Lock both so
	// resume or deletion through either alias cannot race exit confirmation.
	acquired := 0
	defer func() {
		for _, alias := range slices.Backward(aliases[:acquired]) {
			cfg.ResumeLocks.For(alias).Unlock()
		}
	}()
	// Deletion publication takes the same per-alias reservations an in-flight
	// Resume holds across its launch. Take them before cancelling whenever they
	// are free, so the deletion re-check under them is the final validation and
	// runs before cancelActiveResumes aborts a Resume the request may still have
	// to refuse. A reservation an in-flight launch already holds blocks deletion
	// publication itself, so that case keeps the cancel-then-acquire order, and
	// the re-check after ownership below stays authoritative.
	reservationsHeld := tryLockForceStopReservations(cfg.ResumeLocks, aliases)
	if reservationsHeld {
		acquired = len(aliases)
		for _, alias := range aliases {
			if err := deletionFenceError(cfg, "", alias, ""); err != nil {
				return err
			}
		}
	}
	// A Resume can register after the initial snapshot while process discovery
	// is running. The fence now prevents new registrations; cancel and drain
	// any operation that entered that window before waiting for ownership.
	releaseResumes, err := cancelActiveResumes(ctx, cfg.ResumeLocks, aliases)
	if err != nil {
		return forceStopResumeStopError(err)
	}
	defer releaseResumes()
	if sources != nil {
		if source, ok := sources.Source("local"); ok {
			if local, ok := source.(*appsource.LocalDaemonSource); ok {
				release := local.BeginRecovery(entry)
				defer release()
			}
		}
	}
	if !reservationsHeld {
		for _, id := range aliases {
			if err := cfg.ResumeLocks.For(id).LockContext(ctx); err != nil {
				return err
			}
			acquired++
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	currentTarget := cfg.ResumeLocks.RecoveryState(ref.ThreadID).ResumeSessionID
	if currentTarget != recoveryTarget {
		return appwire.Unavailable("session recovery authority changed; retry force stop")
	}
	current, err := forceStopRereadEntry(cfg.RunDir, ref.ThreadID, entry, cfg.DaemonProcesses, currentTarget, params.ExpectedDaemon)
	if err != nil {
		return appwire.Unavailable(err.Error())
	}
	if err := expectedDaemonConflict(current, params.ExpectedDaemon); err != nil {
		return err
	}
	// A deletion record may name any alias in the ownership group, not only
	// the one the request addressed. Loop over every alias here, the way the
	// reservationsHeld branch and confirmedStoppedWithoutClaim already do, so
	// a sibling-alias fence cannot slip past the cancel-then-acquire fallback
	// before the exited/kill branch.
	for _, alias := range aliases {
		if err := deletionFenceError(cfg, "", alias, ""); err != nil {
			return err
		}
	}
	if exited {
		// There is no retained process handle to reverify. An unchanged marker
		// alone cannot confirm absence after waiting for ownership.
		process, err = controller.Open(target)
		if !errors.Is(err, daemonprocess.ErrExited) {
			if err == nil {
				err = errors.New("daemon is live after initial exit observation")
			}
			return appwire.Unavailable(fmt.Sprintf("daemon exit is not confirmed: %v", err))
		}
		// An exited claim cannot replace newer authority held by any alias
		// it would overwrite, even when the requested alias still names it.
		for _, alias := range aliases {
			authority := cfg.ResumeLocks.RecoveryState(alias).ResumeSessionID
			if authority != "" && authority != target.SessionID {
				return appwire.Unavailable("exited daemon claim conflicts with alias recovery authority")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Commit recovery authority before the signal can take effect. Interrupted
	// signaling conservatively retains the explicit-Resume requirement.
	if err := cfg.ResumeLocks.PersistForceStop(aliases, target.SessionID); err != nil {
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
// without waiting for child reaping while holding any session mutex.
func cancelActiveResumes(ctx context.Context, locks *hubcore.ResumeLocks, aliases []string) (func(), error) {
	var stops []*hubcore.ResumeStop
	release := func() {
		for _, stop := range stops {
			stop.Release()
		}
	}
	for _, alias := range aliases {
		if stop := locks.BeginActiveResumeStop(alias); stop != nil {
			stops = append(stops, stop)
			if err := stop.Wait(ctx); err != nil {
				release()
				return nil, err
			}
		}
	}
	return release, nil
}

// tryLockForceStopReservations takes every per-alias reservation without
// blocking. It returns true only when all of them are held, leaving them held
// for the caller's deferred release; on any failure it releases the prefix it
// took. Holding them excludes deletion publication, which takes the same
// reservations, so a deletion check made while they are held is final.
func tryLockForceStopReservations(locks *hubcore.ResumeLocks, aliases []string) bool {
	acquired := 0
	for _, alias := range aliases {
		if !locks.For(alias).TryLock() {
			for _, held := range slices.Backward(aliases[:acquired]) {
				locks.For(held).Unlock()
			}
			return false
		}
		acquired++
	}
	return true
}

// A missing marker is not proof of exit. Only already-confirmed durable
// recovery authority can authorize this no-op, and only if strict discovery
// finds no claim against ANY alias while all those aliases are reserved.
func confirmedStoppedWithoutClaim(ctx context.Context, cfg hubcore.WebConfig, sessionID string, stopResumes bool) (bool, error) {
	if cfg.ResumeLocks == nil || cfg.RunDir == "" {
		return false, nil
	}
	state := cfg.ResumeLocks.RecoveryState(sessionID)
	if !state.ResumeRequired || !state.ExitConfirmed || state.ResumeSessionID == "" {
		return false, nil
	}
	if !stopResumes && state.Stopping != 0 {
		return false, nil
	}
	aliases := cfg.ResumeLocks.RecoveryAliases(sessionID)
	slices.Sort(aliases)
	if stopResumes {
		finish := cfg.ResumeLocks.BeginForceStop(aliases)
		defer finish(false)
		// A deletion record may name any alias in the ownership group, not only
		// the one the request addressed. Refuse a deleted group here, before any
		// cancellation: the authoritative per-alias re-check runs under the alias
		// locks, which is only reachable after the in-flight Resume has already
		// been aborted. That re-check still runs, so a deletion that starts in
		// this window is still caught.
		for _, alias := range aliases {
			if err := deletionFenceError(cfg, "", alias, ""); err != nil {
				return false, err
			}
		}
	} else if cfg.ResumeLocks.HasActiveResume(aliases) {
		// Ordinary shutdown is not force stop: do not cancel a pending restore,
		// change its admission epochs, or wait behind it to manufacture a no-op.
		return false, nil
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
		for _, alias := range aliases {
			if err := deletionFenceError(cfg, "", alias, ""); err != nil {
				return false, err
			}
		}
	}
	if stopResumes {
		releaseResumes, err := cancelActiveResumes(ctx, cfg.ResumeLocks, aliases)
		if err != nil {
			return false, err
		}
		defer releaseResumes()
	}
	// Capture after our own fences and cancellations, then compare the entire
	// authority (including epochs/group identity) after acquiring ownership.
	expected := make(map[string]hubcore.SessionRecoveryState, len(aliases))
	for _, alias := range aliases {
		current := cfg.ResumeLocks.RecoveryState(alias)
		if !current.ResumeRequired || !current.ExitConfirmed || current.ResumeSessionID != state.ResumeSessionID {
			if !stopResumes {
				return false, nil
			}
			return false, appwire.Unavailable("session recovery authority changed; retry force stop")
		}
		expected[alias] = current
	}
	if !reservationsHeld {
		for _, alias := range aliases {
			if err := cfg.ResumeLocks.For(alias).LockContext(ctx); err != nil {
				return false, err
			}
			acquired++
		}
	}
	// Every alias in the resolved group is an identity a deletion record may
	// name. The shortcut is only a no-op while none of them is deleted, or a
	// deletion of another alias in the same group is bypassed and reported as
	// success.
	for _, alias := range aliases {
		if err := deletionFenceError(cfg, "", alias, ""); err != nil {
			return false, err
		}
	}
	currentAliases := cfg.ResumeLocks.RecoveryAliases(sessionID)
	slices.Sort(currentAliases)
	if !slices.Equal(aliases, currentAliases) {
		if !stopResumes {
			return false, nil
		}
		return false, appwire.Unavailable("session recovery aliases changed; retry force stop")
	}
	for _, alias := range aliases {
		if cfg.ResumeLocks.RecoveryState(alias) != expected[alias] {
			if !stopResumes {
				return false, nil
			}
			return false, appwire.Unavailable("session recovery authority changed; retry force stop")
		}
	}
	if !stopResumes && cfg.ResumeLocks.HasActiveResume(aliases) {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	entries, err := rendezvous.ListStrict(cfg.RunDir)
	if err != nil {
		if !stopResumes {
			// Ordinary shutdown tolerates a transient discovery failure on a
			// session already confirmed exited: fall through to the source
			// attempt, which treats an already-exited session as a no-op.
			return false, nil
		}
		return false, appwire.Unavailable(err.Error())
	}
	for _, entry := range entries {
		for _, alias := range forceStopAliases(entry) {
			if slices.Contains(aliases, alias) {
				// An existing claim, even foreign or stale, must take the ordinary
				// verified process path. Never turn its eventual error into success.
				return false, nil
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
	return true, nil
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
