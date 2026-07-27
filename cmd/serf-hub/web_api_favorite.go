package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
)

// handleAPIFavorite handles POST /api/favorite.
// Body: {"kind":"session","id":"...","favorited":true|false}
func (s *WebServer) handleAPIFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Kind      string `json:"kind"`
		ID        string `json:"id"`
		Favorited bool   `json:"favorited"`
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
	if body.Kind == "session" {
		resolved, ok := s.topLevelFavoriteSessionID(r.Context(), body.ID)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "session id must name a real top-level session")
			return
		}
		body.ID = resolved
	}
	if s.cfg.Favorite == nil {
		writeAPIError(w, http.StatusInternalServerError, "favorite store not configured")
		return
	}
	if err := s.cfg.Favorite.Set(body.Kind, body.ID, body.Favorited, time.Now()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "favorite store error: "+err.Error())
		return
	}
	s.notifyMutation()
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *WebServer) topLevelFavoriteSessionID(ctx context.Context, requested string) (string, bool) {
	if strings.HasPrefix(requested, "cluster:") {
		return "", false
	}
	var ids []string
	if s.cfg.Past != nil {
		for id := range hubcore.TopLevelSessionIDs(s.cfg.Past.AllMetas()) {
			ids = append(ids, id)
		}
	}
	tree, _ := s.memoTree(ctx)
	for _, node := range tree.Live {
		if node.Kind == "session" {
			ids = append(ids, node.ID)
		}
	}
	projects := append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...)
	for _, project := range projects {
		for _, tier := range [][]hubcore.TreeNode{project.Current, project.Recent, project.Archived} {
			for _, node := range tier {
				if node.Kind == "session" {
					ids = append(ids, node.ID)
				}
			}
		}
	}
	for _, id := range ids {
		if favoriteSessionIDMatches(requested, id) {
			return id, true
		}
	}
	return "", false
}

func favoriteSessionIDMatches(requested, actual string) bool {
	if requested == actual {
		return true
	}
	actualRef := hubRefFromTreeNodeID(actual)
	if requestedRef, err := hubapi.ParseRef(requested); err == nil && requestedRef == actualRef {
		return true
	}
	return actualRef.HostID == "local" && requested == actualRef.SessionID
}
