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
// Body: {"kind":"project","id":"...","favorited":true|false}
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
	if body.Kind == "session" {
		writeAPIError(w, http.StatusBadRequest, "session favorites moved to /api/session-pin")
		return
	}
	if body.Kind != "project" {
		writeAPIError(w, http.StatusBadRequest, `kind must be "project"`)
		return
	}
	if body.ID == "" {
		writeAPIError(w, http.StatusBadRequest, "id is required")
		return
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
	metas, live, _ := s.navigationTreeInputs(ctx)
	ids := hubcore.TopLevelSessionIDs(metas)
	metaIDs := make(map[string]struct{}, len(metas))
	for _, meta := range metas {
		metaIDs[meta.ID] = struct{}{}
	}
	// A live session can be visible in the tree before its metadata reaches
	// PastIndex. Such a session is a top-level root by construction; sessions
	// with metadata are classified by the same helper as tree construction.
	for _, entry := range live {
		if entry.SessionID == "" {
			continue
		}
		if _, known := metaIDs[entry.SessionID]; !known {
			ids[entry.SessionID] = struct{}{}
		}
	}
	for id := range ids {
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
