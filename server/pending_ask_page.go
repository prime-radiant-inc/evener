package server

import (
	"strings"

	"primeradiant.com/evener/appwire"
)

// projectPendingAsk leaves the owner whole and bounds only its wire-row copy.
func projectPendingAsk(state *appwire.PendingAskState) *appwire.PendingAskState {
	projected := appwire.ClonePendingAsk(state)
	if projected != nil {
		projected.CompositeTotal = len(projected.Calls)
		projected.CallsTruncated = len(projected.Calls) > appwire.MaxPendingAskComposites
		if projected.CallsTruncated {
			projected.Calls = projected.Calls[:appwire.MaxPendingAskComposites]
		}
	}
	return projected
}

// appPendingAskPage samples the live owner without a projection or read-cut lock.
// The returned row and page carry that one snapshot, even when publication lags.
func (s *Server) appPendingAskPage(params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	s.mu.RLock()
	rootID := s.appThreadID
	target, _ := s.appReadTargetLocked(appwire.ThreadReadParams{Ref: params.Ref})
	source := s.appEnvelopeSource
	generation := s.appEnvelopeSourceGeneration
	s.mu.RUnlock()
	if target == "" || target != rootID || strings.TrimSpace(params.ThreadID) != "" && strings.TrimSpace(params.ThreadID) != rootID {
		return appwire.ThreadReadResponse{}, appwire.InvalidParams("pending ask pages require the current root ref")
	}
	mismatch := appwire.WireError{Code: appwire.CodeConflict, Message: "pending question set changed", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorAskFenceMismatch}}
	if source == nil {
		return appwire.ThreadReadResponse{}, mismatch
	}
	question, state := source.PendingAskSnapshot()
	if state == nil || state.InstanceID != rootID || state.BootGeneration != params.ExpectedAskBootGeneration || state.Revision != *params.ExpectedAskRevision || len(state.Calls) == 0 && state.CalllessCount == 0 {
		return appwire.ThreadReadResponse{}, mismatch
	}
	cut := s.appThreadReadCutForTarget(params, rootID)
	s.mu.RLock()
	current := s.appThreadID == rootID && s.appEnvelopeSourceGeneration == generation
	s.mu.RUnlock()
	if !current || !cut.found {
		return appwire.ThreadReadResponse{}, mismatch
	}
	offset := 0
	if params.Offset != nil {
		offset = max(0, min(*params.Offset, len(state.Calls)))
	}
	limit := appwire.MaxPendingAskPageLimit
	if params.Limit != nil && *params.Limit > 0 {
		limit = min(*params.Limit, limit)
	}
	end := offset + min(limit, len(state.Calls)-offset)
	page := &appwire.PendingAskPage{Composites: append([]appwire.PendingAskCall{}, state.Calls[offset:end]...), Total: len(state.Calls), CalllessCount: state.CalllessCount}
	if end < len(state.Calls) {
		page.NextOffset = &end
	}
	response := cut.response
	response.Thread.Evener.PendingQuestion = appwire.ClonePendingQuestion(question)
	response.Thread.Evener.AskPending = question != nil
	response.Thread.Evener.PendingAsk = projectPendingAsk(state)
	response.PendingAskPage = page
	return response, nil
}
