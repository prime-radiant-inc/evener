package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/llm"
)

// ctxKey is a private type for context keys in this package.
type ctxKey string

// ctxToolCallID carries the tool call ID into tool execution closures via context.
const ctxToolCallID ctxKey = "toolCallID"

// SessionState represents the current lifecycle state of a session.
type SessionState string

const (
	SessionIdle          SessionState = "IDLE"
	SessionProcessing    SessionState = "PROCESSING"
	SessionAwaitingInput SessionState = "AWAITING_INPUT"
	SessionClosed        SessionState = "CLOSED"
)

func nonInteractiveGuidance(resultToolName string) string {
	return fmt.Sprintf(`

## Non-interactive mode — CRITICAL

You are running in a non-interactive, headless environment. There is no human available to
answer questions, provide clarification, or confirm your approach. Nobody will ever respond
to you. Any attempt to ask a question or wait for confirmation wastes your limited rounds.

RULES (these override ANY skill instructions that conflict):
- NEVER use %s to ask a question or request confirmation.
- The ONLY valid use of %s is to deliver FINAL work output.
- The task prompt IS the complete specification. Read it carefully, then BUILD.
- If a skill says "ask your human partner", "confirm with user", or "explore user intent":
  make those judgment calls yourself. You are both the implementer and the decision-maker.
- The brainstorming skill's "explore user intent" step means carefully re-reading the spec
  and extracting every requirement — NOT asking questions.
- Start coding within your first 3 tool calls. Read the spec, read relevant files, then write code.
- Focus on: read spec → plan internally → test → implement → verify → deliver.
`, resultToolName, resultToolName)
}

type SessionConfig struct {
	MaxToolRoundsPerInput   int `json:"max_tool_rounds_per_input,omitempty"`
	MaxTurns                int `json:"max_turns,omitempty"`
	DefaultCommandTimeoutMS int `json:"default_command_timeout_ms,omitempty"`
	MaxCommandTimeoutMS     int `json:"max_command_timeout_ms,omitempty"`
	MaxSubagentDepth        int `json:"max_subagent_depth,omitempty"`

	// ToolOutputLimits overrides default per-tool truncation behavior.
	ToolOutputLimits map[string]ToolOutputLimit `json:"tool_output_limits,omitempty"`

	// UserInstructionOverride is appended to the end of the system prompt (highest priority).
	UserInstructionOverride string `json:"user_instruction_override,omitempty"`

	// BasePromptOverride replaces the profile's base prompt entirely.
	// Used by subagents to replace the full prompt resolution with core + persona.
	BasePromptOverride string `json:"base_prompt_override,omitempty"`

	// AgentName selects a persona for prompt composition. When set, the persona's
	// SystemPrompt is appended after core.md instead of the default coordinator.
	// Looked up from built-in agents, then plugin agents.
	AgentName string `json:"agent_name,omitempty"`

	// ReasoningEffort is passed through to the Unified LLM request when non-empty.
	// Valid values are provider-dependent but typically include: low|medium|high.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// SkillsDirs are extra directories to scan for skills (each is treated
	// as a directory whose subdirectories contain SKILL.md files).
	SkillsDirs []string `json:"skills_dirs,omitempty"`

	// MCPConfigFiles are paths to .mcp.json files (--mcp-config flag).
	MCPConfigFiles []string `json:"mcp_config_files,omitempty"`

	// MCPInline are inline MCP server specs (--mcp flag, format: name:command args...).
	MCPInline []string `json:"mcp_inline,omitempty"`

	// PluginDirs are directories to scan for plugins (each contains a plugin.yaml manifest).
	PluginDirs []string `json:"plugin_dirs,omitempty"`

	// SystemPromptFile overrides the embedded system prompt with the contents of this file.
	// Highest priority in the prompt resolution chain (CLI --system-prompt flag).
	SystemPromptFile string `json:"system_prompt_file,omitempty"`

	// SystemPromptAppend are file paths whose contents are appended to the system prompt.
	// Always applied, even when SystemPromptFile is set (CLI --system-prompt-append flag).
	SystemPromptAppend []string `json:"system_prompt_append,omitempty"`

	// NoProjectPrompts suppresses loading .serf/prompts/ from the project directory.
	// Useful for A/B testing to match Docker container behavior (no project prompts).
	NoProjectPrompts bool `json:"no_project_prompts,omitempty"`

	// NonInteractive indicates no human is available for questions or confirmation.
	// The task prompt is the complete specification; the agent must make all decisions
	// autonomously. Appends guidance to the system prompt adapting skill behavior.
	NonInteractive bool `json:"non_interactive,omitempty"`

	// ContextStrategy selects the context management strategy: compact|recall|session-log|ooda.
	ContextStrategy string `json:"context_strategy,omitempty"`

	// MinResultRound sets the minimum tool round before submit_result
	// is accepted. Before this round, the tool returns an error asking the
	// model to verify its solution. 0 means no minimum (accept immediately).
	MinResultRound int `json:"min_result_round,omitempty"`

	// EnableReviewerGate, when true, spawns a reviewer subagent at depth 0
	// to validate the result before accepting it. At depth > 0 (subagents),
	// submit_result always passes through directly.
	EnableReviewerGate bool `json:"enable_reviewer_gate,omitempty"`

	// ResultToolName overrides the name of the result tool.
	// When set, all internal references use this name instead of "communicate".
	// Used for A/B testing tool names. Empty means "communicate".
	ResultToolName string `json:"result_tool_name,omitempty"`

	EnableLoopDetection *bool `json:"enable_loop_detection,omitempty"`
	LoopDetectionWindow int   `json:"loop_detection_window,omitempty"`

	// LLMRetryPolicy controls retries for retryable Unified LLM errors (429, 5xx, etc).
	// Nil means use llm.DefaultRetryPolicy().
	LLMRetryPolicy *llm.RetryPolicy `json:"-"`
	LLMSleep       llm.SleepFunc    `json:"-"`

	// ContextStrategyOverride, when non-nil, is used instead of creating
	// a strategy from the ContextStrategy string. For testing.
	ContextStrategyOverride ContextStrategy `json:"-"`

	// CompactionThresholdScale multiplies all compaction thresholds by this
	// factor. 1.0 = defaults, 0.1 = trigger at 10% of normal pressure.
	// Used for evaluation testing. 0 means use defaults.
	CompactionThresholdScale float64 `json:"compaction_threshold_scale,omitempty"`

	// ParentSessionID links sub-agent sessions to their parent (set by spawnAgent).
	ParentSessionID string `json:"-"`

	// ParentToolCallID is the tool call ID that spawned this sub-agent session.
	ParentToolCallID string `json:"-"`

	// SubagentTask is the task description passed to spawn_agent.
	SubagentTask string `json:"-"`

	// Depth is the sub-agent nesting depth (0 for root sessions).
	Depth int `json:"-"`

	// StateDir, when non-empty, enables incremental session persistence.
	// Snapshots are written to <StateDir>/sessions/ and tasks to <StateDir>/tasks/.
	StateDir string `json:"-"`

	// ExportATIFPath, when non-empty, causes Session.Close to export an ATIF v1.6
	// trajectory JSON file to this path. Only root sessions (Depth==0) export.
	ExportATIFPath string `json:"-"`
}

func (c *SessionConfig) applyDefaults() {
	if c.MaxToolRoundsPerInput == 0 {
		c.MaxToolRoundsPerInput = 200
	}
	if c.DefaultCommandTimeoutMS <= 0 {
		c.DefaultCommandTimeoutMS = 10_000
	}
	if c.MaxCommandTimeoutMS <= 0 {
		c.MaxCommandTimeoutMS = 600_000
	}
	if c.MaxSubagentDepth <= 0 {
		c.MaxSubagentDepth = 1
	}
	if c.EnableLoopDetection == nil {
		v := true
		c.EnableLoopDetection = &v
	}
	if c.LoopDetectionWindow <= 0 {
		c.LoopDetectionWindow = 10
	}
}

type Session struct {
	id       string
	cfg      SessionConfig
	client   *llm.Client
	profile  ProviderProfile
	env      ExecutionEnvironment
	stateDir string

	events  chan SessionEvent
	envInfo EnvironmentInfo

	mu      sync.Mutex
	state   SessionState
	turns   int
	history []Turn

	reg *ToolRegistry

	steeringQueue []string
	followups     []string

	// submit_result tool state (transient, reset each processOneInput call)
	resultDelivered bool
	resultText      string
	currentRound    int // updated each tool loop iteration for MinResultRound gate

	// subagents
	depth     int
	subagents map[string]*subagent

	// context management
	contextMgr *ContextManager
	strategy   ContextStrategy

	// skills discovered at session startup
	skills            map[string]SkillMeta
	embeddedSkillsDir string // temp dir for extracted embedded skills; cleaned up in Close

	// MCP server connections
	mcpMgr   *MCPManager
	mcpTools []llm.ToolDefinition

	// Plugin-provided components
	plugins          []LoadedPlugin
	hookRunner       *HookRunner
	pluginAgents     map[string]PluginAgent
	pluginMCPConfigs []MCPServerConfig

	// Tool names registered during session initialization (not custom).
	coreToolNames map[string]bool

	// Project docs loaded once at session init and cached for lifetime.
	projectDocs          []ProjectDoc
	projectDocsTruncated bool

	// read-before-write guardrail
	readFiles map[string]bool
	readFilesMu sync.RWMutex

	// SESSION_END deduplication: emitted exactly once across ProcessInput and Close.
	sessionEndEmitted bool

	// closeOnce ensures Close() body runs exactly once.
	closeOnce sync.Once

	// Session-level cancel: Close() cancels in-flight LLM calls.
	sessionCtx context.Context
	cancelFunc context.CancelFunc

	// task list (lazy-init)
	taskStore     *TaskStore
	taskStoreOnce sync.Once

	// task reminder tracking
	taskToolLastRound int  // totalRounds value at last task_list tool call
	taskToolEverUsed  bool // whether task_list has ever been called
	taskNudgeFired    bool // whether the "consider using task_list" nudge has fired
	totalRounds       int  // cumulative tool rounds across all inputs

	// stuck detection
	loopDetectionCount int // how many times loop detection has fired

	// transcript writer (nil when StateDir is empty)
	transcript *TranscriptWriter

	// Cached tool definitions (Issue 1: avoid rebuilding every round).
	// cachedToolDefs is the full list; cachedToolDefsNoResult excludes the result tool.
	cachedToolDefs         []llm.ToolDefinition
	cachedToolDefsNoResult []llm.ToolDefinition

	// Cached system prompt components (Issue 3: avoid recomputing every round).
	cachedSkillList                []SkillMeta
	cachedExtraTools               string
	cachedAgentSection             string
	cachedNonInteractiveGuidance   string
}

// selectStrategy creates the appropriate ContextStrategy from config.
func selectStrategy(cfg SessionConfig, cm *ContextManager, sess *Session) (ContextStrategy, error) {
	if cfg.ContextStrategyOverride != nil {
		return cfg.ContextStrategyOverride, nil
	}
	switch cfg.ContextStrategy {
	case "", "compact":
		return NewCompactStrategy(cm), nil
	case "recall":
		return NewRecallStrategy(cm, sess), nil
	case "session-log":
		return NewSessionLogStrategy(cm, sess)
	case "ooda":
		return NewOODAStrategy(cm, sess)
	case "obs-mask":
		return NewObsMaskStrategy(cm), nil
	case "checkpoint-pred":
		return NewCheckpointPredStrategy(cm), nil
	case "memory-crystals":
		return NewMemoryCrystalsStrategy(cm), nil
	case "recursive-distill":
		return NewRecursiveDistillStrategy(cm), nil
	default:
		return nil, fmt.Errorf("unknown context strategy: %q", cfg.ContextStrategy)
	}
}

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
	cfg.applyDefaults()
	// Let the provider profile override the generic default command timeout.
	if profileTimeout := profile.DefaultCommandTimeoutMS(); profileTimeout > 0 && cfg.DefaultCommandTimeoutMS == 10_000 {
		cfg.DefaultCommandTimeoutMS = profileTimeout
	}

	sessCtx, sessCancel := context.WithCancel(context.Background())
	s := &Session{
		id:         ulid.Make().String(),
		cfg:        cfg,
		client:     client,
		profile:    profile,
		env:        env,
		stateDir:   cfg.StateDir,
		state:      SessionIdle,
		events:     make(chan SessionEvent, 256),
		history:    []Turn{},
		subagents:  map[string]*subagent{},
		readFiles:  map[string]bool{},
		sessionCtx: sessCtx,
		cancelFunc: sessCancel,
	}

	promptSources, err := s.initSessionState()
	if err != nil {
		return nil, err
	}

	s.depth = cfg.Depth

	// Create transcript writer if state persistence is enabled.
	if s.stateDir != "" {
		hdr := TranscriptHeader{
			SessionID:        s.id,
			ParentSessionID:  cfg.ParentSessionID,
			ParentToolCallID: cfg.ParentToolCallID,
			Task:             cfg.SubagentTask,
			CreatedAt:        time.Now().UTC(),
			ProfileID:        profile.ID(),
			Model:            profile.Model(),
			WorkingDir:       s.envInfo.WorkingDir,
			Depth:            cfg.Depth,
			BuildVersion:     buildinfo.Version(),
			SystemPrompt:     s.buildInitialSystemPrompt(),
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
	s.contextMgr.OnCompactionTurn = func(t Turn) {
		if err := s.transcript.Append(t); err != nil {
			s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript compaction write: %v", err)})
		}
		// After compaction, inject full task list if tasks exist.
		if s.taskStore != nil {
			if reminder := taskReminderFull(s.taskStore); reminder != "" {
				s.Steer(reminder)
			}
		}
	}

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

	s.emit(EventSessionStart, SessionStartData{
		Profile:           profile.ID(),
		Model:             profile.Model(),
		ContextWindowSize: profile.ContextWindowSize(),
	})
	for _, src := range promptSources {
		s.emit(EventPromptLoaded, PromptLoadedData{Label: src.Label, Size: src.Size})
	}
	return s, nil
}

// RestoreSession creates a Session from a saved snapshot, restoring the
// conversation history while reconstructing non-serializable parts (tools,
// client, profile) fresh. The session retains the original snapshot ID.
func RestoreSession(client *llm.Client, profile ProviderProfile, env ExecutionEnvironment, snap SessionSnapshot, stateDir string) (*Session, error) {
	cfg := snap.Config
	cfg.StateDir = stateDir
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

	// Try transcript-based resume first, fall back to snapshot history.
	var resumeHistory []Turn
	if stateDir != "" {
		tpath := filepath.Join(stateDir, sessionsSubdir, snap.ID+".transcript.jsonl")
		_, entries, _, readErr := ReadTranscript(tpath)
		if readErr == nil && len(entries) > 0 {
			resumeHistory = ResumeHistory(entries)
		}
	}
	if resumeHistory == nil {
		resumeHistory = append([]Turn{}, snap.History...)
	}

	sessCtx, sessCancel := context.WithCancel(context.Background())
	s := &Session{
		id:         snap.ID,
		cfg:        cfg,
		client:     client,
		profile:    profile,
		env:        env,
		stateDir:   cfg.StateDir,
		state:      SessionIdle,
		events:     make(chan SessionEvent, 256),
		history:    resumeHistory,
		turns:      snap.TurnCount,
		subagents:  map[string]*subagent{},
		readFiles:  map[string]bool{},
		sessionCtx: sessCtx,
		cancelFunc: sessCancel,
	}

	promptSources, err := s.initSessionState()
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
			hdr := TranscriptHeader{
				SessionID:        s.id,
				ParentSessionID:  cfg.ParentSessionID,
				ParentToolCallID: cfg.ParentToolCallID,
				Task:             cfg.SubagentTask,
				CreatedAt:        snap.CreatedAt,
				ProfileID:        profile.ID(),
				Model:            profile.Model(),
				WorkingDir:       s.envInfo.WorkingDir,
				Depth:            cfg.Depth,
				BuildVersion:     buildinfo.Version(),
				SystemPrompt:     s.buildInitialSystemPrompt(),
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
	s.contextMgr.OnCompactionTurn = func(t Turn) {
		if err := s.transcript.Append(t); err != nil {
			s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript compaction write: %v", err)})
		}
		// After compaction, inject full task list if tasks exist.
		if s.taskStore != nil {
			if reminder := taskReminderFull(s.taskStore); reminder != "" {
				s.Steer(reminder)
			}
		}
	}

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

	s.emit(EventSessionStart, SessionStartData{
		Profile:           profile.ID(),
		Model:             profile.Model(),
		Restored:          true,
		Turns:             s.turns,
		LastInputTokens:   snap.LastInputTokens,
		ContextWindowSize: profile.ContextWindowSize(),
	})
	for _, src := range promptSources {
		s.emit(EventPromptLoaded, PromptLoadedData{Label: src.Label, Size: src.Size})
	}
	return s, nil
}

// RestoreSessionFromMeta creates a Session from a SessionMeta, recovering
// history exclusively from the transcript JSONL. If no transcript exists,
// the session starts with empty history (no snapshot fallback).
func RestoreSessionFromMeta(client *llm.Client, profile ProviderProfile, env ExecutionEnvironment, meta SessionMeta, stateDir string) (*Session, error) {
	cfg := meta.Config
	cfg.StateDir = stateDir
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

	// Recover history from transcript JSONL. No snapshot fallback.
	var resumeHistory []Turn
	if stateDir != "" {
		tpath := filepath.Join(stateDir, sessionsSubdir, meta.ID+".transcript.jsonl")
		_, entries, _, readErr := ReadTranscript(tpath)
		if readErr == nil && len(entries) > 0 {
			resumeHistory = ResumeHistory(entries)
		}
	}
	if resumeHistory == nil {
		resumeHistory = []Turn{}
	}

	sessCtx, sessCancel := context.WithCancel(context.Background())
	s := &Session{
		id:         meta.ID,
		cfg:        cfg,
		client:     client,
		profile:    profile,
		env:        env,
		stateDir:   cfg.StateDir,
		state:      SessionIdle,
		events:     make(chan SessionEvent, 256),
		history:    resumeHistory,
		turns:      meta.TurnCount,
		subagents:  map[string]*subagent{},
		readFiles:  map[string]bool{},
		sessionCtx: sessCtx,
		cancelFunc: sessCancel,
	}

	promptSources, err := s.initSessionState()
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
			hdr := TranscriptHeader{
				SessionID:        s.id,
				ParentSessionID:  cfg.ParentSessionID,
				ParentToolCallID: cfg.ParentToolCallID,
				Task:             cfg.SubagentTask,
				CreatedAt:        meta.CreatedAt,
				ProfileID:        profile.ID(),
				Model:            profile.Model(),
				WorkingDir:       s.envInfo.WorkingDir,
				Depth:            cfg.Depth,
				BuildVersion:     buildinfo.Version(),
				SystemPrompt:     s.buildInitialSystemPrompt(),
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
	s.contextMgr.OnCompactionTurn = func(t Turn) {
		if err := s.transcript.Append(t); err != nil {
			s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript compaction write: %v", err)})
		}
		if s.taskStore != nil {
			if reminder := taskReminderFull(s.taskStore); reminder != "" {
				s.Steer(reminder)
			}
		}
	}

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

	s.emit(EventSessionStart, SessionStartData{
		Profile:           profile.ID(),
		Model:             profile.Model(),
		Restored:          true,
		Turns:             s.turns,
		LastInputTokens:   meta.LastInputTokens,
		ContextWindowSize: profile.ContextWindowSize(),
	})
	for _, src := range promptSources {
		s.emit(EventPromptLoaded, PromptLoadedData{Label: src.Label, Size: src.Size})
	}
	return s, nil
}

func (s *Session) ID() string                  { return s.id }
func (s *Session) Events() <-chan SessionEvent { return s.events }

// resultToolName returns the effective name for the submit_result tool.
func (s *Session) resultToolName() string {
	if s.cfg.ResultToolName != "" {
		return s.cfg.ResultToolName
	}
	return "communicate"
}

// State returns the current session state.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Snapshot captures the current session state as a SessionSnapshot.
func (s *Session) Snapshot() SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	return SessionSnapshot{
		ID:              s.id,
		ProfileID:       s.profile.ID(),
		Model:           s.profile.Model(),
		Config:          s.cfg,
		EnvInfo:         s.envInfo,
		History:         append([]Turn{}, s.history...),
		CreatedAt:       now,
		UpdatedAt:       now,
		TurnCount:       s.turns,
		LastInputTokens: s.contextMgr.LastInputTokens(),
	}
}

// Meta returns the current session metadata without the conversation history.
func (s *Session) Meta() SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	return SessionMeta{
		ID:              s.id,
		ProfileID:       s.profile.ID(),
		Model:           s.profile.Model(),
		Config:          s.cfg,
		EnvInfo:         s.envInfo,
		CreatedAt:       now,
		UpdatedAt:       now,
		TurnCount:       s.turns,
		LastInputTokens: s.contextMgr.LastInputTokens(),
	}
}

// SetReasoningEffort updates the reasoning effort used for future LLM calls.
// Takes effect on the next request (spec).
func (s *Session) SetReasoningEffort(effort string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == SessionClosed {
		return
	}
	s.cfg.ReasoningEffort = strings.TrimSpace(effort)
}

// Compact forces context compaction regardless of current pressure.
// Runs all compaction layers (observation masking, thinking clearing,
// checkpoint, and LLM summarization). Safe to call while idle.
func (s *Session) Compact(ctx context.Context) error {
	if s.contextMgr == nil {
		return fmt.Errorf("context manager not initialized")
	}

	s.contextMgr.Meta = s.buildCompactionMeta()

	s.mu.Lock()
	histCopy := append([]Turn{}, s.history...)
	s.mu.Unlock()

	s.contextMgr.ForceCompact(ctx, &histCopy, s.emit)

	s.mu.Lock()
	s.history = histCopy
	s.mu.Unlock()

	s.maybeAutoSave()
	return nil
}

// buildCompactionMeta gathers session-level metadata for enriching compaction summaries.
func (s *Session) buildCompactionMeta() CompactionMeta {
	meta := CompactionMeta{}

	// Transcript path.
	if s.stateDir != "" {
		meta.TranscriptPath = filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
	}

	// Task list snapshot (if tasks have been used).
	if s.taskStore != nil {
		meta.TaskSnapshot = s.taskStore.View()
	}

	return meta
}

// ContextPressure returns the estimated context pressure as a fraction (0.0–1.0).
// Returns 0 if the context manager is not initialized.
func (s *Session) ContextPressure() float64 {
	if s.contextMgr == nil {
		return 0
	}
	s.mu.Lock()
	hist := append([]Turn{}, s.history...)
	s.mu.Unlock()
	return s.contextMgr.Pressure(hist, 0)
}

// SetModel changes the model used for future LLM calls.
// Takes effect on the next request.
func (s *Session) SetModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profile = s.profile.WithModel(model)
	if s.contextMgr != nil {
		s.contextMgr.SetProfile(s.profile)
	}
}

// SetTimeout changes the default command timeout for shell tool invocations.
// Takes effect on the next tool execution.
func (s *Session) SetTimeout(timeoutMS int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.DefaultCommandTimeoutMS = timeoutMS
}

// RegisterTool registers a custom tool at runtime.
func (s *Session) RegisterTool(name, description string, params map[string]any, fn func(ctx context.Context, args any) (any, error)) {
	_ = s.reg.Register(RegisteredTool{
		Tool: llm.Tool{
			Definition: llm.ToolDefinition{
				Name:        name,
				Description: description,
				Parameters:  params,
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			return fn(ctx, args)
		},
	})
	// Rebuild caches so the new tool appears in tool defs and system prompt.
	s.rebuildToolDefsCache()
	s.rebuildPromptCache()
}

// Steer queues a message to inject after the current tool round completes.
func (s *Session) Steer(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == SessionClosed {
		return
	}
	if strings.TrimSpace(msg) == "" {
		return
	}
	s.steeringQueue = append(s.steeringQueue, msg)
}

// FollowUp queues a message to process after the current input completes.
func (s *Session) FollowUp(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == SessionClosed {
		return
	}
	if strings.TrimSpace(msg) == "" {
		return
	}
	s.followups = append(s.followups, msg)
}

// ResultDelivered reports whether submit_result was called during the
// most recent ProcessInput invocation.
func (s *Session) ResultDelivered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resultDelivered
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		// Collect subagents and clear the map under the lock, then close
		// outside the lock to avoid deadlock (sub.sess.Close() acquires its own mu).
		subs := make([]*subagent, 0, len(s.subagents))
		for id, sub := range s.subagents {
			subs = append(subs, sub)
			delete(s.subagents, id)
		}
		turns := s.turns
		emitEnd := !s.sessionEndEmitted
		s.sessionEndEmitted = true
		s.mu.Unlock()

		// Spec Appendix B graceful shutdown ordering:
		// 1. Cancel in-flight LLM calls.
		if s.cancelFunc != nil {
			s.cancelFunc()
		}

		// 2-4. Kill running child processes (SIGTERM → wait 2s → SIGKILL).
		s.env.Cleanup()

		// SessionEnd hooks (best-effort, bounded timeout)
		if s.hookRunner != nil {
			hookCtx, hookCancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.hookRunner.RunSessionEnd(hookCtx, s.hookInput(HookSessionEnd))
			hookCancel()
		}

		// 5-6. Emit SESSION_END with final state.
		if emitEnd {
			s.emit(EventSessionEnd, SessionEndData{
				Reason: "session_closed",
				State:  string(SessionClosed),
				Turns:  turns,
			})
		}

		// 7. Close subagents.
		for _, sub := range subs {
			sub.sess.Close()
		}

		if s.mcpMgr != nil {
			s.mcpMgr.Close()
		}

		if s.transcript != nil {
			_ = s.transcript.Close()
		}

		// Export ATIF trajectory if configured (root session only, after transcript flush).
		if s.cfg.ExportATIFPath != "" && s.stateDir != "" && s.cfg.Depth == 0 {
			tpath := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
			if err := ExportATIF(tpath, s.cfg.ExportATIFPath); err != nil {
				s.emit(EventWarning, WarningData{Message: fmt.Sprintf("ATIF export failed: %v", err)})
			}
		}

		if s.embeddedSkillsDir != "" {
			os.RemoveAll(s.embeddedSkillsDir)
		}

		// 8. Transition to CLOSED last, then close the events channel.
		s.mu.Lock()
		s.state = SessionClosed
		s.mu.Unlock()
		close(s.events)
	})
}

// stripPromptSection removes a markdown ## section by heading from text.
// It removes from "## <heading>" (case-insensitive) through the next ## heading
// or end of string, including any trailing blank lines.
func stripPromptSection(text, heading string) string {
	lines := strings.Split(text, "\n")
	var out []string
	skipping := false
	target := strings.ToLower(heading)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			sectionName := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			if strings.EqualFold(sectionName, target) {
				skipping = true
				continue
			}
			skipping = false
		}
		if !skipping {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// reviewVerdict is the result of a reviewer subagent evaluation.
type reviewVerdict struct {
	Pass     bool
	Feedback string
}

// extractOriginalTask returns the text of the first user input in the session history.
// If compaction removed it, falls back to the SubagentTask from config.
func (s *Session) extractOriginalTask() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.history {
		if t.Kind == TurnUserInput {
			return t.Message.Text()
		}
	}
	return s.cfg.SubagentTask
}

// spawnReviewer creates a synchronous reviewer subagent at depth+1 that evaluates
// the claimed result. Returns a verdict with Pass/Fail and feedback.
func (s *Session) spawnReviewer(ctx context.Context, claimedResult string) (reviewVerdict, error) {
	// Look up the builtin reviewer agent.
	agent, ok := s.pluginAgents["reviewer"]
	if !ok {
		return reviewVerdict{Pass: true}, fmt.Errorf("builtin reviewer agent not found")
	}

	originalTask := s.extractOriginalTask()

	// Compose the reviewer prompt with task context and claimed result.
	reviewPrompt := fmt.Sprintf(
		"## Original Task\n\n%s\n\n## Claimed Result\n\n%s",
		originalTask, claimedResult,
	)

	// Create reviewer session config.
	subCfg := s.cfg
	subCfg.MCPConfigFiles = nil
	subCfg.MCPInline = nil
	subCfg.ParentSessionID = s.id
	subCfg.SubagentTask = reviewPrompt
	subCfg.Depth = s.depth + 1
	subCfg.MaxTurns = 20
	subCfg.MaxToolRoundsPerInput = 30 // reviewer shouldn't need many rounds

	// Compose system prompt: core + reviewer role.
	// Strip the ## communicate section from core so the reviewer isn't confused
	// by contradictory instructions — reviewer.md has its own verdict section.
	core := stripPromptSection(CorePrompt(), s.resultToolName())
	composed := core + "\n\n" + agent.SystemPrompt
	subCfg.BasePromptOverride = composed

	subProfile := s.profile.WithBasePrompt(composed)
	if agent.Model != "inherit" && agent.Model != "" {
		subProfile = subProfile.WithModel(agent.Model)
	}

	subSess, err := NewSession(s.client, subProfile, s.env, subCfg)
	if err != nil {
		return reviewVerdict{Pass: true}, fmt.Errorf("creating reviewer session: %w", err)
	}
	defer subSess.Close()

	// Register approve/reject tools on the reviewer session.
	// The tool name IS the verdict — no text parsing needed.
	verdict := reviewVerdict{Pass: true} // default fail-open
	verdictSet := false
	deliverResult := func(v reviewVerdict) {
		verdict = v
		verdictSet = true
		subSess.mu.Lock()
		subSess.resultDelivered = true
		subSess.resultText = v.Feedback
		subSess.mu.Unlock()
	}
	_ = subSess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "approve",
			Description: "Approve the work. Call when the agent's implementation meets the task requirements.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{"type": "string", "description": "Brief summary of what you verified"},
				},
				"required": []string{"message"},
			},
		}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			msg, _ := args["message"].(string)
			deliverResult(reviewVerdict{Pass: true, Feedback: msg})
			return map[string]any{"accepted": true}, nil
		},
	})
	_ = subSess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "reject",
			Description: "Reject the work. Call when the agent's implementation has issues that must be fixed.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"feedback": map[string]any{"type": "string", "description": "Specific issues with file paths and evidence"},
				},
				"required": []string{"feedback"},
			},
		}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			fb, _ := args["feedback"].(string)
			deliverResult(reviewVerdict{Pass: false, Feedback: fb})
			return map[string]any{"rejected": true}, nil
		},
	})

	// Restrict reviewer tools to read-only + approve/reject.
	allowed := make(map[string]bool, len(agent.Tools)+2)
	for _, t := range agent.Tools {
		allowed[t] = true
	}
	allowed["approve"] = true
	allowed["reject"] = true
	subSess.reg.RestrictKeepingResultTool(allowed, s.resultToolName())
	// Restrict auto-adds result tool; remove it so the reviewer
	// must use approve/reject (the tool name IS the verdict).
	subSess.reg.Remove(s.resultToolName())
	// Rebuild cached tool definitions after restriction/removal.
	subSess.rebuildToolDefsCache()

	// Run reviewer synchronously. If the reviewer outputs text instead of
	// calling approve/reject, nudge it to use the tools.
	const maxReviewerAttempts = 3
	for attempt := 0; attempt < maxReviewerAttempts; attempt++ {
		prompt := reviewPrompt
		if attempt > 0 {
			prompt = "You must call either the `approve` or `reject` tool to deliver your verdict. Do not write your verdict as text — only a tool call counts."
		}
		_, err = subSess.ProcessInput(ctx, prompt)
		if err != nil {
			return reviewVerdict{Pass: true}, fmt.Errorf("reviewer ProcessInput: %w", err)
		}
		if verdictSet {
			break
		}
	}

	return verdict, nil
}

func (s *Session) ProcessInput(ctx context.Context, input string) (string, error) {
	// Reset so SESSION_END can fire at the end of this input's processing.
	s.mu.Lock()
	s.sessionEndEmitted = false
	s.mu.Unlock()

	outputs := []string{}
	next := input
	for {
		out, err := s.processOneInput(ctx, next)
		if strings.TrimSpace(out) != "" {
			outputs = append(outputs, out)
		}
		if err != nil {
			// Spec: abort signal closes the session and stops the loop.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				s.Close()
			}
			return strings.Join(outputs, "\n"), err
		}
		fu := s.popFollowUp()
		if strings.TrimSpace(fu) == "" {
			s.mu.Lock()
			if !s.sessionEndEmitted {
				s.sessionEndEmitted = true
				turns := s.turns
				state := s.state
				s.mu.Unlock()
				s.emit(EventSessionEnd, SessionEndData{
					Reason: "input_complete",
					State:  string(state),
					Turns:  turns,
				})
			} else {
				s.mu.Unlock()
			}
			return strings.Join(outputs, "\n"), nil
		}
		next = fu
	}
}

func (s *Session) execTool(ctx context.Context, call llm.ToolCallData) ToolExecResult {
	// PreToolUse hooks
	if s.hookRunner != nil {
		hi := s.hookInput(HookPreToolUse)
		hi.ToolName = MapSerfToolNameToClaude(call.Name)
		if len(call.Arguments) > 0 {
			_ = json.Unmarshal(call.Arguments, &hi.ToolInput)
		}

		preResult := s.hookRunner.RunPreToolUse(ctx, hi)
		for _, msg := range preResult.SystemMessages {
			s.Steer(msg)
		}
		if preResult.Denied {
			denyMsg := "Tool call denied by hook"
			if preResult.DenyMessage != "" {
				denyMsg = preResult.DenyMessage
			}
			return ToolExecResult{
				ToolName:   call.Name,
				CallID:     call.ID,
				Output:     denyMsg,
				FullOutput: denyMsg,
				IsError:    true,
			}
		}
	}

	argsJSON, _ := json.Marshal(call.Arguments)
	startData := ToolCallStartData{
		ToolName:      call.Name,
		CallID:        call.ID,
		ArgumentsJSON: string(argsJSON),
	}
	// Promote description to top-level event field for observability (shell tool).
	var args map[string]any
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	if desc, ok := args["description"].(string); ok && desc != "" {
		startData.Description = desc
	}
	s.emit(EventToolCallStart, startData)

	// Session-level tools (subagents) are registered in the registry with closures.
	ctx = context.WithValue(ctx, ctxToolCallID, call.ID)
	toolStart := time.Now()
	res := s.reg.ExecuteCall(ctx, s.env, call)
	res.DurationMS = time.Since(toolStart).Milliseconds()

	// Emit output deltas (best-effort). Even for non-streaming tools, this gives consumers a uniform
	// incremental event pattern that mirrors provider LLM streaming.
	full := res.FullOutput
	const chunk = 4000
	for i := 0; i < len(full); i += chunk {
		j := i + chunk
		if j > len(full) {
			j = len(full)
		}
		s.emit(EventToolCallOutputDelta, ToolCallOutputDeltaData{
			ToolName: res.ToolName,
			CallID:   res.CallID,
			Delta:    full[i:j],
		})
	}

	endData := ToolCallEndData{
		ToolName: res.ToolName,
		CallID:   res.CallID,
	}
	if res.IsError {
		endData.Error = res.FullOutput
	} else {
		endData.Output = res.FullOutput
	}
	s.emit(EventToolCallEnd, endData)

	// PostToolUse hooks
	if s.hookRunner != nil {
		hi := s.hookInput(HookPostToolUse)
		hi.ToolName = MapSerfToolNameToClaude(call.Name)
		hi.ToolResult = res.FullOutput
		postResult := s.hookRunner.RunPostToolUse(ctx, hi)
		for _, msg := range postResult.SystemMessages {
			s.Steer(msg)
		}
	}

	return res
}

func (s *Session) appendTurn(kind TurnKind, m llm.Message) {
	t := NewTurn(kind, m)
	s.mu.Lock()
	s.history = append(s.history, t)
	s.mu.Unlock()
	if err := s.transcript.Append(t); err != nil {
		s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
}

// appendAssistantTurn appends an assistant turn that carries the full response
// metadata (usage stats and response ID) alongside the message content.
func (s *Session) appendAssistantTurn(resp llm.Response) {
	t := Turn{
		Kind:       TurnAssistant,
		Message:    resp.Message,
		Timestamp:  time.Now().UTC(),
		Usage:      resp.Usage,
		ResponseID: resp.ID,
	}
	s.mu.Lock()
	s.history = append(s.history, t)
	s.mu.Unlock()
	if err := s.transcript.Append(t); err != nil {
		s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
	}
}

// maybeAutoSave persists the session metadata if StateDir is configured.
// Writes only lightweight SessionMeta (~500 bytes), not the full history.
// The conversation history is already durably recorded by the transcript JSONL.
func (s *Session) maybeAutoSave() {
	if s.stateDir == "" {
		return
	}
	meta := s.Meta()
	if err := SaveSessionMeta(s.stateDir, meta); err != nil {
		s.emit(EventWarning, WarningData{
			Message: fmt.Sprintf("auto-save failed: %v", err),
		})
	}
}

func (s *Session) maybeWarnContextUsage(msgs []llm.Message) bool {
	if s == nil || s.profile == nil {
		return false
	}
	cw := s.profile.ContextWindowSize()
	if cw <= 0 {
		return false
	}

	totalChars := 0
	for _, m := range msgs {
		totalChars += messageCharCount(m)
	}
	approxTokens := float64(totalChars) / 4.0
	threshold := float64(cw) * 0.8
	if approxTokens <= threshold {
		return false
	}

	pct := int(math.Round((approxTokens / float64(cw)) * 100.0))
	msg := fmt.Sprintf("Context usage at ~%d%% of context window", pct)
	s.emit(EventWarning, WarningData{
		Message:           msg,
		ApproxTokens:      int(math.Round(approxTokens)),
		ContextWindowSize: cw,
		Percent:           pct,
	})
	return true
}

func messageCharCount(m llm.Message) int {
	n := 0
	n += len(m.Name)
	n += len(m.ToolCallID)
	for _, p := range m.Content {
		switch p.Kind {
		case llm.ContentText:
			n += len(p.Text)
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				n += len(p.ToolCall.ID)
				n += len(p.ToolCall.Name)
				n += len(p.ToolCall.Arguments)
			}
		case llm.ContentToolResult:
			if p.ToolResult != nil {
				n += len(p.ToolResult.ToolCallID)
				n += len(p.ToolResult.Name)
				switch x := p.ToolResult.Content.(type) {
				case string:
					n += len(x)
				case []byte:
					n += len(x)
				default:
					b, _ := json.Marshal(x)
					n += len(b)
				}
			}
		case llm.ContentThinking, llm.ContentRedThinking:
			if p.Thinking != nil {
				n += len(p.Thinking.Text)
				n += len(p.Thinking.Signature)
			}
		default:
			// Fallback to a best-effort JSON encoding.
			b, _ := json.Marshal(p)
			n += len(b)
		}
	}
	return n
}

func (s *Session) emit(kind EventKind, data any) {
	if s == nil || s.events == nil {
		return
	}
	ev := SessionEvent{
		Kind:      kind,
		Timestamp: time.Now().UTC(),
		SessionID: s.id,
		Data:      data,
	}
	// Close() may happen concurrently with emit (abort signal while tools run in parallel).
	// Sending on a closed channel would panic; v1 semantics are best-effort delivery.
	defer func() { _ = recover() }()
	select {
	case s.events <- ev:
	default:
		// Drop events if consumer is too slow; v1 is best-effort.
	}
}

// hookInput creates a HookInput with the session's ID and working directory pre-filled.
func (s *Session) hookInput(event HookEvent) HookInput {
	return HookInput{
		SessionID:     s.id,
		CWD:           s.env.WorkingDirectory(),
		HookEventName: string(event),
	}
}

func (s *Session) processOneInput(ctx context.Context, input string) (string, error) {
	// Derive a context that cancels when either the caller's ctx or the session ctx cancels.
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-s.sessionCtx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	defer cancel()

	s.mu.Lock()
	if s.state == SessionClosed {
		s.mu.Unlock()
		return "", fmt.Errorf("session is closed")
	}
	s.state = SessionProcessing
	s.resultDelivered = false
	s.resultText = ""
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.emit(EventError, ErrorData{Error: ctx.Err().Error()})
		return "", ctx.Err()
	default:
	}

	s.emit(EventUserInput, UserInputData{Text: input})
	s.appendTurn(TurnUserInput, llm.User(input))

	// UserPromptSubmit hooks
	if s.hookRunner != nil {
		hi := s.hookInput(HookUserPromptSubmit)
		hi.UserPrompt = input
		result := s.hookRunner.RunUserPromptSubmit(ctx, hi)
		for _, msg := range result.SystemMessages {
			s.Steer(msg)
		}
	}

	// Count conversation turns (user input -> model response pairs), not LLM round-trips.
	// Check the limit before incrementing so MaxTurns=N allows exactly N inputs.
	s.mu.Lock()
	turns := s.turns
	s.mu.Unlock()

	if s.cfg.MaxTurns > 0 && turns >= s.cfg.MaxTurns {
		s.emit(EventTurnLimit, TurnLimitData{MaxTurns: s.cfg.MaxTurns})
		s.mu.Lock()
		s.state = SessionIdle
		s.mu.Unlock()
		return "", nil
	}

	s.mu.Lock()
	s.turns++
	s.mu.Unlock()

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	for _, msg := range s.drainSteering() {
		s.appendTurn(TurnSteering, llm.User(msg))
		s.emit(EventSteeringInjected, SteeringInjectedData{Text: msg})
	}

	var toolSigs []string
	var lastText string // accumulated assistant text for round-limit return
	ctxWarned := false
	contentFilterRetried := false // track whether we've already tried recovering from a content filter error
	consecutiveEmptyResponses := 0
	totalEmptyResponses := 0
	consecutiveBareTextResponses := 0
	const maxEmptyRetries = 3
	const maxTotalEmptyResponses = 8 // prevent repeated burst-and-recover spirals
	const maxBareTextRetries = 3

	for round := 0; s.cfg.MaxToolRoundsPerInput < 0 || round < s.cfg.MaxToolRoundsPerInput; round++ {
		roundStart := time.Now()
		var timings RoundTimings
		timings.Round = round

		s.mu.Lock()
		s.currentRound = round
		s.totalRounds++
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			s.emit(EventError, ErrorData{Error: ctx.Err().Error()})
			return "", ctx.Err()
		default:
		}

		// --- Phase: SystemPrompt ---
		tPhaseStart := time.Now()

		// Rebuild system prompt using cached project docs and cached prompt components.
		sys := s.profile.BuildSystemPrompt(s.envInfo, s.projectDocs, s.cachedSkillList, s.cachedExtraTools)

		if s.cachedAgentSection != "" {
			sys += s.cachedAgentSection
		}

		sys += s.cachedNonInteractiveGuidance

		if strings.TrimSpace(s.cfg.UserInstructionOverride) != "" {
			sys = sys + "\n\n" + strings.TrimSpace(s.cfg.UserInstructionOverride) + "\n"
		}

		timings.SystemPrompt = time.Since(tPhaseStart)

		// --- Phase: ContextMgmt ---
		tPhaseStart = time.Now()

		// PreCompact hooks
		if s.hookRunner != nil {
			preCompactResult := s.hookRunner.RunPreCompact(ctx, s.hookInput(HookPreCompact))
			for _, msg := range preCompactResult.SystemMessages {
				s.Steer(msg)
			}
		}

		// Copy history once for both context management and message expansion.
		s.mu.Lock()
		historyTurns := append([]Turn{}, s.history...)
		s.mu.Unlock()

		// Apply context management before each LLM request.
		if s.strategy != nil {
			// Populate compaction metadata so checkpoint/summarize have session context.
			s.contextMgr.Meta = s.buildCompactionMeta()

			if err := s.strategy.ManageContext(ctx, &historyTurns, len(sys), s.emit); err != nil {
				s.emit(EventWarning, WarningData{Message: "context strategy error: " + err.Error()})
			}

			s.mu.Lock()
			s.history = historyTurns
			s.mu.Unlock()
		}

		timings.ContextMgmt = time.Since(tPhaseStart)

		// --- Phase: HistoryExpand ---
		tPhaseStart = time.Now()

		// Reuse historyTurns from context management — no redundant copy.
		history := make([]llm.Message, 0, len(historyTurns))
		for _, t := range historyTurns {
			if t.Kind == TurnSteering {
				history = append(history, llm.User(t.Message.Text()))
				continue
			}
			if t.Kind == TurnToolResults {
				// Expand aggregated tool results into individual messages.
				for _, p := range t.Message.Content {
					if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
						history = append(history, llm.ToolResultNamed(
							p.ToolResult.ToolCallID,
							p.ToolResult.Name,
							p.ToolResult.Content,
							p.ToolResult.IsError,
						))
					}
				}
				continue
			}
			if t.Kind == TurnCheckpoint || t.Kind == TurnSummary {
				// Compaction turns carry user-role messages; include as-is.
				history = append(history, t.Message)
				continue
			}
			history = append(history, t.Message)
		}

		timings.HistoryExpand = time.Since(tPhaseStart)

		// --- Phase: ToolDefs ---
		tPhaseStart = time.Now()
		toolDefs := s.allToolDefinitions(round)
		timings.ToolDefs = time.Since(tPhaseStart)

		req := llm.Request{
			Model:      s.profile.Model(),
			Provider:   s.profile.ID(),
			Messages:   append([]llm.Message{llm.System(sys)}, history...),
			Tools:      toolDefs,
			ToolChoice: &llm.ToolChoice{Mode: "auto"},
			WebSearch:  true,
			AdapterTimeout: &llm.AdapterTimeout{
				Connect:    10 * time.Second,
				Request:    10 * time.Minute,
				StreamRead: 30 * time.Second,
			},
		}
		if opts := s.profile.ProviderOptions(); opts != nil {
			req.ProviderOptions = opts
		}
		if strings.TrimSpace(s.cfg.ReasoningEffort) != "" {
			v := strings.TrimSpace(s.cfg.ReasoningEffort)
			req.ReasoningEffort = &v
		}

		// --- Phase: LLMCall ---
		tPhaseStart = time.Now()

		policy := llm.DefaultRetryPolicy()
		if s.cfg.LLMRetryPolicy != nil {
			policy = *s.cfg.LLMRetryPolicy
		}
		callCtx := llm.WithAPILogContext(ctx, s.id, round)
		resp, err := llm.Retry(callCtx, policy, s.cfg.LLMSleep, nil, func() (llm.Response, error) {
			return s.client.Complete(callCtx, req)
		})

		timings.LLMCall = time.Since(tPhaseStart)

		if err != nil {
			s.emit(EventError, ErrorData{Error: err.Error()})

			// Content filter recovery: compaction often removes the offending
			// content, allowing the next request to succeed. Try once.
			var cfe *llm.ContentFilterError
			if errors.As(err, &cfe) && !contentFilterRetried && s.contextMgr != nil {
				contentFilterRetried = true
				s.emit(EventWarning, WarningData{Message: "Content filter hit — compacting context and retrying"})
				s.mu.Lock()
				s.contextMgr.ForceCompact(ctx, &s.history, s.emit)
				s.mu.Unlock()
				continue
			}

			// Spec: context overflow should emit a warning (no automatic compaction).
			var cle *llm.ContextLengthError
			if errors.As(err, &cle) {
				s.emit(EventWarning, WarningData{Message: "Context length exceeded"})
			}
			// Spec: non-retryable/unrecoverable errors transition the session to CLOSED.
			var le llm.Error
			if errors.As(err, &le) && !le.Retryable() {
				s.Close()
			}
			return "", err
		}

		// Accumulate usage and record exact input token count for pressure calculation.
		if s.contextMgr != nil {
			s.contextMgr.AddUsage(resp.Usage)

			// Detect server-side web search in the response. Anthropic makes
			// multiple forward passes for server tools, reporting combined usage
			// (~2x actual). Skip recording the inflated baseline; the previous
			// lastInputTokens value remains valid for pressure estimation.
			hasServerWebSearch := false
			for _, p := range resp.Message.Content {
				if p.Kind == llm.ContentWebSearch {
					hasServerWebSearch = true
					break
				}
			}
			if !hasServerWebSearch {
				totalInput := resp.Usage.InputTokens
				if resp.Usage.CacheReadTokens != nil {
					totalInput += *resp.Usage.CacheReadTokens
				}
				if resp.Usage.CacheWriteTokens != nil {
					totalInput += *resp.Usage.CacheWriteTokens
				}
				if totalInput > 0 {
					s.mu.Lock()
					hLen := len(s.history)
					s.mu.Unlock()
					s.contextMgr.RecordInputTokens(totalInput, hLen)
				}
			}
		}

		// Context window awareness: emit a warning when we exceed ~80% of the profile's context window.
		if !ctxWarned {
			if s.maybeWarnContextUsage(req.Messages) {
				ctxWarned = true
			}
		}

		txt := resp.Text()
		lastText = txt
		calls := resp.ToolCalls()

		// Two concepts of "empty":
		// 1. noContent: no text and no tool calls — triggers retry logic
		// 2. skipHistory: no text, no tool calls, AND no phase metadata —
		//    truly nothing to append. Responses with phase annotations
		//    (e.g., "final_answer") must be preserved in history so the
		//    model sees its own phase metadata and can course-correct.
		noContent := strings.TrimSpace(txt) == "" && len(calls) == 0
		hasPhase := false
		for _, p := range resp.Message.Content {
			if p.Phase != "" {
				hasPhase = true
				break
			}
		}
		skipHistory := noContent && !hasPhase

		s.emit(EventAssistantTextStart, AssistantTextStartData{
			Model: resp.Model,
		})
		if !skipHistory {
			s.appendAssistantTurn(resp)
		}
		if strings.TrimSpace(txt) != "" {
			s.emit(EventAssistantTextDelta, AssistantTextDeltaData{Delta: txt})
		}
		textEndData := AssistantTextEndData{
			Text:         txt,
			Usage:        resp.Usage,
			FinishReason: resp.Finish.Reason,
			Model:        resp.Model,
		}
		if reasoning := resp.ReasoningText(); reasoning != "" {
			textEndData.Reasoning = reasoning
		}
		s.emit(EventAssistantTextEnd, textEndData)
		// pause_turn: model needs another turn (e.g. server-side web search still running).
		if resp.Finish.Reason == llm.FinishReasonPauseTurn {
			round-- // Don't count pause_turn as a tool round.
			timings.TotalRound = time.Since(roundStart)
			timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
			s.emit(EventRoundTimings, timings)
			continue
		}

		// Reverse-map provider-specific tool names to canonical names for registry lookup.
		if nameMap := s.profile.ToolNameMap(); len(nameMap) > 0 {
			reverse := make(map[string]string, len(nameMap))
			for canonical, provider := range nameMap {
				reverse[provider] = canonical
			}
			for i := range calls {
				if canonical, ok := reverse[calls[i].Name]; ok {
					calls[i].Name = canonical
				}
			}
		}

		if len(calls) == 0 {
			// Empty response (no text, no tool calls): likely a model glitch
			// (e.g. gpt-5.3-codex null-content). Inject escalating steering and retry.
			// Note: phase-only responses are still retried — they carry metadata
			// but the model hasn't produced any useful output.
			if noContent {
				consecutiveEmptyResponses++
				totalEmptyResponses++
				if consecutiveEmptyResponses <= maxEmptyRetries && totalEmptyResponses <= maxTotalEmptyResponses {
					s.emit(EventWarning, WarningData{
						Message: fmt.Sprintf("empty response from model (retry %d/%d)", consecutiveEmptyResponses, maxEmptyRetries),
					})
					var steering string
					switch consecutiveEmptyResponses {
					case 1:
						steering = "Your previous response was empty. Please continue working on the task."
					case 2:
						steering = "Your response was empty again. If you're stuck, write what you've tried so far " +
							"to notes.txt, then try a different approach. You have plenty of rounds left."
					default:
						steering = "You've produced multiple empty responses. You MUST either call a tool to continue " +
							"working, or call " + s.resultToolName() + " with your best effort so far. Take a breath — you've " +
							"got this. Try a completely different approach."
					}
					s.appendTurn(TurnSteering, llm.User(steering))
					s.emit(EventSteeringInjected, SteeringInjectedData{Text: steering})
					round-- // Don't count empty-response retries as tool rounds.
					timings.TotalRound = time.Since(roundStart)
					timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
					s.emit(EventRoundTimings, timings)
					continue
				}
				// Exhausted retries — fall through to exit.
			} else if s.cfg.NonInteractive {
				// Non-empty bare text without tool calls in non-interactive mode.
				// The model should use submit_result instead. Redirect it.
				consecutiveEmptyResponses = 0
				consecutiveBareTextResponses++
				if consecutiveBareTextResponses <= maxBareTextRetries {
					s.emit(EventWarning, WarningData{
						Message: fmt.Sprintf("bare text response without tool call (retry %d/%d)", consecutiveBareTextResponses, maxBareTextRetries),
					})
					steering := "You responded with bare text instead of a tool call. " +
						"You must use the " + s.resultToolName() + " tool to deliver results. " +
						"If you are done, call " + s.resultToolName() + ". " +
						"If you still have work to do, call your next tool."
					s.appendTurn(TurnSteering, llm.User(steering))
					s.emit(EventSteeringInjected, SteeringInjectedData{Text: steering})
					round-- // Don't count bare-text retries as tool rounds.
					timings.TotalRound = time.Since(roundStart)
					timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
					s.emit(EventRoundTimings, timings)
					continue
				}
				// Exhausted retries — fall through to exit with last text.
			} else {
				// Interactive mode: bare text is a normal response.
				consecutiveEmptyResponses = 0
			}

			s.mu.Lock()
			if looksLikeQuestion(txt) {
				s.state = SessionAwaitingInput
			} else {
				s.state = SessionIdle
			}
			s.mu.Unlock()
			s.maybeAutoSave()
			timings.TotalRound = time.Since(roundStart)
			timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall
			s.emit(EventRoundTimings, timings)
			return txt, nil
		}

		// Model produced tool calls — reset retry counters.
		consecutiveEmptyResponses = 0
		consecutiveBareTextResponses = 0

		// --- Phase: ToolExec ---
		tPhaseStart = time.Now()

		// Execute tool calls (possibly in parallel) and send results back.
		results := make([]ToolExecResult, len(calls))
		if s.profile.SupportsParallelToolCalls() && len(calls) > 1 {
			var wg sync.WaitGroup
			wg.Add(len(calls))
			for i := range calls {
				go func() {
					defer wg.Done()
					results[i] = s.execTool(ctx, calls[i])
				}()
			}
			wg.Wait()
		} else {
			for i := range calls {
				results[i] = s.execTool(ctx, calls[i])
			}
		}

		timings.ToolExec = time.Since(tPhaseStart)

		// --- Phase: Persistence ---
		tPhaseStart = time.Now()

		// Aggregate all tool results into a single TurnToolResults turn.
		var parts []llm.ContentPart
		for _, r := range results {
			parts = append(parts, llm.ContentPart{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID:     r.CallID,
					Name:           r.ToolName,
					Content:        r.Output,
					IsError:        r.IsError,
					DurationMS:     r.DurationMS,
					ImageData:      r.ImageData,
					ImageMediaType: r.ImageMediaType,
				},
			})
		}
		s.appendTurn(TurnToolResults, llm.Message{Role: llm.RoleTool, Content: parts})
		// Persist the completed tool round so resumed sessions always include
		// tool_result turns for any prior assistant tool calls.
		s.maybeAutoSave()

		timings.Persistence = time.Since(tPhaseStart)

		// --- Phase: AfterAction ---
		tPhaseStart = time.Now()

		// Notify the context strategy that a tool round completed.
		// AfterAction takes []Turn (not *[]Turn) so it cannot mutate the slice.
		// Pass s.history directly — no copy needed since the loop is single-
		// threaded and nothing else modifies history until AfterAction returns.
		if s.strategy != nil {
			s.mu.Lock()
			hist := s.history
			s.mu.Unlock()
			if err := s.strategy.AfterAction(ctx, hist, s.client); err != nil {
				s.emit(EventWarning, WarningData{Message: "strategy AfterAction error: " + err.Error()})
			}
		}

		timings.AfterAction = time.Since(tPhaseStart)

		// Loop detection: track per-call signatures and check for repeating patterns.
		for _, call := range calls {
			toolSigs = append(toolSigs, call.Name+":"+shortHash(call.Arguments))
		}
		if s.cfg.EnableLoopDetection != nil && *s.cfg.EnableLoopDetection {
			if detectLoop(toolSigs, s.cfg.LoopDetectionWindow) {
				s.mu.Lock()
				s.loopDetectionCount++
				count := s.loopDetectionCount
				s.mu.Unlock()

				warning := s.stuckEscalation(count)
				s.emit(EventLoopDetection, LoopDetectionData{Message: warning})
				s.appendTurn(TurnSteering, llm.User(warning))
				s.emit(EventSteeringInjected, SteeringInjectedData{Text: warning})
			}
		}

		// Inject any queued steering messages before the next model call.
		for _, msg := range s.drainSteering() {
			s.appendTurn(TurnSteering, llm.User(msg))
			s.emit(EventSteeringInjected, SteeringInjectedData{Text: msg})
		}

		// Task reminder injection.
		if reminder := s.maybeInjectTaskReminder(); reminder != "" {
			s.appendTurn(TurnSteering, llm.User(reminder))
			s.emit(EventSteeringInjected, SteeringInjectedData{Text: reminder})
		}

		// Emit round timings before checking result delivery.
		timings.TotalRound = time.Since(roundStart)
		timings.LoopOverhead = timings.TotalRound - timings.SystemPrompt - timings.ContextMgmt - timings.HistoryExpand - timings.ToolDefs - timings.LLMCall - timings.ToolExec - timings.Persistence - timings.AfterAction
		s.emit(EventRoundTimings, timings)

		// submit_result sets the flag; exit the loop with the result message.
		s.mu.Lock()
		delivered := s.resultDelivered
		text := s.resultText
		s.mu.Unlock()
		if delivered {
			// Stop hooks
			if s.hookRunner != nil {
				hi := s.hookInput(HookStop)
				hi.Reason = "communicate"
				stopResult := s.hookRunner.RunStop(ctx, hi)
				for _, msg := range stopResult.SystemMessages {
					s.Steer(msg)
				}
				if stopResult.Blocked {
					// Don't return — continue the loop
					continue
				}
			}
			s.mu.Lock()
			s.state = SessionIdle
			s.mu.Unlock()
			return text, nil
		}
	}

	s.emit(EventTurnLimit, TurnLimitData{MaxToolRoundsPerInput: s.cfg.MaxToolRoundsPerInput})
	s.mu.Lock()
	s.state = SessionIdle
	s.mu.Unlock()
	return lastText, nil
}

// stuckEscalation returns the steering message for the nth loop detection.
// First detection bumps reasoning effort; subsequent detections get increasingly
// direct about abandoning the current approach.
func (s *Session) stuckEscalation(count int) string {
	switch count {
	case 1:
		// Bump reasoning effort to help the agent think harder.
		s.mu.Lock()
		prev := s.cfg.ReasoningEffort
		if prev == "" || prev == "low" || prev == "medium" {
			s.cfg.ReasoningEffort = "high"
		} else if prev == "high" {
			s.cfg.ReasoningEffort = "xhigh"
		}
		s.mu.Unlock()
		return "You are stuck in a loop. Your reasoning effort has been increased. " +
			"Stop and think about why your current approach is not working. " +
			"What assumption are you making that might be wrong?"
	case 2:
		return "You are still stuck. Your current approach is fundamentally not working. " +
			"Abandon it completely and try a different strategy. " +
			"What is the simplest possible way to achieve the goal?"
	default:
		return "You have been stuck for a long time. " +
			"If you cannot make progress, report what you tried and what failed."
	}
}

func (s *Session) drainSteering() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steeringQueue) == 0 {
		return nil
	}
	out := append([]string{}, s.steeringQueue...)
	s.steeringQueue = nil
	return out
}

func (s *Session) popFollowUp() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.followups) == 0 {
		return ""
	}
	msg := s.followups[0]
	s.followups = s.followups[1:]
	return msg
}

// looksLikeQuestion returns true when the assistant text appears to be asking
// the user a question or requesting input (ends with "?" or ":").
func looksLikeQuestion(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	return strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, ":")
}

// detectLoop checks the last windowSize tool call signatures for repeating
// patterns of length 1, 2, or 3.
func detectLoop(signatures []string, windowSize int) bool {
	if len(signatures) < windowSize {
		return false
	}
	recent := signatures[len(signatures)-windowSize:]
	for patLen := 1; patLen <= 3; patLen++ {
		if windowSize%patLen != 0 {
			continue
		}
		pattern := recent[:patLen]
		allMatch := true
		for i := patLen; i < windowSize; i++ {
			if recent[i] != pattern[i%patLen] {
				allMatch = false
				break
			}
		}
		if allMatch {
			return true
		}
	}
	return false
}

// customToolDescriptions returns formatted descriptions of tools in the registry
// that were registered after session initialization (not core or MCP tools).
func (s *Session) customToolDescriptions() string {
	// Build MCP tool name set for exclusion (MCP tools have their own section).
	mcpNames := make(map[string]bool, len(s.mcpTools))
	for _, td := range s.mcpTools {
		mcpNames[td.Name] = true
	}

	var b strings.Builder
	for _, td := range s.reg.Definitions() {
		if s.coreToolNames[td.Name] || mcpNames[td.Name] {
			continue
		}
		desc := strings.TrimSpace(td.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString(fmt.Sprintf("- %s: %s\n", td.Name, desc))
	}
	return b.String()
}

// allToolDefinitions returns cached tool definitions, filtering the result
// tool when MinResultRound is configured and the round is below the threshold.
// The cache is built once at session init via rebuildToolDefsCache.
func (s *Session) allToolDefinitions(round int) []llm.ToolDefinition {
	if s.cfg.MinResultRound > 0 && round < s.cfg.MinResultRound {
		return s.cachedToolDefsNoResult
	}
	return s.cachedToolDefs
}

// rebuildToolDefsCache builds the cached tool definition lists from the
// current profile, MCP tools, and registry state. Called once at session init
// and again if tools are added at runtime (e.g. MCP or custom tools).
func (s *Session) rebuildToolDefsCache() {
	registered := s.reg.RegisteredNames()
	resultName := s.resultToolName()

	// Profile tool definitions use provider-specific names (e.g. "exec_command"
	// for OpenAI). Build a reverse map from provider name → canonical name so
	// we can filter against the registry which uses canonical names.
	nameMap := s.profile.ToolNameMap() // canonical → provider, may be nil
	reverseMap := make(map[string]string, len(nameMap))
	for canonical, provider := range nameMap {
		reverseMap[provider] = canonical
	}

	var defs []llm.ToolDefinition
	included := make(map[string]bool)
	for _, td := range s.profile.ToolDefinitions() {
		canonical := td.Name
		if c, ok := reverseMap[td.Name]; ok {
			canonical = c
		}
		if registered[canonical] {
			defs = append(defs, td)
			included[canonical] = true
			// Also track the provider-mapped name so loop 3 (registry tools)
			// won't add a registry tool whose canonical name matches the
			// provider name (e.g. OpenAI maps glob→list_dir; the registry
			// also has a separate list_dir tool that must be excluded).
			included[td.Name] = true
		}
	}
	for _, td := range s.mcpTools {
		if registered[td.Name] && !included[td.Name] {
			defs = append(defs, td)
			included[td.Name] = true
		}
	}
	// Include any tools registered directly on the registry (e.g. approve/reject
	// on reviewer sessions) that weren't already covered by profile or MCP.
	for _, td := range s.reg.Definitions() {
		if included[td.Name] {
			continue
		}
		// Normalize empty parameters to a valid object schema so the LLM
		// client doesn't reject the tool definition.
		if td.Parameters != nil && td.Parameters["type"] == nil {
			td.Parameters = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		defs = append(defs, td)
	}

	s.cachedToolDefs = defs

	// Build the filtered list that excludes the result tool.
	filtered := make([]llm.ToolDefinition, 0, len(defs))
	for _, td := range defs {
		canonical := td.Name
		if c, ok := reverseMap[td.Name]; ok {
			canonical = c
		}
		if canonical != resultName {
			filtered = append(filtered, td)
		}
	}
	s.cachedToolDefsNoResult = filtered
}

// rebuildPromptCache caches system prompt components that don't change between
// rounds: skill list, extra tools string, agent section, and non-interactive
// guidance. Called once at session init.
func (s *Session) rebuildPromptCache() {
	// Skill list.
	s.cachedSkillList = make([]SkillMeta, 0, len(s.skills))
	for _, sm := range s.skills {
		s.cachedSkillList = append(s.cachedSkillList, sm)
	}

	// Extra tool descriptions (MCP + custom-registered).
	var extraTools strings.Builder
	if len(s.mcpTools) > 0 {
		extraTools.WriteString("MCP Tools (from external servers):\n")
		for _, td := range s.mcpTools {
			desc := strings.TrimSpace(td.Description)
			if desc == "" {
				desc = "(no description)"
			}
			extraTools.WriteString(fmt.Sprintf("- %s: %s\n", td.Name, desc))
		}
	}
	if extra := s.customToolDescriptions(); len(extra) > 0 {
		if extraTools.Len() > 0 {
			extraTools.WriteString("\n")
		}
		extraTools.WriteString("Additional tools:\n")
		extraTools.WriteString(extra)
	}
	s.cachedExtraTools = extraTools.String()

	// Plugin agents section.
	s.cachedAgentSection = FormatPluginAgentsPrompt(s.pluginAgents)

	// Non-interactive guidance (empty when not in non-interactive mode).
	if s.cfg.NonInteractive {
		s.cachedNonInteractiveGuidance = nonInteractiveGuidance(s.resultToolName())
	}
}

// initSessionState performs the shared initialization steps for both
// NewSession and RestoreSession: environment snapshot, system prompt
// resolution, skills discovery, tool registry setup, and MCP connection.
// The Session struct fields (client, profile, env, cfg) must already be set.
// Returns the prompt sources so the caller can emit events after SessionStart.
func (s *Session) initSessionState() ([]PromptSource, error) {
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

	// Load built-in agents (coordinator, worker, subagent, explorer, etc.) as a base layer.
	// This must happen before prompt resolution so we can look up the persona.
	builtins, err := builtinAgents()
	if err != nil {
		return nil, fmt.Errorf("loading built-in agents: %w", err)
	}
	s.pluginAgents = builtins

	var promptSources []PromptSource
	var basePrompt string
	if s.cfg.BasePromptOverride != "" {
		// Subagents use BasePromptOverride to replace the full prompt with
		// core + persona, composed by the parent.
		basePrompt = s.cfg.BasePromptOverride
	} else {
		gitRoot := gitRootOrEmpty(s.env, ei.WorkingDir)
		projDir := ProjectPromptsDir(gitRoot)
		if s.cfg.NoProjectPrompts {
			projDir = ""
		}
		resolvedPrompt, sources, err := ResolveSystemPromptWithSources(
			s.profile.ID(), s.profile.Model(),
			s.cfg.SystemPromptFile,
			projDir,
			GlobalPromptsDir(),
			s.cfg.SystemPromptAppend,
		)
		if err != nil {
			return nil, fmt.Errorf("resolve system prompt: %w", err)
		}
		basePrompt = resolvedPrompt
		promptSources = sources

		// Append persona. Default to "coordinator" when no --agent flag is set
		// and no --system-prompt override is active.
		if s.cfg.SystemPromptFile == "" {
			agentName := s.cfg.AgentName
			if agentName == "" {
				agentName = "coordinator"
			}
			if agent, ok := s.pluginAgents[agentName]; ok {
				persona := strings.TrimSpace(agent.SystemPrompt)
				if persona != "" {
					basePrompt += "\n\n" + persona
					promptSources = append(promptSources, PromptSource{
						Label: "persona:" + agentName,
						Size:  len(persona),
					})
				}
			}
		}
	}

	// If the result tool has been renamed, update all references in the system prompt.
	if name := s.resultToolName(); name != "communicate" {
		basePrompt = strings.ReplaceAll(basePrompt, "communicate", name)
	}
	s.profile = s.profile.WithBasePrompt(basePrompt)

	// Extract embedded skills to a temp dir as the base layer.
	// Filesystem-discovered skills (project + extraDirs) shadow embedded ones.
	// Skip skill discovery for subagents with BasePromptOverride — they have
	// their own focused prompts and would get confused by <skills> listings.
	s.skills = make(map[string]SkillMeta)
	if s.cfg.BasePromptOverride == "" {
		if dir, err := extractEmbeddedSkills(); err == nil {
			s.embeddedSkillsDir = dir
			// Scan extracted dir directly (skill subdirs are immediate children).
			scanSkillsDir(dir, s.skills)
		}
		for name, meta := range DiscoverSkills(s.env, s.cfg.SkillsDirs...) {
			s.skills[name] = meta // filesystem shadows embedded
		}
	}

	// Initialize plugins (skills, agents, hooks). Plugin agents override builtins.
	if err := s.initPlugins(); err != nil {
		return nil, fmt.Errorf("plugin initialization: %w", err)
	}

	s.contextMgr = NewContextManager(s.profile, s.client)
	s.contextMgr.ResultToolName = s.resultToolName()

	reg := s.profile.NewToolRegistry()
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

	// Cache project docs once; reused every round for system prompt rebuilds.
	s.projectDocs, s.projectDocsTruncated = LoadProjectDocs(s.env, s.profile.ProjectDocFiles()...)

	// Cache tool definitions and prompt components that don't change per-round.
	s.rebuildToolDefsCache()
	s.rebuildPromptCache()

	return promptSources, nil
}

// buildInitialSystemPrompt constructs the system prompt as the model would see
// it on its first turn. Persisted in the transcript header for debugging.
func (s *Session) buildInitialSystemPrompt() string {
	docs := s.projectDocs
	var skillList []SkillMeta
	for _, sm := range s.skills {
		skillList = append(skillList, sm)
	}
	var extraTools strings.Builder
	if len(s.mcpTools) > 0 {
		extraTools.WriteString("MCP Tools (from external servers):\n")
		for _, td := range s.mcpTools {
			desc := strings.TrimSpace(td.Description)
			if desc == "" {
				desc = "(no description)"
			}
			extraTools.WriteString(fmt.Sprintf("- %s: %s\n", td.Name, desc))
		}
	}
	if extra := s.customToolDescriptions(); len(extra) > 0 {
		if extraTools.Len() > 0 {
			extraTools.WriteString("\n")
		}
		extraTools.WriteString("Additional tools:\n")
		extraTools.WriteString(extra)
	}
	sys := s.profile.BuildSystemPrompt(s.envInfo, docs, skillList, extraTools.String())
	if agentSection := FormatPluginAgentsPrompt(s.pluginAgents); agentSection != "" {
		sys += agentSection
	}
	if s.cfg.NonInteractive {
		sys += nonInteractiveGuidance(s.resultToolName())
	}
	if strings.TrimSpace(s.cfg.UserInstructionOverride) != "" {
		sys = sys + "\n\n" + strings.TrimSpace(s.cfg.UserInstructionOverride) + "\n"
	}
	return sys
}

// initPlugins loads plugins from configured directories, merging their skills,
// agents, and hooks into the session. Fires SessionStart hooks after setup.
func (s *Session) initPlugins() error {
	if len(s.cfg.PluginDirs) == 0 {
		return nil
	}

	plugins, err := LoadPlugins(s.cfg.PluginDirs)
	if err != nil {
		return err
	}

	s.plugins = plugins

	runner := NewHookRunner(clientAdapter{s.client}, s.profile.Model())
	allAgents := map[string]PluginAgent{}

	for _, p := range plugins {
		for name, meta := range p.Skills {
			s.skills[name] = meta
		}
		for name, agent := range p.Agents {
			allAgents[name] = agent
		}
		for event, eventHooks := range p.Hooks {
			runner.Add(event, eventHooks...)
		}

		s.pluginMCPConfigs = append(s.pluginMCPConfigs, p.MCPConfigs...)

		s.emit(EventPluginLoaded, PluginLoadedData{
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
	result := s.hookRunner.RunSessionStart(context.Background(), s.hookInput(HookSessionStart))
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

	mgr, err := NewMCPManager(ctx, configs, nil)
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

// Tool registration.

func registerCoreTools(reg *ToolRegistry, s *Session) error {
	// read_file
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defReadFile()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			offset := optionalIntArg(args, "offset")
			limit := optionalIntArg(args, "limit")
			result, err := env.ReadFile(path, offset, limit)
			if err == nil {
				s.trackReadFile(path)
				// If the file is an image, return an ImageResult so the
				// model receives the image as a proper content part.
				if img := parseImageResult(path, result); img != nil {
					return *img, nil
				}
			}
			return result, err
		},
	}); err != nil {
		return err
	}

	// read_many_files (Gemini-aligned; safe to register globally)
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defReadManyFiles()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pathsAny := args["file_paths"]
			var paths []string
			switch x := pathsAny.(type) {
			case []any:
				for _, it := range x {
					paths = append(paths, fmt.Sprint(it))
				}
			case []string:
				paths = append(paths, x...)
			}
			offset := optionalIntArg(args, "offset")
			limit := optionalIntArg(args, "limit")

			var b strings.Builder
			for _, p := range paths {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				b.WriteString("----- BEGIN " + p + " -----\n")
				txt, err := env.ReadFile(p, offset, limit)
				if err != nil {
					b.WriteString("[ERROR] " + err.Error() + "\n")
				} else {
					s.trackReadFile(p)
					b.WriteString(txt)
					if !strings.HasSuffix(txt, "\n") {
						b.WriteString("\n")
					}
				}
				b.WriteString("----- END " + p + " -----\n")
			}
			return b.String(), nil
		},
	})

	// write_file
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defWriteFile()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			warn := s.readBeforeWriteWarning(path)
			result, err := env.WriteFile(path, fmt.Sprint(args["content"]))
			if err == nil && warn != "" {
				return warn + fmt.Sprint(result), nil
			}
			return result, err
		},
	}); err != nil {
		return err
	}

	// edit_file
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defEditFile()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			replaceAll := false
			if v, ok := args["replace_all"].(bool); ok {
				replaceAll = v
			}
			warn := s.readBeforeWriteWarning(path)
			result, err := env.EditFile(path, fmt.Sprint(args["old_string"]), fmt.Sprint(args["new_string"]), replaceAll)
			if err == nil && warn != "" {
				return warn + fmt.Sprint(result), nil
			}
			return result, err
		},
	})

	// shell
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defShell()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			cmd := fmt.Sprint(args["command"])
			timeout := s.cfg.DefaultCommandTimeoutMS
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			if s.cfg.MaxCommandTimeoutMS > 0 && timeout > s.cfg.MaxCommandTimeoutMS {
				timeout = s.cfg.MaxCommandTimeoutMS
			}
			res, err := env.ExecCommand(ctx, cmd, timeout, "", nil)

			// Return a line-oriented tool output so line truncation works as intended for shell output.
			var b strings.Builder
			if strings.TrimSpace(res.Stdout) != "" {
				b.WriteString(res.Stdout)
				if !strings.HasSuffix(res.Stdout, "\n") {
					b.WriteString("\n")
				}
			}
			if strings.TrimSpace(res.Stderr) != "" {
				b.WriteString(res.Stderr)
				if !strings.HasSuffix(res.Stderr, "\n") {
					b.WriteString("\n")
				}
			}
			if res.TimedOut {
				b.WriteString(fmt.Sprintf("[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the timeout_ms parameter.]\n", timeout))
			}
			b.WriteString(fmt.Sprintf("exit_code=%d duration_ms=%d timed_out=%t\n", res.ExitCode, res.DurationMS, res.TimedOut))
			return b.String(), err
		},
	}); err != nil {
		return err
	}

	// list_dir (Gemini-aligned)
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defListDir()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["path"])
			depth := 1
			if v, ok := args["depth"].(float64); ok && int(v) > 0 {
				depth = int(v)
			}
			return env.ListDirectory(path, depth)
		},
	})

	// grep
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defGrep()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := fmt.Sprint(args["pattern"])
			path := fmt.Sprint(args["path"])
			glob := fmt.Sprint(args["glob_filter"])
			ci := false
			if v, ok := args["case_insensitive"].(bool); ok {
				ci = v
			}
			maxRes := 100
			if v, ok := args["max_results"].(float64); ok && int(v) > 0 {
				maxRes = int(v)
			}
			outputMode := ""
			if v, ok := args["output_mode"].(string); ok {
				outputMode = v
			}
			return env.Grep(pat, path, glob, ci, maxRes, outputMode)
		},
	}); err != nil {
		return err
	}

	// glob
	if err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defGlob()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			pat := fmt.Sprint(args["pattern"])
			path := fmt.Sprint(args["path"])
			matches, err := env.Glob(pat, path)
			if err != nil {
				return "", err
			}
			return strings.Join(matches, "\n"), nil
		},
	}); err != nil {
		return err
	}

	// apply_patch (OpenAI-specific; best-effort implementation lives in this repo)
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defApplyPatch()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			patch := fmt.Sprint(args["patch"])
			return ApplyPatch(env.WorkingDirectory(), patch)
		},
	})

	// Subagent tools (best-effort; synchronous completion for v1).
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defSpawnAgent()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			task := fmt.Sprint(args["task"])
			model := ""
			if v, ok := args["model"]; ok && v != nil {
				model = fmt.Sprint(v)
			}
			workingDir := ""
			if v, ok := args["working_dir"]; ok && v != nil {
				workingDir = fmt.Sprint(v)
			}
			maxTurns := 0
			if v, ok := args["max_turns"].(float64); ok {
				maxTurns = int(v)
			}
			agentType := ""
			if v, ok := args["agent_type"]; ok && v != nil {
				agentType = fmt.Sprint(v)
			}
			blocking := false
			if v, ok := args["blocking"].(bool); ok {
				blocking = v
			}
			reasoningEffort := ""
			if v, ok := args["reasoning_effort"]; ok && v != nil {
				reasoningEffort = fmt.Sprint(v)
			}
			result, err := s.spawnAgent(ctx, task, model, workingDir, maxTurns, agentType, reasoningEffort)
			if err != nil || !blocking {
				return result, err
			}
			// Blocking mode: extract agent_id and wait for completion.
			var spawnResult map[string]any
			if err := json.Unmarshal([]byte(result.(string)), &spawnResult); err != nil {
				return result, nil
			}
			agentID, _ := spawnResult["agent_id"].(string)
			if agentID == "" {
				return result, nil
			}
			waitResult, waitErr := s.waitAgent(ctx, agentID, 0) // 0 = wait indefinitely
			// Include agent_id in the blocking result so the caller can
			// use resume_agent later if needed (e.g. to iterate with a planner).
			if waitStr, ok := waitResult.(string); ok {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(waitStr), &parsed); err == nil {
					parsed["agent_id"] = agentID
					b, _ := json.Marshal(parsed)
					return string(b), waitErr
				}
			}
			return waitResult, waitErr
		},
	})
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defSendInput()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			agentID := fmt.Sprint(args["agent_id"])
			result, err := s.sendInput(ctx, agentID, fmt.Sprint(args["message"]))
			if err != nil {
				return result, err
			}
			blocking, _ := args["blocking"].(bool)
			if !blocking {
				return result, nil
			}
			// Blocking mode: wait for the agent to finish and return its result.
			waitResult, waitErr := s.waitAgent(ctx, agentID, 0)
			if waitStr, ok := waitResult.(string); ok {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(waitStr), &parsed); err == nil {
					parsed["agent_id"] = agentID
					b, _ := json.Marshal(parsed)
					return string(b), waitErr
				}
			}
			return waitResult, waitErr
		},
	})
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defWait()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			timeout := 0
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			// Clamp to minimum to prevent rapid-retry burn.
			if timeout > 0 && timeout < minWaitTimeoutMS {
				timeout = minWaitTimeoutMS
			}
			return s.waitAgent(ctx, fmt.Sprint(args["agent_id"]), timeout)
		},
	})
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defCloseAgent()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.closeAgent(fmt.Sprint(args["agent_id"]))
		},
	})

	// Task management.
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defTaskList()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			s.mu.Lock()
			s.taskToolEverUsed = true
			s.taskToolLastRound = s.totalRounds
			s.mu.Unlock()
			store := s.getOrCreateTaskStore()
			action := fmt.Sprint(args["action"])
			switch action {
			case "view":
				return store.View(), nil
			case "append":
				raw, ok := args["tasks"].([]any)
				if !ok || len(raw) == 0 {
					return nil, fmt.Errorf("append requires a non-empty 'tasks' array")
				}
				var items []TaskInput
				for _, r := range raw {
					m, ok := r.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("each task must be an object with description and prompt")
					}
					var depIDs []int
					if depsRaw, ok := m["depends_on"].([]any); ok {
						for _, d := range depsRaw {
							if v, ok := d.(float64); ok {
								depIDs = append(depIDs, int(v))
							}
						}
					}
					var taskType TaskType
					if t, ok := m["type"].(string); ok {
						taskType = TaskType(t)
					}
					items = append(items, TaskInput{
						Type:        taskType,
						Description: fmt.Sprint(m["description"]),
						Prompt:      fmt.Sprint(m["prompt"]),
						DependsOn:   depIDs,
					})
				}
				return store.Append(items)
			case "update":
				raw, ok := args["updates"].([]any)
				if !ok || len(raw) == 0 {
					return nil, fmt.Errorf("update requires a non-empty 'updates' array")
				}
				var updates []TaskUpdate
				for _, r := range raw {
					m, ok := r.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("each update must be an object with id and status")
					}
					id := 0
					if v, ok := m["id"].(float64); ok {
						id = int(v)
					}
					u := TaskUpdate{
						ID:     id,
						Status: TaskStatus(fmt.Sprint(m["status"])),
					}
					if n, ok := m["notes"].(string); ok {
						u.Notes = n
					}
					if depsRaw, ok := m["depends_on"]; ok {
						var depIDs []int
						if arr, ok := depsRaw.([]any); ok {
							for _, d := range arr {
								if v, ok := d.(float64); ok {
									depIDs = append(depIDs, int(v))
								}
							}
						}
						u.DependsOn = &depIDs
					}
					updates = append(updates, u)
				}
				if err := store.Update(updates); err != nil {
					return nil, err
				}

				// Check if any task was marked done or cancelled — if so, suggest next tasks.
				var completedAny bool
				for _, u := range updates {
					if u.Status == TaskDone || u.Status == TaskCancelled {
						completedAny = true
						break
					}
				}

				if !completedAny {
					return "Updated.", nil
				}

				eligible := store.NextEligible()
				total, done := store.Progress()

				var msg strings.Builder
				msg.WriteString(fmt.Sprintf("Updated. Progress: %d/%d tasks complete.\n", done, total))

				switch len(eligible) {
				case 0:
					if done == total {
						msg.WriteString("All tasks complete.")
					} else {
						msg.WriteString("No tasks are currently ready (remaining tasks have unsatisfied dependencies).")
					}
				case 1:
					msg.WriteString(fmt.Sprintf("\nNext task: #%d — %s.", eligible[0].ID, eligible[0].Description))
				if eligible[0].Prompt != "" {
					msg.WriteString(fmt.Sprintf("\nInstructions: %s", eligible[0].Prompt))
				}
				msg.WriteString("\nMark it in_progress to begin.")
				default:
					msg.WriteString("\nReady tasks:\n")
					for _, t := range eligible {
						msg.WriteString(fmt.Sprintf("  #%d — %s\n", t.ID, t.Description))
					if t.Prompt != "" {
						msg.WriteString(fmt.Sprintf("      Instructions: %s\n", t.Prompt))
					}
					}
					msg.WriteString("Pick one and mark it in_progress.")
				}
				return msg.String(), nil
			default:
				return nil, fmt.Errorf("unknown task_list action %q: use view, append, or update", action)
			}
		},
	})

	// Web fetch.
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defWebFetch()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			rawURL := fmt.Sprint(args["url"])
			question := fmt.Sprint(args["question"])
			return s.webFetch(ctx, rawURL, question)
		},
	})

	// Web search (Gemini only — see tool_web_search.go for why).
	// OpenAI and Anthropic handle web search natively via req.WebSearch;
	// registering a function tool named "web_search" for those providers
	// causes a duplicate name collision with the adapter-injected server tool.
	if s.profile.ID() == "gemini" {
		_ = reg.Register(RegisteredTool{
			Tool: llm.Tool{Definition: defWebSearch()},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				query := fmt.Sprint(args["query"])
				return s.webSearch(ctx, query)
			},
		})
	}

	// Submit result (exits session).
	// Use the profile's definition if available (it may have been modified by
	// WithAllowedDecisions to add extra fields to the output schema).
	// Fall back to the base definition otherwise.
	resultToolDef := defSubmitResultNamed(s.resultToolName())
	if existing := reg.Get(s.resultToolName()); existing != nil {
		resultToolDef = existing.Definition
	}
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: resultToolDef},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			message := ""
			if v, ok := args["message"]; ok {
				message = fmt.Sprint(v)
			}

			if v, ok := args["output"]; (!ok || v == nil) && strings.TrimSpace(message) == "" {
				return nil, fmt.Errorf("submit_result requires either message or output")
			}

			resultText := message
			if output, ok := args["output"]; ok && output != nil {
				// Only use canonicalized output when there's no top-level message.
				// When both message and output are provided, message is the detailed
				// text intended for the parent; output is structured metadata.
				if strings.TrimSpace(message) == "" {
					resultText = canonicalNodeOutputText(output)
					if outMsg := submitResultOutputMessage(output); outMsg != "" {
						message = outMsg
					}
				}
			}

			// Reviewer gate: at depth 0 (root session) with EnableReviewerGate,
			// spawn a reviewer subagent to validate the work before accepting.
			// At depth > 0 (subagent), pass through directly to prevent recursion.
			if s.cfg.EnableReviewerGate && s.depth == 0 {
				verdict, err := s.spawnReviewer(ctx, resultText)
				if err != nil {
					// Fail-open: reviewer error should not block result delivery.
					_ = err // reviewer error logged via event system if needed
				} else if !verdict.Pass {
					// Reviewer rejected. Deliver feedback via steering (user-role
					// message) instead of as the tool result. A JSON rejection blob
					// in the function_call_output triggers gpt-5.3-codex's empty
					// final_answer mode ~42% of the time. A user-role steering
					// message only triggers it ~2% of the time.
					steering := "Your submission was not accepted. Here is the feedback:\n\n" +
						verdict.Feedback +
						"\n\n---\n" +
						"Take a breath. You've got this — we trust you and we know you're capable.\n\n" +
						"Before your next attempt:\n" +
						"1. Write down what you tried and why it didn't work in notes.txt\n" +
						"2. Read notes.txt to see the full picture of what's been attempted\n" +
						"3. Step back and try a fundamentally different approach\n"
					s.Steer(steering)
					return "Not yet — your solution needs more work. Check the feedback and keep going.", nil
				}
			}

			s.emit(EventSubmitResult, SubmitResultData{
				Message: message,
			})

			// Drain steering queue into the inbox.
			inbox := s.drainSteering()

			s.mu.Lock()
			s.resultDelivered = true
			s.resultText = resultText
			s.mu.Unlock()

			resp := map[string]any{
				"accepted":  true,
				"delivered": true,
				"inbox":     inbox,
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	})

	// use_skill (progressive disclosure of skill instructions).
	// Only present for profiles that include the use_skill tool definition
	// (Anthropic, Gemini). OpenAI models use read_file on SKILL.md paths instead.
	if reg.Get("use_skill") != nil {
		_ = reg.Register(RegisteredTool{
			Tool: llm.Tool{Definition: defUseSkill()},
			Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
				_ = ctx
				_ = env
				skillName := fmt.Sprint(args["skill_name"])
				meta, ok := s.skills[skillName]
				if !ok {
					return nil, fmt.Errorf("skill %q not found", skillName)
				}
				s.emit(EventSkillActivated, SkillActivatedData{Name: skillName})
				body, err := LoadSkillBody(meta)
				if err != nil {
					return nil, fmt.Errorf("loading skill %q: %w", skillName, err)
				}
				return fmt.Sprintf("Skill: %s\nLocation: %s\n\n---\n\n%s", skillName, meta.Dir, body), nil
			},
		})
	}

	return nil
}

type nodeOutput struct {
	Decision  string         `json:"decision,omitempty"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data"`
	Artifacts []string       `json:"artifacts"`
}

func canonicalNodeOutputText(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return "{}"
	}

	var decision string
	if d, ok := m["decision"].(string); ok {
		decision = d
	}
	out := nodeOutput{
		Decision:  decision,
		Message:   fmt.Sprint(m["message"]),
		Data:      map[string]any{},
		Artifacts: []string{},
	}

	if data, ok := m["data"].(map[string]any); ok {
		out.Data = data
	}
	if arts, ok := m["artifacts"]; ok {
		switch v := arts.(type) {
		case []string:
			out.Artifacts = append([]string{}, v...)
		case []any:
			out.Artifacts = make([]string, 0, len(v))
			for _, a := range v {
				out.Artifacts = append(out.Artifacts, fmt.Sprint(a))
			}
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func submitResultOutputMessage(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	msg, _ := m["message"].(string)
	return msg
}

// trackReadFile records that a file has been read in this session.
func (s *Session) trackReadFile(path string) {
	s.readFilesMu.Lock()
	s.readFiles[s.resolveFilePath(path)] = true
	s.readFilesMu.Unlock()
}

// readBeforeWriteWarning returns a warning string if the file exists but hasn't
// been read in this session. Returns "" for new files or previously-read files.
func (s *Session) readBeforeWriteWarning(path string) string {
	abs := s.resolveFilePath(path)
	s.readFilesMu.RLock()
	_, seen := s.readFiles[abs]
	s.readFilesMu.RUnlock()
	if seen {
		return ""
	}
	// New file creation is exempt from the warning.
	if !s.env.FileExists(path) {
		return ""
	}
	return "[WARNING: Writing to file that has not been read in this session. Consider reading first.]\n"
}

func (s *Session) resolveFilePath(path string) string {
	p := strings.TrimSpace(path)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.env.WorkingDirectory(), p)
}

// applyThresholdScale scales all compaction thresholds on cm by the given
// factor. A factor of 0 or 1 leaves defaults unchanged.
//
// Thresholds are clamped to a minimum of 0.20 so that aggressive scaling
// doesn't collapse all layers into a narrow band.
func applyThresholdScale(cm *ContextManager, scale float64) {
	if scale > 0 && scale != 1.0 {
		clamp := func(v float64) float64 {
			if v < 0.20 {
				return 0.20
			}
			return v
		}
		cm.ObservationMaskThreshold = clamp(cm.ObservationMaskThreshold * scale)
		cm.ThinkingClearThreshold = clamp(cm.ThinkingClearThreshold * scale)
		cm.CheckpointThreshold = clamp(cm.CheckpointThreshold * scale)
		cm.SummarizeThreshold = clamp(cm.SummarizeThreshold * scale)
	}
}

func (s *Session) getOrCreateTaskStore() *TaskStore {
	s.taskStoreOnce.Do(func() {
		dir := s.stateDir
		if dir == "" {
			dir = s.env.WorkingDirectory()
		}
		s.taskStore = NewTaskStore(dir, s.id)
		_ = s.taskStore.Load()
	})
	return s.taskStore
}

// maybeInjectTaskReminder checks whether a task-related steering message
// should be injected before the next LLM call. Returns the message or "".
func (s *Session) maybeInjectTaskReminder() string {
	s.mu.Lock()
	totalRounds := s.totalRounds
	lastRound := s.taskToolLastRound
	everUsed := s.taskToolEverUsed
	nudgeFired := s.taskNudgeFired
	s.mu.Unlock()

	roundsSinceUse := totalRounds - lastRound

	// Trigger 3: never used task_list, 10+ rounds in.
	if !everUsed && !nudgeFired && totalRounds >= 10 {
		s.mu.Lock()
		s.taskNudgeFired = true
		s.mu.Unlock()
		return taskReminderNudge()
	}

	// Trigger 2: tasks exist, not used in 5+ rounds.
	if everUsed && roundsSinceUse >= 5 {
		store := s.getOrCreateTaskStore()
		if len(store.View()) > 0 {
			s.mu.Lock()
			s.taskToolLastRound = totalRounds
			s.mu.Unlock()
			return taskReminderForInactivity(store)
		}
	}

	return ""
}

// optionalIntArg extracts an optional integer pointer from tool arguments.
func optionalIntArg(args map[string]any, key string) *int {
	v, ok := args[key]
	if !ok {
		return nil
	}
	if n, ok := v.(float64); ok {
		ni := int(n)
		return &ni
	}
	return nil
}
