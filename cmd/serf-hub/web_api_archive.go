package main

import (
	"encoding/json"
	"net/http"
	"time"

	"primeradiant.com/serf/identifier"
)

// handleAPIArchive handles POST /api/archive.
// Body: {"kind":"session"|"project","id":"...","archived":true|false}
// On success: 200 {"ok":true}. On bad input: 400. On wrong method: 405.
func (s *WebServer) handleAPIArchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Kind       string `json:"kind"`
		ID         string `json:"id"`
		WorkingDir string `json:"working_dir"`
		Archived   bool   `json:"archived"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Kind != "session" && body.Kind != "project" {
		writeAPIError(w, http.StatusBadRequest, `kind must be "session" or "project"`)
		return
	}
	if body.ID == "" {
		writeAPIError(w, http.StatusBadRequest, "id is required")
		return
	}
	if body.Kind == "project" {
		if body.ID == "no-project" {
			writeAPIError(w, http.StatusBadRequest, "no-project is not a local project")
			return
		}
		if err := identifier.ValidateProjectID(body.ID); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid project ID: "+err.Error())
			return
		}
		if body.WorkingDir == "" {
			writeAPIError(w, http.StatusBadRequest, "working_dir is required for project archive")
			return
		}
		project, err := identifier.ResolveProject(body.WorkingDir)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "resolve project: "+err.Error())
			return
		}
		if project.ID != body.ID {
			writeAPIError(w, http.StatusBadRequest, "project ID does not match working_dir")
			return
		}
	}
	if s.cfg.Archive == nil {
		writeAPIError(w, http.StatusInternalServerError, "archive store not configured")
		return
	}
	if err := s.cfg.Archive.Set(body.Kind, body.ID, body.Archived, time.Now()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "archive store error: "+err.Error())
		return
	}
	// An archive decision can move a session in or out of tier eligibility;
	// nudge the attention watcher so the badge/notification state doesn't lag
	// behind the sidebar until the next tick.
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
