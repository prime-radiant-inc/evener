package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	agentsandbox "primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

type projectDeleteSkip = appwire.ProjectDeleteSkip

// projectRemoteSources returns the hosts a tree project reports as owning it.
// The tree spells the controller's own sessions with the empty string — the
// decision store's key — so every other entry is a host that shares the
// project's canonical ID and path. Sorted by the tree, so the refusal reads
// deterministically.
func projectRemoteSources(sources []string) []string {
	var hosts []string
	for _, source := range sources {
		if source != "" {
			hosts = append(hosts, source)
		}
	}
	return hosts
}

func (s *WebServer) projectDeleteResult(ctx context.Context, deleted []string, skipped []projectDeleteSkip, changed bool, project string) (appwire.ProjectDeleteResponse, error) {
	navigation := s.emptyNavigationMutation()
	if changed {
		hint := navigationChangeHint{Projects: []string{project}}
		navigation = s.navigationAfterDeletion(ctx, hint)
	}
	return appwire.ProjectDeleteResponse{
		Deleted:    append([]string{}, deleted...),
		Skipped:    append([]projectDeleteSkip{}, skipped...),
		Navigation: navigation,
	}, nil
}

// Roster and navigation refreshes do not undo committed artifact removal.
// Keep discovery errors visible in the roster and log stale projections while
// returning the durable deletion outcome to the caller.
func (s *WebServer) refreshRosterAfterDeletion(ctx context.Context) {
	if s.cfg.Roster != nil {
		if err := hubRosterRefresh(ctx, s.cfg.Roster); err != nil {
			fmt.Fprintf(os.Stderr, "[hub] deletion completed with stale roster: %v\n", err)
		}
	}
}

func (s *WebServer) navigationAfterDeletion(ctx context.Context, hint navigationChangeHint) hubapi.NavigationMutation {
	navigation, err := s.navigation.Refresh(ctx, hint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hub] deletion completed with stale navigation: %v\n", err)
		return s.emptyNavigationMutation()
	}
	return navigation
}

var (
	removeProjectSessionFile = os.Remove
	removeProjectSessionDir  = os.RemoveAll
	// removeProjectSessionRendezvousEntry deletes one matched session's
	// rendezvous file only while it still carries the exact identity that was
	// just read. Deleting by PID alone would destroy a live replacement's entry
	// if that PID were reused (or a respawn raced a slow exit) between the list
	// and the unlink, permanently losing Hub discovery for the replacement.
	removeProjectSessionRendezvousEntry = rendezvous.RemoveIfOwned
	rebuildProjectDeletionPast          = func(past *hubcore.PastIndex) (bool, error) { return past.Rebuild() }
	// projectSessionLive is the deletion-safety liveness predicate (kata
	// 8at6): a retained crash marker (LiveEntry.Crashed=true, written by
	// hubcore.Roster.Refresh only once a daemon's PID is confirmed gone) is
	// historical error state, not a live daemon, and must not block deletion.
	// Confirmed live daemons and unresolved live-process claims block it;
	// a failed first probe cannot authorize deletion. Used at both the
	// whole-project entry preflight and the per-session ownership re-check
	// below, so the TOCTOU protection applies the same rule at both sites.
	projectSessionLive = func(roster *hubcore.Roster, id string) bool {
		if unconfirmedDaemonForThread(roster, id) {
			return true
		}
		_, live := liveDaemonForThread(roster, id)
		return live
	}
)

// projectSessionOwnership checks direct ownership and persisted delegate
// ancestry. Independent forks do not inherit their ancestor's live ownership.
func projectSessionOwnership(ctx context.Context, cfg hubcore.WebConfig, id string) (bool, error) {
	if cfg.Roster == nil {
		return false, nil
	}
	if projectSessionLive(cfg.Roster, id) {
		return true, nil
	}
	owner, _, err := lookupDaemonOwner(ctx, cfg, "", id, true)
	return owner.SessionID != "" || owner.ThreadID != "", err
}

// projectDelete removes every session file under a project and scrubs only the
// decision rows for artifacts it removed. It validates both the project key
// and working directory and refuses the whole operation when anything is live
// at entry.
func (s *WebServer) projectDelete(ctx context.Context, params appwire.ProjectDeleteParams) (appwire.ProjectDeleteResponse, error) {
	if params.Key == "" || params.WorkingDir == "" {
		return appwire.ProjectDeleteResponse{}, appwire.InvalidParams("key and workingDir are required")
	}
	// Deletion is local-only: v1 has no remote deletion, and this handler only
	// ever removes the controller's own sessions. A request that names a host is
	// refused before any resolution or removal — a remote row must never be
	// answered with a controller-local delete, and a remote project that merely
	// shares an ID or path with a local one must not take the local project's
	// sessions with it. "local" (and an absent field) is the controller's own
	// source, the same normalization archive and favorite use.
	if source := hubcore.NormalizeDecisionSource(params.Source); source != "" {
		if err := validateDecisionSource(s.cfg, source); err != nil {
			return appwire.ProjectDeleteResponse{}, err
		}
		return appwire.ProjectDeleteResponse{}, appwire.InvalidParams("project delete is local-only; " + source + " is not this hub")
	}
	if params.Key == "no-project" {
		return appwire.ProjectDeleteResponse{}, appwire.InvalidParams("no-project is not a local project")
	}
	if err := identifier.ValidateProjectID(params.Key); err != nil {
		return appwire.ProjectDeleteResponse{}, appwire.InvalidParams("invalid project ID: " + err.Error())
	}
	project, err := identifier.ResolveProject(params.WorkingDir)
	if err != nil {
		return appwire.ProjectDeleteResponse{}, appwire.InvalidParams("resolve project: " + err.Error())
	}
	if project.ID != params.Key {
		return appwire.ProjectDeleteResponse{}, appwire.InvalidParams("project ID does not match workingDir")
	}
	if s.cfg.Past == nil {
		return appwire.ProjectDeleteResponse{}, appwire.InternalError("past index not configured")
	}
	if s.deletionStoreErr != nil {
		return appwire.ProjectDeleteResponse{}, appwire.InternalError("load deletion state: " + s.deletionStoreErr.Error())
	}
	if record, ok := s.cfg.DeletionStore.DeletingProject(project.ID); ok {
		releaseOwnership, ownerErr := s.acquireProjectDeletionOwnership(ctx, record, nil)
		if ownerErr != nil {
			// Cancellation while acquiring a later target is not a skipped
			// target: the resume never ran, so it must not be reported as a
			// successful deletion outcome.
			if err := ctx.Err(); err != nil {
				return appwire.ProjectDeleteResponse{}, err
			}
			skipped := []projectDeleteSkip{{ID: ownerErr.ThreadID, Reason: ownerErr.Error()}}
			if errors.Is(ownerErr.Err, llm.ErrAPILogTargetLocked) || ownerErr.Live {
				skipped = appendProjectDeleteLiveSkip(nil, ownerErr.ThreadID)
			}
			return s.projectDeleteResult(ctx, []string{}, skipped, false, project.ID)
		}
		defer func() {
			if releaseOwnership != nil {
				releaseOwnership()
			}
		}()
		// Ownership is held but the request may have been abandoned while it
		// waited; fail before the destructive cleanup, as the fresh path and
		// sessionDelete already do.
		if err := ctx.Err(); err != nil {
			return appwire.ProjectDeleteResponse{}, err
		}
		result := s.cleanupProjectDeletion(ctx, record, nil)
		if len(result.DecisionErrors) > 0 {
			return appwire.ProjectDeleteResponse{}, appwire.InternalError(strings.Join(result.DecisionErrors, "; "))
		}
		releaseOwnership()
		releaseOwnership = nil
		return s.projectDeleteResult(ctx, result.Deleted, result.Skipped, len(result.Deleted) > 0, project.ID)
	}
	// Validate the body against the current tree entry for that key — never
	// invert the lossy slug on a destructive path (round-2 A11).
	tree, _ := s.memoTree(ctx)
	var matched *hubcore.TreeProject
	for _, p := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		if p.Key == params.Key {
			pp := p
			matched = &pp
			break
		}
	}
	if matched == nil || matched.WorkingDir != project.CanonicalPath {
		return appwire.ProjectDeleteResponse{}, appwire.InvalidParams("key does not match workingDir")
	}
	// A merged project — the controller's own rows plus a host's under the same
	// canonical ID and path — is not deletable here either. The rail refuses it
	// before the confirmation dialog opens, and the wire must refuse it too: a
	// request that named no source would otherwise remove the controller's
	// sessions of a project a host also owns, leaving one project half-deleted
	// and the UI and the API disagreeing about the same row. The tree's sources
	// are the authority ("" is the controller), so the gate matches the
	// ownership the client just rendered.
	if hosts := projectRemoteSources(matched.Sources); len(hosts) > 0 {
		return appwire.ProjectDeleteResponse{}, appwire.InvalidParams(
			"project delete is local-only; this project also belongs to " + strings.Join(hosts, ", "))
	}

	// Resolve every distinct candidate path before deleting anything. This uses
	// the same canonical identity map as tree building, but fails closed rather
	// than presenting an unresolvable path in the no-project bucket.
	all := s.cfg.Past.All()
	metas := make([]schema.SessionMeta, 0, len(all))
	for _, e := range all {
		metas = append(metas, e.Meta)
	}
	projects, err := hubcore.ResolveProjectMapStrict(metas, nil)
	if err != nil {
		return appwire.ProjectDeleteResponse{}, appwire.InternalError("resolve project membership: " + err.Error())
	}

	// Select the session set from All() (carries StateDir), uncapped.
	var entries []hubcore.PastEntry
	for _, e := range all {
		workingDir := hubcore.EffectiveWorkingDir(e.Meta)
		if projects[workingDir].ID == params.Key {
			entries = append(entries, e)
		}
	}

	// Whole-project fast path: refuse when anything is live at entry.
	if s.cfg.Roster != nil {
		if err := s.cfg.Roster.OwnershipError(); err != nil {
			return appwire.ProjectDeleteResponse{}, appwire.Unavailable(err.Error())
		}
		var liveNames []string
		for _, e := range entries {
			live, err := projectSessionOwnership(ctx, s.cfg, e.ID)
			if err != nil {
				return appwire.ProjectDeleteResponse{}, appwire.Unavailable(err.Error())
			}
			if live {
				liveNames = append(liveNames, hubcore.ShortID(e.ID))
			}
		}
		if len(liveNames) > 0 {
			return appwire.ProjectDeleteResponse{}, appwire.WireError{
				Code:    appwire.CodeConflict,
				Message: "project has live sessions",
				Data: appwire.ProjectDeleteConflictData{
					ErrorData: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorConflict},
					Live:      liveNames,
				},
			}
		}
	}

	if len(entries) == 0 {
		return s.projectDeleteResult(ctx, []string{}, []projectDeleteSkip{}, false, project.ID)
	}
	targets := make([]hubcore.DeletionTarget, 0, len(entries))
	stateDirs := make(map[string]string, len(entries))
	for _, entry := range entries {
		targets = append(targets, hubcore.DeletionTarget{
			Ref:      localAppRef(entry.ID),
			ThreadID: entry.ID,
		})
		stateDirs[entry.ID] = entry.StateDir
	}
	ownedTargets, skipped, releaseOwnership := s.acquireProjectDeletionCandidates(ctx, targets, stateDirs)
	defer func() {
		if releaseOwnership != nil {
			releaseOwnership()
		}
	}()
	if err := ctx.Err(); err != nil {
		return appwire.ProjectDeleteResponse{}, err
	}
	if len(ownedTargets) == 0 {
		return s.projectDeleteResult(ctx, []string{}, skipped, false, project.ID)
	}

	record, err := s.cfg.DeletionStore.BeginProject(project.ID, ownedTargets, len(skipped) == 0)
	if err != nil {
		return appwire.ProjectDeleteResponse{}, appwire.InternalError("commit deletion fence: " + err.Error())
	}
	result := s.cleanupProjectDeletion(ctx, record, stateDirs)
	result.Skipped = append(skipped, result.Skipped...)
	if len(result.DecisionErrors) > 0 {
		return appwire.ProjectDeleteResponse{}, appwire.InternalError(strings.Join(result.DecisionErrors, "; "))
	}
	releaseOwnership()
	releaseOwnership = nil
	return s.projectDeleteResult(ctx, result.Deleted, result.Skipped, len(result.Deleted) > 0, project.ID)
}

type projectDeletionOwnershipError struct {
	ThreadID string
	Live     bool
	Err      error
}

func (e projectDeletionOwnershipError) Error() string {
	if e.Live {
		return "resumed live"
	}
	return e.Err.Error()
}

func (e projectDeletionOwnershipError) Unwrap() error {
	return e.Err
}

type projectDeletionCleanupResult struct {
	Deleted        []string
	Skipped        []projectDeleteSkip
	DecisionErrors []string
}

func (s *WebServer) acquireProjectDeletionCandidates(
	ctx context.Context,
	targets []hubcore.DeletionTarget,
	stateDirs map[string]string,
) ([]hubcore.DeletionTarget, []projectDeleteSkip, func()) {
	targets = append([]hubcore.DeletionTarget(nil), targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].ThreadID < targets[j].ThreadID })
	var owned []hubcore.DeletionTarget
	var skipped []projectDeleteSkip
	var releases []func()
	release := func() {
		for _, fn := range slices.Backward(releases) {
			fn()
		}
	}
	for _, target := range targets {
		record := hubcore.DeletionRecord{ProjectID: "", Targets: []hubcore.DeletionTarget{target}}
		stateDir := stateDirs[target.ThreadID]
		releaseTarget, err := s.acquireProjectDeletionOwnership(ctx, record, map[string]string{target.ThreadID: stateDir})
		if err == nil {
			owned = append(owned, target)
			releases = append(releases, releaseTarget)
			continue
		}
		if errors.Is(err.Err, llm.ErrAPILogTargetLocked) || err.Live {
			skipped = appendProjectDeleteLiveSkip(skipped, target.ThreadID)
		} else {
			skipped = append(skipped, projectDeleteSkip{ID: target.ThreadID, Reason: err.Error()})
		}
	}
	return owned, skipped, release
}

func (s *WebServer) resumeProjectDeletions() error {
	if s.cfg.DeletionStore == nil {
		return nil
	}
	var firstErr error
	for _, record := range s.cfg.DeletionStore.Deleting() {
		release, err := s.acquireProjectDeletionOwnership(context.Background(), record, nil)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		result := s.cleanupProjectDeletion(context.Background(), record, nil)
		release()
		if len(result.Skipped) > 0 || len(result.DecisionErrors) > 0 {
			if firstErr == nil {
				firstErr = fmt.Errorf("resume deletion %s/%d incomplete", record.ProjectID, record.Generation)
			}
		}
	}
	return firstErr
}

func (s *WebServer) acquireProjectDeletionOwnership(
	ctx context.Context,
	record hubcore.DeletionRecord,
	stateDirs map[string]string,
) (func(), *projectDeletionOwnershipError) {
	targets := append([]hubcore.DeletionTarget(nil), record.Targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].ThreadID < targets[j].ThreadID })
	var locks []*hubcore.ResumeMutex
	var owners []*llm.APILogger
	release := func() {
		for _, owner := range slices.Backward(owners) {
			_ = owner.Close()
		}
		for _, lock := range slices.Backward(locks) {
			lock.Unlock()
		}
	}
	for _, target := range targets {
		lock := s.lockForSession(target.ThreadID)
		if err := lock.LockContext(ctx); err != nil {
			release()
			return nil, &projectDeletionOwnershipError{ThreadID: target.ThreadID, Err: err}
		}
		locks = append(locks, lock)
		if s.cfg.Roster != nil {
			if err := s.cfg.Roster.OwnershipError(); err != nil {
				release()
				return nil, &projectDeletionOwnershipError{ThreadID: target.ThreadID, Err: err}
			}
		}
		live, ownershipErr := projectSessionOwnership(ctx, s.cfg, target.ThreadID)
		if live || ownershipErr != nil {
			release()
			return nil, &projectDeletionOwnershipError{ThreadID: target.ThreadID, Live: live, Err: ownershipErr}
		}
		stateDir := s.projectDeletionStateDir(record.ProjectID, target.ThreadID, stateDirs)
		if stateDir == "" {
			release()
			return nil, &projectDeletionOwnershipError{
				ThreadID: target.ThreadID,
				Err:      errors.New("session state directory is not resolvable"),
			}
		}
		owner, err := llm.NewSessionAPILogger(stateDir)
		if err == nil {
			err = owner.ReserveSession(target.ThreadID)
		}
		if err != nil {
			if owner != nil {
				_ = owner.Close()
			}
			release()
			return nil, &projectDeletionOwnershipError{ThreadID: target.ThreadID, Err: err}
		}
		owners = append(owners, owner)
	}
	return release, nil
}

// cleanupProjectDeletionTargetAndDecisions removes one target's artifacts via
// cleanupProjectDeletionTarget, then scrubs its session-kind archive/favorite
// decisions on success. A failed artifact removal reports skip (with a
// reason) and never touches decisions, so a retried delete finds them intact.
// Shared by whole-project deletion (cleanupProjectDeletion, below) and
// single-session deletion (sessionDelete) so both apply the exact
// same per-target contract instead of two copies of it.
func (s *WebServer) cleanupProjectDeletionTargetAndDecisions(stateDir, threadID string) (deleted bool, skip *projectDeleteSkip, decisionErrors []string) {
	// Remove the artifacts first: the release below is authorized by that
	// deletion succeeding, not by the attempt. Releasing before a removal that
	// can still fail would tombstone a session whose metadata and transcript
	// survive (and stay resumable), so a later resume would mint replacement
	// scratch and let the originally retained directories be collected.
	if err := s.cleanupProjectDeletionTarget(stateDir, threadID); err != nil {
		return false, &projectDeleteSkip{ID: threadID, Reason: err.Error()}, decisionErrors
	}
	// Under this verified deletion ownership, release the exact target root's
	// scratch-retention manifest now that its state is purged. A manifest owned
	// by a surviving root is never touched, so a child-only deletion cannot
	// release its parent's retained scratch. A release failure is recorded, not
	// silently dropped, so deletion still proceeds conservatively.
	if err := releaseProjectDeletionScratchRetention(stateDir, threadID); err != nil {
		decisionErrors = append(decisionErrors, "scratch retention release error: "+err.Error())
	}
	return true, nil, append(decisionErrors, s.scrubSessionDecisions(threadID)...)
}

// releaseProjectDeletionScratchRetention writes the terminal tombstone for the
// scratch-retention manifest owned by exactly sessionID. It is a no-op when no
// manifest exists for that id, so a session that is not a retention root (a
// delegate child, or one that never minted scratch) cannot release a surviving
// root's manifest.
func releaseProjectDeletionScratchRetention(stateDir, sessionID string) error {
	if stateDir == "" || sessionID == "" {
		return nil
	}
	manifestPath := filepath.Join(stateDir, "scratch-retention", sessionID+".json")
	if _, err := os.Stat(manifestPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return agentsandbox.ReleaseScratchRetention(agentsandbox.ScratchOwner{StateDir: stateDir, RootSessionID: sessionID})
}

func (s *WebServer) scrubSessionDecisions(threadID string) (decisionErrors []string) {
	authority := s.sessionDecisionAuthority()
	aliases := hubcore.LocalSessionDecisionAliases(threadID, authority)
	if s.cfg.Archive != nil {
		for _, id := range aliases {
			if err := s.cfg.Archive.Delete("", "session", id); err != nil {
				decisionErrors = append(decisionErrors, fmt.Sprintf("archive store error: %v", err))
			}
		}
	}
	if s.cfg.Favorite != nil {
		for _, id := range aliases {
			if err := s.cfg.Favorite.Delete("", "session", id); err != nil {
				decisionErrors = append(decisionErrors, fmt.Sprintf("favorite store error: %v", err))
			}
		}
	}
	if s.cfg.PinSections != nil {
		if _, err := s.cfg.PinSections.DeleteSession("", threadID); err != nil {
			decisionErrors = append(decisionErrors, fmt.Sprintf("pin section store error: %v", err))
		}
	}
	return decisionErrors
}

func (s *WebServer) sessionDecisionAuthority() hubcore.FavoriteAuthority {
	if s.cfg.Past == nil {
		return hubcore.FavoriteAuthority{}
	}
	_, _, _, authority := s.memoTreeWithAuthority(context.Background())
	return authority
}

func (s *WebServer) cleanupProjectDeletion(
	ctx context.Context,
	record hubcore.DeletionRecord,
	stateDirs map[string]string,
) projectDeletionCleanupResult {
	result := projectDeletionCleanupResult{}
	for _, target := range record.Targets {
		stateDir := s.projectDeletionStateDir(record.ProjectID, target.ThreadID, stateDirs)
		deleted, skip, decisionErrors := s.cleanupProjectDeletionTargetAndDecisions(stateDir, target.ThreadID)
		result.DecisionErrors = append(result.DecisionErrors, decisionErrors...)
		if !deleted {
			result.Skipped = append(result.Skipped, *skip)
			continue
		}
		result.Deleted = append(result.Deleted, target.ThreadID)
	}
	if len(result.Skipped) == 0 && record.WholeProject {
		if s.cfg.Archive != nil {
			if err := s.cfg.Archive.Delete("", "project", record.ProjectID); err != nil {
				result.DecisionErrors = append(result.DecisionErrors, fmt.Sprintf("archive store error: %v", err))
			}
		}
		if s.cfg.Favorite != nil {
			if err := s.cfg.Favorite.Delete("", "project", record.ProjectID); err != nil {
				result.DecisionErrors = append(result.DecisionErrors, fmt.Sprintf("favorite store error: %v", err))
			}
		}
	}
	rebuilt := false
	if s.cfg.Past != nil {
		var err error
		rebuilt, err = rebuildProjectDeletionPast(s.cfg.Past)
		if err != nil {
			result.DecisionErrors = append(result.DecisionErrors, "past index rebuild error: "+err.Error())
		}
	}
	if len(result.Deleted) > 0 {
		// Refresh the roster before the bump and broadcast below, so the
		// UI's immediate follow-up navigation read is built from a roster that
		// already dropped the deleted sessions (their rendezvous files were
		// just unlinked) instead of showing ghost rows until the 5s tick.
		s.refreshRosterAfterDeletion(ctx)
		// Bust the tree memo unconditionally: a no-delta past rebuild plus a
		// nil PokeAttention would otherwise leave InputsVersion unmoved and
		// navigation serving the memoized pre-delete snapshot for its bucket.
		if s.cfg.Inputs != nil {
			s.cfg.Inputs.Bump()
		}
		if s.cfg.PokeAttention != nil {
			s.cfg.PokeAttention()
		}
		if !rebuilt {
			s.navigation.Invalidate(navigationChangeHint{})
		}
	}
	if len(result.Skipped) == 0 && len(result.DecisionErrors) == 0 {
		if err := s.cfg.DeletionStore.MarkDeleted(record.ProjectID, record.Generation); err != nil {
			result.DecisionErrors = append(result.DecisionErrors, "commit deleted state: "+err.Error())
		}
	}
	return result
}

func (s *WebServer) cleanupProjectDeletionTarget(stateDir, sessionID string) error {
	// Tombstone first, under the metadata writers' lock: an in-flight
	// out-of-process autosave holding or waiting on that lock would otherwise
	// recreate the meta after the sweep. The tombstone makes every later write
	// refuse. The sweep still removes the meta itself, so a removal failure
	// leaves the metadata in place for a resume.
	if err := schema.TombstoneSessionMeta(stateDir, sessionID); err != nil {
		return err
	}
	if err := s.removeProjectDeletionArtifacts(stateDir, sessionID); err != nil {
		// The sweep failed. While the metadata still exists the session is
		// resumable and must stay writable, so roll the tombstone back; otherwise a
		// transient IO or permission error would fence a live session's autosave,
		// rename, and observer appends with ErrSessionDeleted until the deletion is
		// retried to completion, which may never happen. Once the sweep has removed
		// the metadata the deletion is effectively done and the marker stays as the
		// resurrection fence. Best-effort: a failed rollback leaves the session
		// fenced, and the deletion's retry clears it.
		if sessionMetaFilePresent(stateDir, sessionID) {
			_ = schema.UntombstoneSessionMeta(stateDir, sessionID)
		}
		return err
	}
	// The metadata is durably gone and the tombstone now fences any writer, so
	// the lock inode has no further role: a writer that opens a fresh lock still
	// re-checks the tombstone under it, and every later write is refused. Drop it
	// so a completed deletion does not leave a dead lock file per session. This
	// is best-effort: the deletion has already succeeded, and failing here would
	// report a session whose metadata and transcript are gone as "skipped",
	// retain its archive/favorite/pin decisions, and leave the deletion record
	// disagreeing with the tree. Log and continue.
	lockPath := filepath.Join(stateDir, "sessions", sessionID+".meta.json.lock")
	if err := removeProjectSessionFile(lockPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "[hub] cleanupProjectDeletionTarget(%s): remove stale meta lock: %v\n", sessionID, err)
	}
	return nil
}

// removeProjectDeletionArtifacts removes every artifact of a tombstoned session
// except the lock and tombstone fences. The metadata is removed by the flat
// sweep, before the API log (so a scheduled contender cannot re-reserve a
// session whose metadata is already gone); a failure before that leaves the
// metadata in place, which is what lets the caller roll the tombstone back.
func (s *WebServer) removeProjectDeletionArtifacts(stateDir, sessionID string) error {
	sessionsDir := filepath.Join(stateDir, "sessions")
	if err := removeFlatProjectSessionArtifacts(sessionsDir, sessionID); err != nil {
		return err
	}
	if err := removeProjectSessionDir(filepath.Join(sessionsDir, sessionID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, path := range []string{
		filepath.Join(stateDir, "mutations", sessionID+".json"),
		filepath.Join(stateDir, "queues", sessionID+".json"),
		filepath.Join(stateDir, "tasks", sessionID+".json"),
	} {
		if err := removeProjectSessionFile(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := removeProjectSessionRendezvous(s.cfg.RunDir, sessionID); err != nil {
		return err
	}
	if err := removeProjectSessionDaemonLog(s.cfg.RunDir, sessionID); err != nil {
		return err
	}
	apiLogPath := filepath.Join(sessionsDir, sessionID+".api.jsonl")
	if err := removeProjectSessionFile(apiLogPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// sessionMetaFilePresent reports whether the session's metadata still exists on
// disk, used to decide whether a failed deletion sweep must roll its tombstone
// back (see cleanupProjectDeletionTarget). Only a confirmed absence counts as
// absent: a transient stat error (EACCES, EIO) must read as present, so a failed
// sweep still rolls the tombstone back and cannot fence a live, resumable
// session with ErrSessionDeleted.
func sessionMetaFilePresent(stateDir, sessionID string) bool {
	_, err := os.Stat(filepath.Join(stateDir, "sessions", sessionID+".meta.json"))
	return !os.IsNotExist(err)
}

func (s *WebServer) projectDeletionStateDir(projectID, threadID string, stateDirs map[string]string) string {
	if stateDir := stateDirs[threadID]; stateDir != "" {
		return stateDir
	}
	if s.cfg.StateDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.StateDir, "projects", projectID)
}

func removeProjectSessionRendezvous(runDir, sessionID string) error {
	if runDir == "" {
		return nil
	}
	entries, err := rendezvous.List(runDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.SessionID != sessionID && entry.ThreadID != sessionID {
			continue
		}
		if err := removeProjectSessionRendezvousEntry(runDir, entry); err != nil {
			return err
		}
	}
	return nil
}

// removeProjectSessionDaemonLog deletes the per-daemon log this session wrote
// under <run-dir>/logs (spawn_daemonlog.go). Nothing else ever removes one:
// rendezvous.List skips that subdirectory and hubcore.Roster prunes rendezvous
// entries only, so a machine that has spawned sessions for months keeps every
// daemon log it ever wrote (kata dd8d).
//
// Deletion is the one moment the hub knows for certain that nobody owns the
// file, which is why the reaping lives here and not behind an age or size
// policy: an operator reads these logs after a crash, sometimes days later,
// and a session that still exists is a session whose log still has a reader.
//
// Same run-dir boundary the rendezvous removal above already crosses, and
// sessionID has been through identifier.ValidateSessionID by the time this
// runs (removeFlatProjectSessionArtifacts, at the top of the caller);
// daemonLogName folds anything else away regardless.
func removeProjectSessionDaemonLog(runDir, sessionID string) error {
	if runDir == "" {
		return nil
	}
	path := filepath.Join(runDir, daemonLogDirName, daemonLogName(sessionID))
	if err := removeProjectSessionFile(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func removeFlatProjectSessionArtifacts(sessionsDir, sessionID string) error {
	if err := identifier.ValidateSessionID(sessionID); err != nil {
		return fmt.Errorf("invalid session ID: %w", err)
	}
	entries, err := os.ReadDir(sessionsDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sessions directory: %w", err)
	}
	prefix := sessionID + "."
	apiLogName := sessionID + ".api.jsonl"
	// Leave the cross-process meta lock file in place: deleting its inode while a
	// writer (the daemon) still holds it would let that writer recreate the meta
	// through the old inode while a new writer locks a fresh one, defeating the
	// serialization and resurrecting a deleted session.
	metaLockName := sessionID + ".meta.json.lock"
	// The tombstone must outlive the sweep: it is what stops a writer from
	// recreating the meta after deletion.
	tombstoneName := sessionID + schema.SessionMetaTombstoneSuffix
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == apiLogName || entry.Name() == metaLockName || entry.Name() == tombstoneName || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if err := removeProjectSessionFile(filepath.Join(sessionsDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func appendProjectDeleteLiveSkip(skipped []projectDeleteSkip, id string) []projectDeleteSkip {
	return append(skipped, projectDeleteSkip{ID: id, Reason: "resumed live"})
}
