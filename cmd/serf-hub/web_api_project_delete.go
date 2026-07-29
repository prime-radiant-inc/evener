package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

type projectDeleteSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

var (
	removeProjectSessionFile            = os.Remove
	removeProjectSessionDir             = os.RemoveAll
	removeProjectSessionRendezvousEntry = rendezvous.Remove
	rebuildProjectDeletionPast          = func(past *hubcore.PastIndex) (bool, error) { return past.Rebuild() }
	projectSessionLive                  = func(roster *hubcore.Roster, id string) bool {
		_, ok := roster.Find(id)
		return ok
	}
)

// handleAPIProjectDelete removes every session file under a project and scrubs
// only the decision rows for artifacts it removed. Path-validated; refuses
// when anything is live at entry.
func (s *WebServer) handleAPIProjectDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Key        string `json:"key"`
		WorkingDir string `json:"working_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Key == "" || body.WorkingDir == "" {
		writeAPIError(w, http.StatusBadRequest, "key and workingDir are required")
		return
	}
	if body.Key == "no-project" {
		writeAPIError(w, http.StatusBadRequest, "no-project is not a local project")
		return
	}
	if err := identifier.ValidateProjectID(body.Key); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid project ID: "+err.Error())
		return
	}
	project, err := identifier.ResolveProject(body.WorkingDir)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "resolve project: "+err.Error())
		return
	}
	if project.ID != body.Key {
		writeAPIError(w, http.StatusBadRequest, "project ID does not match working_dir")
		return
	}
	if s.cfg.Past == nil {
		writeAPIError(w, http.StatusInternalServerError, "past index not configured")
		return
	}
	if s.deletionStoreErr != nil {
		writeAPIError(w, http.StatusInternalServerError, "load deletion state: "+s.deletionStoreErr.Error())
		return
	}
	if record, ok := s.cfg.DeletionStore.DeletingProject(project.ID); ok {
		releaseOwnership, ownerErr := s.acquireProjectDeletionOwnership(record, nil)
		if ownerErr != nil {
			skipped := []projectDeleteSkip{{ID: ownerErr.ThreadID, Reason: ownerErr.Error()}}
			if errors.Is(ownerErr.Err, llm.ErrAPILogTargetLocked) || ownerErr.Live {
				skipped = appendProjectDeleteLiveSkip(nil, ownerErr.ThreadID)
			}
			writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": []string{}, "skipped": skipped})
			return
		}
		defer releaseOwnership()
		result := s.cleanupProjectDeletion(record, nil)
		if len(result.DecisionErrors) > 0 {
			writeAPIError(w, http.StatusInternalServerError, strings.Join(result.DecisionErrors, "; "))
			return
		}
		writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": result.Deleted, "skipped": result.Skipped})
		return
	}
	// Validate the body against the current tree entry for that key — never
	// invert the lossy slug on a destructive path (round-2 A11).
	tree, _ := s.memoTree(r.Context())
	var matched *hubcore.TreeProject
	for _, p := range append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...) {
		if p.Key == body.Key {
			pp := p
			matched = &pp
			break
		}
	}
	if matched == nil || matched.WorkingDir != project.CanonicalPath {
		writeAPIError(w, http.StatusBadRequest, "key does not match workingDir")
		return
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
		writeAPIError(w, http.StatusInternalServerError, "resolve project membership: "+err.Error())
		return
	}

	// Select the session set from All() (carries StateDir), uncapped.
	var entries []hubcore.PastEntry
	for _, e := range all {
		workingDir := hubcore.EffectiveWorkingDir(e.Meta)
		if projects[workingDir].ID == body.Key {
			entries = append(entries, e)
		}
	}

	// Whole-project fast path: refuse when anything is live at entry.
	if s.cfg.Roster != nil {
		var liveNames []string
		for _, e := range entries {
			if projectSessionLive(s.cfg.Roster, e.ID) {
				liveNames = append(liveNames, hubcore.ShortID(e.ID))
			}
		}
		if len(liveNames) > 0 {
			writeAPIJSON(w, http.StatusConflict, map[string]any{"error": "project has live sessions", "live": liveNames})
			return
		}
	}

	if len(entries) == 0 {
		writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": []string{}, "skipped": []projectDeleteSkip{}})
		return
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
	ownedTargets, skipped, releaseOwnership := s.acquireProjectDeletionCandidates(targets, stateDirs)
	defer releaseOwnership()
	if len(ownedTargets) == 0 {
		writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": []string{}, "skipped": skipped})
		return
	}

	record, err := s.cfg.DeletionStore.BeginProject(project.ID, ownedTargets, len(skipped) == 0)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "commit deletion fence: "+err.Error())
		return
	}
	result := s.cleanupProjectDeletion(record, stateDirs)
	result.Skipped = append(skipped, result.Skipped...)
	if len(result.DecisionErrors) > 0 {
		writeAPIError(w, http.StatusInternalServerError, strings.Join(result.DecisionErrors, "; "))
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": result.Deleted, "skipped": result.Skipped})
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
	targets []hubcore.DeletionTarget,
	stateDirs map[string]string,
) ([]hubcore.DeletionTarget, []projectDeleteSkip, func()) {
	targets = append([]hubcore.DeletionTarget(nil), targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].ThreadID < targets[j].ThreadID })
	var owned []hubcore.DeletionTarget
	var skipped []projectDeleteSkip
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, target := range targets {
		record := hubcore.DeletionRecord{ProjectID: "", Targets: []hubcore.DeletionTarget{target}}
		stateDir := stateDirs[target.ThreadID]
		releaseTarget, err := s.acquireProjectDeletionOwnership(record, map[string]string{target.ThreadID: stateDir})
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
		release, err := s.acquireProjectDeletionOwnership(record, nil)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		result := s.cleanupProjectDeletion(record, nil)
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
	record hubcore.DeletionRecord,
	stateDirs map[string]string,
) (func(), *projectDeletionOwnershipError) {
	targets := append([]hubcore.DeletionTarget(nil), record.Targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].ThreadID < targets[j].ThreadID })
	var locks []*sync.Mutex
	var owners []*llm.APILogger
	release := func() {
		for i := len(owners) - 1; i >= 0; i-- {
			_ = owners[i].Close()
		}
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
	for _, target := range targets {
		lock := s.lockForSession(target.ThreadID)
		lock.Lock()
		locks = append(locks, lock)
		if s.cfg.Roster != nil && projectSessionLive(s.cfg.Roster, target.ThreadID) {
			release()
			return nil, &projectDeletionOwnershipError{ThreadID: target.ThreadID, Live: true}
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

func (s *WebServer) cleanupProjectDeletion(
	record hubcore.DeletionRecord,
	stateDirs map[string]string,
) projectDeletionCleanupResult {
	result := projectDeletionCleanupResult{}
	for _, target := range record.Targets {
		stateDir := s.projectDeletionStateDir(record.ProjectID, target.ThreadID, stateDirs)
		if err := s.cleanupProjectDeletionTarget(stateDir, target.ThreadID); err != nil {
			result.Skipped = append(result.Skipped, projectDeleteSkip{ID: target.ThreadID, Reason: err.Error()})
			continue
		}
		result.Deleted = append(result.Deleted, target.ThreadID)
		if s.cfg.Archive != nil {
			if err := s.cfg.Archive.Delete("session", target.ThreadID); err != nil {
				result.DecisionErrors = append(result.DecisionErrors, fmt.Sprintf("archive store error: %v", err))
			}
		}
		if s.cfg.Favorite != nil {
			if err := s.cfg.Favorite.Delete("session", target.ThreadID); err != nil {
				result.DecisionErrors = append(result.DecisionErrors, fmt.Sprintf("favorite store error: %v", err))
			}
		}
	}
	if len(result.Skipped) == 0 && record.WholeProject {
		if s.cfg.Archive != nil {
			if err := s.cfg.Archive.Delete("project", record.ProjectID); err != nil {
				result.DecisionErrors = append(result.DecisionErrors, fmt.Sprintf("archive store error: %v", err))
			}
		}
		if s.cfg.Favorite != nil {
			if err := s.cfg.Favorite.Delete("project", record.ProjectID); err != nil {
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
		if s.cfg.PokeAttention != nil {
			s.cfg.PokeAttention()
		}
		if !rebuilt {
			notifyTreeChanged(s.appRPC)
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
	apiLogPath := filepath.Join(sessionsDir, sessionID+".api.jsonl")
	if err := removeProjectSessionFile(apiLogPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
		if err := removeProjectSessionRendezvousEntry(runDir, entry.PID); err != nil {
			return err
		}
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
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == apiLogName || !strings.HasPrefix(entry.Name(), prefix) {
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
