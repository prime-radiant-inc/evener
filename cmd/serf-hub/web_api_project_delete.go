package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

type projectDeleteSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

var (
	removeProjectSessionFile = os.Remove
	removeProjectSessionDir  = os.RemoveAll
	projectSessionLive       = func(roster *hubcore.Roster, id string) bool {
		_, ok := roster.Find(id)
		return ok
	}
)

// handleAPIProjectDelete removes every session file under a project and scrubs
// its decision rows. Path-validated; refuses when anything is live at entry.
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

	deleted := []string{}
	skipped := []projectDeleteSkip{}
	for _, e := range entries {
		owner, err := llm.NewSessionAPILogger(e.StateDir)
		if err == nil {
			err = owner.ReserveSession(e.ID)
		}
		if err != nil {
			if owner != nil {
				_ = owner.Close()
			}
			if errors.Is(err, llm.ErrAPILogTargetLocked) {
				skipped = appendProjectDeleteLiveSkip(skipped, e.ID)
			} else {
				skipped = append(skipped, projectDeleteSkip{ID: e.ID, Reason: err.Error()})
			}
			continue
		}

		// Hold the same target-file ownership lock used by resume across the
		// final liveness decision and removal.
		if s.cfg.Roster != nil {
			if projectSessionLive(s.cfg.Roster, e.ID) {
				_ = owner.Close()
				skipped = appendProjectDeleteLiveSkip(skipped, e.ID)
				continue
			}
		}
		sess := filepath.Join(e.StateDir, "sessions")
		removeErr := removeFlatProjectSessionArtifacts(sess, e.ID)
		if removeErr != nil {
			_ = owner.Close()
			skipped = append(skipped, projectDeleteSkip{ID: e.ID, Reason: removeErr.Error()})
			continue
		}
		_ = removeProjectSessionDir(filepath.Join(sess, e.ID))
		if s.cfg.Archive != nil {
			_ = s.cfg.Archive.Delete("session", e.ID)
		}
		if s.cfg.Favorite != nil {
			_ = s.cfg.Favorite.Delete("session", e.ID)
		}
		// The API-log pathname is the ownership boundary. Remove it only after
		// every other session artifact, so a replacement owner can never acquire
		// the pathname while deletion still has destructive work to do.
		apiLogPath := filepath.Join(sess, e.ID+".api.jsonl")
		if err := removeProjectSessionFile(apiLogPath); err != nil && !os.IsNotExist(err) {
			_ = owner.Close()
			skipped = append(skipped, projectDeleteSkip{ID: e.ID, Reason: err.Error()})
			continue
		}
		_ = owner.Close()
		deleted = append(deleted, e.ID)
	}

	// Scrub only the canonical project-level decision rows. Display basenames
	// are never decision keys.
	if s.cfg.Archive != nil {
		_ = s.cfg.Archive.Delete("project", project.ID)
	}
	if s.cfg.Favorite != nil {
		_ = s.cfg.Favorite.Delete("project", project.ID)
	}

	_ = s.cfg.Past.Rebuild() // also the FTS scrub
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "skipped": skipped})
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
