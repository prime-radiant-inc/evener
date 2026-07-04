package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

type projectDeleteSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// handleAPIProjectDelete removes every session file under a project and scrubs
// its decision rows. Path-validated; refuses when anything is live at entry.
func (s *WebServer) handleAPIProjectDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Key        string `json:"key"`
		WorkingDir string `json:"workingDir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Key == "" || body.WorkingDir == "" {
		writeAPIError(w, http.StatusBadRequest, "key and workingDir are required")
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
	if matched == nil || matched.WorkingDir != body.WorkingDir {
		writeAPIError(w, http.StatusBadRequest, "key does not match workingDir")
		return
	}

	// Resolve the session set from All() (carries StateDir), uncapped.
	var entries []hubcore.PastEntry
	for _, e := range s.cfg.Past.All() {
		if hubcore.EffectiveWorkingDir(e.Meta) == body.WorkingDir {
			entries = append(entries, e)
		}
	}

	// Whole-project fast path: refuse when anything is live at entry.
	if s.cfg.Roster != nil {
		var liveNames []string
		for _, e := range entries {
			if _, ok := s.cfg.Roster.Find(e.ID); ok {
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
		// TOCTOU re-check via the probe-resolved Roster.Find (round-2 A9): a
		// genuine resume between entry and removal aborts this session.
		if s.cfg.Roster != nil {
			if _, ok := s.cfg.Roster.Find(e.ID); ok {
				skipped = append(skipped, projectDeleteSkip{ID: e.ID, Reason: "resumed live"})
				continue
			}
		}
		sess := filepath.Join(e.StateDir, "sessions")
		var removeErr error
		for _, p := range []string{
			filepath.Join(sess, e.ID+".meta.json"),
			filepath.Join(sess, e.ID+".transcript.jsonl"),
			filepath.Join(sess, e.ID+".log.jsonl"),
		} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				removeErr = err
				break
			}
		}
		if removeErr != nil {
			skipped = append(skipped, projectDeleteSkip{ID: e.ID, Reason: removeErr.Error()})
			continue
		}
		_ = os.RemoveAll(filepath.Join(sess, e.ID))
		if s.cfg.Archive != nil {
			_ = s.cfg.Archive.Delete("session", e.ID)
		}
		if s.cfg.Favorite != nil {
			_ = s.cfg.Favorite.Delete("session", e.ID)
		}
		deleted = append(deleted, e.ID)
	}

	// Scrub the project-level decision rows: the path row always, and the
	// legacy basename row only when no other project still uses that basename
	// (round-3 G3 — otherwise the legacy row re-hides a recreated project).
	basename := filepath.Base(body.WorkingDir)
	basenameStillUsed := false
	for _, e := range s.cfg.Past.All() {
		wd := hubcore.EffectiveWorkingDir(e.Meta)
		if wd != body.WorkingDir && filepath.Base(wd) == basename {
			basenameStillUsed = true
			break
		}
	}
	if s.cfg.Archive != nil {
		_ = s.cfg.Archive.Delete("project", body.WorkingDir)
		if !basenameStillUsed {
			_ = s.cfg.Archive.Delete("project", basename)
		}
	}
	if s.cfg.Favorite != nil {
		_ = s.cfg.Favorite.Delete("project", body.WorkingDir)
		if !basenameStillUsed {
			_ = s.cfg.Favorite.Delete("project", basename)
		}
	}

	_ = s.cfg.Past.Rebuild() // also the FTS scrub
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "skipped": skipped})
}
