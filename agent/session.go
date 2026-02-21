package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

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
	// Used by default subagents to avoid inheriting the parent's delegation instructions.
	BasePromptOverride string `json:"base_prompt_override,omitempty"`

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

	// ContextStrategy selects the context management strategy: compact|recall|session-log|ooda.
	ContextStrategy string `json:"context_strategy,omitempty"`

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

	// communicate tool state (transient, reset each processOneInput call)
	resultDelivered bool
	resultText      string

	// subagents
	depth     int
	subagents map[string]*subagent

	// context management
	contextMgr *ContextManager
	strategy   ContextStrategy

	// skills discovered at session startup
	skills map[string]SkillMeta

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

	// transcript writer (nil when StateDir is empty)
	transcript *TranscriptWriter
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

	if err := s.initSessionState(); err != nil {
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
		}
		tpath := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
		tw, twErr := NewTranscriptWriter(tpath, hdr)
		if twErr != nil {
			s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript create failed: %v", twErr)})
		}
		s.transcript = tw
	}

	applyThresholdScale(s.contextMgr, cfg.CompactionThresholdScale)
	s.contextMgr.OnCompactionTurn = func(t Turn) {
		if err := s.transcript.Append(t); err != nil {
			s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript compaction write: %v", err)})
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

	if err := s.initSessionState(); err != nil {
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
			}
			tw, twErr = NewTranscriptWriter(tpath, hdr)
			if twErr != nil {
				s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript open failed: %v", twErr)})
			}
		}
		s.transcript = tw
	}

	applyThresholdScale(s.contextMgr, cfg.CompactionThresholdScale)
	s.contextMgr.OnCompactionTurn = func(t Turn) {
		if err := s.transcript.Append(t); err != nil {
			s.emit(EventWarning, WarningData{Message: fmt.Sprintf("transcript compaction write: %v", err)})
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
	return s, nil
}

func (s *Session) ID() string                  { return s.id }
func (s *Session) Events() <-chan SessionEvent { return s.events }

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

// ResultDelivered reports whether communicate(result) was called during the
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

		// 8. Transition to CLOSED last, then close the events channel.
		s.mu.Lock()
		s.state = SessionClosed
		s.mu.Unlock()
		close(s.events)
	})
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
	res := s.reg.ExecuteCall(ctx, s.env, call)

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

// maybeAutoSave persists the session state if StateDir is configured.
// Errors are emitted as warnings but do not interrupt the session.
func (s *Session) maybeAutoSave() {
	if s.stateDir == "" {
		return
	}
	snap := s.Snapshot()
	if err := SaveSession(s.stateDir, snap); err != nil {
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
	budgetWarned := false  // track whether 80% warning was injected
	budgetCritical := false // track whether critical warning was injected

	for round := 0; s.cfg.MaxToolRoundsPerInput < 0 || round < s.cfg.MaxToolRoundsPerInput; round++ {
		select {
		case <-ctx.Done():
			s.emit(EventError, ErrorData{Error: ctx.Err().Error()})
			return "", ctx.Err()
		default:
		}

		// Budget awareness: inject steering when approaching round limit.
		if max := s.cfg.MaxToolRoundsPerInput; max > 0 {
			remaining := max - round
			pctUsed := float64(round) / float64(max)
			if !budgetCritical && remaining <= 3 {
				budgetCritical = true
				msg := fmt.Sprintf("CRITICAL BUDGET WARNING: Only %d rounds remaining out of %d. "+
					"You must call communicate(result) immediately with whatever you have. "+
					"Do not start new work.", remaining, max)
				s.Steer(msg)
			} else if !budgetWarned && pctUsed >= 0.80 {
				budgetWarned = true
				msg := fmt.Sprintf("BUDGET WARNING: You have used %d of %d tool rounds (%d remaining). "+
					"Focus on completing your current approach and call communicate(result) soon.",
					round, max, remaining)
				s.Steer(msg)
			}
		}

		// Rebuild system prompt each iteration so tool side-effects (e.g. new AGENTS.md) are reflected.
		docs, _ := LoadProjectDocs(s.env, s.profile.ProjectDocFiles()...)
		var skillList []SkillMeta
		for _, sm := range s.skills {
			skillList = append(skillList, sm)
		}
		// Build extra tool descriptions (MCP + custom-registered) for layer 3 insertion.
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

		// Add plugin agents section if any are available.
		if agentSection := FormatPluginAgentsPrompt(s.pluginAgents); agentSection != "" {
			sys += agentSection
		}

		if strings.TrimSpace(s.cfg.UserInstructionOverride) != "" {
			sys = sys + "\n\n" + strings.TrimSpace(s.cfg.UserInstructionOverride) + "\n"
		}

		// PreCompact hooks
		if s.hookRunner != nil {
			preCompactResult := s.hookRunner.RunPreCompact(ctx, s.hookInput(HookPreCompact))
			for _, msg := range preCompactResult.SystemMessages {
				s.Steer(msg)
			}
		}

		// Apply context management before each LLM request.
		// Copy history out to avoid holding s.mu during potential LLM calls (Layer 4).
		if s.strategy != nil {
			// Populate compaction metadata so checkpoint/summarize have session context.
			s.contextMgr.Meta = s.buildCompactionMeta()

			s.mu.Lock()
			histCopy := append([]Turn{}, s.history...)
			s.mu.Unlock()

			if err := s.strategy.ManageContext(ctx, &histCopy, len(sys), s.emit); err != nil {
				s.emit(EventWarning, WarningData{Message: "context strategy error: " + err.Error()})
			}

			s.mu.Lock()
			s.history = histCopy
			s.mu.Unlock()
		}

		s.mu.Lock()
		historyTurns := append([]Turn{}, s.history...)
		s.mu.Unlock()

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

		req := llm.Request{
			Model:      s.profile.Model(),
			Provider:   s.profile.ID(),
			Messages:   append([]llm.Message{llm.System(sys)}, history...),
			Tools:      s.allToolDefinitions(),
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

		policy := llm.DefaultRetryPolicy()
		if s.cfg.LLMRetryPolicy != nil {
			policy = *s.cfg.LLMRetryPolicy
		}
		resp, err := llm.Retry(ctx, policy, s.cfg.LLMSleep, nil, func() (llm.Response, error) {
			return s.client.Complete(ctx, req)
		})
		if err != nil {
			s.emit(EventError, ErrorData{Error: err.Error()})
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
		s.emit(EventAssistantTextStart, AssistantTextStartData{
			Model: resp.Model,
		})
		s.appendAssistantTurn(resp)
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
			continue
		}

		calls := resp.ToolCalls()

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
			s.mu.Lock()
			if looksLikeQuestion(txt) {
				s.state = SessionAwaitingInput
			} else {
				s.state = SessionIdle
			}
			s.mu.Unlock()
			s.maybeAutoSave()
			return txt, nil
		}

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
					ImageData:      r.ImageData,
					ImageMediaType: r.ImageMediaType,
				},
			})
		}
		s.appendTurn(TurnToolResults, llm.Message{Role: llm.RoleTool, Content: parts})
		// Persist the completed tool round so resumed sessions always include
		// tool_result turns for any prior assistant tool calls.
		s.maybeAutoSave()

		// Notify the context strategy that a tool round completed.
		if s.strategy != nil {
			s.mu.Lock()
			histCopy := append([]Turn{}, s.history...)
			s.mu.Unlock()
			if err := s.strategy.AfterAction(ctx, histCopy, s.client); err != nil {
				s.emit(EventWarning, WarningData{Message: "strategy AfterAction error: " + err.Error()})
			}
		}

		// Loop detection: track per-call signatures and check for repeating patterns.
		for _, call := range calls {
			toolSigs = append(toolSigs, call.Name+":"+shortHash(call.Arguments))
		}
		if s.cfg.EnableLoopDetection != nil && *s.cfg.EnableLoopDetection {
			if detectLoop(toolSigs, s.cfg.LoopDetectionWindow) {
				warning := fmt.Sprintf("Warning: Loop detected — the same tool call pattern has repeated %d times. Consider changing approach.", s.cfg.LoopDetectionWindow)
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

		// communicate(result) sets the flag; exit the loop with the result message.
		s.mu.Lock()
		delivered := s.resultDelivered
		text := s.resultText
		s.mu.Unlock()
		if delivered {
			// Stop hooks
			if s.hookRunner != nil {
				hi := s.hookInput(HookStop)
				hi.Reason = "communicate_result"
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

// allToolDefinitions returns tool definitions for tools registered in the
// session's tool registry, plus any MCP tools. Only tools present in the
// registry are included, ensuring tool restrictions (e.g. from plugin agents)
// are reflected in what the LLM sees.
func (s *Session) allToolDefinitions() []llm.ToolDefinition {
	registered := s.reg.RegisteredNames()

	// Profile tool definitions use provider-specific names (e.g. "exec_command"
	// for OpenAI). Build a reverse map from provider name → canonical name so
	// we can filter against the registry which uses canonical names.
	nameMap := s.profile.ToolNameMap() // canonical → provider, may be nil
	reverseMap := make(map[string]string, len(nameMap))
	for canonical, provider := range nameMap {
		reverseMap[provider] = canonical
	}

	var defs []llm.ToolDefinition
	for _, td := range s.profile.ToolDefinitions() {
		canonical := td.Name
		if c, ok := reverseMap[td.Name]; ok {
			canonical = c
		}
		if registered[canonical] {
			defs = append(defs, td)
		}
	}
	for _, td := range s.mcpTools {
		if registered[td.Name] {
			defs = append(defs, td)
		}
	}
	return defs
}

// initSessionState performs the shared initialization steps for both
// NewSession and RestoreSession: environment snapshot, system prompt
// resolution, skills discovery, tool registry setup, and MCP connection.
// The Session struct fields (client, profile, env, cfg) must already be set.
func (s *Session) initSessionState() error {
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

	if s.cfg.BasePromptOverride != "" {
		// Subagents use BasePromptOverride to replace the parent's base.md entirely,
		// avoiding inherited delegation instructions the subagent cannot follow.
		s.profile = s.profile.WithBasePrompt(s.cfg.BasePromptOverride)
	} else {
		gitRoot := gitRootOrEmpty(s.env, ei.WorkingDir)
		resolvedPrompt, err := ResolveSystemPrompt(
			s.profile.ID(), s.profile.Model(),
			s.cfg.SystemPromptFile,
			ProjectPromptsDir(gitRoot),
			GlobalPromptsDir(),
			s.cfg.SystemPromptAppend,
		)
		if err != nil {
			return fmt.Errorf("resolve system prompt: %w", err)
		}
		s.profile = s.profile.WithBasePrompt(resolvedPrompt)
	}

	s.skills = DiscoverSkills(s.env, s.cfg.SkillsDirs...)

	// Load built-in agents (explorer, etc.) as a base layer.
	builtins, err := builtinAgents()
	if err != nil {
		return fmt.Errorf("loading built-in agents: %w", err)
	}
	s.pluginAgents = builtins

	// Initialize plugins (skills, agents, hooks). Plugin agents override builtins.
	if err := s.initPlugins(); err != nil {
		return fmt.Errorf("plugin initialization: %w", err)
	}

	s.contextMgr = NewContextManager(s.profile, s.client)

	reg := s.profile.NewToolRegistry()
	if err := registerCoreTools(reg, s); err != nil {
		return err
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
		return fmt.Errorf("MCP initialization: %w", err)
	}
	return nil
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
			result, err := s.spawnAgent(ctx, task, model, workingDir, maxTurns, agentType)
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
			return s.waitAgent(ctx, agentID, 0) // 0 = wait indefinitely
		},
	})
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defSendInput()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.sendInput(ctx, fmt.Sprint(args["agent_id"]), fmt.Sprint(args["message"]))
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
					items = append(items, TaskInput{
						Description: fmt.Sprint(m["description"]),
						Prompt:      fmt.Sprint(m["prompt"]),
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
					updates = append(updates, u)
				}
				return nil, store.Update(updates)
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
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defWebSearch()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			query := fmt.Sprint(args["query"])
			return s.webSearch(ctx, query)
		},
	})

	// Communicate (structured I/O).
	_ = reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: defCommunicate()},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			action := fmt.Sprint(args["action"])
			message := ""
			if v, ok := args["message"]; ok {
				message = fmt.Sprint(v)
			}

			switch action {
			case "status":
				// Some workflows accidentally include an output object with status updates.
				// Treat it as ignorable noise and proceed as long as we have a message.
				if strings.TrimSpace(message) == "" {
					if output, ok := args["output"]; ok && output != nil {
						if outMsg := communicateOutputMessage(output); strings.TrimSpace(outMsg) != "" {
							message = outMsg
						}
					}
					if strings.TrimSpace(message) == "" {
						return nil, fmt.Errorf("communicate(status) requires a non-empty message")
					}
				}
			case "result":
				if v, ok := args["output"]; (!ok || v == nil) && strings.TrimSpace(message) == "" {
					return nil, fmt.Errorf("communicate(result) requires either message or output")
				}
			default:
				return nil, fmt.Errorf("unknown communicate action %q: use status or result", action)
			}

			resultText := message
			if action == "result" {
				if output, ok := args["output"]; ok && output != nil {
					resultText = canonicalNodeOutputText(output)
					if message == "" {
						if outMsg := communicateOutputMessage(output); outMsg != "" {
							message = outMsg
						}
					}
				}
			}

			s.emit(EventCommunicate, CommunicateData{
				Action:  action,
				Message: message,
			})

			// Drain steering queue into the inbox.
			inbox := s.drainSteering()

			if action == "result" {
				s.mu.Lock()
				s.resultDelivered = true
				s.resultText = resultText
				s.mu.Unlock()
			}

			resp := map[string]any{
				"delivered": true,
				"inbox":     inbox,
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	})

	// use_skill (progressive disclosure of skill instructions).
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
			return body, nil
		},
	})

	return nil
}

type nodeOutput struct {
	Decision  string         `json:"decision"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data"`
	Artifacts []string       `json:"artifacts"`
}

func canonicalNodeOutputText(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return "{}"
	}

	out := nodeOutput{
		Decision:  fmt.Sprint(m["decision"]),
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

func communicateOutputMessage(raw any) string {
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
