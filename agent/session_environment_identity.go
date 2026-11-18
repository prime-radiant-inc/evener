package agent

import (
	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

func (s *Session) setPromptEnvironmentOrigin(header transcript.Header) {
	s.promptProvider, s.promptModel, s.promptBuild = header.ProfileID, header.Model, header.BuildVersion
	if s.promptProvider == "" || s.promptModel == "" {
		s.promptProvider, s.promptModel = s.profile.ID(), s.profile.Model()
	}
	if s.promptBuild == "" {
		s.promptBuild = BuildVersion
	}
	if s.fork.parentID == "" || s.fork.divergence == 0 {
		return
	}
	if !s.isSubagentSession() {
		s.inheritedEnvironmentIdentity = true
		return
	}
	// A context-fork delegate has its own startup prompt. Only a model-facing
	// identity update in the copied conversation can supersede that prompt.
	var tracker envctx.Tracker
	for _, turn := range s.history {
		if turn.Kind == schema.TurnEnvironment && turn.Authorship != nil && turn.Authorship.Inherited && tracker.ReplayBlock(turn.Message.Text()) {
			last := tracker.State().Last
			if last.SessionID != "" || last.Model != "" || last.CommitTrailer != "" {
				s.inheritedEnvironmentIdentity = true
				return
			}
		}
	}
}

// setEnvironmentIdentity supplies only facts that need to supersede the launch
// prompt or an earlier environment block. The session lock protects the profile.
func (s *Session) setEnvironmentIdentity(snap *envctx.Snapshot, last envctx.Snapshot) {
	if s.profile == nil {
		return
	}
	fork := s.inheritedEnvironmentIdentity
	changedModel := s.promptModel != "" && (s.profile.ID() != s.promptProvider || s.profile.Model() != s.promptModel)
	if fork {
		snap.SessionID = s.id
	}
	if fork || changedModel || last.Model != "" {
		snap.Model = s.profile.ID() + "/" + s.profile.Model()
		snap.KnowledgeCutoff = s.profile.KnowledgeCutoff()
	}
	if fork || changedModel || last.CommitTrailer != "" || (s.promptBuild != "" && s.promptBuild != BuildVersion) {
		snap.CommitTrailer = s.commitTrailerPrompt()
	}
}

// A quota retry or a resumed fork can issue a request without accepting a new
// user input. Publish the same durable context before that request takes its
// history snapshot. Unchanged rounds need no environment probes or writes.
func (s *Session) appendChangedEnvironmentIdentity() error {
	s.mu.RLock()
	if s.envTracker == nil {
		s.mu.RUnlock()
		return nil
	}
	last := s.envTracker.State().Last
	next := last
	s.setEnvironmentIdentity(&next, last)
	s.mu.RUnlock()
	if next == last {
		return nil
	}
	return s.maybeAppendEnvironmentContext()
}
