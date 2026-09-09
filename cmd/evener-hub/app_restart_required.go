package hub

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// daemonDiscoveryError describes uncertainty outside the requested ownership
// chain. Saved projections remain readable, but cannot grant mutation authority.
type daemonDiscoveryError struct {
	err error
}

func (e *daemonDiscoveryError) Error() string {
	return fmt.Sprintf("daemon ownership discovery failed: %v", e.err)
}
func (e *daemonDiscoveryError) Unwrap() error { return e.err }

func isDaemonDiscoveryError(err error) bool {
	var discovery *daemonDiscoveryError
	return errors.As(err, &discovery)
}

// restartRequiredDaemon only uses an authenticated probe's mismatch verdict.
// A rendezvous file or a live PID alone cannot establish daemon identity.
func restartRequiredDaemon(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string) (hubcore.LiveEntry, bool, error) {
	return lookupDaemonOwner(ctx, cfg, ref, threadID, false)
}

func lookupDaemonOwner(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string, verifyCompatibleAncestry bool) (hubcore.LiveEntry, bool, error) {
	if ref != "" {
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			return hubcore.LiveEntry{}, false, err
		}
		if parsed.SourceID != "local" {
			return hubcore.LiveEntry{}, false, nil
		}
		threadID = parsed.ThreadID
	}
	if cfg.Roster == nil || threadID == "" {
		return hubcore.LiveEntry{}, false, nil
	}
	type ownershipEdge struct {
		stateDir, ownerID, childID string
		isSubagent                 bool
	}
	var edges []ownershipEdge
	verifyOwner := func(entry hubcore.LiveEntry, unconfirmed bool) (hubcore.LiveEntry, bool, error) {
		if entry.Status != appwire.ThreadStatusRestartRequired && !unconfirmed && !verifyCompatibleAncestry {
			return entry, false, nil
		}
		for _, edge := range edges {
			owned, err := agent.SessionOwnsDelegate(ctx, edge.stateDir, edge.ownerID, edge.childID)
			if err != nil {
				return hubcore.LiveEntry{}, false, fmt.Errorf("read daemon ownership for %s: %w", edge.childID, err)
			}
			if !owned {
				if edge.isSubagent {
					return hubcore.LiveEntry{}, false, fmt.Errorf("cannot verify delegate %s ownership: descriptor for parent %s is missing", edge.childID, edge.ownerID)
				}
				return hubcore.LiveEntry{}, false, nil
			}
		}
		if unconfirmed {
			return hubcore.LiveEntry{}, false, fmt.Errorf("cannot verify daemon ownership for session %s", entry.SessionID)
		}
		return entry, entry.Status == appwire.ThreadStatusRestartRequired, nil
	}
	var jobTreeRootID string
	var subagentAncestry, reachedRoot bool
	seen := make(map[string]bool)
	for !seen[threadID] {
		seen[threadID] = true
		if unconfirmedDaemonForThread(cfg.Roster, threadID) {
			return verifyOwner(hubcore.LiveEntry{SessionID: threadID}, true)
		}
		findOwner := liveDaemonForThread
		if len(edges) != 0 {
			findOwner = liveDaemonForSession
		}
		if entry, ok := findOwner(cfg.Roster, threadID); ok {
			return verifyOwner(entry, false)
		}
		// Ancestry locates a possible daemon. Every edge must have a persisted
		// delegate descriptor before that daemon can be classified as the owner.
		child, ok, err := ownershipEntry(ctx, cfg, threadID)
		if err != nil {
			return hubcore.LiveEntry{}, false, fmt.Errorf("read session ownership for %s: %w", threadID, err)
		}
		if !ok {
			break
		}
		// A fork's parent records provenance, not daemon ownership. An
		// independent activity root does not require its parent's metadata.
		if !child.Meta.IsSubagent && (child.Meta.JobTreeRootSessionID == "" || child.Meta.JobTreeRootSessionID == threadID) {
			reachedRoot = true
			break
		}
		subagentAncestry = subagentAncestry || child.Meta.IsSubagent
		if jobTreeRootID == "" {
			jobTreeRootID = child.Meta.JobTreeRootSessionID
		}
		if child.Meta.ParentSessionID == "" {
			reachedRoot = !child.Meta.IsSubagent
			break
		}
		parentID := child.Meta.ParentSessionID
		stateDir := child.StateDir
		if parent, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{ThreadID: parentID}); ok {
			stateDir = parent.StateDir
		}
		// Missing indexed metadata cannot hide a known live parent. The next
		// iteration checks the roster first; verifying its descriptor then
		// reports unreadable ownership instead of permitting a delegate write.
		edges = append(edges, ownershipEdge{stateDir: stateDir, ownerID: parentID, childID: threadID, isSubagent: child.Meta.IsSubagent})
		threadID = parentID
	}

	if subagentAncestry && !reachedRoot && !cfg.Roster.DaemonOwnershipAbsent() {
		return hubcore.LiveEntry{}, false, fmt.Errorf("cannot verify incomplete subagent ancestry at session %s", threadID)
	}

	// An incomplete ancestry chain cannot establish that an incompatible
	// job-tree owner has released this descendant. Report uncertainty until
	// its metadata and descriptor chain can be verified.
	if owner, ok := liveDaemonForSession(cfg.Roster, jobTreeRootID); ok && owner.Status == appwire.ThreadStatusRestartRequired {
		return hubcore.LiveEntry{}, false, fmt.Errorf("cannot verify delegate ownership at session %s in incompatible job tree %s", threadID, jobTreeRootID)
	}
	if unconfirmedDaemonForThread(cfg.Roster, jobTreeRootID) {
		return hubcore.LiveEntry{}, false, fmt.Errorf("cannot verify daemon ownership in job tree %s", jobTreeRootID)
	}
	if err := cfg.Roster.OwnershipError(); err != nil {
		return hubcore.LiveEntry{}, false, &daemonDiscoveryError{err: err}
	}
	return hubcore.LiveEntry{}, false, nil
}

// Indexed entries carry their project directory. Without one, inspect only the
// requested metadata across configured projects; a failed read is not absence.
func ownershipEntry(ctx context.Context, cfg hubcore.WebConfig, threadID string) (hubcore.PastEntry, bool, error) {
	if entry, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{ThreadID: threadID}); ok {
		return entry, true, nil
	}
	if cfg.StateDir == "" {
		return hubcore.PastEntry{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return hubcore.PastEntry{}, false, err
	}
	dirs := []string{cfg.StateDir}
	projects, err := os.ReadDir(filepath.Join(cfg.StateDir, "projects"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return hubcore.PastEntry{}, false, err
	}
	for _, project := range projects {
		if project.IsDir() {
			dirs = append(dirs, filepath.Join(cfg.StateDir, "projects", project.Name()))
		}
	}
	var found hubcore.PastEntry
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return hubcore.PastEntry{}, false, err
		}
		meta, err := schema.LoadSessionMeta(dir, threadID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return hubcore.PastEntry{}, false, err
		}
		if found.ID != "" {
			return hubcore.PastEntry{}, false, fmt.Errorf("session %s has ambiguous project ownership", threadID)
		}
		found = hubcore.PastEntry{ID: threadID, Meta: meta, StateDir: dir}
	}
	if found.ID == "" && cfg.Roster != nil && !cfg.Roster.DaemonOwnershipAbsent() {
		return hubcore.PastEntry{}, false, fmt.Errorf("cannot locate ownership metadata for session %s", threadID)
	}
	return found, found.ID != "", nil
}

// A workspace route can survive clear while its former session's journal and
// delegates are released. An ancestor must match the daemon's current session.
func liveDaemonForSession(roster *hubcore.Roster, sessionID string) (hubcore.LiveEntry, bool) {
	if sessionID == "" {
		return hubcore.LiveEntry{}, false
	}
	for _, entry := range roster.List() {
		if !entry.Crashed && cmp.Or(entry.SessionID, entry.Entry.SessionID, entry.ThreadID) == sessionID {
			return entry, true
		}
	}
	return hubcore.LiveEntry{}, false
}

func liveDaemonForThread(roster *hubcore.Roster, threadID string) (hubcore.LiveEntry, bool) {
	if threadID == "" {
		return hubcore.LiveEntry{}, false
	}
	if entry, ok := roster.Find(threadID); ok && !entry.Crashed {
		return entry, true
	}
	workspaceRef := localAppRef(threadID)
	for _, entry := range roster.List() {
		if !entry.Crashed && localSpawnWorkspaceRef(entry.Entry) == workspaceRef {
			return entry, true
		}
	}
	return hubcore.LiveEntry{}, false
}

// Refresh ownership before a local metadata write or before treating a missing
// daemon as proof that a mutation was not accepted. Successful delivery uses
// the live source directly and does not wait for an unrelated roster probe.
func refreshDaemonRestartRequiredError(ctx context.Context, cfg hubcore.WebConfig, ref, threadID, mutationID string) error {
	if ref != "" {
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			return appwire.InvalidParams(err.Error())
		}
		if parsed.SourceID != "local" {
			return nil
		}
	}
	if cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
			wire := appwire.Unavailable(err.Error())
			if mutationID != "" {
				return restartRequiredMutationError(wire, mutationID)
			}
			return wire
		}
	}
	return daemonRestartRequiredError(ctx, cfg, ref, threadID, mutationID)
}

func daemonRestartRequiredError(ctx context.Context, cfg hubcore.WebConfig, ref, threadID, mutationID string) error {
	if ref != "" {
		if _, err := appwire.ParseRef(ref); err != nil {
			return appwire.InvalidParams(err.Error())
		}
	}

	entry, ok, err := restartRequiredDaemon(ctx, cfg, ref, threadID)
	if err != nil {
		wire := appwire.Unavailable(err.Error())
		if mutationID != "" {
			return restartRequiredMutationError(wire, mutationID)
		}
		return wire
	}
	if !ok {
		return nil
	}
	wire := appwire.WireError{Code: appwire.CodeConflict, Message: fmt.Sprintf("Session restart required: daemon pid %d uses an incompatible protocol; this hub requires %s. Stop the daemon, then resume this session. Stopping interrupts active work.", entry.PID, appwire.ProtocolVersion), Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorConflict, Cause: "daemonRestartRequired"}}
	if mutationID != "" {
		return restartRequiredMutationError(wire, mutationID)
	}
	return wire
}

func isDaemonRestartRequiredError(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		return data.Cause == "daemonRestartRequired"
	case map[string]any:
		return data["cause"] == "daemonRestartRequired"
	default:
		return false
	}
}

// A retry may name a mutation accepted before the protocol upgrade. Without
// its daemon's receipt history, the hub cannot claim that ID was rejected.
func restartRequiredMutationError(err error, mutationID string) error {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return err
	}
	switch data := wire.Data.(type) {
	case appwire.ErrorData:
		data.ClientMutationID = mutationID
		data.MutationOutcome = appwire.MutationOutcomeUnknown
		data.RetryDisposition = appwire.RetryDispositionBlocked
		wire.Data = data
	case map[string]any:
		updated := maps.Clone(data)
		updated["clientMutationId"] = mutationID
		updated["mutationOutcome"] = string(appwire.MutationOutcomeUnknown)
		updated["retryDisposition"] = string(appwire.RetryDispositionBlocked)
		wire.Data = updated
	}
	return wire
}

func unconfirmedDaemonForThread(roster *hubcore.Roster, threadID string) bool {
	if threadID == "" {
		return false
	}
	for _, entry := range roster.UnconfirmedEntries() {
		// A live claim without a usable identity cannot exclude this target.
		if _, err := appwire.ParseRef(localSpawnWorkspaceRef(entry)); err != nil {
			return true
		}
		if entry.SessionID == threadID || entry.ThreadID == threadID || localSpawnWorkspaceRef(entry) == localAppRef(threadID) {
			return true
		}
	}
	return false
}
