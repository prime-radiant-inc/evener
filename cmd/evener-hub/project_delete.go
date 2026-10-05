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
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

type projectDeleteSkip = appwire.ProjectDeleteSkip

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
	// The request is validated against the project's own facts, not a full
	// tree: the working directory resolved to the requested key above, and the
	// remote threads say whether a host shares the project (round-2 A11: never
	// invert the lossy slug on a destructive path).
	//
	// A merged project — the controller's own rows plus a host's under the same
	// canonical ID and path — is not deletable here. The rail refuses it before
	// the confirmation dialog opens, and the wire must refuse it too: a request
	// that named no source would otherwise remove the controller's sessions of
	// a project a host also owns, leaving one project half-deleted and the UI
	// and the API disagreeing about the same row.
	if hosts := s.remoteProjectHosts(ctx, project); len(hosts) > 0 {
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
	entries := projectDeleteEntries(all, func(e hubcore.PastEntry) bool {
		return projects[hubcore.EffectiveWorkingDir(e.Meta)].ID == params.Key
	})

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
		target := hubcore.DeletionTarget{
			Ref:      localAppRef(entry.ID),
			ThreadID: entry.ID,
		}
		if owner, ok := stateDirProjectID(entry.StateDir); ok && owner != project.ID {
			target.StateProjectID = owner
		}
		targets = append(targets, target)
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

// projectDeleteEntries is the set a project delete removes: every entry whose
// own working directory belongs to the project (inProject), plus every subagent
// reachable from one of those through parent or job-tree-root links at any
// depth. The reach keeps a subagent that ran from another directory (an
// isolated worktree, say) from being leaked; the own-directory rule keeps an
// orphan subagent whose parent is gone. Fork continuations are independent
// roots and are not followed: one that ran elsewhere belongs to that project.
func projectDeleteEntries(all []hubcore.PastEntry, inProject func(hubcore.PastEntry) bool) []hubcore.PastEntry {
	subagentsUnder := make(map[string][]int)
	for i, e := range all {
		if !e.Meta.IsSubagent {
			continue
		}
		for _, owner := range []string{e.Meta.ParentSessionID, e.Meta.JobTreeRootSessionID} {
			if owner != "" {
				subagentsUnder[owner] = append(subagentsUnder[owner], i)
			}
		}
	}
	selected := make(map[int]bool)
	var pending []int
	for i, e := range all {
		if inProject(e) {
			selected[i] = true
			pending = append(pending, i)
		}
	}
	for len(pending) > 0 {
		i := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, child := range subagentsUnder[all[i].ID] {
			if !selected[child] {
				selected[child] = true
				pending = append(pending, child)
			}
		}
	}
	entries := make([]hubcore.PastEntry, 0, len(selected))
	for i, e := range all {
		if selected[i] {
			entries = append(entries, e)
		}
	}
	return entries
}

// remoteProjectHosts names the hosts whose threads carry this project's
// identity, sorted. The controller's own rows are not listed.
func (s *WebServer) remoteProjectHosts(ctx context.Context, project identifier.Project) []string {
	seen := make(map[string]bool)
	for _, thread := range s.remoteThreadFetch(ctx).threads {
		ref, ok := appThreadTreeRef(thread)
		if !ok {
			continue
		}
		_, entry, _ := appThreadTreeEntries(thread)
		if entry.Project.ID != project.ID || entry.Project.CanonicalPath != project.CanonicalPath {
			continue
		}
		if source := hubcore.NormalizeDecisionSource(ref.SourceID); source != "" {
			seen[source] = true
		}
	}
	hosts := make([]string, 0, len(seen))
	for host := range seen {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
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
		stateDir := s.projectDeletionStateDir(target.StateProjectID, record.ProjectID, target.ThreadID, stateDirs)
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
	if err := s.cleanupProjectDeletionTarget(stateDir, threadID); err != nil {
		return false, &projectDeleteSkip{ID: threadID, Reason: err.Error()}, decisionErrors
	}
	return true, nil, append(decisionErrors, s.scrubSessionDecisions(threadID)...)
}

func (s *WebServer) scrubSessionDecisions(threadID string) (decisionErrors []string) {
	authority := s.sessionDecisionAuthority(threadID)
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
	if s.cfg.SessionSeen != nil {
		if _, err := s.cfg.SessionSeen.Delete("", threadID); err != nil {
			decisionErrors = append(decisionErrors, fmt.Sprintf("seen marker store error: %v", err))
		}
	}
	// A deleted session's words leave search now, not at the next refresh.
	if s.cfg.MessageSearch != nil {
		if err := s.cfg.MessageSearch.Forget(context.Background(), threadID); err != nil {
			decisionErrors = append(decisionErrors, fmt.Sprintf("message search index error: %v", err))
		}
	}
	return decisionErrors
}

// sessionDecisionAuthority is the referenced-ID authority for the one session
// being scrubbed: the same collection navigation runs for the ids the stores
// refer to, with no tree behind it. The deleted session has no live or remote
// facts of its own to add, so the snapshot is empty.
func (s *WebServer) sessionDecisionAuthority(threadID string) hubcore.FavoriteAuthority {
	sessions, _ := referencedSessionAuthorities([]string{threadID}, navigationSnapshot{}, s.localSessionIndex())
	return hubcore.FavoriteAuthority{Sessions: sessions}
}

func (s *WebServer) cleanupProjectDeletion(
	ctx context.Context,
	record hubcore.DeletionRecord,
	stateDirs map[string]string,
) projectDeletionCleanupResult {
	result := projectDeletionCleanupResult{}
	for _, target := range record.Targets {
		stateDir := s.projectDeletionStateDir(target.StateProjectID, record.ProjectID, target.ThreadID, stateDirs)
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
	// The transcript's index sidecar is derived from it and goes with it; the
	// hub's cached handle on it is closed first, so nothing keeps the deleted
	// files open.
	transcriptPath := filepath.Join(sessionsDir, sessionID+".transcript.jsonl")
	pastTranscriptIndexes.Forget(transcriptPath)
	if err := removeProjectSessionDir(transcriptindex.DirFor(transcriptPath)); err != nil && !os.IsNotExist(err) {
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
	// Last and best-effort, as on archive: the session is already gone, and an
	// entry the scratch removal cannot take must not leave it half-deleted.
	_ = agentsandbox.RemoveSessionScratchTree(sessionID)
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

// projectDeletionStateDir locates a target's state directory: the caller's
// live index entry when it has one, else the project directory the target was
// recorded under (stateProjectID), else the deleted project's own.
func (s *WebServer) projectDeletionStateDir(stateProjectID, projectID, threadID string, stateDirs map[string]string) string {
	if stateProjectID != "" {
		projectID = stateProjectID
	}
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
