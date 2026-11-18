package agent

import (
	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/commitprovenance"
)

func (s *Session) setPromptEnvironmentOrigin(header transcript.Header) {
	if s.envTracker != nil && s.env != nil {
		cwd := s.envTracker.State().Last.Cwd
		if cwd == "" {
			cwd = header.WorkingDir
		}
		if cwd != "" && cwd != s.env.WorkingDirectory() {
			s.worktreeContextPending = true
		}
	}
	s.promptProvider, s.promptModel, s.promptBuild = header.ProfileID, header.Model, header.BuildVersion
	if s.promptProvider == "" || s.promptModel == "" {
		s.promptProvider, s.promptModel = s.profile.ID(), s.profile.Model()
	}
	if s.promptBuild == "" {
		s.promptBuild = BuildVersion
	}
	if s.cfg.CommitProvenance != "" && header.SessionID != "" {
		// Resume may override the saved setting. The original prompt survives
		// subsequent config checkpoints and identifies the directive it contains.
		hadTrailer := commitprovenance.PromptHasTrailer(header.SystemPrompt, s.promptBuild, s.promptProvider+"/"+s.promptModel, header.SessionID)
		s.promptProvenanceOverridden = hadTrailer != (s.commitTrailerPrompt() != "")
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
			if last.SessionID != "" || last.Model != "" || last.CommitTrailer != "" || last.OmitCommitTrailer {
				s.inheritedEnvironmentIdentity = true
				return
			}
		}
	}
}

// rehydrateEnvironmentContext reconstructs only the observations visible to
// the next request. Both restore and a winning fold supply their retained history.
func (s *Session) rehydrateEnvironmentContext(history []schema.Turn) {
	s.envTracker = envctx.NewTracker(envctx.State{})
	s.envContextState = nil
	var retained bool
	for _, turn := range history {
		if turn.Kind == schema.TurnEnvironment && s.envTracker.ReplayBlock(turn.Message.Text()) {
			retained = true
		}
	}
	if retained {
		state := s.envTracker.State()
		s.envContextState = &state
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
	overrideTrailer := fork || s.promptProvenanceOverridden || last.CommitTrailer != "" || last.OmitCommitTrailer
	if overrideTrailer || changedModel || (s.promptBuild != "" && s.promptBuild != BuildVersion) {
		snap.CommitTrailer = s.commitTrailerPrompt()
		snap.OmitCommitTrailer = overrideTrailer && snap.CommitTrailer == ""
	}
}

// A fork, a model switch, or a fold can leave the current attribution
// absent from the history a request will see. The caller holds the session
// and transcript locks so the check refers to the same history snapshot.
func (s *Session) needsEnvironmentIdentityLocked() bool {
	if s.envTracker == nil || s.envCollector == nil || s.closingOrClosedLocked() {
		return false
	}
	if s.worktreeContextPending {
		return true
	}
	last := s.envTracker.State().Last
	next := last
	s.setEnvironmentIdentity(&next, last)
	return next != last
}
