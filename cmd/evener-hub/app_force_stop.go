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
	recoveryTarget := cfg.ResumeLocks.RecoveryState(ref.ThreadID).ResumeSessionID
	entry, err := forceStopEntry(cfg.RunDir, ref.ThreadID, cfg.DaemonProcesses, nil, recoveryTarget)
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
	sessionID := entry.SessionID
	if sessionID == "" {
		sessionID = entry.ThreadID
	}
	target := daemonprocess.Target{PID: entry.PID, SessionID: sessionID, StateDir: entry.StateDir, StartedAt: entry.StartedAt}
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
	if exited && recoveryTarget != "" && sessionID != recoveryTarget {
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
		ids, err := agent.SessionOwnedDelegateIDs(scanCtx, entry.StateDir, sessionID)
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
	if sources != nil {
		if source, ok := sources.Source("local"); ok {
			if local, ok := source.(*appsource.LocalDaemonSource); ok {
				release := local.BeginRecovery(entry)
				defer release()
			}
		}
	}
	// Clear gives one daemon stable and current session aliases. Lock both so
	// resume or deletion through either alias cannot race exit confirmation.
	for _, id := range aliases {
		cfg.ResumeLocks.For(id).Lock()
	}
	defer func() {
		for _, alias := range slices.Backward(aliases) {
			cfg.ResumeLocks.For(alias).Unlock()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	currentTarget := cfg.ResumeLocks.RecoveryState(ref.ThreadID).ResumeSessionID
	if currentTarget != recoveryTarget {
		return appwire.Unavailable("session recovery authority changed; retry force stop")
	}
	current, err := forceStopRereadEntry(cfg.RunDir, ref.ThreadID, entry, cfg.DaemonProcesses, currentTarget)
	if err != nil {
		return appwire.Unavailable(err.Error())
	}
	if err := expectedDaemonConflict(current, params.ExpectedDaemon); err != nil {
		return err
	}
	if err := deletionFenceError(cfg, params.Ref, ref.ThreadID, ""); err != nil {
		return err
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
			if authority != "" && authority != sessionID {
				return appwire.Unavailable("exited daemon claim conflicts with alias recovery authority")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Commit recovery authority before the signal can take effect. Interrupted
	// signaling conservatively retains the explicit-Resume requirement.
	if err := cfg.ResumeLocks.PersistForceStop(aliases, sessionID); err != nil {
		return appwire.Unavailable(fmt.Sprintf("persist session recovery: %v", err))
	}
	if exited {
		if err := cfg.ResumeLocks.ConfirmForceStop(sessionID); err != nil {
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
	if err := cfg.ResumeLocks.ConfirmForceStop(sessionID); err != nil {
		return appwire.Unavailable(fmt.Sprintf("persist confirmed daemon exit: %v", err))
	}
	refreshAfterForceStop(ctx, cfg)
	return nil
}

// forceStopOwnershipUnchanged revalidates discovery after acquiring every
// alias lock, before signaling the verified process.
func forceStopOwnershipUnchanged(runDir, sessionID string, previous rendezvous.Entry, controller daemonprocess.Controller, recoveryTarget string) error {
	_, err := forceStopRereadEntry(runDir, sessionID, previous, controller, recoveryTarget)
	return err
}

// forceStopRereadEntry revalidates discovery after acquiring every alias
// lock and returns the current verified entry for the caller's own fences.
func forceStopRereadEntry(runDir, sessionID string, previous rendezvous.Entry, controller daemonprocess.Controller, recoveryTarget string) (rendezvous.Entry, error) {
	current, err := forceStopEntry(runDir, sessionID, controller, &previous, recoveryTarget)
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

// sameForceStopEntry compares persisted ownership values; timestamp location
// pointers may differ across reads even when they represent the same instant.
func sameForceStopEntry(a, b rendezvous.Entry) bool {
	if !a.StartedAt.Equal(b.StartedAt) {
		return false
	}
	b.StartedAt = a.StartedAt
	return a == b
}

func forceStopEntry(runDir, sessionID string, controller daemonprocess.Controller, previous *rendezvous.Entry, recoveryTarget string) (rendezvous.Entry, error) {
	entries, err := rendezvous.ListStrict(runDir)
	if err != nil {
		return rendezvous.Entry{}, err
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
			id := entry.SessionID
			if id == "" {
				id = entry.ThreadID
			}
			process, err := controller.Open(daemonprocess.Target{PID: entry.PID, SessionID: id, StateDir: entry.StateDir, StartedAt: entry.StartedAt})
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
