package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"primeradiant.com/serf/agent/internal/installid"
	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/llm"
)

// selectStrategy creates the appropriate ContextStrategy from config.
func selectStrategy(cfg SessionConfig, cm *contextManager, sess *Session) (ContextStrategy, error) {
	if cfg.ContextStrategyOverride != nil {
		return cfg.ContextStrategyOverride, nil
	}
	switch cfg.ContextStrategy {
	case "", "compact":
		return newCompactStrategy(cm), nil
	case "recall":
		return newRecallStrategy(cm, sess), nil
	case "session-log":
		return newSessionLogStrategy(cm, sess)
	case "ooda":
		return newOODAStrategy(cm, sess)
	case "obs-mask":
		return newObsMaskStrategy(cm), nil
	case "checkpoint-pred":
		return newCheckpointPredStrategy(cm), nil
	case "memory-crystals":
		return newMemoryCrystalsStrategy(cm), nil
	case "recursive-distill":
		return newRecursiveDistillStrategy(cm), nil
	default:
		return nil, fmt.Errorf("unknown context strategy: %q", cfg.ContextStrategy)
	}
}

// NewSession creates a new Session from the given client, provider profile,
// execution environment, and config. It validates the inputs, initializes the
// environment, applies config defaults, performs the shared session-state setup
// (system prompt, skills, tools, MCP), populates default agent tasks for
// non-interactive root sessions, creates the transcript writer when state
// persistence is enabled, installs the configured context strategy, and emits
// the initial SessionStart envelope. It returns an error if any input is nil or
// if initialization fails.
func NewSession(client *llm.Client, profile ProviderProfile, env ExecutionEnvironment, cfg SessionConfig) (*Session, error) {
	if client == nil {
		return nil, fmt.Errorf("llm client is nil")
	}
	if profile == nil {
		return nil, fmt.Errorf("profile is nil")
	}
	if env == nil {
		return nil, fmt.Errorf("execution environment is nil")
	}
	if err := env.Initialize(); err != nil {
		return nil, fmt.Errorf("env initialize: %w", err)
	}
	profile = resolveLiveModelProfileWithTimeout(client, profile)
	cfg.applyDefaults()
	// Let the provider profile override the generic default command timeout.
	if profileTimeout := profile.DefaultCommandTimeoutMS(); profileTimeout > 0 && cfg.DefaultCommandTimeoutMS == 10_000 {
		cfg.DefaultCommandTimeoutMS = profileTimeout
	}

	sessCtx, sessCancel := context.WithCancel(context.Background())
	s := &Session{
		id:             ulid.Make().String(),
		cfg:            cfg,
		client:         client,
		profile:        profile,
		resolveProfile: cfg.ResolveProfile,
		depth:          cfg.spawn.depth,
		env:            env,
		stateDir:       cfg.StateDir,
		installID:      installid.LoadOrCreateInstallationID(cfg.StateDir),
		state:          SessionIdle,
		events:         make(chan SessionEvent, 256),
		history:        []Turn{},
		readFiles:      map[string]bool{},
		sessionCtx:     sessCtx,
		cancelFunc:     sessCancel,
	}
	s.subagents = newSubagentManager(s.emit)

	promptSources, err := s.initSessionState(cfg.SessionStartKind)
	if err != nil {
		return nil, err
	}

	// Populate default tasks from agent definition (non-interactive/eval mode only).
	agentName := cfg.AgentName
	if agentName == "" {
		agentName = defaultAgentName
	}
	if cfg.NonInteractive && cfg.spawn.parentSessionID == "" {
		// Root sessions only. Subagent tasks are populated in spawnAgent
		// where parentTasks from the coordinator's task_list parameter are
		// available. Populating here with nil parentTasks would leave the
		// insert:parent_tasks placeholder unexpanded, and the later
		// PopulateFromTemplates call in spawnAgent would be a no-op
		// (tasks already exist).
		if agent, ok := s.pluginAgents[agentName]; ok && len(agent.Tasks) > 0 {
			store := s.getOrCreateTaskStore()
			if err := store.PopulateFromTemplates(agent.Tasks, nil); err == nil {
				// Inject first task prompt and set reasoning effort.
				if current, ok := store.CurrentInProgress(); ok {
					if current.ReasoningEffort != "" {
						s.cfg.ReasoningEffort = current.ReasoningEffort
					}
					s.Steer(formatCurrentTaskSteering(current))
				}
			}
		}
	}

	// Create transcript writer if state persistence is enabled.
	if s.stateDir != "" {
		var agentTasks []Task
		if s.taskStore != nil {
			agentTasks = s.taskStore.View()
		}
		hdr := TranscriptHeader{
			SessionID:        s.id,
			ParentSessionID:  cfg.spawn.parentSessionID,
			ParentToolCallID: cfg.spawn.parentToolCallID,
			Task:             cfg.spawn.subagentTask,
			CreatedAt:        time.Now().UTC(),
			ProfileID:        profile.ID(),
			Model:            profile.Model(),
			WorkingDir:       s.envInfo.WorkingDir,
			Depth:            cfg.spawn.depth,
			BuildVersion:     buildinfo.Version(),
			SystemPrompt:     s.cachedSystemPrompt,
			AgentTasks:       agentTasks,
		}
		tpath := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
		tw, twErr := NewTranscriptWriter(tpath, hdr)
		if twErr != nil {
			s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript create failed: %v", twErr)})
		}
		if tw != nil {
			tw.SyncInterval = 1 * time.Second
		}
		s.transcript = tw
	}

	applyThresholdScale(s.contextMgr, cfg.CompactionThresholdScale)
	s.contextMgr.OnCompactionTurn = s.handleCompactionTurn

	// Create context strategy.
	strat, err := selectStrategy(cfg, s.contextMgr, s)
	if err != nil {
		return nil, err
	}
	s.strategy = strat

	// Register any tools provided by the context strategy.
	if s.strategy != nil {
		for _, tool := range s.strategy.Tools() {
			if err := s.reg.Register(tool); err != nil {
				return nil, fmt.Errorf("register strategy tool: %w", err)
			}
		}
	}

	s.emitSessionStartEnvelope(SessionStartData{
		Profile:           profile.ID(),
		Model:             profile.Model(),
		ContextWindowSize: profile.ContextWindowSize(),
	}, promptSources)
	// Write the initial meta.json so the hub's past index can discover this
	// session immediately (without waiting for the first completed turn).
	s.maybeAutoSave()
	return s, nil
}

// RestoreSession creates a Session from a saved snapshot, restoring the
// conversation history while reconstructing non-serializable parts (tools,
// client, profile) fresh. The session retains the original snapshot ID.
func RestoreSession(client *llm.Client, profile ProviderProfile, env ExecutionEnvironment, snap SessionSnapshot, stateDir string) (*Session, error) {
	cfg := snap.Config
	cfg.StateDir = stateDir
	cfg.SessionStartKind = SessionStartKindResume
	cfg.applyDefaults()

	if client == nil {
		return nil, fmt.Errorf("llm client is nil")
	}
	if profile == nil {
		return nil, fmt.Errorf("profile is nil")
	}
	if env == nil {
		return nil, fmt.Errorf("execution environment is nil")
	}
	if err := env.Initialize(); err != nil {
		return nil, fmt.Errorf("env initialize: %w", err)
	}
	profile = resolveLiveModelProfileWithTimeout(client, profile)

	// Try transcript-based resume first, fall back to snapshot history.
	var resumeHistory []Turn
	if stateDir != "" {
		tpath := filepath.Join(stateDir, sessionsSubdir, snap.ID+".transcript.jsonl")
		_, entries, _, readErr := readTranscript(tpath)
		if readErr == nil && len(entries) > 0 {
			resumeHistory = ResumeHistory(entries)
		}
	}
	if resumeHistory == nil {
		resumeHistory = append([]Turn{}, snap.History...)
	}

	sessCtx, sessCancel := context.WithCancel(context.Background())
	s := &Session{
		id:             snap.ID,
		cfg:            cfg,
		client:         client,
		profile:        profile,
		depth:          cfg.spawn.depth,
		env:            env,
		stateDir:       cfg.StateDir,
		installID:      installid.LoadOrCreateInstallationID(cfg.StateDir),
		state:          SessionIdle,
		events:         make(chan SessionEvent, 256),
		history:        resumeHistory,
		modelResponses: snap.TurnCount,
		readFiles:      map[string]bool{},
		sessionCtx:     sessCtx,
		cancelFunc:     sessCancel,
	}
	s.subagents = newSubagentManager(s.emit)

	promptSources, err := s.initSessionState(cfg.SessionStartKind)
	if err != nil {
		return nil, err
	}

	// Seed the context manager with the snapshot's token count so pressure
	// estimation is accurate on the very first turn after resume.
	if snap.LastInputTokens > 0 && s.contextMgr != nil {
		s.contextMgr.RecordInputTokens(snap.LastInputTokens, len(s.history))
	}

	// Open or create transcript for appending.
	if s.stateDir != "" {
		tpath := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
		tw, twErr := OpenTranscriptWriter(tpath)
		if twErr != nil {
			// Transcript might not exist (old session). Create new.
			var agentTasks []Task
			if s.taskStore != nil {
				agentTasks = s.taskStore.View()
			}
			hdr := TranscriptHeader{
				SessionID:        s.id,
				ParentSessionID:  cfg.spawn.parentSessionID,
				ParentToolCallID: cfg.spawn.parentToolCallID,
				Task:             cfg.spawn.subagentTask,
				CreatedAt:        snap.CreatedAt,
				ProfileID:        profile.ID(),
				Model:            profile.Model(),
				WorkingDir:       s.envInfo.WorkingDir,
				Depth:            cfg.spawn.depth,
				BuildVersion:     buildinfo.Version(),
				SystemPrompt:     s.cachedSystemPrompt,
				AgentTasks:       agentTasks,
			}
			tw, twErr = NewTranscriptWriter(tpath, hdr)
			if twErr != nil {
				s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript open failed: %v", twErr)})
			}
		}
		if tw != nil {
			tw.SyncInterval = 1 * time.Second
		}
		s.transcript = tw
	}

	applyThresholdScale(s.contextMgr, cfg.CompactionThresholdScale)
	s.contextMgr.OnCompactionTurn = s.handleCompactionTurn

	// Create context strategy.
	strat, err := selectStrategy(cfg, s.contextMgr, s)
	if err != nil {
		return nil, err
	}
	s.strategy = strat

	// Register any tools provided by the context strategy.
	if s.strategy != nil {
		for _, tool := range s.strategy.Tools() {
			if err := s.reg.Register(tool); err != nil {
				return nil, fmt.Errorf("register strategy tool: %w", err)
			}
		}
	}

	s.emitSessionStartEnvelope(SessionStartData{
		Profile:           profile.ID(),
		Model:             profile.Model(),
		Restored:          true,
		Turns:             s.modelResponses,
		LastInputTokens:   snap.LastInputTokens,
		ContextWindowSize: profile.ContextWindowSize(),
	}, promptSources)
	return s, nil
}

// RestoreSessionFromMeta creates a Session from a SessionMeta, recovering
// history exclusively from the transcript JSONL. If no transcript exists,
// the session starts with empty history (no snapshot fallback).
func RestoreSessionFromMeta(client *llm.Client, profile ProviderProfile, env ExecutionEnvironment, meta SessionMeta, stateDir string) (*Session, error) {
	cfg := meta.Config
	cfg.StateDir = stateDir
	cfg.SessionStartKind = SessionStartKindResume
	cfg.applyDefaults()

	if client == nil {
		return nil, fmt.Errorf("llm client is nil")
	}
	if profile == nil {
		return nil, fmt.Errorf("profile is nil")
	}
	if env == nil {
		return nil, fmt.Errorf("execution environment is nil")
	}
	if err := env.Initialize(); err != nil {
		return nil, fmt.Errorf("env initialize: %w", err)
	}
	profile = resolveLiveModelProfileWithTimeout(client, profile)

	// Recover history from transcript JSONL. No snapshot fallback.
	var resumeHistory []Turn
	if stateDir != "" {
		tpath := filepath.Join(stateDir, sessionsSubdir, meta.ID+".transcript.jsonl")
		_, entries, _, readErr := readTranscript(tpath)
		if readErr == nil && len(entries) > 0 {
			resumeHistory = ResumeHistory(entries)
		}
	}
	if resumeHistory == nil {
		resumeHistory = []Turn{}
	}

	sessCtx, sessCancel := context.WithCancel(context.Background())
	s := &Session{
		id:             meta.ID,
		cfg:            cfg,
		client:         client,
		profile:        profile,
		depth:          cfg.spawn.depth,
		env:            env,
		stateDir:       cfg.StateDir,
		installID:      installid.LoadOrCreateInstallationID(cfg.StateDir),
		state:          SessionIdle,
		events:         make(chan SessionEvent, 256),
		history:        resumeHistory,
		modelResponses: meta.TurnCount,
		forkParentID:   meta.ParentSessionID,
		forkDivergence: meta.DivergenceTurn,
		forkLabel:      meta.ForkLabel,
		name:           meta.Name,
		nameSource:     meta.NameSource,
		nameUpdated:    meta.NameUpdatedAt,
		nameSet:        strings.TrimSpace(meta.Name) != "",
		readFiles:      map[string]bool{},
		sessionCtx:     sessCtx,
		cancelFunc:     sessCancel,
	}
	s.subagents = newSubagentManager(s.emit)

	promptSources, err := s.initSessionState(cfg.SessionStartKind)
	if err != nil {
		return nil, err
	}

	// Seed context manager with the meta's token count.
	if meta.LastInputTokens > 0 && s.contextMgr != nil {
		s.contextMgr.RecordInputTokens(meta.LastInputTokens, len(s.history))
	}

	// Open or create transcript for appending.
	if s.stateDir != "" {
		tpath := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
		tw, twErr := OpenTranscriptWriter(tpath)
		if twErr != nil {
			// Transcript might not exist (new session from meta). Create new.
			var agentTasks []Task
			if s.taskStore != nil {
				agentTasks = s.taskStore.View()
			}
			hdr := TranscriptHeader{
				SessionID:        s.id,
				ParentSessionID:  cfg.spawn.parentSessionID,
				ParentToolCallID: cfg.spawn.parentToolCallID,
				Task:             cfg.spawn.subagentTask,
				CreatedAt:        meta.CreatedAt,
				ProfileID:        profile.ID(),
				Model:            profile.Model(),
				WorkingDir:       s.envInfo.WorkingDir,
				Depth:            cfg.spawn.depth,
				BuildVersion:     buildinfo.Version(),
				SystemPrompt:     s.cachedSystemPrompt,
				AgentTasks:       agentTasks,
			}
			tw, twErr = NewTranscriptWriter(tpath, hdr)
			if twErr != nil {
				s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript open failed: %v", twErr)})
			}
		}
		if tw != nil {
			tw.SyncInterval = 1 * time.Second
		}
		s.transcript = tw
	}

	applyThresholdScale(s.contextMgr, cfg.CompactionThresholdScale)
	s.contextMgr.OnCompactionTurn = s.handleCompactionTurn

	// Create context strategy.
	strat, err := selectStrategy(cfg, s.contextMgr, s)
	if err != nil {
		return nil, err
	}
	s.strategy = strat

	if s.strategy != nil {
		for _, tool := range s.strategy.Tools() {
			if err := s.reg.Register(tool); err != nil {
				return nil, fmt.Errorf("register strategy tool: %w", err)
			}
		}
	}

	s.emitSessionStartEnvelope(SessionStartData{
		Profile:           profile.ID(),
		Model:             profile.Model(),
		Restored:          true,
		Turns:             s.modelResponses,
		LastInputTokens:   meta.LastInputTokens,
		ContextWindowSize: profile.ContextWindowSize(),
	}, promptSources)
	return s, nil
}

// initSessionState performs the shared initialization steps for both
// NewSession and RestoreSession: environment snapshot, system prompt
// resolution, skills discovery, tool registry setup, and MCP connection.
// The Session struct fields (client, profile, env, cfg) must already be set.
// Returns the prompt sources so the caller can emit events after SessionStart.
func (s *Session) initSessionState(sessionStartKind SessionStartKind) ([]promptSource, error) {
	ei := envInfoFromEnv(s.env)
	ei.KnowledgeCutoff = s.profile.KnowledgeCutoff()
	if inRepo, branch, mod, untracked, commits := snapshotGit(s.env, ei.WorkingDir); inRepo {
		ei.IsGitRepo = true
		ei.GitBranch = branch
		ei.GitModifiedFiles = mod
		ei.GitUntrackedFiles = untracked
		ei.GitRecentCommitTitles = commits
		ei.GitOriginURL = gitOriginURL(s.env, ei.WorkingDir)
	}
	s.envInfo = ei

	// Load the core built-in agents first. Configured plugin agents are merged in
	// during plugin initialization below.
	builtins, err := builtinAgents()
	if err != nil {
		return nil, fmt.Errorf("loading built-in agents: %w", err)
	}
	s.pluginAgents = builtins

	if s.cfg.SystemPromptFile != "" && s.depth == 0 {
		b, err := os.ReadFile(s.cfg.SystemPromptFile)
		if err != nil {
			return nil, fmt.Errorf("reading system prompt override %s: %w", s.cfg.SystemPromptFile, err)
		}
		s.systemPromptOverride = string(b)
	}

	// Extract embedded skills to a temp dir as the base layer.
	// Filesystem-discovered skills (project + extraDirs) shadow embedded ones.
	s.skills = make(map[string]SkillMeta)
	if dir, err := extractEmbeddedSkills(); err == nil {
		s.embeddedSkillsDir = dir
		// Scan extracted dir directly (skill subdirs are immediate children).
		scanSkillsDir(dir, s.skills)
	}
	for name, meta := range DiscoverSkills(s.env, s.cfg.SkillsDirs...) {
		s.skills[name] = meta // filesystem shadows embedded
	}

	// Initialize plugins (skills, agents, hooks). Plugin agents override builtins.
	if err := s.initPlugins(sessionStartKind); err != nil {
		return nil, fmt.Errorf("plugin initialization: %w", err)
	}
	s.applyAgentRolePromptOverride()

	if err := s.validateModelFallbacks(); err != nil {
		return nil, err
	}

	s.contextMgr = newContextManager(s.profile, s.client)
	s.contextMgr.ResultToolName = s.resultToolName()

	reg := newProfileToolRegistry(s.profile)
	if err := registerCoreTools(reg, s); err != nil {
		return nil, err
	}
	if len(s.cfg.ToolOutputLimits) > 0 {
		reg.mu.Lock()
		for name, lim := range s.cfg.ToolOutputLimits {
			t, ok := reg.tools[name]
			if !ok {
				continue
			}
			if lim.MaxChars > 0 {
				t.Limit.MaxChars = lim.MaxChars
			}
			if lim.MaxLines > 0 {
				t.Limit.MaxLines = lim.MaxLines
			}
			if lim.Strategy != "" {
				t.Limit.Strategy = lim.Strategy
			}
			reg.tools[name] = t
		}
		reg.mu.Unlock()
	}
	s.reg = reg

	s.coreToolNames = reg.RegisteredNames()

	if err := s.initMCP(); err != nil {
		return nil, fmt.Errorf("MCP initialization: %w", err)
	}
	if len(s.cfg.spawn.allowedToolNames) > 0 {
		allowed := make(map[string]bool, len(s.cfg.spawn.allowedToolNames))
		for _, name := range s.cfg.spawn.allowedToolNames {
			allowed[name] = true
		}
		s.reg.RestrictKeepingResultTool(allowed, s.resultToolName())
	}
	for _, name := range s.cfg.spawn.deniedToolNames {
		s.reg.Remove(name)
	}
	if s.depth > 0 {
		for _, name := range rootOnlyAgentManagementTools {
			s.reg.Remove(name)
		}
	}

	// Cache project docs once; reused every round for system prompt rebuilds.
	s.projectDocs, s.projectDocsTruncated = LoadProjectDocs(s.env, s.profile.ProjectDocFiles()...)

	// Cache tool definitions and the rendered prompt.
	s.rebuildToolDefsCache()
	s.refreshSystemPromptCache()

	return s.promptSourceLog, nil
}

func (s *Session) validateModelFallbacks() error {
	for _, fbModel := range s.cfg.ModelFallbacks {
		// Always check whether the ref is a cross-provider switch by inspecting
		// decidePrefixAction, regardless of whether a resolver is present.
		// Cross-provider fallbacks are unsupported because the prompt/tool
		// surfaces differ between providers.
		if parts := strings.SplitN(fbModel, "/", 2); len(parts) == 2 {
			provider := strings.ToLower(parts[0])
			if decidePrefixAction(s.profile.BehaviorTag(), s.profile.ID(), provider) == prefixActionSwitch {
				// Resolve to get the target provider name for the error message.
				targetTag := provider // best-effort for the error message
				fbProfile, crossProvider, err := s.resolveProfileForRef(s.profile, fbModel)
				if err != nil {
					return fmt.Errorf("model_fallbacks entry %q: %w", fbModel, err)
				}
				if crossProvider {
					targetTag = fbProfile.BehaviorTag()
				}
				return fmt.Errorf("model_fallbacks entry %q switches provider from %q to %q; cross-provider fallbacks are not supported because provider prompt/tool surfaces differ", fbModel, s.profile.BehaviorTag(), targetTag)
			}
		}
		fbProfile, _, err := s.resolveProfileForRef(s.profile, fbModel)
		if err != nil {
			return fmt.Errorf("model_fallbacks entry %q: %w", fbModel, err)
		}
		// Same-provider resolution: guard on BehaviorTag rather than ID so
		// renamed instances still pass.
		if fbProfile.BehaviorTag() != s.profile.BehaviorTag() {
			return fmt.Errorf("model_fallbacks entry %q switches provider from %q to %q; cross-provider fallbacks are not supported because provider prompt/tool surfaces differ", fbModel, s.profile.BehaviorTag(), fbProfile.BehaviorTag())
		}
	}
	return nil
}

func modelFallbackEligible(err error) bool {
	switch llm.Classify(err) {
	case llm.ErrorClassPermanent, llm.ErrorClassFallback:
		return true
	default:
		return false
	}
}

func (s *Session) applyAgentRolePromptOverride() {
	if strings.TrimSpace(s.cfg.spawn.rolePromptOverride) != "" {
		return
	}
	agentName := strings.TrimSpace(s.cfg.AgentName)
	if agentName == "" {
		return
	}
	agent, ok := s.pluginAgents[agentName]
	if !ok || agent.PluginName == "builtin" {
		return
	}
	if prompt := strings.TrimSpace(agent.SystemPrompt); prompt != "" {
		s.cfg.spawn.rolePromptOverride = prompt
	}
}

// initPlugins loads configured plugin directories, merging their skills,
// agents, and hooks into the session. Fires SessionStart hooks after setup.
func (s *Session) initPlugins(sessionStartKind SessionStartKind) error {
	if len(s.cfg.PluginDirs) == 0 {
		return nil
	}

	plugins, err := LoadPlugins(s.cfg.PluginDirs)
	if err != nil {
		return err
	}

	s.plugins = plugins

	runner := newHookRunner(clientAdapter{s.client}, s.profile.Model())
	allAgents := map[string]PluginAgent{}

	for _, p := range plugins {
		for name, meta := range p.Skills {
			s.skills[name] = meta
		}
		for rawKey, agent := range p.Agents {
			allAgents[exposedAgentCatalogKey(p, rawKey, agent)] = agent
		}
		for event, eventHooks := range p.Hooks {
			runner.Add(event, eventHooks...)
		}

		s.pluginMCPConfigs = append(s.pluginMCPConfigs, p.MCPConfigs...)

		s.pendingPluginEvents = append(s.pendingPluginEvents, PluginLoadedData{
			Name:       p.Manifest.Name,
			Dir:        p.Dir,
			SkillCount: len(p.Skills),
			AgentCount: len(p.Agents),
			MCPCount:   len(p.MCPConfigs),
		})
	}

	runner.SetEventCallback(func(kind EventKind, data any) {
		s.emit(kind, data)
	})
	s.hookRunner = runner
	// Merge plugin agents on top of built-in agents (plugin agents win on conflict).
	for name, agent := range allAgents {
		s.pluginAgents[name] = agent
	}

	// Fire SessionStart hooks
	result := s.hookRunner.RunSessionStartFor(context.Background(), s.hookInput(HookSessionStart), sessionStartKind)
	for _, msg := range result.SystemMessages {
		s.Steer(msg)
	}

	return nil
}

// initMCP discovers and connects to MCP servers if configured.
// Uses a 30-second timeout since NewSession doesn't take a context.
func (s *Session) initMCP() error {
	configs, err := DiscoverMCPConfigs(s.env, s.cfg.MCPConfigFiles, s.cfg.MCPInline)
	if err != nil {
		return err
	}
	// Merge plugin MCP configs as a base layer (global/project/CLI can shadow them).
	if len(s.pluginMCPConfigs) > 0 {
		configs = MergeMCPConfigs(s.pluginMCPConfigs, configs)
	}
	if len(configs) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr, err := newMCPManager(ctx, configs, nil)
	if err != nil {
		return err
	}

	if err := mgr.RegisterTools(s.reg); err != nil {
		mgr.Close()
		return err
	}

	s.mcpMgr = mgr
	s.mcpTools = mgr.ToolDefinitions()
	return nil
}
