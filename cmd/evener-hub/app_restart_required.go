package hub

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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

// lookupDaemonOwner answers "which live daemon owns this thread" from the
// roster as it stands now. Callers that must also decide something else from
// the same publication use lookupDaemonOwnerIn with the snapshot they already
// hold, so the two answers cannot come from different publications.
func lookupDaemonOwner(ctx context.Context, cfg hubcore.WebConfig, ref, threadID string, verifyCompatibleAncestry bool) (hubcore.LiveEntry, bool, error) {
	return lookupDaemonOwnerIn(ctx, cfg, liveRosterView(cfg.Roster), ref, threadID, verifyCompatibleAncestry)
}

// lookupDaemonOwnerIn is lookupDaemonOwner with every roster question answered
// from one publication. The walk must not read the roster again partway
// through: a refresh landing there can retract the owner it had already found
// and leave nothing in its place, and that reads as absence when it is only a
// second, different publication. Disk metadata reads (descriptors, session
// meta) are not roster publications and stay as they are.
func lookupDaemonOwnerIn(ctx context.Context, cfg hubcore.WebConfig, view rosterView, ref, threadID string, verifyCompatibleAncestry bool) (hubcore.LiveEntry, bool, error) {
	if ref != "" {
		parsed, err := appwire.ParseRef(ref)
		if err != nil {
			return hubcore.LiveEntry{}, false, err
		}
		if parsed.SourceID != appwire.LocalSourceID {
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
		if view.unconfirmedFor(threadID) {
			return verifyOwner(hubcore.LiveEntry{SessionID: threadID}, true)
		}
		findOwner := view.liveForThread
		if len(edges) != 0 {
			findOwner = view.liveForSession
		}
		if entry, ok := findOwner(threadID); ok {
			return verifyOwner(entry, false)
		}
		// Ancestry locates a possible daemon. Every edge must have a persisted
		// delegate descriptor before that daemon can be classified as the owner.
		child, ok, err := ownershipEntryIn(ctx, cfg, view, threadID)
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

	if subagentAncestry && !reachedRoot && !view.absence() {
		return hubcore.LiveEntry{}, false, fmt.Errorf("cannot verify incomplete subagent ancestry at session %s", threadID)
	}

	// An incomplete ancestry chain cannot establish that an incompatible
	// job-tree owner has released this descendant. Report uncertainty until
	// its metadata and descriptor chain can be verified.
	if owner, ok := view.liveForSession(jobTreeRootID); ok && owner.Status == appwire.ThreadStatusRestartRequired {
		return hubcore.LiveEntry{}, false, fmt.Errorf("cannot verify delegate ownership at session %s in incompatible job tree %s", threadID, jobTreeRootID)
	}
	if view.unconfirmedFor(jobTreeRootID) {
		return hubcore.LiveEntry{}, false, fmt.Errorf("cannot verify daemon ownership in job tree %s", jobTreeRootID)
	}
	if err := view.ownershipError(); err != nil {
		return hubcore.LiveEntry{}, false, &daemonDiscoveryError{err: err}
	}
	if cfg.ResumeLocks != nil {
		if _, ok := cfg.ResumeLocks.PendingLaunch(threadID); ok {
			// A child is restoring this session but has not registered yet.
			// Reporting absence would let a caller act as if no daemon were
			// coming; the retryable answer is to wait for the launch.
			return hubcore.LiveEntry{}, false, fmt.Errorf("%w: session %s", errResumeStillStarting, threadID)
		}
	}
	return hubcore.LiveEntry{}, false, nil
}

// Indexed entries carry their project directory. Without one, inspect only the
// requested metadata across configured projects; a failed read is not absence.
func ownershipEntry(ctx context.Context, cfg hubcore.WebConfig, threadID string) (hubcore.PastEntry, bool, error) {
	return ownershipEntryIn(ctx, cfg, liveRosterView(cfg.Roster), threadID)
}

// ownershipEntryIn is ownershipEntry with its roster question answered from the
// caller's publication, so a walk does not consult a newer one here either.
func ownershipEntryIn(ctx context.Context, cfg hubcore.WebConfig, view rosterView, threadID string) (hubcore.PastEntry, bool, error) {
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
	if found.ID == "" && !view.absence() {
		return hubcore.PastEntry{}, false, fmt.Errorf("cannot locate ownership metadata for session %s", threadID)
	}
	return found, found.ID != "", nil
}

// rosterView answers the ownership questions an ancestry walk asks, either
// from the roster as it stands now or from one publication a caller already
// holds.
//
// The distinction matters. A caller that must decide several things from the
// same publication — "did this one snapshot establish absence?" — binds a
// snapshot and never lets the walk read the roster again, because a refresh
// landing mid-walk can retract an owner the walk has already seen and leave
// nothing in its place, and two publications that disagree are not evidence
// that nobody owns the thread. A caller that only wants today's answer keeps
// reading the live roster through the existing seams, so scripting those seams
// still scripts the walk.
type rosterView struct {
	// roster is set for a live view and nil for a snapshot view.
	roster *hubcore.Roster
	// snapshot is the single publication a snapshot view answers from.
	snapshot hubcore.RosterSnapshot
	// bySession is that publication's session-keyed view, what Find answers
	// from. Live may hold several entries naming one session; this holds the
	// one Find would return, so both answers come from the same publication.
	bySession    map[string]hubcore.LiveEntry
	fromSnapshot bool
}

// liveRosterView reads the roster as it stands now, through the same helpers
// and seams the rest of the hub uses.
func liveRosterView(roster *hubcore.Roster) rosterView {
	return rosterView{roster: roster}
}

// rosterViewOf binds a snapshot a caller already holds, so the walk and the
// rest of that caller's decision come from the same publication.
func rosterViewOf(snapshot hubcore.RosterSnapshot) rosterView {
	return rosterView{snapshot: snapshot, bySession: snapshot.BySession, fromSnapshot: true}
}

// absence reports whether this view established that no daemon owns any
// thread: the scan completed, no unverified claim survives, and every entry is
// confirmed gone. Anything else leaves ownership unresolved.
func (v rosterView) absence() bool {
	if !v.fromSnapshot {
		return v.roster != nil && v.roster.DaemonOwnershipAbsent()
	}
	if v.ownershipError() != nil || len(v.snapshot.Unconfirmed) != 0 {
		return false
	}
	for _, entry := range v.snapshot.Live {
		if !entry.Crashed {
			return false
		}
	}
	return true
}

// ownershipError reports why discovery is incomplete, in whichever mode this
// view answers from: a live view asks the roster now, a snapshot view reports
// the error its publication carried.
//
// It is an accessor rather than a field read on purpose. The snapshot is
// zero-valued in a live view, so reaching for the field silently drops the
// discovery error, and a caller that then reports it as absence or as an
// unreadable thread turns an incomplete scan into a worse answer than the
// evidence supports.
func (v rosterView) ownershipError() error {
	if !v.fromSnapshot {
		if v.roster == nil {
			return nil
		}
		return v.roster.OwnershipError()
	}
	return v.snapshot.OwnershipError
}

func (v rosterView) liveForSession(sessionID string) (hubcore.LiveEntry, bool) {
	if !v.fromSnapshot {
		return liveDaemonForSession(v.roster, sessionID)
	}
	if sessionID == "" {
		return hubcore.LiveEntry{}, false
	}
	for _, entry := range v.snapshot.Live {
		if !entry.Crashed && cmp.Or(entry.SessionID, entry.Entry.SessionID, entry.ThreadID) == sessionID {
			return entry, true
		}
	}
	return hubcore.LiveEntry{}, false
}

func (v rosterView) liveForThread(threadID string) (hubcore.LiveEntry, bool) {
	if !v.fromSnapshot {
		return liveDaemonForThread(v.roster, threadID)
	}
	if threadID == "" {
		return hubcore.LiveEntry{}, false
	}
	if entry, ok := v.find(threadID); ok && !entry.Crashed {
		return entry, true
	}
	workspaceRef := localAppRef(threadID)
	for _, entry := range v.snapshot.Live {
		if !entry.Crashed && localSpawnWorkspaceRef(entry.Entry) == workspaceRef {
			return entry, true
		}
	}
	return hubcore.LiveEntry{}, false
}

// find mirrors Roster.Find over the publication: the session whose own
// SessionID is exactly this one, without the workspace-route fallback.
func (v rosterView) find(sessionID string) (hubcore.LiveEntry, bool) {
	if !v.fromSnapshot {
		if v.roster == nil || sessionID == "" {
			return hubcore.LiveEntry{}, false
		}
		return v.roster.Find(sessionID)
	}
	if sessionID == "" {
		return hubcore.LiveEntry{}, false
	}
	entry, ok := v.bySession[sessionID]
	return entry, ok
}

func (v rosterView) unconfirmedFor(threadID string) bool {
	if !v.fromSnapshot {
		return unconfirmedDaemonForThread(v.roster, threadID)
	}
	if threadID == "" {
		return false
	}
	for _, entry := range v.snapshot.Unconfirmed {
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
	for _, entry := range hubRosterList(roster) {
		if !entry.Crashed && localSpawnWorkspaceRef(entry.Entry) == workspaceRef {
			return entry, true
		}
	}
	return hubcore.LiveEntry{}, false
}

// recoveryGroupClaimExists reports whether fresh discovery still shows a
// process that could be the stopped owner: a live entry on any recovery
// alias, or an unconfirmed claim whose process is alive but unverified —
// "absence from List" is not proof of released ownership for those. Crash
// markers are the opposite case (a PID confirmed gone) and count as exit
// evidence, so they never block here. The unconfirmed side deliberately
// shares unconfirmedDaemonForThread's predicate — canonicalized workspace
// matching and the cannot-exclude fallback for an unresolvable identity — so
// the escape cannot under-refuse a claim the delete path would have kept.
func recoveryGroupClaimExists(roster *hubcore.Roster, aliases []string) bool {
	for _, alias := range aliases {
		if _, ok := liveDaemonForThread(roster, alias); ok {
			return true
		}
		if unconfirmedDaemonForThread(roster, alias) {
			return true
		}
	}
	return false
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
		if parsed.SourceID != appwire.LocalSourceID {
			return nil
		}
	}
	if cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
			wire := appwire.Unavailable(err.Error())
			if mutationID != "" {
				return blockedAdmissionMutationError(wire, mutationID)
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
			return blockedAdmissionMutationError(wire, mutationID)
		}
		return wire
	}
	if !ok {
		return nil
	}
	wire := appwire.WireError{Code: appwire.CodeConflict, Message: fmt.Sprintf("Session restart required: daemon pid %d uses an incompatible protocol; this hub requires %s. Stop the daemon, then resume this session. Stopping interrupts active work.", entry.PID, appwire.ProtocolVersion), Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorConflict, Cause: "daemonRestartRequired"}}
	if mutationID != "" {
		return blockedAdmissionMutationError(wire, mutationID)
	}
	return wire
}

func isDaemonRestartRequiredError(err error) bool {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	data, _ := appwire.DecodedErrorData(wire.Data)
	return data.Cause == "daemonRestartRequired"
}

// Admission fences cannot resolve an earlier acceptance of the same mutation.
// Preserve the original cause and block retries until receipt reconciliation.
func blockedAdmissionMutationError(err error, mutationID string) error {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return err
	}
	wire, _ = wire.WithMutation(mutationID, appwire.MutationOutcomeUnknown, appwire.RetryDispositionBlocked)
	if isSessionRecoveryAdmissionError(err) {
		return sessionRecoveryAdmissionError{wire}
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
