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

	"primeradiant.com/serf/internal/llm"
)

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

	// SystemPromptFile overrides the embedded system prompt with the contents of this file.
	// Highest priority in the prompt resolution chain (CLI --system-prompt flag).
	SystemPromptFile string `json:"system_prompt_file,omitempty"`

	// SystemPromptAppend are file paths whose contents are appended to the system prompt.
	// Always applied, even when SystemPromptFile is set (CLI --system-prompt-append flag).
	SystemPromptAppend []string `json:"system_prompt_append,omitempty"`

	EnableLoopDetection *bool `json:"enable_loop_detection,omitempty"`
	LoopDetectionWindow int   `json:"loop_detection_window,omitempty"`

	// LLMRetryPolicy controls retries for retryable Unified LLM errors (429, 5xx, etc).
	// Nil means use llm.DefaultRetryPolicy().
	LLMRetryPolicy *llm.RetryPolicy `json:"-"`
	LLMSleep       llm.SleepFunc    `json:"-"`

	// StateDir, when non-empty, enables incremental session persistence.
	// Snapshots are written to <StateDir>/sessions/ and tasks to <StateDir>/tasks/.
	StateDir string `json:"-"`
}

func (c *SessionConfig) applyDefaults() {
	if c.MaxToolRoundsPerInput <= 0 {
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

	events chan SessionEvent
	envInfo EnvironmentInfo

	mu    sync.Mutex
	state SessionState
	turns int
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

	// skills discovered at session startup
	skills map[string]SkillMeta

	// MCP server connections
	mcpMgr   *MCPManager
	mcpTools []llm.ToolDefinition

	// read-before-write guardrail
	readFiles map[string]bool

	// task list (lazy-init)
	taskStore     *TaskStore
	taskStoreOnce sync.Once
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

	s := &Session{
		id:        ulid.Make().String(),
		cfg:       cfg,
		client:    client,
		profile:   profile,
		env:       env,
		stateDir:  cfg.StateDir,
		state:     SessionIdle,
		events:    make(chan SessionEvent, 256),
		history:   []Turn{},
		subagents: map[string]*subagent{},
		readFiles: map[string]bool{},
	}

	// Snapshot environment context once per session (spec).
	ei := envInfoFromEnv(env)
	ei.KnowledgeCutoff = profile.KnowledgeCutoff()
	if inRepo, branch, mod, untracked, commits := snapshotGit(env, ei.WorkingDir); inRepo {
		ei.IsGitRepo = true
		ei.GitBranch = branch
		ei.GitModifiedFiles = mod
		ei.GitUntrackedFiles = untracked
		ei.GitRecentCommitTitles = commits
		ei.GitOriginURL = gitOriginURL(env, ei.WorkingDir)
	}
	s.envInfo = ei

	// Resolve system prompt: embedded base+provider, global/project additions, CLI overrides.
	gitRoot := gitRootOrEmpty(env, ei.WorkingDir)
	resolvedPrompt, err := ResolveSystemPrompt(
		profile.ID(), profile.Model(),
		cfg.SystemPromptFile,
		ProjectPromptsDir(gitRoot),
		GlobalPromptsDir(),
		cfg.SystemPromptAppend,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve system prompt: %w", err)
	}
	s.profile = profile.WithBasePrompt(resolvedPrompt)

	s.skills = DiscoverSkills(env, cfg.SkillsDirs...)
	s.contextMgr = NewContextManager(s.profile, client)

	reg := NewToolRegistry()
	if err := registerCoreTools(reg, s); err != nil {
		return nil, err
	}
	// Allow SessionConfig to override default tool output limits (spec).
	if len(cfg.ToolOutputLimits) > 0 {
		reg.mu.Lock()
		for name, lim := range cfg.ToolOutputLimits {
			t, ok := reg.tools[name]
			if !ok {
				continue
			}
			// Merge: only positive overrides take effect.
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

	// MCP server discovery and connection.
	if err := s.initMCP(); err != nil {
		return nil, fmt.Errorf("MCP initialization: %w", err)
	}

	s.emit(EventSessionStart, map[string]any{
		"profile": profile.ID(),
		"model":   profile.Model(),
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

	s := &Session{
		id:        snap.ID,
		cfg:       cfg,
		client:    client,
		profile:   profile,
		env:       env,
		stateDir:  cfg.StateDir,
		state:     SessionIdle,
		events:    make(chan SessionEvent, 256),
		history:   append([]Turn{}, snap.History...),
		turns:     snap.TurnCount,
		subagents: map[string]*subagent{},
		readFiles: map[string]bool{},
	}

	// Refresh environment context for the current state.
	ei := envInfoFromEnv(env)
	ei.KnowledgeCutoff = profile.KnowledgeCutoff()
	if inRepo, branch, mod, untracked, commits := snapshotGit(env, ei.WorkingDir); inRepo {
		ei.IsGitRepo = true
		ei.GitBranch = branch
		ei.GitModifiedFiles = mod
		ei.GitUntrackedFiles = untracked
		ei.GitRecentCommitTitles = commits
		ei.GitOriginURL = gitOriginURL(env, ei.WorkingDir)
	}
	s.envInfo = ei

	// Resolve system prompt (same layered resolution as NewSession).
	gitRoot := gitRootOrEmpty(env, ei.WorkingDir)
	resolvedPrompt, err := ResolveSystemPrompt(
		profile.ID(), profile.Model(),
		cfg.SystemPromptFile,
		ProjectPromptsDir(gitRoot),
		GlobalPromptsDir(),
		cfg.SystemPromptAppend,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve system prompt: %w", err)
	}
	s.profile = profile.WithBasePrompt(resolvedPrompt)

	s.skills = DiscoverSkills(env, cfg.SkillsDirs...)
	s.contextMgr = NewContextManager(s.profile, client)

	reg := NewToolRegistry()
	if err := registerCoreTools(reg, s); err != nil {
		return nil, err
	}
	if len(cfg.ToolOutputLimits) > 0 {
		reg.mu.Lock()
		for name, lim := range cfg.ToolOutputLimits {
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

	// MCP server discovery and connection.
	if err := s.initMCP(); err != nil {
		return nil, fmt.Errorf("MCP initialization: %w", err)
	}

	s.emit(EventSessionStart, map[string]any{
		"profile":  profile.ID(),
		"model":    profile.Model(),
		"restored": true,
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
		ID:        s.id,
		ProfileID: s.profile.ID(),
		Model:     s.profile.Model(),
		Config:    s.cfg,
		EnvInfo:   s.envInfo,
		History:   append([]Turn{}, s.history...),
		CreatedAt: now, // overwritten by existing file if already saved
		UpdatedAt: now,
		TurnCount: s.turns,
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

func (s *Session) Close() {
	s.mu.Lock()
	if s.state == SessionClosed {
		s.mu.Unlock()
		return
	}
	s.state = SessionClosed

	// Collect subagents and clear the map under the lock, then close outside
	// the lock to avoid deadlock (sub.sess.Close() also acquires its own mu).
	subs := make([]*subagent, 0, len(s.subagents))
	for id, sub := range s.subagents {
		subs = append(subs, sub)
		delete(s.subagents, id)
	}
	turns := s.turns
	s.mu.Unlock()

	for _, sub := range subs {
		sub.sess.Close()
	}

	if s.mcpMgr != nil {
		s.mcpMgr.Close()
	}

	s.env.Cleanup()

	s.emit(EventSessionEnd, map[string]any{
		"reason": "session_closed",
		"state":  string(SessionClosed),
		"turns":  turns,
	})
	close(s.events)
}

func (s *Session) ProcessInput(ctx context.Context, input string) (string, error) {
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
			s.emit(EventSessionEnd, map[string]any{
				"reason": "input_complete",
				"state":  string(s.State()),
			})
			return strings.Join(outputs, "\n"), nil
		}
		next = fu
	}
}

func (s *Session) execTool(ctx context.Context, call llm.ToolCallData) ToolExecResult {
	argsJSON, _ := json.Marshal(call.Arguments)
	startData := map[string]any{
		"tool_name":      call.Name,
		"call_id":        call.ID,
		"arguments_json": string(argsJSON),
	}
	// Promote description to top-level event field for observability (shell tool).
	var args map[string]any
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	if desc, ok := args["description"].(string); ok && desc != "" {
		startData["description"] = desc
	}
	s.emit(EventToolCallStart, startData)

	// Session-level tools (subagents) are registered in the registry with closures.
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
		s.emit(EventToolCallOutputDelta, map[string]any{
			"tool_name": res.ToolName,
			"call_id":   res.CallID,
			"delta":     full[i:j],
		})
	}

	s.emit(EventToolCallEnd, map[string]any{
		"tool_name":   res.ToolName,
		"call_id":     res.CallID,
		"is_error":    res.IsError,
		"full_output": res.FullOutput,
	})
	return res
}

func (s *Session) appendTurn(kind TurnKind, m llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, NewTurn(kind, m))
}

// appendAssistantTurn appends an assistant turn that carries the full response
// metadata (usage stats and response ID) alongside the message content.
func (s *Session) appendAssistantTurn(resp llm.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, Turn{
		Kind:       TurnAssistant,
		Message:    resp.Message,
		Timestamp:  time.Now().UTC(),
		Usage:      resp.Usage,
		ResponseID: resp.ID,
	})
}

// maybeAutoSave persists the session state if StateDir is configured.
// Errors are emitted as warnings but do not interrupt the session.
func (s *Session) maybeAutoSave() {
	if s.stateDir == "" {
		return
	}
	snap := s.Snapshot()
	if err := SaveSession(s.stateDir, snap); err != nil {
		s.emit(EventWarning, map[string]any{
			"message": fmt.Sprintf("auto-save failed: %v", err),
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
	s.emit(EventWarning, map[string]any{
		"message":             msg,
		"approx_tokens":       int(math.Round(approxTokens)),
		"context_window_size": cw,
		"percent":             pct,
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

func (s *Session) emit(kind EventKind, data map[string]any) {
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

func (s *Session) processOneInput(ctx context.Context, input string) (string, error) {
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
		s.emit(EventError, map[string]any{"error": ctx.Err().Error()})
		return "", ctx.Err()
	default:
	}

	s.emit(EventUserInput, map[string]any{"text": input})
	s.appendTurn(TurnUserInput, llm.User(input))

	// Count conversation turns (user input -> model response pairs), not LLM round-trips.
	s.mu.Lock()
	s.turns++
	turns := s.turns
	s.mu.Unlock()

	if s.cfg.MaxTurns > 0 && turns > s.cfg.MaxTurns {
		s.emit(EventTurnLimit, map[string]any{"max_turns": s.cfg.MaxTurns})
		s.mu.Lock()
		s.state = SessionIdle
		s.mu.Unlock()
		return "", fmt.Errorf("turn limit reached")
	}

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	for _, msg := range s.drainSteering() {
		s.appendTurn(TurnSteering, llm.User(msg))
		s.emit(EventSteeringInjected, map[string]any{"text": msg})
	}

		docs, _ := LoadProjectDocs(s.env, s.profile.ProjectDocFiles()...)
		var skillList []SkillMeta
		for _, sm := range s.skills {
			skillList = append(skillList, sm)
		}
		sys := s.profile.BuildSystemPrompt(s.envInfo, docs, skillList)

		// Append MCP tool descriptions to the system prompt so the model knows about them.
		if len(s.mcpTools) > 0 {
			sys += "\nMCP Tools (from external servers):\n"
			for _, td := range s.mcpTools {
				desc := strings.TrimSpace(td.Description)
				if desc == "" {
					desc = "(no description)"
				}
				sys += fmt.Sprintf("- %s: %s\n", td.Name, desc)
			}
		}

		if strings.TrimSpace(s.cfg.UserInstructionOverride) != "" {
			sys = sys + "\n\n" + strings.TrimSpace(s.cfg.UserInstructionOverride) + "\n"
		}

	var toolSigs []string
	ctxWarned := false

	for round := 0; round < s.cfg.MaxToolRoundsPerInput; round++ {
		select {
		case <-ctx.Done():
			s.emit(EventError, map[string]any{"error": ctx.Err().Error()})
			return "", ctx.Err()
		default:
		}
		// Apply context management before each LLM request.
		// Copy history out to avoid holding s.mu during potential LLM calls (Layer 4).
		if s.contextMgr != nil {
			s.mu.Lock()
			histCopy := append([]Turn{}, s.history...)
			s.mu.Unlock()

			s.contextMgr.MaybeCompact(ctx, &histCopy, len(sys), s.emit)

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
				history = append(history, t.Message)
			}

		req := llm.Request{
			Model:      s.profile.Model(),
			Provider:   s.profile.ID(),
			Messages:   append([]llm.Message{llm.System(sys)}, history...),
			Tools:      s.allToolDefinitions(),
			ToolChoice: &llm.ToolChoice{Mode: "auto"},
			WebSearch:  true,
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
				s.emit(EventError, map[string]any{"error": err.Error()})
				// Spec: context overflow should emit a warning (no automatic compaction).
				var cle *llm.ContextLengthError
				if errors.As(err, &cle) {
					s.emit(EventWarning, map[string]any{"message": "Context length exceeded"})
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
			if resp.Usage.InputTokens > 0 {
				s.mu.Lock()
				hLen := len(s.history)
				s.mu.Unlock()
				s.contextMgr.RecordInputTokens(resp.Usage.InputTokens, hLen)
			}
		}

		// Context window awareness: emit a warning when we exceed ~80% of the profile's context window.
		if !ctxWarned {
			if s.maybeWarnContextUsage(req.Messages) {
				ctxWarned = true
			}
		}

			txt := resp.Text()
			s.emit(EventAssistantTextStart, map[string]any{})
			s.appendAssistantTurn(resp)
			if strings.TrimSpace(txt) != "" {
				s.emit(EventAssistantTextDelta, map[string]any{"delta": txt})
			}
			endData := map[string]any{
				"text":          txt,
				"usage":         resp.Usage,
				"finish_reason": resp.Finish.Reason,
				"model":         resp.Model,
			}
			if reasoning := resp.ReasoningText(); reasoning != "" {
				endData["reasoning"] = reasoning
			}
			s.emit(EventAssistantTextEnd, endData)
			s.maybeAutoSave()

		// pause_turn: model needs another turn (e.g. server-side web search still running).
		if resp.Finish.Reason == llm.FinishReasonPauseTurn {
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
			trimmed := strings.TrimSpace(txt)
			if strings.HasSuffix(trimmed, "?") {
				s.state = SessionAwaitingInput
			} else {
				s.state = SessionIdle
			}
			s.mu.Unlock()
			return txt, nil
		}

		// Execute tool calls (possibly in parallel) and send results back.
		results := make([]ToolExecResult, len(calls))
		if s.profile.SupportsParallelToolCalls() && len(calls) > 1 {
			var wg sync.WaitGroup
			wg.Add(len(calls))
			for i := range calls {
				i := i
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
						ToolCallID: r.CallID,
						Name:       r.ToolName,
						Content:    r.Output,
						IsError:    r.IsError,
					},
				})
			}
			s.appendTurn(TurnToolResults, llm.Message{Role: llm.RoleTool, Content: parts})

		// Loop detection: track per-call signatures and check for repeating patterns.
		for _, call := range calls {
			toolSigs = append(toolSigs, call.Name+":"+shortHash(call.Arguments))
		}
		if s.cfg.EnableLoopDetection != nil && *s.cfg.EnableLoopDetection {
			if detectLoop(toolSigs, s.cfg.LoopDetectionWindow) {
				warning := fmt.Sprintf("Loop detected: repeating pattern in the last %d tool calls. Try a different approach.", s.cfg.LoopDetectionWindow)
				s.emit(EventLoopDetection, map[string]any{"message": warning})
				s.appendTurn(TurnSteering, llm.User(warning))
				s.emit(EventSteeringInjected, map[string]any{"text": warning})
			}
		}

			// Inject any queued steering messages before the next model call.
			for _, msg := range s.drainSteering() {
				s.appendTurn(TurnSteering, llm.User(msg))
				s.emit(EventSteeringInjected, map[string]any{"text": msg})
			}

		// communicate(result) sets the flag; exit the loop with the result message.
		s.mu.Lock()
		delivered := s.resultDelivered
		text := s.resultText
		s.mu.Unlock()
		if delivered {
			s.mu.Lock()
			s.state = SessionIdle
			s.mu.Unlock()
			return text, nil
		}
	}

	s.emit(EventTurnLimit, map[string]any{"max_tool_rounds_per_input": s.cfg.MaxToolRoundsPerInput})
	s.mu.Lock()
	s.state = SessionIdle
	s.mu.Unlock()
	return "", fmt.Errorf("max tool rounds reached")
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

// allToolDefinitions returns profile tool definitions plus any MCP tools.
func (s *Session) allToolDefinitions() []llm.ToolDefinition {
	defs := s.profile.ToolDefinitions()
	if len(s.mcpTools) > 0 {
		defs = append(append([]llm.ToolDefinition{}, defs...), s.mcpTools...)
	}
	return defs
}

// initMCP discovers and connects to MCP servers if configured.
// Uses a 30-second timeout since NewSession doesn't take a context.
func (s *Session) initMCP() error {
	configs, err := DiscoverMCPConfigs(s.env, s.cfg.MCPConfigFiles, s.cfg.MCPInline)
	if err != nil {
		return err
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
		Definition: defReadFile(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			var offset *int
			var limit *int
			if v, ok := args["offset"]; ok {
				if n, ok := v.(float64); ok {
					ni := int(n)
					offset = &ni
				}
			}
			if v, ok := args["limit"]; ok {
				if n, ok := v.(float64); ok {
					ni := int(n)
					limit = &ni
				}
			}
			result, err := env.ReadFile(path, offset, limit)
			if err == nil {
				s.trackReadFile(path)
			}
			return result, err
		},
	}); err != nil {
		return err
	}

	// read_many_files (Gemini-aligned; safe to register globally)
	_ = reg.Register(RegisteredTool{
		Definition: defReadManyFiles(),
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
			var offset *int
			var limit *int
			if v, ok := args["offset"]; ok {
				if n, ok := v.(float64); ok {
					ni := int(n)
					offset = &ni
				}
			}
			if v, ok := args["limit"]; ok {
				if n, ok := v.(float64); ok {
					ni := int(n)
					limit = &ni
				}
			}

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
		Definition: defWriteFile(),
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
		Definition: defEditFile(),
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
			Definition: defShell(),
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
		Definition: defListDir(),
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
		Definition: defGrep(),
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
			return env.Grep(pat, path, glob, ci, maxRes)
		},
	}); err != nil {
		return err
	}

		// glob
		if err := reg.Register(RegisteredTool{
			Definition: defGlob(),
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
		Definition: defApplyPatch(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			patch := fmt.Sprint(args["patch"])
			return ApplyPatch(env.WorkingDirectory(), patch)
		},
	})

	// Subagent tools (best-effort; synchronous completion for v1).
	_ = reg.Register(RegisteredTool{
		Definition: defSpawnAgent(),
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
			return s.spawnAgent(ctx, task, model, workingDir, maxTurns)
		},
	})
	_ = reg.Register(RegisteredTool{
		Definition: defSendInput(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.sendInput(ctx, fmt.Sprint(args["agent_id"]), fmt.Sprint(args["message"]))
		},
	})
	_ = reg.Register(RegisteredTool{
		Definition: defWait(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			timeout := 0
			if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
				timeout = int(v)
			}
			return s.waitAgent(ctx, fmt.Sprint(args["agent_id"]), timeout)
		},
	})
	_ = reg.Register(RegisteredTool{
		Definition: defCloseAgent(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			return s.closeAgent(fmt.Sprint(args["agent_id"]))
		},
	})

	// Task management.
	_ = reg.Register(RegisteredTool{
		Definition: defTaskList(),
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
					updates = append(updates, TaskUpdate{
						ID:     id,
						Status: TaskStatus(fmt.Sprint(m["status"])),
					})
				}
				return nil, store.Update(updates)
			default:
				return nil, fmt.Errorf("unknown task_list action %q: use view, append, or update", action)
			}
		},
	})

	// Web fetch.
	_ = reg.Register(RegisteredTool{
		Definition: defWebFetch(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			rawURL := fmt.Sprint(args["url"])
			question := fmt.Sprint(args["question"])
			return s.webFetch(ctx, rawURL, question)
		},
	})

	// Web search (Gemini only — see tool_web_search.go for why).
	_ = reg.Register(RegisteredTool{
		Definition: defWebSearch(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			query := fmt.Sprint(args["query"])
			return s.webSearch(ctx, query)
		},
	})

	// Communicate (structured I/O).
	_ = reg.Register(RegisteredTool{
		Definition: defCommunicate(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			action := fmt.Sprint(args["action"])
			message := fmt.Sprint(args["message"])

			s.emit(EventCommunicate, map[string]any{
				"action":  action,
				"message": message,
			})

			// Drain steering queue into the inbox.
			inbox := s.drainSteering()

			if action == "result" {
				s.mu.Lock()
				s.resultDelivered = true
				s.resultText = message
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
		Definition: defUseSkill(),
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			skillName := fmt.Sprint(args["skill_name"])
			meta, ok := s.skills[skillName]
			if !ok {
				return nil, fmt.Errorf("skill %q not found", skillName)
			}
			s.emit(EventSkillActivated, map[string]any{"name": skillName})
			body, err := LoadSkillBody(meta)
			if err != nil {
				return nil, fmt.Errorf("loading skill %q: %w", skillName, err)
			}
			return body, nil
		},
	})

	return nil
}

// trackReadFile records that a file has been read in this session.
func (s *Session) trackReadFile(path string) {
	s.readFiles[s.resolveFilePath(path)] = true
}

// readBeforeWriteWarning returns a warning string if the file exists but hasn't
// been read in this session. Returns "" for new files or previously-read files.
func (s *Session) readBeforeWriteWarning(path string) string {
	abs := s.resolveFilePath(path)
	if s.readFiles[abs] {
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
