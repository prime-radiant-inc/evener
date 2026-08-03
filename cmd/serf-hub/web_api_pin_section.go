package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
)

type pinSectionMutationResponse struct {
	OK      bool              `json:"ok"`
	Changed bool              `json:"changed"`
	Section hubapi.PinSection `json:"section"`
}

type pinSectionDeleteResponse struct {
	OK          bool `json:"ok"`
	Changed     bool `json:"changed"`
	MemberCount int  `json:"member_count"`
}

func (s *WebServer) handleAPIPinSections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	if s.cfg.PinSections == nil {
		writeAPIError(w, http.StatusInternalServerError, "pin section store not configured")
		return
	}
	sections, err := s.cfg.PinSections.Sections()
	if err != nil {
		writePinSectionError(w, err)
		return
	}
	out := make([]hubapi.PinSection, 0, len(sections))
	for _, section := range sections {
		out = append(out, apiPinSection(section))
	}
	writeAPIJSON(w, http.StatusOK, out)
}

func (s *WebServer) handleAPIPinSection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		writeAPIError(w, http.StatusMethodNotAllowed, "PATCH or DELETE required")
		return
	}
	if s.cfg.PinSections == nil {
		writeAPIError(w, http.StatusInternalServerError, "pin section store not configured")
		return
	}
	sectionID, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/pin-sections/"))
	if err != nil || sectionID == "" || strings.Contains(sectionID, "/") {
		writeAPIError(w, http.StatusNotFound, hubcore.ErrPinSectionNotFound.Error())
		return
	}
	if r.Method == http.MethodDelete {
		s.handleAPIPinSectionDelete(w, sectionID)
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	section, changed, err := s.cfg.PinSections.Rename(sectionID, body.Name, time.Now())
	if err != nil {
		writePinSectionError(w, err)
		return
	}
	section, err = s.pinSectionWithCount(section.ID)
	if err != nil {
		if changed {
			s.notifyMutation()
		}
		writePinSectionError(w, err)
		return
	}
	if changed {
		s.notifyMutation()
	}
	writeAPIJSON(w, http.StatusOK, pinSectionMutationResponse{OK: true, Changed: changed, Section: apiPinSection(section)})
}

func (s *WebServer) handleAPIPinSectionDelete(w http.ResponseWriter, sectionID string) {
	memberCount, changed, err := s.cfg.PinSections.DeleteSection(sectionID)
	if err != nil {
		writePinSectionError(w, err)
		return
	}
	if changed {
		s.notifyMutation()
	}
	writeAPIJSON(w, http.StatusOK, pinSectionDeleteResponse{OK: true, Changed: changed, MemberCount: memberCount})
}

func (s *WebServer) handleAPISessionPin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeAPIError(w, http.StatusMethodNotAllowed, "POST or DELETE required")
		return
	}
	if s.cfg.PinSections == nil {
		writeAPIError(w, http.StatusInternalServerError, "pin section store not configured")
		return
	}
	if r.Method == http.MethodDelete {
		s.handleAPISessionUnpin(w, r)
		return
	}

	var body struct {
		SessionRef  string  `json:"session_ref"`
		SectionID   *string `json:"section_id"`
		SectionName *string `json:"section_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if (body.SectionID == nil) == (body.SectionName == nil) {
		writeAPIError(w, http.StatusBadRequest, "exactly one of section_id or section_name is required")
		return
	}
	sessionID, ok := s.topLevelFavoriteSessionID(r.Context(), body.SessionRef)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "session_ref must name a real top-level session")
		return
	}

	var section hubcore.PinSection
	var changed bool
	var err error
	if body.SectionID != nil {
		section, changed, err = s.cfg.PinSections.Assign(*body.SectionID, sessionID, time.Now())
	} else {
		section, changed, err = s.cfg.PinSections.CreateOrReuseAndAssign(*body.SectionName, sessionID, time.Now())
	}
	if err != nil {
		writePinSectionError(w, err)
		return
	}
	section, err = s.pinSectionWithCount(section.ID)
	if err != nil {
		if changed {
			s.notifyMutation()
		}
		writePinSectionError(w, err)
		return
	}
	if changed {
		s.notifyMutation()
	}
	writeAPIJSON(w, http.StatusOK, hubapi.SessionPinMutationResponse{
		OK:      true,
		Changed: changed,
		Assignment: hubapi.SessionPinAssignment{
			SessionRef: hubRefFromTreeNodeID(sessionID).String(),
			Section:    apiPinSection(section),
		},
	})
}

func (s *WebServer) handleAPISessionUnpin(w http.ResponseWriter, r *http.Request) {
	requested := r.URL.Query().Get("ref")
	sessionID, ok := s.topLevelFavoriteSessionID(r.Context(), requested)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "ref must name a real top-level session")
		return
	}
	changed, err := s.cfg.PinSections.Unpin(sessionID)
	if err != nil {
		writePinSectionError(w, err)
		return
	}
	if changed {
		s.notifyMutation()
	}
	writeAPIJSON(w, http.StatusOK, hubapi.SessionPinMutationResponse{
		OK:      true,
		Changed: changed,
		Assignment: hubapi.SessionPinAssignment{
			SessionRef: hubRefFromTreeNodeID(sessionID).String(),
		},
	})
}

func (s *WebServer) pinSectionWithCount(sectionID string) (hubcore.PinSection, error) {
	sections, err := s.cfg.PinSections.Sections()
	if err != nil {
		return hubcore.PinSection{}, err
	}
	for _, section := range sections {
		if section.ID == sectionID {
			return section, nil
		}
	}
	return hubcore.PinSection{}, hubcore.ErrPinSectionNotFound
}

func apiPinSection(section hubcore.PinSection) hubapi.PinSection {
	return hubapi.PinSection{ID: section.ID, Name: section.Name, MemberCount: section.MemberCount}
}

func writePinSectionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hubcore.ErrPinSectionName):
		writeAPIError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, hubcore.ErrPinSectionNotFound):
		writeAPIError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, hubcore.ErrPinSectionConflict):
		writeAPIError(w, http.StatusConflict, err.Error())
	default:
		writeAPIError(w, http.StatusInternalServerError, "pin section store error: "+err.Error())
	}
}
