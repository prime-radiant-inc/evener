package main

import (
	"encoding/json"
	"net/http"
	"time"
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
	if s.cfg.Favorite == nil {
		writeAPIError(w, http.StatusInternalServerError, "favorite store not configured")
		return
	}
	if err := s.cfg.Favorite.Set(body.Kind, body.ID, body.Favorited, time.Now()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "favorite store error: "+err.Error())
		return
	}
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	writeAPIJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
