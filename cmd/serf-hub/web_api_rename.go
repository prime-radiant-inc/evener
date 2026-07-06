package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
)

// handleAPIRename renames a session. Live serf sessions route through the
// daemon method (daemon-truth); ended local sessions have their meta edited
// behind a probe-resolved Roster.Find re-check. Both paths refresh the past
// index (UpdateMeta) + bump inputs so the next resync reflects the new name
// (round-3 G1). Legacy daemons that 404 the method surface a toast client-side;
// the hub never falls back to editing a *live* session's meta file.
func (s *WebServer) handleAPIRename(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "name is required")
		return
	}
	ref := appRefFromRouteID(id)

	if s.isLive(id) {
		source, err := sourceForThread(s.sources, ref, "")
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "session not live")
			return
		}
		if err := source.SetThreadName(r.Context(), appwire.ThreadNameSetParams{Ref: ref, Name: name}); err != nil {
			writeAPIWireError(w, http.StatusBadGateway, err)
			return
		}
		s.refreshRenamedMeta(id, name)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Ended path: edit meta behind a pre-write Roster.Find re-check; if it turns
	// live, route through the daemon instead (round-2 A2 / round-3 G1).
	//
	// NOTE: the hard-fail below (T18) is unreachable via a real HTTP request
	// today — the dispatcher (handleAPISession) strips the "local:" prefix
	// before calling handleAPIRename, so this recheck keys off the identical
	// string as the top-level isLive(id) check above and can never diverge
	// from it. Kept as defense-in-depth against future dispatch changes,
	// router-bypassing callers, or a Roster that starts tracking non-local
	// sessions.
	if s.cfg.Roster != nil {
		if _, live := s.cfg.Roster.Find(canonicalRouteID(id)); live {
			source, err := sourceForThread(s.sources, ref, "")
			if err != nil {
				writeAPIError(w, http.StatusNotFound, "session became live but its source is unavailable")
				return
			}
			if err := source.SetThreadName(r.Context(), appwire.ThreadNameSetParams{Ref: ref, Name: name}); err != nil {
				// The session raced back to live; editing the persisted meta now
				// would be silently reverted by the live session's next autosave
				// (T18). Fail loudly instead of writing a doomed meta edit.
				writeAPIWireError(w, http.StatusBadGateway, err)
				return
			}
			s.refreshRenamedMeta(id, name)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	pe, ok := s.cfg.Past.Find(canonicalRouteID(id))
	if !ok {
		writeAPIError(w, http.StatusNotFound, "session not found")
		return
	}
	meta, err := schema.LoadSessionMeta(pe.StateDir, pe.ID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "load meta: "+err.Error())
		return
	}
	meta.Name = name
	meta.NameSource = "user"
	meta.NameUpdatedAt = time.Now().UTC()
	if err := schema.SaveSessionMeta(pe.StateDir, meta); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save meta: "+err.Error())
		return
	}
	s.cfg.Past.UpdateMeta(pe.ID, meta) // re-sort + FTS + inputs bump
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
	w.WriteHeader(http.StatusNoContent)
}

// refreshRenamedMeta re-reads the persisted meta after a live rename and pushes
// it into the past index so the next tree resync shows the new name without a
// full Rebuild.
func (s *WebServer) refreshRenamedMeta(id, name string) {
	rid := canonicalRouteID(id)
	if pe, ok := s.cfg.Past.Find(rid); ok {
		if meta, err := schema.LoadSessionMeta(pe.StateDir, pe.ID); err == nil {
			s.cfg.Past.UpdateMeta(pe.ID, meta)
			if s.cfg.PokeAttention != nil {
				s.cfg.PokeAttention()
			}
			return
		}
		m := pe.Meta
		m.Name = name
		m.NameSource = "user"
		s.cfg.Past.UpdateMeta(pe.ID, m)
	}
	if s.cfg.PokeAttention != nil {
		s.cfg.PokeAttention()
	}
}
