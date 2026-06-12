package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/contextmgr"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/internal/installid"
	"primeradiant.com/serf/agent/internal/mcp"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// BuildVersion is recorded in each session's metadata (SessionMeta.BuildVersion).
// It defaults to "dev"; an embedding application sets it to its build version at
// startup (the serf binaries set it from the linker-stamped build info). Kept as
// a package-level setting because it is a per-process constant — the same value
// for every session in a run — mirroring openai.ClientVersion in the llm module.
var BuildVersion = "dev"

// selectStrategy creates the appropriate contextmgr.Strategy from config.
func selectStrategy(cfg SessionConfig, cm *contextmgr.Manager, sess *Session) (contextmgr.Strategy, error) {
	if cfg.testOnly.contextStrategyOverride != nil {
		return cfg.testOnly.contextStrategyOverride, nil
	}
	host := &ctxHost{sess}
	switch cfg.ContextStrategy {
	case "", "compact":
		return contextmgr.NewCompactStrategy(cm), nil
	case "recall":
		// "recall" is retained as an alias for "compact" so existing configs
		// keep working; the recall tool itself was superseded by the
		// read_session_transcript / find_session_transcripts tools.
		return contextmgr.NewCompactStrategy(cm), nil
	case "session-log":
		return contextmgr.NewSessionLogStrategy(cm, host)
	case "ooda":
		return contextmgr.NewOODAStrategy(cm, host)
	case "obs-mask":
		return contextmgr.NewObsMaskStrategy(cm), nil
	case "checkpoint-pred":
		return contextmgr.NewCheckpointPredStrategy(cm), nil
	case "memory-crystals":
		return contextmgr.NewMemoryCrystalsStrategy(cm), nil
	case "recursive-distill":
		return contextmgr.NewRecursiveDistillStrategy(cm), nil
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
func NewSession(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg SessionConfig) (*Session, error) {
	if client == nil {
		return nil, errors.New("llm client is nil")
	}
	if profile == nil {
		return nil, errors.New("profile is nil")
	}
	if env == nil {
		return nil, errors.New("execution environment is nil")
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
		events:         make(chan events.SessionEvent, 256),
		history:        []schema.Turn{},
		readFiles:      map[string]bool{},
		sessionCtx:     sessCtx,
		cancelFunc:     sessCancel,
	}
	s.subagents = newSubagentManager(s.emit)
	jm, err := newJobManager(s.stateDir, s.id, s.enqueueJobNotificationAndNotify)
	if err != nil {
		return nil, fmt.Errorf("job manager: %w", err)
	}
	closeJobManagerOnError := true
	defer func() {
		if closeJobManagerOnError {
			_ = jm.closeStoreOnly()
		}
	}()
	jm.forward = cfg.spawn.forwardJobEvent
	jm.parentJobID = cfg.spawn.parentJobID
	jm.send = s.sendDelegateMessage
	jm.wake = s.notify
	jm.emit = s.emit
	s.jobManager = jm

	promptSources, err := s.initSessionState(cfg.SessionStartKind, true)
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
		var agentTasks []task.Task
		if s.taskStore != nil {
			agentTasks = s.taskStore.View()
		}
		hdr := transcript.Header{
			SessionID:        s.id,
			ParentSessionID:  cfg.spawn.parentSessionID,
			ParentToolCallID: cfg.spawn.parentToolCallID,
			Task:             cfg.spawn.subagentTask,
			CreatedAt:        time.Now().UTC(),
			ProfileID:        profile.ID(),
			Model:            profile.Model(),
			WorkingDir:       s.envInfo.WorkingDir,
			Depth:            cfg.spawn.depth,
			BuildVersion:     BuildVersion,
			SystemPrompt:     s.cachedSystemPrompt,
			AgentTasks:       agentTasks,
		}
		tpath := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
		tw, twErr := transcript.NewWriter(tpath, hdr)
		if twErr != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript create failed: %v", twErr)})
		}
		if tw != nil {
			tw.SyncInterval = 1 * time.Second
		}
		s.transcript = tw
	}

	contextmgr.ApplyThresholdScale(s.contextMgr, cfg.testOnly.compactionThresholdScale)
	s.contextMgr.OnCompactionTurn = s.handleCompactionTurn

	// Create context strategy.
	strategy, err := selectStrategy(cfg, s.contextMgr, s)
	if err != nil {
		return nil, err
	}
	s.strategy = strategy

	// Register any tools provided by the context strategy.
	if s.strategy != nil {
		for _, tool := range s.strategy.Tools() {
			if err := s.reg.Register(tool); err != nil {
				return nil, fmt.Errorf("register strategy tool: %w", err)
			}
		}
	}

	s.emitSessionStartEnvelope(events.SessionStartData{
		Profile:           profile.ID(),
		Model:             profile.Model(),
		ContextWindowSize: profile.ContextWindowSize(),
	}, promptSources)
	// Write the initial meta.json so the hub's past index can discover this
	// session immediately (without waiting for the first completed turn).
	s.maybeAutoSave()
	closeJobManagerOnError = false
	return s, nil
}

// RestoreSessionConfig carries runtime-only settings needed when restoring a
// persisted session. Persisted fields still come from SessionMeta.Config; this
// struct layers non-serialized values such as StateDir and ResolveProfile.
type RestoreSessionConfig struct {
	StateDir                string
	ResolveProfile          func(ref string) (*provider.Profile, error)
	ModelFallbacks          []string
	LLMRetryPolicy          *llm.RetryPolicy
	LLMSleep                llm.SleepFunc
	spawn                   spawnConfig
	resumeHistory           []schema.Turn
	deferRestoreSideEffects bool
}

// RestoreSessionFromMeta creates a Session from a SessionMeta, recovering
// history exclusively from the transcript JSONL. If no transcript exists,
// the session starts with empty history (no snapshot fallback).
func RestoreSessionFromMeta(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, meta schema.SessionMeta, stateDir string) (*Session, error) {
	return RestoreSessionFromMetaWithConfig(client, profile, env, meta, RestoreSessionConfig{StateDir: stateDir})
}

// RestoreSessionFromMetaWithConfig is RestoreSessionFromMeta with explicit
// runtime-only restore configuration.
func RestoreSessionFromMetaWithConfig(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, meta schema.SessionMeta, restoreCfg RestoreSessionConfig) (*Session, error) {
	cfg := configFromSnapshot(meta.Config)
	cfg.StateDir = restoreCfg.StateDir
	cfg.ResolveProfile = restoreCfg.ResolveProfile
	if restoreCfg.spawn.parentSessionID != "" {
		cfg.spawn = restoreCfg.spawn
	}
	if restoreCfg.ModelFallbacks != nil {
		cfg.ModelFallbacks = append([]string(nil), restoreCfg.ModelFallbacks...)
	}
	cfg.LLMRetryPolicy = restoreCfg.LLMRetryPolicy
	cfg.LLMSleep = restoreCfg.LLMSleep
	cfg.SessionStartKind = plugin.SessionStartKindResume
	cfg.applyDefaults()

	if client == nil {
		return nil, errors.New("llm client is nil")
	}
	if profile == nil {
		return nil, errors.New("profile is nil")
	}
	if env == nil {
		return nil, errors.New("execution environment is nil")
	}
	if err := env.Initialize(); err != nil {
		return nil, fmt.Errorf("env initialize: %w", err)
	}
	profile = resolveLiveModelProfileWithTimeout(client, profile)

	// Recover history from transcript JSONL. No snapshot fallback.
	var resumeHistory []schema.Turn
	if restoreCfg.resumeHistory != nil {
		resumeHistory = append([]schema.Turn(nil), restoreCfg.resumeHistory...)
	} else if cfg.StateDir != "" {
		tpath := filepath.Join(cfg.StateDir, sessionsSubdir, meta.ID+".transcript.jsonl")
		_, entries, _, readErr := readTranscript(tpath)
		if readErr == nil && len(entries) > 0 {
			resumeHistory = ResumeHistory(entries)
		}
	}
	if resumeHistory == nil {
		resumeHistory = []schema.Turn{}
	}

	sessCtx, sessCancel := context.WithCancel(context.Background())
	s := &Session{
		id:             meta.ID,
		cfg:            cfg,
		client:         client,
		profile:        profile,
		resolveProfile: cfg.ResolveProfile,
		depth:          cfg.spawn.depth,
		env:            env,
		stateDir:       cfg.StateDir,
		installID:      installid.LoadOrCreateInstallationID(cfg.StateDir),
		state:          SessionIdle,
		events:         make(chan events.SessionEvent, 256),
		history:        resumeHistory,
		modelResponses: meta.TurnCount,
		fork: forkInfo{
			parentID:   meta.ParentSessionID,
			divergence: meta.DivergenceTurn,
			label:      meta.ForkLabel,
		},
		naming: sessionName{
			value:   meta.Name,
			source:  meta.NameSource,
			updated: meta.NameUpdatedAt,
			set:     strings.TrimSpace(meta.Name) != "",
		},
		readFiles:  map[string]bool{},
		sessionCtx: sessCtx,
		cancelFunc: sessCancel,
	}
	s.subagents = newSubagentManager(s.emit)
	jm, err := newJobManager(s.stateDir, s.id, nil)
	if err != nil {
		return nil, fmt.Errorf("job manager: %w", err)
	}
	closeJobManagerOnError := true
	defer func() {
		if closeJobManagerOnError {
			_ = jm.closeStoreOnly()
		}
	}()
	jm.forward = cfg.spawn.forwardJobEvent
	jm.parentJobID = cfg.spawn.parentJobID
	jm.enqueue = s.enqueueJobNotificationAndNotify
	jm.send = s.sendDelegateMessage
	jm.wake = s.notify
	jm.emit = s.emit
	s.jobManager = jm
	if !restoreCfg.deferRestoreSideEffects {
		if err := jm.reconcileLostJobs(); err != nil {
			return nil, fmt.Errorf("job reconcile: %w", err)
		}
		if err := jm.recoverForwardedTerminalEvents(); err != nil {
			return nil, fmt.Errorf("nested job recovery: %w", err)
		}
	}

	// Restore persisted goal state before initSessionState so the goal store is
	// populated before any turn runs. No kick is wired yet ("loaded but idle"):
	// the goal resumes on the user's next turn via the normal gate path.
	//
	// Only an ACTIVE goal is reloaded. A goal that already finished is dropped:
	// terminal transitions are now persisted (/par A4), and re-restoring a
	// complete/blocked goal would re-emit its terminal report on the first gate call
	// (the once-gate resets on load) and leave a stale terminal status chip (/par #2).
	if meta.Goal != nil && meta.Goal.Status == string(goal.StatusActive) {
		g := meta.Goal
		s.getOrCreateGoalStore().Restore(g.Objective, g.Status, g.StopReason, g.Iterations, g.NoProgressStreak, g.MadeProgressOnce, g.CreatedAt, g.UpdatedAt)
	}

	promptSources, err := s.initSessionState(cfg.SessionStartKind, !restoreCfg.deferRestoreSideEffects)
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
		tw, twErr := transcript.OpenWriter(tpath)
		if twErr != nil {
			// Transcript might not exist (new session from meta). Create new.
			var agentTasks []task.Task
			if s.taskStore != nil {
				agentTasks = s.taskStore.View()
			}
			hdr := transcript.Header{
				SessionID:        s.id,
				ParentSessionID:  cfg.spawn.parentSessionID,
				ParentToolCallID: cfg.spawn.parentToolCallID,
				Task:             cfg.spawn.subagentTask,
				CreatedAt:        meta.CreatedAt,
				ProfileID:        profile.ID(),
				Model:            profile.Model(),
				WorkingDir:       s.envInfo.WorkingDir,
				Depth:            cfg.spawn.depth,
				BuildVersion:     BuildVersion,
				SystemPrompt:     s.cachedSystemPrompt,
				AgentTasks:       agentTasks,
			}
			tw, twErr = transcript.NewWriter(tpath, hdr)
			if twErr != nil {
				s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript open failed: %v", twErr)})
			}
		}
		if tw != nil {
			tw.SyncInterval = 1 * time.Second
		}
		s.transcript = tw
	}

	if !restoreCfg.deferRestoreSideEffects {
		if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
			return nil, fmt.Errorf("watch send retry: %w", err)
		}
		if err := jm.armPendingTerminalNotifications(); err != nil {
			return nil, fmt.Errorf("job notifications: %w", err)
		}
		if err := jm.recoverForwardedPendingNotifications(); err != nil {
			return nil, fmt.Errorf("nested job notifications: %w", err)
		}
	}

	contextmgr.ApplyThresholdScale(s.contextMgr, cfg.testOnly.compactionThresholdScale)
	s.contextMgr.OnCompactionTurn = s.handleCompactionTurn

	// Create context strategy.
	strategy, err := selectStrategy(cfg, s.contextMgr, s)
	if err != nil {
		return nil, err
	}
	s.strategy = strategy

	if s.strategy != nil {
		for _, tool := range s.strategy.Tools() {
			if err := s.reg.Register(tool); err != nil {
				return nil, fmt.Errorf("register strategy tool: %w", err)
			}
		}
	}

	s.emitSessionStartEnvelope(events.SessionStartData{
		Profile:           profile.ID(),
		Model:             profile.Model(),
		Restored:          true,
		Turns:             s.modelResponses,
		LastInputTokens:   meta.LastInputTokens,
		ContextWindowSize: profile.ContextWindowSize(),
	}, promptSources)
	closeJobManagerOnError = false
	return s, nil
}

// initSessionState performs the shared initialization steps for both
// NewSession and RestoreSession: environment snapshot, system prompt
// resolution, skills discovery, tool registry setup, and MCP connection.
// The Session struct fields (client, profile, env, cfg) must already be set.
// Returns the prompt sources so the caller can emit events after SessionStart.
func (s *Session) initSessionState(sessionStartKind plugin.SessionStartKind, runSessionStartHooks bool) ([]promptSource, error) {
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
	s.skills = make(map[string]skill.SkillMeta)
	if dir, err := skill.ExtractEmbeddedSkills(); err == nil {
		s.embeddedSkillsDir = dir
		// Scan extracted dir directly (skill subdirs are immediate children).
		skill.ScanSkillsDir(dir, s.skills)
	}
	for name, meta := range skill.DiscoverSkills(s.env, s.cfg.SkillsDirs...) {
		s.skills[name] = meta // filesystem shadows embedded
	}

	// Initialize plugins (skills, agents, hooks). Plugin agents override builtins.
	if err := s.initPlugins(sessionStartKind, runSessionStartHooks); err != nil {
		return nil, fmt.Errorf("plugin initialization: %w", err)
	}
	s.applyAgentRolePromptOverride()

	if err := s.validateModelFallbacks(); err != nil {
		return nil, err
	}

	s.contextMgr = contextmgr.NewManager(s.profile, s.client)
	s.contextMgr.ResultToolName = s.resultToolName()

	reg := newProfileToolRegistry(s.profile)
	if err := registerCoreTools(reg, s); err != nil {
		return nil, err
	}
	reg.OverrideLimits(s.cfg.ToolOutputLimits)
	enforceShellToolJSONLimit(reg)
	enforceJobToolJSONLimits(reg)
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
		for _, name := range rootOnlySubagentTools() {
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
		// Always check whether the ref is a cross-provider switch, regardless of
		// whether a resolver is present. Cross-provider fallbacks are unsupported
		// because the prompt/tool surfaces differ between providers.
		if parts := strings.SplitN(fbModel, "/", 2); len(parts) == 2 {
			if s.profile.CrossProviderRef(fbModel) {
				// Resolve to get the target provider name for the error message.
				targetTag := strings.ToLower(parts[0]) // best-effort for the error message
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
// agents, and hooks into the session. Fires SessionStart hooks after setup when requested.
func (s *Session) initPlugins(sessionStartKind plugin.SessionStartKind, runSessionStartHooks bool) error {
	if len(s.cfg.PluginDirs) == 0 {
		return nil
	}

	plugins, err := plugin.LoadAll(s.cfg.PluginDirs)
	if err != nil {
		return err
	}

	s.plugins = plugins

	runner := hooks.NewRunner(s.client, s.profile.Model())
	allAgents := map[string]plugin.Agent{}

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

		// Accumulate recognized-but-unsupported events for /status diagnostics and
		// queue a loud warning per event: a plugin author who declares a hook for a
		// reserved event serf does not fire yet gets a visible signal, not silence.
		unsupported := make([]string, 0, len(p.UnsupportedHooks))
		for event := range p.UnsupportedHooks {
			if s.unsupportedPluginHookEvents == nil {
				s.unsupportedPluginHookEvents = make(map[plugin.HookEvent]bool)
			}
			s.unsupportedPluginHookEvents[event] = true
			unsupported = append(unsupported, string(event))
		}
		sort.Strings(unsupported)
		for _, event := range unsupported {
			s.pendingHookWarnings = append(s.pendingHookWarnings, events.WarningData{
				Source:     "hooks",
				Title:      "unsupported hook event",
				Message:    unsupportedHookEventWarning(p.Manifest.Name, event),
				PluginName: p.Manifest.Name,
				EventName:  event,
			})
		}

		// Queue a loud warning per UNKNOWN event name: not a recognized Claude or
		// serf event (likely a typo), so the hook will never fire. This is the
		// headline diagnostic — an unknown event must never fail silently.
		unknown := make([]string, 0, len(p.UnknownHooks))
		for event := range p.UnknownHooks {
			unknown = append(unknown, event)
		}
		sort.Strings(unknown)
		for _, event := range unknown {
			s.pendingHookWarnings = append(s.pendingHookWarnings, events.WarningData{
				Source:     "hooks",
				Title:      "unknown hook event",
				Message:    unknownHookEventWarning(p.Manifest.Name, event),
				PluginName: p.Manifest.Name,
				EventName:  event,
			})
		}

		// Queue a loud warning per handler whose type is reserved/unsupported
		// (http/mcp_tool/agent/other — anything but command/prompt). The parser
		// keeps these handlers in p.Hooks, but runHook's dispatch skips them
		// silently; without this they would be a silent no-op. Done once at load
		// (not per dispatch) and via the diagnostic warning path, so it never
		// fires the Notification hook.
		s.pendingHookWarnings = append(s.pendingHookWarnings, unsupportedHandlerTypeWarnings(p)...)

		s.pluginMCPConfigs = append(s.pluginMCPConfigs, p.MCPConfigs...)

		s.pendingPluginEvents = append(s.pendingPluginEvents, events.PluginLoadedData{
			Name:       p.Manifest.Name,
			Dir:        p.Dir,
			SkillCount: len(p.Skills),
			AgentCount: len(p.Agents),
			MCPCount:   len(p.MCPConfigs),
		})
	}

	// Validate every registered matcher ONCE at load time. An invalid-regex
	// matcher gets a single loud diagnostic here; MatchHooks then skips it
	// silently at dispatch (no per-tool-call storm, and no dispatch-time
	// EventWarning that would recurse through the Notification hook).
	for _, d := range runner.Validate() {
		s.pendingHookWarnings = append(s.pendingHookWarnings, events.WarningData{
			Source:     "hooks",
			Title:      "invalid hook matcher",
			Message:    d.Message,
			PluginName: d.PluginName,
			EventName:  d.Event,
		})
	}

	// The runner callback carries hook lifecycle events (HookStart/HookEnd) and
	// any genuine warnings raised during hook execution; route them through emit.
	runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		s.emit(kind, data)
	})
	s.hookRunner = runner
	// Merge plugin agents on top of built-in agents (plugin agents win on conflict).
	for name, agent := range allAgents {
		s.pluginAgents[name] = agent
	}

	if !runSessionStartHooks {
		return nil
	}
	s.runSessionStartHooks(sessionStartKind)
	return nil
}

func (s *Session) runSessionStartHooks(sessionStartKind plugin.SessionStartKind) {
	if s == nil || s.hookRunner == nil {
		return
	}
	result := s.hookRunner.RunSessionStartFor(context.Background(), s.hookInput(plugin.HookSessionStart), sessionStartKind)
	for _, m := range result.ModelContext {
		s.deliverHookContext(m)
	}
	for _, m := range result.UserMessages {
		s.deliverHookUserMessage(m)
	}
}

func (s *Session) runDeferredRestoreSideEffects() error {
	if s == nil || s.jobManager == nil {
		return nil
	}
	if err := s.jobManager.reconcileLostJobs(); err != nil {
		return fmt.Errorf("job reconcile: %w", err)
	}
	if err := s.jobManager.recoverForwardedTerminalEvents(); err != nil {
		return fmt.Errorf("nested job recovery: %w", err)
	}
	if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
		return fmt.Errorf("watch send retry: %w", err)
	}
	if err := s.jobManager.armPendingTerminalNotifications(); err != nil {
		return fmt.Errorf("job notifications: %w", err)
	}
	if err := s.jobManager.recoverForwardedPendingNotifications(); err != nil {
		return fmt.Errorf("nested job notifications: %w", err)
	}
	s.runSessionStartHooks(s.cfg.SessionStartKind)
	return nil
}

// unknownHookEventWarning builds the load-time warning text for a hook declared
// under an event name that is neither a serf event nor a recognized Claude
// event — almost always a typo. It names the plugin and the offending event
// only; no hook payload or secret is included.
func unknownHookEventWarning(pluginName, event string) string {
	return fmt.Sprintf(
		"plugin %q declares a hook for %q, which is not a recognized Claude or serf hook event (likely a typo); this hook will never fire",
		pluginName, event)
}

// unsupportedHookEventWarning builds the load-time warning text for a hook
// declared under a recognized Claude event that serf does not yet fire
// (reserved-placeholder). It names the plugin and the reserved event only.
func unsupportedHookEventWarning(pluginName, event string) string {
	return fmt.Sprintf(
		"plugin %q declares a hook for the reserved event %q, which serf does not yet fire; this hook will not run",
		pluginName, event)
}

// supportedHookHandlerTypes is the set of handler "type" values serf actually
// executes. Everything else (http, mcp_tool, agent, …) is reserved and skipped.
// Must stay in sync with the dispatch-side hooks.supportedHandlerTypes / runHook
// type switch; both list exactly "command" and "prompt".
var supportedHookHandlerTypes = map[string]bool{
	"command": true,
	"prompt":  true,
}

// unsupportedHandlerTypeWarnings builds one diagnostic per registered hook whose
// handler type is reserved/unsupported (not command or prompt), in deterministic
// event/group/handler order. These handlers parse and survive in p.Hooks but are
// skipped silently at dispatch; the warning names the plugin, event, and type so
// the no-op is visible. It carries names/reasons only — no hook payload.
func unsupportedHandlerTypeWarnings(p plugin.Instance) []events.WarningData {
	eventNames := make([]string, 0, len(p.Hooks))
	for event := range p.Hooks {
		eventNames = append(eventNames, string(event))
	}
	sort.Strings(eventNames)

	var out []events.WarningData
	for _, event := range eventNames {
		for _, h := range p.Hooks[plugin.HookEvent(event)] {
			if supportedHookHandlerTypes[h.Type] {
				continue
			}
			out = append(out, events.WarningData{
				Source:     "hooks",
				Title:      "unsupported hook handler type",
				Message:    unsupportedHandlerTypeWarning(p.Manifest.Name, event, h.Type),
				PluginName: p.Manifest.Name,
				EventName:  event,
			})
		}
	}
	return out
}

// unsupportedHandlerTypeWarning builds the load-time warning text for a hook
// handler whose type serf does not execute (http/mcp_tool/agent/other). It names
// the plugin, event, and the reserved type only; no hook payload or secret.
func unsupportedHandlerTypeWarning(pluginName, event, handlerType string) string {
	shown := handlerType
	if shown == "" {
		shown = "(empty)"
	}
	return fmt.Sprintf(
		"plugin %q declares a %q-type handler for %s, which is a reserved/unsupported handler type (serf runs only \"command\" and \"prompt\"); this handler will not run",
		pluginName, shown, event)
}

// initMCP discovers and connects to MCP servers if configured.
// Uses a 30-second timeout since NewSession doesn't take a context.
func (s *Session) initMCP() error {
	configs, err := mcpconfig.Discover(s.env, s.cfg.MCPConfigFiles, s.cfg.MCPInline)
	if err != nil {
		return err
	}
	// Merge plugin MCP configs as a base layer (global/project/CLI can shadow them).
	if len(s.pluginMCPConfigs) > 0 {
		configs = mcpconfig.Merge(s.pluginMCPConfigs, configs)
	}
	if len(configs) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr, err := mcp.NewManager(ctx, configs, nil)
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
