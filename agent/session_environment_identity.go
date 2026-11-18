package agent

import (
	"cmp"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
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
	if header.SessionID != "" {
		model := s.promptProvider + "/" + s.promptModel
		saved := commitprovenance.TrailerInPrompt(header.SystemPrompt, s.promptBuild, model, header.SessionID)
		if saved != "" || s.cfg.CommitProvenance != "" {
			// Compare the hostname and mode at the same launch identity. A
			// subsequent model switch can return to this model before dispatch.
			s.promptProvenanceOverridden = saved != s.commitTrailerFor(s.promptBuild, model)
		}
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
	// The startup prompt survives every fold. Retained environment updates
	// supersede its facts, including an explicitly cleared path or branch.
	baseline := envctx.State{
		Last:                          envctx.Snapshot{Cwd: s.promptWorkingDir},
		GitBranchUnknown:              s.cachedSystemPrompt != "" && s.promptLaunch.GitBranch == nil,
		ReportUnconfirmedLaunchBranch: s.promptLaunch.GitBranchUnconfirmed,
		ScratchDirUnknown:             s.cachedSystemPrompt != "" && s.promptLaunch.ScratchDir == nil,
		WritableRootsUnknown:          s.cachedSystemPrompt != "" && s.promptLaunch.WritableRoots == nil,
	}
	if branch := s.promptLaunch.GitBranch; branch != nil {
		baseline.Last.GitBranch = *branch
	}
	if scratch := s.promptLaunch.ScratchDir; scratch != nil {
		baseline.Last.ScratchDir = *scratch
	}
	if roots := s.promptLaunch.WritableRoots; roots != nil {
		baseline.Last.WritableRoots = *roots
	}
	if cutoff := s.promptLaunch.KnowledgeCutoff; cutoff != nil {
		baseline.Last.KnowledgeCutoff = *cutoff
	}
	baseline.Last.ShellNetwork = s.promptLaunch.ShellNetwork
	baseline.Last.DetachedShell = s.promptLaunch.DetachedShell
	s.envTracker = envctx.NewTracker(baseline)
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

// The durable transcript keeps workspace observations removed by a fold. Git
// uses the latest confirmation for each checkout. A scratch directory once
// advertised still needs reopening after a temporary failure cleared its path.
func (s *Session) restoreWorkspaceObservations(entries []transcript.Entry) bool {
	scratch := s.promptLaunch.ScratchDir
	reopenScratch := (scratch == nil && s.cachedSystemPrompt != "") || (scratch != nil && *scratch != "")
	replay := func(cwd string, entries []transcript.Entry) {
		for _, entry := range entries {
			turn := entry.Turn
			if turn.Kind != schema.TurnEnvironment || turn.OriginalOrdinal != nil {
				continue
			}
			block := envctx.NewTracker(envctx.State{Last: envctx.Snapshot{Cwd: cwd}, GitBranchUnknown: true})
			if !block.ReplayBlock(turn.Message.Text()) {
				continue
			}
			state := block.State()
			reopenScratch = reopenScratch || state.Last.ScratchDir != ""
			cwd = state.Last.Cwd
			if !state.GitBranchUnknown {
				s.envCollector.RememberGitBranch(cwd, state.Last.GitBranch)
			}
		}
	}
	if s.isSubagentSession() && s.fork.divergence > 0 {
		// A context-fork delegate's own launch follows its copied prefix.
		// Only the parent's environment turns can identify the parent's checkout.
		end := min(s.fork.divergence-1, len(entries))
		replay("", entries[:end])
		entries = entries[end:]
	}
	if branch := s.promptLaunch.GitBranch; branch != nil {
		s.envCollector.RememberGitBranch(s.promptWorkingDir, *branch)
	}
	replay(s.promptWorkingDir, entries)
	return reopenScratch
}

func installedScratchDir(env execenv.ExecutionEnvironment) string {
	if local, ok := env.(*execenv.LocalExecutionEnvironment); ok && local != nil {
		return local.SessionScratchDir()
	}
	return ""
}

func installedWritableRoots(env execenv.ExecutionEnvironment) string {
	if local, ok := env.(*execenv.LocalExecutionEnvironment); ok && local != nil && local.Sandbox != nil && local.Sandbox.Enforced() {
		return writableRootsSummary(*local.Sandbox)
	}
	return ""
}

// A read-only delegate's file tools can be confined even when the host
// cannot enforce a sandbox for spawned commands.
func installedFileToolWrites(env execenv.ExecutionEnvironment) string {
	local, ok := env.(*execenv.LocalExecutionEnvironment)
	if !ok || local == nil {
		return ""
	}
	if local.Sandbox == nil || !local.Sandbox.FileToolConfined() {
		return envctx.FileWritesHost
	}
	if len(local.Sandbox.FileTool.WriteRoots) == 0 {
		return envctx.FileWritesScratch
	}
	return envctx.FileWritesSandbox
}

// The installed wrapper decides policy even when an embedding supplied it
// independently of the session config. The advertised shell owns its modes.
func (s *Session) shellCapabilities() (network, detached string) {
	network = envctx.NetworkAllowed
	if w := s.sandboxWrapper(); w != nil && !w.Policy().Network {
		network = envctx.NetworkBlocked
	}
	detached = envctx.DetachUnavailable
	if s.canInstructDetachedShell(s.env) {
		detached = envctx.DetachAvailable
	}
	return network, detached
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
		snap.ParentSessionID = s.fork.parentID
		snap.OrdinarySession = !s.cfg.Manager
	}
	if fork || last.ReportingAudience != "" || last.ResultToolName != "" {
		snap.ResultToolName = s.resultToolName()
		snap.ReportingAudience = envctx.ReportToCaller
		if s.reportsToParent() {
			snap.ReportingAudience = envctx.ReportToParent
		}
	}
	snap.ScratchDir = installedScratchDir(s.env)
	snap.WritableRoots = s.workspaceWritableRoots
	fileWrites := cmp.Or(installedFileToolWrites(s.env), last.FileToolWrites)
	if fileWrites != s.promptLaunch.FileToolWrites || last.FileToolWrites != "" {
		snap.FileToolWrites = fileWrites
	}
	snap.ShellNetwork, snap.DetachedShell = s.shellCapabilities()
	snap.KnowledgeCutoff = s.profile.KnowledgeCutoff()
	// A fold can retain a cutoff update without the model line that preceded it.
	unknownCutoff := s.promptLaunch.KnowledgeCutoff == nil && s.cachedSystemPrompt != ""
	reportCutoff := s.promptModel != "" && (unknownCutoff || last.KnowledgeCutoff != snap.KnowledgeCutoff)
	if fork || changedModel || reportCutoff || last.Model != "" {
		snap.Model = s.profile.ID() + "/" + s.profile.Model()
	}
	overrideTrailer := fork || s.promptProvenanceOverridden || last.CommitTrailer != "" || last.OmitCommitTrailer
	if overrideTrailer || changedModel || (s.promptBuild != "" && s.promptBuild != BuildVersion) {
		snap.CommitTrailer = s.commitTrailerPrompt()
		snap.OmitCommitTrailer = overrideTrailer && snap.CommitTrailer == ""
	}
	if launch := s.promptLaunch.TurnEndsProcess; launch == nil || *launch != s.cfg.TurnEndsProcess || last.ExecutionMode != "" {
		snap.ExecutionMode = envctx.DaemonMode
		if s.cfg.TurnEndsProcess {
			snap.ExecutionMode = envctx.OneShotMode
		}
	}
	canAsk := s.canInstructTool(askUserToolName)
	if launch := s.promptLaunch.HasAskUser; launch == nil || *launch != canAsk || last.AskUserState != "" {
		snap.AskUserState = envctx.QuestionsUnavailable
		if canAsk {
			snap.AskUserState = envctx.QuestionsAvailable
		}
	}
	acceptsEndReason := s.resultAcceptsEndReason()
	if launch := s.promptLaunch.HasEndReason; launch == nil || *launch != acceptsEndReason || last.ResultEndReason != "" {
		snap.ResultEndReason = envctx.OmitEndReason
		if acceptsEndReason {
			snap.ResultEndReason = envctx.EndReasonAvailable
		}
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
	state := s.envTracker.State()
	if state.ScratchDirUnknown || state.WritableRootsUnknown {
		return true
	}
	last := state.Last
	next := last
	s.setEnvironmentIdentity(&next, last)
	return next != last
}
