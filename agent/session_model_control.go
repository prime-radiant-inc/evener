package agent

import (
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
)

// ModelControlRequest is one requested change to the session's canonical model.
// Every request names one client intent and the exact Session generation that
// may accept it.
type ModelControlRequest struct {
	ModelProvider          string
	Model                  string
	ClientMutationID       string
	ExpectedInstanceID     string
	ExpectedBootGeneration string
}

func (r ModelControlRequest) ref() string {
	if r.ModelProvider == "" {
		return r.Model
	}
	return r.ModelProvider + "/" + r.Model
}

func (s *Session) validateModelControlAuthorityLocked(request ModelControlRequest) error {
	if request.ClientMutationID == "" || request.ExpectedInstanceID == "" || request.ExpectedBootGeneration == "" {
		return appwire.InvalidParams("model control identity requires client mutation, instance, and boot generation")
	}
	if request.ExpectedInstanceID != s.id || request.ExpectedBootGeneration != s.bootGeneration {
		return appwire.ModelControlAuthorityStale(request.ClientMutationID, "model control authority does not match the active session generation")
	}
	return nil
}

func modelControlActive(profile *provider.Profile) appwire.ModelControlActive {
	if profile == nil {
		return appwire.ModelControlActive{ReasoningEffortLevels: []string{}}
	}
	return appwire.ModelControlActive{
		ModelProvider:         profile.ID(),
		Model:                 profile.Model(),
		ReasoningEffortLevels: append([]string{}, profile.ReasoningEffortLevels()...),
		SupportsReasoning:     profile.SupportsReasoning(),
	}
}

func cloneModelControlSnapshot(snapshot appwire.ModelControlSnapshot) appwire.ModelControlSnapshot {
	snapshot.Active.ReasoningEffortLevels = append([]string{}, snapshot.Active.ReasoningEffortLevels...)
	if snapshot.Desired != nil {
		desired := *snapshot.Desired
		snapshot.Desired = &desired
	}
	return snapshot
}

// ModelControlSnapshot returns one detached copy of the Session-owned model
// authority. Active model metadata and desired intent are sampled in one lock
// hold so consumers never combine different revisions.
func (s *Session) ModelControlSnapshot() appwire.ModelControlSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneModelControlSnapshot(s.modelControl)
}

func (s *Session) modelControlResponseLocked(disposition appwire.ModelControlDisposition) appwire.ThreadModelSetResponse {
	snapshot := cloneModelControlSnapshot(s.modelControl)
	return appwire.ThreadModelSetResponse{
		ModelControlVersion: appwire.ModelControlVersion1,
		Disposition:         disposition,
		ModelControl:        &snapshot,
	}
}

// advanceModelControlLocked publishes one model-control transition.
//
// The desired record belongs to the newest issue, so an older pick that is
// still applying as a fallback updates Active without touching it. Replacing
// it would do two kinds of damage at once: clients would show the fallback as
// the newest intent while the queue still tries the newer pick first, and the
// newer pick's own rejection could no longer match the record, so both that
// rejection and its transcript warning would vanish. Callers hold s.mu.
func (s *Session) advanceModelControlLocked(profile *provider.Profile, fence modelSwitchFence, lifecycle appwire.ModelControlLifecycle) appwire.ModelControlSnapshot {
	if s.modelControl.Revision == 0 {
		s.modelControl.Authority = appwire.ModelControlAuthority{InstanceID: s.id, BootGeneration: s.bootGeneration}
		s.modelControl.Active = modelControlActive(s.profile)
	}
	s.modelControl.Revision++
	if profile != nil {
		s.modelControl.Active = modelControlActive(profile)
	}
	if desired := s.modelControl.Desired; desired != nil && desired.RequestSequence > fence.pickSeq {
		return cloneModelControlSnapshot(s.modelControl)
	}
	s.modelControl.Desired = &appwire.ModelControlDesired{
		RequestSequence:  fence.pickSeq,
		ClientMutationID: fence.clientMutationID,
		ModelProvider:    fence.modelProvider,
		Model:            fence.model,
		Lifecycle:        lifecycle,
	}
	return cloneModelControlSnapshot(s.modelControl)
}

func (s *Session) emitModelControlChanged(snapshot appwire.ModelControlSnapshot) {
	s.emit(events.ModelControlChangedData{Snapshot: snapshot})
}

func (s *Session) modelControlDesiredMatchesLocked(fence modelSwitchFence, lifecycle appwire.ModelControlLifecycle) bool {
	desired := s.modelControl.Desired
	return desired != nil &&
		desired.RequestSequence == fence.pickSeq &&
		desired.ClientMutationID == fence.clientMutationID &&
		desired.Lifecycle == lifecycle
}

func (s *Session) modelControlFenceAuthorityMatchesLocked(fence modelSwitchFence) bool {
	if fence.authority.InstanceID == "" && fence.authority.BootGeneration == "" {
		return true
	}
	return fence.authority.InstanceID == s.id && fence.authority.BootGeneration == s.bootGeneration
}

// modelControlPickSupersededLocked reports whether a switch must drop at its
// commit because a newer pick already committed a swap.
//
// Only a committed swap supersedes a pick. A newer pick that has merely been
// issued or queued does not: the boundary re-runs its own history check, and a
// newer pick that fails there must not take an older choice down with it. This
// is the same rule popNewestPendingModelLocked applies, so a slow older pick
// still lands when the newer one is rejected. A fence with no issue stamp
// carries no ordering and is never superseded. Callers hold s.mu.
func (s *Session) modelControlPickSupersededLocked(fence modelSwitchFence) bool {
	return fence.pickSeq != 0 && fence.pickSeq < s.modelPickAccepted
}

func (s *Session) settleModelControlValidationFailure(fence modelSwitchFence, validationErr error) (appwire.ThreadModelSetResponse, error) {
	s.modelControlPublishMu.Lock()
	defer s.modelControlPublishMu.Unlock()
	s.mu.Lock()
	if !s.modelControlFenceAuthorityMatchesLocked(fence) ||
		!fence.deferred && fence.pickSeq != 0 && fence.pickSeq != s.modelControlRequestSequence {
		response := s.modelControlResponseLocked(appwire.ModelControlDispositionSuperseded)
		s.mu.Unlock()
		return response, nil
	}
	if !fence.deferred {
		s.mu.Unlock()
		return appwire.ThreadModelSetResponse{}, validationErr
	}
	if !s.modelControlDesiredMatchesLocked(fence, appwire.ModelControlLifecyclePending) {
		response := s.modelControlResponseLocked(appwire.ModelControlDispositionSuperseded)
		s.mu.Unlock()
		return response, nil
	}
	s.modelControl.Revision++
	s.modelControl.Desired.Lifecycle = appwire.ModelControlLifecycleRejected
	snapshot := cloneModelControlSnapshot(s.modelControl)
	s.mu.Unlock()
	s.emitModelControlChanged(snapshot)
	return appwire.ThreadModelSetResponse{}, validationErr
}

func normalizedModelControlTarget(profile *provider.Profile, request ModelControlRequest) (string, string) {
	if profile != nil {
		return profile.ID(), profile.Model()
	}
	return strings.TrimSpace(request.ModelProvider), strings.TrimSpace(request.Model)
}
